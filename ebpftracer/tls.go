// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package ebpftracer

import (
	"bufio"
	"bytes"
	"debug/buildinfo"
	"debug/elf"
	"errors"
	"io/fs"
	"os"
	"regexp"
	"strings"
	"syscall"

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
	writeEnter := "openssl_SSL_write_enter"
	readEnter := "openssl_SSL_read_enter"
	readExEnter := "openssl_SSL_read_ex_enter"
	readExit := "openssl_SSL_read_exit"
	v, err := common.VersionFromString(version)
	if err != nil {
		log("failed to determine version", err)
		return nil
	}
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

	ef, err := OpenELFFile(libPath)
	if err != nil {
		log("open elf", err)
		return nil
	}
	defer ef.Close()

	for _, p := range progs {
		s, err := ef.GetSymbol(p.symbol)
		if err != nil {
			log("failed to get symbol", err)
			closeLinks()
			return nil
		}
		if p.uprobe != "" {
			l, err := s.AttachUprobe(exe, t.uprobes[p.uprobe], pid)
			if err != nil {
				log("failed to attach uprobe", err)
				closeLinks()
				return nil
			}
			links = append(links, l)
		}
		if p.uretprobe != "" {
			ls, err := s.AttachUretprobes(exe, t.uprobes[p.uretprobe], pid)
			links = append(links, ls...)
			if err != nil {
				log("failed to attach exit uprobe", err)
				closeLinks()
				return nil
			}
		}
	}
	if len(links) > 0 {
		log("libssl uprobes attached", nil)
	}
	return links
}

// goTlsBinaryInfo is everything AttachGoTlsUprobes needs from a Go binary. Computing it
// parses the binary's full ELF symbol table (tens of MB of transient allocations for large
// binaries), so it is cached per file identity and reused for every process running the
// same binary instead of being recomputed per PID.
type goTlsBinaryInfo struct {
	isGo           bool
	name, version  string
	skipReason     string // non-empty: don't attach (unsupported version, symbols missing, ...)
	skipErr        error
	transient      bool // the result came from an I/O failure (e.g. the process exited): don't cache it
	writeAddr      uint64
	readAddr       uint64
	readRetOffsets []int
}

var goTlsBinaryCache = newFileInfoCache[*goTlsBinaryInfo](256)

// isTransientFileError reports whether err comes from accessing the file (it vanished with its
// process, permissions, I/O) rather than from its contents. Such results are not cached: the
// next process running the same binary may succeed.
func isTransientFileError(err error) bool {
	var pe *fs.PathError
	return errors.As(err, &pe) || errors.Is(err, syscall.ESRCH) || errors.Is(err, syscall.EIO)
}

func cacheableGoTlsBinaryInfo(info *goTlsBinaryInfo) bool { return info != nil && !info.transient }

func loadGoTlsBinaryInfo(path string) *goTlsBinaryInfo {
	info := &goTlsBinaryInfo{}
	bi, err := buildinfo.ReadFile(path)
	if err != nil {
		info.skipReason, info.skipErr = "failed to read build info", err
		info.transient = isTransientFileError(err)
		return info
	}
	info.isGo = true
	info.version = bi.GoVersion
	if name, err := os.Readlink(path); err == nil {
		info.name = name
	} else {
		info.name = path
	}
	v, err := common.VersionFromString(strings.Replace(bi.GoVersion, "go", "", 1))
	if err != nil {
		info.skipReason, info.skipErr = "failed to determine version", err
		return info
	}
	if !v.GreaterOrEqual(common.NewVersion(1, 17, 0)) {
		info.skipReason = "versions below 1.17 are not supported"
		return info
	}
	ef, err := OpenELFFile(path)
	if err != nil {
		info.skipReason, info.skipErr = "failed to open as elf binary", err
		info.transient = isTransientFileError(err)
		return info
	}
	defer ef.Close()
	ws, err := ef.GetSymbol(goTlsWriteSymbol)
	if err != nil {
		info.skipReason, info.skipErr = "failed to get symbol", err
		return info
	}
	rs, err := ef.GetSymbol(goTlsReadSymbol)
	if err != nil {
		info.skipReason, info.skipErr = "failed to get symbol", err
		return info
	}
	offsets, err := rs.ReturnOffsets()
	if err != nil {
		info.skipReason, info.skipErr = "failed to find read_exit offsets", err
		return info
	}
	info.writeAddr = ws.Address()
	info.readAddr = rs.Address()
	info.readRetOffsets = offsets
	return info
}

