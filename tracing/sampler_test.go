// Copyright Codexray
// SPDX-License-Identifier: Apache-2.0

package tracing

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func fixed(v float64) func() float64 { return func() float64 { return v } }

func TestSamplerFixedRate(t *testing.T) {
	s := sampler{rate: 0.1, slow: 500 * time.Millisecond, rnd: fixed(0.05)}
	keep, reason := s.decide(10*time.Millisecond, false)
	require.True(t, keep)
	require.Equal(t, "rate", reason)

	s.rnd = fixed(0.5)
	keep, reason = s.decide(10*time.Millisecond, false)
	require.False(t, keep)
	require.Equal(t, "sampled_out", reason)

	// Without adaptive sampling, errors and slow spans get no special treatment.
	keep, _ = s.decide(10*time.Millisecond, true)
	require.False(t, keep)
	keep, _ = s.decide(5*time.Second, false)
	require.False(t, keep)
}

func TestSamplerAdaptive(t *testing.T) {
	s := sampler{rate: 0.1, adaptive: true, slow: 500 * time.Millisecond, rnd: fixed(0.99)}

	keep, reason := s.decide(10*time.Millisecond, true)
	require.True(t, keep)
	require.Equal(t, "error", reason, "every error span is kept")

	keep, reason = s.decide(501*time.Millisecond, false)
	require.True(t, keep)
	require.Equal(t, "slow", reason, "every span slower than the threshold is kept")

	keep, reason = s.decide(500*time.Millisecond, false)
	require.False(t, keep, "exactly the threshold is not 'more than' it")
	require.Equal(t, "sampled_out", reason)

	keep, _ = s.decide(10*time.Millisecond, false)
	require.False(t, keep, "fast successful spans fall back to the rate")
	s.rnd = fixed(0.01)
	keep, reason = s.decide(10*time.Millisecond, false)
	require.True(t, keep)
	require.Equal(t, "rate", reason)
}

func TestSamplerAdaptiveRateZero(t *testing.T) {
	s := sampler{rate: 0, adaptive: true, slow: 500 * time.Millisecond, rnd: fixed(0)}
	keep, _ := s.decide(10*time.Millisecond, false)
	require.False(t, keep, "rate 0 drops every fast successful span")
	keep, _ = s.decide(time.Second, false)
	require.True(t, keep)
	keep, _ = s.decide(time.Millisecond, true)
	require.True(t, keep)
}

func TestSamplerAdaptiveProportion(t *testing.T) {
	s := newSampler(0.2, true, 500*time.Millisecond)
	kept := 0
	const n = 20000
	for i := 0; i < n; i++ {
		if ok, _ := s.decide(5*time.Millisecond, false); ok {
			kept++
		}
	}
	require.InDelta(t, 0.2, float64(kept)/n, 0.02)
}

func TestNewSamplerValidation(t *testing.T) {
	s := newSampler(1.5, true, 0)
	require.Equal(t, 1.0, s.rate)
	require.Equal(t, 500*time.Millisecond, s.slow)
	require.True(t, s.adaptive)
}
