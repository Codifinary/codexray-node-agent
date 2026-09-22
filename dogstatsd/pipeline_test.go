package dogstatsd

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/golang/snappy"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/prometheus/prompb"
)

func newTestPipeline(t *testing.T, status int) (*Pipeline, *CustomSpool, *Aggregator, *int) {
	t.Helper()
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(status)
	}))
	t.Cleanup(server.Close)
	agg := testAggregator(t)
	spool, err := OpenCustomSpool(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	sender, err := NewCustomSender(server.Client(), server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	pipeline, err := NewPipeline(PipelineConfig{
		FlushInterval:       time.Second,
		MaxBatchBytes:       1 << 20,
		ExternalLabels:      map[string]string{"instance": "real", "job": "codexray-node-agent"},
		RetryMin:            time.Millisecond,
		RetryMax:            time.Second,
		SaturationThreshold: 0.8,
		SaturationDuration:  time.Minute,
	}, agg, spool, sender)
	if err != nil {
		t.Fatal(err)
	}
	return pipeline, spool, agg, &requests
}

func TestPipelineFlushEncodesIdentityAndSuccessRemovesFile(t *testing.T) {
	pipeline, spool, agg, requests := newTestPipeline(t, http.StatusNoContent)
	if !pipeline.Healthy() {
		t.Fatal("new pipeline is not healthy")
	}
	agg.Add(Metric{Name: "orders", Type: MetricTypeCounter, Value: 2, SampleRate: 1}, map[string]string{"instance": "spoofed"})
	if err := pipeline.FlushOnce(time.UnixMilli(123)); err != nil {
		t.Fatal(err)
	}
	file, err := spool.Oldest()
	if err != nil || file == "" {
		t.Fatalf("oldest=(%q,%v)", file, err)
	}
	request, err := readCustomPayload(file)
	if err != nil {
		t.Fatal(err)
	}
	if len(request.Timeseries) != 1 || labelValue(request.Timeseries[0].Labels, "instance") != "real" {
		t.Fatalf("unexpected request: %#v", request)
	}
	if disposition, _, err := pipeline.ReplayOnce(); disposition != SendSuccess || err != nil {
		t.Fatalf("replay=(%v,%v)", disposition, err)
	}
	if *requests != 1 {
		t.Fatalf("requests=%d", *requests)
	}
	if file, _ := spool.Oldest(); file != "" {
		t.Fatalf("delivered file remains: %q", file)
	}
	if got := counterValue(t, pipeline.metrics.spooled); got != 1 {
		t.Fatalf("spooled series=%v", got)
	}
	if got := counterValue(t, pipeline.metrics.deliveredBatches); got != 1 {
		t.Fatalf("delivered batches=%v", got)
	}
	if got := counterValue(t, pipeline.metrics.deliveredSamples); got != 1 {
		t.Fatalf("delivered samples=%v", got)
	}
	if !pipeline.Healthy() {
		t.Fatal("successful delivery did not restore health")
	}
}

func TestPipelineCountsDeliveredSamplesFromDurablePayload(t *testing.T) {
	pipeline, spool, _, requests := newTestPipeline(t, http.StatusNoContent)
	payload, err := marshalCustomBatch([]prompb.TimeSeries{
		{Labels: []prompb.Label{{Name: "__name__", Value: "first"}}, Samples: []prompb.Sample{{Value: 1}, {Value: 2}}},
		{Labels: []prompb.Label{{Name: "__name__", Value: "second"}}, Samples: []prompb.Sample{{Value: 3}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := spool.Put(payload); err != nil {
		t.Fatal(err)
	}
	if disposition, _, err := pipeline.ReplayOnce(); disposition != SendSuccess || err != nil {
		t.Fatalf("replay=(%v,%v)", disposition, err)
	}
	if *requests != 1 {
		t.Fatalf("requests=%d, want 1", *requests)
	}
	if got := counterValue(t, pipeline.metrics.deliveredSamples); got != 3 {
		t.Fatalf("delivered samples=%v, want 3", got)
	}
}

func TestPipelineQuarantinesCorruptPayloadWithoutSending(t *testing.T) {
	pipeline, spool, _, requests := newTestPipeline(t, http.StatusNoContent)
	if _, err := spool.Put([]byte("not-snappy-protobuf")); err != nil {
		t.Fatal(err)
	}
	disposition, _, err := pipeline.ReplayOnce()
	if disposition != SendPermanent || err == nil {
		t.Fatalf("replay=(%v,%v), want permanent corruption error", disposition, err)
	}
	if *requests != 0 {
		t.Fatalf("corrupt payload sent %d requests", *requests)
	}
	if file, _ := spool.Oldest(); file != "" {
		t.Fatalf("corrupt payload remains replayable: %q", file)
	}
	entries, err := os.ReadDir(spool.quarantine)
	if err != nil || len(entries) != 1 {
		t.Fatalf("quarantine entries=%d err=%v", len(entries), err)
	}
	if got := counterValue(t, pipeline.metrics.deliveredSamples); got != 0 {
		t.Fatalf("delivered samples=%v, want 0", got)
	}
}

func counterValue(t *testing.T, counter interface{ Write(*dto.Metric) error }) float64 {
	t.Helper()
	metric := &dto.Metric{}
	if err := counter.Write(metric); err != nil {
		t.Fatal(err)
	}
	return metric.GetCounter().GetValue()
}

func TestRetryJitterStaysBounded(t *testing.T) {
	base := 10 * time.Second
	for i := 0; i < 100; i++ {
		got := jitter(base)
		if got < 8*time.Second || got > 12*time.Second {
			t.Fatalf("jitter=%s outside 80-120%%", got)
		}
	}
}

func TestPipelineRetryRetainsAndPermanentQuarantines(t *testing.T) {
	for _, tc := range []struct {
		status   int
		want     SendDisposition
		retained bool
	}{
		{http.StatusServiceUnavailable, SendRetry, true},
		{http.StatusUnauthorized, SendRetry, true},
		{http.StatusBadRequest, SendPermanent, false},
	} {
		pipeline, spool, agg, _ := newTestPipeline(t, tc.status)
		agg.Add(Metric{Name: "orders", Type: MetricTypeCounter, Value: 1, SampleRate: 1}, nil)
		if err := pipeline.FlushOnce(time.Now()); err != nil {
			t.Fatal(err)
		}
		disposition, _, err := pipeline.ReplayOnce()
		if disposition != tc.want || err == nil {
			t.Fatalf("status %d replay=(%v,%v)", tc.status, disposition, err)
		}
		file, _ := spool.Oldest()
		if (file != "") != tc.retained {
			t.Fatalf("status %d retained=%v, want %v", tc.status, file != "", tc.retained)
		}
		if pipeline.Healthy() {
			t.Fatalf("status %d did not degrade pipeline health", tc.status)
		}
		if tc.want == SendPermanent {
			entries, err := os.ReadDir(spool.quarantine)
			if err != nil || len(entries) != 1 {
				t.Fatalf("quarantine entries=%d err=%v", len(entries), err)
			}
		}
	}
}

func TestLocalFlushDoesNotClearAuthenticationFailure(t *testing.T) {
	pipeline, _, agg, _ := newTestPipeline(t, http.StatusUnauthorized)
	agg.Add(Metric{Name: "orders", Type: MetricTypeCounter, Value: 1, SampleRate: 1}, nil)
	if err := pipeline.FlushOnce(time.Now()); err != nil {
		t.Fatal(err)
	}
	if disposition, _, err := pipeline.ReplayOnce(); disposition != SendRetry || err == nil {
		t.Fatalf("replay=(%v,%v), want retained authentication failure", disposition, err)
	}
	if pipeline.Healthy() {
		t.Fatal("authentication failure did not degrade pipeline health")
	}

	agg.Add(Metric{Name: "orders", Type: MetricTypeCounter, Value: 1, SampleRate: 1}, nil)
	if err := pipeline.FlushOnce(time.Now()); err != nil {
		t.Fatal(err)
	}
	if pipeline.Healthy() {
		t.Fatal("successful local spool write cleared authentication failure")
	}
}

func TestAPIKeyRotationReplaysRetainedPayloadAfterRestart(t *testing.T) {
	spoolDir := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "new-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	spool, err := OpenCustomSpool(spoolDir, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	oldSender, err := NewCustomSender(server.Client(), server.URL, map[string]string{"X-Api-Key": "old-key"})
	if err != nil {
		t.Fatal(err)
	}
	oldPipeline, err := NewPipeline(testPipelineConfig(), testAggregator(t), spool, oldSender)
	if err != nil {
		t.Fatal(err)
	}
	oldPipeline.aggregator.Add(Metric{Name: "orders", Type: MetricTypeCounter, Value: 1, SampleRate: 1}, nil)
	if err := oldPipeline.FlushOnce(time.Now()); err != nil {
		t.Fatal(err)
	}
	if disposition, _, err := oldPipeline.ReplayOnce(); disposition != SendRetry || err == nil {
		t.Fatalf("old-key replay=(%v,%v), want retained authentication failure", disposition, err)
	}
	retained, err := spool.Oldest()
	if err != nil || retained == "" {
		t.Fatalf("authentication-rejected payload not retained: file=%q err=%v", retained, err)
	}

	reopenedSpool, err := OpenCustomSpool(spoolDir, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	newSender, err := NewCustomSender(server.Client(), server.URL, map[string]string{"X-Api-Key": "new-key"})
	if err != nil {
		t.Fatal(err)
	}
	newPipeline, err := NewPipeline(testPipelineConfig(), testAggregator(t), reopenedSpool, newSender)
	if err != nil {
		t.Fatal(err)
	}
	if disposition, _, err := newPipeline.ReplayOnce(); disposition != SendSuccess || err != nil {
		t.Fatalf("rotated-key replay=(%v,%v), want success", disposition, err)
	}
	if file, err := reopenedSpool.Oldest(); err != nil || file != "" {
		t.Fatalf("replayed payload remains: file=%q err=%v", file, err)
	}
	if !newPipeline.Healthy() {
		t.Fatal("successful rotated-key replay did not restore pipeline health")
	}
}

func testPipelineConfig() PipelineConfig {
	return PipelineConfig{
		FlushInterval:       time.Second,
		MaxBatchBytes:       1 << 20,
		RetryMin:            time.Millisecond,
		RetryMax:            time.Second,
		SaturationThreshold: 0.8,
		SaturationDuration:  time.Minute,
	}
}

func TestPipelineReadinessRequiresSustainedSpoolPressure(t *testing.T) {
	pipeline, _, _, _ := newTestPipeline(t, http.StatusNoContent)
	now := time.Unix(100, 0)
	high := int64(float64(pipeline.spool.maxBytes) * 0.9)
	pipeline.updateSpoolPressure(high, now)
	if !pipeline.healthy.Load() {
		t.Fatal("brief spool pressure degraded readiness")
	}
	pipeline.updateSpoolPressure(high, now.Add(time.Minute))
	if pipeline.healthy.Load() {
		t.Fatal("sustained spool pressure did not degrade readiness")
	}
	pipeline.updateSpoolPressure(0, now.Add(time.Minute+time.Second))
	if !pipeline.Healthy() {
		t.Fatal("spool pressure recovery did not restore readiness")
	}
}

func TestPipelineRejectsOversizedEncodedBatch(t *testing.T) {
	pipeline, spool, agg, _ := newTestPipeline(t, http.StatusNoContent)
	pipeline.cfg.MaxBatchBytes = 1
	agg.Add(Metric{Name: "orders", Type: MetricTypeCounter, Value: 1, SampleRate: 1}, nil)
	if err := pipeline.FlushOnce(time.Now()); err == nil {
		t.Fatal("expected encoded batch limit error")
	}
	if file, _ := spool.Oldest(); file != "" {
		t.Fatalf("oversized batch was spooled: %q", file)
	}
	pipeline.cfg.MaxBatchBytes = 1 << 20
	if err := pipeline.FlushOnce(time.UnixMilli(999)); err != nil {
		t.Fatal(err)
	}
	file, err := spool.Oldest()
	if err != nil || file == "" {
		t.Fatalf("reserved window was not retried: file=%q err=%v", file, err)
	}
	request, err := readCustomPayload(file)
	if err != nil || len(request.Timeseries) != 1 {
		t.Fatalf("retried request=%#v err=%v", request, err)
	}
}

func TestEncodeCustomBatchesSplitsWithoutDroppingSeries(t *testing.T) {
	at := time.UnixMilli(1)
	series := []prompb.TimeSeries{
		aggregateSeries("requests_counter_count", map[string]string{"service": "api", "padding": strings.Repeat("a", 200)}, 1, at),
		aggregateSeries("requests_counter_count", map[string]string{"service": "worker", "padding": strings.Repeat("b", 200)}, 1, at),
	}
	one, err := marshalCustomBatch(series[:1])
	if err != nil {
		t.Fatal(err)
	}
	two, err := marshalCustomBatch(series[1:])
	if err != nil {
		t.Fatal(err)
	}
	limit := len(one)
	if len(two) > limit {
		limit = len(two)
	}
	payloads, err := encodeCustomBatches(series, limit)
	if err != nil {
		t.Fatal(err)
	}
	if len(payloads) != 2 {
		t.Fatalf("payloads=%d, want 2", len(payloads))
	}
	var total int
	for _, payload := range payloads {
		decoded, err := snappy.Decode(nil, payload)
		if err != nil {
			t.Fatal(err)
		}
		request := &prompb.WriteRequest{}
		if err := request.Unmarshal(decoded); err != nil {
			t.Fatal(err)
		}
		total += len(request.Timeseries)
	}
	if total != len(series) {
		t.Fatalf("encoded series=%d, want %d", total, len(series))
	}
}
