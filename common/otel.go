// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package common

import (
	"net/url"
	"regexp"
	"strings"
)

// The container env keys the agent reads the OpenTelemetry service identity from.
const (
	OtelServiceNameEnv        = "OTEL_SERVICE_NAME"
	OtelResourceAttributesEnv = "OTEL_RESOURCE_ATTRIBUTES"
)

var (
	deploymentPodRegex  = regexp.MustCompile(`(/k8s/[a-z0-9-]+/[a-z0-9-]+)-[0-9a-f]{1,10}-[bcdfghjklmnpqrstvwxz2456789]{5}/.+`)
	daemonsetPodRegex   = regexp.MustCompile(`(/k8s/[a-z0-9-]+/[a-z0-9-]+)-[bcdfghjklmnpqrstvwxz2456789]{5}/.+`)
	statefulsetPodRegex = regexp.MustCompile(`(/k8s/[a-z0-9-]+/[a-z0-9-]+)-\d+/.+`)
	cronjobPodRegex     = regexp.MustCompile(`(/k8s-cronjob/[a-z0-9-]+/[a-z0-9-]+)/.+`)
)

func ContainerIdToOtelServiceName(containerId string) string {
	if !strings.HasPrefix(containerId, "/k8s/") {
		return containerId
	}
	for _, r := range []*regexp.Regexp{deploymentPodRegex, daemonsetPodRegex, statefulsetPodRegex, cronjobPodRegex} {
		if g := r.FindStringSubmatch(containerId); len(g) == 2 {
			return g[1]
		}
	}
	return containerId
}

// OtelServiceFromEnv is the service name and version an OpenTelemetry SDK would report from
// this env: OTEL_SERVICE_NAME, else service.name of OTEL_RESOURCE_ATTRIBUTES, and its
// service.version. Malformed attribute pairs are skipped.
func OtelServiceFromEnv(env map[string]string) (name, version string) {
	for _, pair := range strings.Split(env[OtelResourceAttributesEnv], ",") {
		key, value, ok := strings.Cut(pair, "=")
		if !ok {
			continue
		}
		if decoded, err := url.PathUnescape(strings.TrimSpace(value)); err == nil {
			value = decoded
		}
		switch strings.TrimSpace(key) {
		case "service.name":
			name = strings.TrimSpace(value)
		case "service.version":
			version = strings.TrimSpace(value)
		}
	}
	if serviceName := strings.TrimSpace(env[OtelServiceNameEnv]); serviceName != "" {
		name = serviceName
	}
	return name, version
}
