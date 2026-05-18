# Manual GitHub Setup

Este runbook registra as acoes manuais necessarias quando a automacao via GitHub API nao estiver disponivel.

## Falha encontrada

Tentativa de aplicar branch protection via:

```bash
gh api -X PUT repos/nicrepository/nchat/branches/main/protection
gh api -X PUT repos/nicrepository/nchat/branches/develop/protection
```

Resultado para `main` e `develop`:

```text
HTTP 403
Upgrade to GitHub Pro or make this repository public to enable this feature.
```

`gh ruleset check main` e `gh ruleset check develop` tambem retornaram o mesmo HTTP 403.

O repositorio foi criado como privado por seguranca. Nao tornar publico sem confirmacao manual.

O workflow `Governance` ja apareceu no PR inicial como o check `Repository governance` e pode ser selecionado manualmente nas regras de protecao quando o recurso estiver disponivel.

## Protecao manual de `main`

1. Abrir `https://github.com/nicrepository/nchat/settings/branches`.
2. Selecionar **Add branch protection rule**.
3. Em **Branch name pattern**, informar `main`.
4. Habilitar **Require a pull request before merging**.
5. Habilitar **Require status checks to pass before merging**.
6. Selecionar o status check `Repository governance`.
7. Habilitar **Require branches to be up to date before merging**.
8. Habilitar **Require conversation resolution before merging**.
9. Habilitar **Require linear history** se a opcao estiver disponivel.
10. Habilitar **Include administrators** se a opcao estiver disponivel.
11. Habilitar **Block force pushes**.
12. Habilitar **Block deletions**.
13. Salvar a regra.

## Protecao manual de `develop`

1. Abrir `https://github.com/nicrepository/nchat/settings/branches`.
2. Selecionar **Add branch protection rule**.
3. Em **Branch name pattern**, informar `develop`.
4. Habilitar **Require a pull request before merging**.
5. Habilitar **Require status checks to pass before merging**.
6. Selecionar o status check `Repository governance`.
7. Habilitar **Require branches to be up to date before merging**.
8. Habilitar **Require linear history** se a opcao estiver disponivel.
9. Habilitar **Include administrators** se a opcao estiver disponivel.
10. Habilitar **Block force pushes**.
11. Habilitar **Block deletions**.
12. Salvar a regra.

## Validacao apos ajuste manual

Executar:

```bash
gh ruleset check main
gh ruleset check develop
gh api repos/nicrepository/nchat/branches/main/protection
gh api repos/nicrepository/nchat/branches/develop/protection
```

Se `gh ruleset check` nao estiver disponivel na versao instalada do GitHub CLI, validar pela tela **Settings > Branches**.
