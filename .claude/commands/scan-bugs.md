---
description: Whole-codebase capability bug sweep → legacy bugs → assigned ClickUp tickets
---

# /scan-bugs — capability-wide legacy bug sweep

The broad, periodic audit. Sweep the **entire node-agent codebase** for pre-existing
(legacy) bugs, capability by capability, and file everything found. Not tied to a
PR or a branch. This is the widest-scope of the four commands.

| | |
|---|---|
| **Scope** | The whole `codexray-node-agent` first-party codebase — every capability, one at a time. Optionally narrowed to one capability via `$ARGUMENTS`. Vendored `internal/prom/` and `internal/pyroscope-ebpf/` are out of scope except where first-party code calls them wrongly. |
| **Reads (looks into)** | All first-party `.go` source and `ebpftracer/ebpf/*.c` in scope; the **ClickUp Bugs list** (the ledger); this repo's CLAUDE.md rules; the consumer contract in `codexray-mainv2` (ingest handlers + PromQL); the wire protocols and kernel file formats the code parses. **No PR, no branch diff, no spec doc.** |
| **Writes (acts into)** | ClickUp tickets tagged `node-agent` + `legacy` + capability, via `.claude/bug-ticketing.md`. **Never writes tests, never touches source.** |

**Arguments** (`$ARGUMENTS`, optional): a capability key to focus this run — one of
the keys in the STEP 1 table (`ebpf`, `l7`, `containers`, `runtimes`, `cgroup`,
`proc`, `node`, `metadata`, `logs`, `tracing`, `profiling`, `remote-write`, `flags`,
`jvm`, `dotnet`, `gpu`, `pinger`, `deploy`) or a cross-cutting concern
(`concurrency`, `leaks`, `untrusted-input`, `data-exposure`, `metrics-contract`).
**Omit to sweep everything.**

You run under **Plan mode** (`.claude/settings.local.json` →
`permissions.defaultMode: "plan"`). STEP 1–4 are read-only; you present the plan
via `ExitPlanMode` and only create tickets **after approval**.

**Ticketing is defined once, in `.claude/bug-ticketing.md` — read it before STEP 3
and follow it verbatim.** The ClickUp Bugs list is the ledger: it holds what has
already been filed, what is still open, and the next free bug ID. There is no local
bug file.

Repo: `Codifinary/codexray-node-agent` (Go, module
`github.com/codifinary/codexray-node-agent`). Sibling for the intended contract:
`Codifinary/codexray-mainv2` (collection-service ingest + query-service PromQL). Use
`gh` for the sibling — do **not** clone it.

---

## Core principles

1. **Contract-driven, not code-mirroring.** A bug is where the code diverges from the
   *intended* contract — the wire protocol a parser decodes, the kernel file format a
   reader parses, the metric/label/attribute mainv2 consumes, CLAUDE.md rules (never
   crash on untrusted input, tolerate process exit, bounded labels and buffers) — never
   from what the code happens to emit.
2. **No diff to lean on.** Unlike `/feature-bugs`, this run has no changed-file
   signal — give every flow equal attention. Legacy bugs hide in the upstream code
   nobody here has touched since the import.
3. **Upstream behavior is not automatically correct.** Much of this code came from
   `coroot/coroot-node-agent`. "Upstream does it too" is context, not a verdict; if it
   breaks the contract on a CodexRay node, it's a bug here.
4. **Verify the ledger, don't trust it.** Every open `NA-` ticket in scope is
   re-checked against current code this run. A ticket's text is a claim, not
   evidence. The finding is **reported in chat** — never written back to the ticket.
5. **Never file a bug that already has a ticket.** Dedup against the full ClickUp
   list including closed tickets, per `.claude/bug-ticketing.md` STEP 2.
6. **Create tickets, mutate nothing.** No status change, no comment, no edit on any
   existing ticket — ticket state is the assignees' to manage.

## STEP 1 — Pick the capability set and map its flows (read-only)

