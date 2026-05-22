# Jaeger

Local Jaeger all-in-one instance for NChat distributed tracing.

## Access

- UI: http://localhost:16686
- OTLP gRPC: localhost:4317
- OTLP HTTP: localhost:4318

## Configuration

Jaeger all-in-one runs with `COLLECTOR_OTLP_ENABLED=true`.

Go services send traces via OTLP HTTP when `OTEL_ENABLED=true` and `OTEL_EXPORTER_OTLP_ENDPOINT` is set.

## Starting

```bash
make dev-observability-up
```

## Searching traces

1. Open http://localhost:16686
2. Select a service (e.g. `auth-service`) from the dropdown
3. Click **Find Traces**
