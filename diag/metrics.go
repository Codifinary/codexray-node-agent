// Copyright Codexray
// SPDX-License-Identifier: Apache-2.0

// Package diag holds the node agent's self-observability metrics.
//
// Every metric here describes the agent itself, never the workloads it observes,
// and every label has a small, fixed set of values (a component name, a reason, a
// perf map name). PIDs, container IDs, trace IDs, endpoints and file paths must
// never be used as labels in this package: these series exist to explain the
// agent's own memory and CPU use and must not become a cardinality problem of their own.
package diag

import (
	"github.com/prometheus/client_golang/prometheus"
)

const ns = "node_agent"

var (
	// Registry / discovery
	ContainersActive = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: ns, Name: "containers_active",
		Help: "Number of containers currently tracked by the agent's container registry",
	})
	ProcessesActive = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: ns, Name: "processes_active",
		Help: "Number of processes currently tracked by the agent across all containers",
	})
	UprobesActive = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: ns, Name: "uprobes_active",
		Help: "Number of uprobe/uretprobe links currently attached by the agent (TLS, Python, Node.js)",
	})
	UprobesLeakedOnClose = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: ns, Name: "uprobes_closed_after_owner_exit_total",
		Help: "Uprobe links that were attached after their owning process had already been closed and were detached immediately",
	})
	LogParsersActive = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: ns, Name: "log_parsers_active",
		Help: "Number of active log parsers (each owns a tailer or journald subscription and its goroutines)",
	})

	// eBPF event pipeline
	EventQueueLength = prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Namespace: ns, Name: "ebpf_event_queue_length",
		Help: "Current number of eBPF events waiting in the registry queue",
	}, func() float64 { return float64(eventQueueLen()) })
	EventQueueCapacity = prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Namespace: ns, Name: "ebpf_event_queue_capacity",
		Help: "Capacity of the registry eBPF event queue",
	}, func() float64 { return float64(eventQueueCap()) })
	PerfLostSamples = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: ns, Name: "ebpf_perf_lost_samples_total",
		Help: "Samples dropped by the kernel because a perf ring buffer was full (the agent did not drain it in time)",
	}, []string{"map"})

	// Dropped / discarded data. Reason values are a fixed set defined by the call sites.
	DroppedTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: ns, Name: "dropped_total",
		Help: "Telemetry items intentionally dropped by a bounded buffer or limit, by component and reason",
	}, []string{"component", "reason"})

	// Exporters
	ExportFailures = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: ns, Name: "export_failures_total",
		Help: "Failed attempts to deliver telemetry to the collector, by signal",
	}, []string{"signal"})

	// Profiling
	ProfilingCollectDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: ns, Name: "profiling_collect_duration_seconds",
		Help:    "Time spent collecting and symbolizing one round of eBPF CPU profiles",
		Buckets: []float64{0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60},
	})
	ProfilingProfiles = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: ns, Name: "profiling_profiles_total",
		Help: "Profiles built and their upload outcome",
	}, []string{"result"})
	ProfilingErrors = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: ns, Name: "profiling_errors_total",
		Help: "Profiling errors by stage",
	}, []string{"stage"})
	ProfilingTargets = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: ns, Name: "profiling_targets",
		Help: "Number of processes known to the profiling target finder",
	})
	ProfilingSessionActive = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: ns, Name: "profiling_session_active",
		Help: "1 if the eBPF profiling session is running",
	})

	// Feature state, so dashboards can tell "disabled" apart from "broken".
	FeatureEnabled = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: ns, Name: "feature_enabled",
		Help: "1 if a telemetry feature is enabled by configuration",
	}, []string{"feature"})

	// Memory watchdog
	WatchdogDumps = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: ns, Name: "memory_watchdog_dumps_total",
		Help: "Diagnostic dumps written by the memory watchdog",
	}, []string{"result"})
)

var (
	eventQueueLen = func() int { return 0 }
	eventQueueCap = func() int { return 0 }
)

// SetEventQueue wires the queue depth gauges to the registry's event channel.
func SetEventQueue(length, capacity func() int) {
	eventQueueLen = length
	eventQueueCap = capacity
}

// Register adds all diagnostics collectors to reg.
func Register(reg prometheus.Registerer) error {
	for _, c := range []prometheus.Collector{
		ContainersActive, ProcessesActive, UprobesActive, UprobesLeakedOnClose, LogParsersActive,
		EventQueueLength, EventQueueCapacity, PerfLostSamples, DroppedTotal, ExportFailures,
		ProfilingCollectDuration, ProfilingProfiles, ProfilingErrors, ProfilingTargets, ProfilingSessionActive,
		FeatureEnabled, WatchdogDumps,
	} {
		if err := reg.Register(c); err != nil {
			return err
		}
	}
	return nil
}

func boolToFloat(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// SetFeature records whether a feature is enabled.
func SetFeature(name string, enabled bool) {
	FeatureEnabled.WithLabelValues(name).Set(boolToFloat(enabled))
}
