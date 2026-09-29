// Copyright Codexray
// SPDX-License-Identifier: Apache-2.0

// This package is a protoc-generated copy of client_model's MetricFamily
// types. It is not imported anywhere else in this repo (the rest of the
// codebase uses github.com/prometheus/client_model/go directly), but per
// the coverage goal for internal/prom we add a minimal round-trip test to
// exercise the generated Marshal/Unmarshal/Size code.
package io_prometheus_client

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMetricFamilyRoundTrip(t *testing.T) {
	mf := &MetricFamily{
		Name: "http_requests_total",
		Help: "Total HTTP requests",
		Type: MetricType_COUNTER,
		Unit: "requests",
		Metric: []Metric{
			{
				Label: []LabelPair{
					{Name: "job", Value: "agent"},
				},
				Counter:     &Counter{Value: 42},
				TimestampMs: 1000,
			},
			{
				Label: []LabelPair{{Name: "job", Value: "agent"}},
				Gauge: &Gauge{Value: 3.5},
			},
			{
				Summary: &Summary{
					SampleCount: 5,
					SampleSum:   10.5,
					Quantile: []Quantile{
						{Quantile: 0.5, Value: 1.5},
					},
				},
			},
			{
				Untyped: &Untyped{Value: 1.1},
			},
			{
				Histogram: &Histogram{
					SampleCount: 3,
					SampleSum:   6.6,
					Bucket: []Bucket{
						{CumulativeCount: 1, UpperBound: 1.0},
						{CumulativeCount: 3, UpperBound: 2.0},
					},
					PositiveSpan:  []BucketSpan{{Offset: 0, Length: 2}},
					PositiveDelta: []int64{1, 0},
				},
			},
		},
	}

	data, err := mf.Marshal()
	require.NoError(t, err)
	assert.NotEmpty(t, data)
	assert.Equal(t, mf.Size(), len(data))

	got := &MetricFamily{}
	require.NoError(t, got.Unmarshal(data))
	assert.Equal(t, mf.Name, got.Name)
	assert.Equal(t, mf.Help, got.Help)
	assert.Equal(t, mf.Type, got.Type)
	assert.Equal(t, mf.Unit, got.Unit)
	require.Len(t, got.Metric, len(mf.Metric))
	assert.Equal(t, mf.Metric[0].GetCounter().GetValue(), got.Metric[0].GetCounter().GetValue())
	assert.Equal(t, mf.Metric[1].GetGauge().GetValue(), got.Metric[1].GetGauge().GetValue())
	assert.Equal(t, mf.Metric[2].GetSummary().GetSampleCount(), got.Metric[2].GetSummary().GetSampleCount())
	assert.Equal(t, mf.Metric[3].GetUntyped().GetValue(), got.Metric[3].GetUntyped().GetValue())
	assert.Equal(t, mf.Metric[4].GetHistogram().GetSampleSum(), got.Metric[4].GetHistogram().GetSampleSum())
}

func TestMetricFamilyEmptyRoundTrip(t *testing.T) {
	mf := &MetricFamily{}
	data, err := mf.Marshal()
	require.NoError(t, err)
	got := &MetricFamily{}
	require.NoError(t, got.Unmarshal(data))
	assert.Equal(t, mf, got)
}

func TestMetricTypeString(t *testing.T) {
	assert.Equal(t, "COUNTER", MetricType_COUNTER.String())
	assert.Equal(t, "GAUGE", MetricType_GAUGE.String())
}
