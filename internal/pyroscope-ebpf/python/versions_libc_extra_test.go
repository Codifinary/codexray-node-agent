package python

import (
	"strings"
	"testing"

	"github.com/go-kit/log"
	"github.com/grafana/pyroscope/ebpf/symtab"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetMuslVersionFromReader(t *testing.T) {
	v, err := GetMuslVersionFromReader(strings.NewReader("junk before 1.2.44,more junk"))
	require.NoError(t, err)
	assert.Equal(t, Version{Major: 1, Minor: 2, Patch: 44}, v)

	_, err = GetMuslVersionFromReader(strings.NewReader("nothing matching here"))
	require.Error(t, err)
}

func TestGetGlibcVersionFromReader(t *testing.T) {
	v, err := GetGlibcVersionFromReader(strings.NewReader("junk before glibc 2.38,more junk"))
	require.NoError(t, err)
	assert.Equal(t, Version{Major: 2, Minor: 38}, v)

	_, err = GetGlibcVersionFromReader(strings.NewReader("nothing matching here"))
	require.Error(t, err)
}

func TestGetGlibcVersionFromFileMissing(t *testing.T) {
	_, err := GetGlibcVersionFromFile("/nonexistent/path/libc.so.6")
	require.Error(t, err)
}

func TestGetMuslVersionFromFileMissing(t *testing.T) {
	_, err := GetMuslVersionFromFile("/nonexistent/path/musl.so.1")
	require.Error(t, err)
}

func TestGetLibcNoLibcFound(t *testing.T) {
	l := log.NewNopLogger()
	_, err := GetLibc(l, 12345, ProcInfo{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no libc found")
}

func TestGetLibcMuslFileMissing(t *testing.T) {
	l := log.NewNopLogger()
	info := ProcInfo{
		Musl: []*symtab.ProcMap{{Pathname: "/lib/ld-musl-x86_64.so.1"}},
	}
	// pid 1 exists but its root won't have this musl path in most sandboxed
	// test environments; either way, the file won't resolve to a valid musl
	// binary, so we expect an error surfaced from GetMuslVersionFromFile.
	_, err := GetLibc(l, 999999999, info)
	require.Error(t, err)
}

func TestGetLibcGlibcFileMissing(t *testing.T) {
	l := log.NewNopLogger()
	info := ProcInfo{
		Glibc: []*symtab.ProcMap{{Pathname: "/usr/lib/x86_64-linux-gnu/libc.so.6"}},
	}
	_, err := GetLibc(l, 999999999, info)
	require.Error(t, err)
}
