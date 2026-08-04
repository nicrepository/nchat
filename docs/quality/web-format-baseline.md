# Baseline de formatação do frontend web

Em 4 de agosto de 2026, `pnpm format:check:web` apontava 35 arquivos preexistentes fora do diff da RF-23. Esse baseline registra a dívida; ele não torna o gate global aprovado, não exclui arquivos do check e não altera a configuração do Prettier.

Arquivos novos e arquivos modificados devem passar no Prettier. Features não devem formatar arquivos não relacionados apenas para reduzir esse baseline; a limpeza dos 35 arquivos deve ocorrer em tarefa separada.

Para validar uma mudança, identifique os arquivos web do diff com `git diff --name-only origin/develop` e os arquivos ainda não rastreados com `git status --short`, então execute:

```sh
pnpm exec prettier --check <arquivos-web-alterados>
```

O resultado desse check focado deve ser registrado junto da falha ainda preexistente do comando global.
