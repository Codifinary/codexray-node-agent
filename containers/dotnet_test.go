// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package containers

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/codifinary/codexray-node-agent/proc"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/pyroscope-io/dotnetdiag/nettrace"
	"github.com/pyroscope-io/dotnetdiag/nettrace/typecode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// dotnetTestMonitor builds a monitor whose background connection loop exits immediately.
func dotnetTestMonitor(t *testing.T, app string) *DotNetMonitor {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return NewDotNetMonitor(ctx, 4_000_000_000, app)
}

func dotnetTestGather(t *testing.T, m *DotNetMonitor) []*dto.MetricFamily {
	t.Helper()
	return metricsTestGather(t, m.Collect)
}

// payload encoders for EventPipe (nettrace) field values: little-endian, strings as
// null-terminated UTF-16LE.
type dotnetTestPayload struct{ bytes.Buffer }

func (p *dotnetTestPayload) str(s string) *dotnetTestPayload {
	for _, u := range utf16.Encode([]rune(s)) {
		_ = binary.Write(&p.Buffer, binary.LittleEndian, u)
	}
	_ = binary.Write(&p.Buffer, binary.LittleEndian, uint16(0))
	return p
}

func (p *dotnetTestPayload) f64(v float64) *dotnetTestPayload {
	_ = binary.Write(&p.Buffer, binary.LittleEndian, v)
	return p
}

func (p *dotnetTestPayload) f32(v float32) *dotnetTestPayload {
	_ = binary.Write(&p.Buffer, binary.LittleEndian, v)
	return p
}

func (p *dotnetTestPayload) i32(v int32) *dotnetTestPayload {
	_ = binary.Write(&p.Buffer, binary.LittleEndian, v)
	return p
}

func dotnetTestField(name string, tc typecode.TypeCode, nested ...nettrace.MetadataField) nettrace.MetadataField {
	return nettrace.MetadataField{Name: name, TypeCode: tc, Payload: nettrace.MetadataPayload{Fields: nested}}
}

func dotnetTestNewMetric() *dotNetMetric {
	return &dotNetMetric{fields: map[string]string{}, values: map[string]float64{}}
}

func TestDotNetMetricValue(t *testing.T) {
	cases := []struct {
		name        string
		counterType string
		values      map[string]float64
		want        float64
	}{
		{"sum uses increment", "Sum", map[string]float64{"Increment": 7, "Mean": 1}, 7},
		{"mean uses mean", "Mean", map[string]float64{"Increment": 7, "Mean": 1.5}, 1.5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := &dotNetMetric{fields: map[string]string{"CounterType": tc.counterType, "Name": "n", "DisplayUnits": "MB"}, values: tc.values}
			assert.Equal(t, tc.want, m.value())
			assert.Equal(t, "n", m.name())
			assert.Equal(t, "MB", m.units())
		})
	}
	for _, ct := range []string{"", "Unknown"} {
		m := &dotNetMetric{fields: map[string]string{"CounterType": ct}, values: map[string]float64{"Increment": 1, "Mean": 1}}
		assert.True(t, math.IsNaN(m.value()), "counter type %q", ct)
	}
}

func TestDotNetParseFieldsEventCounterPayload(t *testing.T) {
	// System.Runtime EventCounters payload: {Payload: {Name, DisplayName, Mean, StandardDeviation,
	// Count, Min, Max, IntervalSec, Series, CounterType, Metadata, DisplayUnits}}
	fields := []nettrace.MetadataField{
		dotnetTestField("Payload", typecode.Object,
			dotnetTestField("Name", typecode.String),
			dotnetTestField("DisplayName", typecode.String),
			dotnetTestField("Mean", typecode.Double),
			dotnetTestField("Count", typecode.Int32),
			dotnetTestField("IntervalSec", typecode.Single),
			dotnetTestField("CounterType", typecode.String),
			dotnetTestField("DisplayUnits", typecode.String),
		),
	}
	p := (&dotnetTestPayload{}).
		str("threadpool-thread-count").
		str("ThreadPool Thread Count").
		f64(12).
		i32(1).
		f32(5).
		str("Mean").
		str("")
	m := dotnetTestNewMetric()
	require.NoError(t, parseFields(fields, nettrace.Parser{Buffer: &p.Buffer}, m))
	assert.Equal(t, "threadpool-thread-count", m.name())
	assert.Equal(t, "", m.units())
	assert.Equal(t, 12.0, m.value())
	assert.Equal(t, 1.0, m.values["Count"])
	assert.Equal(t, 5.0, m.values["IntervalSec"])
	assert.Equal(t, "ThreadPool Thread Count", m.fields["DisplayName"])
}

