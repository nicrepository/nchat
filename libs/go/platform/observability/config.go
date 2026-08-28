// Package observability provides Prometheus metrics and OpenTelemetry tracing
// for NChat Go services.
//
// Observability is opt-in: services will not fail if Prometheus or Jaeger is
// unavailable. Enable via environment variables:
//
//	PROMETHEUS_METRICS_ENABLED=true
//	OTEL_ENABLED=true
//	OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318
package observability

import platformconfig "github.com/nicrepository/nchat/libs/go/platform/config"

// Config holds observability settings for a service.
type Config struct {
	ServiceName      string
	ServiceNamespace string
	MetricsEnabled   bool
	TracingEnabled   bool
	OTLPEndpoint     string
	OTLPProtocol     string
	Environment      string
}

// LoadConfig reads observability configuration from standard environment variables.
func LoadConfig(serviceName string) Config {
	return Config{
		ServiceName:      serviceName,
		ServiceNamespace: platformconfig.GetString("OTEL_SERVICE_NAMESPACE", "nchat"),
		MetricsEnabled:   platformconfig.GetBool("PROMETHEUS_METRICS_ENABLED", false),
		TracingEnabled:   platformconfig.GetBool("OTEL_ENABLED", false),
		OTLPEndpoint:     platformconfig.GetString("OTEL_EXPORTER_OTLP_ENDPOINT", "http://localhost:4318"),
		OTLPProtocol:     platformconfig.GetString("OTEL_EXPORTER_OTLP_PROTOCOL", "http/protobuf"),
		Environment:      platformconfig.GetString("APP_ENV", "development"),
	}
}
