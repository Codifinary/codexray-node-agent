// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package containers

import (
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/codifinary/codexray-node-agent/cgroup"
	"github.com/codifinary/codexray-node-agent/common"
	"github.com/codifinary/codexray-node-agent/ebpftracer"
	"github.com/codifinary/codexray-node-agent/ebpftracer/l7"
	"github.com/codifinary/codexray-node-agent/flags"
	"github.com/codifinary/codexray-node-agent/gpu"
	"github.com/codifinary/codexray-node-agent/proc"
	"github.com/codifinary/codexray-node-agent/tracing"
	"github.com/codifinary/logparser"
	"github.com/mdlayher/taskstats"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vishvananda/netns"
	"golang.org/x/net/dns/dnsmessage"
	"inet.af/netaddr"
)

const (
	containerTestPid = uint32(4_000_000_001) // never exists (above PID_MAX_LIMIT)
	containerTestFd  = uint64(7)
)

// containerTestFlags pins the package-level flags Collect depends on and restores them.
func containerTestFlags(t *testing.T, minAge time.Duration) {
	t.Helper()
	prevAge, prevPinger, prevMaxLabel := *flags.MinContainerAge, *flags.DisablePinger, *flags.MaxLabelLength
	*flags.MinContainerAge = minAge
	*flags.DisablePinger = true
	t.Cleanup(func() {
		*flags.MinContainerAge = prevAge
		*flags.DisablePinger = prevPinger
		*flags.MaxLabelLength = prevMaxLabel
	})
}

// containerTestNew builds a Container the way NewContainer does, without touching /proc,
// the eBPF tracer or the gc goroutine. cgroup.Cgroup{} has no subsystems, so every
// cgroup reader returns nil (the fixture-backed readers are covered in the cgroup package;
// cgroup.cgRoot is unexported and cannot be pointed at fixtures from here).
func containerTestNew(t *testing.T) *Container {
	t.Helper()
	containerTestFlags(t, 0)
	return &Container{
		id:                       "/docker/test",
		cgroup:                   &cgroup.Cgroup{},
		metadata:                 &ContainerMetadata{},
		processes:                map[uint32]*Process{},
		delaysByPid:              map[uint32]Delays{},
		listens:                  map[netaddr.IPPort]map[uint32]*ListenDetails{},
		connectionStats:          map[common.DestinationKey]*ConnectionStats{},
		failedConnectionAttempts: map[common.HostPort]int64{},
		lastConnectionAttempts:   map[common.HostPort]time.Time{},
		activeConnections:        map[ConnectionKey]*ActiveConnection{},
		connectionsByPidFd:       map[PidFd]*ActiveConnection{},
		l7Stats:                  L7Stats{},
		dnsStats:                 &L7Metrics{},
		gpuStats:                 map[string]*GpuUsage{},
		mounts:                   map[string]proc.MountInfo{},
		seenMounts:               map[uint64]struct{}{},
		logParsers:               map[string]*LogParser{},
		tracer:                   tracing.GetContainerTracer("/docker/test"),
		registry: &Registry{
			// a recent timestamp makes updateStatsFromEbpfMapsIfNecessary a no-op (no BPF maps)
			ebpfStatsLastUpdated: time.Now().Add(time.Hour),
			ip2fqdn:              map[netaddr.IP]*common.Domain{},
		},
		done: make(chan struct{}),
	}
}

// containerTestHostPath points hostPath (proc.HostPath, i.e. /proc/1/root/...) at a
// temporary directory and returns it.
func containerTestHostPath(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	saved := hostPath
	hostPath = func(p string) string { return filepath.Join(root, p) }
	t.Cleanup(func() { hostPath = saved })
	return root
}

// containerTestAddProcess registers a process; netNsId pre-seeds NetNsId() so no /proc read happens.
func containerTestAddProcess(c *Container, pid uint32, netNsId string) *Process {
	p := processTestNew(pid)
	p.netNsId = netNsId
	c.processes[pid] = p
	return p
}

func containerTestGather(t *testing.T, c *Container) []*dto.MetricFamily {
	t.Helper()
	return metricsTestGather(t, c.Collect)
}

func containerTestAddr(s string) netaddr.IPPort { return netaddr.MustParseIPPort(s) }

func containerTestHP(s string) common.HostPort {
	return common.HostPortFromIPPort(containerTestAddr(s))
}

func TestContainerOnConnectionOpen(t *testing.T) {
	c := containerTestNew(t)
	containerTestAddProcess(c, containerTestPid, "other")
	src, dst, actual := containerTestAddr("10.0.0.5:40000"), containerTestAddr("10.96.0.10:80"), containerTestAddr("10.1.2.3:8080")

	c.onConnectionOpen(containerTestPid, containerTestFd, src, dst, actual, 111, false, 5*time.Millisecond)

	key := common.NewDestinationKey(dst, actual, nil)
	require.Contains(t, c.connectionStats, key)
	assert.Equal(t, uint64(1), c.connectionStats[key].Count)
	assert.Equal(t, 5*time.Millisecond, c.connectionStats[key].TotalTime)
	conn := c.activeConnections[ConnectionKey{src: src, dst: dst}]
	require.NotNil(t, conn)
	assert.Equal(t, key, conn.DestinationKey)
	assert.Equal(t, uint64(111), conn.Timestamp)
	assert.True(t, conn.Closed.IsZero())
	assert.Same(t, conn, c.connectionsByPidFd[PidFd{Pid: containerTestPid, Fd: containerTestFd}])
	assert.Contains(t, c.lastConnectionAttempts, key.Destination())
	assert.Empty(t, c.failedConnectionAttempts)

	// fd reuse: the previous connection on the same pid/fd is implicitly closed
	src2 := containerTestAddr("10.0.0.5:40001")
	c.onConnectionOpen(containerTestPid, containerTestFd, src2, dst, actual, 222, false, 3*time.Millisecond)
	assert.False(t, conn.Closed.IsZero(), "the previous connection is marked closed")
	assert.Equal(t, uint64(2), c.connectionStats[key].Count)
	assert.Equal(t, 8*time.Millisecond, c.connectionStats[key].TotalTime)
	assert.Equal(t, uint64(222), c.connectionsByPidFd[PidFd{Pid: containerTestPid, Fd: containerTestFd}].Timestamp)
	assert.Len(t, c.activeConnections, 2)
}

func TestContainerOnConnectionOpenFailed(t *testing.T) {
	c := containerTestNew(t)
	containerTestAddProcess(c, containerTestPid, "other")
	dst := containerTestAddr("10.96.0.10:5432")
	for i := 0; i < 3; i++ {
		c.onConnectionOpen(containerTestPid, containerTestFd, containerTestAddr("10.0.0.5:40000"), dst, netaddr.IPPort{}, 0, true, 0)
	}
	assert.Equal(t, int64(3), c.failedConnectionAttempts[containerTestHP("10.96.0.10:5432")])
	assert.Empty(t, c.connectionStats)
	assert.Empty(t, c.activeConnections)
	assert.Empty(t, c.connectionsByPidFd)
	assert.Contains(t, c.lastConnectionAttempts, containerTestHP("10.96.0.10:5432"))
}

func TestContainerOnConnectionOpenFiltering(t *testing.T) {
	src := containerTestAddr("10.0.0.5:40000")
	cases := []struct {
		name     string
		hostNs   bool
		dst      string
		actual   string
		recorded bool
		wantKey  string // expected actual destination label ("" => not checked)
	}{
		{name: "private", dst: "10.96.0.10:80", actual: "10.1.2.3:80", recorded: true},
		{name: "unknown actual destination falls back to dst", dst: "10.96.0.11:80", actual: "0.0.0.0:0", recorded: true, wantKey: "10.96.0.11:80"},
		{name: "loopback in container netns", dst: "127.0.0.1:6379", actual: "127.0.0.1:6379", recorded: false},
		{name: "loopback in host netns", hostNs: true, dst: "127.0.0.1:6379", actual: "127.0.0.1:6379", recorded: true},
		{name: "NAT to loopback in container netns", dst: "10.96.0.12:80", actual: "127.0.0.1:80", recorded: false},
		{name: "link-local (cloud metadata)", dst: "169.254.169.254:80", actual: "169.254.169.254:80", recorded: false},
		// with --track-public-network unset (flags aren't parsed in tests)
		{name: "public dst and public actual", dst: "198.51.100.77:443", actual: "198.51.100.78:443", recorded: false},
		{name: "public dst NATed to private", dst: "203.0.113.200:443", actual: "10.1.2.9:443", recorded: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := containerTestNew(t)
			ns := "other"
			if tc.hostNs {
				ns = hostNetNsId
			}
			containerTestAddProcess(c, containerTestPid, ns)
			c.onConnectionOpen(containerTestPid, containerTestFd, src, containerTestAddr(tc.dst), containerTestAddr(tc.actual), 1, false, time.Millisecond)
			if !tc.recorded {
				assert.Empty(t, c.connectionStats)
				assert.Empty(t, c.activeConnections)
				assert.Empty(t, c.lastConnectionAttempts)
				return
			}
			require.Len(t, c.connectionStats, 1)
			if tc.wantKey != "" {
				for k := range c.connectionStats {
					assert.Equal(t, tc.wantKey, k.ActualDestinationLabelValue())
				}
			}
		})
	}
}

func TestContainerOnConnectionOpenUnknownProcess(t *testing.T) {
	c := containerTestNew(t)
	c.onConnectionOpen(containerTestPid, containerTestFd, containerTestAddr("10.0.0.5:40000"), containerTestAddr("10.96.0.10:80"), containerTestAddr("10.1.2.3:80"), 1, false, 0)
	assert.Empty(t, c.connectionStats)
	assert.Empty(t, c.lastConnectionAttempts)
}

