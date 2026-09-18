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

// getAwsToken / getAwsMetadata dial IMDSv2 from inside the host network
// namespace (proc.GetHostNetNs + setns), which is not reachable in a unit test;
// only the per-variable fetch is exercised here.

func TestGetAwsMetadataVariable(t *testing.T) {
	tr := metadataTestInstall(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-aws-ec2-metadata-token") != "tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/latest/meta-data/instance-id":
			_, _ = w.Write([]byte("i-0abc"))
		case "/latest/meta-data/placement/availability-zone-id":
			_, _ = w.Write([]byte("use1-az4"))
		case "/latest/meta-data/instance-life-cycle":
			_, _ = w.Write([]byte("spot"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))

	assert.Equal(t, "i-0abc", getAwsMetadataVariable("tok", "instance-id"))
	assert.Equal(t, "use1-az4", getAwsMetadataVariable("tok", "placement/availability-zone-id"))
	assert.Equal(t, "spot", getAwsMetadataVariable("tok", "instance-life-cycle"))

	// public-ipv4 is absent (404) on instances without a public IP: empty, no panic.
	assert.Equal(t, "", getAwsMetadataVariable("tok", "public-ipv4"))
	// wrong/expired token: 401 -> empty
	assert.Equal(t, "", getAwsMetadataVariable("bad", "instance-id"))

	urls := tr.urls()
	require.NotEmpty(t, urls)
	assert.Equal(t, "http://169.254.169.254/latest/meta-data/instance-id", urls[0])
	for _, r := range tr.requests {
		assert.Equal(t, http.MethodGet, r.Method)
	}
}
