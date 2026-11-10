# Deployment, canary and rollback (node agent + cluster agent)

Nothing here deploys automatically. Every step is run by an operator and can be reverted.

## 0. Before you start

1. Record the current state so you can roll back:
   ```bash
   kubectl -n codexray get ds node-agent -o yaml > node-agent.before.yaml
   kubectl -n codexray get deploy cluster-agent -o yaml > cluster-agent.before.yaml   # if applicable
   kubectl -n codexray get ds node-agent -o jsonpath='{.spec.template.spec.containers[0].image}{"\n"}'
   ```
2. Check the build flags of the currently running debug image (RCA H-7):
   `kubectl -n codexray exec <pod> -- go version -m /usr/bin/codexray-node-agent`. If the
   container has no `go` binary, `kubectl cp` the binary out and run `go version -m` locally.
3. **Move the API key into a Secret** (if it is passed as `--api-key`):
   ```bash
   kubectl -n codexray create secret generic codexray-agent --from-literal=api-key="$API_KEY"
   ```
   Then use `env: API_KEY` with `secretKeyRef`, as in `manifests/codexray-node-agent.yaml`.
   The old agent served `/debug/pprof/cmdline` on port 10300. If that port was reachable and
   the key was an argument, **rotate the key**.
4. Build and push the new images from the release commits, using the regular images rather
   than the `-debug` variants.

## 1. Canary: one failing worker

The node agent is a DaemonSet. Use `OnDelete` so a template change does not roll every node:

```bash
# Freeze automatic rollout
kubectl -n codexray patch ds node-agent -p '{"spec":{"updateStrategy":{"type":"OnDelete"}}}'

# Update the template (image + env); existing pods keep running the old version
kubectl -n codexray set image ds/node-agent node-agent=ghcr.io/codifinary/codexray-node-agent:<new-tag>
kubectl -n codexray set env ds/node-agent \
  GO_MEMORY_LIMIT_RATIO=0.7 \
  MEMORY_WATCHDOG_THRESHOLD_RATIO=0.6 \
  PPROF_LISTEN=127.0.0.1:10302

# Replace only the pod on the canary worker (pick a node with many restarts, e.g. worker-7)
POD=$(kubectl -n codexray get pod -l app=node-agent --field-selector spec.nodeName=worker-7 -o name)
kubectl -n codexray delete "$POD"
```

Observe for **at least 24 h** (the failing nodes restarted every few minutes to hours). Use
`scripts/soak/monitor.sh` and the acceptance criteria in [SOAK-TEST.md](SOAK-TEST.md):
A1 (no restarts), A2 (cgroup peak), A4 and A5.

Useful queries (ClickHouse/PromQL equivalents):

- `node_agent_cgroup_memory_usage_bytes` versus `process_resident_memory_bytes`. A large gap
  means non-RSS memory (RCA H-9); check `node_agent_cgroup_memory_stat_bytes{type}`.
- `node_agent_profiling_perfmap_parsed_bytes_total`, `node_agent_profiling_collect_duration_seconds`
- `node_agent_processes_active`, `node_agent_uprobes_active`, `node_agent_log_parsers_active`
- `node_agent_dropped_total`, `node_agent_export_failures_total`

If the canary still grows, diagnostic bundles appear in `/data/diagnostics` (`kubectl cp`).
The A/B in RCA §3.5 (`PROFILING_ENABLED=false` on the canary only) tells you whether profiling
is the driver.

## 2. Widen

1. Delete the pods on two more failing workers, then on one stable worker. Observe 24 h.
2. Restore automatic rollout for the rest:
   ```bash
   kubectl -n codexray patch ds node-agent -p '{"spec":{"updateStrategy":{"type":"RollingUpdate","rollingUpdate":{"maxUnavailable":1}}}}'
   ```
   The remaining pods now update one at a time.

## 3. Rollback (any time)

```bash
# Freeze, restore the previous template, then replace affected pods
kubectl -n codexray patch ds node-agent -p '{"spec":{"updateStrategy":{"type":"OnDelete"}}}'
kubectl -n codexray rollout undo ds/node-agent        # or: kubectl apply -f node-agent.before.yaml
kubectl -n codexray delete pod -l app=node-agent --field-selector spec.nodeName=worker-7
```

The new agent's on-disk state is limited to the metrics spool under `--wal-dir` (same format
as before) and `diagnostics/`. Neither prevents a rollback, and both can be deleted.

