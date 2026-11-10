// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package profiling

import (
	"bytes"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"

	"github.com/codifinary/codexray-node-agent/common"
	"github.com/codifinary/codexray-node-agent/containers"
	"github.com/codifinary/codexray-node-agent/diag"
	"github.com/codifinary/codexray-node-agent/flags"
	"github.com/codifinary/codexray-node-agent/jvm"
	"github.com/codifinary/codexray-node-agent/proc"
	"github.com/go-kit/log"
	ebpfspy "github.com/grafana/pyroscope/ebpf"
	"github.com/grafana/pyroscope/ebpf/cpp/demangle"
	"github.com/grafana/pyroscope/ebpf/metrics"
	"github.com/grafana/pyroscope/ebpf/pprof"
	"github.com/grafana/pyroscope/ebpf/sd"
	"github.com/grafana/pyroscope/ebpf/symtab"
	"github.com/grafana/pyroscope/ebpf/symtab/elf"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/prometheus/model/labels"
	"k8s.io/klog/v2"
)

const (
	CollectInterval      = time.Minute
	SampleRate           = 100
	UploadTimeout        = 10 * time.Second
	ProcessInfoQueueSize = 4096
)

var (
	constLabels labels.Labels
	httpClient  = http.Client{
		Timeout: UploadTimeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: *flags.InsecureSkipVerify},
		},
	}
	endpointUrl  *url.URL
	session      ebpfspy.Session
	targetFinder = &TargetFinder{
		processes: map[uint32]*processInfo{},
	}
)

func Init(hostId, hostName string) chan<- containers.ProcessInfo {
	endpointUrl = *flags.ProfilesEndpoint
	if endpointUrl == nil {
		klog.Infoln("no profiles endpoint configured")
		return nil
	}
	klog.Infoln("profiles endpoint:", endpointUrl.String())

	constLabels = labels.Labels{
		{Name: "host.name", Value: hostName},
		{Name: "host.id", Value: hostId},
	}

	// Symbol tables are the largest memory owner in the profiler: every distinct binary and
	// shared library seen in a stack has its tables loaded and cached for KeepRounds
	// collection rounds. These limits are configurable so a node with many distinct
	// binaries can trade symbolization hit rate for memory. Defaults match the previous
	// hard-coded values.
	cacheOpts := symtab.GCacheOptions{
		Size:       *flags.ProfilingSymbolCacheSize,
		KeepRounds: *flags.ProfilingSymbolCacheKeepRounds,
	}
	elf.MaxMiniDebugInfoSize = int64(*flags.ProfilingMaxMiniDebugInfoSize)
	elf.MaxStringCacheEntries = *flags.ProfilingSymbolNameCacheEntries
	symtab.MaxPerfMapSize = int64(*flags.ProfilingMaxPerfMapSize)
	klog.Infof("profiling symbol cache: size=%d keep_rounds=%d max_minidebuginfo=%d name_cache_entries=%d",
		cacheOpts.Size, cacheOpts.KeepRounds, elf.MaxMiniDebugInfoSize, elf.MaxStringCacheEntries)

	// pyroscope's own metrics carry a service_name label (per-workload cardinality), so they
	// stay in a private registry; bounded equivalents are exported via the diag package.
	reg := prometheus.NewRegistry()
	so := ebpfspy.SessionOptions{
		CollectUser:               true,
		CollectKernel:             false,
		UnknownSymbolModuleOffset: true,
		UnknownSymbolAddress:      false,
		PythonEnabled:             true,
		CacheOptions: symtab.CacheOptions{
			PidCacheOptions:      cacheOpts,
			BuildIDCacheOptions:  cacheOpts,
			SameFileCacheOptions: cacheOpts,
		},
		SymbolOptions: symtab.SymbolOptions{
			GoTableFallback:    true,
			PythonFullFilePath: true,
			DemangleOptions:    demangle.DemangleFull,
		},
		Metrics: &metrics.Metrics{
			Symtab: metrics.NewSymtabMetrics(reg),
			Python: metrics.NewPythonMetrics(reg),
		},
		SampleRate: SampleRate,
	}
	var err error
	session, err = ebpfspy.NewSession(log.NewNopLogger(), targetFinder, so)
	if err != nil {
		klog.Errorln(err)
		diag.ProfilingErrors.WithLabelValues("session_init").Inc()
		session = nil
		return nil
	}
	err = session.Start()
	if err != nil {
		klog.Errorln(err)
		diag.ProfilingErrors.WithLabelValues("session_start").Inc()
		session = nil
		return nil
	}
	diag.ProfilingSessionActive.Set(1)
	go collect()

	// Buffered: the registry hands processes over without blocking its event loop
	// (it drops and counts when the buffer is full, see containers.Registry).
	processInfoCh := make(chan containers.ProcessInfo, ProcessInfoQueueSize)
	targetFinder.start(processInfoCh)
	return processInfoCh
}

