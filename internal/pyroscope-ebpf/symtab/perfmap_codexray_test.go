package symtab

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func writePerfMap(t *testing.T, path string, n int) {
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "%x %x Lcom/example/Service%d;::handle\n", 0x1000+i*0x100, 0x80, i)
	}
	require.NoError(t, os.WriteFile(path, []byte(b.String()), 0o600))
}

func TestPerfMapReuseAndSizeLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "perf-1.map")
	writePerfMap(t, path, 1000)

	pm, err := loadPerfMap(path, nil)
	require.NoError(t, err)
	require.Len(t, pm.symbols, 1000)
	require.Equal(t, "Lcom/example/Service3;::handle", pm.Resolve(0x1000+3*0x100+1).Name)

	reused := PerfMapReused.Load()
	slot := pm
	pm2, err := loadPerfMap(path, &slot)
	require.NoError(t, err)
	require.Same(t, pm, pm2, "unchanged file is not re-parsed")
	require.Equal(t, reused+1, PerfMapReused.Load())

	// The JVM rewrites the map: new size/mtime -> parsed again.
	writePerfMap(t, path, 1001)
	require.NoError(t, os.Chtimes(path, time.Now().Add(time.Minute), time.Now().Add(time.Minute)))
	slot = pm
	pm = nil
	pm3, err := loadPerfMap(path, &slot)
	require.NoError(t, err)
	require.Len(t, pm3.symbols, 1001)
	require.Nil(t, slot, "the caller's old table is released before the new one is parsed")

	prev := MaxPerfMapSize
	defer func() { MaxPerfMapSize = prev }()
	MaxPerfMapSize = 1024
	skipped := PerfMapSkippedLarge.Load()
	pm4, err := loadPerfMap(path, nil)
	require.NoError(t, err, "too large is not an error: it must not poison the ProcTable")
	require.Nil(t, pm4)
	require.Equal(t, skipped+1, PerfMapSkippedLarge.Load())
}