New settings are only read by the new version, and the old version ignores unknown
environment variables. Rolling back the image is therefore enough. Revert `PPROF_LISTEN`
consumers (port-forward targets) if you changed them.

## 4. Cluster agent

1. Apply the RBAC additions first. These are read-only: `batch`, `autoscaling`, `policy`,
   `networking.k8s.io`, `discovery.k8s.io`, `storage.k8s.io`, `nodes/stats`,
   `nodes/metrics`, `nonResourceURLs: /metrics`. Use
   `codexray-cluster-agent/deploy/cluster-agent.yaml` or merge them into your existing
   ClusterRole.
2. Roll out the new image. It is a single Deployment, so `kubectl rollout undo` reverts it.
3. New behavior is on by default:
   - **Pod lifecycle records:** `POD_LIFECYCLE_EVENTS_ENABLED=true`, OTLP logs with
     `service.name=KubernetesPodLifecycle`.
   - **Pod CPU/memory:** `POD_RESOURCE_METRICS_ENABLED=true`, Summary API through the API
     server proxy every 30 s.
   - **HPA/PDB/Ingress/EndpointSlice** state metrics.

   Kubelet and control-plane scraping stays **off** until enabled: `KUBELET_METRICS_ENABLED`,
   `CONTROL_PLANE_METRICS_ENABLED`.
4. Least-privilege variant: set `KUBELET_ACCESS_MODE=direct` and drop `nodes/proxy`. This
   needs network access from the cluster-agent pod to every node on :10250, plus
   `KUBELET_CA_FILE` (or `KUBELET_INSECURE_SKIP_VERIFY=true` for self-signed kubelet certs).
5. Managed control planes (EKS/GKE/AKS) do not expose scheduler, controller-manager or etcd
   pods. Those jobs then find no targets. API server metrics and the API server's view of etcd
   latency (`etcd_request_duration_seconds`) still work where `/metrics` is authorized.

## 5. Recommended settings for new Kubernetes installs

These are the target defaults once pod-level coverage is verified (§4). They are **not**
code defaults; upgrades keep everything on.

| Setting | Value | Why |
|---|---|---|
| `CONTAINER_RESOURCE_METRICS_ENABLED` | `false` | Pod-level CPU/memory comes from the cluster agent; removes the `container_resources_*` series |
| `TRACES_SAMPLING` | workload-dependent (e.g. `0.1`) | RED metrics are unaffected by sampling |
| `GO_MEMORY_LIMIT_RATIO` | `0.7` | Returns burst memory to the OS sooner |
| `MEMORY_WATCHDOG_THRESHOLD_RATIO` | `0.8` | Captures the owner of any future spike |
| `PROFILING_MAX_PERFMAP_SIZE` | `64MB` (default) | Bounds JVM perf-map parsing |
| `KSM_LABELS_ALLOWLIST` (cluster agent) | e.g. `pods=[app,app.kubernetes.io/name,team]` | Caps label cardinality |

## 6. Kubernetes compatibility

| Feature | Minimum Kubernetes | Notes |
|---|---|---|
| HPA metrics | 1.23 (autoscaling/v2) | |
| PDB metrics | 1.21 (policy/v1) | |
| EndpointSlice metrics | 1.21 (discovery/v1) | |
| Ingress metrics | 1.19 (networking/v1) | |
| Restartable init containers (sidecars) in effective requests | 1.28+ (feature on by default from 1.29) | On older clusters the field is absent and the classic init rule applies |
| Pod-level `spec.resources` | 1.32+ (alpha/beta) | Used when present |
| `DisruptionTarget` condition (eviction time) | 1.26+ | Older clusters: eviction records carry only the observed time |
| Kubelet Summary `only_cpu_and_memory` | all supported versions | |
| Kubelet metric names | vary by version | Keep-list entries absent on a version are simply not present |

## 7. Security checklist

- [ ] API keys come from Secrets and are never passed as arguments; keys exposed via the old pprof are rotated.
- [ ] pprof listens on loopback only (default). If it must be remote, it requires `PPROF_AUTH_TOKEN` (from a Secret).
- [ ] The metrics listener is loopback unless something scrapes it; hostPort 10300 is not needed for push.
- [ ] Watchdog bundles stay on the node's data volume. They contain profiles and memory stats only, with no arguments, environment or payloads.
- [ ] Cluster-agent RBAC is read-only, with no cluster-admin. `nodes/proxy` is only used in proxy mode.
- [ ] etcd client certificates, if used, are mounted from a Secret (`ETCD_*_FILE`).
