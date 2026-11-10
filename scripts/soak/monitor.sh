#!/usr/bin/env bash
# Samples node-agent resource usage every INTERVAL seconds into a CSV.
#   scripts/soak/monitor.sh <namespace> <agent-pod> [interval] > soak.csv
# Requires the agent's metrics listener on 127.0.0.1:10300 inside the pod (default when a
# collector endpoint is set) and kubectl exec access. Read-only.
set -euo pipefail
NS=$1; POD=$2; INTERVAL=${3:-10}
METRICS='process_resident_memory_bytes|go_memstats_heap_inuse_bytes|go_memory_classes_heap_objects_bytes|go_memory_classes_heap_released_bytes|go_goroutines|process_open_fds|node_agent_cgroup_memory_usage_bytes|node_agent_containers_active|node_agent_processes_active|node_agent_uprobes_active|node_agent_log_parsers_active|node_agent_profiling_targets|node_agent_ebpf_event_queue_length|node_agent_metrics_spool_bytes'
echo "ts,restarts,$(echo $METRICS | tr '|' ','),series,scrape_bytes,dropped_total,perf_lost_total"
while true; do
  TS=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  RESTARTS=$(kubectl -n "$NS" get pod "$POD" -o jsonpath='{.status.containerStatuses[0].restartCount}')
  BODY=$(kubectl -n "$NS" exec "$POD" -- sh -c 'exec 3<>/dev/tcp/127.0.0.1/10300 2>/dev/null && printf "GET /metrics HTTP/1.0\r\n\r\n" >&3 && cat <&3' 2>/dev/null \
      || kubectl -n "$NS" exec "$POD" -- curl -s 127.0.0.1:10300/metrics)
  ROW="$TS,$RESTARTS"
  for m in $(echo $METRICS | tr '|' ' '); do
    V=$(echo "$BODY" | awk -v m="$m" '$1 ~ "^"m"({|$)" {s+=$NF} END {printf "%.0f", s}')
    ROW="$ROW,$V"
  done
  SERIES=$(echo "$BODY" | grep -vc '^#')
  BYTES=$(echo "$BODY" | wc -c | tr -d ' ')
  DROPPED=$(echo "$BODY" | awk '$1 ~ /^node_agent_dropped_total/ {s+=$NF} END {printf "%.0f", s}')
  LOST=$(echo "$BODY" | awk '$1 ~ /^node_agent_ebpf_perf_lost_samples_total/ {s+=$NF} END {printf "%.0f", s}')
  echo "$ROW,$SERIES,$BYTES,$DROPPED,$LOST"
  sleep "$INTERVAL"
done
