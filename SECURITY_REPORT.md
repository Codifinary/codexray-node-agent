# SAST / SCA Vulnerability Report — `codexray-node-agent`

**Date:** 2026-05-15
**Scanned commit:** `main` branch (working-tree state)
**Tools used:**
- `gosec` v2 (SAST) — `~/go/bin/gosec -exclude-dir=ebpftracer/ebpf -exclude-dir=vendor ./...`
- `osv-scanner` against `go.mod` (SCA)
- Manual review of `install.sh`, `Dockerfile`, network exposure surface

**Scope:**
- Go source: 85 files, 11,144 LoC (excludes `ebpftracer/ebpf/*.c` and `vendor/`)
- `go.mod` direct + first-level transitive dependencies (OSV lockfile mode)
- Build & install scripts

**Totals:** 31 SAST findings + ~17 known-CVE dependency findings + 28 Go stdlib advisories + 5 manual/configuration findings.

**Caveat:** `govulncheck` source-mode (reachability analysis) was attempted but blocked by an upstream API mismatch in `github.com/cilium/cilium v1.17.2` against the resolved version of `github.com/cilium/ebpf` — so SCA results here are **lockfile-mode (no reachability)**, which over-counts relative to what is actually exploitable.

---

## CRITICAL (1)

| ID | Where | Issue |
|---|---|---|
| **GO-2026-4762 / GHSA-p77j-4mvh-x3m3** (CVSS **9.1**) | `google.golang.org/grpc v1.69.2` (transitive via cilium/otel/containerd) | Critical vulnerability in gRPC-Go. **Bump to latest patched 1.69.x or 1.70+.** |

---

## HIGH (12)

### Network exposure / information disclosure

| ID | Location | Issue |
|---|---|---|
| **pprof exposed by default** | `main.go:10` (`_ "net/http/pprof"`) + `main.go:182` + `flags/flags.go:16` (default `0.0.0.0:80`) | Importing `net/http/pprof` registers `/debug/pprof/*` handlers on the **default mux**, which is what `http.ListenAndServe` serves. Because the agent listens on `0.0.0.0:80` by default and runs as **root with CAP_BPF**, anyone reachable on the node's network can:<br>• read full memory profiles (`/debug/pprof/heap`)<br>• read all goroutine stacks with arguments — potential token/secret leakage (`/debug/pprof/goroutine?debug=2`)<br>• trigger CPU profiling (DoS lever)<br>**Fix:** use a dedicated `*http.ServeMux` for `/metrics`; only mount pprof on an opt-in localhost-only listener. |
| **No HTTP server timeouts** (G114 / CWE-676) | `main.go:182` | `http.ListenAndServe(...)` has no `ReadTimeout`/`WriteTimeout`/`IdleTimeout` → slowloris DoS. Replace with `&http.Server{ReadHeaderTimeout: 5*time.Second, ...}`. |

### Dependency CVEs (CVSS ≥ 7.0)

| ID | Package | CVSS | Note |
|---|---|---|---|
| GO-2026-4887 / GHSA-x744-4wpc-v9h2 | `github.com/docker/docker 27.4.0+incompatible` | 8.8 | Bump moby/docker. |
| GHSA-gj49-89wh-h4gj | `github.com/cilium/cilium 1.17.2` | 7.9 | Bump cilium. |
| GO-2025-4100 / GHSA-pwhc-rpq9-4c8w | `github.com/containerd/containerd 1.6.38` | 7.3 | Containerd path-traversal. Bump to 1.6.39+ / 1.7.x. |
| GO-2025-4098 / GHSA-cgrx-mc8f-2prm | `github.com/opencontainers/selinux 1.11.0` | 7.3 | Bump. |
| GHSA-hfvc-g4fc-pqhx | `go.opentelemetry.io/otel/sdk 1.31.0` | 7.3 | Bump otel SDK. |
| GO-2026-4394 / GHSA-9h8m-3fm2-qjrq | `go.opentelemetry.io/otel/sdk 1.31.0` | 7.0 | Bump otel SDK. |
| GHSA-8rm2-7qqf-34qm | `github.com/prometheus/prometheus 0.51.2` | 7.5 | Bump prometheus. |
| GHSA-wg65-39gg-5wfj | `github.com/prometheus/prometheus 0.51.2` | 7.5 | Bump prometheus. |
| GO-2025-4108 / GHSA-m6hq-p25p-ffr2 | `github.com/containerd/containerd 1.6.38` | 6.9 | Bump containerd. |

