package main

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

func gatherUptime(t *testing.T, registry *prometheus.Registry) float64 {
	t.Helper()
	families, err := registry.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	if len(families) != 1 || len(families[0].GetMetric()) != 1 {
		t.Fatalf("got %v, want one series", families)
	}
	if name := families[0].GetName(); name != "node_agent_uptime_seconds" {
		t.Fatalf("name = %q", name)
	}
	if kind := families[0].GetType().String(); kind != "GAUGE" {
		t.Fatalf("type = %s, want GAUGE", kind)
	}
	return families[0].GetMetric()[0].GetGauge().GetValue()
}

func TestUptimeReportsSecondsSinceStart(t *testing.T) {
	registry := prometheus.NewRegistry()
	registry.MustRegister(uptime("node_agent_uptime_seconds", time.Now().Add(-2*time.Minute)))

	if value := gatherUptime(t, registry); value < 120 || value > 150 {
		t.Fatalf("uptime = %v, want about 120 seconds", value)
	}
}

func TestUptimeGrowsBetweenScrapes(t *testing.T) {
	registry := prometheus.NewRegistry()
	registry.MustRegister(uptime("node_agent_uptime_seconds", time.Now()))

	first := gatherUptime(t, registry)
	time.Sleep(10 * time.Millisecond)
	if second := gatherUptime(t, registry); second <= first {
		t.Fatalf("uptime went from %v to %v, want it to grow", first, second)
	}
}
