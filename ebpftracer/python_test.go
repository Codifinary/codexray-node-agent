// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package ebpftracer

import (
	"debug/elf"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/codifinary/codexray-node-agent/proc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func pythonTestTracer(t *testing.T, funcs map[string][]byte) (*Tracer, string, map[string]uint64, func(*elfTestUprobes) []elfTestProbe) {
	tr := NewTracer(0, 0, false)
	progs := elfTestProgs(tr, "pthread_cond_timedwait_enter", "pthread_cond_timedwait_exit")
	lib := filepath.Join(t.TempDir(), "libpthread.so.0")
	addrs := elfTestLib(t, lib, funcs)
	return tr, lib, addrs, func(f *elfTestUprobes) []elfTestProbe { return f.probes(progs) }
}

func TestAttachPythonUprobes(t *testing.T) {
	pid := uint32(os.Getpid())
	funcs := elfTestFuncs("pthread_cond_timedwait")

	t.Run("entry and every return", func(t *testing.T) {
		tr, lib, addrs, probes := pythonTestTracer(t, funcs)
		f := elfTestFakeUprobes(t, nil)
		links, err := tr.attachPythonUprobes(lib, pid)
		require.NoError(t, err)
		a := addrs["pthread_cond_timedwait"]
		assert.Equal(t, []elfTestProbe{
			{"pthread_cond_timedwait_enter", a, 0},
			{"pthread_cond_timedwait_exit", a, 1},
			{"pthread_cond_timedwait_exit", a, 3},
		}, probes(f))
		assert.Equal(t, f.links(), links)
		for _, c := range f.calls {
			assert.Equal(t, int(pid), c.opts.PID)
		}
	})

	t.Run("entry failure", func(t *testing.T) {
		tr, lib, _, _ := pythonTestTracer(t, funcs)
		f := elfTestFakeUprobes(t, elfTestFailAt(1, errors.New("boom")))
		links, err := tr.attachPythonUprobes(lib, pid)
		assert.EqualError(t, err, "boom")
		assert.Nil(t, links)
		assert.Len(t, f.calls, 1)
	})

	t.Run("return failure closes every link", func(t *testing.T) {
		tr, lib, _, _ := pythonTestTracer(t, funcs)
		f := elfTestFakeUprobes(t, elfTestFailAt(3, errors.New("boom")))
		links, err := tr.attachPythonUprobes(lib, pid)
		assert.EqualError(t, err, "boom")
		assert.Nil(t, links)
		assert.Equal(t, []bool{true, true}, f.closed())
	})

	t.Run("no symbol", func(t *testing.T) {
		tr, lib, _, _ := pythonTestTracer(t, elfTestFuncs("pthread_mutex_lock"))
		f := elfTestFakeUprobes(t, nil)
		_, err := tr.attachPythonUprobes(lib, pid)
		assert.EqualError(t, err, "symbol pthread_cond_timedwait not found")
		assert.Empty(t, f.calls)
	})

	t.Run("not an executable", func(t *testing.T) {
		tr := NewTracer(0, 0, false)
		_, err := tr.attachPythonUprobes(filepath.Join(t.TempDir(), "missing.so"), pid)
		assert.Error(t, err)
	})
}

func TestAttachPythonThreadLockProbesAttached(t *testing.T) {
	pid := uint32(os.Getpid())
	tr, lib, _, _ := pythonTestTracer(t, elfTestFuncs("pthread_cond_timedwait"))
	tlsTestMmap(t, lib)
	// the test binary may also map the real libc (e.g. with -race); whichever pthread lib
	// comes first gets the probes, so only the shape of the result is asserted
	f := elfTestFakeUprobes(t, nil)
	links := tr.AttachPythonThreadLockProbes(pid)
	assert.GreaterOrEqual(t, len(links), 2)
	assert.Equal(t, f.links(), links)

	for _, err := range []error{errors.New("open: permission denied"), errors.New("boom")} {
		elfTestFakeUprobes(t, func(int, *elfTestUprobeCall) error { return err })
		assert.Empty(t, tr.AttachPythonThreadLockProbes(pid))
	}
}
