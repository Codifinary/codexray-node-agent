// Copyright Codexray, Inc.
// SPDX-License-Identifier: Apache-2.0

package ebpftracer

import (
	"debug/elf"
	"fmt"
	"io"
	"os"
	"runtime"
	"sync"

	"k8s.io/klog/v2"
)

// symInfo is the minimal per-symbol data we need to attach uprobes/uretprobes
// without re-opening the ELF. address is where the function starts; for
// uretprobes we also need every RET offset inside its body so we can attach a
// kprobe on each of them.
type symInfo struct {
	address       uint64
	returnOffsets []int
}

// symCacheKey identifies a unique binary by path + mtime + size. mtime/size
// invalidate the entry when the binary on disk changes (rolling upgrade etc.).
type symCacheKey struct {
	path  string
	mtime int64
	size  int64
}

// binarySymCache holds parsed symbol metadata for each unique binary the agent
// has touched. Each TLS uprobe attach previously caused debug/elf to allocate
// ~50-70 MB transient memory; with this cache the parse happens ONCE per
// (path, mtime, size) and subsequent attaches are O(map lookup).
//
// The cache is intentionally tiny — a typical node has at most a few dozen
// unique binaries that speak TLS — so a fixed-size map with random eviction
// is sufficient. No external LRU dep required.
type binarySymCache struct {
	mu  sync.Mutex
	cap int
	m   map[symCacheKey]map[string]symInfo
}

func newBinarySymCache(capEntries int) *binarySymCache {
	return &binarySymCache{
		cap: capEntries,
		m:   make(map[symCacheKey]map[string]symInfo),
	}
}

// makeSymCacheKey stats `path` and returns a key suitable for cache lookup.
// Returns an error if the file is missing or unreadable.
func makeSymCacheKey(path string) (symCacheKey, error) {
	st, err := os.Stat(path)
	if err != nil {
		return symCacheKey{}, err
	}
	return symCacheKey{
		path:  path,
		mtime: st.ModTime().UnixNano(),
		size:  st.Size(),
	}, nil
}

func (c *binarySymCache) get(key symCacheKey) (map[string]symInfo, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	syms, ok := c.m[key]
	return syms, ok
}

func (c *binarySymCache) put(key symCacheKey, syms map[string]symInfo) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.m) >= c.cap {
		// Randomized eviction: drop one entry (Go map iteration order is
		// randomized, so this is effectively random). For a node-agent
		// node-local cache this is fine.
		for k := range c.m {
			delete(c.m, k)
			break
		}
	}
	c.m[key] = syms
}

// Package-level caches. Sized at 128 entries each — comfortably above the
// "every unique binary on a busy K8s node" working set.
var (
	goTlsSymCache   = newBinarySymCache(128)
	opensslSymCache = newBinarySymCache(128)
)

// parseSymInfo opens `path`, extracts the requested symbols and (for those in
// needRetOffsets) their RET offsets, then closes the ELF. After this returns
// the only memory retained is the small symInfo map — the ~50-70 MB
// debug/elf working set has been released and is eligible for GC.
//
// Callers should put the returned map into the appropriate sym cache so future
// attaches against the same (path, mtime, size) avoid this parse entirely.
func parseSymInfo(path string, needRetOffsets map[string]bool, names ...string) (map[string]symInfo, error) {
	// Log every cache miss BEFORE the expensive parse runs. This way we always
	// see which binary triggered the work, even if the pod is killed mid-parse
	// (the "nodejs/Go-TLS uprobes attached" success logs only fire after the
	// attach completes — leaving us blind on OOM-mid-attach scenarios).
	klog.Infof("symcache miss, parsing ELF symbols: path=%s wanted=%v", path, names)

	// Force a GC after this function returns so the ~50-70 MB transient
	// debug/elf working set (raw symtab + strtab bytes + parsed Symbol slice)
	// is reclaimed BEFORE the next attach has a chance to allocate on top of
	// it. This serialises the parse cost so concurrent cache misses can't
	// stack into hundreds of MB of garbage waiting for the next GC cycle.
	// The STW pause is short (~5-15 ms) and only happens on a cache miss —
	// after the binary cache warms up this code path is no longer reached.
	defer runtime.GC()

	ef, err := OpenELFFile(path)
	if err != nil {
		return nil, err
	}
	defer ef.Close() // ELFFile.Close also nils f.symbols / f.textSection (see elf.go)

	// findSymbolsStreaming reads Elf64_Sym entries 24 bytes at a time and
	// resolves names individually via the strtab section's ReaderAt — it
	// never materialises the full []elf.Symbol slice that GetSymbols (and
	// its underlying debug/elf.Symbols()/DynamicSymbols()) would allocate.
	// That slice is the ~50-70 MB-per-binary transient that previously
	// stacked into the agent OOM.
	wanted := make(map[string]struct{}, len(names))
	for _, n := range names {
		wanted[n] = struct{}{}
	}
	syms, err := ef.findSymbolsStreaming(wanted)
	if err != nil {
		return nil, err
	}

	out := make(map[string]symInfo, len(syms))
	for name, s := range syms {
		// IMPORTANT: resolve address and (if needed) return offsets now
		// while the ELFFile handle is still open. After ef.Close() runs
		// the file goes away — but the numbers we extract here are
		// self-contained.
		info := symInfo{address: symbolFileOffset(ef, s)}
		if needRetOffsets[name] {
			offsets, err := readReturnOffsets(ef, s)
			if err != nil {
				return nil, fmt.Errorf("return offsets for %s: %w", name, err)
			}
			info.returnOffsets = offsets
		}
		out[name] = info
	}
	return out, nil
}

// symbolFileOffset translates a symbol's virtual address (st_value) into a
// file offset by locating the executable PT_LOAD segment that contains it.
// Mirrors the logic of (*Symbol).Address() in elf.go but operates directly on
// an elf.Symbol value so we don't need a wrapping *Symbol with a back-ref to
// the ELFFile.
func symbolFileOffset(ef *ELFFile, s elf.Symbol) uint64 {
	addr := s.Value
	for _, p := range ef.elf.Progs {
		if p.Type != elf.PT_LOAD || (p.Flags&elf.PF_X) == 0 {
			continue
		}
		if p.Vaddr <= s.Value && s.Value < (p.Vaddr+p.Memsz) {
			return s.Value - p.Vaddr + p.Off
		}
	}
	return addr
}

// readReturnOffsets extracts every RET-instruction offset within `s`'s body
// by reading the corresponding bytes from .text and decoding them. Mirrors
// (*Symbol).ReturnOffsets() in elf.go.
func readReturnOffsets(ef *ELFFile, s elf.Symbol) ([]int, error) {
	text, reader, err := ef.getTextSectionAndReader()
	if err != nil {
		return nil, err
	}
	if s.Value < text.Addr {
		return nil, fmt.Errorf("symbol value %#x below .text base %#x", s.Value, text.Addr)
	}
	sStart := s.Value - text.Addr
	if _, err := reader.Seek(int64(sStart), io.SeekStart); err != nil {
		return nil, err
	}
	sBytes := make([]byte, s.Size)
	if _, err := io.ReadFull(reader, sBytes); err != nil {
		return nil, err
	}
	offsets := getReturnOffsets(ef.elf.Machine, sBytes)
	if len(offsets) == 0 {
		return nil, fmt.Errorf("no offsets found")
	}
	return offsets, nil
}
