#!/usr/bin/env node
// Resolve the ClickUp owners of a set of buggy source lines via `git blame`, so a
// bug ticket lands on whoever actually wrote the defect. Deterministic + read-only.
//
// Usage:
//   echo '["containers/container.go:596", "ebpftracer/l7/postgres.go:40-58"]' \
//     | node .claude/bug-triage/blame-owners.mjs
//   node .claude/bug-triage/blame-owners.mjs containers/container.go:596 ...
//
// Run from the repo root (git blame resolves paths against the cwd).
//
// Emits on stdout:
//   { "owners": [ { name, gitEmail, clickupEmail, matched, lines } ], "locations": [...] }
// sorted most-lines-touched first, so owners[0] is the primary owner. `matched:false`
// means the git author has no author-map entry and fell back to the default assignee —
// always surface those in the dry-run so a human can re-route the ticket.

import { execFileSync } from "node:child_process";
import { readFileSync, existsSync } from "node:fs";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";

const repoRoot = process.cwd();

// git-author-email -> canonical ClickUp email (+ fallback for departed authors).
// The map lives next to this script (.claude/ is gitignored in this repo, so the
// whole triage kit stays local to it).
const authorMapPath = join(dirname(fileURLToPath(import.meta.url)), "author-map.json");
const authorMap = existsSync(authorMapPath)
  ? JSON.parse(readFileSync(authorMapPath, "utf8"))
  : { fallback: null, map: {} };

function canonical(email) {
  const mapped = authorMap.map?.[email];
  if (mapped) return { clickupEmail: mapped, matched: true };
  return { clickupEmail: authorMap.fallback, matched: false };
}

// "path/to/file.go:412" or "path/to/file.go:317-339"
const LOC = /^(.+?):(\d+)(?:-(\d+))?$/;
function parseLocation(token) {
  const m = String(token).trim().replace(/^`|`$/g, "").match(LOC);
  if (!m) return null;
  return { file: m[1], line: Number(m[2]), endLine: m[3] ? Number(m[3]) : Number(m[2]) };
}

function blameOwners(locations) {
  const owners = new Map(); // git email -> { name, email, lines }
  for (const loc of locations) {
    let rows;
    try {
      const out = execFileSync(
        "git",
        ["blame", "-L", `${loc.line},${loc.endLine}`, "--line-porcelain", "--", loc.file],
        { cwd: repoRoot, encoding: "utf8", stdio: ["ignore", "pipe", "ignore"] }
      );
      rows = out.split("\n");
    } catch {
      // File renamed away, line out of range, or path not in the worktree — the caller
      // still gets a ticket, just unassigned. Never fail the whole run over one location.
      rows = [];
    }
    let name = null;
    for (const r of rows) {
      if (r.startsWith("author ")) name = r.slice(7).trim();
      else if (r.startsWith("author-mail ")) {
        const email = r.slice(12).trim().replace(/^<|>$/g, "");
        if (!email) continue;
        const e = owners.get(email) || { name, email, lines: 0 };
        e.lines += 1;
        e.name = e.name || name;
        owners.set(email, e);
      }
    }
  }

  // Canonicalize to ClickUp, then dedup by ClickUp email — one person may commit under
  // several git identities (e.g. @vizares.com + @codexray.io) that map to the same member.
  const byClickup = new Map();
  for (const o of owners.values()) {
    const c = canonical(o.email);
    const e = byClickup.get(c.clickupEmail);
    if (!e) {
      byClickup.set(c.clickupEmail, {
        name: o.name,
        gitEmail: o.email,
        clickupEmail: c.clickupEmail,
        matched: c.matched,
        lines: o.lines,
      });
    } else {
      e.lines += o.lines;
      if (c.matched && !e.matched) { e.matched = true; e.gitEmail = o.email; } // a real member identity wins
    }
  }
  return [...byClickup.values()].sort((a, b) => b.lines - a.lines);
}

async function readStdin() {
  if (process.stdin.isTTY) return "";
  return new Promise((resolve) => {
    let s = "";
    process.stdin.on("data", (c) => (s += c));
    process.stdin.on("end", () => resolve(s));
  });
}

const argTokens = process.argv.slice(2);
const raw = argTokens.length ? argTokens : JSON.parse((await readStdin()) || "[]");
const locations = raw.map(parseLocation).filter(Boolean);

if (!locations.length) {
  console.error("no parseable locations — expected `path/to/file.go:LINE` or `path:START-END`");
  process.exit(1);
}

process.stdout.write(JSON.stringify({ owners: blameOwners(locations), locations }, null, 2));
