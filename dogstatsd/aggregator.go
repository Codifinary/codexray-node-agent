// Copyright Codexray
// SPDX-License-Identifier: AGPL-3.0

package dogstatsd

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/prometheus/prompb"
)

func ParseBuckets(raw string) ([]float64, error) {
	parts := strings.Split(raw, ",")
	buckets := make([]float64, 0, len(parts))
	for _, part := range parts {
		value, err := strconv.ParseFloat(strings.TrimSpace(part), 64)
		if err != nil {
			return nil, fmt.Errorf("invalid bucket %q: %w", part, err)
		}
		buckets = append(buckets, value)
	}
	return buckets, nil
}

// AggregationConfig defines the production StatsD flush contract. Bucket
// boundaries are upper bounds and are emitted cumulatively, like Prometheus
// histogram buckets. The +Inf bucket is added automatically.
type AggregationConfig struct {
	TimerBucketsMS        []float64
	HistogramBuckets      []float64
	MaxSetValuesPerSeries int
	MaxBytes              int64
	GaugeTTL              time.Duration
}

func (c AggregationConfig) Validate() error {
	if c.MaxSetValuesPerSeries <= 0 {
		return fmt.Errorf("dogstatsd max set values per series must be positive")
	}
	if c.MaxBytes <= 0 {
		return fmt.Errorf("dogstatsd aggregation max bytes must be positive")
	}
	if c.GaugeTTL <= 0 {
		return fmt.Errorf("dogstatsd gauge TTL must be positive")
	}
	if err := validateBuckets("timer", c.TimerBucketsMS); err != nil {
		return err
	}
	return validateBuckets("histogram", c.HistogramBuckets)
}

func validateBuckets(name string, buckets []float64) error {
	if len(buckets) == 0 {
		return fmt.Errorf("dogstatsd %s buckets must not be empty", name)
	}
	previous := math.Inf(-1)
	for _, bucket := range buckets {
		if math.IsNaN(bucket) || math.IsInf(bucket, 0) || bucket <= previous {
			return fmt.Errorf("dogstatsd %s buckets must be finite and strictly increasing", name)
		}
		previous = bucket
	}
	return nil
}

type aggregateState struct {
	name       string
	metricType MetricType
	labels     map[string]string
	value      float64
	count      float64
	sum        float64
	buckets    []float64
	setValues  map[string]struct{}
}

// Aggregator keeps only the current flush window. Flush atomically rotates the
// window, so UDP parsing can continue while the returned series are encoded and
// written to the dedicated custom-metric spool.
type Aggregator struct {
	mu             sync.Mutex
	cfg            AggregationConfig
	series         map[string]*aggregateState
	inflight       []prompb.TimeSeries
	activeBytes    int64
	inflightBytes  int64
	gauges         map[string]gaugeBaseline
	gaugeBytes     int64
	lastGaugePrune time.Time
}

type gaugeBaseline struct {
	value    float64
	lastSeen time.Time
	bytes    int64
}

func NewAggregator(cfg AggregationConfig) (*Aggregator, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	cfg.TimerBucketsMS = append([]float64(nil), cfg.TimerBucketsMS...)
	cfg.HistogramBuckets = append([]float64(nil), cfg.HistogramBuckets...)
	return &Aggregator{cfg: cfg, series: make(map[string]*aggregateState), gauges: make(map[string]gaugeBaseline)}, nil
}

