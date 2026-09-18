// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package containers

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/codifinary/codexray-node-agent/cgroup"
	"github.com/codifinary/codexray-node-agent/proc"
	"github.com/codifinary/logparser"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// crioTestServe starts a fake CRI-O inspect endpoint (GET /containers/<id>) on
// a unix socket and points crioClient at it the same way CrioInit does.
func crioTestServe(t *testing.T, inspect map[string]string) {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "crio.sock")
	l, err := net.Listen("unix", sock)
	require.NoError(t, err)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/containers/")
		if body, ok := inspect[id]; ok && id != r.URL.Path {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(body))
			return
		}
		http.Error(w, "can't find the container with id "+id, http.StatusNotFound)
	})}
	go func() { _ = srv.Serve(l) }()

	saved := crioClient
	crioClient = &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return net.DialTimeout("unix", sock, crioTimeout)
		},
		DisableCompression: true,
	}}
	t.Cleanup(func() {
		crioClient = saved
		_ = srv.Close()
	})
}

func crioTestInfo(t *testing.T, annotations map[string]string) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"name":             "k8s_api_api-0_prod_0a1b_0",
		"pid":              4242,
		"image":            "registry.local/api:1.2.3",
		"labels":           registryTestK8sLabels("prod", "api-0", "api"),
		"log_path":         "/var/log/pods/prod_api-0_0a1b/api/0.log",
		"crio_annotations": annotations,
		"annotations":      map[string]string{"io.kubernetes.container.hash": "abc"},
	})
	require.NoError(t, err)
	return string(b)
}

func TestCrioInspect(t *testing.T) {
	volumes := `[{"container_path":"/data","host_path":"/var/lib/kubelet/pods/0a1b/volumes/kubernetes.io~csi/pvc-0a1b2c3d-1111-2222-3333-444455556666/mount","readonly":false},` +
		`{"container_path":"/etc/config","host_path":"/var/lib/kubelet/pods/0a1b/volumes/kubernetes.io~configmap/cfg","readonly":true}]`
	crioTestServe(t, map[string]string{
		registryTestContainerID: crioTestInfo(t, map[string]string{"io.kubernetes.cri-o.Volumes": volumes}),
	})

	md, err := CrioInspect(registryTestContainerID)
	require.NoError(t, err)
	assert.Equal(t, "k8s_api_api-0_prod_0a1b_0", md.name)
	assert.Equal(t, "registry.local/api:1.2.3", md.image)
	assert.Equal(t, registryTestK8sLabels("prod", "api-0", "api"), md.labels)
	assert.Equal(t, "/var/log/pods/prod_api-0_0a1b/api/0.log", md.logPath)
	assert.IsType(t, logparser.CriDecoder{}, md.logDecoder)
	assert.Equal(t, map[string]string{
		"/data":       "pvc-0a1b2c3d-1111-2222-3333-444455556666",
		"/etc/config": "",
	}, md.volumes)

	// and the id derived from it is the k8s one
	assert.Equal(t, ContainerID("/k8s/prod/api-0/api"), calcId(&cgroup.Cgroup{ContainerType: cgroup.ContainerTypeCrio, ContainerId: registryTestContainerID}, md))
}

func TestCrioInspectVolumeAnnotationTolerance(t *testing.T) {
	for name, annotations := range map[string]map[string]string{
		"no annotations":      nil,
		"missing volumes key": {"io.kubernetes.cri-o.Labels": "{}"},
		"malformed volumes":   {"io.kubernetes.cri-o.Volumes": `[{"container_path":`},
		"wrong json type":     {"io.kubernetes.cri-o.Volumes": `{"container_path":"/x"}`},
	} {
		t.Run(name, func(t *testing.T) {
			crioTestServe(t, map[string]string{registryTestContainerID: crioTestInfo(t, annotations)})
			md, err := CrioInspect(registryTestContainerID)
			require.NoError(t, err, "a bad volumes annotation must not fail the whole inspect")
			assert.NotNil(t, md.volumes)
			assert.Empty(t, md.volumes)
			assert.Equal(t, "registry.local/api:1.2.3", md.image)
		})
	}
}

func TestCrioInspectErrors(t *testing.T) {
	t.Run("client not initialized", func(t *testing.T) {
		saved := crioClient
		crioClient = nil
		t.Cleanup(func() { crioClient = saved })
		md, err := CrioInspect(registryTestContainerID)
		assert.Error(t, err)
		assert.Nil(t, md)
	})
	t.Run("unknown container -> non-200", func(t *testing.T) {
		crioTestServe(t, map[string]string{})
		md, err := CrioInspect(registryTestContainerID)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "404")
		assert.Nil(t, md)
	})
	t.Run("malformed json", func(t *testing.T) {
		crioTestServe(t, map[string]string{registryTestContainerID: `{"name": "a", "labels": [`})
		md, err := CrioInspect(registryTestContainerID)
		assert.Error(t, err)
		assert.Nil(t, md)
	})
}

// crioTestHostHasSocket reports whether a CRI-O socket is visible via the host
// root; tests about the "no CRI-O" path skip on such hosts.
func crioTestHostHasSocket() bool {
	for _, s := range []string{"/var/run/crio/crio.sock", "/run/crio/crio.sock"} {
		if _, err := os.Stat(proc.HostPath(s)); err == nil {
			return true
		}
	}
	return false
}

func crioTestSaveClient(t *testing.T) {
	saved := crioClient
	t.Cleanup(func() { crioClient = saved })
}

func TestCrioInitWithoutSocketLeavesUsableErrorPath(t *testing.T) {
	if crioTestHostHasSocket() {
		t.Skip("host has a CRI-O socket")
	}
	crioTestSaveClient(t)
	crioClient = nil
	if err := CrioInit(); err != nil {
		return // correct contract: nothing more to check
	}
	// If init claims success a client must exist, and using it on a host
	// without CRI-O must fail with an error rather than hang or panic.
	require.NotNil(t, crioClient)
	md, err := CrioInspect(registryTestContainerID)
	assert.Error(t, err)
	assert.Nil(t, md)
}

func TestCrioInitReportsMissingSocket(t *testing.T) {
	// BUG: crio.go:52 `if _, err := os.Stat(...)` shadows the outer err, so CrioInit returns nil (and installs a client dialing "") when no CRI-O socket exists — unskip when fixed
	t.Skip("BUG: CrioInit never reports a missing CRI-O socket (err shadowed)")
	if crioTestHostHasSocket() {
		t.Skip("host has a CRI-O socket")
	}
	crioTestSaveClient(t)
	crioClient = nil
	assert.Error(t, CrioInit())
	assert.Nil(t, crioClient, "no client may be installed when CRI-O is absent")
}

func TestCrioClientHasOverallTimeout(t *testing.T) {
	// BUG: crio.go:63 crioClient has no http.Client.Timeout (crioTimeout only bounds the dial), so a CRI-O that accepts but never answers stalls CrioInspect — and the registry event loop — forever — unskip when fixed
	t.Skip("BUG: crioClient has no overall request timeout; a hung CRI-O blocks the event loop")
	crioTestSaveClient(t)
	_ = CrioInit()
	require.NotNil(t, crioClient)
	assert.Greater(t, crioClient.Timeout, time.Duration(0))
	assert.LessOrEqual(t, crioClient.Timeout, crioTimeout)
}
