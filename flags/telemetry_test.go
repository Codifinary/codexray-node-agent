// Copyright Codexray
// SPDX-License-Identifier: Apache-2.0

package flags

import (
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/alecthomas/kingpin.v2"
)

func ob(v bool) *OptionalBool { return &OptionalBool{set: true, value: v} }

func TestResolveTelemetryDefaultsAndLegacy(t *testing.T) {
	unset := func() *OptionalBool { return &OptionalBool{} }
	// Kingpin defaults: node metrics on, container resource metrics and profiling off.
	base := TelemetryInputs{NodeMetrics: true,
		L7Metrics: unset(), L7Traces: unset(), LogCollection: unset(), LogParsing: unset()}

	// No settings: node metrics only.
	got := ResolveTelemetry(base)
	require.Equal(t, TelemetryConfig{NodeMetrics: true}, got)
	require.False(t, got.L7Probes())
	require.False(t, got.LogPipeline())

	// Legacy DISABLE_* settings change nothing on top of the off defaults.
	in := base
	in.LegacyDisableL7Tracing, in.LegacyDisableLogParsing = true, true
	require.Equal(t, TelemetryConfig{NodeMetrics: true}, ResolveTelemetry(in))

	// An explicit new setting wins over the legacy one: metrics without traces.
	in.L7Metrics = ob(true)
	got = ResolveTelemetry(in)
	require.True(t, got.L7Metrics)
	require.False(t, got.L7Traces)
	require.True(t, got.L7Probes(), "L7 eBPF programs load for metrics")

	// Each feature turns on independently.
	in = base
	in.L7Traces = ob(true)
	got = ResolveTelemetry(in)
	require.False(t, got.L7Metrics)
	require.True(t, got.L7Traces)
	in = base
	in.LogParsing = ob(true)
	got = ResolveTelemetry(in)
	require.False(t, got.LogCollection)
	require.True(t, got.LogParsing)
	require.True(t, got.LogPipeline())
	in = base
	in.LogCollection = ob(true)
	got = ResolveTelemetry(in)
	require.True(t, got.LogCollection)
	require.False(t, got.LogParsing)
}

func TestTelemetryFlagDefaults(t *testing.T) {
	for name, want := range map[string]string{
		"node-metrics-enabled": "true", "container-resource-metrics-enabled": "false", "profiling-enabled": "false",
	} {
		f := kingpin.CommandLine.GetFlag(name)
		require.NotNil(t, f, name)
		require.Equal(t, []string{want}, f.Model().Default, name)
	}
	require.Equal(t, TelemetryConfig{NodeMetrics: true}, Telemetry)
}

// Exhaustive: every combination of the boolean features resolves independently.
func TestResolveTelemetryIndependence(t *testing.T) {
	for mask := 0; mask < 1<<7; mask++ {
		bit := func(i int) bool { return mask&(1<<i) != 0 }
		got := ResolveTelemetry(TelemetryInputs{
			NodeMetrics: bit(0), ContainerResourceMetrics: bit(1), Profiling: bit(2),
			L7Metrics: ob(bit(3)), L7Traces: ob(bit(4)), LogCollection: ob(bit(5)), LogParsing: ob(bit(6)),
			LegacyDisableL7Tracing: true, LegacyDisableLogParsing: true, // explicit values win
		})
		require.Equal(t, TelemetryConfig{bit(0), bit(1), bit(3), bit(4), bit(5), bit(6), bit(2)}, got, "mask %b", mask)
	}
}

func TestOptionalBoolFlagPrecedence(t *testing.T) {
	newApp := func() (*kingpin.Application, *OptionalBool) {
		app := kingpin.New("t", "")
		v := &OptionalBool{}
		app.Flag("x-enabled", "").Envar("X_ENABLED").SetValue(v)
		return app, v
	}
	app, v := newApp()
	_, err := app.Parse(nil)
	require.NoError(t, err)
	require.True(t, v.Or(true), "unset -> default")
	require.False(t, v.Or(false))

	t.Setenv("X_ENABLED", "false")
	app, v = newApp()
	_, err = app.Parse(nil)
	require.NoError(t, err)
	require.False(t, v.Or(true), "env applies")

	app, v = newApp()
	_, err = app.Parse([]string{"--x-enabled"})
	require.NoError(t, err)
	require.True(t, v.Or(false), "flag beats env")

	t.Setenv("X_ENABLED", "true")
	app, v = newApp()
	_, err = app.Parse([]string{"--no-x-enabled"})
	require.NoError(t, err)
	require.False(t, v.Or(true), "--no- form beats env")

	app, v = newApp()
	_, err = app.Parse([]string{"--x-enabled"})
	require.NoError(t, err)
	require.True(t, v.Or(false), "bare flag means true")

	t.Setenv("X_ENABLED", "maybe")
	app, _ = newApp()
	_, err = app.Parse(nil)
	require.Error(t, err, "invalid env value is rejected, not silently defaulted")
}
