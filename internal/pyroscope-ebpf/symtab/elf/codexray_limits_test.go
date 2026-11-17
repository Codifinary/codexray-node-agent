package elf

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMiniDebugInfoSizeLimit(t *testing.T) {
	f, err := NewMMapedElfFile("testdata/elfs/elf.minidebuginfo")
	require.NoError(t, err)
	defer f.Close()

	prev := MaxMiniDebugInfoSize
	defer func() { MaxMiniDebugInfoSize = prev }()

	MaxMiniDebugInfoSize = 16 // far below the decompressed size
	_, err = f.NewMiniDebugInfoSymbolTable(&SymbolsOptions{})
	require.True(t, errors.Is(err, ErrMiniDebugInfoTooLarge), "got %v", err)

	MaxMiniDebugInfoSize = 32 << 20
	st, err := f.NewMiniDebugInfoSymbolTable(&SymbolsOptions{})
	require.NoError(t, err)
	require.Greater(t, st.Size(), 0)
}

func TestStringCacheBounded(t *testing.T) {
	f, err := NewMMapedElfFile("testdata/elfs/elf.minidebuginfo")
	require.NoError(t, err)
	defer f.Close()
	st, err := f.NewMiniDebugInfoSymbolTable(&SymbolsOptions{})
	require.NoError(t, err)

	prev := MaxStringCacheEntries
	defer func() { MaxStringCacheEntries = prev }()
	MaxStringCacheEntries = 4

	reader := st.SymReader.(*InMemElfFile)
	for i := 0; i < st.Size(); i++ {
		_, _ = st.symbolName(i)
		require.LessOrEqual(t, len(reader.stringCache), 4)
	}
	// Names still resolve correctly after evictions.
	require.Equal(t, "__on_dlclose", st.Resolve(0x1000))
}
