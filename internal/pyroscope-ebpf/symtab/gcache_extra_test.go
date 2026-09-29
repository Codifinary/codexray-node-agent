package symtab

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGCacheSizesAndIteration(t *testing.T) {
	cache, err := NewGCache[string, *mockResource](GCacheOptions{Size: 10, KeepRounds: 3})
	require.NoError(t, err)

	require.Equal(t, 0, cache.LRUSize())
	require.Equal(t, 0, cache.RoundSize())

	cache.Cache("k1", &mockResource{name: "r1"})
	cache.Cache("k2", &mockResource{name: "r2"})

	require.Equal(t, 2, cache.LRUSize())
	require.Equal(t, 2, cache.RoundSize())

	var lruNames []string
	cache.EachLRU(func(k string, v *mockResource, round int) {
		lruNames = append(lruNames, v.name)
	})
	require.ElementsMatch(t, []string{"r1", "r2"}, lruNames)

	var roundNames []string
	cache.EachRound(func(k string, v *mockResource, round int) {
		roundNames = append(roundNames, v.name)
	})
	require.ElementsMatch(t, []string{"r1", "r2"}, roundNames)

	var eachCount int
	cache.Each(func(k string, v *mockResource, round int) {
		eachCount++
	})
	require.Equal(t, 4, eachCount) // Each calls both EachLRU and EachRound

	cache.Update(GCacheOptions{Size: 1, KeepRounds: 1})
	require.Equal(t, GCacheOptions{Size: 1, KeepRounds: 1}, cache.options)
	require.LessOrEqual(t, cache.LRUSize(), 1)
}

func TestGCacheDebugInfo(t *testing.T) {
	cache, err := NewGCache[string, *mockResource](GCacheOptions{Size: 10, KeepRounds: 3})
	require.NoError(t, err)
	cache.Cache("k1", &mockResource{name: "r1"})

	info := DebugInfo[string, *mockResource, string](cache, func(k string, v *mockResource, round int) string {
		return v.name
	})
	require.Equal(t, 1, info.LRUSize)
	require.Equal(t, 1, info.RoundSize)
	require.Equal(t, 0, info.CurrentRound)
	require.Contains(t, info.LRUDump, "r1")
	require.Contains(t, info.RoundDump, "r1")
}

func TestGCacheRemove(t *testing.T) {
	cache, err := NewGCache[string, *mockResource](GCacheOptions{Size: 10, KeepRounds: 3})
	require.NoError(t, err)
	r1 := &mockResource{name: "r1"}
	cache.Cache("k1", r1)
	require.NotNil(t, cache.Get("k1"))
	cache.Remove("k1")
	require.Nil(t, cache.Get("k1"))
}

func TestGCacheZeroKey(t *testing.T) {
	cache, err := NewGCache[string, *mockResource](GCacheOptions{Size: 10, KeepRounds: 3})
	require.NoError(t, err)
	// zero-value key must be a no-op for Cache/Get
	cache.Cache("", &mockResource{name: "should not be cached"})
	require.Nil(t, cache.Get(""))
}
