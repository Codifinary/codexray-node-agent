// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package metadata

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

// awsTestServer is a fake IMDSv2: PUT /latest/api/token returns "tok" (or
// tokenStatus), GET /latest/meta-data/<var> requires that token.
type awsTestServer struct {
	*httptest.Server
	mu       sync.Mutex
	requests []metadataTestRequest
}

func (s *awsTestServer) recorded() []metadataTestRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]metadataTestRequest(nil), s.requests...)
}

func awsTestStart(t *testing.T, tokenStatus int, vars map[string]string) *awsTestServer {
	t.Helper()
	s := &awsTestServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.requests = append(s.requests, metadataTestRequest{Method: r.Method, URL: r.URL.String(), Header: r.Header.Clone()})
		s.mu.Unlock()
		if r.URL.Path == "/latest/api/token" {
			if r.Method != http.MethodPut || r.Header.Get("X-aws-ec2-metadata-token-ttl-seconds") != "21600" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if tokenStatus != http.StatusOK {
				w.WriteHeader(tokenStatus)
				return
			}
			_, _ = w.Write([]byte("tok"))
			return
		}
		if r.Header.Get("X-aws-ec2-metadata-token") != "tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		v, ok := vars[strings.TrimPrefix(r.URL.Path, "/latest/meta-data/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(v))
	}))
	t.Cleanup(s.Close)
	awsTestUseURL(t, s.URL+"/latest")
	return s
}

func awsTestUseURL(t *testing.T, u string) {
	t.Helper()
	orig := awsInstanceMetadataURL
	awsInstanceMetadataURL = u
	t.Cleanup(func() { awsInstanceMetadataURL = orig })
	metadataTestSelfNetNs(t)
}

func awsTestVars() map[string]string {
	return map[string]string{
		"instance-id":                    "i-0abc",
		"instance-life-cycle":            "spot",
		"instance-type":                  "m5.large",
		"placement/region":               "us-east-1",
		"placement/availability-zone":    "us-east-1a",
		"placement/availability-zone-id": "use1-az4",
		"local-ipv4":                     "10.0.0.5",
		"public-ipv4":                    "3.1.2.3",
		"identity-credentials/ec2/info":  `{"Code":"Success","LastUpdated":"2025-01-01T00:00:00Z","AccountId":"123456789012"}`,
	}
}

func TestGetAwsToken(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		s := awsTestStart(t, http.StatusOK, nil)
		token, err := getAwsToken()
		require.NoError(t, err)
		assert.Equal(t, "tok", token)
		reqs := s.recorded()
		require.Len(t, reqs, 1)
		assert.Equal(t, http.MethodPut, reqs[0].Method)
		assert.Equal(t, "/latest/api/token", reqs[0].URL)
	})
	t.Run("non-200", func(t *testing.T) {
		awsTestStart(t, http.StatusForbidden, nil)
		token, err := getAwsToken()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "403")
		assert.Empty(t, token)
	})
	t.Run("dial error", func(t *testing.T) {
		s := awsTestStart(t, http.StatusOK, nil)
		s.Close()
		token, err := getAwsToken()
		require.Error(t, err)
		assert.Empty(t, token)
	})
	t.Run("host netns lookup failure", func(t *testing.T) {
		s := awsTestStart(t, http.StatusOK, nil)
		metadataTestHostNetNsErr(t, errors.New("permission denied"))
		_, err := getAwsToken()
		assert.EqualError(t, err, "permission denied")
		assert.Empty(t, s.recorded(), "the metadata service must not be queried")
	})
}

func TestGetAwsMetadata(t *testing.T) {
	t.Run("happy path", func(t *testing.T) {
		s := awsTestStart(t, http.StatusOK, awsTestVars())
		assert.Equal(t, &CloudMetadata{
			Provider:           CloudProviderAWS,
			AccountId:          "123456789012",
			InstanceId:         "i-0abc",
			InstanceType:       "m5.large",
			LifeCycle:          "spot",
			Region:             "us-east-1",
			AvailabilityZone:   "us-east-1a",
			AvailabilityZoneId: "use1-az4",
			LocalIPv4:          "10.0.0.5",
			PublicIPv4:         "3.1.2.3",
		}, getAwsMetadata())
		reqs := s.recorded()
		require.Len(t, reqs, 1+9)
		assert.Equal(t, http.MethodPut, reqs[0].Method)
		for _, r := range reqs[1:] {
			assert.Equal(t, http.MethodGet, r.Method)
			assert.Equal(t, "tok", r.Header.Get("X-aws-ec2-metadata-token"), r.URL)
			assert.True(t, strings.HasPrefix(r.URL, "/latest/meta-data/"), r.URL)
		}
	})
	t.Run("token non-200: provider only", func(t *testing.T) {
		s := awsTestStart(t, http.StatusUnauthorized, awsTestVars())
		assert.Equal(t, &CloudMetadata{Provider: CloudProviderAWS}, getAwsMetadata())
		assert.Len(t, s.recorded(), 1, "no metadata GETs without a token")
	})
	t.Run("token dial error: provider only", func(t *testing.T) {
		s := awsTestStart(t, http.StatusOK, awsTestVars())
		s.Close()
		assert.Equal(t, &CloudMetadata{Provider: CloudProviderAWS}, getAwsMetadata())
	})
	t.Run("host netns lookup failure: provider only", func(t *testing.T) {
		s := awsTestStart(t, http.StatusOK, awsTestVars())
		metadataTestHostNetNsErr(t, errors.New("permission denied"))
		assert.Equal(t, &CloudMetadata{Provider: CloudProviderAWS}, getAwsMetadata())
		assert.Empty(t, s.recorded())
	})
	t.Run("malformed identity info", func(t *testing.T) {
		vars := awsTestVars()
		vars["identity-credentials/ec2/info"] = `{"AccountId":`
		awsTestStart(t, http.StatusOK, vars)
		md := getAwsMetadata()
		assert.Empty(t, md.AccountId)
		assert.Equal(t, "i-0abc", md.InstanceId)
		assert.Equal(t, "use1-az4", md.AvailabilityZoneId)
	})
	t.Run("no identity info, no public ip", func(t *testing.T) {
		vars := awsTestVars()
		delete(vars, "identity-credentials/ec2/info")
		delete(vars, "public-ipv4")
		awsTestStart(t, http.StatusOK, vars)
		md := getAwsMetadata()
		assert.Empty(t, md.AccountId)
		assert.Empty(t, md.PublicIPv4)
		assert.Equal(t, "10.0.0.5", md.LocalIPv4)
	})
}
