package python

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVersionCompare(t *testing.T) {
	testdata := []struct {
		name string
		a, b Version
		want int
	}{
		{"equal", Version{3, 9, 1}, Version{3, 9, 1}, 0},
		{"major greater", Version{4, 0, 0}, Version{3, 9, 1}, 1},
		{"major less", Version{2, 9, 1}, Version{3, 9, 1}, -1},
		{"minor greater", Version{3, 10, 0}, Version{3, 9, 0}, 1},
		{"minor less", Version{3, 8, 0}, Version{3, 9, 0}, -1},
		{"patch greater", Version{3, 9, 5}, Version{3, 9, 1}, 4},
		{"patch less", Version{3, 9, 1}, Version{3, 9, 5}, -4},
	}
	for _, td := range testdata {
		t.Run(td.name, func(t *testing.T) {
			got := td.a.Compare(&td.b)
			if td.want == 0 {
				assert.Equal(t, 0, got)
			} else if td.want > 0 {
				assert.Positive(t, got)
			} else {
				assert.Negative(t, got)
			}
		})
	}
}

func TestVersionString(t *testing.T) {
	v := Version{3, 11, 4}
	assert.Equal(t, "3.11.4", v.String())
}

func TestGetPythonPatchVersionNotFound(t *testing.T) {
	r := strings.NewReader("this reader has no python version string in it at all")
	_, err := GetPythonPatchVersion(r, Version{Major: 3, Minor: 11})
	require.Error(t, err)
}

func TestGetPythonPatchVersionFound(t *testing.T) {
	r := strings.NewReader("some junk before python3.11.7 and after")
	v, err := GetPythonPatchVersion(r, Version{Major: 3, Minor: 11})
	require.NoError(t, err)
	assert.Equal(t, 7, v.Patch)
	assert.Equal(t, 3, v.Major)
	assert.Equal(t, 11, v.Minor)
}

func TestGetPythonPatchVersionBadPatch(t *testing.T) {
	// the regexp only captures digits, so a non-digit patch can't actually
	// reach strconv.Atoi via this path; instead exercise rgrep's EOF error
	// wrapping by using an empty reader.
	r := strings.NewReader("")
	_, err := GetPythonPatchVersion(r, Version{Major: 3, Minor: 11})
	require.Error(t, err)
}

func TestGetUserOffsetsExact(t *testing.T) {
	for v := range pyVersions {
		offsets, guess, err := GetUserOffsets(v)
		require.NoError(t, err)
		assert.False(t, guess)
		assert.Same(t, pyVersions[v], offsets)
		break
	}
}

func TestGetUserOffsetsUnsupported(t *testing.T) {
	_, _, err := GetUserOffsets(Version{Major: 99, Minor: 99, Patch: 99})
	require.Error(t, err)
}

func TestGetVersionGuessingExactAndGuessAndMissing(t *testing.T) {
	type dummy struct{ v int }
	m := map[Version]*dummy{
		{1, 0, 0}: {v: 100},
		{1, 0, 1}: {v: 101},
		{1, 0, 5}: {v: 105},
	}

	// exact match
	offsets, guess, err := getVersionGuessing(Version{1, 0, 1}, m)
	require.NoError(t, err)
	assert.False(t, guess)
	assert.Equal(t, 101, offsets.v)

	// guess: picks the highest patch within same major.minor
	offsets, guess, err = getVersionGuessing(Version{1, 0, 3}, m)
	require.NoError(t, err)
	assert.True(t, guess)
	assert.Equal(t, 105, offsets.v)

	// no match at all
	_, _, err = getVersionGuessing(Version{2, 0, 0}, m)
	require.Error(t, err)
}
