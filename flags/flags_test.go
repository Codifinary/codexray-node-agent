// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package flags

import (
	"bytes"
	"encoding/json"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/alecthomas/kingpin.v2"
)

func TestGetString(t *testing.T) {
	assert.Equal(t, "", GetString(nil))
	empty := ""
	assert.Equal(t, "", GetString(&empty))
	v := "us-east-1"
	assert.Equal(t, "us-east-1", GetString(&v))
}

// TestFlagDefaultsAndEnv parses the real flag set once (kingpin accumulates
// repeated Strings() values, so this must be the only Parse in the package).
var flagsTestParsed bool

func TestFlagDefaultsAndEnv(t *testing.T) {
	if flagsTestParsed {
		t.Skip("kingpin.CommandLine can only be parsed once per process (-count>1)")
	}
	flagsTestParsed = true

	// init() returns early for *.test binaries, so kingpin never parsed and no
	// endpoint was derived: nothing may be set before an explicit Parse.
	assert.Nil(t, *CollectorEndpoint)
	assert.Nil(t, *MetricsEndpoint)
	assert.Equal(t, "", *ListenAddress)

	t.Setenv("API_KEY", "secret")
	t.Setenv("REGION", "eu-west-1")
	t.Setenv("CONTAINER_DENYLIST", "pause\nsidecar-.*")
	_, err := kingpin.CommandLine.Parse([]string{
		"--collector-endpoint=https://collector.example.com/base",
		"--scrape-interval=30s",
	})
	require.NoError(t, err)

	// defaults documented in README / deployments
	assert.Equal(t, "0.0.0.0:80", *ListenAddress)
	assert.Equal(t, "/sys/fs/cgroup", *CgroupRoot)
	assert.Equal(t, "32768-60999", *EphemeralPortRange)
	assert.Equal(t, []string{"0.0.0.0/0"}, *ExternalNetworksWhitelist)
	assert.Equal(t, 30*time.Second, *MinContainerAge)
	assert.Equal(t, 30*time.Second, *InstrumentationDelay)
	assert.Equal(t, 10.0, *LogPerSecond)
	assert.Equal(t, 100, *LogBurst)
	assert.Equal(t, 4096, *MaxLabelLength)
	assert.Equal(t, 1.0, *TracesSampling)
	assert.Equal(t, "/tmp/Codexray-node-agent", *WalDir)
	assert.Equal(t, int64(500*1024*1024), int64(*MaxSpoolSize))
	assert.False(t, *InsecureSkipVerify)
	assert.False(t, *DisableLogParsing)
	assert.False(t, *agentVersion)

	// explicit args
	assert.Equal(t, 30*time.Second, *ScrapeInterval)
	require.NotNil(t, *CollectorEndpoint)
	assert.Equal(t, "https://collector.example.com/base", (*CollectorEndpoint).String())

	// env vars
	assert.Equal(t, "secret", *ApiKey)
	assert.Equal(t, "eu-west-1", *Region)
	assert.Equal(t, []string{"pause", "sidecar-.*"}, *ContainerDenylist)

	// per-signal endpoints are derived only in init(), not by Parse
	assert.Nil(t, *MetricsEndpoint)
}

// init() skips kingpin parsing when os.Args[0] ends in ".test", so the real
// parse + endpoint derivation is exercised by re-executing this test binary
// under a name without that suffix. With FLAGS_HELPER=1 the child has already
// run init() (parse, --version handling, derivation) by the time TestMain
// runs; it dumps the resolved values and exits before testing parses its own
// flags.
const flagsHelperEnv = "FLAGS_HELPER"

type flagsHelperResult struct {
	ListenAddress     string
	CollectorEndpoint string
	MetricsEndpoint   string
	TracesEndpoint    string
	LogsEndpoint      string
	ProfilesEndpoint  string
	ApiKey            string
	ScrapeInterval    string
	WalDir            string
	Denylist          []string
}

func flagsHelperURL(u *url.URL) string {
	if u == nil {
		return ""
	}
	return u.String()
}

