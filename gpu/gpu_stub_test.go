//go:build !gpu
// +build !gpu

// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package gpu

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStubNewCollector(t *testing.T) {
	c, err := NewCollector()
	require.NoError(t, err, "a build without GPU support must never fail agent startup")
	require.NotNil(t, c)
	// main.go hands this channel to containers.NewRegistry; it must exist and be
	// buffered the same as the NVML build.
	require.NotNil(t, c.ProcessUsageSampleCh)
	assert.Equal(t, 100, cap(c.ProcessUsageSampleCh))
}

func TestStubCollectorEmitsNothing(t *testing.T) {
	c, err := NewCollector()
	require.NoError(t, err)

	descs := make(chan *prometheus.Desc, 16)
	c.Describe(descs)
	close(descs)
	assert.Empty(t, descs)

	metrics := make(chan prometheus.Metric, 16)
	c.Collect(metrics)
	close(metrics)
	assert.Empty(t, metrics)

	// Registers cleanly (an unchecked collector) and scrapes to zero series.
	reg := prometheus.NewPedanticRegistry()
	require.NoError(t, reg.Register(c))
	mfs, err := reg.Gather()
	require.NoError(t, err)
	assert.Empty(t, mfs)

	assert.NotPanics(t, c.Close)
}
