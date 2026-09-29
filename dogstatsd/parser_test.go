package dogstatsd

import (
	"testing"
	"time"
)

func TestParseLine(t *testing.T) {
	now := time.Unix(123, 0)
	metric, err := parseLine("orders.created:2|c|@0.5|#service:checkout,env:dev", now, 255)
	if err != nil {
		t.Fatal(err)
	}
	if metric.Name != "orders.created" || metric.Type != MetricTypeCounter || metric.Value != 2 || metric.SampleRate != 0.5 {
		t.Fatalf("unexpected metric: %#v", metric)
	}
	if metric.Tags["service"] != "checkout" || metric.Tags["env"] != "dev" {
		t.Fatalf("unexpected tags: %#v", metric.Tags)
	}
}

func TestParseLineRejectsUnsafeInput(t *testing.T) {
	tests := []string{
		"missing-separator",
		"metric:not-a-number|g",
		"metric:1|unknown",
		"metric:1|c|@0",
		"metric:1|c|@1.5",
		"metric:1|g|@0.5",
		"metric:user-1|s|@0.5",
		"metric:1|c|@0.5|@0.25",
		"metric:1|c|#service:api|#env:dev",
		"metric:1|c|#service:api,service:worker",
		"metric:1|c|#region-one:a,region_one:b",
	}
	for _, input := range tests {
		t.Run(input, func(t *testing.T) {
			if _, err := parseLine(input, time.Now(), 255); err == nil {
				t.Fatalf("expected %q to fail", input)
			}
		})
	}
}

func TestParseLineMarksSignedGaugesAsRelative(t *testing.T) {
	for _, input := range []string{"metric:+5|g", "metric:-2|g"} {
		metric, err := parseLine(input, time.Now(), 255)
		if err != nil {
			t.Fatalf("parseLine(%q): %v", input, err)
		}
		if !metric.Relative {
			t.Fatalf("parseLine(%q) did not mark the gauge relative", input)
		}
	}
}

func TestParseLineMarksUnsignedGaugeAsAbsolute(t *testing.T) {
	metric, err := parseLine("metric:5|g", time.Now(), 255)
	if err != nil {
		t.Fatal(err)
	}
	if metric.Relative {
		t.Fatal("unsigned gauge was marked relative")
	}
}

func TestParseLineIgnoresUnknownExtensions(t *testing.T) {
	metric, err := parseLine("metric:1|c|T1700000000|c:container-id|e:local-origin", time.Now(), 255)
	if err != nil {
		t.Fatal(err)
	}
	if metric.Name != "metric" || metric.Value != 1 || metric.Type != MetricTypeCounter {
		t.Fatalf("unknown extensions changed metric: %#v", metric)
	}
}

func TestParseLineEnforcesMetricNameLength(t *testing.T) {
	if _, err := parseLine("longname:1|c", time.Now(), 4); err == nil {
		t.Fatal("expected metric name length error")
	}
}

func TestParseLineNormalizesLabelsForPrometheus(t *testing.T) {
	metric, err := parseLine("metric:1|c|#R\u00e9gion:west,9-zone:a", time.Now(), 255)
	if err != nil {
		t.Fatal(err)
	}
	if metric.Tags["r_gion"] != "west" {
		t.Fatalf("unicode label key was not normalized: %#v", metric.Tags)
	}
	if metric.Tags["_9_zone"] != "a" {
		t.Fatalf("numeric label key was not prefixed: %#v", metric.Tags)
	}
}
