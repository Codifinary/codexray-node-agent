// Copyright Codexray
// SPDX-License-Identifier: Apache-2.0

package flags

import (
	"fmt"
	"strconv"

	"gopkg.in/alecthomas/kingpin.v2"
)

// OptionalBool is a tri-state flag: unset, true or false. It lets a new setting override a
// legacy one only when it is explicitly configured, so existing deployments keep their
// behavior on upgrade.
type OptionalBool struct {
	set   bool
	value bool
}

func (o *OptionalBool) Set(s string) error {
	v, err := strconv.ParseBool(s)
	if err != nil {
		return fmt.Errorf("invalid boolean %q", s)
	}
	o.set, o.value = true, v
	return nil
}

func (o *OptionalBool) String() string {
	if !o.set {
		return ""
	}
	return strconv.FormatBool(o.value)
}

// IsBoolFlag allows `--flag` (true) and `--no-flag`-less usage like a regular bool flag.
func (o *OptionalBool) IsBoolFlag() bool { return true }

// Or returns the configured value, or def when unset.
func (o *OptionalBool) Or(def bool) bool {
	if o == nil || !o.set {
		return def
	}
	return o.value
}

func optionalBool(name, help, envar string) *OptionalBool {
	v := &OptionalBool{}
	kingpin.Flag(name, help).Envar(envar).SetValue(v)
	return v
}

// Precedence for every setting: command-line flag > environment variable > legacy setting
// (for settings that replace one) > default. kingpin applies flag > env; Resolve applies the rest.
var (
	NodeMetricsEnabled = kingpin.Flag("node-metrics-enabled",
		"Collect node-level CPU, memory, disk, filesystem and network metrics").
		Default("true").Envar("NODE_METRICS_ENABLED").Bool()
	ContainerResourceMetricsEnabled = kingpin.Flag("container-resource-metrics-enabled",
		"Export container-level CPU, memory, throttling, pressure, disk I/O and GPU usage series (container_resources_*). Turning it off does not affect container discovery, process attribution, network, L7, logs, traces or profiling").
		Default("true").Envar("CONTAINER_RESOURCE_METRICS_ENABLED").Bool()
	EbpfL7MetricsEnabled = optionalBool("ebpf-l7-metrics-enabled",
		"Export eBPF L7 RED metrics (container_<protocol>_requests_total/_duration). Default: true, or false when --disable-l7-tracing is set", "EBPF_L7_METRICS_ENABLED")
	EbpfL7TracesEnabled = optionalBool("ebpf-l7-traces-enabled",
		"Export eBPF L7 spans (sampled with --traces-sampling, or adaptively with --traces-adaptive-sampling-enabled). Default: true, or false when --disable-l7-tracing is set", "EBPF_L7_TRACES_ENABLED")
	ContainerLogCollectionEnabled = optionalBool("container-log-collection-enabled",
		"Tail container/journald logs and export them via OTLP. Default: true, or false when --disable-log-parsing is set", "CONTAINER_LOG_COLLECTION_ENABLED")
	ContainerLogParsingEnabled = optionalBool("container-log-parsing-enabled",
		"Extract log patterns and export container_log_messages_total. Default: true, or false when --disable-log-parsing is set", "CONTAINER_LOG_PARSING_ENABLED")
	ProfilingEnabled = kingpin.Flag("profiling-enabled",
		"Run the eBPF CPU profiler (requires a profiles endpoint). Disabling it does not affect L7 tracing or dependency detection").
		Default("true").Envar("PROFILING_ENABLED").Bool()
)

// TelemetryConfig is the resolved, effective set of telemetry features.
type TelemetryConfig struct {
	NodeMetrics              bool
	ContainerResourceMetrics bool
	L7Metrics                bool
	L7Traces                 bool
	LogCollection            bool
	LogParsing               bool
	Profiling                bool
}

// L7Probes reports whether the L7 eBPF programs (and TLS/Go uprobes) are needed.
func (t TelemetryConfig) L7Probes() bool { return t.L7Metrics || t.L7Traces }

// LogPipeline reports whether log tailers/parsers are needed at all.
func (t TelemetryConfig) LogPipeline() bool { return t.LogCollection || t.LogParsing }

// TelemetryInputs are the raw settings Resolve works from (separated for testing).
type TelemetryInputs struct {
	NodeMetrics, ContainerResourceMetrics, Profiling bool
	L7Metrics, L7Traces, LogCollection, LogParsing   *OptionalBool
	LegacyDisableL7Tracing, LegacyDisableLogParsing  bool
}

// ResolveTelemetry applies the defaults, which match earlier releases: every feature is on.
// The legacy --disable-l7-tracing turns off L7 metrics and traces, and --disable-log-parsing
// turns off log collection and parsing, as before. An explicit new setting wins over them.
func ResolveTelemetry(in TelemetryInputs) TelemetryConfig {
	return TelemetryConfig{
		NodeMetrics:              in.NodeMetrics,
		ContainerResourceMetrics: in.ContainerResourceMetrics,
		L7Metrics:                in.L7Metrics.Or(!in.LegacyDisableL7Tracing),
		L7Traces:                 in.L7Traces.Or(!in.LegacyDisableL7Tracing),
		LogCollection:            in.LogCollection.Or(!in.LegacyDisableLogParsing),
		LogParsing:               in.LogParsing.Or(!in.LegacyDisableLogParsing),
		Profiling:                in.Profiling,
	}
}

// AllTelemetry is the default configuration: every feature on.
var AllTelemetry = TelemetryConfig{true, true, true, true, true, true, true}

// Telemetry is resolved after flag parsing. Before that (and in tests) it matches the defaults.
var Telemetry = AllTelemetry

func resolveTelemetryFromFlags() {
	Telemetry = ResolveTelemetry(TelemetryInputs{
		NodeMetrics:              *NodeMetricsEnabled,
		ContainerResourceMetrics: *ContainerResourceMetricsEnabled,
		Profiling:                *ProfilingEnabled,
		L7Metrics:                EbpfL7MetricsEnabled,
		L7Traces:                 EbpfL7TracesEnabled,
		LogCollection:            ContainerLogCollectionEnabled,
		LogParsing:               ContainerLogParsingEnabled,
		LegacyDisableL7Tracing:   *DisableL7Tracing,
		LegacyDisableLogParsing:  *DisableLogParsing,
	})
}
