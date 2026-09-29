package dogstatsd

import (
	"testing"
	"time"

	"github.com/prometheus/prometheus/prompb"
)

func testAggregator(t *testing.T) *Aggregator {
	t.Helper()
	agg, err := NewAggregator(AggregationConfig{
		TimerBucketsMS:        []float64{10, 50, 100},
		HistogramBuckets:      []float64{100, 500},
		MaxSetValuesPerSeries: 2,
		MaxBytes:              1 << 20,
		GaugeTTL:              time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	return agg
}

func TestAggregatorPreservesCommonMetricSemantics(t *testing.T) {
	agg := testAggregator(t)
	tags := map[string]string{"service": "checkout"}
	inputs := []Metric{
		{Name: "orders.created", Type: MetricTypeCounter, Value: 2, SampleRate: .5},
		{Name: "orders.created", Type: MetricTypeCounter, Value: 3, SampleRate: 1},
		{Name: "checkout.inflight", Type: MetricTypeGauge, Value: 2, SampleRate: 1},
		{Name: "checkout.inflight", Type: MetricTypeGauge, Value: 7, SampleRate: 1},
		{Name: "checkout.duration", Type: MetricTypeTimer, Value: 5, SampleRate: 1},
		{Name: "checkout.duration", Type: MetricTypeTimer, Value: 75, SampleRate: 1},
		{Name: "checkout.payload", Type: MetricTypeHistogram, Value: 250, SampleRate: 1},
		{Name: "checkout.users", Type: MetricTypeSet, SetValue: "a", SampleRate: 1},
		{Name: "checkout.users", Type: MetricTypeSet, SetValue: "a", SampleRate: 1},
		{Name: "checkout.users", Type: MetricTypeSet, SetValue: "b", SampleRate: 1},
	}
	for _, input := range inputs {
		if accepted, reason := agg.Add(input, tags); !accepted {
			t.Fatalf("Add(%v) rejected: %s", input.Type, reason)
		}
	}

	series := agg.Flush(time.UnixMilli(1234))
	assertSeriesValue(t, series, "orders_created_counter_count", nil, 7)
	assertSeriesValue(t, series, "checkout_inflight_gauge", nil, 7)
	assertSeriesValue(t, series, "checkout_duration_timer_ms_count", nil, 2)
	assertSeriesValue(t, series, "checkout_duration_timer_ms_sum", nil, 80)
	assertSeriesValue(t, series, "checkout_duration_timer_ms_bucket", map[string]string{"le": "10"}, 1)
	assertSeriesValue(t, series, "checkout_duration_timer_ms_bucket", map[string]string{"le": "100"}, 2)
	assertSeriesValue(t, series, "checkout_duration_timer_ms_bucket", map[string]string{"le": "+Inf"}, 2)
	assertSeriesValue(t, series, "checkout_payload_histogram_count", nil, 1)
	assertSeriesValue(t, series, "checkout_payload_histogram_sum", nil, 250)
	assertSeriesValue(t, series, "checkout_payload_histogram_bucket", map[string]string{"le": "100"}, 0)
	assertSeriesValue(t, series, "checkout_payload_histogram_bucket", map[string]string{"le": "500"}, 1)
	assertSeriesValue(t, series, "checkout_users_set_cardinality", nil, 2)
	for _, item := range series {
		if labelValue(item.Labels, "is_custom") != "true" || labelValue(item.Labels, "service") != "checkout" {
			t.Fatalf("missing protected/application labels: %#v", item.Labels)
		}
		if item.Samples[0].Timestamp != 1234 {
			t.Fatalf("timestamp=%d, want 1234", item.Samples[0].Timestamp)
		}
	}
}

func TestAggregatorSetLimitAndWindowReset(t *testing.T) {
	agg := testAggregator(t)
	for _, value := range []string{"a", "b"} {
		if accepted, reason := agg.Add(Metric{Name: "users", Type: MetricTypeSet, SetValue: value, SampleRate: 1}, nil); !accepted {
			t.Fatalf("set value %q rejected: %s", value, reason)
		}
	}
	if accepted, reason := agg.Add(Metric{Name: "users", Type: MetricTypeSet, SetValue: "c", SampleRate: 1}, nil); accepted || reason != "set_limit" {
		t.Fatalf("third set value = (%v,%q), want (false,set_limit)", accepted, reason)
	}
	first := agg.Flush(time.UnixMilli(1))
	assertSeriesValue(t, first, "users_set_cardinality", nil, 2)
	if second := agg.Flush(time.UnixMilli(2)); len(second) != 0 {
		t.Fatalf("second flush returned %d series", len(second))
	}
}

func TestAggregatorSeparatesLabelSets(t *testing.T) {
	agg := testAggregator(t)
	metric := Metric{Name: "requests", Type: MetricTypeCounter, Value: 1, SampleRate: 1}
	agg.Add(metric, map[string]string{"service": "api"})
	agg.Add(metric, map[string]string{"service": "worker"})
	series := agg.Flush(time.UnixMilli(1))
	if len(series) != 2 {
		t.Fatalf("series=%d, want 2", len(series))
	}
}

func TestAggregatorCarriesRelativeGaugeAcrossFlushWindows(t *testing.T) {
	agg := testAggregator(t)
	now := time.Unix(100, 0)
	inputs := []Metric{
		{Name: "workers", Type: MetricTypeGauge, Value: 10, SampleRate: 1, ReceivedAt: now},
		{Name: "workers", Type: MetricTypeGauge, Value: 5, Relative: true, SampleRate: 1, ReceivedAt: now},
	}
	for _, metric := range inputs {
		if accepted, reason := agg.Add(metric, nil); !accepted {
			t.Fatalf("gauge rejected: %s", reason)
		}
	}
	assertSeriesValue(t, agg.Flush(time.UnixMilli(1)), "workers_gauge", nil, 15)

	if accepted, reason := agg.Add(Metric{Name: "workers", Type: MetricTypeGauge, Value: -2, Relative: true, SampleRate: 1, ReceivedAt: now.Add(time.Minute)}, nil); !accepted {
		t.Fatalf("relative gauge rejected: %s", reason)
	}
	assertSeriesValue(t, agg.Flush(time.UnixMilli(2)), "workers_gauge", nil, 13)
}

func TestAggregatorExpiresRelativeGaugeBaseline(t *testing.T) {
	agg := testAggregator(t)
	now := time.Unix(100, 0)
	agg.Add(Metric{Name: "workers", Type: MetricTypeGauge, Value: 10, SampleRate: 1, ReceivedAt: now}, nil)
	agg.Flush(time.UnixMilli(1))

	metric := Metric{Name: "workers", Type: MetricTypeGauge, Value: 2, Relative: true, SampleRate: 1, ReceivedAt: now.Add(2 * time.Hour)}
	if accepted, reason := agg.Add(metric, nil); !accepted {
		t.Fatalf("relative gauge rejected: %s", reason)
	}
	assertSeriesValue(t, agg.Flush(time.UnixMilli(2)), "workers_gauge", nil, 2)
}

func TestAggregatorSetsNegativeGaugeByResetThenDelta(t *testing.T) {
	agg := testAggregator(t)
	now := time.Unix(100, 0)
	if accepted, reason := agg.Add(Metric{Name: "temperature", Type: MetricTypeGauge, Value: 0, SampleRate: 1, ReceivedAt: now}, nil); !accepted {
		t.Fatalf("gauge reset rejected: %s", reason)
	}
	if accepted, reason := agg.Add(Metric{Name: "temperature", Type: MetricTypeGauge, Value: -5, Relative: true, SampleRate: 1, ReceivedAt: now}, nil); !accepted {
		t.Fatalf("negative gauge delta rejected: %s", reason)
	}
	assertSeriesValue(t, agg.Flush(time.UnixMilli(1)), "temperature_gauge", nil, -5)
}

func TestAggregatorEnforcesByteLimitAcrossInflightAndActiveWindows(t *testing.T) {
	agg, err := NewAggregator(AggregationConfig{
		TimerBucketsMS:        []float64{10},
		HistogramBuckets:      []float64{10},
		MaxSetValuesPerSeries: 10,
		MaxBytes:              300,
		GaugeTTL:              time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	metric := Metric{Name: "requests", Type: MetricTypeCounter, Value: 1, SampleRate: 1}
	if accepted, reason := agg.Add(metric, nil); !accepted {
		t.Fatalf("first series rejected: %s", reason)
	}
	before := agg.Bytes()
	if before <= 0 {
		t.Fatal("aggregation bytes were not tracked")
	}
	if got := agg.Snapshot(time.Now()); len(got) != 1 {
		t.Fatalf("snapshot series=%d", len(got))
	}
	if agg.Bytes() != before {
		t.Fatalf("inflight bytes=%d, want %d", agg.Bytes(), before)
	}
	if accepted, reason := agg.Add(Metric{Name: "other", Type: MetricTypeCounter, Value: 1, SampleRate: 1}, nil); accepted || reason != "aggregation_bytes" {
		t.Fatalf("second window add=(%v,%q), want aggregation_bytes", accepted, reason)
	}
	agg.Commit()
	if agg.Bytes() != 0 {
		t.Fatalf("bytes after commit=%d", agg.Bytes())
	}
}

func TestAggregationConfigValidation(t *testing.T) {
	tests := []AggregationConfig{
		{TimerBucketsMS: nil, HistogramBuckets: []float64{1}, MaxSetValuesPerSeries: 1},
		{TimerBucketsMS: []float64{2, 1}, HistogramBuckets: []float64{1}, MaxSetValuesPerSeries: 1},
		{TimerBucketsMS: []float64{1}, HistogramBuckets: []float64{1}, MaxSetValuesPerSeries: 0},
	}
	for _, cfg := range tests {
		if _, err := NewAggregator(cfg); err == nil {
			t.Fatalf("expected invalid config: %#v", cfg)
		}
	}
}

func assertSeriesValue(t *testing.T, series []prompb.TimeSeries, name string, labels map[string]string, want float64) {
	t.Helper()
	for _, item := range series {
		if labelValue(item.Labels, "__name__") != name {
			continue
		}
		matches := true
		for key, value := range labels {
			if labelValue(item.Labels, key) != value {
				matches = false
				break
			}
		}
		if matches {
			if got := item.Samples[0].Value; got != want {
				t.Fatalf("%s value=%v, want %v", name, got, want)
			}
			return
		}
	}
	t.Fatalf("series %s labels=%v not found", name, labels)
}
