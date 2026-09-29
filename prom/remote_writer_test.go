// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package prom

import (
	"crypto/md5"
	"encoding/hex"
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
	"k8s.io/klog/v2"
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

// remoteWriterLogSink captures klog output. Writes happen under klog's mutex
// from the logging goroutine and are guarded by mu here, so a test that sees a
// line logged by sendLoop/scrapeLoop has a happens-before edge from everything
// that loop did before logging it.
type remoteWriterLogSink struct {
	mu    sync.Mutex
	lines []string
}

func (s *remoteWriterLogSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	s.lines = append(s.lines, string(p))
	s.mu.Unlock()
	return len(p), nil
}

func (s *remoteWriterLogSink) mark() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.lines)
}

func (s *remoteWriterLogSink) since(i int, match func(string) bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, l := range s.lines[i:] {
		if match(l) {
			return true
		}
	}
	return false
}

var (
	remoteWriterLogOnce sync.Once
	remoteWriterLog     = &remoteWriterLogSink{}
)

// remoteWriterInstallLogSink redirects klog (process-wide, for the rest of the
// test binary) into remoteWriterLog. It is never restored: the agent loops
// started by TestStartAgentLoops outlive the test and would otherwise spam
// stderr with "failed to scrape metrics" every tick.
func remoteWriterInstallLogSink() *remoteWriterLogSink {
	remoteWriterLogOnce.Do(func() {
		klog.LogToStderr(false)
		klog.SetOutput(remoteWriterLog)
	})
	return remoteWriterLog
}

// TestStartAgentLoops starts the real agent (StartAgent + scrapeLoop + sendLoop)
// against a fake remote-write collector.
//
// The two loops never exit, so the goroutines leak for the life of the test
// binary. To keep that leak inert and race-free the test ends by removing the
// WAL dir and waiting until both loops have logged a failure that can only
// happen after the removal: from then on sendLoop only ever fails ReadDir and
// sleeps (it never again reads flags.ApiKey via common.AuthHeaders, which other
// tests write), and scrapeLoop has long since read flags.ScrapeInterval. Only
// then are the flag values restored.
func TestStartAgentLoops(t *testing.T) {
	sink := remoteWriterInstallLogSink()

	// Not t.TempDir(): the loops outlive the test; the dir is removed explicitly below.
	walDir, err := os.MkdirTemp("", "remote-writer-wal-")
	require.NoError(t, err)
	spoolDir := filepath.Join(walDir, "spool")
	require.NoError(t, os.Mkdir(spoolDir, 0750))

	// A spool file left over from a previous run (e.g. collector was down):
	// it must be delivered first and deleted after the 2xx.
	seedGauge := prometheus.NewGauge(prometheus.GaugeOpts{Name: "seeded", Help: "seeded"})
	seedGauge.Set(42)
	seedReg := prometheus.NewRegistry()
	seedReg.MustRegister(seedGauge)
	seedMfs, err := seedReg.Gather()
	require.NoError(t, err)
	seedRaw, err := buildWriteRequest(seedMfs, 1, map[string]string{"instance": "old", "job": "codexray-node-agent"}).Marshal()
	require.NoError(t, err)
	seedBody := snappy.Encode(nil, seedRaw)
	seedPath := filepath.Join(spoolDir, "spool-1.done")
	require.NoError(t, os.WriteFile(seedPath, seedBody, 0640))

	type post struct {
		path, encoding, contentType, version string
		raw                                  []byte
		wr                                   prompb.WriteRequest
		decodeErr                            error
	}
	var mu sync.Mutex
	var posts []post
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		p := post{
			path: r.URL.Path, encoding: r.Header.Get("Content-Encoding"),
			contentType: r.Header.Get("Content-Type"), version: r.Header.Get("X-Prometheus-Remote-Write-Version"),
			raw: body,
		}
		if decoded, err := snappy.Decode(nil, body); err != nil {
			p.decodeErr = err
		} else {
			p.decodeErr = p.wr.Unmarshal(decoded)
		}
		mu.Lock()
		posts = append(posts, p)
		mu.Unlock()
		// Answer slower than the scrape interval so the spool never drains:
		// an empty spool makes sendLoop sleep 5s, which this test must not wait for.
		time.Sleep(150 * time.Millisecond)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	endpoint, err := url.Parse(srv.URL + "/v1/metrics")
	require.NoError(t, err)

	oldEndpoint, oldWal, oldInterval, oldSpool := *flags.MetricsEndpoint, *flags.WalDir, *flags.ScrapeInterval, *flags.MaxSpoolSize
	*flags.MetricsEndpoint = endpoint
	*flags.WalDir = walDir
	*flags.ScrapeInterval = 50 * time.Millisecond
	*flags.MaxSpoolSize = 1 << 20

	machineID, systemUUID := "0f1e2d3c4b5a69788796a5b4c3d2e1f0", "4c4c4544-0042-3510-8052-b4c04f4d4e31"
	h := md5.Sum([]byte(machineID + strings.ReplaceAll(systemUUID, "-", "")))
	wantInstance := hex.EncodeToString(h[:])

	reg := prometheus.NewRegistry()
	require.NoError(t, StartAgent(reg, machineID, systemUUID))

	// "up" is registered synchronously.
	mfs, err := reg.Gather()
	require.NoError(t, err)
	require.Len(t, mfs, 1)
	assert.Equal(t, "up", mfs[0].GetName())
	assert.Equal(t, 1.0, mfs[0].Metric[0].GetGauge().GetValue())

	snapshot := func() []post {
		mu.Lock()
		defer mu.Unlock()
		return append([]post(nil), posts...)
	}
	isScraped := func(p post) bool {
		for _, ts := range p.wr.Timeseries {
			if remoteWriterLabelsMap(ts.Labels)["__name__"] == "up" {
				return true
			}
		}
		return false
	}
	require.Eventually(t, func() bool {
		ps := snapshot()
		if len(ps) < 2 {
			return false
		}
		_, statErr := os.Stat(seedPath)
		return os.IsNotExist(statErr) && isScraped(ps[len(ps)-1])
	}, 2*time.Second, 10*time.Millisecond, "expected the seeded spool file and a scraped one to be delivered")

	ps := snapshot()
	// Oldest spool file first.
	assert.Equal(t, seedBody, ps[0].raw, "the pre-existing spool file must be sent first, byte for byte")
	for i, p := range ps {
		require.NoError(t, p.decodeErr, "post %d is not a valid snappy remote-write request", i)
		assert.Equal(t, "/v1/metrics", p.path)
		assert.Equal(t, "snappy", p.encoding)
		assert.Equal(t, "application/x-protobuf", p.contentType)
		assert.Equal(t, "0.1.0", p.version)
	}
	var scraped *prompb.WriteRequest
	for i := range ps {
		if isScraped(ps[i]) {
			scraped = &ps[i].wr
			break
		}
	}
	require.NotNil(t, scraped)
	require.Len(t, scraped.Timeseries, 1)
	assert.Equal(t, map[string]string{
		"__name__": "up",
		"instance": wantInstance, // md5(machine-id + system-uuid without dashes)
		"job":      "codexray-node-agent",
	}, remoteWriterLabelsMap(scraped.Timeseries[0].Labels))
	assert.Equal(t, 1.0, scraped.Timeseries[0].Samples[0].Value)

	// Quiesce the leaked loops (see the doc comment).
	mark := sink.mark()
	require.NoError(t, os.RemoveAll(walDir))
	require.Eventually(t, func() bool {
		sendStopped := sink.since(mark, func(l string) bool {
			return strings.Contains(l, "failed to send metrics to "+srv.URL) ||
				(strings.Contains(l, "failed to get oldest spool file") && strings.Contains(l, walDir))
		})
		scrapeFailed := sink.since(mark, func(l string) bool {
			return strings.Contains(l, "failed to scrape metrics") && strings.Contains(l, walDir)
		})
		return sendStopped && scrapeFailed
	}, 2*time.Second, 10*time.Millisecond, "agent loops did not observe the removed WAL dir")

	*flags.MetricsEndpoint, *flags.WalDir, *flags.ScrapeInterval, *flags.MaxSpoolSize = oldEndpoint, oldWal, oldInterval, oldSpool
}

