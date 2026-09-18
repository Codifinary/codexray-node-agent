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

func TestContainerIdToOtelServiceNameWorkloads(t *testing.T) {
	cases := []struct{ in, want string }{
		// Deployment: <name>-<rs hash>-<5 char suffix>
		{"/k8s/default/api-7d9f8b6c5d-x2x4z/api", "/k8s/default/api"},
		// DaemonSet: <name>-<5 char suffix>
		{"/k8s/kube-system/kube-proxy-9zq7c/kube-proxy", "/k8s/kube-system/kube-proxy"},
		// StatefulSet: <name>-<ordinal>
		{"/k8s/db/postgres-0/postgres", "/k8s/db/postgres"},
		{"/k8s/db/postgres-10/postgres", "/k8s/db/postgres"},
		// bare pod: nothing to strip
		{"/k8s/default/mypod/app", "/k8s/default/mypod/app"},
		// non-k8s containers keep their id
		{"/talos/kubelet", "/talos/kubelet"},
		{"/system.slice/docker.service", "/system.slice/docker.service"},
		{"", ""},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, ContainerIdToOtelServiceName(c.in), c.in)
	}
}

func TestContainerIdToOtelServiceNameCronJob(t *testing.T) {
	// containers/registry.go emits /k8s-cronjob/<ns>/<job>/<container> for cronjob pods;
	// cronjobPodRegex intends to map it to /k8s-cronjob/<ns>/<job>, but the "/k8s/" prefix
	// guard returns early so the regex is never reached.
	// BUG: ContainerIdToOtelServiceName never strips the container from /k8s-cronjob/ ids — unskip when fixed
	t.Skip("BUG: ContainerIdToOtelServiceName never strips the container from /k8s-cronjob/ ids")
	assert.Equal(t, "/k8s-cronjob/default/backup", ContainerIdToOtelServiceName("/k8s-cronjob/default/backup/worker"))
}
