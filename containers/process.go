// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package containers

import (
	"bytes"
	"context"
	"os"
	"sync"
	"time"

	"github.com/cilium/ebpf/link"
	"github.com/codifinary/codexray-node-agent/diag"
	"github.com/codifinary/codexray-node-agent/ebpftracer"
	"github.com/codifinary/codexray-node-agent/flags"
	"github.com/codifinary/codexray-node-agent/gpu"
	"github.com/codifinary/codexray-node-agent/proc"
	"github.com/jpillora/backoff"
	"github.com/mdlayher/taskstats"
)

type GpuUsage struct {
	GPU    float64
	Memory float64
}

func (gu *GpuUsage) Reset() {
	gu.Memory = 0
	gu.GPU = 0
}

type Process struct {
	Pid       uint32
	StartedAt time.Time

	Flags proc.Flags

	netNsId string

	ctx        context.Context
	cancelFunc context.CancelFunc

	dotNetMonitor *DotNetMonitor
	isGolangApp   bool

	// uprobes is written by the instrumentation goroutine (Python/Node.js probes) and by the
	// registry event loop (TLS probes), and drained by Close. uprobesLock guards it and closed,
	// so a link attached after Close is detached immediately instead of leaking until GC.
	uprobesLock           sync.Mutex
	uprobes               []link.Link
	closed                bool
	goTlsUprobesChecked   bool
	openSslUprobesChecked bool
	pythonGilChecked      bool
	nodejsChecked         bool
	nodejsPrevStats       *ebpftracer.NodejsStats
	pythonPrevStats       *ebpftracer.PythonStats

	gpuUsageSamples []gpu.ProcessUsageSample
}

func NewProcess(pid uint32, stats *taskstats.Stats, tracer *ebpftracer.Tracer) *Process {
	p := &Process{Pid: pid, StartedAt: stats.BeginTime}
	p.Flags, _ = proc.GetFlags(pid)
	p.ctx, p.cancelFunc = context.WithCancel(context.Background())
	diag.ProcessesActive.Inc()
	go p.instrument(tracer)
	return p
}

func (p *Process) NetNsId() string {
	if p.netNsId == "" {
		ns, err := proc.GetNetNs(p.Pid)
		if err != nil {
			return ""
		}
		p.netNsId = ns.UniqueId()
		_ = ns.Close()
	}
	return p.netNsId
}

func (p *Process) isHostNs() bool {
	return p.NetNsId() == hostNetNsId
}

func (p *Process) instrument(tracer *ebpftracer.Tracer) {
	if delay := *flags.InstrumentationDelay; delay > 0 && !p.StartedAt.IsZero() {
		if wait := delay - time.Since(p.StartedAt); wait > 0 {
			select {
			case <-p.ctx.Done():
				return
			case <-time.After(wait):
			}
		}
	}
	b := backoff.Backoff{Factor: 2, Min: time.Second, Max: time.Minute}
	for {
		select {
		case <-p.ctx.Done():
			return
		default:
			dest, err := os.Readlink(proc.Path(p.Pid, "exe"))
			if err != nil {
				return
			}
			cmdline := proc.GetCmdline(p.Pid)
			if dest != "/" && len(cmdline) > 0 {
				p.instrumentPython(cmdline, tracer)
				p.instrumentNodejs(dest, tracer)
				if dotNetAppName, err := dotNetApp(cmdline, p.Pid); err == nil {
					if dotNetAppName != "" {
						p.dotNetMonitor = NewDotNetMonitor(p.ctx, p.Pid, dotNetAppName)
					}
				}
				return
			}
			time.Sleep(b.Duration())
		}
	}
}

func (p *Process) instrumentPython(cmdline []byte, tracer *ebpftracer.Tracer) {
	if p.pythonGilChecked {
		return
	}
	p.pythonGilChecked = true
	parts := bytes.Split(cmdline, []byte{0})
	cmd := parts[0]
	if len(cmd) == 0 {
		return
	}
	cmd = bytes.TrimSuffix(bytes.Fields(cmd)[0], []byte{':'})
	if !pythonCmd.Match(cmd) {
		return
	}
	p.pythonPrevStats = &ebpftracer.PythonStats{}
	p.addUprobes(tracer.AttachPythonThreadLockProbes(p.Pid))
}

func (p *Process) instrumentNodejs(exe string, tracer *ebpftracer.Tracer) {
	if p.nodejsChecked {
		return
	}
	p.nodejsChecked = true
	if !nodejsCmd.MatchString(exe) {
		return
	}
	p.nodejsPrevStats = &ebpftracer.NodejsStats{}
	p.addUprobes(tracer.AttachNodejsProbes(p.Pid, exe))
}

func (p *Process) addGpuUsageSample(sample gpu.ProcessUsageSample) {
	p.removeOldGpuUsageSamples(sample.Timestamp.Add(-gpuStatsWindow))
	p.gpuUsageSamples = append(p.gpuUsageSamples, sample)
}

func (p *Process) getGPUUsage() map[string]*GpuUsage {
	p.removeOldGpuUsageSamples(time.Now().Add(-gpuStatsWindow))
	if len(p.gpuUsageSamples) == 0 {
		return nil
	}
	gpuStatsWindowSeconds := gpuStatsWindow.Seconds()
	res := make(map[string]*GpuUsage)
	for _, sample := range p.gpuUsageSamples {
		u := res[sample.UUID]
		if u == nil {
			u = &GpuUsage{}
			res[sample.UUID] = u
		}
		u.GPU += float64(sample.GPUPercent) / gpuStatsWindowSeconds
		u.Memory += float64(sample.MemoryPercent) / gpuStatsWindowSeconds
	}
	return res
}

func (p *Process) removeOldGpuUsageSamples(cutoff time.Time) {
	i := 0
	for ; i < len(p.gpuUsageSamples); i++ {
		if p.gpuUsageSamples[i].Timestamp.After(cutoff) {
			break
		}
	}
	if i > 0 {
		copy(p.gpuUsageSamples, p.gpuUsageSamples[i:])
		p.gpuUsageSamples = p.gpuUsageSamples[:len(p.gpuUsageSamples)-i]
	}
}

// addUprobes takes ownership of links. If the process has already been closed (it exited while
// the probes were being attached), the links are detached immediately.
func (p *Process) addUprobes(links []link.Link) {
	if len(links) == 0 {
		return
	}
	p.uprobesLock.Lock()
	defer p.uprobesLock.Unlock()
	if p.closed {
		for _, l := range links {
			_ = l.Close()
		}
		diag.UprobesLeakedOnClose.Add(float64(len(links)))
		return
	}
	p.uprobes = append(p.uprobes, links...)
	diag.UprobesActive.Add(float64(len(links)))
}

func (p *Process) uprobeCount() int {
	p.uprobesLock.Lock()
	defer p.uprobesLock.Unlock()
	return len(p.uprobes)
}

// Close cancels the process's background work and detaches its uprobes. It is idempotent.
func (p *Process) Close() {
	p.cancelFunc()
	p.uprobesLock.Lock()
	if p.closed {
		p.uprobesLock.Unlock()
		return
	}
	p.closed = true
	links := p.uprobes
	p.uprobes = nil
	p.uprobesLock.Unlock()
	for _, u := range links {
		_ = u.Close()
	}
	diag.UprobesActive.Sub(float64(len(links)))
	diag.ProcessesActive.Dec()
}
