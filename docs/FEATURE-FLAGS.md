# Node Agent feature flags

**Precedence** for every setting: command-line flag > environment variable > legacy setting
(only where a new setting replaces one) > built-in default.

Boolean flags follow kingpin conventions: `--x-enabled` or `--no-x-enabled` on the command
line, `X_ENABLED=true|false` in the environment. An invalid value is rejected at startup; it
is never silently defaulted.

## Telemetry categories

| Setting (flag / env) | Default | Controls | Not affected |
|---|---|---|---|
| `--node-metrics-enabled` / `NODE_METRICS_ENABLED` | `true` | `node_*` CPU, memory, load, disk I/O, filesystem, network, uptime, cloud info | Everything else |
| `--container-resource-metrics-enabled` / `CONTAINER_RESOURCE_METRICS_ENABLED` | **`false`** | `container_resources_*`: CPU usage/limit/throttling/delay, memory RSS/cache/limit, PSI, disk size/usage/IO, GPU. Also skips the cgroup and taskstats reads behind them | Container discovery, process attribution, `container_info`, `container_restarts_total`, `container_oom_kills_total`, network, L7, logs, traces, profiling, JVM/.NET/Python/Node.js runtime metrics |
| `--ebpf-l7-metrics-enabled` / `EBPF_L7_METRICS_ENABLED` | **`false`** | `container_<protocol>_requests_total` / `_duration_*` RED metrics | Traces |
| `--ebpf-l7-traces-enabled` / `EBPF_L7_TRACES_ENABLED` | **`false`** | L7 spans (sampled, see Trace sampling). Off: tracer not initialized, no span allocation | L7 metrics. The L7 eBPF programs and TLS uprobes stay loaded while either L7 setting is on |
| `--container-log-collection-enabled` / `CONTAINER_LOG_COLLECTION_ENABLED` | **`false`** | Tailing and OTLP export of container/journald logs | Tracing, metrics |
| `--container-log-parsing-enabled` / `CONTAINER_LOG_PARSING_ENABLED` | **`false`** | Pattern extraction and `container_log_messages_total` | Log export |
| `--profiling-enabled` / `PROFILING_ENABLED` | **`false`** | eBPF CPU profiler (session, symbolization, JVM perf-map dumps, process handoff). Also requires a profiles endpoint | L7 tracing, dependency detection, TLS uprobes |

**Only node metrics are on by default.** Every other category is opt-in: set its setting to `true`.
With both L7 settings off, the L7 eBPF programs and TLS/Go uprobes are not loaded; TCP connection
metrics (used for the service map and network connectivity) are not L7 settings and stay on.
With both log settings off, no tailers, journald reader, parsers or log exporter are created.

`node_agent_feature_enabled{feature}` reports the **effective** value of each setting.

### Interactions

- **L7 metrics off and traces off:** L7 eBPF programs and TLS/Go uprobes are not loaded.
  This equals `DISABLE_L7_TRACING=true`.
- **Traces off:** RED metrics keep aggregating. Disabling traces never disables metrics.
- **Collection on, parsing off:** multiline assembly and export only, no pattern CPU or
  memory. **Parsing on, collection off:** pattern metrics only.
- **Per-process overrides still apply:** the `CODEXRAY_EBPF_TRACES`,
  `CODEXRAY_EBPF_PROFILING` and `CODEXRAY_LOG_MONITORING=disabled` environment variables
  inside workloads.

## Trace sampling

| Setting (flag / env) | Default | Effect |
|---|---|---|
| `--traces-sampling` / `TRACES_SAMPLING` | `1.0` | Fraction of spans exported (0.0 to 1.0) |
| `--traces-adaptive-sampling-enabled` / `TRACES_ADAPTIVE_SAMPLING_ENABLED` | `false` | When `true`: every error span is kept, every span longer than `TRACES_SLOW_THRESHOLD` is kept, and the remaining spans are sampled at `TRACES_SAMPLING` |
| `--traces-slow-threshold` / `TRACES_SLOW_THRESHOLD` | `500ms` | The "slow" cut-off for adaptive sampling (strictly longer than) |

