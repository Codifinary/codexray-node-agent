// Copyright Codexray
// SPDX-License-Identifier: AGPL-3.0

package dogstatsd

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/prometheus/prompb"
	"golang.org/x/time/rate"
	"k8s.io/klog/v2"
)

type udpPacket struct {
	payload []byte
	source  netip.Addr
}

type receiverMetrics struct {
	packetsReceived        prometheus.Counter
	bytesReceived          prometheus.Counter
	linesReceived          prometheus.Counter
	parsed                 prometheus.Counter
	accepted               prometheus.Counter
	parseErrors            *prometheus.CounterVec
	dropped                *prometheus.CounterVec
	droppedTags            *prometheus.CounterVec
	activeSeries           prometheus.Gauge
	bufferedEvents         prometheus.Gauge
	packetQueueBytes       prometheus.Gauge
	aggregationBytes       prometheus.Gauge
	packetQueueUtilization prometheus.Gauge
	packetQueueSaturated   prometheus.Gauge
	shutdownPendingPackets prometheus.Gauge
	shutdownDropped        prometheus.Counter
}

type Receiver struct {
	cfg            Config
	metrics        receiverMetrics
	tagPolicy      tagPolicy
	limiter        *seriesLimiter
	buffer         *seriesBuffer
	aggregator     *Aggregator
	allowedSources []netip.Prefix
	byteLimiter    *rate.Limiter
	eventLimiter   *rate.Limiter

	conn             *net.UDPConn
	packets          chan udpPacket
	packetBytes      atomic.Int64
	stop             chan struct{}
	closeOnce        sync.Once
	shutdownDropOnce sync.Once
	closeDone        chan struct{}
	closeErr         error
	wg               sync.WaitGroup
	ready            atomic.Bool
	queuePressure    *pressureState
}

func New(cfg Config, registerer prometheus.Registerer) (*Receiver, error) {
	return newReceiver(cfg, registerer, nil)
}

// NewProduction configures the receiver to aggregate native StatsD semantics.
// Production receivers do not enqueue raw series into the shared node-metric
// remote-write source.
func NewProduction(cfg Config, registerer prometheus.Registerer, aggregator *Aggregator) (*Receiver, error) {
	if aggregator == nil {
		return nil, errors.New("production DogStatsD aggregator must not be nil")
	}
	return newReceiver(cfg, registerer, aggregator)
}

