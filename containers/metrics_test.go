// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package containers

import (
	"regexp"
	"strings"
	"testing"

	"github.com/codifinary/codexray-node-agent/ebpftracer/l7"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// metricsTestCollector adapts a collect function into an unchecked prometheus.Collector
// (Describe sends nothing), so const metrics with arbitrary descriptors can be gathered.
type metricsTestCollector struct {
	collect func(ch chan<- prometheus.Metric)
}

func (c metricsTestCollector) Describe(chan<- *prometheus.Desc) {}

func (c metricsTestCollector) Collect(ch chan<- prometheus.Metric) { c.collect(ch) }

// metricsTestGather gathers everything the collect function emits through a regular
// (non-pedantic) registry — the same kind main.go serves /metrics from — so invalid
// metrics, duplicate series and label inconsistencies surface as a Gather error.
func metricsTestGather(t *testing.T, collect func(ch chan<- prometheus.Metric)) []*dto.MetricFamily {
	t.Helper()
	mfs, err := metricsTestTryGather(collect)
	require.NoError(t, err)
	return mfs
}

func metricsTestTryGather(collect func(ch chan<- prometheus.Metric)) ([]*dto.MetricFamily, error) {
	reg := prometheus.NewRegistry()
	if err := reg.Register(metricsTestCollector{collect: collect}); err != nil {
		return nil, err
	}
	return reg.Gather()
}

func metricsTestLabels(m *dto.Metric) map[string]string {
	res := map[string]string{}
	for _, lp := range m.GetLabel() {
		res[lp.GetName()] = lp.GetValue()
	}
	return res
}

// metricsTestFind returns all series of the family `name` whose labels contain `labels`.
func metricsTestFind(mfs []*dto.MetricFamily, name string, labels map[string]string) []*dto.Metric {
	var res []*dto.Metric
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
	metrics:
		for _, m := range mf.GetMetric() {
			ls := metricsTestLabels(m)
			for k, v := range labels {
				if ls[k] != v {
					continue metrics
				}
			}
			res = append(res, m)
		}
	}
	return res
}

// metricsTestOne returns the only matching series, failing the test otherwise.
func metricsTestOne(t *testing.T, mfs []*dto.MetricFamily, name string, labels map[string]string) *dto.Metric {
	t.Helper()
	ms := metricsTestFind(mfs, name, labels)
	require.Lenf(t, ms, 1, "expected exactly one %s%v", name, labels)
	return ms[0]
}

func metricsTestNames(mfs []*dto.MetricFamily) []string {
	var res []string
	for _, mf := range mfs {
		res = append(res, mf.GetName())
	}
	return res
}

var metricsTestDescRe = regexp.MustCompile(`^Desc\{fqName: "([^"]*)", help: "(.*)", constLabels: \{(.*)\}, variableLabels: \{(.*)\}\}$`)

// metricsTestParseDesc extracts fqName, help and the ordered variable label names from a Desc.
func metricsTestParseDesc(t *testing.T, d *prometheus.Desc) (string, string, []string) {
	t.Helper()
	m := metricsTestDescRe.FindStringSubmatch(d.String())
	require.NotNil(t, m, "unexpected Desc format: %s", d.String())
	var labels []string
	if m[4] != "" {
		labels = strings.Split(m[4], ",")
	}
	return m[1], m[2], labels
}

