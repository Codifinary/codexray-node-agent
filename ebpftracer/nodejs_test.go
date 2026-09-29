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

var nodejsTestCallbacks = []string{"uv__stream_io", "uv__async_io", "uv__poll_io", "uv__server_io", "uv__udp_io"}

func nodejsTestTracer(t *testing.T, funcs map[string][]byte) (*Tracer, string, map[string]uint64, func(*elfTestUprobes) []elfTestProbe) {
	tr := NewTracer(0, 0, false)
	progs := elfTestProgs(tr, "uv_io_poll_enter", "uv_io_poll_exit", "uv_io_cb_enter", "uv_io_cb_exit")
	lib := filepath.Join(t.TempDir(), "libuv.so.1")
	addrs := elfTestLib(t, lib, funcs)
	return tr, lib, addrs, func(f *elfTestUprobes) []elfTestProbe { return f.probes(progs) }
}

// nodejsTestExpected is the attach sequence for uv__io_poll followed by the given callbacks.
func nodejsTestExpected(addrs map[string]uint64, callbacks ...string) []elfTestProbe {
	a := addrs["uv__io_poll"]
	res := []elfTestProbe{{"uv_io_poll_enter", a, 0}, {"uv_io_poll_exit", a, 1}, {"uv_io_poll_exit", a, 3}}
	for _, cb := range callbacks {
		a = addrs[cb]
		res = append(res, elfTestProbe{"uv_io_cb_enter", a, 0}, elfTestProbe{"uv_io_cb_exit", a, 1}, elfTestProbe{"uv_io_cb_exit", a, 3})
	}
	return res
}

func TestAttachNodejsUprobes(t *testing.T) {
	pid := uint32(os.Getpid())
	all := elfTestFuncs(append([]string{"uv__io_poll"}, nodejsTestCallbacks...)...)

	t.Run("event loop and every io callback", func(t *testing.T) {
		tr, lib, addrs, probes := nodejsTestTracer(t, all)
		f := elfTestFakeUprobes(t, nil)
		links, err := tr.attachNodejsUprobes(lib, pid)
		require.NoError(t, err)
		assert.Equal(t, nodejsTestExpected(addrs, nodejsTestCallbacks...), probes(f))
		assert.Equal(t, f.links(), links)
		for _, c := range f.calls {
			assert.Equal(t, int(pid), c.opts.PID)
		}
	})

	t.Run("callbacks stop at the first missing symbol", func(t *testing.T) {
		funcs := elfTestFuncs("uv__io_poll", "uv__stream_io", "uv__async_io", "uv__server_io", "uv__udp_io")
		tr, lib, addrs, probes := nodejsTestTracer(t, funcs)
		f := elfTestFakeUprobes(t, nil)
		links, err := tr.attachNodejsUprobes(lib, pid)
		require.NoError(t, err)
		assert.Equal(t, nodejsTestExpected(addrs, "uv__stream_io", "uv__async_io"), probes(f))
		assert.Len(t, links, 9)
	})

	t.Run("callback attach failures keep what is attached", func(t *testing.T) {
		tr, lib, addrs, probes := nodejsTestTracer(t, all)
		// call 7 = uv__async_io entry
		f := elfTestFakeUprobes(t, elfTestFailAt(7, errors.New("boom")))
		links, err := tr.attachNodejsUprobes(lib, pid)
		require.NoError(t, err)
		assert.Equal(t, append(nodejsTestExpected(addrs, "uv__stream_io"), elfTestProbe{"uv_io_cb_enter", addrs["uv__async_io"], 0}), probes(f))
		assert.Equal(t, f.links(), links)
		assert.Len(t, links, 6)
		assert.NotContains(t, f.closed(), true)

		// call 6 = second uv__stream_io return
		f = elfTestFakeUprobes(t, elfTestFailAt(6, errors.New("boom")))
		links, err = tr.attachNodejsUprobes(lib, pid)
		require.NoError(t, err)
		assert.Len(t, f.calls, 6)
		assert.Equal(t, f.links(), links)
		assert.Len(t, links, 5)
		assert.NotContains(t, f.closed(), true)
	})

	t.Run("event loop enter failure", func(t *testing.T) {
		tr, lib, _, _ := nodejsTestTracer(t, all)
		f := elfTestFakeUprobes(t, elfTestFailAt(1, errors.New("boom")))
		links, err := tr.attachNodejsUprobes(lib, pid)
		assert.EqualError(t, err, "boom")
		assert.Nil(t, links)
		assert.Len(t, f.calls, 1)
	})

	t.Run("event loop exit failure closes every link", func(t *testing.T) {
		tr, lib, _, _ := nodejsTestTracer(t, all)
		f := elfTestFakeUprobes(t, elfTestFailAt(3, errors.New("boom")))
		links, err := tr.attachNodejsUprobes(lib, pid)
		assert.EqualError(t, err, "boom")
		assert.Nil(t, links)
		assert.Equal(t, []bool{true, true}, f.closed())
	})

	t.Run("event loop without a return instruction", func(t *testing.T) {
		tr, lib, _, _ := nodejsTestTracer(t, map[string][]byte{"uv__io_poll": {0x90, 0x90}})
		f := elfTestFakeUprobes(t, nil)
		links, err := tr.attachNodejsUprobes(lib, pid)
		assert.EqualError(t, err, "no offsets found")
		assert.Nil(t, links)
		assert.Equal(t, []bool{true}, f.closed())
	})

	t.Run("no event loop symbol", func(t *testing.T) {
		tr, lib, _, _ := nodejsTestTracer(t, elfTestFuncs("uv__stream_io"))
		f := elfTestFakeUprobes(t, nil)
		_, err := tr.attachNodejsUprobes(lib, pid)
		assert.EqualError(t, err, "symbol uv__io_poll not found")
		assert.Empty(t, f.calls)
	})
}

func TestAttachNodejsProbesAttached(t *testing.T) {
	pid := uint32(os.Getpid())
	tr, exe, _, _ := nodejsTestTracer(t, elfTestFuncs("uv__io_poll"))
	f := elfTestFakeUprobes(t, nil)
	links := tr.AttachNodejsProbes(pid, exe)
	assert.Len(t, links, 3)
	assert.Equal(t, f.links(), links)

	// every candidate fails: quiet (permission denied) and logged errors alike yield nothing
	for _, err := range []error{errors.New("open: permission denied"), errors.New("boom")} {
		elfTestFakeUprobes(t, func(int, *elfTestUprobeCall) error { return err })
		assert.Nil(t, tr.AttachNodejsProbes(pid, exe))
	}
}
