# Tarefa 7 — Lint, formatter e testes base

## Status

Concluida na branch `chore/task-007-lint-format-tests` com validacao local executada.

Validacoes executadas:

- `pnpm install`
- `pnpm format`
- `pnpm format:check`
- `pnpm lint`
- `pnpm typecheck:web`
- `pnpm test`
- `pnpm test:coverage`
- `pnpm build`
- `pnpm run ci`
- `make format-check`
- `make lint`
- `make test`
- `make coverage`
- `make build`
- `make ci`

Observacao local: o ambiente desta maquina esta com Node `v22.22.2`, abaixo do alvo do
projeto (`24.x`), portanto o pnpm emitiu warning de engine. Os workflows configuram Node 24.

## Objetivo

Padronizar qualidade de codigo, formatacao e testes do monorepo.

## Ferramentas

- gofmt
- go vet
- go test
- golangci-lint
- ESLint
- Prettier
- TypeScript
- Vitest
- coverage v8

## Comandos locais

- `make format`
- `make format-check`
- `make lint`
- `make test`
- `make coverage`
- `make build`
- `make ci`

## Go

- `scripts/ci/go-fmt-check.sh`: valida `gofmt` por modulo Go.
- `scripts/ci/go-vet.sh`: executa `go vet ./...` por modulo Go.
- `scripts/ci/go-test.sh`: executa `go test ./...` por modulo Go.
- `scripts/ci/go-lint.sh`: executa `golangci-lint run ./...` por modulo Go.
- `scripts/ci/go-coverage.sh`: gera coverage por modulo em `coverage/go`.
- `scripts/ci/go-coverage-check.sh`: aplica threshold de statement coverage por modulo.
- `.golangci.yml`: habilita um conjunto inicial de linters pragmatico.

O `gosec` permanece ativo. Apenas `G104` foi excluido porque pode gerar ruido inicial em
writes HTTP simples; `errcheck` continua ativo para preservar a verificacao de erros.

## Frontend

- ESLint usa flat config para TypeScript, React hooks e React refresh.
- `eslint-config-prettier` desativa conflitos entre ESLint e Prettier.
- Prettier roda no root para web, docs, YAML e JSON.
- TypeScript e validado por `pnpm typecheck:web`.
- Vitest usa `jsdom`, `src/setupTests.ts` e coverage com provider `v8`.
- Thresholds de coverage web:
  - lines: 90%
  - functions: 90%
  - branches: 90%
  - statements: 90%

O coverage Go exige statement coverage total >= 90% por modulo para pacotes de biblioteca
e `internal`. Pacotes `cmd` ficam fora do threshold unitario porque inicializam processos e
servidores; o relatorio bruto de `scripts/ci/go-coverage.sh` continua incluindo esses
entrypoints.

## CI

- Backend: `gofmt`, `go vet`, `go test` e `golangci-lint`.
- Frontend: install frozen, Prettier check, ESLint, TypeScript, Vitest, coverage e build.
- Quality: check agregado para formatacao, lint, typecheck, testes e build.
- Repository governance: permanece separado e sem alteracao de branch protection nesta tarefa.

## Politica

- Nenhum PR deve ser mergeado se format/lint/test/build falhar.
- Nenhuma regra deve ser desabilitada sem justificativa.
- Coverage thresholds iniciais podem ser elevados progressivamente.
- Security scanners avancados entram em tarefa futura.

## Pendencias

- Adicionar o check `Quality` como required check manualmente em tarefa futura de governanca.

## Definition of Done

- [x] golangci-lint configurado
- [x] gofmt check configurado
- [x] go vet/test padronizados
- [x] go coverage criado
- [x] ESLint validado
- [x] Prettier configurado
- [x] Vitest coverage configurado
- [x] Scripts root atualizados
- [x] Makefile atualizado
- [x] CI atualizado
- [x] README atualizado
- [x] Runbook criado
- [x] PR aberto
