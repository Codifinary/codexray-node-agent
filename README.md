# Codexray-node-agent

[![Go Report Card](https://goreportcard.com/badge/github.com/codifinary/codexray-node-agent)](https://goreportcard.com/report/github.com/codifinary/codexray-node-agent)
[![License](https://img.shields.io/badge/License-AGPL_v3-blue.svg)](https://www.gnu.org/licenses/agpl-3.0)

> Licensed under **AGPL-3.0** (see [LICENSE](LICENSE)). Incorporates [coroot/coroot-node-agent](https://github.com/coroot/coroot-node-agent) under Apache-2.0 (see [LICENSE.APACHE-2.0](LICENSE.APACHE-2.0)) and eBPF programs under GPL-2.0. See [NOTICE](NOTICE) and [LICENSING.md](LICENSING.md) for attribution and licensing details.

The agent gathers metrics related to a node and the containers running on it, and it exposes them in the Prometheus format.

It uses eBPF to track container related events such as TCP connects, so the minimum supported Linux kernel version is 4.16.

## Features

### TCP connection tracing

To provide visibility into the relationships between services, the agent traces containers TCP events, such as *connect()* and *listen()*.

Exported metrics are useful for:
* Obtaining an actual map of inter-service communications. It doesn't require integration of distributed tracing frameworks into your code.
* Detecting connections errors from one service to another.
* Measuring network latency between containers, nodes and availability zones.

### Log patterns extraction

Log management is usually quite expensive. In most cases, you do not need to analyze each event individually.
It is enough to extract recurring patterns and the number of the related events.

This approach drastically reduces the amount of data required for express log analysis.

The agent discovers container logs and parses them right on the node.

At the moment the following sources are supported:
* Direct logging to files in */var/log/*
* Journald
* Dockerd (JSON file driver)
* Containerd (CRI logs)

### Delay accounting

[Delay accounting](https://www.kernel.org/doc/html/latest/accounting/delay-accounting.html) allows engineers to accurately
identify situations where a container is experiencing a lack of CPU time or waiting for I/O.

The agent gathers per-process counters through [Netlink](https://man7.org/linux/man-pages/man7/netlink.7.html) and aggregates them into per-container metrics:
* `container_resources_cpu_delay_seconds_total`
* `container_resources_disk_delay_seconds_total`

### Out-of-memory events tracing

The `container_oom_kills_total` metric shows that a container has been terminated by the OOM killer.

### Instance meta information

If a node is a cloud instance, the agent identifies a cloud provider and collects additional information using the related metadata services.

Supported cloud providers: [AWS](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/instancedata-data-retrieval.html), [GCP](https://cloud.google.com/compute/docs/metadata/overview), [Azure](https://docs.microsoft.com/en-us/azure/virtual-machines/linux/instance-metadata-service?tabs=linux), [Hetzner](https://docs.hetzner.cloud/#server-metadata)

Collected info:
* AccountID
* InstanceID
* Instance/machine type
* Region
* AvailabilityZone
* AvailabilityZoneId (AWS only)
* LifeCycle: on-demand/spot (AWS and GCP only)
* Private & Public IP addresses

## Building

The agent targets Linux (it uses eBPF and Linux-only syscalls). All dependencies are public — no private credentials are required to build.

### Docker (recommended)

```sh
docker build -t codexray-node-agent .
```

For GPU support:

```sh
docker build --build-arg BUILD_GPU=true -t codexray-node-agent-gpu .
```

### Local build (Linux)

Requires Go 1.25+ and `libsystemd-dev`.

```sh
CGO_ENABLED=1 go build -o codexray-node-agent .
```

## Dependencies

The build is fully reproducible from public sources. A few dependencies are pulled in non-standard ways:

| Library | How it's consumed | License |
|---|---|---|
| [codifinary/logparser](https://github.com/Codifinary/logparser) | direct Go module dependency (`go.mod` `require`) | Apache-2.0 |
| [codifinary/dotnetdiag](https://github.com/Codifinary/dotnetdiag) | `replace` directive for `pyroscope-io/dotnetdiag` | Apache-2.0 |
| [prometheus/prometheus](https://github.com/prometheus/prometheus) (subset) | vendored under [`internal/prom/`](internal/prom/) as a build-time shim — only `model/labels`, `prompb`, and `util/fmtutil` are copied to avoid pulling the full Prometheus module (which forces an incompatible `k8s.io` version). See [`internal/prom/go.mod`](internal/prom/go.mod) for rationale. | Apache-2.0 |
| [grafana/pyroscope/ebpf](https://github.com/grafana/pyroscope/tree/main/ebpf) | vendored under [`internal/pyroscope-ebpf/`](internal/pyroscope-ebpf/) and wired via a local `replace` directive | Apache-2.0 |

Attribution and license texts for all vendored code are preserved at [`internal/prom/LICENSE`](internal/prom/LICENSE), [`internal/prom/NOTICE`](internal/prom/NOTICE), and [`internal/pyroscope-ebpf/LICENSE`](internal/pyroscope-ebpf/LICENSE), in accordance with Apache-2.0 §4.

## Contributing
To start contributing, check out our [Contributing Guide](CONTRIBUTING.md).

## License

codexray-node-agent is licensed under the [GNU Affero General Public License, Version 3.0](LICENSE) (AGPL-3.0).

It incorporates code from [coroot/coroot-node-agent](https://github.com/coroot/coroot-node-agent) under the Apache License, Version 2.0 (preserved at [LICENSE.APACHE-2.0](LICENSE.APACHE-2.0)). The eBPF programs under `ebpftracer/ebpf/` are licensed under the GNU General Public License, Version 2.0, as required by the Linux kernel's eBPF verifier.

See [LICENSING.md](LICENSING.md) for details on how the AGPL-3.0 user-space code, Apache-2.0 vendored libraries, and GPL-2.0 eBPF programs are distributed together, and [NOTICE](NOTICE) for the full upstream attribution.
