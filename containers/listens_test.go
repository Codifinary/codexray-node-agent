package containers

import (
	"context"
	"testing"
	"time"

	"github.com/codifinary/codexray-node-agent/proc"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"inet.af/netaddr"
)

func listenProcess(pid uint32, startedAt time.Time, netNs string) *Process {
	return &Process{Pid: pid, StartedAt: startedAt, netNsId: netNs}
}

func TestDeadOwnerListenMovesToTheProcessHoldingTheSocket(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	addr := netaddr.MustParseIPPort("10.0.0.5:8080")
	c := newTestContainer()
	c.processes[200] = listenProcess(200, now.Add(-time.Hour), "NS(1:2)")
	// pid 100 opened the listen, forked 200 and exited: its entry is still open.
	c.listens[addr] = map[uint32]*ListenDetails{100: {}}
	assert.Equal(t, map[netaddr.IPPort]int{addr: 0}, c.getListens(), "an unregistered owner's listen reads as closed")

	rekeys := counterValue(t, agentMetrics.listenAttributions, attributionGCRekey)
	c.revalidateListens(now, map[netaddr.IPPort]string{addr: "777"}, func() map[string]uint32 {
		return map[string]uint32{"777": 200}
	})
	assert.Equal(t, map[netaddr.IPPort]int{addr: 1}, c.getListens())
	assert.Contains(t, c.listens[addr], uint32(200))
	assert.NotContains(t, c.listens[addr], uint32(100), "the dead owner's entry is dropped")
	assert.Equal(t, rekeys+1, counterValue(t, agentMetrics.listenAttributions, attributionGCRekey))
}

func TestOwnedListenSkipsTheFdScan(t *testing.T) {
	addr := netaddr.MustParseIPPort("10.0.0.5:8080")
	c := newTestContainer()
	c.processes[200] = listenProcess(200, time.Unix(1, 0), "NS(1:2)")
	c.listens[addr] = map[uint32]*ListenDetails{200: {}}
	c.revalidateListens(time.Unix(2, 0), map[netaddr.IPPort]string{addr: "777"}, func() map[string]uint32 {
		t.Fatal("no fds are read while every listen has a registered owner")
		return nil
	})
	assert.Equal(t, map[netaddr.IPPort]int{addr: 1}, c.getListens())
}

func TestVanishedListenClosesThenAgesOut(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	addr := netaddr.MustParseIPPort("10.0.0.5:8080")
	c := newTestContainer()
	c.processes[200] = listenProcess(200, now.Add(-time.Hour), "NS(1:2)")
	c.listens[addr] = map[uint32]*ListenDetails{200: {}}
	noOwners := func() map[string]uint32 { return nil }

	c.revalidateListens(now, map[netaddr.IPPort]string{}, noOwners)
	assert.Equal(t, map[netaddr.IPPort]int{addr: 0}, c.getListens(), "closed, still reported for a while")
	c.revalidateListens(now.Add(gcInterval+time.Second), map[netaddr.IPPort]string{}, noOwners)
	assert.Empty(t, c.listens)
}

func counterOf(t *testing.T, c prometheus.Counter) float64 {
	t.Helper()
	var m dto.Metric
	require.NoError(t, c.Write(&m))
	return m.GetCounter().GetValue()
}

func TestOrphanedListenWithoutAHolderIsCounted(t *testing.T) {
	addr := netaddr.MustParseIPPort("10.0.0.5:8080")
	c := newTestContainer()
	c.processes[300] = listenProcess(300, time.Unix(1, 0), "NS(1:2)")
	c.listens[addr] = map[uint32]*ListenDetails{100: {}}
	before := counterOf(t, agentMetrics.listensUnattributed)
	c.revalidateListens(time.Unix(2, 0), map[netaddr.IPPort]string{addr: "777"}, func() map[string]uint32 { return nil })
	assert.Equal(t, before+1, counterOf(t, agentMetrics.listensUnattributed))
	assert.Equal(t, map[netaddr.IPPort]int{addr: 0}, c.getListens(), "no registered process holds it")
}

