package observability

import (
	"context"
	"testing"
)

func TestSetupTracingDisabledReturnsNoop(t *testing.T) {
	cfg := Config{TracingEnabled: false}

	shutdown, err := SetupTracing(context.Background(), cfg)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if shutdown == nil {
		t.Fatal("expected non-nil shutdown func")
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("noop shutdown returned error: %v", err)
	}
}

func TestSetupTracingEnabledDoesNotFailOnStartup(t *testing.T) {
	// The OTLP exporter is lazy — it does not connect until first export.
	// SetupTracing must succeed even when no collector is running.
	cfg := Config{
		ServiceName:      "test-svc",
		ServiceNamespace: "test",
		Environment:      "test",
		TracingEnabled:   true,
		OTLPEndpoint:     "http://localhost:0", // nothing listening
	}

	shutdown, err := SetupTracing(context.Background(), cfg)
	if err != nil {
		t.Fatalf("expected no startup error, got %v", err)
	}
	if shutdown == nil {
		t.Fatal("expected non-nil shutdown func")
	}

	// Shutdown context with timeout to avoid blocking.
	ctx, cancel := context.WithTimeout(context.Background(), 0)
	defer cancel()
	// Ignore shutdown error — may fail because no collector accepted spans.
	_ = shutdown(ctx)
}

func TestOTLPEndpointOptionsHTTP(t *testing.T) {
	opts := otlpEndpointOptions("http://localhost:4318")
	if len(opts) != 2 {
		t.Fatalf("expected 2 options for http:// endpoint, got %d", len(opts))
	}
}

func TestOTLPEndpointOptionsHTTPS(t *testing.T) {
	opts := otlpEndpointOptions("https://collector:4318")
	if len(opts) != 1 {
		t.Fatalf("expected 1 option for https:// endpoint, got %d", len(opts))
	}
}

func TestOTLPEndpointOptionsHostPort(t *testing.T) {
	opts := otlpEndpointOptions("collector:4318")
	if len(opts) != 2 {
		t.Fatalf("expected 2 options for host:port endpoint, got %d", len(opts))
	}
}

func TestOTLPEndpointOptionsEmpty(t *testing.T) {
	opts := otlpEndpointOptions("")
	if opts != nil {
		t.Fatalf("expected nil options for empty endpoint, got %v", opts)
	}
}

func TestNoopShutdown(t *testing.T) {
	if err := noopShutdown(context.Background()); err != nil {
		t.Fatalf("noop shutdown returned error: %v", err)
	}
}
