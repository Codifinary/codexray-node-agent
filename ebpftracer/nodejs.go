// Copyright Codexray, Inc.
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package ebpftracer

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/cilium/ebpf/link"
	"github.com/codifinary/codexray-node-agent/proc"
	"golang.org/x/exp/maps"
	"k8s.io/klog/v2"
)

// nodejsSymCache caches the parsed addresses + return-offsets for each libuv /
// node binary the agent has seen. Without this, every new Node.js process that
// the agent attaches to triggers a full debug/elf symbol-table parse (~18 MB
// transient) — the same shape of bug as the Go-TLS path that we already fixed
// via parseSymInfo + binarySymCache.
var nodejsSymCache = newBinarySymCache(128)

// nodejsCallbackSymbols are the libuv I/O callbacks attachNodejsUprobes tries
// to instrument; one ELF parse covers all of them.
var nodejsCallbackSymbols = []string{
	"uv__io_poll",
	"uv__stream_io",
	"uv__async_io",
	"uv__poll_io",
	"uv__server_io",
	"uv__udp_io",
}

func (t *Tracer) AttachNodejsProbes(pid uint32, exe string) []link.Link {
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

	for _, libPath := range append(getLibuv(pid), proc.Path(pid, "root", exe)) {
		if links, err := t.attachNodejsUprobes(libPath, pid); err == nil {
			log(libPath, "nodejs uprobes attached", nil)
			return links
		} else {
			log(libPath, "failed to attach nodejs uprobes", err)
		}
	}
	return nil
}

func (t *Tracer) attachNodejsUprobes(libPath string, pid uint32) ([]link.Link, error) {
	// Symbol metadata: parsed once per unique (libPath, mtime, size). On a
	// cache hit we skip the ~18 MB transient debug/elf parse entirely.
	key, err := makeSymCacheKey(libPath)
	if err != nil {
		return nil, err
	}
	syms, cached := nodejsSymCache.get(key)
	if !cached {
		// uv__io_poll needs ret offsets (uretprobes); the rest only need
		// entry uprobes — but since the caller also attaches uretprobes on
		// every callback below, compute ret offsets for all of them in one
		// parse. ELF is opened, all wanted symbols extracted, then closed
		// (debug/elf working set released by deferred runtime.GC).
		needRet := map[string]bool{}
		for _, name := range nodejsCallbackSymbols {
			needRet[name] = true
		}
		syms, err = parseSymInfo(libPath, needRet, nodejsCallbackSymbols...)
		if err != nil {
			return nil, err
		}
		nodejsSymCache.put(key, syms)
	}

	pollSym, ok := syms["uv__io_poll"]
	if !ok || pollSym.address == 0 {
		return nil, fmt.Errorf("uv__io_poll not found in %s", libPath)
	}

	exe, err := link.OpenExecutable(libPath)
	if err != nil {
		return nil, err
	}

	var links []link.Link
	closeLinks := func() {
		for _, l := range links {
			_ = l.Close()
		}
	}

	// uv__io_poll: entry + every RET inside it
	l, err := exe.Uprobe("uv__io_poll", t.uprobes["uv_io_poll_enter"], &link.UprobeOptions{Address: pollSym.address, PID: int(pid)})
	if err != nil {
		return nil, err
	}
	links = append(links, l)
	for _, off := range pollSym.returnOffsets {
		l, err := exe.Uprobe("uv__io_poll", t.uprobes["uv_io_poll_exit"], &link.UprobeOptions{Address: pollSym.address, Offset: uint64(off), PID: int(pid)})
		if err != nil {
			closeLinks()
			return nil, err
		}
		links = append(links, l)
	}

	// libuv I/O callbacks: entry + RETs for each one that exists in this binary
	for _, cb := range nodejsCallbackSymbols {
		if cb == "uv__io_poll" {
			continue
		}
		cbSym, ok := syms[cb]
		if !ok || cbSym.address == 0 {
			// Not every libuv build has every callback symbol; that's
			// expected, just skip the missing ones.
			continue
		}
		l, err := exe.Uprobe(cb, t.uprobes["uv_io_cb_enter"], &link.UprobeOptions{Address: cbSym.address, PID: int(pid)})
		if err != nil {
			break
		}
		links = append(links, l)
		for _, off := range cbSym.returnOffsets {
			l, err := exe.Uprobe(cb, t.uprobes["uv_io_cb_exit"], &link.UprobeOptions{Address: cbSym.address, Offset: uint64(off), PID: int(pid)})
			if err != nil {
				closeLinks()
				return links, err
			}
			links = append(links, l)
		}
	}
	return links, nil
}

func getLibuv(pid uint32) []string {
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
		if strings.Contains(libPath, "libuv") {
			libs[proc.Path(pid, "root", libPath)] = true
		}
	}
	return maps.Keys(libs)
}
