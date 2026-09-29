// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package metadata

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/codifinary/codexray-node-agent/proc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vishvananda/netns"
)

// metadataTestRequest is what the fake metadata service observed.
type metadataTestRequest struct {
	Method string
	URL    string
	Header http.Header
}

// metadataTestBody wraps a response body and records whether it was closed.
type metadataTestBody struct {
	io.Reader
	mu     sync.Mutex
	closed bool
}

func (b *metadataTestBody) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	return nil
}

func (b *metadataTestBody) isClosed() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.closed
}

// metadataTestTransport serves every request of http.DefaultClient from an
// in-memory handler, so the provider code runs unchanged against its real
// (const) metadata URLs without touching the network.
type metadataTestTransport struct {
	handler  http.Handler
	mu       sync.Mutex
	requests []metadataTestRequest
	bodies   []*metadataTestBody
}

func (tr *metadataTestTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	tr.mu.Lock()
	tr.requests = append(tr.requests, metadataTestRequest{Method: r.Method, URL: r.URL.String(), Header: r.Header.Clone()})
	tr.mu.Unlock()
	rec := httptest.NewRecorder()
	tr.handler.ServeHTTP(rec, r)
	resp := rec.Result()
	body := &metadataTestBody{Reader: resp.Body}
	resp.Body = body
	resp.Request = r
	tr.mu.Lock()
	tr.bodies = append(tr.bodies, body)
	tr.mu.Unlock()
	return resp, nil
}

func (tr *metadataTestTransport) urls() []string {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	var res []string
	for _, r := range tr.requests {
		res = append(res, r.URL)
	}
	return res
}

// metadataTestInstall routes http.DefaultClient to the handler and restores it after the test.
func metadataTestInstall(t *testing.T, h http.Handler) *metadataTestTransport {
	t.Helper()
	tr := &metadataTestTransport{handler: h}
	origTransport := http.DefaultClient.Transport
	origTimeout := http.DefaultClient.Timeout
	http.DefaultClient.Transport = tr
	t.Cleanup(func() {
		http.DefaultClient.Transport = origTransport
		http.DefaultClient.Timeout = origTimeout
	})
	return tr
}

// metadataTestPaths serves fixed bodies per URL path; unknown paths are 404.
func metadataTestPaths(bodies map[string]string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, ok := bodies[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, b)
	})
}

func TestHttpCallWithTimeout(t *testing.T) {
	t.Run("200 returns response", func(t *testing.T) {
		metadataTestInstall(t, metadataTestPaths(map[string]string{"/x": "ok"}))
		r, _ := http.NewRequest(http.MethodGet, "http://169.254.169.254/x", nil)
		resp, err := httpCallWithTimeout(r)
		require.NoError(t, err)
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		assert.Equal(t, "ok", string(b))
	})
	t.Run("non-200 is an error", func(t *testing.T) {
		metadataTestInstall(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		r, _ := http.NewRequest(http.MethodGet, "http://169.254.169.254/x", nil)
		resp, err := httpCallWithTimeout(r)
		require.Error(t, err)
		assert.Nil(t, resp)
		assert.Contains(t, err.Error(), "500")
	})
	t.Run("transport error is returned", func(t *testing.T) {
		origTransport := http.DefaultClient.Transport
		origTimeout := http.DefaultClient.Timeout
		t.Cleanup(func() {
			http.DefaultClient.Transport = origTransport
			http.DefaultClient.Timeout = origTimeout
		})
		http.DefaultClient.Transport = metadataTestErrTransport{}
		r, _ := http.NewRequest(http.MethodGet, "http://169.254.169.254/x", nil)
		resp, err := httpCallWithTimeout(r)
		assert.Error(t, err)
		assert.Nil(t, resp)
	})
}

type metadataTestErrTransport struct{}

func (metadataTestErrTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, io.ErrUnexpectedEOF
}

func TestHttpCallWithTimeoutClosesBodyOnNon200(t *testing.T) {
	// BUG: httpCallWithTimeout returns nil on non-200 without closing resp.Body (leaks the connection) — unskip when fixed
	t.Skip("BUG: httpCallWithTimeout does not close resp.Body on non-200 (metadata.go:122)")
	tr := metadataTestInstall(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	r, _ := http.NewRequest(http.MethodGet, "http://169.254.169.254/x", nil)
	_, err := httpCallWithTimeout(r)
	require.Error(t, err)
	require.Len(t, tr.bodies, 1)
	assert.True(t, tr.bodies[0].isClosed(), "body of a discarded non-200 response must be closed")
}

func TestHttpCallWithTimeoutDoesNotMutateDefaultClient(t *testing.T) {
	// BUG: httpCallWithTimeout sets http.DefaultClient.Timeout globally (process-wide side effect) — unskip when fixed
	t.Skip("BUG: httpCallWithTimeout mutates the global http.DefaultClient.Timeout (metadata.go:116-117)")
	metadataTestInstall(t, metadataTestPaths(map[string]string{"/x": "ok"}))
	http.DefaultClient.Timeout = 0
	r, _ := http.NewRequest(http.MethodGet, "http://169.254.169.254/x", nil)
	resp, err := httpCallWithTimeout(r)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, time.Duration(0), http.DefaultClient.Timeout)
}

func TestHttpCallWithTimeoutAppliesTimeout(t *testing.T) {
	// Contract: every metadata call is bounded by metadataServiceTimeout. The
	// constant (5s) is not overridable, so instead of hanging we assert the
	// request reaches the transport with a deadline no later than that bound.
	var deadline time.Time
	var hasDeadline bool
	metadataTestInstall(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		deadline, hasDeadline = r.Context().Deadline()
		_, _ = io.WriteString(w, "ok")
	}))
	start := time.Now()
	r, _ := http.NewRequest(http.MethodGet, "http://169.254.169.254/x", nil)
	resp, err := httpCallWithTimeout(r)
	require.NoError(t, err)
	resp.Body.Close()
	require.True(t, hasDeadline, "metadata request must carry a deadline")
	assert.False(t, deadline.After(start.Add(metadataServiceTimeout+time.Second)))
}