func Start() {
	if session == nil {
		return
	}
	targetFinder.setNow(time.Now().UnixNano())
	session.UpdateTargets(sd.TargetsOptions{})
}

func Stop() {
	if session != nil {
		session.Stop()
		diag.ProfilingSessionActive.Set(0)
	}
}

func collect() {
	ticker := time.NewTicker(CollectInterval)
	defer ticker.Stop()
	for t := range ticker.C {
		session.UpdateTargets(sd.TargetsOptions{})
		bs := pprof.NewProfileBuilders(pprof.BuildersOptions{SampleRate: SampleRate, PerPIDProfile: false})
		if err := pprof.Collect(bs, session); err != nil {
			klog.Errorln(err)
			diag.ProfilingErrors.WithLabelValues("collect").Inc()
		}
		collectDuration := time.Since(t)
		diag.ProfilingCollectDuration.Observe(collectDuration.Seconds())
		klog.Infof("collected %d profiles in %s", len(bs.Builders), collectDuration.Truncate(time.Millisecond))
		t = time.Now()
		var uploaded int
		for _, b := range bs.Builders {
			if err := upload(b); err != nil {
				klog.Errorln(err)
				diag.ProfilingErrors.WithLabelValues("upload").Inc()
				diag.ExportFailures.WithLabelValues("profiles").Inc()
				// The collector is likely unavailable; skip the rest of this round rather than
				// retrying (profiles are not spooled), but account for what was discarded.
				diag.ProfilingProfiles.WithLabelValues("failed").Inc()
				diag.ProfilingProfiles.WithLabelValues("skipped").Add(float64(len(bs.Builders) - uploaded - 1))
				break
			}
			uploaded++
			diag.ProfilingProfiles.WithLabelValues("uploaded").Inc()
		}
		klog.Infof("uploaded %d profiles in %s", uploaded, time.Since(t).Truncate(time.Millisecond))
	}
}