| Key | Code | Flow to trace |
|---|---|---|
| `ebpf` | `ebpftracer/ebpf/*.c`, `ebpftracer/tracer.go`, `init.go` | variant selection → load → attach → perf readers → decode → `Registry.events` |
| `l7` | `ebpftracer/l7/*`, `containers/l7.go`, L7 paths in `containers/container.go` | `l7Event` → protocol parser → `L7Stats` metrics + span |
| `containers` | `containers/registry.go`, `container.go`, `process.go` | event → `getOrCreateContainer` → `calcId` → filters → register/unregister → gc |
| `runtimes` | `containers/{dockerd,containerd,crio,systemd,cilium}.go`, `internal/dockerclient/` | pid → runtime metadata → id/labels/log path/actual destination |
| `cgroup` | `cgroup/` | `/proc/<pid>/cgroup` → id → v1/v2 stat files → container_resources_* |
| `proc` | `proc/` | `/proc` helpers, fds, sockets, netns, `HostPath`, opt-out flags |
| `node` | `node/*.go` | `/proc/{stat,meminfo,diskstats,uptime}` + netlink → node_* |
| `metadata` | `node/metadata/` | DMI/hypervisor detection → provider IMDS → `node_cloud_info` |
| `logs` | `logs/`, log wiring in `containers/` | file/journald → tail → logparser → `container_log_messages_total` + OTLP |
| `tracing` | `tracing/` | L7 request → sampling → span attributes → OTLP export |
| `profiling` | `profiling/` (+ calls into `internal/pyroscope-ebpf`) | `ProcessInfo` → `TargetFinder` → collect → pprof → upload |
| `remote-write` | `prom/` | gather → `buildWriteRequest` → spool → send/backoff → truncate |
| `flags` | `flags/`, `common/{container,net}.go` init, `main.go` | flag/env → derived endpoints/listen → filters built in `init()` |
| `jvm` / `dotnet` | `containers/jvm.go`, `jvm/`, `containers/dotnet.go` | hsperfdata / attach / diagnostics IPC → container_jvm_* / container_dotnet_* |
| `gpu` | `gpu/` (tag `gpu`), GPU paths in `containers/` | NVML → node_gpu_* + per-process samples → container_resources_gpu_* |
| `pinger` | `pinger/`, pinger call in `Container.Collect` | destinations → netns → ICMP → container_net_latency_seconds |
| `deploy` | `manifests/`, `Dockerfile`, `install.sh`, `.github/workflows/` | build → image → DaemonSet/systemd → flags actually applied |

```bash
cd "$(git rev-parse --show-toplevel)"
find . -path ./internal -prune -o -type f \( -name '*.go' -o -name '*.c' -o -name '*.h' \) -not -name '*_test.go' -not -name 'ebpf.go' -print | grep -iE '<capability path>' | sort
```
For the capability in hand, list every entry point, the goroutine it runs on (perf
reader, `handleEvents`, scrape/`Collect`, per-process goroutine, exporter loop), and
the metrics/spans/logs it produces. Then **trace every flow end-to-end** per the table,
including its error paths and its teardown (process exit, container gc, dependency
down). Note cross-capability seams: `Registry.handleEvents`, `Container.Collect`,
`common` filters, `flags` globals.

**Sweeping everything is large.** If one run can't finish all capabilities,
finish **whole** capabilities (never half of each) and state which remain so the next
run resumes. Never stop mid-capability and imply the codebase is clean.

## STEP 2 — Nail the intended contract per flow

Derive "correct" from these sources — never from the code's current behavior:
- **The consumer** (`codexray-mainv2`) — what it queries and ingests. Read it at a
  branch the user confirms (ask once per run; offer `main` and the release branch);
  never `gh search code`:
  ```bash
  gh api "repos/Codifinary/codexray-mainv2/contents/constructor/queries.go?ref=<BE_REF>" -q .content | base64 -d | grep -n '<metric>'
  gh api "repos/Codifinary/codexray-mainv2/contents/collector/collector.go?ref=<BE_REF>" -q .content | base64 -d | grep -n 'ApiKeyHeader\|getProjectContext'
  ```
- **Protocol / kernel specs** — the wire format a parser decodes (Postgres/MySQL/
  Mongo/Redis/HTTP2/…), the `/proc` and cgroup v1/v2 file formats, perf/BPF semantics.
- **CLAUDE.md rules** — event-loop ownership, `Container.lock`, no slow work in the
  loop, tolerate process exit, `HostPath`, closed handles, `MustNewConstMetric` label
  order, bounded labels/buffers, timed HTTP clients, capped backoff, no runtime panic,
  no widened data exposure.