func TestDotNetParseFieldsNonASCIIString(t *testing.T) {
	fields := []nettrace.MetadataField{dotnetTestField("DisplayName", typecode.String), dotnetTestField("Mean", typecode.Double)}
	p := (&dotnetTestPayload{}).str("計数").f64(3)
	m := dotnetTestNewMetric()
	require.NoError(t, parseFields(fields, nettrace.Parser{Buffer: &p.Buffer}, m))
	assert.Equal(t, "計数", m.fields["DisplayName"])
	assert.Equal(t, 3.0, m.values["Mean"])
}

func TestDotNetParseFieldsErrors(t *testing.T) {
	t.Run("unsupported type", func(t *testing.T) {
		p := (&dotnetTestPayload{}).i32(1)
		err := parseFields([]nettrace.MetadataField{dotnetTestField("x", typecode.Int64)}, nettrace.Parser{Buffer: &p.Buffer}, dotnetTestNewMetric())
		assert.Error(t, err)
	})
	t.Run("unsupported type nested", func(t *testing.T) {
		p := (&dotnetTestPayload{}).i32(1)
		fields := []nettrace.MetadataField{dotnetTestField("Payload", typecode.Object, dotnetTestField("x", typecode.Boolean))}
		assert.Error(t, parseFields(fields, nettrace.Parser{Buffer: &p.Buffer}, dotnetTestNewMetric()))
	})
	for _, tc := range []typecode.TypeCode{typecode.Double, typecode.Single, typecode.Int32} {
		t.Run(fmt.Sprintf("truncated %d", tc), func(t *testing.T) {
			p := &dotnetTestPayload{}
			p.WriteByte(1) // shorter than any numeric type
			m := dotnetTestNewMetric()
			assert.Error(t, parseFields([]nettrace.MetadataField{dotnetTestField("v", tc)}, nettrace.Parser{Buffer: &p.Buffer}, m))
			assert.NotContains(t, m.values, "v")
		})
	}
	t.Run("empty payload", func(t *testing.T) {
		p := &dotnetTestPayload{}
		assert.NotPanics(t, func() {
			err := parseFields([]nettrace.MetadataField{dotnetTestField("Name", typecode.String), dotnetTestField("Mean", typecode.Double)}, nettrace.Parser{Buffer: &p.Buffer}, dotnetTestNewMetric())
			assert.Error(t, err)
		})
	})
	t.Run("no fields", func(t *testing.T) {
		p := &dotnetTestPayload{}
		assert.NoError(t, parseFields(nil, nettrace.Parser{Buffer: &p.Buffer}, dotnetTestNewMetric()))
	})
}

func TestDotNetMonitorCollectRequiresFreshData(t *testing.T) {
	m := dotnetTestMonitor(t, "Accounting")
	assert.Equal(t, "Accounting", m.AppName())
	assert.Empty(t, dotnetTestGather(t, m), "nothing is reported before the first event")

	m.processMetric("threadpool-thread-count", "", 4)
	assert.NotEmpty(t, dotnetTestGather(t, m))

	m.lastUpdate = time.Now().Add(-3 * dotNetEventInterval)
	assert.Empty(t, dotnetTestGather(t, m), "stale data (no events for 2 intervals) is not reported")
}

