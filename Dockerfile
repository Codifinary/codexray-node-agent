FROM debian:bullseye AS builder
# Using Debian instead of the official Golang image because it’s based on newer OS versions
# with newer glibc, which causes compatibility issues.

RUN apt-get update && apt-get install -y \
    curl git build-essential pkg-config libsystemd-dev

ARG GO_VERSION=1.24.9
RUN curl -fsSL https://go.dev/dl/go${GO_VERSION}.linux-$(dpkg --print-architecture).tar.gz -o go.tar.gz && \
    tar -C /usr/local -xzf go.tar.gz && rm go.tar.gz
ENV PATH="/usr/local/go/bin:${PATH}"

WORKDIR /tmp/src
COPY go.mod .
COPY go.sum .
RUN go mod download
COPY . .
ARG VERSION=unknown
RUN CGO_ENABLED=1 go build -mod=readonly -ldflags "-extldflags='-Wl,-z,lazy' -X 'github.com/codifinary/codexray-node-agent/flags.Version=${VERSION}'" -o codexray-node-agent .

FROM registry.access.redhat.com/ubi9/ubi

ARG VERSION=unknown
LABEL name="codexray-node-agent" \
      vendor="codexray" \
      maintainer="codexray" \
      version=${VERSION} \
      release="1" \
      summary="Codexray Node Agent." \
      description="Codexray Node Agent container image."

COPY LICENSE /licenses/LICENSE

COPY --from=builder /tmp/src/codexray-node-agent /usr/bin/codexray-node-agent
ENTRYPOINT ["codexray-node-agent"]
