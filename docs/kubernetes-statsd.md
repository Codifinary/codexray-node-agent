# Kubernetes StatsD application setup

This guide configures Kubernetes applications to send StatsD or DogStatsD
metrics to the node-local CodexRay node-agent:

```text
application -> node IP UDP :8125 -> node-agent -> CodexRay collector
```

Applications do not need the CodexRay project API key or collector URL. The
node-agent authenticates and batches metrics when forwarding them.

## 1. Discover the cluster Pod CIDRs

Run this command on the client cluster:

```bash
kubectl get nodes \
  -o custom-columns='NODE:.metadata.name,POD_CIDR:.spec.podCIDR,POD_CIDRS:.spec.podCIDRs'
```

Example:

```text
wrkr1   10.244.1.0/24
wrkr2   10.244.2.0/24
wrkr3   10.244.3.0/24
```

Use the smallest supernet covering all Pod CIDRs, such as `10.244.0.0/16`.
Do not use the Kubernetes Service CIDR: packets reaching the node-agent
originate from pod or node addresses, not Service addresses.

The default private-network allowlist works for most clusters:

```text
127.0.0.0/8,10.0.0.0/8,172.16.0.0/12,192.168.0.0/16,::1/128,fc00::/7
```

For a narrower client-specific configuration, set:

```yaml
args:
  - "--statsd-allowed-source-cidrs=10.244.0.0/16"
```

For a dual-stack cluster, include the IPv6 Pod CIDR as another comma-separated
entry.

## 2. Expose the node-local listener

Run the node-agent DaemonSet on every application node and expose UDP `8125`
through `hostPort`:

```yaml
args:
  - "--statsd-enabled"
  - "--statsd-listen=0.0.0.0:8125"
  - "--statsd-allowed-source-cidrs=10.244.0.0/16"

ports:
  - name: statsd
    containerPort: 8125
    hostPort: 8125
    protocol: UDP
```

## 3. Configure an application

Label every pod allowed to emit custom metrics and inject its Kubernetes node
IP through the Downward API:

```yaml
metadata:
  labels:
    codexray.io/statsd-client: "true"
spec:
  containers:
    - name: application
      env:
        - name: NODE_IP
          valueFrom:
            fieldRef:
              fieldPath: status.hostIP
        - name: STATSD_ADDR
          value: "$(NODE_IP):8125"
```

Declare `NODE_IP` before `STATSD_ADDR` so Kubernetes expands it. Configure the
application's StatsD library with `STATSD_ADDR`. StatsD uses UDP, so the value
must use `host:port` without an `http://` prefix.

For libraries that accept separate settings, use the same Downward API value
as `STATSD_HOST` and set `STATSD_PORT` to `8125`.

## 4. Restrict access with NetworkPolicy

Allow only explicitly labeled application pods to send UDP traffic to the
node-agent:

```yaml
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: node-agent-statsd
  namespace: codexray
spec:
  podSelector:
    matchLabels:
      app: node-agent
  policyTypes:
    - Ingress
  ingress:
    - ports:
        - port: 10300
          protocol: TCP
    - from:
        - podSelector:
            matchLabels:
              codexray.io/statsd-client: "true"
      ports:
        - port: 8125
          protocol: UDP
```

This `podSelector` selects senders in the same namespace. Add a
`namespaceSelector` when applications in other namespaces must send metrics.

Some CNIs do not enforce pod-targeted NetworkPolicy consistently for
`hostPort` traffic. Retain the node-agent CIDR allowlist as a second layer and
test allowed and denied pods on every supported CNI. Use a node firewall when
the CNI does not enforce the policy before hostPort DNAT.

## 5. Validate the configuration

Confirm the application and a node-agent are scheduled on the same node:

```bash
kubectl -n APPLICATION_NAMESPACE get pod APPLICATION_POD -o wide
kubectl -n codexray get pods -l app=node-agent -o wide
```

Confirm the application received the node-local destination:

```bash
kubectl -n APPLICATION_NAMESPACE exec APPLICATION_POD -- printenv STATSD_ADDR
```

The value should resemble:

```text
10.10.11.62:8125
```

Send a test metric from the application, then inspect node-agent receiver
counters and the metrics backend. Also send from an unlabeled pod; its packets
must be rejected by NetworkPolicy or the node firewall.

## Client onboarding checklist

- Discover and configure the Pod CIDR or CIDRs.
- Confirm the node-agent exposes UDP `8125` on every application node.
- Label authorized application pods with
  `codexray.io/statsd-client: "true"`.
- Inject `status.hostIP` and configure the StatsD destination as
  `$(NODE_IP):8125`.
- Apply and verify NetworkPolicy or equivalent node-firewall restrictions.
- Verify accepted metrics and `is_custom="true"` in the backend.
