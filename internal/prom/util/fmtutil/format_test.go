// Copyright Codexray
// SPDX-License-Identifier: Apache-2.0

package fmtutil

import (
	"strings"
	"testing"

	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/prometheus/prometheus/prompb"
)

func strp(s string) *string { return &s }

func findSeries(t *testing.T, wr *prompb.WriteRequest, name string) []prompb.TimeSeries {
	t.Helper()
	var out []prompb.TimeSeries
	for _, ts := range wr.Timeseries {
		for _, l := range ts.Labels {
			if l.Name == "__name__" && l.Value == name {
				out = append(out, ts)
			}
		}
	}
	return out
}

func labelValue(ts prompb.TimeSeries, name string) (string, bool) {
	for _, l := range ts.Labels {
		if l.Name == name {
			return l.Value, true
		}
	}
	return "", false
}

const exposition = `
# HELP http_requests_total Total requests
# TYPE http_requests_total counter
http_requests_total{method="get",job="foo"} 10 1000

# HELP temp_gauge a gauge
# TYPE temp_gauge gauge
temp_gauge 23.5

# HELP req_duration a summary
# TYPE req_duration summary
req_duration{quantile="0.5"} 1
req_duration{quantile="0.9"} 2
req_duration_sum 10
req_duration_count 5

# HELP req_latency a histogram
# TYPE req_latency histogram
req_latency_bucket{le="0.1"} 1
req_latency_bucket{le="0.5"} 3
req_latency_bucket{le="+Inf"} 5
req_latency_sum 12
req_latency_count 5

# HELP untyped_metric an untyped metric
# TYPE untyped_metric untyped
untyped_metric 7
`

func TestMetricTextToWriteRequest(t *testing.T) {
	wr, err := MetricTextToWriteRequest(strings.NewReader(exposition), map[string]string{"instance": "host1"})
	require.NoError(t, err)
	require.NotNil(t, wr)

	// Metadata should be present, one per metric family, sorted by name.
	require.Len(t, wr.Metadata, 5)
	var metaNames []string
	for _, md := range wr.Metadata {
		metaNames = append(metaNames, md.MetricFamilyName)
	}
	assert.Equal(t, []string{
		"http_requests_total", "req_duration", "req_latency", "temp_gauge", "untyped_metric",
	}, metaNames)

	// Counter series.
	counterSeries := findSeries(t, wr, "http_requests_total")
	require.Len(t, counterSeries, 1)
	require.Len(t, counterSeries[0].Samples, 1)
	assert.Equal(t, 10.0, counterSeries[0].Samples[0].Value)
	assert.Equal(t, int64(1000), counterSeries[0].Samples[0].Timestamp)
	// extra label applied
	v, ok := labelValue(counterSeries[0], "instance")
	assert.True(t, ok)
	assert.Equal(t, "host1", v)
	// "job" label on the metric itself gets renamed via ExportedLabelPrefix.
	_, hasPlainJob := labelValue(counterSeries[0], "job")
	assert.False(t, hasPlainJob)
	exportedJob, hasExportedJob := labelValue(counterSeries[0], "exported_job")
	assert.True(t, hasExportedJob)
	assert.Equal(t, "foo", exportedJob)

	// Gauge series.
	gaugeSeries := findSeries(t, wr, "temp_gauge")
	require.Len(t, gaugeSeries, 1)
	assert.Equal(t, 23.5, gaugeSeries[0].Samples[0].Value)

	// Summary series: 2 quantiles + sum + count = 4 series.
	quantileSeries := findSeries(t, wr, "req_duration")
	assert.Len(t, quantileSeries, 2)
	sumSeries := findSeries(t, wr, "req_duration_sum")
	require.Len(t, sumSeries, 1)
	assert.Equal(t, 10.0, sumSeries[0].Samples[0].Value)
	countSeries := findSeries(t, wr, "req_duration_count")
	require.Len(t, countSeries, 1)
	assert.Equal(t, 5.0, countSeries[0].Samples[0].Value)

	// Histogram series: 3 buckets + sum + count.
	bucketSeries := findSeries(t, wr, "req_latency_bucket")
	assert.Len(t, bucketSeries, 3)
	histSumSeries := findSeries(t, wr, "req_latency_sum")
	require.Len(t, histSumSeries, 1)
	assert.Equal(t, 12.0, histSumSeries[0].Samples[0].Value)
	histCountSeries := findSeries(t, wr, "req_latency_count")
	require.Len(t, histCountSeries, 1)
	assert.Equal(t, 5.0, histCountSeries[0].Samples[0].Value)

	// Untyped series.
	untypedSeries := findSeries(t, wr, "untyped_metric")
	require.Len(t, untypedSeries, 1)
	assert.Equal(t, 7.0, untypedSeries[0].Samples[0].Value)
}

