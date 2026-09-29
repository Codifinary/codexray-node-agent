// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package metadata

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// gcpTestServer serves the GCE metadata API (http://$GCE_METADATA_HOST/computeMetadata/v1/...).
func gcpTestServer(t *testing.T, vars map[string]string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Metadata-Flavor") != "Google" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		v, ok := vars[strings.TrimPrefix(r.URL.Path, "/computeMetadata/v1/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(v))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("GCE_METADATA_HOST", strings.TrimPrefix(srv.URL, "http://"))
}

func TestGetGcpMetadata(t *testing.T) {
	t.Run("happy path on-demand", func(t *testing.T) {
		gcpTestServer(t, map[string]string{
			"project/project-id":               "my-project",
			"instance/id":                      "1234567890",
			"instance/network-interfaces/0/ip": "10.128.0.5",
			"instance/network-interfaces/0/access-configs/0/external-ip": "34.1.2.3",
			"instance/scheduling/preemptible":                            "FALSE",
			"instance/machine-type":                                      "projects/123456/machineTypes/e2-standard-4",
			"instance/zone":                                              "projects/123456/zones/us-central1-a",
		})
		assert.Equal(t, &CloudMetadata{
			Provider:         CloudProviderGCP,
			AccountId:        "my-project",
			InstanceId:       "1234567890",
			InstanceType:     "e2-standard-4",
			LifeCycle:        "on-demand",
			Region:           "us-central1",
			AvailabilityZone: "us-central1-a",
			LocalIPv4:        "10.128.0.5",
			PublicIPv4:       "34.1.2.3",
		}, getGcpMetadata())
	})
	t.Run("preemptible, no external ip, malformed machine-type and zone", func(t *testing.T) {
		gcpTestServer(t, map[string]string{
			"project/project-id":              "p",
			"instance/scheduling/preemptible": "TRUE",
			"instance/machine-type":           "e2-small",
			"instance/zone":                   "us-central1-a",
		})
		md := getGcpMetadata()
		assert.Equal(t, "preemptible", md.LifeCycle)
		assert.Empty(t, md.PublicIPv4)
		assert.Empty(t, md.InstanceType)
		assert.Empty(t, md.AvailabilityZone)
		assert.Empty(t, md.Region)
	})
	t.Run("zone without dash", func(t *testing.T) {
		gcpTestServer(t, map[string]string{
			"project/project-id": "p",
			"instance/zone":      "projects/1/zones/local",
		})
		md := getGcpMetadata()
		assert.Equal(t, "local", md.AvailabilityZone)
		assert.Empty(t, md.Region)
		assert.Empty(t, md.LifeCycle)
	})
	t.Run("no project id stops early", func(t *testing.T) {
		gcpTestServer(t, map[string]string{"instance/id": "should-not-be-read"})
		assert.Equal(t, &CloudMetadata{Provider: CloudProviderGCP}, getGcpMetadata())
	})
}
