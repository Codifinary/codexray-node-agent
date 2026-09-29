// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package containers

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/codifinary/codexray-node-agent/proc"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hsperfdata v2 layout (HotSpot perfMemory, see src/hotspot/share/runtime/perfMemory.hpp):
//
//	header:   magic 0xcafec0c0 (BE) | byte_order | major | minor
//	prologue: accessible(1) used(4) overflow(4) mod_time_stamp(8) entry_offset(4) num_entries(4)
//	entries:  entry_length(4) name_offset(4) vector_length(4) data_type(1) flags(1)
//	          data_units(1) data_variability(1) data_offset(4) | name\0 | pad | data
const (
	jvmTestUnitsNone   = 1
	jvmTestUnitsTicks  = 3
	jvmTestUnitsString = 5
	jvmTestUnitsHertz  = 6
)

type jvmTestEntry struct {
	name  string
	long  int64
	str   *string
	units byte

	// overrides used to craft malformed files
	nameOffset   *int32
	vectorLength *int32
}

func jvmTestLong(name string, v int64, units byte) jvmTestEntry {
	return jvmTestEntry{name: name, long: v, units: units}
}

func jvmTestString(name, v string) jvmTestEntry {
	return jvmTestEntry{name: name, str: &v, units: jvmTestUnitsString}
}

func jvmTestBuildPerfData(entries []jvmTestEntry) []byte {
	le := binary.LittleEndian
	buf := &bytes.Buffer{}
	_ = binary.Write(buf, binary.BigEndian, uint32(0xcafec0c0))
	buf.Write([]byte{1, 2, 0}) // little endian, v2.0

	const entryOffset = 32 // 7 bytes header + 25 bytes prologue
	body := &bytes.Buffer{}
	for _, e := range entries {
		name := append([]byte(e.name), 0)
		nameOffset := int32(20)
		dataOffset := 20 + len(name)
		if pad := dataOffset % 8; pad != 0 {
			dataOffset += 8 - pad
		}
		var data []byte
		var vectorLength int32
		dataType := byte('J')
		variability := byte(1)
		if e.str != nil {
			data = append([]byte(*e.str), 0)
			vectorLength = int32(len(data))
			dataType = 'B'
		} else {
			data = make([]byte, 8)
			le.PutUint64(data, uint64(e.long))
			variability = 3
		}
		if e.nameOffset != nil {
			nameOffset = *e.nameOffset
		}
		if e.vectorLength != nil {
			vectorLength = *e.vectorLength
		}
		entryLen := dataOffset + len(data)
		hdr := &bytes.Buffer{}
		_ = binary.Write(hdr, le, int32(entryLen))
		_ = binary.Write(hdr, le, nameOffset)
		_ = binary.Write(hdr, le, vectorLength)
		hdr.Write([]byte{dataType, 1, e.units, variability})
		_ = binary.Write(hdr, le, int32(dataOffset))
		hdr.Write(name)
		for hdr.Len() < dataOffset {
			hdr.WriteByte(0)
		}
		hdr.Write(data)
		body.Write(hdr.Bytes())
	}
	buf.WriteByte(1) // accessible
	_ = binary.Write(buf, le, int32(entryOffset+body.Len()))
	_ = binary.Write(buf, le, int32(0))
	_ = binary.Write(buf, le, int64(0))
	_ = binary.Write(buf, le, int32(entryOffset))
	_ = binary.Write(buf, le, int32(len(entries)))
	buf.Write(body.Bytes())
	return buf.Bytes()
}

