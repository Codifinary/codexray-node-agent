// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package common

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestContainerIdToServiceName(t *testing.T) {
	f := ContainerIdToOtelServiceName
	assert.Equal(t,
		f("/k8s/otel-demo/otel-demo-frauddetectionservice-64cd4f9686-mvtnb/frauddetectionservice"),
		"/k8s/otel-demo/otel-demo-frauddetectionservice")

	assert.Equal(t,
		f("/k8s/codifinary/codexray-node-agent-np9pk/node-agent"),
		"/k8s/codifinary/codexray-node-agent")

	assert.Equal(t,
		f("/k8s/codifinary/pyroscope-df884bb79-hhxtv/pyroscope"),
		"/k8s/codifinary/pyroscope")

	assert.Equal(t,
		f("/k8s/default/cassandra-main-12/cassandra"),
		"/k8s/default/cassandra-main")

	assert.Equal(t,
		f("/k8s/default/hello-28283967-khz2f/xz"),
		"/k8s/default/hello")

	assert.Equal(t,
		f("/system.slice/k3s.service"),
		"/system.slice/k3s.service")

	assert.Equal(t,
		f("/docker/container_name"),
		"/docker/container_name")
}

func TestOtelServiceFromEnv(t *testing.T) {
	for _, tc := range []struct {
		name, wantName, wantVersion string
		env                         map[string]string
	}{
		{name: "no env", env: nil},
		{name: "service name only", env: map[string]string{OtelServiceNameEnv: "shop"}, wantName: "shop"},
		{name: "resource attributes", env: map[string]string{
			OtelResourceAttributesEnv: "service.name=shop,service.version=1.2.3,deployment.environment=lab"},
			wantName: "shop", wantVersion: "1.2.3"},
		{name: "OTEL_SERVICE_NAME wins", env: map[string]string{
			OtelServiceNameEnv: " orders ", OtelResourceAttributesEnv: "service.name=shop,service.version=2"},
			wantName: "orders", wantVersion: "2"},
		{name: "spaces and percent-encoding", env: map[string]string{
			OtelResourceAttributesEnv: " service.version = 1.0%2Brc1 , service.name=a%20b"},
			wantName: "a b", wantVersion: "1.0+rc1"},
		{name: "malformed pairs are skipped", env: map[string]string{
			OtelResourceAttributesEnv: "garbage,service.version=3,=x,service.name"},
			wantVersion: "3"},
		{name: "undecodable value kept as is", env: map[string]string{
			OtelResourceAttributesEnv: "service.version=1%zz"}, wantVersion: "1%zz"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			name, version := OtelServiceFromEnv(tc.env)
			assert.Equal(t, tc.wantName, name)
			assert.Equal(t, tc.wantVersion, version)
		})
	}
}
