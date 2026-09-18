// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package metadata

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func alibabaTestBodies() map[string]string {
	return map[string]string{
		"/latest/meta-data/instance-id":            "i-bp67acfmxazb4p****",
		"/latest/meta-data/region-id":              "cn-hangzhou",
		"/latest/meta-data/zone-id":                "cn-hangzhou-i",
		"/latest/meta-data/owner-account-id":       "1609****",
		"/latest/meta-data/instance/instance-type": "ecs.g6.large",
		"/latest/meta-data/private-ipv4":           "192.168.0.10",
	}
}

func TestGetAlibabaMetadata(t *testing.T) {
	t.Run("happy path", func(t *testing.T) {
		tr := metadataTestInstall(t, metadataTestPaths(alibabaTestBodies()))
		md := getAlibabaMetadata()
		require.NotNil(t, md)
		assert.Equal(t, &CloudMetadata{
			Provider:         CloudProviderAlibaba,
			AccountId:        "1609****",
			InstanceId:       "i-bp67acfmxazb4p****",
			InstanceType:     "ecs.g6.large",
			Region:           "cn-hangzhou",
			AvailabilityZone: "cn-hangzhou-i",
			LocalIPv4:        "192.168.0.10",
		}, md)
		for _, u := range tr.urls() {
			assert.True(t, strings.HasPrefix(u, "http://100.100.100.200/latest/meta-data/"), u)
		}
	})
	t.Run("any variable failing yields nil", func(t *testing.T) {
		b := alibabaTestBodies()
		delete(b, "/latest/meta-data/owner-account-id")
		metadataTestInstall(t, metadataTestPaths(b))
		assert.Nil(t, getAlibabaMetadata())
	})
	t.Run("non-200", func(t *testing.T) {
		metadataTestInstall(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		}))
		assert.Nil(t, getAlibabaMetadata())
	})
}