func newReceiver(cfg Config, registerer prometheus.Registerer, aggregator *Aggregator) (*Receiver, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	allowedSources, err := parseAllowedSourceCIDRs(cfg.AllowedSourceCIDRs)
	if err != nil {
		return nil, err
	}
	if registerer == nil {
		registerer = prometheus.DefaultRegisterer
	}

	m := receiverMetrics{
		packetsReceived: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "node_agent", Subsystem: "dogstatsd", Name: "packets_received_total",
			Help: "Total number of DogStatsD UDP packets received.",
		}),
		bytesReceived: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "node_agent", Subsystem: "dogstatsd", Name: "bytes_received_total",
			Help: "Total number of DogStatsD UDP payload bytes received.",
		}),
		linesReceived: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "node_agent", Subsystem: "dogstatsd", Name: "lines_received_total",
			Help: "Total number of non-empty StatsD metric lines received.",
		}),
		parsed: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "node_agent", Subsystem: "dogstatsd", Name: "parsed_total",
			Help: "Total number of StatsD metric lines parsed successfully.",
		}),
		accepted: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "node_agent", Subsystem: "dogstatsd", Name: "accepted_total",
			Help: "DogStatsD metric lines accepted after parsing, policy, and cardinality checks.",
		}),
		parseErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "node_agent", Subsystem: "dogstatsd", Name: "parse_errors_total",
			Help: "Total number of StatsD parse errors by bounded reason.",
		}, []string{"reason"}),
		dropped: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "node_agent", Subsystem: "dogstatsd", Name: "dropped_total",
			Help: "Total number of DogStatsD packets or metrics dropped by reason.",
		}, []string{"reason"}),
		droppedTags: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "node_agent", Subsystem: "dogstatsd", Name: "dropped_tags_total",
			Help: "Total number of DogStatsD tags dropped by policy reason.",
		}, []string{"reason"}),
		activeSeries: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "node_agent", Subsystem: "dogstatsd", Name: "active_series",
			Help: "Current number of tracked DogStatsD series.",
		}),
		bufferedEvents: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "node_agent", Subsystem: "dogstatsd", Name: "buffered_events",
			Help: "Current number of converted DogStatsD events waiting to be spooled.",
		}),
		packetQueueBytes: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "node_agent", Subsystem: "dogstatsd", Name: "packet_queue_bytes",
			Help: "Current bytes waiting in the DogStatsD parser queue.",
		}),
		aggregationBytes: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "node_agent", Subsystem: "dogstatsd", Name: "aggregation_bytes",
			Help: "Estimated bytes retained by active and in-flight DogStatsD aggregation windows.",
		}),
		packetQueueUtilization: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "node_agent", Subsystem: "dogstatsd", Name: "packet_queue_utilization_ratio",
			Help: "Maximum of DogStatsD packet queue count and byte utilization.",
		}),
		packetQueueSaturated: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "node_agent", Subsystem: "dogstatsd", Name: "packet_queue_saturated",
			Help: "Whether packet queue utilization has remained above the saturation threshold for the configured duration.",
		}),
		shutdownPendingPackets: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "node_agent", Subsystem: "dogstatsd", Name: "shutdown_pending_packets",
			Help: "Packets that were queued when graceful DogStatsD shutdown began.",
		}),
		shutdownDropped: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "node_agent", Subsystem: "dogstatsd", Name: "shutdown_dropped_packets_total",
			Help: "Queued DogStatsD packets left unprocessed when the shutdown drain deadline expired.",
		}),
	}
	registerer.MustRegister(
		m.packetsReceived, m.bytesReceived, m.linesReceived, m.parsed, m.accepted,
		m.parseErrors, m.dropped, m.droppedTags, m.activeSeries, m.bufferedEvents,
		m.packetQueueBytes, m.aggregationBytes, m.packetQueueUtilization, m.packetQueueSaturated,
		m.shutdownPendingPackets, m.shutdownDropped,
	)

	return &Receiver{
		cfg:            cfg,
		metrics:        m,
		tagPolicy:      newTagPolicy(cfg.TagKeyBlocklist, cfg.MaxTagsPerMetric, cfg.MaxTagKeyLength, cfg.MaxTagValueLength),
		limiter:        newSeriesLimiter(cfg.ActiveSeriesPerMetricCap, cfg.ActiveSeriesGlobalCap, cfg.ActiveSeriesTTL),
		buffer:         newSeriesBuffer(cfg.BufferMaxEvents),
		aggregator:     aggregator,
		allowedSources: allowedSources,
		byteLimiter:    rate.NewLimiter(rate.Limit(cfg.MaxBytesPerSecond), maxInt(cfg.MaxPacketBytes, cfg.MaxBytesPerSecond*2)),
		eventLimiter:   rate.NewLimiter(rate.Limit(cfg.MaxEventsPerSecond), cfg.MaxEventsPerSecond*2),
		packets:        make(chan udpPacket, cfg.PacketQueueSize),
		stop:           make(chan struct{}),
		queuePressure:  newPressureState(cfg.SaturationThreshold, cfg.SaturationDuration),
		closeDone:      make(chan struct{}),
	}, nil
}

func (r *Receiver) Start() error {
	addr, err := net.ResolveUDPAddr("udp", r.cfg.ListenAddr)
	if err != nil {
		return err
	}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return err
	}
	r.conn = conn
	r.ready.Store(true)
	_ = conn.SetReadBuffer(4 << 20)

	r.wg.Add(1 + r.cfg.ParseWorkers)
	go r.readLoop()
	for i := 0; i < r.cfg.ParseWorkers; i++ {
		go r.parseLoop()
	}
	klog.Infof("DogStatsD receiver listening on %s with %d parser workers", conn.LocalAddr(), r.cfg.ParseWorkers)
	return nil
}

