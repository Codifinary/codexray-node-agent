// Copyright Codexray, Inc.
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package ebpftracer

import (
	"bufio"
	"bytes"
	"debug/buildinfo"
	"debug/elf"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/cilium/ebpf/link"
	"github.com/codifinary/codexray-node-agent/common"
	"github.com/codifinary/codexray-node-agent/proc"
	"k8s.io/klog/v2"
)

const (
	goTlsWriteSymbol = "crypto/tls.(*Conn).Write"
	goTlsReadSymbol  = "crypto/tls.(*Conn).Read"
)

var (
	opensslVersionRe = regexp.MustCompile(`OpenSSL\s(\d\.\d+\.\d+)`)
)

func (t *Tracer) AttachOpenSslUprobes(pid uint32) []link.Link {
	if t.disableL7Tracing {
		return nil
	}
	libPath, version := getSslLibPathAndVersion(pid)
	if libPath == "" || version == "" {
		return nil
	}

	log := func(msg string, err error) {
		if err != nil {
			for _, s := range []string{"no such file or directory", "no such process", "permission denied"} {
				if strings.HasSuffix(err.Error(), s) {
					return
				}
			}
			klog.ErrorfDepth(1, "pid=%d libssl_version=%s: %s: %s", pid, version, msg, err)
			return
		}
		klog.InfofDepth(1, "pid=%d libssl_version=%s: %s", pid, version, msg)
	}

	v, err := common.VersionFromString(version)
	if err != nil {
		log("failed to determine version", err)
		return nil
	}

	writeEnter := "openssl_SSL_write_enter"
	readEnter := "openssl_SSL_read_enter"
	readExEnter := "openssl_SSL_read_ex_enter"
	readExit := "openssl_SSL_read_exit"
	switch {
	case v.GreaterOrEqual(common.NewVersion(3, 0, 0)):
		writeEnter = "openssl_SSL_write_enter_v3_0"
		readEnter = "openssl_SSL_read_enter_v3_0"
		readExEnter = "openssl_SSL_read_ex_enter_v3_0"
	case v.GreaterOrEqual(common.NewVersion(1, 1, 1)):
		writeEnter = "openssl_SSL_write_enter_v1_1_1"
		readEnter = "openssl_SSL_read_enter_v1_1_1"
		readExEnter = "openssl_SSL_read_ex_enter_v1_1_1"
	}

	type prog struct {
		symbol    string
		uprobe    string
		uretprobe string
	}
	progs := []prog{
		{symbol: "SSL_write", uprobe: writeEnter},
		{symbol: "SSL_read", uprobe: readEnter},
		{symbol: "SSL_read", uretprobe: readExit},
	}
	if v.GreaterOrEqual(common.NewVersion(1, 1, 1)) {
		progs = append(progs, []prog{
			{symbol: "SSL_write_ex", uprobe: writeEnter},
			{symbol: "SSL_read_ex", uprobe: readExEnter},
			{symbol: "SSL_read_ex", uretprobe: readExit},
		}...)
	}

	// Collect the set of symbol names we need + the subset that requires
	// return offsets (uretprobes). One ELF parse per unique libssl.so.
	needNames := map[string]struct{}{}
	needRet := map[string]bool{}
	for _, p := range progs {
		needNames[p.symbol] = struct{}{}
		if p.uretprobe != "" {
			needRet[p.symbol] = true
		}
	}
	symNames := make([]string, 0, len(needNames))
	for n := range needNames {
		symNames = append(symNames, n)
	}

	key, err := makeSymCacheKey(libPath)
	if err != nil {
		log("failed to stat libssl", err)
		return nil
	}
	syms, ok := opensslSymCache.get(key)
	if !ok {
		syms, err = parseSymInfo(libPath, needRet, symNames...)
		if err != nil {
			log("failed to parse symbols", err)
			return nil
		}
		opensslSymCache.put(key, syms)
		log(fmt.Sprintf("parsed libssl symbols (%d cached, peak parse done once)", len(syms)), nil)
	}

	exe, err := link.OpenExecutable(libPath)
	if err != nil {
		log("failed to open executable", err)
		return nil
	}
	var links []link.Link
	closeLinks := func() {
		for _, l := range links {
			l.Close()
		}
	}

	for _, p := range progs {
		s, ok := syms[p.symbol]
		if !ok || s.address == 0 {
			log("failed to get symbol", fmt.Errorf("missing %s", p.symbol))
			closeLinks()
			return nil
		}
		if p.uprobe != "" {
			l, err := exe.Uprobe(p.symbol, t.uprobes[p.uprobe], &link.UprobeOptions{Address: s.address, PID: int(pid)})
			if err != nil {
				log("failed to attach uprobe", err)
				closeLinks()
				return nil
			}
			links = append(links, l)
		}
		if p.uretprobe != "" {
			for _, off := range s.returnOffsets {
				l, err := exe.Uprobe(p.symbol, t.uprobes[p.uretprobe], &link.UprobeOptions{Address: s.address, Offset: uint64(off), PID: int(pid)})
				if err != nil {
					log("failed to attach exit uprobe", err)
					closeLinks()
					return nil
				}
				links = append(links, l)
			}
		}
	}
	if len(links) > 0 {
		log("libssl uprobes attached", nil)
	}
	return links
}

