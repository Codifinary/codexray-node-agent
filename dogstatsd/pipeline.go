// Copyright Codexray
// SPDX-License-Identifier: AGPL-3.0

package dogstatsd

import (
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"os"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/golang/snappy"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/prometheus/prompb"
)

var ErrCustomBatchTooLarge = errors.New("custom metric encoded batch exceeds limit")

type PipelineConfig struct {
	FlushInterval       time.Duration
	MaxBatchBytes       int
	ExternalLabels      map[string]string
	RetryMin            time.Duration
	RetryMax            time.Duration
	SaturationThreshold float64
	SaturationDuration  time.Duration
	Registerer          prometheus.Registerer
}

type pipelineMetrics struct {
	aggregated       prometheus.Counter
	spooled          prometheus.Counter
	deliveredBatches prometheus.Counter
	deliveredSamples prometheus.Counter
	retried          prometheus.Counter
	rejected         *prometheus.CounterVec
	flushErrors      *prometheus.CounterVec
	retentionRemoved *prometheus.CounterVec
	spoolFiles       prometheus.Gauge
	spoolBytes       prometheus.Gauge
	spoolOldestAge   prometheus.Gauge
	spoolUtilization prometheus.Gauge
	spoolSaturated   prometheus.Gauge
	healthy          prometheus.Gauge
	componentHealthy *prometheus.GaugeVec
}

func (c PipelineConfig) Validate() error {
	if c.FlushInterval <= 0 {
		return fmt.Errorf("custom metric flush interval must be positive")
	}
	if c.MaxBatchBytes <= 0 {
		return fmt.Errorf("custom metric max batch bytes must be positive")
	}
	if c.RetryMin <= 0 || c.RetryMax < c.RetryMin {
		return fmt.Errorf("custom metric retry bounds are invalid")
	}
	if c.SaturationThreshold <= 0 || c.SaturationThreshold > 1 || c.SaturationDuration <= 0 {
		return fmt.Errorf("custom metric saturation settings are invalid")
	}
	return nil
}

type Pipeline struct {
	cfg        PipelineConfig
	aggregator *Aggregator
	spool      *CustomSpool
	sender     *CustomSender
	metrics    pipelineMetrics

	stop              chan struct{}
	done              chan struct{}
	once              sync.Once
	healthy           atomic.Bool
	healthMu          sync.Mutex
	health            map[string]bool
	spoolPressure     *pressureState
	spoolBytesCurrent atomic.Int64
}

