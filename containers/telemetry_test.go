// Copyright Codexray
// SPDX-License-Identifier: Apache-2.0

package containers

import (
	"strings"
	"testing"
	"time"

	"github.com/codifinary/codexray-node-agent/cgroup"
	"github.com/codifinary/codexray-node-agent/common"
	"github.com/codifinary/codexray-node-agent/flags"
	"github.com/codifinary/codexray-node-agent/proc"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

func collectNames(t *testing.T, c *Container) map[string]int {
	ch := make(chan prometheus.Metric, 1000)
	c.registry.ebpfStatsLastUpdated = time.Now() // skip the eBPF map refresh (no tracer in tests)
	c.Collect(ch)
	close(ch)
	names := map[string]int{}
	for m := range ch {
		d := m.Desc().String()
		name := d[strings.Index(d, `fqName: "`)+9:]
		name = name[:strings.Index(name, `"`)]
		names[name]++
	}
	return names
}

func testContainer(t *testing.T) *Container {
	cgroup.SetRootsForTesting("../cgroup/fixtures/cgroup", "../cgroup/fixtures/cgroup")
	cg, err := cgroup.NewFromProcessCgroupFile("../cgroup/fixtures/proc/200/cgroup")
	require.NoError(t, err)
	c := &Container{
		id: "/docker/test", cgroup: cg, metadata: &ContainerMetadata{image: "img"},
		processes: map[uint32]*Process{}, delaysByPid: map[uint32]Delays{},
		connectionStats: map[common.DestinationKey]*ConnectionStats{}, l7Stats: L7Stats{}, dnsStats: &L7Metrics{},
		gpuStats: map[string]*GpuUsage{}, mounts: map[string]proc.MountInfo{}, logParsers: map[string]*LogParser{},
		registry: &Registry{}, restarts: 2, oomKills: 1,
	}
	return c
}

// CONTAINER_RESOURCE_METRICS_ENABLED=false suppresses resource series only; lifecycle
// counters (restarts, OOM kills) and identity (container_info) remain.
func TestContainerResourceMetricsFlag(t *testing.T) {
	prev := flags.Telemetry
	defer func() { flags.Telemetry = prev }()

	flags.Telemetry.ContainerResourceMetrics = true
	on := collectNames(t, testContainer(t))
	require.Contains(t, on, "container_resources_cpu_usage_seconds_total")
	require.Contains(t, on, "container_resources_cpu_limit_cores")
	require.Contains(t, on, "container_resources_memory_rss_bytes")

	flags.Telemetry.ContainerResourceMetrics = false
	off := collectNames(t, testContainer(t))
	for name := range off {
		require.False(t, strings.HasPrefix(name, "container_resources_"), "%s must be suppressed", name)
	}
	require.Contains(t, off, "container_info")
	require.Contains(t, off, "container_restarts_total")
	require.Contains(t, off, "container_oom_kills_total")
	t.Logf("series per container: resource metrics on=%d off=%d", total(on), total(off))
}

func total(m map[string]int) int {
	n := 0
	for _, v := range m {
		n += v
	}
	return n
}

func TestL7MetricsDisabledIsNilSafe(t *testing.T) {
	var m *L7Metrics
	m.observe("200", "", time.Millisecond) // must not panic
}
