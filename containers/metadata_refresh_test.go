package containers

import (
	"errors"
	"testing"
	"time"

	"github.com/codifinary/codexray-node-agent/cgroup"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"inet.af/netaddr"
)

// stubInspect makes restart re-inspections immediate and answers them with md or err.
func stubInspect(t *testing.T, md *ContainerMetadata, err error) <-chan string {
	t.Helper()
	calls := make(chan string, 4)
	previousInspect, previousDelay := dockerdInspectFn, metadataRefreshDelay
	dockerdInspectFn = func(id string) (*ContainerMetadata, error) {
		calls <- id
		return md, err
	}
	metadataRefreshDelay = 0
	t.Cleanup(func() { dockerdInspectFn, metadataRefreshDelay = previousInspect, previousDelay })
	return calls
}

func newRestartedDockerContainer() *Container {
	c := newTestContainer()
	c.id = "/docker/rnd"
	c.cgroup = &cgroup.Cgroup{Id: "/system.slice/docker-4d1f.scope", ContainerType: cgroup.ContainerTypeDocker, ContainerId: "4d1f"}
	c.metadata = &ContainerMetadata{logPath: "/var/log/rnd.log",
		hostListens: map[string][]netaddr.IPPort{"dockerd": {netaddr.MustParseIPPort("0.0.0.0:40001")}}}
	c.done = make(chan struct{})
	return c
}

func refreshed(c *Container) func() bool {
	return func() bool {
		c.lock.Lock()
		defer c.lock.Unlock()
		return !c.metadataRefreshInFlight
	}
}

func TestRestartRefreshesPortBindings(t *testing.T) {
	newPort := netaddr.MustParseIPPort("0.0.0.0:40002")
	calls := stubInspect(t, &ContainerMetadata{logPath: "/other.log",
		hostListens: map[string][]netaddr.IPPort{"dockerd": {newPort}}}, nil)
	ok := counterValue(t, agentMetrics.dockerMetadataRefreshes, refreshOK)
	skipped := counterValue(t, agentMetrics.dockerMetadataRefreshes, refreshSkipped)
	c := newRestartedDockerContainer()

	c.lock.Lock()
	c.scheduleMetadataRefreshLocked()
	c.scheduleMetadataRefreshLocked()
	c.lock.Unlock()
	assert.Equal(t, "4d1f", <-calls)
	require.Eventually(t, refreshed(c), time.Second, time.Millisecond)

	c.lock.Lock()
	defer c.lock.Unlock()
	assert.Equal(t, []netaddr.IPPort{newPort}, c.metadata.hostListens["dockerd"])
	assert.Equal(t, "/var/log/rnd.log", c.metadata.logPath, "only the bindings and networks are replaced")
	assert.Equal(t, ok+1, counterValue(t, agentMetrics.dockerMetadataRefreshes, refreshOK))
	assert.Equal(t, skipped+1, counterValue(t, agentMetrics.dockerMetadataRefreshes, refreshSkipped), "one refresh at a time")
	assert.Empty(t, calls)
}

func TestRestartRefreshIsDroppedWhenStale(t *testing.T) {
	old := []netaddr.IPPort{netaddr.MustParseIPPort("0.0.0.0:40001")}
	for _, tc := range []struct {
		name   string
		err    error
		change func(c *Container)
	}{
		{name: "inspect failed", err: errors.New("dockerd down")},
		{name: "the cgroup was replaced", change: func(c *Container) { c.cgroup = &cgroup.Cgroup{Id: "other"} }},
		{name: "the container is gone", change: func(c *Container) { close(c.done) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := stubInspect(t, &ContainerMetadata{hostListens: map[string][]netaddr.IPPort{}}, tc.err)
			c := newRestartedDockerContainer()
			c.lock.Lock()
			c.scheduleMetadataRefreshLocked()
			if tc.change != nil {
				tc.change(c)
			}
			c.lock.Unlock()
			if tc.name != "the container is gone" {
				<-calls
				require.Eventually(t, refreshed(c), time.Second, time.Millisecond)
			} else {
				time.Sleep(10 * time.Millisecond)
			}
			c.lock.Lock()
			defer c.lock.Unlock()
			assert.Equal(t, old, c.metadata.hostListens["dockerd"])
		})
	}
}

func TestOnlyDockerContainersAreRefreshed(t *testing.T) {
	stubInspect(t, nil, nil)
	c := newRestartedDockerContainer()
	c.cgroup.ContainerType = cgroup.ContainerTypeContainerd
	c.lock.Lock()
	c.scheduleMetadataRefreshLocked()
	c.lock.Unlock()
	assert.False(t, c.metadataRefreshInFlight)
}
