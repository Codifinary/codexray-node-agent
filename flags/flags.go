// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package flags

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"gopkg.in/alecthomas/kingpin.v2"
)

var (
	ListenAddress  = kingpin.Flag("listen", "Listen address - ip:port or :port").Default("0.0.0.0:80").Envar("LISTEN").String()
	PprofListen    = kingpin.Flag("pprof-listen", "Listen address for /debug/pprof/ (separate from --listen). Loopback by default; a non-loopback address requires --pprof-auth-token. Empty disables pprof").Default("127.0.0.1:10302").Envar("PPROF_LISTEN").String()
	PprofAuthToken = kingpin.Flag("pprof-auth-token", "Bearer token required by the pprof listener (prefer the PPROF_AUTH_TOKEN env var from a Secret)").Envar("PPROF_AUTH_TOKEN").String()

	GoMemoryLimitRatio = kingpin.Flag("go-memory-limit-ratio", "Set the Go soft memory limit to this fraction of the container memory limit (e.g. 0.7). Ignored if GOMEMLIMIT is set. 0 disables").Default("0").Envar("GO_MEMORY_LIMIT_RATIO").Float64()

	MemoryWatchdogThreshold      = kingpin.Flag("memory-watchdog-threshold", "Write a diagnostic bundle when the agent's cgroup memory usage reaches this size (e.g. 3GB). 0 disables").Default("0").Envar("MEMORY_WATCHDOG_THRESHOLD").Bytes()
	MemoryWatchdogThresholdRatio = kingpin.Flag("memory-watchdog-threshold-ratio", "Write a diagnostic bundle at this fraction of the cgroup memory limit (e.g. 0.8). 0 disables").Default("0").Envar("MEMORY_WATCHDOG_THRESHOLD_RATIO").Float64()
	MemoryWatchdogMinInterval    = kingpin.Flag("memory-watchdog-min-interval", "Minimum time between diagnostic bundles").Default("15m").Envar("MEMORY_WATCHDOG_MIN_INTERVAL").Duration()
	MemoryWatchdogMaxDumps       = kingpin.Flag("memory-watchdog-max-dumps", "Diagnostic bundles kept on disk").Default("3").Envar("MEMORY_WATCHDOG_MAX_DUMPS").Int()
	MemoryWatchdogDir            = kingpin.Flag("memory-watchdog-dir", "Directory for diagnostic bundles (defaults to <wal-dir>/diagnostics)").Envar("MEMORY_WATCHDOG_DIR").String()

	NodeFSExcludeTypes       = kingpin.Flag("node-fs-exclude-types", "Regex of filesystem types excluded from node_filesystem_* (default excludes virtual/pseudo/overlay/tmpfs)").Envar("NODE_FS_EXCLUDE_TYPES").String()
	NodeFSExcludeMountPoints = kingpin.Flag("node-fs-exclude-mount-points", "Regex of mount points excluded from node_filesystem_* (default excludes /dev, /proc, /sys, /run, runtime and pod volume mounts)").Envar("NODE_FS_EXCLUDE_MOUNT_POINTS").String()
	NodeFSMaxMounts          = kingpin.Flag("node-fs-max-mounts", "Maximum filesystems reported per node (cardinality cap)").Default("64").Envar("NODE_FS_MAX_MOUNTS").Int()

	CgroupRoot        = kingpin.Flag("cgroupfs-root", "The mount point of the host cgroupfs root").Default("/sys/fs/cgroup").Envar("CGROUPFS_ROOT").String()
	DisableLogParsing = kingpin.Flag("disable-log-parsing", "Disable container log parsing").Default("false").Envar("DISABLE_LOG_PARSING").Bool()
	DisablePinger     = kingpin.Flag("disable-pinger", "Don't ping upstreams").Default("false").Envar("DISABLE_PINGER").Bool()
	DisableL7Tracing  = kingpin.Flag("disable-l7-tracing", "Disable L7 tracing").Default("false").Envar("DISABLE_L7_TRACING").Bool()

	ContainerAllowlist = kingpin.Flag("container-allowlist", "List of allowed containers (regex patterns)").Envar("CONTAINER_ALLOWLIST").Strings()
	ContainerDenylist  = kingpin.Flag("container-denylist", "List of denied containers (regex patterns)").Envar("CONTAINER_DENYLIST").Strings()

	ExcludeHTTPMetricsByPath = kingpin.Flag("exclude-http-requests-by-path", "Skip HTTP metrics and traces by path").Envar("EXCLUDE_HTTP_REQUESTS_BY_PATH").Strings()

	ExternalNetworksWhitelist = kingpin.
					Flag("track-public-network", "Allow track connections to the specified IP networks, all private networks are allowed by default (e.g., Y.Y.Y.Y/mask)").
					Envar("TRACK_PUBLIC_NETWORK").
					Default("0.0.0.0/0").
					Strings()
	EphemeralPortRange = kingpin.Flag("ephemeral-port-range", "Destination and Listen TCP ports from this range will be skipped").Default("32768-60999").Envar("EPHEMERAL_PORT_RANGE").String()

	MinContainerAge          = kingpin.Flag("min-container-age", "Don't report metrics for containers younger than this. Suppresses short-lived job/cronjob pods that produce high-cardinality series. Logs and traces of young containers are still exported unless --min-container-age-applies-to=all. 0 disables.").Default("30s").Envar("MIN_CONTAINER_AGE").Duration()
	MinContainerAgeAppliesTo = kingpin.Flag("min-container-age-applies-to", "metrics: --min-container-age suppresses metrics only, so crash logs and traces of short-lived pods stay available (default). all: also drop logs and eBPF traces of containers younger than --min-container-age").Default("metrics").Envar("MIN_CONTAINER_AGE_APPLIES_TO").Enum("metrics", "all")
	InstrumentationDelay     = kingpin.Flag("instrumentation-delay", "Delay before attaching language-runtime instrumentation (Python GIL, Node.js event loop, etc.) to a newly started process.").Default("30s").Envar("INSTRUMENTATION_DELAY").Duration()

	Provider          = kingpin.Flag("provider", "`provider` label for `node_cloud_info` metric").Envar("PROVIDER").String()
	Region            = kingpin.Flag("region", "`region` label for `node_cloud_info` metric").Envar("REGION").String()
	AvailabilityZone  = kingpin.Flag("availability-zone", "`availability_zone` label for `node_cloud_info` metric").Envar("AVAILABILITY_ZONE").String()
	InstanceType      = kingpin.Flag("instance-type", "`instance_type` label for `node_cloud_info` metric").Envar("INSTANCE_TYPE").String()
	InstanceLifeCycle = kingpin.Flag("instance-life-cycle", "`instance_life_cycle` label for `node_cloud_info` metric").Envar("INSTANCE_LIFE_CYCLE").String()
	LogPerSecond      = kingpin.Flag("log-per-second", "The number of logs per second").Default("10.0").Envar("LOG_PER_SECOND").Float64()
	LogBurst          = kingpin.Flag("log-burst", "The maximum number of tokens that can be consumed in a single call to allow").Default("100").Envar("LOG_BURST").Int()

	MaxLabelLength = kingpin.Flag("max-label-length", "Maximum length of a metric label value").Default("4096").Envar("MAX_LABEL_LENGTH").Int()

	// Cardinality controls. Defaults keep the existing output; see docs/cardinality.md.
	L7ActualDestination       = kingpin.Flag("l7-actual-destination", "keep: label network/L7 metrics with both destination and actual_destination (default). drop: aggregate by destination only, which removes per-backend-pod series behind a Service").Default("keep").Envar("L7_ACTUAL_DESTINATION").Enum("keep", "drop")
	L7LatencyBuckets          = kingpin.Flag("l7-latency-buckets", "Comma-separated upper bounds in seconds for L7 and DNS latency histograms, e.g. 0.01,0.05,0.1,0.5,1,5. Empty keeps the Prometheus default buckets (11 bounds)").Envar("L7_LATENCY_BUCKETS").String()
	DNSMaxDomainsPerContainer = kingpin.Flag("dns-max-domains-per-container", "Maximum distinct DNS domains tracked per container; further domains are counted under domain=\"overflow\". 0 disables the cap").Default("500").Envar("DNS_MAX_DOMAINS_PER_CONTAINER").Int()
	LogPatternsMax            = kingpin.Flag("log-patterns-max", "Maximum distinct warning/error log patterns tracked per log source; further new patterns are counted under pattern_hash=\"overflow\". 0 = unlimited").Default("512").Envar("LOG_PATTERNS_MAX").Int()
	ContainerLogMinLevel      = kingpin.Flag("container-log-min-level", "Least severe log level exported via OTLP when log collection is on: all (default, every line), debug, info, warning (warning, error and critical), error (error and critical) or critical. Lines whose level cannot be detected are dropped unless --container-log-keep-unknown-level. Does not affect container_log_messages_total").Default("all").Envar("CONTAINER_LOG_MIN_LEVEL").Enum("all", "debug", "info", "warning", "error", "critical")
	ContainerLogKeepUnknown   = kingpin.Flag("container-log-keep-unknown-level", "With --container-log-min-level other than all, still export lines whose level cannot be detected (plain-text output, e.g. some panics and stack traces printed without a level)").Default("false").Envar("CONTAINER_LOG_KEEP_UNKNOWN_LEVEL").Bool()

	CollectorEndpoint = kingpin.Flag("collector-endpoint", "A base endpoint URL for metrics, traces, logs, and profiles").Envar("COLLECTOR_ENDPOINT").URL()
	ApiKey            = kingpin.Flag("api-key", "Codexray API key").Envar("API_KEY").String()
	MetricsEndpoint   = kingpin.Flag("metrics-endpoint", "The URL of the endpoint to send metrics to").Envar("METRICS_ENDPOINT").URL()
	TracesEndpoint    = kingpin.Flag("traces-endpoint", "The URL of the endpoint to send traces to").Envar("TRACES_ENDPOINT").URL()
	TracesSampling    = kingpin.Flag("traces-sampling", "Trace sampling rate (0.0 to 1.0). With adaptive sampling, the rate for spans that are neither errors nor slow").Default("1.0").Envar("TRACES_SAMPLING").Float64()

	TracesAdaptiveSampling = kingpin.Flag("traces-adaptive-sampling-enabled", "Adaptive trace sampling: keep every error span and every span slower than --traces-slow-threshold, and sample the rest at --traces-sampling").Default("false").Envar("TRACES_ADAPTIVE_SAMPLING_ENABLED").Bool()
	TracesSlowThreshold    = kingpin.Flag("traces-slow-threshold", "With adaptive sampling, spans longer than this are always kept").Default("500ms").Envar("TRACES_SLOW_THRESHOLD").Duration()
	LogsEndpoint           = kingpin.Flag("logs-endpoint", "The URL of the endpoint to send logs to").Envar("LOGS_ENDPOINT").URL()
	ProfilesEndpoint       = kingpin.Flag("profiles-endpoint", "The URL of the endpoint to send profiles to").Envar("PROFILES_ENDPOINT").URL()
	InsecureSkipVerify     = kingpin.Flag("insecure-skip-verify", "whether to skip verifying the certificate or not").Envar("INSECURE_SKIP_VERIFY").Default("false").Bool()

	ProfilingSymbolCacheSize        = kingpin.Flag("profiling-symbol-cache-size", "Entries in each eBPF profiler symbol cache (per-PID, build-ID and same-file caches)").Default("256").Envar("PROFILING_SYMBOL_CACHE_SIZE").Int()
	ProfilingSymbolCacheKeepRounds  = kingpin.Flag("profiling-symbol-cache-keep-rounds", "Collection rounds (1 round = 1m) an unused symbol table is kept in cache").Default("8").Envar("PROFILING_SYMBOL_CACHE_KEEP_ROUNDS").Int()
	ProfilingMaxMiniDebugInfoSize   = kingpin.Flag("profiling-max-minidebuginfo-size", "Maximum decompressed size of an ELF .gnu_debugdata section loaded for symbolization; larger ones are skipped. 0 = unlimited").Default("32MB").Envar("PROFILING_MAX_MINIDEBUGINFO_SIZE").Bytes()
	ProfilingMaxPerfMapSize         = kingpin.Flag("profiling-max-perfmap-size", "Skip JIT perf map files (/tmp/perf-<pid>.map from JVM/Node.js) larger than this when symbolizing; JIT frames are then left unresolved. 0 = unlimited").Default("64MB").Envar("PROFILING_MAX_PERFMAP_SIZE").Bytes()
	ProfilingJvmPerfmapInterval     = kingpin.Flag("profiling-jvm-perfmap-dump-interval", "How often to ask each JVM to rewrite its perf map (Compiler.perfmap). Each dump makes the JVM write, and the agent re-parse, the whole map").Default("1m").Envar("PROFILING_JVM_PERFMAP_DUMP_INTERVAL").Duration()
	ProfilingSymbolNameCacheEntries = kingpin.Flag("profiling-symbol-name-cache-entries", "Maximum cached symbol names per loaded ELF file. 0 = unlimited").Default("16384").Envar("PROFILING_SYMBOL_NAME_CACHE_ENTRIES").Int()

	ScrapeInterval = kingpin.Flag("scrape-interval", "How often to gather metrics from the agent").Default("20s").Envar("SCRAPE_INTERVAL").Duration()
	WalDir         = kingpin.Flag("wal-dir", "Path to where the agent stores data (e.g. the metrics Write-Ahead Log)").Default("/tmp/Codexray-node-agent").Envar("WAL_DIR").String()
	MaxSpoolSize   = kingpin.Flag("max-spool-size", "Maximum size of the on-disk spool used to buffer data when it cannot be sent to collector. Supports size suffixes like KB, MB, or GB.").Default("500MB").Envar("MAX_SPOOL_SIZE").Bytes()

	DogStatsDEnabled                  = kingpin.Flag("statsd-enabled", "Enable the node-local StatsD/DogStatsD UDP receiver").Default("false").Envar("STATSD_ENABLED").Bool()
	DogStatsDListen                   = kingpin.Flag("statsd-listen", "StatsD UDP listen address").Default("0.0.0.0:8125").Envar("STATSD_LISTEN").String()
	DogStatsDMaxPacketBytes           = kingpin.Flag("statsd-max-packet-bytes", "Maximum accepted StatsD UDP packet size").Default("8192").Envar("STATSD_MAX_PACKET_BYTES").Int()
	DogStatsDPacketQueueSize          = kingpin.Flag("statsd-packet-queue-size", "Maximum number of UDP packets waiting for parsing").Default("4096").Envar("STATSD_PACKET_QUEUE_SIZE").Int()
	DogStatsDPacketQueueMaxBytes      = kingpin.Flag("statsd-packet-queue-max-bytes", "Maximum UDP payload bytes waiting for parsing").Default("16MB").Envar("STATSD_PACKET_QUEUE_MAX_BYTES").Bytes()
	DogStatsDParseWorkers             = kingpin.Flag("statsd-parse-workers", "Number of StatsD parser workers").Default("2").Envar("STATSD_PARSE_WORKERS").Int()
	DogStatsDBufferMaxEvents          = kingpin.Flag("statsd-buffer-max-events", "Maximum converted StatsD events buffered in memory").Default("100000").Envar("STATSD_BUFFER_MAX_EVENTS").Int()
	DogStatsDBatchMaxSeries           = kingpin.Flag("statsd-batch-max-series", "Maximum buffered StatsD series included in one node-agent scrape").Default("10000").Envar("STATSD_BATCH_MAX_SERIES").Int()
	DogStatsDMaxMetricNameLength      = kingpin.Flag("statsd-max-metric-name-length", "Maximum StatsD metric name length").Default("255").Envar("STATSD_MAX_METRIC_NAME_LENGTH").Int()
	DogStatsDMaxTagsPerMetric         = kingpin.Flag("statsd-max-tags-per-metric", "Maximum retained StatsD tags per metric").Default("20").Envar("STATSD_MAX_TAGS_PER_METRIC").Int()
	DogStatsDMaxTagKeyLength          = kingpin.Flag("statsd-max-tag-key-length", "Maximum StatsD tag key length").Default("64").Envar("STATSD_MAX_TAG_KEY_LENGTH").Int()
	DogStatsDMaxTagValueLength        = kingpin.Flag("statsd-max-tag-value-length", "Maximum StatsD tag value length").Default("128").Envar("STATSD_MAX_TAG_VALUE_LENGTH").Int()
	DogStatsDActiveSeriesPerMetricCap = kingpin.Flag("statsd-active-series-per-metric-cap", "Maximum active StatsD series per metric name").Default("5000").Envar("STATSD_ACTIVE_SERIES_PER_METRIC_CAP").Int()
	DogStatsDActiveSeriesGlobalCap    = kingpin.Flag("statsd-active-series-global-cap", "Maximum active StatsD series across all metric names").Default("100000").Envar("STATSD_ACTIVE_SERIES_GLOBAL_CAP").Int()
	DogStatsDActiveSeriesTTL          = kingpin.Flag("statsd-active-series-ttl", "How long an inactive StatsD series consumes a cardinality slot").Default("1h").Envar("STATSD_ACTIVE_SERIES_TTL").Duration()
	DogStatsDTagKeyBlocklist          = kingpin.Flag("statsd-tag-key-blocklist", "Comma-separated StatsD tag keys to discard").Default("user_id,request_id,session_id,trace_id").Envar("STATSD_TAG_KEY_BLOCKLIST").String()
	DogStatsDFlushInterval            = kingpin.Flag("statsd-flush-interval", "Production StatsD aggregation flush interval").Default("10s").Envar("STATSD_FLUSH_INTERVAL").Duration()
	DogStatsDMaxBatchBytes            = kingpin.Flag("statsd-max-batch-bytes", "Maximum compressed custom-metric remote-write batch size").Default("4MB").Envar("STATSD_MAX_BATCH_BYTES").Bytes()
	DogStatsDCustomSpoolDir           = kingpin.Flag("statsd-custom-spool-dir", "Independent custom-metric spool directory; defaults below WAL_DIR").Envar("STATSD_CUSTOM_SPOOL_DIR").String()
	DogStatsDCustomSpoolMaxBytes      = kingpin.Flag("statsd-custom-spool-max-bytes", "Maximum independent custom-metric spool size").Default("250MB").Envar("STATSD_CUSTOM_SPOOL_MAX_BYTES").Bytes()
	DogStatsDCustomSpoolMaxAge        = kingpin.Flag("statsd-custom-spool-max-age", "Maximum age of an undelivered custom-metric batch").Default("24h").Envar("STATSD_CUSTOM_SPOOL_MAX_AGE").Duration()
	DogStatsDQuarantineMaxBytes       = kingpin.Flag("statsd-quarantine-max-bytes", "Maximum disk bytes retained for permanently rejected/corrupt custom batches").Default("25MB").Envar("STATSD_QUARANTINE_MAX_BYTES").Bytes()
	DogStatsDTimerBucketsMS           = kingpin.Flag("statsd-timer-buckets-ms", "Comma-separated production timer bucket upper bounds in milliseconds").Default("5,10,25,50,100,250,500,1000,2500,5000").Envar("STATSD_TIMER_BUCKETS_MS").String()
	DogStatsDHistogramBuckets         = kingpin.Flag("statsd-histogram-buckets", "Comma-separated production histogram bucket upper bounds").Default("0.1,0.5,1,2.5,5,10,25,50,100,250,500,1000").Envar("STATSD_HISTOGRAM_BUCKETS").String()
	DogStatsDSetMaxValuesPerSeries    = kingpin.Flag("statsd-set-max-values-per-series", "Maximum unique set values retained per series and flush window").Default("10000").Envar("STATSD_SET_MAX_VALUES_PER_SERIES").Int()
	DogStatsDAggregationMaxBytes      = kingpin.Flag("statsd-aggregation-max-bytes", "Maximum estimated bytes retained in active and in-flight aggregation windows").Default("128MB").Envar("STATSD_AGGREGATION_MAX_BYTES").Bytes()
	DogStatsDAllowedSourceCIDRs       = kingpin.Flag("statsd-allowed-source-cidrs", "Comma-separated CIDRs allowed to send UDP metrics").Default("127.0.0.0/8,10.0.0.0/8,172.16.0.0/12,192.168.0.0/16,::1/128,fc00::/7").Envar("STATSD_ALLOWED_SOURCE_CIDRS").String()
	DogStatsDMaxBytesPerSecond        = kingpin.Flag("statsd-max-bytes-per-second", "Maximum accepted StatsD UDP bytes per second per node").Default("10485760").Envar("STATSD_MAX_BYTES_PER_SECOND").Int()
	DogStatsDMaxEventsPerSecond       = kingpin.Flag("statsd-max-events-per-second", "Maximum accepted StatsD metric lines per second per node").Default("10000").Envar("STATSD_MAX_EVENTS_PER_SECOND").Int()
	DogStatsDSaturationThreshold      = kingpin.Flag("statsd-saturation-threshold", "Queue or spool utilization that begins the sustained-saturation readiness timer").Default("0.8").Envar("STATSD_SATURATION_THRESHOLD").Float64()
	DogStatsDSaturationDuration       = kingpin.Flag("statsd-saturation-duration", "How long StatsD queue or spool pressure must remain high before readiness fails").Default("5m").Envar("STATSD_SATURATION_DURATION").Duration()
	DogStatsDShutdownDrainTimeout     = kingpin.Flag("statsd-shutdown-drain-timeout", "Maximum time to drain queued StatsD packets before shutdown continues").Default("10s").Envar("STATSD_SHUTDOWN_DRAIN_TIMEOUT").Duration()

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
	normalizeLegacyStatsDConfig()
	os.Args = append(os.Args[:1], NormalizeBoolArgs(kingpin.CommandLine.Model(), os.Args[1:])...)
	kingpin.Parse()
	resolveTelemetryFromFlags()

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

	if *MetricsEndpoint != nil && !listenExplicitlyConfigured() {
		*ListenAddress = "127.0.0.1:10300"
	}
}

