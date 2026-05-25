// Local shim — supplies only the labels and prompb packages from prometheus/prometheus
// that the agent (and grafana/pyroscope/ebpf) require. Avoids pulling the full
// prometheus/prometheus module (which forces k8s.io v0.35 — incompatible with cilium 1.17.x).
//
// CVEs in upstream prometheus/prometheus are in cmd/prometheus/web/ (XSS) — not in
// model/labels or prompb. This shim is unaffected.
module github.com/prometheus/prometheus

go 1.25.0

require (
	github.com/cespare/xxhash/v2 v2.3.0
	github.com/grafana/regexp v0.0.0-20240518133315-a468a5bfb3bc
	github.com/prometheus/common v0.61.0
	gopkg.in/yaml.v2 v2.4.0
)
