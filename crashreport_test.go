package main

import (
	"os"
	"path/filepath"
	"runtime/debug"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadLastRun(t *testing.T) {
	tests := []struct {
		name      string
		crashLog  string
		marker    bool
		want      runOutcome
		wantCrash string
	}{
		{name: "first start", want: runClean},
		{name: "clean shutdown leaves an empty crash log", crashLog: "", want: runClean},
		{name: "a crash wins over the marker", crashLog: "fatal error: runtime: out of memory\n", marker: true, want: runCrashed, wantCrash: "fatal error: runtime: out of memory\n"},
		{name: "killed without a crash report", crashLog: "", marker: true, want: runKilled},
		{name: "a blank crash log is not a crash", crashLog: "\n  \n", marker: true, want: runKilled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if tt.name != "first start" {
				require.NoError(t, os.WriteFile(filepath.Join(dir, crashLogName), []byte(tt.crashLog), 0600))
			}
			if tt.marker {
				require.NoError(t, os.WriteFile(filepath.Join(dir, runningMarkerName), []byte("1"), 0600))
			}

			last, err := readLastRun(dir)

			require.NoError(t, err)
			assert.Equal(t, tt.want, last.outcome)
			assert.Equal(t, tt.wantCrash, last.crash)
		})
	}
}

func TestReadLastRunCutsALongCrash(t *testing.T) {
	dir := t.TempDir()
	long := make([]byte, crashReportMaxBytes+100)
	for i := range long {
		long[i] = 'x'
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, crashLogName), long, 0600))

	last, err := readLastRun(dir)

	require.NoError(t, err)
	assert.Len(t, last.crash, crashReportMaxBytes)
}

func TestStartCrashCaptureKeepsThePreviousCrashAndMarksTheRun(t *testing.T) {
	t.Cleanup(func() { _ = debug.SetCrashOutput(nil, debug.CrashOptions{}) })
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, crashLogName), []byte("panic: boom\n"), 0600))

	last, markCleanShutdown, err := startCrashCapture(dir)

	require.NoError(t, err)
	assert.Equal(t, runCrashed, last.outcome)
	prev, err := os.ReadFile(filepath.Join(dir, crashLogName+".prev"))
	require.NoError(t, err)
	assert.Equal(t, "panic: boom\n", string(prev), "the crash is kept for a person to read")
	current, err := os.ReadFile(filepath.Join(dir, crashLogName))
	require.NoError(t, err)
	assert.Empty(t, current, "this run starts with an empty crash log")
	assert.FileExists(t, filepath.Join(dir, runningMarkerName))

	markCleanShutdown()

	assert.NoFileExists(t, filepath.Join(dir, runningMarkerName))
	next, err := readLastRun(dir)
	require.NoError(t, err)
	assert.Equal(t, runClean, next.outcome)
}
