// Copyright Codexray, Inc.
// SPDX-License-Identifier: Apache-2.0

package containers

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime/pprof"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/codifinary/codexray-node-agent/flags"
)

// heapDumper writes runtime/pprof heap profiles to a directory and keeps the
// last N files. Dumps are gzip'd pprof — load with `go tool pprof <file>`.
//
// Concurrent calls are serialised; dumping holds a mutex but is not on any
// hot path (memdiag tick boundary at most).
type heapDumper struct {
	dir  string
	keep int
	mu   sync.Mutex
}

func newHeapDumper() *heapDumper {
	dir := *flags.HeapDumpDir
	if dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "heapdump: cannot create %s: %v\n", dir, err)
		return nil
	}
	keep := *flags.HeapDumpKeep
	if keep <= 0 {
		keep = 8
	}
	return &heapDumper{dir: dir, keep: keep}
}

// Dump writes a heap profile and returns the file path. The reason becomes
// part of the filename so it is obvious in the directory listing whether the
// dump was triggered by growth, by interval, or manually.
func (d *heapDumper) Dump(reason string) (string, error) {
	if d == nil {
		return "", nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	safe := strings.ReplaceAll(reason, "/", "_")
	name := fmt.Sprintf("heap-%s-%s.pb.gz", time.Now().UTC().Format("20060102T150405Z"), safe)
	path := filepath.Join(d.dir, name)

	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	// pprof.Lookup("heap").WriteTo emits gzipped pprof when debug=0.
	if err := pprof.Lookup("heap").WriteTo(f, 0); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return "", err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	d.gcOld()
	return path, nil
}

func (d *heapDumper) gcOld() {
	entries, err := os.ReadDir(d.dir)
	if err != nil {
		return
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if strings.HasPrefix(n, "heap-") && strings.HasSuffix(n, ".pb.gz") {
			files = append(files, n)
		}
	}
	if len(files) <= d.keep {
		return
	}
	sort.Strings(files) // timestamp prefix → lexicographic == chronological
	for _, name := range files[:len(files)-d.keep] {
		_ = os.Remove(filepath.Join(d.dir, name))
	}
}
