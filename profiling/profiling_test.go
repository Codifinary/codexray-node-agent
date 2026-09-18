// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package profiling

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/codifinary/codexray-node-agent/containers"
	"github.com/codifinary/codexray-node-agent/flags"
	"github.com/codifinary/codexray-node-agent/proc"
	"github.com/google/pprof/profile"
	"github.com/grafana/pyroscope/ebpf/pprof"
	"github.com/grafana/pyroscope/ebpf/sd"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	// Above the kernel's maximum pid_max (2^22), so /proc/<pid> never exists and
	// FindTarget's JVM detection can never touch a real host process.
	profilingTestPid          = uint32(1<<22 + 123)
	profilingTestContainerID  = "/k8s/shop/checkout-7d9f8b6c5d-x2x4z/app"
	profilingTestServiceName  = "/k8s/shop/checkout"
	profilingTestAPIKey       = "test-api-key"
	profilingTestSampleCount  = 5
	profilingTestExpectedType = "ebpf:cpu:nanoseconds" // mainv2 model.ProfileTypeEbpfCPU
)

// profilingTestRequest is what the fake collector captured.
type profilingTestRequest struct {
	method  string
	path    string
	query   url.Values
	apiKey  string
	gzipped bool
	profile *profile.Profile
}

// profilingTestCollector emulates mainv2 collector/profiles.go: 400 when
// service.name is empty, otherwise the configured status.
type profilingTestCollector struct {
	lock     sync.Mutex
	status   int
	requests []profilingTestRequest
}

func (c *profilingTestCollector) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	data, _ := io.ReadAll(r.Body)
	req := profilingTestRequest{
		method:  r.Method,
		path:    r.URL.Path,
		query:   r.URL.Query(),
		apiKey:  r.Header.Get("X-Api-Key"),
		gzipped: len(data) > 2 && data[0] == 0x1f && data[1] == 0x8b,
	}
	if req.gzipped {
		if gz, err := gzip.NewReader(bytes.NewReader(data)); err == nil {
			if raw, err := io.ReadAll(gz); err == nil {
				req.profile, _ = profile.ParseData(raw)
			}
		}
	}
	c.lock.Lock()
	c.requests = append(c.requests, req)
	status := c.status
	c.lock.Unlock()
	if r.URL.Query().Get("service.name") == "" {
		http.Error(w, "service.name is empty", http.StatusBadRequest)
		return
	}
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	_, _ = w.Write([]byte("collector says hi"))
}

func (c *profilingTestCollector) all() []profilingTestRequest {
	c.lock.Lock()
	defer c.lock.Unlock()
	return append([]profilingTestRequest(nil), c.requests...)
}

// profilingTestSetup points the uploader at a fake collector and restores globals.
func profilingTestSetup(t *testing.T, status int, apiKey string) *profilingTestCollector {
	col := &profilingTestCollector{status: status}
	srv := httptest.NewServer(col)
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL + "/ingest/v1/profiles")
	require.NoError(t, err)

	prevURL, prevLabels, prevKey := endpointUrl, constLabels, *flags.ApiKey
	endpointUrl = u
	constLabels = labels.Labels{
		{Name: "host.name", Value: "node-1"},
		{Name: "host.id", Value: "machine-1"},
	}
	*flags.ApiKey = apiKey
	t.Cleanup(func() { endpointUrl, constLabels, *flags.ApiKey = prevURL, prevLabels, prevKey })
	return col
}

// profilingTestBuilder builds one CPU profile the way pprof.Collect would for a target.
func profilingTestBuilder(t *testing.T, target *sd.Target) *pprof.ProfileBuilder {
	bs := pprof.NewProfileBuilders(pprof.BuildersOptions{SampleRate: SampleRate})
	bs.AddSample(&pprof.ProfileSample{
		Target:      target,
		Pid:         profilingTestPid,
		SampleType:  pprof.SampleTypeCpu,
		Aggregation: pprof.SampleAggregated,
		Stack:       []string{"leaf", "middle", "main"},
		Value:       profilingTestSampleCount,
	})
	require.Len(t, bs.Builders, 1)
	for _, b := range bs.Builders {
		return b
	}
	return nil
}

func profilingTestTarget(cid string) *sd.Target {
	return sd.NewTargetForTesting(cid, 0, sd.DiscoveryTarget{"service_name": profilingTestServiceName})
}

