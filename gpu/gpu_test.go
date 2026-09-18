//go:build gpu
// +build gpu

// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package gpu

import (
	"encoding/binary"
	"math"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NVIDIA/go-nvml/pkg/nvml"
	"github.com/NVIDIA/go-nvml/pkg/nvml/mock"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func gpuTestLE(size int, put func([]byte)) [8]byte {
	var v [8]byte
	put(v[:size])
	return v
}

func TestValueToFloat(t *testing.T) {
	cases := []struct {
		name string
		typ  nvml.ValueType
		raw  [8]byte
		want float64
	}{
		{"double", nvml.VALUE_TYPE_DOUBLE, gpuTestLE(8, func(b []byte) { binary.LittleEndian.PutUint64(b, math.Float64bits(42.5)) }), 42.5},
		{"uint", nvml.VALUE_TYPE_UNSIGNED_INT, gpuTestLE(4, func(b []byte) { binary.LittleEndian.PutUint32(b, 87) }), 87},
		{"ulong", nvml.VALUE_TYPE_UNSIGNED_LONG, gpuTestLE(8, func(b []byte) { binary.LittleEndian.PutUint64(b, 1<<40) }), 1 << 40},
		{"ulonglong", nvml.VALUE_TYPE_UNSIGNED_LONG_LONG, gpuTestLE(8, func(b []byte) { binary.LittleEndian.PutUint64(b, 7) }), 7},
		{"slonglong", nvml.VALUE_TYPE_SIGNED_LONG_LONG, gpuTestLE(8, func(b []byte) { binary.LittleEndian.PutUint64(b, uint64(0xFFFFFFFFFFFFFFFB)) }), -5},
		{"sint", nvml.VALUE_TYPE_SIGNED_INT, gpuTestLE(4, func(b []byte) { binary.LittleEndian.PutUint32(b, 0xFFFFFFFE) }), -2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := valueToFloat(c.typ, c.raw)
			require.NoError(t, err)
			assert.Equal(t, c.want, got)
		})
	}
	_, err := valueToFloat(nvml.ValueType(99), [8]byte{})
	assert.Error(t, err, "unknown value types are rejected, not misread")
}

func TestDescribe(t *testing.T) {
	c := &Collector{}
	ch := make(chan *prometheus.Desc, 16)
	c.Describe(ch)
	close(ch)
	var names []string
	for d := range ch {
		names = append(names, d.String())
	}
	require.Len(t, names, 9)
	for _, want := range []string{
		"node_gpu_info", "node_resources_gpu_memory_total_bytes", "node_resources_gpu_memory_used_bytes",
		"node_resources_gpu_memory_utilization_percent_avg", "node_resources_gpu_memory_utilization_percent_peak",
		"node_resources_gpu_utilization_percent_avg", "node_resources_gpu_utilization_percent_peak",
		"node_resources_gpu_temperature_celsius", "node_resources_gpu_power_usage_watts",
	} {
		assert.True(t, strings.Contains(strings.Join(names, "\n"), `"`+want+`"`), want)
	}
}

var gpuTestFqNameRe = regexp.MustCompile(`fqName: "([^"]+)"`)

// gpuTestCollect scrapes a collector into "name{k=v,...}" -> gauge value.
func gpuTestCollect(t *testing.T, c prometheus.Collector) map[string]float64 {
	t.Helper()
	ch := make(chan prometheus.Metric, 64)
	c.Collect(ch)
	close(ch)
	res := map[string]float64{}
	for m := range ch {
		g := gpuTestFqNameRe.FindStringSubmatch(m.Desc().String())
		require.Len(t, g, 2)
		pb := &dto.Metric{}
		require.NoError(t, m.Write(pb))
		var lbls []string
		for _, lp := range pb.GetLabel() {
			lbls = append(lbls, lp.GetName()+"="+lp.GetValue())
		}
		sort.Strings(lbls)
		res[g[1]+"{"+strings.Join(lbls, ",")+"}"] = pb.GetGauge().GetValue()
	}
	return res
}

func gpuTestUint(v uint32) [8]byte {
	return gpuTestLE(4, func(b []byte) { binary.LittleEndian.PutUint32(b, v) })
}

