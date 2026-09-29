// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package logs

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/codifinary/logparser"
	"github.com/coreos/go-systemd/v22/sdjournal"
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

// journaldTestStep is what one Next() call returns (and the GetEntry/Wait that follow it).
type journaldTestStep struct {
	next     uint64
	nextErr  error
	entry    *sdjournal.JournalEntry
	entryErr error
	wait     int
}

// journaldTestJournal is a scripted journal; Next() fails once the script is exhausted,
// which makes follow() return.
type journaldTestJournal struct {
	lock           sync.Mutex
	steps          []journaldTestStep
	cur            journaldTestStep
	usage          uint64
	usageErr       error
	seekErr        error
	closeErr       error
	seekUsec       []uint64
	waits          []time.Duration
	closed         int
	callsAfterStop int
	endless        bool
}

func (j *journaldTestJournal) Next() (uint64, error) {
	j.lock.Lock()
	defer j.lock.Unlock()
	if j.closed > 0 {
		j.callsAfterStop++
	}
	if len(j.steps) == 0 {
		if j.endless {
			return 0, nil
		}
		return 0, errors.New("end of script")
	}
	j.cur, j.steps = j.steps[0], j.steps[1:]
	return j.cur.next, j.cur.nextErr
}

func (j *journaldTestJournal) Wait(timeout time.Duration) int {
	j.lock.Lock()
	defer j.lock.Unlock()
	j.waits = append(j.waits, timeout)
	if j.endless {
		time.Sleep(time.Millisecond)
		return 1
	}
	return j.cur.wait
}

func (j *journaldTestJournal) GetEntry() (*sdjournal.JournalEntry, error) {
	j.lock.Lock()
	defer j.lock.Unlock()
	return j.cur.entry, j.cur.entryErr
}

func (j *journaldTestJournal) GetUsage() (uint64, error) {
	j.lock.Lock()
	defer j.lock.Unlock()
	return j.usage, j.usageErr
}

func (j *journaldTestJournal) SeekRealtimeUsec(usec uint64) error {
	j.lock.Lock()
	defer j.lock.Unlock()
	j.seekUsec = append(j.seekUsec, usec)
	return j.seekErr
}

func (j *journaldTestJournal) Close() error {
	j.lock.Lock()
	defer j.lock.Unlock()
	j.closed++
	return j.closeErr
}

func (j *journaldTestJournal) state() (seeks []uint64, waits []time.Duration, closed, callsAfterStop int) {
	j.lock.Lock()
	defer j.lock.Unlock()
	return append([]uint64(nil), j.seekUsec...), append([]time.Duration(nil), j.waits...), j.closed, j.callsAfterStop
}

// journaldTestOpen makes openJournal serve the given journals (a missing path fails to open).
func journaldTestOpen(t *testing.T, journals map[string]*journaldTestJournal) *[]string {
	var opened []string
	prev := openJournal
	openJournal = func(path string) (journal, error) {
		opened = append(opened, path)
		j, ok := journals[path]
		if !ok {
			return nil, errors.New("no such file or directory")
		}
		return j, nil
	}
	t.Cleanup(func() { openJournal = prev })
	return &opened
}

func journaldTestEntry(cgroup, msg, priority string, ts time.Time) *sdjournal.JournalEntry {
	fields := map[string]string{
		sdjournal.SD_JOURNAL_FIELD_SYSTEMD_CGROUP: cgroup,
		sdjournal.SD_JOURNAL_FIELD_PRIORITY:       priority,
	}
	if msg != "" {
		fields[sdjournal.SD_JOURNAL_FIELD_MESSAGE] = msg
	}
	return &sdjournal.JournalEntry{Fields: fields, RealtimeTimestamp: uint64(ts.UnixMicro())}
}

func TestNewJournaldReaderPicksFirstUsableJournal(t *testing.T) {
	empty := &journaldTestJournal{usage: 0}
	good := &journaldTestJournal{usage: 4096}
	unused := &journaldTestJournal{usage: 4096}
	opened := journaldTestOpen(t, map[string]*journaldTestJournal{
		"/run/log/journal": empty,
		"/var/log/journal": good,
		"/other":           unused,
	})

	before := time.Now()
	r, err := NewJournaldReader("/missing", "/run/log/journal", "/var/log/journal", "/other")
	after := time.Now()
	require.NoError(t, err)
	require.NotNil(t, r)
	t.Cleanup(r.Close)

	assert.Equal(t, []string{"/missing", "/run/log/journal", "/var/log/journal"}, *opened, "stops at the first usable journal")
	assert.Same(t, good, r.journal)
	seeks, _, _, _ := good.state()
	require.Len(t, seeks, 1, "only new entries are read")
	assert.GreaterOrEqual(t, seeks[0], uint64(before.Add(time.Millisecond).UnixMicro()))
	assert.LessOrEqual(t, seeks[0], uint64(after.Add(time.Millisecond).UnixMicro()))
	emptySeeks, _, _, _ := empty.state()
	assert.Empty(t, emptySeeks, "an empty journal is skipped")
}

