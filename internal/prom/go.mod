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
	github.com/gogo/protobuf v1.3.2
	github.com/grafana/regexp v0.0.0-20240518133315-a468a5bfb3bc
	github.com/prometheus/client_model v0.6.1
	github.com/prometheus/common v0.61.0
	github.com/stretchr/testify v1.10.0
)

require (
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/kr/pretty v0.3.1 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	google.golang.org/protobuf v1.35.2 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)
