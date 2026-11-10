// Copyright Codexray
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

func TestRuntimeDiagnosticsExposed(t *testing.T) {
	registry := prometheus.NewRegistry()
	reg := prometheus.WrapRegistererWith(prometheus.Labels{"machine_id": "m", "system_uuid": "u"}, registry)
	registerRuntimeDiagnostics(reg)

	mfs, err := registry.Gather()
	require.NoError(t, err)
	names := map[string]int{}
	series := 0 // exposition lines: each histogram bucket/sum/count and summary quantile counts
	for _, mf := range mfs {
		names[mf.GetName()] = len(mf.GetMetric())
		for _, m := range mf.GetMetric() {
			switch {
			case m.Histogram != nil:
				series += len(m.Histogram.GetBucket()) + 3 // + Inf, sum, count
			case m.Summary != nil:
				series += len(m.Summary.GetQuantile()) + 2
			default:
				series++
			}
		}
	}
	for _, n := range []string{
		"go_memstats_heap_alloc_bytes", "go_memstats_heap_inuse_bytes", "go_memstats_heap_objects",
		"go_memory_classes_heap_objects_bytes", "go_memory_classes_heap_released_bytes", "go_goroutines",
		"process_resident_memory_bytes", "process_open_fds", "process_cpu_seconds_total",
		"node_agent_containers_active", "node_agent_processes_active", "node_agent_uprobes_active",
		"node_agent_log_parsers_active", "node_agent_ebpf_event_queue_length",
		"node_agent_profiling_targets",
	} {
		_, ok := names[n]
		require.True(t, ok, "missing %s", n)
	}
	t.Logf("runtime diagnostics: %d metric families, %d series", len(mfs), series)
	require.Less(t, series, 150, "self-observability must stay small")
}
