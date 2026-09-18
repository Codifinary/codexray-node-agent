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

func TestGetOracleMetadata(t *testing.T) {
	t.Run("happy path", func(t *testing.T) {
		tr := metadataTestInstall(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer Oracle" || r.URL.Path != "/opc/v2/instance/" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"id":"ocid1.instance.oc1.iad.abc","canonicalRegionName":"us-ashburn-1",
				"availabilityDomain":"Uocm:US-ASHBURN-AD-2","shape":"VM.Standard.E4.Flex","region":"iad"}`))
		}))
		md := getOracleMetadata()
		require.NotNil(t, md)
		assert.Equal(t, &CloudMetadata{
			Provider:         CloudProviderOracle,
			InstanceId:       "ocid1.instance.oc1.iad.abc",
			InstanceType:     "VM.Standard.E4.Flex",
			Region:           "us-ashburn-1",
			AvailabilityZone: "us-ashburn-1-ad-2",
		}, md)
		assert.Equal(t, []string{"http://169.254.169.254/opc/v2/instance/"}, tr.urls())
	})
	t.Run("unrecognized availability domain", func(t *testing.T) {
		metadataTestInstall(t, metadataTestPaths(map[string]string{
			"/opc/v2/instance/": `{"id":"x","canonicalRegionName":"eu-frankfurt-1","availabilityDomain":"weird"}`,
		}))
		md := getOracleMetadata()
		require.NotNil(t, md)
		assert.Equal(t, "eu-frankfurt-1", md.Region)
		assert.Empty(t, md.AvailabilityZone)
	})
	t.Run("malformed json", func(t *testing.T) {
		metadataTestInstall(t, metadataTestPaths(map[string]string{"/opc/v2/instance/": `{"id":`}))
		assert.Nil(t, getOracleMetadata())
	})
	t.Run("non-200", func(t *testing.T) {
		metadataTestInstall(t, metadataTestPaths(nil))
		assert.Nil(t, getOracleMetadata())
	})
}
