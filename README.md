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
make build
make ci
```

Todo PR deve passar por formatacao, lint, typecheck, testes e build antes de merge. Codigo
Go deve passar por `gofmt`, `go vet`, `go test` e `golangci-lint`. O frontend deve passar
por ESLint, Prettier, TypeScript e Vitest.

O coverage minimo inicial do web e:

- lines: 60
- functions: 60
- branches: 50
- statements: 60

Portas padrao dos servicos:

- `auth-service`: 8081
- `chat-service`: 8082
- `file-service`: 8083
- `notification-service`: 8084
- `admin-service`: 8085
- `search-service`: 8086
- `media-service`: 8087

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
