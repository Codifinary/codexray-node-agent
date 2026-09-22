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
	_ "net/http/pprof"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/codifinary/codexray-node-agent/common"
	"github.com/codifinary/codexray-node-agent/containers"
	"github.com/codifinary/codexray-node-agent/dogstatsd"
	"github.com/codifinary/codexray-node-agent/flags"
	"github.com/codifinary/codexray-node-agent/gpu"
	"github.com/codifinary/codexray-node-agent/logs"
	"github.com/codifinary/codexray-node-agent/node"
	"github.com/codifinary/codexray-node-agent/proc"
	"github.com/codifinary/codexray-node-agent/profiling"
	"github.com/codifinary/codexray-node-agent/prom"
	"github.com/codifinary/codexray-node-agent/tracing"
	"github.com/prometheus/client_golang/prometheus"
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

	tracing.Init(machineId, hostname, version)
	logs.Init(machineId, hostname, version)

	nodeCollector := node.NewCollector(hostname, kv)

	registry := prometheus.NewRegistry()

	registerer := prometheus.WrapRegistererWith(
		prometheus.Labels{"machine_id": machineId, "system_uuid": systemUuid},
		registry,
	)
	if err := registerer.Register(nodeCollector); err != nil {
		klog.Exitln(err)
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
	processInfoCh := profiling.Init(machineId, hostname)
	cr, err := containers.NewRegistry(registerer, processInfoCh, gpuCollector.ProcessUsageSampleCh)
	if err != nil {
		klog.Exitln(err)
	}
	defer cr.Close()

	profiling.Start()
	defer profiling.Stop()

	var seriesSources []prom.SeriesSource
	var statsDPipeline *dogstatsd.Pipeline
	var statsDReceiver *dogstatsd.Receiver
	if *flags.StatsDEnabled {
		if *flags.MetricsEndpoint == nil {
			klog.Exitln("StatsD requires --collector-endpoint or --metrics-endpoint")
		}
		timerBuckets, parseErr := dogstatsd.ParseBuckets(*flags.StatsDTimerBucketsMS)
		if parseErr != nil {
			klog.Exitln("failed to parse StatsD timer buckets:", parseErr)
		}
		histogramBuckets, parseErr := dogstatsd.ParseBuckets(*flags.StatsDHistogramBuckets)
		if parseErr != nil {
			klog.Exitln("failed to parse StatsD histogram buckets:", parseErr)
		}
		aggregator, aggregatorErr := dogstatsd.NewAggregator(dogstatsd.AggregationConfig{
			TimerBucketsMS:        timerBuckets,
			HistogramBuckets:      histogramBuckets,
			MaxSetValuesPerSeries: *flags.StatsDSetMaxValuesPerSeries,
			MaxBytes:              int64(*flags.StatsDAggregationMaxBytes),
			GaugeTTL:              *flags.StatsDActiveSeriesTTL,
		})
		if aggregatorErr != nil {
			klog.Exitln("failed to configure StatsD aggregation:", aggregatorErr)
		}
		customSpoolDir := *flags.StatsDCustomSpoolDir
		if customSpoolDir == "" {
			customSpoolDir = filepath.Join(*flags.WalDir, "custom-metrics-spool")
		}
		customSpool, spoolErr := dogstatsd.OpenCustomSpoolWithPolicy(customSpoolDir, int64(*flags.StatsDCustomSpoolMaxBytes), *flags.StatsDCustomSpoolMaxAge, int64(*flags.StatsDQuarantineMaxBytes))
		if spoolErr != nil {
			klog.Exitln("failed to configure StatsD custom spool:", spoolErr)
		}
		customSender, senderErr := dogstatsd.NewCustomSender(&http.Client{
			Timeout:   30 * time.Second,
			Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: *flags.InsecureSkipVerify}},
		}, (*flags.MetricsEndpoint).String(), common.AuthHeaders())
		if senderErr != nil {
			klog.Exitln("failed to configure StatsD custom sender:", senderErr)
		}
		pipeline, pipelineErr := dogstatsd.NewPipeline(dogstatsd.PipelineConfig{
			FlushInterval:       *flags.StatsDFlushInterval,
			MaxBatchBytes:       int(*flags.StatsDMaxBatchBytes),
			ExternalLabels:      prom.SourceLabels(machineId, systemUuid),
			RetryMin:            5 * time.Second,
			RetryMax:            time.Minute,
			SaturationThreshold: *flags.StatsDSaturationThreshold,
			SaturationDuration:  *flags.StatsDSaturationDuration,
			Registerer:          registerer,
		}, aggregator, customSpool, customSender)
		if pipelineErr != nil {
			klog.Exitln("failed to configure StatsD custom pipeline:", pipelineErr)
		}
		pipeline.Start()
		defer pipeline.Close()
		statsDPipeline = pipeline

		receiver, receiverErr := dogstatsd.NewProduction(dogstatsd.Config{
			ListenAddr:               *flags.StatsDListen,
			MaxPacketBytes:           *flags.StatsDMaxPacketBytes,
			PacketQueueSize:          *flags.StatsDPacketQueueSize,
			PacketQueueMaxBytes:      int64(*flags.StatsDPacketQueueMaxBytes),
			ParseWorkers:             *flags.StatsDParseWorkers,
			BufferMaxEvents:          *flags.StatsDBufferMaxEvents,
			BatchMaxSeries:           *flags.StatsDBatchMaxSeries,
			MaxMetricNameLength:      *flags.StatsDMaxMetricNameLength,
			MaxTagsPerMetric:         *flags.StatsDMaxTagsPerMetric,
			MaxTagKeyLength:          *flags.StatsDMaxTagKeyLength,
			MaxTagValueLength:        *flags.StatsDMaxTagValueLength,
			ActiveSeriesPerMetricCap: *flags.StatsDActiveSeriesPerMetricCap,
			ActiveSeriesGlobalCap:    *flags.StatsDActiveSeriesGlobalCap,
			ActiveSeriesTTL:          *flags.StatsDActiveSeriesTTL,
			TagKeyBlocklist:          *flags.StatsDTagKeyBlocklist,
			AllowedSourceCIDRs:       *flags.StatsDAllowedSourceCIDRs,
			MaxBytesPerSecond:        *flags.StatsDMaxBytesPerSecond,
			MaxEventsPerSecond:       *flags.StatsDMaxEventsPerSecond,
			SaturationThreshold:      *flags.StatsDSaturationThreshold,
			SaturationDuration:       *flags.StatsDSaturationDuration,
			ShutdownDrainTimeout:     *flags.StatsDShutdownDrainTimeout,
		}, registerer, aggregator)
		if receiverErr != nil {
			klog.Exitln("failed to configure StatsD receiver:", receiverErr)
		}
		if receiverErr = receiver.Start(); receiverErr != nil {
			klog.Exitln("failed to start StatsD receiver:", receiverErr)
		}
		defer receiver.Close()
		statsDReceiver = receiver
	}

	if err := prom.StartAgent(registry, machineId, systemUuid, seriesSources...); err != nil {
		klog.Exitln(err)
	}

	http.Handle("/metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{ErrorLog: logger{}, Registry: registerer}))
	http.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	http.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		if *flags.StatsDEnabled && (statsDReceiver == nil || !statsDReceiver.Ready() || statsDPipeline == nil || !statsDPipeline.Healthy()) {
			http.Error(w, "statsd pipeline not ready", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready\n"))
	})
	server := &http.Server{
		Addr:              *flags.ListenAddress,
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
	if statsDReceiver != nil {
		if err := statsDReceiver.Close(); err != nil {
			klog.Warningln("StatsD receiver shutdown did not drain cleanly:", err)
		}
	}
	if statsDPipeline != nil {
		if err := statsDPipeline.Close(); err != nil {
			klog.Warningln("StatsD final spool flush failed:", err)
		}
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