func TestGetInstanceMetadataUnknownProviderSmoke(t *testing.T) {
	// getCloudProvider reads hard-coded /sys paths; the result is host-dependent,
	// so only assert it is one of the known values and never panics.
	p := getCloudProvider()
	assert.Contains(t, []CloudProvider{
		CloudProviderAWS, CloudProviderGCP, CloudProviderAzure, CloudProviderHetzner,
		CloudProviderDigitalOcean, CloudProviderAlibaba, CloudProviderScaleway,
		CloudProviderIBM, CloudProviderOracle, CloudProviderUnknown,
	}, p)
}

// metadataTestReadErrTransport answers 200 with a body that fails mid-read
// (e.g. the metadata service resetting the connection).
type metadataTestReadErrTransport struct{}

type metadataTestErrBody struct{}

func (metadataTestErrBody) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func (metadataTestErrBody) Close() error             { return nil }

func (metadataTestReadErrTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: metadataTestErrBody{}, Header: http.Header{}, Request: r}, nil
}

func TestProvidersBodyReadError(t *testing.T) {
	origTransport := http.DefaultClient.Transport
	origTimeout := http.DefaultClient.Timeout
	t.Cleanup(func() {
		http.DefaultClient.Transport = origTransport
		http.DefaultClient.Timeout = origTimeout
	})
	http.DefaultClient.Transport = metadataTestReadErrTransport{}

	assert.Equal(t, "", getAwsMetadataVariable("tok", "instance-id"))
	assert.Nil(t, getAlibabaMetadata())
	assert.Nil(t, getDigitalOceanMetadata())
	assert.Nil(t, getScalewayMetadata())
	assert.Nil(t, getAzureMetadata())
	assert.Nil(t, getHetznerMetadata())
	assert.Nil(t, getOracleMetadata())
}

func TestGetInstanceMetadataDispatch(t *testing.T) {
	// Every metadata request is served by the in-memory fake (404 everywhere),
	// so this is safe and offline on any host, cloud or not. Which branch runs
	// depends on the host's /sys DMI files, so only invariants are asserted.
	tr := metadataTestInstall(t, http.NotFoundHandler())

	p := getCloudProvider()
	assert.Equal(t, p, getCloudProvider(), "provider detection must be deterministic")

	var md *CloudMetadata
	require.NotPanics(t, func() { md = GetInstanceMetadata() })
	if p == CloudProviderUnknown {
		assert.Nil(t, md, "unknown provider: no metadata")
		assert.Empty(t, tr.urls(), "unknown provider: the metadata service must not be queried")
		return
	}
	if md != nil {
		assert.Equal(t, p, md.Provider, "metadata must be attributed to the detected provider")
	}
	for _, b := range tr.bodies {
		assert.True(t, b.isClosed(), "every metadata response body must be closed")
	}
}

// metadataTestSysfs points getCloudProvider at a fake /sys built from files
// (paths relative to /sys).
func metadataTestSysfs(t *testing.T, files map[string]string) {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		p := filepath.Join(root, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	}
	orig := sysfsRoot
	sysfsRoot = root
	t.Cleanup(func() { sysfsRoot = orig })
}

