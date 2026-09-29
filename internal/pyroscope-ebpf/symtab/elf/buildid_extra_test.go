package elf

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuildIDGNU(t *testing.T) {
	gnu := GNUBuildID("abc123")
	require.True(t, gnu.GNU())
	require.False(t, gnu.Empty())

	goID := GoBuildID("abc123")
	require.False(t, goID.GNU())

	var empty BuildID
	require.False(t, empty.GNU())
	require.True(t, empty.Empty())
}

func TestMMapedElfFileFilePath(t *testing.T) {
	me, err := NewMMapedElfFile("./testdata/elfs/elf")
	require.NoError(t, err)
	defer me.Close()
	require.Equal(t, "./testdata/elfs/elf", me.FilePath())
}
