# Node Agent OOM kills: root cause analysis

Status (2026-11-10): **a primary root cause was confirmed on staging (R-0, ClickHouse L7
parser) and fixed in code.** The fix is not yet verified in a running cluster. The other
causes below remain valid contributors. Do not treat the OOMs as fixed until the canary
and soak criteria in [SOAK-TEST.md](SOAK-TEST.md) pass.

## 0. Staging evidence (v1.4.1, 2026-11-10)

Cluster: kubeadm v1.30.14, 1 control-plane + 3 workers, flannel, node agent limit 5Gi.

| Node | ClickHouse L7 series | Agent restarts (4d21h) | Last termination |
|---|---|---|---|
| worker 1 | 92 | 56 | OOMKilled (137) |
| worker 2 | 30 | 68 | OOMKilled (137) |
| worker 3 | 90 | 108 | OOMKilled (137) |
| master | 0 | 0 | n/a |

- Heap profile of the worker 1 agent (RSS 1.1 GiB at the time): **87 % of in-use heap,
  217 MB, under `l7.ParseClickhouse` -> `ch-go proto.(*Setting).Decode` -> `Reader.Str` ->
  `Buffer.Ensure`**, reached from `Registry.handleEvents` -> `Container.onL7Request`.
- Series per node were 804-2,293. Cardinality is not the driver.

### R-0 ClickHouse L7 parser allocates attacker/garbage-controlled lengths (confirmed; fixed)

- `ebpftracer/l7/clickhouse.go` decoded eBPF-captured TCP payloads with ch-go's
  `proto.Reader`. `Reader.StrRaw` calls `Buffer.Ensure(n)`, which allocates `n` bytes **before**
  reading, where `n` is a uvarint taken from the packet. Any packet on a ClickHouse
  connection that is misread as a Query (pipelined data, a mid-stream capture, a corrupted
  payload) can declare a length up to 2^63 and trigger a single multi-GB allocation. This
  matches sudden OOM kills from a low steady-state heap (~40 MB live on worker 3).
- **Fixed** (commit `fix(l7): bounds-checked ClickHouse query parser`): a zero-copy decoder
  walks the same wire format and validation, and rejects any length beyond the captured
  bytes. Existing test vectors pass. A regression test asserts at most 1 allocation for
  4 GiB declared lengths. A fuzz target ran 4.2M inputs clean.
- **Verify:** canary on one worker; `node_agent_cgroup_memory_usage_bytes` stays flat, and
  no OOMKilled for 24 h while ClickHouse L7 series keep updating.

## 1. What the incident data says

| Observation | Implication |
|---|---|
| 7 of N agents restart hundreds of times on the same image; others are stable | Node-specific workload drives memory, not a fixed baseline or config difference |
| ~1,228 series, 363 KB per scrape, ~31 containers | Metric cardinality is not the driver (see §4 on the 363 KB) |
| Live Go heap after GC ≈ 21 MB, RSS ≈ 685 MiB (anon 610 MiB) | The process is not *retaining* much. RSS reflects **transient peaks** that the Go runtime has not yet returned to the OS, or non-Go memory |
| RSS swings repeatedly in a 10 s series | Periodic allocation bursts. The profiler's collection round is 60 s; scrape is 15 s |
| RSS high-water mark 1.4 GiB vs a 5 GiB limit | The OOM killer acts on **cgroup** usage. Either bursts above ~5 GiB happen between 10 s samples (VmHWM only covers the current process lifetime), or memory not counted in RSS is charged to the cgroup (kernel memory, tmpfs/emptyDir `medium: Memory`, page cache under pressure) |
| Heap profile on a stable agent: `profiling.collect`, `CollectProfiles`, ELF symbol loading, `NewMiniDebugInfoSymbolTable`, `bytes.growSlice` | Profiler symbolization is a major allocator even on stable nodes |
| ~35 goroutines in each of three log-parser groups | Matches 3 goroutines per active parser; no evidence of a parser leak *at that moment* |
| Running image is the **debug** variant of v1.2.2; this repository is v1.3.1 | Build flags of the debug image are unknown (see H-7) |

## 2. Ranked causes

Legend: **Confirmed defect** = proven by code inspection and a test or benchmark in this
repository. **Hypothesis** = plausible mechanism that production data has not yet confirmed.

### R-1 Profiler re-parses JIT perf maps every minute (confirmed defect; prime suspect for node-specific bursts)

- `profiling/profiling.go` `TargetFinder.FindTarget` asked every JVM to rewrite its perf map
  (`jvm.DumpPerfmap`, jcmd `Compiler.perfmap`) **every round**.
