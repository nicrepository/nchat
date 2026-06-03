# TASK-18 — Configurar Prometheus, Grafana e Jaeger

## Status

Concluído. PR aberto para develop.

## Objetivo

Configurar a base de observabilidade do NChat com Prometheus, Grafana OSS e Jaeger all-in-one, incluindo instrumentação mínima dos serviços Go.

## Componentes

| Componente     | Tecnologia                | Versão  |
| -------------- | ------------------------- | ------- |
| Métricas       | Prometheus                | v2.54.1 |
| Dashboards     | Grafana OSS               | 11.4.0  |
| Tracing UI     | Jaeger all-in-one         | 1.62.0  |
| Instrumentação | OpenTelemetry SDK (Go)    | v1.43.0 |
| Métricas Go    | prometheus/client_golang  | v1.22.0 |
| Exporter       | OTLP HTTP (otlptracehttp) | v1.43.0 |

## Escopo desta tarefa

- Docker Compose local com profile `observability`
- `infra/observability/prometheus/prometheus.yml` — scrape config
- `infra/observability/grafana/provisioning/datasources/datasources.yml` — Prometheus + Jaeger datasources
- `libs/go/platform/observability/` — pacote Go compartilhado
- `/metrics` em todos os 7 serviços Go
- Spans básicos por request HTTP
- Scripts dev `scripts/dev/dev-observability-*.sh`
- CI config check `scripts/ci/observability-config-check.sh`
- `make ci` e `pnpm run ci` passando

## Fora do escopo

- Dashboards detalhados (TASK-19)
- Alertmanager
- Loki / OpenSearch logs
- Produção / Kubernetes real
- ServiceMonitor / Prometheus Operator
- SLOs
- Load test

## Serviços instrumentados

| Serviço              | Porta | /metrics                      |
| -------------------- | ----- | ----------------------------- |
| auth-service         | 8081  | http://localhost:8081/metrics |
| chat-service         | 8082  | http://localhost:8082/metrics |
| file-service         | 8083  | http://localhost:8083/metrics |
| notification-service | 8084  | http://localhost:8084/metrics |
| admin-service        | 8085  | http://localhost:8085/metrics |
| search-service       | 8086  | http://localhost:8086/metrics |
| media-service        | 8087  | http://localhost:8087/metrics |

## Métricas mínimas

| Métrica                               | Tipo      | Descrição                                  |
| ------------------------------------- | --------- | ------------------------------------------ |
| `nchat_service_info`                  | Gauge     | Sempre presente — build info do serviço    |
| `nchat_http_requests_total`           | Counter   | Requires `PROMETHEUS_METRICS_ENABLED=true` |
| `nchat_http_request_duration_seconds` | Histogram | Requires `PROMETHEUS_METRICS_ENABLED=true` |
| `nchat_http_in_flight_requests`       | Gauge     | Requires `PROMETHEUS_METRICS_ENABLED=true` |

## Tracing

| Env var                       | Padrão                  | Descrição                         |
| ----------------------------- | ----------------------- | --------------------------------- |
| `OTEL_ENABLED`                | `false`                 | Habilita tracing                  |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `http://localhost:4318` | Endpoint OTLP HTTP                |
| `OTEL_SERVICE_NAMESPACE`      | `nchat`                 | Namespace do serviço              |
| `APP_ENV`                     | `development`           | Ambiente (deployment.environment) |

Resource attributes: `service.name`, `service.namespace`, `deployment.environment`.

## Pacote Go `libs/go/platform/observability`

```
observability/
├── config.go      — Config + LoadConfig
├── metrics.go     — Metrics + NewMetrics + Handler
├── tracing.go     — SetupTracing + otlpEndpointOptions
├── middleware.go  — HTTPMiddleware + responseWriter
├── noop.go        — noopShutdown
├── config_test.go
├── metrics_test.go
├── tracing_test.go
└── middleware_test.go
```

## Como validar localmente

```bash
# Subir stack
make dev-observability-up
make dev-observability-status

# Validar saúde
make dev-observability-validate

# Iniciar um serviço
cd services/auth-service
PORT=8081 PROMETHEUS_METRICS_ENABLED=true OTEL_ENABLED=true \
  OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318 \
  go run ./cmd/auth-service

# Verificar métricas
curl http://localhost:8081/metrics

# Abrir UIs
# Prometheus: http://localhost:9090
# Grafana:    http://localhost:3000
# Jaeger:     http://localhost:16686
```

## Segurança

- Interfaces observability ligadas apenas em `127.0.0.1` no dev compose.
- Authorization, Cookie, Set-Cookie e request body nunca são registrados.
- Labels de métricas evitam alta cardinalidade.
- Credenciais Grafana no `.env.dev.example` são dev-only.
- `/metrics` deve ser protegido em staging/produção (network policy ou autenticação).
- Hardening futuro de `/metrics` documentado em SECURITY.md.

## Limitações

- Sem dashboards detalhados (TASK-19).
- Sem alertas (Alertmanager — tarefa futura).
- Sem logs centralizados (Loki/OpenSearch — tarefa futura).
- Sem produção / Kubernetes real nesta tarefa.
- Sem Prometheus Operator / ServiceMonitor nesta tarefa.

## Definition of Done

- [x] Prometheus configurado e scrape config criado
- [x] Grafana configurado com datasources provisionados
- [x] Jaeger configurado com OTLP habilitado
- [x] `/metrics` em todos os 7 serviços Go
- [x] Tracing básico configurado (opt-in via `OTEL_ENABLED`)
- [x] Pacote `libs/go/platform/observability` criado com testes
- [x] Scripts dev criados (`up`, `down`, `status`, `logs`, `validate`)
- [x] CI config check criado e passando
- [x] README atualizado com seção Observability
- [x] SECURITY.md atualizado
- [x] Runbook criado
- [x] `make ci` passando
- [x] PR aberto para develop
