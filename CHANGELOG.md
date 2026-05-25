# Changelog

All notable changes to `codexray-node-agent` are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and the project aims to follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

- **Added** for new features.
- **Changed** for changes in existing functionality.
- **Deprecated** for soon-to-be-removed features.
- **Removed** for now-removed features.
- **Fixed** for any bug fix.
- **Security** in case of vulnerabilities.

---

## [Unreleased]

Memory-leak investigation and mitigations. See [MEMORY_LEAK_INVESTIGATION.md](MEMORY_LEAK_INVESTIGATION.md)
for the full timeline of suspects ruled out, what shipped, and what's parked.

### Fixed

- **`ebpftracer/elf.go`**: ELF symbol-table parsing no longer allocates ~50-70 MB
  of transient heap per attach. Added `findSymbolsStreaming()` that reads
  `Elf64_Sym` entries 24 bytes at a time via the symbol section's
  `io.ReadSeeker` and resolves names individually through the linked
  string-table's `ReaderAt` — never materialises the full `[]elf.Symbol` slice
  that `debug/elf.Symbols()` / `DynamicSymbols()` would. Replaces the bulk
  parser used by every uprobe attach path (Go TLS, OpenSSL, Node.js, Python).
- **`ebpftracer/elf.go`**: `ELFFile.Close()` nils `f.symbols`, `f.textSection`,
  `f.textSectionReader` so GC can reclaim the symbol slice immediately on
  `defer ef.Close()`, rather than waiting for the entire `*ELFFile` to become
  unreachable through `Symbol.f` back-references.
- **`ebpftracer/elf.go`**: `readSymbols(wanted map[string]struct{})` only retains
  entries whose name is in `wanted`; the rest is dropped in the same stack
  frame so the full debug/elf slice can be GC'd immediately.
- **`ebpftracer/elf.go`**: new `GetSymbols(names...)` does multi-name lookup in
  a single symbol-table pass (used by TLS attach which needs both
  `crypto/tls.(*Conn).Write` and `crypto/tls.(*Conn).Read`).
- **`ebpftracer/symcache.go`** (new): per-binary symbol-address cache keyed by
  `(path, mtime, size)`. Each unique binary on the node is parsed exactly
  once across the agent's lifetime; subsequent attaches are O(1) map lookups.
  Used by `tls.go`, `nodejs.go`, `python.go`.
- **`ebpftracer/symcache.go`**: `parseSymInfo()` does `defer runtime.GC()` after
  each cache miss so the ~50 MB transient debug/elf working set is reclaimed
  *before* the next attach can stack on top of it.
- **`ebpftracer/tls.go`**: Go TLS and OpenSSL uprobe attach use the new symbol
  cache. Returned `link.Link` slice is unchanged — only the parse path differs.
- **`ebpftracer/nodejs.go`**: Node.js libuv I/O callback uprobe attach uses the
  cache. All six callback symbols (`uv__io_poll` plus 5 callbacks) are now
  resolved in one ELF pass instead of separate `GetSymbol` calls.
- **`ebpftracer/python.go`**: Python `pthread_cond_timedwait` uprobe attach
  uses the cache for libc / musl / libpthread.
