// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package ebpftracer

import (
	"debug/elf"
	"os"
	"path/filepath"
	"testing"

	"github.com/codifinary/codexray-node-agent/proc"
	"github.com/stretchr/testify/assert"
)

func TestPthreadLibRegexps(t *testing.T) {
	for _, p := range []string{"/lib/x86_64-linux-gnu/libc.so.6", "/usr/lib/libc-2.31.so"} {
		assert.True(t, libcRegexp.MatchString(p), p)
	}
	for _, p := range []string{"/lib/ld-musl-x86_64.so.1", "/lib/ld-musl-aarch64.so.1"} {
		assert.True(t, muslRegexp.MatchString(p), p)
	}
	for _, p := range []string{"/usr/lib/libcrypto.so.3", "/usr/lib/libcap.so.2", "/usr/lib/libcurl.so.4"} {
		assert.False(t, libcRegexp.MatchString(p) || muslRegexp.MatchString(p), p)
	}
}

func TestGetPthreadLibs(t *testing.T) {
	pid := uint32(os.Getpid())
	assert.Nil(t, getPthreadLibs(0x7fffffff), "process gone")

	lib := filepath.Join(t.TempDir(), "libpthread.so.0")
	elfTestBuild(t, lib, elfTestSpec{machine: elf.EM_X86_64, rodata: []byte("p")})
	tlsTestMmap(t, lib)
	other := filepath.Join(t.TempDir(), "libcrypto.so.3")
	elfTestBuild(t, other, elfTestSpec{machine: elf.EM_X86_64, rodata: []byte("c")})
	tlsTestMmap(t, other)

	libs := getPthreadLibs(pid)
	assert.Contains(t, libs, proc.Path(pid, "root", lib))
	assert.NotContains(t, libs, proc.Path(pid, "root", other))
}

func TestAttachPythonThreadLockProbesWithoutPrograms(t *testing.T) {
	// uprobe programs are not loaded (no BPF): every candidate lib must fail cleanly
	tr := NewTracer(0, 0, false)
	lib := filepath.Join(t.TempDir(), "libpthread.so.0")
	elfTestBuild(t, lib, elfTestSpec{machine: elf.EM_X86_64, rodata: []byte("p")})
	tlsTestMmap(t, lib)
	assert.Empty(t, tr.AttachPythonThreadLockProbes(uint32(os.Getpid())))
	assert.Empty(t, tr.AttachPythonThreadLockProbes(0x7fffffff))
}
