package dogstatsd

import (
	"strings"
	"testing"
	"time"
)

func TestAggregatorMarksWindowedSeriesAsDelta(t *testing.T) {
	agg := testAggregator(t)
	now := time.Now()
	for _, m := range []Metric{
		{Name: "jobs", Type: MetricTypeCounter, Value: 3, SampleRate: 1, ReceivedAt: now},
		{Name: "lat", Type: MetricTypeTimer, Value: 20, SampleRate: 1, ReceivedAt: now},
		{Name: "size", Type: MetricTypeHistogram, Value: 200, SampleRate: 1, ReceivedAt: now},
		{Name: "temp", Type: MetricTypeGauge, Value: 1, SampleRate: 1, ReceivedAt: now},
	} {
		if ok, reason := agg.Add(m, nil); !ok {
			t.Fatalf("add %s: %s", m.Name, reason)
		}
	}
	for _, s := range agg.Snapshot(now) {
		name := labelValue(s.Labels, "__name__")
		wantDelta := strings.HasSuffix(name, "_count") || strings.HasSuffix(name, "_bucket") || strings.HasSuffix(name, "_sum")
		got := labelValue(s.Labels, temporalityLabel)
		if wantDelta && got != "Delta" {
			t.Errorf("%s: temporality %q, want Delta", name, got)
		}
		if !wantDelta && got != "" {
			t.Errorf("%s: unexpected temporality %q", name, got)
		}
		if wantDelta {
			wantType := "Histogram"
			if name == "jobs_counter_count" {
				wantType = "Sum"
			}
			if mt := labelValue(s.Labels, metricTypeLabel); mt != wantType {
				t.Errorf("%s: metric type %q, want %q", name, mt, wantType)
			}
		}
	}
}

func TestParseTagsStripsBracesAndDropsEmptyValues(t *testing.T) {
	tags, err := parseTags("{env}:{prod},{region}:us-east-1},empty:,blank:{},{}:x,flag")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"env": "prod", "region": "us-east-1", "flag": "true"}
	if len(tags) != len(want) {
		t.Fatalf("got %v, want %v", tags, want)
	}
	for k, v := range want {
		if tags[k] != v {
			t.Fatalf("got %v, want %v", tags, want)
		}
	}
}

func TestParseLineAcceptsDistributionAsHistogram(t *testing.T) {
	m, err := parseLine("req.size:42|d|#env:prod", time.Now(), 200)
	if err != nil {
		t.Fatal(err)
	}
	if m.Type != MetricTypeHistogram || m.Value != 42 || m.Tags["env"] != "prod" {
		t.Fatalf("unexpected metric %+v", m)
	}
}
