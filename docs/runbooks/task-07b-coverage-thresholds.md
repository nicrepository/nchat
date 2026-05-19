# TASK-07B — Coverage thresholds >90%

## Status

Validado localmente na branch `chore/task-007b-coverage-thresholds`.

## Objetivo

Elevar os thresholds minimos de coverage para maior que 90%.

## Politica de coverage

### Frontend

- lines >= 90%
- functions >= 90%
- branches >= 90%
- statements >= 90%

### Go

- total statement coverage >= 90% por modulo Go.

## Decisoes

- Usar 90% como threshold minimo conforme decisao desta tarefa.
- Vitest aplica thresholds para lines/functions/branches/statements.
- Go aplica statement coverage via `go test -coverprofile` e `go tool cover -func`.
- O check Go mede pacotes de biblioteca e `internal` por modulo.
- Pacotes `cmd` ficam fora do threshold unitario porque sao entrypoints de processo; o
  relatorio bruto de `pnpm test:coverage:go` continua incluindo esses pacotes.
- Coverage gerado fica em `coverage/` e nao e versionado.

## Comandos

- `pnpm test:coverage:web`
- `pnpm test:coverage:go`
- `pnpm test:coverage:go:check`
- `pnpm test:coverage`
- `make coverage`
- `make go-coverage-check`
- `make ci`

## Resultado validado

### Frontend

- statements: 100%
- branches: 100%
- functions: 100%
- lines: 100%

### Go

- `libs/go/platform`: 93.8%
- `services/admin-service`: 100.0%
- `services/auth-service`: 100.0%
- `services/chat-service`: 100.0%
- `services/file-service`: 100.0%
- `services/media-service`: 100.0%
- `services/notification-service`: 100.0%
- `services/search-service`: 100.0%

## Fora do escopo

- E2E tests
- Playwright
- SonarQube
- Mutation testing
- Cobertura de integracao com containers

## Definition of Done

- [x] Vitest thresholds elevados para 90%
- [x] Go threshold check criado
- [x] Scripts root atualizados
- [x] Makefile atualizado
- [x] CI Quality atualizado
- [x] Testes ajustados/adicionados
- [x] README atualizado
- [x] Runbook criado
- [x] make coverage passa
- [x] make ci passa
- [ ] PR aberto
