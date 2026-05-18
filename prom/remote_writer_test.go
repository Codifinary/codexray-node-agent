package prom

import (
	"testing"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/prometheus/prompb"
)

func TestMetricMetadataType(t *testing.T) {
	tests := []struct {
		name string
		in   dto.MetricType
		want prompb.MetricMetadata_MetricType
	}{
		{name: "counter", in: dto.MetricType_COUNTER, want: prompb.MetricMetadata_COUNTER},
		{name: "gauge", in: dto.MetricType_GAUGE, want: prompb.MetricMetadata_GAUGE},
		{name: "histogram", in: dto.MetricType_HISTOGRAM, want: prompb.MetricMetadata_HISTOGRAM},
		{name: "gauge_histogram", in: dto.MetricType_GAUGE_HISTOGRAM, want: prompb.MetricMetadata_GAUGEHISTOGRAM},
		{name: "summary", in: dto.MetricType_SUMMARY, want: prompb.MetricMetadata_SUMMARY},
		{name: "untyped", in: dto.MetricType_UNTYPED, want: prompb.MetricMetadata_UNKNOWN},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := metricMetadataType(tc.in)
			if got != tc.want {
				t.Fatalf("metricMetadataType(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
