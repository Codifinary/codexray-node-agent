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

	DogStatsDEnabled                  = kingpin.Flag("dogstatsd-enabled", "Enable the node-local DogStatsD/StatsD UDP receiver").Default("false").Envar("DOGSTATSD_ENABLED").Bool()
	DogStatsDListen                   = kingpin.Flag("dogstatsd-listen", "DogStatsD UDP listen address").Default("0.0.0.0:8125").Envar("DOGSTATSD_LISTEN").String()
	DogStatsDMaxPacketBytes           = kingpin.Flag("dogstatsd-max-packet-bytes", "Maximum accepted DogStatsD UDP packet size").Default("8192").Envar("DOGSTATSD_MAX_PACKET_BYTES").Int()
	DogStatsDPacketQueueSize          = kingpin.Flag("dogstatsd-packet-queue-size", "Maximum number of UDP packets waiting for parsing").Default("4096").Envar("DOGSTATSD_PACKET_QUEUE_SIZE").Int()
	DogStatsDPacketQueueMaxBytes      = kingpin.Flag("dogstatsd-packet-queue-max-bytes", "Maximum UDP payload bytes waiting for parsing").Default("16MB").Envar("DOGSTATSD_PACKET_QUEUE_MAX_BYTES").Bytes()
	DogStatsDParseWorkers             = kingpin.Flag("dogstatsd-parse-workers", "Number of DogStatsD parser workers").Default("2").Envar("DOGSTATSD_PARSE_WORKERS").Int()
	DogStatsDBufferMaxEvents          = kingpin.Flag("dogstatsd-buffer-max-events", "Maximum converted DogStatsD events buffered in memory").Default("100000").Envar("DOGSTATSD_BUFFER_MAX_EVENTS").Int()
	DogStatsDBatchMaxSeries           = kingpin.Flag("dogstatsd-batch-max-series", "Maximum buffered DogStatsD series included in one node-agent scrape").Default("10000").Envar("DOGSTATSD_BATCH_MAX_SERIES").Int()
	DogStatsDMaxMetricNameLength      = kingpin.Flag("dogstatsd-max-metric-name-length", "Maximum DogStatsD metric name length").Default("255").Envar("DOGSTATSD_MAX_METRIC_NAME_LENGTH").Int()
	DogStatsDMaxTagsPerMetric         = kingpin.Flag("dogstatsd-max-tags-per-metric", "Maximum retained DogStatsD tags per metric").Default("20").Envar("DOGSTATSD_MAX_TAGS_PER_METRIC").Int()
	DogStatsDMaxTagKeyLength          = kingpin.Flag("dogstatsd-max-tag-key-length", "Maximum DogStatsD tag key length").Default("64").Envar("DOGSTATSD_MAX_TAG_KEY_LENGTH").Int()
	DogStatsDMaxTagValueLength        = kingpin.Flag("dogstatsd-max-tag-value-length", "Maximum DogStatsD tag value length").Default("128").Envar("DOGSTATSD_MAX_TAG_VALUE_LENGTH").Int()
	DogStatsDActiveSeriesPerMetricCap = kingpin.Flag("dogstatsd-active-series-per-metric-cap", "Maximum active DogStatsD series per metric name").Default("5000").Envar("DOGSTATSD_ACTIVE_SERIES_PER_METRIC_CAP").Int()
	DogStatsDActiveSeriesGlobalCap    = kingpin.Flag("dogstatsd-active-series-global-cap", "Maximum active DogStatsD series across all metric names").Default("100000").Envar("DOGSTATSD_ACTIVE_SERIES_GLOBAL_CAP").Int()
	DogStatsDActiveSeriesTTL          = kingpin.Flag("dogstatsd-active-series-ttl", "How long an inactive DogStatsD series consumes a cardinality slot").Default("1h").Envar("DOGSTATSD_ACTIVE_SERIES_TTL").Duration()
	DogStatsDTagKeyBlocklist          = kingpin.Flag("dogstatsd-tag-key-blocklist", "Comma-separated DogStatsD tag keys to discard").Default("user_id,request_id,session_id,trace_id").Envar("DOGSTATSD_TAG_KEY_BLOCKLIST").String()
	DogStatsDFlushInterval            = kingpin.Flag("dogstatsd-flush-interval", "Production DogStatsD aggregation flush interval").Default("10s").Envar("DOGSTATSD_FLUSH_INTERVAL").Duration()
	DogStatsDMaxBatchBytes            = kingpin.Flag("dogstatsd-max-batch-bytes", "Maximum compressed custom-metric remote-write batch size").Default("4MB").Envar("DOGSTATSD_MAX_BATCH_BYTES").Bytes()
	DogStatsDCustomSpoolDir           = kingpin.Flag("dogstatsd-custom-spool-dir", "Independent custom-metric spool directory; defaults below WAL_DIR").Envar("DOGSTATSD_CUSTOM_SPOOL_DIR").String()
	DogStatsDCustomSpoolMaxBytes      = kingpin.Flag("dogstatsd-custom-spool-max-bytes", "Maximum independent custom-metric spool size").Default("250MB").Envar("DOGSTATSD_CUSTOM_SPOOL_MAX_BYTES").Bytes()
	DogStatsDCustomSpoolMaxAge        = kingpin.Flag("dogstatsd-custom-spool-max-age", "Maximum age of an undelivered custom-metric batch").Default("24h").Envar("DOGSTATSD_CUSTOM_SPOOL_MAX_AGE").Duration()
	DogStatsDQuarantineMaxBytes       = kingpin.Flag("dogstatsd-quarantine-max-bytes", "Maximum disk bytes retained for permanently rejected/corrupt custom batches").Default("25MB").Envar("DOGSTATSD_QUARANTINE_MAX_BYTES").Bytes()
	DogStatsDTimerBucketsMS           = kingpin.Flag("dogstatsd-timer-buckets-ms", "Comma-separated production timer bucket upper bounds in milliseconds").Default("5,10,25,50,100,250,500,1000,2500,5000").Envar("DOGSTATSD_TIMER_BUCKETS_MS").String()
	DogStatsDHistogramBuckets         = kingpin.Flag("dogstatsd-histogram-buckets", "Comma-separated production histogram bucket upper bounds").Default("0.1,0.5,1,2.5,5,10,25,50,100,250,500,1000").Envar("DOGSTATSD_HISTOGRAM_BUCKETS").String()
	DogStatsDSetMaxValuesPerSeries    = kingpin.Flag("dogstatsd-set-max-values-per-series", "Maximum unique set values retained per series and flush window").Default("10000").Envar("DOGSTATSD_SET_MAX_VALUES_PER_SERIES").Int()
	DogStatsDAggregationMaxBytes      = kingpin.Flag("dogstatsd-aggregation-max-bytes", "Maximum estimated bytes retained in active and in-flight aggregation windows").Default("128MB").Envar("DOGSTATSD_AGGREGATION_MAX_BYTES").Bytes()
	DogStatsDAllowedSourceCIDRs       = kingpin.Flag("dogstatsd-allowed-source-cidrs", "Comma-separated CIDRs allowed to send UDP metrics").Default("127.0.0.0/8,10.0.0.0/8,172.16.0.0/12,192.168.0.0/16,::1/128,fc00::/7").Envar("DOGSTATSD_ALLOWED_SOURCE_CIDRS").String()
	DogStatsDMaxBytesPerSecond        = kingpin.Flag("dogstatsd-max-bytes-per-second", "Maximum accepted DogStatsD UDP bytes per second per node").Default("10485760").Envar("DOGSTATSD_MAX_BYTES_PER_SECOND").Int()
	DogStatsDMaxEventsPerSecond       = kingpin.Flag("dogstatsd-max-events-per-second", "Maximum accepted DogStatsD metric lines per second per node").Default("10000").Envar("DOGSTATSD_MAX_EVENTS_PER_SECOND").Int()
	DogStatsDSaturationThreshold      = kingpin.Flag("dogstatsd-saturation-threshold", "Queue or spool utilization that begins the sustained-saturation readiness timer").Default("0.8").Envar("DOGSTATSD_SATURATION_THRESHOLD").Float64()
	DogStatsDSaturationDuration       = kingpin.Flag("dogstatsd-saturation-duration", "How long DogStatsD queue or spool pressure must remain high before readiness fails").Default("5m").Envar("DOGSTATSD_SATURATION_DURATION").Duration()
	DogStatsDShutdownDrainTimeout     = kingpin.Flag("dogstatsd-shutdown-drain-timeout", "Maximum time to drain queued DogStatsD packets before shutdown continues").Default("10s").Envar("DOGSTATSD_SHUTDOWN_DRAIN_TIMEOUT").Duration()

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

	if *MetricsEndpoint != nil && !listenExplicitlyConfigured() {
		*ListenAddress = "127.0.0.1:10300"
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
