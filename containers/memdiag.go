// Copyright Codexray, Inc.
// SPDX-License-Identifier: Apache-2.0

package containers

import (
	"bufio"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/codifinary/codexray-node-agent/ebpftracer/l7"
	"github.com/codifinary/codexray-node-agent/flags"
	"k8s.io/klog/v2"
)

// Fields from /proc/self/status that locate memory the kernel attributes to
// this process. RssAnon vs HeapInuse is the key delta — anon RSS that is NOT
// in the Go heap is either BPF maps, mmap'd regions, CGO arenas, or stacks.
var procStatusMemFields = map[string]bool{
	"VmRSS":    true, // resident set
	"RssAnon":  true, // anonymous resident — main OOM driver here
	"RssFile":  true, // file-backed resident
	"RssShmem": true,
	"VmData":   true, // data + bss + heap (process-wide, not Go-specific)
	"VmStk":    true, // initial thread stack
	"VmLib":    true, // shared libs
	"VmPTE":    true, // page table entries
	"VmSwap":   true,
}

func readProcStatusMemFields() (string, error) {
	f, err := os.Open("/proc/self/status")
	if err != nil {
		return "", err
	}
	defer f.Close()
	var b strings.Builder
	sc := bufio.NewScanner(f)
	first := true
	for sc.Scan() {
		line := sc.Text()
		colon := strings.IndexByte(line, ':')
		if colon < 0 {
			continue
		}
		key := line[:colon]
		if !procStatusMemFields[key] {
			continue
		}
		val := strings.TrimSpace(line[colon+1:])
		val = strings.TrimSuffix(val, " kB")
		if !first {
			b.WriteByte(' ')
		}
		first = false
		b.WriteString(key)
		b.WriteByte('=')
		b.WriteString(val)
		b.WriteString("kB")
	}
	return b.String(), sc.Err()
}

// memDiagTopN bounds how many "worst" containers are logged per tick per metric.
const memDiagTopN = 10

// containerMemSnapshot captures sizes of the per-container data structures
// suspected of unbounded growth. Read under c.lock.
type containerMemSnapshot struct {
	id ContainerID

	processes      int
	activeConns    int
	connStats      int
	failedAttempts int
	lastAttempts   int
	listens        int
	mounts         int
	logParsers     int
	l7DestByProto  map[l7.Protocol]int
	l7DestTotal    int
	pgPreparedSum  int
	myPreparedSum  int
	h2ActiveSum    int
	pgPreparedMax  int
	myPreparedMax  int
	h2ActiveMax    int
}

// snapshot must be called with c.lock held.
func (c *Container) snapshot() containerMemSnapshot {
	s := containerMemSnapshot{
		id:             c.id,
		processes:      len(c.processes),
		activeConns:    len(c.activeConnections),
		connStats:      len(c.connectionStats),
		failedAttempts: len(c.failedConnectionAttempts),
		lastAttempts:   len(c.lastConnectionAttempts),
		listens:        len(c.listens),
		mounts:         len(c.mounts),
		logParsers:     len(c.logParsers),
		l7DestByProto:  map[l7.Protocol]int{},
	}
	for proto, byDst := range c.l7Stats {
		s.l7DestByProto[proto] = len(byDst)
		s.l7DestTotal += len(byDst)
	}
	for _, ac := range c.activeConnections {
		if n := ac.postgresParser.PreparedStatementsLen(); n > 0 {
			s.pgPreparedSum += n
			if n > s.pgPreparedMax {
				s.pgPreparedMax = n
			}
		}
		if n := ac.mysqlParser.PreparedStatementsLen(); n > 0 {
			s.myPreparedSum += n
			if n > s.myPreparedMax {
				s.myPreparedMax = n
			}
		}
		if n := ac.http2Parser.ActiveRequestsLen(); n > 0 {
			s.h2ActiveSum += n
			if n > s.h2ActiveMax {
				s.h2ActiveMax = n
			}
		}
	}
	return s
}

// memDiagSink writes directly to os.Stderr (bypassing the agent's klog rate
// limiter) and, if configured, mirrors to a file with fsync after every tick
// so the last tick survives an OOM SIGKILL.
type memDiagSink struct {
	file *os.File
	mu   sync.Mutex
}

