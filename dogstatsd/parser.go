// Copyright Codexray
// SPDX-License-Identifier: AGPL-3.0

package dogstatsd

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

func parseLine(line string, now time.Time, maxMetricNameLength int) (*Metric, error) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return nil, fmt.Errorf("empty line")
	}

	nameAndBody := strings.SplitN(trimmed, ":", 2)
	if len(nameAndBody) != 2 {
		return nil, fmt.Errorf("invalid metric format: missing ':'")
	}

	name := strings.TrimSpace(nameAndBody[0])
	if name == "" {
		return nil, fmt.Errorf("metric name is empty")
	}
	if len(name) > maxMetricNameLength {
		return nil, fmt.Errorf("metric name exceeds %d bytes", maxMetricNameLength)
	}

	parts := strings.Split(nameAndBody[1], "|")
	if len(parts) < 2 {
		return nil, fmt.Errorf("invalid metric format: expected value|type")
	}

	valueToken := strings.TrimSpace(parts[0])
	typeToken := strings.TrimSpace(parts[1])
	if valueToken == "" {
		return nil, fmt.Errorf("metric value is empty")
	}
	if typeToken == "" {
		return nil, fmt.Errorf("metric type is empty")
	}

	m := &Metric{Name: name, SampleRate: 1, ReceivedAt: now}
	switch MetricType(typeToken) {
	case MetricTypeCounter, MetricTypeGauge, MetricTypeTimer, MetricTypeHistogram:
		v, err := strconv.ParseFloat(valueToken, 64)
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
			return nil, fmt.Errorf("invalid numeric value %q", valueToken)
		}
		m.Type = MetricType(typeToken)
		m.Value = v
		if m.Type == MetricTypeGauge && (strings.HasPrefix(valueToken, "+") || strings.HasPrefix(valueToken, "-")) {
			m.Relative = true
		}
	case MetricTypeSet:
		m.Type = MetricTypeSet
		m.SetValue = valueToken
	default:
		return nil, fmt.Errorf("unsupported metric type %q", typeToken)
	}

	seenSampleRate := false
	seenTags := false
	for _, rawToken := range parts[2:] {
		token := strings.TrimSpace(rawToken)
		if token == "" {
			continue
		}
		switch {
		case strings.HasPrefix(token, "@"):
			if seenSampleRate {
				return nil, fmt.Errorf("duplicate sample-rate section")
			}
			seenSampleRate = true
			sr, err := strconv.ParseFloat(strings.TrimPrefix(token, "@"), 64)
			if err != nil || math.IsNaN(sr) || math.IsInf(sr, 0) || sr <= 0 || sr > 1 {
				return nil, fmt.Errorf("sample rate must be greater than 0 and no greater than 1")
			}
			m.SampleRate = sr
		case strings.HasPrefix(token, "#"):
			if seenTags {
				return nil, fmt.Errorf("duplicate tag section")
			}
			seenTags = true
			tags, err := parseTags(strings.TrimPrefix(token, "#"))
			if err != nil {
				return nil, err
			}
			m.Tags = tags
		default:
			// Forward-compatible DogStatsD extensions are ignored in v1.
		}
	}
	if m.SampleRate != 1 && (m.Type == MetricTypeGauge || m.Type == MetricTypeSet) {
		return nil, fmt.Errorf("sample rate is not supported for metric type %q", m.Type)
	}

	return m, nil
}

func parseTags(raw string) (map[string]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	tags := make(map[string]string)
	for _, part := range strings.Split(raw, ",") {
		piece := strings.TrimSpace(part)
		if piece == "" {
			continue
		}
		kv := strings.SplitN(piece, ":", 2)
		key := normalizeLabelKey(kv[0])
		if key == "" || key == "label" {
			return nil, fmt.Errorf("invalid empty tag key in %q", piece)
		}
		value := "true"
		if len(kv) == 2 {
			value = sanitizeLabelValue(kv[1])
		}
		if _, exists := tags[key]; exists {
			return nil, fmt.Errorf("duplicate or normalized-colliding tag key %q", key)
		}
		tags[key] = value
	}
	if len(tags) == 0 {
		return nil, nil
	}
	return tags, nil
}
