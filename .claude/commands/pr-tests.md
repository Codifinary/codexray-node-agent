Write **flow-driven tests for the changed files of PR #$ARGUMENTS** on
`Codifinary/codexray-node-agent`, using that PR's `/review-pr` inline annotations as
the map of what's already suspect. Scope is **the PR diff only** — not the whole
capability, not the codebase. This is the post-review companion to `/review-pr`:
review-pr finds and posts the issues; this command locks the changed behavior in
with tests.

| | |
|---|---|
| **Scope** | Only the changed `.go` files of PR #$ARGUMENTS (the diff) + their immediate blast radius (callers of a changed function, the collector or parser it feeds). **Not the whole capability, not the codebase.** |
| **Reads (looks into)** | The PR diff + changed files; `/review-pr`'s inline `claude-pr-review:<kind>:<path>:<symbol>` annotations + its sticky Summary+Notes comment on this PR; the outbound contract in `codexray-mainv2` (the PromQL and ingest handlers that consume the changed signal), and the wire-protocol spec for any changed L7 parser. |
| **Writes (acts into)** | Unit test files beside the package (`<pkg>/<file>_test.go`) and fixtures under `<pkg>/fixtures/`. **Never files bug tickets, never edits source.** |

You run under **Plan mode** (`.claude/settings.local.json` →
`permissions.defaultMode: "plan"`). STEP 1–4 are read-only research + planning;
you present the plan via `ExitPlanMode` and only write tests **after approval**.

This command **does not find bugs and does not file tickets.** PR-scoped defects
already live as `/review-pr` inline annotations; codebase/capability bugs are
`/scan-bugs` and `/feature-bugs`' job. Here you only write tests.

Repo: `Codifinary/codexray-node-agent` (Go, module
`github.com/codifinary/codexray-node-agent`). Sibling for the intended contract of a
changed signal: `Codifinary/codexray-mainv2` (the collection-service + query-service
that ingest and query what this agent emits). Use `gh` only — do **not** clone.

Constraints from CLAUDE.md: `stretchr/testify` (`assert`/`require`); tests beside the
package; fixtures in `<pkg>/fixtures/`; override package vars (`procRoot` in `node/`,
`cgRoot` in `cgroup/`) to point at fixtures. **Unit tests must not need root,
BPF, a real container runtime, a network, or a live collector.** There is exactly
**one tier** you write here — **unit**. The repo's only other tier is the root-only,
`VM`-gated `ebpftracer/tracer_test.go` run through Vagrant
(`ebpftracer/Makefile test_vm*`); do not add to it from this command and do not add
integration/e2e/Docker tiers.

---

## Core principle — assert the contract, never rubber-stamp the diff

A test written *from* the changed code so it passes is a defect, not coverage.
Every assertion states what the code is **supposed** to do — per the wire protocol it
parses, the kernel/cgroup file format it reads, the metric/label contract mainv2
queries, and CLAUDE.md rules (never panic on untrusted input, tolerate process exit,
bounded labels). When the changed code is wrong, you still write the correct-contract
assertion — you just guard it with a `t.Skip` (STEP 3) so the suite stays green and the
bug stays documented, rather than baking the bug into a passing test.

## STEP 1 — Gather PR context (do in parallel)

1a. `gh pr diff $ARGUMENTS --repo Codifinary/codexray-node-agent --patch`

1b. `gh pr view $ARGUMENTS --repo Codifinary/codexray-node-agent --json files,headRefOid,baseRefName`

1c. `gh api "repos/Codifinary/codexray-node-agent/contents/<path>?ref=<headSHA>" -q .content | base64 -d` for every changed `.go` file at the PR head ref. **This is the scope.** The tests cover exactly these files' changed behavior and their immediate blast radius — nothing capability-wide. Skip `ebpftracer/ebpf.go` (generated) and anything under `internal/` (vendored).

1d. **Nail the intended contract** for what the change emits or parses, so
expectations reflect the real contract, not the code:
- **Metric/label changes** → the consumer PromQL in mainv2. Use the backend branch the
  `/review-pr` sticky names, or ask the user which `codexray-mainv2` branch to read
  (never `gh search code`, never assume `main`):
  ```bash
  gh api "repos/Codifinary/codexray-mainv2/contents/constructor/queries.go?ref=<BE_REF>" -q .content | base64 -d | grep -n '<metric_name>'
  ```
- **L7 parser changes** → the protocol's wire format (message layout, length fields,
  status encoding). Take real captured bytes from existing tests in `ebpftracer/l7/`
  where they exist; otherwise build the frame from the spec, and state which.
- **cgroup / `/proc` / node readers** → the kernel's documented file format; copy the
  closest existing fixture in `<pkg>/fixtures/` and vary it.

## STEP 2 — Read `/review-pr`'s findings (the suspect map)

`/review-pr` posts inline annotations whose bodies begin with a stable marker
`<!-- claude-pr-review:<kind>:<path>:<symbol> -->` (e.g.
`kind=index-oob`, `label-order-mismatch`, `proc-exit-not-tolerated`; `<symbol>` is
the enclosing func, kebab-cased), plus a sticky Summary+Notes comment (marker
`<!-- claude-pr-review:summary -->`). Pull both — the sticky's Notes carry the
findings that had no line to attach to, including its missing-test suggestions:
```bash
gh api "repos/Codifinary/codexray-node-agent/pulls/$ARGUMENTS/comments" --paginate
gh api "repos/Codifinary/codexray-node-agent/issues/$ARGUMENTS/comments" --paginate \
  -q '.[] | select(.body | startswith("<!-- claude-pr-review:summary -->")) | .body'
```
Take the comment's own `path` + `line` from the API for the code coordinate — the
marker's `<symbol>` is a dedup key, not a location. Classify each changed behavior:
- **Flagged CRITICAL/WARNING** → the reviewer believes it's a bug. Your test
  asserts the *correct* behavior and is guarded with a `t.Skip` (STEP 3).