func normalizeLegacyStatsDConfig() {
	const legacyFlagPrefix = "--dogstatsd-"
	const canonicalFlagPrefix = "--statsd-"
	for i, arg := range os.Args[1:] {
		if strings.HasPrefix(arg, legacyFlagPrefix) {
			os.Args[i+1] = canonicalFlagPrefix + strings.TrimPrefix(arg, legacyFlagPrefix)
		}
	}

	const legacyEnvPrefix = "DOGSTATSD_"
	const canonicalEnvPrefix = "STATSD_"
	for _, entry := range os.Environ() {
		name, value, found := strings.Cut(entry, "=")
		if !found || !strings.HasPrefix(name, legacyEnvPrefix) {
			continue
		}
		canonicalName := canonicalEnvPrefix + strings.TrimPrefix(name, legacyEnvPrefix)
		if _, exists := os.LookupEnv(canonicalName); !exists {
			_ = os.Setenv(canonicalName, value)
		}
	}
}

func listenExplicitlyConfigured() bool {
	if _, exists := os.LookupEnv("LISTEN"); exists {
		return true
	}
	for _, arg := range os.Args[1:] {
		if arg == "--listen" || strings.HasPrefix(arg, "--listen=") {
			return true
		}
	}
	return false
}

// NormalizeBoolArgs rewrites --flag=false to --no-flag and --flag=true to --flag for boolean
// flags, which kingpin otherwise rejects ("unexpected false"). Other arguments are unchanged;
// an unparsable value is left for kingpin to report.
func NormalizeBoolArgs(model *kingpin.ApplicationModel, args []string) []string {
	isBool := map[string]bool{}
	for _, f := range model.Flags {
		isBool[f.Name] = f.IsBoolFlag()
	}
	out := make([]string, 0, len(args))
	for i, a := range args {
		if a == "--" {
			return append(out, args[i:]...)
		}
		name, val, ok := strings.Cut(strings.TrimPrefix(a, "--"), "=")
		if !ok || !strings.HasPrefix(a, "--") || !isBool[name] {
			out = append(out, a)
			continue
		}
		b, err := strconv.ParseBool(val)
		switch {
		case err != nil:
			out = append(out, a)
		case b:
			out = append(out, "--"+name)
		default:
			out = append(out, "--no-"+name)
		}
	}
	return out
}
