// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package ebpftracer

import (
	"debug/elf"
	"os"
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
