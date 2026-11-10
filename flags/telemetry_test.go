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
	base := TelemetryInputs{NodeMetrics: true, ContainerResourceMetrics: true, Profiling: true,
		L7Metrics: unset(), L7Traces: unset(), LogCollection: unset(), LogParsing: unset()}

	// Upgrade with no new settings: everything stays on.
	got := ResolveTelemetry(base)
	require.Equal(t, TelemetryConfig{true, true, true, true, true, true, true}, got)

	// Legacy DISABLE_L7_TRACING keeps meaning "no L7 at all".
	in := base
	in.LegacyDisableL7Tracing = true
	got = ResolveTelemetry(in)
	require.False(t, got.L7Metrics)
	require.False(t, got.L7Traces)
	require.False(t, got.L7Probes())

	// ...unless the new, finer setting is explicit: metrics without traces.
	in.L7Metrics = ob(true)
	got = ResolveTelemetry(in)
	require.True(t, got.L7Metrics)
	require.False(t, got.L7Traces)
	require.True(t, got.L7Probes(), "L7 eBPF programs stay loaded for metrics")

	// Legacy DISABLE_LOG_PARSING keeps meaning "no log pipeline".
	in = base
	in.LegacyDisableLogParsing = true
	got = ResolveTelemetry(in)
	require.False(t, got.LogPipeline())

	// Collection without parsing, and parsing without collection.
	in = base
	in.LogParsing = ob(false)
	got = ResolveTelemetry(in)
	require.True(t, got.LogCollection)
	require.False(t, got.LogParsing)
	require.True(t, got.LogPipeline())
	in = base
	in.LogCollection = ob(false)
	got = ResolveTelemetry(in)
	require.False(t, got.LogCollection)
	require.True(t, got.LogParsing)

	// Traces off does not affect L7 metrics.
	in = base
	in.L7Traces = ob(false)
	got = ResolveTelemetry(in)
	require.True(t, got.L7Metrics)
	require.False(t, got.L7Traces)
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