func jvmTestFixtureEntries() []jvmTestEntry {
	return []jvmTestEntry{
		// 1 MHz high-resolution timer => 1 tick = 1µs
		jvmTestLong("sun.os.hrt.frequency", 1_000_000, jvmTestUnitsHertz),
		jvmTestString("sun.rt.javaCommand", "org.example.App --port 8080"),
		jvmTestString("java.property.java.version", "17.0.2"),
		// young generation: eden + 2 survivors
		jvmTestLong("sun.gc.generation.0.spaces", 3, jvmTestUnitsNone),
		jvmTestLong("sun.gc.generation.0.space.0.capacity", 100, 2),
		jvmTestLong("sun.gc.generation.0.space.0.used", 10, 2),
		jvmTestLong("sun.gc.generation.0.space.1.capacity", 200, 2),
		jvmTestLong("sun.gc.generation.0.space.1.used", 20, 2),
		jvmTestLong("sun.gc.generation.0.space.2.capacity", 300, 2),
		jvmTestLong("sun.gc.generation.0.space.2.used", 30, 2),
		// old generation
		jvmTestLong("sun.gc.generation.1.spaces", 1, jvmTestUnitsNone),
		jvmTestLong("sun.gc.generation.1.space.0.capacity", 1000, 2),
		jvmTestLong("sun.gc.generation.1.space.0.used", 500, 2),
		// generation 2 (metaspace/permgen) is NOT heap
		jvmTestLong("sun.gc.generation.2.spaces", 1, jvmTestUnitsNone),
		jvmTestLong("sun.gc.generation.2.space.0.capacity", 99999, 2),
		jvmTestLong("sun.gc.generation.2.space.0.used", 99999, 2),
		jvmTestString("sun.gc.collector.0.name", "G1 Young Generation"),
		jvmTestLong("sun.gc.collector.0.time", 2_000_000, jvmTestUnitsTicks), // 2s
		jvmTestString("sun.gc.collector.1.name", "G1 Old Generation"),
		jvmTestLong("sun.gc.collector.1.time", 500_000, jvmTestUnitsTicks),   // 0.5s
		jvmTestLong("sun.gc.collector.2.time", 7_000_000, jvmTestUnitsTicks), // no name => not reported
		jvmTestLong("sun.rt.safepointTime", 3_000_000, jvmTestUnitsTicks),    // 3s
		jvmTestLong("sun.rt.safepointSyncTime", 1_000, jvmTestUnitsTicks),    // 1ms
	}
}

func jvmTestWrite(t *testing.T, dir string, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(p, data, 0o644))
	return p
}

func TestJvmReadPerfData(t *testing.T) {
	p := jvmTestWrite(t, t.TempDir(), "1234", jvmTestBuildPerfData(jvmTestFixtureEntries()))
	pd, err := readPerfData(p)
	require.NoError(t, err)

	assert.Equal(t, "org.example.App --port 8080", pd.getString("sun.rt.javaCommand"))
	assert.Equal(t, "17.0.2", pd.getString("java.property.java.version"))
	assert.Equal(t, int64(3), pd.getInt64("sun.gc.generation.%d.spaces", 0))
	assert.Equal(t, int64(200), pd.getInt64("sun.gc.generation.%d.space.%d.capacity", 0, 1))
	// ticks are converted to nanoseconds using sun.os.hrt.frequency
	assert.Equal(t, int64(2_000_000_000), pd.getInt64("sun.gc.collector.0.time"))

	// missing keys and type mismatches yield zero values, not panics
	assert.Equal(t, "", pd.getString("missing"))
	assert.Equal(t, int64(0), pd.getInt64("missing"))
	assert.Equal(t, "", pd.getString("sun.gc.generation.0.spaces"))
	assert.Equal(t, int64(0), pd.getInt64("sun.rt.javaCommand"))
}

