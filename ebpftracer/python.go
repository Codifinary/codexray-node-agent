// Copyright Codexray, Inc.
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package ebpftracer

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/cilium/ebpf/link"
	"github.com/codifinary/codexray-node-agent/proc"
	"golang.org/x/exp/maps"
	"k8s.io/klog/v2"
)

var (
	libcRegexp = regexp.MustCompile(`libc[\.-]`)
	muslRegexp = regexp.MustCompile(`ld-musl[\.-]`)

	// pythonSymCache: parsed addresses + ret offsets for each pthread-providing
	// library (libc / musl / libpthread). Same rationale as goTlsSymCache —
	// avoid the ~5-15 MB debug/elf transient on every Python uprobe attach.
	pythonSymCache = newBinarySymCache(128)
)

const pythonLockSymbol = "pthread_cond_timedwait"

func (t *Tracer) AttachPythonThreadLockProbes(pid uint32) []link.Link {
	log := func(libPath, msg string, err error) {
		if err != nil {
			for _, s := range []string{"no such file or directory", "no such process", "permission denied"} {
				if strings.HasSuffix(err.Error(), s) {
					return
				}
			}
			klog.ErrorfDepth(1, "pid=%d lib=%s: %s: %s", pid, libPath, msg, err)
			return
		}
		klog.InfofDepth(1, "pid=%d lib=%s: %s", pid, libPath, msg)
	}

	var (
		links []link.Link
		err   error
	)

	for _, libPath := range getPthreadLibs(pid) {
		if links, err = t.attachPythonUprobes(libPath, pid); err == nil {
			log(libPath, "python uprobes attached", nil)
			return links
		} else {
			log(libPath, "failed to attach python uprobes", err)
		}
	}
	if len(links) > 0 {

	}
	return links
}

func (t *Tracer) attachPythonUprobes(libPath string, pid uint32) ([]link.Link, error) {
	// Cache the pthread_cond_timedwait address + RET offsets per (libPath,
	// mtime, size). libc / musl / libpthread are typically shared across many
	// processes on a node, so the first attach pays the parse cost and every
	// subsequent attach is a map lookup.
	key, err := makeSymCacheKey(libPath)
	if err != nil {
		return nil, err
	}
	syms, cached := pythonSymCache.get(key)
	if !cached {
		syms, err = parseSymInfo(libPath, map[string]bool{pythonLockSymbol: true}, pythonLockSymbol)
		if err != nil {
			return nil, err
		}
		pythonSymCache.put(key, syms)
	}
	info, ok := syms[pythonLockSymbol]
	if !ok || info.address == 0 {
		return nil, fmt.Errorf("%s not found in %s", pythonLockSymbol, libPath)
	}

	exe, err := link.OpenExecutable(libPath)
	if err != nil {
		return nil, err
	}

	l, err := exe.Uprobe(pythonLockSymbol, t.uprobes["pthread_cond_timedwait_enter"], &link.UprobeOptions{Address: info.address, PID: int(pid)})
	if err != nil {
		return nil, err
	}
	links := []link.Link{l}
	for _, off := range info.returnOffsets {
		l, err := exe.Uprobe(pythonLockSymbol, t.uprobes["pthread_cond_timedwait_exit"], &link.UprobeOptions{Address: info.address, Offset: uint64(off), PID: int(pid)})
		if err != nil {
			for _, l := range links {
				_ = l.Close()
			}
			return nil, err
		}
		links = append(links, l)
	}
	return links, nil
}

func getPthreadLibs(pid uint32) []string {
	f, err := os.Open(proc.Path(pid, "maps"))
	if err != nil {
		return nil
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Split(bufio.ScanLines)
	libs := map[string]bool{}
	for scanner.Scan() {
		parts := strings.Fields(scanner.Text())
		if len(parts) <= 5 {
			continue
		}
		libPath := parts[5]
		if libcRegexp.MatchString(libPath) || muslRegexp.MatchString(libPath) || strings.Contains(libPath, "libpthread") {
			libs[proc.Path(pid, "root", libPath)] = true
		}
	}
	return maps.Keys(libs)
}
