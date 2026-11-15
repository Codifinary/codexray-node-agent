package logs

import (
	"sync"
	"testing"
	"time"

	"github.com/codifinary/logparser"
	"github.com/stretchr/testify/assert"
)

func TestLevelFilterAll(t *testing.T) {
	assert.Nil(t, levelFilter("all", false))
	assert.Nil(t, levelFilter("", false))
}

func TestLevelFilterWarning(t *testing.T) {
	keep := levelFilter("warning", false)
	cases := map[logparser.Level]string{
		logparser.LevelCritical: "",
		logparser.LevelError:    "",
		logparser.LevelWarning:  "",
		logparser.LevelInfo:     "below_min_level",
		logparser.LevelDebug:    "below_min_level",
		logparser.LevelUnknown:  "unknown_level",
	}
	for level, reason := range cases {
		ok, r := keep(level)
		assert.Equal(t, reason == "", ok, level.String())
		assert.Equal(t, reason, r, level.String())
	}
}

func TestLevelFilterErrorKeepUnknown(t *testing.T) {
	keep := levelFilter("error", true)
	for level, want := range map[logparser.Level]bool{
		logparser.LevelCritical: true,
		logparser.LevelError:    true,
		logparser.LevelWarning:  false,
		logparser.LevelInfo:     false,
		logparser.LevelUnknown:  true,
	} {
		ok, _ := keep(level)
		assert.Equal(t, want, ok, level.String())
	}
}

// TestLevelFilterThroughParser feeds typical log formats through the real parser (level
// detection + multiline assembly) and checks which messages a warning filter exports.
func TestLevelFilterThroughParser(t *testing.T) {
	keep := levelFilter("warning", false)
	var mu sync.Mutex
	var exported []string
	ch := make(chan logparser.LogEntry)
	p := logparser.NewParser(ch, nil, func(ts time.Time, level logparser.Level, hash, msg string) {
		if ok, _ := keep(level); ok {
			mu.Lock()
			exported = append(exported, msg)
			mu.Unlock()
		}
	}, 50*time.Millisecond)
	defer p.Stop()

	lines := []string{
		`2026-11-15T10:00:00Z INFO server started on :8080`,
		`2026-11-15T10:00:01Z WARN slow query took 1.2s`,
		`2026-11-15T10:00:01Z level=error msg="payment failed" order=42`,
		`2026-11-15T10:00:01Z level=debug msg="cache hit"`,
		`E1115 10:00:02.000000 1 main.go:10] cannot reach upstream`,
		`2026-11-15 10:00:03,000 ERROR Traceback (most recent call last):`,
		`  File "app.py", line 3, in <module>`,
		`ZeroDivisionError: division by zero`,
		`2026-11-15T10:00:04Z GET /healthz 200`,
	}
	now := time.Now()
	for _, l := range lines {
		ch <- logparser.LogEntry{Timestamp: now, Content: l, Level: logparser.LevelUnknown}
	}
	assert.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(exported) >= 4
	}, 2*time.Second, 20*time.Millisecond)
	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if assert.Len(t, exported, 4, "%q", exported) {
		assert.Contains(t, exported[0], "slow query")
		assert.Contains(t, exported[1], "payment failed")
		assert.Contains(t, exported[2], "cannot reach upstream")
		// the traceback continuation lines travel with the ERROR line that started it
		assert.Contains(t, exported[3], "ZeroDivisionError")
	}
}