A span is an error when the protocol reports one: HTTP status 400 or higher (HTTP/2: above 400
or a non-zero gRPC status), or a failed database, cache or ZooKeeper call.
Decisions are counted in `node_agent_traces_sampling_decisions_total{decision="kept|dropped",reason}`
with reasons `error`, `slow`, `rate` and `sampled_out`. Example: `TRACES_SAMPLING=0.1` with adaptive
sampling keeps all failures and slow requests and 10% of the fast successful ones.

## Compatibility with existing settings

| Existing setting | Behavior after upgrade | New equivalent |
|---|---|---|
| `DISABLE_L7_TRACING=true` | No effect beyond the new default (L7 off). An explicit new L7 setting wins | `EBPF_L7_METRICS_ENABLED=false` + `EBPF_L7_TRACES_ENABLED=false` |
| `DISABLE_LOG_PARSING=true` | No effect beyond the new default (logs off). An explicit new log setting wins | `CONTAINER_LOG_COLLECTION_ENABLED=false` + `CONTAINER_LOG_PARSING_ENABLED=false` |
| `TRACES_SAMPLING` | Unchanged; applies when traces are enabled. With adaptive sampling it is the rate for spans that are neither errors nor slow | `TRACES_ADAPTIVE_SAMPLING_ENABLED=true` |
| `MIN_CONTAINER_AGE` | Unchanged; suppresses **metrics** of young containers. Their logs and traces are still exported (crash logs of short-lived job pods are kept for RCA) | `MIN_CONTAINER_AGE_APPLIES_TO=all` also drops their logs and eBPF traces |
| Container resource metrics, L7 metrics and traces, log collection and parsing, profiling all on by default | **Changed: all off by default.** Set each one to `true` to keep it | `CONTAINER_RESOURCE_METRICS_ENABLED`, `EBPF_L7_METRICS_ENABLED`, `EBPF_L7_TRACES_ENABLED`, `CONTAINER_LOG_COLLECTION_ENABLED`, `CONTAINER_LOG_PARSING_ENABLED`, `PROFILING_ENABLED` |
| Agent metrics gathered every 15 s | **Changed: every 20 s** | `SCRAPE_INTERVAL=15s` |

**Defaults change in this release: only node metrics are on, and `SCRAPE_INTERVAL` is `20s`.**
To keep the previous behaviour, set every category you use to `true` explicitly, for example:

```
CONTAINER_RESOURCE_METRICS_ENABLED=true  EBPF_L7_METRICS_ENABLED=true  EBPF_L7_TRACES_ENABLED=true
CONTAINER_LOG_COLLECTION_ENABLED=true    CONTAINER_LOG_PARSING_ENABLED=true  PROFILING_ENABLED=true
```

Pod-level CPU and memory come from the cluster agent (`codexray_pod_*`). Container discovery,
process attribution, `container_info`, restarts/OOM kills and TCP connection metrics stay on.

> **Before upgrading:** service request rates and latencies, traces, logs, profiles and
> `container_resources_*` stop unless their settings are set to `true`. Any dashboard built on
> them goes empty. That includes the CodeXray UI's node disk panels, which are built from
> `container_resources_disk_*`: set `CONTAINER_RESOURCE_METRICS_ENABLED=true` until those panels
> read node-level data.

Boolean flags also accept `--flag=false` / `--flag=true` (rewritten to `--no-flag` / `--flag`
before parsing).

## Cardinality and guard rails

| Setting | Default | Purpose |
|---|---|---|
| `L7_ACTUAL_DESTINATION` | `keep` | `keep` labels network/L7 series with the destination and the actual backend (pod IP). Keep it: the CodeXray dependency graph and network connectivity are built from these IPs. `drop` aggregates by destination only |
| `L7_LATENCY_BUCKETS` | empty (Prometheus default, 11 bounds) | Comma-separated bounds for L7 and DNS latency histograms, e.g. `0.01,0.05,0.1,0.5,1,5` (12 → 7 series per histogram). Invalid lists stop the agent at startup |
| `DNS_MAX_DOMAINS_PER_CONTAINER` | `500` | Distinct DNS domains per container; extra domains are counted as `domain="overflow"` (one warning per container) |
| `MIN_CONTAINER_AGE_APPLIES_TO` | `metrics` | `all` also drops logs and eBPF traces of containers younger than `MIN_CONTAINER_AGE` |
| `EPHEMERAL_PORT_RANGE` | `32768-60999` | Ports in this range are not tracked. A range starting below 10000 logs a warning, because connections to services on those ports become invisible (`1-65535` removes all network, L7 and trace data) |
| `CGROUPFS_ROOT` | `/sys/fs/cgroup` | A path that is not a cgroup filesystem now logs an error at startup (previously container resource metrics silently went missing) |

