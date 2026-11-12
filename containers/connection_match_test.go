// Copyright Codexray
// SPDX-License-Identifier: Apache-2.0

package containers

import (
	"testing"
	"time"

	"github.com/codifinary/codexray-node-agent/common"
	"github.com/codifinary/codexray-node-agent/ebpftracer"
	"github.com/codifinary/codexray-node-agent/ebpftracer/l7"
	"github.com/codifinary/codexray-node-agent/flags"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"
	"inet.af/netaddr"
)

func connTestContainer() *Container {
	return &Container{
		id:                       "/system.slice/client.service",
		processes:                map[uint32]*Process{42: {Pid: 42}},
		connectionStats:          map[common.DestinationKey]*ConnectionStats{},
		failedConnectionAttempts: map[common.HostPort]int64{},
		lastConnectionAttempts:   map[common.HostPort]time.Time{},
		activeConnections:        map[ConnectionKey]*ActiveConnection{},
		connectionsByPidFd:       map[PidFd]*ActiveConnection{},
		replacedConnections:      map[pidFdTs]*ActiveConnection{},
		l7Stats:                  L7Stats{},
		dnsStats:                 &L7Metrics{},
		registry:                 &Registry{},
	}
}

func httpRequests(t *testing.T, c *Container) map[string]float64 {
	res := map[string]float64{}
	for _, byDst := range c.l7Stats {
		for _, m := range byDst {
			ch := make(chan prometheus.Metric, 10)
			m.Requests.Collect(ch)
			close(ch)
			for metric := range ch {
				pb := &dto.Metric{}
				require.NoError(t, metric.Write(pb))
				for _, l := range pb.Label {
					if l.GetName() == "status" {
						res[l.GetValue()] += pb.Counter.GetValue()
					}
				}
			}
		}
	}
	return res
}

// A client that opens one connection per request reuses the same fd for the next
// connection. The L7 event of the previous connection is read from a different perf
// buffer and can be processed after the next connection's open event. It must still be
// attributed to its own connection instead of being dropped.
func TestL7EventAfterFdReuse(t *testing.T) {
	prev := flags.Telemetry
	defer func() { flags.Telemetry = prev }()
	flags.Telemetry.L7Metrics = true
	flags.Telemetry.L7Traces = false

	c := connTestContainer()
	src1 := netaddr.MustParseIPPort("10.0.0.1:40001")
	src2 := netaddr.MustParseIPPort("10.0.0.1:40002")
	dst := netaddr.MustParseIPPort("10.0.0.2:8080")
	req := func(status int) *l7.RequestData {
		return &l7.RequestData{Protocol: l7.ProtocolHTTP, Status: l7.Status(status), Duration: time.Millisecond,
			Payload: []byte("GET /fast HTTP/1.1\r\nHost: x\r\n\r\n")}
	}

	c.onConnectionOpen(42, 5, src1, dst, dst, 1000, false, time.Millisecond)
	c.onConnectionClose(ebpftracer.Event{Pid: 42, Fd: 5, Timestamp: 1000})
	c.onConnectionOpen(42, 5, src2, dst, dst, 2000, false, time.Millisecond) // fd 5 reused

	c.onL7Request(42, 5, 1000, req(200)) // late event of the first connection
	c.onL7Request(42, 5, 2000, req(200)) // event of the current connection
	c.onL7Request(42, 5, 3000, req(200)) // unknown connection: dropped
	require.Equal(t, map[string]float64{"2xx": 2}, httpRequests(t, c))

	require.Len(t, c.replacedConnections, 1)
}

// An L7 event processed before its own connection's open event is replayed when the
// open event arrives, and dropped after pendingL7TTL if it never does.
func TestL7EventBeforeConnectionOpen(t *testing.T) {
	prev := flags.Telemetry
	defer func() { flags.Telemetry = prev }()
	flags.Telemetry.L7Metrics = true
	flags.Telemetry.L7Traces = false

	c := connTestContainer()
	src := netaddr.MustParseIPPort("10.0.0.1:40001")
	dst := netaddr.MustParseIPPort("10.0.0.2:8080")
	req := &l7.RequestData{Protocol: l7.ProtocolHTTP, Status: 200, Duration: time.Millisecond,
		Payload: []byte("GET /fast HTTP/1.1\r\nHost: x\r\n\r\n")}

	c.onL7Request(42, 7, 5000, req) // arrives first
	c.onL7Request(42, 8, 6000, req) // its connection never shows up
	require.Equal(t, 2, c.pendingL7Count)
	require.Empty(t, c.l7Stats)

	c.onConnectionOpen(42, 7, src, dst, dst, 5000, false, time.Millisecond)
	require.Equal(t, map[string]float64{"2xx": 1}, httpRequests(t, c))
	require.Equal(t, 1, c.pendingL7Count)

	c.expirePendingL7(time.Now().Add(pendingL7TTL + time.Second))
	require.Zero(t, c.pendingL7Count)
	require.Empty(t, c.pendingL7)
}

// Deferred events are capped per container.
func TestPendingL7Cap(t *testing.T) {
	c := connTestContainer()
	req := &l7.RequestData{Protocol: l7.ProtocolHTTP, Status: 200}
	for i := 0; i < maxPendingL7PerContainer+50; i++ {
		c.onL7Request(42, uint64(i), uint64(1000+i), req)
	}
	require.Equal(t, maxPendingL7PerContainer, c.pendingL7Count)
}
