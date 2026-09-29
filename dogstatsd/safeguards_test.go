package dogstatsd

import (
	"testing"
	"time"

	"github.com/prometheus/prometheus/prompb"
)

func TestSeriesLimiterCapsAndExpires(t *testing.T) {
	limiter := newSeriesLimiter(1, 2, time.Minute)
	series := func(name, service string) prompb.TimeSeries {
		return prompb.TimeSeries{Labels: []prompb.Label{
			{Name: "__name__", Value: name},
			{Name: "service", Value: service},
		}}
	}
	now := time.Unix(100, 0)
	if allowed, _, _ := limiter.Allow(series("requests", "a"), now); !allowed {
		t.Fatal("first series should be allowed")
	}
	if allowed, reason, _ := limiter.Allow(series("requests", "b"), now); allowed || reason != "series_cap_metric" {
		t.Fatalf("second series = (%v, %q), want metric cap", allowed, reason)
	}
	if allowed, _, count := limiter.Allow(series("requests", "b"), now.Add(2*time.Minute)); !allowed || count != 1 {
		t.Fatalf("expired replacement = (%v, %d), want allowed with count 1", allowed, count)
	}
}