// Every descriptor below is part of the wire contract with codexray-mainv2
// (constructor/queries.go + constructor/containers.go, jvm.go, logs.go, fqdn.go):
// the name is what the PromQL selects and the variable labels are what the
// constructor reads via metric.Labels[...]. Label ORDER matters because
// counter()/gauge() pass label values positionally.
func TestMetricsDescriptorsContract(t *testing.T) {
	cases := []struct {
		desc   *prometheus.Desc
		name   string
		labels []string
	}{
		{metrics.ContainerInfo, "container_info", []string{"image", "systemd_triggered_by"}},
		{metrics.Restarts, "container_restarts_total", nil},
		{metrics.CPULimit, "container_resources_cpu_limit_cores", nil},
		{metrics.CPUUsage, "container_resources_cpu_usage_seconds_total", nil},
		{metrics.CPUDelay, "container_resources_cpu_delay_seconds_total", nil},
		{metrics.ThrottledTime, "container_resources_cpu_throttled_seconds_total", nil},
		{metrics.MemoryLimit, "container_resources_memory_limit_bytes", nil},
		{metrics.MemoryRss, "container_resources_memory_rss_bytes", nil},
		{metrics.MemoryCache, "container_resources_memory_cache_bytes", nil},
		{metrics.OOMKills, "container_oom_kills_total", nil},
		{metrics.PsiCPU, "container_resources_cpu_pressure_waiting_seconds_total", []string{"kind"}},
		{metrics.PsiMemory, "container_resources_memory_pressure_waiting_seconds_total", []string{"kind"}},
		{metrics.PsiIO, "container_resources_io_pressure_waiting_seconds_total", []string{"kind"}},
		{metrics.DiskDelay, "container_resources_disk_delay_seconds_total", nil},
		{metrics.DiskSize, "container_resources_disk_size_bytes", []string{"mount_point", "device", "volume"}},
		{metrics.DiskUsed, "container_resources_disk_used_bytes", []string{"mount_point", "device", "volume"}},
		{metrics.DiskReserved, "container_resources_disk_reserved_bytes", []string{"mount_point", "device", "volume"}},
		{metrics.DiskReadOps, "container_resources_disk_reads_total", []string{"mount_point", "device", "volume"}},
		{metrics.DiskReadBytes, "container_resources_disk_read_bytes_total", []string{"mount_point", "device", "volume"}},
		{metrics.DiskWriteOps, "container_resources_disk_writes_total", []string{"mount_point", "device", "volume"}},
		{metrics.DiskWriteBytes, "container_resources_disk_written_bytes_total", []string{"mount_point", "device", "volume"}},
		{metrics.NetListenInfo, "container_net_tcp_listen_info", []string{"listen_addr", "proxy"}},
		{metrics.NetConnectionsSuccessful, "container_net_tcp_successful_connects_total", []string{"destination", "actual_destination"}},
		{metrics.NetConnectionsTotalTime, "container_net_tcp_connection_time_seconds_total", []string{"destination", "actual_destination"}},
		{metrics.NetConnectionsFailed, "container_net_tcp_failed_connects_total", []string{"destination"}},
		{metrics.NetConnectionsActive, "container_net_tcp_active_connections", []string{"destination", "actual_destination"}},
		{metrics.NetRetransmits, "container_net_tcp_retransmits_total", []string{"destination", "actual_destination"}},
		{metrics.NetLatency, "container_net_latency_seconds", []string{"destination_ip"}},
		{metrics.NetBytesSent, "container_net_tcp_bytes_sent_total", []string{"destination", "actual_destination"}},
		{metrics.NetBytesReceived, "container_net_tcp_bytes_received_total", []string{"destination", "actual_destination"}},
		{metrics.LogMessages, "container_log_messages_total", []string{"source", "level", "pattern_hash", "sample"}},
		{metrics.ApplicationType, "container_application_type", []string{"application_type"}},
		{metrics.JvmInfo, "container_jvm_info", []string{"jvm", "java_version"}},
		{metrics.JvmHeapSize, "container_jvm_heap_size_bytes", []string{"jvm"}},
		{metrics.JvmHeapUsed, "container_jvm_heap_used_bytes", []string{"jvm"}},
		{metrics.JvmGCTime, "container_jvm_gc_time_seconds", []string{"jvm", "gc"}},
		{metrics.JvmSafepointTime, "container_jvm_safepoint_time_seconds", []string{"jvm"}},
		{metrics.JvmSafepointSyncTime, "container_jvm_safepoint_sync_time_seconds", []string{"jvm"}},
		{metrics.PythonThreadLockWaitTime, "container_python_thread_lock_wait_time_seconds", nil},
		{metrics.NodejsEventLoopBlockedTime, "container_nodejs_event_loop_blocked_time_seconds_total", nil},
		{metrics.GpuUsagePercent, "container_resources_gpu_usage_percent", []string{"gpu_uuid"}},
		{metrics.GpuMemoryUsagePercent, "container_resources_gpu_memory_usage_percent", []string{"gpu_uuid"}},
		{metrics.Ip2Fqdn, "ip_to_fqdn", []string{"ip", "fqdn"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.NotNil(t, tc.desc)
			name, help, labels := metricsTestParseDesc(t, tc.desc)
			assert.Equal(t, tc.name, name)
			assert.NotEmpty(t, help)
			assert.Equal(t, tc.labels, labels, "variable label names/order")
			// every descriptor must produce a valid metric when given exactly its label values
			values := make([]string, len(tc.labels))
			for i := range values {
				values[i] = "v"
			}
			_, err := prometheus.NewConstMetric(tc.desc, prometheus.GaugeValue, 1, values...)
			assert.NoError(t, err)
		})
	}
}

