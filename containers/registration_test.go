package containers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mdlayher/taskstats"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"inet.af/netaddr"
)

// counterValue reads one child of a self-metrics counter vector.
func counterValue(t *testing.T, vec *prometheus.CounterVec, label string) float64 {
	t.Helper()
	var m dto.Metric
	require.NoError(t, vec.WithLabelValues(label).Write(&m))
	return m.GetCounter().GetValue()
}

// stubProcesses replaces the process seams: taskstats knows the pids in taskstatsStarts, /proc
// the pids in procStarts, and new processes record whether they were closed.
func stubProcesses(t *testing.T, taskstatsStarts, procStarts map[uint32]time.Time) map[uint32][]*Process {
	t.Helper()
	created := map[uint32][]*Process{}
	prevTaskstats, prevProc, prevNew := taskstatsPIDFn, procStartTimeFn, newProcessFn
	taskstatsPIDFn = func(pid uint32) (*taskstats.Stats, error) {
		if at, ok := taskstatsStarts[pid]; ok {
			return &taskstats.Stats{BeginTime: at}, nil
		}
		return nil, errors.New("taskstats: no such process")
	}
	procStartTimeFn = func(pid uint32) (time.Time, error) {
		if at, ok := procStarts[pid]; ok {
			return at, nil
		}
		return time.Time{}, errors.New("no such process")
	}
	newProcessFn = func(pid uint32, startedAt time.Time, _ *Registry) *Process {
		p := &Process{Pid: pid, StartedAt: startedAt}
		p.ctx, p.cancelFunc = context.WithCancel(context.Background())
		created[pid] = append(created[pid], p)
		return p
	}
	t.Cleanup(func() { taskstatsPIDFn, procStartTimeFn, newProcessFn = prevTaskstats, prevProc, prevNew })
	return created
}

func newTestContainer() *Container {
	return &Container{processes: map[uint32]*Process{}, listens: map[netaddr.IPPort]map[uint32]*ListenDetails{}}
}

func TestProcessRegistration(t *testing.T) {
	boot := time.Unix(1_700_000_000, 0)
	created := stubProcesses(t,
		map[uint32]time.Time{10: boot},
		map[uint32]time.Time{20: boot.Add(time.Minute)})
	regs := agentMetrics.processRegistrations
	ok, fallback := counterValue(t, regs, registrationOK), counterValue(t, regs, registrationTaskstatsFallback)
	gone, failed := counterValue(t, regs, registrationGone), counterValue(t, regs, registrationTaskstatsFailed)
	c := newTestContainer()

	require.NotNil(t, c.onProcessStart(10))
	assert.Equal(t, boot, c.startedAt)
	assert.Nil(t, c.onProcessStart(20), "a start event never falls back to /proc")
	assert.Nil(t, c.processes[20])

	p := c.ensureProcess(20)
	require.NotNil(t, p, "a listen's owner falls back to /proc")
	assert.Equal(t, boot.Add(time.Minute), p.StartedAt)
	assert.Nil(t, c.ensureProcess(20), "already registered")
	assert.Nil(t, c.ensureProcess(30), "gone")

	assert.Equal(t, ok+1, counterValue(t, regs, registrationOK))
	assert.Equal(t, fallback+1, counterValue(t, regs, registrationTaskstatsFallback))
	assert.Equal(t, gone+1, counterValue(t, regs, registrationGone))
	assert.Equal(t, failed+1, counterValue(t, regs, registrationTaskstatsFailed))
	assert.Len(t, created[20], 1)
}

func TestProcessRegistrationDuplicatesAndPidReuse(t *testing.T) {
	boot := time.Unix(1_700_000_000, 0)
	starts := map[uint32]time.Time{10: boot}
	created := stubProcesses(t, starts, nil)
	c := newTestContainer()

	first := c.onProcessStart(10)
	require.NotNil(t, first)
	starts[10] = boot.Add(time.Second)
	assert.Nil(t, c.onProcessStart(10), "the same process seen twice, its times a rounding apart")
	assert.Same(t, first, c.processes[10])
	assert.NoError(t, first.ctx.Err(), "the registered process is kept running")

	starts[10] = boot.Add(time.Hour)
	second := c.onProcessStart(10)
	require.NotNil(t, second, "a new process with a reused pid")
	assert.Same(t, second, c.processes[10])
	assert.Error(t, first.ctx.Err(), "the old process is closed")
	assert.Len(t, created[10], 2)
}

func TestUpdateStartedAtDetectsRestarts(t *testing.T) {
	boot := time.Unix(1_700_000_000, 0)
	c := newTestContainer()
	assert.False(t, c.updateStartedAtLocked(boot))
	c.processes[1] = &Process{StartedAt: boot}
	assert.False(t, c.updateStartedAtLocked(boot.Add(time.Minute)), "a second process while the first runs")

	c.processes = map[uint32]*Process{2: {StartedAt: boot.Add(time.Hour)}}
	assert.True(t, c.updateStartedAtLocked(boot.Add(time.Hour)), "every process is newer than the container's start")
	assert.Equal(t, 1, c.restarts)
	assert.Equal(t, boot.Add(time.Hour), c.startedAt)
}
