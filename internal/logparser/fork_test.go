package logparser

import (
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// letters encodes n as a lowercase word so that messages differ in every word
// (digits are stripped by NewPattern, so numbers would not make patterns distinct).
func letters(n int) string {
	var b strings.Builder
	b.WriteString("w")
	for {
		b.WriteByte(byte('a' + n%26))
		n /= 26
		if n == 0 {
			break
		}
	}
	b.WriteString("x")
	return b.String()
}

func distinctError(i int) string {
	words := make([]string, 6)
	for k := range words {
		words[k] = letters(i*7 + k)
	}
	return "ERROR " + strings.Join(words, " ")
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met within 5s")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func sumMessages(p *Parser) int {
	var total int
	for _, c := range p.GetCounters() {
		total += c.Messages
	}
	return total
}

func TestParserMaxPatternsBoundsTable(t *testing.T) {
	ch := make(chan LogEntry)
	p := NewParserWithOptions(ch, nil, nil, 20*time.Millisecond, Options{MaxPatterns: 50, MaxSampleLen: 16})
	defer p.Stop()

	const n = 500
	for i := 0; i < n; i++ {
		ch <- LogEntry{Timestamp: time.Now(), Content: distinctError(i), Level: LevelError}
	}
	waitFor(t, func() bool { return sumMessages(p) == n })

	// 50 real patterns + 1 overflow bucket for the single level used.
	require.Equal(t, 51, p.PatternCount())
	require.Equal(t, n-50, p.Overflowed())
	for _, c := range p.GetCounters() {
		require.LessOrEqual(t, len(c.Sample), 21, "sample must be truncated") // "pattern limit reached" is 21 bytes
		if c.Hash == OverflowPatternHash {
			require.Equal(t, n-50, c.Messages)
		}
	}
}

func TestParserUnlimitedByDefaultOptions(t *testing.T) {
	ch := make(chan LogEntry)
	p := NewParserWithOptions(ch, nil, nil, 20*time.Millisecond, Options{})
	defer p.Stop()
	for i := 0; i < 100; i++ {
		ch <- LogEntry{Timestamp: time.Now(), Content: distinctError(i), Level: LevelError}
	}
	waitFor(t, func() bool { return sumMessages(p) == 100 })
	require.Equal(t, 100, p.PatternCount())
	require.Equal(t, 0, p.Overflowed())
}

func TestParserDisablePatternsStillEmits(t *testing.T) {
	ch := make(chan LogEntry)
	var emitted atomic.Int64
	cb := func(ts time.Time, level Level, hash string, msg string) {
		require.Empty(t, hash)
		emitted.Add(1)
	}
	p := NewParserWithOptions(ch, nil, cb, 20*time.Millisecond, Options{DisablePatterns: true})
	defer p.Stop()
	for i := 0; i < 20; i++ {
		ch <- LogEntry{Timestamp: time.Now(), Content: distinctError(i), Level: LevelError}
	}
	waitFor(t, func() bool { return emitted.Load() == 20 })
	require.Equal(t, 0, p.PatternCount())
}

func TestTruncateUtf8(t *testing.T) {
	require.Equal(t, "abc", truncateUtf8("abc", 0))
	require.Equal(t, "ab", truncateUtf8("abc", 2))
	require.Equal(t, "a", truncateUtf8("aé", 2)) // é is 2 bytes: never split a rune
}

// Stopping parsers while producers and a slow consumer are active must neither panic
// ("send on closed channel" in the upstream version) nor leave goroutines behind.
func TestParserStopUnderLoadLeavesNoGoroutines(t *testing.T) {
	runtime.GC()
	base := runtime.NumGoroutine()

	slow := func(ts time.Time, level Level, hash string, msg string) { time.Sleep(time.Millisecond) }
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		ch := make(chan LogEntry)
		p := NewParserWithOptions(ch, nil, slow, time.Millisecond, Options{MaxPatterns: 10})
		stopProducer := make(chan struct{})
		wg.Add(1)
		go func() { // producer, like TailReader: stops before the parser is stopped
			defer wg.Done()
			for j := 0; ; j++ {
				select {
				case <-stopProducer:
					return
				case ch <- LogEntry{Timestamp: time.Now(), Content: fmt.Sprintf("ERROR line %s", letters(j)), Level: LevelError}:
				}
			}
		}()
		time.Sleep(time.Duration(i%5) * time.Millisecond)
		close(stopProducer)
		p.Stop()
	}
	wg.Wait()
	waitFor(t, func() bool { return runtime.NumGoroutine() <= base })
}
