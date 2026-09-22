package flags

import (
	"os"
	"reflect"
	"testing"
)

func TestNormalizeLegacyStatsDArgs(t *testing.T) {
	originalArgs := os.Args
	t.Cleanup(func() { os.Args = originalArgs })

	os.Args = []string{
		"codexray-node-agent",
		"--dogstatsd-enabled",
		"--dogstatsd-listen=127.0.0.1:8125",
		"--listen=:10300",
	}
	normalizeLegacyStatsDConfig()

	want := []string{
		"codexray-node-agent",
		"--statsd-enabled",
		"--statsd-listen=127.0.0.1:8125",
		"--listen=:10300",
	}
	if !reflect.DeepEqual(os.Args, want) {
		t.Fatalf("normalized args = %#v, want %#v", os.Args, want)
	}
}

func TestNormalizeLegacyStatsDEnv(t *testing.T) {
	const legacyName = "DOGSTATSD_COMPATIBILITY_TEST"
	const canonicalName = "STATSD_COMPATIBILITY_TEST"
	restoreEnv(t, legacyName)
	restoreEnv(t, canonicalName)

	if err := os.Setenv(legacyName, "legacy-value"); err != nil {
		t.Fatal(err)
	}
	if err := os.Unsetenv(canonicalName); err != nil {
		t.Fatal(err)
	}

	normalizeLegacyStatsDConfig()
	if got := os.Getenv(canonicalName); got != "legacy-value" {
		t.Fatalf("canonical env = %q, want legacy-value", got)
	}
}

func TestNormalizeLegacyStatsDEnvDoesNotOverrideCanonical(t *testing.T) {
	const legacyName = "DOGSTATSD_PRECEDENCE_TEST"
	const canonicalName = "STATSD_PRECEDENCE_TEST"
	restoreEnv(t, legacyName)
	restoreEnv(t, canonicalName)

	if err := os.Setenv(legacyName, "legacy-value"); err != nil {
		t.Fatal(err)
	}
	if err := os.Setenv(canonicalName, "canonical-value"); err != nil {
		t.Fatal(err)
	}

	normalizeLegacyStatsDConfig()
	if got := os.Getenv(canonicalName); got != "canonical-value" {
		t.Fatalf("canonical env = %q, want canonical-value", got)
	}
}

func restoreEnv(t *testing.T, name string) {
	t.Helper()
	value, existed := os.LookupEnv(name)
	t.Cleanup(func() {
		if existed {
			_ = os.Setenv(name, value)
			return
		}
		_ = os.Unsetenv(name)
	})
}
