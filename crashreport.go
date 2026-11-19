// Copyright Codexray
// SPDX-License-Identifier: AGPL-3.0

package main

import (
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/codifinary/codexray-node-agent/containers"
	"github.com/prometheus/client_golang/prometheus"
	"k8s.io/klog/v2"
)

const (
	crashLogName      = "crash.log"
	runningMarkerName = "agent.running"

	// startupReportDelay lets the agent start tailing its own container log, which it reads from the end.
	startupReportDelay  = 60 * time.Second
	crashReportMaxBytes = 8 << 10
)

type runOutcome int

const (
	runClean runOutcome = iota
	runCrashed
	runKilled
)

// lastRun is how the previous agent process ended.
type lastRun struct {
	outcome runOutcome
	crash   string
}

// readLastRun reads the crash log and the running marker the previous process left in dir.
func readLastRun(dir string) (lastRun, error) {
	crash, err := readFileHead(filepath.Join(dir, crashLogName), crashReportMaxBytes)
	if err != nil && !os.IsNotExist(err) {
		return lastRun{}, err
	}
	if strings.TrimSpace(crash) != "" {
		return lastRun{outcome: runCrashed, crash: crash}, nil
	}
	_, err = os.Stat(filepath.Join(dir, runningMarkerName))
	switch {
	case err == nil:
		return lastRun{outcome: runKilled}, nil
	case os.IsNotExist(err):
		return lastRun{outcome: runClean}, nil
	}
	return lastRun{}, err
}

func readFileHead(name string, maxBytes int64) (string, error) {
	f, err := os.Open(name)
	if err != nil {
		return "", err
	}
	defer f.Close()
	head, err := io.ReadAll(io.LimitReader(f, maxBytes))
	return string(head), err
}

// startCrashCapture reads how the previous run ended, then sends a fatal crash of this run to dir.
// The returned func marks a clean shutdown.
func startCrashCapture(dir string) (lastRun, func(), error) {
	noop := func() {}
	if err := os.MkdirAll(dir, 0750); err != nil {
		return lastRun{}, noop, err
	}
	last, err := readLastRun(dir)
	if err != nil {
		return lastRun{}, noop, err
	}
	crashLog := filepath.Join(dir, crashLogName)
	if last.outcome == runCrashed {
		if err := os.Rename(crashLog, crashLog+".prev"); err != nil {
			return last, noop, err
		}
	}
	f, err := os.OpenFile(crashLog, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0640)
	if err != nil {
		return last, noop, err
	}
	defer f.Close() // the runtime keeps its own copy of the descriptor
	if err := debug.SetCrashOutput(f, debug.CrashOptions{}); err != nil {
		return last, noop, err
	}
	marker := filepath.Join(dir, runningMarkerName)
	if err := os.WriteFile(marker, []byte(strconv.Itoa(os.Getpid())), 0640); err != nil {
		return last, noop, err
	}
	return last, func() {
		if err := os.Remove(marker); err != nil && !os.IsNotExist(err) {
			klog.Warningln("failed to mark a clean shutdown:", err)
		}
	}, nil
}

func (l lastRun) report() {
	switch l.outcome {
	case runCrashed:
		klog.Errorf("previous agent process crashed:\n%s", l.crash)
	case runKilled:
		klog.Errorln("previous agent process ended without a clean shutdown or a crash report: killed (SIGKILL, OOM kill), a fatal startup error, or a reboot")
	}
}

func previousRunOutcome(last lastRun) prometheus.Collector {
	g := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "node_agent_previous_run_outcome",
		Help: "How the previous agent process ended: 0 clean shutdown, 1 crashed, 2 ended without a crash report (killed).",
	})
	g.Set(float64(last.outcome))
	return g
}

// reportStartupDiagnostics logs what went wrong before the agent's own log was being shipped.
func reportStartupDiagnostics(last lastRun) {
	time.Sleep(startupReportDelay)
	last.report()
	if err := containers.JournaldInitError(); err != nil {
		klog.Warningln("logs of systemd services are not collected:", err)
	}
}
