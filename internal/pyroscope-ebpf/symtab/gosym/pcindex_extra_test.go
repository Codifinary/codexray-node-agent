package gosym

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPCIndexBasics32(t *testing.T) {
	idx := NewPCIndex(3)
	idx.Set(0, 0x1000)
	idx.Set(1, 0x2000)
	idx.Set(2, 0x3000)

	require.True(t, idx.Is32())
	require.Equal(t, 3, idx.Length())
	require.Equal(t, uint64(0x1000), idx.Get(0))
	require.Equal(t, uint64(0x1000), idx.First())
	require.Equal(t, uint64(0x3000), idx.Value(2))

	require.Equal(t, 0, idx.FindIndex(0x1000))
	require.Equal(t, 1, idx.FindIndex(0x2500))
	require.Equal(t, -1, idx.FindIndex(0x500))
}

func TestPCIndexUpgradeTo64(t *testing.T) {
	idx := NewPCIndex(3)
	idx.Set(0, 0x1000)
	idx.Set(1, 0x2000)
	require.True(t, idx.Is32())

	// crossing math.MaxUint32 forces the index to switch to 64-bit storage
	// via setImpl, converting all previously-set values.
	idx.Set(2, uint64(math.MaxUint32)+1)
	require.False(t, idx.Is32())
	require.Equal(t, uint64(0x1000), idx.Get(0))
	require.Equal(t, uint64(0x2000), idx.Get(1))
	require.Equal(t, uint64(math.MaxUint32)+1, idx.Get(2))
	require.Equal(t, uint64(math.MaxUint32)+1, idx.Value(2))
	require.Equal(t, 3, idx.Length())
}

func TestPCIndexSetImplOnAlready64(t *testing.T) {
	idx := NewPCIndex(2)
	idx.Set(0, uint64(math.MaxUint32)+1) // forces 64-bit immediately
	require.False(t, idx.Is32())
	idx.Set(1, 0x42) // subsequent Set on an already-64-bit index
	require.Equal(t, uint64(0x42), idx.Get(1))
}

func TestPCIndex64Conversion(t *testing.T) {
	idx := NewPCIndex(2)
	idx.Set(0, 0x10)
	idx.Set(1, 0x20)
	require.True(t, idx.Is32())

	idx64 := idx.PCIndex64()
	require.False(t, idx64.Is32())
	require.Equal(t, uint64(0x10), idx64.Get(0))
	require.Equal(t, uint64(0x20), idx64.Get(1))

	// calling PCIndex64 on an already-64-bit index is a no-op passthrough
	idx64Again := idx64.PCIndex64()
	require.False(t, idx64Again.Is32())
	require.Equal(t, idx64.Get(0), idx64Again.Get(0))
}

func TestPCIndexFindIndex64(t *testing.T) {
	idx := NewPCIndex(3)
	idx.Set(0, uint64(math.MaxUint32)+1)
	idx.Set(1, uint64(math.MaxUint32)+100)
	idx.Set(2, uint64(math.MaxUint32)+200)
	require.False(t, idx.Is32())

	require.Equal(t, -1, idx.FindIndex(0))
	require.Equal(t, 0, idx.FindIndex(uint64(math.MaxUint32)+1))
	require.Equal(t, 1, idx.FindIndex(uint64(math.MaxUint32)+150))
}

func TestPCIndexFindIndexDuplicates(t *testing.T) {
	idx := NewPCIndex(4)
	idx.Set(0, 0x100)
	idx.Set(1, 0x100)
	idx.Set(2, 0x100)
	idx.Set(3, 0x200)

	// FindIndex should return the first of a run of duplicate values.
	require.Equal(t, 0, idx.FindIndex(0x100))
	require.Equal(t, 0, idx.FindIndex(0x150))
}
