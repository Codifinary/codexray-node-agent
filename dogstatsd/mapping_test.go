package dogstatsd

import (
	"testing"
	"time"
)

func TestBuildRawSeries(t *testing.T) {
	tests := []struct {
		typeToken MetricType
		name      string
		wantName  string
		wantValue float64
	}{
		{MetricTypeCounter, "orders.created", "orders_created_counter_event", 4},
		{MetricTypeGauge, "checkout.inflight", "checkout_inflight_gauge", 2},
		{MetricTypeTimer, "checkout.duration", "checkout_duration_timer_ms", 2},
		{MetricTypeHistogram, "checkout.payload", "checkout_payload_histogram_value", 2},
		{MetricTypeSet, "checkout.users", "checkout_users_set_event", 1},
	}
	for _, tc := range tests {
		t.Run(string(tc.typeToken), func(t *testing.T) {
			series, err := buildRawSeries(Metric{
				Name: tc.name, Type: tc.typeToken, Value: 2, SampleRate: 0.5,
				ReceivedAt: time.UnixMilli(123),
			}, map[string]string{"service": "checkout"})
			if err != nil {
				t.Fatal(err)
			}
			if got := labelValue(series.Labels, "__name__"); got != tc.wantName {
				t.Fatalf("metric name = %q, want %q", got, tc.wantName)
			}
			if got := series.Samples[0].Value; got != tc.wantValue {
				t.Fatalf("value = %v, want %v", got, tc.wantValue)
			}
			if got := labelValue(series.Labels, "is_custom"); got != "true" {
				t.Fatalf("is_custom = %q", got)
			}
			if got := labelValue(series.Labels, "statsd_type"); got != "" {
				t.Fatalf("statsd_type must not be emitted, got %q", got)
			}
		})
	}
}

func TestTagPolicyProtectsAgentLabels(t *testing.T) {
	policy := newTagPolicy("request_id", 20, 64, 128)
	tags, drops := policy.Apply(map[string]string{
		"is_custom":   "false",
		"statsd_type": "counter",
		"instance":    "spoofed",
		"request_id":  "high-cardinality",
		"service":     "checkout",
	})
	if len(tags) != 1 || tags["service"] != "checkout" {
		t.Fatalf("unexpected filtered tags: %#v", tags)
	}
	if drops.BlockedKeys != 4 {
		t.Fatalf("blocked drops = %d, want 4", drops.BlockedKeys)
	}
}
