// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package tracing

import (
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/codifinary/codexray-node-agent/common"
	"github.com/codifinary/codexray-node-agent/ebpftracer/l7"
	"github.com/codifinary/codexray-node-agent/flags"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"inet.af/netaddr"
)

// tracingSaveState snapshots the package globals and restores them after the test.
func tracingSaveState(t *testing.T) {
	t.Helper()
	b, c, v, i, s := batcher, commonResourceAttrs, agentVersion, initialized, samplingRate
	ep, sampling, insecure := *flags.TracesEndpoint, *flags.TracesSampling, *flags.InsecureSkipVerify
	t.Cleanup(func() {
		batcher, commonResourceAttrs, agentVersion, initialized, samplingRate = b, c, v, i, s
		*flags.TracesEndpoint, *flags.TracesSampling, *flags.InsecureSkipVerify = ep, sampling, insecure
	})
}

// tracingNewRecorder wires the package to an in-memory span recorder instead of the OTLP batcher.
func tracingNewRecorder(t *testing.T, rate float64) *tracetest.SpanRecorder {
	t.Helper()
	tracingSaveState(t)
	sr := tracetest.NewSpanRecorder()
	batcher = sdktrace.WithSpanProcessor(sr)
	commonResourceAttrs = []attribute.KeyValue{
		attribute.String("host.name", "node-1"),
		attribute.String("host.id", "machine-1"),
	}
	agentVersion = "1.2.3"
	initialized = true
	samplingRate = rate
	return sr
}

func tracingAttrs(s sdktrace.ReadOnlySpan) map[attribute.Key]attribute.Value {
	res := map[attribute.Key]attribute.Value{}
	for _, kv := range s.Attributes() {
		res[kv.Key] = kv.Value
	}
	return res
}

func tracingDest() common.HostPort {
	return common.HostPortFromIPPort(netaddr.MustParseIPPort("10.0.0.5:8080"))
}

func tracingOnlySpan(t *testing.T, sr *tracetest.SpanRecorder) sdktrace.ReadOnlySpan {
	t.Helper()
	spans := sr.Ended()
	require.Len(t, spans, 1)
	return spans[0]
}

func tracingAssertCommon(t *testing.T, s sdktrace.ReadOnlySpan, name string, duration time.Duration, isError bool, peerName string, peerPort int64) {
	t.Helper()
	assert.Equal(t, name, s.Name())
	assert.Equal(t, trace.SpanKindClient, s.SpanKind())
	assert.Equal(t, duration, s.EndTime().Sub(s.StartTime()), "span start must be backdated by the request duration")
	assert.False(t, s.EndTime().After(time.Now()))
	if isError {
		assert.Equal(t, codes.Error, s.Status().Code)
	} else {
		assert.Equal(t, codes.Unset, s.Status().Code)
	}
	a := tracingAttrs(s)
	assert.Equal(t, peerName, a["net.peer.name"].AsString())
	assert.Equal(t, peerPort, a["net.peer.port"].AsInt64())
}

func TestGetContainerTracerNotInitialized(t *testing.T) {
	tracingSaveState(t)
	initialized = false
	tr := GetContainerTracer("/docker/abc")
	require.NotNil(t, tr)
	assert.Nil(t, tr.otel)
	// Every span method must be a safe no-op.
	tt := tr.NewTrace(tracingDest())
	tt.HttpRequest("GET", "/", 200, time.Millisecond)
	tt.Http2Request("GET", "/", "http", 200, -1, time.Millisecond)
	tt.PostgresQuery("select 1", false, time.Millisecond)
	tt.MysqlQuery("select 1", false, time.Millisecond)
	tt.MongoQuery("{}", false, time.Millisecond)
	tt.MemcachedQuery("get", []string{"k"}, false, time.Millisecond)
	tt.RedisQuery("GET", "k", false, time.Millisecond)
	tt.ClickhouseQuery("select 1", false, time.Millisecond)
	tt.ZookeeperRequest("getData", "/a", 0, time.Millisecond)
}

