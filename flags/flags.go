// Copyright Codexray, Inc.
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package flags

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/alecthomas/kingpin.v2"
)

var (
	ListenAddress     = kingpin.Flag("listen", "Listen address - ip:port or :port").Default("0.0.0.0:80").Envar("LISTEN").String()
	CgroupRoot        = kingpin.Flag("cgroupfs-root", "The mount point of the host cgroupfs root").Default("/sys/fs/cgroup").Envar("CGROUPFS_ROOT").String()
	DisableLogParsing = kingpin.Flag("disable-log-parsing", "Disable container log parsing").Default("false").Envar("DISABLE_LOG_PARSING").Bool()
	DisablePinger     = kingpin.Flag("disable-pinger", "Don't ping upstreams").Default("false").Envar("DISABLE_PINGER").Bool()
	DisableL7Tracing  = kingpin.Flag("disable-l7-tracing", "Disable L7 tracing").Default("false").Envar("DISABLE_L7_TRACING").Bool()
	// DisableProfiling skips Pyroscope ebpf session initialisation entirely.
	// Useful when you suspect the profiling pipeline (its own BPF maps and
	// symbol caches) of contributing to memory pressure, or when no profile
	// collector is configured. Setting --profiles-endpoint='' has the same
	// effect ONLY when --collector-endpoint is also unset, since the latter
	// auto-derives /v1/profiles. This flag overrides both.
	DisableProfiling = kingpin.Flag("disable-profiling", "Disable eBPF CPU profiling (Pyroscope ebpf session)").Default("false").Envar("DISABLE_PROFILING").Bool()

	ContainerAllowlist = kingpin.Flag("container-allowlist", "List of allowed containers (regex patterns)").Envar("CONTAINER_ALLOWLIST").Strings()
	ContainerDenylist  = kingpin.Flag("container-denylist", "List of denied containers (regex patterns)").Envar("CONTAINER_DENYLIST").Strings()

	ExcludeHTTPMetricsByPath = kingpin.Flag("exclude-http-requests-by-path", "Skip HTTP metrics and traces by path").Envar("EXCLUDE_HTTP_REQUESTS_BY_PATH").Strings()

	ExternalNetworksWhitelist = kingpin.
					Flag("track-public-network", "Allow track connections to the specified IP networks, all private networks are allowed by default (e.g., Y.Y.Y.Y/mask)").
					Envar("TRACK_PUBLIC_NETWORK").
					Default("0.0.0.0/0").
					Strings()
	EphemeralPortRange = kingpin.Flag("ephemeral-port-range", "Destination and Listen TCP ports from this range will be skipped").Default("32768-60999").Envar("EPHEMERAL_PORT_RANGE").String()

	// MinContainerAge skips metric/trace emission for containers that have
	// existed for less than this duration. Removes high-cardinality noise
	// from short-lived cron/init/job pods. Ported from upstream coroot v1.32.5.
	MinContainerAge = kingpin.Flag("min-container-age", "Skip containers younger than this (cardinality control)").Default("30s").Envar("MIN_CONTAINER_AGE").Duration()
	// MaxFqdnsPerContainer caps the number of unique FQDN destinations tracked
	// per container; once exceeded, additional FQDNs are bucketed under
	// "~other" instead of growing the per-container metric set unboundedly.
	// Ported from upstream coroot v1.32.5.
	MaxFqdnsPerContainer = kingpin.Flag("max-fqdns-per-container", "Cap on unique FQDN labels per container (0 = unbounded)").Default("50").Envar("MAX_FQDNS_PER_CONTAINER").Int()
	// InstrumentationDelay delays Python GIL / Node.js event-loop uprobe
	// attachment by this duration after the process is first seen. Avoids
	// instrumenting short-lived processes that exit before we'd benefit, and
	// directly mitigates the startup ELF-parse burst that triggers our OOM.
	// Ported from upstream coroot v1.32.5.
	InstrumentationDelay = kingpin.Flag("instrumentation-delay", "Wait this long after process start before attaching Python/Node.js uprobes").Default("30s").Envar("INSTRUMENTATION_DELAY").Duration()

	Provider          = kingpin.Flag("provider", "`provider` label for `node_cloud_info` metric").Envar("PROVIDER").String()
	Region            = kingpin.Flag("region", "`region` label for `node_cloud_info` metric").Envar("REGION").String()
	AvailabilityZone  = kingpin.Flag("availability-zone", "`availability_zone` label for `node_cloud_info` metric").Envar("AVAILABILITY_ZONE").String()
	InstanceType      = kingpin.Flag("instance-type", "`instance_type` label for `node_cloud_info` metric").Envar("INSTANCE_TYPE").String()
	InstanceLifeCycle = kingpin.Flag("instance-life-cycle", "`instance_life_cycle` label for `node_cloud_info` metric").Envar("INSTANCE_LIFE_CYCLE").String()
	LogPerSecond      = kingpin.Flag("log-per-second", "The number of logs per second").Default("10.0").Envar("LOG_PER_SECOND").Float64()
	LogBurst          = kingpin.Flag("log-burst", "The maximum number of tokens that can be consumed in a single call to allow").Default("100").Envar("LOG_BURST").Int()

	MaxLabelLength = kingpin.Flag("max-label-length", "Maximum length of a metric label value").Default("4096").Envar("MAX_LABEL_LENGTH").Int()

	CollectorEndpoint  = kingpin.Flag("collector-endpoint", "A base endpoint URL for metrics, traces, logs, and profiles").Envar("COLLECTOR_ENDPOINT").URL()
	ApiKey             = kingpin.Flag("api-key", "Codexray API key").Envar("API_KEY").String()
	MetricsEndpoint    = kingpin.Flag("metrics-endpoint", "The URL of the endpoint to send metrics to").Envar("METRICS_ENDPOINT").URL()
	TracesEndpoint     = kingpin.Flag("traces-endpoint", "The URL of the endpoint to send traces to").Envar("TRACES_ENDPOINT").URL()
	TracesSampling     = kingpin.Flag("traces-sampling", "Trace sampling rate (0.0 to 1.0)").Default("1.0").Envar("TRACES_SAMPLING").Float64()
	LogsEndpoint       = kingpin.Flag("logs-endpoint", "The URL of the endpoint to send logs to").Envar("LOGS_ENDPOINT").URL()
	ProfilesEndpoint   = kingpin.Flag("profiles-endpoint", "The URL of the endpoint to send profiles to").Envar("PROFILES_ENDPOINT").URL()
	InsecureSkipVerify = kingpin.Flag("insecure-skip-verify", "whether to skip verifying the certificate or not").Envar("INSECURE_SKIP_VERIFY").Default("false").Bool()

	ScrapeInterval = kingpin.Flag("scrape-interval", "How often to gather metrics from the agent").Default("15s").Envar("SCRAPE_INTERVAL").Duration()

	// MemDiagInterval enables a periodic diagnostic log line summarising per-container
	// and per-connection memory hotspots (L7 parser map sizes, active connection counts,
	// destination cardinality, registry event-channel length). Default 0 disables it.
	MemDiagInterval = kingpin.Flag("memdiag-interval", "If >0, log per-container/per-connection memory diagnostics at this interval").Default("0s").Envar("MEMDIAG_INTERVAL").Duration()
	// MemDiagFile mirrors memdiag output to a file with fsync after every tick.
	// Useful for OOM post-mortems: stderr is buffered and the tail is often lost
	// when the kernel SIGKILLs the process; a synced file keeps the last tick.
	MemDiagFile = kingpin.Flag("memdiag-file", "If set, also write memdiag output to this file (fsync'd each tick)").Default("").Envar("MEMDIAG_FILE").String()
	// HeapDumpDir, when set, makes memdiag drop a runtime/pprof heap profile to
	// this directory: (a) every HeapDumpEvery ticks, and (b) whenever HeapAlloc
	// grows by >= HeapDumpOnGrowthMB since the last tick. The last HeapDumpKeep
	// files are retained.
	HeapDumpDir        = kingpin.Flag("heap-dump-dir", "If set, memdiag periodically writes runtime/pprof heap profiles here").Default("").Envar("HEAP_DUMP_DIR").String()
	HeapDumpEvery      = kingpin.Flag("heap-dump-every", "Take a heap dump every N memdiag ticks (0 = only on growth)").Default("0").Envar("HEAP_DUMP_EVERY").Int()
	HeapDumpOnGrowthMB = kingpin.Flag("heap-dump-on-growth-mb", "Take a heap dump when HeapAlloc grows by this many MB between memdiag ticks (0 = disabled)").Default("50").Envar("HEAP_DUMP_ON_GROWTH_MB").Int()
	HeapDumpKeep       = kingpin.Flag("heap-dump-keep", "Number of heap dumps to retain in --heap-dump-dir").Default("8").Envar("HEAP_DUMP_KEEP").Int()
	// MemProfileRate tunes runtime.MemProfileRate at startup if >0. Lower values
	// (e.g. 4096) increase profile fidelity at small CPU cost; 1 = sample every
	// allocation (expensive — use only when chasing a leak).
	MemProfileRate = kingpin.Flag("mem-profile-rate", "If >0, set runtime.MemProfileRate at startup (bytes between samples; default Go behavior = 524288)").Default("0").Envar("MEM_PROFILE_RATE").Int()
	// PprofListen, when non-empty, starts an additional HTTP server with the
	// net/http/pprof handlers on this address. Use to easily kubectl
	// port-forward into pprof without sharing the /metrics port. Example:
	//   PPROF_LISTEN=:6060 → all interfaces, port 6060
	//   PPROF_LISTEN=127.0.0.1:6060 → loopback only (still reachable via
	//   kubectl port-forward; recommended on hostNetwork DaemonSets to avoid
	//   exposing the endpoint on the node's external IP).
	PprofListen  = kingpin.Flag("pprof-listen", "If non-empty, expose /debug/pprof/* on this address").Default("").Envar("PPROF_LISTEN").String()
	WalDir       = kingpin.Flag("wal-dir", "Path to where the agent stores data (e.g. the metrics Write-Ahead Log)").Default("/tmp/Codexray-node-agent").Envar("WAL_DIR").String()
	MaxSpoolSize = kingpin.Flag("max-spool-size", "Maximum size of the on-disk spool used to buffer data when it cannot be sent to collector. Supports size suffixes like KB, MB, or GB.").Default("500MB").Envar("MAX_SPOOL_SIZE").Bytes()

	agentVersion = kingpin.Flag("version", "Print version and exit").Default("false").Bool()
	Version      = "unknown"
)

func GetString(fl *string) string {
	if fl == nil {
		return ""
	}
	return *fl
}

func init() {
	if strings.HasSuffix(os.Args[0], ".test") {
		return
	}

	kingpin.HelpFlag.Short('h').Hidden()
	kingpin.Parse()

	if *agentVersion {
		fmt.Println("Version:", Version)
		os.Exit(0)
	}

	if *CollectorEndpoint != nil {
		u := *CollectorEndpoint
		if *MetricsEndpoint == nil {
			*MetricsEndpoint = u.JoinPath("/v1/metrics")
		}
		if *TracesEndpoint == nil {
			*TracesEndpoint = u.JoinPath("/v1/traces")
		}
		if *LogsEndpoint == nil {
			*LogsEndpoint = u.JoinPath("/v1/logs")
		}
		if *ProfilesEndpoint == nil {
			*ProfilesEndpoint = u.JoinPath("/v1/profiles")
		}
	}

	if *MetricsEndpoint != nil {
		*ListenAddress = "127.0.0.1:10300"
	}
}