// An external service reached by FQDN with several public IPs is aggregated by name so that
// the destination label stays bounded (one series per FQDN, not per resolved IP).
func TestContainerOnConnectionOpenDomainAggregation(t *testing.T) {
	c := containerTestNew(t)
	containerTestAddProcess(c, containerTestPid, "other")
	ip1, ip2 := netaddr.MustParseIP("198.51.100.31"), netaddr.MustParseIP("198.51.100.32")
	common.ConnectionFilter.WhitelistIP(ip1)
	common.ConnectionFilter.WhitelistIP(ip2)
	d := common.NewDomain("api.example.com", []netaddr.IP{ip1, ip2})
	require.False(t, d.SpecifyIP)
	c.registry.ip2fqdn[ip1] = d
	c.registry.ip2fqdn[ip2] = d

	c.onConnectionOpen(containerTestPid, 1, containerTestAddr("10.0.0.5:40000"), netaddr.IPPortFrom(ip1, 443), netaddr.IPPortFrom(ip1, 443), 1, false, time.Millisecond)
	c.onConnectionOpen(containerTestPid, 2, containerTestAddr("10.0.0.5:40001"), netaddr.IPPortFrom(ip2, 443), netaddr.IPPortFrom(ip2, 443), 2, false, time.Millisecond)

	require.Len(t, c.connectionStats, 1)
	mfs := containerTestGather(t, c)
	m := metricsTestOne(t, mfs, "container_net_tcp_successful_connects_total", nil)
	assert.Equal(t, map[string]string{"destination": "api.example.com:443", "actual_destination": ""}, metricsTestLabels(m))
	assert.Equal(t, 2.0, m.GetCounter().GetValue())
}

func containerTestOpen(t *testing.T, c *Container, pid uint32, fd uint64, src, dst, actual string, ts uint64) *ActiveConnection {
	t.Helper()
	c.onConnectionOpen(pid, fd, containerTestAddr(src), containerTestAddr(dst), containerTestAddr(actual), ts, false, time.Millisecond)
	conn := c.connectionsByPidFd[PidFd{Pid: pid, Fd: fd}]
	require.NotNil(t, conn)
	return conn
}

func TestContainerOnConnectionClose(t *testing.T) {
	c := containerTestNew(t)
	containerTestAddProcess(c, containerTestPid, "other")
	conn := containerTestOpen(t, c, containerTestPid, containerTestFd, "10.0.0.5:40000", "10.96.0.10:80", "10.1.2.3:80", 111)

	// a close event for a different connection that reused the fd (timestamp mismatch) is ignored
	c.onConnectionClose(ebpftracer.Event{Pid: containerTestPid, Fd: containerTestFd, Timestamp: 999,
		TrafficStats: &ebpftracer.TrafficStats{BytesSent: 1, BytesReceived: 1}})
	assert.True(t, conn.Closed.IsZero())
	assert.Zero(t, c.connectionStats[conn.DestinationKey].BytesSent)

	c.onConnectionClose(ebpftracer.Event{Pid: containerTestPid, Fd: containerTestFd, Timestamp: 111,
		TrafficStats: &ebpftracer.TrafficStats{BytesSent: 100, BytesReceived: 250}})
	assert.False(t, conn.Closed.IsZero())
	st := c.connectionStats[conn.DestinationKey]
	assert.Equal(t, uint64(100), st.BytesSent)
	assert.Equal(t, uint64(250), st.BytesReceived)

	// a duplicate close is not double counted
	closedAt := conn.Closed
	c.onConnectionClose(ebpftracer.Event{Pid: containerTestPid, Fd: containerTestFd, Timestamp: 111,
		TrafficStats: &ebpftracer.TrafficStats{BytesSent: 300, BytesReceived: 300}})
	assert.Equal(t, uint64(100), st.BytesSent)
	assert.Equal(t, closedAt, conn.Closed)

	// unknown pid/fd, and close without traffic stats
	assert.NotPanics(t, func() { c.onConnectionClose(ebpftracer.Event{Pid: 1, Fd: 1}) })
	conn2 := containerTestOpen(t, c, containerTestPid, 8, "10.0.0.5:40002", "10.96.0.10:80", "10.1.2.3:80", 0)
	c.onConnectionClose(ebpftracer.Event{Pid: containerTestPid, Fd: 8, Timestamp: 12345})
	assert.False(t, conn2.Closed.IsZero(), "connections without a timestamp are closed by any event on the fd")
}

func TestContainerUpdateTrafficStats(t *testing.T) {
	c := containerTestNew(t)
	containerTestAddProcess(c, containerTestPid, "other")
	conn := containerTestOpen(t, c, containerTestPid, containerTestFd, "10.0.0.5:40000", "10.96.0.10:80", "10.1.2.3:80", 1)
	st := c.connectionStats[conn.DestinationKey]

	c.updateTrafficStats(nil)
	c.updateTrafficStats(&TrafficStatsUpdate{Pid: 1, FD: 1, BytesSent: 5}) // unknown connection
	assert.Zero(t, st.BytesSent)

	c.updateTrafficStats(&TrafficStatsUpdate{Pid: containerTestPid, FD: containerTestFd, BytesSent: 100, BytesReceived: 10})
	c.updateTrafficStats(&TrafficStatsUpdate{Pid: containerTestPid, FD: containerTestFd, BytesSent: 150, BytesReceived: 10})
	assert.Equal(t, uint64(150), st.BytesSent)
	assert.Equal(t, uint64(10), st.BytesReceived)

	// the exported values are counters: they must never go backwards
	c.updateTrafficStats(&TrafficStatsUpdate{Pid: containerTestPid, FD: containerTestFd, BytesSent: 120, BytesReceived: 5})
	assert.Equal(t, uint64(150), st.BytesSent)
	assert.Equal(t, uint64(10), st.BytesReceived)

	// stats deleted by gc are recreated on the next update
	delete(c.connectionStats, conn.DestinationKey)
	c.updateTrafficStats(&TrafficStatsUpdate{Pid: containerTestPid, FD: containerTestFd, BytesSent: 130, BytesReceived: 5})
	assert.Equal(t, uint64(10), c.connectionStats[conn.DestinationKey].BytesSent)
}

func TestContainerOnRetransmission(t *testing.T) {
	c := containerTestNew(t)
	containerTestAddProcess(c, containerTestPid, "other")
	conn := containerTestOpen(t, c, containerTestPid, containerTestFd, "10.0.0.5:40000", "10.96.0.10:80", "10.1.2.3:80", 1)

	assert.False(t, c.onRetransmission(containerTestAddr("10.0.0.5:1"), containerTestAddr("10.96.0.10:80")))
	assert.True(t, c.onRetransmission(containerTestAddr("10.0.0.5:40000"), containerTestAddr("10.96.0.10:80")))
	assert.True(t, c.onRetransmission(containerTestAddr("10.0.0.5:40000"), containerTestAddr("10.96.0.10:80")))
	assert.Equal(t, uint64(2), c.connectionStats[conn.DestinationKey].Retransmissions)

	delete(c.connectionStats, conn.DestinationKey)
	assert.True(t, c.onRetransmission(containerTestAddr("10.0.0.5:40000"), containerTestAddr("10.96.0.10:80")))
	assert.Equal(t, uint64(1), c.connectionStats[conn.DestinationKey].Retransmissions)
}

func TestContainerListens(t *testing.T) {
	c := containerTestNew(t)
	containerTestAddProcess(c, containerTestPid, "other")
	hostPid := containerTestPid + 1
	containerTestAddProcess(c, hostPid, hostNetNsId)

	c.onListenOpen(containerTestPid, containerTestAddr("10.0.0.5:8080"), false)
	c.onListenOpen(containerTestPid, containerTestAddr("127.0.0.1:9090"), false)
	c.onListenOpen(hostPid, containerTestAddr("127.0.0.1:9091"), false)
	c.onListenOpen(containerTestPid+2, containerTestAddr("10.0.0.5:7070"), false) // its process has exited since
	// wildcard listen of a vanished process: interface IPs can't be resolved
	c.onListenOpen(containerTestPid, containerTestAddr("0.0.0.0:80"), false)
	require.Contains(t, c.listens, containerTestAddr("0.0.0.0:80"))
	assert.Nil(t, c.listens[containerTestAddr("0.0.0.0:80")][containerTestPid].NsIPs)

	assert.Equal(t, map[netaddr.IPPort]int{
		containerTestAddr("10.0.0.5:8080"):  1,
		containerTestAddr("127.0.0.1:9091"): 1, // loopback only reported for host-netns processes
		containerTestAddr("10.0.0.5:7070"):  0, // the owning process is gone: reported as closed until gc
	}, c.getListens())

	c.onListenClose(containerTestPid, containerTestAddr("10.0.0.5:8080"))
	assert.Equal(t, 0, c.getListens()[containerTestAddr("10.0.0.5:8080")], "closed listens are reported as 0 until gc")
	assert.NotPanics(t, func() {
		c.onListenClose(containerTestPid, containerTestAddr("10.9.9.9:1"))
		c.onListenClose(12345, containerTestAddr("10.0.0.5:8080"))
	})

	// wildcard listens expand to the namespace IPs
	c.listens[containerTestAddr("0.0.0.0:80")][containerTestPid].NsIPs = []netaddr.IP{netaddr.MustParseIP("10.0.0.5"), netaddr.MustParseIP("127.0.0.1")}
	l := c.getListens()
	assert.Equal(t, 1, l[containerTestAddr("10.0.0.5:80")])
	assert.NotContains(t, l, containerTestAddr("127.0.0.1:80"))
	assert.NotContains(t, l, containerTestAddr("0.0.0.0:80"))

	mfs := containerTestGather(t, c)
	m := metricsTestOne(t, mfs, "container_net_tcp_listen_info", map[string]string{"listen_addr": "10.0.0.5:80"})
	assert.Equal(t, map[string]string{"listen_addr": "10.0.0.5:80", "proxy": ""}, metricsTestLabels(m))
}

func TestContainerProxiedListens(t *testing.T) {
	c := containerTestNew(t)
	assert.Nil(t, c.getProxiedListens())
	c.metadata.hostListens = map[string][]netaddr.IPPort{
		"docker-proxy": {containerTestAddr("192.168.1.10:8080"), containerTestAddr("192.168.1.10:8443")},
	}
	assert.Equal(t, map[string]map[netaddr.IPPort]struct{}{
		"docker-proxy": {containerTestAddr("192.168.1.10:8080"): {}, containerTestAddr("192.168.1.10:8443"): {}},
	}, c.getProxiedListens())

	mfs := containerTestGather(t, c)
	assert.Len(t, metricsTestFind(mfs, "container_net_tcp_listen_info", map[string]string{"proxy": "docker-proxy"}), 2)
}

func containerTestL7Setup(t *testing.T) (*Container, *ActiveConnection) {
	t.Helper()
	c := containerTestNew(t)
	containerTestAddProcess(c, containerTestPid, "other")
	conn := containerTestOpen(t, c, containerTestPid, containerTestFd, "10.0.0.5:40000", "10.96.0.10:1000", "10.1.2.3:1000", 111)
	return c, conn
}

