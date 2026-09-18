// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package prom

import (
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/codifinary/codexray-node-agent/flags"
	"github.com/golang/snappy"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/prometheus/prompb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// remoteWriterExtraLabels mirrors what StartAgent attaches to every series.
func remoteWriterExtraLabels() map[string]string {
	return map[string]string{"instance": "node-1", "job": "codexray-node-agent"}
}

func remoteWriterLabelPairs(kv ...string) []*dto.LabelPair {
	var res []*dto.LabelPair
	for i := 0; i+1 < len(kv); i += 2 {
		res = append(res, &dto.LabelPair{Name: proto.String(kv[i]), Value: proto.String(kv[i+1])})
	}
	return res
}

func remoteWriterLabelsMap(ls []prompb.Label) map[string]string {
	res := make(map[string]string, len(ls))
	for _, l := range ls {
		res[l.Name] = l.Value
	}
	return res
}

// remoteWriterSeriesKey renders a series as name{k=v,...} with sorted labels (excluding __name__).
func remoteWriterSeriesKey(ls []prompb.Label) string {
	m := remoteWriterLabelsMap(ls)
	var parts []string
	for k, v := range m {
		if k == "__name__" {
			continue
		}
		parts = append(parts, k+"="+v)
	}
	sort.Strings(parts)
	return m["__name__"] + "{" + strings.Join(parts, ",") + "}"
}

func remoteWriterFixtureFamilies() []*dto.MetricFamily {
	gauge := dto.MetricType_GAUGE
	counter := dto.MetricType_COUNTER
	histogram := dto.MetricType_HISTOGRAM
	summary := dto.MetricType_SUMMARY
	untyped := dto.MetricType_UNTYPED
	return []*dto.MetricFamily{
		{
			Name: proto.String("container_memory_rss"),
			Help: proto.String("RSS"),
			Type: &gauge,
			Metric: []*dto.Metric{
				{Label: remoteWriterLabelPairs("container_id", "/docker/a"), Gauge: &dto.Gauge{Value: proto.Float64(1024)}},
				{Label: remoteWriterLabelPairs("container_id", "/docker/b"), Gauge: &dto.Gauge{Value: proto.Float64(2048)}},
			},
		},
		{
			Name:   proto.String("container_net_tcp_connects_total"),
			Help:   proto.String("connects"),
			Type:   &counter,
			Metric: []*dto.Metric{{Label: remoteWriterLabelPairs("destination", "10.0.0.1:5432"), Counter: &dto.Counter{Value: proto.Float64(7)}}},
		},
		{
			Name: proto.String("container_http_requests_duration_seconds_total"),
			Help: proto.String("latency"),
			Type: &histogram,
			Metric: []*dto.Metric{{
				Label: remoteWriterLabelPairs("destination", "api:80"),
				Histogram: &dto.Histogram{
					SampleCount: proto.Uint64(10),
					SampleSum:   proto.Float64(1.5),
					Bucket: []*dto.Bucket{
						{UpperBound: proto.Float64(0.1), CumulativeCount: proto.Uint64(4)},
						{UpperBound: proto.Float64(0.5), CumulativeCount: proto.Uint64(9)},
					},
				},
			}},
		},
		{
			Name:   proto.String("some_summary"),
			Type:   &summary,
			Metric: []*dto.Metric{{Summary: &dto.Summary{SampleCount: proto.Uint64(1), SampleSum: proto.Float64(1)}}},
		},
		{
			Name:   proto.String("some_untyped"),
			Type:   &untyped,
			Metric: []*dto.Metric{{Untyped: &dto.Untyped{Value: proto.Float64(3)}}},
		},
		{
			Name:   proto.String("empty_family"),
			Type:   &gauge,
			Metric: nil,
		},
	}
}

func TestBuildWriteRequest(t *testing.T) {
	const ts = int64(1700000000123)
	wr := buildWriteRequest(remoteWriterFixtureFamilies(), ts, remoteWriterExtraLabels())

	got := map[string]float64{}
	for _, series := range wr.Timeseries {
		require.Len(t, series.Samples, 1)
		assert.Equal(t, ts, series.Samples[0].Timestamp)

		// Remote write 0.1.0 requires labels sorted by name, and no duplicate names.
		names := make([]string, 0, len(series.Labels))
		for _, l := range series.Labels {
			names = append(names, l.Name)
		}
		assert.True(t, sort.StringsAreSorted(names), "labels must be sorted: %v", names)
		assert.Len(t, remoteWriterLabelsMap(series.Labels), len(series.Labels), "duplicate label names: %v", names)

		m := remoteWriterLabelsMap(series.Labels)
		assert.Equal(t, "node-1", m["instance"])
		assert.Equal(t, "codexray-node-agent", m["job"])
		// mainv2 appends these itself; the agent must never emit them.
		assert.NotContains(t, m, "tenant_id")
		assert.NotContains(t, m, "project_id")

		key := remoteWriterSeriesKey(series.Labels)
		_, dup := got[key]
		assert.False(t, dup, "duplicate series %s", key)
		got[key] = series.Samples[0].Value
	}

	const common = "instance=node-1,job=codexray-node-agent"
	expected := map[string]float64{
		"container_memory_rss{container_id=/docker/a," + common + "}":                                      1024,
		"container_memory_rss{container_id=/docker/b," + common + "}":                                      2048,
		"container_net_tcp_connects_total{destination=10.0.0.1:5432," + common + "}":                       7,
		"container_http_requests_duration_seconds_total_bucket{destination=api:80," + common + ",le=0.1}":  4,
		"container_http_requests_duration_seconds_total_bucket{destination=api:80," + common + ",le=0.5}":  9,
		"container_http_requests_duration_seconds_total_bucket{destination=api:80," + common + ",le=+Inf}": 10,
		"container_http_requests_duration_seconds_total_sum{destination=api:80," + common + "}":            1.5,
		"container_http_requests_duration_seconds_total_count{destination=api:80," + common + "}":          10,
	}
	assert.Equal(t, expected, got)

	// Metadata: one entry per non-empty family, typed per the remote-write enum.
	meta := map[string]prompb.MetricMetadata{}
	for _, m := range wr.Metadata {
		meta[m.MetricFamilyName] = m
	}
	assert.NotContains(t, meta, "empty_family")
	require.Contains(t, meta, "container_memory_rss")
	assert.Equal(t, prompb.MetricMetadata_GAUGE, meta["container_memory_rss"].Type)
	assert.Equal(t, "RSS", meta["container_memory_rss"].Help)
	assert.Equal(t, prompb.MetricMetadata_COUNTER, meta["container_net_tcp_connects_total"].Type)
	assert.Equal(t, prompb.MetricMetadata_HISTOGRAM, meta["container_http_requests_duration_seconds_total"].Type)
	assert.Equal(t, prompb.MetricMetadata_SUMMARY, meta["some_summary"].Type)
	assert.Equal(t, prompb.MetricMetadata_UNKNOWN, meta["some_untyped"].Type)
}

func TestBuildWriteRequestEmpty(t *testing.T) {
	wr := buildWriteRequest(nil, 1, remoteWriterExtraLabels())
	assert.Empty(t, wr.Timeseries)
	assert.Empty(t, wr.Metadata)
}

func TestBuildWriteRequestExplicitInfBucket(t *testing.T) {
	// BUG: an explicit +Inf bucket in the dto histogram produces a duplicate le="+Inf" series — unskip when fixed
	t.Skip("BUG: an explicit +Inf bucket in the dto histogram produces a duplicate le=\"+Inf\" series")

	histogram := dto.MetricType_HISTOGRAM
	mfs := []*dto.MetricFamily{{
		Name: proto.String("h"),
		Type: &histogram,
		Metric: []*dto.Metric{{Histogram: &dto.Histogram{
			SampleCount: proto.Uint64(3),
			SampleSum:   proto.Float64(1),
			Bucket: []*dto.Bucket{
				{UpperBound: proto.Float64(1), CumulativeCount: proto.Uint64(2)},
				{UpperBound: proto.Float64(math.Inf(1)), CumulativeCount: proto.Uint64(3)},
			},
		}}},
	}}
	wr := buildWriteRequest(mfs, 1, nil)
	seen := map[string]int{}
	for _, s := range wr.Timeseries {
		seen[remoteWriterSeriesKey(s.Labels)]++
	}
	assert.Equal(t, 1, seen["h_bucket{le=+Inf}"])
}

func TestBuildWriteRequestFromRegistry(t *testing.T) {
	reg := prometheus.NewRegistry()
	g := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "g", Help: "g"}, []string{"a"})
	g.WithLabelValues("x").Set(2)
	c := prometheus.NewCounter(prometheus.CounterOpts{Name: "c_total", Help: "c"})
	c.Add(3)
	h := prometheus.NewHistogram(prometheus.HistogramOpts{Name: "h_seconds", Help: "h", Buckets: []float64{0.1, 1}})
	h.Observe(0.05)
	h.Observe(0.5)
	h.Observe(5)
	s := prometheus.NewSummary(prometheus.SummaryOpts{Name: "s", Help: "s"})
	s.Observe(1)
	reg.MustRegister(g, c, h, s)

	mfs, err := reg.Gather()
	require.NoError(t, err)
	wr := buildWriteRequest(mfs, 42, remoteWriterExtraLabels())

	got := map[string]float64{}
	for _, series := range wr.Timeseries {
		got[remoteWriterSeriesKey(series.Labels)] = series.Samples[0].Value
	}
	const common = "instance=node-1,job=codexray-node-agent"
	assert.Equal(t, map[string]float64{
		"g{a=x," + common + "}":                    2,
		"c_total{" + common + "}":                  3,
		"h_seconds_bucket{" + common + ",le=0.1}":  1,
		"h_seconds_bucket{" + common + ",le=1}":    2,
		"h_seconds_bucket{" + common + ",le=+Inf}": 3,
		"h_seconds_sum{" + common + "}":            5.55,
		"h_seconds_count{" + common + "}":          3,
	}, got)
}

