// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package node

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/codifinary/codexray-node-agent/flags"
	"github.com/codifinary/codexray-node-agent/node/metadata"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func collectorTestProcRoot(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
	}
	prev := procRoot
	procRoot = dir
	t.Cleanup(func() { procRoot = prev })
	return dir
}

func collectorTestGatherFamilies(t *testing.T, c prometheus.Collector) []*dto.MetricFamily {
	t.Helper()
	reg := prometheus.NewPedanticRegistry()
	require.NoError(t, reg.Register(c))
	mfs, err := reg.Gather()
	require.NoError(t, err)
	return mfs
}

// collectorTestGather returns name -> `{k="v",...}` -> value
func collectorTestGather(t *testing.T, c prometheus.Collector) map[string]map[string]float64 {
	res := map[string]map[string]float64{}
	for _, mf := range collectorTestGatherFamilies(t, c) {
		series := map[string]float64{}
		for _, m := range mf.GetMetric() {
			var lbls []string
			for _, l := range m.GetLabel() {
				lbls = append(lbls, l.GetName()+"=\""+l.GetValue()+"\"")
			}
			v := m.GetGauge().GetValue()
			if mf.GetType() == dto.MetricType_COUNTER {
				v = m.GetCounter().GetValue()
			}
			series["{"+strings.Join(lbls, ",")+"}"] = v
		}
		res[mf.GetName()] = series
	}
	return res
}

func collectorTestTypes(t *testing.T, c prometheus.Collector) map[string]dto.MetricType {
	res := map[string]dto.MetricType{}
	for _, mf := range collectorTestGatherFamilies(t, c) {
		res[mf.GetName()] = mf.GetType()
	}
	return res
}

// names consumed by codexray-mainv2 constructor/queries.go
var collectorTestMainv2Names = []string{
	"node_info",
	"node_cloud_info",
	"node_uptime_seconds",
	"node_resources_cpu_logical_cores",
	"node_resources_cpu_usage_seconds_total",
	"node_resources_memory_total_bytes",
	"node_resources_memory_available_bytes",
	"node_resources_memory_free_bytes",
	"node_resources_memory_cached_bytes",
	"node_resources_disk_read_time_seconds_total",
	"node_resources_disk_write_time_seconds_total",
	"node_resources_disk_reads_total",
	"node_resources_disk_writes_total",
	"node_resources_disk_read_bytes_total",
	"node_resources_disk_written_bytes_total",
	"node_resources_disk_io_time_seconds_total",
	"node_net_interface_up",
	"node_net_interface_ip",
	"node_net_received_bytes_total",
	"node_net_transmitted_bytes_total",
}

func TestCollectorDescribeMatchesMainv2(t *testing.T) {
	c := &Collector{instanceMetadata: &metadata.CloudMetadata{}}
	ch := make(chan *prometheus.Desc, 100)
	c.Describe(ch)
	close(ch)
	var described []string
	for d := range ch {
		s := d.String()
		i := strings.Index(s, `fqName: "`) + len(`fqName: "`)
		described = append(described, s[i:i+strings.Index(s[i:], `"`)])
	}
	assert.Len(t, described, 22)
	for _, n := range collectorTestMainv2Names {
		assert.Contains(t, described, n)
	}
	assert.Contains(t, described, "node_net_received_packets_total")
	assert.Contains(t, described, "node_net_transmitted_packets_total")
}

