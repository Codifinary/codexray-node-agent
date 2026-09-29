// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package containers

import (
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/codifinary/codexray-node-agent/cgroup"
	"github.com/codifinary/codexray-node-agent/logs"
	"github.com/codifinary/logparser"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestJournaldWithoutReader(t *testing.T) {
	saved := journaldReader
	journaldReader = nil
	t.Cleanup(func() { journaldReader = saved })

	cg := &cgroup.Cgroup{Id: "/system.slice/nginx.service", ContainerType: cgroup.ContainerTypeSystemdService, ContainerId: "/system.slice/nginx.service"}
	ch := make(chan logparser.LogEntry, 1)
	assert.Error(t, JournaldSubscribe(cg, ch))
	assert.NotPanics(t, func() { JournaldUnsubscribe(cg) })
	assert.Empty(t, ch)
}

// journaldTestReader is a fake systemd journal reader (a real one needs a
// non-empty journal and libsystemd).
type journaldTestReader struct {
	lock         sync.Mutex
	subscribers  map[string]chan<- logparser.LogEntry
	unsubscribed []string
}

func (r *journaldTestReader) Subscribe(cg string, ch chan<- logparser.LogEntry) error {
	r.lock.Lock()
	defer r.lock.Unlock()
	if _, ok := r.subscribers[cg]; ok {
		return fmt.Errorf("duplicate subscriber for cgroup %s", cg)
	}
	r.subscribers[cg] = ch
	return nil
}

func (r *journaldTestReader) Unsubscribe(cg string) {
	r.lock.Lock()
	defer r.lock.Unlock()
	delete(r.subscribers, cg)
	r.unsubscribed = append(r.unsubscribed, cg)
}

func journaldTestUse(t *testing.T) *journaldTestReader {
	t.Helper()
	r := &journaldTestReader{subscribers: map[string]chan<- logparser.LogEntry{}}
	saved := journaldReader
	journaldReader = r
	t.Cleanup(func() { journaldReader = saved })
	return r
}

func TestJournaldInit(t *testing.T) {
	root := containerTestHostPath(t)
	savedReader, savedNew := journaldReader, newJournaldReader
	t.Cleanup(func() { journaldReader, newJournaldReader = savedReader, savedNew })

	var paths []string
	newJournaldReader = func(journalPaths ...string) (*logs.JournaldReader, error) {
		paths = journalPaths
		return nil, errors.New("systemd journal not found")
	}
	journaldReader = nil
	assert.Error(t, JournaldInit())
	assert.Nil(t, journaldReader, "a failed init must not install a (typed-nil) reader")
	// the volatile journal is preferred over the persistent one, both looked up on the host
	assert.Equal(t, []string{filepath.Join(root, "/run/log/journal"), filepath.Join(root, "/var/log/journal")}, paths)

	r := &logs.JournaldReader{}
	newJournaldReader = func(...string) (*logs.JournaldReader, error) { return r, nil }
	require.NoError(t, JournaldInit())
	assert.Same(t, r, journaldReader)
}

func TestJournaldSubscribe(t *testing.T) {
	r := journaldTestUse(t)
	cg := &cgroup.Cgroup{Id: "/system.slice/nginx.service", ContainerType: cgroup.ContainerTypeSystemdService}
	ch := make(chan logparser.LogEntry)

	require.NoError(t, JournaldSubscribe(cg, ch))
	assert.Contains(t, r.subscribers, cg.Id, "subscriptions are keyed by the cgroup id")
	assert.Error(t, JournaldSubscribe(cg, ch), "reader errors are propagated")

	JournaldUnsubscribe(cg)
	assert.NotContains(t, r.subscribers, cg.Id)
	assert.Equal(t, []string{cg.Id}, r.unsubscribed)
}