func TestMakeLabelsMap(t *testing.T) {
	m := &dto.Metric{Label: remoteWriterLabelPairs("container_id", "/k8s/ns/pod/c", "destination", "a:1")}
	got := makeLabelsMap(m, "metric_name", remoteWriterExtraLabels())
	assert.Equal(t, map[string]string{
		"__name__":     "metric_name",
		"instance":     "node-1",
		"job":          "codexray-node-agent",
		"container_id": "/k8s/ns/pod/c",
		"destination":  "a:1",
	}, got)

	got = makeLabelsMap(&dto.Metric{}, "only_name", nil)
	assert.Equal(t, map[string]string{"__name__": "only_name"}, got)
}

func TestMakeLabels(t *testing.T) {
	lm := map[string]string{"__name__": "m", "z": "1", "a": "2"}

	assert.Equal(t, []prompb.Label{
		{Name: "__name__", Value: "m"}, {Name: "a", Value: "2"}, {Name: "z", Value: "1"},
	}, makeLabels(lm, "", ""))

	assert.Equal(t, []prompb.Label{
		{Name: "__name__", Value: "m_sum"}, {Name: "a", Value: "2"}, {Name: "z", Value: "1"},
	}, makeLabels(lm, "_sum", ""))

	assert.Equal(t, []prompb.Label{
		{Name: "__name__", Value: "m_bucket"}, {Name: "a", Value: "2"}, {Name: "le", Value: "0.25"}, {Name: "z", Value: "1"},
	}, makeLabels(lm, "_bucket", "0.25"))

	// The input map must not be mutated by the suffix.
	assert.Equal(t, "m", lm["__name__"])
}

