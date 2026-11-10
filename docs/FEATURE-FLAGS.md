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
| `--container-resource-metrics-enabled` / `CONTAINER_RESOURCE_METRICS_ENABLED` | `true` | `container_resources_*`: CPU usage/limit/throttling/delay, memory RSS/cache/limit, PSI, disk size/usage/IO, GPU. Also skips the cgroup and taskstats reads behind them | Container discovery, process attribution, `container_info`, `container_restarts_total`, `container_oom_kills_total`, network, L7, logs, traces, profiling, JVM/.NET/Python/Node.js runtime metrics |
| `--ebpf-l7-metrics-enabled` / `EBPF_L7_METRICS_ENABLED` | `!DISABLE_L7_TRACING` | `container_<protocol>_requests_total` / `_duration_*` RED metrics | Traces |
| `--ebpf-l7-traces-enabled` / `EBPF_L7_TRACES_ENABLED` | `!DISABLE_L7_TRACING` | L7 spans (sampled with `TRACES_SAMPLING`). Off: tracer not initialized, no span allocation | L7 metrics. The L7 eBPF programs and TLS uprobes stay loaded while either L7 setting is on |
| `--container-log-collection-enabled` / `CONTAINER_LOG_COLLECTION_ENABLED` | `!DISABLE_LOG_PARSING` | Tailing and OTLP export of container/journald logs | Tracing, metrics |
| `--container-log-parsing-enabled` / `CONTAINER_LOG_PARSING_ENABLED` | `!DISABLE_LOG_PARSING` | Pattern extraction and `container_log_messages_total` | Log export |
| `--profiling-enabled` / `PROFILING_ENABLED` | `true` | eBPF CPU profiler (session, symbolization, JVM perf-map dumps, process handoff). Also requires a profiles endpoint | L7 tracing, dependency detection, TLS uprobes |

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

## Compatibility with existing settings

| Existing setting | Behavior after upgrade | New equivalent |
|---|---|---|
| `DISABLE_L7_TRACING=true` | Unchanged: no L7 metrics, no traces, no L7 probes, **unless** a new L7 setting is set explicitly, which then wins for that category | `EBPF_L7_METRICS_ENABLED=false` + `EBPF_L7_TRACES_ENABLED=false` |
| `DISABLE_LOG_PARSING=true` | Unchanged: no log pipeline at all, unless a new log setting is set explicitly | `CONTAINER_LOG_COLLECTION_ENABLED=false` + `CONTAINER_LOG_PARSING_ENABLED=false` |
| `TRACES_SAMPLING` | Unchanged; applies when traces are enabled | n/a |
| `MIN_CONTAINER_AGE` | Unchanged; suppresses series of young containers | n/a (cluster-agent lifecycle records do not depend on it) |
| Profiling active whenever a profiles endpoint exists (derived from `--collector-endpoint`) | Unchanged (`PROFILING_ENABLED` defaults to `true`) | `PROFILING_ENABLED=false` |

**No defaults change in this release.** Upgrading does not disable any telemetry. The
recommended Kubernetes defaults for **new** installs are shown under "Recommended" in
[DEPLOYMENT.md](DEPLOYMENT.md).

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
