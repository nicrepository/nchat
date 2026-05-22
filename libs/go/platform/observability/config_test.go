package observability

import "testing"

func TestLoadConfigDefaults(t *testing.T) {
	cfg := LoadConfig("test-service")

	if cfg.ServiceName != "test-service" {
		t.Fatalf("expected service name test-service, got %q", cfg.ServiceName)
	}
	if cfg.ServiceNamespace != "nchat" {
		t.Fatalf("expected namespace nchat, got %q", cfg.ServiceNamespace)
	}
	if cfg.MetricsEnabled {
		t.Fatal("expected MetricsEnabled to be false by default")
	}
	if cfg.TracingEnabled {
		t.Fatal("expected TracingEnabled to be false by default")
	}
	if cfg.OTLPEndpoint != "http://localhost:4318" {
		t.Fatalf("expected OTLP endpoint http://localhost:4318, got %q", cfg.OTLPEndpoint)
	}
	if cfg.OTLPProtocol != "http/protobuf" {
		t.Fatalf("expected OTLP protocol http/protobuf, got %q", cfg.OTLPProtocol)
	}
	if cfg.Environment != "development" {
		t.Fatalf("expected env development, got %q", cfg.Environment)
	}
}

func TestLoadConfigFromEnv(t *testing.T) {
	t.Setenv("PROMETHEUS_METRICS_ENABLED", "true")
	t.Setenv("OTEL_ENABLED", "true")
	t.Setenv("OTEL_SERVICE_NAMESPACE", "myns")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://jaeger:4318")
	t.Setenv("APP_ENV", "staging")

	cfg := LoadConfig("svc")

	if !cfg.MetricsEnabled {
		t.Fatal("expected MetricsEnabled to be true")
	}
	if !cfg.TracingEnabled {
		t.Fatal("expected TracingEnabled to be true")
	}
	if cfg.ServiceNamespace != "myns" {
		t.Fatalf("expected namespace myns, got %q", cfg.ServiceNamespace)
	}
	if cfg.OTLPEndpoint != "http://jaeger:4318" {
		t.Fatalf("expected OTLP endpoint http://jaeger:4318, got %q", cfg.OTLPEndpoint)
	}
	if cfg.Environment != "staging" {
		t.Fatalf("expected env staging, got %q", cfg.Environment)
	}
}