func remoteWriterNewAgent(t *testing.T, endpoint string, maxSpool int64) *Agent {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "spool")
	require.NoError(t, os.Mkdir(dir, 0750))
	a := &Agent{
		reg:          prometheus.NewRegistry(),
		labels:       remoteWriterExtraLabels(),
		httpClient:   http.Client{Timeout: 5 * time.Second},
		spoolDir:     dir,
		maxSpoolSize: maxSpool,
	}
	if endpoint != "" {
		u, err := url.Parse(endpoint)
		require.NoError(t, err)
		a.url = u
	}
	return a
}

func remoteWriterDirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestWriteToSpool(t *testing.T) {
	a := remoteWriterNewAgent(t, "", 1<<20)
	payload := []byte("payload-bytes")
	require.NoError(t, a.writeToSpool(1700000000000, payload))

	// Only the final, renamed file remains; the temp file is gone.
	assert.Equal(t, []string{"spool-1700000000000.done"}, remoteWriterDirNames(t, a.spoolDir))
	data, err := os.ReadFile(filepath.Join(a.spoolDir, "spool-1700000000000.done"))
	require.NoError(t, err)
	assert.Equal(t, payload, data)

	// Temp files (CreateTemp appends a random suffix) must never be picked up by the sender.
	require.NoError(t, os.WriteFile(filepath.Join(a.spoolDir, "spool-1.done123456"), []byte("partial"), 0640))
	require.NoError(t, os.WriteFile(filepath.Join(a.spoolDir, "other.done"), []byte("x"), 0640))
	files, err := a.listSpoolFiles()
	require.NoError(t, err)
	assert.Equal(t, []string{filepath.Join(a.spoolDir, "spool-1700000000000.done")}, files)
}