// metadataTestSelfNetNs makes the "host" netns the test's own one, so the
// token dialers run without setns (and without root).
func metadataTestSelfNetNs(t *testing.T) {
	t.Helper()
	orig := getHostNetNs
	getHostNetNs = proc.GetSelfNetNs
	t.Cleanup(func() { getHostNetNs = orig })
}

func metadataTestHostNetNsErr(t *testing.T, err error) {
	t.Helper()
	orig := getHostNetNs
	getHostNetNs = func() (netns.NsHandle, error) { return netns.None(), err }
	t.Cleanup(func() { getHostNetNs = orig })
}

func TestGetCloudProvider(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files map[string]string
		want  CloudProvider
	}{
		{"empty sysfs", nil, CloudProviderUnknown},
		{"xen ec2 uuid", map[string]string{"hypervisor/uuid": "ec2e1916-9099-7caf-fd21-012345abcdef\n"}, CloudProviderAWS},
		{"xen ec2 uuid upper-case", map[string]string{"hypervisor/uuid": "EC2E1916-9099-7CAF-FD21-012345ABCDEF\n"}, CloudProviderAWS},
		{"xen non-ec2 uuid", map[string]string{"hypervisor/uuid": "4c4c4544-0042-3510-8052-b4c04f4d3232\n"}, CloudProviderUnknown},
		{"ec2 uuid wins over board_vendor", map[string]string{
			"hypervisor/uuid": "ec2abc", "class/dmi/id/board_vendor": "Google\n",
		}, CloudProviderAWS},
		{"non-ec2 uuid falls through to dmi", map[string]string{
			"hypervisor/uuid": "abc", "class/dmi/id/board_vendor": "Google\n",
		}, CloudProviderGCP},
		{"board Amazon EC2", map[string]string{"class/dmi/id/board_vendor": "Amazon EC2\n"}, CloudProviderAWS},
		{"board Google", map[string]string{"class/dmi/id/board_vendor": "Google\n"}, CloudProviderGCP},
		{"board Microsoft", map[string]string{"class/dmi/id/board_vendor": "Microsoft Corporation\n"}, CloudProviderAzure},
		{"board DigitalOcean", map[string]string{"class/dmi/id/board_vendor": "DigitalOcean\n"}, CloudProviderDigitalOcean},
		{"sys Hetzner", map[string]string{
			"class/dmi/id/board_vendor": "Dell Inc.\n", "class/dmi/id/sys_vendor": "Hetzner\n",
		}, CloudProviderHetzner},
		{"sys Alibaba", map[string]string{"class/dmi/id/sys_vendor": "Alibaba Cloud\n"}, CloudProviderAlibaba},
		{"sys Scaleway", map[string]string{"class/dmi/id/sys_vendor": "Scaleway\n"}, CloudProviderScaleway},
		{"chassis IBM", map[string]string{
			"class/dmi/id/sys_vendor": "QEMU\n", "class/dmi/id/chassis_vendor": "IBM:Cloud Compute Server 1.0:Nitro\n",
		}, CloudProviderIBM},
		{"chassis Oracle", map[string]string{
			"class/dmi/id/chassis_vendor": "QEMU\n", "class/dmi/id/chassis_asset_tag": "OracleCloud.com\n",
		}, CloudProviderOracle},
		{"bare metal", map[string]string{
			"class/dmi/id/board_vendor":      "LENOVO\n",
			"class/dmi/id/sys_vendor":        "LENOVO\n",
			"class/dmi/id/chassis_vendor":    "LENOVO\n",
			"class/dmi/id/chassis_asset_tag": "No Asset Information\n",
		}, CloudProviderUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			metadataTestSysfs(t, tc.files)
			assert.Equal(t, tc.want, getCloudProvider())
		})
	}
}

