# Changelog

All notable changes to the Codexray Node Agent are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [1.2.3] — 2026-06-13

Security-only release. Addresses HIGH-severity findings reported in the Black
Duck scan dated 2026-06-12 by bumping Go-side dependencies to their latest
patched releases. No functional or API changes.

### Security
- **Go toolchain**: `1.25.10` → `1.25.11` — clears 9 stdlib HIGH CVEs reported
  by Black Duck (xss-via-URL-escaping, DoS, path-traversal, checksum-bypass).
  ([Dockerfile](Dockerfile))
- **`github.com/cilium/cilium`**: `v1.17.2` → `v1.17.16` — clears
  CVE-2026-41520 (HIGH) and 1 transitive MEDIUM. Patch-only bump within the
  1.17.x line; API-compatible. ([go.mod](go.mod))
- **`go.mongodb.org/mongo-driver`**: `v1.14.0` → `v1.17.9` — clears 1 HIGH
  (heap out-of-bounds read). ([go.mod](go.mod))
- **`golang.org/x/net`**: `v0.55.0` → `v0.56.0` — clears 4 HIGH DoS findings
  (HTML parser, HTTP/2 server, net validation). ([go.mod](go.mod))
- **`github.com/ulikunitz/xz`**: `v0.5.12` → `v0.5.15` — clears 1 MEDIUM
  (CVE-2025-58058). Indirect dependency. ([go.mod](go.mod))

### Notes
- Trivy scan on the resulting image reports **0 CRITICAL / 0 HIGH / 30 MEDIUM
  / 18 LOW** — every remaining item is a Red Hat UBI base-OS package marked
  `affected` or `will_not_fix` upstream by Red Hat and cannot be removed
  without breaking the container runtime.
- Verified against staging Kubernetes (10.10.11.60) — DaemonSet rollout
  clean, eBPF tracking active, remote-write to collector working.

## [1.2.2] — 2026-05-30

### Added
- `--min-container-age` / `MIN_CONTAINER_AGE` (default `30s`): suppresses metric emission for containers younger than the configured threshold, reducing high-cardinality series from short-lived job/cronjob pods. Ported from upstream coroot-node-agent; the gate falls back to the cgroup directory mtime when taskstats hasn't populated `startedAt`, so containers whose initial taskstats lookup failed (PID-vs-TGID races, restricted netlink caps, missing `CONFIG_TASKSTATS_NETLINK`) still emit metrics instead of being permanently filtered.
- `--instrumentation-delay` / `INSTRUMENTATION_DELAY` (default `30s`): delays attaching language-runtime probes (Python GIL, Node.js event loop, etc.) to newly started processes, avoiding probe churn on processes that exit during startup. Ported from upstream coroot-node-agent.

### Upgrade notes
- The `30s` defaults change behavior for existing deployments: containers younger than 30s will be suppressed from metric output, and language-runtime probes attach 30s after process start. Set either flag (or env var) to `0` to restore pre-1.2.1 behavior.

## [1.2.0] — 2026-05-28

