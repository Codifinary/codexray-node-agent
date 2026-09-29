// Copyright Codexray
// SPDX-License-Identifier: Apache-2.0

// These tests exercise the protoc-generated Marshal/Unmarshal/Size code in
// remote.pb.go and types.pb.go for the message types actually constructed
// elsewhere in this repo (see prom/remote_writer.go and
// util/fmtutil/format.go): WriteRequest, TimeSeries, Sample, Histogram,
// Exemplar and MetricMetadata. Rather than hand-enumerating every generated
// getter, a round trip (construct -> Marshal -> Unmarshal -> compare) gives
// meaningful statement coverage over the generated (de)serialization code.
package prompb

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteRequestRoundTrip(t *testing.T) {
	wr := &WriteRequest{
		Timeseries: []TimeSeries{
			{
				Labels: []Label{
					{Name: "__name__", Value: "http_requests_total"},
					{Name: "job", Value: "node-agent"},
				},
				Samples: []Sample{
					{Value: 42.5, Timestamp: 1000},
					{Value: 43.5, Timestamp: 2000},
				},
				Exemplars: []Exemplar{
					{
						Labels:    []Label{{Name: "trace_id", Value: "abc123"}},
						Value:     1.23,
						Timestamp: 1500,
					},
				},
				Histograms: []Histogram{
					{
						Count:          &Histogram_CountInt{CountInt: 10},
						Sum:            123.45,
						Schema:         3,
						ZeroThreshold:  0.001,
						ZeroCount:      &Histogram_ZeroCountInt{ZeroCountInt: 1},
						NegativeSpans:  []BucketSpan{{Offset: 1, Length: 2}},
						NegativeDeltas: []int64{1, -1},
						PositiveSpans:  []BucketSpan{{Offset: 0, Length: 3}},
						PositiveDeltas: []int64{2, 0, -1},
						ResetHint:      Histogram_UNKNOWN,
						Timestamp:      3000,
					},
					{
						// float histogram variant
						Count:          &Histogram_CountFloat{CountFloat: 10.5},
						Sum:            99.9,
						ZeroCount:      &Histogram_ZeroCountFloat{ZeroCountFloat: 0.5},
						PositiveCounts: []float64{1.5, 2.5},
						NegativeCounts: []float64{0.5},
						Timestamp:      4000,
					},
				},
			},
		},
		Metadata: []MetricMetadata{
			{
				Type:             MetricMetadata_COUNTER,
				MetricFamilyName: "http_requests_total",
				Help:             "Total HTTP requests",
				Unit:             "requests",
			},
		},
	}

	data, err := wr.Marshal()
	require.NoError(t, err)
	assert.NotEmpty(t, data)
	assert.Equal(t, wr.Size(), len(data))

	got := &WriteRequest{}
	require.NoError(t, got.Unmarshal(data))
	assert.Equal(t, wr.Timeseries, got.Timeseries)
	assert.Equal(t, wr.Metadata, got.Metadata)

	// MarshalTo / MarshalToSizedBuffer paths.
	buf := make([]byte, wr.Size())
	n, err := wr.MarshalTo(buf)
	require.NoError(t, err)
	assert.Equal(t, len(data), n)

	buf2 := make([]byte, wr.Size())
	n2, err := wr.MarshalToSizedBuffer(buf2)
	require.NoError(t, err)
	assert.Equal(t, data, buf2[len(buf2)-n2:])
}

func TestWriteRequestEmptyRoundTrip(t *testing.T) {
	wr := &WriteRequest{}
	data, err := wr.Marshal()
	require.NoError(t, err)
	assert.Empty(t, data)

	got := &WriteRequest{}
	require.NoError(t, got.Unmarshal(data))
	assert.Equal(t, wr, got)
}

func TestSampleRoundTrip(t *testing.T) {
	s := Sample{Value: 3.14159, Timestamp: 1620000000000}
	data, err := s.Marshal()
	require.NoError(t, err)

	var got Sample
	require.NoError(t, got.Unmarshal(data))
	assert.Equal(t, s, got)
}

func TestExemplarRoundTrip(t *testing.T) {
	e := Exemplar{
		Labels:    []Label{{Name: "trace_id", Value: "xyz"}},
		Value:     9.99,
		Timestamp: 555,
	}
	data, err := e.Marshal()
	require.NoError(t, err)

	var got Exemplar
	require.NoError(t, got.Unmarshal(data))
	assert.Equal(t, e, got)
}

func TestTimeSeriesRoundTrip(t *testing.T) {
	ts := TimeSeries{
		Labels: []Label{{Name: "__name__", Value: "up"}},
		Samples: []Sample{
			{Value: 1, Timestamp: 100},
		},
	}
	data, err := ts.Marshal()
	require.NoError(t, err)

	var got TimeSeries
	require.NoError(t, got.Unmarshal(data))
	assert.Equal(t, ts, got)
}

func TestHistogramRoundTripBothVariants(t *testing.T) {
	intHist := Histogram{
		Count:     &Histogram_CountInt{CountInt: 100},
		ZeroCount: &Histogram_ZeroCountInt{ZeroCountInt: 3},
		Sum:       55.5,
		Timestamp: 42,
	}
	data, err := intHist.Marshal()
	require.NoError(t, err)
	var got Histogram
	require.NoError(t, got.Unmarshal(data))
	assert.Equal(t, intHist, got)
	assert.False(t, got.IsFloatHistogram())

	floatHist := Histogram{
		Count:     &Histogram_CountFloat{CountFloat: 100.5},
		ZeroCount: &Histogram_ZeroCountFloat{ZeroCountFloat: 3.5},
		Sum:       55.5,
		Timestamp: 42,
	}
	data2, err := floatHist.Marshal()
	require.NoError(t, err)
	var got2 Histogram
	require.NoError(t, got2.Unmarshal(data2))
	assert.Equal(t, floatHist, got2)
	assert.True(t, got2.IsFloatHistogram())
}

func TestUnmarshalInvalidData(t *testing.T) {
	var wr WriteRequest
	err := wr.Unmarshal([]byte{0xFF, 0xFF, 0xFF})
	assert.Error(t, err)

	var s Sample
	err = s.Unmarshal([]byte{0x09}) // truncated varint/field tag
	assert.Error(t, err)
}

func TestLabelRoundTrip(t *testing.T) {
	l := Label{Name: "foo", Value: "bar"}
	data, err := l.Marshal()
	require.NoError(t, err)

	var got Label
	require.NoError(t, got.Unmarshal(data))
	assert.Equal(t, l, got)
}

func TestMetricMetadataRoundTrip(t *testing.T) {
	md := MetricMetadata{
		Type:             MetricMetadata_HISTOGRAM,
		MetricFamilyName: "request_duration_seconds",
		Help:             "request duration",
		Unit:             "seconds",
	}
	data, err := md.Marshal()
	require.NoError(t, err)

	var got MetricMetadata
	require.NoError(t, got.Unmarshal(data))
	assert.Equal(t, md, got)
}
