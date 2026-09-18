// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package logs

import (
	"path/filepath"
	"testing"

	"github.com/codifinary/logparser"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func journaldReaderNewTest() *JournaldReader {
	return &JournaldReader{subscribers: map[string]chan<- logparser.LogEntry{}}
}

func TestJournaldReaderSubscribe(t *testing.T) {
	r := journaldReaderNewTest()
	a := make(chan logparser.LogEntry)
	b := make(chan logparser.LogEntry)

	require.NoError(t, r.Subscribe("/system.slice/a.service", a))
	require.NoError(t, r.Subscribe("/system.slice/b.service", b))
	assert.Error(t, r.Subscribe("/system.slice/a.service", b), "a cgroup has exactly one subscriber")
	assert.Len(t, r.subscribers, 2)

	r.Unsubscribe("/system.slice/a.service")
	assert.Len(t, r.subscribers, 1)
	require.NoError(t, r.Subscribe("/system.slice/a.service", a), "re-subscribing after unsubscribe is allowed")

	assert.NotPanics(t, func() { r.Unsubscribe("/system.slice/unknown.service") })
	assert.Len(t, r.subscribers, 2)
}

func TestNewJournaldReaderNoJournal(t *testing.T) {
	// No journal files in any of the paths (a node without persistent journald):
	// the constructor must return an error, never a reader with a nil journal.
	var r *JournaldReader
	var err error
	require.NotPanics(t, func() {
		r, err = NewJournaldReader(filepath.Join(t.TempDir(), "missing"), t.TempDir())
	})
	assert.Error(t, err)
	assert.Nil(t, r)
}