// The metric names mainv2's queries.go selects from the node agent. Every one must be
// produced by a descriptor or an L7 counter/histogram option here.
func TestMetricsMainv2QueriedNamesAreProduced(t *testing.T) {
	produced := map[string]bool{}
	for _, d := range []*prometheus.Desc{
		metrics.ContainerInfo, metrics.Restarts, metrics.CPULimit, metrics.CPUUsage, metrics.CPUDelay,
		metrics.ThrottledTime, metrics.MemoryLimit, metrics.MemoryRss, metrics.MemoryCache, metrics.OOMKills,
		metrics.DiskSize, metrics.DiskUsed, metrics.NetListenInfo, metrics.NetConnectionsSuccessful,
		metrics.NetConnectionsTotalTime, metrics.NetConnectionsFailed, metrics.NetConnectionsActive,
		metrics.NetRetransmits, metrics.NetLatency, metrics.NetBytesSent, metrics.NetBytesReceived,
		metrics.LogMessages, metrics.ApplicationType, metrics.JvmInfo, metrics.JvmHeapSize, metrics.JvmHeapUsed,
		metrics.JvmGCTime, metrics.JvmSafepointTime, metrics.JvmSafepointSyncTime, metrics.PythonThreadLockWaitTime,
		metrics.Ip2Fqdn,
	} {
		name, _, _ := metricsTestParseDesc(t, d)
		produced[name] = true
	}
	for _, o := range L7Requests {
		produced[o.Name] = true
	}
	for _, o := range L7Latency {
		// histograms are exposed as <name>_bucket/_sum/_count
		produced[o.Name+"_bucket"] = true
		produced[o.Name+"_sum"] = true
		produced[o.Name+"_count"] = true
	}

	queried := []string{
		"container_info", "container_net_latency_seconds", "container_net_tcp_successful_connects_total",
		"container_net_tcp_failed_connects_total", "container_net_tcp_active_connections",
		"container_net_tcp_connection_time_seconds_total", "container_net_tcp_bytes_sent_total",
		"container_net_tcp_bytes_received_total", "container_net_tcp_listen_info", "container_net_tcp_retransmits_total",
		"container_log_messages_total", "container_application_type", "container_resources_cpu_limit_cores",
		"container_resources_cpu_usage_seconds_total", "container_resources_cpu_delay_seconds_total",
		"container_resources_cpu_throttled_seconds_total", "container_resources_memory_rss_bytes",
		"container_resources_memory_cache_bytes", "container_resources_memory_limit_bytes", "container_oom_kills_total",
		"container_restarts_total", "container_resources_disk_size_bytes", "container_resources_disk_used_bytes",
		"container_jvm_info", "container_jvm_heap_size_bytes", "container_jvm_heap_used_bytes",
		"container_jvm_gc_time_seconds", "container_jvm_safepoint_sync_time_seconds", "container_jvm_safepoint_time_seconds",
		"container_python_thread_lock_wait_time_seconds", "ip_to_fqdn",
		"container_rabbitmq_messages_total", "container_nats_messages_total",
		"container_dns_requests_total", "container_dns_requests_duration_seconds_total_bucket",
	}
	for _, p := range []string{"http_requests", "postgres_queries", "redis_queries", "memcached_queries", "mysql_queries",
		"mongo_queries", "kafka_requests", "cassandra_queries", "clickhouse_queries", "zookeeper_requests"} {
		queried = append(queried,
			"container_"+p+"_total",
			"container_"+p+"_duration_seconds_total_sum",
			"container_"+p+"_duration_seconds_total_count",
			"container_"+p+"_duration_seconds_total_bucket",
		)
	}
	for _, q := range queried {
		assert.Truef(t, produced[q], "mainv2 queries %s but the agent doesn't produce it", q)
	}
}

