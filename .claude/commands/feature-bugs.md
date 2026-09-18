---
description: Branch + feature bug finder checked against the ClickUp spec doc (intent-matching) → fresh bugs → tickets
---

# /feature-bugs — branch + feature bug finder (spec-driven)

The narrow, spec-driven run. Find bugs for **one feature on one branch**, checked
against that feature's **ClickUp spec doc**. The opposite end of the scope
spectrum from `/scan-bugs`: one feature's packages + its blast radius, not the
codebase.

| | |
|---|---|
| **Scope** | The feature's Go package(s) on `<branch-name>` (e.g. `containers/` + `flags/` for a throttling flag, `profiling/` + `internal/pyroscope-ebpf` wiring for Python profiling, `gpu/` for GPU support) — **plus its blast radius** (callers of a changed function, the event-loop / `Collect` / exporter path it flows through). **Not the whole codebase.** |
| **Reads (looks into)** | The branch checkout + its `<base>...HEAD` diff; the feature's **ClickUp spec doc** (auto-discovered); the **ClickUp Bugs list** (the ledger); the consumer contract in `codexray-mainv2` for any signal the feature emits. |
| **Writes (acts into)** | ClickUp tickets tagged `node-agent` + `feature` + the feature key, via `.claude/bug-ticketing.md`. **Never writes tests, never touches source.** |

**Arguments** (`$ARGUMENTS`): `<branch-name> <feature> [clickup-doc-ref]`
- `<branch-name>` — branch to check out and diff (e.g. `feature/added-throttling`,
  `fix/gpu-support`).
- `<feature>` — a short kebab-case key (becomes `NA-<feature>-NNN`); maps to its
  package(s) by the branch diff + the `/scan-bugs` capability table (ask only if
  genuinely ambiguous).
- `[clickup-doc-ref]` — the spec doc, given as either its **ClickUp URL** or its
  **doc name** (the ClickUp MCP is already connected, so the name alone resolves
  it — no URL needed). **Optional — auto-discovered when omitted** (STEP 1).

You run under **Plan mode** (`.claude/settings.local.json` →
`permissions.defaultMode: "plan"`). STEP 1–5 are read-only; you present the plan
via `ExitPlanMode` and only create tickets **after approval**.

**Ticketing is defined once, in `.claude/bug-ticketing.md` — read it before STEP 4
and follow it verbatim.** The ClickUp Bugs list is the ledger: it holds what has
already been filed, what is still open, and the next free bug ID. There is no local
bug file. Note the two ClickUp surfaces this command touches are different things:
the **spec doc** (the oracle, STEP 1) and the **Bugs list** (the ledger, STEP 4).

Repo: `Codifinary/codexray-node-agent` (Go, module
`github.com/codifinary/codexray-node-agent`). Sibling for the intended contract:
`Codifinary/codexray-mainv2` (ingest + PromQL). Use `gh` for the sibling — do
**not** clone it.

---

## Core principle — intent-matching

A bug is where the **agent fails to deliver an intent the spec doc states**. Node-agent
spec docs describe what an operator configures and what they should see in CodexRay
("short-lived pods don't create series", "Python services get CPU flame graphs",
"GPU utilization per container"), so a naive read of the code finds nothing to check.
You must convert the doc into *intended-behavior* assertions, map each to the **code
path and signal that serves it** — a flag, a metric/label, a span/log attribute, a
profile upload, a host effect — and check the agent delivers the contract. That
conversion (STEP 2) is the heart of this command.

## STEP 1 — Locate the spec doc (auto-fetch, then ask)

The spec-doc arg accepts either a **ClickUp URL** or a **doc name** — the ClickUp
MCP is already connected, so a name alone resolves to the doc.

