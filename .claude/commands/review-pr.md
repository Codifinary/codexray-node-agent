Run a fast multi-agent review of a PR on `Codifinary/codexray-node-agent`.

**Arguments** (`$ARGUMENTS`): `<pr-number> [backend-branch]`

- `<pr-number>` — required.
- `[backend-branch]` — **optional.** The branch in `Codifinary/codexray-mainv2` that
  consumes this PR's telemetry, used to cross-check the **outbound contract** of any
  changed signal: metric names/types/labels queried by `constructor/queries.go` and
  `constructor/containers.go`, and the ingest handlers in `collector/` (`/v1/metrics`,
  `/v1/traces`, `/v1/logs`, `/v1/profiles`, `X-API-Key`). The command runs fine without
  it — a PR that changes no metric, label, span/log attribute, endpoint or header never
  needs the backend repo. **But if, mid-review, a contract check genuinely needs the
  backend code and no branch was supplied, STOP and ask the user for it** (see 1e).
  Never `gh search code` and never assume `main`: GitHub code search indexes only the
  default branch, so a query living on a branch is invisible and the search surfaces a
  same-named query from `main` that reads as authoritative and is wrong — this has
  produced real false CRITICALs on the sibling repo.

You are the **orchestrator agent**. This command's only job is to **review the
PR**: gather the diff + surrounding context, spawn specialised subagents in
parallel, and post one consolidated review. It does **not** run tests, vet,
builds, BPF compilation, or any local CI — reviewing is the whole task, and the
subagents are where the value is. Keep it quick.

**GitHub access: use the `gh` CLI for every GitHub operation.** The `gh` CLI is
authenticated (OS keyring). Go straight to `gh`.

---

## STEP 1 — Gather PR context (all via `gh`, in parallel)

```bash
REPO=Codifinary/codexray-node-agent
BE_REPO=Codifinary/codexray-mainv2
# $ARGUMENTS = "<pr-number> [backend-branch]" — first token is the PR, optional
# second token is the mainv2 branch for contract cross-checks (see 1e).
PR=$(echo "$ARGUMENTS" | awk '{print $1}')
BE_REF=$(echo "$ARGUMENTS" | awk '{print $2}')   # empty if not supplied; may be set in 1e

# 1a. PR metadata: changed files, head SHA, base ref (do NOT assume main — this repo
#     also merges into `develop`, and stacked PRs differ)
gh pr view $PR --repo $REPO --json number,title,body,headRefName,headRefOid,baseRefName,files

# 1b. Full patch
gh pr diff $PR --repo $REPO --patch
```

1c. **Changed file contents at the PR head** — for every changed `.go`, `.c`/`.h`
(under `ebpftracer/ebpf/`), `Dockerfile`, `manifests/*.yaml`, `install.sh` and
workflow file (`headRefOid` from 1a as `<headSHA>`). **Skip the body of
`ebpftracer/ebpf.go`** — it is ~2 MB of generated base64; only note whether it
changed alongside `ebpftracer/ebpf/*.c`:
```bash
gh api "repos/$REPO/contents/<path>?ref=<headSHA>" -q .content | base64 -d
```
Changes under `internal/prom/` or `internal/pyroscope-ebpf/` are vendored upstream
code (`.gitattributes` marks them `linguist-vendored`): fetch them, but review them
only as "is this an intentional, documented sync/patch" — never line-by-line.

1d. **Shared context at the PR base ref** (`baseRefName` from 1a — not necessarily
`main`), so findings reason about the real surrounding code:
```bash
for f in CLAUDE.md .claude/agents/node-agent-reviewer.md .claude/agents/security-reviewer.md \
         main.go flags/flags.go containers/registry.go containers/metrics.go common/api.go; do
  gh api "repos/$REPO/contents/$f?ref=<baseRef>" -q .content | base64 -d
done
# plus the package-level files the change sits in (e.g. containers/container.go for a
# Collect change, ebpftracer/tracer.go for an attach change, prom/remote_writer.go for
# an exporter change)
```
`CLAUDE.md` and `.claude/` are gitignored in this repo, so they may be absent at the
base ref — fall back to the local working-tree copies
(`$(git rev-parse --show-toplevel)/CLAUDE.md`, `.claude/agents/*.md`).