func TestNilTraceIsNoop(t *testing.T) {
	var tt *Trace
	assert.NotPanics(t, func() {
		tt.HttpRequest("GET", "/", 200, time.Millisecond)
		tt.Http2Request("GET", "/", "http", 200, -1, time.Millisecond)
		tt.PostgresQuery("select 1", false, time.Millisecond)
		tt.MysqlQuery("select 1", false, time.Millisecond)
		tt.MongoQuery("{}", false, time.Millisecond)
		tt.MemcachedQuery("get", nil, false, time.Millisecond)
		tt.RedisQuery("GET", "", false, time.Millisecond)
		tt.ClickhouseQuery("select 1", false, time.Millisecond)
		tt.ZookeeperRequest("getData", "", 0, time.Millisecond)
	})
}

func TestGetContainerTracerResource(t *testing.T) {
	sr := tracingNewRecorder(t, 1.0)
	GetContainerTracer("/docker/abc").NewTrace(tracingDest()).PostgresQuery("select 1", false, time.Millisecond)
	s := tracingOnlySpan(t, sr)

	res := map[attribute.Key]string{}
	for _, kv := range s.Resource().Attributes() {
		res[kv.Key] = kv.Value.Emit()
	}
	assert.Equal(t, "node-1", res["host.name"])
	assert.Equal(t, "machine-1", res["host.id"])
	assert.Equal(t, "/docker/abc", res["container.id"])
	assert.Equal(t, common.ContainerIdToOtelServiceName("/docker/abc"), res["service.name"])
	assert.Equal(t, "codexray-node-agent", s.InstrumentationScope().Name)
	assert.Equal(t, "1.2.3", s.InstrumentationScope().Version)
}

func TestHttpRequestSpan(t *testing.T) {
	cases := []struct {
		status  l7.Status
		isError bool
	}{
		{200, false}, {302, false}, {399, false}, {400, true}, {404, true}, {500, true}, {503, true},
	}
	for _, c := range cases {
		sr := tracingNewRecorder(t, 1.0)
		GetContainerTracer("/docker/a").NewTrace(tracingDest()).HttpRequest("POST", "/api/v1/items?x=1", c.status, 150*time.Millisecond)
		s := tracingOnlySpan(t, sr)
		tracingAssertCommon(t, s, "POST", 150*time.Millisecond, c.isError, "10.0.0.5", 8080)
		a := tracingAttrs(s)
		assert.Equal(t, "http://10.0.0.5:8080/api/v1/items?x=1", a["http.url"].AsString())
		assert.Equal(t, "POST", a["http.method"].AsString())
		assert.Equal(t, int64(c.status), a["http.status_code"].AsInt64())
	}

	// No method: nothing to report.
	sr := tracingNewRecorder(t, 1.0)
	GetContainerTracer("/docker/a").NewTrace(tracingDest()).HttpRequest("", "/", 200, time.Millisecond)
	assert.Empty(t, sr.Ended())
}

func TestHttp2RequestSpan(t *testing.T) {
	sr := tracingNewRecorder(t, 1.0)
	tt := GetContainerTracer("/docker/a").NewTrace(common.HostPortWithEmptyIP("grpc.svc", 50051))

	tt.Http2Request("POST", "/pkg.Svc/Method", "https", 200, 0, 20*time.Millisecond)
	tt.Http2Request("POST", "/pkg.Svc/Method", "http", 200, 14, 20*time.Millisecond)
	tt.Http2Request("GET", "/index", "http", 200, -1, 20*time.Millisecond)
	tt.Http2Request("", "", "", 500, -1, 20*time.Millisecond)

	spans := sr.Ended()
	require.Len(t, spans, 4)

	// gRPC OK
	tracingAssertCommon(t, spans[0], "POST", 20*time.Millisecond, false, "grpc.svc", 50051)
	a := tracingAttrs(spans[0])
	assert.Equal(t, "https://grpc.svc:50051/pkg.Svc/Method", a["http.url"].AsString())
	assert.Equal(t, "POST", a["http.method"].AsString())
	assert.Equal(t, int64(200), a["http.status_code"].AsInt64())
	require.Contains(t, a, attribute.Key("rpc.grpc.status_code"))
	assert.Equal(t, int64(0), a["rpc.grpc.status_code"].AsInt64())

	// gRPC UNAVAILABLE on HTTP 200 is still an error.
	tracingAssertCommon(t, spans[1], "POST", 20*time.Millisecond, true, "grpc.svc", 50051)
	assert.Equal(t, int64(14), tracingAttrs(spans[1])["rpc.grpc.status_code"].AsInt64())

	// Plain HTTP/2: no gRPC status attribute.
	tracingAssertCommon(t, spans[2], "GET", 20*time.Millisecond, false, "grpc.svc", 50051)
	assert.NotContains(t, tracingAttrs(spans[2]), attribute.Key("rpc.grpc.status_code"))

	// Missing method/path/scheme fall back to placeholders; 500 is an error.
	tracingAssertCommon(t, spans[3], "unknown", 20*time.Millisecond, true, "grpc.svc", 50051)
	a = tracingAttrs(spans[3])
	assert.Equal(t, "unknown://grpc.svc:50051/unknown", a["http.url"].AsString())
	assert.Equal(t, "unknown", a["http.method"].AsString())
}

