// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/codifinary/codexray-node-agent/common"
	"github.com/codifinary/codexray-node-agent/containers"
	"github.com/codifinary/codexray-node-agent/diag"
	"github.com/codifinary/codexray-node-agent/dogstatsd"
	"github.com/codifinary/codexray-node-agent/flags"
	"github.com/codifinary/codexray-node-agent/gpu"
	"github.com/codifinary/codexray-node-agent/logs"
	"github.com/codifinary/codexray-node-agent/node"
	"github.com/codifinary/codexray-node-agent/proc"
	"github.com/codifinary/codexray-node-agent/profiling"
	"github.com/codifinary/codexray-node-agent/prom"
	"github.com/codifinary/codexray-node-agent/tracing"
	"github.com/codifinary/logparser"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"golang.org/x/sys/unix"
	"golang.org/x/time/rate"
	"k8s.io/klog/v2"
)

var (
	version = flags.Version
)

func uname() (string, string, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	f, err := os.Open("/proc/1/ns/uts")
	if err != nil {
		return "", "", err
	}
	defer f.Close()

	self, err := os.Open("/proc/self/ns/uts")
	if err != nil {
		return "", "", err
	}
	defer self.Close()

	defer func() {
		unix.Setns(int(self.Fd()), unix.CLONE_NEWUTS)
	}()

	err = unix.Setns(int(f.Fd()), unix.CLONE_NEWUTS)
	if err != nil {
		return "", "", err
	}
	var utsname unix.Utsname
	if err := unix.Uname(&utsname); err != nil {
		return "", "", err
	}
	hostname := string(bytes.Split(utsname.Nodename[:], []byte{0})[0])
	kernelVersion := string(bytes.Split(utsname.Release[:], []byte{0})[0])
	return hostname, kernelVersion, nil
}

func machineID() string {
	for _, p := range []string{"/etc/machine-id", "/var/lib/dbus/machine-id", "/sys/devices/virtual/dmi/id/product_uuid"} {
		payload, err := os.ReadFile(proc.HostPath(p))
		if err != nil {
			klog.Warningln("failed to read machine-id:", err)
			continue
		}
		id := strings.TrimSpace(strings.Replace(string(payload), "-", "", -1))
		klog.Infoln("machine-id: ", id)
		return id
	}
	return ""
}

func systemUUID() string {
	payload, err := os.ReadFile(proc.HostPath("/sys/devices/virtual/dmi/id/product_uuid"))
	if err != nil {
		klog.Warningln("failed to read system-uuid:", err)
		return ""
	}
	return strings.TrimSpace(string(payload))
}

func whitelistNodeExternalNetworks() {
	netdevs, err := node.NetDevices()
	if err != nil {
		klog.Warningln("failed to get network interfaces:", err)
		return
	}
	for _, iface := range netdevs {
		for _, p := range iface.IPPrefixes {
			if p.IP().IsLoopback() || common.IsIpPrivate(p.IP()) {
				continue
			}
			// if the node has an external network IP, whitelist that network
			common.ConnectionFilter.WhitelistPrefix(p)
		}
	}
}

