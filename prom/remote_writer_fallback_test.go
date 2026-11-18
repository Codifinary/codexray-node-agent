package prom

import (
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/golang/snappy"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/model"
	"github.com/prometheus/prometheus/prompb"
)

// collector records the bodies it receives and answers with the configured status.
type collector struct {
	status int
	bodies []string
}

func (c *collector) client() http.Client {
	return http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		c.bodies = append(c.bodies, string(body))
		return &http.Response{
			StatusCode: c.status,
			Status:     http.StatusText(c.status),
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("")),
			Request:    r,
		}, nil
	})}
}

func newFallbackTestAgent(t *testing.T, spoolDir string, c *collector) *Agent {
	t.Helper()
	reg := prometheus.NewRegistry()
	node := prometheus.NewGauge(prometheus.GaugeOpts{Name: "node_metric"})
	node.Set(1)
	reg.MustRegister(node)
	endpoint, err := url.Parse("http://collector.test/ingest/v1/metrics")
	if err != nil {
		t.Fatal(err)
	}
	a := &Agent{
		reg:          reg,
		url:          endpoint,
		labels:       map[string]string{model.InstanceLabel: "node-1"},
		sourceLabels: map[string]string{model.InstanceLabel: "node-1"},
		spoolDir:     spoolDir,
		maxSpoolSize: 1024 * 1024,
		metrics:      newAgentMetrics(prometheus.NewRegistry()),
	}
	if c != nil {
		a.httpClient = c.client()
	}
	return a
}

func metricNames(t *testing.T, compressed []byte) []string {
	t.Helper()
	raw, err := snappy.Decode(nil, compressed)
	if err != nil {
		t.Fatal(err)
	}
	var wr prompb.WriteRequest
	if err := wr.Unmarshal(raw); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, ts := range wr.Timeseries {
		names = append(names, remoteWriteLabelValue(ts.Labels, model.MetricNameLabel))
	}
	return names
}

func metricValue(t *testing.T, m prometheus.Metric) float64 {
	t.Helper()
	var out dto.Metric
	if err := m.Write(&out); err != nil {
		t.Fatal(err)
	}
	if out.Counter != nil {
		return out.Counter.GetValue()
	}
	return out.Gauge.GetValue()
}

func TestScrapeKeepsOnlyNodeMetricsInMemoryWhenSpoolFails(t *testing.T) {
	a := newFallbackTestAgent(t, blockedSpoolDir(t), nil)
	source := &testSeriesSource{series: []prompb.TimeSeries{{
		Labels:  []prompb.Label{{Name: model.MetricNameLabel, Value: "custom_metric"}},
		Samples: []prompb.Sample{{Value: 1, Timestamp: 123}},
	}}}
	a.sources = []SeriesSource{source}

	if err := a.scrape(); err == nil {
		t.Fatal("expected spool failure")
	}
	if source.committed != 0 {
		t.Fatalf("source committed=%d, want 0", source.committed)
	}
	if got := metricValue(t, a.metrics.spoolHealthy); got != 0 {
		t.Fatalf("spool_healthy=%v, want 0", got)
	}
	p, ok := a.takeOldestFromMemory()
	if !ok {
		t.Fatal("no payload kept in memory")
	}
	names := metricNames(t, p.data)
	if strings.Join(names, ",") != "node_metric" {
		t.Fatalf("buffered metrics=%v, want only node_metric", names)
	}
}

