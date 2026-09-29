package symtab

import (
	"os"
	"testing"

	"github.com/grafana/pyroscope/ebpf/metrics"
	"github.com/grafana/pyroscope/ebpf/util"
	"github.com/stretchr/testify/require"
)

func TestNewSymbolCachePanicsOnNilMetrics(t *testing.T) {
	require.Panics(t, func() {
		_, _ = NewSymbolCache(util.TestLogger(t), CacheOptions{}, nil)
	})
}

func newTestSymbolCache(t *testing.T) *SymbolCache {
	t.Helper()
	sc, err := NewSymbolCache(util.TestLogger(t), CacheOptions{
		PidCacheOptions:      testCacheOptions,
		BuildIDCacheOptions:  testCacheOptions,
		SameFileCacheOptions: testCacheOptions,
	}, metrics.NewSymtabMetrics(nil))
	require.NoError(t, err)
	require.NotNil(t, sc)
	return sc
}

func TestSymbolCacheProcTableLifecycle(t *testing.T) {
	sc := newTestSymbolCache(t)

	pid := PidKey(os.Getpid())

	require.Nil(t, sc.GetProcTableCached(pid))

	table := sc.NewProcTable(pid, DefaultSymbolOptions)
	require.NotNil(t, table)
	require.Equal(t, int(pid), table.Pid())

	cached := sc.GetProcTableCached(pid)
	require.Equal(t, table, cached)

	sc.NextRound()

	info := sc.PidCacheDebugInfo()
	require.Equal(t, 1, info.LRUSize)

	elfInfo := sc.ElfCacheDebugInfo()
	require.Equal(t, 0, elfInfo.BuildIDCache.LRUSize)

	sc.RemoveDeadPID(pid)
	require.Nil(t, sc.GetProcTableCached(pid))

	sc.Cleanup()

	sc.UpdateOptions(CacheOptions{
		PidCacheOptions:      GCacheOptions{Size: 5, KeepRounds: 1},
		BuildIDCacheOptions:  GCacheOptions{Size: 5, KeepRounds: 1},
		SameFileCacheOptions: GCacheOptions{Size: 5, KeepRounds: 1},
	})
}

func TestSymbolCacheGetKallsyms(t *testing.T) {
	sc := newTestSymbolCache(t)
	// GetKallsyms lazily initializes kallsyms; whether or not /proc/kallsyms
	// is readable in this environment, it must return a non-nil table and
	// must not panic, and repeated calls should return the same instance.
	tab1 := sc.GetKallsyms()
	require.NotNil(t, tab1)
	tab2 := sc.GetKallsyms()
	require.Same(t, tab1, tab2)
}
