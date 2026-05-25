package logs

import (
	"context"
	"crypto/tls"
	"strings"
	"time"

	otel "github.com/agoda-com/opentelemetry-logs-go"
	"github.com/agoda-com/opentelemetry-logs-go/exporters/otlp/otlplogs"
	"github.com/agoda-com/opentelemetry-logs-go/exporters/otlp/otlplogs/otlplogshttp"
	otelLogs "github.com/agoda-com/opentelemetry-logs-go/logs"
	sdk "github.com/agoda-com/opentelemetry-logs-go/sdk/logs"
	"github.com/codifinary/codexray-node-agent/common"
	"github.com/codifinary/codexray-node-agent/flags"
	"github.com/codifinary/logparser"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.18.0"
	"k8s.io/klog/v2"
)

var (
	otelLogger        otelLogs.Logger
	otelLoggerDefault otelLogs.Logger
)

func Init(machineId, hostname, version string) {
	endpointUrl := *flags.LogsEndpoint
	if endpointUrl == nil {
		klog.Infoln("no OpenTelemetry logs collector endpoint configured")
		return
	}
	klog.Infoln("OpenTelemetry logs collector endpoint:", endpointUrl.String())
	urlPath := endpointUrl.Path
	if urlPath == "" {
		urlPath = "/"
	}

	resourceAttrs := resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceName("codexray-node-agent"),
		semconv.HostName(hostname),
		semconv.HostID(machineId),
	)

	// Regular logger provider (uses API_KEY)
	opts := []otlplogshttp.Option{
		otlplogshttp.WithEndpoint(endpointUrl.Host),
		otlplogshttp.WithURLPath(urlPath),
		otlplogshttp.WithHeaders(common.AuthHeaders()),
		otlplogshttp.WithTLSClientConfig(&tls.Config{InsecureSkipVerify: *flags.InsecureSkipVerify}),
	}
	if endpointUrl.Scheme != "https" {
		opts = append(opts, otlplogshttp.WithInsecure())
	}
	client := otlplogshttp.NewClient(opts...)
	exporter, _ := otlplogs.NewExporter(context.Background(), otlplogs.WithClient(client))

	loggerProvider := sdk.NewLoggerProvider(
		sdk.WithBatcher(exporter),
		sdk.WithResource(resourceAttrs),
	)
	otel.SetLoggerProvider(loggerProvider)
	otelLogger = loggerProvider.Logger("codexray-node-agent", otelLogs.WithInstrumentationVersion(version))

	// Default logger provider (uses DEFAULT_API_KEY for codexray containers)
	defaultOpts := []otlplogshttp.Option{
		otlplogshttp.WithEndpoint(endpointUrl.Host),
		otlplogshttp.WithURLPath(urlPath),
		otlplogshttp.WithHeaders(common.AuthHeadersForContainer("codexray")),
		otlplogshttp.WithTLSClientConfig(&tls.Config{InsecureSkipVerify: *flags.InsecureSkipVerify}),
	}
	if endpointUrl.Scheme != "https" {
		defaultOpts = append(defaultOpts, otlplogshttp.WithInsecure())
	}
	defaultClient := otlplogshttp.NewClient(defaultOpts...)
	defaultExporter, _ := otlplogs.NewExporter(context.Background(), otlplogs.WithClient(defaultClient))

	defaultLoggerProvider := sdk.NewLoggerProvider(
		sdk.WithBatcher(defaultExporter),
		sdk.WithResource(resourceAttrs),
	)
	otelLoggerDefault = defaultLoggerProvider.Logger("codexray-node-agent", otelLogs.WithInstrumentationVersion(version))
}

func OtelLogEmitter(containerId string) logparser.OnMsgCallbackF {
	if otelLogger == nil {
		return nil
	}

	// Choose the appropriate logger based on service name
	serviceName := common.ContainerIdToOtelServiceName(containerId)
	logger := otelLogger
	apiKeyType := "REGULAR"
	if strings.Contains(serviceName, "codexray") && otelLoggerDefault != nil {
		logger = otelLoggerDefault
		apiKeyType = "DEFAULT"
	}
	logsApiKey := common.AuthHeadersForServiceName(serviceName)["X-Api-Key"]
	klog.Infof("[type=logs] emitter created for container_id=%s service_name=%s api_key_type=%s X-Api-Key=%s", containerId, serviceName, apiKeyType, logsApiKey)

	return func(ts time.Time, level logparser.Level, patternHash string, msg string) {
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

		logger.Emit(
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