- **Product intent** — what the signal is for on a CodexRay dashboard (a container's
  CPU, a service map edge, an error rate): a value that is technically emitted but
  semantically wrong (wrong unit, wrong container, double-counted) is a bug.

## STEP 3 — Load the ClickUp ledger, re-verify it, then sweep for bugs

Load the whole Bugs list first — **`.claude/bug-ticketing.md` STEP 1**, every page,
`include_closed=true`. That single pass gives you three things you need before you
find anything: what is already filed (dedup input), what is still open (re-verify
input), and the next free `NA-<capability>-NNN` id.

**Re-verify** every open `NA-` ticket (`to do` / `in progress`) belonging to a
capability you are sweeping this run: read the cited source and decide fixed or not.
Fixed → collect it for the chat report (`.claude/bug-ticketing.md` STEP 5) so the user
can close it themselves. **Do not change its status and do not comment on it.** Only
re-verify tickets inside the swept scope — you haven't read the rest of the code.

Then walk each flow and log a bug for every behavioral discrepancy:
- **Crash / stall** — panic on malformed payload or `/proc` content, nil deref,
  index OOB, `MustNewConstMetric` label mismatch, blocking send or slow call inside
  `handleEvents`/perf readers, lock held across a blocking call.
- **Races** — event-loop-owned maps touched elsewhere, `Container` fields written
  without `c.lock`, unsynchronized package globals.
- **Leaks** — goroutines, fds, netlink/netns handles, uprobe links, per-connection
  parser state, per-destination stats, spool files that are never cleaned up.
- **Wrong data** — wrong unit or counter-vs-gauge, double counting, wrong container
  attribution (pid reuse, cgroup v1/v2/hybrid mismatch), dropped series, label value
  that breaks the mainv2 query.
- **Exporter behavior** — no timeout, unbounded or infinite retry (including on 4xx),
  unbounded buffering, body not closed, TLS/insecure mismatch, API key exposure.
- **Data exposure** — payload/env/cmdline content leaving the node beyond the
  documented set, opt-out env vars not honored.
- **Config** — flag documented but not applied, derived endpoint/listen surprises,
  `install.sh`/manifests not passing a flag through.

Style is **not** a bug. Each bug captures **expected vs actual**, `file.go:line`,
severity (per `.claude/bug-ticketing.md`) + preconditions, and a concrete **reproduce**
block — environment (kernel, cgroup version, runtime, k8s/systemd), trigger (the
workload or event), and the observation that proves it (a `/metrics` grep on
`127.0.0.1:10300` or `:80`, an agent log line, a panic trace, a missing series in
mainv2). This is exactly the description template's field set.

**Dedup each candidate against the loaded ledger before it reaches the plan**
(`.claude/bug-ticketing.md` STEP 2, both tiers). A candidate matching a `complete`
or `cancelled` ticket is still a duplicate — do not resurrect it as a new bug.

## STEP 4 — Present the plan (`ExitPlanMode`)

Summarize: capabilities swept (and any deferred), new `legacy` bugs with their
allocated ids and blame assignees (flag `matched:false` and `upstream import 67c8d5f`
owners), candidates **skipped as duplicates** (with the ticket url each matched), and
open tickets whose bug now looks fixed. Then `ExitPlanMode`. Create nothing yet.

## STEP 5 — File the new tickets (after approval)

Run `.claude/bug-ticketing.md` STEP 3–4: blame owners → create each new ticket
named `[<severity>] NA-<capability>-NNN — <title>`, **created with `task_type="Bug"`**
→ tag it `node-agent` + `legacy` + the capability, assigned to its blame owners.
Creating the assigned ticket is the whole delivery; ClickUp notifies the assignees
itself.

**Creating is the only write.** Every open ticket you found already fixed goes in
the chat report per `.claude/bug-ticketing.md` STEP 5 — untouched in ClickUp, so the
user can close it themselves.

The created tickets are the ledger — there is nothing else to write down. No
tests, no source edits.

Report: capabilities covered, tickets filed (+assignees), duplicates skipped,
open tickets whose bug now looks fixed (with their urls, flagged as **not**
closed by this run), and any `matched:false` / upstream-import blame fallbacks a
human should re-route.