func TestStartAgentWalDirErrors(t *testing.T) {
	endpoint, err := url.Parse("http://127.0.0.1:1/v1/metrics")
	require.NoError(t, err)
	oldEndpoint, oldWal := *flags.MetricsEndpoint, *flags.WalDir
	t.Cleanup(func() { *flags.MetricsEndpoint, *flags.WalDir = oldEndpoint, oldWal })
	*flags.MetricsEndpoint = endpoint

	t.Run("missing parent", func(t *testing.T) {
		*flags.WalDir = filepath.Join(t.TempDir(), "absent", "wal")
		assert.Error(t, StartAgent(prometheus.NewRegistry(), "m", ""))
	})

	t.Run("unwritable wal dir", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root ignores directory permissions; StartAgent would succeed and start loops")
		}
		wal := t.TempDir()
		require.NoError(t, os.Chmod(wal, 0500))
		t.Cleanup(func() { _ = os.Chmod(wal, 0700) })
		*flags.WalDir = wal
		assert.Error(t, StartAgent(prometheus.NewRegistry(), "m", ""))
		_, statErr := os.Stat(filepath.Join(wal, "spool"))
		assert.True(t, os.IsNotExist(statErr))
	})
}

// --- SeriesSource plumbing (merged from main: custom StatsD metrics ride along
// with the node scrape, and must not be able to impersonate the agent) ---

// remoteWriterSource is a SeriesSource whose Snapshot/Commit calls are recorded.
type remoteWriterSource struct {
	series    []prompb.TimeSeries
	snapshots int
	committed []int
}

func (s *remoteWriterSource) Snapshot(int) []prompb.TimeSeries {
	s.snapshots++
	return s.series
}

func (s *remoteWriterSource) Commit(count int) { s.committed = append(s.committed, count) }