### SAST — integer overflow (G115 / CWE-190)

5 instances where `uint64` values from eBPF ring-buffer events are cast to `int64`:
- `ebpftracer/tracer.go:408` (`l7.Status` cast)
- `ebpftracer/tracer.go:448` (Timestamp cast)
- `ebpftracer/l7/http2.go:161`
- `ebpftracer/elf.go:51`
- `ebpftracer/elf.go:79`

Low *practical* impact (kernel-supplied values are bounded), but a malicious / malformed eBPF event could produce negative timestamps or status codes. Add bounds checks or annotate with `//#nosec G115` and a comment.

---

## MEDIUM (~28)

### Configuration / misuse

| ID | Location | Issue |
|---|---|---|
| **TLS verification can be disabled cluster-wide via env var** (G402 / CWE-295) | `tracing/tracing.go:64`, `logs/otel.go:44`, `prom/remote_writer.go:74`, `profiling/profiling.go:45` | All four outbound TLS clients honor the `INSECURE_SKIP_VERIFY` env var (`flags/flags.go:51`). Default is `false` (good), but a single env var flip disables cert validation everywhere. Consider removing the flag entirely, or scoping it per-endpoint. |
| **Weak hash MD5** (G501/G401 / CWE-327/328) | `prom/remote_writer.go:8` (import), `prom/remote_writer.go:58` (use) | If used for de-dup/sharding it's fine; if used for any integrity/identity check, switch to SHA-256. Annotate with `//#nosec` + justification if intentional. |

### SAST — file inclusion via variable (G304 / CWE-22) — 12 instances

| Location | Notes |
|---|---|
| `common/file.go:14` (`ReadIntFromFile`) | Helper used with `/proc`, `/sys` paths. |
| `common/file.go:22` (`ReadUintFromFile`) | Same. |
| `cgroup/utils.go:16` (`readVariablesFromFile`) | cgroupfs paths. |
| `cgroup/psi.go:60` | `/sys/fs/cgroup/.../pressure`. |
| `cgroup/io.go:114` (`readBlkioStatFile`) | blkio stat files. |
| `cgroup/cgroup.go:124` (`NewFromProcessCgroupFile`) | `/proc/<pid>/cgroup`. |
| `proc/net.go:44` (`readSockets`) | `/proc/<pid>/net/...`. |
| `node/uptime.go:16` | `/proc/uptime`. |
| `node/memory.go:19` | `/proc/meminfo`. |
| `node/cpu.go:23` | `/proc/stat`. |
| `logs/tail_reader.go:44` | Container log files. |
| `ebpftracer/tracer.go:470` (`isCtxExtraPaddingRequired`) | tracefs path. |
| `prom/remote_writer.go:138` (`send`) | WAL files written by agent itself. |

**Context:** all paths come from kernel-managed roots (`/proc`, `/sys`, cgroupfs) or agent-owned WAL — not user input. Real-world exploitability requires an attacker who can already write into those roots (i.e. root). Recommend pinning with `os.Root` (Go 1.24+) on the proc/cgroup roots to scope traversal.

### SAST — file permissions (G306 / CWE-276)

