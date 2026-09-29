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

func TestGetDigitalOceanMetadata(t *testing.T) {
	t.Run("happy path", func(t *testing.T) {
		tr := metadataTestInstall(t, metadataTestPaths(map[string]string{
			"/metadata/v1/id":     "2756294",
			"/metadata/v1/region": "nyc3",
		}))
		md := getDigitalOceanMetadata()
		require.NotNil(t, md)
		assert.Equal(t, &CloudMetadata{
			Provider:         CloudProviderDigitalOcean,
			InstanceId:       "2756294",
			Region:           "nyc3",
			AvailabilityZone: "nyc3", // DO has no AZs: region doubles as AZ
		}, md)
		assert.ElementsMatch(t, []string{
			"http://169.254.169.254/metadata/v1/id",
			"http://169.254.169.254/metadata/v1/region",
		}, tr.urls())
	})
	t.Run("any variable failing yields nil", func(t *testing.T) {
		metadataTestInstall(t, metadataTestPaths(map[string]string{"/metadata/v1/id": "2756294"}))
		assert.Nil(t, getDigitalOceanMetadata())
	})
	t.Run("non-200", func(t *testing.T) {
		metadataTestInstall(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		assert.Nil(t, getDigitalOceanMetadata())
	})
}