// Add records one validated metric. The caller must apply tag policy and
// cardinality limits first. set_limit is returned when a new set member would
// exceed the configured per-series bound; repeated members remain accepted.
func (a *Aggregator) Add(metric Metric, tags map[string]string) (accepted bool, reason string) {
	now := metric.ReceivedAt
	if now.IsZero() {
		now = time.Now()
	}
	labels := cloneLabels(tags)
	labels["is_custom"] = "true"
	baseName := normalizeMetricName(metric.Name)
	key := aggregationKey(baseName, metric.Type, labels)

	a.mu.Lock()
	defer a.mu.Unlock()
	a.pruneGaugesLocked(now)
	state := a.series[key]
	createdState := false
	var stateBytes int64
	if state == nil {
		state = &aggregateState{name: baseName, metricType: metric.Type, labels: labels}
		switch metric.Type {
		case MetricTypeTimer:
			state.buckets = make([]float64, len(a.cfg.TimerBucketsMS)+1)
		case MetricTypeHistogram:
			state.buckets = make([]float64, len(a.cfg.HistogramBuckets)+1)
		case MetricTypeSet:
			state.setValues = make(map[string]struct{})
		}
		stateBytes = estimateAggregateStateBytes(state)
		if a.totalBytesLocked()+stateBytes > a.cfg.MaxBytes {
			return false, "aggregation_bytes"
		}
		a.series[key] = state
		a.activeBytes += stateBytes
		createdState = true
	}

	switch metric.Type {
	case MetricTypeCounter:
		if metric.SampleRate <= 0 {
			return false, "invalid_sample_rate"
		}
		state.value += metric.Value / metric.SampleRate
	case MetricTypeGauge:
		baseline, exists := a.gauges[key]
		if !exists {
			baseline.bytes = int64(64 + len(key))
			if a.totalBytesLocked()+baseline.bytes > a.cfg.MaxBytes {
				if createdState {
					delete(a.series, key)
					a.activeBytes -= stateBytes
				}
				return false, "aggregation_bytes"
			}
			a.gaugeBytes += baseline.bytes
		}
		if metric.Relative {
			baseline.value += metric.Value
		} else {
			baseline.value = metric.Value
		}
		baseline.lastSeen = now
		a.gauges[key] = baseline
		state.value = baseline.value
	case MetricTypeTimer:
		weight := sampleWeight(metric.SampleRate)
		if weight == 0 {
			return false, "invalid_sample_rate"
		}
		state.count += weight
		state.sum += metric.Value * weight
		addBucketValue(state.buckets, a.cfg.TimerBucketsMS, metric.Value, weight)
	case MetricTypeHistogram:
		weight := sampleWeight(metric.SampleRate)
		if weight == 0 {
			return false, "invalid_sample_rate"
		}
		state.count += weight
		state.sum += metric.Value * weight
		addBucketValue(state.buckets, a.cfg.HistogramBuckets, metric.Value, weight)
	case MetricTypeSet:
		if _, exists := state.setValues[metric.SetValue]; exists {
			return true, ""
		}
		if len(state.setValues) >= a.cfg.MaxSetValuesPerSeries {
			return false, "set_limit"
		}
		valueBytes := int64(len(metric.SetValue) + 16)
		if a.totalBytesLocked()+valueBytes > a.cfg.MaxBytes {
			return false, "aggregation_bytes"
		}
		state.setValues[metric.SetValue] = struct{}{}
		a.activeBytes += valueBytes
	default:
		delete(a.series, key)
		return false, "unsupported_type"
	}
	return true, ""
}

func sampleWeight(rate float64) float64 {
	if rate <= 0 || rate > 1 || math.IsNaN(rate) || math.IsInf(rate, 0) {
		return 0
	}
	return 1 / rate
}

func addBucketValue(counts []float64, bounds []float64, value, weight float64) {
	for i, bound := range bounds {
		if value <= bound {
			counts[i] += weight
		}
	}
	counts[len(counts)-1] += weight
}

// Flush returns one ordered sample per generated output series and resets the
// active aggregation window. Empty windows produce no series.
func (a *Aggregator) Flush(at time.Time) []prompb.TimeSeries {
	series := a.Snapshot(at)
	a.Commit()
	return series
}

// Snapshot rotates the active window once and reserves the encoded semantic
// series until Commit. Repeated calls after an encoding/spool failure return
// the same window while new packets accumulate independently.
func (a *Aggregator) Snapshot(at time.Time) []prompb.TimeSeries {
	a.mu.Lock()
	if len(a.inflight) > 0 {
		result := cloneSeries(a.inflight)
		a.mu.Unlock()
		return result
	}
	window := a.series
	a.series = make(map[string]*aggregateState)
	windowBytes := a.activeBytes
	a.activeBytes = 0
	a.mu.Unlock()

	keys := make([]string, 0, len(window))
	for key := range window {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]prompb.TimeSeries, 0, len(keys))
	for _, key := range keys {
		state := window[key]
		switch state.metricType {
		case MetricTypeCounter:
			result = append(result, aggregateSeries(state.name+"_counter_count", state.labels, state.value, at))
		case MetricTypeGauge:
			result = append(result, aggregateSeries(state.name+"_gauge", state.labels, state.value, at))
		case MetricTypeTimer:
			result = append(result, distributionSeries(state.name+"_timer_ms", state, a.cfg.TimerBucketsMS, at)...)
		case MetricTypeHistogram:
			result = append(result, distributionSeries(state.name+"_histogram", state, a.cfg.HistogramBuckets, at)...)
		case MetricTypeSet:
			result = append(result, aggregateSeries(state.name+"_set_cardinality", state.labels, float64(len(state.setValues)), at))
		}
	}
	a.mu.Lock()
	a.inflight = cloneSeries(result)
	a.inflightBytes = windowBytes
	a.mu.Unlock()
	return result
}