func TestDotNetMonitorProcessMetricMapping(t *testing.T) {
	type sample struct {
		name  string
		units string
		v     float64
	}
	m := dotnetTestMonitor(t, "Accounting")
	m.info.WithLabelValues("8.0.1").Set(1)
	for _, s := range []sample{
		{"alloc-rate", "B", 100},
		{"alloc-rate", "B", 50},
		{"gen-0-gc-count", "", 3},
		{"gen-0-gc-count", "", 2},
		{"gen-1-gc-count", "", 1},
		{"gen-2-gc-count", "", 1},
		{"gen-0-size", "B", 10},
		{"gen-1-size", "B", 20},
		{"gen-2-size", "B", 30},
		{"loh-size", "B", 40},
		{"poh-size", "MB", 0.5}, // MB => bytes
		{"gc-fragmentation", "%", 12.5},
		{"monitor-lock-contention-count", "", 2},
		{"monitor-lock-contention-count", "", 3},
		{"threadpool-completed-items-count", "", 10},
		{"threadpool-queue-length", "", 7},
		{"threadpool-thread-count", "", 9},
		{"threadpool-thread-count", "", 8},
		{"cpu-usage", "%", 50},            // not mapped: ignored
		{"working-set", "MB", math.NaN()}, // NaN: ignored
	} {
		m.processMetric(s.name, s.units, s.v)
	}
	mfs := dotnetTestGather(t, m)
	app := map[string]string{"application": "Accounting"}
	withApp := func(extra ...string) map[string]string {
		res := map[string]string{"application": "Accounting"}
		for i := 0; i+1 < len(extra); i += 2 {
			res[extra[i]] = extra[i+1]
		}
		return res
	}
	counterVal := func(name string, labels map[string]string) float64 {
		m := metricsTestOne(t, mfs, name, labels)
		require.NotNil(t, m.GetCounter(), "%s must be a counter (mainv2 applies rate())", name)
		return m.GetCounter().GetValue()
	}
	gaugeVal := func(name string, labels map[string]string) float64 {
		m := metricsTestOne(t, mfs, name, labels)
		require.NotNil(t, m.GetGauge(), "%s must be a gauge", name)
		return m.GetGauge().GetValue()
	}

	assert.Equal(t, 1.0, gaugeVal("container_dotnet_info", withApp("runtime_version", "8.0.1")))
	assert.Equal(t, 150.0, counterVal("container_dotnet_memory_allocated_bytes_total", app))
	assert.Equal(t, 5.0, counterVal("container_dotnet_gc_count_total", withApp("generation", "Gen0")))
	assert.Equal(t, 1.0, counterVal("container_dotnet_gc_count_total", withApp("generation", "Gen1")))
	assert.Equal(t, 1.0, counterVal("container_dotnet_gc_count_total", withApp("generation", "Gen2")))
	assert.Equal(t, 10.0, gaugeVal("container_dotnet_memory_heap_size_bytes", withApp("generation", "Gen0")))
	assert.Equal(t, 20.0, gaugeVal("container_dotnet_memory_heap_size_bytes", withApp("generation", "Gen1")))
	assert.Equal(t, 30.0, gaugeVal("container_dotnet_memory_heap_size_bytes", withApp("generation", "Gen2")))
	assert.Equal(t, 40.0, gaugeVal("container_dotnet_memory_heap_size_bytes", withApp("generation", "LOH")))
	assert.Equal(t, 500000.0, gaugeVal("container_dotnet_memory_heap_size_bytes", withApp("generation", "POH")))
	assert.Equal(t, 12.5, gaugeVal("container_dotnet_heap_fragmentation_percent", app))
	assert.Equal(t, 5.0, counterVal("container_dotnet_monitor_lock_contentions_total", app))
	assert.Equal(t, 10.0, counterVal("container_dotnet_thread_pool_completed_items_total", app))
	assert.Equal(t, 7.0, gaugeVal("container_dotnet_thread_pool_queue_length", app))
	assert.Equal(t, 8.0, gaugeVal("container_dotnet_thread_pool_size", app))

	// every family carries the application const label mainv2 groups by
	for _, mf := range mfs {
		for _, s := range mf.GetMetric() {
			assert.Equal(t, "Accounting", metricsTestLabels(s)["application"], mf.GetName())
		}
	}
	// only the names mainv2 queries (constructor/queries.go) are exported
	assert.ElementsMatch(t, []string{
		"container_dotnet_info", "container_dotnet_memory_allocated_bytes_total", "container_dotnet_exceptions_total",
		"container_dotnet_memory_heap_size_bytes", "container_dotnet_gc_count_total", "container_dotnet_heap_fragmentation_percent",
		"container_dotnet_monitor_lock_contentions_total", "container_dotnet_thread_pool_completed_items_total",
		"container_dotnet_thread_pool_queue_length", "container_dotnet_thread_pool_size",
	}, metricsTestNames(mfs))
}

func TestDotNetMonitorNaNOnlyRefreshesLastUpdate(t *testing.T) {
	m := dotnetTestMonitor(t, "app")
	m.processMetric("threadpool-queue-length", "", math.NaN())
	assert.False(t, m.lastUpdate.IsZero())
	mfs := dotnetTestGather(t, m)
	assert.Equal(t, 0.0, metricsTestOne(t, mfs, "container_dotnet_thread_pool_queue_length", nil).GetGauge().GetValue())
}

// "exception-count" is a Sum EventCounter: each event carries the number of exceptions
// thrown during the last interval (Increment). mainv2 queries
// rate(container_dotnet_exceptions_total[...]), so the agent must accumulate it into a
// monotonic counter; Set()-ing a gauge to the per-interval increment makes rate() produce
// garbage (every drop is treated as a counter reset).
func TestDotNetMonitorExceptionCountIsCounter(t *testing.T) {
	// BUG: exception-count (a per-interval Sum) is exported via Gauge.Set as container_dotnet_exceptions_total instead of accumulated into a counter — unskip when fixed
	t.Skip("BUG: exception-count (a per-interval Sum) is exported via Gauge.Set as container_dotnet_exceptions_total instead of accumulated into a counter")

	m := dotnetTestMonitor(t, "app")
	m.processMetric("exception-count", "", 3)
	m.processMetric("exception-count", "", 2)
	mfs := dotnetTestGather(t, m)
	e := metricsTestOne(t, mfs, "container_dotnet_exceptions_total", nil)
	require.NotNil(t, e.GetCounter())
	assert.Equal(t, 5.0, e.GetCounter().GetValue())
}

