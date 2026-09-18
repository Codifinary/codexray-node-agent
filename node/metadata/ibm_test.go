// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package metadata

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// getIBMToken / getIBMMetadata fetch the token from inside the host network
// namespace (setns), which is not reachable in a unit test. The pure parts —
// the response shapes and the zone -> region derivation — are covered here.

func TestIBMAZSuffix(t *testing.T) {
	for zone, region := range map[string]string{
		"us-south-1": "us-south",
		"eu-de-3":    "eu-de",
		"jp-tok":     "jp-tok",
		"":           "",
	} {
		assert.Equal(t, region, ibmAZSuffix.ReplaceAllString(zone, ""), zone)
	}
}

func TestIBMResponseDecoding(t *testing.T) {
	tr := ibmTokenResponse{}
	require.NoError(t, json.Unmarshal([]byte(`{"access_token":"eyJ.x.y","created_at":"2025-01-01T00:00:00Z","expires_in":300}`), &tr))
	assert.Equal(t, "eyJ.x.y", tr.AccessToken)

	md := ibmMetadata{}
	require.NoError(t, json.Unmarshal([]byte(`{"id":"0717_abc","name":"vsi-1","profile":{"name":"bx2-2x8"},"zone":{"name":"us-south-1"}}`), &md))
	assert.Equal(t, "0717_abc", md.Id)
	assert.Equal(t, "bx2-2x8", md.Profile.Name)
	assert.Equal(t, "us-south-1", md.Zone.Name)
}