- On every round, for every profiled PID, `internal/pyroscope-ebpf/symtab/proc.go`
  `ProcTable.Refresh` → `perfmap.go` `NewPerfMap` parsed the **entire**
  `/tmp/perf-<pid>.map` into Go strings. The previous table stayed live until the
  assignment, so two copies coexisted.
- Large JIT'd applications produce maps of tens to hundreds of MB. Heap demand per round is
  roughly `Σ JVMs × (2–3 × map size)`, which is periodic (60 s), node-specific (Java-heavy
  nodes) and invisible in a post-GC heap profile taken between rounds.
- **Fixed** (commit `fix(profiling): bound JIT perf-map parsing`): unchanged maps are reused,
  maps above `PROFILING_MAX_PERFMAP_SIZE` (64 MB) are skipped, the slice is pre-sized, the old
  table is released before parsing, and `PROFILING_JVM_PERFMAP_DUMP_INTERVAL` is configurable.
- **Evidence still needed:** on a failing node, `ls -la /proc/*/root/tmp/perf-*.map`, the count
  of JVMs, and after upgrade `node_agent_profiling_perfmap_parsed_bytes_total`.

### R-2 Go TLS uprobe attach parses the full ELF symbol table per process (confirmed defect, measured)

- `ebpftracer/tls.go` `AttachGoTlsUprobes` ran `elf.Symbols()` (the whole `.symtab`) for
  **every new process** of a Go binary, synchronously on the registry event loop.
- `BenchmarkGoTlsBinaryInfo` against a 130 MB unstripped Go binary measured
  **89 MB allocated, 16 ms CPU per process**. Process churn of a large Go binary therefore
  causes repeated ~100 MB heap bursts and stalls the event loop.
- **Fixed** (`perf(ebpftracer): cache Go TLS / OpenSSL binary analysis`): results are cached by
  file identity. Each later process costs ~200 B / 1 µs.

### R-3 Profiler symbol tables: unbounded name cache and MiniDebugInfo growth (confirmed defect)

- `symtab/elf/symbol_table.go` `NewMiniDebugInfoSymbolTable` decompressed `.gnu_debugdata`
  into an unsized `bytes.Buffer` (repeated doubling: the `bytes.growSlice` in the profile) and
  **retained the decompressed image** for the life of the cached table.
- `symtab/elf/elfinmem.go` `getString` cached every resolved, demangled symbol name per
  file with **no bound**.
- Each cache (per-PID, build-ID, same-file) holds up to 256 entries for 8 rounds. Measured on
  a 130 MB Go binary: 12 MB allocated and 2 MB retained per symbol-table load. Cost scales with
  the number of **distinct binaries/libraries** on the node.
- **Fixed** (`fix(profiling): bound symbolization memory`): decompression is pre-sized and
  capped (`PROFILING_MAX_MINIDEBUGINFO_SIZE`, 32 MB), the name cache is bounded
  (`PROFILING_SYMBOL_NAME_CACHE_ENTRIES`, 16384), and cache size and keep-rounds are
  configurable (defaults unchanged).

### R-4 Per-container log pattern table is unbounded (confirmed defect; slow growth)

- `logparser.Parser.inc` added every new warning/error pattern for the life of the container.
  It kept a sample message of up to 64 KiB and matched each message against all patterns
  linearly.
- Each pattern is also exported as a `container_log_messages_total` series whose `sample`
  label can be 4 KiB. A few hundred such series can account for much of a 363 KB scrape.
- **Fixed** (`fix(logs): ... bounded per-source log pattern table`): `LOG_PATTERNS_MAX` (512 per
  source) caps the table, with overflow counted under `pattern_hash="overflow"`, and stored
  samples are truncated to `MAX_LABEL_LENGTH`.

### R-5 Profiling target map grows with process churn (confirmed defect)

- `TargetFinder.processes` received **every** process start. Entries were removed only by a
  perf-buffer "dead PID" event (dropped when its 1024-entry queue is full), and processes
  handed over after their exit had been processed were never removed.
- **Fixed**: reconciled against `/proc` every round, and the gauge is exported
  (`node_agent_profiling_targets`).

### R-6 Lifecycle races, leaks and hangs (confirmed defects; contributing risk)

