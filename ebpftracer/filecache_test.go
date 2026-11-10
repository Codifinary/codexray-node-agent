// Copyright Codexray
// SPDX-License-Identifier: Apache-2.0

package ebpftracer

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestFileInfoCacheKeyedByFileIdentity(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a")
	require.NoError(t, os.WriteFile(a, []byte("v1"), 0o600))
	link := filepath.Join(dir, "link")
	require.NoError(t, os.Symlink(a, link))

	loads := 0
	load := func(string) int { loads++; return loads }
	c := newFileInfoCache[int](10)

	v, cached := c.get(a, load)
	require.Equal(t, 1, v)
	require.False(t, cached)
	v, cached = c.get(link, load) // same inode through a symlink, like /proc/<pid>/exe
	require.Equal(t, 1, v)
	require.True(t, cached)

	// A replaced binary (new content, new mtime) is a different key.
	require.NoError(t, os.WriteFile(a, []byte("v2-longer"), 0o600))
	require.NoError(t, os.Chtimes(a, time.Now().Add(time.Hour), time.Now().Add(time.Hour)))
	v, cached = c.get(a, load)
	require.Equal(t, 2, v)
	require.False(t, cached)

	// Missing files are not cached.
	_, cached = c.get(filepath.Join(dir, "missing"), load)
	require.False(t, cached)
	require.Equal(t, 2, c.len())
}

func TestFileInfoCacheBounded(t *testing.T) {
	dir := t.TempDir()
	c := newFileInfoCache[int](3)
	for i := 0; i < 10; i++ {
		p := filepath.Join(dir, string(rune('a'+i)))
		require.NoError(t, os.WriteFile(p, []byte{byte(i)}, 0o600))
		c.get(p, func(string) int { return i })
		require.LessOrEqual(t, c.len(), 3)
	}
}

func TestLoadGoTlsBinaryInfoOnGoBinary(t *testing.T) {
	info := loadGoTlsBinaryInfo(os.Args[0]) // the test binary is a Go executable
	require.True(t, info.isGo)
	require.NotEmpty(t, info.version)
}

// BenchmarkGoTlsBinaryInfo shows the per-process cost that the cache removes for every
// process after the first one running the same binary.
// Set CX_BENCH_BINARY to an unstripped Go binary for realistic numbers (go test binaries
// are linked without a symbol table).
func BenchmarkGoTlsBinaryInfo(b *testing.B) {
	path := os.Getenv("CX_BENCH_BINARY")
	if path == "" {
		path = os.Args[0]
	}
	b.Run("uncached", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			loadGoTlsBinaryInfo(path)
		}
	})
	b.Run("cached", func(b *testing.B) {
		c := newFileInfoCache[*goTlsBinaryInfo](16)
		c.get(path, loadGoTlsBinaryInfo)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			c.get(path, loadGoTlsBinaryInfo)
		}
	})
}