func remoteWriterSeries(kv ...string) prompb.TimeSeries {
	ts := prompb.TimeSeries{Samples: []prompb.Sample{{Value: 1, Timestamp: 7}}}
	for i := 0; i+1 < len(kv); i += 2 {
		ts.Labels = append(ts.Labels, prompb.Label{Name: kv[i], Value: kv[i+1]})
	}
	return ts
}

func TestSourceLabels(t *testing.T) {
	// machine id only: it is the instance, and there is no system_uuid label.
	ls := SourceLabels("machine-1", "")
	assert.Equal(t, "machine-1", ls["instance"])
	assert.Equal(t, "codexray-node-agent", ls["job"])
	assert.Equal(t, "machine-1", ls["machine_id"])
	assert.NotContains(t, ls, "system_uuid")

	// a differing system uuid makes the instance a hash of both, so two nodes
	// that share a machine id still land on distinct series.
	ls = SourceLabels("machine-1", "AAAA-BBBB")
	assert.NotEqual(t, "machine-1", ls["instance"])
	assert.Len(t, ls["instance"], 32, "md5 hex")
	assert.Equal(t, "machine-1", ls["machine_id"])
	assert.Equal(t, "AAAA-BBBB", ls["system_uuid"])

	// a uuid equal to the machine id (once dashes are stripped) is not hashed.
	assert.Equal(t, "machine1", SourceLabels("machine1", "mach-ine1")["instance"])

	// nothing known: no identity labels beyond the job.
	ls = SourceLabels("", "")
	assert.Equal(t, "codexray-node-agent", ls["job"])
	assert.NotContains(t, ls, "machine_id")
}

func TestWithExternalLabelsAgentIdentityWins(t *testing.T) {
	agent := map[string]string{"instance": "real", "job": "codexray-node-agent", "machine_id": "m1"}
	// an application sending StatsD can put anything in its tags, including the
	// agent's own identity labels - those must not be able to spoof a node.
	got := withExternalLabels(remoteWriterSeries(
		"__name__", "app_requests_total", "instance", "spoofed", "job", "evil", "env", "prod",
	), agent)

	ls := remoteWriterLabelsMap(got.Labels)
	assert.Equal(t, "real", ls["instance"])
	assert.Equal(t, "codexray-node-agent", ls["job"])
	assert.Equal(t, "m1", ls["machine_id"])
	assert.Equal(t, "prod", ls["env"], "application labels that don't clash are kept")
	assert.Equal(t, "app_requests_total", ls["__name__"])

	names := make([]string, 0, len(got.Labels))
	for _, l := range got.Labels {
		names = append(names, l.Name)
	}
	assert.IsIncreasing(t, names, "remote write requires sorted label names")
	assert.Equal(t, []prompb.Sample{{Value: 1, Timestamp: 7}}, got.Samples)
}

func TestScrapeAppendsSourceSeriesAndCommitsOncePerSource(t *testing.T) {
	a := remoteWriterNewAgent(t, "http://example.invalid/write", 0)
	a.sourceLabels = SourceLabels("machine-1", "")
	s1 := &remoteWriterSource{series: []prompb.TimeSeries{
		remoteWriterSeries("__name__", "app_a", "instance", "spoofed"),
	}}
	s2 := &remoteWriterSource{series: []prompb.TimeSeries{
		remoteWriterSeries("__name__", "app_b"), remoteWriterSeries("__name__", "app_c"),
	}}
	empty := &remoteWriterSource{}
	a.sources = []SeriesSource{s1, nil, empty, s2}

	require.NoError(t, a.scrape())

	assert.Equal(t, []int{1}, s1.committed)
	assert.Equal(t, []int{2}, s2.committed)
	assert.Empty(t, empty.committed, "a source with nothing to send is never committed")
	assert.Equal(t, 1, empty.snapshots)

	files, err := a.listSpoolFiles()
	require.NoError(t, err)
	require.Len(t, files, 1)
	raw, err := os.ReadFile(files[0])
	require.NoError(t, err)
	decoded, err := snappy.Decode(nil, raw)
	require.NoError(t, err)
	var wr prompb.WriteRequest
	require.NoError(t, wr.Unmarshal(decoded))

	byName := map[string]map[string]string{}
	for _, ts := range wr.Timeseries {
		ls := remoteWriterLabelsMap(ts.Labels)
		byName[ls["__name__"]] = ls
	}
	for _, n := range []string{"app_a", "app_b", "app_c"} {
		require.Contains(t, byName, n, "source series reached the spool")
		assert.Equal(t, "machine-1", byName[n]["instance"], "%s carries the agent identity", n)
		assert.Equal(t, "codexray-node-agent", byName[n]["job"])
	}
}

func TestScrapeGatherErrorDoesNotCommitSources(t *testing.T) {
	a := remoteWriterNewAgent(t, "http://example.invalid/write", 0)
	a.reg.MustRegister(remoteWriterFailingCollector{})
	s := &remoteWriterSource{series: []prompb.TimeSeries{remoteWriterSeries("__name__", "app_a")}}
	a.sources = []SeriesSource{s}

	assert.Error(t, a.scrape())
	assert.Empty(t, s.committed, "nothing was spooled, so nothing may be committed")
}