func TestSendOnceSendsPayloadFromMemoryWhenSpoolIsEmpty(t *testing.T) {
	c := &collector{status: http.StatusInternalServerError}
	a := newFallbackTestAgent(t, t.TempDir(), c)
	a.bufferInMemory(1, []byte("from-memory"))

	if sent, err := a.sendOnce(); !sent || err == nil {
		t.Fatalf("sent=%v err=%v, want a failed send", sent, err)
	}
	if len(a.memBuf) != 1 {
		t.Fatalf("buffered payloads=%d after a failed send, want 1", len(a.memBuf))
	}

	c.status = http.StatusNoContent
	if sent, err := a.sendOnce(); !sent || err != nil {
		t.Fatalf("sent=%v err=%v, want a successful send", sent, err)
	}
	if len(a.memBuf) != 0 || a.memBufBytes != 0 {
		t.Fatalf("buffer not emptied: payloads=%d bytes=%d", len(a.memBuf), a.memBufBytes)
	}
	if got := strings.Join(c.bodies, ","); got != "from-memory,from-memory" {
		t.Fatalf("collector received %q", got)
	}

	if sent, err := a.sendOnce(); sent || err != nil {
		t.Fatalf("sent=%v err=%v with nothing pending", sent, err)
	}
}

func TestSendOnceSendsSpoolFilesBeforeMemory(t *testing.T) {
	c := &collector{status: http.StatusNoContent}
	spoolDir := t.TempDir()
	a := newFallbackTestAgent(t, spoolDir, c)
	spooled := filepath.Join(spoolDir, "spool-1.done")
	if err := os.WriteFile(spooled, []byte("from-disk"), 0600); err != nil {
		t.Fatal(err)
	}
	a.bufferInMemory(2, []byte("from-memory"))

	for i := 0; i < 2; i++ {
		if _, err := a.sendOnce(); err != nil {
			t.Fatal(err)
		}
	}
	if got := strings.Join(c.bodies, ","); got != "from-disk,from-memory" {
		t.Fatalf("send order=%q, want from-disk,from-memory", got)
	}
	if _, err := os.Stat(spooled); !os.IsNotExist(err) {
		t.Fatalf("sent spool file still exists: %v", err)
	}
}

func TestScrapeMovesBufferedPayloadsToSpoolWhenItRecovers(t *testing.T) {
	a := newFallbackTestAgent(t, blockedSpoolDir(t), nil)
	for i := 0; i < 2; i++ {
		if err := a.scrape(); err == nil {
			t.Fatal("expected spool failure")
		}
		time.Sleep(2 * time.Millisecond) // distinct millisecond timestamps, as real scrapes have
	}
	if a.Healthy() {
		t.Fatal("agent reports healthy while the spool cannot be written")
	}

	a.spoolDir = filepath.Join(t.TempDir(), "spool")
	if err := a.scrape(); err != nil {
		t.Fatal(err)
	}
	if !a.Healthy() {
		t.Fatal("agent still unhealthy after the spool recovered")
	}
	if len(a.memBuf) != 0 {
		t.Fatalf("buffered payloads=%d after recovery, want 0", len(a.memBuf))
	}
	files, err := a.listSpoolFiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 3 {
		t.Fatalf("spool files=%v, want 3", files)
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if names := metricNames(t, data); len(names) == 0 {
			t.Fatalf("spool file %s holds no metrics", f)
		}
	}
}

func TestMemoryBufferDropsOldestPayloadsOverLimit(t *testing.T) {
	a := newFallbackTestAgent(t, t.TempDir(), nil)
	a.maxMemBytes = 10

	a.bufferInMemory(1, []byte("aaaaaa"))
	a.bufferInMemory(2, []byte("bbbbbb"))
	a.bufferInMemory(3, []byte("this payload alone is over the limit"))

	if len(a.memBuf) != 1 || a.memBuf[0].timestamp != 3 {
		t.Fatalf("buffer=%+v, want only the newest payload", a.memBuf)
	}
	if got := metricValue(t, a.metrics.droppedPayloads); got != 2 {
		t.Fatalf("dropped_payloads_total=%v, want 2", got)
	}
	if got := metricValue(t, a.metrics.memoryBufferBytes); got != float64(len(a.memBuf[0].data)) {
		t.Fatalf("memory_buffer_bytes=%v, want %d", got, len(a.memBuf[0].data))
	}
}