// gpuTestDevice is an NVML device mock with fixed readings.
func gpuTestDevice(samples map[nvml.SamplingType][]nvml.Sample) *mock.Device {
	return &mock.Device{
		GetMemoryInfoFunc: func() (nvml.Memory, nvml.Return) {
			return nvml.Memory{Total: 16 << 30, Used: 4 << 30, Free: 12 << 30}, nvml.SUCCESS
		},
		GetTemperatureFunc: func(nvml.TemperatureSensors) (uint32, nvml.Return) { return 65, nvml.SUCCESS },
		GetPowerUsageFunc:  func() (uint32, nvml.Return) { return 125500, nvml.SUCCESS }, // milliwatts
		GetSamplesFunc: func(st nvml.SamplingType, lastTs uint64) (nvml.ValueType, []nvml.Sample, nvml.Return) {
			return nvml.VALUE_TYPE_UNSIGNED_INT, samples[st], nvml.SUCCESS
		},
	}
}

func TestCollectWithMockDevice(t *testing.T) {
	dev := &Device{
		UUID:           "GPU-1111",
		Name:           "Tesla T4",
		lastSampleTime: map[nvml.SamplingType]uint64{},
		device: gpuTestDevice(map[nvml.SamplingType][]nvml.Sample{
			nvml.GPU_UTILIZATION_SAMPLES: {
				{TimeStamp: 10, SampleValue: gpuTestUint(20)},
				{TimeStamp: 20, SampleValue: gpuTestUint(80)},
				{TimeStamp: 30, SampleValue: gpuTestUint(50)},
			},
			nvml.MEMORY_UTILIZATION_SAMPLES: {
				{TimeStamp: 10, SampleValue: gpuTestUint(10)},
				{TimeStamp: 20, SampleValue: gpuTestUint(30)},
			},
		}),
	}
	c := &Collector{devices: []*Device{dev}}

	const u = "{gpu_uuid=GPU-1111}"
	assert.Equal(t, map[string]float64{
		"node_gpu_info{gpu_uuid=GPU-1111,name=Tesla T4}":         1,
		"node_resources_gpu_memory_total_bytes" + u:              16 << 30,
		"node_resources_gpu_memory_used_bytes" + u:               4 << 30,
		"node_resources_gpu_temperature_celsius" + u:             65,
		"node_resources_gpu_power_usage_watts" + u:               125.5, // NVML reports milliwatts
		"node_resources_gpu_utilization_percent_avg" + u:         50,
		"node_resources_gpu_utilization_percent_peak" + u:        80,
		"node_resources_gpu_memory_utilization_percent_avg" + u:  20,
		"node_resources_gpu_memory_utilization_percent_peak" + u: 30,
	}, gpuTestCollect(t, c))
	assert.Equal(t, uint64(30), dev.lastSampleTime[nvml.GPU_UTILIZATION_SAMPLES])
	assert.Equal(t, uint64(20), dev.lastSampleTime[nvml.MEMORY_UTILIZATION_SAMPLES])

	// Second scrape: NVML returns the same (already seen) samples => no avg/peak
	// series, since there is nothing new in the interval.
	second := gpuTestCollect(t, c)
	assert.Len(t, second, 5, "info, memory total/used, temperature and power remain")
	assert.NotContains(t, second, "node_resources_gpu_utilization_percent_avg"+u)
}

func TestCollectToleratesNVMLErrors(t *testing.T) {
	dev := &Device{
		UUID:           "GPU-2222",
		Name:           "A100",
		lastSampleTime: map[nvml.SamplingType]uint64{},
		device: &mock.Device{
			GetMemoryInfoFunc:  func() (nvml.Memory, nvml.Return) { return nvml.Memory{}, nvml.ERROR_GPU_IS_LOST },
			GetTemperatureFunc: func(nvml.TemperatureSensors) (uint32, nvml.Return) { return 0, nvml.ERROR_NOT_SUPPORTED },
			GetPowerUsageFunc:  func() (uint32, nvml.Return) { return 0, nvml.ERROR_NOT_SUPPORTED },
			GetSamplesFunc: func(nvml.SamplingType, uint64) (nvml.ValueType, []nvml.Sample, nvml.Return) {
				return 0, nil, nvml.ERROR_NOT_FOUND
			},
		},
	}
	c := &Collector{devices: []*Device{dev}}
	var got map[string]float64
	require.NotPanics(t, func() { got = gpuTestCollect(t, c) })
	assert.Equal(t, map[string]float64{"node_gpu_info{gpu_uuid=GPU-2222,name=A100}": 1}, got,
		"only node_gpu_info when every NVML reading fails")
}

