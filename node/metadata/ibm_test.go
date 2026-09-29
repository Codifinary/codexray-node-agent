// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package metadata

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

const ibmTestInstance = `{"id":"0717_abc","name":"vsi-1","profile":{"name":"bx2-2x8"},"zone":{"name":"us-south-1"}}`

// ibmTestHandler is a fake IBM instance metadata service speaking plain http.
func ibmTestHandler(tokenBody string, instanceStatus int, instanceBody string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/instance_identity/v1/token":
			if r.Header.Get("Metadata-Flavor") != "ibm" || r.URL.Query().Get("version") != "2025-04-22" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if tokenBody == "" {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			_, _ = w.Write([]byte(tokenBody))
		case r.Method == http.MethodGet && r.URL.Path == "/metadata/v1/instance":
			if r.Header.Get("Authorization") != "Bearer tok" || r.URL.Query().Get("version") != "2025-04-22" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.WriteHeader(instanceStatus)
			_, _ = w.Write([]byte(instanceBody))
		default:
			http.NotFound(w, r)
		}
	})
}

// ibmTestStart serves h at ibmInstanceMetadataAddress and counts accepted
// connections (a TLS attempt against this plain-http server is one more).
func ibmTestStart(t *testing.T, h http.Handler) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	conns := &atomic.Int32{}
	srv := httptest.NewUnstartedServer(h)
	srv.Config.ConnState = func(_ net.Conn, st http.ConnState) {
		if st == http.StateNew {
			conns.Add(1)
		}
	}
	srv.Start()
	t.Cleanup(srv.Close)
	orig := ibmInstanceMetadataAddress
	ibmInstanceMetadataAddress = strings.TrimPrefix(srv.URL, "http://")
	t.Cleanup(func() { ibmInstanceMetadataAddress = orig })
	metadataTestSelfNetNs(t)
	origTimeout := http.DefaultClient.Timeout
	t.Cleanup(func() { http.DefaultClient.Timeout = origTimeout })
	return srv, conns
}

func TestGetIBMToken(t *testing.T) {
	t.Run("http success", func(t *testing.T) {
		ibmTestStart(t, ibmTestHandler(`{"access_token":"tok","expires_in":300}`, http.StatusOK, ibmTestInstance))
		token, err := getIBMToken("http")
		require.NoError(t, err)
		assert.Equal(t, "tok", token)
	})
	t.Run("non-200", func(t *testing.T) {
		ibmTestStart(t, ibmTestHandler("", http.StatusOK, ibmTestInstance))
		_, err := getIBMToken("http")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "500")
	})
	t.Run("bad json", func(t *testing.T) {
		ibmTestStart(t, ibmTestHandler(`{"access_token":`, http.StatusOK, ibmTestInstance))
		_, err := getIBMToken("http")
		assert.Error(t, err)
	})
	t.Run("https against a non-TLS endpoint", func(t *testing.T) {
		ibmTestStart(t, ibmTestHandler(`{"access_token":"tok"}`, http.StatusOK, ibmTestInstance))
		_, err := getIBMToken("https")
		assert.Error(t, err)
	})
	t.Run("host netns lookup failure", func(t *testing.T) {
		_, conns := ibmTestStart(t, ibmTestHandler(`{"access_token":"tok"}`, http.StatusOK, ibmTestInstance))
		metadataTestHostNetNsErr(t, errors.New("permission denied"))
		_, err := getIBMToken("http")
		assert.EqualError(t, err, "permission denied")
		assert.Zero(t, conns.Load())
	})
}

func TestGetIBMMetadata(t *testing.T) {
	t.Run("happy path", func(t *testing.T) {
		_, conns := ibmTestStart(t, ibmTestHandler(`{"access_token":"tok"}`, http.StatusOK, ibmTestInstance))
		assert.Equal(t, &CloudMetadata{
			Provider:         CloudProviderIBM,
			InstanceId:       "0717_abc",
			InstanceType:     "bx2-2x8",
			Region:           "us-south",
			AvailabilityZone: "us-south-1",
		}, getIBMMetadata())
		assert.EqualValues(t, 2, conns.Load(), "token over http, then the instance GET; no https fallback")
	})
	t.Run("http token fails: falls back to https, which also fails", func(t *testing.T) {
		_, conns := ibmTestStart(t, ibmTestHandler("", http.StatusOK, ibmTestInstance))
		assert.Nil(t, getIBMMetadata())
		assert.EqualValues(t, 2, conns.Load(), "one http token attempt and one https token attempt")
	})
	t.Run("http token bad json: falls back to https", func(t *testing.T) {
		_, conns := ibmTestStart(t, ibmTestHandler(`not json`, http.StatusOK, ibmTestInstance))
		assert.Nil(t, getIBMMetadata())
		assert.EqualValues(t, 2, conns.Load())
	})
	t.Run("service unreachable", func(t *testing.T) {
		srv, _ := ibmTestStart(t, ibmTestHandler(`{"access_token":"tok"}`, http.StatusOK, ibmTestInstance))
		srv.Close()
		assert.Nil(t, getIBMMetadata())
	})
	t.Run("host netns lookup failure", func(t *testing.T) {
		_, conns := ibmTestStart(t, ibmTestHandler(`{"access_token":"tok"}`, http.StatusOK, ibmTestInstance))
		metadataTestHostNetNsErr(t, errors.New("permission denied"))
		assert.Nil(t, getIBMMetadata())
		assert.Zero(t, conns.Load())
	})
	t.Run("instance non-200", func(t *testing.T) {
		ibmTestStart(t, ibmTestHandler(`{"access_token":"tok"}`, http.StatusForbidden, ibmTestInstance))
		assert.Nil(t, getIBMMetadata())
	})
	t.Run("instance bad json", func(t *testing.T) {
		ibmTestStart(t, ibmTestHandler(`{"access_token":"tok"}`, http.StatusOK, `{"id":`))
		assert.Nil(t, getIBMMetadata())
	})
	t.Run("zone without numeric suffix", func(t *testing.T) {
		ibmTestStart(t, ibmTestHandler(`{"access_token":"tok"}`, http.StatusOK, `{"id":"x","zone":{"name":"jp-tok"}}`))
		md := getIBMMetadata()
		require.NotNil(t, md)
		assert.Equal(t, "jp-tok", md.Region)
		assert.Equal(t, "jp-tok", md.AvailabilityZone)
		assert.Empty(t, md.InstanceType)
	})
}