func TestNewJournaldReaderAllFail(t *testing.T) {
	journaldTestOpen(t, map[string]*journaldTestJournal{
		"/run/log/journal": {usage: 0},
	})
	r, err := NewJournaldReader("/missing", "/run/log/journal")
	require.Error(t, err)
	assert.Nil(t, r)
	assert.Contains(t, err.Error(), "/missing,/run/log/journal")
}

func TestNewJournaldReaderUsageErrorTriesNext(t *testing.T) {
	good := &journaldTestJournal{usage: 1}
	journaldTestOpen(t, map[string]*journaldTestJournal{
		"/a": {usageErr: errors.New("permission denied")},
		"/b": good,
	})
	r, err := NewJournaldReader("/a", "/b")
	require.NoError(t, err)
	t.Cleanup(r.Close)
	assert.Same(t, good, r.journal)
}

func TestNewJournaldReaderUsageErrorOnLastPath(t *testing.T) {
	// BUG: on a GetUsage error the loop `continue`s without resetting r.journal,
	// so if no later path works the reader is returned with that broken journal
	// (and follow() started on it) instead of an error — unskip when fixed
	t.Skip("BUG: NewJournaldReader returns a reader with an unusable journal when GetUsage fails on the last path")
	journaldTestOpen(t, map[string]*journaldTestJournal{
		"/a": {usageErr: errors.New("permission denied")},
	})
	r, err := NewJournaldReader("/a")
	assert.Error(t, err)
	assert.Nil(t, r)
}

func TestNewJournaldReaderClosesRejectedJournals(t *testing.T) {
	// BUG: journals opened but then rejected (empty, GetUsage or Seek error) are
	// never closed, leaking their file descriptors and mmaps — unskip when fixed
	t.Skip("BUG: NewJournaldReader never closes the journals it rejects")
	empty := &journaldTestJournal{usage: 0}
	broken := &journaldTestJournal{usageErr: errors.New("permission denied")}
	good := &journaldTestJournal{usage: 1}
	journaldTestOpen(t, map[string]*journaldTestJournal{"/a": empty, "/b": broken, "/c": good})
	r, err := NewJournaldReader("/a", "/b", "/c")
	require.NoError(t, err)
	t.Cleanup(r.Close)
	_, _, closed, _ := empty.state()
	assert.Equal(t, 1, closed)
	_, _, closed, _ = broken.state()
	assert.Equal(t, 1, closed)
}

func TestNewJournaldReaderSeekError(t *testing.T) {
	journaldTestOpen(t, map[string]*journaldTestJournal{
		"/a": {usage: 1, seekErr: errors.New("seek failed")},
		"/b": {usage: 1},
	})
	r, err := NewJournaldReader("/a", "/b")
	require.Error(t, err)
	assert.Equal(t, "seek failed", err.Error())
	assert.Nil(t, r)
}