1. **URL passed in `$ARGUMENTS`** → use it directly; skip discovery.
2. **Doc name passed** → resolve it by title via the MCP (skip fuzzy auto-discovery):
   ```
   mcp__clickup__clickup_search  query="<doc-name>"
   ```
   - **Exact / near-exact title match** → use it; state the doc (name + url) so the user can veto.
   - **Several titles match** → list them (name + url) and ask which.
   - **No match** → fall back to auto-discovery (item 3).
3. **Omitted → auto-discover** in workspace `9016681265`:
   ```
   mcp__clickup__clickup_search  query="node agent <feature>"   # then retry "<feature> spec", "<feature> user stories"
   ```
   Feature docs live under the **"User Story for Codexray"** docs folder — prefer a doc
   whose title matches `<feature>` + "agent"/"spec"/"user stor". Also check the ClickUp
   task linked from the branch's PR body (`gh pr list --repo Codifinary/codexray-node-agent --head <branch-name> --json body`) — node-agent work is often specced in the task itself.
   - **One strong match** → use it; state which doc (name + url) so the user can veto.
   - **Multiple candidates** → list them (name + url) and ask which.
   - **No match** → **ask the user for the doc URL** (or doc + page id, or the ClickUp
     task holding the spec). Do not proceed without a spec — it is the oracle.
4. **Pull every page** of the chosen doc. In a URL `…/v/dc/<document_id>/<page_id>`
   the first id after `/dc/` is the document, the second is the page:
   ```
   mcp__clickup__clickup_list_document_pages  document_id=<document_id>
   mcp__clickup__clickup_get_document_pages   document_id=<document_id> page_ids=[<page_id>, …] content_format="text/md"
   ```
   For a spec held in a task instead: `mcp__clickup__clickup_get_task task_id=<id> include=["description"]`.

## STEP 2 — Build the intent checklist (the oracle) and map to code + signal

Turn every `As a user/operator, I can …` story and every limit/default/constraint line
into a **concrete, checkable intent**, then map each to the **code path and observable
signal** that must satisfy it. Examples of the conversion:
- "Containers younger than 30s don't report metrics (configurable, 0 disables)" →
  `--min-container-age` / `MIN_CONTAINER_AGE`, default `30s`, is applied in
  `Container.Collect` for *every* container metric family (including L7 and log
  metrics), uses `startedAt` with the cgroup-mtime fallback, and `0` really disables
  it — a family that still emits for a 5s-old pod is a bug.
- "Language instrumentation attaches after a delay" → `--instrumentation-delay`
  gates Python/Node.js/.NET attach, but the delay must not block `handleEvents`.
- "Python services get CPU profiles" → processes found by `TargetFinder` are uploaded
  to `/v1/profiles` with the `service.name`/`container.id` params mainv2 expects, and
  `CODEXRAY_EBPF_PROFILING=disabled` opts a process out.
- "Works on GPU nodes" → the `gpu` build tag produces `node_gpu_*` and
  `container_resources_gpu_*`, and the non-GPU image still starts cleanly.
- "Configurable via env in k8s and systemd" → the flag has an `Envar`, and the
  manifests + `install.sh` env whitelist pass it through.

**Mark intents with no agent surface `N/A (backend/frontend-only)`** — dashboard
layout, alert rules, UI filters live in `codexray-mainv2`/`codexray-frontendv2`; don't
invent an agent contract the doc doesn't imply. Do check that the agent emits what
those features consume.

## STEP 3 — Check out the branch and scope the blast radius

