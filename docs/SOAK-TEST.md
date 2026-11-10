# Node Agent memory soak test

Unit tests prove individual mechanisms (bounded tables, no leaked goroutines or uprobes,
cancellable shutdown). They **do not** prove the OOMs are gone. This procedure does, and it
must pass before the fix is rolled out beyond the canary.

## Environment

- A test cluster, or a dedicated canary node, sized like the failing workers. Label it:
  `kubectl label node <node> codexray.io/soak=true`
- The agent under test runs the **regular** (non-debug) image with production limits:
  2 GiB request, 5 GiB limit, 500m CPU. Run it once with production defaults, and once with
  the recommended settings from DEPLOYMENT.md.
- Diagnostics enabled: `MEMORY_WATCHDOG_THRESHOLD_RATIO=0.6`. Keep `PPROF_LISTEN` on loopback.

## Load profile ([scripts/soak/workloads.yaml](../scripts/soak/workloads.yaml))

| Workload | Exercises |
|---|---|
| `process-churn` (3 pods, ~20 new processes/s each, plus 2 s exec probes) | Process start/exit handling, PID handoff to the profiler, registry cleanup |
| `short-lived` CronJob (10 × 3 s pods per minute, 25% fail) | Container create/delete, log parsers start/stop, short-lived pods below `MIN_CONTAINER_AGE`, cluster-agent lifecycle records |
| `busy-http` (Go HTTP + HTTPS loop, process restarted every 60 s) | L7 metrics and traces, Go TLS uprobe attach/detach |
| `log-flood` (~3,000 lines/s per pod, multiline traces, ever-new error patterns) | Tailers, multiline collector, bounded pattern table, log export |
| `profiled-runtimes` (JVM with many JIT'd methods, Python, Go) | Profiler symbolization, JVM perf maps, Python profiler |

Add the following phases in order (times are minimums):

1. **Baseline, 30 min:** no soak workloads.
2. **Load, 12 h:** apply `workloads.yaml`.
3. **Collector unavailability, 30 min (during load):** point the canary agent's
   `COLLECTOR_ENDPOINT` at an unroutable address (for example `http://10.255.255.1:8080`), or
   scale the test collector to 0. Then restore it.
4. **Temporary network failure, 10 min:** on a test cluster, drop egress from the node to the
   collector (`iptables -I OUTPUT -d <collector-ip> -j DROP` on the node), then remove the rule.
5. **Node pressure, 30 min:** run a memory/CPU hog on the node (for example `stress-ng --vm 2
   --vm-bytes 75%`) with requests set so the agent is not evicted.
6. **Cooldown, 1 h:** `kubectl delete ns cx-soak`.

## Measurement

```bash
POD=$(kubectl -n codexray get pod -l app=codexray-node-agent --field-selector spec.nodeName=<node> -o name | cut -d/ -f2)
scripts/soak/monitor.sh codexray "$POD" 10 > soak.csv      # leave running for the whole test
scripts/soak/summarize.sh soak.csv
```

The CSV records RSS, Go heap (in use, live objects, released), goroutines, open FDs, cgroup
memory usage, active containers, processes, uprobes, log parsers and profiling targets. It also
records event-queue length, spool bytes, series count, scrape bytes, dropped items and perf
lost samples.

Also record each phase's agent CPU (`rate(process_cpu_seconds_total[5m])`), the cluster
agent's lifecycle counter, and the collector-side ingestion bytes per signal.

If the watchdog fires, copy the bundle and analyze it:

```bash
kubectl -n codexray cp "$POD":/data/diagnostics ./diagnostics
go tool pprof -top -sample_index=inuse_space diagnostics/memdump-*/heap.pb.gz
go tool pprof -top -sample_index=alloc_space diagnostics/memdump-*/allocs.pb.gz
```

## Acceptance criteria

| # | Criterion | How to evaluate |
|---|---|---|
| A1 | No OOM kill and no restart for the whole test | `restarts` column constant; `kubectl get events --field-selector reason=OOMKilling` |
| A2 | Cgroup memory peak below 50% of the limit (2.5 GiB of 5 GiB) with production defaults | `node_agent_cgroup_memory_usage_bytes` peak |
| A3 | No unbounded growth: live heap and RSS hourly minima rise less than 2% per hour over the last 8 h of load | Hourly min of `go_memory_classes_heap_objects_bytes` and `process_resident_memory_bytes`; linear fit |
| A4 | Return to baseline: within 15 min of cooldown, goroutines, FDs, uprobes, log parsers, processes and profiling targets are within 10% (or ±5 absolute) of the phase-1 baseline | `summarize.sh` final vs baseline |
| A5 | Every memory spike has an instrumented owner | Spikes correlate with a `node_agent_*` series (profiling duration, perf-map bytes, processes, parsers) or a watchdog bundle |
| A6 | Losses are visible | During phases 3 and 4, `node_agent_export_failures_total` and `node_agent_dropped_total` increase. After restore, export resumes and the spool drains (`node_agent_metrics_spool_bytes` falls) |
| A7 | Disabling a feature reduces its cost | In separate 1 h runs, `PROFILING_ENABLED=false`, both log settings false, and `CONTAINER_RESOURCE_METRICS_ENABLED=false` each reduce CPU/RSS and the relevant ingestion bytes compared with the default run |
| A8 | No sustained eBPF loss under nominal load | `rate(node_agent_ebpf_perf_lost_samples_total[5m])` ≈ 0 outside the pressure phase |

Rollout beyond the canary (DEPLOYMENT.md, step 4) requires A1–A6 on the canary node and A1–A8
in the test cluster.

## What the local benchmark in this repository does *not* cover

The quick benchmark (see the test report) ran on a Docker Desktop VM: 9 containers, synthetic
load, 4 minutes per configuration. It cannot reproduce production process counts, JVM
perf-map sizes or node pressure. It also could not attach uprobes, because the VM's virtiofs
returns EIO for `perf_uprobe`. Use it only as a relative comparison between builds and flags.