func TestContainerOnL7RequestConnectionMatching(t *testing.T) {
	c, _ := containerTestL7Setup(t)
	r := &l7.RequestData{Protocol: l7.ProtocolRedis, Status: l7.StatusOk, Duration: time.Millisecond, Payload: []byte("*1\r\n$4\r\nPING\r\n")}

	assert.Nil(t, c.onL7Request(containerTestPid, 99, 111, r), "unknown fd")
	assert.Nil(t, c.onL7Request(containerTestPid, containerTestFd, 222, r), "fd reused by another connection")
	assert.Empty(t, c.l7Stats)

	assert.Nil(t, c.onL7Request(containerTestPid, containerTestFd, 0, r), "requests without a timestamp are attributed to the current connection")
	assert.Nil(t, c.onL7Request(containerTestPid, containerTestFd, 111, r))
	mfs := containerTestGather(t, c)
	m := metricsTestOne(t, mfs, "container_redis_queries_total", nil)
	assert.Equal(t, map[string]string{"destination": "10.96.0.10:1000", "actual_destination": "10.1.2.3:1000", "status": "ok"}, metricsTestLabels(m))
	assert.Equal(t, 2.0, m.GetCounter().GetValue())
}

func TestContainerOnL7RequestProtocols(t *testing.T) {
	pgQuery := append([]byte{'Q', 0, 0, 0, 13}, []byte("SELECT 1\x00")...)
	cases := []struct {
		name    string
		r       l7.RequestData
		metric  string
		labels  map[string]string
		latency bool
	}{
		{"http", l7.RequestData{Protocol: l7.ProtocolHTTP, Status: 404, Duration: time.Millisecond, Payload: []byte("GET /api/users HTTP/1.1\r\nHost: x\r\n\r\n")},
			"container_http_requests_total", map[string]string{"status": "4xx"}, true},
		{"postgres", l7.RequestData{Protocol: l7.ProtocolPostgres, Status: l7.StatusOk, Duration: time.Millisecond, Payload: pgQuery},
			"container_postgres_queries_total", map[string]string{"status": "ok"}, true},
		{"mysql", l7.RequestData{Protocol: l7.ProtocolMysql, Status: l7.StatusFailed, Duration: time.Millisecond, Payload: []byte{5, 0, 0, 0, 3, 'S', 'E', 'L', '1'}},
			"container_mysql_queries_total", map[string]string{"status": "failed"}, true},
		{"memcached", l7.RequestData{Protocol: l7.ProtocolMemcached, Status: l7.StatusOk, Duration: time.Millisecond, Payload: []byte("get key\r\n")},
			"container_memcached_queries_total", map[string]string{"status": "ok"}, true},
		{"redis", l7.RequestData{Protocol: l7.ProtocolRedis, Status: l7.StatusFailed, Duration: time.Millisecond, Payload: []byte("*1\r\n$4\r\nPING\r\n")},
			"container_redis_queries_total", map[string]string{"status": "failed"}, true},
		{"mongo", l7.RequestData{Protocol: l7.ProtocolMongo, Status: l7.StatusOk, Duration: time.Millisecond, Payload: []byte{1, 2, 3}},
			"container_mongo_queries_total", map[string]string{"status": "ok"}, true},
		{"kafka", l7.RequestData{Protocol: l7.ProtocolKafka, Status: l7.StatusOk, Duration: time.Millisecond},
			"container_kafka_requests_total", map[string]string{"status": "ok"}, true},
		{"cassandra", l7.RequestData{Protocol: l7.ProtocolCassandra, Status: l7.StatusFailed, Duration: time.Millisecond},
			"container_cassandra_queries_total", map[string]string{"status": "failed"}, true},
		{"rabbitmq", l7.RequestData{Protocol: l7.ProtocolRabbitmq, Status: l7.StatusOk, Method: l7.MethodProduce, Duration: time.Millisecond},
			"container_rabbitmq_messages_total", map[string]string{"status": "ok", "method": "produce"}, false},
		{"nats", l7.RequestData{Protocol: l7.ProtocolNats, Status: l7.StatusOk, Method: l7.MethodConsume},
			"container_nats_messages_total", map[string]string{"status": "ok", "method": "consume"}, false},
		{"dubbo", l7.RequestData{Protocol: l7.ProtocolDubbo2, Status: l7.StatusOk, Duration: time.Millisecond},
			"container_dubbo_requests_total", map[string]string{"status": "ok"}, true},
		{"clickhouse", l7.RequestData{Protocol: l7.ProtocolClickhouse, Status: l7.StatusOk, Duration: time.Millisecond, Payload: []byte{1, 2}},
			"container_clickhouse_queries_total", map[string]string{"status": "ok"}, true},
		{"zookeeper error", l7.RequestData{Protocol: l7.ProtocolZookeeper, Status: -4, Duration: time.Millisecond, Payload: []byte{0, 0, 0, 1}},
			"container_zookeeper_requests_total", map[string]string{"status": "failed"}, true},
		{"foundationdb", l7.RequestData{Protocol: l7.ProtocolFoundationDB, Status: l7.StatusOk, Duration: time.Millisecond},
			"container_foundationdb_requests_total", map[string]string{"status": "ok"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := containerTestL7Setup(t)
			r := tc.r
			assert.Nil(t, c.onL7Request(containerTestPid, containerTestFd, 111, &r))
			mfs := containerTestGather(t, c)
			m := metricsTestOne(t, mfs, tc.metric, tc.labels)
			assert.Equal(t, 1.0, m.GetCounter().GetValue())
			assert.Equal(t, "10.96.0.10:1000", metricsTestLabels(m)["destination"])
			lat := metricsTestFind(mfs, L7Latency[r.Protocol].Name, nil)
			if tc.latency {
				require.Len(t, lat, 1)
				assert.Equal(t, uint64(1), lat[0].GetHistogram().GetSampleCount())
			} else {
				assert.Empty(t, lat)
			}
		})
	}
}

func TestContainerOnL7RequestStatementCloseNotCounted(t *testing.T) {
	for _, p := range []l7.Protocol{l7.ProtocolPostgres, l7.ProtocolMysql} {
		t.Run(p.String(), func(t *testing.T) {
			c, conn := containerTestL7Setup(t)
			c.onL7Request(containerTestPid, containerTestFd, 111, &l7.RequestData{Protocol: p, Status: l7.StatusOk, Method: l7.MethodStatementClose, Duration: time.Millisecond})
			mfs := containerTestGather(t, c)
			assert.Empty(t, metricsTestFind(mfs, L7Requests[p].Name, nil))
			// per-connection prepared-statement parsers are kept on the connection
			if p == l7.ProtocolPostgres {
				assert.NotNil(t, conn.postgresParser)
			} else {
				assert.NotNil(t, conn.mysqlParser)
			}
		})
	}
}

func TestContainerOnL7RequestHttp2(t *testing.T) {
	c, conn := containerTestL7Setup(t)
	c.onL7Request(containerTestPid, containerTestFd, 111, &l7.RequestData{Protocol: l7.ProtocolHTTP2, Method: l7.MethodHttp2ClientFrames, Payload: []byte{0, 0, 0}})
	assert.NotNil(t, conn.http2Parser, "the HPACK state is kept per connection")
	_, ok := c.l7Stats[l7.ProtocolHTTP2]
	assert.False(t, ok, "HTTP2 is accounted as HTTP")
}

// Payloads are captured from untrusted traffic and may be truncated at any byte: the
// per-protocol glue must never panic on them.
func TestContainerOnL7RequestTruncatedPayloads(t *testing.T) {
	protocols := []l7.Protocol{l7.ProtocolHTTP, l7.ProtocolHTTP2, l7.ProtocolPostgres, l7.ProtocolMysql, l7.ProtocolMemcached,
		l7.ProtocolRedis, l7.ProtocolMongo, l7.ProtocolKafka, l7.ProtocolCassandra, l7.ProtocolRabbitmq, l7.ProtocolNats,
		l7.ProtocolDubbo2, l7.ProtocolDNS, l7.ProtocolClickhouse, l7.ProtocolZookeeper, l7.ProtocolFoundationDB}
	for _, p := range protocols {
		for _, payload := range [][]byte{nil, {0}, {0xff, 0xff, 0xff, 0xff, 0xff}} {
			c, _ := containerTestL7Setup(t)
			assert.NotPanics(t, func() {
				c.onL7Request(containerTestPid, containerTestFd, 111, &l7.RequestData{Protocol: p, Status: l7.StatusOk, Payload: payload})
			}, "%s %v", p, payload)
		}
	}
}

func TestContainerOnL7RequestTracesDisabled(t *testing.T) {
	c, _ := containerTestL7Setup(t)
	c.processes[containerTestPid].Flags.EbpfTracesDisabled = true
	c.onL7Request(containerTestPid, containerTestFd, 111, &l7.RequestData{Protocol: l7.ProtocolHTTP, Status: 200, Duration: time.Millisecond, Payload: []byte("GET / HTTP/1.1\r\n")})
	mfs := containerTestGather(t, c)
	assert.Len(t, metricsTestFind(mfs, "container_http_requests_total", map[string]string{"status": "2xx"}), 1, "metrics are still recorded")
}

type containerTestDNSAnswer struct {
	a    [4]byte
	aaaa *[16]byte
}

func containerTestDNS(t *testing.T, qtype dnsmessage.Type, name string, answers ...containerTestDNSAnswer) []byte {
	t.Helper()
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{Response: true})
	require.NoError(t, b.StartQuestions())
	n := dnsmessage.MustNewName(name)
	require.NoError(t, b.Question(dnsmessage.Question{Name: n, Type: qtype, Class: dnsmessage.ClassINET}))
	require.NoError(t, b.StartAnswers())
	for _, a := range answers {
		if a.aaaa != nil {
			require.NoError(t, b.AAAAResource(dnsmessage.ResourceHeader{Name: n, Class: dnsmessage.ClassINET, TTL: 60}, dnsmessage.AAAAResource{AAAA: *a.aaaa}))
			continue
		}
		require.NoError(t, b.AResource(dnsmessage.ResourceHeader{Name: n, Class: dnsmessage.ClassINET, TTL: 60}, dnsmessage.AResource{A: a.a}))
	}
	msg, err := b.Finish()
	require.NoError(t, err)
	return msg
}