func TestMetricsL7OptionsCoverProtocols(t *testing.T) {
	all := []l7.Protocol{
		l7.ProtocolHTTP, l7.ProtocolPostgres, l7.ProtocolRedis, l7.ProtocolMemcached, l7.ProtocolMysql,
		l7.ProtocolMongo, l7.ProtocolKafka, l7.ProtocolCassandra, l7.ProtocolRabbitmq, l7.ProtocolNats,
		l7.ProtocolDubbo2, l7.ProtocolDNS, l7.ProtocolClickhouse, l7.ProtocolZookeeper, l7.ProtocolFoundationDB,
	}
	for _, p := range all {
		t.Run(p.String(), func(t *testing.T) {
			c, ok := L7Requests[p]
			require.True(t, ok, "no requests counter for %s", p)
			assert.True(t, strings.HasPrefix(c.Name, "container_"), c.Name)
			assert.True(t, strings.HasSuffix(c.Name, "_total"), c.Name)
			assert.NotEmpty(t, c.Help)
			h, ok := L7Latency[p]
			if p == l7.ProtocolRabbitmq || p == l7.ProtocolNats {
				// messaging protocols have no request/response latency
				assert.False(t, ok, "unexpected latency histogram for %s", p)
				return
			}
			require.True(t, ok, "no latency histogram for %s", p)
			assert.Equal(t, strings.TrimSuffix(c.Name, "_total")+"_duration_seconds_total", h.Name)
			assert.NotEmpty(t, h.Help)
		})
	}
	// HTTP2 is folded into HTTP by L7Stats.get, so it must not have its own series names
	_, ok := L7Requests[l7.ProtocolHTTP2]
	assert.False(t, ok)
	_, ok = L7Latency[l7.ProtocolHTTP2]
	assert.False(t, ok)
}

func TestMetricsConstructorHelpers(t *testing.T) {
	cl := prometheus.Labels{"application": "Accounting"}

	c := newCounter("test_counter_total", "help", cl)
	c.Add(2)
	gauge := newGauge("test_gauge", "help", cl)
	gauge.Set(3)
	cv := newCounterVec("test_counter_vec_total", "help", cl, "generation")
	cv.WithLabelValues("Gen0").Inc()
	gv := newGaugeVec("test_gauge_vec", "help", cl, "generation")
	gv.WithLabelValues("LOH").Set(5)

	mfs := metricsTestGather(t, func(ch chan<- prometheus.Metric) {
		c.Collect(ch)
		gauge.Collect(ch)
		cv.Collect(ch)
		gv.Collect(ch)
	})
	assert.Equal(t, 2.0, metricsTestOne(t, mfs, "test_counter_total", map[string]string{"application": "Accounting"}).GetCounter().GetValue())
	assert.Equal(t, 3.0, metricsTestOne(t, mfs, "test_gauge", map[string]string{"application": "Accounting"}).GetGauge().GetValue())
	assert.Equal(t, 1.0, metricsTestOne(t, mfs, "test_counter_vec_total", map[string]string{"application": "Accounting", "generation": "Gen0"}).GetCounter().GetValue())
	assert.Equal(t, 5.0, metricsTestOne(t, mfs, "test_gauge_vec", map[string]string{"application": "Accounting", "generation": "LOH"}).GetGauge().GetValue())
}

func TestMetricsCounterGaugeHelpers(t *testing.T) {
	mfs := metricsTestGather(t, func(ch chan<- prometheus.Metric) {
		ch <- counter(metrics.NetConnectionsFailed, 4, "10.0.0.1:80")
		ch <- gauge(metrics.NetListenInfo, 1, "10.0.0.1:80", "")
	})
	m := metricsTestOne(t, mfs, "container_net_tcp_failed_connects_total", map[string]string{"destination": "10.0.0.1:80"})
	require.NotNil(t, m.GetCounter())
	assert.Equal(t, 4.0, m.GetCounter().GetValue())
	m = metricsTestOne(t, mfs, "container_net_tcp_listen_info", map[string]string{"listen_addr": "10.0.0.1:80", "proxy": ""})
	require.NotNil(t, m.GetGauge())
	assert.Equal(t, 1.0, m.GetGauge().GetValue())

	// a label-count mismatch is a programming error and must be loud
	assert.Panics(t, func() { counter(metrics.NetConnectionsFailed, 1) })
	assert.Panics(t, func() { gauge(metrics.Restarts, 1, "unexpected") })
}