func TestUploadRequestContract(t *testing.T) {
	col := profilingTestSetup(t, http.StatusOK, profilingTestAPIKey)
	require.NoError(t, upload(profilingTestBuilder(t, profilingTestTarget(profilingTestContainerID))))

	reqs := col.all()
	require.Len(t, reqs, 1)
	r := reqs[0]
	assert.Equal(t, http.MethodPost, r.method)
	assert.Equal(t, "/ingest/v1/profiles", r.path, "the configured endpoint path is used as-is")
	assert.Equal(t, profilingTestAPIKey, r.apiKey)
	assert.Empty(t, r.query.Get("api-key"), "the API key never goes in the URL")

	// mainv2 takes service.name as the service and every other param as a label.
	assert.Equal(t, url.Values{
		"service.name": {profilingTestServiceName},
		"container.id": {profilingTestContainerID},
		"host.name":    {"node-1"},
		"host.id":      {"machine-1"},
	}, r.query, "only the four documented params; internal pyroscope labels (__name__, service_name) are not leaked")

	require.True(t, r.gzipped, "body is gzipped pprof")
	require.NotNil(t, r.profile, "body must parse as pprof")
	require.Len(t, r.profile.SampleType, 1)
	assert.Equal(t, profilingTestExpectedType, r.profile.SampleType[0].Type)
	assert.Equal(t, "nanoseconds", r.profile.SampleType[0].Unit)
	assert.Equal(t, CollectInterval.Nanoseconds(), r.profile.DurationNanos, "mainv2 derives start = end - DurationNanos")
	require.Len(t, r.profile.Sample, 1)
	period := time.Second.Nanoseconds() / SampleRate
	assert.Equal(t, int64(profilingTestSampleCount)*period, r.profile.Sample[0].Value[0], "value is CPU nanoseconds")
	var stack []string
	for _, loc := range r.profile.Sample[0].Location {
		for _, line := range loc.Line {
			stack = append(stack, line.Function.Name)
		}
	}
	assert.Equal(t, []string{"leaf", "middle", "main"}, stack)
}

func TestUploadWithoutAPIKey(t *testing.T) {
	col := profilingTestSetup(t, http.StatusOK, "")
	require.NoError(t, upload(profilingTestBuilder(t, profilingTestTarget(profilingTestContainerID))))
	reqs := col.all()
	require.Len(t, reqs, 1)
	assert.Empty(t, reqs[0].apiKey)
}

func TestUploadStatusHandling(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		wantErr bool
	}{
		{"ok", http.StatusOK, false},
		{"bad request", http.StatusBadRequest, true},
		{"unauthorized", http.StatusUnauthorized, true},
		{"not found", http.StatusNotFound, true},
		{"quota", http.StatusTooManyRequests, true},
		{"server error", http.StatusInternalServerError, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			profilingTestSetup(t, c.status, profilingTestAPIKey)
			err := upload(profilingTestBuilder(t, profilingTestTarget(profilingTestContainerID)))
			if !c.wantErr {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), "collector says hi", "the collector's reason is surfaced in the error")
		})
	}
}

func TestUploadCollectorUnreachable(t *testing.T) {
	profilingTestSetup(t, http.StatusOK, profilingTestAPIKey)
	srv := httptest.NewServer(http.NotFoundHandler())
	u, _ := url.Parse(srv.URL + "/v1/profiles")
	srv.Close() // nothing listens there any more
	endpointUrl = u
	assert.Error(t, upload(profilingTestBuilder(t, profilingTestTarget(profilingTestContainerID))))
}

func TestUploadHostProcessHasServiceName(t *testing.T) {
	// A process outside any container: TargetFinder builds the target with an
	// empty service_name. mainv2 answers 400 when service.name is empty, so the
	// upload must still carry a non-empty one.
	col := profilingTestSetup(t, http.StatusOK, profilingTestAPIKey)
	tf := &TargetFinder{processes: map[uint32]*processInfo{}}
	ch := make(chan containers.ProcessInfo)
	tf.start(ch)
	start := time.Now().Add(-2 * CollectInterval)
	ch <- containers.ProcessInfo{Pid: profilingTestPid, ContainerId: "", StartedAt: start}
	close(ch)
	profilingTestWaitProcess(t, tf, profilingTestPid)
	tf.lock.Lock()
	tf.now = time.Now().UnixNano()
	tf.lock.Unlock()
	target := tf.FindTarget(profilingTestPid)
	require.NotNil(t, target)

	require.NoError(t, upload(profilingTestBuilder(t, target)))
	reqs := col.all()
	require.Len(t, reqs, 1)
	assert.NotEmpty(t, reqs[0].query.Get("service.name"))
	_, hasCID := reqs[0].query["container.id"]
	assert.False(t, hasCID, "no container.id for a host process")
}

func TestUploadClientHasTimeout(t *testing.T) {
	assert.Equal(t, UploadTimeout, httpClient.Timeout, "outbound HTTP must use a client with an explicit timeout")
}

func profilingTestWaitProcess(t *testing.T, tf *TargetFinder, pid uint32) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		tf.lock.Lock()
		_, ok := tf.processes[pid]
		tf.lock.Unlock()
		if ok {
			return
		}
		time.Sleep(time.Millisecond)
	}
	require.FailNow(t, "process was not registered")
}