| Location | Issue |
|---|---|
| `jvm/jattach.go:43` | `os.WriteFile(attachFile, ..., 0660)` — JVM attach trigger file group-readable. Tighten to `0600`. |
| `ebpftracer/tracer.go:504` | `os.WriteFile(nfConntrackEventsParameterPath, ..., 0644)` — writing `/proc/sys/...` (kernel ignores mode). Suppress with `//#nosec G306` + comment. |

### Dependency CVEs (CVSS 4.0 – 6.9)

| ID | Package | CVSS |
|---|---|---|
| GO-2025-3603 / GHSA-m454-3xv7-qj85 | `github.com/ClickHouse/ch-go 0.62.0` | 5.9 |
| GO-2025-3635 / GHSA-5vxx-c285-pcq4 | `github.com/cilium/cilium 1.17.2` | 4.0 |
| GO-2026-4856 / GHSA-hxv8-4j4r-cqgv | `github.com/cilium/cilium 1.17.2` | 5.4 |
| GO-2025-4167 | `github.com/cilium/cilium 1.17.2` | — |
| GHSA-4vq8-7jfc-9cvp | `github.com/docker/docker 27.4.0+incompatible` | 3.3 |
| GO-2026-4883 / GHSA-pxq6-2prw-chj9 | `github.com/docker/docker 27.4.0+incompatible` | 6.8 |
| GHSA-vffh-x6r8-xx99 | `github.com/prometheus/prometheus 0.51.2` | 6.1 |
| GHSA-fw8g-cg8f-9j28 | `github.com/prometheus/prometheus 0.51.2` | — |
| GO-2025-3922 / GHSA-jc7w-c686-c4v9 | `github.com/ulikunitz/xz 0.5.12` | 5.3 |
| GHSA-w8rr-5gcm-pp58 | `go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp 1.28.0` | 5.3 |
| GO-2026-4918 | `golang.org/x/net 0.46.0` | — |

### Go stdlib advisories — 28 findings against `go 1.24.7`

All bound to the `go` directive in `go.mod` (currently `go 1.24.7`):

`GO-2025-4006`, `GO-2025-4007`, `GO-2025-4008`, `GO-2025-4009`, `GO-2025-4010`, `GO-2025-4011`, `GO-2025-4012`, `GO-2025-4013`, `GO-2025-4014`, `GO-2025-4015`, `GO-2025-4155`, `GO-2025-4175`, `GO-2026-4337`, `GO-2026-4340`, `GO-2026-4341`, `GO-2026-4342`, `GO-2026-4601`, `GO-2026-4602`, `GO-2026-4603`, `GO-2026-4864`, `GO-2026-4865`, `GO-2026-4869`, `GO-2026-4870`, `GO-2026-4918`, `GO-2026-4946`, `GO-2026-4947`, `GO-2026-4971`, `GO-2026-4976`, `GO-2026-4977`, `GO-2026-4980`, `GO-2026-4981`, `GO-2026-4982`, `GO-2026-4986`.

**Bump the `go` directive to latest 1.24.x or 1.25.x** and rebuild — this clears all 28 in one change.

---

## LOW (5)

| ID | Location | Issue |
|---|---|---|
| G404 / CWE-338 — weak RNG | `tracing/tracing.go:93` | `math/rand` used in tracing path. If for IDs, switch to `crypto/rand`. If for jitter only, switch to `math/rand/v2` and document. |
| G104 / CWE-703 — unhandled errors | `ebpftracer/tls.go:61`, `ebpftracer/tls.go:202` | `l.Close()` in cleanup loop. Explicit `_ = l.Close()`. |
| `.env` file committed | `.env` (contents: `VERSION=v1`) | No secret in current value, but committing `.env` is a habit-smell. Drop and add to `.gitignore`. |

---

## INFORMATIONAL / Hardening (4)

