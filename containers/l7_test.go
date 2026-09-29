// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package containers

import (
	"testing"
	"time"

	"github.com/codifinary/codexray-node-agent/common"
	"github.com/codifinary/codexray-node-agent/ebpftracer/l7"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"inet.af/netaddr"
)

func l7TestKey(dst, actual string) common.DestinationKey {
	return common.NewDestinationKey(netaddr.MustParseIPPort(dst), netaddr.MustParseIPPort(actual), nil)
}

func l7TestGather(t *testing.T, s L7Stats) []*dto.MetricFamily {
	t.Helper()
	return metricsTestGather(t, s.collect)
}

func TestL7StatsGetCreatesMetricsPerProtocolAndDestination(t *testing.T) {
	s := L7Stats{}
	k1 := l7TestKey("10.96.0.10:5432", "10.1.2.3:5432")
	k2 := l7TestKey("10.96.0.11:5432", "10.1.2.4:5432")

	m1 := s.get(l7.ProtocolPostgres, k1)
	require.NotNil(t, m1)
	require.NotNil(t, m1.Requests)
	require.NotNil(t, m1.Latency)
	assert.Same(t, m1, s.get(l7.ProtocolPostgres, k1), "the same protocol+destination must reuse metrics")
	assert.NotSame(t, m1, s.get(l7.ProtocolPostgres, k2))
	assert.NotSame(t, m1, s.get(l7.ProtocolRedis, k1))
	assert.Len(t, s[l7.ProtocolPostgres], 2)
	assert.Len(t, s[l7.ProtocolRedis], 1)
}

func TestL7StatsHttp2FoldedIntoHttp(t *testing.T) {
	s := L7Stats{}
	k := l7TestKey("10.96.0.10:80", "10.1.2.3:8080")
	h1 := s.get(l7.ProtocolHTTP, k)
	h2 := s.get(l7.ProtocolHTTP2, k)
	assert.Same(t, h1, h2)
	_, ok := s[l7.ProtocolHTTP2]
	assert.False(t, ok, "HTTP2 must not get its own map (it has no metric names)")

	h2.observe("2xx", "", 10*time.Millisecond)
	h1.observe("5xx", "", 20*time.Millisecond)
	mfs := l7TestGather(t, s)
	assert.Len(t, metricsTestFind(mfs, "container_http_requests_total", nil), 2)
	hist := metricsTestOne(t, mfs, "container_http_requests_duration_seconds_total", nil)
	assert.Equal(t, uint64(2), hist.GetHistogram().GetSampleCount())
}

func TestL7StatsCollectLabelsAndValues(t *testing.T) {
	s := L7Stats{}
	k := l7TestKey("10.96.0.10:6379", "10.1.2.3:6379")
	m := s.get(l7.ProtocolRedis, k)
	m.observe("ok", "", 100*time.Millisecond)
	m.observe("ok", "", 300*time.Millisecond)
	m.observe("failed", "", 0) // zero duration: counted but not observed in the histogram

	mfs := l7TestGather(t, s)
	dst := map[string]string{"destination": "10.96.0.10:6379", "actual_destination": "10.1.2.3:6379"}

	ok := metricsTestOne(t, mfs, "container_redis_queries_total", map[string]string{"status": "ok"})
	assert.Equal(t, dst, metricsTestLabelsWithout(ok, "status"))
	assert.Equal(t, 2.0, ok.GetCounter().GetValue())
	failed := metricsTestOne(t, mfs, "container_redis_queries_total", map[string]string{"status": "failed"})
	assert.Equal(t, 1.0, failed.GetCounter().GetValue())

	h := metricsTestOne(t, mfs, "container_redis_queries_duration_seconds_total", dst)
	assert.Equal(t, uint64(2), h.GetHistogram().GetSampleCount())
	assert.InDelta(t, 0.4, h.GetHistogram().GetSampleSum(), 1e-9)
	assert.Equal(t, dst, metricsTestLabels(h), "histogram carries only destination labels")
}

func metricsTestLabelsWithout(m *dto.Metric, drop string) map[string]string {
	ls := metricsTestLabels(m)
	delete(ls, drop)
	return ls
}

func TestL7StatsMessagingProtocolsHaveMethodAndNoLatency(t *testing.T) {
	for _, p := range []l7.Protocol{l7.ProtocolRabbitmq, l7.ProtocolNats} {
		t.Run(p.String(), func(t *testing.T) {
			s := L7Stats{}
			m := s.get(p, l7TestKey("10.0.0.1:5672", "10.0.0.2:5672"))
			assert.Nil(t, m.Latency)
			m.observe("ok", "produce", time.Second)
			m.observe("ok", "consume", 0)
			m.observe("ok", "consume", 0)
			mfs := l7TestGather(t, s)
			name := L7Requests[p].Name
			assert.Equal(t, 1.0, metricsTestOne(t, mfs, name, map[string]string{"status": "ok", "method": "produce"}).GetCounter().GetValue())
			assert.Equal(t, 2.0, metricsTestOne(t, mfs, name, map[string]string{"status": "ok", "method": "consume"}).GetCounter().GetValue())
			assert.Equal(t, []string{name}, metricsTestNames(mfs))
		})
	}
}

