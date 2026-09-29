package elf

import (
	"debug/elf"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSymbolTableBasics(t *testing.T) {
	me, err := NewMMapedElfFile("./testdata/elfs/elf")
	require.NoError(t, err)
	defer me.Close()

	st, err := me.NewSymbolTable(new(SymbolsOptions))
	require.NoError(t, err)

	require.False(t, st.IsDead())
	require.Equal(t, len(st.Index.Names), st.Size())
	require.True(t, st.HasSection(elf.SHT_SYMTAB))
	require.False(t, st.HasSection(elf.SHT_NOTE))

	info := st.DebugInfo()
	require.Equal(t, st.Size(), info.Size)
	require.Contains(t, info.Name, "SymbolTable")
	require.False(t, info.MiniDebugInfo)

	require.Contains(t, st.DebugString(), "sz =")

	st.Refresh()

	require.Equal(t, "iter", st.Resolve(0x1149))

	st.Cleanup()
	// Cleanup() closes the file but does not set an error by itself; set one
	// directly to exercise the IsDead() true path (mirrors real usage where
	// a later reopen attempt fails and populates File.err).
	st.File.err = ErrNoSymbols
	require.True(t, st.IsDead())
}

func TestNewMiniDebugInfoSymbolTable(t *testing.T) {
	me, err := NewMMapedElfFile("./testdata/elfs/elf.minidebuginfo")
	require.NoError(t, err)
	defer me.Close()

	st, err := me.NewMiniDebugInfoSymbolTable(new(SymbolsOptions))
	require.NoError(t, err)
	require.NotNil(t, st)
	require.Greater(t, st.Size(), 0)
}

func TestNewMiniDebugInfoSymbolTableMissingSection(t *testing.T) {
	me, err := NewMMapedElfFile("./testdata/elfs/elf")
	require.NoError(t, err)
	defer me.Close()

	_, err = me.NewMiniDebugInfoSymbolTable(new(SymbolsOptions))
	require.ErrorIs(t, err, ErrNoSymbols)
}