```bash
REPO_ROOT="$(git rev-parse --show-toplevel)"
git -C "$REPO_ROOT" rev-parse --abbrev-ref HEAD   # save current
git -C "$REPO_ROOT" status --porcelain            # uncommitted work? stop and tell the user before switching
git -C "$REPO_ROOT" fetch origin
git -C "$REPO_ROOT" checkout <branch-name>
# base: the branch's PR base if one exists, else develop if the branch was cut from it, else main
BASE=$(gh pr list --repo Codifinary/codexray-node-agent --head <branch-name> --json baseRefName -q '.[0].baseRefName'); BASE=${BASE:-main}
git -C "$REPO_ROOT" diff "origin/$BASE"...HEAD --name-only     # branch changes
```
Restore the original branch at the end. Scope = the feature's packages **plus the
blast radius** of the branch's changes — for each changed function, its callers and
the goroutine it runs on (perf reader, `handleEvents`, `Collect`, exporter loop):
```bash
grep -rnE '<ChangedSymbol1>|<ChangedSymbol2>' --include='*.go' . | grep -vE '_test\.go|^\./internal/'
```
For signals the feature emits, confirm the consumer in `codexray-mainv2` at a branch
the user confirms (`gh api "repos/Codifinary/codexray-mainv2/contents/<path>?ref=<BE_REF>"`)
— never `gh search code`, never `main` by assumption.

## STEP 4 — Match intents to the code, find bugs

For each agent-relevant intent from STEP 2, trace the flow over the branch's code
(flag/env → derived config → the goroutine that applies it → the signal emitted →
the consumer) and decide: **delivered / bug / can't-tell**. A gap between a stated
intent and the real behavior is a bug — so is a CLAUDE.md violation the feature
introduces (stall in the event loop, unbounded state, panic on untrusted input,
widened data exposure). Each bug captures **expected (the intent) vs actual**,
`file.go:line`, severity + preconditions, and the reproduce block (environment,
trigger, observation), per the description template.

Then load the ClickUp Bugs list (`.claude/bug-ticketing.md` STEP 1 — every page,
`include_closed=true`) and use it twice:

- **Re-verify.** Every open `NA-` ticket (`to do` / `in progress`) **for this
  feature** — whatever sweep tag it carries — gets re-checked against the branch's
  current code. Fixed → collect it for the chat report (`.claude/bug-ticketing.md`
  STEP 5) so the user can close it themselves. **Never change a ticket's status and
  never comment on one.** Skip tickets outside this feature; you haven't read that code.
- **Dedup.** Run both tiers of `.claude/bug-ticketing.md` STEP 2 over every
  candidate. A `legacy`-tagged ticket from `/scan-bugs` matches a `feature`
  candidate for the same defect — the tag is not part of the key, so it is
  correctly a duplicate and is never double-filed. Closed tickets match too.

The bug ids you allocate come from the ledger's max sequence for this feature key, so
they never collide with `/scan-bugs`'s.

## STEP 5 — Present the plan (`ExitPlanMode`)

Summarize: the branch + base + feature, an **intent-coverage list** (each intent ✅
delivered / 🐞 bug / ❔ can't-tell / N/A backend/frontend-only) so the user sees the
spec was checked end-to-end, new `feature` bugs with their allocated ids and blame
assignees (flag `matched:false` and upstream-import owners), candidates **skipped as
duplicates** (with the ticket url each matched), and open tickets whose bug now looks
fixed. Then `ExitPlanMode`. Create nothing yet.

## STEP 6 — File the new tickets (after approval)

Run `.claude/bug-ticketing.md` STEP 3–4: blame owners → create each new ticket
named `[<severity>] NA-<feature>-NNN — <title>`, **created with `task_type="Bug"`**
→ tag it `node-agent` + `feature` + the feature key, assigned to its blame owners.
Add the spec intent the bug violates to the description's `### Flow` section, so the
ticket carries its oracle.

**Creating is the only write.** Every open ticket you found already fixed goes in
the chat report per `.claude/bug-ticketing.md` STEP 5 — untouched in ClickUp, so the
user can close it themselves.

The created tickets are the ledger — there is nothing else to write down. No
tests, no source edits.

Report: intent coverage, tickets filed (+assignees), duplicates skipped,
open tickets whose bug now looks fixed (with their urls, flagged as **not** closed
by this run), and any `matched:false` / upstream-import blame fallbacks a human
should re-route. **Restore the original branch.**
