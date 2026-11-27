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

func TestParseTagsBracesAndEmptyValues(t *testing.T) {
	for raw, want := range map[string]map[string]string{
		// Template braces: one balanced outer pair only.
		"{env}:{prod}":      {"env": "prod"},
		`j:{"a":1}`:         {"j": `"a":1`},
		"v:a{b}":            {"v": "a{b}"},
		"v:{a}}":            {"v": "a}"},
		"region:us-east-1}": {"region": "us-east-1}"},
		// Empty value means true, as before this change; a bare key too.
		"key:,blank:{},flag": {"key": "true", "blank": "true", "flag": "true"},
		"9zone:x":            {"_9zone": "x"},
	} {
		tags, err := parseTags(raw)
		if err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		if len(tags) != len(want) {
			t.Fatalf("%s: got %v, want %v", raw, tags, want)
		}
		for k, v := range want {
			if tags[k] != v {
				t.Fatalf("%s: got %v, want %v", raw, tags, want)
			}
		}
	}
}

// Every unusable key rejects the line, as before: empty and symbol-only keys,
// and keys that normalize alike (braces and edge underscores included).
func TestParseTagsRejectsEmptyAndCollidingKeys(t *testing.T) {
	for _, raw := range []string{":x", "{}:y", "___:z", "_label_:x", "env:a,_env_:b", "env:a,{env}:b", "}}env{{:p,env:q"} {
		if tags, err := parseTags(raw); err == nil {
			t.Fatalf("%s: want an error, got %v", raw, tags)
		}
	}
}

func TestTagPolicyBlocksReservedAndSemanticKeys(t *testing.T) {
	policy := newTagPolicy("_internal_", 64, 200, 200)
	tags, err := parseTags("__codexray_temporality__:Cumulative,__codexray_metric_type__:Gauge,__codexray_is_monotonic__:false,le:5,quantile:0.5,_internal_:x,name:api")
	if err != nil {
		t.Fatal(err)
	}
	out, drops := policy.Apply(tags)
	if len(out) != 1 || out["name"] != "api" || drops.BlockedKeys != 6 {
		t.Fatalf("got %v drops %+v; only name may pass", out, drops)
	}
}

func TestClientTagsCannotChangeSemantics(t *testing.T) {
	policy := newTagPolicy("", 64, 200, 200)
	agg := testAggregator(t)
	now := time.Now()
	for _, line := range []string{
		"g:1|g|#__codexray_temporality__:Delta",
		"s:a|s|#__codexray_metric_type__:Sum",
		"c:1|c|#__codexray_temporality__:Cumulative,__codexray_is_monotonic__:false",
		"h:3|h|#le:5,quantile:0.9",
	} {
		m, err := parseLine(line, now, 200)
		if err != nil {
			t.Fatal(err)
		}
		tags, _ := policy.Apply(m.Tags)
		if ok, reason := agg.Add(*m, tags); !ok {
			t.Fatal(reason)
		}
	}
	for _, s := range agg.Snapshot(now) {
		name := labelValue(s.Labels, "__name__")
		switch name {
		case "g_gauge", "s_set_cardinality":
			if labelValue(s.Labels, temporalityLabel) != "" || labelValue(s.Labels, metricTypeLabel) != "" {
				t.Fatalf("%s got semantic labels: %v", name, s.Labels)
			}
		case "c_counter_count":
			if labelValue(s.Labels, temporalityLabel) != "Delta" || labelValue(s.Labels, monotonicLabel) != "true" {
				t.Fatalf("%s: %v", name, s.Labels)
			}
		case "h_histogram_sum", "h_histogram_count":
			if labelValue(s.Labels, "le") != "" || labelValue(s.Labels, "quantile") != "" {
				t.Fatalf("%s kept a client le/quantile: %v", name, s.Labels)
			}
		}
		for _, l := range s.Labels {
			if strings.HasPrefix(l.Name, "codexray_") {
				t.Fatalf("%s: client semantic tag survived as %s", name, l.Name)
			}
		}
	}
}

// A negative increment keeps the counter monotonic: the window is still an
// additive total, and the query service would read a non-monotonic Sum as a
// gauge.
func TestNegativeCounterStaysMonotonic(t *testing.T) {
	agg := testAggregator(t)
	now := time.Now()
	m, err := parseLine("c:-3|c", now, 200)
	if err != nil {
		t.Fatal(err)
	}
	agg.Add(*m, nil)
	series := agg.Snapshot(now)
	if len(series) != 1 || series[0].Samples[0].Value != -3 || labelValue(series[0].Labels, monotonicLabel) != "true" {
		t.Fatalf("got %v", series)
	}
}

