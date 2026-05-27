FROM debian:bullseye AS builder
# Using Debian instead of the official Golang image because it’s based on newer OS versions
# with newer glibc, which causes compatibility issues.

RUN for i in 1 2 3; do \
        apt-get update && \
        apt-get install -y --fix-missing \
        curl git build-essential pkg-config libsystemd-dev && \
        break || sleep 5; \
    done

ARG GO_VERSION=1.25.10
RUN curl -fsSL https://go.dev/dl/go${GO_VERSION}.linux-$(dpkg --print-architecture).tar.gz -o go.tar.gz && \
    tar -C /usr/local -xzf go.tar.gz && rm go.tar.gz
ENV PATH="/usr/local/go/bin:${PATH}"

WORKDIR /tmp/src
COPY go.mod .
COPY go.sum .
# internal/ contains local replace targets (e.g., internal/prom shim that supplies
# prometheus/prometheus/{model/labels,prompb} without pulling the full module)
# — must be present before `go mod download` because go.mod references it.
COPY internal/ ./internal/

# Configure Git for private repositories
ARG GHCR_PAT
RUN if [ -n "$GHCR_PAT" ]; then \
        git config --global credential.helper store && \
        echo "https://x-access-token:${GHCR_PAT}@github.com" > ~/.git-credentials && \
        chmod 600 ~/.git-credentials && \
        git config --global url."https://x-access-token:${GHCR_PAT}@github.com/".insteadOf "https://github.com/"; \
    fi && \
    go env -w GOPRIVATE=github.com/codifinary/* && \
    go env -w GONOPROXY=github.com/codifinary/* && \
    go env -w GONOSUMDB=github.com/codifinary/*

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
ENTRYPOINT ["codexray-node-agent"]
