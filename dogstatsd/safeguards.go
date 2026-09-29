// Copyright Codexray
// SPDX-License-Identifier: AGPL-3.0

package dogstatsd

import (
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/prometheus/prompb"
)

var reservedLabelKeys = []string{
	"__name__",
	"is_custom",
	// Do not allow applications to reintroduce the removed generated label.
	"statsd_type",
	"instance",
	"job",
	"machine_id",
	"system_uuid",
	"tenant_id",
	"project_id",
}

type tagDropStats struct {
	BlockedKeys      int
	MaxKeyLength     int
	MaxValueLength   int
	MaxTagsPerMetric int
}

type tagPolicy struct {
	blockedKeys       map[string]struct{}
	maxTagsPerMetric  int
	maxTagKeyLength   int
	maxTagValueLength int
}

func newTagPolicy(blocklistCSV string, maxTagsPerMetric, maxTagKeyLength, maxTagValueLength int) tagPolicy {
	blocked := make(map[string]struct{})
	for _, raw := range append(strings.Split(blocklistCSV, ","), reservedLabelKeys...) {
		key := normalizeLabelKey(raw)
		if key != "" && key != "label" {
			blocked[key] = struct{}{}
		}
	}
	return tagPolicy{
		blockedKeys:       blocked,
		maxTagsPerMetric:  maxTagsPerMetric,
		maxTagKeyLength:   maxTagKeyLength,
		maxTagValueLength: maxTagValueLength,
	}
}

func (p tagPolicy) Apply(tags map[string]string) (map[string]string, tagDropStats) {
	keys := make([]string, 0, len(tags))
	for key := range tags {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	filtered := make(map[string]string, len(tags))
	var drops tagDropStats
	for _, rawKey := range keys {
		key := normalizeLabelKey(rawKey)
		if _, blocked := p.blockedKeys[key]; blocked {
			drops.BlockedKeys++
			continue
		}
		if len(key) > p.maxTagKeyLength {
			drops.MaxKeyLength++
			continue
		}
		value := sanitizeLabelValue(tags[rawKey])
		if len(value) > p.maxTagValueLength {
			drops.MaxValueLength++
			continue
		}
		if len(filtered) >= p.maxTagsPerMetric {
			drops.MaxTagsPerMetric++
			continue
		}
		filtered[key] = value
	}
	return filtered, drops
}

type seriesLimiter struct {
	mu           sync.Mutex
	perMetricCap int
	globalCap    int
	ttl          time.Duration
	lastPrune    time.Time
	byMetric     map[string]map[string]time.Time
	total        int
}

func newSeriesLimiter(perMetricCap, globalCap int, ttl time.Duration) *seriesLimiter {
	return &seriesLimiter{
		perMetricCap: perMetricCap,
		globalCap:    globalCap,
		ttl:          ttl,
		byMetric:     make(map[string]map[string]time.Time),
	}
}

func (l *seriesLimiter) Allow(series prompb.TimeSeries, now time.Time) (bool, string, int) {
	metricName := labelValue(series.Labels, "__name__")
	if metricName == "" {
		metricName = "unnamed_metric"
	}
	fingerprint := seriesFingerprint(series)

	l.mu.Lock()
	defer l.mu.Unlock()
	if metricSeries := l.byMetric[metricName]; metricSeries != nil {
		if _, exists := metricSeries[fingerprint]; exists {
			metricSeries[fingerprint] = now
			return true, "", l.total
		}
	}

	pruneEvery := l.ttl / 10
	if pruneEvery > time.Minute {
		pruneEvery = time.Minute
	}
	if pruneEvery <= 0 || l.lastPrune.IsZero() || now.Sub(l.lastPrune) >= pruneEvery || l.total >= l.globalCap {
		l.pruneExpiredLocked(now)
	}

	metricSeries := l.byMetric[metricName]
	if len(metricSeries) >= l.perMetricCap {
		return false, "series_cap_metric", l.total
	}
	if l.total >= l.globalCap {
		return false, "series_cap_global", l.total
	}
	if metricSeries == nil {
		metricSeries = make(map[string]time.Time)
		l.byMetric[metricName] = metricSeries
	}
	metricSeries[fingerprint] = now
	l.total++
	return true, "", l.total
}

func (l *seriesLimiter) pruneExpiredLocked(now time.Time) {
	cutoff := now.Add(-l.ttl)
	for metricName, metricSeries := range l.byMetric {
		for fingerprint, lastSeen := range metricSeries {
			if lastSeen.Before(cutoff) {
				delete(metricSeries, fingerprint)
				l.total--
			}
		}
		if len(metricSeries) == 0 {
			delete(l.byMetric, metricName)
		}
	}
	l.lastPrune = now
}

func labelValue(labels []prompb.Label, name string) string {
	for _, label := range labels {
		if label.Name == name {
			return label.Value
		}
	}
	return ""
}

func seriesFingerprint(series prompb.TimeSeries) string {
	var b strings.Builder
	for _, label := range series.Labels {
		b.WriteString(label.Name)
		b.WriteByte('=')
		b.WriteString(label.Value)
		b.WriteByte(0xff)
	}
	return b.String()
}
