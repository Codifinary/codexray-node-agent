// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package profiling

import (
	"bytes"
	"compress/gzip"
	"errors"
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
	"github.com/go-kit/log"
	"github.com/google/pprof/profile"
	ebpfspy "github.com/grafana/pyroscope/ebpf"
	"github.com/grafana/pyroscope/ebpf/cpp/demangle"
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

// profilingTestSession is a fake eBPF session: no root, no BPF, scripted samples.
type profilingTestSession struct {
	lock       sync.Mutex
	startErr   error
	collectErr error
	samples    []pprof.ProfileSample
	started    int
	stopped    int
	updates    int
	collects   int
}

func (s *profilingTestSession) CollectProfiles(cb pprof.CollectProfilesCallback) error {
	s.lock.Lock()
	s.collects++
	samples := s.samples
	s.lock.Unlock()
	for _, sample := range samples {
		cb(sample)
	}
	return s.collectErr
}

func (s *profilingTestSession) Start() error {
	s.lock.Lock()
	defer s.lock.Unlock()
	s.started++
	return s.startErr
}

func (s *profilingTestSession) Stop() {
	s.lock.Lock()
	defer s.lock.Unlock()
	s.stopped++
}

func (s *profilingTestSession) Update(ebpfspy.SessionOptions) error { return nil }

func (s *profilingTestSession) UpdateTargets(sd.TargetsOptions) {
	s.lock.Lock()
	defer s.lock.Unlock()
	s.updates++
}

func (s *profilingTestSession) DebugInfo() interface{} { return nil }

func (s *profilingTestSession) counts() (started, stopped, updates, collects int) {
	s.lock.Lock()
	defer s.lock.Unlock()
	return s.started, s.stopped, s.updates, s.collects
}

// profilingTestUseSession installs fake as the package session and a fresh targetFinder.
func profilingTestUseSession(t *testing.T, fake ebpfspy.Session) {
	prevSession, prevFinder := session, targetFinder
	session = fake
	targetFinder = &TargetFinder{processes: map[uint32]*processInfo{}}
	t.Cleanup(func() { session, targetFinder = prevSession, prevFinder })
}

// profilingTestTicks makes newTicker return a ticker that fires n times and then is closed,
// so collect() runs exactly n iterations and returns.
func profilingTestTicks(t *testing.T, n int) *[]time.Duration {
	var intervals []time.Duration
	prev := newTicker
	newTicker = func(d time.Duration) *time.Ticker {
		intervals = append(intervals, d)
		ch := make(chan time.Time, n)
		for i := 0; i < n; i++ {
			ch <- time.Now()
		}
		close(ch)
		return &time.Ticker{C: ch}
	}
	t.Cleanup(func() { newTicker = prev })
	return &intervals
}

func profilingTestSamples(cids ...string) []pprof.ProfileSample {
	var res []pprof.ProfileSample
	for _, cid := range cids {
		res = append(res, pprof.ProfileSample{
			Target:      profilingTestTarget(cid),
			Pid:         profilingTestPid,
			SampleType:  pprof.SampleTypeCpu,
			Aggregation: pprof.SampleAggregated,
			Stack:       []string{"leaf", "main"},
			Value:       profilingTestSampleCount,
		})
	}
	return res
}

func TestCollectUploadsEveryProfile(t *testing.T) {
	col := profilingTestSetup(t, http.StatusOK, profilingTestAPIKey)
	fake := &profilingTestSession{samples: profilingTestSamples("/docker/a", "/docker/b", "/docker/c")}
	profilingTestUseSession(t, fake)
	intervals := profilingTestTicks(t, 1)

	collect()

	assert.Equal(t, []time.Duration{CollectInterval}, *intervals)
	_, _, updates, collects := fake.counts()
	assert.Equal(t, 1, updates, "targets are refreshed before each collection")
	assert.Equal(t, 1, collects)
	reqs := col.all()
	require.Len(t, reqs, 3, "one upload per profile builder")
	var cids []string
	for _, r := range reqs {
		cids = append(cids, r.query.Get("container.id"))
		require.NotNil(t, r.profile)
		assert.Equal(t, profilingTestExpectedType, r.profile.SampleType[0].Type)
	}
	assert.ElementsMatch(t, []string{"/docker/a", "/docker/b", "/docker/c"}, cids)
}

func TestCollectEveryTick(t *testing.T) {
	col := profilingTestSetup(t, http.StatusOK, profilingTestAPIKey)
	fake := &profilingTestSession{samples: profilingTestSamples("/docker/a")}
	profilingTestUseSession(t, fake)
	profilingTestTicks(t, 3)

	collect()

	_, _, updates, collects := fake.counts()
	assert.Equal(t, 3, updates)
	assert.Equal(t, 3, collects)
	assert.Len(t, col.all(), 3, "fresh builders every interval")
}

func TestCollectStopsUploadingOnError(t *testing.T) {
	col := profilingTestSetup(t, http.StatusInternalServerError, profilingTestAPIKey)
	fake := &profilingTestSession{samples: profilingTestSamples("/docker/a", "/docker/b", "/docker/c")}
	profilingTestUseSession(t, fake)
	profilingTestTicks(t, 2)

	collect()

	assert.Len(t, col.all(), 2, "the first failed upload ends the iteration; the next tick tries again")
}

func TestCollectErrorStillUploadsCollected(t *testing.T) {
	col := profilingTestSetup(t, http.StatusOK, profilingTestAPIKey)
	fake := &profilingTestSession{
		samples:    profilingTestSamples("/docker/a"),
		collectErr: errors.New("perf map read failed"),
	}
	profilingTestUseSession(t, fake)
	profilingTestTicks(t, 1)

	collect()

	assert.Len(t, col.all(), 1, "a partial collection is still uploaded")
}

func TestCollectNothingCollected(t *testing.T) {
	col := profilingTestSetup(t, http.StatusOK, profilingTestAPIKey)
	fake := &profilingTestSession{}
	profilingTestUseSession(t, fake)
	profilingTestTicks(t, 1)

	collect()

	_, _, _, collects := fake.counts()
	assert.Equal(t, 1, collects)
	assert.Empty(t, col.all())
}

func TestStartStopWithSession(t *testing.T) {
	fake := &profilingTestSession{}
	profilingTestUseSession(t, fake)

	before := time.Now().UnixNano()
	Start()
	assert.GreaterOrEqual(t, targetFinder.now, before, "Start stamps the finder's clock")
	assert.LessOrEqual(t, targetFinder.now, time.Now().UnixNano())
	_, stopped, updates, _ := fake.counts()
	assert.Equal(t, 1, updates)
	assert.Zero(t, stopped)

	Stop()
	_, stopped, _, _ = fake.counts()
	assert.Equal(t, 1, stopped)
}

type profilingTestNewSessionCall struct {
	finder  sd.TargetFinder
	options ebpfspy.SessionOptions
}

// profilingTestInit wires Init to a fake collector endpoint and a fake session factory.
// The returned channel receives once when collect() has obtained its ticker.
func profilingTestInit(t *testing.T, fake ebpfspy.Session, newErr error) (*[]profilingTestNewSessionCall, <-chan struct{}) {
	u, err := url.Parse("http://collector.test/v1/profiles")
	require.NoError(t, err)
	prevEndpoint, prevURL, prevLabels := *flags.ProfilesEndpoint, endpointUrl, constLabels
	prevSession, prevFinder, prevNewSession, prevNewTicker := session, targetFinder, newSession, newTicker
	*flags.ProfilesEndpoint = u
	targetFinder = &TargetFinder{processes: map[uint32]*processInfo{}}
	t.Cleanup(func() {
		*flags.ProfilesEndpoint, endpointUrl, constLabels = prevEndpoint, prevURL, prevLabels
		session, targetFinder, newSession, newTicker = prevSession, prevFinder, prevNewSession, prevNewTicker
	})

	var calls []profilingTestNewSessionCall
	newSession = func(_ log.Logger, tf sd.TargetFinder, so ebpfspy.SessionOptions) (ebpfspy.Session, error) {
		calls = append(calls, profilingTestNewSessionCall{finder: tf, options: so})
		if newErr != nil {
			return nil, newErr
		}
		return fake, nil
	}
	tickerCreated := make(chan struct{}, 1)
	newTicker = func(time.Duration) *time.Ticker {
		ch := make(chan time.Time)
		close(ch) // collect() returns right away
		tickerCreated <- struct{}{}
		return &time.Ticker{C: ch}
	}
	return &calls, tickerCreated
}

func TestInitStartsSession(t *testing.T) {
	fake := &profilingTestSession{}
	calls, tickerCreated := profilingTestInit(t, fake, nil)
	finder := targetFinder

	ch := Init("machine-1", "node-1")
	require.NotNil(t, ch)
	select {
	case <-tickerCreated:
	case <-time.After(2 * time.Second):
		require.FailNow(t, "collect() was not started")
	}

	assert.Equal(t, "http://collector.test/v1/profiles", endpointUrl.String())
	assert.Equal(t, labels.Labels{
		{Name: "host.name", Value: "node-1"},
		{Name: "host.id", Value: "machine-1"},
	}, constLabels)
	assert.Same(t, fake, session)
	started, _, _, _ := fake.counts()
	assert.Equal(t, 1, started)

	require.Len(t, *calls, 1)
	c := (*calls)[0]
	assert.Same(t, finder, c.finder, "the session resolves targets through the package TargetFinder")
	so := c.options
	assert.Equal(t, SampleRate, so.SampleRate)
	assert.True(t, so.CollectUser)
	assert.False(t, so.CollectKernel)
	assert.True(t, so.UnknownSymbolModuleOffset)
	assert.False(t, so.UnknownSymbolAddress)
	assert.True(t, so.PythonEnabled)
	for _, o := range []struct {
		name       string
		size, keep int
	}{
		{"pid", so.CacheOptions.PidCacheOptions.Size, so.CacheOptions.PidCacheOptions.KeepRounds},
		{"build id", so.CacheOptions.BuildIDCacheOptions.Size, so.CacheOptions.BuildIDCacheOptions.KeepRounds},
		{"same file", so.CacheOptions.SameFileCacheOptions.Size, so.CacheOptions.SameFileCacheOptions.KeepRounds},
	} {
		assert.Equal(t, 256, o.size, o.name)
		assert.Equal(t, 8, o.keep, o.name)
	}
	assert.True(t, so.SymbolOptions.GoTableFallback)
	assert.True(t, so.SymbolOptions.PythonFullFilePath)
	assert.Equal(t, demangle.DemangleFull, so.SymbolOptions.DemangleOptions)
	require.NotNil(t, so.Metrics)
	assert.NotNil(t, so.Metrics.Symtab)
	assert.NotNil(t, so.Metrics.Python)

	// the returned channel feeds the TargetFinder
	ch <- containers.ProcessInfo{Pid: profilingTestPid, ContainerId: profilingTestContainerID, StartedAt: time.Now()}
	close(ch)
	profilingTestWaitProcess(t, finder, profilingTestPid)
}

func TestInitNewSessionError(t *testing.T) {
	calls, tickerCreated := profilingTestInit(t, nil, errors.New("operation not permitted"))
	assert.Nil(t, Init("machine-1", "node-1"))
	assert.Len(t, *calls, 1)
	assert.Nil(t, session, "no half-initialized session is kept")
	assert.Empty(t, tickerCreated, "collect() is not started")
	assert.NotPanics(t, Start)
	assert.NotPanics(t, Stop)
}

func TestInitSessionStartError(t *testing.T) {
	fake := &profilingTestSession{startErr: errors.New("failed to load BPF program")}
	_, tickerCreated := profilingTestInit(t, fake, nil)
	assert.Nil(t, Init("machine-1", "node-1"))
	assert.Nil(t, session, "no half-initialized session is kept")
	assert.Empty(t, tickerCreated, "collect() is not started")
	Stop()
	_, stopped, _, _ := fake.counts()
	assert.Zero(t, stopped)
}

// profilingTestJvm stubs /proc/<pid>/cmdline and the JVM perfmap dump.
type profilingTestJvm struct {
	lock     sync.Mutex
	cmdlines int
	dumps    []uint32
	dumpErr  error
}

func (j *profilingTestJvm) dumpCount() int {
	j.lock.Lock()
	defer j.lock.Unlock()
	return len(j.dumps)
}

func (j *profilingTestJvm) cmdlineCount() int {
	j.lock.Lock()
	defer j.lock.Unlock()
	return j.cmdlines
}

func profilingTestStubJvm(t *testing.T, cmdline string, dumpErr error) *profilingTestJvm {
	j := &profilingTestJvm{dumpErr: dumpErr}
	prevCmdline, prevDump := getCmdline, dumpPerfmap
	getCmdline = func(uint32) []byte {
		j.lock.Lock()
		defer j.lock.Unlock()
		j.cmdlines++
		return []byte(cmdline)
	}
	dumpPerfmap = func(pid uint32) error {
		j.lock.Lock()
		defer j.lock.Unlock()
		j.dumps = append(j.dumps, pid)
		return j.dumpErr
	}
	t.Cleanup(func() { getCmdline, dumpPerfmap = prevCmdline, prevDump })
	return j
}

func profilingTestAdvance(tf *TargetFinder, d time.Duration) {
	tf.lock.Lock()
	tf.now += int64(d)
	tf.lock.Unlock()
}

func TestTargetFinderJvmPerfmapDump(t *testing.T) {
	j := profilingTestStubJvm(t, "/usr/lib/jvm/bin/java\x00-XX:+PreserveFramePointer\x00-jar\x00app.jar", nil)
	tf := profilingTestFinder(t, 2*CollectInterval, proc.Flags{})

	target := tf.FindTarget(profilingTestPid)
	require.NotNil(t, target)
	tf.lock.Lock()
	assert.True(t, tf.processes[profilingTestPid].jvmPerfmapDumpSupported)
	tf.lock.Unlock()
	assert.Equal(t, 1, j.dumpCount())

	assert.Same(t, target, tf.FindTarget(profilingTestPid))
	assert.Equal(t, 1, j.dumpCount(), "one dump per collection round")

	profilingTestAdvance(tf, CollectInterval)
	assert.Same(t, target, tf.FindTarget(profilingTestPid))
	assert.Equal(t, 2, j.dumpCount(), "dumped again once the round changes")
	assert.Equal(t, []uint32{profilingTestPid, profilingTestPid}, j.dumps)
	assert.Equal(t, 1, j.cmdlineCount(), "the cmdline is read once per process")
}

func TestTargetFinderJvmWithoutFramePointer(t *testing.T) {
	j := profilingTestStubJvm(t, "/usr/bin/java\x00-jar\x00app.jar", nil)
	tf := profilingTestFinder(t, 2*CollectInterval, proc.Flags{})

	require.NotNil(t, tf.FindTarget(profilingTestPid))
	profilingTestAdvance(tf, CollectInterval)
	require.NotNil(t, tf.FindTarget(profilingTestPid))
	tf.lock.Lock()
	assert.False(t, tf.processes[profilingTestPid].jvmPerfmapDumpSupported)
	tf.lock.Unlock()
	assert.Zero(t, j.dumpCount(), "perfmap dump needs -XX:+PreserveFramePointer")
}

func TestTargetFinderNonJvm(t *testing.T) {
	j := profilingTestStubJvm(t, "/usr/bin/python3\x00-XX:+PreserveFramePointer", nil)
	tf := profilingTestFinder(t, 2*CollectInterval, proc.Flags{})
	require.NotNil(t, tf.FindTarget(profilingTestPid))
	assert.Equal(t, 1, j.cmdlineCount())
	assert.Zero(t, j.dumpCount())
}

func TestTargetFinderJvmPerfmapDumpError(t *testing.T) {
	j := profilingTestStubJvm(t, "/usr/bin/java\x00-XX:+PreserveFramePointer", errors.New("failed to attach to JVM"))
	tf := profilingTestFinder(t, 2*CollectInterval, proc.Flags{})

	target := tf.FindTarget(profilingTestPid)
	assert.NotNil(t, target, "a failed dump only logs; the process is still profiled")
	assert.Equal(t, 1, j.dumpCount())
	assert.Same(t, target, tf.FindTarget(profilingTestPid))
	assert.Equal(t, 1, j.dumpCount(), "no retry within the same round")
	profilingTestAdvance(tf, CollectInterval)
	tf.FindTarget(profilingTestPid)
	assert.Equal(t, 2, j.dumpCount(), "retried next round")
}

func TestTargetFinderProfilingDisabledSkipsJvmDetection(t *testing.T) {
	j := profilingTestStubJvm(t, "/usr/bin/java\x00-XX:+PreserveFramePointer", nil)
	tf := profilingTestFinder(t, 2*CollectInterval, proc.Flags{EbpfProfilingDisabled: true})
	assert.Nil(t, tf.FindTarget(profilingTestPid))
	profilingTestAdvance(tf, CollectInterval)
	assert.Nil(t, tf.FindTarget(profilingTestPid))
	assert.Zero(t, j.cmdlineCount(), "the process is not inspected at all")
	assert.Zero(t, j.dumpCount())
}
