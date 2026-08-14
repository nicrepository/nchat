# Contributing

Este documento define a governanca inicial de desenvolvimento do NChat.

## Branches principais

- `main`: codigo estavel/release.
- `develop`: integracao do MVP.

## Branches de trabalho

- `feature/<epic>-<id>-<descricao>`
- `fix/<epic>-<id>-<descricao>`
- `chore/<epic>-<id>-<descricao>`
- `security/<epic>-<id>-<descricao>`
- `hotfix/<id>-<descricao>`
- `release/<versao>`

## Fluxo

- Fluxo regular: `feature/* -> develop -> release/* -> main`.
- Hotfix: `hotfix/* -> main` e depois `hotfix/* -> develop`.
- Toda mudanca deve entrar por Pull Request.
- PRs devem ter escopo pequeno, revisao registrada e evidencias de validacao.

## Conventional Commits

Formato:

```text
<tipo>(<escopo opcional>): <descricao curta>
```

Tambem e aceito o marcador de breaking change `!`, por exemplo
`feat(api)!: change attachment contract` e `feat!: change public API`.

Tipos aceitos:

- `feat`
- `fix`
- `docs`
- `test`
- `refactor`
- `chore`
- `ci`
- `build`
- `security`
- `perf`

Exemplos:

```text
feat(chat): add message delivery contract
security(auth): harden session validation
docs(repo): document branch strategy
```

## Validacao pelo CI

- O CI valida nomes de branches de contribuicoes humanas conforme os formatos
  acima.
- Titulos de PR e commit subjects de trabalho devem seguir Conventional
  Commits; merge commits de sincronizacao com a branch base sao ignorados.
- PRs fora desses padroes nao podem ser mergeadas.
- Automacoes aprovadas podem ter formato proprio de branch, mas continuam
  sujeitas aos gates aplicaveis e a revisao humana.

## Definition of Done

- Codigo implementado.
- Testes criados/atualizados.
- Build passando.
- Lint passando.
- Analise de vulnerabilidades passando.
- Documentacao minima atualizada.
- Comportamento validado.
- PR revisado antes do merge.

## Regra de escopo

> Se nao esta no criterio de aceite do MVP ou no cronograma, nao entra agora.