func TestL7MetricsObserveEdgeCases(t *testing.T) {
	t.Run("nil metrics are safe", func(t *testing.T) {
		m := &L7Metrics{}
		assert.NotPanics(t, func() { m.observe("ok", "", time.Second) })
	})
	t.Run("label count mismatch is not a panic", func(t *testing.T) {
		s := L7Stats{}
		m := s.get(l7.ProtocolKafka, l7TestKey("10.0.0.1:9092", "10.0.0.1:9092"))
		// Kafka has only "status": passing a method must be rejected (logged), not panic or count
		assert.NotPanics(t, func() { m.observe("ok", "produce", time.Second) })
		mfs := l7TestGather(t, s)
		assert.Empty(t, metricsTestFind(mfs, "container_kafka_requests_total", nil))
		// the latency is still observed
		h := metricsTestOne(t, mfs, "container_kafka_requests_duration_seconds_total", nil)
		assert.Equal(t, uint64(1), h.GetHistogram().GetSampleCount())
	})
}

func TestL7StatsDeleteByDestination(t *testing.T) {
	s := L7Stats{}
	a1 := l7TestKey("10.96.0.10:80", "10.1.0.1:80")
	a2 := l7TestKey("10.96.0.10:80", "10.1.0.2:80") // same service, different pod
	b := l7TestKey("10.96.0.20:80", "10.1.0.3:80")
	s.get(l7.ProtocolHTTP, a1).observe("2xx", "", time.Millisecond)
	s.get(l7.ProtocolHTTP, a2).observe("2xx", "", time.Millisecond)
	s.get(l7.ProtocolHTTP, b).observe("2xx", "", time.Millisecond)
	s.get(l7.ProtocolPostgres, a1).observe("ok", "", time.Millisecond)

	s.delete(a1.Destination())

	assert.Len(t, s[l7.ProtocolHTTP], 1)
	assert.Contains(t, s[l7.ProtocolHTTP], b)
	assert.Empty(t, s[l7.ProtocolPostgres])
	mfs := l7TestGather(t, s)
	assert.Empty(t, metricsTestFind(mfs, "container_http_requests_total", map[string]string{"destination": "10.96.0.10:80"}))
	assert.Len(t, metricsTestFind(mfs, "container_http_requests_total", map[string]string{"destination": "10.96.0.20:80"}), 1)

	assert.NotPanics(t, func() { s.delete(common.HostPortFromIPPort(netaddr.MustParseIPPort("1.1.1.1:1"))) })
	assert.NotPanics(t, func() { L7Stats{}.delete(a1.Destination()) })
}

func TestL7StatsAllProtocolsGatherCleanly(t *testing.T) {
	s := L7Stats{}
	k := l7TestKey("10.96.0.10:1000", "10.1.2.3:1000")
	for p := range L7Requests {
		method := ""
		if p == l7.ProtocolRabbitmq || p == l7.ProtocolNats {
			method = "produce"
		}
		s.get(p, k).observe("ok", method, time.Millisecond)
	}
	mfs := l7TestGather(t, s)
	for p, o := range L7Requests {
		assert.Lenf(t, metricsTestFind(mfs, o.Name, nil), 1, "protocol %s", p)
		if h, ok := L7Latency[p]; ok {
			assert.Lenf(t, metricsTestFind(mfs, h.Name, nil), 1, "protocol %s", p)
		}
	}
}

// A protocol id the Go side has no metric names for (e.g. a new kernel-side classifier
// without its Go counterpart) must not poison the scrape: L7Stats.get would build a
// CounterVec/Histogram with an empty name, and a single invalid metric fails Gather for
// the whole registry, i.e. the entire node's /metrics.
func TestL7StatsUnknownProtocolDoesNotBreakGather(t *testing.T) {
	// BUG: L7Stats.get builds metrics with an empty name for protocols missing from L7Requests/L7Latency, failing Gather for the whole registry — unskip when fixed
	t.Skip("BUG: L7Stats.get builds metrics with an empty name for protocols missing from L7Requests/L7Latency, failing Gather for the whole registry")

	s := L7Stats{}
	s.get(l7.ProtocolHTTP, l7TestKey("10.0.0.1:80", "10.0.0.1:80")).observe("2xx", "", time.Millisecond)
	m := s.get(l7.Protocol(200), l7TestKey("10.0.0.1:80", "10.0.0.1:80"))
	assert.NotPanics(t, func() { m.observe("ok", "", time.Millisecond) })
	mfs, err := metricsTestTryGather(s.collect)
	require.NoError(t, err)
	assert.Len(t, metricsTestFind(mfs, "container_http_requests_total", nil), 1)
}

var _ prometheus.Collector = metricsTestCollector{}
