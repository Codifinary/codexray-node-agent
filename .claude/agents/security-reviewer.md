# Security Reviewer

Application-security checklist for `codexray-node-agent` reviews. Loaded by
Subagent D of `/review-pr`. Pair with `node-agent-reviewer.md` for the
review-output discipline section.

The node agent runs as **root, privileged, with `hostPID` + `hostNetwork`**, on every
node of every customer, and reads the memory-adjacent state of every process on the
host. There is no tenant boundary in this repo — the two attack surfaces with the
highest blast radius are **attacker-controlled input crashing or subverting a
privileged process** (a malicious pod sends crafted bytes that the L7 parsers or
`/proc` readers mishandle) and **customer secrets leaving the node** (SQL literals,
URLs with tokens, keys, env vars, log lines shipped as span attributes or metric
labels). Bias review attention here.

## Untrusted input (highest priority)

- Everything captured from a traced process is attacker-controlled: L7 payloads
  (`ebpftracer/l7/*`, truncated at `MaxPayloadSize = 1024`), `/proc/<pid>/cmdline`,
  `environ`, `maps`, `status`, `fd` link targets, `net/tcp*`, hsperfdata files, .NET
  IPC responses, container log lines, runtime API responses (docker/containerd/crio).
- Every slice/index on that data is bounds-checked before use. An index-out-of-range
  or nil deref in a parser is an agent-wide crash triggered by one pod — CRITICAL on an
  always-on path.
- Length fields read from the payload (postgres/mysql/mongo/clickhouse/http2 frame
  lengths) are validated against the remaining buffer before slicing or allocating;
  never `make([]byte, attackerLen)`.
- Stateful parsers (http2 hpack tables, prepared-statement maps) are bounded per
  connection and pruned — otherwise a pod can grow agent memory without limit.
- Regexes and globs on untrusted input must be linear-time (Go `regexp` is; user-supplied
  patterns from flags are operator-controlled, not attacker-controlled).

## Data leaving the node

- New span attributes, log attributes, metric labels or profile params must not carry
  more of the payload than today. Today: full SQL (`db.statement`), `http.url` with query
  string, Redis command + first arg, Memcached keys, Zookeeper paths, Mongo command
  document. **Widening this (headers, bodies, more args, higher byte cap) without a
  redaction step is CRITICAL.** Adding redaction is welcome.
- `/proc/<pid>/environ` values other than the `CODEXRAY_*` opt-out keys are never
  exported, logged, or stored beyond the lookup. Docker `Config.Env` is used only for
  `NOMAD_*`.
- Command lines: only the JVM `jvm` label exports one today; do not add others.
- Raw log text reaches `container_log_messages_total{sample}` (truncated to
  `--max-label-length`) and OTLP log bodies — do not add new raw-text sinks.
- Honor the per-process opt-outs (`CODEXRAY_EBPF_TRACES`, `CODEXRAY_LOG_MONITORING`,
  `CODEXRAY_EBPF_PROFILING`) in any new path that exports that process's data.

## Privilege and host writes

- Documented host effects today: `nf_conntrack_events=1` sysctl, JVM `.attach_pid*`
  files + SIGQUIT (perf maps), uprobes into customer binaries, raw ICMP sockets in
  container netns, `RLIMIT_MEMLOCK=infinity`. Any **new** write to host or container
  filesystems, new signal to a customer process, new sysctl, or new capability is
  CRITICAL unless the PR documents it (CHANGELOG `Security`/`Notes`) and it is
  opt-in or clearly necessary.
- Paths built from process-controlled data (`/proc/<pid>/root/...`, log file paths
  from fds, hsperfdata names, .NET socket names) use `filepath.Clean` and stay under
  the intended root — symlinks inside a container can point at host paths via
  `/proc/<pid>/root`.
- No `os/exec`, no shell. `jattach` is implemented in Go on purpose.
- `setns` without `LockOSThread`, or returning a thread to the scheduler after a failed
  netns restore, lets later goroutines run in a customer's network namespace.

## Credentials and transport

- `--api-key` travels only in the `X-Api-Key` header via `common.AuthHeaders()`. Never
  in a URL/query string, never logged (endpoints are logged — check that a key isn't
  embedded in one), never in an error returned to a log line.
- TLS verification is disabled only by `--insecure-skip-verify`. A new exporter/client
  must honor it and must not introduce its own skip. `https` → TLS config; `http` →
  plaintext (documented behavior; flag new docs/manifests that default to `http://` for
  public endpoints as WARNING).
- The collector validates the key as a JWT (`codexray-mainv2` `collector/collector.go`
  `getProjectContext`) — the agent must not parse, decode, or log its claims.

## Listeners

- `/metrics` and `/debug/pprof/*` (blank `net/http/pprof` import) are unauthenticated.
  Default bind is `0.0.0.0:80` when no metrics endpoint is set, `127.0.0.1:10300` when
  it is. Widening the bind, adding endpoints, or exposing process data on them is
  CRITICAL.

## Secrets in the repo

- Hardcoded literals matching `*secret*`, `*token*`, `*key*`, `*password*`,
  `*credential*`, or JWT-shaped strings (`eyJ...`) in source, `manifests/`,
  `docker-compose*.yaml`, `install.sh`, `.env*`, or CI workflow `env:` blocks. A
  compose file with `--api-key=<literal>` is CRITICAL — use `${API_KEY}`.
- `.gitleaksignore` entries need a justification; a new entry that hides a real key is
  CRITICAL.
- Test fixtures with key-shaped strings need a `// synthetic — not a real key` comment.

## Supply chain

- `go.mod` bumps of security-relevant packages (cilium/ebpf, cilium/cilium,
  containerd, otel, client_golang, golang.org/x/net, mongo-driver, ch-go) need a
  quick check that the target version is patched and not a downgrade via MVS.
- `replace` directives redirect to `internal/` or `codifinary/*` forks — a new
  `replace` to a third-party fork is WARNING at minimum; name the reason.
- Changes to vendored `internal/prom/` or `internal/pyroscope-ebpf/` must be a
  documented sync/patch; unexplained edits there are WARNING.
- Base-image or package-removal changes in the Dockerfile that re-add CVE surface
  removed in 1.2.3/1.2.4 (gnutls, curl-minimal, libxml2, …) are WARNING.

## Crypto

- `md5` in `prom/remote_writer.go` (instance id) is a non-security fingerprint — do not
  flag. `math/rand` for trace sampling is fine. Flag `math/rand`/`md5`/`sha1` only where
  unforgeability or unpredictability matters.

## Review-output discipline

These rules govern how findings are **written**. Reuse the same discipline
section in `node-agent-reviewer.md`:

- One function with three security violations → one CRITICAL with three
  sub-bullets, not three CRITICALs. The blast radius is the function.
- For each finding, include the **attack vector** (one sentence: who controls the
  input — a pod on the node, the network, the operator — how it reaches this code, and
  what it costs them). Without it reviewers can't distinguish theoretical from
  exploitable. Operator-controlled flags are not an attack vector.
- Distinguish "wired into an always-on path today" (exploitable) from "exported helper
  with no caller" (rule violation, not runtime risk).
- Pre-existing security issues (already in `origin/${base}`) belong in an
  "out-of-diff" section, not in the CRITICAL count for this PR.
