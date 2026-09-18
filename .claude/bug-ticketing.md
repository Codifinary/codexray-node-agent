# Bug ticketing — ClickUp is the ledger

Shared pipeline for `/scan-bugs` and `/feature-bugs`. **Not a slash command** — both
commands load this file and follow it verbatim for their ticketing step.

There is no local bug ledger. A markdown file that has to be hand-kept in sync with
ClickUp drifts, gets cleared when it bloats, renumbers its IDs, and re-files bugs that
already have tickets — that is what happened on `codexray-mainv2` before this pipeline.
**The ClickUp "Bugs" list is the single source of truth** — for what has been filed,
for what is still open, and for the next free bug ID.

```
list_id  = 901615779983    (Codexray-team → Bugs → "List") — shared with codexray-mainv2
statuses = to do → in progress → complete | cancelled
```

The list is shared with the backend and frontend pipelines. This repo's tickets are
kept apart by their **ID prefix `NA-`** (node agent) and the **`node-agent` tag** —
never allocate, dedup against, or re-verify a `BE-`/`FE-` ticket as if it were ours.

## Create only — never mutate

**The only write this pipeline performs is creating a new ticket** (plus its tags).
It never changes a status, never reopens or closes anything, never edits a title or
description, never comments on a ticket, and never sends a notification of its own.

Ticket state belongs to the people working the tickets. A sweep that moved tickets to
`complete` on its own read of the code would overwrite a human's triage with a
model's guess, and do it silently across dozens of tickets at once — while the person
who owned the ticket got a "status changed" notification they didn't ask for.

The pipeline still *reads* every ticket, and re-verifying them is worth doing — it is
how dedup works, and it surfaces bugs that have quietly been fixed. **Everything it
learns is reported to the user in chat** (STEP 5). The user decides what, if anything,
happens to the ticket.

---

## The ticket name IS the ledger record

Every ticket this pipeline creates is named to exactly this grammar, because the name
is what dedup reads:

```
[<severity>] <BUG-ID> — <short title>
```

- `<severity>` — `critical` | `high` | `medium` | `low`, lowercase, in square brackets.
- `<BUG-ID>` — `NA-<capability>-<NNN>`. `<capability>` is one of the capability keys
  in `/scan-bugs` (`ebpf`, `l7`, `containers`, `cgroup`, `proc`, `node`, `metadata`,
  `logs`, `tracing`, `profiling`, `remote-write`, `flags`, `runtimes`, `jvm`,
  `dotnet`, `gpu`, `pinger`, `deploy`) or a feature key chosen by `/feature-bugs`.
  `<NNN>` is zero-padded to 3.
- The separator is an em dash `—` with a space on each side. Not a hyphen.

A ticket that does not parse to this grammar with an `NA-` prefix is **foreign** — a
human-filed bug, or a `BE-`/`FE-` ticket from another repo's pipeline. Never treat it as
one of ours; it only participates in dedup as a title to compare against (STEP 2,
tier 2). (Nothing here modifies any ticket regardless — see above.)

## STEP 1 — Load the ledger (always, before finding anything)

Page through the whole list, **closed tickets included**. A `complete` or `cancelled`
ticket is still a filed bug: re-filing it because it's no longer open is the exact
duplicate this pipeline exists to prevent. This is a read — loading the ledger changes
nothing.

```
mcp__clickup__clickup_filter_tasks  list_ids=["901615779983"] include_closed=true page=0
mcp__clickup__clickup_filter_tasks  list_ids=["901615779983"] include_closed=true page=1
… until a page returns fewer than 100 tasks
```

Parse every returned `name` into `{severity, id, capability, seq, title, taskId, status, url}`
(foreign names → `{title, taskId, status}` only). Keep the whole set in memory as
`ledger` for the rest of the run. This is one cheap pass — do not fetch descriptions here.

