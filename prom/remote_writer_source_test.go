package prom

import (
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/codifinary/codexray-node-agent/flags"
	"github.com/golang/snappy"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/model"
	"github.com/prometheus/prometheus/prompb"
)

type testSeriesSource struct {
	series    []prompb.TimeSeries
	committed int
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func (s *testSeriesSource) Snapshot(_ int) []prompb.TimeSeries {
	return append([]prompb.TimeSeries(nil), s.series...)
}

func (s *testSeriesSource) Commit(count int) {
	s.committed += count
	s.series = s.series[count:]
}

func TestScrapeSpoolsAndCommitsExternalSeries(t *testing.T) {
	spoolDir := t.TempDir()
	source := &testSeriesSource{series: []prompb.TimeSeries{{
		Labels: []prompb.Label{
			{Name: model.MetricNameLabel, Value: "orders_created_counter_event"},
			{Name: "instance", Value: "spoofed"},
			{Name: "is_custom", Value: "true"},
		},
		Samples: []prompb.Sample{{Value: 1, Timestamp: 123}},
	}}}
	agent := &Agent{
		reg: prometheus.NewRegistry(),
		labels: map[string]string{
			model.InstanceLabel: "node-1",
			model.JobLabel:      "codexray-node-agent",
		},
		sourceLabels: map[string]string{
			model.InstanceLabel: "node-1",
			model.JobLabel:      "codexray-node-agent",
		},
		sources:      []SeriesSource{source},
		spoolDir:     spoolDir,
		maxSpoolSize: 1024 * 1024,
	}

	if err := agent.scrape(); err != nil {
		t.Fatal(err)
	}
	if source.committed != 1 || len(source.series) != 0 {
		t.Fatalf("source committed=%d remaining=%d", source.committed, len(source.series))
	}

	files, err := filepath.Glob(filepath.Join(spoolDir, "spool-*.done"))
	if err != nil || len(files) != 1 {
		t.Fatalf("spool files=%v err=%v", files, err)
	}
	compressed, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	payload, err := snappy.Decode(nil, compressed)
	if err != nil {
		t.Fatal(err)
	}
	var request prompb.WriteRequest
	if err := request.Unmarshal(payload); err != nil {
		t.Fatal(err)
	}
	if len(request.Timeseries) != 1 {
		t.Fatalf("timeseries=%d, want 1", len(request.Timeseries))
	}
	if got := remoteWriteLabelValue(request.Timeseries[0].Labels, "instance"); got != "node-1" {
		t.Fatalf("instance=%q, want node-1", got)
	}
}

func TestScrapeDoesNotCommitExternalSeriesWhenSpoolFails(t *testing.T) {
	source := &testSeriesSource{series: []prompb.TimeSeries{{
		Labels:  []prompb.Label{{Name: model.MetricNameLabel, Value: "metric"}},
		Samples: []prompb.Sample{{Value: 1}},
	}}}
	agent := &Agent{
		reg:          prometheus.NewRegistry(),
		labels:       map[string]string{},
		sources:      []SeriesSource{source},
		spoolDir:     filepath.Join(t.TempDir(), "missing", "spool"),
		maxSpoolSize: 1024,
	}
	if err := agent.scrape(); err == nil {
		t.Fatal("expected spool failure")
	}
	if source.committed != 0 || len(source.series) != 1 {
		t.Fatalf("source committed=%d remaining=%d", source.committed, len(source.series))
	}
}

func TestSendUsesNodeAgentAuthenticationAndRemoteWriteHeaders(t *testing.T) {
	type requestHeaders struct {
		apiKey          string
		contentType     string
		contentEncoding string
		remoteWrite     string
	}
	var received requestHeaders
	client := http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		received = requestHeaders{
			apiKey:          r.Header.Get("X-API-Key"),
			contentType:     r.Header.Get("Content-Type"),
			contentEncoding: r.Header.Get("Content-Encoding"),
			remoteWrite:     r.Header.Get("X-Prometheus-Remote-Write-Version"),
		}
		return &http.Response{
			StatusCode: http.StatusNoContent,
			Status:     "204 No Content",
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("")),
			Request:    r,
		}, nil
	})}

	endpoint, err := url.Parse("http://collector.test/ingest/v1/metrics")
	if err != nil {
		t.Fatal(err)
	}
	payloadPath := filepath.Join(t.TempDir(), "spool.done")
	if err := os.WriteFile(payloadPath, []byte("payload"), 0600); err != nil {
		t.Fatal(err)
	}

	oldAPIKey := *flags.ApiKey
	*flags.ApiKey = "project-api-key"
	defer func() { *flags.ApiKey = oldAPIKey }()

	agent := &Agent{url: endpoint, httpClient: client}
	if err := agent.send(payloadPath); err != nil {
		t.Fatal(err)
	}
	got := received
	if got.apiKey != "project-api-key" {
		t.Fatalf("X-API-Key=%q, want project-api-key", got.apiKey)
	}
	if got.contentType != "application/x-protobuf" || got.contentEncoding != "snappy" || got.remoteWrite != "0.1.0" {
		t.Fatalf("unexpected remote-write headers: %#v", got)
	}
}

func remoteWriteLabelValue(labels []prompb.Label, name string) string {
	for _, label := range labels {
		if label.Name == name {
			return label.Value
		}
	}
	return ""
}