func TestWriteToSpoolMissingDir(t *testing.T) {
	a := &Agent{spoolDir: filepath.Join(t.TempDir(), "absent"), maxSpoolSize: 100}
	assert.Error(t, a.writeToSpool(1, []byte("x")))
}

func TestWriteToSpoolRenameFailureCleansTemp(t *testing.T) {
	a := remoteWriterNewAgent(t, "", 1<<20)
	// A non-empty directory at the destination path makes the final rename fail.
	dst := filepath.Join(a.spoolDir, "spool-5.done")
	require.NoError(t, os.MkdirAll(filepath.Join(dst, "x"), 0750))
	assert.Error(t, a.writeToSpool(5, []byte("x")))
	// The partially written temp file must not be left behind.
	assert.Equal(t, []string{"spool-5.done"}, remoteWriterDirNames(t, a.spoolDir))
}

func TestListSpoolFilesOrdering(t *testing.T) {
	a := remoteWriterNewAgent(t, "", 1<<20)
	oldest, err := a.getOldestSpoolFile()
	require.NoError(t, err)
	assert.Equal(t, "", oldest)

	for _, ts := range []int64{1700000000300, 1700000000100, 1700000000200} {
		require.NoError(t, a.writeToSpool(ts, []byte("x")))
	}
	files, err := a.listSpoolFiles()
	require.NoError(t, err)
	assert.Equal(t, []string{
		filepath.Join(a.spoolDir, "spool-1700000000100.done"),
		filepath.Join(a.spoolDir, "spool-1700000000200.done"),
		filepath.Join(a.spoolDir, "spool-1700000000300.done"),
	}, files)

	oldest, err = a.getOldestSpoolFile()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(a.spoolDir, "spool-1700000000100.done"), oldest)

	b := &Agent{spoolDir: filepath.Join(t.TempDir(), "absent")}
	_, err = b.listSpoolFiles()
	assert.Error(t, err)
	_, err = b.getOldestSpoolFile()
	assert.Error(t, err)
}

func TestTruncateSpoolIfNeeded(t *testing.T) {
	write := func(a *Agent, ts string, size int) {
		require.NoError(t, os.WriteFile(filepath.Join(a.spoolDir, "spool-"+ts+".done"), make([]byte, size), 0640))
	}

	t.Run("over limit removes only the oldest", func(t *testing.T) {
		a := remoteWriterNewAgent(t, "", 25)
		write(a, "1700000000001", 10)
		write(a, "1700000000002", 10)
		write(a, "1700000000003", 10)
		require.NoError(t, a.truncateSpoolIfNeeded())
		assert.Equal(t, []string{"spool-1700000000002.done", "spool-1700000000003.done"}, remoteWriterDirNames(t, a.spoolDir))
	})

	t.Run("under limit keeps everything", func(t *testing.T) {
		a := remoteWriterNewAgent(t, "", 100)
		write(a, "1700000000001", 10)
		write(a, "1700000000002", 10)
		require.NoError(t, a.truncateSpoolIfNeeded())
		assert.Len(t, remoteWriterDirNames(t, a.spoolDir), 2)
	})

	t.Run("a single oversized file is kept", func(t *testing.T) {
		a := remoteWriterNewAgent(t, "", 5)
		write(a, "1700000000001", 50)
		require.NoError(t, a.truncateSpoolIfNeeded())
		assert.Len(t, remoteWriterDirNames(t, a.spoolDir), 1)
	})

	t.Run("writes keep spool bounded", func(t *testing.T) {
		a := remoteWriterNewAgent(t, "", 30)
		for i := 0; i < 10; i++ {
			require.NoError(t, a.writeToSpool(int64(1700000000000+i), make([]byte, 10)))
		}
		names := remoteWriterDirNames(t, a.spoolDir)
		assert.LessOrEqual(t, len(names), 4)
		assert.Contains(t, names, "spool-1700000000009.done", "newest data must survive truncation")
	})

	t.Run("missing dir", func(t *testing.T) {
		a := &Agent{spoolDir: filepath.Join(t.TempDir(), "absent")}
		assert.Error(t, a.truncateSpoolIfNeeded())
	})
}

