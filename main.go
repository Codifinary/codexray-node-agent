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

	http.Handle("/metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{ErrorLog: logger{}, Registry: registerer}))
	http.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	http.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		if *flags.DogStatsDEnabled && (dogStatsDReceiver == nil || !dogStatsDReceiver.Ready() || dogStatsDPipeline == nil || !dogStatsDPipeline.Healthy()) {
			http.Error(w, "dogstatsd pipeline not ready", http.StatusServiceUnavailable)
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
