package symtab

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestElfCacheUpdateAndNextRound(t *testing.T) {
	elfCache, err := NewElfCache(testCacheOptions, testCacheOptions)
	require.NoError(t, err)

	elfCache.NextRound()
	require.Equal(t, 1, elfCache.BuildIDCache.round)
	require.Equal(t, 1, elfCache.SameFileCache.round)

	newOpts := GCacheOptions{Size: 5, KeepRounds: 1}
	elfCache.Update(newOpts, newOpts)
	require.Equal(t, newOpts, elfCache.BuildIDCache.options)
	require.Equal(t, newOpts, elfCache.SameFileCache.options)
}

func TestElfCacheDebugInfoEmpty(t *testing.T) {
	elfCache, err := NewElfCache(testCacheOptions, testCacheOptions)
	require.NoError(t, err)
	info := elfCache.DebugInfo()
	require.Equal(t, 0, info.BuildIDCache.LRUSize)
	require.Equal(t, 0, info.SameFileCache.LRUSize)
}