func TestScrapeWritesDecodableSpoolFile(t *testing.T) {
	a := remoteWriterNewAgent(t, "", 1<<20)
	g := prometheus.NewGauge(prometheus.GaugeOpts{Name: "up", Help: "up"})
	g.Set(1)
	a.reg.MustRegister(g)

	before := time.Now().UnixMilli()
	require.NoError(t, a.scrape())
	after := time.Now().UnixMilli()

	files, err := a.listSpoolFiles()
	require.NoError(t, err)
	require.Len(t, files, 1)
	data, err := os.ReadFile(files[0])
	require.NoError(t, err)

	decoded, err := snappy.Decode(nil, data)
	require.NoError(t, err)
	var wr prompb.WriteRequest
	require.NoError(t, wr.Unmarshal(decoded))
	require.Len(t, wr.Timeseries, 1)
	assert.Equal(t, "up{instance=node-1,job=codexray-node-agent}", remoteWriterSeriesKey(wr.Timeseries[0].Labels))
	ts := wr.Timeseries[0].Samples[0].Timestamp
	assert.GreaterOrEqual(t, ts, before)
	assert.LessOrEqual(t, ts, after)
	assert.Equal(t, 1.0, wr.Timeseries[0].Samples[0].Value)
}

type remoteWriterFailingCollector struct{}

func (remoteWriterFailingCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- prometheus.NewDesc("broken", "broken", nil, nil)
}

func (remoteWriterFailingCollector) Collect(ch chan<- prometheus.Metric) {
	ch <- prometheus.NewInvalidMetric(prometheus.NewDesc("broken", "broken", nil, nil), errors.New("boom"))
}

func TestScrapeGatherError(t *testing.T) {
	a := remoteWriterNewAgent(t, "", 1<<20)
	a.reg.MustRegister(remoteWriterFailingCollector{})
	assert.Error(t, a.scrape())
	assert.Empty(t, remoteWriterDirNames(t, a.spoolDir))
}

type remoteWriterCaptured struct {
	method  string
	path    string
	headers http.Header
	body    []byte
}

func remoteWriterServer(t *testing.T, status int) (*httptest.Server, func() []remoteWriterCaptured) {
	t.Helper()
	var mu sync.Mutex
	var reqs []remoteWriterCaptured
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		reqs = append(reqs, remoteWriterCaptured{method: r.Method, path: r.URL.Path, headers: r.Header.Clone(), body: body})
		mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []remoteWriterCaptured {
		mu.Lock()
		defer mu.Unlock()
		return append([]remoteWriterCaptured(nil), reqs...)
	}
}

func remoteWriterSetAPIKey(t *testing.T, key string) {
	t.Helper()
	old := *flags.ApiKey
	*flags.ApiKey = key
	t.Cleanup(func() { *flags.ApiKey = old })
}

func TestSendSuccess(t *testing.T) {
	remoteWriterSetAPIKey(t, "secret-key")
	srv, captured := remoteWriterServer(t, http.StatusNoContent)
	a := remoteWriterNewAgent(t, srv.URL+"/ingest/v1/metrics", 1<<20)

	g := prometheus.NewGauge(prometheus.GaugeOpts{Name: "up", Help: "up"})
	g.Set(1)
	a.reg.MustRegister(g)
	require.NoError(t, a.scrape())
	fName, err := a.getOldestSpoolFile()
	require.NoError(t, err)
	onDisk, err := os.ReadFile(fName)
	require.NoError(t, err)

	require.NoError(t, a.send(fName))

	reqs := captured()
	require.Len(t, reqs, 1)
	r := reqs[0]
	assert.Equal(t, http.MethodPost, r.method)
	assert.Equal(t, "/ingest/v1/metrics", r.path)
	assert.Equal(t, "secret-key", r.headers.Get("X-Api-Key"))
	assert.Equal(t, "codexray-node-agent", r.headers.Get("User-Agent"))
	assert.Equal(t, "application/x-protobuf", r.headers.Get("Content-Type"))
	assert.Equal(t, "snappy", r.headers.Get("Content-Encoding"))
	assert.Equal(t, "0.1.0", r.headers.Get("X-Prometheus-Remote-Write-Version"))
	assert.Equal(t, onDisk, r.body)

	decoded, err := snappy.Decode(nil, r.body)
	require.NoError(t, err)
	var wr prompb.WriteRequest
	require.NoError(t, wr.Unmarshal(decoded))
	require.Len(t, wr.Timeseries, 1)
	assert.Equal(t, "up", remoteWriterLabelsMap(wr.Timeseries[0].Labels)["__name__"])
}