| ID | Location | Issue |
|---|---|---|
| **Build-arg secret leakage** | `Dockerfile:23-27` | `GHCR_PAT` passed as `ARG` → token written to `~/.git-credentials` in the builder layer. Final runtime image is unaffected (only the compiled binary is copied), but the **builder image (if cached or pushed) and Docker build history leak the PAT**. Switch to BuildKit `--mount=type=secret`. |
| **Container runs as root** | `Dockerfile:55-59` | No `USER` directive on final `ubi9` image. The agent legitimately needs CAP_BPF / CAP_PERFMON so non-root is hard, but consider running as a dedicated UID with file capabilities. |
| **install.sh has no checksum / signature verification** | `install.sh:121-136` | Downloads binary over HTTPS but never verifies sha256/GPG before `chmod 755` + install as root. A compromised GitHub release or MITM can ship arbitrary code that runs privileged. Publish a `SHA256SUMS` (signed) and verify before install. |
| **Stray test artifacts in repo root** | `HTTP Request.jmx`, `jmeter.log`, `nginx.yaml` | Untracked, not in `.gitignore`. Delete or ignore. |

---

## Summary table

| Severity | Count | Type breakdown |
|---|---|---|
| Critical | 1 | SCA (gRPC 9.1) |
| High | 12 | 2 SAST (pprof, no timeouts) + 5 SAST G115 overflow + 9 SCA CVE |
| Medium | ~28 | 12 SAST G304 + 2 SAST G306 + 2 SAST G401/G402/G501 + 11 SCA CVE + see also 28 stdlib advisories below |
| Stdlib advisories | 28 | All against `go 1.24.7` — fixed by bumping `go` directive |
| Low | 5 | SAST G404 + 2× G104 + repo hygiene |
| Informational | 4 | Docker / install hardening |

---

## Top-3 action items (highest leverage)

1. **Bind metrics to a private interface and split pprof off the default mux** — single-file change that closes the pprof information-disclosure window cluster-wide. (`main.go:182`)

2. **Bump dependency floor in `go.mod`**:
   - `grpc → latest 1.69.x patched`
   - `docker/docker → latest`
   - `cilium/cilium → 1.17.x patched`
   - `containerd → 1.6.39+`
   - `otel/sdk → 1.32+`
   - `prometheus → 0.55+`
   - `go` directive → `1.24.10` (or 1.25.x)

   This single round of bumps eliminates the Critical + most of the High SCA findings + all 28 stdlib advisories.

3. **Add HTTP server timeouts** (`main.go:182`) — kills the slowloris exposure in 3 lines.

---

## Comparison with Black Duck scan (56 High / 216 Medium)

The Black Duck scan returns substantially more findings than the report above. Likely reasons for the gap:

1. **Black Duck almost certainly scanned the container image, not just Go source.** The Dockerfile builds on `debian:bullseye` (builder) and ships `registry.access.redhat.com/ubi9/ubi` (runtime). UBI9 + Debian apt/rpm metadata carry hundreds of CVEs in openssl, glibc, curl, libsystemd, expat, zlib, gnutls, etc. — many won't-fix or low-EPSS, but Black Duck reports them all. This likely accounts for the majority of the gap.

2. **BDSA (Black Duck Security Advisories) is a superset of OSV/NVD.** BD researchers publish advisories before NVD assignment and backport advisories to older minor versions OSV ignores.

3. **Counting methodology.** OSV dedupes one CVE per package-version. Black Duck typically counts each (CVE × consuming-component) pair — so one stdlib CVE consumed by 8 modules can show as 8 findings.

4. **No reachability filtering** in Black Duck — every vulnerable dependency is counted regardless of whether the vulnerable function is actually called.

5. **Severity bucketing.** This report downgrades some findings based on context (e.g. G304 file-inclusion on kernel-managed `/proc` paths). Black Duck reports raw CVSS.

To triage the BD report, separate **Go-module findings** (this report's responsibility) from **OS-package findings** (fix by base-image swap, e.g. distroless runtime). The Go-module Highs in the BD report should be a superset of section "HIGH → Dependency CVEs" above; anything additional is net-new.
