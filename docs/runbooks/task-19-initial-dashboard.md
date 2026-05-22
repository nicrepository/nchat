# TASK-19 — Dashboard inicial: serviços up/down, latência e erros

## Status

Concluído — PR aberto para `develop`.

## Objetivo

Criar o dashboard inicial de observabilidade do NChat no Grafana para monitoramento
operacional básico dos serviços Go.

## Dependência

- **TASK-18**: Prometheus, Grafana, Jaeger e `/metrics` em todos os serviços Go.

## Entregas

| Artefato                                                               | Descrição                              |
| ---------------------------------------------------------------------- | -------------------------------------- |
| `infra/observability/grafana/dashboards/nchat-overview.json`           | Dashboard JSON provisionado            |
| `infra/observability/grafana/provisioning/dashboards/dashboards.yml`   | Config de provisioning de dashboards   |
| `infra/observability/grafana/provisioning/datasources/datasources.yml` | Datasources com UIDs explícitos        |
| `infra/compose/compose.dev.yml`                                        | Volume `dashboards` montado no Grafana |
| `scripts/ci/grafana-dashboard-check.sh`                                | Script CI de validação                 |

## Painéis

| Seção          | Painel                        | Tipo        |
| -------------- | ----------------------------- | ----------- |
| Service health | Services up                   | Stat        |
| Service health | Service inventory             | Table       |
| Traffic        | HTTP request rate by service  | Time series |
| Traffic        | HTTP request rate by status   | Time series |
| Latency        | HTTP latency p95 by service   | Time series |
| Latency        | HTTP latency p50 by service   | Time series |
| Latency        | HTTP latency p99 by service   | Time series |
| Errors         | 5xx error rate by service     | Time series |
| Errors         | 4xx response rate by service  | Time series |
| Errors         | Error ratio 5xx               | Stat        |
| Concurrency    | In-flight requests by service | Time series |
| Tracing        | Jaeger instructions           | Text        |

## PromQL principal

```promql
# Status dos serviços
up{job="nchat-go-services"}

# Inventário
nchat_service_info

# Request rate
sum by (service) (rate(nchat_http_requests_total[5m]))
sum by (service, status) (rate(nchat_http_requests_total[5m]))

# Latência
histogram_quantile(0.95, sum by (le, service) (rate(nchat_http_request_duration_seconds_bucket[5m])))
histogram_quantile(0.50, sum by (le, service) (rate(nchat_http_request_duration_seconds_bucket[5m])))
histogram_quantile(0.99, sum by (le, service) (rate(nchat_http_request_duration_seconds_bucket[5m])))

# Erros
sum by (service) (rate(nchat_http_requests_total{status=~"5.."}[5m]))
sum by (service) (rate(nchat_http_requests_total{status=~"4.."}[5m]))
sum(rate(nchat_http_requests_total{status=~"5.."}[5m])) / clamp_min(sum(rate(nchat_http_requests_total[5m])), 1)

# In-flight
sum by (service) (nchat_http_in_flight_requests)
```

## Como validar localmente

### 1. Subir stack de observabilidade

```bash
cp infra/compose/.env.dev.example infra/compose/.env.dev   # se necessário
make dev-observability-up
make dev-observability-status
```

### 2. Subir auth-service com métricas e traces

```bash
cd services/auth-service
PORT=8081 \
  PROMETHEUS_METRICS_ENABLED=true \
  OTEL_ENABLED=true \
  OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318 \
  go run ./cmd/auth-service
```

### 3. Gerar tráfego

```bash
for i in {1..30}; do
  curl -s http://localhost:8081/healthz >/dev/null
  curl -s http://localhost:8081/readyz >/dev/null
  curl -s http://localhost:8081/version >/dev/null
done
```

### 4. Validar métricas no Prometheus

```bash
# Serviços online
curl -sG 'http://localhost:9090/api/v1/query' \
  --data-urlencode 'query=up{job="nchat-go-services"}' | python3 -m json.tool

# Service info
curl -sG 'http://localhost:9090/api/v1/query' \
  --data-urlencode 'query=nchat_service_info' | python3 -m json.tool

# Request rate
curl -sG 'http://localhost:9090/api/v1/query' \
  --data-urlencode 'query=sum by (service) (rate(nchat_http_requests_total[5m]))' | python3 -m json.tool

# Latência p95
curl -sG 'http://localhost:9090/api/v1/query' \
  --data-urlencode 'query=histogram_quantile(0.95, sum by (le, service) (rate(nchat_http_request_duration_seconds_bucket[5m])))' | python3 -m json.tool
```

### 5. Abrir Grafana

- URL: http://localhost:3000
- Login: `admin` / `admin`
- Pasta: **NChat**
- Dashboard: **NChat Overview**

### 6. Verificar via API Grafana

```bash
# Listar dashboards (substitua GF_USER e GF_PASSWORD pelas credenciais configuradas)
curl -s -u "${GF_USER}:${GF_PASSWORD}" 'http://localhost:3000/api/search?query=NChat' | python3 -m json.tool

# Obter dashboard por UID
curl -s -u "${GF_USER}:${GF_PASSWORD}" 'http://localhost:3000/api/dashboards/uid/nchat-overview' | python3 -m json.tool
```

### 7. Validar CI (sem containers)

```bash
make grafana-dashboard-check
make observability-config-check
make ci
```

## Limitações

- **Sem alertas** — Alertmanager e regras de alerta serão adicionados em tarefa futura.
- **Sem logs** — Loki ou OpenSearch serão adicionados em tarefa futura.
- **Sem dashboard de negócio** — MAU/DAU/storage em tarefa futura.
- **Sem SLO formal** — SLO/SLA serão definidos em tarefa futura.
- Serviços precisam estar em execução para aparecerem nos painéis de métricas.
- Prometheus scrape depende das portas locais (8081–8087 acessíveis via `host.docker.internal`).

## Segurança

- Nenhum dado sensível é exposto nos labels das métricas.
- Headers de autorização, cookies e request bodies não são registrados.
- Credenciais dev (`admin/admin`) são usadas apenas localmente.
- Grafana escuta em `127.0.0.1` — não exposto na rede.

## Definition of Done

- [x] Dashboard JSON criado (`nchat-overview.json`)
- [x] Provisioning de dashboards criado (`dashboards.yml`)
- [x] Datasources com UIDs explícitos (`prometheus`, `jaeger`)
- [x] Volume `dashboards` montado no Grafana (`compose.dev.yml`)
- [x] Dashboard aparece no Grafana na pasta NChat
- [x] Painéis sem erros de query (validado localmente)
- [x] CI `grafana-dashboard-check` criado e passando
- [x] README atualizado
- [x] Runbook criado
- [x] `make ci` passando
- [x] PR aberto para `develop`