func newMemDiagSink(path string) *memDiagSink {
	s := &memDiagSink{}
	if path != "" {
		// Append, create if missing, no truncate. Restart-friendly.
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
		if err != nil {
			// Print to stderr directly so the failure is visible even under
			// rate-limiting; do not fall back silently.
			fmt.Fprintf(os.Stderr, "memdiag: failed to open %s: %v (continuing with stderr only)\n", path, err)
		} else {
			s.file = f
		}
	}
	return s
}

func (s *memDiagSink) write(line string) {
	if line == "" {
		return
	}
	if line[len(line)-1] != '\n' {
		line += "\n"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _ = os.Stderr.Write([]byte(line))
	if s.file != nil {
		_, _ = s.file.Write([]byte(line))
	}
}

func (s *memDiagSink) sync() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file != nil {
		_ = s.file.Sync()
	}
}

// startMemDiag launches the periodic diagnostic logger if --memdiag-interval > 0.
// Output bypasses the agent's klog rate limiter so it is never silently dropped.
func (r *Registry) startMemDiag() {
	interval := *flags.MemDiagInterval
	// Print the configuration banner unconditionally so it is obvious from
	// the startup log whether memdiag is on, off, or misconfigured.
	banner := fmt.Sprintf("memdiag: configured interval=%s file=%q (set MEMDIAG_INTERVAL=30s to enable)", interval, *flags.MemDiagFile)
	fmt.Fprintln(os.Stderr, banner)
	klog.Infoln(banner)

	if interval <= 0 {
		return
	}

	sink := newMemDiagSink(*flags.MemDiagFile)
	dumper := newHeapDumper()
	if dumper != nil {
		sink.write(fmt.Sprintf("memdiag: heap dumps -> %s (every=%d growth_mb=%d keep=%d)", *flags.HeapDumpDir, *flags.HeapDumpEvery, *flags.HeapDumpOnGrowthMB, *flags.HeapDumpKeep))
	}
	sink.write(fmt.Sprintf("memdiag: starting at %s, interval=%s, pid=%d", time.Now().Format(time.RFC3339), interval, os.Getpid()))
	sink.sync()

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		state := &memDiagState{dumper: dumper}
		// Fire once immediately so we don't lose the first interval to startup.
		r.logMemDiag(sink, state)
		for range ticker.C {
			r.logMemDiag(sink, state)
		}
	}()
}

// memDiagState carries info that needs to survive across ticks (e.g. previous
// HeapAlloc to detect growth, tick counter for interval-based heap dumps).
type memDiagState struct {
	dumper        *heapDumper
	prevHeapAlloc uint64
	ticks         int
}

