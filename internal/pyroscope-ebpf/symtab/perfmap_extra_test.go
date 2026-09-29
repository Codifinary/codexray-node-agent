package symtab

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParsePerfMap(t *testing.T) {
	data := "0x1000 0x100 foo\n0x2000 0x50 bar\n"
	syms, err := parsePerfMap(strings.NewReader(data))
	require.NoError(t, err)
	require.Len(t, syms, 2)
	require.Equal(t, "foo", syms[0].Name)
	require.Equal(t, uint64(0x1000), syms[0].Start)
	require.Equal(t, "bar", syms[1].Name)
	require.Equal(t, uint64(0x2000), syms[1].Start)
}

func TestParsePerfMapInvalidLine(t *testing.T) {
	_, err := parsePerfMap(strings.NewReader("not enough fields\n"))
	require.Error(t, err)
}

func TestParsePerfMapBadAddr(t *testing.T) {
	_, err := parsePerfMap(strings.NewReader("0xzz 0x100 foo\n"))
	require.Error(t, err)
}

func TestParsePerfMapBadSize(t *testing.T) {
	_, err := parsePerfMap(strings.NewReader("0x1000 0xzz foo\n"))
	require.Error(t, err)
}

func TestPerfMapResolve(t *testing.T) {
	pm := &PerfMap{symbols: []Symbol{
		{Start: 0x1000, Name: "foo", size: 0x100},
		{Start: 0x2000, Name: "bar", size: 0x50},
	}}
	require.Equal(t, "foo", pm.Resolve(0x1000).Name)
	require.Equal(t, "foo", pm.Resolve(0x1050).Name)
	require.Equal(t, "bar", pm.Resolve(0x2000).Name)
	require.Equal(t, Symbol{}, pm.Resolve(0x1500))
	require.Equal(t, Symbol{}, pm.Resolve(0x50))
}

func TestNewPerfMapNotExist(t *testing.T) {
	// a pid that (almost certainly) does not exist, so /proc/<pid>/root/...
	// resolves to os.ErrNotExist rather than a permission error.
	pm, err := NewPerfMap(987654321)
	require.NoError(t, err)
	require.Nil(t, pm)
}

func TestGetNsPidNoProc(t *testing.T) {
	// a pid that (almost certainly) doesn't exist: getNsPid should fall back
	// to returning the input pid unchanged.
	require.Equal(t, 987654321, getNsPid(987654321))
}

func TestRemoveOverlaps(t *testing.T) {
	syms := []Symbol{
		{Start: 0x1000, Name: "a", size: 0x200, generation: 0},
		{Start: 0x1100, Name: "b", size: 0x100, generation: 1}, // overlaps a, newer generation wins
	}
	res := removeOverlaps(syms)
	require.Len(t, res, 1)
	require.Equal(t, "b", res[0].Name)

	// no overlap
	syms2 := []Symbol{
		{Start: 0x1000, Name: "a", size: 0x50},
		{Start: 0x2000, Name: "b", size: 0x50},
	}
	res2 := removeOverlaps(syms2)
	require.Len(t, res2, 2)

	require.Nil(t, removeOverlaps(nil))
}
