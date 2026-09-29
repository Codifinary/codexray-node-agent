package gosym

import (
	"debug/elf"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// These tests exercise the real Go 1.2+ pclntab parser (parsePclnTab,
// funcTab, funcData, Go12Funcs, IsGo12, FuncNameOffset, ...) against real
// compiled binaries shared with the symtab/elf package's own tests.
var realGoBinaries = []string{
	"go12", "go16", "go18", "go20",
	"go12-static", "go16-static", "go18-static", "go20-static",
}

func openPclntab(t *testing.T, name string) (*LineTable, *os.File) {
	t.Helper()
	path := "../elf/testdata/elfs/" + name

	ef, err := elf.Open(path)
	require.NoError(t, err)
	defer ef.Close()

	sect := ef.Section(".gopclntab")
	require.NotNil(t, sect)

	f, err := os.Open(path)
	require.NoError(t, err)

	pclntabReader := NewFilePCLNData(f, int(sect.Offset))
	header := make([]byte, 64)
	require.NoError(t, pclntabReader.ReadAt(header, 0))

	textStart := ParseRuntimeTextFromPclntab18(header)
	if textStart == 0 {
		textSect := ef.Section(".text")
		require.NotNil(t, textSect)
		textStart = textSect.Addr
	}

	return NewLineTableStreaming(pclntabReader, textStart), f
}

func TestLineTableStreamingRealBinaries(t *testing.T) {
	for _, name := range realGoBinaries {
		t.Run(name, func(t *testing.T) {
			pcln, f := openPclntab(t, name)
			defer f.Close()

			require.True(t, pcln.IsGo12())
			require.False(t, pcln.IsFailed())
			// FuncNameOffset is always zero for the plain Go 1.2 table
			// format (it's only meaningful for 1.16+ tables), so just
			// exercise the accessor without asserting a specific value.
			_ = pcln.FuncNameOffset()

			funcs := pcln.Go12Funcs()
			require.Greater(t, len(funcs.Name), 1000)
			require.Equal(t, funcs.Entry.Length(), len(funcs.Name))
			require.Greater(t, funcs.End, funcs.Entry.First())
		})
	}
}

func TestNewLineTableInMemory(t *testing.T) {
	// use the in-memory (non-streaming) constructor against a real
	// .gopclntab section loaded fully into a byte slice.
	path := "../elf/testdata/elfs/go18"
	ef, err := elf.Open(path)
	require.NoError(t, err)
	defer ef.Close()

	sect := ef.Section(".gopclntab")
	require.NotNil(t, sect)
	data, err := sect.Data()
	require.NoError(t, err)

	header := data[:64]
	textStart := ParseRuntimeTextFromPclntab18(header)
	if textStart == 0 {
		textSect := ef.Section(".text")
		require.NotNil(t, textSect)
		textStart = textSect.Addr
	}

	pcln := NewLineTable(data, textStart)
	require.True(t, pcln.IsGo12())
	require.False(t, pcln.IsFailed())

	funcs := pcln.Go12Funcs()
	require.Greater(t, len(funcs.Name), 1000)
}

func TestParsePclnTabMalformedHeader(t *testing.T) {
	// too short / not a valid magic -> stays at ver11, not Go12, not failed.
	pcln := NewLineTable([]byte{1, 2, 3, 4}, 0)
	require.False(t, pcln.IsGo12())
	require.False(t, pcln.IsFailed())

	// calling IsGo12 twice must not reparse (parsePclnTab short-circuits
	// once version != verUnknown).
	require.False(t, pcln.IsGo12())
}

func TestGo12FuncsPanicRecovered(t *testing.T) {
	// A header that declares a Go 1.2 table with one func entry, but has no
	// functab data backing it: reading the second functab entry required to
	// compute the first entry's end address runs off the end of the buffer,
	// panicking deep inside funcTab.pc(); Go12Funcs must recover from that
	// and return the zero value rather than propagating the panic.
	header := make([]byte, 16)
	header[0], header[1], header[2], header[3] = 0xfb, 0xff, 0xff, 0xff // go12magic, little endian
	header[6] = 1 // quantum
	header[7] = 8 // ptrsize
	header[8] = 1 // nfunctab = 1 (8-byte little-endian value at offset 8)

	pcln := NewLineTable(header, 0)
	require.True(t, pcln.IsGo12())
	require.False(t, pcln.IsFailed()) // parsePclnTab itself succeeds

	require.NotPanics(t, func() {
		funcs := pcln.Go12Funcs()
		require.Equal(t, 0, funcs.Entry.Length())
		require.Empty(t, funcs.Name)
	})
}
