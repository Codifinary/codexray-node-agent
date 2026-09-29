package elf

import (
	"bytes"
	"debug/elf"
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/require"
)

// bytesReaderAt adapts a byte slice to io.ReaderAt for use as the
// InMemElfFile's raw section reader in unit tests that don't want to parse a
// full ELF32 binary from scratch.
type bytesReaderAt struct {
	data []byte
}

func (b *bytesReaderAt) ReadAt(p []byte, off int64) (int, error) {
	return bytes.NewReader(b.data).ReadAt(p, off)
}

// buildSym32 builds a single 16-byte Elf32_Sym record as used by getSymbols32.
func buildSym32(name, value uint32, info byte) []byte {
	buf := make([]byte, elf.Sym32Size)
	binary.LittleEndian.PutUint32(buf[0:4], name)
	binary.LittleEndian.PutUint32(buf[4:8], value)
	// Size field (buf[8:12]) is unused by getSymbols32.
	buf[12] = info
	return buf
}

func newFake32ElfFile(symtabData []byte, typ elf.SectionType) *InMemElfFile {
	return &InMemElfFile{
		FileHeader: elf.FileHeader{
			Class:     elf.ELFCLASS32,
			ByteOrder: binary.LittleEndian,
		},
		Sections: []elf.SectionHeader{
			{Type: typ, Offset: 0, Size: uint64(len(symtabData)), Link: 7},
		},
		reader: &bytesReaderAt{data: symtabData},
	}
}

func TestGetSymbols32(t *testing.T) {
	// entry 0 is the mandatory all-zero entry, skipped by getSymbols32.
	zero := make([]byte, elf.Sym32Size)
	fn := buildSym32(0x10, 0x2000, byte(elf.STT_FUNC))
	notFn := buildSym32(0x20, 0x3000, byte(elf.STT_OBJECT))
	data := append(append(zero, fn...), notFn...)

	f := newFake32ElfFile(data, elf.SHT_SYMTAB)
	syms, link, err := f.getSymbols32(elf.SHT_SYMTAB, &SymbolsOptions{})
	require.NoError(t, err)
	require.Equal(t, uint32(7), link)
	require.Len(t, syms, 1)
	require.Equal(t, uint32(0x10), syms[0].Name.NameIndex())
	require.Equal(t, sectionTypeSym, syms[0].Name.LinkIndex())
}

func TestGetSymbols32DynSym(t *testing.T) {
	zero := make([]byte, elf.Sym32Size)
	fn := buildSym32(0x30, 0x4000, byte(elf.STT_FUNC))
	data := append(zero, fn...)

	f := newFake32ElfFile(data, elf.SHT_DYNSYM)
	syms, _, err := f.getSymbols32(elf.SHT_DYNSYM, &SymbolsOptions{})
	require.NoError(t, err)
	require.Len(t, syms, 1)
	require.Equal(t, sectionTypeDynSym, syms[0].Name.LinkIndex())
}

func TestGetSymbols32FilterRange(t *testing.T) {
	zero := make([]byte, elf.Sym32Size)
	fn := buildSym32(0x10, 0x2000, byte(elf.STT_FUNC))
	data := append(zero, fn...)

	f := newFake32ElfFile(data, elf.SHT_SYMTAB)
	syms, _, err := f.getSymbols32(elf.SHT_SYMTAB, &SymbolsOptions{FilterFrom: 0x1000, FilterTo: 0x3000})
	require.NoError(t, err)
	require.Len(t, syms, 0)
}

func TestGetSymbols32NoSection(t *testing.T) {
	f := &InMemElfFile{FileHeader: elf.FileHeader{Class: elf.ELFCLASS32, ByteOrder: binary.LittleEndian}}
	_, _, err := f.getSymbols32(elf.SHT_SYMTAB, &SymbolsOptions{})
	require.ErrorIs(t, err, ErrNoSymbols)
}

func TestGetSymbols32BadLength(t *testing.T) {
	f := newFake32ElfFile(make([]byte, 5), elf.SHT_SYMTAB)
	_, _, err := f.getSymbols32(elf.SHT_SYMTAB, &SymbolsOptions{})
	require.Error(t, err)
}

func TestGetSymbolsDispatchesOn32BitClass(t *testing.T) {
	zero := make([]byte, elf.Sym32Size)
	fn := buildSym32(0x10, 0x2000, byte(elf.STT_FUNC))
	data := append(zero, fn...)

	f := newFake32ElfFile(data, elf.SHT_SYMTAB)
	syms, _, err := f.getSymbols(elf.SHT_SYMTAB, &SymbolsOptions{})
	require.NoError(t, err)
	require.Len(t, syms, 1)
}