**Allocate IDs from the ledger, never from memory or a counter.** The next id for a
capability is `max(seq of every ledger entry whose prefix is NA-<capability>- ) + 1`,
counting closed tickets. IDs are never reused, so a capability whose highest ticket is
`NA-l7-006` starts at `NA-l7-007` even if 001–006 are all complete.

## STEP 2 — Dedup every candidate bug before creating anything

Run **both** tiers on every candidate. A candidate that matches at either tier is a
duplicate: do not create a ticket, report it as `skipped (duplicate of <url>)`.

Normalization used by both tiers: lowercase, replace every run of non-alphanumeric
characters with a single space, trim.

**Tier 1 — name key (cheap, catches the common case).**
Build `capabilityKey :: normalizedTitle` for the candidate and for every `NA-` ledger
entry. Exact match → duplicate. This alone catches a re-run of the same sweep, which
is how the backend ended up with `FE-customDashboards-027` filed twice under its old
pipeline.

**Tier 2 — near-miss, resolved by source location.**
Titles get reworded between runs, so an exact-match-only check leaks duplicates. For
each candidate, take every ledger entry **in the same capability** — plus every
foreign ticket tagged `node-agent` or whose title names this repo — whose normalized
title shares ≥ 0.6 Jaccard token overlap with the candidate's. For those — and only
those, so the call count stays bounded — fetch the description and compare source
files:

```
mcp__clickup__clickup_get_task  task_id=<taskId> include=["description"]
```

Same buggy file(s) (compare paths **without line numbers** — lines drift with every
edit) **and** the same defect → duplicate. Different defect at the same file → NEW;
file it.

**Bias: when tier 2 is genuinely ambiguous, treat it as new and say so in the report.**
A duplicate ticket is one click to merge; a silently-dropped real bug is invisible.

**The sweep tag is never part of dedup.** A defect found by `/scan-bugs` (`legacy`) and
again by `/feature-bugs` (`feature`) is one bug with one ticket.

## STEP 3 — Resolve owners (`git blame` on the buggy line)

Ownership is whoever wrote the defective line, not whoever owns the capability:

```bash
cd "$(git rev-parse --show-toplevel)"
echo '["containers/container.go:596", "ebpftracer/l7/postgres.go:40-58"]' \
  | node .claude/bug-triage/blame-owners.mjs
```

Returns `owners[]` sorted most-lines-touched first, each with a `clickupEmail`.
`matched:false` means the git author has no `.claude/bug-triage/author-map.json` entry
and fell back to the default assignee — **always surface those in the dry-run** so a
human can re-route. Keep `author-map.json` current as contributors join and leave.

This repo's history starts with a single import of upstream `coroot/coroot-node-agent`
(`67c8d5f`, "codexray node agent intial commit", by `vyas@vizares.com`, who has left
and is deliberately not in the author map). Blame on untouched upstream code therefore
lands on that commit — most of `containers/container.go`, for example — not on anyone
who wrote the logic, and falls through to the fallback assignee. When every owner of a
bug is that import commit, say so in the dry-run (`owner = upstream import 67c8d5f`)
so a human picks the assignee rather than trusting the fallback.

## STEP 4 — Dry-run (always) then create (only when the user approves)

Print the table first: `id · severity · assignee(s) · title · dedup verdict`. Both
commands run under Plan mode, so this is part of what `ExitPlanMode` presents.
**Never create tickets before approval** — assigning one notifies a real person.

After approval, per surviving bug:

```
mcp__clickup__clickup_create_task
  list_id=901615779983
  name="[<severity>] <BUG-ID> — <title>"
  task_type="Bug"
  assignees=<every owner's clickupEmail>
  priority=  critical→urgent · high→high · medium→normal · low→low · unset→normal
  markdown_description=<template below>
```

**`task_type="Bug"` is mandatory on every ticket this pipeline creates.** Omitting it
makes ClickUp fall back to the list's default type, `Task` — so the ticket reads as an
ordinary work item instead of a bug and drops out of any Bug-typed view or filter. This
pipeline only ever files bugs, so there is no case where the default type is correct.
If the create call is rejected because the workspace has no `Bug` type, stop and tell
the user — do not silently create the ticket as a `Task`.