func (t *Tracer) AttachGoTlsUprobes(pid uint32) ([]link.Link, bool) {
	if t.disableL7Tracing {
		return nil, false
	}

	path := proc.Path(pid, "exe")
	info, cached := goTlsBinaryCache.getIf(path, loadGoTlsBinaryInfo, cacheableGoTlsBinaryInfo)
	if info == nil || !info.isGo {
		return nil, false
	}
	name, version := info.name, info.version
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
	if info.skipReason != "" {
		if !cached { // log once per binary, not once per process
			log(info.skipReason, info.skipErr)
		}
		return nil, true
	}

	exe, err := link.OpenExecutable(path)
	if err != nil {
		log("failed to open executable", err)
		return nil, true
	}

	var links []link.Link
	closeLinks := func() {
		for _, l := range links {
			l.Close()
		}
	}
	l, err := exe.Uprobe(goTlsWriteSymbol, t.uprobes["go_crypto_tls_write_enter"], &link.UprobeOptions{Address: info.writeAddr, PID: int(pid)})
	if err != nil {
		log("failed to attach write_enter uprobe", err)
		return nil, true
	}
	links = append(links, l)

	l, err = exe.Uprobe(goTlsReadSymbol, t.uprobes["go_crypto_tls_read_enter"], &link.UprobeOptions{Address: info.readAddr, PID: int(pid)})
	if err != nil {
		log("failed to attach read_enter uprobe", err)
		closeLinks()
		return nil, true
	}
	links = append(links, l)

	for _, offset := range info.readRetOffsets {
		l, err = exe.Uprobe("pthread_cond_timedwait", t.uprobes["go_crypto_tls_read_exit"], &link.UprobeOptions{Address: info.readAddr, Offset: uint64(offset), PID: int(pid)})
		if err != nil {
			log("failed to attach read_exit uprobe", err)
			closeLinks()
			return nil, true
		}
		links = append(links, l)
	}
	log("crypto/tls uprobes attached", nil)
	return links, true
}

// opensslVersionCache avoids reading libcrypto's .rodata (megabytes) for every process
// that maps the same library file.
// Results of transient file errors are not cached: libcrypto is shared by most processes on
// the node, so one process exiting mid-read would otherwise disable OpenSSL uprobes node-wide.
var opensslVersionCache = newFileInfoCache[opensslVersionResult](256)

type opensslVersionResult struct {
	version   string
	transient bool
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

	res, _ := opensslVersionCache.getIf(libcryptoPath, loadOpensslVersion,
		func(r opensslVersionResult) bool { return !r.transient })
	version := res.version
	if version == "" {
		return "", ""
	}
	return libsslPath, version
}

func loadOpensslVersion(libcryptoPath string) opensslVersionResult {
	v, err := readOpensslVersion(libcryptoPath)
	return opensslVersionResult{version: v, transient: err != nil && isTransientFileError(err)}
}

func readOpensslVersion(libcryptoPath string) (string, error) {
	ef, err := elf.Open(libcryptoPath)
	if err != nil {
		return "", err
	}
	defer ef.Close()
	rodataSection := ef.Section(".rodata")
	if rodataSection == nil {
		return "", nil
	}
	rodataSectionData, err := rodataSection.Data()
	if err != nil {
		return "", err
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
	return "v" + version, nil
}
