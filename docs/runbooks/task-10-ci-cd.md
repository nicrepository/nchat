# TASK-10 - GitHub Actions/GitLab CI

## Status

Validado localmente na branch `chore/task-010-ci-cd-pipelines`; PR pendente no momento deste runbook.

## Objetivo

Consolidar CI do NChat com GitHub Actions e criar pipeline equivalente para GitLab CI.

## GitHub Actions

Workflows mantidos ou adicionados:

- Repository governance: valida arquivos obrigatorios e ausencia de `.env` versionado.
- Backend: valida `gofmt`, `go vet`, `go test`, coverage Go e `golangci-lint`.
- Frontend: valida Prettier, ESLint, TypeScript, Vitest coverage e build web.
- Quality: check agregado local equivalente a `pnpm run ci`.
- CI: workflow agregado para metadata, quality, backend, frontend e manifests.
- Security: secret scan, `govulncheck`, Trivy filesystem e Trivy config.

## GitLab CI

`.gitlab-ci.yml` e um template equivalente para futuro mirror GitLab. Ele cobre stages de governance, quality, backend, frontend, manifests e security.

Nao ha deploy nesta tarefa. O pipeline GitLab nao faz `kubectl apply`, nao faz build/push de imagens e nao depende de secrets reais.

## Gates obrigatorios

- format
- lint
- typecheck
- test
- coverage
- build
- dev env config check
- k8s manifests check
- govulncheck
- secret scan
- Trivy fs/config

## Seguranca

- Sem secrets no repo.
- Sem deploy automatico.
- Sem kubeconfig.
- Sem tokens.
- Security scan roda em PR, push e schedule semanal no GitHub Actions.
- Trivy esta configurado para falhar em vulnerabilidades HIGH/CRITICAL.

## Required checks recomendados

Apos merge e validacao, considerar na branch protection:

- Repository governance
- Quality
- CI / Quality ou CI agregado
- Security

Nao alterar branch protection nesta tarefa.

## Limitacoes

- Sem deploy.
- Sem build/push de imagens.
- Sem ArgoCD.
- Sem GitLab Runner ativo neste momento.
- Sem validacao GitLab remota se nao houver projeto GitLab.
- GitLab usa imagens publicas oficiais como template para futuro mirror.

## Comandos

```bash
make ci
make security
make ci-config-check
pnpm run ci
pnpm security
pnpm ci:config-check
```

## Validacao local

Executado nesta maquina:

- `node -v`: `v22.22.2` (abaixo do alvo `24.x`; pnpm emitiu warning de engine).
- `pnpm -v`: `11.1.2`.
- `go version`: `go1.25.9 linux/amd64`.
- `golangci-lint version`: `2.12.2`.
- `pnpm install`.
- `pnpm ci:config-check`.
- `pnpm format:check`.
- `pnpm lint`.
- `pnpm typecheck:web`.
- `pnpm test`.
- `pnpm test:coverage`.
- `pnpm build`.
- `pnpm run ci`.
- `make ci-config-check`.
- `make ci`.

Observacoes locais:

- `ruby`, `actionlint` e `gitlab-ci-lint` nao estavam instalados; `ci-config-check` registrou skip controlado.
- `yamllint` estava instalado e validou os YAMLs com regras voltadas a sintaxe/estrutura.
- `govulncheck`, `trivy` e `gitleaks` nao estavam instalados localmente; `pnpm security` e `make security` nao foram executados nesta maquina.
- O workflow `Security` instala/usa essas ferramentas no GitHub Actions.
- `pnpm k8s:ci` renderizou manifests e pulou apenas `kubectl apply --dry-run=client` porque nao havia API server Kubernetes local acessivel.

## Definition of Done

- [x] `ci.yml` criado
- [x] `security.yml` criado
- [x] `.gitlab-ci.yml` criado
- [x] scripts security criados
- [x] script config check criado
- [x] `package.json` atualizado
- [x] `Makefile` atualizado
- [x] Dependabot configurado
- [x] README atualizado
- [x] runbook criado
- [x] `make ci` passa
- [ ] PR aberto
- [ ] checks passam