- **`ebpftracer/l7/postgres.go`**: `PostgresParser.preparedStatements` map is
  capped at 1024 entries per connection. On overflow, evicts one random entry
  (Go's map iteration order is randomized) before inserting the new statement.
  Mitigates unbounded growth from clients that never CLOSE prepared statements
  (ORMs, pgbouncer session pooling).
- **`ebpftracer/l7/mysql.go`**: same 1024-statement cap on
  `MysqlParser.preparedStatements`.

### Added

- **`containers/memdiag.go`** (new): periodic memory-diagnostic logger that
  writes per-container map sizes, Go runtime accounting (heap/stack/sys/cgo
  fields), and `/proc/self/status` (`VmRSS`, `RssAnon`, `RssFile`, …) to
  stderr. Output bypasses the klog rate limiter so lines are never silently
  dropped. Optionally mirrors to an fsync'd file so the last tick before SIGKILL
  survives. Top-N "worst" containers logged per leak indicator (active
  connections, L7 destinations, prepared-statement count, HTTP/2 streams).
- **`containers/heapdump.go`** (new): auto-rotating heap-profile dumper.
  Triggers on `HeapAlloc` growth (configurable threshold) or on a fixed
  cadence (configurable interval). Gzipped pprof; keeps the last N files.
- **`main.go`**: optional dedicated `/debug/pprof/*` HTTP listener
  (`--pprof-listen`), defaulting to loopback for `hostNetwork: true` DaemonSet
  safety. Reachable via `kubectl port-forward`.
- **`main.go`**: optional `runtime.MemProfileRate` tuning at startup via
  `--mem-profile-rate`.
- **`profiling/profiling.go`**: early-return in `Init()` when
  `--disable-profiling=true`, before any Pyroscope `ebpfspy.Session` is
  constructed. Useful when investigating whether the profiling pipeline (its
  own BPF maps + symbol caches) contributes to memory pressure.
- **`ebpftracer/l7/http2.go`**: `Http2Parser.ActiveRequestsLen()` accessor
  (read-only, for memdiag introspection).

### Added — ports from upstream `coroot/coroot-node-agent` v1.32.5

- **`--instrumentation-delay`** (default `30s`): delays Python GIL / Node.js
  event-loop / .NET uprobe attachment in `Process.instrument()` via a
  cancellable select. Short-lived processes that exit before the delay elapses
  are never instrumented. Directly mitigates the startup uprobe-attach burst
  that's been the leading contributor to RSS spikes.
- **`--min-container-age`** (default `30s`): `Container.Collect()` early-returns
  for containers younger than this. Removes cardinality from cronjob / init /
  job pods that exist for seconds.
- **`--max-fqdns-per-container`** (default `50`): per-container `seenFQDNs` set;
  once full, additional FQDNs are bucketed under `~other` for the DNS metric
  label. Bounds DNS-metric label cardinality.

### Added — new operator flags (custom to this fork)

| Flag | Env | Default | Purpose |
|---|---|---|---|
| `--disable-profiling` | `DISABLE_PROFILING` | `false` | Skip Pyroscope ebpf session entirely |
| `--memdiag-interval` | `MEMDIAG_INTERVAL` | `0s` | Periodic memory-diagnostic cadence (0 = off) |
| `--memdiag-file` | `MEMDIAG_FILE` | `` | Fsync'd mirror for OOM post-mortem |
| `--heap-dump-dir` | `HEAP_DUMP_DIR` | `` | Auto heap-dump destination directory |
| `--heap-dump-every` | `HEAP_DUMP_EVERY` | `0` | Dump every N memdiag ticks |
| `--heap-dump-on-growth-mb` | `HEAP_DUMP_ON_GROWTH_MB` | `50` | Dump when `HeapAlloc` grows by this much |
| `--heap-dump-keep` | `HEAP_DUMP_KEEP` | `8` | Heap-dump retention count |
| `--mem-profile-rate` | `MEM_PROFILE_RATE` | `0` | `runtime.MemProfileRate` at startup |
| `--pprof-listen` | `PPROF_LISTEN` | `` | Dedicated `/debug/pprof/*` listener address |

### Documentation

- **`MEMORY_LEAK_INVESTIGATION.md`** (new): full investigation timeline. Every
  suspect tested and ruled out. Top hypotheses parked (kernel-side perf_event
  buffer accumulation from uretprobes; BPF map preallocation). Recommended
  next steps if investigation resumes. Diagnostic-tool reference.
- **`.gitignore`**: ignore pprof artifacts (`*.pb.gz`, `*.pprof`), JMeter
  outputs (`*.jmx`, `jmeter.log`), and editor metadata (`.claude/`).

### Known limitations

- **OOM root cause remains undetermined.** All Go-side memory sources have
  been audited and bounded. The remaining suspect is kernel-side: either
  uretprobe per-CPU `perf_event` buffer accumulation, or BPF map
  preallocation, charged to the cgroup memcg. See
  [MEMORY_LEAK_INVESTIGATION.md](MEMORY_LEAK_INVESTIGATION.md) section
  "Open questions" for the missing diagnostic (`/sys/fs/cgroup/.../memory.stat`).
- **Recommended production posture**: use `--container-allowlist` to scope the
  agent to only the workloads you actively monitor. New cluster workloads then
  cannot trigger an OOM regression without an explicit opt-in.

---

## [v1.0.5] — 2026-01-06

### Changed

- CI/CD: improved version handling in CD workflow; updated GitHub Release
  action.

[Diff: v1.0.0...v1.0.5](https://github.com/Codifinary/codexray-node-agent/compare/v1.0.0...v1.0.5)

## [v2.0.0] / [v1.0.1] — 2026-01-06

### Changed

- CI workflow streamlined: removed redundant step IDs, enhanced summary
  generation.
- CI summary now includes detailed job status and overall outcome.
- CI / CD workflows simplified: removed redundant check-ci jobs, enhanced
  error handling.

Note: `v2.0.0` and `v1.0.1` point at the same commit (`8fe3142`). Tag history
is non-monotonic; the SemVer story will be cleaned up in future releases.

## [v1.0.0] — 2026-01-03

Initial tagged release.

### Added

- CI/CD with release and develop branch automation.
- CI status check before CD runs.
- GitHub token in checkout steps across all workflows.

---

## Releasing — workflow for maintainers

When cutting a release:

1. **Finalize `[Unreleased]`**:
   - Move the contents under a new `## [vX.Y.Z] — YYYY-MM-DD` heading.
   - Leave the `[Unreleased]` heading empty above (with `### Added`, `### Fixed`
     etc. subheadings) so the next merge has a place to land.

2. **Pick the version**:
   - **MAJOR** when there's a breaking change to the agent's CLI flags, env
     vars, or metric schema.
   - **MINOR** when adding flags, metrics, or operator-visible features
     without breaking existing usage.
   - **PATCH** for bug fixes that don't add or change operator-visible surface.

   For the current `[Unreleased]` (memory-leak mitigations), the right bump
   is **MINOR**: new flags + new diagnostic infrastructure + fixes, no
   breaking changes. Next likely version: **v1.1.0**.

3. **Tag and push**:
   ```sh
   VERSION=v1.1.0
   git tag -a $VERSION -m "Release $VERSION

   See CHANGELOG.md for full notes."
   git push origin $VERSION
   ```

4. **Update Docker image tag** in any consuming manifests (`manifests/*.yaml`,
   Helm chart values, etc.).

5. **Open a GitHub Release** if you use them: copy the new section from this
   file into the release notes.

### Pre-release / hotfix

- Pre-releases follow the SemVer pre-release convention: `v1.1.0-rc.1`,
  `v1.1.0-beta.2`, `v1.1.0-alpha.1`.
- Hotfixes against a released line use a PATCH bump: `v1.1.1`.

### When backporting an upstream change

If a change is ported verbatim from `coroot/coroot-node-agent`, reference the
upstream commit SHA in the changelog entry so the relationship is auditable.
Example:

```markdown
### Fixed
- Port upstream coroot/coroot-node-agent@aa5f2cdd91 — TLS `.text` section
  streaming to reduce allocations.
```

[Unreleased]: https://github.com/Codifinary/codexray-node-agent/compare/v1.0.5...HEAD
[v1.0.5]: https://github.com/Codifinary/codexray-node-agent/releases/tag/v1.0.5
[v2.0.0]: https://github.com/Codifinary/codexray-node-agent/releases/tag/v2.0.0
[v1.0.1]: https://github.com/Codifinary/codexray-node-agent/releases/tag/v1.0.1
[v1.0.0]: https://github.com/Codifinary/codexray-node-agent/releases/tag/v1.0.0
