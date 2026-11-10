package symtab

import (
	"bufio"
	"cmp"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
)

type PerfMap struct {
	symbols []Symbol

	// identity of the file the symbols were parsed from, to skip re-parsing an unchanged file
	size  int64
	mtime int64
}

// Codexray: JVM (Compiler.perfmap) and Node.js perf maps are re-read for every profiled PID
// on every collection round and parsed fully into memory. For large JIT'd applications the
// file reaches tens to hundreds of MB, so each JVM could cost several times that in heap,
// every minute, with the previous table still live during the parse.
var (
	// MaxPerfMapSize skips perf map files larger than this (JIT frames then resolve as
	// unknown). 0 = unlimited.
	MaxPerfMapSize int64 = 64 << 20

	PerfMapLoads        atomic.Int64 // files parsed
	PerfMapReused       atomic.Int64 // unchanged files not re-parsed
	PerfMapSkippedLarge atomic.Int64 // files skipped because of MaxPerfMapSize
	PerfMapBytesParsed  atomic.Int64
)

// NewPerfMap loads the perf map of pid. prev, if non-nil, is returned as-is when the file
// has not changed since it was parsed.
func NewPerfMap(pid int, prev *PerfMap) (*PerfMap, error) {
	nsPid := getNsPid(pid)

	perfMapPath := fmt.Sprintf("/proc/%d/root/tmp/perf-%d.map", pid, nsPid)
	return loadPerfMap(perfMapPath, prev)
}

func loadPerfMap(perfMapPath string, prev *PerfMap) (*PerfMap, error) {
	f, err := os.Open(perfMapPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if prev != nil && prev.size == fi.Size() && prev.mtime == fi.ModTime().UnixNano() {
		PerfMapReused.Add(1)
		return prev, nil
	}
	if MaxPerfMapSize > 0 && fi.Size() > MaxPerfMapSize {
		// Not an error: an error would mark the whole ProcTable as failed and stop refreshing
		// its ELF mappings. Native frames keep resolving; only JIT frames are left unknown.
		PerfMapSkippedLarge.Add(1)
		return nil, nil
	}
	pm := &PerfMap{size: fi.Size(), mtime: fi.ModTime().UnixNano()}
	if pm.symbols, err = parsePerfMapSized(f, fi.Size()); err != nil {
		return nil, err
	}
	PerfMapLoads.Add(1)
	PerfMapBytesParsed.Add(fi.Size())
	return pm, nil
}

func (pm *PerfMap) Resolve(pc uint64) Symbol {
	i, ok := slices.BinarySearchFunc(pm.symbols, -1, func(s Symbol, _ int) int {
		if s.Start <= pc {
			if pc < s.Start+s.size {
				return 0
			}
			return -1
		}
		return 1
	})
	if ok {
		return pm.symbols[i]
	}
	return Symbol{}
}

func parsePerfMap(reader io.Reader) ([]Symbol, error) {
	return parsePerfMapSized(reader, 0)
}

// parsePerfMapSized pre-sizes the symbol slice from the file size (lines are typically
// 60-200 bytes) so appends don't repeatedly double a large backing array.
func parsePerfMapSized(reader io.Reader, size int64) ([]Symbol, error) {
	var (
		syms []Symbol
		err  error
	)
	if size > 0 {
		syms = make([]Symbol, 0, size/80+1)
	}
	scanner := bufio.NewScanner(reader)
	for i := 0; scanner.Scan(); i++ {
		line := scanner.Text()
		items := strings.SplitN(line, " ", 3)
		if len(items) != 3 {
			return nil, fmt.Errorf("invalid line (does not contain 3 parts): %s", line)
		}
		s := Symbol{
			Name:       items[2],
			generation: i,
		}
		s.Start, err = strconv.ParseUint(strings.TrimPrefix(items[0], "0x"), 16, 64)
		if err != nil {
			return nil, err
		}
		s.size, err = strconv.ParseUint(strings.TrimPrefix(items[1], "0x"), 16, 64)
		if err != nil {
			return nil, err
		}
		syms = append(syms, s)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	slices.SortFunc(syms, func(a, b Symbol) int {
		return cmp.Compare(a.Start, b.Start)
	})
	syms = removeOverlaps(syms)
	return syms, nil
}

// Node.js appends new symbols to the perf map file, and their addresses can overlap earlier ones.
// This function removes overlapping symbols, keeping those that appeared latest.
func removeOverlaps(syms []Symbol) []Symbol {
	if len(syms) == 0 {
		return nil
	}

	var (
		lastValid = 0
		toRemove  = make(map[int]struct{})
	)

	for i := 1; i < len(syms); i++ {
		prev := syms[lastValid]
		curr := syms[i]

		if prev.Start+prev.size > curr.Start {
			// Overlap detected
			if prev.generation > curr.generation {
				toRemove[i] = struct{}{}
			} else {
				toRemove[lastValid] = struct{}{}
				lastValid = i
			}
		} else {
			lastValid = i
		}
	}

	// Compact the slice in-place
	write := 0
	for i := range syms {
		if _, skip := toRemove[i]; !skip {
			syms[write] = syms[i]
			write++
		}
	}

	return syms[:write]
}

func getNsPid(pid int) int {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return pid
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if fields[0] == "NSpid:" {
			if len(fields) == 3 {
				if nsPid, err := strconv.ParseUint(fields[2], 10, 32); err == nil {
					return int(nsPid)
				}
			}
			return pid
		}
	}
	return pid
}
