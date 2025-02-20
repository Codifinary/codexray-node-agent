FROM golang:1.23-bullseye AS builder
RUN apt update && apt install -y libsystemd-dev
WORKDIR /tmp/src
COPY go.mod .
COPY go.sum .
RUN go mod download
COPY . .
ARG VERSION=unknown
RUN CGO_ENABLED=1 go build -mod=readonly -ldflags "-X main.version=$VERSION" -o codexray-node-agent .

FROM registry.access.redhat.com/ubi9/ubi

ARG VERSION=unknown
LABEL name="codexray-node-agent" \
      vendor="Codexray, Inc." \
      version=${VERSION} \
      summary="Codexray Node Agent."

COPY LICENSE /licenses/LICENSE

COPY --from=builder /tmp/src/codexray-node-agent /usr/bin/codexray-node-agent
ENTRYPOINT ["codexray-node-agent"]