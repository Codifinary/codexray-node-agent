// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package logs

import (
	"context"
	"crypto/tls"
	"time"

	otel "github.com/agoda-com/opentelemetry-logs-go"
	"github.com/agoda-com/opentelemetry-logs-go/exporters/otlp/otlplogs"
	"github.com/agoda-com/opentelemetry-logs-go/exporters/otlp/otlplogs/otlplogshttp"
	otelLogs "github.com/agoda-com/opentelemetry-logs-go/logs"
	sdk "github.com/agoda-com/opentelemetry-logs-go/sdk/logs"
	"github.com/codifinary/codexray-node-agent/common"
	"github.com/codifinary/codexray-node-agent/diag"
	"github.com/codifinary/codexray-node-agent/flags"
	"github.com/codifinary/logparser"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.18.0"
	"k8s.io/klog/v2"
)

var otelLogger otelLogs.Logger

func Init(machineId, hostname, version string) {
	endpointUrl := *flags.LogsEndpoint
	if endpointUrl == nil {
		klog.Infoln("no OpenTelemetry logs collector endpoint configured")
		return
	}
	klog.Infoln("OpenTelemetry logs collector endpoint:", endpointUrl.String())
	if configuredLevelFilter() != nil {
		klog.Infoln("exporting logs at level", *flags.ContainerLogMinLevel, "and above; lines without a detectable level:",
			map[bool]string{true: "kept", false: "dropped"}[*flags.ContainerLogKeepUnknown])
	}
	path := endpointUrl.Path
	if path == "" {
		path = "/"
	}

	opts := []otlplogshttp.Option{
		otlplogshttp.WithEndpoint(endpointUrl.Host),
		otlplogshttp.WithURLPath(path),
		otlplogshttp.WithHeaders(common.AuthHeaders()),
	}
	if endpointUrl.Scheme == "https" {
		opts = append(opts, otlplogshttp.WithTLSClientConfig(&tls.Config{InsecureSkipVerify: *flags.InsecureSkipVerify}))
	} else {
		opts = append(opts, otlplogshttp.WithInsecure())
	}
	client := otlplogshttp.NewClient(opts...)
	exporter, _ := otlplogs.NewExporter(context.Background(), otlplogs.WithClient(client))

	loggerProvider := sdk.NewLoggerProvider(
		sdk.WithBatcher(countingLogExporter{exporter}),
		sdk.WithResource(
			resource.NewWithAttributes(
				semconv.SchemaURL,
				semconv.ServiceName("codexray-node-agent"),
				semconv.HostName(hostname),
				semconv.HostID(machineId),
			),
		),
	)
	otel.SetLoggerProvider(loggerProvider)
	otelLogger = loggerProvider.Logger("codexray-node-agent", otelLogs.WithInstrumentationVersion(version))
}

// minLevels maps --container-log-min-level to the least severe level kept. logparser levels
// are ordered from most severe (LevelCritical=1) to least severe (LevelDebug=5).
var minLevels = map[string]logparser.Level{
	"debug":    logparser.LevelDebug,
	"info":     logparser.LevelInfo,
	"warning":  logparser.LevelWarning,
	"error":    logparser.LevelError,
	"critical": logparser.LevelCritical,
}

// levelFilter returns nil when every line is exported (min "all"), otherwise a function that
// reports whether a line of the given level is exported, and the drop reason when it is not.
func levelFilter(min string, keepUnknown bool) func(logparser.Level) (bool, string) {
	minLevel, ok := minLevels[min]
	if !ok {
		return nil
	}
	return func(level logparser.Level) (bool, string) {
		if level == logparser.LevelUnknown {
			if keepUnknown {
				return true, ""
			}
			return false, "unknown_level"
		}
		if level > minLevel {
			return false, "below_min_level"
		}
		return true, ""
	}
}

func configuredLevelFilter() func(logparser.Level) (bool, string) {
	min, keepUnknown := "all", false
	if flags.ContainerLogMinLevel != nil {
		min = *flags.ContainerLogMinLevel
	}
	if flags.ContainerLogKeepUnknown != nil {
		keepUnknown = *flags.ContainerLogKeepUnknown
	}
	return levelFilter(min, keepUnknown)
}

func OtelLogEmitter(containerId string) logparser.OnMsgCallbackF {
	if otelLogger == nil {
		return nil
	}
	keep := configuredLevelFilter()
	return func(ts time.Time, level logparser.Level, patternHash string, msg string) {
		if keep != nil {
			if ok, reason := keep(level); !ok {
				diag.DroppedTotal.WithLabelValues("logs", reason).Inc()
				return
			}
		}
		severityText := level.String()
		severityNumber := otelLogs.UNSPECIFIED
		switch level {
		case logparser.LevelCritical:
			severityNumber = otelLogs.FATAL
		case logparser.LevelError:
			severityNumber = otelLogs.ERROR
		case logparser.LevelWarning:
			severityNumber = otelLogs.WARN
		case logparser.LevelInfo:
			severityNumber = otelLogs.INFO
		case logparser.LevelDebug:
			severityNumber = otelLogs.DEBUG
		}

		otelLogger.Emit(
			otelLogs.NewLogRecord(otelLogs.LogRecordConfig{
				ObservedTimestamp: ts,
				SeverityText:      &severityText,
				SeverityNumber:    &severityNumber,
				Body:              &msg,
				Resource: resource.NewSchemaless(
					semconv.ServiceName(common.ContainerIdToOtelServiceName(containerId)),
					semconv.ContainerID(containerId),
				),
				Attributes: &[]attribute.KeyValue{
					attribute.Key("pattern.hash").String(patternHash),
				},
			}),
		)
	}
}

// countingLogExporter makes failed log deliveries visible as
// node_agent_export_failures_total{signal="logs"} (the SDK only logs them).
type countingLogExporter struct {
	sdk.LogRecordExporter
}

func (e countingLogExporter) Export(ctx context.Context, batch []sdk.ReadableLogRecord) error {
	err := e.LogRecordExporter.Export(ctx, batch)
	if err != nil {
		diag.ExportFailures.WithLabelValues("logs").Inc()
		diag.DroppedTotal.WithLabelValues("logs", "export_failed").Add(float64(len(batch)))
	}
	return err
}