func NewPipeline(cfg PipelineConfig, aggregator *Aggregator, spool *CustomSpool, sender *CustomSender) (*Pipeline, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if aggregator == nil || spool == nil || sender == nil {
		return nil, fmt.Errorf("custom metric pipeline dependencies must not be nil")
	}
	registerer := cfg.Registerer
	if registerer == nil {
		registerer = prometheus.NewRegistry()
	}
	m := pipelineMetrics{
		aggregated:       prometheus.NewCounter(prometheus.CounterOpts{Namespace: "node_agent", Subsystem: "dogstatsd", Name: "aggregated_series_total", Help: "Custom output series produced by aggregation windows."}),
		spooled:          prometheus.NewCounter(prometheus.CounterOpts{Namespace: "node_agent", Subsystem: "dogstatsd", Name: "spooled_series_total", Help: "Custom output series persisted to the independent spool."}),
		deliveredBatches: prometheus.NewCounter(prometheus.CounterOpts{Namespace: "node_agent", Subsystem: "dogstatsd", Name: "delivered_batches_total", Help: "Custom batches accepted by the collector."}),
		deliveredSamples: prometheus.NewCounter(prometheus.CounterOpts{Namespace: "node_agent", Subsystem: "dogstatsd", Name: "delivered_samples_total", Help: "Custom metric samples durably removed after collector acceptance."}),
		retried:          prometheus.NewCounter(prometheus.CounterOpts{Namespace: "node_agent", Subsystem: "dogstatsd", Name: "retry_total", Help: "Retryable custom delivery failures."}),
		rejected:         prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: "node_agent", Subsystem: "dogstatsd", Name: "rejected_batches_total", Help: "Permanently rejected custom batches."}, []string{"code"}),
		flushErrors:      prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: "node_agent", Subsystem: "dogstatsd", Name: "flush_errors_total", Help: "Custom flush failures by bounded reason."}, []string{"reason"}),
		retentionRemoved: prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: "node_agent", Subsystem: "dogstatsd", Name: "retention_removed_total", Help: "Custom spool entries removed by bounded retention policy."}, []string{"reason"}),
		spoolFiles:       prometheus.NewGauge(prometheus.GaugeOpts{Namespace: "node_agent", Subsystem: "dogstatsd", Name: "spool_files", Help: "Custom batches waiting in the independent spool."}),
		spoolBytes:       prometheus.NewGauge(prometheus.GaugeOpts{Namespace: "node_agent", Subsystem: "dogstatsd", Name: "spool_bytes", Help: "Bytes waiting in the independent custom spool."}),
		spoolOldestAge:   prometheus.NewGauge(prometheus.GaugeOpts{Namespace: "node_agent", Subsystem: "dogstatsd", Name: "spool_oldest_age_seconds", Help: "Age of the oldest custom spool batch."}),
		spoolUtilization: prometheus.NewGauge(prometheus.GaugeOpts{Namespace: "node_agent", Subsystem: "dogstatsd", Name: "spool_utilization_ratio", Help: "Independent custom spool bytes divided by its configured byte limit."}),
		spoolSaturated:   prometheus.NewGauge(prometheus.GaugeOpts{Namespace: "node_agent", Subsystem: "dogstatsd", Name: "spool_saturated", Help: "Whether custom spool utilization has remained above the saturation threshold for the configured duration."}),
		healthy:          prometheus.NewGauge(prometheus.GaugeOpts{Namespace: "node_agent", Subsystem: "dogstatsd", Name: "pipeline_healthy", Help: "Whether the custom aggregation/spool/sender pipeline is healthy."}),
		componentHealthy: prometheus.NewGaugeVec(prometheus.GaugeOpts{Namespace: "node_agent", Subsystem: "dogstatsd", Name: "component_healthy", Help: "Whether each custom metric pipeline component is healthy."}, []string{"component"}),
	}
	registerer.MustRegister(m.aggregated, m.spooled, m.deliveredBatches, m.deliveredSamples, m.retried, m.rejected, m.flushErrors, m.retentionRemoved, m.spoolFiles, m.spoolBytes, m.spoolOldestAge, m.spoolUtilization, m.spoolSaturated, m.healthy, m.componentHealthy)
	m.healthy.Set(1)
	cfg.ExternalLabels = cloneLabels(cfg.ExternalLabels)
	health := map[string]bool{"processing": true, "spool": true, "delivery": true, "authentication": true, "saturation": true}
	for component := range health {
		m.componentHealthy.WithLabelValues(component).Set(1)
	}
	pipeline := &Pipeline{cfg: cfg, aggregator: aggregator, spool: spool, sender: sender, metrics: m, stop: make(chan struct{}), done: make(chan struct{}), health: health, spoolPressure: newPressureState(cfg.SaturationThreshold, cfg.SaturationDuration)}
	pipeline.healthy.Store(true)
	return pipeline, nil
}

func (p *Pipeline) Start() {
	go p.run()
}

func (p *Pipeline) Healthy() bool {
	p.updateSpoolPressure(p.spoolBytesCurrent.Load(), time.Now())
	return p.healthy.Load()
}

func (p *Pipeline) setComponentHealthy(component string, healthy bool) {
	p.healthMu.Lock()
	p.health[component] = healthy
	overall := true
	for _, componentHealthy := range p.health {
		overall = overall && componentHealthy
	}
	p.healthMu.Unlock()
	p.metrics.componentHealthy.WithLabelValues(component).Set(boolFloat(healthy))
	p.healthy.Store(overall)
	if overall {
		p.metrics.healthy.Set(1)
	} else {
		p.metrics.healthy.Set(0)
	}
}

func boolFloat(value bool) float64 {
	if value {
		return 1
	}
	return 0
}

// Close stops intake scheduling, performs one final aggregation flush, and
// leaves any unsent files in the custom spool for the next process.
func (p *Pipeline) Close() error {
	var flushErr error
	p.once.Do(func() {
		close(p.stop)
		<-p.done
		flushErr = p.FlushOnce(time.Now())
	})
	return flushErr
}

