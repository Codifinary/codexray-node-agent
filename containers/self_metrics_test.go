package containers

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSelfMetricsStartAtZero(t *testing.T) {
	m := newSelfMetrics()
	reg := prometheus.NewRegistry()
	for _, c := range m.collectors() {
		require.NoError(t, reg.Register(c))
	}
	families, err := reg.Gather()
	require.NoError(t, err)
	series := map[string]int{}
	for _, f := range families {
		series[f.GetName()] = len(f.GetMetric())
		for _, metric := range f.GetMetric() {
			assert.Zero(t, metric.GetCounter().GetValue(), f.GetName())
		}
	}
	assert.Equal(t, map[string]int{
		"node_agent_listen_open_events_total":        4,
		"node_agent_container_lookup_failures_total": 4,
		"node_agent_process_registrations_total":     4,
		"node_agent_listen_attributions_total":       3,
		"node_agent_listens_unattributed_total":      1,
		"node_agent_docker_port_bindings_total":      3,
		"node_agent_docker_metadata_refreshes_total": 3,
	}, series, "every label value is exported from the start, at 0")
}
