# Tarefa 5 - Monorepo Go + Frontend React

## Status

- [x] Go workspace criado.
- [x] Lib Go compartilhada criada.
- [x] Servicos Go minimos criados.
- [x] Health endpoint por servico criado.
- [x] Frontend React + TypeScript + Vite criado.
- [x] pnpm workspace criado.
- [x] Scripts CI locais criados.
- [x] Makefile criado.
- [x] CI backend criado.
- [x] CI frontend criado.
- [x] Testes minimos criados.
- [x] Validacao local concluida.
- [x] PR aberto para `develop`: `https://github.com/nicrepository/nchat/pull/17`.

## Objetivo

Criar a base tecnica compilavel e testavel do monorepo NChat.

## Entregas

- Go workspace
- Lib Go compartilhada
- Servicos Go minimos
- Health endpoint por servico
- Frontend React + TypeScript + Vite
- pnpm workspace
- Scripts CI locais
- Makefile
- CI backend
- CI frontend
- Testes minimos

## Servicos Go criados

| Servico | Modulo | Porta |
| --- | --- | --- |
| `auth-service` | `github.com/nicrepository/nchat/services/auth-service` | 8081 |
| `chat-service` | `github.com/nicrepository/nchat/services/chat-service` | 8082 |
| `file-service` | `github.com/nicrepository/nchat/services/file-service` | 8083 |
| `notification-service` | `github.com/nicrepository/nchat/services/notification-service` | 8084 |
| `admin-service` | `github.com/nicrepository/nchat/services/admin-service` | 8085 |
| `search-service` | `github.com/nicrepository/nchat/services/search-service` | 8086 |
| `media-service` | `github.com/nicrepository/nchat/services/media-service` | 8087 |

## Frontend

- Stack: React, TypeScript, Vite, Vitest, Testing Library e ESLint.
- App: `apps/web`.
- Comandos:
  - `pnpm dev:web`
  - `pnpm lint:web`
  - `pnpm typecheck:web`
  - `pnpm test:web`
  - `pnpm build:web`

## Comandos de validacao

- `pnpm install`
- `pnpm lint:web`
- `pnpm typecheck:web`
- `pnpm test:web`
- `pnpm build:web`
- `bash scripts/ci/go-fmt-check.sh`
- `bash scripts/ci/go-vet.sh`
- `bash scripts/ci/go-test.sh`
- `make test`
- `make lint`
- `make build`

## Fora do escopo

- Auth real
- DB
- WebSocket
- LiveKit
- Docker
- Deploy

## Pendencias

- O host local estava com Node `v22.22.2`, enquanto o projeto exige Node 24. Os comandos frontend passaram com warning de engine; o CI usa Node 24.
- O host local nao tinha `go`/`gofmt` no PATH. Para validar localmente, foi usada uma toolchain Go 1.25.10 temporaria em `/tmp/nchat-go1.25.10/go/bin`.

## Definition of Done

- [x] Go workspace criado
- [x] Servicos Go minimos criados
- [x] Health endpoints testados
- [x] Frontend React criado
- [x] Testes frontend criados
- [x] Scripts CI criados
- [x] Makefile criado
- [x] CI backend criado
- [x] CI frontend criado
- [x] README atualizado
- [x] PR aberto para `develop`