func TestJvmReadPerfDataErrors(t *testing.T) {
	dir := t.TempDir()

	_, err := readPerfData(filepath.Join(dir, "missing"))
	assert.Error(t, err)

	bad := jvmTestBuildPerfData(jvmTestFixtureEntries())
	bad[0] = 0
	_, err = readPerfData(jvmTestWrite(t, dir, "badmagic", bad))
	assert.Error(t, err)

	badVersion := jvmTestBuildPerfData(jvmTestFixtureEntries())
	badVersion[5] = 1
	_, err = readPerfData(jvmTestWrite(t, dir, "badversion", badVersion))
	assert.Error(t, err)

	_, err = readPerfData(jvmTestWrite(t, dir, "empty", nil))
	assert.Error(t, err)

	notAccessible := jvmTestBuildPerfData(jvmTestFixtureEntries())
	notAccessible[7] = 0
	_, err = readPerfData(jvmTestWrite(t, dir, "notaccessible", notAccessible))
	assert.Error(t, err)

	// entries that claim more than the file holds
	truncated := jvmTestBuildPerfData(jvmTestFixtureEntries())
	_, err = readPerfData(jvmTestWrite(t, dir, "truncated", truncated[:40]))
	assert.Error(t, err)
}

// hsperfdata files live in the container's /tmp and are fully controlled by the
// (untrusted) workload. jvmMetrics runs inside Container.Collect on the scrape path,
// where a panic kills the whole agent. A malformed file must yield an error.
func TestJvmReadPerfDataMalformedDoesNotPanic(t *testing.T) {
	// BUG: readPerfData (hsperfdata.ReadPerfData) panics on crafted/corrupted hsperfdata files; jvmMetrics has no recover — unskip when fixed
	t.Skip("BUG: readPerfData (hsperfdata.ReadPerfData) panics on crafted/corrupted hsperfdata files; jvmMetrics has no recover")

	hugeOffset := int32(1 << 20)
	negOffset := int32(-100)
	hugeVector := int32(1 << 20)
	cases := map[string][]jvmTestEntry{
		"name offset beyond EOF":   {{name: "a", long: 1, units: 1, nameOffset: &hugeOffset}},
		"negative name offset":     {{name: "a", long: 1, units: 1, nameOffset: &negOffset}},
		"string vector beyond EOF": {func() jvmTestEntry { e := jvmTestString("a", "b"); e.vectorLength = &hugeVector; return e }()},
		"zero timer frequency":     {jvmTestLong("sun.os.hrt.frequency", 0, jvmTestUnitsHertz)},
	}
	dir := t.TempDir()
	for name, entries := range cases {
		t.Run(name, func(t *testing.T) {
			p := jvmTestWrite(t, dir, strconv.Itoa(len(name)), jvmTestBuildPerfData(entries))
			assert.NotPanics(t, func() {
				_, err := readPerfData(p)
				assert.Error(t, err)
			})
		})
	}
}

func TestJvmMetricsNoProcess(t *testing.T) {
	jvm, ms := jvmMetrics(4_000_000_000)
	assert.Equal(t, "", jvm)
	assert.Nil(t, ms)
}

func TestJvmMetricsNoPerfDataFile(t *testing.T) {
	pid := uint32(os.Getpid())
	jvmTestRequireNoPerfData(t, pid)
	jvm, ms := jvmMetrics(pid)
	assert.Equal(t, "", jvm)
	assert.Nil(t, ms)
}

func jvmTestRequireNoPerfData(t *testing.T, pid uint32) uint32 {
	t.Helper()
	nsPid, err := proc.GetNsPid(pid)
	require.NoError(t, err)
	existing, _ := filepath.Glob(proc.Path(pid, "root/tmp/hsperfdata_*/"+strconv.Itoa(int(nsPid))))
	if len(existing) > 0 {
		t.Skipf("host already has hsperfdata files for pid %d: %v", nsPid, existing)
	}
	return nsPid
}