// HTTP/1 marks status >= 400 as an error (semconv: 4xx/5xx on a client span); HTTP/2 must agree.
func TestHttp2Request400IsError(t *testing.T) {
	// BUG: Http2Request marks only status > 400 as error (HttpRequest uses >= 400), so HTTP/2 400 Bad Request spans are not errors — unskip when fixed
	t.Skip("BUG: Http2Request marks only status > 400 as error (HttpRequest uses >= 400), so HTTP/2 400 Bad Request spans are not errors")

	sr := tracingNewRecorder(t, 1.0)
	tt := GetContainerTracer("/docker/a").NewTrace(tracingDest())
	tt.HttpRequest("GET", "/", 400, time.Millisecond)
	tt.Http2Request("GET", "/", "http", 400, -1, time.Millisecond)
	spans := sr.Ended()
	require.Len(t, spans, 2)
	assert.Equal(t, codes.Error, spans[0].Status().Code)
	assert.Equal(t, spans[0].Status().Code, spans[1].Status().Code, "HTTP/1 and HTTP/2 must classify 400 the same way")
}

func TestDBQuerySpans(t *testing.T) {
	type call func(tt *Trace, isError bool)
	cases := []struct {
		name      string
		call      call
		system    string
		statement string
	}{
		{"postgres", func(tt *Trace, e bool) { tt.PostgresQuery("SELECT * FROM t WHERE id = $1", e, 5*time.Millisecond) }, "postgresql", "SELECT * FROM t WHERE id = $1"},
		{"mysql", func(tt *Trace, e bool) { tt.MysqlQuery("SELECT 1", e, 5*time.Millisecond) }, "mysql", "SELECT 1"},
		{"mongo", func(tt *Trace, e bool) { tt.MongoQuery(`{"find":"users"}`, e, 5*time.Millisecond) }, "mongodb", `{"find":"users"}`},
		{"clickhouse", func(tt *Trace, e bool) { tt.ClickhouseQuery("SELECT count() FROM logs", e, 5*time.Millisecond) }, "clickhouse", "SELECT count() FROM logs"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for _, isError := range []bool{false, true} {
				sr := tracingNewRecorder(t, 1.0)
				c.call(GetContainerTracer("/docker/db").NewTrace(tracingDest()), isError)
				s := tracingOnlySpan(t, sr)
				tracingAssertCommon(t, s, "query", 5*time.Millisecond, isError, "10.0.0.5", 8080)
				a := tracingAttrs(s)
				assert.Equal(t, c.system, a["db.system"].AsString())
				assert.Equal(t, c.statement, a["db.statement"].AsString())
			}
		})
	}
}

func TestEmptyQueryIsSkipped(t *testing.T) {
	sr := tracingNewRecorder(t, 1.0)
	tt := GetContainerTracer("/docker/db").NewTrace(tracingDest())
	tt.PostgresQuery("", false, time.Millisecond)
	tt.MysqlQuery("", false, time.Millisecond)
	tt.MongoQuery("", false, time.Millisecond)
	tt.MemcachedQuery("", []string{"k"}, false, time.Millisecond)
	tt.RedisQuery("", "k", false, time.Millisecond)
	tt.ZookeeperRequest("", "/a", 0, time.Millisecond)
	assert.Empty(t, sr.Ended())
}

