package flags

import (
	"os"
	"testing"
)

func TestNormalizeLegacyStatsDConfig(t *testing.T) {
	originalArgs := os.Args
	t.Cleanup(func() { os.Args = originalArgs })
	os.Args = []string{"node-agent", "--dogstatsd-enabled", "--dogstatsd-listen=127.0.0.1:8125", "--listen=:10300"}

	t.Setenv("DOGSTATSD_MAX_EVENTS_PER_SECOND", "2000")
	t.Setenv("STATSD_MAX_TAGS_PER_METRIC", "30")
	t.Setenv("DOGSTATSD_MAX_TAGS_PER_METRIC", "10")

	normalizeLegacyStatsDConfig()

	if got, want := os.Args[1], "--statsd-enabled"; got != want {
		t.Fatalf("legacy flag was not normalized: got %q, want %q", got, want)
	}
	if got, want := os.Args[2], "--statsd-listen=127.0.0.1:8125"; got != want {
		t.Fatalf("legacy value flag was not normalized: got %q, want %q", got, want)
	}
	if got, want := os.Args[3], "--listen=:10300"; got != want {
		t.Fatalf("unrelated flag changed: got %q, want %q", got, want)
	}
	if got, want := os.Getenv("STATSD_MAX_EVENTS_PER_SECOND"), "2000"; got != want {
		t.Fatalf("legacy environment variable was not normalized: got %q, want %q", got, want)
	}
	if got, want := os.Getenv("STATSD_MAX_TAGS_PER_METRIC"), "30"; got != want {
		t.Fatalf("canonical environment variable did not take precedence: got %q, want %q", got, want)
	}
}