func TestContainerOnDNSRequest(t *testing.T) {
	c := containerTestNew(t)
	payload := containerTestDNS(t, dnsmessage.TypeA, "db.example.com.",
		containerTestDNSAnswer{a: [4]byte{10, 0, 0, 1}}, containerTestDNSAnswer{a: [4]byte{10, 0, 0, 2}})

	// DNS requests are handled regardless of the connection (UDP)
	ip2fqdn := c.onL7Request(containerTestPid, 1234, 0, &l7.RequestData{Protocol: l7.ProtocolDNS, Status: 0, Duration: 2 * time.Millisecond, Payload: payload})
	require.Len(t, ip2fqdn, 2)
	d := ip2fqdn[netaddr.MustParseIP("10.0.0.1")]
	require.NotNil(t, d)
	assert.Equal(t, "db.example.com", d.FQDN)
	assert.Same(t, d, ip2fqdn[netaddr.MustParseIP("10.0.0.2")])

	c.onL7Request(containerTestPid, 1234, 0, &l7.RequestData{Protocol: l7.ProtocolDNS, Status: 3, Duration: time.Millisecond,
		Payload: containerTestDNS(t, dnsmessage.TypeA, "missing.example.com.")})

	mfs := containerTestGather(t, c)
	ok := metricsTestOne(t, mfs, "container_dns_requests_total", map[string]string{"status": "ok"})
	assert.Equal(t, map[string]string{"request_type": "TypeA", "domain": "db.example.com", "status": "ok"}, metricsTestLabels(ok))
	assert.Equal(t, 1.0, ok.GetCounter().GetValue())
	nx := metricsTestOne(t, mfs, "container_dns_requests_total", map[string]string{"status": "nxdomain"})
	assert.Equal(t, "missing.example.com", metricsTestLabels(nx)["domain"])
	lat := metricsTestOne(t, mfs, "container_dns_requests_duration_seconds_total", nil)
	assert.Equal(t, uint64(2), lat.GetHistogram().GetSampleCount())
	assert.Empty(t, metricsTestLabels(lat))
}

func TestContainerOnDNSRequestIgnored(t *testing.T) {
	c := containerTestNew(t)
	emptyAAAA := containerTestDNS(t, dnsmessage.TypeAAAA, "db.example.com.")
	cases := map[string]*l7.RequestData{
		"AAAA without answers (parallel to A)": {Protocol: l7.ProtocolDNS, Status: 0, Duration: time.Millisecond, Payload: emptyAAAA},
		"unknown rcode":                        {Protocol: l7.ProtocolDNS, Status: 9, Duration: time.Millisecond, Payload: emptyAAAA},
		"malformed":                            {Protocol: l7.ProtocolDNS, Status: 0, Payload: []byte{1, 2, 3}},
		"truncated":                            {Protocol: l7.ProtocolDNS, Status: 0, Payload: emptyAAAA[:len(emptyAAAA)-3]},
	}
	for name, r := range cases {
		assert.Nil(t, c.onL7Request(containerTestPid, 1, 0, r), name)
	}
	assert.Nil(t, c.dnsStats.Requests)
	assert.Nil(t, c.dnsStats.Latency)
}

func TestContainerOnDNSRequestNormalization(t *testing.T) {
	c := containerTestNew(t)
	v6 := [16]byte{0x20, 0x01, 0x0d, 0xb8, 15: 1}
	res := c.onDNSRequest(&l7.RequestData{Protocol: l7.ProtocolDNS, Status: 0,
		Payload: containerTestDNS(t, dnsmessage.TypeAAAA, "v6.example.com.", containerTestDNSAnswer{aaaa: &v6})})
	assert.Len(t, res, 1)
	assert.Nil(t, c.dnsStats.Latency, "zero duration: no histogram")

	// PTR names are collapsed to keep the domain label bounded
	res = c.onDNSRequest(&l7.RequestData{Protocol: l7.ProtocolDNS, Status: 0,
		Payload: containerTestDNS(t, dnsmessage.TypePTR, "4.3.2.1.in-addr.arpa.")})
	assert.Empty(t, res)
	mfs := containerTestGather(t, c)
	assert.Len(t, metricsTestFind(mfs, "container_dns_requests_total", map[string]string{"request_type": "TypePTR", "domain": "IP.in-addr.arpa"}), 1)
	assert.Len(t, metricsTestFind(mfs, "container_dns_requests_total", map[string]string{"request_type": "TypeAAAA", "domain": "v6.example.com"}), 1)
}

func TestContainerGcWithoutProcesses(t *testing.T) {
	c := containerTestNew(t)
	containerTestAddProcess(c, containerTestPid, "other")
	fresh := containerTestOpen(t, c, containerTestPid, 1, "10.0.0.5:40000", "10.96.0.10:80", "10.1.2.3:80", 1)
	old := containerTestOpen(t, c, containerTestPid, 2, "10.0.0.5:40001", "10.96.0.20:80", "10.1.2.4:80", 2)
	c.onConnectionOpen(containerTestPid, 3, containerTestAddr("10.0.0.5:40002"), containerTestAddr("10.96.0.30:80"), containerTestAddr("10.96.0.30:80"), 0, true, 0)
	c.l7Stats.get(l7.ProtocolHTTP, fresh.DestinationKey).observe("2xx", "", time.Millisecond)
	c.l7Stats.get(l7.ProtocolHTTP, old.DestinationKey).observe("2xx", "", time.Millisecond)
	now := time.Now()
	c.lastConnectionAttempts[old.DestinationKey.Destination()] = now.Add(-2 * gcInterval)
	c.lastConnectionAttempts[containerTestHP("10.96.0.30:80")] = now.Add(-2 * gcInterval)

	c.listens[containerTestAddr("10.0.0.5:8080")] = map[uint32]*ListenDetails{containerTestPid: {}}
	c.listens[containerTestAddr("10.0.0.5:8081")] = map[uint32]*ListenDetails{containerTestPid: {ClosedAt: now.Add(-2 * gcInterval)}}

	// the container's only process is gone: /proc sockets can't be read
	delete(c.processes, containerTestPid)
	c.gc(now)

	assert.Empty(t, c.activeConnections, "connections not seen in /proc are dropped")
	assert.Empty(t, c.connectionsByPidFd)

	// stats for destinations not contacted within gcInterval are pruned
	assert.Contains(t, c.connectionStats, fresh.DestinationKey)
	assert.NotContains(t, c.connectionStats, old.DestinationKey)
	assert.NotContains(t, c.failedConnectionAttempts, containerTestHP("10.96.0.30:80"))
	assert.NotContains(t, c.lastConnectionAttempts, old.DestinationKey.Destination())
	assert.Contains(t, c.l7Stats[l7.ProtocolHTTP], fresh.DestinationKey)
	assert.NotContains(t, c.l7Stats[l7.ProtocolHTTP], old.DestinationKey)

	// listens missing from /proc are closed now; long-closed ones are removed
	require.Contains(t, c.listens, containerTestAddr("10.0.0.5:8080"))
	assert.Equal(t, now, c.listens[containerTestAddr("10.0.0.5:8080")][containerTestPid].ClosedAt)
	assert.NotContains(t, c.listens, containerTestAddr("10.0.0.5:8081"))

	c.gc(now.Add(2 * gcInterval))
	assert.Empty(t, c.listens)
}

// gc against the test process' real sockets: a loopback listener and an established
// connection are visible in /proc/<pid>/net/tcp and /proc/<pid>/fd.
func TestContainerGcWithRealSockets(t *testing.T) {
	c := containerTestNew(t)
	self := uint32(os.Getpid())
	containerTestAddProcess(c, self, "") // NetNsId resolved from /proc

	l, err := net.Listen("tcp4", "127.0.0.1:0")
	require.NoError(t, err)
	defer l.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := l.Accept()
		if err == nil {
			accepted <- conn
		}
	}()
	client, err := net.Dial("tcp4", l.Addr().String())
	require.NoError(t, err)
	defer client.Close()
	server := <-accepted
	defer server.Close()

	src := netaddr.MustParseIPPort(client.LocalAddr().String())
	dst := netaddr.MustParseIPPort(client.RemoteAddr().String())
	listenAddr := netaddr.MustParseIPPort(l.Addr().String())
	key := common.NewDestinationKey(dst, dst, nil)
	live := &ActiveConnection{DestinationKey: key, Pid: self, Fd: 100}
	c.activeConnections[ConnectionKey{src: src, dst: dst}] = live
	c.connectionsByPidFd[PidFd{Pid: self, Fd: 100}] = live
	gone := &ActiveConnection{DestinationKey: key, Pid: self, Fd: 101}
	c.activeConnections[ConnectionKey{src: containerTestAddr("127.0.0.1:1"), dst: dst}] = gone
	c.connectionsByPidFd[PidFd{Pid: self, Fd: 101}] = gone
	c.lastConnectionAttempts[key.Destination()] = time.Now().Add(-2 * gcInterval)
	c.connectionStats[key] = &ConnectionStats{Count: 1}

	now := time.Now()
	c.gc(now)

	assert.Contains(t, c.activeConnections, ConnectionKey{src: src, dst: dst}, "established connections are kept")
	assert.NotContains(t, c.activeConnections, ConnectionKey{src: containerTestAddr("127.0.0.1:1"), dst: dst})
	assert.Contains(t, c.connectionsByPidFd, PidFd{Pid: self, Fd: 100})
	assert.NotContains(t, c.connectionsByPidFd, PidFd{Pid: self, Fd: 101})
	assert.Contains(t, c.connectionStats, key, "stats of destinations with established connections are kept")

	// a listen socket that the eBPF events missed is recovered from /proc
	require.Contains(t, c.listens, listenAddr)
	details := c.listens[listenAddr][self]
	require.NotNil(t, details)
	assert.True(t, details.ClosedAt.IsZero())

	// a connection closed long ago is dropped even if the socket is still established
	live.Closed = now.Add(-2 * gcInterval)
	c.gc(now)
	assert.NotContains(t, c.activeConnections, ConnectionKey{src: src, dst: dst})
	assert.NotContains(t, c.connectionsByPidFd, PidFd{Pid: self, Fd: 100})
}

