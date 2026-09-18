// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package logs

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	otel "github.com/agoda-com/opentelemetry-logs-go"
	otelLogs "github.com/agoda-com/opentelemetry-logs-go/logs"
	sdk "github.com/agoda-com/opentelemetry-logs-go/sdk/logs"
	"github.com/codifinary/codexray-node-agent/flags"
	"github.com/codifinary/logparser"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.18.0"
	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	"google.golang.org/protobuf/proto"
)

// A deployment pod's container id; ContainerIdToOtelServiceName maps it to the workload.
const (
	otelTestContainerID   = "/k8s/shop/checkout-7d9f8b6c5d-x2x4z/app"
	otelTestServiceName   = "/k8s/shop/checkout"
	otelTestNonK8sID      = "/docker/nginx"
	otelTestPatternHash   = "a1b2c3d4"
	otelTestLoggerVersion = "1.2.3"
)

// otelTestExporter is an in-memory LogRecordExporter.
type otelTestExporter struct {
	lock    sync.Mutex
	records []sdk.ReadableLogRecord
}

func (e *otelTestExporter) Export(_ context.Context, batch []sdk.ReadableLogRecord) error {
	e.lock.Lock()
	defer e.lock.Unlock()
	e.records = append(e.records, batch...)
	return nil
}

func (e *otelTestExporter) Shutdown(context.Context) error { return nil }

func (e *otelTestExporter) all() []sdk.ReadableLogRecord {
	e.lock.Lock()
	defer e.lock.Unlock()
	return append([]sdk.ReadableLogRecord(nil), e.records...)
}

// otelTestUseLogger swaps the package logger for one backed by an in-memory
// exporter with the same provider resource Init builds.
func otelTestUseLogger(t *testing.T) *otelTestExporter {
	exp := &otelTestExporter{}
	lp := sdk.NewLoggerProvider(
		sdk.WithSyncer(exp),
		sdk.WithResource(resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName("codexray-node-agent"),
			semconv.HostName("node-1"),
			semconv.HostID("machine-1"),
		)),
	)
	prev := otelLogger
	otelLogger = lp.Logger("codexray-node-agent")
	t.Cleanup(func() {
		otelLogger = prev
		_ = lp.Shutdown(context.Background())
	})
	return exp
}

func otelTestResourceAttr(r *resource.Resource, key attribute.Key) (string, bool) {
	if r == nil {
		return "", false
	}
	v, ok := r.Set().Value(key)
	return v.AsString(), ok
}

func TestOtelLogEmitterDisabledWithoutInit(t *testing.T) {
	prev := otelLogger
	otelLogger = nil
	t.Cleanup(func() { otelLogger = prev })
	assert.Nil(t, OtelLogEmitter(otelTestContainerID), "no exporter configured => no callback")
}

func TestOtelLogEmitterRecordFields(t *testing.T) {
	exp := otelTestUseLogger(t)
	emit := OtelLogEmitter(otelTestContainerID)
	require.NotNil(t, emit)

	ts := time.Date(2026, 1, 2, 3, 4, 5, 6, time.UTC)
	emit(ts, logparser.LevelError, otelTestPatternHash, "connection refused")

	recs := exp.all()
	require.Len(t, recs, 1)
	r := recs[0]

	require.NotNil(t, r.Body())
	assert.Equal(t, "connection refused", *r.Body())
	assert.Equal(t, ts, r.ObservedTimestamp())
	require.NotNil(t, r.SeverityText())
	assert.Equal(t, "error", *r.SeverityText())
	require.NotNil(t, r.SeverityNumber())
	assert.Equal(t, otelLogs.ERROR, *r.SeverityNumber())

	// mainv2 filters logs on ResourceAttributes['container.id'] and groups by ServiceName.
	cid, ok := otelTestResourceAttr(r.Resource(), semconv.ContainerIDKey)
	assert.True(t, ok)
	assert.Equal(t, otelTestContainerID, cid)
	svc, ok := otelTestResourceAttr(r.Resource(), semconv.ServiceNameKey)
	assert.True(t, ok)
	assert.Equal(t, otelTestServiceName, svc, "the record's service.name must override the agent's own")
	host, ok := otelTestResourceAttr(r.Resource(), semconv.HostNameKey)
	assert.True(t, ok, "provider resource attributes are merged into every record")
	assert.Equal(t, "node-1", host)

	// mainv2 filters on LogAttributes['pattern.hash'].
	require.NotNil(t, r.Attributes())
	assert.Contains(t, *r.Attributes(), attribute.String("pattern.hash", otelTestPatternHash))
}

func TestOtelLogEmitterNonK8sServiceName(t *testing.T) {
	exp := otelTestUseLogger(t)
	OtelLogEmitter(otelTestNonK8sID)(time.Now(), logparser.LevelInfo, "", "hi")
	recs := exp.all()
	require.Len(t, recs, 1)
	svc, _ := otelTestResourceAttr(recs[0].Resource(), semconv.ServiceNameKey)
	assert.Equal(t, otelTestNonK8sID, svc)
}

