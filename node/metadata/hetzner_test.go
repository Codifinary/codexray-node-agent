// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package metadata

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetHetznerMetadata(t *testing.T) {
	t.Run("happy path", func(t *testing.T) {
		tr := metadataTestInstall(t, metadataTestPaths(map[string]string{
			"/hetzner/v1/metadata": "availability-zone: fsn1-dc14\nhostname: node-1\ninstance-id: 42\n" +
				"local-ipv4: 10.0.0.2\npublic-ipv4: 1.2.3.4\nregion: eu-central\npublic-keys: []\n",
		}))
		md := getHetznerMetadata()
		require.NotNil(t, md)
		assert.Equal(t, &CloudMetadata{
			Provider:         CloudProviderHetzner,
			InstanceId:       "42",
			Region:           "eu-central",
			AvailabilityZone: "fsn1-dc14",
			LocalIPv4:        "10.0.0.2",
			PublicIPv4:       "1.2.3.4",
		}, md)
		assert.Equal(t, []string{"http://169.254.169.254/hetzner/v1/metadata"}, tr.urls())
	})
	t.Run("malformed yaml", func(t *testing.T) {
		metadataTestInstall(t, metadataTestPaths(map[string]string{"/hetzner/v1/metadata": "region: [unterminated\n"}))
		assert.Nil(t, getHetznerMetadata())
	})
	t.Run("non-200", func(t *testing.T) {
		metadataTestInstall(t, metadataTestPaths(nil))
		assert.Nil(t, getHetznerMetadata())
	})
}