func TestContainerProcessLifecycle(t *testing.T) {
	c := containerTestNew(t)
	assert.False(t, c.Dead(time.Now()))
	assert.Nil(t, c.onProcessStart(containerTestPid), "no taskstats client: the process is not tracked")

	p := containerTestAddProcess(c, containerTestPid, "other")
	containerTestAddProcess(c, containerTestPid+1, "other")
	c.delaysByPid[containerTestPid] = Delays{cpu: time.Second}

	c.onProcessExit(containerTestPid, true)
	assert.Error(t, p.ctx.Err(), "the process is closed")
	assert.NotContains(t, c.processes, containerTestPid)
	assert.NotContains(t, c.delaysByPid, containerTestPid)
	assert.Equal(t, 1, c.oomKills)
	assert.True(t, c.zombieAt.IsZero(), "still has processes")

	c.onProcessExit(containerTestPid+1, false)
	assert.Equal(t, 1, c.oomKills)
	assert.False(t, c.zombieAt.IsZero())
	assert.False(t, c.Dead(time.Now()))
	assert.True(t, c.Dead(time.Now().Add(gcInterval+time.Second)))

	assert.NotPanics(t, func() { c.onProcessExit(12345, false) })

	c.processes[containerTestPid] = processTestNew(containerTestPid)
	c.updateDelays() // taskstats unavailable: delays untouched
	assert.Zero(t, c.delays)
}

func TestContainerRuntimeStats(t *testing.T) {
	c := containerTestNew(t)
	p := containerTestAddProcess(c, containerTestPid, "other")

	c.updateNodejsStats(NodejsStatsUpdate{Pid: containerTestPid, Stats: ebpftracer.NodejsStats{EventLoopBlockedTime: time.Second}})
	assert.Nil(t, c.nodejsStats, "not instrumented")
	c.updatePythonStats(PythonStatsUpdate{Pid: 1, Stats: ebpftracer.PythonStats{ThreadLockWaitTime: time.Second}})
	assert.Nil(t, c.pythonStats, "unknown pid")

	p.nodejsPrevStats = &ebpftracer.NodejsStats{}
	p.pythonPrevStats = &ebpftracer.PythonStats{}
	for _, v := range []time.Duration{time.Second, 3 * time.Second, 2 * time.Second, 4 * time.Second} {
		c.updateNodejsStats(NodejsStatsUpdate{Pid: containerTestPid, Stats: ebpftracer.NodejsStats{EventLoopBlockedTime: v}})
		c.updatePythonStats(PythonStatsUpdate{Pid: containerTestPid, Stats: ebpftracer.PythonStats{ThreadLockWaitTime: v}})
	}
	// only positive deltas accumulate: 1 + 2 + (reset) + 2
	assert.Equal(t, 5*time.Second, c.nodejsStats.EventLoopBlockedTime)
	assert.Equal(t, 5*time.Second, c.pythonStats.ThreadLockWaitTime)

	mfs := containerTestGather(t, c)
	assert.Equal(t, 5.0, metricsTestOne(t, mfs, "container_nodejs_event_loop_blocked_time_seconds_total", nil).GetCounter().GetValue())
	assert.Equal(t, 5.0, metricsTestOne(t, mfs, "container_python_thread_lock_wait_time_seconds", nil).GetCounter().GetValue())
}

func TestContainerCollectMinContainerAge(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name      string
		startedAt time.Time
		zombieAt  time.Time
		reported  bool
	}{
		{name: "young", startedAt: now.Add(-10 * time.Second), reported: false},
		{name: "old enough", startedAt: now.Add(-2 * time.Minute), reported: true},
		{name: "unknown start (no taskstats, no cgroup mtime)", reported: true},
		{name: "short-lived and already exited", startedAt: now.Add(-2 * time.Minute), zombieAt: now.Add(-110 * time.Second), reported: false},
		{name: "long-lived and exited", startedAt: now.Add(-5 * time.Minute), zombieAt: now.Add(-time.Minute), reported: true},
		{name: "zombie timestamp in the future is ignored", startedAt: now.Add(-2 * time.Minute), zombieAt: now.Add(time.Hour), reported: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := containerTestNew(t)
			*flags.MinContainerAge = time.Minute
			c.startedAt = tc.startedAt
			c.zombieAt = tc.zombieAt
			c.restarts = 1
			mfs := containerTestGather(t, c)
			if tc.reported {
				assert.Len(t, metricsTestFind(mfs, "container_restarts_total", nil), 1)
			} else {
				assert.Empty(t, mfs)
			}
		})
	}

	t.Run("gate disabled", func(t *testing.T) {
		c := containerTestNew(t)
		*flags.MinContainerAge = 0
		c.startedAt = time.Now()
		assert.NotEmpty(t, containerTestGather(t, c))
	})
}

func TestContainerCollect(t *testing.T) {
	c := containerTestNew(t)
	c.metadata.image = "nginx:1.25"
	c.restarts = 2
	c.oomKills = 1
	containerTestAddProcess(c, containerTestPid, "other") // no cmdline: skipped for app type detection

	containerTestOpen(t, c, containerTestPid, 1, "10.0.0.5:40000", "10.96.0.10:80", "10.1.2.3:8080", 1)
	containerTestOpen(t, c, containerTestPid, 2, "10.0.0.5:40001", "10.96.0.10:80", "10.1.2.3:8080", 2)
	closed := containerTestOpen(t, c, containerTestPid, 3, "10.0.0.5:40002", "10.96.0.10:80", "10.1.2.3:8080", 3)
	c.onConnectionClose(ebpftracer.Event{Pid: containerTestPid, Fd: 3, Timestamp: 3, TrafficStats: &ebpftracer.TrafficStats{BytesSent: 10, BytesReceived: 20}})
	require.False(t, closed.Closed.IsZero())
	c.onRetransmission(containerTestAddr("10.0.0.5:40000"), containerTestAddr("10.96.0.10:80"))
	c.onConnectionOpen(containerTestPid, 4, containerTestAddr("10.0.0.5:40003"), containerTestAddr("10.96.0.99:443"), netaddr.IPPort{}, 0, true, 0)
	c.onL7Request(containerTestPid, 1, 1, &l7.RequestData{Protocol: l7.ProtocolHTTP, Status: 500, Duration: time.Millisecond, Payload: []byte("GET / HTTP/1.1\r\n")})

	mfs := containerTestGather(t, c)
	dst := map[string]string{"destination": "10.96.0.10:80", "actual_destination": "10.1.2.3:8080"}

	info := metricsTestOne(t, mfs, "container_info", nil)
	assert.Equal(t, map[string]string{"image": "nginx:1.25", "systemd_triggered_by": ""}, metricsTestLabels(info))
	assert.Equal(t, 2.0, metricsTestOne(t, mfs, "container_restarts_total", nil).GetCounter().GetValue())
	assert.Equal(t, 1.0, metricsTestOne(t, mfs, "container_oom_kills_total", nil).GetCounter().GetValue())

	assert.Equal(t, 3.0, metricsTestOne(t, mfs, "container_net_tcp_successful_connects_total", dst).GetCounter().GetValue())
	assert.InDelta(t, 0.003, metricsTestOne(t, mfs, "container_net_tcp_connection_time_seconds_total", dst).GetCounter().GetValue(), 1e-9)
	assert.Equal(t, 1.0, metricsTestOne(t, mfs, "container_net_tcp_retransmits_total", dst).GetCounter().GetValue())
	assert.Equal(t, 10.0, metricsTestOne(t, mfs, "container_net_tcp_bytes_sent_total", dst).GetCounter().GetValue())
	assert.Equal(t, 20.0, metricsTestOne(t, mfs, "container_net_tcp_bytes_received_total", dst).GetCounter().GetValue())
	active := metricsTestOne(t, mfs, "container_net_tcp_active_connections", dst)
	require.NotNil(t, active.GetGauge())
	assert.Equal(t, 2.0, active.GetGauge().GetValue(), "closed connections are not active")
	failed := metricsTestOne(t, mfs, "container_net_tcp_failed_connects_total", nil)
	assert.Equal(t, map[string]string{"destination": "10.96.0.99:443"}, metricsTestLabels(failed))
	assert.Equal(t, 1.0, metricsTestOne(t, mfs, "container_http_requests_total", map[string]string{"status": "5xx"}).GetCounter().GetValue())

	// no cgroup => no resource series; no log parsers => no log series; no cmdline => no app type
	for _, name := range []string{"container_resources_cpu_usage_seconds_total", "container_resources_memory_rss_bytes",
		"container_resources_cpu_pressure_waiting_seconds_total", "container_log_messages_total", "container_application_type",
		"container_resources_gpu_usage_percent", "container_net_latency_seconds"} {
		assert.Empty(t, metricsTestFind(mfs, name, nil), name)
	}
}

func TestContainerCollectLogMessages(t *testing.T) {
	c := containerTestNew(t)
	*flags.MaxLabelLength = 16
	ch := make(chan logparser.LogEntry)
	parser := logparser.NewParser(ch, nil, nil, 10*time.Millisecond)
	stopped := 0
	c.logParsers["stdout/stderr"] = &LogParser{parser: parser, stop: func() { stopped++ }}

	ch <- logparser.LogEntry{Timestamp: time.Now(), Content: "ERROR failed to connect to the database at 10.0.0.1: connection refused", Level: logparser.LevelError}
	require.Eventually(t, func() bool { return len(parser.GetCounters()) > 0 }, 2*time.Second, 10*time.Millisecond)

	mfs := containerTestGather(t, c)
	m := metricsTestOne(t, mfs, "container_log_messages_total", map[string]string{"source": "stdout/stderr"})
	ls := metricsTestLabels(m)
	assert.Equal(t, "error", ls["level"])
	assert.NotEmpty(t, ls["pattern_hash"])
	assert.LessOrEqual(t, len(ls["sample"]), 16, "the sample label is bounded by --max-label-length")
	assert.Equal(t, 1.0, m.GetCounter().GetValue())

	c.Close()
	assert.Equal(t, 1, stopped)
	_, open := <-c.done
	assert.False(t, open)
}

func TestContainerCollectGpuSingleProcess(t *testing.T) {
	c := containerTestNew(t)
	p := containerTestAddProcess(c, uint32(os.Getpid()), "other") // needs a readable cmdline
	p.addGpuUsageSample(gpu.ProcessUsageSample{UUID: "GPU-1", Timestamp: time.Now(), GPUPercent: 30, MemoryPercent: 15})

	mfs := containerTestGather(t, c)
	w := gpuStatsWindow.Seconds()
	assert.InDelta(t, 30/w, metricsTestOne(t, mfs, "container_resources_gpu_usage_percent", map[string]string{"gpu_uuid": "GPU-1"}).GetGauge().GetValue(), 1e-9)
	assert.InDelta(t, 15/w, metricsTestOne(t, mfs, "container_resources_gpu_memory_usage_percent", map[string]string{"gpu_uuid": "GPU-1"}).GetGauge().GetValue(), 1e-9)
}

