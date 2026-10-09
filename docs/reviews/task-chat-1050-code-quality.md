# Code Quality Review — task #1050

## Resultado e escopo

APROVADO. Nenhum achado relevante de qualidade no delta da task; zero achados
críticos, altos, médios ou baixos. Revisão do commit `7dde2c8` e do estado após o
merge `ab763a1`, contra `upstream/develop` em `73ca036`.

O escopo inclui os dialogs, picker, estado de seleção/submissão, integração no
painel/roster, CSS e testes alterados. Não houve mudança funcional adicional nesta
rodada de revisão. O merge não apresentou conflitos.

## Complexidade medida

Medição real com ESLint e `eslint-plugin-sonarjs` 4.2.2, regras `complexity` e
`sonarjs/cognitive-complexity`. Para obter valores, os limites foram temporariamente
configurados em zero por linha de comando; isso produz mensagens de medição,
não falhas do código. Nenhuma configuração ou dependência do projeto foi alterada.
Valores máximos por função em cada arquivo de produção:

| Arquivo                      | Cognitiva | Ciclomática |
| ---------------------------- | --------: | ----------: |
| ConversationDetailsPanel.tsx |         6 |           8 |
| OwnershipActionDialog.tsx    |         7 |           8 |
| OwnershipDialogs.tsx         |         4 |          10 |
| OwnershipRoster.tsx          |         3 |           8 |
| OwnershipTargetPicker.tsx    |         0 |           2 |
| ownershipDialogState.ts      |         7 |           5 |
| useOwnershipSelection.ts     |         5 |           8 |
| useOwnershipSubmit.ts        |         8 |           9 |

Todos os arquivos TypeScript/TSX alterados também foram medidos, incluindo os
cinco arquivos de testes: máximo cognitivo 7 e ciclomático 6 nos testes. Ambos os
limites solicitados, ≤10, são atendidos inclusive nos arquivos de integração.
Artefato: `/tmp/nchat1050-postmerge-complexity.json`; configuração:
`/tmp/nchat1050-quality.config.mjs`.

## Evidências da revisão

- SRP: seleção e elegibilidade de UI ficam em `useOwnershipSelection`; execução,
  proteção contra submissão dupla e recuperação ficam em `useOwnershipSubmit`;
  mensagens/estados tipados ficam em `ownershipDialogState`.
- A chave de idempotência permanece no retry de resultado incerto. Alterar a
  intenção cria uma nova chave; inputs ficam bloqueados enquanto o resultado
  anterior é incerto.
- Conflitos mantêm o dialog aberto, recarregam a projeção e removem seleção stale.
  Falha do refresh não descarta o dialog. A troca da conversa fecha o dialog.
- Promoção, transferência e transferência com saída usam operações distintas.
  A saída com sucessor manual usa uma única operação atômica da API existente.
- O preview automático usa `leavePreview.successorUserId` enviado pelo backend;
  não há escolha automática de sucessor implementada no cliente.
- O dialog é um portal e preserva o composer e a timeline montados. Os testes
  Playwright após o merge passaram em desktop e 390px, verificando foco, Escape,
  navegação por teclado, scroll, identidade dos nós e frames WebSocket.

## Correções e limites

Sem correção de código indicada. Formatter, lint e typecheck web/admin passaram
na rodada pós-merge. Os três warnings de hooks do web estão fora do delta.
O resultado global do CI e a cobertura estão registrados no relatório de aceite.
Não foi executada análise remota SonarQube; a medição cognitiva é da regra SonarJS
local. O navegador utiliza APIs e WebSocket simulados; não equivale a validação
em produção.