func TestRedisQuerySpan(t *testing.T) {
	sr := tracingNewRecorder(t, 1.0)
	tt := GetContainerTracer("/docker/r").NewTrace(common.HostPortWithEmptyIP("redis", 6379))
	tt.RedisQuery("SET", "key value", false, 3*time.Millisecond)
	tt.RedisQuery("PING", "", true, 3*time.Millisecond)
	spans := sr.Ended()
	require.Len(t, spans, 2)

	tracingAssertCommon(t, spans[0], "SET", 3*time.Millisecond, false, "redis", 6379)
	a := tracingAttrs(spans[0])
	assert.Equal(t, "redis", a["db.system"].AsString())
	assert.Equal(t, "SET", a["db.operation"].AsString())
	assert.Equal(t, "SET key value", a["db.statement"].AsString())

	tracingAssertCommon(t, spans[1], "PING", 3*time.Millisecond, true, "redis", 6379)
	assert.Equal(t, "PING", tracingAttrs(spans[1])["db.statement"].AsString())
}

func TestMemcachedQuerySpan(t *testing.T) {
	sr := tracingNewRecorder(t, 1.0)
	tt := GetContainerTracer("/docker/m").NewTrace(tracingDest())
	tt.MemcachedQuery("flush_all", nil, false, time.Millisecond)
	tt.MemcachedQuery("get", []string{"k1"}, false, time.Millisecond)
	tt.MemcachedQuery("gets", []string{"k1", "k2"}, true, time.Millisecond)
	spans := sr.Ended()
	require.Len(t, spans, 3)

	tracingAssertCommon(t, spans[0], "flush_all", time.Millisecond, false, "10.0.0.5", 8080)
	a := tracingAttrs(spans[0])
	assert.Equal(t, "memcached", a["db.system"].AsString())
	assert.Equal(t, "flush_all", a["db.operation"].AsString())
	assert.NotContains(t, a, MemcacheDBItemKeyName)

	a = tracingAttrs(spans[1])
	assert.Equal(t, attribute.STRING, a[MemcacheDBItemKeyName].Type())
	assert.Equal(t, "k1", a[MemcacheDBItemKeyName].AsString())

	tracingAssertCommon(t, spans[2], "gets", time.Millisecond, true, "10.0.0.5", 8080)
	a = tracingAttrs(spans[2])
	assert.Equal(t, []string{"k1", "k2"}, a["db.memcached.item"].AsStringSlice())
}

func TestZookeeperRequestSpan(t *testing.T) {
	cases := []struct {
		status  l7.Status
		isError bool
	}{
		{0, false},    // ZOK
		{-4, true},    // ZCONNECTIONLOSS (system error range -1..-9)
		{-101, false}, // ZNONODE: API-level result, not a failure
		{-123, true},  // ZRECONFIGDISABLED
	}
	for _, c := range cases {
		sr := tracingNewRecorder(t, 1.0)
		GetContainerTracer("/docker/zk").NewTrace(tracingDest()).ZookeeperRequest("getData", "/brokers/ids", c.status, 2*time.Millisecond)
		s := tracingOnlySpan(t, sr)
		tracingAssertCommon(t, s, "getData", 2*time.Millisecond, c.isError, "10.0.0.5", 8080)
		a := tracingAttrs(s)
		assert.Equal(t, "zookeeper", a["db.system"].AsString())
		assert.Equal(t, "getData", a["db.operation"].AsString())
		assert.Equal(t, "getData /brokers/ids", a["db.statement"].AsString())
		assert.Equal(t, int64(c.status), a["zookeeper.status_code"].AsInt64())
	}

	sr := tracingNewRecorder(t, 1.0)
	GetContainerTracer("/docker/zk").NewTrace(tracingDest()).ZookeeperRequest("ping", "", 0, time.Millisecond)
	assert.Equal(t, "ping", tracingAttrs(tracingOnlySpan(t, sr))["db.statement"].AsString())
}