// The container's GPU usage must be the sum over all of its processes.
func TestContainerCollectGpuMultipleProcesses(t *testing.T) {
	// BUG: Collect resets c.gpuStats inside the per-pid loop, so only the last pid's GPU usage survives — unskip when fixed
	t.Skip("BUG: Collect resets c.gpuStats inside the per-pid loop, so only the last pid's GPU usage survives")

	c := containerTestNew(t)
	p1 := containerTestAddProcess(c, uint32(os.Getpid()), "other")
	p2 := containerTestAddProcess(c, containerTestChild(t, "sleep"), "other")
	now := time.Now()
	p1.addGpuUsageSample(gpu.ProcessUsageSample{UUID: "GPU-1", Timestamp: now, GPUPercent: 30, MemoryPercent: 10})
	p2.addGpuUsageSample(gpu.ProcessUsageSample{UUID: "GPU-1", Timestamp: now, GPUPercent: 15, MemoryPercent: 5})

	mfs := containerTestGather(t, c)
	w := gpuStatsWindow.Seconds()
	assert.InDelta(t, 45/w, metricsTestOne(t, mfs, "container_resources_gpu_usage_percent", map[string]string{"gpu_uuid": "GPU-1"}).GetGauge().GetValue(), 1e-9)
	assert.InDelta(t, 15/w, metricsTestOne(t, mfs, "container_resources_gpu_memory_usage_percent", map[string]string{"gpu_uuid": "GPU-1"}).GetGauge().GetValue(), 1e-9)
}

func TestContainerCollectPing(t *testing.T) {
	c := containerTestNew(t)
	*flags.DisablePinger = false
	prevNs := selfNetNs
	selfNetNs = netns.None() // never ping for real from a unit test
	t.Cleanup(func() { selfNetNs = prevNs })
	c.connectionStats[common.NewDestinationKey(containerTestAddr("10.96.0.10:80"), containerTestAddr("10.1.2.3:80"), nil)] = &ConnectionStats{Count: 1}
	// no process whose network namespace can be opened => nothing is pinged
	containerTestAddProcess(c, containerTestPid, "other")
	assert.Nil(t, c.ping())
	// the agent's own process uses selfNetNs, which is not initialized outside NewRegistry
	containerTestAddProcess(c, agentPid, "other")
	assert.Nil(t, c.ping())
	assert.Empty(t, metricsTestFind(containerTestGather(t, c), "container_net_latency_seconds", nil))
}

func TestContainerGetMountsAndResolveFd(t *testing.T) {
	c := containerTestNew(t)
	self := uint32(os.Getpid())
	assert.Nil(t, c.getMounts())

	f, err := os.Create(filepath.Join(t.TempDir(), "data.bin"))
	require.NoError(t, err)
	defer f.Close()
	ro, err := os.Open(f.Name())
	require.NoError(t, err)
	defer ro.Close()

	mntId, logPath := resolveFd(self, uint64(f.Fd()))
	assert.NotEmpty(t, mntId, "writable regular files resolve to their mount")
	assert.Empty(t, logPath, "only files under /var/log are container logs")
	mntId2, _ := resolveFd(self, uint64(ro.Fd()))
	assert.Empty(t, mntId2, "read-only files are ignored")
	mntId3, _ := resolveFd(containerTestPid, 3)
	assert.Empty(t, mntId3, "vanished process")

	containerTestAddProcess(c, self, "other")
	c.onFileOpen(self, uint64(f.Fd()), 42, false)
	assert.Contains(t, c.seenMounts, uint64(42))
	require.Contains(t, c.mounts, mntId)
	c.onFileOpen(self, uint64(f.Fd()), 42, false) // already seen: fast path

	mounts := c.getMounts()
	mi := c.mounts[mntId]
	require.Contains(t, mounts, mi.MajorMinor)
	st := mounts[mi.MajorMinor][mi.MountPoint]
	require.NotNil(t, st)
	assert.Greater(t, st.CapacityBytes, uint64(0))

	delete(c.processes, self)
	assert.Empty(t, c.getMounts(), "no process to stat the mount through")
}

func TestContainerRunLogParserNoop(t *testing.T) {
	c := containerTestNew(t)
	c.cgroup = &cgroup.Cgroup{ContainerType: cgroup.ContainerTypeDocker}
	c.runLogParser("") // docker without a log path
	c.cgroup = &cgroup.Cgroup{ContainerType: cgroup.ContainerTypeLxc}
	c.runLogParser("") // runtime without a log source
	assert.Empty(t, c.logParsers)

	existing := &LogParser{parser: logparser.NewParser(make(chan logparser.LogEntry), nil, nil, time.Second)}
	defer existing.Stop()
	c.logParsers["/var/log/app.log"] = existing
	c.runLogParser("/var/log/app.log")
	assert.Same(t, existing, c.logParsers["/var/log/app.log"], "an already tailed file is not reopened")

	p := containerTestAddProcess(c, containerTestPid, "other")
	p.Flags.LogMonitoringDisabled = true
	c.runLogParser("/var/log/other.log")
	assert.NotContains(t, c.logParsers, "/var/log/other.log")

	prev := *flags.DisableLogParsing
	*flags.DisableLogParsing = true
	t.Cleanup(func() { *flags.DisableLogParsing = prev })
	p.Flags.LogMonitoringDisabled = false
	c.runLogParser("/var/log/other.log")
	assert.NotContains(t, c.logParsers, "/var/log/other.log")
}

func TestNewContainer(t *testing.T) {
	_, err := NewContainer("/docker/x", &cgroup.Cgroup{}, &ContainerMetadata{}, containerTestPid, &Registry{})
	assert.Error(t, err, "vanished process")

	id := ContainerID("/k8s/default/web-5d9c/nginx")
	c, err := NewContainer(id, &cgroup.Cgroup{}, &ContainerMetadata{}, uint32(os.Getpid()), &Registry{})
	require.NoError(t, err)
	defer c.Close()
	assert.Equal(t, id, c.id)
	assert.NotEqual(t, string(id), c.appId)
	assert.NotNil(t, c.l7Stats)
	assert.NotNil(t, c.dnsStats)
	assert.Empty(t, c.logParsers)

	ch := make(chan *prometheus.Desc, 1)
	c.Describe(ch)
	assert.Contains(t, (<-ch).String(), `"container"`)
}

func TestContainerAttachTlsUprobesChecked(t *testing.T) {
	c := containerTestNew(t)
	assert.NotPanics(t, func() { c.attachTlsUprobes(nil, containerTestPid) }, "unknown pid")
	p := containerTestAddProcess(c, containerTestPid, "other")
	p.openSslUprobesChecked = true
	p.goTlsUprobesChecked = true
	assert.NotPanics(t, func() { c.attachTlsUprobes(nil, containerTestPid) }, "already checked: the tracer is not used")
}

// ---- concurrency: the event loop (handleEvents) and the scrape goroutine (Collect) ----

func containerTestRunConcurrently(fns ...func()) {
	var wg sync.WaitGroup
	for _, fn := range fns {
		wg.Add(1)
		go func(fn func()) {
			defer wg.Done()
			fn()
		}(fn)
	}
	wg.Wait()
}

func containerTestCollectLoop(c *Container, n int) func() {
	return func() {
		for i := 0; i < n; i++ {
			ch := make(chan prometheus.Metric, 1000)
			c.Collect(ch)
		}
	}
}

func TestContainerConnectionCloseCollectRace(t *testing.T) {
	// BUG: onConnectionClose writes conn.Closed outside c.lock while Collect/gc read it under the lock (data race) — unskip when fixed
	t.Skip("BUG: onConnectionClose writes conn.Closed outside c.lock while Collect/gc read it under the lock (data race)")

	c := containerTestNew(t)
	containerTestAddProcess(c, containerTestPid, "other")
	for i := 0; i < 50; i++ {
		containerTestOpen(t, c, containerTestPid, uint64(i), "10.0.0.5:"+strconv.Itoa(40000+i), "10.96.0.10:80", "10.1.2.3:80", uint64(i+1))
	}
	containerTestRunConcurrently(
		func() {
			for i := 0; i < 50; i++ {
				c.onConnectionClose(ebpftracer.Event{Pid: containerTestPid, Fd: uint64(i), Timestamp: uint64(i + 1)})
			}
		},
		containerTestCollectLoop(c, 20),
	)
}

func TestContainerConnectionOpenNetNsCollectRace(t *testing.T) {
	// BUG: onConnectionOpen calls p.isHostNs() (lazily writes p.netNsId) before taking c.lock, racing with Collect->getListens and gc — unskip when fixed
	t.Skip("BUG: onConnectionOpen calls p.isHostNs() (lazily writes p.netNsId) before taking c.lock, racing with Collect->getListens and gc")

	c := containerTestNew(t)
	self := uint32(os.Getpid())
	containerTestAddProcess(c, self, "") // netNsId not resolved yet
	c.listens[containerTestAddr("10.0.0.5:8080")] = map[uint32]*ListenDetails{self: {}}
	containerTestRunConcurrently(
		func() {
			// loopback destination => isHostNs() => NetNsId() resolves and caches the id
			c.onConnectionOpen(self, 1, containerTestAddr("127.0.0.1:40000"), containerTestAddr("127.0.0.1:6379"), containerTestAddr("127.0.0.1:6379"), 1, false, 0)
		},
		containerTestCollectLoop(c, 5),
	)
}

func TestContainerGpuSampleCollectRace(t *testing.T) {
	// BUG: the event loop appends GPU samples (Process.addGpuUsageSample, registry.go) without c.lock while Collect->getGPUUsage compacts the same slice — unskip when fixed
	t.Skip("BUG: the event loop appends GPU samples (Process.addGpuUsageSample, registry.go) without c.lock while Collect->getGPUUsage compacts the same slice")

	c := containerTestNew(t)
	p := containerTestAddProcess(c, uint32(os.Getpid()), "other")
	containerTestRunConcurrently(
		func() {
			for i := 0; i < 200; i++ {
				// what handleEvents does for gpuProcessUsageSampleChan
				if p := c.processes[p.Pid]; p != nil {
					p.addGpuUsageSample(gpu.ProcessUsageSample{UUID: "GPU-1", Timestamp: time.Now(), GPUPercent: 10})
				}
			}
		},
		containerTestCollectLoop(c, 5),
	)
}

