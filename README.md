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

Portas padrao dos servicos:

- `auth-service`: 8081
- `chat-service`: 8082
- `file-service`: 8083
- `notification-service`: 8084
- `admin-service`: 8085
- `search-service`: 8086
- `media-service`: 8087
