// Copyright Codexray
// SPDX-License-Identifier: AGPL-3.0

package containers

import (
	"github.com/prometheus/client_golang/prometheus"
)

// selfMetrics count what happens to listens and the processes and containers they need,
// so a listen the agent misses shows up as a number instead of a missing series.
type selfMetrics struct {
	listenOpenEvents        *prometheus.CounterVec
	containerLookupFailures *prometheus.CounterVec
	processRegistrations    *prometheus.CounterVec
	listenAttributions      *prometheus.CounterVec
	listensUnattributed     prometheus.Counter
	dockerPortBindings      *prometheus.CounterVec
	dockerMetadataRefreshes *prometheus.CounterVec
}

// Label values of the self metrics.
const (
	listenStored       = "stored"
	listenPortFiltered = "port_filtered"
	listenNoContainer  = "no_container"

	lookupCgroup   = "cgroup"
	lookupMetadata = "metadata"
	lookupCreate   = "create"
	lookupRegister = "register"

	registrationOK                = "ok"
	registrationTaskstatsFallback = "taskstats_fallback"
	registrationGone              = "failed_gone"
	registrationTaskstatsFailed   = "failed_taskstats"

	attributionEventRetry = "event_retry"
	attributionGCMissing  = "gc_missing"
	attributionGCRekey    = "gc_rekey"

	bindingOK           = "ok"
	bindingParseFailed  = "parse_failed"
	bindingPortFiltered = "port_filtered"

	refreshOK      = "ok"
	refreshError   = "error"
	refreshSkipped = "skipped"
)

var agentMetrics = newSelfMetrics()

func newSelfMetrics() *selfMetrics {
	counterVec := func(name, help, label string, values ...string) *prometheus.CounterVec {
		v := prometheus.NewCounterVec(prometheus.CounterOpts{Name: name, Help: help}, []string{label})
		for _, value := range values {
			v.WithLabelValues(value)
		}
		return v
	}
	return &selfMetrics{
		listenOpenEvents: counterVec("node_agent_listen_open_events_total",
			"TCP listen open events by what became of them", "result", listenStored, listenPortFiltered, listenNoContainer),
		containerLookupFailures: counterVec("node_agent_container_lookup_failures_total",
			"Pids whose container could not be resolved, by the step that failed", "reason",
			lookupCgroup, lookupMetadata, lookupCreate, lookupRegister),
		processRegistrations: counterVec("node_agent_process_registrations_total",
			"Attempts to register a process with its container", "result",
			registrationOK, registrationTaskstatsFallback, registrationGone, registrationTaskstatsFailed),
		listenAttributions: counterVec("node_agent_listen_attributions_total",
			"Listens tied to a process after the listen event itself could not be", "source",
			attributionEventRetry, attributionGCMissing, attributionGCRekey),
		listensUnattributed: prometheus.NewCounter(prometheus.CounterOpts{Name: "node_agent_listens_unattributed_total",
			Help: "Listens still open in the kernel that no registered process of their container holds, so they are not reported"}),
		dockerPortBindings: counterVec("node_agent_docker_port_bindings_total",
			"Docker published TCP port bindings by what became of them", "result", bindingOK, bindingParseFailed, bindingPortFiltered),
		dockerMetadataRefreshes: counterVec("node_agent_docker_metadata_refreshes_total",
			"Docker re-inspections after a container restart", "result", refreshOK, refreshError, refreshSkipped),
	}
}

func (m *selfMetrics) collectors() []prometheus.Collector {
	return []prometheus.Collector{m.listenOpenEvents, m.containerLookupFailures, m.processRegistrations,
		m.listenAttributions, m.listensUnattributed, m.dockerPortBindings, m.dockerMetadataRefreshes}
}
