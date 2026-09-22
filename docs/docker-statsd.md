# Docker StatsD application setup

This guide configures host applications and Docker containers to send StatsD
or DogStatsD metrics to a CodexRay node-agent running with Docker Compose:

```text
application -> UDP :8125 -> node-agent -> CodexRay collector
```

Applications only need the StatsD destination. They do not need the CodexRay
project API key or collector URL; the node-agent authenticates and forwards
batches to the collector.

## Requirements

- Use a Linux host for production node telemetry, eBPF, host PID visibility,
  and cgroup access.
- Docker Engine and Docker Compose must be installed.
- UDP port `8125` must be unused on the host.
- The host must be able to reach the configured CodexRay collector.

Docker Desktop on macOS or Windows can test the StatsD receiver, but the
node-agent observes Docker Desktop's Linux VM rather than the physical host.
Use a real Linux host for production validation.

## 1. Create the application network

Create a dedicated Docker network once:

```bash
docker network create codexray-network
```

If it already exists, Docker reports that condition and no replacement is
required.

Find the network subnet when configuring a narrow source CIDR allowlist:

```bash
docker network inspect codexray-network \
  --format '{{range .IPAM.Config}}{{println .Subnet}}{{end}}'
```

Example:

```text
172.20.0.0/16
```

## 2. Configure the node-agent

Use this Compose configuration as a starting point. Replace the image tag and
collector endpoint with release values.

```yaml
name: codexray-node-agent

services:
  node-agent:
    image: ghcr.io/codifinary/codexray-node-agent:RELEASE_TAG
    pull_policy: always
    restart: always
    privileged: true
    pid: host
    environment:
      API_KEY: ${CODEXRAY_API_KEY}
    volumes:
      - /proc:/host/proc:ro
      - /sys:/host/sys:ro
      - /:/host/root:ro
      - /sys/kernel/tracing:/sys/kernel/tracing
      - /sys/kernel/debug:/sys/kernel/debug
      - /sys/fs/cgroup:/host/sys/fs/cgroup:ro
      - node_agent_data:/data
      - custom_metrics_spool:/custom-metrics-spool
    ports:
      - "127.0.0.1:8125:8125/udp"
      - "127.0.0.1:10300:10300/tcp"
    command:
      - "--listen=:10300"
      - "--collector-endpoint=https://COLLECTOR_HOST/ingest"
      - "--cgroupfs-root=/host/sys/fs/cgroup"
      - "--wal-dir=/data"
      - "--statsd-enabled"
      - "--statsd-listen=0.0.0.0:8125"
      - "--statsd-allowed-source-cidrs=127.0.0.0/8,172.20.0.0/16"
      - "--statsd-custom-spool-dir=/custom-metrics-spool"
    networks:
      - codexray-network
    logging:
      driver: json-file
      options:
        max-size: "10m"
        max-file: "3"

volumes:
  node_agent_data: {}
  custom_metrics_spool: {}

networks:
  codexray-network:
    external: true
```

Store the API key outside the Compose file:

```bash
export CODEXRAY_API_KEY='PROJECT_API_KEY'
docker compose up -d node-agent
```

Binding the published ports to `127.0.0.1` prevents other machines from
reaching them. Containers attached to `codexray-network` communicate directly
with `node-agent:8125`; published ports are not involved in that path.

## 3. Configure a containerized application

Attach the application to `codexray-network` and configure the node-agent
service name as its StatsD destination:

```yaml
services:
  application:
    image: your-application:latest
    environment:
      STATSD_ADDR: node-agent:8125
    networks:
      - codexray-network

networks:
  codexray-network:
    external: true
```

If the library accepts separate settings, use:

```yaml
environment:
  STATSD_HOST: node-agent
  STATSD_PORT: "8125"
```

StatsD uses UDP. Do not add an `http://` or `https://` prefix.

The application and node-agent may be defined in separate Compose projects as
long as both join the same external `codexray-network` network.

## 4. Configure an application running on the host

An application running directly on the Linux host sends to the loopback
published port:

```bash
export STATSD_ADDR=127.0.0.1:8125
```

No project API key is required in the application.

## 5. Containers that cannot join the shared network

Joining `codexray-network` is the preferred configuration. If that is not
possible, a Linux container can reach the host-published port using the host
gateway:

```yaml
services:
  application:
    extra_hosts:
      - "host.docker.internal:host-gateway"
    environment:
      STATSD_ADDR: host.docker.internal:8125
```

For this path, publish UDP `8125` on a reachable host address instead of only
`127.0.0.1`, and restrict access with the host firewall:

```yaml
ports:
  - "0.0.0.0:8125:8125/udp"
```

Do not expose UDP `8125` publicly. Add the relevant Docker bridge subnet to
`--statsd-allowed-source-cidrs` and allow only trusted source networks in
the firewall.

## 6. Start and validate

Validate and start the Compose project:

```bash
docker compose config
docker compose pull
docker compose up -d
docker compose ps
```

Check the node-agent logs and health endpoint:

```bash
docker compose logs --tail=100 node-agent
curl -fsS http://127.0.0.1:10300/healthz
curl -fsS http://127.0.0.1:10300/readyz
```

Confirm the application configuration:

```bash
docker compose exec application printenv STATSD_ADDR
```

Send a metric using the application or a StatsD client, then verify:

- the node-agent accepted and parsed the UDP packet;
- dropped, rejected, and rate-limited counters did not unexpectedly increase;
- the custom-metrics spool is not continuously growing;
- the metric appears in the backend with `is_custom="true"`.

UDP does not provide an application-level delivery acknowledgement. Validate
the node-agent receiver counters and backend data instead of treating a
successful socket write as proof of ingestion.

## Security checklist

- Keep the CodexRay API key only in the node-agent environment or a supported
  Docker secret workflow.
- Prefer the shared Docker network over publishing UDP `8125` externally.
- Bind host-only ports to `127.0.0.1`.
- If external access is required, combine a narrow CIDR allowlist with host
  firewall rules.
- Never expose UDP `8125` to the public internet.
- Use log rotation and persistent spool storage.
- Pin a released node-agent image tag instead of `latest`.