func TestCollectSkipsUnknownSampleValueType(t *testing.T) {
	dev := &Device{
		UUID:           "GPU-3333",
		Name:           "L4",
		lastSampleTime: map[nvml.SamplingType]uint64{},
		device:         gpuTestDevice(nil),
	}
	dev.device.(*mock.Device).GetSamplesFunc = func(nvml.SamplingType, uint64) (nvml.ValueType, []nvml.Sample, nvml.Return) {
		return nvml.ValueType(99), []nvml.Sample{{TimeStamp: 5, SampleValue: gpuTestUint(1)}}, nvml.SUCCESS
	}
	c := &Collector{devices: []*Device{dev}}
	assert.NotContains(t, gpuTestCollect(t, c), "node_resources_gpu_utilization_percent_avg{gpu_uuid=GPU-3333}")
}

func TestFindNvidiaMLLibAndNewCollectorWithoutNVML(t *testing.T) {
	if _, err := findNvidiaMLLib(); err == nil {
		t.Skip("host exposes libnvidia-ml; this test covers the no-NVML path")
	}
	c, err := NewCollector()
	require.NoError(t, err, "a node without NVIDIA drivers must not fail startup")
	require.NotNil(t, c)
	assert.Equal(t, 100, cap(c.ProcessUsageSampleCh))
	assert.Empty(t, c.devices)
	assert.Empty(t, gpuTestCollect(t, c))
}

func TestCloseWithoutNVML(t *testing.T) {
	// BUG: Collector.Close dereferences c.iface, which is nil when libnvidia-ml
	// was not found (NewCollector returns a usable collector) — unskip when fixed
	t.Skip("BUG: gpu.Collector.Close panics (nil iface) when NVML was not found")
	c := &Collector{ProcessUsageSampleCh: make(chan ProcessUsageSample, 100)}
	assert.NotPanics(t, c.Close)
}

func TestCloseShutsDownNVML(t *testing.T) {
	var calls int32
	c := &Collector{iface: &mock.Interface{ShutdownFunc: func() nvml.Return {
		atomic.AddInt32(&calls, 1)
		return nvml.SUCCESS
	}}}
	c.Close()
	assert.EqualValues(t, 1, atomic.LoadInt32(&calls))
}

// gpuTestPollerDevice returns one fresh process sample per call.
func gpuTestPollerDevice(ts uint64, pid uint32) *mock.Device {
	return &mock.Device{
		GetProcessUtilizationFunc: func(lastTs uint64) ([]nvml.ProcessUtilizationSample, nvml.Return) {
			return []nvml.ProcessUtilizationSample{{Pid: pid, TimeStamp: ts, SmUtil: 40, MemUtil: 10}}, nvml.SUCCESS
		},
	}
}

func TestProcessUtilizationPollerPerDeviceTimestamps(t *testing.T) {
	// BUG: processUtilizationPoller keeps ONE lastTs for all devices, so a
	// device whose newest sample is older than another device's newest sample
	// is dropped (per-process GPU usage lost on multi-GPU nodes) — unskip when fixed.
	// The poller also has no exit path (ticker loop never stops), which is why
	// this test is skip-guarded rather than run and leaked.
	t.Skip("BUG: processUtilizationPoller shares one lastTs across devices and never exits")
	now := uint64(time.Now().Add(time.Hour).UnixMicro())
	c := &Collector{
		ProcessUsageSampleCh: make(chan ProcessUsageSample, 100),
		devices: []*Device{
			{UUID: "GPU-A", device: gpuTestPollerDevice(now+100, 1)},
			{UUID: "GPU-B", device: gpuTestPollerDevice(now+50, 2)},
		},
	}
	go c.processUtilizationPoller()
	seen := map[string]bool{}
	deadline := time.After(1500 * time.Millisecond)
	for len(seen) < 2 {
		select {
		case s := <-c.ProcessUsageSampleCh:
			seen[s.UUID] = true
			assert.EqualValues(t, 40, s.GPUPercent)
			assert.EqualValues(t, 10, s.MemoryPercent)
		case <-deadline:
			assert.Equal(t, map[string]bool{"GPU-A": true, "GPU-B": true}, seen)
			return
		}
	}
}
