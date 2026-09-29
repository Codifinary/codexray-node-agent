// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package ebpftracer

import (
	"bytes"
	"debug/elf"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/link"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type elfTestSection struct {
	name    string
	typ     elf.SectionType
	flags   elf.SectionFlag
	addr    uint64
	data    []byte
	link    uint32
	entsize uint64
}

type elfTestSym struct {
	name  string
	info  byte
	value uint64
	size  uint64
}

type elfTestSpec struct {
	machine  elf.Machine
	text     []byte // placed at textVaddr, covered by an executable PT_LOAD
	rodata   []byte
	symbols  []elfTestSym
	noPtLoad bool
}

const elfTestTextVaddr = 0x10000

// elfTestBuild writes a minimal little-endian ELF64 file and returns the file offset of .text.
func elfTestBuild(t *testing.T, path string, spec elfTestSpec) uint64 {
	t.Helper()
	le := binary.LittleEndian
	var sections []elfTestSection
	if spec.text != nil {
		sections = append(sections, elfTestSection{name: ".text", typ: elf.SHT_PROGBITS, flags: elf.SHF_ALLOC | elf.SHF_EXECINSTR, addr: elfTestTextVaddr, data: spec.text})
	}
	if spec.rodata != nil {
		sections = append(sections, elfTestSection{name: ".rodata", typ: elf.SHT_PROGBITS, flags: elf.SHF_ALLOC, data: spec.rodata})
	}
	if spec.symbols != nil {
		strtab := []byte{0}
		symtab := make([]byte, 24) // null symbol
		for _, s := range spec.symbols {
			rec := make([]byte, 24)
			le.PutUint32(rec[0:], uint32(len(strtab)))
			rec[4] = s.info
			le.PutUint16(rec[6:], 1) // shndx: .text (index 1)
			le.PutUint64(rec[8:], s.value)
			le.PutUint64(rec[16:], s.size)
			symtab = append(symtab, rec...)
			strtab = append(append(strtab, s.name...), 0)
		}
		strtabIdx := uint32(len(sections) + 2) // +1 null section, +1 symtab itself
		sections = append(sections,
			elfTestSection{name: ".symtab", typ: elf.SHT_SYMTAB, data: symtab, link: strtabIdx, entsize: 24},
			elfTestSection{name: ".strtab", typ: elf.SHT_STRTAB, data: strtab},
		)
	}
	shstrtab := []byte{0}
	nameOff := map[string]uint32{}
	for _, s := range append(sections, elfTestSection{name: ".shstrtab"}) {
		nameOff[s.name] = uint32(len(shstrtab))
		shstrtab = append(append(shstrtab, s.name...), 0)
	}
	sections = append(sections, elfTestSection{name: ".shstrtab", typ: elf.SHT_STRTAB, data: shstrtab})

	const ehSize, phSize, shSize = 64, 56, 64
	phnum := 1
	if spec.noPtLoad || spec.text == nil {
		phnum = 0
	}
	off := uint64(ehSize + phSize*phnum)
	body := &bytes.Buffer{}
	offsets := make([]uint64, len(sections))
	var textOff uint64
	for i, s := range sections {
		offsets[i] = off + uint64(body.Len())
		if s.name == ".text" {
			textOff = offsets[i]
		}
		body.Write(s.data)
	}
	shoff := off + uint64(body.Len())

	out := &bytes.Buffer{}
	ident := [16]byte{0x7f, 'E', 'L', 'F', byte(elf.ELFCLASS64), byte(elf.ELFDATA2LSB), byte(elf.EV_CURRENT)}
	out.Write(ident[:])
	hdr := make([]byte, ehSize-16)
	le.PutUint16(hdr[0:], uint16(elf.ET_DYN))
	le.PutUint16(hdr[2:], uint16(spec.machine))
	le.PutUint32(hdr[4:], uint32(elf.EV_CURRENT))
	if phnum > 0 {
		le.PutUint64(hdr[16:], ehSize) // phoff
	}
	le.PutUint64(hdr[24:], shoff)
	le.PutUint16(hdr[36:], ehSize)
	le.PutUint16(hdr[38:], phSize)
	le.PutUint16(hdr[40:], uint16(phnum))
	le.PutUint16(hdr[42:], shSize)
	le.PutUint16(hdr[44:], uint16(len(sections)+1))
	le.PutUint16(hdr[46:], uint16(len(sections))) // shstrndx: last
	out.Write(hdr)
	if phnum > 0 {
		ph := make([]byte, phSize)
		le.PutUint32(ph[0:], uint32(elf.PT_LOAD))
		le.PutUint32(ph[4:], uint32(elf.PF_R|elf.PF_X))
		le.PutUint64(ph[8:], textOff)
		le.PutUint64(ph[16:], elfTestTextVaddr)
		le.PutUint64(ph[24:], elfTestTextVaddr)
		le.PutUint64(ph[32:], uint64(len(spec.text)))
		le.PutUint64(ph[40:], uint64(len(spec.text)))
		le.PutUint64(ph[48:], 0x1000)
		out.Write(ph)
	}
	out.Write(body.Bytes())
	out.Write(make([]byte, shSize)) // null section
	for i, s := range sections {
		sh := make([]byte, shSize)
		le.PutUint32(sh[0:], nameOff[s.name])
		le.PutUint32(sh[4:], uint32(s.typ))
		le.PutUint64(sh[8:], uint64(s.flags))
		le.PutUint64(sh[16:], s.addr)
		le.PutUint64(sh[24:], offsets[i])
		le.PutUint64(sh[32:], uint64(len(s.data)))
		le.PutUint32(sh[40:], s.link)
		le.PutUint64(sh[48:], 1)
		le.PutUint64(sh[56:], s.entsize)
		out.Write(sh)
	}
	require.NoError(t, os.WriteFile(path, out.Bytes(), 0o755))
	return textOff
}

