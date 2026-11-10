# Local performance comparison (baseline vs this change)

**Scope:** Docker Desktop VM, 9 containers, synthetic load (HTTP client/server, log
generator, container churn), one 4-minute run per configuration, 15 s scrape interval.
Telemetry was pushed to a local counting sink. This is a *relative* comparison between builds
and flags. It is not a production measurement. It also does not reproduce or disprove the
OOM, because uprobes could not attach in the VM (virtiofs EIO). See
[SOAK-TEST.md](SOAK-TEST.md).

Harness: `bench-baseline/run.sh`, `matrix.sh`, `summarize.py` (outside the repo).
RSS = average of the last three samples. HWM = peak RSS. CPU = utime+stime over the run.
KB columns = bytes received by the sink.

| Run | Build | Settings | Series | Scrape KB | RSS MB | HWM MB | CPU % | Metrics KB | Traces KB | Logs KB | Profiles KB |
|---|---|---|---|---|---|---|---|---|---|---|---|
| A | baseline b136a7d | defaults | 245 | 42 | 72 | 145 | 3.0 | 135 | 39 | 68 | 90 |
| **B2** | **final (76b1fc3)** | **defaults** | **380** | **68** | **85** | **121** | **3.5** | **261** | **39** | **70** | **107** |
| B | interim¹ | defaults | 488 | 85 | 105 | 142 | 2.9 | 321 | 37 | 70 | 66 |
| C | interim¹ | `CONTAINER_RESOURCE_METRICS_ENABLED=false` | 382 | 67 | 75 | 144 | 3.1 | 281 | 37 | 70 | 88 |
| D | interim¹ | `PROFILING_ENABLED=false` | 486 | 84 | 75 | 145 | 2.3 | 318 | 32 | 68 | 0 |
| E | interim¹ | logs off (collection + parsing) | 476 | 82 | 74 | 113 | 3.1 | 312 | 29 | 0 | 91 |
| F | interim¹ | `EBPF_L7_TRACES_ENABLED=false` | 482 | 84 | 80 | 127 | 3.4 | 323 | 0 | 68 | 113 |

¹ Interim build with the full Go runtime histogram set (about 108 more series than the
final build). Compare C–F with B, not with B2.

## Reading

- **Series, final vs baseline (+135):**
  - self-observability: `node_agent_*` 35, `go_*` 49, `process_*` 9 (93 total)
  - `node_filesystem_*`: 36 (6 mounts in the VM)
  - `node_load_average`: 3

  Container and node series are unchanged. On a real node, the filesystem count depends on
  mounts (capped by `NODE_FS_MAX_MOUNTS`) and self-observability stays roughly constant. The
  container-series savings below scale with the number of containers.
- **`CONTAINER_RESOURCE_METRICS_ENABLED=false`** removed 106 series (B → C), which is 11–12
  per container here, and about 13 % of metrics bytes.
- **Profiling off** lowered CPU from 2.9 % to 2.3 % and stopped profile upload. **Logs off**
  lowered peak RSS from 142 MB to 113 MB. **Traces off** stopped span export with no effect on
  L7 metric series (482 vs 488).
- **Memory, final vs baseline:** RSS was 85 vs 72 MB, while peak (HWM) was 121 vs 145 MB.
  With a single 4-minute run per build, RSS differences of ±15 MB are within noise.
  **No memory saving is claimed from this benchmark.** The OOM-related fixes target growth
  over hours (perf-map re-parsing, per-process ELF parsing, cache and pattern growth), and
  4 minutes cannot show that. Use the canary and the soak criteria.
- Micro-benchmark evidence for R-2: `BenchmarkGoTlsBinaryInfo` (130 MB Go binary) measured
  89 MB allocated / 16 ms per process uncached, versus 208 B with the new cache hit
  (RCA §2).