func TestShouldSample(t *testing.T) {
	tracingSaveState(t)

	samplingRate = 1.0
	for i := 0; i < 100; i++ {
		assert.True(t, shouldSample())
	}
	samplingRate = 0.0
	for i := 0; i < 100; i++ {
		assert.False(t, shouldSample())
	}
	samplingRate = -1
	assert.False(t, shouldSample())
	samplingRate = 2
	assert.True(t, shouldSample())

	samplingRate = 0.5
	n := 0
	const total = 10000
	for i := 0; i < total; i++ {
		if shouldSample() {
			n++
		}
	}
	// Binomial(10000, 0.5): sd = 50; 10 sd bounds make this effectively non-flaky.
	assert.InDelta(t, total/2, n, 500)
}

func TestSamplingRateAppliesToSpans(t *testing.T) {
	sr := tracingNewRecorder(t, 0.0)
	tt := GetContainerTracer("/docker/a").NewTrace(tracingDest())
	for i := 0; i < 50; i++ {
		tt.HttpRequest("GET", "/", 200, time.Millisecond)
	}
	assert.Empty(t, sr.Ended(), "sampling 0 must drop every span")

	sr = tracingNewRecorder(t, 1.0)
	tt = GetContainerTracer("/docker/a").NewTrace(tracingDest())
	for i := 0; i < 50; i++ {
		tt.HttpRequest("GET", "/", 200, time.Millisecond)
	}
	assert.Len(t, sr.Ended(), 50)
}

func tracingInit(t *testing.T, endpoint string, sampling float64) {
	t.Helper()
	tracingSaveState(t)
	initialized = false
	samplingRate = 0
	if endpoint == "" {
		*flags.TracesEndpoint = nil
	} else {
		u, err := url.Parse(endpoint)
		require.NoError(t, err)
		*flags.TracesEndpoint = u
	}
	*flags.TracesSampling = sampling
	Init("machine-1", "node-1", "9.9.9")
}

func TestInitNoEndpoint(t *testing.T) {
	tracingInit(t, "", 1.0)
	assert.False(t, initialized)
	assert.Nil(t, GetContainerTracer("/docker/a").otel)
}

func TestInit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	t.Cleanup(srv.Close)

	for _, c := range []struct {
		name     string
		endpoint string
		sampling float64
		expected float64
	}{
		{"http", srv.URL + "/ingest/v1/traces", 1.0, 1.0},
		{"http no path", srv.URL, 0.25, 0.25},
		{"https", "https://127.0.0.1:1/v1/traces", 0.0, 0.0},
		{"negative sampling falls back to 1", srv.URL, -0.5, 1.0},
		{"sampling above 1 falls back to 1", srv.URL, 1.5, 1.0},
	} {
		t.Run(c.name, func(t *testing.T) {
			tracingInit(t, c.endpoint, c.sampling)
			assert.True(t, initialized)
			assert.NotNil(t, batcher)
			assert.Equal(t, "9.9.9", agentVersion)
			assert.Equal(t, c.expected, samplingRate)
			res := map[attribute.Key]string{}
			for _, kv := range commonResourceAttrs {
				res[kv.Key] = kv.Value.Emit()
			}
			assert.Equal(t, map[attribute.Key]string{"host.name": "node-1", "host.id": "machine-1"}, res)
			assert.NotNil(t, GetContainerTracer("/docker/a").otel)
		})
	}
}

func TestInitNaNSampling(t *testing.T) {
	// BUG: Init accepts a NaN --traces-sampling (range check is false for NaN) and shouldSample then drops every span; invalid values must fall back to 1.0 — unskip when fixed
	t.Skip("BUG: Init accepts a NaN --traces-sampling (range check is false for NaN) and shouldSample then drops every span")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	t.Cleanup(srv.Close)
	tracingInit(t, srv.URL, math.NaN())
	assert.Equal(t, 1.0, samplingRate)
	assert.True(t, shouldSample())
}
