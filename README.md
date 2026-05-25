# NChat

NChat e um chat corporativo interno para comunicacao segura, rastreavel e operavel dentro da organizacao.

## Status

MVP em desenvolvimento.

## Architecture decisions

As decisões arquiteturais do NChat são registradas como ADRs em `docs/adr/`.

- [ADR-0001 — Estratégia de repositório](docs/adr/0001-repository-strategy.md)
- [ADR-0002 — Decisão provisória da stack tecnológica](docs/adr/0002-provisional-stack-decision.md)
- [Stack provisória consolidada](docs/architecture/provisional-stack.md)

A stack atual está registrada no ADR-0002. Destaques:

- SeaweedFS é **decisão provisória** até Sprint 3, quando será confirmado ou substituído.
- Valkey 8 foi **validado no Sprint 0** para Pub/Sub, Streams, locks, TTL e sliding window.
- Chamadas de áudio/vídeo (LiveKit) estão fora do MVP conforme requisitos v5.0; integram na V1.0.
- Próximas decisões críticas serão registradas via ADR antes de serem implementadas.

## Project tracking

O cronograma macro do NChat está na planilha `Cronograma NChat.ods`. O controle operacional
ocorre no **GitHub Project "NChat MVP — 17 Ago 2026"** (Project #2).

- Cada tarefa do cronograma tem uma issue com prefixo `[TASK-XX]`.
- PRs devem referenciar a issue com `Closes #N` ou `Refs #N`.
- RFs são rastreados por épicos e tarefas — não individualmente como issues.
- Detalhes: [docs/runbooks/task-gov-project-sync.md](docs/runbooks/task-gov-project-sync.md)

## Stack planejada

- Frontend: React, TypeScript
- Backend: Go
- Banco de dados: PostgreSQL
- Cache e filas leves: Valkey
- Arquivos: SeaweedFS
- Antimalware: ClamAV
- Realtime: WebSocket
- Notificacoes: servico dedicado
- Voz MVP: LiveKit
- Observabilidade: logs estruturados, metricas e traces

## Branch Strategy

- `main`: codigo estavel e pronto para release.
- `develop`: integracao principal do MVP.
- `feature/*`, `fix/*`, `chore/*` e `security/*`: trabalho diario com Pull Request para `develop`.
- `release/*`: estabilizacao antes de promover para `main`.
- `hotfix/*`: correcao urgente a partir de `main`, com backport para `develop`.

## Contribuicao interna

Este repositorio sera desenvolvido inicialmente por uma pessoa com apoio de IAs. Mesmo assim, toda mudanca deve manter rastreabilidade:

- criar branch de trabalho com nome padronizado;
- usar Conventional Commits;
- abrir Pull Request;
- preencher o template de PR;
- validar testes, build, lint e seguranca aplicaveis;
- documentar decisoes relevantes em ADR.

## Seguranca

Nao commitar secrets. Arquivos `.env`, tokens, senhas, chaves privadas, certificados privados, dumps reais e logs sensiveis devem permanecer fora do Git. Use somente arquivos de exemplo quando necessario.

## Development bootstrap

Versoes base:

- Node 24
- pnpm 11
- Go 1.25

Instalacao:

```bash
corepack enable
pnpm install
```

Rodar o frontend:

```bash
make dev-web
```

Validacao:

```bash
make test
make test-go
make test-web
```

## Quality gates

Comandos locais:

```bash
make format
make format-check
make lint
make test
make coverage
make go-coverage-check
make web-coverage
make build
make ci
```

Todo PR deve passar por formatacao, lint, typecheck, testes e build antes de merge. Codigo
Go deve passar por `gofmt`, `go vet`, `go test` e `golangci-lint`. O frontend deve passar
por ESLint, Prettier, TypeScript e Vitest.

O coverage minimo do frontend e:

- lines >= 90%
- functions >= 90%
- branches >= 90%
- statements >= 90%

O coverage minimo Go e statement coverage total >= 90% por modulo Go para pacotes de
biblioteca e `internal`. Pacotes `cmd` ficam fora do threshold unitario porque sao
entrypoints de processo; o relatorio bruto de `pnpm test:coverage:go` continua incluindo
esses pacotes.

## CI/CD

GitHub Actions e o pipeline principal do NChat. GitLab CI foi adicionado como espelho futuro para um mirror GitLab, sem deploy automatico nesta etapa.

Workflows GitHub Actions:

- `Governance`: valida governanca basica do repositorio.
- `Backend`: valida formatacao, vet, testes, coverage e lint Go.
- `Frontend`: valida formatacao, lint, typecheck, testes, coverage e build web.
- `Quality`: executa o gate agregado local.
- `CI`: agrega metadata, quality, backend, frontend e manifests para facilitar required checks futuros.
- `Security`: executa secret scan, `govulncheck` e Trivy em PR, push e schedule semanal.

Comandos locais:

```bash
make ci
make security
make ci-config-check
```

Equivalentes pnpm:

```bash
pnpm run ci
pnpm security
pnpm ci:config-check
```

Ainda nao existe nesta etapa:

- deploy automatico;
- ArgoCD;
- build/push de imagens;
- ambientes staging/prod reais.

## Local development infrastructure

Prerequisites:

- Docker
- Docker Compose v2 (`docker compose`)

Start the local data services:

```bash
cp infra/compose/.env.dev.example infra/compose/.env.dev
make dev-env-up
make dev-env-validate
```

Stop services without deleting data:

```bash
make dev-env-down
```

Reset local volumes:

```bash
make dev-env-reset
```

Services and default ports:

| Service          | Local endpoint          |
| ---------------- | ----------------------- |
| PostgreSQL       | `localhost:5432`        |
| Valkey           | `localhost:6379`        |
| SeaweedFS master | `http://localhost:9333` |
| SeaweedFS volume | `http://localhost:8088` |
| SeaweedFS filer  | `http://localhost:8888` |
| SeaweedFS S3     | `http://localhost:8333` |

This environment is only for local development. Do not use the example passwords in
production. Patroni, real HA, TLS, scheduled backup/restore, Traefik/Nginx and production
hardening are outside this local stack. SeaweedFS is provisional in Sprint 0 and depends on
full validation by the end of Sprint 3.

Portas padrao dos servicos:

- `auth-service`: 8081
- `chat-service`: 8082
- `file-service`: 8083
- `notification-service`: 8084
- `admin-service`: 8085
- `search-service`: 8086
- `media-service`: 8087

## Health and readiness

Os servicos Go expõem probes padronizadas para Kubernetes e operacao local. As respostas usam o envelope JSON compartilhado `{"data": ...}` e nao incluem secrets, DSNs, tokens, hostname interno, stack traces nem detalhes sensiveis de infraestrutura.

- `/healthz` e liveness. Ele confirma que o processo HTTP responde e nao verifica PostgreSQL, Valkey, SeaweedFS ou qualquer dependencia externa.
- `/readyz` e readiness. Ele indica se o servico pode receber trafego. Hoje executa apenas checks locais (`service-bootstrap` e `config-loaded`); checks reais de PostgreSQL, Valkey e SeaweedFS entram quando essas integracoes forem implementadas.
- Readiness retorna `503` quando um check critico falha. Falhas apenas em checks nao criticos resultam em `degraded` com HTTP `200`.

| Endpoint   | Purpose           | External dependencies                | Success            | Failure                                 |
| ---------- | ----------------- | ------------------------------------ | ------------------ | --------------------------------------- |
| `/healthz` | Process liveness  | No                                   | 200 ok             | 500 only on unexpected internal failure |
| `/readyz`  | Traffic readiness | Local checks now, dependencies later | 200 ready/degraded | 503 unready                             |

Exemplo `/healthz`:

```json
{
  "data": {
    "service": "auth-service",
    "probe": "liveness",
    "status": "ok",
    "version": "0.0.0",
    "commit": "dev",
    "checkedAt": "2026-05-21T12:00:00Z"
  }
}
```

Exemplo `/readyz`:

```json
{
  "data": {
    "service": "auth-service",
    "probe": "readiness",
    "status": "ready",
    "version": "0.0.0",
    "commit": "dev",
    "checkedAt": "2026-05-21T12:00:00Z",
    "checks": [
      {
        "name": "service-bootstrap",
        "status": "pass",
        "critical": true,
        "durationMs": 0
      },
      {
        "name": "config-loaded",
        "status": "pass",
        "critical": true,
        "durationMs": 0
      }
    ]
  }
}
```

## Local gateway

O gateway local usa Traefik como padrao. Nginx permanece alternativa futura. O Traefik roda pelo Docker Compose com o profile `gateway` e roteia para a web e os servicos Go executando no host.

Comandos:

```bash
make dev-gateway-up
make dev-gateway-status
make dev-gateway-validate
make dev-gateway-logs
make dev-gateway-down
```

Adicione no `/etc/hosts`:

```text
127.0.0.1 nchat.local
```

Rotas locais:

- `http://nchat.local:8080/`
- `http://nchat.local:8080/api/auth/healthz`
- `http://nchat.local:8080/api/chat/healthz`
- `http://nchat.local:8080/api/files/healthz`
- `http://nchat.local:8080/api/notifications/healthz`
- `http://nchat.local:8080/api/admin/healthz`
- `http://nchat.local:8080/api/search/healthz`
- `http://nchat.local:8080/api/media/healthz`
- Dashboard: `http://localhost:8090/dashboard/`

Avisos:

- Local only.
- HTTPS local e preferencial. HTTP permanece disponivel para compatibilidade de dev.
- Dashboard local exposto apenas em loopback.
- Nao usar configuracao em producao.
- Docker socket nao e montado.
- Web e servicos precisam estar rodando no host para as rotas responderem.

## TLS dev/staging

TLS local usa Traefik com certificados gerados fora do Git. O caminho preferencial e `mkcert`; se ele nao estiver instalado, o script usa `openssl` para gerar um certificado self-signed que o navegador nao confia automaticamente.

Prepare o host local:

```text
127.0.0.1 nchat.local
```

Gere o certificado e suba o gateway:

```bash
make dev-tls-generate
make dev-gateway-up
```

Endpoints locais:

- `http://nchat.local:8080`
- `https://nchat.local:8443`

Comandos uteis:

```bash
make dev-tls-status
make tls-config-check
make dev-tls-clean
```

O Traefik local configura `VersionTLS13` como minimo nos roteadores HTTPS quando suportado pela versao em uso. Certificados e chaves ficam em `infra/traefik/local/certs/` e nao devem ser versionados.

Staging inicial usa o overlay `infra/k8s/overlays/k3s-staging` com Ingress TLS para `staging.nchat.local` e Secret placeholder `nchat-staging-tls`. TLS publico real e cert-manager ficam para tarefas futuras.

## Sealed Secrets

Sealed Secrets permite versionar secrets apenas de forma criptografada para o cluster alvo. A politica do NChat exige escopo `strict` por padrao, owner por secret e rotacao manual documentada.

Comandos principais:

```bash
make sealed-secrets-install-controller
make sealed-secrets-validate
make sealed-secrets-fetch-cert
scripts/secrets/sealed-secrets-seal.sh \
  infra/k8s/secrets/unsealed/nchat-secrets.yaml \
  infra/k8s/secrets/sealed/nchat-secrets.sealed.yaml \
  nchat
make sealed-secrets-policy-check
```

Fluxo esperado:

1. Copiar um template de `infra/k8s/secrets/templates/` para `infra/k8s/secrets/unsealed/`.
2. Preencher valores reais apenas localmente.
3. Selar com `kubeseal --scope strict` via script.
4. Commitar apenas o `SealedSecret` em `infra/k8s/secrets/sealed/`.

Nunca commitar secrets unsealed, private keys, kubeconfig, certificados reais ou valores sensiveis em logs, issues ou PRs.

## Kubernetes/k3s manifests

Os manifests iniciais para Kubernetes/k3s ficam em `infra/k8s`. Eles cobrem dev/staging inicial com Kustomize base e overlay `k3s-dev`, usando imagens placeholder versionadas `0.0.0`.

Comandos principais:

```bash
make k8s-render
make k8s-validate
make k8s-apply-dev
make k8s-status-dev
make k8s-delete-dev
```

Esses manifests nao sao de producao. Secrets reais nao sao versionados; `infra/k8s/base/secrets.example.yaml` e apenas modelo e nao entra no kustomization. O overlay `k3s-dev` assume Traefik disponivel no k3s e expoe `nchat.local` sem TLS real. O overlay `k3s-staging` adiciona Ingress TLS placeholder para `staging.nchat.local` com `VersionTLS13` via Traefik TLSOption quando os CRDs existem. Cert-manager, ArgoCD, Dockerfiles, build/push de imagens, producao real e data services em K8s entram em tarefas futuras.

## Services base structure

Os servicos Go seguem uma estrutura interna padronizada:

- `cmd/`: entrada do binario do servico.
- `internal/app`: composicao da aplicacao, logger e handler HTTP.
- `internal/config`: configuracao do servico por ambiente com fallbacks seguros.
- `internal/http`: rotas, handlers HTTP, middlewares e respostas JSON.
- `internal/domain`: entidades e regras de dominio futuras.
- `internal/service`: casos de uso futuros.
- `internal/storage`: persistencia futura.
- `libs/go/platform`: utilitarios compartilhados para config, HTTP, logging e health.

| Service              | Default port | Current endpoints           |
| -------------------- | -----------: | --------------------------- |
| auth-service         |         8081 | /healthz, /readyz, /version |
| chat-service         |         8082 | /healthz, /readyz, /version |
| file-service         |         8083 | /healthz, /readyz, /version |
| notification-service |         8084 | /healthz, /readyz, /version |
| admin-service        |         8085 | /healthz, /readyz, /version |
| search-service       |         8086 | /healthz, /readyz, /version |
| media-service        |         8087 | /healthz, /readyz, /version |

## Auth data model

The initial PostgreSQL identity schema for `auth-service` creates the `auth` schema
model for users, credentials, invites, sessions, devices, login attempts, password reset
tokens, and policy settings.

- Architecture: [docs/architecture/auth-data-model.md](docs/architecture/auth-data-model.md)
- Runbook: [docs/runbooks/task-21-auth-postgres-models.md](docs/runbooks/task-21-auth-postgres-models.md)
- Migration: `migrations/auth/000001_auth_identity_schema.{up,down}.sql`

## Local infrastructure PoCs

> PoCs locais não são benchmark final de produção. Resultados servem para decisão técnica incremental.

### SeaweedFS

```bash
make poc-seaweedfs
```

- Usa Docker Compose local com perfil `seaweed-replication`.
- Sobe segundo volume server (`seaweed-volume-2`) para validar replicação básica.
- Valida upload/download pequeno e grande, integridade SHA-256 e latência.
- Gera resultados em `poc-results/seaweedfs/` (não versionado).

### Valkey

```bash
make poc-valkey
```

- Valida Pub/Sub, Streams (XADD/XREAD/XRANGE), SETNX lock, TTL/EXPIRE e sliding window.
- Mede latência básica das operações principais.
- Gera resultados em `poc-results/valkey/` (não versionado).

### CI-safe config check

```bash
make poc-config-check
```

Valida scripts, sintaxe, configurações e .gitignore sem subir containers.
Incluído automaticamente em `make ci`.

## Observability

NChat Go services expose Prometheus metrics at `/metrics` and send OpenTelemetry traces to Jaeger via OTLP HTTP. Observability is opt-in via environment variables.

### Local stack

Start the full observability stack (Prometheus, Grafana, Jaeger):

```bash
cp infra/compose/.env.dev.example infra/compose/.env.dev
make dev-observability-up
make dev-observability-status
make dev-observability-validate
```

| Service    | URL                    | Purpose                    |
| ---------- | ---------------------- | -------------------------- |
| Prometheus | http://localhost:9090  | Metrics scraping and query |
| Grafana    | http://localhost:3000  | Dashboards and datasources |
| Jaeger     | http://localhost:16686 | Distributed traces UI      |

Stop the stack (volumes are preserved):

```bash
make dev-observability-down
```

### Go service metrics

All services expose `/metrics` (Prometheus text format). Enable HTTP instrumentation:

```bash
PROMETHEUS_METRICS_ENABLED=true go run ./cmd/<service>
```

Metrics emitted:

| Metric                                | Type      | Labels                        |
| ------------------------------------- | --------- | ----------------------------- |
| `nchat_service_info`                  | Gauge     | service, version, commit, env |
| `nchat_http_requests_total`           | Counter   | service, method, path, status |
| `nchat_http_request_duration_seconds` | Histogram | service, method, path, status |
| `nchat_http_in_flight_requests`       | Gauge     | service                       |

### Go service tracing

Enable OpenTelemetry traces (sent via OTLP HTTP to Jaeger):

```bash
OTEL_ENABLED=true \
OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318 \
go run ./cmd/<service>
```

Services use span names `<METHOD> <path>` and record `http.method`, `http.route`, and `service.name` attributes. Authorization headers, Cookies, and request bodies are never recorded.

### CI config check

```bash
make observability-config-check
```

Validates Prometheus config, Grafana datasources, Compose config and security properties — no containers started. Included automatically in `make ci`.

### Dashboards

Grafana datasources (Prometheus and Jaeger) and the **NChat Overview** dashboard are provisioned
automatically on startup.

| Item      | Detail                                 |
| --------- | -------------------------------------- |
| Folder    | NChat                                  |
| Dashboard | NChat Overview (`uid: nchat-overview`) |
| URL       | http://localhost:3000                  |

The initial dashboard covers:

- **Service health** — `up` status and service inventory from `nchat_service_info`
- **Traffic** — request rate per service and per HTTP status code
- **Latency** — p50 / p95 / p99 histograms per service
- **Errors** — 4xx and 5xx rates; 5xx error-ratio stat panel
- **Concurrency** — in-flight requests per service
- **Tracing** — link/instructions to open Jaeger at http://localhost:16686

Validate dashboard provisioning files (no containers needed):

```bash
make grafana-dashboard-check
```

Alerts and product/business dashboards (MAU/DAU/storage) will be added in future tasks.

### Logs

Centralised log aggregation (Loki or OpenSearch) will be added in a future task.

## Weekly reviews

Weekly reviews consolidate deliveries, risks, decisions and recommended next steps.
The GitHub Project remains the operational source of truth. ADRs record architectural decisions.

| Review | Document                                                       |
| ------ | -------------------------------------------------------------- |
| Week 1 | [docs/reviews/week-1-review.md](docs/reviews/week-1-review.md) |