func (r *Receiver) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), r.cfg.ShutdownDrainTimeout)
	defer cancel()
	return r.CloseContext(ctx)
}

// CloseContext stops UDP intake and waits for parser workers to drain packets
// already accepted into the bounded queue. A deadline never blocks process
// shutdown indefinitely; remaining queued packets are explicitly accounted.
func (r *Receiver) CloseContext(ctx context.Context) error {
	r.closeOnce.Do(func() {
		r.ready.Store(false)
		r.metrics.shutdownPendingPackets.Set(float64(len(r.packets)))
		close(r.stop)
		if r.conn != nil {
			r.closeErr = r.conn.Close()
		}
		go func() {
			r.wg.Wait()
			r.metrics.shutdownPendingPackets.Set(0)
			close(r.closeDone)
		}()
	})
	select {
	case <-r.closeDone:
		return r.closeErr
	case <-ctx.Done():
		remaining := len(r.packets)
		r.shutdownDropOnce.Do(func() {
			if remaining > 0 {
				r.metrics.shutdownDropped.Add(float64(remaining))
			}
		})
		return fmt.Errorf("dogstatsd shutdown drain timed out with %d queued packets: %w", remaining, ctx.Err())
	}
}

func (r *Receiver) Ready() bool {
	return r.ready.Load() && !r.observeQueuePressure(time.Now())
}

func (r *Receiver) observeQueuePressure(now time.Time) bool {
	countUtilization := float64(len(r.packets)) / float64(r.cfg.PacketQueueSize)
	byteUtilization := float64(r.packetBytes.Load()) / float64(r.cfg.PacketQueueMaxBytes)
	utilization := countUtilization
	if byteUtilization > utilization {
		utilization = byteUtilization
	}
	saturated := r.queuePressure.Observe(utilization, now)
	r.metrics.packetQueueUtilization.Set(utilization)
	r.metrics.packetQueueSaturated.Set(boolFloat(saturated))
	return saturated
}

func (r *Receiver) Addr() net.Addr {
	if r.conn == nil {
		return nil
	}
	return r.conn.LocalAddr()
}

func (r *Receiver) Snapshot(maxSeries int) []prompb.TimeSeries {
	if maxSeries <= 0 || maxSeries > r.cfg.BatchMaxSeries {
		maxSeries = r.cfg.BatchMaxSeries
	}
	return r.buffer.Snapshot(maxSeries)
}

func (r *Receiver) Commit(count int) {
	remaining := r.buffer.Commit(count)
	r.metrics.bufferedEvents.Set(float64(remaining))
}

func (r *Receiver) readLoop() {
	defer r.wg.Done()
	defer close(r.packets)
	buf := make([]byte, r.cfg.MaxPacketBytes+1)
	for {
		n, source, err := r.conn.ReadFromUDP(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			select {
			case <-r.stop:
				return
			default:
				klog.Warningf("DogStatsD UDP read failed: %v", err)
				r.metrics.dropped.WithLabelValues("udp_read_error").Inc()
				continue
			}
		}
		r.metrics.packetsReceived.Inc()
		r.metrics.bytesReceived.Add(float64(n))
		sourceAddr, validSource := netip.AddrFromSlice(source.IP)
		if !validSource || !r.sourceAllowed(sourceAddr.Unmap()) {
			r.metrics.dropped.WithLabelValues("untrusted_source").Inc()
			continue
		}
		if n > r.cfg.MaxPacketBytes {
			r.metrics.dropped.WithLabelValues("packet_too_large").Inc()
			continue
		}
		if !r.byteLimiter.AllowN(time.Now(), n) {
			r.metrics.dropped.WithLabelValues("byte_rate_limit").Inc()
			continue
		}
		payload := append([]byte(nil), buf[:n]...)
		queuedBytes := r.packetBytes.Add(int64(n))
		if queuedBytes > r.cfg.PacketQueueMaxBytes {
			r.packetBytes.Add(-int64(n))
			r.metrics.dropped.WithLabelValues("packet_queue_bytes").Inc()
			r.observeQueuePressure(time.Now())
			continue
		}
		select {
		case r.packets <- udpPacket{payload: payload, source: sourceAddr.Unmap()}:
			r.metrics.packetQueueBytes.Set(float64(queuedBytes))
			r.observeQueuePressure(time.Now())
		case <-r.stop:
			r.packetBytes.Add(-int64(n))
			return
		default:
			r.packetBytes.Add(-int64(n))
			r.metrics.dropped.WithLabelValues("packet_queue_full").Inc()
			r.observeQueuePressure(time.Now())
		}
	}
}