func (t *Tracer) AttachGoTlsUprobes(pid uint32) ([]link.Link, bool) {
	isGolangApp := false
	if t.disableL7Tracing {
		return nil, isGolangApp
	}

	path := proc.Path(pid, "exe")

	var err error
	var name, version string
	log := func(msg string, err error) {
		if err != nil {
			for _, s := range []string{"not a Go executable", "no such file or directory", "no such process", "permission denied"} {
				if strings.HasSuffix(err.Error(), s) {
					return
				}
			}
			klog.ErrorfDepth(1, "pid=%d golang_app=%s golang_version=%s: %s: %s", pid, name, version, msg, err)
			return
		}
		klog.InfofDepth(1, "pid=%d golang_app=%s golang_version=%s: %s", pid, name, version, msg)
	}

	bi, err := buildinfo.ReadFile(path)
	if err != nil {
		log("failed to read build info", err)
		return nil, isGolangApp
	}
	isGolangApp = true

	name, err = os.Readlink(path)
	if err != nil {
		log("failed to read name", err)
		return nil, isGolangApp
	}
	version = bi.GoVersion
	v, err := common.VersionFromString(strings.Replace(bi.GoVersion, "go", "", 1))
	if err != nil {
		log("failed to determine version", err)
	}
	if !v.GreaterOrEqual(common.NewVersion(1, 17, 0)) {
		log("versions below 1.17 are not supported", nil)
		return nil, isGolangApp
	}

	// Symbol metadata: parsed once per unique (path, mtime, size). On cache
	// hit we avoid the ~50-70 MB transient allocation inside debug/elf.
	key, err := makeSymCacheKey(path)
	if err != nil {
		log("failed to stat binary", err)
		return nil, isGolangApp
	}
	syms, cached := goTlsSymCache.get(key)
	if !cached {
		syms, err = parseSymInfo(path, map[string]bool{goTlsReadSymbol: true}, goTlsWriteSymbol, goTlsReadSymbol)
		if err != nil {
			log("failed to read symbols", err)
			return nil, isGolangApp
		}
		goTlsSymCache.put(key, syms)
		log(fmt.Sprintf("parsed Go-TLS symbols (cached for future attaches; %d entries)", len(syms)), nil)
	}
	write, wok := syms[goTlsWriteSymbol]
	read, rok := syms[goTlsReadSymbol]
	if !wok || !rok || write.address == 0 || read.address == 0 {
		log("failed to get symbol", fmt.Errorf("missing crypto/tls symbols"))
		return nil, isGolangApp
	}

	exe, err := link.OpenExecutable(path)
	if err != nil {
		log("failed to open executable", err)
		return nil, isGolangApp
	}

	var links []link.Link
	closeLinks := func() {
		for _, l := range links {
			l.Close()
		}
	}

	l, err := exe.Uprobe(goTlsWriteSymbol, t.uprobes["go_crypto_tls_write_enter"], &link.UprobeOptions{Address: write.address, PID: int(pid)})
	if err != nil {
		log("failed to attach write_enter uprobe", err)
		return nil, isGolangApp
	}
	links = append(links, l)

	l, err = exe.Uprobe(goTlsReadSymbol, t.uprobes["go_crypto_tls_read_enter"], &link.UprobeOptions{Address: read.address, PID: int(pid)})
	if err != nil {
		log("failed to attach read_enter uprobe", err)
		closeLinks()
		return nil, isGolangApp
	}
	links = append(links, l)

	for _, off := range read.returnOffsets {
		l, err := exe.Uprobe(goTlsReadSymbol, t.uprobes["go_crypto_tls_read_exit"], &link.UprobeOptions{Address: read.address, Offset: uint64(off), PID: int(pid)})
		if err != nil {
			log("failed to attach read_exit uprobe", err)
			closeLinks()
			return nil, isGolangApp
		}
		links = append(links, l)
	}
	if len(links) == 0 {
		return nil, isGolangApp
	}
	log("crypto/tls uprobes attached", nil)
	return links, isGolangApp
}

func getSslLibPathAndVersion(pid uint32) (string, string) {
	f, err := os.Open(proc.Path(pid, "maps"))
	if err != nil {
		return "", ""
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Split(bufio.ScanLines)
	var libsslPath, libcryptoPath string
	for scanner.Scan() {
		parts := strings.Fields(scanner.Text())
		if len(parts) <= 5 {
			continue
		}
		libPath := parts[5]
		switch {
		case libsslPath == "" && strings.Contains(libPath, "libssl.so"):
			fullPath := proc.Path(pid, "root", libPath)
			if _, err = os.Stat(fullPath); err == nil {
				libsslPath = fullPath
			}
		case libcryptoPath == "" && strings.Contains(libPath, "libcrypto.so"):
			fullPath := proc.Path(pid, "root", libPath)
			if _, err = os.Stat(fullPath); err == nil {
				libcryptoPath = fullPath
			}
		default:
			continue
		}
		if libsslPath != "" && libcryptoPath != "" {
			break
		}
	}
	if libsslPath == "" || libcryptoPath == "" {
		return "", ""
	}

	ef, err := elf.Open(libcryptoPath)
	if err != nil {
		return "", ""
	}
	defer ef.Close()
	rodataSection := ef.Section(".rodata")
	if rodataSection == nil {
		return "", ""
	}
	rodataSectionData, err := rodataSection.Data()
	if err != nil {
		return "", ""
	}
	var version string
	for _, b := range bytes.Split(rodataSectionData, []byte("\x00")) {
		if len(b) == 0 {
			continue
		}
		s := string(b)
		if !strings.HasPrefix(s, "OpenSSL") {
			continue
		}
		if m := opensslVersionRe.FindStringSubmatch(s); len(m) > 1 {
			version = m[1]
		}
	}
	return libsslPath, "v" + version
}