func main() {
	klog.LogToStderr(false)
	klog.SetOutput(&RateLimitedLogOutput{limiter: rate.NewLimiter(rate.Limit(*flags.LogPerSecond), *flags.LogBurst)})

	klog.Infoln("agent version:", version)
	diag.ApplyGoMemoryLimit(*flags.GoMemoryLimitRatio)
	watchdogDir := *flags.MemoryWatchdogDir
	if watchdogDir == "" {
		watchdogDir = filepath.Join(*flags.WalDir, "diagnostics")
	}
	watchdog := diag.NewWatchdog(diag.WatchdogConfig{
		Dir:            watchdogDir,
		ThresholdBytes: uint64(*flags.MemoryWatchdogThreshold),
		ThresholdRatio: *flags.MemoryWatchdogThresholdRatio,
		MinInterval:    *flags.MemoryWatchdogMinInterval,
		MaxDumps:       *flags.MemoryWatchdogMaxDumps,
	})
	watchdog.Start()
	defer watchdog.Stop()

	hostname, kv, err := uname()
	if err != nil {
		klog.Exitln("failed to get uname:", err)
	}
	klog.Infoln("hostname:", hostname)
	klog.Infoln("kernel version:", kv)

	if err = common.SetKernelVersion(kv); err != nil {
		klog.Exitln(err)
	}

	if !common.GetKernelVersion().GreaterOrEqual(common.NewVersion(4, 16, 0)) {
		klog.Exitln("the minimum Linux kernel version required is 4.16 or later")
	}

	whitelistNodeExternalNetworks()

	machineId := machineID()
	systemUuid := systemUUID()

	logparser.DefaultOptions = logparser.Options{
		MaxPatterns:  *flags.LogPatternsMax,
		MaxSampleLen: *flags.MaxLabelLength, // the exported sample label is truncated to this anyway
	}

	tm := flags.Telemetry
	klog.Infof("telemetry: node_metrics=%t container_resource_metrics=%t l7_metrics=%t l7_traces=%t log_collection=%t log_parsing=%t profiling=%t",
		tm.NodeMetrics, tm.ContainerResourceMetrics, tm.L7Metrics, tm.L7Traces, tm.LogCollection, tm.LogParsing, tm.Profiling)
	logparser.DefaultOptions.DisablePatterns = !tm.LogParsing

	if tm.L7Traces {
		tracing.Init(machineId, hostname, version)
	} else {
		klog.Infoln("eBPF L7 traces disabled")
	}
	if tm.LogCollection {
		logs.Init(machineId, hostname, version)
	} else {
		klog.Infoln("container log collection (export) disabled")
	}

	nodeCollector := node.NewCollector(hostname, kv)

	registry := prometheus.NewRegistry()

	registerer := prometheus.WrapRegistererWith(
		prometheus.Labels{"machine_id": machineId, "system_uuid": systemUuid},
		registry,
	)
	registerRuntimeDiagnostics(registerer)
	for name, enabled := range map[string]bool{
		"node_metrics": tm.NodeMetrics, "container_resource_metrics": tm.ContainerResourceMetrics,
		"ebpf_l7_metrics": tm.L7Metrics, "ebpf_l7_traces": tm.L7Traces,
		"container_log_collection": tm.LogCollection, "container_log_parsing": tm.LogParsing,
		"profiling": tm.Profiling,
	} {
		diag.SetFeature(name, enabled)
	}
	if tm.NodeMetrics {
		if err := registerer.Register(nodeCollector); err != nil {
			klog.Exitln(err)
		}
	} else {
		klog.Infoln("node metrics disabled")
	}

	gpuCollector, err := gpu.NewCollector()
	if err != nil {
		klog.Warningln("failed to initialize GPU collector:", err)
	}
	if err := registerer.Register(gpuCollector); err != nil {
		klog.Exitln(err)
	}
	registerer.MustRegister(info("node_agent_info", version))

	if md := nodeCollector.Metadata(); md != nil {
		region := md.Region
		az := md.AvailabilityZone
		if region != "" && az != "" {
			registerer = prometheus.WrapRegistererWith(prometheus.Labels{"az": az, "region": region}, registerer)
		}
	}
	var processInfoCh chan<- containers.ProcessInfo
	if tm.Profiling {
		processInfoCh = profiling.Init(machineId, hostname)
	} else {
		klog.Infoln("profiling disabled")
	}
	cr, err := containers.NewRegistry(registerer, processInfoCh, gpuCollector.ProcessUsageSampleCh)
	if err != nil {
		klog.Exitln(err)
	}
	defer cr.Close()

	profiling.Start()
	defer profiling.Stop()

	var seriesSources []prom.SeriesSource
	var dogStatsDPipeline *dogstatsd.Pipeline
	var dogStatsDReceiver *dogstatsd.Receiver
	if *flags.DogStatsDEnabled {
		if *flags.MetricsEndpoint == nil {
			klog.Exitln("DogStatsD requires --collector-endpoint or --metrics-endpoint")
		}
		timerBuckets, parseErr := dogstatsd.ParseBuckets(*flags.DogStatsDTimerBucketsMS)
		if parseErr != nil {
			klog.Exitln("failed to parse DogStatsD timer buckets:", parseErr)
		}
		histogramBuckets, parseErr := dogstatsd.ParseBuckets(*flags.DogStatsDHistogramBuckets)
		if parseErr != nil {
			klog.Exitln("failed to parse DogStatsD histogram buckets:", parseErr)
		}
		aggregator, aggregatorErr := dogstatsd.NewAggregator(dogstatsd.AggregationConfig{
			TimerBucketsMS:        timerBuckets,
			HistogramBuckets:      histogramBuckets,
			MaxSetValuesPerSeries: *flags.DogStatsDSetMaxValuesPerSeries,
			MaxBytes:              int64(*flags.DogStatsDAggregationMaxBytes),
			GaugeTTL:              *flags.DogStatsDActiveSeriesTTL,
		})
		if aggregatorErr != nil {
			klog.Exitln("failed to configure DogStatsD aggregation:", aggregatorErr)
		}
		customSpoolDir := *flags.DogStatsDCustomSpoolDir
		if customSpoolDir == "" {
			customSpoolDir = filepath.Join(*flags.WalDir, "custom-metrics-spool")
		}
		customSpool, spoolErr := dogstatsd.OpenCustomSpoolWithPolicy(customSpoolDir, int64(*flags.DogStatsDCustomSpoolMaxBytes), *flags.DogStatsDCustomSpoolMaxAge, int64(*flags.DogStatsDQuarantineMaxBytes))
		if spoolErr != nil {
			klog.Exitln("failed to configure DogStatsD custom spool:", spoolErr)
		}
		customSender, senderErr := dogstatsd.NewCustomSender(&http.Client{
			Timeout:   30 * time.Second,
			Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: *flags.InsecureSkipVerify}},
		}, (*flags.MetricsEndpoint).String(), common.AuthHeaders())
		if senderErr != nil {
			klog.Exitln("failed to configure DogStatsD custom sender:", senderErr)
		}
		pipeline, pipelineErr := dogstatsd.NewPipeline(dogstatsd.PipelineConfig{
			FlushInterval:       *flags.DogStatsDFlushInterval,
			MaxBatchBytes:       int(*flags.DogStatsDMaxBatchBytes),
			ExternalLabels:      prom.SourceLabels(machineId, systemUuid),
			RetryMin:            5 * time.Second,
			RetryMax:            time.Minute,
			SaturationThreshold: *flags.DogStatsDSaturationThreshold,
			SaturationDuration:  *flags.DogStatsDSaturationDuration,
			Registerer:          registerer,
		}, aggregator, customSpool, customSender)
		if pipelineErr != nil {
			klog.Exitln("failed to configure DogStatsD custom pipeline:", pipelineErr)
		}
		pipeline.Start()
		defer pipeline.Close()
		dogStatsDPipeline = pipeline

		receiver, receiverErr := dogstatsd.NewProduction(dogstatsd.Config{
			ListenAddr:               *flags.DogStatsDListen,
			MaxPacketBytes:           *flags.DogStatsDMaxPacketBytes,
			PacketQueueSize:          *flags.DogStatsDPacketQueueSize,
			PacketQueueMaxBytes:      int64(*flags.DogStatsDPacketQueueMaxBytes),
			ParseWorkers:             *flags.DogStatsDParseWorkers,
			BufferMaxEvents:          *flags.DogStatsDBufferMaxEvents,
			BatchMaxSeries:           *flags.DogStatsDBatchMaxSeries,
			MaxMetricNameLength:      *flags.DogStatsDMaxMetricNameLength,
			MaxTagsPerMetric:         *flags.DogStatsDMaxTagsPerMetric,
			MaxTagKeyLength:          *flags.DogStatsDMaxTagKeyLength,
			MaxTagValueLength:        *flags.DogStatsDMaxTagValueLength,
			ActiveSeriesPerMetricCap: *flags.DogStatsDActiveSeriesPerMetricCap,
			ActiveSeriesGlobalCap:    *flags.DogStatsDActiveSeriesGlobalCap,
			ActiveSeriesTTL:          *flags.DogStatsDActiveSeriesTTL,
			TagKeyBlocklist:          *flags.DogStatsDTagKeyBlocklist,
			AllowedSourceCIDRs:       *flags.DogStatsDAllowedSourceCIDRs,
			MaxBytesPerSecond:        *flags.DogStatsDMaxBytesPerSecond,
			MaxEventsPerSecond:       *flags.DogStatsDMaxEventsPerSecond,
			SaturationThreshold:      *flags.DogStatsDSaturationThreshold,
			SaturationDuration:       *flags.DogStatsDSaturationDuration,
			ShutdownDrainTimeout:     *flags.DogStatsDShutdownDrainTimeout,
		}, registerer, aggregator)
		if receiverErr != nil {
			klog.Exitln("failed to configure DogStatsD receiver:", receiverErr)
		}
		if receiverErr = receiver.Start(); receiverErr != nil {
			klog.Exitln("failed to start DogStatsD receiver:", receiverErr)
		}
		defer receiver.Close()
		dogStatsDReceiver = receiver
	}

	if err := prom.StartAgent(registry, machineId, systemUuid, seriesSources...); err != nil {
		klog.Exitln(err)
	}

	// A dedicated mux: the metrics listener is commonly reachable through hostPort, so it must
	// not inherit handlers registered on http.DefaultServeMux (net/http/pprof used to be).
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{ErrorLog: logger{}, Registry: registerer}))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		if *flags.DogStatsDEnabled && (dogStatsDReceiver == nil || !dogStatsDReceiver.Ready() || dogStatsDPipeline == nil || !dogStatsDPipeline.Healthy()) {
			http.Error(w, "dogstatsd pipeline not ready", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready\n"))
	})
	if err := diag.StartPprof(*flags.PprofListen, *flags.PprofAuthToken); err != nil {
		klog.Errorln(err)
	}
	server := &http.Server{
		Addr:              *flags.ListenAddress,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	serverErr := make(chan error, 1)
	go func() {
		klog.Infoln("listening on:", *flags.ListenAddress)
		serverErr <- server.ListenAndServe()
	}()

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(signals)
	select {
	case sig := <-signals:
		klog.Infof("received %s, starting graceful shutdown", sig)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		if err := server.Shutdown(ctx); err != nil {
			klog.Warningln("HTTP shutdown did not complete cleanly:", err)
		}
		cancel()
	case err := <-serverErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			klog.Errorln("HTTP server failed:", err)
		}
	}
	if dogStatsDReceiver != nil {
		if err := dogStatsDReceiver.Close(); err != nil {
			klog.Warningln("DogStatsD receiver shutdown did not drain cleanly:", err)
		}
	}
	if dogStatsDPipeline != nil {
		if err := dogStatsDPipeline.Close(); err != nil {
			klog.Warningln("DogStatsD final spool flush failed:", err)
		}
	}
}