func (r *Receiver) parseLoop() {
	defer r.wg.Done()
	for packet := range r.packets {
		remaining := r.packetBytes.Add(-int64(len(packet.payload)))
		r.metrics.packetQueueBytes.Set(float64(remaining))
		r.observeQueuePressure(time.Now())
		r.processPacket(packet.payload)
	}
}

func (r *Receiver) processPacket(payload []byte) {
	now := time.Now()
	for _, line := range strings.Split(string(payload), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if !r.eventLimiter.Allow() {
			r.metrics.dropped.WithLabelValues("event_rate_limit").Inc()
			continue
		}
		r.metrics.linesReceived.Inc()
		metric, err := parseLine(line, now, r.cfg.MaxMetricNameLength)
		if err != nil {
			r.metrics.parseErrors.WithLabelValues(parseErrorReason(err)).Inc()
			continue
		}
		r.metrics.parsed.Inc()

		tags, drops := r.tagPolicy.Apply(metric.Tags)
		r.recordTagDrops(drops)
		series, err := buildRawSeries(*metric, tags)
		if err != nil {
			r.metrics.dropped.WithLabelValues("mapping_error").Inc()
			continue
		}
		allowed, reason, active := r.limiter.Allow(series, now)
		r.metrics.activeSeries.Set(float64(active))
		if !allowed {
			r.metrics.dropped.WithLabelValues(reason).Inc()
			continue
		}
		r.metrics.accepted.Inc()
		if r.aggregator != nil {
			accepted, reason := r.aggregator.Add(*metric, tags)
			if !accepted {
				r.metrics.dropped.WithLabelValues(reason).Inc()
			}
			r.metrics.aggregationBytes.Set(float64(r.aggregator.Bytes()))
			continue
		}
		dropped, buffered := r.buffer.Enqueue(series)
		if dropped > 0 {
			r.metrics.dropped.WithLabelValues("buffer_full").Add(float64(dropped))
		}
		r.metrics.bufferedEvents.Set(float64(buffered))
	}
}

func (r *Receiver) sourceAllowed(source netip.Addr) bool {
	for _, prefix := range r.allowedSources {
		if prefix.Contains(source) {
			return true
		}
	}
	return false
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func (r *Receiver) recordTagDrops(drops tagDropStats) {
	if drops.BlockedKeys > 0 {
		r.metrics.droppedTags.WithLabelValues("blocked_key").Add(float64(drops.BlockedKeys))
	}
	if drops.MaxKeyLength > 0 {
		r.metrics.droppedTags.WithLabelValues("key_too_long").Add(float64(drops.MaxKeyLength))
	}
	if drops.MaxValueLength > 0 {
		r.metrics.droppedTags.WithLabelValues("value_too_long").Add(float64(drops.MaxValueLength))
	}
	if drops.MaxTagsPerMetric > 0 {
		r.metrics.droppedTags.WithLabelValues("too_many_tags").Add(float64(drops.MaxTagsPerMetric))
	}
}

func parseErrorReason(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "unsupported metric type"):
		return "unsupported_type"
	case strings.Contains(msg, "sample rate"):
		return "invalid_sample_rate"
	case strings.Contains(msg, "name"):
		return "invalid_name"
	case strings.Contains(msg, "value"), strings.Contains(msg, "gauge"):
		return "invalid_value"
	default:
		return "invalid_format"
	}
}
