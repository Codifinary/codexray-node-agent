# Changelog

All notable changes to the Codexray Node Agent are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

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
