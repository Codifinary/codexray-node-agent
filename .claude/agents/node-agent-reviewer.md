# Node Agent Reviewer

Focus areas when reviewing Go changes for `codexray-node-agent` (the privileged eBPF
DaemonSet that feeds `codexray-mainv2`). Loaded by Subagent A of `/review-pr` as its
rubric. Every rule here is a condensed form of `CLAUDE.md` — CLAUDE.md wins on conflict.

## eBPF / kernel

- C changes in `ebpftracer/ebpf/` ship with a regenerated `ebpftracer/ebpf.go` in the
  same PR (`cd ebpftracer && make build`); `ebpf.go` is never hand-edited
- Go event structs (`procEvent`, `tcpEvent`, `fileEvent`, `l7Event`) match the C layout
  byte-for-byte — both sides change together
- New BPF code loads on every `__KERNEL_FROM` variant it is compiled into (kernels 4.16, 4.20,
  5.6, 5.12, 5.12 + `__CTX_EXTRA_PADDING`), amd64 + arm64; minimum kernel 4.16
- Optional probe attach failures warn and continue; uprobe links are closed in
  `Process.Close`

## Event loop / concurrency

- `Registry.containersBy*` maps are touched only from the `handleEvents` goroutine
- State shared between the event loop and `Collect` is guarded by `Container.lock` on
  both sides
- No **new** slow work (network, runtime API, `jattach`, sleeps) inline in `handleEvents`
  or a perf reader (existing upstream exceptions are listed in CLAUDE.md — pre-existing)
- Goroutines have an exit path; per-container/per-process goroutines stop with their owner
- `defer mu.Unlock()` immediately after `mu.Lock()`
- `setns` only under `runtime.LockOSThread`, via `proc.ExecuteInNetNs`; never call
  `updateStatsFromEbpfMapsIfNecessary` while holding a `Container.lock`

## /proc, cgroups, host

- `/proc/<pid>/…` reads tolerate process exit (`common.IsNotExist`) quietly
- Host files via `proc.HostPath` / `proc.Path(pid, "root", …)` — never a bare host path
- Files, netlink/netns handles and sockets closed on every path
- cgroup v1, v2 and hybrid all handled; no new host writes without a CHANGELOG entry

## Metrics

- Descriptors in `containers/metrics.go` / `node/collector.go`; `container_`/`node_`
  prefixes, `_total` counters, unit suffixes
- `MustNewConstMetric` label values match the `Desc` in count **and order**
- Every new label bounded; no pids, cmdlines, raw payloads or IDs as label values
- No rename/retype of an existing metric or label without a paired `codexray-mainv2` PR
- Per-container / per-destination state pruned on gc

## Exporters

- One HTTP client per exporter with an explicit timeout; no `http.DefaultClient`
- Bodies closed on every status; retries via `jpillora/backoff` with a cap
- New buffering is bounded; TLS only via `--insecure-skip-verify`; `X-Api-Key` from
  `common.AuthHeaders()`, never logged

## Security

- L7 / `/proc` data is attacker-controlled — bounds-checked, never panics
- No widening of captured payloads, environ, or cmdline export without redaction
- No `os/exec`, no process-controlled path without `filepath.Clean` + root check
- No hardcoded API keys/credentials in source, manifests, compose files, or CI

## Errors / logging

- klog only; `klog.Exit*` only at startup/config; runtime errors `Warning` + continue
- No `panic` on runtime paths; `%w` wrapping; no silent `_` except Close/Setns cleanup
- No per-event `Info`+ logging in hot paths (global rate limiter drops it)

## Config / build

- New flags: kingpin + `Envar`, safe default, CHANGELOG entry, manifests and
  `install.sh` whitelist if applicable
- Go version bumps move `go.mod`, Dockerfile `GO_VERSION` and CI together
- 3-line Codexray/coroot/SPDX header on first-party Go files
- `internal/prom/` and `internal/pyroscope-ebpf/` are vendored — changes only as an
  intentional, documented sync/patch

## Testing

- Tests beside the package, `testify`; fixture dirs for file-format readers, inline data
  for parsers and pure helpers
- Parsers tested with malformed + truncated input
- No root/BPF/runtime/network in unit tests — use the `VM` env gate for those

## Review-output discipline

These rules govern how the review is **written**, not what it finds. They exist
because past reviews have misled readers into rejecting mergeable PRs.

### Coverage claims

- This repo's `go test ./...` skips the nested vendored modules and the VM-gated
  `ebpftracer` tests, and `containers/`, `prom/`, `tracing/`, `profiling/` have no
  tests at all. A low or zero package number there is the baseline, not this PR's fault.
- Never present a low coverage number as a PR regression without evidence (a Δ vs
  baseline, or the test that disappeared). If you cite a number, cite the profile it
  came from in the same sentence.

### Severity counting

- One function with three rule violations is **one finding with three sub-bullets**,
  not three CRITICALs. The blast radius is the function, not the line count.
- "CRITICAL" is reserved for: a panic or event-loop stall on an always-on path (the node
  loses all telemetry), a BPF program that fails to load on a supported kernel,
  unbounded memory/fd/goroutine/uprobe growth under normal container churn, customer
  secrets/PII leaving the node, a new privilege or host write, or a silent break of a
  metric/label/endpoint contract `codexray-mainv2` consumes. Bumping a debatable issue
  to CRITICAL to pad the count is forbidden.

### Testability claims

- Before writing "untestable, refactor first", look for how the same package is already
  tested: fixtures + overridden package vars (`procRoot`, `cgRoot`), byte-slice inputs
  for parsers, the `VM` gate for BPF. If similar code is tested that way, ask for that
  test, not a refactor.
- "Untestable" requires a specific reason (e.g. the logic is fused with a live
  netlink/BPF call and takes no injectable input).

### Severity language

- For races: use "non-deterministic" or "undefined behavior". Do not write "will crash"
  unless the panic is deterministic for every call pattern.
- For `panic()` / index errors: distinguish "always-on path" (every event, every
  scrape) from "dormant helper with no current caller". The latter is a rule violation,
  not a runtime risk.
- For kernel issues: name the kernel variant(s) affected; "breaks on 4.16–5.5 only" is
  a different finding from "breaks everywhere".

### Pre-existing vs PR-introduced

- Tag anything that exists in `origin/${base}` with `(pre-existing, out of diff)`. If
  you can't tell, run `git log -L:funcname:file.go origin/${base}` before flagging.
- Upstream-coroot behavior this PR didn't touch is pre-existing even if it looks wrong.
- Do not include pre-existing vet/lint noise in the CRITICAL or WARNING counts — it
  belongs in a separate "out-of-diff" section.
