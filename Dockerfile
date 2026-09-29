FROM registry.access.redhat.com/ubi9/ubi AS builder
RUN dnf install -y \
        ca-certificates curl-minimal git gcc gcc-c++ make \
        pkgconf-pkg-config systemd-devel tar gzip && \
    dnf clean all

ARG TARGETARCH
ARG GO_VERSION=1.26.4
RUN test -n "$TARGETARCH" && \
    curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${TARGETARCH}.tar.gz" -o go.tar.gz && \
    tar -C /usr/local -xzf go.tar.gz && rm go.tar.gz
ENV PATH="/usr/local/go/bin:${PATH}"

WORKDIR /tmp/src
COPY go.mod .
COPY go.sum .
# internal/ contains local replace targets:
#   - internal/prom: Prometheus shim (model/labels, prompb, util/fmtutil)
#   - internal/pyroscope-ebpf: vendored grafana/pyroscope/ebpf
# — must be present before `go mod download` because go.mod references them.
COPY internal/ ./internal/

RUN go mod download
COPY . .
ARG VERSION=unknown
ARG BUILD_GPU=false
# Build without GPU support by default (set BUILD_GPU=true to enable GPU support)
RUN if [ "$BUILD_GPU" = "true" ]; then \
        CGO_ENABLED=1 go build -mod=readonly -tags gpu -ldflags "-extldflags='-Wl,-z,lazy' -X 'github.com/codifinary/codexray-node-agent/flags.Version=${VERSION}'" -o codexray-node-agent .; \
    else \
        CGO_ENABLED=1 go build -mod=readonly -ldflags "-extldflags='-Wl,-z,lazy' -X 'github.com/codifinary/codexray-node-agent/flags.Version=${VERSION}'" -o codexray-node-agent .; \
    fi

FROM registry.access.redhat.com/ubi9/ubi-minimal

ARG VERSION=unknown
LABEL name="codexray-node-agent" \
      vendor="codexray" \
      maintainer="codexray" \
      version=${VERSION} \
      release="1" \
      summary="Codexray Node Agent." \
      description="Codexray Node Agent container image." \
      license="AGPL-3.0" \
      org.opencontainers.image.licenses="AGPL-3.0"

# Smaller attack surface: ubi9-minimal ships ~110 packages vs ~250 on full UBI9,
# already has systemd-libs (the only runtime dep), and no python/gdb/vim.
# Apply OS security updates and drop gnutls (not used by the Go binary — Go has
# its own crypto/tls; nothing else on the image requires it).
RUN microdnf upgrade -y && \
    microdnf clean all

# Force-remove packages not needed by the Go agent. The Go binary uses Go's own
# crypto/tls, not gnutls; removing gnutls/gnupg2/glib2 and friends eliminates a
# large CVE surface. ubi9-minimal doesn't need them after the upgrade is done.
RUN for pkg in \
        gnutls gnupg2 glib2 json-glib libksba npth pinentry \
        curl-minimal libcurl-minimal libxml2 libarchive openldap libtasn1 \
        libsolv libsmartcols sqlite-libs \
        ; do \
        rpm -q "$pkg" >/dev/null 2>&1 && rpm -e --nodeps "$pkg" || true; \
    done && \
    rm -rf /var/cache/yum /var/cache/dnf /var/lib/rpm/__db.* /var/lib/dnf

COPY LICENSE /licenses/LICENSE

COPY --from=builder /tmp/src/codexray-node-agent /usr/bin/codexray-node-agent
EXPOSE 10300/tcp
EXPOSE 8125/udp
ENTRYPOINT ["codexray-node-agent"]
