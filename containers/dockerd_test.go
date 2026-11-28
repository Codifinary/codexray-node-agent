package containers

import (
	"net"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/codifinary/codexray-node-agent/internal/dockerclient"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeDockerd answers every container inspect with body for the rest of the test.
func fakeDockerd(t *testing.T, body string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "docker.sock")
	l, err := net.Listen("unix", path)
	require.NoError(t, err)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) })}
	go func() { _ = srv.Serve(l) }()
	client, err := dockerclient.NewClient(path)
	require.NoError(t, err)
	previous := dockerdClient
	dockerdClient = client
	t.Cleanup(func() {
		dockerdClient = previous
		_ = srv.Close()
	})
}

func TestDockerdInspectVersionData(t *testing.T) {
	fakeDockerd(t, `{"Id":"4d1f9c","Image":"sha256:ab12","Name":"/shop","LogPath":"/var/log/x.log",
		"Config":{"Image":"registry/shop:1.2.3","Labels":{"team":"a"},
			"Env":["DB_PASSWORD=secret","PATH=/bin","OTEL_SERVICE_NAME=shop",
				"OTEL_RESOURCE_ATTRIBUTES=service.version=1.2.3,deployment.environment=lab","NOMAD_JOB_NAME=j","BROKEN"]},
		"HostConfig":{"LogConfig":{"Type":"json-file"}}}`)
	md, err := DockerdInspect("shop")
	require.NoError(t, err)
	assert.Equal(t, "shop", md.name)
	assert.Equal(t, "4d1f9c", md.runtimeID)
	assert.Equal(t, "sha256:ab12", md.imageID)
	assert.Equal(t, "registry/shop:1.2.3", md.image)
	assert.Equal(t, "shop", md.otelServiceName)
	assert.Equal(t, "1.2.3", md.otelServiceVersion)
	assert.Equal(t, "/var/log/x.log", md.logPath)
	assert.Equal(t, map[string]string{
		"OTEL_SERVICE_NAME": "shop", "OTEL_RESOURCE_ATTRIBUTES": "service.version=1.2.3,deployment.environment=lab",
		"NOMAD_JOB_NAME": "j",
	}, md.env, "only the keys the agent reads are kept")
}

func TestDockerdInspectWithoutConfigOrHostConfig(t *testing.T) {
	fakeDockerd(t, `{"Id":"4d1f9c","Name":"/bare","LogPath":"/var/log/x.log"}`)
	md, err := DockerdInspect("bare")
	require.NoError(t, err)
	assert.Equal(t, "bare", md.name)
	assert.Empty(t, md.image)
	assert.Empty(t, md.logPath, "no log driver is known without HostConfig")
	assert.Empty(t, md.env)
}