var (
	// push rbp; mov rbp,rsp; ret   /   nop; ret; nop; ret
	elfTestX86FuncA = []byte{0x55, 0x48, 0x89, 0xe5, 0xc3}
	elfTestX86FuncB = []byte{0x90, 0xc3, 0x90, 0xc3}
)

func elfTestFixture(t *testing.T) (string, uint64) {
	path := filepath.Join(t.TempDir(), "libtest.so")
	text := append(append([]byte{}, elfTestX86FuncA...), elfTestX86FuncB...)
	funcInfo := elf.ST_INFO(elf.STB_GLOBAL, elf.STT_FUNC)
	textOff := elfTestBuild(t, path, elfTestSpec{
		machine: elf.EM_X86_64,
		text:    text,
		symbols: []elfTestSym{
			{name: "zero_size", info: funcInfo, value: elfTestTextVaddr, size: 0},
			{name: "undefined", info: funcInfo, value: 0, size: 5},
			{name: "an_object", info: elf.ST_INFO(elf.STB_GLOBAL, elf.STT_OBJECT), value: elfTestTextVaddr, size: 5},
			{name: "func_a", info: funcInfo, value: elfTestTextVaddr, size: uint64(len(elfTestX86FuncA))},
			{name: "func_b", info: funcInfo, value: elfTestTextVaddr + uint64(len(elfTestX86FuncA)), size: uint64(len(elfTestX86FuncB))},
			{name: "outside_load", info: funcInfo, value: 0x90000, size: 1},
		},
	})
	return path, textOff
}

func TestELFFileSymbols(t *testing.T) {
	path, textOff := elfTestFixture(t)
	f, err := OpenELFFile(path)
	require.NoError(t, err)
	defer f.Close()

	a, err := f.GetSymbol("func_a")
	require.NoError(t, err)
	assert.Equal(t, "func_a", a.Name())
	// the uprobe address is the file offset of the function's first instruction
	assert.Equal(t, textOff, a.Address())
	assert.Equal(t, textOff, a.Address(), "cached")
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, elfTestX86FuncA, raw[a.Address():a.Address()+uint64(len(elfTestX86FuncA))])

	offs, err := a.ReturnOffsets()
	require.NoError(t, err)
	assert.Equal(t, []int{4}, offs)

	b, err := f.GetSymbol("func_b")
	require.NoError(t, err)
	assert.Equal(t, textOff+uint64(len(elfTestX86FuncA)), b.Address())
	offs, err = b.ReturnOffsets()
	require.NoError(t, err)
	assert.Equal(t, []int{1, 3}, offs)

	// symbols outside an executable PT_LOAD keep their virtual address
	o, err := f.GetSymbol("outside_load")
	require.NoError(t, err)
	assert.Equal(t, uint64(0x90000), o.Address())

	for _, name := range []string{"zero_size", "undefined", "an_object", "missing"} {
		_, err = f.GetSymbol(name)
		assert.Error(t, err, name)
	}
}