func (p *Pipeline) run() {
	defer close(p.done)
	flushTicker := time.NewTicker(p.cfg.FlushInterval)
	defer flushTicker.Stop()
	retryDelay := p.cfg.RetryMin
	retryTimer := time.NewTimer(0)
	defer retryTimer.Stop()
	for {
		select {
		case <-p.stop:
			return
		case at := <-flushTicker.C:
			_ = p.FlushOnce(at)
		case <-retryTimer.C:
			disposition, _, err := p.ReplayOnce()
			if err != nil && disposition == SendRetry {
				retryDelay *= 2
				if retryDelay > p.cfg.RetryMax {
					retryDelay = p.cfg.RetryMax
				}
			} else {
				retryDelay = p.cfg.RetryMin
			}
			retryTimer.Reset(jitter(retryDelay))
		}
	}
}

func (p *Pipeline) FlushOnce(at time.Time) error {
	series := p.aggregator.Snapshot(at)
	if len(series) == 0 {
		p.refreshSpoolMetrics()
		return nil
	}
	for i := range series {
		series[i] = addExternalLabels(series[i], p.cfg.ExternalLabels)
	}
	payloads, err := encodeCustomBatches(series, p.cfg.MaxBatchBytes)
	if err != nil {
		p.metrics.flushErrors.WithLabelValues("batch_too_large").Inc()
		p.setComponentHealthy("processing", false)
		return err
	}
	_, err = p.spool.PutBatch(payloads)
	if err != nil {
		reason := "spool_write"
		if errors.Is(err, ErrCustomSpoolFull) {
			reason = "spool_full"
		}
		p.metrics.flushErrors.WithLabelValues(reason).Inc()
		p.setComponentHealthy("spool", false)
		p.refreshSpoolMetrics()
		return err
	}
	p.aggregator.Commit()
	p.metrics.aggregated.Add(float64(len(series)))
	p.metrics.spooled.Add(float64(len(series)))
	p.setComponentHealthy("processing", true)
	p.setComponentHealthy("spool", true)
	p.refreshSpoolMetrics()
	return nil
}

func encodeCustomBatches(series []prompb.TimeSeries, maxBytes int) ([][]byte, error) {
	if len(series) == 0 {
		return nil, nil
	}
	var result [][]byte
	for start := 0; start < len(series); {
		low, high := 1, len(series)-start
		best := 0
		var bestPayload []byte
		for low <= high {
			mid := low + (high-low)/2
			payload, err := marshalCustomBatch(series[start : start+mid])
			if err != nil {
				return nil, err
			}
			if len(payload) <= maxBytes {
				best = mid
				bestPayload = payload
				low = mid + 1
			} else {
				high = mid - 1
			}
		}
		if best == 0 {
			payload, err := marshalCustomBatch(series[start : start+1])
			if err != nil {
				return nil, err
			}
			return nil, fmt.Errorf("%w: single series is %d bytes, limit %d", ErrCustomBatchTooLarge, len(payload), maxBytes)
		}
		result = append(result, bestPayload)
		start += best
	}
	return result, nil
}

func marshalCustomBatch(series []prompb.TimeSeries) ([]byte, error) {
	payload, err := (&prompb.WriteRequest{Timeseries: series}).Marshal()
	if err != nil {
		return nil, err
	}
	return snappy.Encode(nil, payload), nil
}

