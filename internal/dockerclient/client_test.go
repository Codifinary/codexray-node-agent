package dockerclient

import (
	"context"
	"net"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// serveUnix runs handler on a unix socket in a temp dir and returns the socket path.
func serveUnix(t *testing.T, handler http.Handler) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "docker.sock")
	l, err := net.Listen("unix", path)
	require.NoError(t, err)
	srv := &http.Server{Handler: handler}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })
	return path
}

func TestContainerInspectDecodesIDAndImage(t *testing.T) {
	const body = `{"Id":"0f3c` + `5e1a9b7d2c4e6f8a0b1c3d5e7f9a1b3c5d7e9f1a3b5c7d9e1f3a5b7c9d1e3f5a7b","Image":"sha256:ab12",
		"Name":"/web","Config":{"Image":"nginx:1.25","Labels":{"a":"b"},"Env":["X=1"]},
		"NetworkSettings":{"Ports":{"80/tcp":[{"HostIp":"0.0.0.0","HostPort":"8080"}]}}}`
	path := serveUnix(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1.41/containers/web/json", r.URL.Path)
		_, _ = w.Write([]byte(body))
	}))
	c, err := NewClient(path)
	require.NoError(t, err)
	got, err := c.ContainerInspect(context.Background(), "web")
	require.NoError(t, err)
	assert.Equal(t, "0f3c5e1a9b7d2c4e6f8a0b1c3d5e7f9a1b3c5d7e9f1a3b5c7d9e1f3a5b7c9d1e3f5a7b", got.ID)
	assert.Equal(t, "sha256:ab12", got.Image)
	assert.Equal(t, "nginx:1.25", got.Config.Image)
	assert.Equal(t, []PortBinding{{HostIP: "0.0.0.0", HostPort: "8080"}}, got.NetworkSettings.Ports["80/tcp"])
}