func TestELFFileErrors(t *testing.T) {
	dir := t.TempDir()

	_, err := OpenELFFile(filepath.Join(dir, "missing"))
	assert.Error(t, err)

	notElf := filepath.Join(dir, "not-elf")
	require.NoError(t, os.WriteFile(notElf, []byte("#!/bin/sh\necho hi\n"), 0o755))
	_, err = OpenELFFile(notElf)
	assert.Error(t, err)

	// stripped library: no .symtab and no .dynsym
	stripped := filepath.Join(dir, "stripped.so")
	elfTestBuild(t, stripped, elfTestSpec{machine: elf.EM_X86_64, rodata: []byte("x")})
	f, err := OpenELFFile(stripped)
	require.NoError(t, err)
	_, err = f.GetSymbol("SSL_write")
	assert.EqualError(t, err, "no symbols found")

	// a symbol whose file has no .text section
	s := &Symbol{s: &elf.Symbol{Name: "x", Value: 1, Size: 1}, f: f}
	_, err = s.ReturnOffsets()
	assert.EqualError(t, err, "no .text")
	assert.Equal(t, uint64(1), s.Address())
	require.NoError(t, f.Close())

	// a function without a RET instruction
	noRet := filepath.Join(dir, "noret.so")
	elfTestBuild(t, noRet, elfTestSpec{machine: elf.EM_X86_64, text: []byte{0x90, 0x90},
		symbols: []elfTestSym{{name: "f", info: elf.ST_INFO(elf.STB_GLOBAL, elf.STT_FUNC), value: elfTestTextVaddr, size: 2}}})
	f, err = OpenELFFile(noRet)
	require.NoError(t, err)
	defer f.Close()
	s, err = f.GetSymbol("f")
	require.NoError(t, err)
	_, err = s.ReturnOffsets()
	assert.EqualError(t, err, "no offsets found")
}

func TestGetReturnOffsets(t *testing.T) {
	assert.Equal(t, []int{4}, getReturnOffsets(elf.EM_X86_64, elfTestX86FuncA))
	assert.Equal(t, []int{1, 3}, getReturnOffsets(elf.EM_X86_64, elfTestX86FuncB))
	// undecodable bytes are skipped, not looped on forever
	assert.Equal(t, []int{2}, getReturnOffsets(elf.EM_X86_64, []byte{0x06, 0xd6, 0xc3}))
	// a multi-byte instruction containing 0xc3 is not a RET
	assert.Empty(t, getReturnOffsets(elf.EM_X86_64, []byte{0xb8, 0xc3, 0x00, 0x00, 0x00}))
	// truncated trailing instruction
	assert.Equal(t, []int{0}, getReturnOffsets(elf.EM_X86_64, []byte{0xc3, 0x48}))
	assert.Empty(t, getReturnOffsets(elf.EM_X86_64, nil))

	nop := []byte{0x1f, 0x20, 0x03, 0xd5}
	ret := []byte{0xc0, 0x03, 0x5f, 0xd6}
	arm := append(append(append(append([]byte{}, nop...), ret...), nop...), ret...)
	assert.Equal(t, []int{4, 12}, getReturnOffsets(elf.EM_AARCH64, arm))
	assert.Equal(t, []int{0}, getReturnOffsets(elf.EM_AARCH64, append(append([]byte{}, ret...), 0xc0, 0x03)), "trailing partial word")
	assert.Empty(t, getReturnOffsets(elf.EM_AARCH64, []byte{0xff, 0xff, 0xff, 0xff}))

	assert.Nil(t, getReturnOffsets(elf.EM_386, []byte{0xc3}), "unsupported arch")
}

