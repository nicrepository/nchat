# Security Review — task #1050

## Resultado e escopo

APROVADO. Nenhuma vulnerabilidade confirmada ou risco provável relevante
introduzido pelo delta. Zero achados críticos, altos, médios ou baixos.
Revisão separada de qualidade, sobre o commit `7dde2c8` após o merge `ab763a1`,
contra `upstream/develop` em `73ca036`.

Escopo: fronteiras de autorização, entradas no picker, requests de
transferência/saída, erro recuperável e idempotência. Os handlers/storage
existentes foram lidos para confirmar a autoridade final do backend; não houve
alteração de API, backend, migrations ou dependências.

## Evidências

- `useOwnershipSelection.candidatesOf` exige `member.actions.transfer === true`
  e exclui o próprio ator. A UI usa capabilities do servidor, sem criar um role
  engine ou conferir autoridade apenas pelo badge.
- `ownershipApi.ts` usa `authenticatedFetch` e codifica os IDs nos paths. O
  payload de transfer contém apenas `new_owner_user_id` e `actor_new_role`; não
  inclui identidade de ator ou workspace forjável.
- `ownership_handler.go` obtém ator/workspace do contexto autenticado. Seu
  decoder estrito valida a operação, ID, papel e chave de idempotência.
  `authorizeOwnershipTransfer` verifica participantes e autoridade no servidor,
  dentro da transação existente. Esconder controles não é a barreira de segurança.
- A saída manual chama `transfer-and-leave`, sem encadear uma transferência e
  uma saída independentes. O fluxo não anuncia sucesso antes de a API confirmar.
- `ownershipFailure` usa mensagens locais para 403/404/409 e falhas incertas;
  não renderiza detalhes privados vindos do erro. Os testes verificam isso.
- Nomes e busca são strings renderizadas por React, sem inserção de HTML cru,
  eval ou execução de comandos. Avatares seguem o componente e saneamento
  existentes. Não foram introduzidos logs de dados privados ou credenciais.
- Retry incerto mantém chave e parâmetros originais; a proteção síncrona em
  `pending.current` impede submissão dupla antes do próximo render.

## Validação e limites

Os dois cenários Chromium pós-merge passaram usando servidor exclusivo do
worktree. A suíte de componentes inclui erros 403/404/409, retry, troca de
intenção, submissão dupla e sucessor que perde elegibilidade.

O artefato anterior `/tmp/nchat1050-semgrep.json` registra zero resultados e zero
erros nos sete módulos de ownership; é evidência da rodada anterior, não uma
nova execução nesta rodada. A revisão atual é de código e contratos acessíveis,
sem pentest, auditoria global de dependências ou QA de ambiente implantado.
Nenhuma correção de segurança indicada. O resultado do CI global está no
relatório de aceite e não deve ser interpretado como uma aprovação de deploy.
