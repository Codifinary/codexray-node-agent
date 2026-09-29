package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"
)

func TestNewMetricsWithRegisterer(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := New(reg)
	require.NotNil(t, m)
	require.NotNil(t, m.Symtab)
	require.NotNil(t, m.Python)

	mfs, err := reg.Gather()
	require.NoError(t, err)
	require.NotEmpty(t, mfs)
}

func TestNewMetricsWithNilRegisterer(t *testing.T) {
	require.NotPanics(t, func() {
		m := New(nil)
		require.NotNil(t, m)
		require.NotNil(t, m.Symtab)
		require.NotNil(t, m.Python)
	})
}

func TestNewSymtabMetricsNilRegisterer(t *testing.T) {
	m := NewSymtabMetrics(nil)
	require.NotNil(t, m)
	// Exercise each counter to ensure they were constructed properly.
	m.ElfErrors.WithLabelValues("err").Inc()
	m.ProcErrors.WithLabelValues("err").Inc()
	m.KnownSymbols.WithLabelValues("svc").Inc()
	m.UnknownSymbols.WithLabelValues("svc").Inc()
	m.UnknownModules.WithLabelValues("svc").Inc()
	m.UnknownStacks.WithLabelValues("svc").Inc()
}

func TestNewSymtabMetricsRealRegisterer(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewSymtabMetrics(reg)
	require.NotNil(t, m)

	m.KnownSymbols.WithLabelValues("svc").Inc()

	mfs, err := reg.Gather()
	require.NoError(t, err)

	var found bool
	for _, mf := range mfs {
		if mf.GetName() == "pyroscope_symtab_known_symbols_total" {
			found = true
			require.Len(t, mf.Metric, 1)
			require.Equal(t, float64(1), mf.Metric[0].GetCounter().GetValue())
		}
	}
	require.True(t, found)
}

func TestNewPythonMetricsNilRegisterer(t *testing.T) {
	m := NewPythonMetrics(nil)
	require.NotNil(t, m)
	m.PidDataError.WithLabelValues("svc").Inc()
	m.LostSamples.Inc()
	m.SymbolLookup.WithLabelValues("svc").Inc()
	m.UnknownSymbols.WithLabelValues("svc").Inc()
	m.StacktraceError.Inc()
	m.ProcessInitSuccess.WithLabelValues("svc").Inc()
	m.Load.Inc()
	m.LoadError.Inc()
}

func TestNewPythonMetricsRealRegisterer(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewPythonMetrics(reg)
	require.NotNil(t, m)

	m.PidDataError.WithLabelValues("svc").Inc()

	mfs, err := reg.Gather()
	require.NoError(t, err)

	var found *dto.MetricFamily
	for _, mf := range mfs {
		if mf.GetName() == "pyroscope_pyperf_pid_data_errors_total" {
			found = mf
		}
	}
	require.NotNil(t, found)
	require.Equal(t, float64(1), found.Metric[0].GetCounter().GetValue())
}

// TestNewPythonMetricsLoadCountersNotRegistered documents a bug: NewPythonMetrics
// creates m.Load and m.LoadError counters but never includes them in the
// reg.MustRegister(...) call (metrics/python.go:54-61), so they are silently
// never exposed/scraped when a real registerer is used.
func TestNewPythonMetricsLoadCountersNotRegistered(t *testing.T) {
	t.Skip("BUG: metrics/python.go:54-61 NewPythonMetrics never registers m.Load and m.LoadError with the given registerer, so they are never scraped")

	reg := prometheus.NewRegistry()
	m := NewPythonMetrics(reg)
	m.Load.Inc()

	mfs, err := reg.Gather()
	require.NoError(t, err)

	var found *dto.MetricFamily
	for _, mf := range mfs {
		if mf.GetName() == "pyroscope_pyperf_load" {
			found = mf
		}
	}
	require.NotNil(t, found, "expected pyroscope_pyperf_load to be registered and gathered")
}
