// Copyright Codexray
// SPDX-License-Identifier: AGPL-3.0

package dogstatsd

import (
	"sort"
	"strings"

	"github.com/prometheus/prometheus/prompb"
)

func normalizeMetricName(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	if n == "" {
		return "unnamed_metric"
	}
	var b strings.Builder
	b.Grow(len(n))
	for _, r := range n {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == ':' {
			b.WriteRune(r)
			continue
		}
		b.WriteByte('_')
	}
	out := collapseUnderscores(b.String())
	if strings.Trim(out, "_") == "" {
		out = "unnamed_metric"
	}
	if out[0] >= '0' && out[0] <= '9' {
		out = "_" + out
	}
	return out
}

func normalizeLabelKey(key string) string {
	k := strings.ToLower(strings.TrimSpace(key))
	if k == "" {
		return "label"
	}
	var b strings.Builder
	b.Grow(len(k))
	for _, r := range k {
		// Keep the output compatible with the traditional Prometheus label-name
		// grammar. Non-ASCII input is still accepted, but it is normalized to an
		// underscore instead of leaking an invalid remote-write label downstream.
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			b.WriteRune(r)
			continue
		}
		b.WriteByte('_')
	}
	out := collapseUnderscores(b.String())
	if strings.Trim(out, "_") == "" {
		out = "label"
	}
	if out[0] >= '0' && out[0] <= '9' {
		out = "_" + out
	}
	return out
}

func sanitizeLabelValue(value string) string {
	v := strings.TrimSpace(strings.ToValidUTF8(value, "\uFFFD"))
	if v == "" {
		return "true"
	}
	return v
}

func labelsMapToSortedPromLabels(metricName string, labels map[string]string) []prompb.Label {
	res := make([]prompb.Label, 0, len(labels)+1)
	res = append(res, prompb.Label{Name: "__name__", Value: metricName})
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		res = append(res, prompb.Label{Name: k, Value: labels[k]})
	}
	return res
}

func collapseUnderscores(s string) string {
	for strings.Contains(s, "__") {
		s = strings.ReplaceAll(s, "__", "_")
	}
	return s
}
