// Copyright Codexray
// SPDX-License-Identifier: AGPL-3.0

package dogstatsd

import (
	"fmt"
	"net/netip"
	"strings"
	"time"
)

type MetricType string

const (
	MetricTypeCounter   MetricType = "c"
	MetricTypeGauge     MetricType = "g"
	MetricTypeTimer     MetricType = "ms"
	MetricTypeHistogram MetricType = "h"
	MetricTypeSet       MetricType = "s"
)

type Metric struct {
	Name       string
	Type       MetricType
	Value      float64
	SetValue   string
	Relative   bool
	SampleRate float64
	Tags       map[string]string
	ReceivedAt time.Time
}

type Config struct {
	ListenAddr               string
	MaxPacketBytes           int
	PacketQueueSize          int
	PacketQueueMaxBytes      int64
	ParseWorkers             int
	BufferMaxEvents          int
	BatchMaxSeries           int
	MaxMetricNameLength      int
	MaxTagsPerMetric         int
	MaxTagKeyLength          int
	MaxTagValueLength        int
	ActiveSeriesPerMetricCap int
	ActiveSeriesGlobalCap    int
	ActiveSeriesTTL          time.Duration
	TagKeyBlocklist          string
	AllowedSourceCIDRs       string
	MaxBytesPerSecond        int
	MaxEventsPerSecond       int
	SaturationThreshold      float64
	SaturationDuration       time.Duration
	ShutdownDrainTimeout     time.Duration
}

func (c Config) Validate() error {
	checks := []struct {
		name  string
		value int
	}{
		{"max-packet-bytes", c.MaxPacketBytes},
		{"packet-queue-size", c.PacketQueueSize},
		{"parse-workers", c.ParseWorkers},
		{"buffer-max-events", c.BufferMaxEvents},
		{"batch-max-series", c.BatchMaxSeries},
		{"max-metric-name-length", c.MaxMetricNameLength},
		{"max-tags-per-metric", c.MaxTagsPerMetric},
		{"max-tag-key-length", c.MaxTagKeyLength},
		{"max-tag-value-length", c.MaxTagValueLength},
		{"active-series-per-metric-cap", c.ActiveSeriesPerMetricCap},
		{"active-series-global-cap", c.ActiveSeriesGlobalCap},
		{"max-bytes-per-second", c.MaxBytesPerSecond},
		{"max-events-per-second", c.MaxEventsPerSecond},
	}
	if c.PacketQueueMaxBytes <= 0 {
		return fmt.Errorf("dogstatsd packet-queue-max-bytes must be positive")
	}
	if c.ListenAddr == "" {
		return fmt.Errorf("dogstatsd listen address must not be empty")
	}
	for _, check := range checks {
		if check.value <= 0 {
			return fmt.Errorf("dogstatsd %s must be positive", check.name)
		}
	}
	if c.ActiveSeriesTTL <= 0 {
		return fmt.Errorf("dogstatsd active-series-ttl must be positive")
	}
	if c.SaturationThreshold <= 0 || c.SaturationThreshold > 1 {
		return fmt.Errorf("dogstatsd saturation-threshold must be greater than 0 and no greater than 1")
	}
	if c.SaturationDuration <= 0 {
		return fmt.Errorf("dogstatsd saturation-duration must be positive")
	}
	if c.ShutdownDrainTimeout <= 0 {
		return fmt.Errorf("dogstatsd shutdown-drain-timeout must be positive")
	}
	return nil
}

func parseAllowedSourceCIDRs(raw string) ([]netip.Prefix, error) {
	parts := strings.Split(raw, ",")
	prefixes := make([]netip.Prefix, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(part)
		if err != nil {
			return nil, fmt.Errorf("invalid DogStatsD source CIDR %q: %w", part, err)
		}
		prefixes = append(prefixes, prefix.Masked())
	}
	if len(prefixes) == 0 {
		return nil, fmt.Errorf("dogstatsd allowed source CIDRs must not be empty")
	}
	return prefixes, nil
}
