package dogstatsd

import (
	"math"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/prometheus/client_golang/prometheus"
)

func FuzzParseLine(f *testing.F) {
	for _, seed := range []string{
		"orders:1|c",
		"latency:12.5|ms|@0.5|#service:api,env:test",
		"workers:-2|g",
		"users:user-1|s|#region:west",
		"metric:1|c|#region-one:a,region_one:b",
		"\x00\xff:NaN|g|@-1|#:\x00",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, input string) {
		if len(input) > 8192 {
			return
		}
		metric, err := parseLine(input, time.Unix(1, 0), 255)
		if err != nil {
			return
		}
		if metric.Name == "" || len(metric.Name) > 255 {
			t.Fatalf("accepted invalid metric name length: %d", len(metric.Name))
		}
		if metric.Type != MetricTypeSet && (math.IsNaN(metric.Value) || math.IsInf(metric.Value, 0)) {
			t.Fatalf("accepted non-finite value: %v", metric.Value)
		}
		if metric.SampleRate <= 0 || metric.SampleRate > 1 {
			t.Fatalf("accepted sample rate: %v", metric.SampleRate)
		}
		for key, value := range metric.Tags {
			if !validNormalizedLabelKey(key) {
				t.Fatalf("accepted invalid normalized key %q", key)
			}
			if !utf8.ValidString(value) {
				t.Fatalf("accepted invalid UTF-8 label value %q", value)
			}
		}
	})
}

func FuzzTagNormalization(f *testing.F) {
	for _, seed := range []string{"service", "9-zone", "Région", "---", "a__b", "\x00\xff"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		if len(input) > 1024 {
			return
		}
		key := normalizeLabelKey(input)
		if !validNormalizedLabelKey(key) {
			t.Fatalf("normalizeLabelKey(%q)=%q", input, key)
		}
		value := sanitizeLabelValue(input)
		if value == "" || !utf8.ValidString(value) {
			t.Fatalf("sanitizeLabelValue(%q)=%q", input, value)
		}
	})
}

func FuzzReceiverProcessDatagram(f *testing.F) {
	for _, seed := range [][]byte{
		[]byte("orders:1|c\nlatency:10|ms|#service:api"),
		[]byte("workers:+1|g\nworkers:-2|g"),
		[]byte("metric:1|c|#duplicate:a,duplicate:b"),
		{0, 1, 2, 3, 255},
	} {
		f.Add(seed)
	}

	cfg := testConfig()
	agg, err := NewAggregator(AggregationConfig{
		TimerBucketsMS:        []float64{10, 100},
		HistogramBuckets:      []float64{1, 10},
		MaxSetValuesPerSeries: 32,
		MaxBytes:              1 << 20,
		GaugeTTL:              time.Minute,
	})
	if err != nil {
		f.Fatal(err)
	}
	receiver, err := NewProduction(cfg, prometheus.NewRegistry(), agg)
	if err != nil {
		f.Fatal(err)
	}

	f.Fuzz(func(t *testing.T, payload []byte) {
		if len(payload) > cfg.MaxPacketBytes {
			return
		}
		receiver.processPacket(payload)
		if agg.Bytes() > int64(1<<20) {
			t.Fatalf("aggregation exceeded configured byte bound: %d", agg.Bytes())
		}
	})
}

func validNormalizedLabelKey(key string) bool {
	if key == "" || (key[0] >= '0' && key[0] <= '9') {
		return false
	}
	for _, r := range key {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			continue
		}
		return false
	}
	return !strings.Contains(key, "__")
}
