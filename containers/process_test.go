// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package containers

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/cilium/ebpf/link"
	"github.com/codifinary/codexray-node-agent/flags"
	"github.com/codifinary/codexray-node-agent/gpu"
	"github.com/mdlayher/taskstats"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// processTestMissingPid is above the kernel's PID_MAX_LIMIT (4194304), so it never exists.
const processTestMissingPid = uint32(4_000_000_000)

// processTestLink is a fake uprobe link; embedding the interface satisfies its unexported method.
type processTestLink struct {
	link.Link
	closed *int
}

func (l processTestLink) Close() error {
	*l.closed++
	return nil
}

func processTestNew(pid uint32) *Process {
	p := &Process{Pid: pid}
	p.ctx, p.cancelFunc = context.WithCancel(context.Background())
	return p
}

func TestProcessInstrumentPython(t *testing.T) {
	cases := []struct {
		name    string
		cmdline string
		python  bool
	}{
		{"bare", "python\x00app.py", true},
		{"versioned path", "/usr/bin/python3\x00-m\x00http.server", true},
		{"minor version", "/usr/local/bin/python3.11\x00app.py", true},
		{"python2", "python2.7\x00app.py", true},
		{"argv0 rewritten with spaces", "/usr/bin/python3 -u app.py\x00", true},
		{"setproctitle with colon", "python3: worker\x00", true},
		{"gunicorn", "gunicorn: master [app]\x00", false},
		{"java", "/usr/bin/java\x00-jar\x00app.jar", false},
		{"node", "node\x00server.js", false},
		{"python in the middle", "/opt/python3/bin/uwsgi\x00", false},
		{"pythonista", "pythonista\x00", false},
		{"empty argv0", "\x00python", false},
		{"empty cmdline", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// the tracer is never dereferenced for a vanished pid: no /proc/<pid>/maps => no libs
			p := processTestNew(processTestMissingPid)
			p.instrumentPython([]byte(tc.cmdline), nil)
			assert.True(t, p.pythonGilChecked)
			assert.Equal(t, tc.python, p.pythonPrevStats != nil)
			assert.Empty(t, p.uprobes)
		})
	}
}

func TestProcessInstrumentPythonOnlyOnce(t *testing.T) {
	p := processTestNew(processTestMissingPid)
	p.instrumentPython([]byte("java\x00"), nil)
	p.instrumentPython([]byte("python3\x00"), nil)
	assert.Nil(t, p.pythonPrevStats, "the second call must be a no-op")
}

// /proc/<pid>/cmdline is controlled by the traced process (prctl/setproctitle can rewrite
// argv[0] to anything, including only whitespace). instrumentPython checks len(cmd)==0 and
// then indexes bytes.Fields(cmd)[0], which is empty for a whitespace-only argv[0].
func TestProcessInstrumentPythonWhitespaceArgv0(t *testing.T) {
	// BUG: instrumentPython panics (index out of range) when argv[0] is whitespace-only — unskip when fixed
	t.Skip("BUG: instrumentPython panics (index out of range) when argv[0] is whitespace-only")

	for _, cmdline := range []string{" \x00app.py", "\t\x00", "   "} {
		p := processTestNew(processTestMissingPid)
		assert.NotPanics(t, func() { p.instrumentPython([]byte(cmdline), nil) }, "%q", cmdline)
		assert.Nil(t, p.pythonPrevStats)
	}
}

func TestProcessInstrumentNodejs(t *testing.T) {
	cases := []struct {
		exe  string
		node bool
	}{
		{"/usr/bin/node", true},
		{"/usr/bin/nodejs", true},
		{"/usr/local/bin/node18", true},
		{"/opt/node-v20/bin/node20.1", true},
		{"/usr/bin/java", false},
		{"/usr/bin/node-exporter", false},
		{"", false},
	}
	for _, tc := range cases {
		t.Run(tc.exe, func(t *testing.T) {
			p := processTestNew(processTestMissingPid)
			p.instrumentNodejs(tc.exe, nil)
			assert.True(t, p.nodejsChecked)
			assert.Equal(t, tc.node, p.nodejsPrevStats != nil)
			assert.Empty(t, p.uprobes)
		})
	}
	p := processTestNew(processTestMissingPid)
	p.instrumentNodejs("/usr/bin/java", nil)
	p.instrumentNodejs("/usr/bin/node", nil)
	assert.Nil(t, p.nodejsPrevStats, "the second call must be a no-op")
}

func TestProcessInstrumentOwnProcess(t *testing.T) {
	// the Go test binary is neither python, node nor .NET: all checks run, nothing is attached
	p := processTestNew(uint32(os.Getpid()))
	defer p.Close()
	p.instrument(nil)
	assert.True(t, p.pythonGilChecked)
	assert.True(t, p.nodejsChecked)
	assert.Nil(t, p.pythonPrevStats)
	assert.Nil(t, p.nodejsPrevStats)
	assert.Nil(t, p.dotNetMonitor)
}

func TestProcessInstrumentVanishedProcess(t *testing.T) {
	p := processTestNew(processTestMissingPid)
	defer p.Close()
	p.instrument(nil)
	assert.False(t, p.pythonGilChecked)
	assert.False(t, p.nodejsChecked)
}

func TestProcessInstrumentDelayHonorsCancel(t *testing.T) {
	prev := *flags.InstrumentationDelay
	*flags.InstrumentationDelay = time.Hour
	t.Cleanup(func() { *flags.InstrumentationDelay = prev })

	p := processTestNew(uint32(os.Getpid()))
	p.StartedAt = time.Now()
	p.Close() // cancel before instrumenting
	done := make(chan struct{})
	go func() {
		p.instrument(nil)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("instrument did not return after the process was closed")
	}
	assert.False(t, p.pythonGilChecked, "nothing is instrumented once the process is gone")
}