1e. **Trace the change's blast radius** (read-only, no checkout). A finding read
off the hunk alone is often wrong — the event loop already serializes the access,
`Container.lock` is already held by the caller, a test pins the behavior. For each
changed exported symbol **and each changed unexported function in `containers/` or
`ebpftracer/`** (most of the logic is unexported):
```bash
cd "$(git rev-parse --show-toplevel)"
grep -rnE '<ChangedSymbol1>|<ChangedSymbol2>' --include='*.go' . | grep -vE '_test\.go|^\./internal/'   # callers + callees
grep -rnE '<ChangedSymbol1>|<ChangedSymbol2>' --include='*_test.go' .                                 # tests that pin behavior
```
For a changed function in `containers/`, also establish **which goroutine calls it**
(the `handleEvents` loop, a scrape via `Collect`, a per-process goroutine, a timer) —
the concurrency findings depend on it.

**Backend contract — only when the change needs it, and never via code search.**
This is conditional: most PRs skip it. If the diff changes no metric name/type/label
(including label order in `MustNewConstMetric`), no span/log resource or attribute,
no profile upload parameter, no endpoint path and no header, there is nothing to
cross-check, so skip it and do not ask the user for anything.

Trigger it only when a finding would actually rest on the consumer's contract — a
renamed/removed/retyped metric, a new or dropped label, a changed attribute key, a
"mainv2 queries X" claim. **Resolve the ref here in STEP 1, before the STEP 2
fan-out** — subagents run in parallel and cannot pause to prompt, so the orchestrator
decides up front:

- **`BE_REF` was supplied** (2nd arg) → use it.
- **A contract check is needed and `BE_REF` is empty** → **STOP and ask the user**
  before reading anything. Do not `gh search code` (default-branch-only → false
  CRITICALs), do not assume `main`, do not guess. Use `AskUserQuestion`:

  > "This PR changes `<metric/label/attribute>`, which `codexray-mainv2` queries. Which
  > branch there should I check the consumer on?"

  Offer discovered options — branches matching the feature
  (`git -C ${CODEXRAY_BACKEND_DIR:-$(git rev-parse --show-toplevel)/../codexray-mainv2} branch -a 2>/dev/null | grep -i <feature>`
  or use git worktree), the backend's release branch, `main` — always with a free-text
  fallback. User names a branch → set `BE_REF` and continue. User declines/skips → the
  contract is **UNVERIFIED**: note it plainly in the summary Notes ("contract
  unverified — user skipped backend branch") and **raise no CRITICAL about a consumer
  you could not confirm.**

Then read the consumer at the resolved ref:
```bash
for f in constructor/queries.go constructor/containers.go cmd/collection/main.go \
         collector/collector.go collector/metrics.go collector/traces.go collector/logs.go \
         collector/profiles.go collector/migrate.go clickhouse/logs.go clickhouse/queries.go; do
  gh api "repos/$BE_REPO/contents/$f?ref=$BE_REF" -q .content | base64 -d
done
# then grep that content for the changed metric/label/attribute name
```
Pass these contents (with `BE_REF`) to Subagent G verbatim — `telemetry-contract-reviewer`
is read-only and has no shell, so it can only check what you hand it.
If the metric/label is not referenced on `$BE_REF`, say so ("not queried by mainv2 at
`$BE_REF`") — a rename of an unqueried series is WARNING (external dashboards/alerts
may still use it), not CRITICAL. Never raise a CRITICAL about a consumer you could
not confirm on the correct ref.
**Rule:** a finding that a caller, the event-loop ownership model, a sibling layer,
or an existing test already handles is a false positive — drop it (or mark it INFO,
which has the same effect: INFO is never posted; see STEP 3). Legit findings cite
where in the *surrounding* code the contract breaks.

---

## STEP 2 — Spawn every reviewer agent in parallel (Agent tool)

**Run every agent in `.claude/agents/`.** The folder holds 17 agent files: 15 are
spawnable predefined agents (they have YAML frontmatter) and two —
`node-agent-reviewer.md` and `security-reviewer.md` — have no frontmatter, so they
are **rubrics** loaded into lanes A and D rather than spawned. Every spawnable agent
gets its own review lane below; none sit unused.

**Speed rule: fire ALL the Agent calls in a single message so they run
concurrently — never await one before spawning the next.** They all run at once, so
total wall-clock is ≈ the slowest single subagent, not the sum. Do no local work
(vet/build/tests/BPF compile) around them.

Give each: the full diff, changed file contents, CLAUDE.md + shared snapshots, and
the blast-radius context from Step 1e (callers, callees, calling goroutine, pinning
tests, backend contract). Instruct every subagent to ground findings in that
surrounding code and drop anything a caller/test/ownership rule already handles.

**Each subagent MUST run as a predefined agent from `.claude/agents/`** — pass the
mapped `subagent_type` to the Agent tool (do not inline the work yourself). The
agent's own definition supplies its expertise and repo knowledge; the lane text below
adds only the lane boundary + the shared output contract. **Lane discipline:** every
agent stays inside its own lane and DROPS any finding another lane owns — the
slug-dedup in STEP 3 is the backstop, not a license to duplicate. **Conditional lanes
(K–P) return _nothing_ unless the diff actually touches their area** — a one-line
"N/A: diff doesn't touch <area>" is the whole output in that case, so they cost
≈nothing on an unrelated PR.

| Subagent | Agent (`subagent_type`) | Lane |
|---|---|---|
| A — Code Review (master) | *(default agent)* + **`.claude/agents/node-agent-reviewer.md`** rubric | Repo rule enforcement (no frontmatter → rubric, not spawned). |
| B — Go idioms & concurrency | `golang-pro` | Goroutine lifecycles, channels, mutex/`defer`, context, `%w`/`errors.Is`, `LockOSThread`+`setns`, build tags, acronym case. |
| C — Code quality & tests | `code-reviewer` | Redundancy, dead code, readability, magic numbers, license header **and** missing tests for new/changed functions and parsers. |
| D — Security | `security-auditor` + **`.claude/agents/security-reviewer.md`** rubric | Untrusted L7//proc input, data leaving the node, privilege/host writes, API key + TLS, listeners, secrets, supply chain. |
| E — Architecture | `architect-reviewer` | Package layering, event-loop ownership model, startup/`init()` order, build-tag split, placement, vendored-module boundary, upstream divergence. |
| F — Performance | `performance-engineer` | Per-event cost in perf readers/`handleEvents`, work under `Container.lock` in `Collect`, `/proc` re-reads, parser allocs, lost samples, scrape latency. |
| G — Telemetry contract | `telemetry-contract-reviewer` | Metric names/types/labels/order/units, label cardinality, OTLP attributes, profile params, remote-write format/headers — cross-checked against mainv2 at `BE_REF`. |
| H — Runtime bug hunt | `debugger` | nil deref, index OOB, off-by-one, wrong conditionals, unchecked errors, pid/cgroup reuse, exit races, struct decoding, unit mistakes. Read-only (no repro). |
| I — Reliability / SRE | `sre-engineer` | Exporter timeouts/retry/backoff, spool bounds, degradation when collector/runtime is down, silent drops, log volume, footprint over days of churn. |
| J — Maintainability | `maintainability-reviewer` | Duplication across collectors/parsers, function size, wrong package, global-state coupling, hardcoded values, speculative abstraction, untestable shapes. |
| K — eBPF *(conditional)* | `ebpf-reviewer` | Only if diff touches `ebpftracer/ebpf/`, `ebpftracer/ebpf.go`, load/attach/perf code in `ebpftracer/*.go`, or `internal/pyroscope-ebpf/bpf`. |
| L — L7 protocols *(conditional)* | `l7-protocol-reviewer` | Only if diff touches `ebpftracer/l7/`, L7 handling in `containers/`, or `tracing/`. |
| M — Linux systems *(conditional)* | `linux-systems-reviewer` | Only if diff touches `cgroup/`, `proc/`, runtime/cgroup/process code in `containers/`, `node/`, `pinger/`, `jvm/`, `logs/` readers, `gpu/`. |
| N — Resilience / chaos *(conditional)* | `chaos-engineer` | Only if diff adds/changes an external dependency call (collector HTTP, runtime sockets, dbus, cloud metadata, NVML, .NET IPC, JVM attach, netlink/conntrack). |
| O — Deploy / k8s *(conditional)* | `kubernetes-specialist` | Only if diff touches `manifests/`, `Dockerfile`, `ebpftracer/Dockerfile`, `install.sh`, `docker-compose*`, `.github/workflows/`, or `flags/`. |
| P — Documentation *(conditional)* | `documentation-engineer` | Only if diff adds/changes a flag, metric, exported identifier, behavior, or release: CHANGELOG, README, doc comments, LICENSING/NOTICE. |

**All lanes use the same output contract:**
`[CRITICAL|WARNING|INFO] kind=<slug> file:line — problem (+ attack vector for
security) + fix`, where `kind` is a stable kebab-case slug and the cross-run dedup
key in STEP 3. Also report the **enclosing function/method name** for every finding —
STEP 3a anchors the dedup marker to it, not to a line. **Delivery:** in-diff findings
are inline comments on the offending line; anything with no line to attach to goes in
the sticky comment's **Notes** section (3b).
**INFO findings are dropped — never posted to the PR, inline or in the body. Only
CRITICAL and WARNING are ever delivered.** Subagents may still classify something as
INFO internally; the orchestrator filters INFO out at post time (STEP 3).
**Apply your own agent expertise** — the lanes below give only the lane boundary and
the highest-value checks (plus the loaded rubric for A/D), not how to do your job.

### Subagent A — Code Review (master)
Agent: default agent, **adopting `.claude/agents/node-agent-reviewer.md`** as its rubric. Enforce that rubric + CLAUDE.md on every changed line. Repo rules to hit: C change ⇒ regenerated `ebpf.go`, Go↔C struct parity; event-loop maps only from `handleEvents`, `Container.lock` on both sides of shared state, nothing slow in the loop; `defer mu.Unlock()`; `setns` only via `proc.ExecuteInNetNs`; `/proc` reads tolerate exit (`common.IsNotExist`), host files via `proc.HostPath`, handles closed on every path; `MustNewConstMetric` label count+order match the `Desc`, bounded labels, no metric rename without a mainv2 PR; one timed HTTP client per exporter, bodies closed, capped backoff, bounded buffering; klog only, `klog.Exit*` only at startup, no runtime `panic`, `%w`; new flag has `Envar` + safe default + docs/manifests/`install.sh` wiring; 3-line license header; vendored `internal/` untouched unless documented. Slug e.g. `ebpf-go-not-regenerated`, `struct-layout-mismatch`, `event-loop-map-race`, `slow-work-in-event-loop`, `missing-defer-unlock`, `setns-without-lockosthread`, `proc-exit-not-tolerated`, `bare-host-path`, `handle-leak`, `label-order-mismatch`, `unbounded-label`, `metric-renamed`, `default-http-client`, `unbounded-retry`, `runtime-panic`, `swallowed-error`, `flag-not-wired`, `missing-license-header`, `vendored-code-edited`.

### Subagent B — Go idioms & concurrency
Agent: `subagent_type: golang-pro`. Go-language correctness only (defer repo-rule/security/perf to A/D/F): goroutines without an exit path; channel sends that can block while holding a lock; unsynchronized maps/slices shared across goroutines; `LockOSThread`/`setns` pairing; `%w` + `errors.Is/As`; build-tag parity between `gpu.go` and `gpu_stub.go`; idioms and acronym case in new code. Slugs e.g. `goroutine-leak`, `blocking-send-under-lock`, `unsynchronized-map`, `errors-is-not-used`, `build-tag-drift`.

### Subagent C — Code quality & tests
Agent: `subagent_type: code-reviewer`. Owns both quality and tests:
- **Quality:** redundancy (same block in two collectors/parsers → extract), dead code, readability, magic numbers → named constants, missing 3-line license header on new files.
- **Tests (reason off the diff — no coverage run):** for each **new or changed function** in a testable package (`l7/`, `cgroup/`, `proc/`, `node/`, `common/`, `logs/`, `prom/`, `tracing/`, pure helpers in `containers/`), grep for a matching test:
  ```bash
  grep -rnE 'func Test.*<FuncName>|<FuncName>\(' --include='*_test.go' . ; ls <pkg>/fixtures 2>/dev/null
  ```
  None found → request one (unit, beside the package, `testify`, fixture or byte-slice input — include malformed/truncated input for parsers) with a minimal scaffold in a `<details>` block. Only for what this PR added/changed — not pre-existing gaps. Code that needs root/BPF → ask for a `VM`-gated test, not a unit test.
Slugs e.g. `duplicate-block`, `dead-code`, `magic-number`, `missing-license-header`, `missing-test`.

### Subagent D — Security
Agent: `subagent_type: security-auditor`, **adopting `.claude/agents/security-reviewer.md`** as its rubric — follow that rubric's priority order (highest first): **untrusted input** (unchecked slicing/lengths in L7 parsers and `/proc` readers → CRITICAL on an always-on path) → **data leaving the node** (widened payload capture, environ/cmdline export, new raw-text sinks) → **privilege/host writes** → **API key + TLS** → **listeners** → **secrets in repo** → **supply chain**. Always include the one-sentence attack vector (a pod on the node / the network / the operator). Slug e.g. `untrusted-index`, `untrusted-length-alloc`, `payload-exposure`, `environ-exposure`, `new-host-write`, `api-key-in-log`, `tls-verification-bypass`, `listener-widened`, `hardcoded-secret`, `path-traversal`.

### Subagent E — Architecture
Agent: `subagent_type: architect-reviewer`. Layering `ebpftracer` → `Registry.handleEvents` → `Container` → collectors → exporters; new state placed with its owner goroutine; `main.go` startup order and cross-package `init()` order (flags parse before filters/cgroup/profiling read them); `gpu` build-tag split; code in the package that owns the concern; no reach into vendored `internal/` beyond its public surface; divergence from upstream coroot is deliberate and noted. Slugs e.g. `layering-violation`, `ownership-violation`, `init-order`, `wrong-package`, `build-tag-leak`, `vendored-boundary`, `upstream-divergence`.

### Subagent F — Performance
Agent: `subagent_type: performance-engineer`. Measurable agent overhead only (not correctness): per-event allocations/syscalls in perf readers and `handleEvents`; work added under `Container.lock` in `Collect` (it already does cgroup/taskstats/`/proc`/JVM/.NET/pinger there); repeated `/proc` or cgroup reads that could be cached per scrape; parser allocations per L7 event; perf buffer sizing vs lost samples; spool write/read amplification. Slugs e.g. `hot-path-alloc`, `work-under-collect-lock`, `redundant-proc-read`, `perf-buffer-undersized`, `scrape-latency`.

### Subagent G — Telemetry contract
Agent: `subagent_type: telemetry-contract-reviewer`. Give it `BE_REF` (or "unverified"). Metric/label rename/retype/removal vs mainv2 PromQL; label order/count vs `Desc`; new label cardinality; unit/suffix conventions; span/log resource + attribute keys; profile upload query params; remote-write headers + format; `X-Api-Key` path. **Any consumer check reads mainv2 at the `BE_REF` resolved in Step 1e — never `gh search code` and never `main` by assumption. If a finding needs the consumer and no ref was resolved, flag the need back to the orchestrator (which asks the user) rather than guessing.** Slugs e.g. `metric-renamed`, `metric-retyped`, `label-removed`, `label-cardinality`, `unit-suffix`, `attribute-renamed`, `ingest-contract-break`, `be-pr-needed`.

### Subagent H — Runtime bug hunt
Agent: `subagent_type: debugger` — **read-only, no repro**; reason off the diff + blast-radius. Hunt bugs the change could introduce: nil deref / nil map write / type-assertion panic; slice bounds; off-by-one; inverted conditionals; error branch that proceeds with a zero value; pid or cgroup-id reuse after exit; events for a process that already exited; little-endian decoding and struct offsets; ns/µs/s and jiffies/USER_HZ mix-ups; first-scrape / empty-container edge cases. Ground each finding in a concrete failing input; drop anything a caller/test/ownership rule already guards (Step 1e). Slugs e.g. `nil-deref`, `index-oob`, `unchecked-error`, `pid-reuse`, `unit-mismatch`, `race`.

### Subagent I — Reliability / SRE
Agent: `subagent_type: sre-engineer`. Operational robustness of changed code: outbound calls without a timeout or with unbounded retry (including retrying a 4xx forever); unbounded queues/spool/maps; behavior when the collector, a runtime socket, or dbus is down; failure paths with no log line or metric (silent drop); log volume against the global rate limiter; goroutine/fd/memory footprint after days of container churn; startup failure modes (`klog.Exit*` vs degrade). Slugs e.g. `no-timeout`, `unbounded-retry`, `unbounded-buffer`, `silent-drop`, `log-flood`, `footprint-growth`, `fatal-on-optional-dependency`.

### Subagent J — Maintainability
Agent: `subagent_type: maintainability-reviewer`. Code shape only: duplicated logic across collectors/parsers/runtime clients that should be reused; functions over ~80 lines or nested >3; code in the wrong package; new package-level mutable state; inline timeouts/sizes that should be named constants or flags; single-use abstractions; logic fused with `/proc`/BPF/netlink I/O so it can't be unit-tested; needless churn in upstream-derived files. Slugs per the agent file (`duplicate-block`, `missed-reuse`, `function-too-long`, `wrong-package`, `global-state-coupling`, `magic-number`, `speculative-abstraction`, `untestable-shape`, `upstream-churn`, …).

### Subagents K–P — Conditional lanes
Each runs as its mapped `subagent_type` from the table above and **returns nothing but a one-line `N/A: diff doesn't touch <area>` unless the diff actually touches its area.** When it does apply, output uses the same contract (`[SEVERITY] kind=<slug> file:line — problem + fix`):
- **K — `ebpf-reviewer`**: verifier safety on every `__KERNEL_FROM` variant, `ebpf.go` regenerated, C↔Go struct parity, map sizes/eviction, perf pages/wakeups, uprobe attach/detach lifecycle.
- **L — `l7-protocol-reviewer`**: per-protocol parse correctness on truncated 1024-byte payloads, stateful parser keying/pruning, status/method mapping into metrics and spans.
- **M — `linux-systems-reviewer`**: `/proc` + cgroup v1/v2/hybrid semantics, runtime discovery, namespace switching, `HostPath`, fd/netlink/socket lifecycle, taskstats, journald/tail readers, NVML.
- **N — `chaos-engineer`**: what the node looks like when the new/changed dependency is down, slow, hung, or partially available — missing timeout, fallback, or recovery.
- **O — `kubernetes-specialist`**: DaemonSet privileges/mounts/probes/resources, RBAC scope, image build (builder, runtime, CGO, gpu variant, version ldflags), systemd unit + env whitelist, CI matrix, secrets in compose/manifests.
- **P — `documentation-engineer`**: CHANGELOG entry (Keep a Changelog, `## [X.Y.Z] — YYYY-MM-DD`), README flag/metric docs, doc comments on exported identifiers, LICENSING/NOTICE when vendored code changes.

---

## STEP 3 — Post the review (all via `gh`)

The review lands as **exactly one prose artifact**, plus the inline notes themselves:

1. **One sticky comment** — a single issue comment holding **Summary + Notes and
   nothing else**, re-edited in place on every re-run so the PR never accumulates a
   pile of stale summaries. This is the *only* place prose ever goes.
2. **Standalone inline comments**, posted one per finding straight onto the diff line.

**Never submit a PR review** (`POST /pulls/$PR/reviews`) — that is what used to put a
second prose block on the PR, and it cannot be fixed by shortening its body. GitHub
rejects an empty body on a `COMMENT` review, so a submitted review *always* carries
prose and *always* renders as its own comment block next to the sticky. Worse, a
submitted review's inline comment set is immutable, so every re-run needs a brand-new
review and the PR accretes one more block each time.

Post each comment standalone via `POST /pulls/$PR/comments` instead. **What matters is
the review body, not the review count:** GitHub still wraps standalone comments in an
implicit review object, but that wrapper has an **empty body**, so it renders as just
the diff threads with no prose block — and it is reaped automatically if its comments
are later deleted (verified on codexray-mainv2 #665: posting one standalone comment took the review
count 1 → 2, deleting it took it back to 1, and the wrapper's `body` was `""`
throughout). So do not treat a non-zero review count as failure. The invariant to hold
is **exactly one review-or-comment body of ours that is non-empty**, and that one is
the sticky.

- **Inline:** any **CRITICAL or WARNING** finding that anchors to a changed line,
  **from every subagent (A–P)**. **INFO / minor-correctness findings are dropped** —
  do not post them inline, even when they anchor to a changed line. A PR with zero
  CRITICAL/WARNING gets no inline notes; that's a clean review, not an incomplete one.
- **Never suppress a finding because ANOTHER author flagged it.** If a finding of
  ours coincides with a comment from a different bot (e.g. `gemini-code-assist`),
  a different account, or a human reviewer, **post ours anyway — duplicate it.**
  Cross-author duplication is expected and fine. The ONLY thing that suppresses a
  finding is *our own* prior `claude-pr-review` marker for the same
  `kind:path:symbol` (see 3a). Never read, count, dedup against, resolve, or
  otherwise touch any thread that lacks our marker — leave every other bot's and
  human's comment exactly as is.

### 3a. Prepare the inline comments (dedup against prior runs)

Every inline body begins with a stable marker
`<!-- claude-pr-review:<kind>:<path>:<symbol> -->`:

- `<kind>` — the slug the subagent emitted.
- `<path>` — the file.
- `<symbol>` — the **enclosing Go function or method** the finding sits in, kebab-cased
  (`handleEvents`, `(*Container).onL7Request` → `handle-events`, `container-on-l7-request`).
  Package-level declarations outside any func use `file-level`.

**The anchor is a symbol, never a line number — this is load-bearing.** A line number
is a coordinate in a diff that moves the moment anyone pushes anything: the *same
unfixed finding* re-reported at `:437` instead of `:416` looks nothing like its own
prior marker at `:416`. Keying on the line makes every re-run after a push
simultaneously (a) resolve a thread whose bug was never fixed, claiming credit, and
(b) re-post that same finding as a brand-new duplicate thread. Function names survive
edits above them; line numbers do not. Never put a line number in the marker.

1. **Read prior state:**
   ```bash
   gh api "repos/$REPO/pulls/$PR/comments" --paginate
   gh api graphql -F pr=$PR -f query='query($pr:Int!){repository(owner:"Codifinary",name:"codexray-node-agent"){pullRequest(number:$pr){reviewThreads(first:100){nodes{id isResolved isOutdated path line originalLine comments(first:1){nodes{body author{login}}}}}}}}'
   ```
   Parse the leading marker from each thread's first comment → `prior: kind:path:symbol → {threadId, isResolved, isOutdated}`. **Only threads whose first comment carries our `<!-- claude-pr-review:... -->` marker count as prior state.** Threads from any other author/bot (gemini, another account, a human) are NOT prior state and NOT dedup keys — ignore them completely; never let them suppress one of our findings. Dedup is strictly *us vs. our own past runs*.

   Older threads from before the symbol anchor carry a trailing line number
   (`…:containers/x.go:416`). Match those by `kind:path` alone, ignoring the last segment —
   otherwise the first run under this format re-posts every finding that already has a thread.

2. **Compute current keys** for each finding from **every subagent (A–P)** that **anchors to a changed line and is CRITICAL or WARNING** → `current: kind:path:symbol → finding`, keeping the finding's fresh `line` alongside for the comment coordinate. **Drop INFO findings entirely — they are never posted.** Findings with no diff line (cross-file, out-of-diff) are not in `current`; the CRITICAL/WARNING ones go in the sticky's Notes section (3b).

3. **Three-way reconcile** (re-runs don't duplicate threads; **fixed findings get closed out**). Only ever act on threads carrying our `claude-pr-review` marker:
   | Prior (ours only) | Current | Action |
   |---|---|---|
   | unresolved | present | Skip — the finding persists and its thread is already there. Drop from `current`; **do not re-post it at its new line.** |
   | unresolved | absent (**fixed**) | **Resolve**: `gh api graphql -f query='mutation($t:ID!){resolveReviewThread(input:{threadId:$t}){thread{id}}}' -f t=<threadId>`. |
   | already resolved | any | Leave resolved; drop from `current`. |
   | absent | present | Keep — this is a new inline comment for 3c. |

   `isOutdated` deliberately does **not** appear in that table, and you cannot set it.
   It is computed by GitHub alone: a thread goes outdated only when the diff hunk it
   is anchored to can no longer be located in the current `base...head` diff. A push
   that *edits* the flagged line usually keeps the hunk locatable, so GitHub silently
   **re-anchors** the thread to its new line and leaves `isOutdated: false` — verified on
   codexray-mainv2 PR #651, where thread `originalLine 416` moved to `line 437` across a fix push
   and stayed not-outdated, while two threads whose hunks vanished went outdated on
   their own. So "the comments didn't go outdated after we pushed the fix" is normal
   GitHub behavior, not a missing step. **Resolving is the signal we control** — a
   resolved thread collapses as done regardless of outdated state. Getting resolve to
   fire reliably is what the symbol anchor above buys.

   After reconcile, `current` holds only the **new** inline comments. **Sort them by
   `path`, then by `line` ascending, before writing the file.** Findings arrive grouped
   by subagent, so unsorted they interleave — `container.go`, `registry.go`,
   `container.go` again — and a reader working through one file has to jump back and
   forth. Sorted, every finding in a file lands as one contiguous run, in the order the
   lines appear in that file. Write them to `/tmp/review-inline.json` as an array of
   `{path, line, body}` objects, where `body` is:
   `"<!-- claude-pr-review:<kind>:<path>:<symbol> -->\n**[<SEVERITY>]** <kind>\n\n<problem + attack vector if security>\n\n**Fix:** <exact fix>"`
   (`<SEVERITY>` = the finding's severity, always CRITICAL or WARNING — INFO was already filtered out in step 2). Track `<new>` / `<persisted>` / `<resolved>` counts for the verdict.

4. **Verify every `line` is a line this PR actually added or changed**, using the
   **combined `base...head` diff** — not the per-commit patch series. `gh pr diff --patch`
   returns one patch per commit, so on a multi-commit PR its line numbers describe
   intermediate file states and will not match what GitHub anchors against. Fetch the
   combined diff and check each target against the set of added lines:
   ```bash
   gh api "repos/$REPO/pulls/$PR" -H "Accept: application/vnd.github.v3.diff" > /tmp/combined.diff
   ```
   A comment whose `line` is not in that set is rejected with `422 Unprocessable Entity`
   at post time. Fix the anchor (move it to the nearest added line inside the same
   function) or move the finding to the sticky's Notes.

### 3b. Upsert the sticky comment — Summary + Notes, nothing else

Compose the sticky body to `/tmp/review-sticky.md`. It carries a stable identity
marker on line 1 and **exactly two headers, `## Summary` and `## Notes`**:

```
<!-- claude-pr-review:summary -->
🤖 **Claude PR Review** — head `<short SHA>`

## Summary
[2-3 sentences: what the PR does + overall severity + merge-ready yes/no.
CRITICAL/WARNING count citing the inline notes. On re-runs, append:
`<N> new · <P> persisted · <R> resolved since last run.`]

## Notes
[One flat bullet list of everything with NO changed line to attach to — fold in
missing-test suggestions (Subagent C, testify scaffold in a <details>), telemetry-contract
+ BE-PR-needed notes (Subagent G), any conditional-lane (K–P) findings, and any
out-of-diff CRITICAL/WARNING findings. **Never INFO — those are dropped.** If
there's nothing to note, write "None.". **Order the bullets by file path, then line**,
same as the inline comments — bullets that cite the same file sit together instead of
interleaving. Bullets citing no file go last.]
```

**Two headers is the whole contract.** No `## Inline review`, no `## Findings`, no
`## Verdict`, no `## Test Coverage`, no `## Telemetry Contract`, no per-lane or per-topic
section, no restating a finding that is already an inline comment, no closing
sign-off block. Anything that isn't the PR's summary or a note with no line to
attach to does not belong on the PR at all. A per-topic heading is the usual way this
bloats — fold every such item into a Notes bullet instead.

Upsert by the marker, so re-runs edit one comment instead of stacking new ones:

```bash
STICKY=$(gh api "repos/$REPO/issues/$PR/comments" --paginate \
  -q '.[] | select(.body | startswith("<!-- claude-pr-review:summary -->")) | .id' | head -1)
if [ -n "$STICKY" ]; then
  STICKY_URL=$(gh api --method PATCH "repos/$REPO/issues/comments/$STICKY" \
    -F body=@/tmp/review-sticky.md -q .html_url)
else
  STICKY_URL=$(gh api --method POST "repos/$REPO/issues/$PR/comments" \
    -F body=@/tmp/review-sticky.md -q .html_url)
fi
```
`-F body=@<file>` reads the value from the file, so the markdown survives verbatim.
**Only ever edit a comment whose body starts with our marker** — never adopt,
overwrite, or delete another author's comment as the sticky. If an older run left a
summary in a PR-review body (the pre-standalone format), leave it; it lingers
harmlessly, and deleting a submitted review is not possible via the API anyway.

### 3c. Post the inline comments — standalone, no review object

**Re-read the head SHA now**, immediately before posting — a push between STEP 1 and
here would otherwise anchor every comment to a stale commit, where GitHub renders
them against old code and marks them outdated on arrival:
```bash
HEAD_SHA=$(gh pr view $PR --repo $REPO --json headRefOid -q .headRefOid)
```
If it differs from the SHA the subagents reviewed, say so in the sticky's Summary —
the review describes the older head.

Post each new finding as its own comment. Each call opens one review thread, exactly
like a review-attached comment, but adds **no prose block** — the implicit review
wrapper GitHub creates has an empty body:
The `sort_by(.path, .line)` below is the guard that keeps 3a's ordering — post order
is what the PR conversation timeline shows, so an unsorted file would scatter a
single file's findings across the thread list even though 3a grouped them:
```bash
jq -c 'sort_by(.path, .line) | .[]' /tmp/review-inline.json | while read -r c; do
  jq -n --arg sha "$HEAD_SHA" --argjson c "$c" \
     '{commit_id:$sha, path:$c.path, line:$c.line, side:"RIGHT", body:$c.body}' \
     > /tmp/one-comment.json
  gh api --method POST "repos/$REPO/pulls/$PR/comments" --input /tmp/one-comment.json \
    -q .html_url || echo "FAILED: $(echo "$c" | jq -r '.path + ":" + (.line|tostring)')"
done
```
- **`side: "RIGHT"`** always — findings are about the new code, never the deleted side.
- A `422` means that `line` is not in the combined `base...head` diff. That is a bug in
  the anchor, not a reason to retry: re-check it against 3a step 4, or move the finding
  to the sticky's Notes.
- **Never** call `POST /pulls/$PR/reviews`, and never fall back to it when a comment
  fails — that reintroduces the second prose block this whole step exists to avoid.
- If 3a produced no new inline comments, post nothing here; the sticky is already
  updated and is the entire review.

**Self-check before finishing.** Confirm the PR carries exactly one prose block of ours:
```bash
# must print 1 — the sticky
gh api "repos/$REPO/issues/$PR/comments" --paginate \
  -q '[.[] | select(.body | startswith("<!-- claude-pr-review:summary -->"))] | length'
# must print 0 for anything THIS run created; a pre-standalone run may leave one behind
gh api "repos/$REPO/pulls/$PR/reviews" --paginate \
  -q '[.[] | select(.user.login == "'"$(gh api user -q .login)"'" and .body != "")] | length'
```
Count only **non-empty** review bodies — the empty implicit wrappers are expected and
harmless. If the second number counts something this run added, that is a mistake.

## Cleanup
```bash
rm -f /tmp/review-sticky.md /tmp/review-inline.json /tmp/one-comment.json /tmp/combined.diff
```