### Added
- Integrated eBPF-based Python profiling engine and symbolication utilities ([eead3e7](https://github.com/Codifinary/codexray-node-agent/commit/eead3e7)).

### Changed
- Relicensed to **AGPL-3.0** while preserving the upstream Apache-2.0 attribution from `coroot-node-agent` ([#36](https://github.com/Codifinary/codexray-node-agent/pull/36), [#35](https://github.com/Codifinary/codexray-node-agent/pull/35)).
- Added AGPL-3.0 license metadata labels to the Docker image.

### Security
- Additional CVE remediation pass on top of v1.1.0 ([#33](https://github.com/Codifinary/codexray-node-agent/pull/33)).

## [1.1.0] — 2026-05-25

### Added
- Optional GPU support for the node agent ([#24](https://github.com/Codifinary/codexray-node-agent/pull/24)).

### Security
- Resolved Black Duck security findings with an overall 85% reduction in flagged components ([#31](https://github.com/Codifinary/codexray-node-agent/pull/31)).
- Vendored `prometheus/util/fmtutil` into `internal/prom` shim to remove vulnerable transitive dependency.
- Marked vendored prom shim and auto-generated files as `linguist-vendored` / `linguist-generated` so Black Duck and language stats ignore them.

### Changed
- Updated copyright headers across the project to **Codexray** and added supporting legal documentation (`LICENSE`, `LICENSING.md`, `NOTICE`) ([#28](https://github.com/Codifinary/codexray-node-agent/pull/28)).

### Docs
- Added `MEMORY_LEAK_INVESTIGATION.md` documenting uprobe-attachment memory growth investigation.
- Removed obsolete agent settings file.

### Reverted
- Reverted `feat: implement symbol caching to reduce memory consumption during uprobe attachment` (2e8aa9f) — landed in error and rolled back pending further investigation.

## [1.0.5] — 2026-01-06

### Changed
- Improved version handling in the CD workflow and updated the GitHub Release action ([#22](https://github.com/Codifinary/codexray-node-agent/pull/22)).
- Streamlined CI workflow by removing redundant step IDs and enhancing summary generation ([#20](https://github.com/Codifinary/codexray-node-agent/pull/20)).
- Enhanced CI summary with detailed job status and overall outcome.
- Simplified CI and CD workflows by removing redundant `check-ci` jobs and improving error handling.

## [1.0.0] — 2026-01-03

### Fixed
- Fixed duplicate permission error in workflows ([#17](https://github.com/Codifinary/codexray-node-agent/pull/17)).
- Fixed CI status check errors that were blocking CD runs ([#12](https://github.com/Codifinary/codexray-node-agent/pull/12)).

### Changed
- Updated CD to verify CI status check before running.
- Added explicit token to checkout steps across all workflows.
- Added release and develop branch CD pipelines.

---

_The entries below predate the adoption of Semantic Versioning and are kept as a historical record, grouped by merge date._

## [2025-12-18] — CI/CD Pipeline

### Added
- Created CI and CD pipelines ([#9](https://github.com/Codifinary/codexray-node-agent/pull/9)).
- Added CI status check before running CD ([#10](https://github.com/Codifinary/codexray-node-agent/pull/10)).

### Fixed
- Fixed image-name error.
- Corrected commit-tag used for the Docker image.

## [2025-12-16] — Application Updates

### Changed
- Refactored application-type detection to simplify return values for CodexRay editions ([#8](https://github.com/Codifinary/codexray-node-agent/pull/8)).
- Updated `Dockerfile` and both workflow files to handle Git authentication during Docker builds.
- Updated agent and authentication format for private-repo access.
- Added credential helper and PAT-based authentication for private repositories.

### Fixed
- Fixed GPU-module import bug.
- Added retries to resolve transient network errors during builds.

## [2025-08-19] — Naming & CI/CD

### Changed
- Resolved naming issues and updated CI/CD ([#7](https://github.com/Codifinary/codexray-node-agent/pull/7)).

### Reverted
- Reverted "Fix/modify name" ([#6](https://github.com/Codifinary/codexray-node-agent/pull/6)).

## [2025-06-11] — Domain & Naming

### Changed
- Changed domain to `.io` ([#5](https://github.com/Codifinary/codexray-node-agent/pull/5)).
- Fixed agent name in OpenTelemetry exporter configuration.

## [2025-02-21] — Release Pipeline Polish

### Changed
- Changed release name used for the Docker image ([#4](https://github.com/Codifinary/codexray-node-agent/pull/4)).
- Added systemd header file in workflow ([#3](https://github.com/Codifinary/codexray-node-agent/pull/3)).
- Bumped artifact action version to v4 ([#2](https://github.com/Codifinary/codexray-node-agent/pull/2)).
- Fixed Dockerfile and `install.sh` per code review ([#1](https://github.com/Codifinary/codexray-node-agent/pull/1)).

## [2025-02-20] — Module Rename

### Changed
- Renamed Go module to Codexray.
- Updated Dockerfile and `install.sh` for the new module path.
- Reworked CI/CD configuration to match.

## [2025-02-01] — Build & Release Infrastructure

### Added
- New workflows to build and replace with Codexray binaries.
- Standalone registry support.

### Changed
- Multiple iterations updating the container registry and `build-push-image.yaml` workflow.

## [2025-01-30] — Initial CI/CD

### Added
- Initial `build-push-image.yaml` workflow and `Dockerfile` updates.

### Removed
- Removed legacy `ci.yml` and `release.yml` workflows superseded by the new pipeline.

## [2025-01-12] — Initial Release

### Added
- Initial commit of the Codexray Node Agent.
