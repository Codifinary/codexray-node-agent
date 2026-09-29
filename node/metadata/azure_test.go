// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package metadata

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Trimmed IMDS /metadata/instance?api-version=2021-05-01 response.
const azureTestResponse = `{
  "compute": {
    "location": "westeurope",
    "vmId": "02aab8a4-74ef-476e-8182-f6d2ba4166a6",
    "vmSize": "Standard_D2s_v3",
    "zone": "2",
    "subscriptionId": "8d10da13-8125-4ba9-a717-bf7490507b3d",
    "priority": "Spot"
  },
  "network": {
    "interface": [{
      "ipv4": {"ipAddress": [{"privateIpAddress": "10.144.133.132", "publicIpAddress": "20.1.2.3"}]}
    }]
  }
}`

func TestGetAzureMetadata(t *testing.T) {
	t.Run("happy path", func(t *testing.T) {
		tr := metadataTestInstall(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Metadata") != "True" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			_, _ = w.Write([]byte(azureTestResponse))
		}))
		md := getAzureMetadata()
		require.NotNil(t, md)
		assert.Equal(t, &CloudMetadata{
			Provider:         CloudProviderAzure,
			AccountId:        "8d10da13-8125-4ba9-a717-bf7490507b3d",
			InstanceId:       "02aab8a4-74ef-476e-8182-f6d2ba4166a6",
			InstanceType:     "Standard_D2s_v3",
			Region:           "westeurope",
			AvailabilityZone: "2",
			LocalIPv4:        "10.144.133.132",
			PublicIPv4:       "20.1.2.3",
		}, md)
		require.Len(t, tr.requests, 1)
		u := tr.requests[0].URL
		assert.Contains(t, u, "http://169.254.169.254/metadata/instance?")
		assert.Contains(t, u, "api-version=2021-05-01")
		assert.Contains(t, u, "format=json")
	})
	t.Run("no network interfaces", func(t *testing.T) {
		metadataTestInstall(t, metadataTestPaths(map[string]string{
			"/metadata/instance": `{"compute":{"location":"eastus","vmId":"id1"},"network":{"interface":[]}}`,
		}))
		md := getAzureMetadata()
		require.NotNil(t, md)
		assert.Equal(t, "eastus", md.Region)
		assert.Equal(t, "id1", md.InstanceId)
		assert.Empty(t, md.LocalIPv4)
		assert.Empty(t, md.PublicIPv4)
	})
	t.Run("interface without addresses", func(t *testing.T) {
		metadataTestInstall(t, metadataTestPaths(map[string]string{
			"/metadata/instance": `{"compute":{},"network":{"interface":[{"ipv4":{"ipAddress":[]}}]}}`,
		}))
		md := getAzureMetadata()
		require.NotNil(t, md)
		assert.Empty(t, md.LocalIPv4)
	})
	t.Run("malformed json", func(t *testing.T) {
		metadataTestInstall(t, metadataTestPaths(map[string]string{"/metadata/instance": `{"compute": {"location": `}))
		assert.Nil(t, getAzureMetadata())
	})
	t.Run("non-200", func(t *testing.T) {
		metadataTestInstall(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(azureTestResponse))
		}))
		assert.Nil(t, getAzureMetadata())
	})
}