| Defect | Effect | Fix |
|---|---|---|
| `Process.uprobes` appended by two goroutines with no lock; links attached after `Close` were never detached | Leaked FDs and kernel perf events (released only by GC finalizers) | Mutex plus "closed" ownership; test runs under `-race` |
| PID reuse in the same cgroup overwrote the `Process` without closing it | Orphaned uprobes and instrumentation goroutine | Old instance closed |
| logparser `dispatch` closed `Messages` while `Add` could be sending | **Panic** (`send on closed channel`), or a goroutine blocked forever holding the lock (parser leak) | Fork in `internal/logparser` |
| `TailReader.Stop` waited on a reader blocked sending to a stopped consumer | `Container.Close` on the registry event loop could hang forever, freezing eBPF processing and piling up scrape goroutines | Send selects on cancellation; test fails on old code |
| Journald follower could block forever on a removed subscriber | All journald logs stop | Per-subscriber done channel |
| Registry → profiler handoff was an unbuffered channel and the JVM perf-map dump ran under the `TargetFinder` lock | Event-loop stalls during profiling rounds | Buffered non-blocking handoff (drops counted); dump outside the lock |

The goroutine snapshot (191 goroutines, parser groups equal) shows these were not active at
that moment. They are real defects, but nothing shows they are the primary OOM cause.

### H-7 Debug image build (hypothesis; cheap to check first)

The pods run the `codexray-node-agent-debug` image. If it is built with `-race`, memory grows
5–10× and CPU 2–20×. Combined with a 500m CPU limit, GC and event processing would also be
throttled. Check: `kubectl exec <pod> -- go version -m /usr/bin/codexray-node-agent` (or copy
the binary out and run it locally). Look for `-race`, `-gcflags`, `-asan`, `-msan` and
`CGO_*`. **Do the canary with the regular image.**

### H-8 Go heap vs RSS gap (hypothesis; mitigated)

No `GOMEMLIMIT` was set, so after a burst the runtime returns memory gradually and RSS stays
near the peak. `GO_MEMORY_LIMIT_RATIO=0.7` (or `GOMEMLIMIT`) makes the GC and scavenger
work harder as the heap nears 70% of the container limit. This is a soft limit, not a fix.

### H-9 Non-RSS memory charged to the cgroup (hypothesis; now observable)

Candidates:
- WAL or spool on a memory-backed volume (up to 500 MB metrics spool, plus 250 MB StatsD spool
  if enabled). Check the production `emptyDir` for `medium: Memory`.
- Page cache from reading large binaries, perf maps and the journal (75 MB file-backed RSS was
  observed).
- Kernel memory (socket buffers, slab from `/proc` scanning).

`node_agent_cgroup_memory_usage_bytes` and `node_agent_cgroup_memory_stat_bytes{type}` now
attribute cgroup memory (anon/file/kernel/sock/shmem). Compare them with
`process_resident_memory_bytes`.

### Ruled out or low likelihood (with evidence)

- **eBPF maps:** all fixed-size (≤ 32K entries, mostly 10,240). Perf buffers total about
  224 KB per CPU (`ebpftracer/tracer.go`). Bounded and allocated once.
- **Metric cardinality:** 1,228 series.
- **OTLP queues:** SDK batch processors are bounded (2,048 records by default) and drop when
  full. Drops are now counted.
- **Metrics spool:** on disk, bounded by `MAX_SPOOL_SIZE`. Its size is now a metric.
- **Python profiler startup:** bounded at 4 attempts per PID.

## 3. How to confirm on a failing node (before or with the canary)

1. Image: check the build flags (H-7).
2. Workload inventory on the 7 nodes compared with stable ones:
   - JVM count and perf-map sizes: `ls -la /proc/*/root/tmp/perf-*.map`
   - Count of large Go binaries, process churn (exec probes, CronJobs)
   - Container churn
   - Log volume
3. Deploy the new build to **one** failing worker with
   `MEMORY_WATCHDOG_THRESHOLD_RATIO=0.6` and `GO_MEMORY_LIMIT_RATIO=0.7`. The watchdog writes
   heap, allocs and goroutine profiles at 60% of the limit to `<wal-dir>/diagnostics/`
   (`kubectl cp` them out). This captures the owner of the next spike.
4. Correlate `node_agent_cgroup_memory_usage_bytes` with
   `node_agent_profiling_collect_duration_seconds`,
   `node_agent_profiling_perfmap_parsed_bytes_total`, `node_agent_processes_active` and
   `node_agent_log_parsers_active`.
5. A/B on the same node: `PROFILING_ENABLED=false` for one hour. If the restarts stop,
   profiling (R-1/R-3) is confirmed as the driver.

## 4. Notes

- `container_log_messages_total` carries a sample message (up to 4 KB) as a label. This is
  preserved for compatibility, but it is the most likely reason a 1,228-series scrape is
  363 KB. Consider lowering `MAX_LABEL_LENGTH`.
- The previous pprof endpoint served `/debug/pprof/cmdline` on `0.0.0.0:10300`. Where the API
  key is passed with `--api-key`, it was readable over HTTP. **Rotate those keys** (see
  DEPLOYMENT.md).
