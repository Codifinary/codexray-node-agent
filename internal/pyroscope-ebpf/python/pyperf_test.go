package python

import (
	"testing"

	"github.com/go-kit/log"
	"github.com/grafana/pyroscope/ebpf/metrics"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestPerf(t *testing.T) *Perf {
	t.Helper()
	p, err := NewPerf(log.NewNopLogger(), metrics.NewPythonMetrics(prometheus.NewRegistry()), nil, nil)
	require.NoError(t, err)
	require.NotNil(t, p)
	return p
}

func TestNewPerf(t *testing.T) {
	p := newTestPerf(t)
	assert.NotNil(t, p.pidCache)
	assert.Nil(t, p.FindProc(1))
}

func TestFindProc(t *testing.T) {
	p := newTestPerf(t)
	assert.Nil(t, p.FindProc(42))

	proc := &Proc{PerfPyPidData: &PerfPyPidData{}}
	p.pidCache[42] = proc
	assert.Same(t, proc, p.FindProc(42))
}

func TestCollectEventsEmpty(t *testing.T) {
	p := newTestPerf(t)
	buf := p.CollectEvents(nil)
	assert.Empty(t, buf)
}

func TestCollectEventsWithinCapacity(t *testing.T) {
	p := newTestPerf(t)
	e1 := &PerfPyEvent{}
	e2 := &PerfPyEvent{}
	p.events = []*PerfPyEvent{e1, e2}

	buf := make([]*PerfPyEvent, 0, 10)
	buf = p.CollectEvents(buf)
	require.Len(t, buf, 2)
	assert.Same(t, e1, buf[0])
	assert.Same(t, e2, buf[1])
	// internal events slice must be drained
	assert.Empty(t, p.events)
}

func TestCollectEventsGrowsBuffer(t *testing.T) {
	p := newTestPerf(t)
	e1 := &PerfPyEvent{}
	e2 := &PerfPyEvent{}
	e3 := &PerfPyEvent{}
	p.events = []*PerfPyEvent{e1, e2, e3}

	buf := make([]*PerfPyEvent, 0, 1) // smaller capacity than events
	buf = p.CollectEvents(buf)
	require.Len(t, buf, 3)
	assert.Same(t, e1, buf[0])
	assert.Same(t, e3, buf[2])
}

func TestGetLazySymbols(t *testing.T) {
	p := newTestPerf(t)
	sym := &PerfPySymbol{}
	p.prevSymbols = map[uint32]*PerfPySymbol{1: sym}

	ls := p.GetLazySymbols()
	require.NotNil(t, ls)
	assert.False(t, ls.fresh)
	assert.Same(t, sym, ls.symbols[1])
}

func TestLazySymbolsGetSymbolCacheHit(t *testing.T) {
	p := newTestPerf(t)
	sym := &PerfPySymbol{}
	ls := &LazySymbols{
		perf:    p,
		symbols: map[uint32]*PerfPySymbol{7: sym},
		fresh:   false,
	}
	got, err := ls.GetSymbol(7, "svc")
	require.NoError(t, err)
	assert.Same(t, sym, got)
}

func TestNewProcCacheHit(t *testing.T) {
	// When the pid is already cached, NewProc must return the cached entry
	// directly without touching the (here nil) eBPF pid-data map.
	p := newTestPerf(t)
	prev := &Proc{PerfPyPidData: &PerfPyPidData{}}
	p.pidCache[55] = prev

	got, err := p.NewProc(55, &PerfPyPidData{}, nil, "svc")
	require.NoError(t, err)
	assert.Same(t, prev, got)
}

func TestLazySymbolsGetSymbolFreshMiss(t *testing.T) {
	// When symbols are already "fresh" and the id isn't present, getSymbol
	// must fail immediately without attempting to refresh from the (nil)
	// eBPF map.
	p := newTestPerf(t)
	ls := &LazySymbols{
		perf:    p,
		symbols: map[uint32]*PerfPySymbol{},
		fresh:   true,
	}
	_, err := ls.GetSymbol(123, "svc")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}
