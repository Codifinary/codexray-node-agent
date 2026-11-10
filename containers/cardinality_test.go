// Copyright Codexray
// SPDX-License-Identifier: Apache-2.0

package containers

import (
	"fmt"
	"testing"
	"time"

	"github.com/codifinary/codexray-node-agent/common"
	"github.com/codifinary/codexray-node-agent/ebpftracer/l7"
	"github.com/codifinary/codexray-node-agent/flags"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"inet.af/netaddr"
)

func TestDNSDomainCap(t *testing.T) {
	prev := common.DNSMaxDomainsPerContainer
	defer func() { common.DNSMaxDomainsPerContainer = prev }()
	common.DNSMaxDomainsPerContainer = 3

	c := &Container{id: "/k8s/test"}
	for i := 0; i < 3; i++ {
		d := fmt.Sprintf("svc-%d.default.svc.cluster.local", i)
		assert.Equal(t, d, c.dnsDomainLabel(d))
	}
	assert.Equal(t, common.DNSOverflowDomain, c.dnsDomainLabel("new.example.com"))
	assert.Equal(t, common.DNSOverflowDomain, c.dnsDomainLabel("other.example.com"))
	// Domains seen before the cap keep their own series.
	assert.Equal(t, "svc-1.default.svc.cluster.local", c.dnsDomainLabel("svc-1.default.svc.cluster.local"))

	common.DNSMaxDomainsPerContainer = 0
	assert.Equal(t, "unlimited.example.com", c.dnsDomainLabel("unlimited.example.com"))
}

func histogramBuckets(t *testing.T, h prometheus.Histogram) int {
	m := &dto.Metric{}
	require.NoError(t, h.Write(m))
	return len(m.GetHistogram().GetBucket())
}

func TestL7LatencyBuckets(t *testing.T) {
	prev := common.L7LatencyBuckets
	defer func() { common.L7LatencyBuckets = prev }()
	key := common.NewDestinationKey(netaddr.MustParseIPPort("10.43.0.10:80"), netaddr.MustParseIPPort("10.42.0.5:80"), nil)

	common.L7LatencyBuckets = prometheus.DefBuckets
	assert.Equal(t, len(prometheus.DefBuckets), histogramBuckets(t, L7Stats{}.get(l7.ProtocolHTTP, key).Latency))

	common.L7LatencyBuckets = []float64{0.01, 0.05, 0.1, 0.5, 1, 5}
	assert.Equal(t, 6, histogramBuckets(t, L7Stats{}.get(l7.ProtocolHTTP, key).Latency))
}

func TestL7ActualDestinationDrop(t *testing.T) {
	prev := common.DropActualDestination
	defer func() { common.DropActualDestination = prev }()
	svc := netaddr.MustParseIPPort("10.43.0.10:80")
	keys := []common.DestinationKey{
		common.NewDestinationKey(svc, netaddr.MustParseIPPort("10.42.0.5:80"), nil),
		common.NewDestinationKey(svc, netaddr.MustParseIPPort("10.42.1.6:80"), nil),
		common.NewDestinationKey(svc, netaddr.MustParseIPPort("10.42.2.7:80"), nil),
	}
	count := func() int {
		s := L7Stats{}
		for _, k := range keys {
			s.get(l7.ProtocolHTTP, k.ForMetrics()).observe("200", "", 1)
		}
		ch := make(chan prometheus.Metric, 100)
		s.collect(ch)
		close(ch)
		n := 0
		for range ch {
			n++
		}
		return n
	}
	common.DropActualDestination = false
	assert.Equal(t, 6, count()) // 3 backends x (requests + latency)
	common.DropActualDestination = true
	assert.Equal(t, 2, count())
}

func TestMinContainerAgeScope(t *testing.T) {
	prevAge, prevScope := *flags.MinContainerAge, *flags.MinContainerAgeAppliesTo
	defer func() { *flags.MinContainerAge, *flags.MinContainerAgeAppliesTo = prevAge, prevScope }()

	now := time.Now()
	young := &Container{id: "/k8s/job", startedAt: now.Add(-5 * time.Second)}
	old := &Container{id: "/k8s/web", startedAt: now.Add(-time.Hour)}

	*flags.MinContainerAge = 30 * time.Second
	assert.True(t, young.youngerThanMinAge(now))
	assert.False(t, old.youngerThanMinAge(now))

	*flags.MinContainerAgeAppliesTo = "metrics"
	assert.False(t, minAgeGatesLogsAndTraces(), "default: logs and traces of young containers are kept")
	*flags.MinContainerAgeAppliesTo = "all"
	assert.True(t, minAgeGatesLogsAndTraces())

	*flags.MinContainerAge = 0
	assert.False(t, young.youngerThanMinAge(now))
	assert.False(t, minAgeGatesLogsAndTraces(), "no gating when min-container-age is disabled")
}
