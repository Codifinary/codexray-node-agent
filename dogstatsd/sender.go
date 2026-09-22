// Copyright Codexray
// SPDX-License-Identifier: AGPL-3.0

package dogstatsd

import (
	"fmt"
	"io"
	"net/http"
	"os"
)

type SendDisposition int

const (
	SendSuccess SendDisposition = iota
	SendRetry
	SendPermanent
)

type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type CustomSender struct {
	client   HTTPDoer
	endpoint string
	headers  map[string]string
}

func NewCustomSender(client HTTPDoer, endpoint string, headers map[string]string) (*CustomSender, error) {
	if client == nil {
		return nil, fmt.Errorf("custom metric HTTP client must not be nil")
	}
	if endpoint == "" {
		return nil, fmt.Errorf("custom metric endpoint must not be empty")
	}
	copyHeaders := make(map[string]string, len(headers))
	for key, value := range headers {
		copyHeaders[key] = value
	}
	return &CustomSender{client: client, endpoint: endpoint, headers: copyHeaders}, nil
}

func (s *CustomSender) SendFile(file string) (SendDisposition, int, error) {
	f, err := os.Open(file)
	if err != nil {
		return SendRetry, 0, err
	}
	defer f.Close()
	req, err := http.NewRequest(http.MethodPost, s.endpoint, f)
	if err != nil {
		return SendPermanent, 0, err
	}
	for key, value := range s.headers {
		req.Header.Set(key, value)
	}
	req.Header.Set("User-Agent", "codexray-node-agent-dogstatsd")
	req.Header.Set("Content-Type", "application/x-protobuf")
	req.Header.Set("Content-Encoding", "snappy")
	req.Header.Set("X-Prometheus-Remote-Write-Version", "0.1.0")
	resp, err := s.client.Do(req)
	if err != nil {
		return SendRetry, 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	disposition := classifyHTTPStatus(resp.StatusCode)
	if disposition == SendSuccess {
		return disposition, resp.StatusCode, nil
	}
	return disposition, resp.StatusCode, fmt.Errorf("collector returned %s", resp.Status)
}

func classifyHTTPStatus(code int) SendDisposition {
	if code >= 200 && code < 300 {
		return SendSuccess
	}
	if code == http.StatusUnauthorized || code == http.StatusForbidden || code == http.StatusRequestTimeout || code == http.StatusTooManyRequests || code >= 500 {
		return SendRetry
	}
	return SendPermanent
}