func TestOtelLogEmitterSeverityMapping(t *testing.T) {
	// OTel log data model severity ranges: DEBUG=5, INFO=9, WARN=13, ERROR=17, FATAL=21.
	cases := []struct {
		level  logparser.Level
		text   string
		number otelLogs.SeverityNumber
	}{
		{logparser.LevelCritical, "critical", otelLogs.FATAL},
		{logparser.LevelError, "error", otelLogs.ERROR},
		{logparser.LevelWarning, "warning", otelLogs.WARN},
		{logparser.LevelInfo, "info", otelLogs.INFO},
		{logparser.LevelDebug, "debug", otelLogs.DEBUG},
		{logparser.LevelUnknown, "unknown", otelLogs.UNSPECIFIED},
	}
	for _, c := range cases {
		t.Run(c.text, func(t *testing.T) {
			exp := otelTestUseLogger(t)
			OtelLogEmitter(otelTestContainerID)(time.Now(), c.level, otelTestPatternHash, "msg")
			recs := exp.all()
			require.Len(t, recs, 1)
			assert.Equal(t, c.text, *recs[0].SeverityText())
			assert.Equal(t, c.number, *recs[0].SeverityNumber())
		})
	}
}

// otelTestCollector is a fake OTLP/HTTP logs endpoint.
type otelTestCollector struct {
	lock     sync.Mutex
	paths    []string
	apiKeys  []string
	requests []*collogspb.ExportLogsServiceRequest
}

func (c *otelTestCollector) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body io.Reader = r.Body
	if r.Header.Get("Content-Encoding") == "gzip" {
		gz, err := gzip.NewReader(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		body = gz
	}
	data, err := io.ReadAll(body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	req := &collogspb.ExportLogsServiceRequest{}
	if err := proto.Unmarshal(data, req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	c.lock.Lock()
	c.paths = append(c.paths, r.Method+" "+r.URL.Path)
	c.apiKeys = append(c.apiKeys, r.Header.Get("X-Api-Key"))
	c.requests = append(c.requests, req)
	c.lock.Unlock()
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(nil)
}

func otelTestAttrs(kvs []*commonpb.KeyValue) map[string]string {
	res := map[string]string{}
	for _, kv := range kvs {
		res[kv.GetKey()] = kv.GetValue().GetStringValue()
	}
	return res
}

func TestInitWithoutEndpoint(t *testing.T) {
	prevEndpoint, prevLogger := *flags.LogsEndpoint, otelLogger
	*flags.LogsEndpoint = nil
	otelLogger = nil
	t.Cleanup(func() { *flags.LogsEndpoint, otelLogger = prevEndpoint, prevLogger })

	Init("machine-1", "node-1", otelTestLoggerVersion)
	assert.Nil(t, otelLogger, "logs export stays off when no endpoint is configured")
	assert.Nil(t, OtelLogEmitter(otelTestContainerID))
}

func TestInitExportsOTLPToCollector(t *testing.T) {
	col := &otelTestCollector{}
	srv := httptest.NewServer(col)
	defer srv.Close()

	u, err := url.Parse(srv.URL + "/ingest/v1/logs")
	require.NoError(t, err)
	prevEndpoint, prevKey, prevLogger, prevProvider := *flags.LogsEndpoint, *flags.ApiKey, otelLogger, otel.GetLoggerProvider()
	*flags.LogsEndpoint = u
	*flags.ApiKey = "test-api-key"
	t.Cleanup(func() {
		*flags.LogsEndpoint, *flags.ApiKey, otelLogger = prevEndpoint, prevKey, prevLogger
		otel.SetLoggerProvider(prevProvider)
	})

	Init("machine-1", "node-1", otelTestLoggerVersion)
	require.NotNil(t, otelLogger)
	lp, ok := otel.GetLoggerProvider().(*sdk.LoggerProvider)
	require.True(t, ok, "Init registers an SDK logger provider globally")
	t.Cleanup(func() { _ = lp.Shutdown(context.Background()) })

	ts := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	OtelLogEmitter(otelTestContainerID)(ts, logparser.LevelWarning, otelTestPatternHash, "disk almost full")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, lp.ForceFlush(ctx))

	col.lock.Lock()
	defer col.lock.Unlock()
	require.Len(t, col.requests, 1)
	assert.Equal(t, "POST /ingest/v1/logs", col.paths[0], "the configured path (incl. mainv2 BASE_PATH) is used as-is")
	assert.Equal(t, "test-api-key", col.apiKeys[0])

	rls := col.requests[0].GetResourceLogs()
	require.Len(t, rls, 1)
	res := otelTestAttrs(rls[0].GetResource().GetAttributes())
	assert.Equal(t, otelTestServiceName, res["service.name"])
	assert.Equal(t, otelTestContainerID, res["container.id"])
	assert.Equal(t, "node-1", res["host.name"])
	assert.Equal(t, "machine-1", res["host.id"])

	// Scope name/version are not asserted: agoda's SDK takes the scope from the
	// record, not from Logger(name, WithInstrumentationVersion) — mainv2 only
	// copies a non-empty scope into LogAttributes, so nothing depends on it.
	sls := rls[0].GetScopeLogs()
	require.Len(t, sls, 1)
	lrs := sls[0].GetLogRecords()
	require.Len(t, lrs, 1)
	lr := lrs[0]
	// mainv2 stores lr.TimeUnixNano as the row Timestamp; it must be the event time, not 0.
	assert.Equal(t, uint64(ts.UnixNano()), lr.GetTimeUnixNano())
	assert.Equal(t, "disk almost full", lr.GetBody().GetStringValue())
	assert.Equal(t, "warning", lr.GetSeverityText())
	assert.EqualValues(t, otelLogs.WARN, lr.GetSeverityNumber())
	assert.Equal(t, otelTestPatternHash, otelTestAttrs(lr.GetAttributes())["pattern.hash"])
	assert.False(t, bytes.Contains([]byte(u.String()), []byte("test-api-key")), "the API key never goes in the URL")
}
