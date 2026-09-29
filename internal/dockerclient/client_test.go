// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package dockerclient

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// clientTestDaemon is a fake dockerd listening on a unix socket.
type clientTestDaemon struct {
	sock  string
	mu    sync.Mutex
	paths []string
}

func (d *clientTestDaemon) seen() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.paths...)
}

func clientTestServe(t *testing.T, h http.HandlerFunc) *clientTestDaemon {
	t.Helper()
	dir := t.TempDir()
	if len(dir) > 90 { // unix socket paths are limited to 108 bytes
		var err error
		dir, err = os.MkdirTemp("/tmp", "dc")
		require.NoError(t, err)
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
	}
	d := &clientTestDaemon{sock: filepath.Join(dir, "d.sock")}
	l, err := net.Listen("unix", d.sock)
	require.NoError(t, err)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		d.paths = append(d.paths, r.Method+" "+r.URL.Path)
		d.mu.Unlock()
		h(w, r)
	})}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })
	return d
}

func clientTestNew(t *testing.T, d *clientTestDaemon) *Client {
	t.Helper()
	c, err := NewClient(d.sock)
	require.NoError(t, err)
	return c
}

// Trimmed real `GET /v1.41/containers/{id}/json` response from dockerd 24.
const clientTestInspect = `{
  "Id": "4fa6e0f0c6786287e131c3852c58a2e01cc697a68231826813597e4994f1d6e2",
  "Created": "2024-01-01T00:00:00.000000000Z",
  "Path": "/docker-entrypoint.sh",
  "State": {"Status": "running", "Running": true, "Pid": 1234, "ExitCode": 0},
  "Name": "/nginx-1",
  "LogPath": "/var/lib/docker/containers/4fa6/4fa6-json.log",
  "HostConfig": {"LogConfig": {"Type": "json-file", "Config": {}}, "NetworkMode": "bridge"},
  "Mounts": [
    {"Type": "bind", "Source": "/srv/html", "Destination": "/usr/share/nginx/html", "Mode": "ro", "RW": false},
    {"Type": "volume", "Name": "data", "Source": "/var/lib/docker/volumes/data/_data", "Destination": "/data"}
  ],
  "Config": {
    "Hostname": "4fa6e0f0c678",
    "Image": "nginx:1.25",
    "Env": ["PATH=/usr/local/sbin:/usr/bin", "NGINX_VERSION=1.25.3", "EMPTY="],
    "Labels": {"com.docker.compose.project": "shop", "com.docker.compose.service": "web"}
  },
  "NetworkSettings": {
    "Ports": {"80/tcp": [{"HostIp": "0.0.0.0", "HostPort": "8080"}, {"HostIp": "::", "HostPort": "8080"}], "53/udp": null},
    "Networks": {"bridge": {"NetworkID": "7ea29fc1412292a2d7bba362f9253545fecdfa8ce9a6e37dd10ba8bee7129812", "IPAddress": "172.17.0.2"}}
  }
}`

func TestNewClientDefaults(t *testing.T) {
	c, err := NewClient("/nonexistent.sock")
	require.NoError(t, err)
	assert.Equal(t, "v1.41", c.apiVersion)
	assert.Equal(t, 30*time.Second, c.http.Timeout)
}

func TestPing(t *testing.T) {
	t.Run("ok", func(t *testing.T) {
		d := clientTestServe(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/_ping" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Api-Version", "1.43")
			_, _ = w.Write([]byte("OK"))
		})
		require.NoError(t, clientTestNew(t, d).Ping(context.Background()))
		assert.Equal(t, []string{"GET /_ping"}, d.seen())
	})
	t.Run("500", func(t *testing.T) {
		d := clientTestServe(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		})
		err := clientTestNew(t, d).Ping(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "500")
	})
	t.Run("daemon down", func(t *testing.T) {
		c, _ := NewClient(filepath.Join(t.TempDir(), "missing.sock"))
		assert.Error(t, c.Ping(context.Background()))
	})
}

func TestNegotiateAPIVersion(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"server newer", 200, `{"Version":"26.0.0","ApiVersion":"1.45","MinAPIVersion":"1.24"}`, "v1.45"},
		{"server older", 200, `{"Version":"19.03.15","ApiVersion":"1.40","MinAPIVersion":"1.12"}`, "v1.40"},
		{"missing ApiVersion keeps default", 200, `{"Version":"x"}`, "v1.41"},
		{"malformed json keeps default", 200, `{"ApiVersion":`, "v1.41"},
		{"500 keeps default", 500, `{"message":"boom"}`, "v1.41"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := clientTestServe(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/version" {
					w.WriteHeader(tc.status)
					_, _ = w.Write([]byte(tc.body))
					return
				}
				if strings.HasSuffix(r.URL.Path, "/json") {
					_, _ = w.Write([]byte(`{"Name":"/x"}`))
					return
				}
				w.WriteHeader(http.StatusNotFound)
			})
			c := clientTestNew(t, d)
			c.NegotiateAPIVersion(context.Background())
			assert.Equal(t, tc.want, c.apiVersion)
			_, err := c.ContainerInspect(context.Background(), "abc")
			require.NoError(t, err)
			assert.Equal(t, []string{"GET /version", "GET /" + tc.want + "/containers/abc/json"}, d.seen())
		})
	}
	t.Run("daemon down keeps default", func(t *testing.T) {
		c, _ := NewClient(filepath.Join(t.TempDir(), "missing.sock"))
		c.NegotiateAPIVersion(context.Background())
		assert.Equal(t, "v1.41", c.apiVersion)
	})
}

