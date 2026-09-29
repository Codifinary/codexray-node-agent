package elf

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGoTableBasics(t *testing.T) {
	me, err := NewMMapedElfFile("./testdata/elfs/go18")
	require.NoError(t, err)
	defer me.Close()

	gt, err := me.NewGoTable()
	require.NoError(t, err)

	require.False(t, gt.IsDead())
	require.Equal(t, len(gt.Index.Name), gt.Size())

	info := gt.DebugInfo()
	require.Equal(t, gt.Size(), info.Size)
	require.Contains(t, info.Name, "GoTable")

	// Refresh is a no-op but must not panic.
	gt.Refresh()

	gt.Cleanup()
	// Cleanup() closes the underlying file but does not itself flag an
	// error; IsDead() reflects File.err, so simulate a subsequent failed
	// reopen to exercise the IsDead() true path.
	gt.File.err = errEmptyText
	require.True(t, gt.IsDead())
}

func TestGoTableWithFallback(t *testing.T) {
	me, err := NewMMapedElfFile("./testdata/elfs/elf")
	require.NoError(t, err)
	defer me.Close()

	symTable, err := me.NewSymbolTable(new(SymbolsOptions))
	require.NoError(t, err)

	meGo, err := NewMMapedElfFile("./testdata/elfs/go18")
	require.NoError(t, err)
	defer meGo.Close()
	goTable, err := meGo.NewGoTable()
	require.NoError(t, err)

	fb := &GoTableWithFallback{
		GoTable:  goTable,
		SymTable: symTable,
	}

	require.False(t, fb.IsDead())
	require.Equal(t, goTable.Size()+symTable.Size(), fb.Size())

	info := fb.DebugInfo()
	require.Equal(t, fb.Size(), info.Size)
	require.Contains(t, info.Name, "GoTableWithFallback")

	fb.Refresh()

	// Resolve should hit the go table for a known symbol, and fall back to
	// the (non-matching) symtab otherwise, returning "".
	first := goTable.Index.Entry.First()
	name := fb.Resolve(first)
	require.NotEmpty(t, name)

	fb.Cleanup()
	fb.GoTable.File.err = errEmptyText
	require.True(t, fb.IsDead())
}

func TestSymbolTableWithMiniDebugInfo(t *testing.T) {
	me, err := NewMMapedElfFile("./testdata/elfs/elf")
	require.NoError(t, err)
	defer me.Close()
	primary, err := me.NewSymbolTable(new(SymbolsOptions))
	require.NoError(t, err)

	stm := &SymbolTableWithMiniDebugInfo{Primary: primary, MiniDebug: nil}
	require.False(t, stm.IsDead())
	require.Equal(t, primary.Size(), stm.Size())

	info := stm.DebugInfo()
	require.Equal(t, stm.Size(), info.Size)
	require.Contains(t, info.Name, "SymbolTableWithMiniDebugInfo")

	stm.Refresh()

	require.Contains(t, stm.DebugString(), "nil")

	// resolve a known symbol via primary
	name := stm.Resolve(0x1149)
	require.Equal(t, "iter", name)

	stm.Cleanup()
	stm.Primary.File.err = errEmptyText
	require.True(t, stm.IsDead())

	// nil Primary/MiniDebug must be handled gracefully
	empty := &SymbolTableWithMiniDebugInfo{}
	require.False(t, empty.IsDead())
	require.Equal(t, 0, empty.Size())
	require.Equal(t, "", empty.Resolve(0x1149))
	empty.Refresh()
	empty.Cleanup()
	require.Equal(t, "SymbolTableWithMiniDebugInfo{ nil nil }", empty.DebugString())
}
