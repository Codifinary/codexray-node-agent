package logs

import (
	"sync"
	"testing"
	"time"

	"github.com/codifinary/logparser"
	"github.com/stretchr/testify/assert"
)

// JSON log lines (one object per line, as most structured loggers write them).
func TestLevelFilterJSONLines(t *testing.T) {
	type got struct {
		level logparser.Level
		msg   string
	}
	var mu sync.Mutex
	var out []got
	ch := make(chan logparser.LogEntry)
	p := logparser.NewParser(ch, nil, func(ts time.Time, level logparser.Level, hash, msg string) {
		mu.Lock()
		out = append(out, got{level, msg})
		mu.Unlock()
	}, 50*time.Millisecond)
	defer p.Stop()
	lines := []string{
		`{"time":"2026-11-15T10:00:00Z","level":"info","msg":"json-info"}`,
		`{"time":"2026-11-15T10:00:01Z","level":"warn","msg":"json-warn"}`,
		`{"severity":"error","timestamp":"2026-11-15T10:00:02Z","message":"json-error"}`,
		`{"level":"debug","msg":"json-debug no timestamp"}`,
		`{"time":"2026-11-15T10:00:03Z","msg":"json-nolevel"}`,
	}
	for _, l := range lines {
		ch <- logparser.LogEntry{Timestamp: time.Now(), Content: l}
	}
	time.Sleep(300 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if !assert.Len(t, out, 4) {
		return
	}
	assert.Equal(t, logparser.LevelInfo, out[0].level)
	assert.Equal(t, logparser.LevelWarning, out[1].level)
	assert.Equal(t, logparser.LevelError, out[2].level, "level read from a \"severity\" field")
	// a line without a timestamp continues the previous message, as for plain text
	assert.Contains(t, out[2].msg, "json-debug no timestamp")
	assert.Equal(t, logparser.LevelUnknown, out[3].level)
}
