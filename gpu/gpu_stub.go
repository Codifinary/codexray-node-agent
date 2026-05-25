//go:build !gpu
// +build !gpu

// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package gpu

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

type Collector struct {
	ProcessUsageSampleCh chan ProcessUsageSample
}

type Device struct{}

type ProcessUsageSample struct {
	UUID          string
	Pid           uint32
	Timestamp     time.Time
	GPUPercent    uint32
	MemoryPercent uint32
}

func NewCollector() (*Collector, error) {
	return &Collector{
		ProcessUsageSampleCh: make(chan ProcessUsageSample, 100),
	}, nil
}

func (c *Collector) Describe(ch chan<- *prometheus.Desc) {
	// No-op: GPU support disabled
}

func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	// No-op: GPU support disabled
}

func (c *Collector) Close() {
	// No-op: GPU support disabled
}