func TestMetricTextToWriteRequestParseError(t *testing.T) {
	_, err := MetricTextToWriteRequest(strings.NewReader("not a valid exposition format {{{"), nil)
	assert.Error(t, err)
}

func TestMetricFamiliesToWriteRequestUnsupportedType(t *testing.T) {
	mf := map[string]*dto.MetricFamily{
		"weird_metric": {
			Name: strp("weird_metric"),
			Type: dto.MetricType_GAUGE.Enum(),
			Metric: []*dto.Metric{
				{
					// No Gauge/Counter/Summary/Histogram/Untyped set: hits
					// the "unsupported metric type" default branch.
					Label: []*dto.LabelPair{{Name: strp("a"), Value: strp("1")}},
				},
			},
		},
	}
	wr, err := MetricFamiliesToWriteRequest(mf, nil)
	assert.Error(t, err)
	assert.EqualError(t, err, "unsupported metric type")
	// Metadata for the family is still appended before the per-metric error.
	require.Len(t, wr.Metadata, 1)
}

func TestMetricFamiliesToWriteRequestNoExtraLabels(t *testing.T) {
	mf := map[string]*dto.MetricFamily{
		"simple_gauge": {
			Name: strp("simple_gauge"),
			Type: dto.MetricType_GAUGE.Enum(),
			Metric: []*dto.Metric{
				{
					Gauge: &dto.Gauge{Value: floatp(1.5)},
				},
			},
		},
	}
	wr, err := MetricFamiliesToWriteRequest(mf, nil)
	require.NoError(t, err)
	series := findSeries(t, wr, "simple_gauge")
	require.Len(t, series, 1)
	assert.Equal(t, 1.5, series[0].Samples[0].Value)
}

func TestMetricFamiliesToWriteRequestTimestampFallback(t *testing.T) {
	// When TimestampMs is unset (0), makeTimeseries falls back to
	// time.Now(); just assert it produces a plausible non-zero timestamp.
	mf := map[string]*dto.MetricFamily{
		"no_ts": {
			Name: strp("no_ts"),
			Type: dto.MetricType_COUNTER.Enum(),
			Metric: []*dto.Metric{
				{Counter: &dto.Counter{Value: floatp(1)}},
			},
		},
	}
	wr, err := MetricFamiliesToWriteRequest(mf, nil)
	require.NoError(t, err)
	series := findSeries(t, wr, "no_ts")
	require.Len(t, series, 1)
	assert.Positive(t, series[0].Samples[0].Timestamp)
}

func floatp(f float64) *float64 { return &f }

func TestMetricMetadataTypeValueTable(t *testing.T) {
	expected := map[string]int32{
		"UNKNOWN":        0,
		"COUNTER":        1,
		"GAUGE":          2,
		"HISTOGRAM":      3,
		"GAUGEHISTOGRAM": 4,
		"SUMMARY":        5,
		"INFO":           6,
		"STATESET":       7,
	}
	assert.Equal(t, expected, MetricMetadataTypeValue)
}
