// Copyright Codexray
// SPDX-License-Identifier: Apache-2.0

package common

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"inet.af/netaddr"
)

func TestParseLatencyBuckets(t *testing.T) {
	b, err := ParseLatencyBuckets("")
	require.NoError(t, err)
	assert.Equal(t, prometheus.DefBuckets, b)

	b, err = ParseLatencyBuckets(" 0.01, 0.05,0.1,0.5,1,5 ")
	require.NoError(t, err)
	assert.Equal(t, []float64{0.01, 0.05, 0.1, 0.5, 1, 5}, b)

	for _, bad := range []string{"0.1,0.05", "0.1,0.1", "0,1", "-1,1", "a,1", ",", "1,+Inf", "NaN"} {
		_, err := ParseLatencyBuckets(bad)
		assert.Error(t, err, bad)
	}
}

func TestDestinationKeyForMetrics(t *testing.T) {
	svc := netaddr.MustParseIPPort("10.43.0.10:8080")
	podA := netaddr.MustParseIPPort("10.42.1.5:8080")
	podB := netaddr.MustParseIPPort("10.42.2.7:8080")
	a := NewDestinationKey(svc, podA, nil)
	b := NewDestinationKey(svc, podB, nil)

	prev := DropActualDestination
	defer func() { DropActualDestination = prev }()

	DropActualDestination = false
	assert.NotEqual(t, a.ForMetrics(), b.ForMetrics())
	assert.Equal(t, "10.42.1.5:8080", a.ForMetrics().ActualDestinationLabelValue())

	DropActualDestination = true
	assert.Equal(t, a.ForMetrics(), b.ForMetrics())
	assert.Equal(t, "10.43.0.10:8080", a.ForMetrics().DestinationLabelValue())
	assert.Equal(t, "", a.ForMetrics().ActualDestinationLabelValue())
	// The original key, used for traces and the pinger, is unchanged.
	assert.Equal(t, "10.42.1.5:8080", a.ActualDestinationLabelValue())
}

func TestEphemeralRangeWarning(t *testing.T) {
	assert.Equal(t, "", EphemeralRangeWarning(32768, 60999))
	assert.Equal(t, "", EphemeralRangeWarning(10000, 65535))
	w := EphemeralRangeWarning(1, 65535)
	assert.Contains(t, w, "1-9999")
	assert.Contains(t, w, "will NOT be tracked")
}
