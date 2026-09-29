// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package containers

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/codifinary/codexray-node-agent/proc"
	"github.com/codifinary/logparser"
	"github.com/containerd/containerd"
	namespacesapi "github.com/containerd/containerd/api/services/namespaces/v1"
	ctrcontainers "github.com/containerd/containerd/containers"
	"github.com/containerd/containerd/errdefs"
	"github.com/containerd/typeurl/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/anypb"
)

// containerdTestContainer describes one container known to the fake store.
type containerdTestContainer struct {
	id         string
	image      string
	labels     map[string]string
	spec       *anypb.Any
	extensions map[string]typeurl.Any
}

// containerdTestStore is an in-memory containers.Store standing in for the
// containerd ContainerService (only Get is used by the agent).
type containerdTestStore struct {
	byId map[string]containerdTestContainer
}

func (s *containerdTestStore) Get(_ context.Context, id string) (ctrcontainers.Container, error) {
	c, ok := s.byId[id]
	if !ok {
		return ctrcontainers.Container{}, errdefs.ErrNotFound
	}
	// mirror containerFromProto: Spec is always the (possibly nil) *anypb.Any
	return ctrcontainers.Container{ID: c.id, Image: c.image, Labels: c.labels, Spec: c.spec, Extensions: c.extensions}, nil
}

func (s *containerdTestStore) List(context.Context, ...string) ([]ctrcontainers.Container, error) {
	return nil, errdefs.ErrNotImplemented
}

func (s *containerdTestStore) Create(context.Context, ctrcontainers.Container) (ctrcontainers.Container, error) {
	return ctrcontainers.Container{}, errdefs.ErrNotImplemented
}

func (s *containerdTestStore) Update(context.Context, ctrcontainers.Container, ...string) (ctrcontainers.Container, error) {
	return ctrcontainers.Container{}, errdefs.ErrNotImplemented
}

func (s *containerdTestStore) Delete(context.Context, string) error {
	return errdefs.ErrNotImplemented
}

// containerdTestUse installs a containerd client backed by the fake store.
func containerdTestUse(t *testing.T, cs ...containerdTestContainer) {
	t.Helper()
	store := &containerdTestStore{byId: map[string]containerdTestContainer{}}
	for _, c := range cs {
		store.byId[c.id] = c
	}
	client, err := containerd.New("", containerd.WithServices(containerd.WithContainerStore(store)))
	require.NoError(t, err)
	saved := containerdClient
	containerdClient = client
	t.Cleanup(func() { containerdClient = saved })
}

func containerdTestAny(t *testing.T, typeUrl string, v any) *anypb.Any {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return &anypb.Any{TypeUrl: typeUrl, Value: b}
}

func TestContainerdInspect(t *testing.T) {
	spec := containerdTestAny(t, "types.containerd.io/opencontainers/runtime-spec/1/Spec", map[string]any{
		"ociVersion": "1.1.0",
		"mounts": []map[string]any{
			{"destination": "/data", "type": "bind", "source": "/var/lib/kubelet/pods/0a1b/volumes/kubernetes.io~csi/pvc-0a1b2c3d-1111-2222-3333-444455556666/mount"},
			{"destination": "/proc", "type": "proc", "source": "proc"},
		},
	})
	meta := containerdTestAny(t, "github.com/containerd/cri/pkg/store/container/Metadata", map[string]any{
		"Version": "v1",
		"Metadata": map[string]any{
			"ID":      registryTestContainerID,
			"Name":    "api",
			"LogPath": "/var/log/pods/prod_api-0_0a1b/api/0.log",
		},
	})
	containerdTestUse(t, containerdTestContainer{
		id:         registryTestContainerID,
		image:      "registry.local/api:1.2.3",
		labels:     registryTestK8sLabels("prod", "api-0", "api"),
		spec:       spec,
		extensions: map[string]typeurl.Any{"io.cri-containerd.container.metadata": meta},
	})

	md, err := ContainerdInspect(registryTestContainerID)
	require.NoError(t, err)
	assert.Equal(t, "registry.local/api:1.2.3", md.image)
	assert.Equal(t, registryTestK8sLabels("prod", "api-0", "api"), md.labels)
	assert.Equal(t, map[string]string{
		"/data": "pvc-0a1b2c3d-1111-2222-3333-444455556666",
		"/proc": "",
	}, md.volumes)
	assert.Equal(t, "/var/log/pods/prod_api-0_0a1b/api/0.log", md.logPath)
	assert.IsType(t, logparser.CriDecoder{}, md.logDecoder)
}

