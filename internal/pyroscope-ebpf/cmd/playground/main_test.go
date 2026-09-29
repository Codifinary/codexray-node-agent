//go:build linux

package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/go-kit/log"
	"github.com/grafana/pyroscope/ebpf/sd"
	"github.com/stretchr/testify/require"
)

// fakeStringer lets us simulate go-kit/log/level's level.Value, which
// implements fmt.Stringer, without importing the level package's internal
// type.
type fakeStringer string

func (f fakeStringer) String() string { return string(f) }

func TestSplitLog_RoutesErrorsToErrWriter(t *testing.T) {
	var errBuf, restBuf bytes.Buffer
	sl := splitLog{
		err:  log.NewLogfmtLogger(&errBuf),
		rest: log.NewLogfmtLogger(&restBuf),
	}

	err := sl.Log("level", fakeStringer("error"), "msg", "boom")
	require.NoError(t, err)
	require.Contains(t, errBuf.String(), "boom")
	require.Empty(t, restBuf.String())
}

func TestSplitLog_RoutesNonErrorsToRestWriter(t *testing.T) {
	var errBuf, restBuf bytes.Buffer
	sl := splitLog{
		err:  log.NewLogfmtLogger(&errBuf),
		rest: log.NewLogfmtLogger(&restBuf),
	}

	err := sl.Log("level", fakeStringer("info"), "msg", "hello")
	require.NoError(t, err)
	require.Contains(t, restBuf.String(), "hello")
	require.Empty(t, errBuf.String())
}

func TestSplitLog_NoLevelKeyGoesToRest(t *testing.T) {
	var errBuf, restBuf bytes.Buffer
	sl := splitLog{
		err:  log.NewLogfmtLogger(&errBuf),
		rest: log.NewLogfmtLogger(&restBuf),
	}

	err := sl.Log("msg", "no level here")
	require.NoError(t, err)
	require.Contains(t, restBuf.String(), "no level here")
	require.Empty(t, errBuf.String())
}

func TestSplitLog_OddKeyvalsGoesToErr(t *testing.T) {
	var errBuf, restBuf bytes.Buffer
	sl := splitLog{
		err:  log.NewLogfmtLogger(&errBuf),
		rest: log.NewLogfmtLogger(&restBuf),
	}

	// odd number of keyvals: dangling key with no value
	err := sl.Log("dangling")
	require.NoError(t, err)
	require.NotEmpty(t, errBuf.String())
	require.Empty(t, restBuf.String())
}

func TestRelabelProcessTargets_NoConfigKeepsAllTargets(t *testing.T) {
	targets := []sd.DiscoveryTarget{
		{"__process_pid__": "1", "service_name": "svc-a"},
		{"__process_pid__": "2", "service_name": "svc-b"},
	}
	out := relabelProcessTargets(targets, nil)
	require.Len(t, out, 2)
}

func TestRelabelProcessTargets_DropAction(t *testing.T) {
	targets := []sd.DiscoveryTarget{
		{"__process_pid__": "1", "service_name": "keep-me"},
		{"__process_pid__": "2", "service_name": "drop-me"},
	}
	cfg := []*RelabelConfig{
		{
			SourceLabels: []string{"service_name"},
			Regex:        "drop-me",
			Action:       "drop",
		},
	}
	out := relabelProcessTargets(targets, cfg)
	require.Len(t, out, 1)
	require.Equal(t, "keep-me", out[0]["service_name"])
}

func TestRelabelProcessTargets_ReplaceAction(t *testing.T) {
	targets := []sd.DiscoveryTarget{
		{"__process_pid__": "1", "service_name": "old-name"},
	}
	cfg := []*RelabelConfig{
		{
			SourceLabels: []string{"service_name"},
			Regex:        "(.*)",
			TargetLabel:  "renamed",
			Replacement:  "${1}-suffix",
			Action:       "replace",
		},
	}
	out := relabelProcessTargets(targets, cfg)
	require.Len(t, out, 1)
	require.Equal(t, "old-name-suffix", out[0]["renamed"])
}

func TestRelabelProcessTargets_EmptyInput(t *testing.T) {
	out := relabelProcessTargets(nil, nil)
	require.Nil(t, out)
}

func TestGetConfig_DefaultsWhenNoFileGiven(t *testing.T) {
	// getConfig relies on the package-level `configFile` flag which defaults
	// to "" and flag.Parse() being safe to call multiple times across the
	// package's test binary. We only assert the default-path behavior here
	// (no file => defaultConfig is returned), without touching flag state
	// that other tests might depend on.
	if *configFile != "" {
		t.Skip("configFile flag was set by another test/flag; skipping default-path check")
	}
	cfg := getConfig()
	require.NotNil(t, cfg)
	require.True(t, cfg.SessionOptions.CollectUser)
	require.True(t, cfg.SessionOptions.CollectKernel)
	require.Equal(t, 97, cfg.SessionOptions.SampleRate)
}

func TestRelabelConfig_SourceLabelsJoin(t *testing.T) {
	// sanity check that our understanding of strings.Join lines up with what
	// prometheus relabel expects for multiple source labels (not directly
	// exercised by relabelProcessTargets, but documents the assumption).
	require.Equal(t, "a;b", strings.Join([]string{"a", "b"}, ";"))
}
