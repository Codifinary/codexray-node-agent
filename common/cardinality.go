// Copyright Codexray
// SPDX-License-Identifier: Apache-2.0

package common

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
)

// DNSOverflowDomain is the domain label used once a container exceeds the DNS domain cap.
const DNSOverflowDomain = "overflow"

var (
	// DropActualDestination aggregates network and L7 metrics by destination only.
	DropActualDestination bool
	// L7LatencyBuckets are the histogram bounds for L7 and DNS latency metrics.
	L7LatencyBuckets = prometheus.DefBuckets
	// DNSMaxDomainsPerContainer caps distinct DNS domains per container (0 = unlimited).
	DNSMaxDomainsPerContainer = 500
)

// ParseLatencyBuckets parses a comma-separated list of positive, strictly increasing bounds.
// An empty string returns prometheus.DefBuckets.
func ParseLatencyBuckets(s string) ([]float64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return prometheus.DefBuckets, nil
	}
	var res []float64
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		v, err := strconv.ParseFloat(part, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid bucket %q: %w", part, err)
		}
		if v <= 0 || math.IsInf(v, 0) || math.IsNaN(v) {
			return nil, fmt.Errorf("invalid bucket %q: must be a positive finite number", part)
		}
		res = append(res, v)
	}
	if len(res) == 0 {
		return nil, fmt.Errorf("no buckets in %q", s)
	}
	if !sort.Float64sAreSorted(res) {
		return nil, fmt.Errorf("buckets must be in increasing order: %q", s)
	}
	for i := 1; i < len(res); i++ {
		if res[i] == res[i-1] {
			return nil, fmt.Errorf("duplicate bucket %v in %q", res[i], s)
		}
	}
	return res, nil
}

// ForMetrics returns the key used to aggregate metrics. With DropActualDestination
// it removes the actual destination so that all backends of a Service share one series.
func (dk DestinationKey) ForMetrics() DestinationKey {
	if DropActualDestination {
		return DestinationKey{destination: dk.destination}
	}
	return dk
}