// processMetric runs on the monitor's own goroutine (the diagnostic stream reader) while
// Collect runs on the Prometheus scrape goroutine; both touch lastUpdate without a lock.
func TestDotNetMonitorProcessMetricCollectRace(t *testing.T) {
	// BUG: DotNetMonitor.lastUpdate is written by processMetric (stream goroutine) and read by Collect (scrape goroutine) without synchronization — unskip when fixed
	t.Skip("BUG: DotNetMonitor.lastUpdate is written by processMetric (stream goroutine) and read by Collect (scrape goroutine) without synchronization")

	m := dotnetTestMonitor(t, "app")
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			m.processMetric("threadpool-thread-count", "", float64(i))
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			ch := make(chan prometheus.Metric, 100)
			m.Collect(ch)
		}
	}()
	wg.Wait()
}

func TestDotNetApp(t *testing.T) {
	cases := []struct {
		name    string
		cmdline string
		want    string
	}{
		{"framework-dependent", "dotnet\x00Accounting.dll", "Accounting"},
		{"full dotnet path with args", "/usr/share/dotnet/dotnet\x00Api.dll\x00--urls\x00http://*:80\x00", "Api"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// the pid is irrelevant for the `dotnet X.dll` form: no /proc access is needed
			app, err := dotNetApp([]byte(tc.cmdline), 4_000_000_000)
			require.NoError(t, err)
			assert.Equal(t, tc.want, app)
		})
	}
}

func TestDotNetAppNotDotnet(t *testing.T) {
	// not `dotnet X.dll`: falls back to the ELF RPATH/RUNPATH of /proc/<pid>/exe
	_, err := dotNetApp([]byte("java\x00-jar\x00app.jar"), 4_000_000_000)
	assert.Error(t, err, "vanished process")

	for _, cmdline := range []string{"", "dotnet", "dotnet\x00app.exe", "node\x00Accounting.dll"} {
		app, err := dotNetApp([]byte(cmdline), uint32(os.Getpid())) // the Go test binary has no $ORIGIN/netcoredeps rpath
		assert.NoError(t, err, "%q", cmdline)
		assert.Equal(t, "", app, "%q", cmdline)
	}
}

func TestDotNetMonitorConnectNoProcess(t *testing.T) {
	m := dotnetTestMonitor(t, "app")
	assert.Error(t, m.connect(context.Background()))
}

func TestDotNetMonitorConnectNoSocket(t *testing.T) {
	m := dotnetTestMonitor(t, "app")
	m.pid = uint32(os.Getpid())
	nsPid, err := proc.GetNsPid(m.pid)
	require.NoError(t, err)
	if existing, _ := filepath.Glob(fmt.Sprintf("/tmp/dotnet-diagnostic-%d-*-socket", nsPid)); len(existing) > 0 {
		t.Skipf("host already has diagnostic sockets for pid %d", nsPid)
	}
	err = m.connect(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no socket found")
}

// A diagnostic socket that accepts and immediately drops connections (a runtime that is
// shutting down): connect must fail cleanly and fall back to runtime_version="unknown".
func TestDotNetMonitorConnectBrokenServer(t *testing.T) {
	m := dotnetTestMonitor(t, "app")
	m.pid = uint32(os.Getpid())
	nsPid, err := proc.GetNsPid(m.pid)
	require.NoError(t, err)
	if existing, _ := filepath.Glob(fmt.Sprintf("/tmp/dotnet-diagnostic-%d-*-socket", nsPid)); len(existing) > 0 {
		t.Skipf("host already has diagnostic sockets for pid %d", nsPid)
	}
	sock := fmt.Sprintf("/tmp/dotnet-diagnostic-%d-%d-socket", nsPid, time.Now().UnixNano())
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("cannot listen on %s: %s", sock, err)
	}
	t.Cleanup(func() { _ = l.Close(); _ = os.Remove(sock) })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()

	assert.Error(t, m.connect(context.Background()))
	m.lastUpdate = time.Now()
	mfs := dotnetTestGather(t, m)
	assert.Equal(t, 1.0, metricsTestOne(t, mfs, "container_dotnet_info", map[string]string{"runtime_version": "unknown"}).GetGauge().GetValue())
}
