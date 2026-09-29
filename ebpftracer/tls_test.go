// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package ebpftracer

import (
	"debug/elf"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/codifinary/codexray-node-agent/proc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// tlsTestMmap maps path into this process so that it shows up in /proc/self/maps,
// which is what the library discovery helpers parse.
func tlsTestMmap(t *testing.T, path string) {
	t.Helper()
	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()
	fi, err := f.Stat()
	require.NoError(t, err)
	data, err := syscall.Mmap(int(f.Fd()), 0, int(fi.Size()), syscall.PROT_READ, syscall.MAP_PRIVATE)
	require.NoError(t, err)
	t.Cleanup(func() { _ = syscall.Munmap(data) })
}

func tlsTestSelfMaps(t *testing.T, substr string) bool {
	data, err := os.ReadFile("/proc/self/maps")
	require.NoError(t, err)
	return strings.Contains(string(data), substr)
}

func tlsTestLibs(t *testing.T, cryptoRodata []byte) (string, string) {
	dir := t.TempDir()
	ssl := filepath.Join(dir, "libssl.so.3")
	crypto := filepath.Join(dir, "libcrypto.so.3")
	elfTestBuild(t, ssl, elfTestSpec{machine: elf.EM_X86_64, rodata: []byte("ssl")})
	if cryptoRodata != nil {
		elfTestBuild(t, crypto, elfTestSpec{machine: elf.EM_X86_64, rodata: cryptoRodata})
	}
	return ssl, crypto
}

func TestGetSslLibPathAndVersion(t *testing.T) {
	if tlsTestSelfMaps(t, "libssl.so") || tlsTestSelfMaps(t, "libcrypto.so") {
		t.Skip("the test binary itself maps libssl/libcrypto")
	}
	pid := uint32(os.Getpid())

	t.Run("no libs mapped", func(t *testing.T) {
		p, v := getSslLibPathAndVersion(pid)
		assert.Equal(t, "", p)
		assert.Equal(t, "", v)
	})

	t.Run("process gone", func(t *testing.T) {
		p, v := getSslLibPathAndVersion(0x7fffffff)
		assert.Equal(t, "", p)
		assert.Equal(t, "", v)
	})

	t.Run("openssl 3", func(t *testing.T) {
		ssl, crypto := tlsTestLibs(t, []byte("\x00foo\x00OpenSSL 3.0.2 15 Mar 2022\x00bar\x00"))
		tlsTestMmap(t, ssl)
		tlsTestMmap(t, crypto)
		p, v := getSslLibPathAndVersion(pid)
		assert.Equal(t, proc.Path(pid, "root", ssl), p)
		assert.Equal(t, "v3.0.2", v)
	})

	t.Run("openssl 1.1.1 with letter suffix", func(t *testing.T) {
		ssl, crypto := tlsTestLibs(t, []byte("\x00OpenSSL 1.1.1w  11 Sep 2023\x00"))
		tlsTestMmap(t, ssl)
		tlsTestMmap(t, crypto)
		_, v := getSslLibPathAndVersion(pid)
		assert.Equal(t, "v1.1.1", v)
	})

	t.Run("libssl without libcrypto", func(t *testing.T) {
		ssl, _ := tlsTestLibs(t, nil)
		tlsTestMmap(t, ssl)
		p, v := getSslLibPathAndVersion(pid)
		assert.Equal(t, "", p)
		assert.Equal(t, "", v)
	})

	t.Run("libcrypto is not an ELF", func(t *testing.T) {
		ssl, crypto := tlsTestLibs(t, nil)
		require.NoError(t, os.WriteFile(crypto, []byte("not an elf file"), 0o644))
		tlsTestMmap(t, ssl)
		tlsTestMmap(t, crypto)
		p, v := getSslLibPathAndVersion(pid)
		assert.Equal(t, "", p)
		assert.Equal(t, "", v)
	})

	t.Run("libcrypto without rodata", func(t *testing.T) {
		ssl, crypto := tlsTestLibs(t, nil)
		elfTestBuild(t, crypto, elfTestSpec{machine: elf.EM_X86_64, text: []byte{0xc3}, noPtLoad: true})
		tlsTestMmap(t, ssl)
		tlsTestMmap(t, crypto)
		p, v := getSslLibPathAndVersion(pid)
		assert.Equal(t, "", p)
		assert.Equal(t, "", v)
	})
}

func TestGetSslLibPathAndVersionUnknownFlavor(t *testing.T) {
	// LibreSSL/BoringSSL libcrypto has no "OpenSSL x.y.z" string; the caller treats version == ""
	// as "not supported", but the function returns "v", which then fails VersionFromString and
	// is logged at error level for every such process.
	// BUG: getSslLibPathAndVersion returns version "v" instead of "" when no OpenSSL version is found — unskip when fixed
	t.Skip("BUG: getSslLibPathAndVersion returns version \"v\" instead of \"\" when no OpenSSL version is found")
	ssl, crypto := tlsTestLibs(t, []byte("\x00LibreSSL 3.8.2\x00"))
	tlsTestMmap(t, ssl)
	tlsTestMmap(t, crypto)
	_, v := getSslLibPathAndVersion(uint32(os.Getpid()))
	assert.Equal(t, "", v)
}

func TestAttachOpenSslUprobesNoSymbols(t *testing.T) {
	if tlsTestSelfMaps(t, "libssl.so") || tlsTestSelfMaps(t, "libcrypto.so") {
		t.Skip("the test binary itself maps libssl/libcrypto")
	}
	tr := NewTracer(0, 0, false)
	pid := uint32(os.Getpid())
	// no libssl -> nothing to attach
	assert.Nil(t, tr.AttachOpenSslUprobes(pid))

	// stripped libssl (no SSL_write symbol) -> nothing attached, no panic, no BPF needed
	ssl, crypto := tlsTestLibs(t, []byte("\x00OpenSSL 3.0.2 15 Mar 2022\x00"))
	tlsTestMmap(t, ssl)
	tlsTestMmap(t, crypto)
	assert.Nil(t, tr.AttachOpenSslUprobes(pid))
}

func TestAttachGoTlsUprobesOnGoBinary(t *testing.T) {
	tr := NewTracer(0, 0, false)
	// the test binary is a Go (>= 1.17) executable; the uprobe programs are not loaded,
	// so attaching must fail cleanly and nothing may leak
	links, isGo := tr.AttachGoTlsUprobes(uint32(os.Getpid()))
	assert.True(t, isGo)
	assert.Nil(t, links)

	links, isGo = tr.AttachGoTlsUprobes(0x7fffffff)
	assert.False(t, isGo, "process gone")
	assert.Nil(t, links)
}

var tlsTestSslFuncs = []string{"SSL_write", "SSL_read", "SSL_write_ex", "SSL_read_ex"}

// tlsTestSslLib maps a libssl exporting funcs and a libcrypto reporting the given OpenSSL version.
func tlsTestSslLib(t *testing.T, version string, funcs ...string) (*Tracer, map[string]uint64, func(*elfTestUprobes) []elfTestProbe) {
	if tlsTestSelfMaps(t, "libssl.so") || tlsTestSelfMaps(t, "libcrypto.so") {
		t.Skip("the test binary itself maps libssl/libcrypto")
	}
	tr := NewTracer(0, 0, false)
	var names []string
	for _, suffix := range []string{"", "_v1_1_1", "_v3_0"} {
		for _, p := range []string{"openssl_SSL_write_enter", "openssl_SSL_read_enter", "openssl_SSL_read_ex_enter"} {
			names = append(names, p+suffix)
		}
	}
	progs := elfTestProgs(tr, append(names, "openssl_SSL_read_exit")...)
	ssl, crypto := tlsTestLibs(t, []byte("\x00OpenSSL "+version+"  1 Jan 2024\x00"))
	addrs := elfTestLib(t, ssl, elfTestFuncs(funcs...))
	tlsTestMmap(t, ssl)
	tlsTestMmap(t, crypto)
	return tr, addrs, func(f *elfTestUprobes) []elfTestProbe { return f.probes(progs) }
}

func TestAttachOpenSslUprobesAttached(t *testing.T) {
	pid := uint32(os.Getpid())
	for _, c := range []struct {
		version string
		suffix  string
		ex      bool
	}{
		{version: "3.0.2", suffix: "_v3_0", ex: true},
		{version: "3.2.1", suffix: "_v3_0", ex: true},
		{version: "1.1.1w", suffix: "_v1_1_1", ex: true},
		{version: "1.1.0l", suffix: ""},
		{version: "1.0.2u", suffix: ""},
	} {
		t.Run(c.version, func(t *testing.T) {
			tr, addrs, probes := tlsTestSslLib(t, c.version, tlsTestSslFuncs...)
			f := elfTestFakeUprobes(t, nil)
			links := tr.AttachOpenSslUprobes(pid)
			w, r := addrs["SSL_write"], addrs["SSL_read"]
			expected := []elfTestProbe{
				{"openssl_SSL_write_enter" + c.suffix, w, 0},
				{"openssl_SSL_read_enter" + c.suffix, r, 0},
				{"openssl_SSL_read_exit", r, 1},
				{"openssl_SSL_read_exit", r, 3},
			}
			if c.ex {
				w, r = addrs["SSL_write_ex"], addrs["SSL_read_ex"]
				expected = append(expected,
					elfTestProbe{"openssl_SSL_write_enter" + c.suffix, w, 0},
					elfTestProbe{"openssl_SSL_read_ex_enter" + c.suffix, r, 0},
					elfTestProbe{"openssl_SSL_read_exit", r, 1},
					elfTestProbe{"openssl_SSL_read_exit", r, 3},
				)
			}
			assert.Equal(t, expected, probes(f))
			assert.Equal(t, f.links(), links)
			for _, call := range f.calls {
				assert.Equal(t, int(pid), call.opts.PID)
			}
		})
	}
}

func TestAttachOpenSslUprobesFailures(t *testing.T) {
	pid := uint32(os.Getpid())

	t.Run("missing symbol closes what is attached", func(t *testing.T) {
		tr, _, _ := tlsTestSslLib(t, "3.0.2", "SSL_write", "SSL_read")
		f := elfTestFakeUprobes(t, nil)
		assert.Nil(t, tr.AttachOpenSslUprobes(pid))
		assert.Len(t, f.calls, 4)
		assert.Equal(t, []bool{true, true, true, true}, f.closed())
	})

	t.Run("uprobe failure", func(t *testing.T) {
		tr, _, _ := tlsTestSslLib(t, "3.0.2", tlsTestSslFuncs...)
		f := elfTestFakeUprobes(t, elfTestFailAt(2, errors.New("boom")))
		assert.Nil(t, tr.AttachOpenSslUprobes(pid))
		assert.Equal(t, []bool{true}, f.closed())
	})

	t.Run("uretprobe failure", func(t *testing.T) {
		tr, _, _ := tlsTestSslLib(t, "3.0.2", tlsTestSslFuncs...)
		f := elfTestFakeUprobes(t, elfTestFailAt(4, errors.New("attach: permission denied")))
		assert.Nil(t, tr.AttachOpenSslUprobes(pid))
		assert.Equal(t, []bool{true, true, true}, f.closed())
	})

	t.Run("unknown libssl version", func(t *testing.T) {
		if tlsTestSelfMaps(t, "libssl.so") || tlsTestSelfMaps(t, "libcrypto.so") {
			t.Skip("the test binary itself maps libssl/libcrypto")
		}
		ssl, crypto := tlsTestLibs(t, []byte("\x00LibreSSL 3.8.2\x00"))
		elfTestLib(t, ssl, elfTestFuncs(tlsTestSslFuncs...))
		tlsTestMmap(t, ssl)
		tlsTestMmap(t, crypto)
		f := elfTestFakeUprobes(t, nil)
		assert.Nil(t, NewTracer(0, 0, false).AttachOpenSslUprobes(pid))
		assert.Empty(t, f.calls)
	})

	t.Run("libssl is not an ELF", func(t *testing.T) {
		if tlsTestSelfMaps(t, "libssl.so") || tlsTestSelfMaps(t, "libcrypto.so") {
			t.Skip("the test binary itself maps libssl/libcrypto")
		}
		ssl, crypto := tlsTestLibs(t, []byte("\x00OpenSSL 3.0.2  1 Jan 2024\x00"))
		require.NoError(t, os.WriteFile(ssl, []byte("not an elf file, but long enough to be mapped"), 0o644))
		tlsTestMmap(t, ssl)
		tlsTestMmap(t, crypto)
		f := elfTestFakeUprobes(t, nil)
		assert.Nil(t, NewTracer(0, 0, false).AttachOpenSslUprobes(pid))
		assert.Empty(t, f.calls)
	})
}

// tlsTestGoProgram builds and starts a Go program that links the crypto/tls.(*Conn) methods
// called in calls (on c) and keeps its symbol table (unlike the test binary itself).
func tlsTestGoProgram(t *testing.T, calls string) (uint32, string) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go toolchain is not available")
	}
	dir := t.TempDir()
	src := `package main

import (
	"crypto/tls"
	"os"
	"time"
)

func main() {
	if len(os.Args) > 1 {
		c := tls.Client(nil, &tls.Config{})
		CALLS
	}
	time.Sleep(time.Minute)
}
`
	src = strings.Replace(src, "CALLS", calls, 1)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.go"), []byte(src), 0o644))
	bin := filepath.Join(dir, "goapp")
	build := exec.Command(goBin, "build", "-o", bin, "main.go")
	build.Dir = dir
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOFLAGS=", "GOTOOLCHAIN=local", "GO111MODULE=off")
	out, err := build.CombinedOutput()
	require.NoError(t, err, string(out))
	cmd := exec.Command(bin)
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return uint32(cmd.Process.Pid), bin
}