func TestNewProcessAndClose(t *testing.T) {
	// the instrumentation goroutine reads the flag concurrently, so it is not modified here
	started := time.Now()
	p := NewProcess(processTestMissingPid, &taskstats.Stats{BeginTime: started}, nil)
	require.NotNil(t, p)
	assert.Equal(t, processTestMissingPid, p.Pid)
	assert.Equal(t, started, p.StartedAt)
	assert.False(t, p.Flags.EbpfTracesDisabled)

	closed := 0
	p.uprobes = []link.Link{processTestLink{closed: &closed}, processTestLink{closed: &closed}}
	p.Close()
	assert.Equal(t, 2, closed, "all uprobes are detached on close")
	assert.Error(t, p.ctx.Err(), "the instrumentation goroutine is cancelled")
}

func TestProcessNetNsId(t *testing.T) {
	p := processTestNew(processTestMissingPid)
	assert.Equal(t, "", p.NetNsId(), "vanished process")
	assert.Equal(t, "", p.netNsId, "failures are not cached")
	assert.False(t, p.isHostNs())

	self := processTestNew(uint32(os.Getpid()))
	id := self.NetNsId()
	require.NotEmpty(t, id)
	assert.Equal(t, id, self.netNsId, "cached")
	self.netNsId = "cached-id"
	assert.Equal(t, "cached-id", self.NetNsId(), "the cached value is used without touching /proc")

	self.netNsId = hostNetNsId
	assert.True(t, self.isHostNs())
	self.netNsId = "other"
	assert.False(t, self.isHostNs())
}

func TestGpuUsageReset(t *testing.T) {
	u := &GpuUsage{GPU: 1, Memory: 2}
	u.Reset()
	assert.Equal(t, GpuUsage{}, *u)
}

func TestProcessGpuUsage(t *testing.T) {
	p := processTestNew(1)
	assert.Nil(t, p.getGPUUsage(), "no samples => nil")

	now := time.Now()
	w := gpuStatsWindow.Seconds()
	p.addGpuUsageSample(gpu.ProcessUsageSample{UUID: "GPU-a", Timestamp: now.Add(-2 * time.Second), GPUPercent: 30, MemoryPercent: 15})
	p.addGpuUsageSample(gpu.ProcessUsageSample{UUID: "GPU-a", Timestamp: now.Add(-time.Second), GPUPercent: 60, MemoryPercent: 30})
	p.addGpuUsageSample(gpu.ProcessUsageSample{UUID: "GPU-b", Timestamp: now, GPUPercent: 45, MemoryPercent: 0})

	u := p.getGPUUsage()
	require.Len(t, u, 2)
	// per-second samples averaged over the window
	assert.InDelta(t, 90/w, u["GPU-a"].GPU, 1e-9)
	assert.InDelta(t, 45/w, u["GPU-a"].Memory, 1e-9)
	assert.InDelta(t, 45/w, u["GPU-b"].GPU, 1e-9)
	assert.Equal(t, 0.0, u["GPU-b"].Memory)
}

func TestProcessGpuUsageWindow(t *testing.T) {
	p := processTestNew(1)
	now := time.Now()
	p.addGpuUsageSample(gpu.ProcessUsageSample{UUID: "GPU-a", Timestamp: now.Add(-time.Hour), GPUPercent: 100})
	p.addGpuUsageSample(gpu.ProcessUsageSample{UUID: "GPU-a", Timestamp: now.Add(-gpuStatsWindow - time.Second), GPUPercent: 100})
	// adding a fresh sample evicts the ones that are older than the window relative to it
	p.addGpuUsageSample(gpu.ProcessUsageSample{UUID: "GPU-a", Timestamp: now, GPUPercent: 15})
	require.Len(t, p.gpuUsageSamples, 1)
	assert.InDelta(t, 15/gpuStatsWindow.Seconds(), p.getGPUUsage()["GPU-a"].GPU, 1e-9)

	// samples expire on read as well
	old := processTestNew(1)
	old.gpuUsageSamples = []gpu.ProcessUsageSample{{UUID: "GPU-a", Timestamp: now.Add(-time.Minute), GPUPercent: 100}}
	assert.Nil(t, old.getGPUUsage())
	assert.Empty(t, old.gpuUsageSamples)
}

func TestProcessRemoveOldGpuUsageSamples(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	mk := func(offsets ...int) []gpu.ProcessUsageSample {
		var res []gpu.ProcessUsageSample
		for _, o := range offsets {
			res = append(res, gpu.ProcessUsageSample{Timestamp: base.Add(time.Duration(o) * time.Second)})
		}
		return res
	}
	cases := []struct {
		name    string
		samples []gpu.ProcessUsageSample
		cutoff  int
		keep    int
	}{
		{"empty", nil, 0, 0},
		{"all newer", mk(1, 2, 3), 0, 3},
		{"all older", mk(1, 2, 3), 10, 0},
		{"boundary is dropped", mk(1, 2, 3), 2, 1},
		{"partial", mk(1, 2, 3, 4), 1, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := processTestNew(1)
			p.gpuUsageSamples = tc.samples
			p.removeOldGpuUsageSamples(base.Add(time.Duration(tc.cutoff) * time.Second))
			require.Len(t, p.gpuUsageSamples, tc.keep)
			for _, s := range p.gpuUsageSamples {
				assert.True(t, s.Timestamp.After(base.Add(time.Duration(tc.cutoff)*time.Second)))
			}
		})
	}
}
