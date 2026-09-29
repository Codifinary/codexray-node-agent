// Copyright Codexray
// SPDX-License-Identifier: AGPL-3.0

package dogstatsd

import (
	"fmt"
	"math"

	"github.com/prometheus/prometheus/prompb"
)

func buildRawSeries(m Metric, tags map[string]string) (prompb.TimeSeries, error) {
	baseName := normalizeMetricName(m.Name)
	labels := map[string]string{
		"is_custom": "true",
	}
	for key, value := range tags {
		labels[key] = value
	}

	var metricName string
	var value float64
	switch m.Type {
	case MetricTypeCounter:
		if m.SampleRate <= 0 {
			return prompb.TimeSeries{}, fmt.Errorf("counter sample rate must be positive")
		}
		metricName = baseName + "_counter_event"
		value = m.Value / m.SampleRate
	case MetricTypeGauge:
		metricName = baseName + "_gauge"
		value = m.Value
	case MetricTypeTimer:
		metricName = baseName + "_timer_ms"
		value = m.Value
	case MetricTypeHistogram:
		metricName = baseName + "_histogram_value"
		value = m.Value
	case MetricTypeSet:
		metricName = baseName + "_set_event"
		value = 1
	default:
		return prompb.TimeSeries{}, fmt.Errorf("unsupported metric type %q", m.Type)
	}
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return prompb.TimeSeries{}, fmt.Errorf("invalid value for metric %q", m.Name)
	}

	return prompb.TimeSeries{
		Labels: labelsMapToSortedPromLabels(metricName, labels),
		Samples: []prompb.Sample{{
			Value:     value,
			Timestamp: m.ReceivedAt.UnixNano() / int64(1e6),
		}},
	}, nil
}