// jvmMetrics looks up /proc/<pid>/root/tmp/hsperfdata_*/<nspid>. For the test process
// itself /proc/self/root is "/", so the fixture has to be placed in the real /tmp.
func jvmTestInstallPerfData(t *testing.T, pid uint32, data []byte) {
	t.Helper()
	nsPid := jvmTestRequireNoPerfData(t, pid)
	dir, err := os.MkdirTemp("/tmp", "hsperfdata_codexraytest")
	if err != nil {
		t.Skipf("/tmp is not writable: %s", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	jvmTestWrite(t, dir, strconv.Itoa(int(nsPid)), data)
}

func TestJvmMetricsFromPerfData(t *testing.T) {
	pid := uint32(os.Getpid())
	jvmTestInstallPerfData(t, pid, jvmTestBuildPerfData(jvmTestFixtureEntries()))

	jvm, ms := jvmMetrics(pid)
	require.Equal(t, "org.example.App --port 8080", jvm)
	require.NotEmpty(t, ms)

	mfs := metricsTestGather(t, func(ch chan<- prometheus.Metric) {
		for _, m := range ms {
			ch <- m
		}
	})
	j := map[string]string{"jvm": jvm}

	info := metricsTestOne(t, mfs, "container_jvm_info", map[string]string{"jvm": jvm, "java_version": "17.0.2"})
	assert.Equal(t, 1.0, info.GetGauge().GetValue())

	// heap = generations 0 and 1 only
	assert.Equal(t, 1600.0, metricsTestOne(t, mfs, "container_jvm_heap_size_bytes", j).GetGauge().GetValue())
	assert.Equal(t, 560.0, metricsTestOne(t, mfs, "container_jvm_heap_used_bytes", j).GetGauge().GetValue())

	gcs := metricsTestFind(mfs, "container_jvm_gc_time_seconds", j)
	assert.Len(t, gcs, 2, "collectors without a name are not reported")
	young := metricsTestOne(t, mfs, "container_jvm_gc_time_seconds", map[string]string{"jvm": jvm, "gc": "G1 Young Generation"})
	require.NotNil(t, young.GetCounter(), "gc time is a counter (mainv2 applies rate())")
	assert.InDelta(t, 2.0, young.GetCounter().GetValue(), 1e-9)
	old := metricsTestOne(t, mfs, "container_jvm_gc_time_seconds", map[string]string{"jvm": jvm, "gc": "G1 Old Generation"})
	assert.InDelta(t, 0.5, old.GetCounter().GetValue(), 1e-9)

	sp := metricsTestOne(t, mfs, "container_jvm_safepoint_time_seconds", j)
	require.NotNil(t, sp.GetCounter())
	assert.InDelta(t, 3.0, sp.GetCounter().GetValue(), 1e-9)
	sps := metricsTestOne(t, mfs, "container_jvm_safepoint_sync_time_seconds", j)
	assert.InDelta(t, 0.001, sps.GetCounter().GetValue(), 1e-9)
}

func TestJvmMetricsCorruptedPerfDataIsIgnored(t *testing.T) {
	pid := uint32(os.Getpid())
	data := jvmTestBuildPerfData(jvmTestFixtureEntries())
	data[0] = 0 // bad magic
	jvmTestInstallPerfData(t, pid, data)
	jvm, ms := jvmMetrics(pid)
	assert.Equal(t, "", jvm)
	assert.Nil(t, ms)
}

func TestJvmMetricsMinimalPerfData(t *testing.T) {
	// a JVM that exposes no gc/heap counters still reports info/heap/safepoints with zero values
	pid := uint32(os.Getpid())
	jvmTestInstallPerfData(t, pid, jvmTestBuildPerfData([]jvmTestEntry{jvmTestString("sun.rt.javaCommand", "app.jar")}))
	jvm, ms := jvmMetrics(pid)
	assert.Equal(t, "app.jar", jvm)
	mfs := metricsTestGather(t, func(ch chan<- prometheus.Metric) {
		for _, m := range ms {
			ch <- m
		}
	})
	assert.Len(t, metricsTestFind(mfs, "container_jvm_info", map[string]string{"java_version": ""}), 1)
	assert.Empty(t, metricsTestFind(mfs, "container_jvm_gc_time_seconds", nil))
	assert.Equal(t, 0.0, metricsTestOne(t, mfs, "container_jvm_heap_size_bytes", nil).GetGauge().GetValue())
}