func TestWildcardListenOfAnUnregisteredOwnerIsHidden(t *testing.T) {
	wildcard := netaddr.MustParseIPPort("0.0.0.0:8080")
	c := newTestContainer()
	c.listens[wildcard] = map[uint32]*ListenDetails{100: {NsIPs: []netaddr.IP{netaddr.MustParseIP("10.0.0.5")}}}
	assert.Empty(t, c.getListens(), "its IPs come only from registered owners")

	c.processes[100] = listenProcess(100, time.Unix(1, 0), "NS(1:2)")
	assert.Equal(t, map[netaddr.IPPort]int{netaddr.MustParseIPPort("10.0.0.5:8080"): 1}, c.getListens())
}

func TestMissingListenIsAddedForItsHolder(t *testing.T) {
	addr := netaddr.MustParseIPPort("10.0.0.5:8080")
	other := netaddr.MustParseIPPort("10.0.0.5:9090")
	c := newTestContainer()
	c.processes[200] = listenProcess(200, time.Unix(1, 0), "NS(1:2)")
	missing := counterValue(t, agentMetrics.listenAttributions, attributionGCMissing)
	// other belongs to a process outside the container that shares its network namespace.
	c.revalidateListens(time.Unix(2, 0), map[netaddr.IPPort]string{addr: "777", other: "888"}, func() map[string]uint32 {
		return map[string]uint32{"777": 200}
	})
	assert.Equal(t, map[netaddr.IPPort]int{addr: 1}, c.getListens())
	assert.Equal(t, missing+1, counterValue(t, agentMetrics.listenAttributions, attributionGCMissing))
}

func TestPickInodeOwnersPrefersTheOldestProcess(t *testing.T) {
	start := time.Unix(1_700_000_000, 0)
	owners := pickInodeOwners(map[uint32][]proc.Fd{
		30: {{SocketInode: "777"}, {SocketInode: "999"}},
		20: {{SocketInode: "777"}, {}},
		10: {{SocketInode: "888"}},
		40: {{SocketInode: "888"}},
	}, map[uint32]time.Time{20: start, 30: start.Add(time.Second), 10: start, 40: start})
	assert.Equal(t, map[string]uint32{"777": 20, "888": 10, "999": 30}, owners)
}

func TestLoopbackListensOnlyInTheHostNamespace(t *testing.T) {
	previous := hostNetNsId
	hostNetNsId = "NS(host)"
	t.Cleanup(func() { hostNetNsId = previous })
	loopback := netaddr.MustParseIPPort("127.0.0.1:8080")

	c := newTestContainer()
	c.processes[200] = listenProcess(200, time.Unix(1, 0), "NS(1:2)")
	c.listens[loopback] = map[uint32]*ListenDetails{200: {}}
	assert.Empty(t, c.getListens(), "a container's own loopback can't be reached from outside")

	c.processes[200] = listenProcess(200, time.Unix(1, 0), "NS(host)")
	assert.Equal(t, map[netaddr.IPPort]int{loopback: 1}, c.getListens())
}

func TestOwnerExitAsksForAResync(t *testing.T) {
	addr := netaddr.MustParseIPPort("10.0.0.5:8080")
	stubProcesses(t, nil, nil)
	c := newTestContainer()
	c.listenResyncCh = make(chan struct{}, 1)
	worker := listenProcess(300, time.Unix(1, 0), "NS(1:2)")
	worker.ctx, worker.cancelFunc = context.WithCancel(context.Background())
	owner := listenProcess(200, time.Unix(1, 0), "NS(1:2)")
	owner.ctx, owner.cancelFunc = context.WithCancel(context.Background())
	c.processes[300], c.processes[200] = worker, owner
	c.listens[addr] = map[uint32]*ListenDetails{200: {}}

	c.onProcessExit(300, false)
	assert.Empty(t, c.listenResyncCh, "a process without listens")
	c.onProcessExit(200, false)
	assert.Len(t, c.listenResyncCh, 1, "the listen's owner")
}