## Memory safeguards and diagnostics

| Setting | Default | Purpose |
|---|---|---|
| `LOG_PATTERNS_MAX` | `512` | Distinct warning/error patterns tracked per log source; extra patterns are counted under `pattern_hash="overflow"`. `0` = unlimited (previous behavior) |
| `PROFILING_SYMBOL_CACHE_SIZE` | `256` | Entries in each profiler symbol cache (unchanged) |
| `PROFILING_SYMBOL_CACHE_KEEP_ROUNDS` | `8` | Rounds (minutes) an unused symbol table stays cached (unchanged) |
| `PROFILING_MAX_MINIDEBUGINFO_SIZE` | `32MB` | Max decompressed `.gnu_debugdata` size |
| `PROFILING_SYMBOL_NAME_CACHE_ENTRIES` | `16384` | Max cached symbol names per ELF file |
| `PROFILING_MAX_PERFMAP_SIZE` | `64MB` | Skip larger JIT perf maps (JIT frames unresolved) |
| `PROFILING_JVM_PERFMAP_DUMP_INTERVAL` | `1m` | How often JVMs are asked to rewrite their perf map |
| `GO_MEMORY_LIMIT_RATIO` | `0` (off) | Go soft memory limit as a fraction of the container limit; ignored if `GOMEMLIMIT` is set |
| `MEMORY_WATCHDOG_THRESHOLD` / `_RATIO` | `0` (off) | Write a diagnostic bundle at a cgroup-usage threshold |
| `MEMORY_WATCHDOG_MIN_INTERVAL` | `15m` | Rate limit for bundles |
| `MEMORY_WATCHDOG_MAX_DUMPS` | `3` | Bundles kept on disk |
| `MEMORY_WATCHDOG_DIR` | `<wal-dir>/diagnostics` | Bundle location (local disk only) |
| `PPROF_LISTEN` | `127.0.0.1:10302` | pprof listener; empty disables it. Previously pprof ran on the metrics listener |
| `PPROF_AUTH_TOKEN` | empty | Required for a non-loopback pprof listener |
| `NODE_FS_EXCLUDE_TYPES` / `NODE_FS_EXCLUDE_MOUNT_POINTS` | see `node/filesystem.go` | Filesystem exclusions (regex) |
| `NODE_FS_MAX_MOUNTS` | `64` | Filesystem series cap |

## New self-observability series (bounded labels only)

- `go_*`: GC, memory classes, scheduler, goroutines
- `process_*`: RSS, FDs, CPU
- `node_agent_cgroup_memory_usage_bytes`, `node_agent_cgroup_memory_limit_bytes`,
  `node_agent_cgroup_memory_stat_bytes{type}`
- `node_agent_containers_active`, `node_agent_processes_active`, `node_agent_uprobes_active`,
  `node_agent_uprobes_closed_after_owner_exit_total`, `node_agent_log_parsers_active`
- `node_agent_ebpf_event_queue_length` / `_capacity`, `node_agent_ebpf_perf_lost_samples_total{map}`
- `node_agent_dropped_total{component,reason}`, `node_agent_export_failures_total{signal}`,
  `node_agent_metrics_spool_bytes`
- `node_agent_profiling_*`: collect duration, profiles by result, errors by stage, targets,
  session state, perf-map counters
- `node_agent_feature_enabled{feature}`, `node_agent_memory_watchdog_dumps_total{result}`

Together these add about 93 exposition series per agent, histogram buckets included (measured in `TestRuntimeDiagnosticsExposed`). Go GC size-class and scheduler histograms are deliberately not enabled.
