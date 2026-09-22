// Copyright Codexray
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

	ContainerAllowlist = kingpin.Flag("container-allowlist", "List of allowed containers (regex patterns)").Envar("CONTAINER_ALLOWLIST").Strings()
	ContainerDenylist  = kingpin.Flag("container-denylist", "List of denied containers (regex patterns)").Envar("CONTAINER_DENYLIST").Strings()

	ExcludeHTTPMetricsByPath = kingpin.Flag("exclude-http-requests-by-path", "Skip HTTP metrics and traces by path").Envar("EXCLUDE_HTTP_REQUESTS_BY_PATH").Strings()

	ExternalNetworksWhitelist = kingpin.
					Flag("track-public-network", "Allow track connections to the specified IP networks, all private networks are allowed by default (e.g., Y.Y.Y.Y/mask)").
					Envar("TRACK_PUBLIC_NETWORK").
					Default("0.0.0.0/0").
					Strings()
	EphemeralPortRange = kingpin.Flag("ephemeral-port-range", "Destination and Listen TCP ports from this range will be skipped").Default("32768-60999").Envar("EPHEMERAL_PORT_RANGE").String()

	MinContainerAge      = kingpin.Flag("min-container-age", "Don't report metrics for containers younger than this. Suppresses short-lived job/cronjob pods that produce high-cardinality series. 0 disables.").Default("30s").Envar("MIN_CONTAINER_AGE").Duration()
	InstrumentationDelay = kingpin.Flag("instrumentation-delay", "Delay before attaching language-runtime instrumentation (Python GIL, Node.js event loop, etc.) to a newly started process.").Default("30s").Envar("INSTRUMENTATION_DELAY").Duration()

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