func upload(b *pprof.ProfileBuilder) error {
	u := *endpointUrl
	q := u.Query()
	for _, l := range b.Labels {
		switch l.Name {
		case "service_name":
			l.Name = "service.name"
		case "__container_id__":
			l.Name = "container.id"
		default:
			continue
		}
		q.Set(l.Name, l.Value)
	}
	for _, l := range constLabels {
		q.Set(l.Name, l.Value)
	}
	u.RawQuery = q.Encode()

	b.Profile.SampleType[0].Type = "ebpf:cpu:nanoseconds"
	b.Profile.DurationNanos = CollectInterval.Nanoseconds()
	body := bytes.NewBuffer(nil)
	_, err := b.Write(body)
	if err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodPost, u.String(), body)
	if err != nil {
		return err
	}
	for k, v := range common.AuthHeaders() {
		req.Header.Set(k, v)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("failed to upload %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

type TargetFinder struct {
	processes map[uint32]*processInfo
	lock      sync.Mutex
	now       int64
}

func (tf *TargetFinder) setNow(now int64) {
	tf.lock.Lock()
	tf.now = now
	tf.lock.Unlock()
}

func (tf *TargetFinder) start(processInfoCh <-chan containers.ProcessInfo) {
	go func() {
		for pi := range processInfoCh {
			tf.add(pi)
		}
	}()
}

func (tf *TargetFinder) add(pi containers.ProcessInfo) {
	cid := string(pi.ContainerId)
	info := &processInfo{
		startedAt: pi.StartedAt.UnixNano(),
		target: sd.NewTargetForTesting(cid, 0, sd.DiscoveryTarget{
			"service_name": common.ContainerIdToOtelServiceName(cid),
		}),
		flags: pi.Flags,
	}
	tf.lock.Lock()
	tf.processes[pi.Pid] = info
	n := len(tf.processes)
	tf.lock.Unlock()
	diag.ProfilingTargets.Set(float64(n))
}

// FindTarget is called by the profiling session for PIDs seen in samples. Slow work (reading
// the cmdline, triggering a JVM perf-map dump through the attach API) runs without holding
// tf.lock, so it never stalls the registry's process handoff or other lookups.
func (tf *TargetFinder) FindTarget(pid uint32) *sd.Target {
	tf.lock.Lock()
	pi := tf.processes[pid]
	if pi == nil || tf.now-pi.startedAt < int64(CollectInterval) {
		tf.lock.Unlock()
		return nil
	}
	needsInit := !pi.initialized
	pi.initialized = true
	pflags := pi.flags
	tf.lock.Unlock()

	if pflags.EbpfProfilingDisabled {
		return nil
	}
	if needsInit {
		cmdline := proc.GetCmdline(pid)
		if proc.IsJvm(cmdline) {
			supported := jvm.IsPerfmapDumpSupported(cmdline)
			klog.Infof("JVM detected PID: %d, perfmap dump supported: %t", pid, supported)
			tf.lock.Lock()
			pi.jvmPerfmapDumpSupported = supported
			tf.lock.Unlock()
		}
	}

	tf.lock.Lock()
	// Rounds are ~CollectInterval apart with jitter; allow half a round of slack so the
	// default 1m interval still dumps every round.
	dump := pi.jvmPerfmapDumpSupported && tf.now-pi.lastPerfmapDump >= int64(jvmPerfmapDumpInterval()-CollectInterval/2)
	if dump {
		pi.lastPerfmapDump = tf.now
	}
	target := pi.target
	tf.lock.Unlock()

	if dump {
		if err := jvm.DumpPerfmap(pid); err != nil {
			klog.Warningln(err)
			diag.ProfilingErrors.WithLabelValues("jvm_perfmap").Inc()
		}
	}
	return target
}

func jvmPerfmapDumpInterval() time.Duration {
	if flags.ProfilingJvmPerfmapInterval == nil || *flags.ProfilingJvmPerfmapInterval <= 0 {
		return CollectInterval
	}
	return *flags.ProfilingJvmPerfmapInterval
}

// RegisterMetrics exposes bounded profiler internals that live in the vendored pyroscope code.
func RegisterMetrics(reg prometheus.Registerer) error {
	for _, c := range []prometheus.Collector{
		prometheus.NewCounterFunc(prometheus.CounterOpts{Name: "node_agent_profiling_perfmap_loads_total", Help: "JIT perf map files parsed"},
			func() float64 { return float64(symtab.PerfMapLoads.Load()) }),
		prometheus.NewCounterFunc(prometheus.CounterOpts{Name: "node_agent_profiling_perfmap_reused_total", Help: "Unchanged JIT perf map files not re-parsed"},
			func() float64 { return float64(symtab.PerfMapReused.Load()) }),
		prometheus.NewCounterFunc(prometheus.CounterOpts{Name: "node_agent_profiling_perfmap_skipped_total", Help: "JIT perf map files skipped because they exceed --profiling-max-perfmap-size"},
			func() float64 { return float64(symtab.PerfMapSkippedLarge.Load()) }),
		prometheus.NewCounterFunc(prometheus.CounterOpts{Name: "node_agent_profiling_perfmap_parsed_bytes_total", Help: "Bytes of JIT perf map files parsed"},
			func() float64 { return float64(symtab.PerfMapBytesParsed.Load()) }),
	} {
		if err := reg.Register(c); err != nil {
			return err
		}
	}
	return nil
}

func (tf *TargetFinder) RemoveDeadPID(pid uint32) {
	tf.lock.Lock()
	delete(tf.processes, pid)
	n := len(tf.processes)
	tf.lock.Unlock()
	diag.ProfilingTargets.Set(float64(n))
}

func (tf *TargetFinder) DebugInfo() []map[string]string {
	return nil
}

// Update is called by the session once per collection round.
func (tf *TargetFinder) Update(_ sd.TargetsOptions) {
	tf.setNow(time.Now().UnixNano())
	tf.reconcile(pidExists)
}

// reconcile removes processes that no longer exist. The session only reports a PID as dead
// through a perf-buffer event that is dropped when its queue is full, and processes handed
// over after their exit event was processed were never removed at all, so without this the
// map grew with process churn (exec probes, cron jobs, short-lived tools).
func (tf *TargetFinder) reconcile(exists func(pid uint32) bool) {
	tf.lock.Lock()
	pids := make([]uint32, 0, len(tf.processes))
	for pid := range tf.processes {
		pids = append(pids, pid)
	}
	tf.lock.Unlock()

	var dead []uint32
	for _, pid := range pids {
		if !exists(pid) {
			dead = append(dead, pid)
		}
	}

	tf.lock.Lock()
	for _, pid := range dead {
		delete(tf.processes, pid)
	}
	n := len(tf.processes)
	tf.lock.Unlock()
	diag.ProfilingTargets.Set(float64(n))
}

func pidExists(pid uint32) bool {
	_, err := os.Stat(proc.Path(pid))
	return err == nil || !os.IsNotExist(err)
}

type processInfo struct {
	startedAt int64
	target    *sd.Target

	initialized bool
	flags       proc.Flags

	jvmPerfmapDumpSupported bool
	lastPerfmapDump         int64
}
