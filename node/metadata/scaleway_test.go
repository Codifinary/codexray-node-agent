// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package metadata

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetScalewayMetadata(t *testing.T) {
	t.Run("happy path", func(t *testing.T) {
		tr := metadataTestInstall(t, metadataTestPaths(map[string]string{
			"/conf": "COMMERCIAL_TYPE=DEV1-S\nHOSTNAME=node-1\nID=0f2ad0b8-6f7a-4a1e-bf8a-000000000000\n" +
				"ZONE=fr-par-1\nTAGS_COUNT=0\nGARBAGE_LINE\nSSH_PUBLIC_KEYS_0_KEY=ssh-rsa AAA=b\n",
		}))
		md := getScalewayMetadata()
		require.NotNil(t, md)
		assert.Equal(t, &CloudMetadata{
			Provider:         CloudProviderScaleway,
			InstanceId:       "0f2ad0b8-6f7a-4a1e-bf8a-000000000000",
			InstanceType:     "DEV1-S",
			Region:           "fr-par",
			AvailabilityZone: "fr-par-1",
		}, md)
		assert.Equal(t, []string{"http://169.254.42.42/conf"}, tr.urls())
	})
	t.Run("empty body", func(t *testing.T) {
		metadataTestInstall(t, metadataTestPaths(map[string]string{"/conf": ""}))
		md := getScalewayMetadata()
		require.NotNil(t, md)
		assert.Equal(t, CloudProviderScaleway, md.Provider)
		assert.Empty(t, md.InstanceId)
		assert.Empty(t, md.Region)
	})
	t.Run("non-200", func(t *testing.T) {
		metadataTestInstall(t, metadataTestPaths(nil))
		assert.Nil(t, getScalewayMetadata())
	})
}

func TestScalewayAZSuffix(t *testing.T) {
	for zone, region := range map[string]string{
		"fr-par-1": "fr-par",
		"nl-ams-3": "nl-ams",
		"pl-waw":   "pl-waw",
		"":         "",
	} {
		assert.Equal(t, region, scalewayAZSuffix.ReplaceAllString(zone, ""), zone)
	}
}
