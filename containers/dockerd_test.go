package containers

import (
	"net"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/codifinary/codexray-node-agent/internal/dockerclient"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"inet.af/netaddr"
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

func TestDockerPublishedListens(t *testing.T) {
	addr := netaddr.MustParseIPPort
	for _, tc := range []struct {
		name        string
		ports       map[dockerclient.Port][]dockerclient.PortBinding
		want        []netaddr.IPPort
		wantInvalid int
	}{
		{name: "IPv4 and IPv6 bindings of one port", ports: map[dockerclient.Port][]dockerclient.PortBinding{
			"80/tcp": {{HostIP: "0.0.0.0", HostPort: "8080"}, {HostIP: "::", HostPort: "8080"}}},
			want: []netaddr.IPPort{addr("0.0.0.0:8080"), addr("[::]:8080")}},
		{name: "empty host IP binds both families", ports: map[dockerclient.Port][]dockerclient.PortBinding{
			"80/tcp": {{HostIP: "", HostPort: "8080"}}},
			want: []netaddr.IPPort{addr("0.0.0.0:8080"), addr("[::]:8080")}},
		{name: "bracketed and plain IPv6", ports: map[dockerclient.Port][]dockerclient.PortBinding{
			"80/tcp": {{HostIP: "[::1]", HostPort: "18081"}, {HostIP: "fd00::5", HostPort: "18082"}}},
			want: []netaddr.IPPort{addr("[::1]:18081"), addr("[fd00::5]:18082")}},
		{name: "IPv4-mapped is unmapped", ports: map[dockerclient.Port][]dockerclient.PortBinding{
			"80/tcp": {{HostIP: "::ffff:10.0.0.1", HostPort: "80"}}},
			want: []netaddr.IPPort{addr("10.0.0.1:80")}},
		{name: "udp ignored, duplicates merged", ports: map[dockerclient.Port][]dockerclient.PortBinding{
			"53/udp": {{HostIP: "0.0.0.0", HostPort: "53"}},
			"80/tcp": {{HostIP: "10.0.0.1", HostPort: "80"}, {HostIP: "10.0.0.1", HostPort: "80"}}},
			want: []netaddr.IPPort{addr("10.0.0.1:80")}},
		{name: "unpublished and invalid", ports: map[dockerclient.Port][]dockerclient.PortBinding{
			"80/tcp":   nil,
			"81/tcp":   {{HostIP: "0.0.0.0", HostPort: ""}, {HostIP: "0.0.0.0", HostPort: "0"}},
			"82/tcp":   {{HostIP: "0.0.0.0", HostPort: "abc"}, {HostIP: "0.0.0.0", HostPort: "70000"}},
			"83/tcp":   {{HostIP: "not-an-ip", HostPort: "83"}},
			"8443/tcp": {{HostIP: "127.0.0.1", HostPort: "8443"}}},
			want: []netaddr.IPPort{addr("127.0.0.1:8443")}, wantInvalid: 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, invalid := dockerPublishedListens(tc.ports)
			assert.Equal(t, tc.want, got)
			assert.Equal(t, tc.wantInvalid, invalid)
		})
	}
}

func TestExpandProxiedListensKeepsTheFamily(t *testing.T) {
	addr := netaddr.MustParseIPPort
	hostIps := []netaddr.IP{netaddr.MustParseIP("192.168.1.10"), netaddr.MustParseIP("2001:db8::10")}
	got := expandProxiedListens(map[string][]netaddr.IPPort{
		"dockerd": {addr("0.0.0.0:8080"), addr("[::]:8080"), addr("[::1]:18081")},
	}, hostIps)
	assert.Equal(t, map[string]map[netaddr.IPPort]struct{}{"dockerd": {
		addr("192.168.1.10:8080"): {}, addr("[2001:db8::10]:8080"): {}, addr("[::1]:18081"): {},
	}}, got)
}

func TestDockerdInspectIPv6PublishedPort(t *testing.T) {
	fakeDockerd(t, `{"Id":"1","Name":"/v6","Config":{},"HostConfig":{},
		"NetworkSettings":{"Ports":{"80/tcp":[{"HostIp":"::1","HostPort":"18081"}]}}}`)
	md, err := DockerdInspect("v6")
	require.NoError(t, err)
	assert.Equal(t, []netaddr.IPPort{netaddr.MustParseIPPort("[::1]:18081")}, md.hostListens["dockerd"])
}