func TestGetInstanceMetadataFakeSysfs(t *testing.T) {
	origTimeout := http.DefaultClient.Timeout
	t.Cleanup(func() { http.DefaultClient.Timeout = origTimeout })

	t.Run("unknown", func(t *testing.T) {
		tr := metadataTestInstall(t, http.NotFoundHandler())
		metadataTestSysfs(t, nil)
		assert.Nil(t, GetInstanceMetadata())
		assert.Empty(t, tr.urls(), "the metadata service must not be queried")
	})
	t.Run("AWS", func(t *testing.T) {
		metadataTestSysfs(t, map[string]string{"class/dmi/id/board_vendor": "Amazon EC2\n"})
		awsTestStart(t, http.StatusOK, awsTestVars())
		md := GetInstanceMetadata()
		require.NotNil(t, md)
		assert.Equal(t, CloudProviderAWS, md.Provider)
		assert.Equal(t, "i-0abc", md.InstanceId)
	})
	t.Run("GCP", func(t *testing.T) {
		metadataTestSysfs(t, map[string]string{"class/dmi/id/board_vendor": "Google\n"})
		gcpTestServer(t, map[string]string{"project/project-id": "my-project", "instance/id": "42"})
		md := GetInstanceMetadata()
		require.NotNil(t, md)
		assert.Equal(t, CloudProviderGCP, md.Provider)
		assert.Equal(t, "42", md.InstanceId)
	})
	t.Run("Azure", func(t *testing.T) {
		metadataTestSysfs(t, map[string]string{"class/dmi/id/board_vendor": "Microsoft Corporation\n"})
		metadataTestInstall(t, metadataTestPaths(map[string]string{"/metadata/instance": azureTestResponse}))
		md := GetInstanceMetadata()
		require.NotNil(t, md)
		assert.Equal(t, CloudProviderAzure, md.Provider)
		assert.Equal(t, "02aab8a4-74ef-476e-8182-f6d2ba4166a6", md.InstanceId)
	})
	t.Run("DigitalOcean", func(t *testing.T) {
		metadataTestSysfs(t, map[string]string{"class/dmi/id/board_vendor": "DigitalOcean\n"})
		metadataTestInstall(t, metadataTestPaths(map[string]string{"/metadata/v1/id": "2756294", "/metadata/v1/region": "nyc3"}))
		md := GetInstanceMetadata()
		require.NotNil(t, md)
		assert.Equal(t, CloudProviderDigitalOcean, md.Provider)
		assert.Equal(t, "2756294", md.InstanceId)
	})
	t.Run("Hetzner", func(t *testing.T) {
		metadataTestSysfs(t, map[string]string{"class/dmi/id/sys_vendor": "Hetzner\n"})
		metadataTestInstall(t, metadataTestPaths(map[string]string{"/hetzner/v1/metadata": "instance-id: 42\nregion: eu-central\n"}))
		md := GetInstanceMetadata()
		require.NotNil(t, md)
		assert.Equal(t, CloudProviderHetzner, md.Provider)
		assert.Equal(t, "42", md.InstanceId)
	})
	t.Run("Alibaba", func(t *testing.T) {
		metadataTestSysfs(t, map[string]string{"class/dmi/id/sys_vendor": "Alibaba Cloud\n"})
		metadataTestInstall(t, metadataTestPaths(alibabaTestBodies()))
		md := GetInstanceMetadata()
		require.NotNil(t, md)
		assert.Equal(t, CloudProviderAlibaba, md.Provider)
		assert.Equal(t, "cn-hangzhou", md.Region)
	})
	t.Run("Scaleway", func(t *testing.T) {
		metadataTestSysfs(t, map[string]string{"class/dmi/id/sys_vendor": "Scaleway\n"})
		metadataTestInstall(t, metadataTestPaths(map[string]string{"/conf": "ID=abc\nZONE=fr-par-1\n"}))
		md := GetInstanceMetadata()
		require.NotNil(t, md)
		assert.Equal(t, CloudProviderScaleway, md.Provider)
		assert.Equal(t, "abc", md.InstanceId)
	})
	t.Run("IBM", func(t *testing.T) {
		metadataTestSysfs(t, map[string]string{"class/dmi/id/chassis_vendor": "IBM:Cloud Compute Server 1.0\n"})
		ibmTestStart(t, ibmTestHandler(`{"access_token":"tok"}`, http.StatusOK, ibmTestInstance))
		md := GetInstanceMetadata()
		require.NotNil(t, md)
		assert.Equal(t, CloudProviderIBM, md.Provider)
		assert.Equal(t, "0717_abc", md.InstanceId)
	})
	t.Run("Oracle", func(t *testing.T) {
		metadataTestSysfs(t, map[string]string{"class/dmi/id/chassis_asset_tag": "OracleCloud.com\n"})
		metadataTestInstall(t, metadataTestPaths(map[string]string{"/opc/v2/instance/": `{"id":"ocid1.instance.oc1.iad.abc","canonicalRegionName":"us-ashburn-1"}`}))
		md := GetInstanceMetadata()
		require.NotNil(t, md)
		assert.Equal(t, CloudProviderOracle, md.Provider)
		assert.True(t, strings.HasPrefix(md.InstanceId, "ocid1."), md.InstanceId)
	})
}