func TestCollectorCollect(t *testing.T) {
	collectorTestProcRoot(t, map[string]string{
		"uptime":    "12345.67 99999.00\n",
		"stat":      "cpu  100 200 300 400 500 600 700 800 0 0\ncpu0 100 200 300 400 500 600 700 800 0 0\ncpu1 0 0 0 0 0 0 0 0 0 0\nintr 1 2 3\nctxt 42\n",
		"meminfo":   "MemTotal:       1000 kB\nMemFree:         200 kB\nMemAvailable:    500 kB\nBuffers:          10 kB\nCached:          300 kB\n",
		"diskstats": "   8       0 sda 10 0 20 30 40 0 50 60 0 70 0 0 0 0 0\n   8       1 sda1 1 0 2 3 4 0 5 6 0 7 0 0 0 0 0\n   7       0 loop0 1 0 1 1 1 0 1 1 0 1 0 0 0 0 0\n",
	})
	c := &Collector{
		hostname:      "node-1",
		kernelVersion: "6.1.0",
		instanceMetadata: &metadata.CloudMetadata{
			Provider: metadata.CloudProviderAWS, AccountId: "123", InstanceId: "i-1", InstanceType: "m5.large",
			LifeCycle: "on-demand", Region: "us-east-1", AvailabilityZone: "us-east-1a", AvailabilityZoneId: "use1-az1",
			LocalIPv4: "10.0.0.1", PublicIPv4: "1.2.3.4",
		},
	}
	assert.Equal(t, c.instanceMetadata, c.Metadata())

	got := collectorTestGather(t, c)
	// net metrics come from netlink in the host netns and are host dependent -> not compared
	assert.Equal(t, map[string]float64{`{hostname="node-1",kernel_version="6.1.0"}`: 1}, got["node_info"])
	assert.Equal(t, map[string]float64{
		`{account_id="123",availability_zone="us-east-1a",availability_zone_id="use1-az1",instance_id="i-1",instance_life_cycle="on-demand",instance_type="m5.large",local_ipv4="10.0.0.1",provider="AWS",public_ipv4="1.2.3.4",region="us-east-1"}`: 1,
	}, got["node_cloud_info"])
	assert.Equal(t, map[string]float64{"{}": 12345.67}, got["node_uptime_seconds"])
	assert.Equal(t, map[string]float64{"{}": 2}, got["node_resources_cpu_logical_cores"])
	// /proc/stat is in USER_HZ (100 on amd64/arm64)
	assert.Equal(t, map[string]float64{
		`{mode="user"}`: 1, `{mode="nice"}`: 2, `{mode="system"}`: 3, `{mode="idle"}`: 4,
		`{mode="iowait"}`: 5, `{mode="irq"}`: 6, `{mode="softirq"}`: 7, `{mode="steal"}`: 8,
	}, got["node_resources_cpu_usage_seconds_total"])
	// only whole block devices (sda), not partitions (sda1) or loop devices
	assert.Equal(t, map[string]float64{`{device="sda"}`: 10}, got["node_resources_disk_reads_total"])
	assert.Equal(t, map[string]float64{`{device="sda"}`: 40}, got["node_resources_disk_writes_total"])
	assert.Equal(t, map[string]float64{`{device="sda"}`: 20 * 512}, got["node_resources_disk_read_bytes_total"])
	assert.Equal(t, map[string]float64{`{device="sda"}`: 50 * 512}, got["node_resources_disk_written_bytes_total"])
	assert.Equal(t, map[string]float64{`{device="sda"}`: 0.03}, got["node_resources_disk_read_time_seconds_total"])
	assert.Equal(t, map[string]float64{`{device="sda"}`: 0.06}, got["node_resources_disk_write_time_seconds_total"])
	assert.Equal(t, map[string]float64{`{device="sda"}`: 0.07}, got["node_resources_disk_io_time_seconds_total"])

	// memory: presence and ordering (the kB multiplier is covered by TestMemoryInfoKibibytes)
	for _, n := range []string{"node_resources_memory_total_bytes", "node_resources_memory_free_bytes",
		"node_resources_memory_available_bytes", "node_resources_memory_cached_bytes"} {
		assert.Len(t, got[n], 1, n)
	}
	assert.Greater(t, got["node_resources_memory_total_bytes"]["{}"], got["node_resources_memory_available_bytes"]["{}"])

	// types consumed via rate() must be counters
	types := collectorTestTypes(t, c)
	for n, typ := range types {
		if strings.HasSuffix(n, "_total") {
			assert.Equal(t, dto.MetricType_COUNTER, typ, n)
		} else {
			assert.Equal(t, dto.MetricType_GAUGE, typ, n)
		}
	}

}

