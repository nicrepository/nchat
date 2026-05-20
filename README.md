# NChat

NChat e um chat corporativo interno para comunicacao segura, rastreavel e operavel dentro da organizacao.

## Status

MVP em desenvolvimento.

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
- ambientes staging/prod.

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

Esses manifests nao sao de producao. Secrets reais nao sao versionados; `infra/k8s/base/secrets.example.yaml` e apenas modelo e nao entra no kustomization. O overlay `k3s-dev` assume Traefik disponivel no k3s e expoe `nchat.local` sem TLS real. TLS/cert-manager, Sealed Secrets, ArgoCD, Dockerfiles, build/push de imagens e data services em K8s entram em tarefas futuras.

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
| media-service        |         8087 | /healthz, /readyz, /version |