func TestAttachGoTlsUprobesAttached(t *testing.T) {
	pid, bin := tlsTestGoProgram(t, "_, _ = c.Write(nil); _, _ = c.Read(nil)")
	tr := NewTracer(0, 0, false)
	progs := elfTestProgs(tr, "go_crypto_tls_write_enter", "go_crypto_tls_read_enter", "go_crypto_tls_read_exit")

	ef, err := OpenELFFile(bin)
	require.NoError(t, err)
	defer ef.Close()
	ws, err := ef.GetSymbol(goTlsWriteSymbol)
	require.NoError(t, err)
	rs, err := ef.GetSymbol(goTlsReadSymbol)
	require.NoError(t, err)
	retOffsets, err := rs.ReturnOffsets()
	require.NoError(t, err)
	require.NotEmpty(t, retOffsets)

	t.Run("write entry, read entry and every read return", func(t *testing.T) {
		f := elfTestFakeUprobes(t, nil)
		links, isGo := tr.AttachGoTlsUprobes(pid)
		assert.True(t, isGo)
		expected := []elfTestProbe{
			{"go_crypto_tls_write_enter", ws.Address(), 0},
			{"go_crypto_tls_read_enter", rs.Address(), 0},
		}
		for _, o := range retOffsets {
			expected = append(expected, elfTestProbe{"go_crypto_tls_read_exit", rs.Address(), uint64(o)})
		}
		assert.Equal(t, expected, f.probes(progs))
		assert.Equal(t, f.links(), links)
		for _, c := range f.calls {
			assert.Equal(t, int(pid), c.opts.PID)
		}
	})

	t.Run("write entry failure", func(t *testing.T) {
		f := elfTestFakeUprobes(t, elfTestFailAt(1, errors.New("boom")))
		links, isGo := tr.AttachGoTlsUprobes(pid)
		assert.True(t, isGo)
		assert.Nil(t, links)
		assert.Len(t, f.calls, 1)
	})

	t.Run("read entry failure closes the write probe", func(t *testing.T) {
		f := elfTestFakeUprobes(t, elfTestFailAt(2, errors.New("boom")))
		links, isGo := tr.AttachGoTlsUprobes(pid)
		assert.True(t, isGo)
		assert.Nil(t, links)
		assert.Equal(t, []bool{true}, f.closed())
	})

	t.Run("read return failure closes every link", func(t *testing.T) {
		f := elfTestFakeUprobes(t, elfTestFailAt(3, errors.New("boom")))
		links, isGo := tr.AttachGoTlsUprobes(pid)
		assert.True(t, isGo)
		assert.Nil(t, links)
		assert.Equal(t, []bool{true, true}, f.closed())
	})
}

