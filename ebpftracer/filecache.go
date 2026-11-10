// Copyright Codexray
// SPDX-License-Identifier: Apache-2.0

package ebpftracer

import (
	"os"
	"sync"
	"syscall"
)

// fileKey identifies file contents without reading them: the same binary mapped by many
// processes (or seen through different /proc/<pid>/root paths) yields the same key, and a
// replaced binary yields a new one.
type fileKey struct {
	dev, ino uint64
	size     int64
	mtimeNs  int64
}

func statFileKey(path string) (fileKey, bool) {
	fi, err := os.Stat(path) // follows /proc/<pid>/exe and /proc/<pid>/root symlinks
	if err != nil {
		return fileKey{}, false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fileKey{}, false
	}
	return fileKey{dev: uint64(st.Dev), ino: st.Ino, size: fi.Size(), mtimeNs: fi.ModTime().UnixNano()}, true
}

// fileInfoCache memoizes an expensive per-file computation. It is bounded: when full it is
// reset, which is O(1) amortized and only costs recomputation.
type fileInfoCache[V any] struct {
	mu      sync.Mutex
	max     int
	entries map[fileKey]V
}

func newFileInfoCache[V any](max int) *fileInfoCache[V] {
	return &fileInfoCache[V]{max: max, entries: map[fileKey]V{}}
}

// get returns the cached value for the file at path, computing it with load on a miss.
// cached reports whether the value came from the cache. If the file cannot be stat'ed,
// load is called without caching.
func (c *fileInfoCache[V]) get(path string, load func(path string) V) (v V, cached bool) {
	key, ok := statFileKey(path)
	if !ok {
		return load(path), false
	}
	c.mu.Lock()
	if v, ok := c.entries[key]; ok {
		c.mu.Unlock()
		return v, true
	}
	c.mu.Unlock()
	v = load(path) // outside the lock: may take a while for large binaries
	c.mu.Lock()
	if len(c.entries) >= c.max {
		c.entries = map[fileKey]V{}
	}
	c.entries[key] = v
	c.mu.Unlock()
	return v, false
}

func (c *fileInfoCache[V]) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}
