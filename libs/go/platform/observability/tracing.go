package observability

import (
	"context"
	"log/slog"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// ShutdownFunc shuts down a tracer provider, flushing pending spans.
type ShutdownFunc func(context.Context) error

// SetupTracing initializes the OpenTelemetry tracer provider.
// If cfg.TracingEnabled is false, a no-op shutdown is returned.
// If the OTLP exporter cannot be created, a warning is logged and a no-op
// shutdown is returned — the service will not fail to start.
func SetupTracing(ctx context.Context, cfg Config) (ShutdownFunc, error) {
	if !cfg.TracingEnabled {
		return noopShutdown, nil
	}

	opts := otlpEndpointOptions(cfg.OTLPEndpoint)
	exporter, err := otlptracehttp.New(ctx, opts...)
	if err != nil {
		slog.Warn("observability: tracing exporter init failed, using noop", "error", err)
		return noopShutdown, nil
	}

	res := resource.NewWithAttributes(
		"",
		attribute.String("service.name", cfg.ServiceName),
		attribute.String("service.namespace", cfg.ServiceNamespace),
		attribute.String("deployment.environment", cfg.Environment),
	)

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)
	otel.SetTracerProvider(tp)

	return tp.Shutdown, nil
}

// otlpEndpointOptions converts an endpoint URL to otlptracehttp options.
// Accepts both "host:port" and "http(s)://host:port" formats.
func otlpEndpointOptions(endpoint string) []otlptracehttp.Option {
	if endpoint == "" {
		return nil
	}
	if strings.HasPrefix(endpoint, "http://") {
		host := strings.TrimPrefix(endpoint, "http://")
		return []otlptracehttp.Option{
			otlptracehttp.WithEndpoint(host),
			otlptracehttp.WithInsecure(),
		}
	}
	if strings.HasPrefix(endpoint, "https://") {
		host := strings.TrimPrefix(endpoint, "https://")
		return []otlptracehttp.Option{otlptracehttp.WithEndpoint(host)}
	}
	return []otlptracehttp.Option{
		otlptracehttp.WithEndpoint(endpoint),
		otlptracehttp.WithInsecure(),
	}
}
