# NChat Observability Stack

Local observability stack for NChat development.

## Components

| Component  | URL                    | Purpose               |
| ---------- | ---------------------- | --------------------- |
| Prometheus | http://localhost:9090  | Metrics collection    |
| Grafana    | http://localhost:3000  | Metrics and traces UI |
| Jaeger     | http://localhost:16686 | Distributed traces UI |

## Starting the stack

```bash
make dev-observability-up
make dev-observability-status
make dev-observability-validate
```

## Stopping

```bash
make dev-observability-down
```

## Configuration

- **Prometheus**: `prometheus/prometheus.yml` — scrapes all Go services on `host.docker.internal`
- **Grafana**: `grafana/provisioning/datasources/datasources.yml` — Prometheus and Jaeger datasources
- **Jaeger**: OTLP collector enabled (gRPC :4317, HTTP :4318)

## Go service instrumentation

All services expose `/metrics` and send traces when:

```bash
PROMETHEUS_METRICS_ENABLED=true
OTEL_ENABLED=true
OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318
```

## Dashboards

Grafana dashboards will be added in **TASK-19**.

## Out of scope (this task)

- Alertmanager
- Loki / OpenSearch logs
- Production deployment
- Kubernetes / ServiceMonitor
- Complex dashboards
