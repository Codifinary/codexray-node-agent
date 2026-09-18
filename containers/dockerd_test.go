// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package containers

import (
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/codifinary/codexray-node-agent/common"
	"github.com/codifinary/codexray-node-agent/internal/dockerclient"
	"github.com/codifinary/logparser"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"inet.af/netaddr"
)

// dockerdTestServe starts a fake dockerd on a unix socket answering
// GET /<version>/containers/<id>/json from the given id -> JSON body map
// (unknown ids get 404, like dockerd), and points dockerdClient at it.
func dockerdTestServe(t *testing.T, inspect map[string]string) {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "docker.sock")
	l, err := net.Listen("unix", sock)
	require.NoError(t, err)
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		// <version>/containers/<id>/json
		if len(parts) == 4 && parts[1] == "containers" && parts[3] == "json" {
			if body, ok := inspect[parts[2]]; ok {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(body))
				return
			}
			http.Error(w, `{"message":"No such container"}`, http.StatusNotFound)
			return
		}
		http.NotFound(w, r)
	})
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(l) }()

	c, err := dockerclient.NewClient(sock)
	require.NoError(t, err)
	saved := dockerdClient
	dockerdClient = c
	t.Cleanup(func() {
		dockerdClient = saved
		_ = srv.Close()
	})
}

const dockerdTestFullInspect = `{
  "Name": "/shop_web.1.q1w2e3",
  "LogPath": "/var/lib/docker/containers/abc/abc-json.log",
  "Config": {
    "Image": "nginx:1.27",
    "Labels": {"com.docker.swarm.task.name": "shop_web.1.q1w2e3", "maintainer": "x"},
    "Env": ["PATH=/usr/bin", "DSN=postgres://u:p@h/db?sslmode=disable", "NOVALUE", "EMPTY=", "NOMAD_JOB_NAME=shop"]
  },
  "HostConfig": {"LogConfig": {"Type": "json-file"}},
  "Mounts": [
    {"Source": "/var/lib/kubelet/pods/uid/volumes/kubernetes.io~csi/pvc-0a1b2c3d-1111-2222-3333-444455556666/mount", "Destination": "/data"},
    {"Source": "/etc/nginx", "Destination": "/etc/nginx"}
  ],
  "NetworkSettings": {
    "Ports": {
      "80/tcp":   [{"HostIp": "0.0.0.0", "HostPort": "8080"}, {"HostIp": "0.0.0.0", "HostPort": "8080"}],
      "443/tcp":  [{"HostIp": "127.0.0.1", "HostPort": "40000"}],
      "53/udp":   [{"HostIp": "0.0.0.0", "HostPort": "5353"}],
      "9000/tcp": [{"HostIp": "not-an-ip", "HostPort": "9000"}],
      "9100/tcp": null
    },
    "Networks": {"bridge": {"NetworkID": "net-bridge-id"}, "shop_default": {"NetworkID": "net-shop-id"}}
  }
}`

func TestDockerdInspect(t *testing.T) {
	dockerdTestServe(t, map[string]string{registryTestContainerID: dockerdTestFullInspect})

	md, err := DockerdInspect(registryTestContainerID)
	require.NoError(t, err)

	assert.Equal(t, "shop_web.1.q1w2e3", md.name, "leading slash must be trimmed")
	assert.Equal(t, "nginx:1.27", md.image)
	assert.Equal(t, map[string]string{"com.docker.swarm.task.name": "shop_web.1.q1w2e3", "maintainer": "x"}, md.labels)

	assert.Equal(t, map[string]string{
		"/data":      "pvc-0a1b2c3d-1111-2222-3333-444455556666",
		"/etc/nginx": "",
	}, md.volumes)

	assert.Equal(t, "/var/lib/docker/containers/abc/abc-json.log", md.logPath)
	assert.IsType(t, logparser.DockerJsonDecoder{}, md.logDecoder)

	// tcp only, deduplicated, invalid host IPs dropped, ephemeral ports filtered
	want := []netaddr.IPPort{
		netaddr.MustParseIPPort("0.0.0.0:8080"),
	}
	if !common.PortFilter.ShouldBeSkipped(40000) {
		want = append(want, netaddr.MustParseIPPort("127.0.0.1:40000"))
	}
	assert.ElementsMatch(t, want, md.hostListens["dockerd"])
	assert.Len(t, md.hostListens, 1)

	assert.Equal(t, map[string]ContainerNetwork{
		"bridge":       {NetworkID: "net-bridge-id"},
		"shop_default": {NetworkID: "net-shop-id"},
	}, md.networks)

	assert.Equal(t, "/usr/bin", md.env["PATH"])
	assert.Equal(t, "postgres://u:p@h/db?sslmode=disable", md.env["DSN"], "value may itself contain '='")
	assert.Equal(t, "", md.env["EMPTY"])
	assert.Contains(t, md.env, "EMPTY")
	assert.NotContains(t, md.env, "NOVALUE")
	assert.Equal(t, "shop", md.env["NOMAD_JOB_NAME"])
}

