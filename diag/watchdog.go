// Copyright Codexray
// SPDX-License-Identifier: Apache-2.0

package diag

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"sort"
	"strings"
	"sync"
	"time"

	"k8s.io/klog/v2"
)

// WatchdogConfig controls the memory watchdog. The watchdog never opens a network
// listener: it writes diagnostic bundles to a local directory (the agent's data volume),
// from where an operator copies them with `kubectl cp`. Bundles contain runtime
// profiles and memory accounting only; no command lines, environment variables,
// configuration values or telemetry payloads.
type WatchdogConfig struct {
	Dir string
	// ThresholdBytes triggers a dump when cgroup memory usage (or process RSS when the
	// cgroup is not readable) reaches it. 0 disables the absolute threshold.
	ThresholdBytes uint64
	// ThresholdRatio triggers a dump at ratio * cgroup limit. 0 disables it.
	ThresholdRatio float64
	CheckInterval  time.Duration
	// MinInterval rate-limits dumps.
	MinInterval time.Duration
	// MaxDumps bounds the number of bundles kept on disk (oldest are removed).
	MaxDumps int
}

func (c WatchdogConfig) Enabled() bool {
	return c.Dir != "" && (c.ThresholdBytes > 0 || c.ThresholdRatio > 0)
}

type Watchdog struct {
	cfg      WatchdogConfig
	lastDump time.Time
	stop     chan struct{}
	once     sync.Once

	// for tests
	now       func() time.Time
	readUsage func() (usage, limit uint64, ok bool)
}

func NewWatchdog(cfg WatchdogConfig) *Watchdog {
	if cfg.CheckInterval <= 0 {
		cfg.CheckInterval = 10 * time.Second
	}
	if cfg.MaxDumps <= 0 {
		cfg.MaxDumps = 3
	}
	return &Watchdog{cfg: cfg, stop: make(chan struct{}), now: time.Now, readUsage: memoryUsage}
}

func memoryUsage() (uint64, uint64, bool) {
	if u, l, ok := CgroupMemory(); ok {
		return u, l, true
	}
	if rss := selfRSS(); rss > 0 {
		return rss, 0, true
	}
	return 0, 0, false
}

func (w *Watchdog) Start() {
	if !w.cfg.Enabled() {
		return
	}
	if err := os.MkdirAll(w.cfg.Dir, 0o700); err != nil {
		klog.Errorln("memory watchdog disabled, cannot create dump dir:", err)
		return
	}
	klog.Infof("memory watchdog: dir=%s threshold_bytes=%d threshold_ratio=%.2f min_interval=%s max_dumps=%d",
		w.cfg.Dir, w.cfg.ThresholdBytes, w.cfg.ThresholdRatio, w.cfg.MinInterval, w.cfg.MaxDumps)
	go func() {
		t := time.NewTicker(w.cfg.CheckInterval)
		defer t.Stop()
		for {
			select {
			case <-w.stop:
				return
			case <-t.C:
				w.check()
			}
		}
	}()
}

func (w *Watchdog) Stop() {
	w.once.Do(func() { close(w.stop) })
}

// threshold returns the effective trigger level in bytes (0 = none).
func (w *Watchdog) threshold(limit uint64) uint64 {
	var th uint64
	if w.cfg.ThresholdRatio > 0 && limit > 0 {
		th = uint64(float64(limit) * w.cfg.ThresholdRatio)
	}
	if w.cfg.ThresholdBytes > 0 && (th == 0 || w.cfg.ThresholdBytes < th) {
		th = w.cfg.ThresholdBytes
	}
	return th
}

// check returns true if a dump was written.
func (w *Watchdog) check() bool {
	usage, limit, ok := w.readUsage()
	if !ok {
		return false
	}
	th := w.threshold(limit)
	if th == 0 || usage < th {
		return false
	}
	now := w.now()
	if !w.lastDump.IsZero() && now.Sub(w.lastDump) < w.cfg.MinInterval {
		WatchdogDumps.WithLabelValues("rate_limited").Inc()
		return false
	}
	w.lastDump = now
	dir, err := w.dump(now, usage, limit, th)
	if err != nil {
		klog.Errorln("memory watchdog dump failed:", err)
		WatchdogDumps.WithLabelValues("error").Inc()
		return false
	}
	klog.Warningf("memory watchdog: usage %d >= threshold %d, diagnostics written to %s", usage, th, dir)
	WatchdogDumps.WithLabelValues("written").Inc()
	w.prune()
	return true
}

func (w *Watchdog) dump(now time.Time, usage, limit, threshold uint64) (string, error) {
	dir := filepath.Join(w.cfg.Dir, "memdump-"+now.UTC().Format("20060102T150405Z"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	write := func(name string, fn func(f *os.File) error) {
		f, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			klog.Warningln(err)
			return
		}
		defer f.Close()
		if err := fn(f); err != nil {
			klog.Warningf("memory watchdog: %s: %s", name, err)
		}
	}
	// heap before GC shows what was live at the spike; allocs shows cumulative churn.
	write("heap.pb.gz", func(f *os.File) error { return pprof.Lookup("heap").WriteTo(f, 0) })
	write("allocs.pb.gz", func(f *os.File) error { return pprof.Lookup("allocs").WriteTo(f, 0) })
	write("goroutine.txt", func(f *os.File) error { return pprof.Lookup("goroutine").WriteTo(f, 1) })
	write("threadcreate.txt", func(f *os.File) error { return pprof.Lookup("threadcreate").WriteTo(f, 1) })
	write("memory.json", func(f *os.File) error {
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		enc := json.NewEncoder(f)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]any{
			"timestamp":          now.UTC(),
			"trigger_usage":      usage,
			"trigger_threshold":  threshold,
			"cgroup_limit":       limit,
			"cgroup_memory_stat": CgroupMemoryStat(),
			"process_rss":        selfRSS(),
			"goroutines":         runtime.NumGoroutine(),
			"memstats":           ms,
		})
	})
	for _, name := range []string{"status", "smaps_rollup"} {
		name := name
		write("proc_self_"+name+".txt", func(f *os.File) error {
			data, err := os.ReadFile("/proc/self/" + name)
			if err != nil {
				return err
			}
			_, err = f.Write(data)
			return err
		})
	}
	return dir, nil
}

func (w *Watchdog) prune() {
	entries, err := os.ReadDir(w.cfg.Dir)
	if err != nil {
		return
	}
	var dumps []string
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "memdump-") {
			dumps = append(dumps, e.Name())
		}
	}
	sort.Strings(dumps) // timestamps sort lexically
	for len(dumps) > w.cfg.MaxDumps {
		_ = os.RemoveAll(filepath.Join(w.cfg.Dir, dumps[0]))
		dumps = dumps[1:]
	}
}

// selfRSS returns VmRSS of the agent process in bytes (0 if unknown).
func selfRSS() uint64 {
	data, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "VmRSS:") {
			var kb uint64
			if _, err := fmt.Sscanf(strings.TrimSpace(strings.TrimPrefix(line, "VmRSS:")), "%d kB", &kb); err == nil {
				return kb * 1024
			}
		}
	}
	return 0
}