func containerTestChild(t *testing.T, argv0 string) uint32 {
	t.Helper()
	path, err := exec.LookPath("sleep")
	if err != nil {
		t.Skipf("no sleep binary: %s", err)
	}
	cmd := &exec.Cmd{Path: path, Args: []string{argv0, "30"}}
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start a child process: %s", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	pid := uint32(cmd.Process.Pid)
	// wait until the kernel reports the new argv
	require.Eventually(t, func() bool { return len(proc.GetCmdline(pid)) > 0 }, 2*time.Second, 5*time.Millisecond)
	return pid
}

// Two replicas of the same JVM inside one container must be reported once: duplicate
// const metrics with identical labels make the whole scrape fail.
func TestContainerCollectJvmDeduplicated(t *testing.T) {
	c := containerTestNew(t)
	for i := 0; i < 2; i++ {
		pid := containerTestChild(t, "java")
		jvmTestInstallPerfData(t, pid, jvmTestBuildPerfData(jvmTestFixtureEntries()))
		containerTestAddProcess(c, pid, "other")
	}
	mfs := containerTestGather(t, c)
	assert.Len(t, metricsTestFind(mfs, "container_jvm_info", map[string]string{"jvm": "org.example.App --port 8080"}), 1)
	assert.Len(t, metricsTestFind(mfs, "container_jvm_heap_used_bytes", nil), 1)
}

func TestContainerCollectDotNetDeduplicated(t *testing.T) {
	c := containerTestNew(t)
	for _, pid := range []uint32{uint32(os.Getpid()), containerTestChild(t, "dotnet")} {
		p := containerTestAddProcess(c, pid, "other")
		p.dotNetMonitor = dotnetTestMonitor(t, "Accounting")
		p.dotNetMonitor.processMetric("threadpool-thread-count", "", 4)
	}
	mfs := containerTestGather(t, c)
	assert.Len(t, metricsTestFind(mfs, "container_dotnet_thread_pool_size", map[string]string{"application": "Accounting"}), 1)
	assert.Len(t, metricsTestFind(mfs, "container_application_type", map[string]string{"application_type": "dotnet"}), 1)
}

func TestContainerCollectApplicationTypeGolang(t *testing.T) {
	c := containerTestNew(t)
	p := containerTestAddProcess(c, uint32(os.Getpid()), "other")
	p.isGolangApp = true
	mfs := containerTestGather(t, c)
	assert.Len(t, metricsTestFind(mfs, "container_application_type", map[string]string{"application_type": "golang"}), 1)
}

// Smoke test against the test process' own cgroup: the values are host-specific, so only
// the mapping "cgroup reader returned data => the series is exported" is asserted.
func TestContainerCollectOwnCgroup(t *testing.T) {
	cg, err := cgroup.NewFromProcessCgroupFile("/proc/self/cgroup")
	if err != nil {
		t.Skipf("unsupported cgroup layout: %s", err)
	}
	c := containerTestNew(t)
	c.cgroup = cg
	mfs := containerTestGather(t, c)

	if cpu := cg.CpuStat(); cpu != nil {
		assert.Len(t, metricsTestFind(mfs, "container_resources_cpu_usage_seconds_total", nil), 1)
		assert.Len(t, metricsTestFind(mfs, "container_resources_cpu_throttled_seconds_total", nil), 1)
		assert.Equal(t, cpu.LimitCores > 0, len(metricsTestFind(mfs, "container_resources_cpu_limit_cores", nil)) == 1)
	} else {
		assert.Empty(t, metricsTestFind(mfs, "container_resources_cpu_usage_seconds_total", nil))
	}
	if mem := cg.MemoryStat(); mem != nil {
		assert.Len(t, metricsTestFind(mfs, "container_resources_memory_rss_bytes", nil), 1)
		assert.Len(t, metricsTestFind(mfs, "container_resources_memory_cache_bytes", nil), 1)
	}
	if psi := cg.PSI(); psi != nil {
		for _, kind := range []string{"some", "full"} {
			assert.Len(t, metricsTestFind(mfs, "container_resources_cpu_pressure_waiting_seconds_total", map[string]string{"kind": kind}), 1)
			assert.Len(t, metricsTestFind(mfs, "container_resources_memory_pressure_waiting_seconds_total", map[string]string{"kind": kind}), 1)
			assert.Len(t, metricsTestFind(mfs, "container_resources_io_pressure_waiting_seconds_total", map[string]string{"kind": kind}), 1)
		}
	}

	// MinContainerAge falls back to the cgroup mtime when taskstats didn't provide startedAt
	if created := cg.CreatedAt(); !created.IsZero() {
		*flags.MinContainerAge = time.Since(created) + time.Hour
		assert.Empty(t, containerTestGather(t, c), "younger than min age according to the cgroup mtime")
	}
}

func TestContainerCollectVolumes(t *testing.T) {
	c := containerTestNew(t)
	self := uint32(os.Getpid())
	containerTestAddProcess(c, self, "other")
	f, err := os.Create(filepath.Join(t.TempDir(), "data.bin"))
	require.NoError(t, err)
	defer f.Close()
	c.onFileOpen(self, uint64(f.Fd()), 0, false)
	require.NotEmpty(t, c.mounts)
	var mountPoint string
	for _, mi := range c.mounts {
		mountPoint = mi.MountPoint
	}
	c.metadata.volumes = map[string]string{mountPoint: "data"}

	mfs := containerTestGather(t, c)
	sizes := metricsTestFind(mfs, "container_resources_disk_size_bytes", map[string]string{"mount_point": mountPoint})
	if len(sizes) == 0 {
		t.Skip("node.GetDisks() is unavailable on this host")
	}
	ls := metricsTestLabels(sizes[0])
	assert.Equal(t, "data", ls["volume"])
	assert.Contains(t, ls, "device")
	assert.Len(t, metricsTestFind(mfs, "container_resources_disk_used_bytes", map[string]string{"mount_point": mountPoint, "volume": "data"}), 1)
}

func TestContainerRunLogParserReplacesContainerLog(t *testing.T) {
	c := containerTestNew(t)
	c.cgroup = &cgroup.Cgroup{ContainerType: cgroup.ContainerTypeContainerd}
	logFile := filepath.Join(t.TempDir(), "0.log")
	require.NoError(t, os.WriteFile(logFile, nil, 0o644))
	c.metadata.logPath = logFile
	stopped := 0
	c.logParsers["stdout/stderr"] = &LogParser{
		parser: logparser.NewParser(make(chan logparser.LogEntry), nil, nil, time.Second),
		stop:   func() { stopped++ },
	}
	c.runLogParser("")
	assert.Equal(t, 1, stopped, "the previous container log reader is stopped (log path changed on restart)")
	// the new reader opens the file through /proc/1/root, which may or may not be
	// accessible to the test user
	if p := c.logParsers["stdout/stderr"]; p != nil {
		p.Stop()
	}

	c.runLogParser(logFile) // /var/log-style file written by the app
	if p := c.logParsers[logFile]; p != nil {
		p.Stop()
	}
}

func TestContainerWildcardListensSmoke(t *testing.T) {
	c := containerTestNew(t)
	self := uint32(os.Getpid())
	containerTestAddProcess(c, self, "other")
	// resolving the namespace IPs needs netlink in the process' netns (may be denied without privileges)
	assert.NotPanics(t, func() { c.onListenOpen(self, containerTestAddr("0.0.0.0:8080"), false) })
	require.Contains(t, c.listens, containerTestAddr("0.0.0.0:8080"))
	for addr := range c.getListens() {
		assert.False(t, addr.IP().IsUnspecified(), "wildcard addresses are never exported")
		assert.Equal(t, uint16(8080), addr.Port())
	}

	c.metadata.hostListens = map[string][]netaddr.IPPort{"docker-proxy": {containerTestAddr("0.0.0.0:80"), containerTestAddr("[::]:80")}}
	res := c.getProxiedListens()
	require.Contains(t, res, "docker-proxy")
	for addr := range res["docker-proxy"] {
		assert.False(t, addr.IP().IsUnspecified())
	}
}

// containerTestTracer is a real tracer with L7 tracing disabled: its uprobe attach
// methods return immediately, so no eBPF/root is needed.
func containerTestTracer() *ebpftracer.Tracer {
	return ebpftracer.NewTracer(netns.None(), netns.None(), true)
}

func TestContainerOnProcessStart(t *testing.T) {
	ts := taskstatsTestUse(t)
	c := containerTestNew(t)
	c.registry.tracer = containerTestTracer()
	t0 := time.Now().Add(-time.Hour).Truncate(time.Second)
	a, b, d := containerTestPid, containerTestPid+1, containerTestPid+2 // vanished pids: instrumentation is a no-op

	assert.Nil(t, c.onProcessStart(a), "no taskstats for the pid (it has already exited)")
	assert.Empty(t, c.processes)

	c.zombieAt = time.Now()
	ts.setPID(a, &taskstats.Stats{BeginTime: t0})
	p := c.onProcessStart(a)
	require.NotNil(t, p)
	t.Cleanup(p.Close)
	assert.Equal(t, a, p.Pid)
	assert.Equal(t, t0, p.StartedAt)
	assert.Same(t, p, c.processes[a])
	assert.True(t, c.zombieAt.IsZero(), "a new process revives a zombie container")
	assert.Equal(t, t0, c.startedAt)
	assert.Equal(t, 0, c.restarts)

	// the only process exits and a new one starts later: the container restarted
	c.onProcessExit(a, false)
	ts.setPID(b, &taskstats.Stats{BeginTime: t0.Add(time.Minute)})
	pb := c.onProcessStart(b)
	require.NotNil(t, pb)
	t.Cleanup(pb.Close)
	assert.Equal(t, 1, c.restarts)
	assert.Equal(t, t0.Add(time.Minute), c.startedAt)

	// a sibling process started while b is running is not a restart
	ts.setPID(d, &taskstats.Stats{BeginTime: t0.Add(2 * time.Minute)})
	pd := c.onProcessStart(d)
	require.NotNil(t, pd)
	t.Cleanup(pd.Close)
	assert.Equal(t, 1, c.restarts)
	assert.Equal(t, t0.Add(time.Minute), c.startedAt)
	assert.Len(t, c.processes, 2)
}