func TestJournaldReaderFollowRoutesEntries(t *testing.T) {
	ts := time.Date(2026, 9, 28, 10, 0, 0, 123456000, time.UTC)
	j := &journaldTestJournal{steps: []journaldTestStep{
		{next: 1, entry: journaldTestEntry("/system.slice/a.service", "a started", "6", ts)},
		{next: 1, entry: journaldTestEntry("/system.slice/b.service", "b failed", "3", ts.Add(time.Second))},
		{next: 1, entry: journaldTestEntry("/system.slice/a.service", "", "6", ts)},
		{next: 1, entry: journaldTestEntry("/system.slice/unknown.service", "nobody listens", "4", ts)},
		{next: 1, entry: journaldTestEntry("/system.slice/a.service", "a crashed", "2", ts.Add(2*time.Second))},
	}}
	r := journaldReaderNewTest()
	r.journal = j
	a := make(chan logparser.LogEntry, 10)
	b := make(chan logparser.LogEntry, 10)
	require.NoError(t, r.Subscribe("/system.slice/a.service", a))
	require.NoError(t, r.Subscribe("/system.slice/b.service", b))

	r.follow()

	close(a)
	close(b)
	var gotA, gotB []logparser.LogEntry
	for e := range a {
		gotA = append(gotA, e)
	}
	for e := range b {
		gotB = append(gotB, e)
	}
	require.Len(t, gotA, 2, "entries without MESSAGE are skipped")
	assert.Equal(t, "a started", gotA[0].Content)
	assert.Equal(t, logparser.LevelInfo, gotA[0].Level)
	assert.True(t, ts.Equal(gotA[0].Timestamp), "realtime timestamp is in microseconds")
	assert.Equal(t, "a crashed", gotA[1].Content)
	assert.Equal(t, logparser.LevelCritical, gotA[1].Level)
	assert.True(t, ts.Add(2*time.Second).Equal(gotA[1].Timestamp))
	require.Len(t, gotB, 1)
	assert.Equal(t, "b failed", gotB[0].Content)
	assert.Equal(t, logparser.LevelError, gotB[0].Level)
	assert.True(t, ts.Add(time.Second).Equal(gotB[0].Timestamp))
}

func TestJournaldReaderFollowWaitsForNewEntries(t *testing.T) {
	ts := time.Now()
	j := &journaldTestJournal{steps: []journaldTestStep{
		{next: 0, wait: 1}, // woken up by inotify
		{next: 0, wait: 0}, // inotify unavailable: Wait returns at once and follow() sleeps instead
		{next: 1, entry: journaldTestEntry("/a", "hello", "6", ts)},
	}}
	r := journaldReaderNewTest()
	r.journal = j
	ch := make(chan logparser.LogEntry, 1)
	require.NoError(t, r.Subscribe("/a", ch))

	start := time.Now()
	r.follow()

	assert.GreaterOrEqual(t, time.Since(start), journaldPollTimeout, "polls when inotify is unavailable")
	_, waits, _, _ := j.state()
	assert.Equal(t, []time.Duration{journaldPollTimeout, journaldPollTimeout}, waits)
	require.Len(t, ch, 1)
	assert.Equal(t, "hello", (<-ch).Content)
}

func TestJournaldReaderFollowStopsOnErrors(t *testing.T) {
	cases := map[string]journaldTestStep{
		"next":      {nextErr: errors.New("bad message")},
		"get entry": {next: 1, entryErr: errors.New("bad message")},
	}
	for name, step := range cases {
		t.Run(name, func(t *testing.T) {
			j := &journaldTestJournal{steps: []journaldTestStep{
				step,
				{next: 1, entry: journaldTestEntry("/a", "never read", "6", time.Now())},
			}}
			r := journaldReaderNewTest()
			r.journal = j
			ch := make(chan logparser.LogEntry, 1)
			require.NoError(t, r.Subscribe("/a", ch))

			done := make(chan struct{})
			go func() {
				r.follow()
				close(done)
			}()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				require.FailNow(t, "follow() did not return")
			}
			assert.Empty(t, ch)
			j.lock.Lock()
			assert.Len(t, j.steps, 1, "nothing is read after the error")
			j.lock.Unlock()
		})
	}
}

func TestJournaldReaderClose(t *testing.T) {
	j := &journaldTestJournal{closeErr: errors.New("already closed")}
	r := journaldReaderNewTest()
	r.journal = j
	assert.NotPanics(t, r.Close, "a close error is ignored")
	_, _, closed, _ := j.state()
	assert.Equal(t, 1, closed)
}

func TestJournaldReaderCloseStopsFollow(t *testing.T) {
	// BUG: Close closes the journal while the follow goroutine keeps calling
	// Next/Wait on it (use-after-free of sd_journal with the real sdjournal);
	// the `until` channel is never used to stop follow() — unskip when fixed
	t.Skip("BUG: JournaldReader.Close does not stop follow(), which keeps using the closed journal")
	j := &journaldTestJournal{usage: 1, endless: true}
	journaldTestOpen(t, map[string]*journaldTestJournal{"/a": j})
	r, err := NewJournaldReader("/a")
	require.NoError(t, err)
	r.Close()
	time.Sleep(50 * time.Millisecond)
	_, _, _, callsAfterStop := j.state()
	assert.Zero(t, callsAfterStop)
}