func TestMain(m *testing.M) {
	if os.Getenv(flagsHelperEnv) == "1" {
		_ = json.NewEncoder(os.Stdout).Encode(flagsHelperResult{
			ListenAddress:     *ListenAddress,
			CollectorEndpoint: flagsHelperURL(*CollectorEndpoint),
			MetricsEndpoint:   flagsHelperURL(*MetricsEndpoint),
			TracesEndpoint:    flagsHelperURL(*TracesEndpoint),
			LogsEndpoint:      flagsHelperURL(*LogsEndpoint),
			ProfilesEndpoint:  flagsHelperURL(*ProfilesEndpoint),
			ApiKey:            *ApiKey,
			ScrapeInterval:    ScrapeInterval.String(),
			WalDir:            *WalDir,
			Denylist:          *ContainerDenylist,
		})
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// flagsHelperEnvVars are the Envar() names of every flag; they are stripped
// from the child's environment so the host/CI env cannot leak into the parse.
var flagsHelperEnvVars = []string{
	"LISTEN", "CGROUPFS_ROOT", "DISABLE_LOG_PARSING", "DISABLE_PINGER", "DISABLE_L7_TRACING",
	"CONTAINER_ALLOWLIST", "CONTAINER_DENYLIST", "EXCLUDE_HTTP_REQUESTS_BY_PATH",
	"TRACK_PUBLIC_NETWORK", "EPHEMERAL_PORT_RANGE", "MIN_CONTAINER_AGE", "INSTRUMENTATION_DELAY",
	"PROVIDER", "REGION", "AVAILABILITY_ZONE", "INSTANCE_TYPE", "INSTANCE_LIFE_CYCLE",
	"LOG_PER_SECOND", "LOG_BURST", "MAX_LABEL_LENGTH", "COLLECTOR_ENDPOINT", "API_KEY",
	"METRICS_ENDPOINT", "TRACES_ENDPOINT", "TRACES_SAMPLING", "LOGS_ENDPOINT", "PROFILES_ENDPOINT",
	"INSECURE_SKIP_VERIFY", "SCRAPE_INTERVAL", "WAL_DIR", "MAX_SPOOL_SIZE", "GOCOVERDIR", "GORACE",
}

// flagsHelperBinary copies the test binary to a path without the ".test" suffix
// (once per test) so init() takes the production path.
func flagsHelperBinary(t *testing.T) string {
	t.Helper()
	src, err := os.Executable()
	require.NoError(t, err)
	in, err := os.Open(src)
	require.NoError(t, err)
	defer in.Close()
	dst := filepath.Join(t.TempDir(), "flagshelper")
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	require.NoError(t, err)
	_, err = io.Copy(out, in)
	require.NoError(t, err)
	require.NoError(t, out.Close())
	return dst
}

// flagsHelperRun execs the helper with args and extra env. If this test binary
// is coverage-instrumented, the child writes its counters to GOCOVERDIR
// (FLAGS_HELPER_COVERDIR if set, else a temp dir); they are NOT merged into
// the parent's -coverprofile automatically (see `go tool covdata`).
func flagsHelperRun(t *testing.T, bin string, env []string, args ...string) (stdout string, exitCode int) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if slices.Contains(flagsHelperEnvVars, name) {
			continue
		}
		cmd.Env = append(cmd.Env, kv)
	}
	// Under -race the child would otherwise sleep 1s at exit (atexit_sleep_ms).
	cmd.Env = append(cmd.Env, flagsHelperEnv+"=1", "GORACE=atexit_sleep_ms=0 "+os.Getenv("GORACE"))
	if testing.CoverMode() != "" {
		dir := os.Getenv("FLAGS_HELPER_COVERDIR")
		if dir == "" {
			dir = t.TempDir()
		}
		cmd.Env = append(cmd.Env, "GOCOVERDIR="+dir)
	}
	cmd.Env = append(cmd.Env, env...)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &outBuf, &errBuf
	err := cmd.Run()
	if err != nil {
		var ee *exec.ExitError
		require.ErrorAs(t, err, &ee, "helper did not run: %v (stderr: %s)", err, errBuf.String())
		return outBuf.String(), ee.ExitCode()
	}
	return outBuf.String(), 0
}

func flagsHelperParse(t *testing.T, bin string, env []string, args ...string) flagsHelperResult {
	t.Helper()
	out, code := flagsHelperRun(t, bin, env, args...)
	require.Equal(t, 0, code, "helper exited non-zero, stdout: %s", out)
	var res flagsHelperResult
	require.NoError(t, json.Unmarshal([]byte(out), &res), "stdout: %q", out)
	return res
}