func TestCollectorCollectMissingProcFiles(t *testing.T) {
	collectorTestProcRoot(t, map[string]string{
		"uptime": "garbage\n",
		"stat":   "cpu  x 1 1 1 1 1 1 1 0 0\n",
	})
	c := &Collector{hostname: "h", kernelVersion: "k", instanceMetadata: &metadata.CloudMetadata{}}
	// unreadable/malformed sources are skipped, node_info + node_cloud_info are always emitted
	var got map[string]map[string]float64
	require.NotPanics(t, func() { got = collectorTestGather(t, c) })
	assert.Contains(t, got, "node_info")
	assert.Contains(t, got, "node_cloud_info")
	for _, n := range []string{"node_uptime_seconds", "node_resources_cpu_usage_seconds_total",
		"node_resources_cpu_logical_cores", "node_resources_memory_total_bytes", "node_resources_disk_reads_total"} {
		assert.NotContains(t, got, n)
	}
}

// collectorTestOnCloud mirrors metadata.getCloudProvider's DMI probes so that NewCollector
// (which calls the cloud metadata service over the network) is only exercised off-cloud.
func collectorTestOnCloud() bool {
	for _, f := range []string{"/sys/hypervisor/uuid", "/sys/class/dmi/id/board_vendor", "/sys/class/dmi/id/sys_vendor",
		"/sys/class/dmi/id/chassis_vendor", "/sys/class/dmi/id/chassis_asset_tag"} {
		d, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		v := strings.ToLower(string(d))
		for _, m := range []string{"ec2", "amazon", "google", "microsoft", "digitalocean", "hetzner", "alibaba", "scaleway", "ibm:cloud", "oraclecloud"} {
			if strings.Contains(v, m) {
				return true
			}
		}
	}
	return false
}

func TestNewCollectorFlagOverrides(t *testing.T) {
	if collectorTestOnCloud() {
		t.Skip("running on a cloud VM: NewCollector would query the real metadata service")
	}
	type kv struct {
		p *string
		v string
	}
	overrides := []kv{
		{flags.Provider, "custom"}, {flags.Region, "r1"}, {flags.AvailabilityZone, "az1"},
		{flags.InstanceType, "big"}, {flags.InstanceLifeCycle, "spot"},
	}
	for _, o := range overrides {
		prev := *o.p
		p := o.p
		t.Cleanup(func() { *p = prev })
		*o.p = o.v
	}
	c := NewCollector("host", "6.1.0")
	md := c.Metadata()
	require.NotNil(t, md)
	assert.Equal(t, metadata.CloudProvider("custom"), md.Provider)
	assert.Equal(t, "r1", md.Region)
	assert.Equal(t, "az1", md.AvailabilityZone)
	assert.Equal(t, "big", md.InstanceType)
	assert.Equal(t, "spot", md.LifeCycle)
	assert.Equal(t, "host", c.hostname)
	assert.Equal(t, "6.1.0", c.kernelVersion)

	for _, o := range overrides {
		*o.p = ""
	}
	md = NewCollector("host", "6.1.0").Metadata()
	require.NotNil(t, md, "no cloud -> empty metadata, never nil")
	assert.Equal(t, metadata.CloudMetadata{}, *md)
}

func TestCollectorCollectOnlyNodeMetrics(t *testing.T) {
	// every emitted family must be one of the described names (no stray metrics)
	collectorTestProcRoot(t, map[string]string{})
	c := &Collector{instanceMetadata: &metadata.CloudMetadata{}}
	var got []string
	for _, mf := range collectorTestGatherFamilies(t, c) {
		got = append(got, mf.GetName())
	}
	for _, n := range got {
		assert.True(t, strings.HasPrefix(n, "node_"), n)
	}
}
