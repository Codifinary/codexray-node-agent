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

func TestGetLibuv(t *testing.T) {
	pid := uint32(os.Getpid())
	assert.Nil(t, getLibuv(0x7fffffff), "process gone")

	lib := filepath.Join(t.TempDir(), "libuv.so.1.0.0")
	elfTestBuild(t, lib, elfTestSpec{machine: elf.EM_X86_64, rodata: []byte("uv")})
	tlsTestMmap(t, lib)
	libs := getLibuv(pid)
	assert.Contains(t, libs, proc.Path(pid, "root", lib))
	for _, l := range libs {
		assert.Contains(t, l, "libuv")
	}
}

func TestAttachNodejsProbesWithoutSymbols(t *testing.T) {
	tr := NewTracer(0, 0, false)
	pid := uint32(os.Getpid())
	lib := filepath.Join(t.TempDir(), "libuv.so.1")
	elfTestBuild(t, lib, elfTestSpec{machine: elf.EM_X86_64, rodata: []byte("uv")})
	tlsTestMmap(t, lib)
	// neither the stripped libuv nor a non-existent exe has uv__io_poll -> nothing attached
	assert.Nil(t, tr.AttachNodejsProbes(pid, "/nonexistent/node"))
}
