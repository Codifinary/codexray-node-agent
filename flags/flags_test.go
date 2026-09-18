// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package flags

import (
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