- **Not flagged** → treat as correct-by-review; write a normal test that pins the
  behavior against the contract.

**Graceful degradation.** If there are no `/review-pr` annotations (review never
ran, or GitHub marked them "outdated" after a later push), say so in the plan and
derive correct expectations purely from the contract (STEP 1d).
Never test against outdated annotations — an outdated inline no longer maps to a
current line.

## STEP 3 — Plan unit tests for the changed files

For each changed file, plan the minimum cases to pin its changed behavior + the
branches review-pr flagged. What is reachable at unit level in this repo:
- **L7 parsers** (`ebpftracer/l7/`) — byte-slice inputs: a well-formed request/
  response, then **truncated at every length boundary, oversized length fields, empty
  payload, garbage**. The assertion for malformed input is "returns a zero/unknown
  result and does not panic". Table-driven.
- **cgroup / proc / node readers** — fixture files under `<pkg>/fixtures/`, v1 + v2
  (+ hybrid where relevant), a missing file (process exited → tolerated), a malformed
  line.
- **common/** filters, naming (`ContainerIdToOtelServiceName`), `TruncateUtf8`,
  `IsIpPrivate`, volumes — pure functions, table-driven.
- **containers/** — only the pure pieces (ID calculation from metadata, label/
  value builders, L7 stats aggregation, gc/prune decisions) extracted from
  `Container`/`Registry`. A metric emitted via a `prometheus.Collector` can be pinned
  with `prometheus/testutil` (`CollectAndCompare`) when the collector can be built
  without the event loop; say so if it can't.
- **prom/** — `buildWriteRequest` output from a hand-built `[]*dto.MetricFamily`;
  spool truncation against a `t.TempDir()`. Send loop against an `httptest.Server`
  (loopback only — it's not "network").
- **tracing/ logs/** — span/record construction via an in-memory exporter or
  `tracetest.SpanRecorder`; tail reader against `t.TempDir()` files.

Unreachable at unit level (excuse in the plan, don't chase): BPF load/attach, perf
readers, `setns`/`ExecuteInNetNs`, taskstats/netlink, runtime sockets
(docker/containerd/crio/dbus), NVML, raw ICMP, JVM attach.

**Encoding a reviewer-flagged bug as a test:** write the assertion for the
*correct* behavior, then guard it so the suite stays green and self-documents:
```go
// BUG (review-pr <kind>): <one-line> — unskip when fixed
t.Skip("BUG (review-pr <kind>): <one-line>")
// …correct-contract assertions below…
```
Reference the review-pr `kind` slug so the marker ties back to the inline
annotation. When the code is fixed, remove the skip → the test enforces the
contract. **A flagged panic gets the same treatment** — the test calls the parser with
the crashing input and asserts no panic + a sane result, skipped until fixed.

New test files start with the repo's 3-line header
(`// Copyright Codexray` / `// Derived from coroot/coroot-node-agent (...)` /
`// SPDX-License-Identifier: Apache-2.0`) — or just the first and last line for a
file with no upstream origin.

Do **not** file or touch ClickUp bug tickets — `/scan-bugs` and `/feature-bugs`
own that, and this command's output is tests only.

## STEP 4 — Present the plan and get approval (`ExitPlanMode`)

Summarize: changed files and, per file, the cases (and fixtures to add); which
review-pr findings become `t.Skip`-guarded tests (or "no annotations found — deriving
from contract"); what is excused as unreachable at unit level; the changed-function
coverage gap each test closes. Then `ExitPlanMode`. Write nothing yet.

## STEP 5 — Implement (after approval)

- Add the planned tests + fixtures, matching existing style (table-style tests,
  `assert`/`require`) in the touched package.
- Run and report (unit only; never `VM=…`, never `sudo`):
  ```bash
  # <changed pkgs> = the packages the PR touched, e.g. ./ebpftracer/l7/... ./cgroup/...
  CGO_ENABLED=1 go test <changed pkgs> -count=1
  ```
  `containers/`, `logs/` and anything importing journald need `libsystemd-dev`; if
  it's missing locally, say so rather than skipping the package silently.

## STEP 6 — Changed-function coverage gate (diff coverage, not package coverage)

The gate is **the functions this PR changed**, not the whole package. Measure
scoped to the changed packages:
```bash
# <changed pkgs>     = the packages the PR touched (dirs)
# <changed coverpkg> = the same set as comma-separated import paths
CGO_ENABLED=1 go test <changed pkgs> -count=1 \
  -coverpkg=<changed coverpkg> -coverprofile=/tmp/pr-$ARGUMENTS.cov
go tool cover -func=/tmp/pr-$ARGUMENTS.cov | sort -k3 -n   # per-func, lowest first
```
Loop: for every **function the diff changed** that is below 100%, add the test
that covers the uncovered branch/guard; a changed path **provably unreachable** at
unit level (BPF, netns, netlink, runtime socket, NVML — the STEP 3 list) is excused
with a one-line reason in the plan — do not chase it. Exit when every changed
function is covered or explicitly excused.

Report: tests + fixtures added, review-pr findings pinned as `t.Skip` guards,
pass/fail of the new suite (skips guarding flagged bugs are expected), final
changed-function coverage, and the excused paths.