func (r *Registry) logMemDiag(sink *memDiagSink, state *memDiagState) {
	state.ticks++
	chLen := len(r.events)
	chCap := cap(r.events)

	// Snapshot all containers. Grab pointers first, then lock each one
	// individually — avoids holding the whole registry while we walk.
	containers := make([]*Container, 0, len(r.containersById))
	for _, c := range r.containersById {
		containers = append(containers, c)
	}
	snaps := make([]containerMemSnapshot, 0, len(containers))
	for _, c := range containers {
		c.lock.Lock()
		s := c.snapshot()
		c.lock.Unlock()
		snaps = append(snaps, s)
	}

	var totalConns, totalConnStats, totalL7Dests, totalPG, totalMY, totalH2 int
	for _, s := range snaps {
		totalConns += s.activeConns
		totalConnStats += s.connStats
		totalL7Dests += s.l7DestTotal
		totalPG += s.pgPreparedSum
		totalMY += s.myPreparedSum
		totalH2 += s.h2ActiveSum
	}

	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)

	ts := time.Now().Format("15:04:05")

	// Decide whether to drop a heap profile this tick.
	if state.dumper != nil {
		var reason string
		growthMB := int64(0)
		if state.prevHeapAlloc > 0 && ms.HeapAlloc > state.prevHeapAlloc {
			growthMB = int64(ms.HeapAlloc-state.prevHeapAlloc) >> 20
		}
		switch {
		case *flags.HeapDumpOnGrowthMB > 0 && growthMB >= int64(*flags.HeapDumpOnGrowthMB):
			reason = fmt.Sprintf("growth-%dMB", growthMB)
		case *flags.HeapDumpEvery > 0 && state.ticks%*flags.HeapDumpEvery == 0:
			reason = "periodic"
		case state.ticks == 1:
			reason = "first-tick"
		}
		if reason != "" {
			if path, err := state.dumper.Dump(reason); err != nil {
				sink.write(fmt.Sprintf("memdiag/heapdump t=%s error=%q", ts, err.Error()))
			} else {
				sink.write(fmt.Sprintf("memdiag/heapdump t=%s reason=%s path=%s heap_alloc=%dMB", ts, reason, path, ms.HeapAlloc>>20))
			}
		}
	}
	state.prevHeapAlloc = ms.HeapAlloc
	sink.write(fmt.Sprintf(
		"memdiag/totals t=%s heap_alloc=%dMB heap_inuse=%dMB heap_idle=%dMB heap_released=%dMB heap_sys=%dMB stack_inuse=%dMB stack_sys=%dMB mspan_sys=%dMB mcache_sys=%dMB gc_sys=%dMB other_sys=%dMB sys=%dMB next_gc=%dMB num_gc=%d cgo_calls=%d goroutines=%d containers=%d events_ch=%d/%d active_conns=%d conn_stats=%d l7_dest=%d pg_prepared=%d mysql_prepared=%d http2_streams=%d",
		ts,
		ms.HeapAlloc>>20, ms.HeapInuse>>20, ms.HeapIdle>>20, ms.HeapReleased>>20, ms.HeapSys>>20,
		ms.StackInuse>>20, ms.StackSys>>20,
		ms.MSpanSys>>20, ms.MCacheSys>>20, ms.GCSys>>20, ms.OtherSys>>20,
		ms.Sys>>20, ms.NextGC>>20,
		ms.NumGC, runtime.NumCgoCall(), runtime.NumGoroutine(),
		len(snaps), chLen, chCap,
		totalConns, totalConnStats, totalL7Dests,
		totalPG, totalMY, totalH2,
	))

	// Also emit a kernel-side view from /proc/self/status: this is what the
	// cgroup memcg accounts and what triggers OOM. It includes BPF locked
	// memory, mmap'd regions, CGO arenas, and goroutine stacks — none of
	// which are visible in Go's HeapAlloc.
	if status, err := readProcStatusMemFields(); err == nil {
		sink.write("memdiag/procstat t=" + ts + " " + status)
	}

	logTop := func(label string, key func(containerMemSnapshot) int) {
		filtered := make([]containerMemSnapshot, 0, len(snaps))
		for _, s := range snaps {
			if key(s) > 0 {
				filtered = append(filtered, s)
			}
		}
		if len(filtered) == 0 {
			return
		}
		sort.Slice(filtered, func(i, j int) bool { return key(filtered[i]) > key(filtered[j]) })
		n := len(filtered)
		if n > memDiagTopN {
			n = memDiagTopN
		}
		for _, s := range filtered[:n] {
			sink.write(fmt.Sprintf(
				"memdiag/top_%s t=%s id=%s procs=%d active_conns=%d conn_stats=%d l7_dst=%d pg_sum=%d pg_max=%d my_sum=%d my_max=%d h2_sum=%d h2_max=%d listens=%d log_parsers=%d",
				label, ts, s.id, s.processes, s.activeConns, s.connStats, s.l7DestTotal,
				s.pgPreparedSum, s.pgPreparedMax,
				s.myPreparedSum, s.myPreparedMax,
				s.h2ActiveSum, s.h2ActiveMax,
				s.listens, s.logParsers,
			))
		}
	}

	logTop("pg_prepared", func(s containerMemSnapshot) int { return s.pgPreparedSum })
	logTop("mysql_prepared", func(s containerMemSnapshot) int { return s.myPreparedSum })
	logTop("http2_streams", func(s containerMemSnapshot) int { return s.h2ActiveSum })
	logTop("active_conns", func(s containerMemSnapshot) int { return s.activeConns })
	logTop("l7_destinations", func(s containerMemSnapshot) int { return s.l7DestTotal })

	// Make sure this tick survives a SIGKILL.
	sink.sync()
}