func TestDeltaSeriesCarryWindowStart(t *testing.T) {
	agg := testAggregator(t)
	start := time.UnixMilli(1_700_000_000_000)
	agg.Add(Metric{Name: "jobs", Type: MetricTypeCounter, Value: 1, SampleRate: 1, ReceivedAt: start}, nil)
	agg.Add(Metric{Name: "lat", Type: MetricTypeTimer, Value: 5, SampleRate: 1, ReceivedAt: start.Add(time.Second)}, nil)
	flush := start.Add(10 * time.Second)
	for _, s := range agg.Snapshot(flush) {
		if got := labelValue(s.Labels, startTimeLabel); got != "1700000000000" {
			t.Fatalf("%s start %q", labelValue(s.Labels, "__name__"), got)
		}
	}
	agg.Commit()
	agg.Add(Metric{Name: "jobs", Type: MetricTypeCounter, Value: 1, SampleRate: 1, ReceivedAt: flush.Add(time.Second)}, nil)
	for _, s := range agg.Snapshot(flush.Add(10 * time.Second)) {
		if got := labelValue(s.Labels, startTimeLabel); got != "1700000010000" {
			t.Fatalf("second window starts at the first flush, got %q", got)
		}
	}
}

func TestEveryDeltaSeriesIsMarked(t *testing.T) {
	agg := testAggregator(t)
	now := time.Now()
	agg.Add(Metric{Name: "lat", Type: MetricTypeTimer, Value: 20, SampleRate: 1, ReceivedAt: now}, nil)
	for _, s := range agg.Snapshot(now) {
		name := labelValue(s.Labels, "__name__")
		if labelValue(s.Labels, monotonicLabel) != "true" || labelValue(s.Labels, temporalityLabel) != "Delta" || labelValue(s.Labels, metricTypeLabel) != "Histogram" {
			t.Fatalf("%s: %v", name, s.Labels)
		}
		if strings.HasSuffix(name, "_bucket") == (labelValue(s.Labels, "le") == "") {
			t.Fatalf("%s: le only on buckets: %v", name, s.Labels)
		}
	}
}

func TestSampledDistributionMatchesHistogram(t *testing.T) {
	now := time.Now()
	d, err := parseLine("x:4|d|@0.5", now, 200)
	if err != nil {
		t.Fatal(err)
	}
	h, _ := parseLine("x:4|h|@0.5", now, 200)
	a, b := testAggregator(t), testAggregator(t)
	a.Add(*d, nil)
	b.Add(*h, nil)
	sa, sb := a.Snapshot(now), b.Snapshot(now)
	if len(sa) != len(sb) {
		t.Fatalf("series %d vs %d", len(sa), len(sb))
	}
	for i := range sa {
		if sa[i].Samples[0].Value != sb[i].Samples[0].Value {
			t.Fatalf("%s: d %v vs h %v", labelValue(sa[i].Labels, "__name__"), sa[i].Samples[0].Value, sb[i].Samples[0].Value)
		}
	}
}

func TestStateBytesCountSemanticLabels(t *testing.T) {
	plain := &aggregateState{name: "x", metricType: MetricTypeGauge, labels: map[string]string{"a": "b"}}
	counter := &aggregateState{name: "x", metricType: MetricTypeCounter, labels: map[string]string{"a": "b"}}
	hist := &aggregateState{name: "x", metricType: MetricTypeHistogram, labels: map[string]string{"a": "b"}, buckets: make([]float64, 4)}
	if estimateAggregateStateBytes(counter)-estimateAggregateStateBytes(plain) != deltaSemanticBytes {
		t.Fatal("a counter adds one series of semantic labels")
	}
	histPlain := &aggregateState{name: "x", metricType: MetricTypeGauge, labels: map[string]string{"a": "b"}, buckets: make([]float64, 4)}
	if estimateAggregateStateBytes(hist)-estimateAggregateStateBytes(histPlain) != 6*deltaSemanticBytes {
		t.Fatal("a histogram adds buckets+2 series of semantic labels")
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