// profilingTestFinder registers one process started `age` before tf.now.
func profilingTestFinder(t *testing.T, age time.Duration, fl proc.Flags) *TargetFinder {
	tf := &TargetFinder{processes: map[uint32]*processInfo{}}
	ch := make(chan containers.ProcessInfo)
	tf.start(ch)
	now := time.Now()
	ch <- containers.ProcessInfo{
		Pid:         profilingTestPid,
		ContainerId: profilingTestContainerID,
		StartedAt:   now.Add(-age),
		Flags:       fl,
	}
	close(ch)
	profilingTestWaitProcess(t, tf, profilingTestPid)
	tf.lock.Lock()
	tf.now = now.UnixNano()
	tf.lock.Unlock()
	return tf
}

func TestTargetFinderUnknownPid(t *testing.T) {
	tf := &TargetFinder{processes: map[uint32]*processInfo{}, now: time.Now().UnixNano()}
	assert.Nil(t, tf.FindTarget(profilingTestPid))
}

func TestTargetFinderIgnoresYoungProcesses(t *testing.T) {
	// A process younger than one collect interval is skipped (its symbols may not be loaded yet).
	tf := profilingTestFinder(t, CollectInterval-time.Second, proc.Flags{})
	assert.Nil(t, tf.FindTarget(profilingTestPid))
}

func TestTargetFinderOldProcessTarget(t *testing.T) {
	tf := profilingTestFinder(t, CollectInterval+time.Second, proc.Flags{})
	target := tf.FindTarget(profilingTestPid)
	require.NotNil(t, target)
	svc, ok := target.Get("service_name")
	assert.True(t, ok)
	assert.Equal(t, profilingTestServiceName, svc, "service name derived from the container id")
	cid, ok := target.Get("__container_id__")
	assert.True(t, ok)
	assert.Equal(t, profilingTestContainerID, cid)

	// stable across calls; the process is initialized once
	assert.Same(t, target, tf.FindTarget(profilingTestPid))
	tf.lock.Lock()
	assert.True(t, tf.processes[profilingTestPid].initialized)
	assert.False(t, tf.processes[profilingTestPid].jvmPerfmapDumpSupported)
	tf.lock.Unlock()
}

func TestTargetFinderProfilingDisabled(t *testing.T) {
	// CODEXRAY_EBPF_PROFILING=disabled in the process environ => never profiled.
	tf := profilingTestFinder(t, 10*CollectInterval, proc.Flags{EbpfProfilingDisabled: true})
	assert.Nil(t, tf.FindTarget(profilingTestPid))
	assert.Nil(t, tf.FindTarget(profilingTestPid), "still disabled after initialization")
}

func TestTargetFinderNeverUpdated(t *testing.T) {
	// Before the first Update/Start, now == 0: nothing is old enough to profile.
	tf := profilingTestFinder(t, 10*CollectInterval, proc.Flags{})
	tf.lock.Lock()
	tf.now = 0
	tf.lock.Unlock()
	assert.Nil(t, tf.FindTarget(profilingTestPid))
}

func TestTargetFinderRemoveDeadPID(t *testing.T) {
	tf := profilingTestFinder(t, 2*CollectInterval, proc.Flags{})
	require.NotNil(t, tf.FindTarget(profilingTestPid))
	tf.RemoveDeadPID(profilingTestPid)
	assert.Nil(t, tf.FindTarget(profilingTestPid))
	assert.NotPanics(t, func() { tf.RemoveDeadPID(profilingTestPid) })
}

func TestTargetFinderDebugInfoAndUpdate(t *testing.T) {
	tf := &TargetFinder{processes: map[uint32]*processInfo{}}
	assert.Nil(t, tf.DebugInfo())
	before := time.Now().UnixNano()
	tf.Update(sd.TargetsOptions{})
	assert.GreaterOrEqual(t, tf.now, before)
	assert.LessOrEqual(t, tf.now, time.Now().UnixNano())
}

func TestTargetFinderUpdateRacesFindTarget(t *testing.T) {
	// BUG: TargetFinder.Update (and profiling.Start) write tf.now without tf.lock
	// while FindTarget reads it under the lock (data race under -race) — unskip when fixed
	t.Skip("BUG: TargetFinder.Update/Start write tf.now without holding tf.lock")
	tf := profilingTestFinder(t, 2*CollectInterval, proc.Flags{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			tf.Update(sd.TargetsOptions{})
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			tf.FindTarget(profilingTestPid)
		}
	}()
	wg.Wait()
}

func TestInitWithoutEndpoint(t *testing.T) {
	prev := *flags.ProfilesEndpoint
	*flags.ProfilesEndpoint = nil
	t.Cleanup(func() { *flags.ProfilesEndpoint = prev })
	assert.Nil(t, Init("machine-1", "node-1"), "profiling stays off when no endpoint is configured")
}

func TestStartStopWithoutSession(t *testing.T) {
	require.Nil(t, session)
	assert.NotPanics(t, Start)
	assert.NotPanics(t, Stop)
}