func TestContainerInspect(t *testing.T) {
	t.Run("decode", func(t *testing.T) {
		d := clientTestServe(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v1.41/containers/4fa6e0f0c678/json" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(clientTestInspect))
		})
		c := clientTestNew(t, d)
		cj, err := c.ContainerInspect(context.Background(), "4fa6e0f0c678")
		require.NoError(t, err)
		assert.Equal(t, "/nginx-1", cj.Name)
		assert.Equal(t, "/var/lib/docker/containers/4fa6/4fa6-json.log", cj.LogPath)
		require.NotNil(t, cj.Config)
		assert.Equal(t, "nginx:1.25", cj.Config.Image)
		assert.Equal(t, map[string]string{"com.docker.compose.project": "shop", "com.docker.compose.service": "web"}, cj.Config.Labels)
		assert.Equal(t, []string{"PATH=/usr/local/sbin:/usr/bin", "NGINX_VERSION=1.25.3", "EMPTY="}, cj.Config.Env)
		require.NotNil(t, cj.HostConfig)
		assert.Equal(t, "json-file", cj.HostConfig.LogConfig.Type)
		assert.Equal(t, []MountPoint{
			{Source: "/srv/html", Destination: "/usr/share/nginx/html"},
			{Source: "/var/lib/docker/volumes/data/_data", Destination: "/data"},
		}, cj.Mounts)
		require.NotNil(t, cj.NetworkSettings)
		assert.Equal(t, []PortBinding{{HostIP: "0.0.0.0", HostPort: "8080"}, {HostIP: "::", HostPort: "8080"}}, cj.NetworkSettings.Ports["80/tcp"])
		assert.Contains(t, cj.NetworkSettings.Ports, Port("53/udp"))
		assert.Nil(t, cj.NetworkSettings.Ports["53/udp"])
		require.Contains(t, cj.NetworkSettings.Networks, "bridge")
		assert.Equal(t, "7ea29fc1412292a2d7bba362f9253545fecdfa8ce9a6e37dd10ba8bee7129812", cj.NetworkSettings.Networks["bridge"].NetworkID)
	})
	t.Run("minimal object leaves pointers nil", func(t *testing.T) {
		d := clientTestServe(t, func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"Name":"/bare"}`))
		})
		cj, err := clientTestNew(t, d).ContainerInspect(context.Background(), "x")
		require.NoError(t, err)
		assert.Equal(t, "/bare", cj.Name)
		assert.Nil(t, cj.Config)
		assert.Nil(t, cj.HostConfig)
		assert.Nil(t, cj.NetworkSettings)
	})
	for _, tc := range []struct {
		name   string
		status int
		body   string
		errSub string
	}{
		{"404 not found", 404, `{"message":"No such container: gone"}`, "404"},
		{"500", 500, `{"message":"internal"}`, "500"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := clientTestServe(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})
			cj, err := clientTestNew(t, d).ContainerInspect(context.Background(), "gone")
			require.Error(t, err)
			assert.Nil(t, cj)
			assert.Contains(t, err.Error(), tc.errSub)
			assert.Contains(t, err.Error(), "gone")
		})
	}
	t.Run("malformed json", func(t *testing.T) {
		d := clientTestServe(t, func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"Name": "/x", "Config": {"Labels": [`))
		})
		cj, err := clientTestNew(t, d).ContainerInspect(context.Background(), "x")
		assert.Error(t, err)
		assert.Nil(t, cj)
	})
	t.Run("wrong types", func(t *testing.T) {
		d := clientTestServe(t, func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"Name": 42}`))
		})
		cj, err := clientTestNew(t, d).ContainerInspect(context.Background(), "x")
		assert.Error(t, err)
		assert.Nil(t, cj)
	})
	t.Run("daemon down", func(t *testing.T) {
		c, _ := NewClient(filepath.Join(t.TempDir(), "missing.sock"))
		_, err := c.ContainerInspect(context.Background(), "x")
		assert.Error(t, err)
	})
}

func TestClientTimeouts(t *testing.T) {
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	d := clientTestServe(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-block:
		case <-r.Context().Done():
		}
	})

	t.Run("context deadline", func(t *testing.T) {
		c := clientTestNew(t, d)
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		start := time.Now()
		_, err := c.ContainerInspect(ctx, "hang")
		require.Error(t, err)
		assert.Less(t, time.Since(start), 2*time.Second)
		assert.ErrorIs(t, err, context.DeadlineExceeded)
	})
	t.Run("client timeout bounds a hung daemon", func(t *testing.T) {
		c := clientTestNew(t, d)
		c.http.Timeout = 100 * time.Millisecond
		start := time.Now()
		err := c.Ping(context.Background())
		require.Error(t, err)
		assert.Less(t, time.Since(start), 2*time.Second)
		c.NegotiateAPIVersion(context.Background())
		assert.Equal(t, "v1.41", c.apiVersion)
	})
}

func TestPortProto(t *testing.T) {
	for p, want := range map[Port]string{
		"80/tcp":   "tcp",
		"53/udp":   "udp",
		"132/sctp": "sctp",
		"8080":     "",
		"":         "",
		"80/":      "",
		"a/b/tcp":  "tcp",
	} {
		assert.Equal(t, want, p.Proto(), string(p))
	}
}