func TestContainerdInspectToleratesBadPayloads(t *testing.T) {
	cases := map[string]containerdTestContainer{
		"nil spec, no extensions": {id: registryTestContainerID, image: "x"},
		"malformed spec": {id: registryTestContainerID, image: "x",
			spec: &anypb.Any{Value: []byte(`{"mounts": [`)}},
		"malformed cri metadata": {id: registryTestContainerID, image: "x",
			spec:       &anypb.Any{Value: []byte(`{}`)},
			extensions: map[string]typeurl.Any{"io.cri-containerd.container.metadata": &anypb.Any{Value: []byte(`not json`)}}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			containerdTestUse(t, c)
			var md *ContainerMetadata
			var err error
			require.NotPanics(t, func() { md, err = ContainerdInspect(registryTestContainerID) })
			require.NoError(t, err)
			assert.Equal(t, "x", md.image)
			assert.NotNil(t, md.volumes)
			assert.Empty(t, md.volumes)
			assert.Equal(t, "", md.logPath)
			assert.Nil(t, md.logDecoder, "no decoder without a log path")
		})
	}
}

func TestContainerdInspectErrors(t *testing.T) {
	t.Run("client not initialized", func(t *testing.T) {
		saved := containerdClient
		containerdClient = nil
		t.Cleanup(func() { containerdClient = saved })
		md, err := ContainerdInspect(registryTestContainerID)
		assert.Error(t, err)
		assert.Nil(t, md)
	})
	t.Run("unknown container", func(t *testing.T) {
		containerdTestUse(t)
		md, err := ContainerdInspect(registryTestContainerID)
		assert.True(t, errdefs.IsNotFound(err))
		assert.Nil(t, md)
	})
}

func TestContainerdInitWithoutContainerd(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root: the host's containerd sockets may be reachable")
	}
	for _, s := range []string{
		"/var/snap/microk8s/common/run/containerd.sock",
		"/run/k0s/containerd.sock",
		"/run/k3s/containerd/containerd.sock",
		"/run/containerd/containerd.sock",
	} {
		if _, err := os.Stat(proc.HostPath(s)); err == nil {
			t.Skip("host has a containerd socket:", s)
		}
	}
	saved := containerdClient
	containerdClient = nil
	t.Cleanup(func() { containerdClient = saved })

	start := time.Now()
	err := ContainerdInit()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "/run/containerd/containerd.sock")
	assert.Nil(t, containerdClient)
	assert.Less(t, time.Since(start), 5*time.Second, "each socket attempt is bounded by a 1s timeout")
}

// containerdTestNamespaces answers the namespace lookup containerd.New does for
// its default namespace.
type containerdTestNamespaces struct {
	namespacesapi.UnimplementedNamespacesServer
}

func (containerdTestNamespaces) Get(_ context.Context, req *namespacesapi.GetNamespaceRequest) (*namespacesapi.GetNamespaceResponse, error) {
	return &namespacesapi.GetNamespaceResponse{Namespace: &namespacesapi.Namespace{Name: req.Name}}, nil
}

// containerdTestServeHost runs a minimal containerd gRPC endpoint at hostPath(socket).
func containerdTestServeHost(t *testing.T, socket string) {
	t.Helper()
	sock := hostPath(socket)
	require.NoError(t, os.MkdirAll(filepath.Dir(sock), 0o755))
	l, err := net.Listen("unix", sock)
	require.NoError(t, err)
	srv := grpc.NewServer()
	namespacesapi.RegisterNamespacesServer(srv, containerdTestNamespaces{})
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(srv.Stop)
}

// containerdTestShortHostPath is containerTestHostPath with a short root: sun_path is
// limited to 108 bytes and t.TempDir() embeds the (long) test name.
func containerdTestShortHostPath(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp("", "ctrd")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	saved := hostPath
	hostPath = func(p string) string { return filepath.Join(root, p) }
	t.Cleanup(func() { hostPath = saved })
	return root
}

func TestContainerdInitUsesFirstReachableSocket(t *testing.T) {
	containerdTestShortHostPath(t)
	saved := containerdClient
	containerdClient = nil
	t.Cleanup(func() { containerdClient = saved })

	// microk8s' socket is tried first
	containerdTestServeHost(t, "/var/snap/microk8s/common/run/containerd.sock")

	start := time.Now()
	require.NoError(t, ContainerdInit())
	require.NotNil(t, containerdClient)
	t.Cleanup(func() { _ = containerdClient.Close() })
	assert.Equal(t, "k8s.io", containerdClient.DefaultNamespace())
	assert.Less(t, time.Since(start), time.Second, "no other socket is tried")
}