func TestInitParseAndDerive(t *testing.T) {
	if testing.Short() {
		t.Skip("re-execs the test binary")
	}
	bin := flagsHelperBinary(t)

	t.Run("defaults", func(t *testing.T) {
		r := flagsHelperParse(t, bin, nil)
		assert.Equal(t, "0.0.0.0:80", r.ListenAddress)
		assert.Empty(t, r.CollectorEndpoint)
		assert.Empty(t, r.MetricsEndpoint)
		assert.Empty(t, r.TracesEndpoint)
		assert.Empty(t, r.LogsEndpoint)
		assert.Empty(t, r.ProfilesEndpoint)
		assert.Equal(t, "15s", r.ScrapeInterval)
		assert.Equal(t, "/tmp/Codexray-node-agent", r.WalDir)
	})

	t.Run("collector endpoint derives every signal and forces loopback listen", func(t *testing.T) {
		r := flagsHelperParse(t, bin, nil,
			"--collector-endpoint=http://h:8080/ingest", "--listen=0.0.0.0:9999")
		assert.Equal(t, "http://h:8080/ingest", r.CollectorEndpoint)
		assert.Equal(t, "http://h:8080/ingest/v1/metrics", r.MetricsEndpoint)
		assert.Equal(t, "http://h:8080/ingest/v1/traces", r.TracesEndpoint)
		assert.Equal(t, "http://h:8080/ingest/v1/logs", r.LogsEndpoint)
		assert.Equal(t, "http://h:8080/ingest/v1/profiles", r.ProfilesEndpoint)
		// remote-write mode: the agent must not expose /metrics publicly
		assert.Equal(t, "127.0.0.1:10300", r.ListenAddress)
	})

	t.Run("trailing slash on collector endpoint does not double it", func(t *testing.T) {
		r := flagsHelperParse(t, bin, nil, "--collector-endpoint=http://h:8080/ingest/")
		assert.Equal(t, "http://h:8080/ingest/v1/metrics", r.MetricsEndpoint)
		assert.Equal(t, "http://h:8080/ingest/v1/profiles", r.ProfilesEndpoint)
	})

	t.Run("per-signal flag overrides the collector-derived one", func(t *testing.T) {
		r := flagsHelperParse(t, bin, nil,
			"--collector-endpoint=http://h:8080/ingest",
			"--traces-endpoint=https://traces.example.com/otlp",
			"--logs-endpoint=https://logs.example.com/l")
		assert.Equal(t, "https://traces.example.com/otlp", r.TracesEndpoint)
		assert.Equal(t, "https://logs.example.com/l", r.LogsEndpoint)
		assert.Equal(t, "http://h:8080/ingest/v1/metrics", r.MetricsEndpoint)
		assert.Equal(t, "http://h:8080/ingest/v1/profiles", r.ProfilesEndpoint)
		assert.Equal(t, "127.0.0.1:10300", r.ListenAddress)
	})

	t.Run("metrics endpoint alone forces loopback listen", func(t *testing.T) {
		r := flagsHelperParse(t, bin, nil, "--metrics-endpoint=http://m:9090/api/v1/write")
		assert.Equal(t, "http://m:9090/api/v1/write", r.MetricsEndpoint)
		assert.Empty(t, r.TracesEndpoint)
		assert.Empty(t, r.CollectorEndpoint)
		assert.Equal(t, "127.0.0.1:10300", r.ListenAddress)
	})

	t.Run("traces endpoint alone keeps the public listen address", func(t *testing.T) {
		r := flagsHelperParse(t, bin, nil, "--traces-endpoint=http://t/v1/traces", "--listen=:8080")
		assert.Equal(t, ":8080", r.ListenAddress)
		assert.Empty(t, r.MetricsEndpoint)
	})

	t.Run("env var forms", func(t *testing.T) {
		r := flagsHelperParse(t, bin, []string{
			"COLLECTOR_ENDPOINT=http://c:1/base",
			"METRICS_ENDPOINT=http://m:2/write",
			"API_KEY=k",
			"SCRAPE_INTERVAL=30s",
			"CONTAINER_DENYLIST=pause\nsidecar-.*",
			"LISTEN=0.0.0.0:1234",
		})
		assert.Equal(t, "http://c:1/base", r.CollectorEndpoint)
		assert.Equal(t, "http://m:2/write", r.MetricsEndpoint, "explicit METRICS_ENDPOINT wins over derivation")
		assert.Equal(t, "http://c:1/base/v1/traces", r.TracesEndpoint)
		assert.Equal(t, "http://c:1/base/v1/logs", r.LogsEndpoint)
		assert.Equal(t, "http://c:1/base/v1/profiles", r.ProfilesEndpoint)
		assert.Equal(t, "k", r.ApiKey)
		assert.Equal(t, "30s", r.ScrapeInterval)
		assert.Equal(t, []string{"pause", "sidecar-.*"}, r.Denylist)
		assert.Equal(t, "127.0.0.1:10300", r.ListenAddress)
	})

	t.Run("flag beats env", func(t *testing.T) {
		r := flagsHelperParse(t, bin, []string{"COLLECTOR_ENDPOINT=http://env/"},
			"--collector-endpoint=http://flag/")
		assert.Equal(t, "http://flag/v1/metrics", r.MetricsEndpoint)
	})

	t.Run("--version prints and exits 0", func(t *testing.T) {
		out, code := flagsHelperRun(t, bin, nil, "--version")
		assert.Equal(t, 0, code)
		assert.Equal(t, "Version: "+Version+"\n", out, "must exit before the helper dump")
	})

	t.Run("invalid flag exits non-zero", func(t *testing.T) {
		for _, args := range [][]string{
			{"--no-such-flag"},
			{"--scrape-interval=soon"},
			{"--collector-endpoint=://bad"},
			{"--max-spool-size=lots"},
		} {
			out, code := flagsHelperRun(t, bin, nil, args...)
			assert.NotEqual(t, 0, code, "args %v", args)
			assert.Empty(t, out, "args %v: must not reach the helper dump", args)
		}
	})
}
