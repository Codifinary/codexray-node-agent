// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package containers

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/mdlayher/taskstats"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTaskstatsWithoutClient(t *testing.T) {
	saved := taskstatsClient
	taskstatsClient = nil
	t.Cleanup(func() { taskstatsClient = saved })

	s, err := TaskstatsTGID(1)
	assert.Error(t, err)
	assert.Nil(t, s)

	s, err = TaskstatsPID(1)
	assert.Error(t, err)
	assert.Nil(t, s)
}

// taskstatsTestSource is a fake genetlink taskstats client (the real one needs
// CAP_NET_ADMIN to query other processes); pids without an entry fail.
type taskstatsTestSource struct {
	lock sync.Mutex
	pid  map[int]*taskstats.Stats
	tgid map[int]*taskstats.Stats
}

func (s *taskstatsTestSource) PID(pid int) (*taskstats.Stats, error) {
	s.lock.Lock()
	defer s.lock.Unlock()
	if st := s.pid[pid]; st != nil {
		return st, nil
	}
	return nil, errors.New("no such process")
}

func (s *taskstatsTestSource) TGID(pid int) (*taskstats.Stats, error) {
	s.lock.Lock()
	defer s.lock.Unlock()
	if st := s.tgid[pid]; st != nil {
		return st, nil
	}
	return nil, errors.New("no such process")
}

func (s *taskstatsTestSource) setPID(pid uint32, st *taskstats.Stats) {
	s.lock.Lock()
	defer s.lock.Unlock()
	s.pid[int(pid)] = st
}

func (s *taskstatsTestSource) setTGID(pid uint32, st *taskstats.Stats) {
	s.lock.Lock()
	defer s.lock.Unlock()
	s.tgid[int(pid)] = st
}

// taskstatsTestUse installs a fake taskstats client for the duration of the test.
func taskstatsTestUse(t *testing.T) *taskstatsTestSource {
	t.Helper()
	src := &taskstatsTestSource{pid: map[int]*taskstats.Stats{}, tgid: map[int]*taskstats.Stats{}}
	saved := taskstatsClient
	taskstatsClient = src
	t.Cleanup(func() { taskstatsClient = saved })
	return src
}

func TestTaskstatsInit(t *testing.T) {
	savedClient, savedNew := taskstatsClient, newTaskstatsClient
	t.Cleanup(func() { taskstatsClient, newTaskstatsClient = savedClient, savedNew })

	taskstatsClient = nil
	newTaskstatsClient = func() (*taskstats.Client, error) { return nil, errors.New("genetlink family TASKSTATS not found") }
	assert.EqualError(t, TaskstatsInit(), "genetlink family TASKSTATS not found")
	assert.Nil(t, taskstatsClient, "a failed init must not install a (typed-nil) client")

	c := &taskstats.Client{}
	newTaskstatsClient = func() (*taskstats.Client, error) { return c, nil }
	require.NoError(t, TaskstatsInit())
	assert.Same(t, c, taskstatsClient)
}

func TestTaskstatsQueries(t *testing.T) {
	src := taskstatsTestUse(t)
	begin := time.Unix(1700000000, 0)
	src.setPID(42, &taskstats.Stats{BeginTime: begin})
	src.setTGID(42, &taskstats.Stats{CPUDelay: time.Second, BlockIODelay: 2 * time.Second})

	s, err := TaskstatsPID(42)
	require.NoError(t, err)
	assert.Equal(t, begin, s.BeginTime)
	s, err = TaskstatsTGID(42)
	require.NoError(t, err)
	assert.Equal(t, time.Second, s.CPUDelay)
	assert.Equal(t, 2*time.Second, s.BlockIODelay)

	s, err = TaskstatsPID(43)
	assert.Error(t, err)
	assert.Nil(t, s)
	s, err = TaskstatsTGID(43)
	assert.Error(t, err)
	assert.Nil(t, s)
}