func TestAttachGoTlsUprobesWithoutReadSymbol(t *testing.T) {
	// A Go app that only ever writes to TLS connections has no crypto/tls.(*Conn).Read (dead-code
	// eliminated). The write uprobe is already attached when the Read lookup fails, and
	// AttachGoTlsUprobes returns nil (so the caller never tracks or closes it) without calling
	// closeLinks() (tls.go:218-221), leaking the uprobe for the lifetime of the agent.
	// BUG: AttachGoTlsUprobes leaks the write uprobe when crypto/tls.(*Conn).Read is missing — unskip when fixed
	t.Skip("BUG: AttachGoTlsUprobes leaks the already attached write uprobe when crypto/tls.(*Conn).Read is missing")
	pid, _ := tlsTestGoProgram(t, "_, _ = c.Write(nil)")
	tr := NewTracer(0, 0, false)
	elfTestProgs(tr, "go_crypto_tls_write_enter", "go_crypto_tls_read_enter", "go_crypto_tls_read_exit")
	f := elfTestFakeUprobes(t, nil)
	links, isGo := tr.AttachGoTlsUprobes(pid)
	assert.True(t, isGo)
	assert.Nil(t, links)
	require.Len(t, f.calls, 1, "only the write entry probe is attached")
	assert.Equal(t, []bool{true}, f.closed())
}