func TestELFFileOnTestBinary(t *testing.T) {
	exe, err := os.Executable()
	require.NoError(t, err)
	f, err := OpenELFFile(exe)
	require.NoError(t, err)
	defer f.Close()
	s, err := f.GetSymbol("github.com/codifinary/codexray-node-agent/ebpftracer.getReturnOffsets")
	if err != nil {
		t.Skipf("test binary is stripped (go test links with -s -w unless built with -c): %s", err)
	}
	// the bytes at the uprobe file offset are the function's bytes in .text
	text, r, err := f.getTextSectionAndReader()
	require.NoError(t, err)
	fromText := make([]byte, 16)
	_, err = r.Seek(int64(s.s.Value-text.Addr), io.SeekStart)
	require.NoError(t, err)
	_, err = io.ReadFull(r, fromText)
	require.NoError(t, err)
	fh, err := os.Open(exe)
	require.NoError(t, err)
	defer fh.Close()
	fromFile := make([]byte, 16)
	_, err = fh.ReadAt(fromFile, int64(s.Address()))
	require.NoError(t, err)
	assert.Equal(t, fromText, fromFile)

	offs, err := s.ReturnOffsets()
	require.NoError(t, err)
	for _, o := range offs {
		assert.Less(t, o, int(s.s.Size))
	}
}

// elfTestLib writes an x86-64 shared object exporting the given functions and returns
// the uprobe address (file offset) of each of them.
func elfTestLib(t *testing.T, path string, funcs map[string][]byte) map[string]uint64 {
	t.Helper()
	var names []string
	for name := range funcs {
		names = append(names, name)
	}
	sort.Strings(names)
	var text []byte
	var syms []elfTestSym
	offs := map[string]uint64{}
	for _, name := range names {
		offs[name] = uint64(len(text))
		syms = append(syms, elfTestSym{name: name, info: elf.ST_INFO(elf.STB_GLOBAL, elf.STT_FUNC), value: elfTestTextVaddr + uint64(len(text)), size: uint64(len(funcs[name]))})
		text = append(text, funcs[name]...)
	}
	textOff := elfTestBuild(t, path, elfTestSpec{machine: elf.EM_X86_64, text: text, symbols: syms})
	for name := range offs {
		offs[name] += textOff
	}
	return offs
}

// elfTestFuncs returns funcs that all have the two RETs of elfTestX86FuncB (return offsets 1 and 3).
func elfTestFuncs(names ...string) map[string][]byte {
	res := map[string][]byte{}
	for _, n := range names {
		res[n] = elfTestX86FuncB
	}
	return res
}

type elfTestLink struct {
	link.Link
	closed int
}

func (l *elfTestLink) Close() error {
	l.closed++
	return nil
}

type elfTestUprobeCall struct {
	symbol string
	prog   *ebpf.Program
	opts   link.UprobeOptions
	link   *elfTestLink
}

type elfTestUprobes struct {
	calls []*elfTestUprobeCall
	fail  func(n int, c *elfTestUprobeCall) error
}

// elfTestFakeUprobes replaces the uprobe attach call; fail (optional) is given the 1-based call number.
func elfTestFakeUprobes(t *testing.T, fail func(n int, c *elfTestUprobeCall) error) *elfTestUprobes {
	f := &elfTestUprobes{fail: fail}
	prev := uprobe
	t.Cleanup(func() { uprobe = prev })
	uprobe = func(ex *link.Executable, symbol string, prog *ebpf.Program, opts *link.UprobeOptions) (link.Link, error) {
		require.NotNil(t, ex)
		c := &elfTestUprobeCall{symbol: symbol, prog: prog, opts: *opts}
		f.calls = append(f.calls, c)
		if f.fail != nil {
			if err := f.fail(len(f.calls), c); err != nil {
				return nil, err
			}
		}
		c.link = &elfTestLink{}
		return c.link, nil
	}
	return f
}

func elfTestFailAt(n int, err error) func(int, *elfTestUprobeCall) error {
	return func(i int, _ *elfTestUprobeCall) error {
		if i == n {
			return err
		}
		return nil
	}
}

func (f *elfTestUprobes) links() []link.Link {
	var res []link.Link
	for _, c := range f.calls {
		if c.link != nil {
			res = append(res, c.link)
		}
	}
	return res
}

func (f *elfTestUprobes) closed() []bool {
	var res []bool
	for _, c := range f.calls {
		if c.link != nil {
			res = append(res, c.link.closed > 0)
		}
	}
	return res
}

// elfTestProbe describes an expected attachment: prog name, uprobe address and return offset.
type elfTestProbe struct {
	prog   string
	addr   uint64
	offset uint64
}

func (f *elfTestUprobes) probes(progs map[*ebpf.Program]string) []elfTestProbe {
	var res []elfTestProbe
	for _, c := range f.calls {
		res = append(res, elfTestProbe{prog: progs[c.prog], addr: c.opts.Address, offset: c.opts.Offset})
	}
	return res
}

