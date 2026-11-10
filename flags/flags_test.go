package flags

import (
	"os"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"gopkg.in/alecthomas/kingpin.v2"
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

func TestNormalizeBoolArgs(t *testing.T) {
	app := kingpin.New("t", "")
	crm := app.Flag("container-resource-metrics-enabled", "").Default("false").Bool()
	pinger := app.Flag("disable-pinger", "").Default("false").Bool()
	l7 := &OptionalBool{}
	app.Flag("ebpf-l7-traces-enabled", "").SetValue(l7)
	name := app.Flag("provider", "").String()

	args := NormalizeBoolArgs(app.Model(), []string{
		"--container-resource-metrics-enabled=true", "--disable-pinger=false", "--ebpf-l7-traces-enabled=false", "--provider=false",
	})
	assert.Equal(t, []string{"--container-resource-metrics-enabled", "--no-disable-pinger", "--no-ebpf-l7-traces-enabled", "--provider=false"}, args)
	_, err := app.Parse(args)
	assert.NoError(t, err)
	assert.True(t, *crm)
	assert.False(t, *pinger)
	assert.True(t, l7.set, "tri-state flag is explicitly set")
	assert.False(t, l7.value)
	assert.Equal(t, "false", *name)

	assert.Equal(t, []string{"--disable-pinger=maybe", "--", "--disable-pinger=false"},
		NormalizeBoolArgs(app.Model(), []string{"--disable-pinger=maybe", "--", "--disable-pinger=false"}))
}

func TestContainerResourceMetricsDefaultOff(t *testing.T) {
	f := kingpin.CommandLine.GetFlag("container-resource-metrics-enabled")
	assert.NotNil(t, f)
	assert.Equal(t, []string{"false"}, f.Model().Default)
	assert.False(t, Telemetry.ContainerResourceMetrics)
}