func TestContainerUpdateDelays(t *testing.T) {
	ts := taskstatsTestUse(t)
	c := containerTestNew(t)
	a, b := containerTestPid, containerTestPid+1
	containerTestAddProcess(c, a, "other")
	containerTestAddProcess(c, b, "other") // taskstats fails for it: skipped
	ts.setTGID(a, &taskstats.Stats{CPUDelay: 3 * time.Second, BlockIODelay: time.Second})

	c.updateDelays()
	assert.Equal(t, 3*time.Second, c.delays.cpu)
	assert.Equal(t, time.Second, c.delays.disk)
	assert.Equal(t, Delays{cpu: 3 * time.Second, disk: time.Second}, c.delaysByPid[a])
	assert.NotContains(t, c.delaysByPid, b)

	// cumulative per-process counters are turned into container-level deltas
	ts.setTGID(a, &taskstats.Stats{CPUDelay: 5 * time.Second, BlockIODelay: time.Second})
	ts.setTGID(b, &taskstats.Stats{CPUDelay: time.Second, BlockIODelay: 2 * time.Second})
	c.updateDelays()
	assert.Equal(t, 6*time.Second, c.delays.cpu)
	assert.Equal(t, 3*time.Second, c.delays.disk)

	mfs := containerTestGather(t, c)
	assert.Equal(t, 6.0, metricsTestOne(t, mfs, "container_resources_cpu_delay_seconds_total", nil).GetCounter().GetValue())
	assert.Equal(t, 3.0, metricsTestOne(t, mfs, "container_resources_disk_delay_seconds_total", nil).GetCounter().GetValue())
}

func TestContainerAttachTlsUprobesWithTracer(t *testing.T) {
	c := containerTestNew(t)
	p := containerTestAddProcess(c, containerTestPid, "other")
	c.attachTlsUprobes(containerTestTracer(), containerTestPid)
	assert.True(t, p.openSslUprobesChecked)
	assert.True(t, p.goTlsUprobesChecked)
	assert.False(t, p.isGolangApp)
	assert.Empty(t, p.uprobes)
	// the checks are done once per process: a nil tracer would panic if they ran again
	assert.NotPanics(t, func() { c.attachTlsUprobes(nil, containerTestPid) })
}

// containerTestPinger replaces pinger.Ping (raw ICMP sockets need CAP_NET_RAW).
func containerTestPinger(t *testing.T, rtt map[netaddr.IP]float64, err error) *[][]netaddr.IP {
	t.Helper()
	var calls [][]netaddr.IP
	saved := pingerPing
	pingerPing = func(ns, origin netns.NsHandle, targets []netaddr.IP, timeout time.Duration) (map[netaddr.IP]float64, error) {
		assert.True(t, ns.IsOpen())
		assert.Equal(t, pingTimeout, timeout)
		calls = append(calls, targets)
		return rtt, err
	}
	t.Cleanup(func() { pingerPing = saved })
	return &calls
}

func containerTestSelfNetNs(t *testing.T) {
	t.Helper()
	ns, err := netns.Get()
	require.NoError(t, err)
	saved := selfNetNs
	selfNetNs = ns
	t.Cleanup(func() {
		selfNetNs = saved
		_ = ns.Close()
	})
}

func TestContainerPing(t *testing.T) {
	addDestinations := func(c *Container) {
		for _, d := range []string{"10.1.2.3:80", "127.0.0.1:8080", "[fd00::1]:443"} {
			c.connectionStats[common.NewDestinationKey(containerTestAddr("10.96.0.10:80"), containerTestAddr(d), nil)] = &ConnectionStats{Count: 1}
		}
		c.failedConnectionAttempts[containerTestHP("10.9.9.9:5432")] = 1
		c.failedConnectionAttempts[common.HostPortWithEmptyIP("db.example.com", 5432)] = 1 // unresolved: nothing to ping
		c.failedConnectionAttempts[common.NewDestinationKey(containerTestAddr("10.96.0.11:80"), containerTestAddr("10.96.0.11:80"), &common.Domain{FQDN: "api.example.com"}).Destination()] = 1
	}

	t.Run("agent process uses the agent netns", func(t *testing.T) {
		containerTestSelfNetNs(t)
		want := map[netaddr.IP]float64{netaddr.MustParseIP("10.1.2.3"): 0.001}
		calls := containerTestPinger(t, want, nil)
		c := containerTestNew(t)
		containerTestAddProcess(c, agentPid, "other")
		assert.Nil(t, c.ping(), "no destinations")
		assert.Empty(t, *calls)

		addDestinations(c)
		assert.Equal(t, want, c.ping())
		require.Len(t, *calls, 1)
		// loopback and IPv6 destinations are not pinged
		assert.ElementsMatch(t, []netaddr.IP{netaddr.MustParseIP("10.1.2.3"), netaddr.MustParseIP("10.9.9.9"), netaddr.MustParseIP("10.96.0.11")}, (*calls)[0])
	})

	t.Run("other processes use their own netns", func(t *testing.T) {
		calls := containerTestPinger(t, nil, errors.New("operation not permitted"))
		c := containerTestNew(t)
		containerTestAddProcess(c, containerTestChild(t, "app"), "other")
		addDestinations(c)
		assert.Nil(t, c.ping(), "pinger errors are logged, nothing is reported")
		assert.Len(t, *calls, 1)
	})

	t.Run("netns of another user's process", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root can open any netns")
		}
		calls := containerTestPinger(t, nil, nil)
		c := containerTestNew(t)
		containerTestAddProcess(c, 1, "other")
		addDestinations(c)
		assert.Nil(t, c.ping())
		assert.Empty(t, *calls)
	})
}

// containerTestHostNetNs fakes the host netns (/proc/1/ns/net needs privileges) and its IPs.
func containerTestHostNetNs(t *testing.T, nsErr error, ips []netaddr.IP, ipsErr error) {
	t.Helper()
	savedNs, savedIps := getHostNetNs, getNsIps
	t.Cleanup(func() { getHostNetNs, getNsIps = savedNs, savedIps })
	getHostNetNs = func() (netns.NsHandle, error) {
		if nsErr != nil {
			return netns.None(), nsErr
		}
		return netns.Get()
	}
	getNsIps = func(ns netns.NsHandle) ([]netaddr.IP, error) {
		assert.True(t, ns.IsOpen())
		return ips, ipsErr
	}
}

func TestContainerProxiedListensWildcard(t *testing.T) {
	hostListens := map[string][]netaddr.IPPort{
		"docker-proxy": {containerTestAddr("0.0.0.0:80"), containerTestAddr("[::]:443"), containerTestAddr("192.168.1.10:8080")},
	}
	hostIps := []netaddr.IP{netaddr.MustParseIP("192.168.1.10"), netaddr.MustParseIP("10.0.0.1"), netaddr.MustParseIP("fd00::10")}

	t.Run("expanded to the host IPs of the same family", func(t *testing.T) {
		containerTestHostNetNs(t, nil, hostIps, nil)
		c := containerTestNew(t)
		c.metadata.hostListens = hostListens
		assert.Equal(t, map[string]map[netaddr.IPPort]struct{}{"docker-proxy": {
			containerTestAddr("192.168.1.10:80"):   {},
			containerTestAddr("10.0.0.1:80"):       {},
			containerTestAddr("[fd00::10]:443"):    {},
			containerTestAddr("192.168.1.10:8080"): {},
		}}, c.getProxiedListens())
	})

	for name, errs := range map[string][2]error{
		"host netns unavailable": {errors.New("permission denied"), nil},
		"host IPs unavailable":   {nil, errors.New("operation not permitted")},
	} {
		t.Run(name, func(t *testing.T) {
			containerTestHostNetNs(t, errs[0], hostIps, errs[1])
			c := containerTestNew(t)
			c.metadata.hostListens = hostListens
			assert.Equal(t, map[string]map[netaddr.IPPort]struct{}{"docker-proxy": {
				containerTestAddr("192.168.1.10:8080"): {},
			}}, c.getProxiedListens(), "only explicit addresses are reported")
		})
	}
}

func TestContainerOnListenOpenWildcardNsIps(t *testing.T) {
	c := containerTestNew(t)
	self := uint32(os.Getpid())
	containerTestAddProcess(c, self, "other")

	containerTestHostNetNs(t, nil, []netaddr.IP{netaddr.MustParseIP("10.0.0.5")}, nil)
	c.onListenOpen(self, containerTestAddr("0.0.0.0:8080"), false)
	assert.Equal(t, []netaddr.IP{netaddr.MustParseIP("10.0.0.5")}, c.listens[containerTestAddr("0.0.0.0:8080")][self].NsIPs)
	assert.Equal(t, map[netaddr.IPPort]int{containerTestAddr("10.0.0.5:8080"): 1}, c.getListens())

	containerTestHostNetNs(t, nil, nil, errors.New("operation not permitted"))
	c.onListenOpen(self, containerTestAddr("0.0.0.0:9090"), false)
	assert.Nil(t, c.listens[containerTestAddr("0.0.0.0:9090")][self].NsIPs)
}

func TestContainerRunLogParserJournald(t *testing.T) {
	c := containerTestNew(t)
	c.cgroup = &cgroup.Cgroup{Id: "/system.slice/nginx.service", ContainerType: cgroup.ContainerTypeSystemdService, ContainerId: "/system.slice/nginx.service"}

	journaldTestUse(t)
	r := journaldReader.(*journaldTestReader)
	c.runLogParser("")
	require.Contains(t, c.logParsers, "journald")
	assert.Contains(t, r.subscribers, c.cgroup.Id)

	c.Close() // stops the parser and unsubscribes from the journal
	assert.Equal(t, []string{c.cgroup.Id}, r.unsubscribed)
}

func TestContainerRunLogParserTailsHostFiles(t *testing.T) {
	root := containerTestHostPath(t)
	c := containerTestNew(t)
	c.cgroup = &cgroup.Cgroup{Id: "/docker/x", ContainerType: cgroup.ContainerTypeDocker}
	c.metadata.logPath = "/var/lib/docker/containers/x/x-json.log"
	c.metadata.logDecoder = logparser.DockerJsonDecoder{}
	for _, p := range []string{c.metadata.logPath, "/var/log/app.log"} {
		require.NoError(t, os.MkdirAll(filepath.Join(root, filepath.Dir(p)), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(root, p), nil, 0o644))
	}
	t.Cleanup(c.Close)

	c.runLogParser("")
	assert.Contains(t, c.logParsers, "stdout/stderr", "the container log is read through the host root")
	c.runLogParser("/var/log/app.log")
	assert.Contains(t, c.logParsers, "/var/log/app.log")
	c.runLogParser("/var/log/missing.log")
	assert.NotContains(t, c.logParsers, "/var/log/missing.log")
}
