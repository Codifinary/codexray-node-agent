package dogstatsd

import (
	"context"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

func TestReceiverAcceptsDogStatsD(t *testing.T) {
	cfg := testConfig()
	cfg.ListenAddr = "127.0.0.1:0"
	receiver, err := New(cfg, prometheus.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	if err := receiver.Start(); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "operation not permitted") {
			t.Skipf("UDP sockets unavailable: %v", err)
		}
		t.Fatal(err)
	}
	defer receiver.Close()

	conn, err := net.Dial("udp", receiver.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("orders.created:1|c|#service:checkout,is_custom:false,statsd_type:gauge")); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		series := receiver.Snapshot(10)
		if len(series) == 1 {
			if got := labelValue(series[0].Labels, "is_custom"); got != "true" {
				t.Fatalf("is_custom = %q", got)
			}
			if got := labelValue(series[0].Labels, "service"); got != "checkout" {
				t.Fatalf("service = %q", got)
			}
			if got := labelValue(series[0].Labels, "statsd_type"); got != "" {
				t.Fatalf("statsd_type must be discarded, got %q", got)
			}
			receiver.Commit(1)
			if len(receiver.Snapshot(10)) != 0 {
				t.Fatal("committed series remains buffered")
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for DogStatsD metric")
}

func TestReceiverShutdownDrainsQueuedPackets(t *testing.T) {
	agg := testAggregator(t)
	receiver, err := NewProduction(testConfig(), prometheus.NewRegistry(), agg)
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("orders:1|c")
	receiver.packetBytes.Store(int64(len(payload)))
	receiver.packets <- udpPacket{payload: payload}
	receiver.ready.Store(true)
	receiver.wg.Add(2)
	go func() {
		defer receiver.wg.Done()
		<-receiver.stop
		close(receiver.packets)
	}()
	go receiver.parseLoop()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := receiver.CloseContext(ctx); err != nil {
		t.Fatal(err)
	}
	if receiver.packetBytes.Load() != 0 {
		t.Fatalf("queued bytes after drain=%d", receiver.packetBytes.Load())
	}
	series := agg.Flush(time.UnixMilli(1))
	assertSeriesValue(t, series, "orders_counter_count", nil, 1)
	if got := counterValue(t, receiver.metrics.shutdownDropped); got != 0 {
		t.Fatalf("shutdown drops=%v, want 0", got)
	}
}

func TestReceiverShutdownTimeoutAccountsQueuedPackets(t *testing.T) {
	receiver, err := New(testConfig(), prometheus.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	receiver.packets <- udpPacket{payload: []byte("orders:1|c")}
	release := make(chan struct{})
	receiver.wg.Add(1)
	go func() {
		defer receiver.wg.Done()
		<-release
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	err = receiver.CloseContext(ctx)
	cancel()
	if err == nil {
		t.Fatal("expected shutdown drain timeout")
	}
	if got := counterValue(t, receiver.metrics.shutdownDropped); got != 1 {
		t.Fatalf("shutdown drops=%v, want 1", got)
	}
	close(release)
	select {
	case <-receiver.closeDone:
	case <-time.After(time.Second):
		t.Fatal("receiver shutdown goroutine did not finish")
	}
}

func TestReceiverReadinessLifecycle(t *testing.T) {
	cfg := testConfig()
	cfg.ListenAddr = "127.0.0.1:0"
	receiver, err := New(cfg, prometheus.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	if receiver.Ready() {
		t.Fatal("receiver ready before Start")
	}
	if err := receiver.Start(); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "operation not permitted") {
			t.Skipf("UDP sockets unavailable: %v", err)
		}
		t.Fatal(err)
	}
	if !receiver.Ready() {
		t.Fatal("receiver not ready after Start")
	}
	if err := receiver.Close(); err != nil {
		t.Fatal(err)
	}
	if receiver.Ready() {
		t.Fatal("receiver ready after Close")
	}
}

func TestProductionReceiverUsesAggregatorInsteadOfRawBuffer(t *testing.T) {
	cfg := testConfig()
	cfg.ListenAddr = "127.0.0.1:0"
	agg := testAggregator(t)
	receiver, err := NewProduction(cfg, prometheus.NewRegistry(), agg)
	if err != nil {
		t.Fatal(err)
	}
	if err := receiver.Start(); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "operation not permitted") {
			t.Skipf("UDP sockets unavailable: %v", err)
		}
		t.Fatal(err)
	}
	defer receiver.Close()
	conn, err := net.Dial("udp", receiver.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("orders.created:2|c|@0.5|#service:checkout")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		series := agg.Snapshot(time.UnixMilli(123))
		if len(series) == 1 {
			assertSeriesValue(t, series, "orders_created_counter_count", nil, 4)
			if raw := receiver.Snapshot(10); len(raw) != 0 {
				t.Fatalf("production receiver populated raw buffer: %d", len(raw))
			}
			agg.Commit()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for production aggregation")
}

func TestReceiverSourceCIDRsAndEventRateLimit(t *testing.T) {
	cfg := testConfig()
	cfg.AllowedSourceCIDRs = "10.0.0.0/8,192.168.1.0/24"
	cfg.MaxEventsPerSecond = 1
	agg := testAggregator(t)
	receiver, err := NewProduction(cfg, prometheus.NewRegistry(), agg)
	if err != nil {
		t.Fatal(err)
	}
	if !receiver.sourceAllowed(netip.MustParseAddr("10.20.30.40")) || !receiver.sourceAllowed(netip.MustParseAddr("192.168.1.9")) {
		t.Fatal("trusted source was rejected")
	}
	if receiver.sourceAllowed(netip.MustParseAddr("8.8.8.8")) {
		t.Fatal("public source was accepted")
	}
	receiver.processPacket([]byte("requests:1|c\nrequests:1|c\nrequests:1|c"))
	series := agg.Snapshot(time.UnixMilli(1))
	assertSeriesValue(t, series, "requests_counter_count", nil, 2)
}

func TestParseAllowedSourceCIDRsRejectsUnsafeConfiguration(t *testing.T) {
	if _, err := parseAllowedSourceCIDRs(""); err == nil {
		t.Fatal("expected empty CIDR list to fail")
	}
	if _, err := parseAllowedSourceCIDRs("not-a-cidr"); err == nil {
		t.Fatal("expected malformed CIDR to fail")
	}
}

func testConfig() Config {
	return Config{
		ListenAddr:               "127.0.0.1:8125",
		MaxPacketBytes:           8192,
		PacketQueueSize:          16,
		PacketQueueMaxBytes:      64 * 1024,
		ParseWorkers:             1,
		BufferMaxEvents:          16,
		BatchMaxSeries:           16,
		MaxMetricNameLength:      255,
		MaxTagsPerMetric:         20,
		MaxTagKeyLength:          64,
		MaxTagValueLength:        128,
		ActiveSeriesPerMetricCap: 10,
		ActiveSeriesGlobalCap:    100,
		ActiveSeriesTTL:          time.Hour,
		TagKeyBlocklist:          "user_id,request_id,session_id,trace_id",
		AllowedSourceCIDRs:       "127.0.0.0/8,::1/128",
		MaxBytesPerSecond:        1 << 20,
		MaxEventsPerSecond:       10000,
		SaturationThreshold:      0.8,
		SaturationDuration:       time.Minute,
		ShutdownDrainTimeout:     time.Second,
	}
}