// registerRuntimeDiagnostics exposes the agent's own resource usage: Go heap and runtime
// memory classes (to separate Go heap from RSS), GC, goroutines, process RSS/FDs/CPU,
// the agent cgroup's memory accounting, and the bounded agent health metrics in diag.
func registerRuntimeDiagnostics(reg prometheus.Registerer) {
	for _, c := range []prometheus.Collector{
		// Default Go metrics (memstats, goroutines, GC summary) plus runtime memory classes,
		// which separate live heap, free-but-unreleased heap, stacks and runtime overhead.
		// GC size-class and scheduler histograms are left out: ~100 extra bucket series.
		collectors.NewGoCollector(collectors.WithGoCollectorRuntimeMetrics(collectors.MetricsMemory)),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		diag.NewCgroupMemoryCollector(),
	} {
		if err := reg.Register(c); err != nil {
			klog.Warningln("failed to register runtime collector:", err)
		}
	}
	if err := diag.Register(reg); err != nil {
		klog.Exitln(err)
	}
	if err := profiling.RegisterMetrics(reg); err != nil {
		klog.Exitln(err)
	}
}

func info(name, version string) prometheus.Collector {
	g := prometheus.NewGauge(prometheus.GaugeOpts{
		Name:        name,
		ConstLabels: prometheus.Labels{"version": version},
	})
	g.Set(1)
	return g
}

type logger struct{}

func (l logger) Println(v ...interface{}) {
	klog.Errorln(v...)
}

type RateLimitedLogOutput struct {
	limiter *rate.Limiter
}

func (o *RateLimitedLogOutput) Write(data []byte) (int, error) {
	if !o.limiter.Allow() {
		return len(data), nil
	}
	return os.Stderr.Write(data)
}