// ReplayOnce processes only the oldest custom payload. Retryable failures leave
// it in place; permanent failures move it to quarantine; success removes it.
func (p *Pipeline) ReplayOnce() (SendDisposition, int, error) {
	file, err := p.spool.Oldest()
	if err != nil {
		p.setComponentHealthy("spool", false)
		return SendRetry, 0, err
	}
	if file == "" {
		return SendSuccess, 0, nil
	}
	sampleCount, countErr := customPayloadSampleCount(file)
	if countErr != nil {
		if _, quarantineErr := p.spool.Quarantine(file, "corrupt-payload"); quarantineErr != nil {
			p.setComponentHealthy("spool", false)
			p.setComponentHealthy("delivery", false)
			return SendRetry, 0, errors.Join(countErr, quarantineErr)
		}
		p.metrics.flushErrors.WithLabelValues("spool_corrupt").Inc()
		p.metrics.rejected.WithLabelValues("corrupt").Inc()
		p.setComponentHealthy("spool", true)
		p.setComponentHealthy("delivery", false)
		p.refreshSpoolMetrics()
		return SendPermanent, 0, countErr
	}
	disposition, status, sendErr := p.sender.SendFile(file)
	switch disposition {
	case SendSuccess:
		if err := p.spool.Remove(file); err != nil {
			p.setComponentHealthy("spool", false)
			p.setComponentHealthy("delivery", false)
			return SendRetry, status, err
		}
		p.metrics.deliveredBatches.Inc()
		p.metrics.deliveredSamples.Add(float64(sampleCount))
		p.setComponentHealthy("spool", true)
		p.setComponentHealthy("delivery", true)
		p.setComponentHealthy("authentication", true)
	case SendPermanent:
		reason := "client-error"
		if status != 0 {
			reason = "http-" + strconv.Itoa(status)
		}
		if _, err := p.spool.Quarantine(file, reason); err != nil {
			p.setComponentHealthy("spool", false)
			p.setComponentHealthy("delivery", false)
			return SendRetry, status, errors.Join(sendErr, err)
		}
		p.metrics.rejected.WithLabelValues(strconv.Itoa(status)).Inc()
		p.setComponentHealthy("spool", true)
		p.setComponentHealthy("delivery", false)
		if status == 401 || status == 403 {
			p.setComponentHealthy("authentication", false)
		}
	case SendRetry:
		p.metrics.retried.Inc()
		p.setComponentHealthy("delivery", false)
		if status == http.StatusUnauthorized || status == http.StatusForbidden {
			p.setComponentHealthy("authentication", false)
		}
	}
	p.refreshSpoolMetrics()
	return disposition, status, sendErr
}

func customPayloadSampleCount(file string) (int, error) {
	request, err := readCustomPayload(file)
	if err != nil {
		return 0, fmt.Errorf("decode custom spool payload: %w", err)
	}
	count := 0
	for _, series := range request.Timeseries {
		count += len(series.Samples)
	}
	if count == 0 {
		return 0, fmt.Errorf("decode custom spool payload: no samples")
	}
	return count, nil
}

func (p *Pipeline) refreshSpoolMetrics() {
	maintenance, err := p.spool.Maintain()
	if err != nil {
		p.setComponentHealthy("spool", false)
		return
	}
	if maintenance.ExpiredReadyFiles > 0 {
		p.metrics.retentionRemoved.WithLabelValues("spool_expired").Add(float64(maintenance.ExpiredReadyFiles))
	}
	if maintenance.RemovedQuarantine > 0 {
		p.metrics.retentionRemoved.WithLabelValues("quarantine_quota").Add(float64(maintenance.RemovedQuarantine))
	}
	stats, err := p.spool.Stats()
	if err != nil {
		p.setComponentHealthy("spool", false)
		return
	}
	p.metrics.spoolFiles.Set(float64(stats.Files))
	p.metrics.spoolBytes.Set(float64(stats.Bytes))
	p.metrics.spoolOldestAge.Set(stats.OldestAge.Seconds())
	p.updateSpoolPressure(stats.Bytes, time.Now())
}

func (p *Pipeline) updateSpoolPressure(bytes int64, now time.Time) {
	p.spoolBytesCurrent.Store(bytes)
	utilization := float64(bytes) / float64(p.spool.maxBytes)
	saturated := p.spoolPressure.Observe(utilization, now)
	p.metrics.spoolUtilization.Set(utilization)
	p.metrics.spoolSaturated.Set(boolFloat(saturated))
	p.setComponentHealthy("saturation", !saturated)
}

func jitter(duration time.Duration) time.Duration {
	return time.Duration(float64(duration) * (0.8 + rand.Float64()*0.4))
}

func addExternalLabels(series prompb.TimeSeries, external map[string]string) prompb.TimeSeries {
	labels := make(map[string]string, len(series.Labels)+len(external))
	for _, label := range series.Labels {
		labels[label.Name] = label.Value
	}
	for key, value := range external {
		labels[key] = value
	}
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := prompb.TimeSeries{Samples: append([]prompb.Sample(nil), series.Samples...)}
	for _, key := range keys {
		result.Labels = append(result.Labels, prompb.Label{Name: key, Value: labels[key]})
	}
	return result
}

// readCustomPayload is intentionally private production code used by tests and
// diagnostics to verify that custom files contain valid remote-write payloads.
func readCustomPayload(file string) (*prompb.WriteRequest, error) {
	compressed, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	payload, err := snappy.Decode(nil, compressed)
	if err != nil {
		return nil, err
	}
	request := &prompb.WriteRequest{}
	if err := request.Unmarshal(payload); err != nil {
		return nil, err
	}
	return request, nil
}
