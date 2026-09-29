package symtab

import (
	"testing"

	"github.com/grafana/pyroscope/ebpf/metrics"
	"github.com/grafana/pyroscope/ebpf/util"
	"github.com/stretchr/testify/require"
)

// TestElfTableCleanupAndDebugInfoBeforeLoad ensures Cleanup and DebugInfo
// work even when the underlying elf file has not been loaded yet (table is
// the noopSymbolNameResolver default).
func TestElfTableCleanupAndDebugInfoBeforeLoad(t *testing.T) {
	elfCache, _ := NewElfCache(testCacheOptions, testCacheOptions)
	logger := util.TestLogger(t)
	tab := NewElfTable(logger, &ProcMap{StartAddr: 0x1000, Offset: 0x1000}, ".", "elf/testdata/elfs/elf",
		ElfTableOptions{
			ElfCache: elfCache,
			Metrics:  metrics.NewSymtabMetrics(nil),
		})

	// not loaded yet
	info := tab.DebugInfo()
	require.Zero(t, info)

	// must not panic
	tab.Cleanup()
}

func TestElfTableDebugInfoAfterLoad(t *testing.T) {
	elfCache, _ := NewElfCache(testCacheOptions, testCacheOptions)
	logger := util.TestLogger(t)
	tab := NewElfTable(logger, &ProcMap{StartAddr: 0x1000, Offset: 0x1000}, ".", "elf/testdata/elfs/elf",
		ElfTableOptions{
			ElfCache: elfCache,
			Metrics:  metrics.NewSymtabMetrics(nil),
		})
	tab.Resolve(0x1149)
	info := tab.DebugInfo()
	require.NotZero(t, info.Size)
	tab.Cleanup()
}

func TestErrorType(t *testing.T) {
	require.Equal(t, "Other", errorType(nil))
}