Then tag it via `mcp__clickup__clickup_add_tag_to_task` with **three** tags:
`node-agent` (the repo), `legacy` or `feature` (which sweep found it), and the
capability. Tags are orthogonal to the task type: the type is always `Bug`.

**Severity for this repo:**
- `critical` — agent panic/crash or event-loop stall on a normal node; BPF fails to
  load on a supported kernel; unbounded memory/fd/goroutine/uprobe growth under normal
  churn; customer secrets leaving the node; a metric/label mainv2 queries silently wrong
  or missing on every node.
- `high` — a signal wrong or missing for a common runtime/kernel/cgroup layout; a leak
  that needs days to matter; a collector outage causing data loss beyond the spool.
- `medium` — wrong for an edge case (rare runtime, IPv6, hybrid cgroup, short-lived
  containers); misleading logs; a flag that doesn't do what it says.
- `low` — cosmetic metric/label issues, doc drift with no behavioral effect.

**Creating the assigned ticket is the whole delivery.** ClickUp already notifies an
assignee when a task lands on them, so this pipeline sends nothing of its own — no
Slack bot, no bot token, no `.env` to keep in sync. If a run cannot resolve an owner,
report the unassigned ticket in chat so a human routes it; do not route around it.

**No new record to write anywhere.** The created ticket IS the ledger entry, so the
next run's STEP 1 sees it.

## STEP 5 — Report what you found, in chat

Every run also walks back the other way, for the capabilities it swept. For each `NA-`
ledger ticket in scope with status `to do` or `in progress`, re-verify the bug
**against current code** — read the cited source, don't trust the ticket.

**This is read-only. Do not call `clickup_update_task`, `clickup_create_comment`, or
any other write against an existing ticket.** Report the outcome to the user in the
chat response and stop there:

```
Fixed since it was filed (ticket still open — close it if you agree):
  NA-profiling-003  [high]  TargetFinder holds lock across jvm.DumpPerfmap
                    https://app.clickup.com/t/<taskId>
                    profiling/profiling.go:<line> now releases tf.lock before the dump.
```

Say plainly that you did not touch the ticket, so nobody assumes it was handled. Also
worth reporting the same way, and equally without acting on it: a ticket whose cited
file or symbol no longer exists, a ticket that duplicates another in the list, and a
ticket whose bug now reproduces differently than described.

Tickets already `complete` / `cancelled` need no mention. Only re-verify tickets
**inside the swept scope** — a `/feature-bugs` run on one feature has not read another
capability's code and cannot say anything useful about its tickets.

## ClickUp description template

```markdown
**Bug `{id}`** — {title}

| | |
|---|---|
| **Severity** | {severity} |
| **Capability** | {capability} |
| **Repo** | codexray-node-agent |
| **Source** | `{location}` |
| **Owner (git blame)** | {owners} |

### Flow
{flow — e.g. perf reader → handleEvents → Container.onL7Request → L7 metric/span}

### Expected
{expected — the contract: protocol spec, kernel file format, mainv2 query, CLAUDE.md rule}

### Actual
{actual}

### Reproduce
**Environment:** {kernel version / arch, cgroup v1|v2|hybrid, container runtime, k8s or systemd, agent flags}
**Trigger:** {what workload or event causes it — e.g. a pod sending a truncated Postgres frame, a container restarting every 10s}
**Observe:** {the exact check that proves it — e.g. `curl -s 127.0.0.1:10300/metrics | grep container_...`, agent log line, a panic trace, the series missing in mainv2}

---
_Filed by {/scan-bugs | /feature-bugs}._
```

`**Source**` must carry `path/to/file.go:LINE` — tier-2 dedup and `blame-owners.mjs`
both read it, and it is the only durable link from ticket back to code.