// elfTestProgs registers distinct (never loaded) programs as the tracer's uprobes and
// returns a reverse lookup for asserting which program got attached.
func elfTestProgs(tr *Tracer, names ...string) map[*ebpf.Program]string {
	res := map[*ebpf.Program]string{}
	for _, n := range names {
		p := &ebpf.Program{}
		tr.uprobes[n] = p
		res[p] = n
	}
	return res
}

func elfTestOpen(t *testing.T, path, symbol string) (*link.Executable, *Symbol) {
	exe, err := link.OpenExecutable(path)
	require.NoError(t, err)
	f, err := OpenELFFile(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = f.Close() })
	s, err := f.GetSymbol(symbol)
	require.NoError(t, err)
	return exe, s
}

func TestSymbolAttachUprobe(t *testing.T) {
	path, textOff := elfTestFixture(t)
	exe, a := elfTestOpen(t, path, "func_a")
	prog := &ebpf.Program{}

	f := elfTestFakeUprobes(t, nil)
	l, err := a.AttachUprobe(exe, prog, 42)
	require.NoError(t, err)
	require.Len(t, f.calls, 1)
	assert.Same(t, f.calls[0].link, l)
	assert.Equal(t, "func_a", f.calls[0].symbol)
	assert.Same(t, prog, f.calls[0].prog)
	assert.Equal(t, link.UprobeOptions{Address: textOff, PID: 42}, f.calls[0].opts)

	f = elfTestFakeUprobes(t, elfTestFailAt(1, errors.New("boom")))
	l, err = a.AttachUprobe(exe, prog, 42)
	assert.EqualError(t, err, "boom")
	assert.Nil(t, l)
}

func TestSymbolAttachUretprobes(t *testing.T) {
	path, textOff := elfTestFixture(t)
	exe, b := elfTestOpen(t, path, "func_b")
	prog := &ebpf.Program{}
	bAddr := textOff + uint64(len(elfTestX86FuncA))

	t.Run("a probe at every return offset", func(t *testing.T) {
		f := elfTestFakeUprobes(t, nil)
		links, err := b.AttachUretprobes(exe, prog, 7)
		require.NoError(t, err)
		assert.Equal(t, f.links(), links)
		require.Len(t, f.calls, 2)
		for i, off := range []uint64{1, 3} {
			assert.Same(t, prog, f.calls[i].prog)
			assert.Equal(t, link.UprobeOptions{Address: bAddr, Offset: off, PID: 7}, f.calls[i].opts)
		}
	})

	t.Run("partial failure returns the links attached so far", func(t *testing.T) {
		f := elfTestFakeUprobes(t, elfTestFailAt(2, errors.New("boom")))
		links, err := b.AttachUretprobes(exe, prog, 7)
		assert.EqualError(t, err, "boom")
		require.Len(t, links, 1)
		assert.Same(t, f.calls[0].link, links[0])
		assert.Equal(t, []bool{false}, f.closed(), "closing is up to the caller")
	})

	t.Run("no return offsets", func(t *testing.T) {
		noRet := filepath.Join(t.TempDir(), "noret.so")
		elfTestLib(t, noRet, map[string][]byte{"f": {0x90, 0x90}})
		exe, s := elfTestOpen(t, noRet, "f")
		f := elfTestFakeUprobes(t, nil)
		links, err := s.AttachUretprobes(exe, prog, 7)
		assert.EqualError(t, err, "no offsets found")
		assert.Nil(t, links)
		assert.Empty(t, f.calls)
	})
}

func TestSymbolReturnOffsetsReadErrors(t *testing.T) {
	path, _ := elfTestFixture(t)
	f, err := OpenELFFile(path)
	require.NoError(t, err)
	defer f.Close()

	// a symbol below .text: the start offset wraps around and seeking fails
	s := &Symbol{s: &elf.Symbol{Name: "below", Value: elfTestTextVaddr - 1, Size: 1}, f: f}
	_, err = s.ReturnOffsets()
	assert.Error(t, err)

	// a symbol past the end of .text: nothing to read
	s = &Symbol{s: &elf.Symbol{Name: "past", Value: elfTestTextVaddr + 0x1000, Size: 1}, f: f}
	_, err = s.ReturnOffsets()
	assert.ErrorIs(t, err, io.EOF)
}