// Commit releases only the window already persisted atomically in the custom
// spool. It never affects metrics accumulated after the rotation.
func (a *Aggregator) Commit() {
	a.mu.Lock()
	a.inflight = nil
	a.inflightBytes = 0
	a.mu.Unlock()
}

func (a *Aggregator) Bytes() int64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.totalBytesLocked()
}

func (a *Aggregator) totalBytesLocked() int64 {
	return a.activeBytes + a.inflightBytes + a.gaugeBytes
}

func (a *Aggregator) pruneGaugesLocked(now time.Time) {
	if now.IsZero() {
		now = time.Now()
	}
	interval := a.cfg.GaugeTTL / 10
	if interval > time.Minute {
		interval = time.Minute
	}
	if !a.lastGaugePrune.IsZero() && now.Sub(a.lastGaugePrune) < interval {
		return
	}
	cutoff := now.Add(-a.cfg.GaugeTTL)
	for key, baseline := range a.gauges {
		if baseline.lastSeen.Before(cutoff) {
			delete(a.gauges, key)
			a.gaugeBytes -= baseline.bytes
		}
	}
	a.lastGaugePrune = now
}

func estimateAggregateStateBytes(state *aggregateState) int64 {
	// Include conservative map/string/slice overhead so the configured limit is
	// an upper safety envelope rather than an exact Go heap accounting value.
	result := int64(192 + len(state.name) + len(state.buckets)*8)
	for key, value := range state.labels {
		result += int64(48 + len(key) + len(value))
	}
	return result
}

func cloneSeries(series []prompb.TimeSeries) []prompb.TimeSeries {
	result := make([]prompb.TimeSeries, len(series))
	for i := range series {
		result[i] = prompb.TimeSeries{
			Labels:  append([]prompb.Label(nil), series[i].Labels...),
			Samples: append([]prompb.Sample(nil), series[i].Samples...),
		}
	}
	return result
}

func distributionSeries(baseName string, state *aggregateState, bounds []float64, at time.Time) []prompb.TimeSeries {
	result := make([]prompb.TimeSeries, 0, len(bounds)+3)
	for i, bound := range bounds {
		labels := cloneLabels(state.labels)
		labels["le"] = strconv.FormatFloat(bound, 'g', -1, 64)
		result = append(result, aggregateSeries(baseName+"_bucket", labels, state.buckets[i], at))
	}
	labels := cloneLabels(state.labels)
	labels["le"] = "+Inf"
	result = append(result, aggregateSeries(baseName+"_bucket", labels, state.buckets[len(state.buckets)-1], at))
	result = append(result,
		aggregateSeries(baseName+"_sum", state.labels, state.sum, at),
		aggregateSeries(baseName+"_count", state.labels, state.count, at),
	)
	return result
}

func aggregateSeries(name string, labels map[string]string, value float64, at time.Time) prompb.TimeSeries {
	return prompb.TimeSeries{
		Labels:  labelsMapToSortedPromLabels(name, labels),
		Samples: []prompb.Sample{{Value: value, Timestamp: at.UnixMilli()}},
	}
}

func aggregationKey(name string, metricType MetricType, labels map[string]string) string {
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(name)
	b.WriteByte(0xff)
	b.WriteString(string(metricType))
	for _, key := range keys {
		b.WriteByte(0xff)
		b.WriteString(key)
		b.WriteByte('=')
		b.WriteString(labels[key])
	}
	return b.String()
}

func cloneLabels(labels map[string]string) map[string]string {
	result := make(map[string]string, len(labels)+1)
	for key, value := range labels {
		result[key] = value
	}
	return result
}