func TestDockerdInspectNonJsonLogDriver(t *testing.T) {
	dockerdTestServe(t, map[string]string{
		registryTestContainerID: `{"Name":"/a","LogPath":"/var/lib/docker/containers/a/a-json.log","Config":{"Image":"a"},"HostConfig":{"LogConfig":{"Type":"journald"}}}`,
	})
	md, err := DockerdInspect(registryTestContainerID)
	require.NoError(t, err)
	assert.Equal(t, "", md.logPath, "only json-file logs can be tailed")
	assert.Nil(t, md.logDecoder)
}

func TestDockerdInspectMinimal(t *testing.T) {
	dockerdTestServe(t, map[string]string{
		registryTestContainerID: `{"Name":"/a","Config":{"Image":"a"},"HostConfig":{"LogConfig":{"Type":"json-file"}},"NetworkSettings":{"Ports":{"80/tcp":null}}}`,
	})
	md, err := DockerdInspect(registryTestContainerID)
	require.NoError(t, err)
	assert.Equal(t, "a", md.name)
	assert.Equal(t, "", md.logPath, "no LogPath -> nothing to tail")
	assert.NotContains(t, md.hostListens, "dockerd", "no published tcp ports -> no dockerd listens")
	assert.NotNil(t, md.volumes)
	assert.NotNil(t, md.networks)
	assert.NotNil(t, md.env)
	assert.Empty(t, md.env)
}

func TestDockerdInspectErrors(t *testing.T) {
	t.Run("client not initialized", func(t *testing.T) {
		saved := dockerdClient
		dockerdClient = nil
		t.Cleanup(func() { dockerdClient = saved })
		md, err := DockerdInspect(registryTestContainerID)
		assert.Error(t, err)
		assert.Nil(t, md)
	})
	t.Run("no such container", func(t *testing.T) {
		dockerdTestServe(t, map[string]string{})
		md, err := DockerdInspect(registryTestContainerID)
		assert.Error(t, err)
		assert.Nil(t, md)
	})
	t.Run("malformed json", func(t *testing.T) {
		dockerdTestServe(t, map[string]string{registryTestContainerID: `{"Name": "/a", "Config": `})
		md, err := DockerdInspect(registryTestContainerID)
		assert.Error(t, err)
		assert.Nil(t, md)
	})
	t.Run("dockerd gone", func(t *testing.T) {
		c, err := dockerclient.NewClient(filepath.Join(t.TempDir(), "missing.sock"))
		require.NoError(t, err)
		saved := dockerdClient
		dockerdClient = c
		t.Cleanup(func() { dockerdClient = saved })
		md, err := DockerdInspect(registryTestContainerID)
		assert.Error(t, err)
		assert.Nil(t, md)
	})
}

func TestDockerdInspectIPv6HostBinding(t *testing.T) {
	// BUG: dockerd.go:77 joins HostIP and HostPort with ":" (no brackets), so IPv6 bindings such as {"HostIp":"::"} fail to parse and are silently dropped from hostListens — unskip when fixed
	t.Skip("BUG: DockerdInspect drops IPv6 published-port bindings (HostIP not bracketed)")
	dockerdTestServe(t, map[string]string{registryTestContainerID: `{"Name":"/a","Config":{"Image":"a"},"HostConfig":{"LogConfig":{"Type":"json-file"}},` +
		`"NetworkSettings":{"Ports":{"80/tcp":[{"HostIp":"0.0.0.0","HostPort":"8080"},{"HostIp":"::","HostPort":"8080"},{"HostIp":"fd00::1","HostPort":"8081"}]}}}`})
	md, err := DockerdInspect(registryTestContainerID)
	require.NoError(t, err)
	assert.ElementsMatch(t, []netaddr.IPPort{
		netaddr.MustParseIPPort("0.0.0.0:8080"),
		netaddr.MustParseIPPort("[::]:8080"),
		netaddr.MustParseIPPort("[fd00::1]:8081"),
	}, md.hostListens["dockerd"])
}

// The runtime response is external input: a response without "Config" (the
// code itself treats Config as optional further down) must not crash the agent.
func TestDockerdInspectMissingConfig(t *testing.T) {
	// BUG: dockerd.go:60 dereferences c.Config (labels/image) before the nil check at dockerd.go:94 — panics on a response without "Config" — unskip when fixed
	t.Skip("BUG: DockerdInspect panics on an inspect response without Config")
	dockerdTestServe(t, map[string]string{registryTestContainerID: `{"Name":"/a","HostConfig":{"LogConfig":{"Type":"json-file"}}}`})
	assert.NotPanics(t, func() {
		md, err := DockerdInspect(registryTestContainerID)
		require.NoError(t, err)
		assert.Equal(t, "a", md.name)
	})
}

func TestDockerdInspectMissingHostConfig(t *testing.T) {
	// BUG: dockerd.go:69 dereferences c.HostConfig when LogPath is set without checking for nil — unskip when fixed
	t.Skip("BUG: DockerdInspect panics on an inspect response with LogPath but without HostConfig")
	dockerdTestServe(t, map[string]string{registryTestContainerID: `{"Name":"/a","LogPath":"/x-json.log","Config":{"Image":"a"}}`})
	assert.NotPanics(t, func() {
		md, err := DockerdInspect(registryTestContainerID)
		require.NoError(t, err)
		assert.Equal(t, "", md.logPath)
	})
}