func TestSendNoAPIKey(t *testing.T) {
	remoteWriterSetAPIKey(t, "")
	srv, captured := remoteWriterServer(t, http.StatusOK)
	a := remoteWriterNewAgent(t, srv.URL, 1<<20)
	require.NoError(t, a.writeToSpool(1, []byte("x")))
	f, _ := a.getOldestSpoolFile()
	require.NoError(t, a.send(f))
	reqs := captured()
	require.Len(t, reqs, 1)
	_, ok := reqs[0].headers["X-Api-Key"]
	assert.False(t, ok, "empty API key must not be sent")
}

func TestSendRetryableFailureKeepsFile(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			srv, captured := remoteWriterServer(t, status)
			a := remoteWriterNewAgent(t, srv.URL, 1<<20)
			require.NoError(t, a.writeToSpool(1700000000001, []byte("x")))
			f, _ := a.getOldestSpoolFile()
			err := a.send(f)
			require.Error(t, err)
			assert.Contains(t, err.Error(), http.StatusText(status))
			assert.Len(t, captured(), 1)
			_, statErr := os.Stat(f)
			assert.NoError(t, statErr, "transient failure must keep the spool file for retry")
		})
	}
}

func TestSendTransportErrors(t *testing.T) {
	a := remoteWriterNewAgent(t, "http://127.0.0.1:1/", 1<<20)
	assert.Error(t, a.send(filepath.Join(a.spoolDir, "spool-missing.done")))

	// Server closed: connection refused.
	srv := httptest.NewServer(http.NotFoundHandler())
	u := srv.URL
	srv.Close()
	a = remoteWriterNewAgent(t, u, 1<<20)
	require.NoError(t, a.writeToSpool(1, []byte("x")))
	f, _ := a.getOldestSpoolFile()
	assert.Error(t, a.send(f))
	_, statErr := os.Stat(f)
	assert.NoError(t, statErr)
}

// A permanent client error (400 bad payload, 401 bad API key, 404 unknown project) can never
// succeed on retry. sendLoop always retries the oldest file, so such a file must be dropped (or
// parked) so that newer spool files are delivered instead of being blocked forever.
func TestSendPermanentClientErrorDoesNotBlockQueue(t *testing.T) {
	// BUG: send() treats every status >= 300 (incl. 400/401/404) as retryable; sendLoop retries the oldest file forever, blocking newer ones — unskip when fixed
	t.Skip("BUG: send() treats every status >= 300 (incl. 400/401/404) as retryable; sendLoop retries the oldest file forever, blocking newer ones")

	for _, status := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusNotFound} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			srv, _ := remoteWriterServer(t, status)
			a := remoteWriterNewAgent(t, srv.URL, 1<<20)
			require.NoError(t, a.writeToSpool(1700000000001, []byte("bad")))
			require.NoError(t, a.writeToSpool(1700000000002, []byte("good")))

			// One iteration of sendLoop.
			first, err := a.getOldestSpoolFile()
			require.NoError(t, err)
			_ = a.send(first)

			next, err := a.getOldestSpoolFile()
			require.NoError(t, err)
			assert.Equal(t, filepath.Join(a.spoolDir, "spool-1700000000002.done"), next,
				"a permanently rejected spool file must not stay at the head of the queue")
		})
	}
}

func TestStartAgentNoEndpoint(t *testing.T) {
	old := *flags.MetricsEndpoint
	*flags.MetricsEndpoint = nil
	t.Cleanup(func() { *flags.MetricsEndpoint = old })

	reg := prometheus.NewRegistry()
	require.NoError(t, StartAgent(reg, "machine", "uuid"))
	mfs, err := reg.Gather()
	require.NoError(t, err)
	assert.Empty(t, mfs, "no metrics endpoint: nothing registered, no goroutines started")
}
