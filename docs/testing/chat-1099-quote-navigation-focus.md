# Issue #1099 — foco durante navegação por citação

## Escopo e reprodução

Investigação do E2E / Web da PR #1168, execução
[42839153167](https://github.com/nicrepository/nchat/actions/runs/42839153167/job/129909349136),
commit `640cc67bef04a68646e9c5cdfa89e53bdb8beb93`.
Correção local em `fix/chat-1099-quote-navigation-focus`, baseada em
`develop` / `9567adf7471fd427630df749bef40ddd2e39920a`.

`git diff origin/develop 640cc67 -- apps/web package.json pnpm-lock.yaml`
é vazio: aplicação, teste, fixtures e dependências do frontend são idênticos.
A árvore `apps/web` tem o mesmo objeto Git
`2e20c9c4135ec956bd24563a8050410b821d601f` nas duas revisões.
A falha foi reproduzida no próprio develop: a série de 20 execuções, um worker
e zero retries, teve 15 aprovações e 5 falhas de foco. Isso demonstra que o
problema antecede a Entrega C. O teste e o fluxo de devolução vieram de
`fcfe84c6` / PR #1122, já integrado à base; não se infere independência apenas da lista de
arquivos da PR.

Ambiente local: Node 24.21.0, pnpm 11.1.2, Playwright 1.60.0,
Chromium 148.0.7778.96 / revisão 1223. O CI usou Node 24.22.2 e as mesmas
versões de pnpm, Playwright e Chromium. As comparações locais usam o mesmo
ambiente e a configuração oficial, Chromium e um worker; `CI=1` impede
reutilizar um servidor de outro worktree, e `--retries=0` desliga os retries.
Uma primeira tentativa local com servidor compartilhado foi invalidada por
`ERR_CONNECTION_REFUSED` quando seu proprietário terminou; ela não integra
os números de reprodução.

## Fluxo e responsabilidade pelo foco

1. `MessageContent.QuoteBlock` trata clique, Enter e Espaço e entrega o ID à
   ação `MessageList.followQuote`.
2. `useTargetFocusReturn` arma uma devolução somente se o foco estava na
   lista. Ela guarda mensagem, entrada de histórico e elemento proprietário,
   e observa `focusin` em captura no documento.
3. `ChatMessageArea.openMessageTarget` navega para `?message=...`, marcando a
   entrada com `withMessageJump`. `useMessageLoading.nextFocusLoad` determina
   se o alvo não carregado exige reabrir a conversa pela página mais recente.
4. A reabertura passa por `state.status = "loading"`:
   `ConversationTimeline` substitui a lista por `LoadingSkeleton`, enquanto
   `ChatMessageArea` mantém o composer da mesma conversa, mas o desabilita.
   `useChatEditor` chama `editor.setEditable(false, false)`. A extensão
   `Tabindex` do TipTap 2.27.2 também retira `tabindex` quando não editável.
5. Recebida a página mais recente, a lista remonta e o composer volta a ser
   editável. `useOpenPositionResolution` busca páginas antigas pelo cursor,
   de forma limitada; `useViewportCore`/TanStack Virtual posicionam a janela;
   `useMessageJump` destaca o destino por 1.200 ms.
6. Um `focusin` em outro elemento cancela a devolução e remove o listener,
   mesmo que esse elemento perca foco depois. Sem outra escolha, a chegada
   foca o original, ou a lista quando não encontrado. Falha ao reabrir foca
   Tentar novamente. Outra entrada de navegação e unmount cancelam a devolução.
   `useListFocusRecovery` leva à lista o foco perdido quando uma linha
   virtualizada é removida; não substitui um controle que já tem foco.

O composer é keyed por `kind:targetId`, não pela query da citação.
Seu foco inicial é tratado uma vez por instância, com proteção contra outra
escolha/overlay, em `ChatComposer`. O rascunho vive no store de conversas.
A navegação não altera seleção ou conteúdo por um comando de foco tardio do
composer.

## Evidência do CI e diagnóstico

O artefato [playwright-report-web](https://github.com/nicrepository/nchat/actions/runs/42839153167/artifacts/13776402987) contém os contextos de erro das três
tentativas e **somente o trace do primeiro retry**. Isso corresponde a
`trace: "on-first-retry"`; não há traces originais ou do retry 2 a analisar.
O ZIP do trace foi validado e contém 29 snapshots DOM e 168 frames.

No trace do primeiro retry (tempos monotônicos, em ms):

| Ação                           |         Tempo | Evidência            |
| ------------------------------ | ------------: | -------------------- |
| Enter na citação               | 769405–769416 | `call@155`           |
| Clique no composer             | 769425–769545 | `call@157`           |
| Primeira assertion de foco     | 769553–769568 | `call@159`, focused  |
| Resposta da reabertura         | 769695–769709 | `call@161`           |
| Skeleton/composer desabilitado |        769729 | frame do screencast  |
| Mensagem original encontrada   |        778343 | `call@149`           |
| Assertion final de foco        | 778434–783435 | `call@177`, inactive |

A screenshot após o clique mostra a lista antiga e o composer ativo; a seguinte
mostra o skeleton e o composer indisponível. O teste clicou **antes da fase que
pretendia testar**: ainda competia com a desabilitação da reabertura, não
necessariamente durante a paginação com o editor disponível. Se a perda ocorre
antes da primeira assertion, ela falha na linha 207; se a assertion consegue
observar o foco antes da perda, a falha aparece na linha 211. A execução original
falhou na linha 207 e ambos os retries na 211.

O trace não gravou uma trilha de `document.activeElement` ou cada chamada de
foco. Portanto `inactive` não identifica sozinho o elemento que recebeu foco,
nem comprova blur da janela. Há sete avisos React de `flushSync` durante a
montagem/medição da timeline e nenhum `pageError`; esses avisos não são tratados
como causa demonstrada da falha de foco.

Instrumentação local temporária observou `focusin`, `focusout`, `blur`, eventos
de ponteiro, mutações do editor, identidade do nó e chamadas de `focus` com
stack. O editor manteve a mesma identidade durante a reabertura; a janela
permaneceu com foco. Nas três execuções instrumentadas iniciais, o clique caiu
após a reativação e o foco foi preservado nas três. Essa instrumentação não faz
parte da alteração final.

Sem captura de trace, uma execução instrumentada reproduziu a falha imediata
(porta local 5174, configuração oficial via `E2E_BASE_URL`, mesma aplicação
original da PR). A série sem trace teve 9 aprovações e 1 falha. A sequência da falha, em ms desde o início da página, foi:

| Tempo  | Evento                               | Estado observado                                               |
| ------ | ------------------------------------ | -------------------------------------------------------------- |
| 1579,3 | pointerdown no parágrafo do composer | citação ainda focada                                           |
| 1579,4 | mousedown                            | citação ainda focada                                           |
| 1586,8 | blur do composer                     | mesmo nó, conectado, contenteditable=false; activeElement=body |
| 1588,4 | mutação contenteditable              | false, aria-disabled=true                                      |
| 1589,2 | click                                | activeElement=body                                             |
| 1673,0 | reativação                           | contenteditable=true, mas activeElement ainda body             |

`document.hasFocus()` permaneceu true em toda a sequência. Não houve
`focus-call` tardio nem remoção do editor após o pointerdown. A perda ocorreu
no próprio clique enquanto o controle era tornado indisponível. Isso explica
a falha da primeira assertion sem atribuí-la a uma restauração assíncrona.
A assertion final do CI, por sua vez, observou foco antes dessa mudança de
estado e falhou depois: o trace documenta essa ordem independentemente da
reprodução da primeira assertion.

| Hipótese                        | Conclusão                                                                                                                                   |
| ------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------- |
| H1: chamada programática tardia | Não ocorreu na sequência local que falhou; o blur coincidiu com a desabilitação. O experimento abaixo verifica separadamente roubo de foco. |
| H2: remount do composer         | Não observado; mesma identidade e key da conversa. Quem remonta é a lista.                                                                  |
| H3: TipTap/seleção              | A mudança de editabilidade é real; não se observou destruição do rascunho ou recriação do editor.                                           |
| H4: sincronização do teste      | Confirmada: não aguardava a reabertura/pedido antigo, nem retinha sua resposta.                                                             |
| H5: restauração ignora usuário  | A implementação cancela por focusin; teste corrigido verifica a remoção do listener e o foco final.                                         |
| H6: virtualização/fallback      | Coberto pelo teste existente de outra mensagem e pelo destino/lista.                                                                        |
| H7: timing do runner            | A ordem assíncrona varia localmente e no CI; não é justificativa para sleeps ou retries.                                                    |

## Correção e acessibilidade

A alteração se limita ao spec e a este documento. O teste reutiliza
`holdMessages`, agora também capaz de selecionar a coleção DM:

- registra a espera do pedido **antes** de Enter;
- aguarda o primeiro pedido com `before`, depois da reabertura;
- confirma editor editável, alvo ainda ausente e devolução pendente;
- clica no composer e mantém a assertion imediata `toBeFocused()`;
- confirma cancelamento e remoção do listener, escreve um rascunho e libera a
  resposta antiga;
- mantém as assertions de chegada/destaque/virtualização e `toBeFocused()` final,
  e verifica texto e ausência de listener residual.

O mesmo contrato passa a ser exercitado em canal, grupo e DM 1:1. Não há
chamada final a `focus`, substituição de assertions, force click, sleep, skip,
aumento de timeout, dependência nova ou alteração na aplicação/configuração
Playwright. Durante a reabertura, controles indisponíveis continuam assim; a
escolha de um controle disponível nessa fase já é coberta pelo teste do header.

## Validação

Comandos executados a partir do worktree isolado (os comandos Playwright
partem de `apps/web`):

```bash
pnpm install --frozen-lockfile
make ci-static-web
pnpm test:web
pnpm build:web

CI=1 pnpm exec playwright test e2e/messaging/quote-history-navigation.spec.ts \
  --grep "foco movido pelo leitor durante a busca" \
  --project=chromium --workers=1 --repeat-each=20 --retries=0

# Depois da correção, só o cenário original de canal na série de 20:
CI=1 pnpm exec playwright test e2e/messaging/quote-history-navigation.spec.ts \
  --grep "foco movido pelo leitor durante a busca" --grep-invert 'grupo:|DM:' \
  --project=chromium --workers=1 --repeat-each=20 --retries=0

CI=1 pnpm exec playwright test e2e/messaging/quote-history-navigation.spec.ts \
  --project=chromium --workers=1 --retries=0
CI=1 pnpm exec playwright test --project=chromium --workers=1 --retries=0 \
  --trace=retain-on-failure
```

| Validação                                                   | Resultado executado                                                                                       |
| ----------------------------------------------------------- | --------------------------------------------------------------------------------------------------------- |
| Original, isolado com trace, develop                        | PASS: 1/1                                                                                                 |
| Original, develop, 20 repetições                            | 15 PASS, 5 FAIL na primeira assertion de foco                                                             |
| Original, commit da PR, 20 repetições                       | 13 PASS, 7 FAIL: 6 de foco (antes/depois), 1 de destaque expirado                                         |
| Arquivo original completo, commit da PR                     | PASS: 15/15                                                                                               |
| Instrumentação com trace                                    | PASS: 3/3 iniciais e 10/10 adicionais; instrumentar altera timing, não demonstra estabilidade do original |
| Mutação: listener removido, devolução indevidamente mantida | FAIL esperado na assertion **final** de foco, linha 235; código restaurado e hash conferido               |
| Instrumentação sem trace                                    | 9 PASS, 1 FAIL; sequência de perda de foco no clique capturada                                            |
| Corrigido, isolado com trace                                | PASS: 1/1                                                                                                 |
| Corrigido, 20 repetições sem retries                        | PASS: 20/20, 3,6 min, sem retries                                                                         |
| Arquivo corrigido completo                                  | PASS: 17/17, 2,4 min                                                                                      |
| E2E / Web completo, sem retries                             | PASS: 525 aprovados, 6 skips preexistentes, 0 falhas, 20,9 min, sem retries                               |
| `make ci-static-web`                                        | PASS: Prettier, ESLint sem erros, TypeScript                                                              |
| `pnpm test:web`                                             | PASS: 281 arquivos, 7.781 testes                                                                          |
| `pnpm build:web`                                            | PASS                                                                                                      |

Na amostra original da PR, parte dos testes coincidiu com unitários/build
locais. Os parâmetros e versões foram os mesmos, mas carga e duração não são
um benchmark nem permitem comparar taxas de falha entre branches. O arquivo
completo e a série posterior não dependem desse resultado para validar a
correção. As duas falhas de foco também existem fora dessa sobreposição.

O inventário Playwright (`--list --reporter=json`) foi comparado entre o commit
original da PR e o spec corrigido: os 15 títulos anteriores permanecem; os
únicos acréscimos são os casos de grupo e DM. Nenhum cenário adversarial foi
removido. Os dois `toBeFocused()` do caso original continuam presentes.

Os seis skips da suíte completa já pertenciam a
`call-floating-handoff.spec.ts`: o bloco exige media-service/LiveKit real,
ausente deste projeto E2E. Nenhuma condição de skip foi adicionada.
Leitura assistiva manual com leitor de tela não foi executada neste ambiente
headless; as verificações de roles, foco e teclado foram executadas.

O ESLint reportou três warnings preexistentes, em arquivos intactos:
`useListBoundaries.ts` (dois `react-hooks/exhaustive-deps`) e
`useTimelineRows.ts` (`react-hooks/incompatible-library`). O build reportou
chunks acima de 500 kB. Nenhum threshold ou configuração foi alterado.

O experimento de mutação modificou temporariamente somente
`armFocusReturn.onFocusIn`: removeu o listener, mantendo indevidamente a
operação pendente. O clique foi confirmado, o listener deixou de existir,
mas a chegada voltou a focar a mensagem. A assertion final detectou o defeito.
A alteração experimental foi revertida em `finally`, verificando o SHA-256;
não integra o diff final.

## Matriz de regressões

| Contrato                         | Cobertura                                                                                        |
| -------------------------------- | ------------------------------------------------------------------------------------------------ |
| Enter chega ao original com foco | Spec: Enter na citação; testes de ChatMessageArea                                                |
| Escolha do composer prevalece    | Caso corrigido em canal, grupo e DM 1:1                                                          |
| Outra mensagem prevalece         | Spec: outra mensagem durante busca; fallback para a lista quando virtualizada                    |
| Original inalcançável            | Spec: aviso role=status, lista focada, nenhum destaque falso                                     |
| Rede/retry                       | Spec: reabertura falha/Tentar novamente; falha ao paginar/nova ativação                          |
| Voltar cancela devolução         | Spec: rota, listeners, ausência de destaque e destino tardio                                     |
| Lista remonta/virtualização      | Spec: Enter, outra mensagem e canvas virtualizado                                                |
| Rascunho preservado              | Caso existente e digitação com a página antiga bloqueada                                         |
| Listener liberado                | Contagem antes/durante/depois da busca e ao Voltar                                               |
| Canal/grupo/DM                   | Mouse em canal/grupo; preservação de foco nos três tipos                                         |
| Mouse/celular                    | Casos existentes de clique e viewport móvel                                                      |
| Semântica acessível              | role=button, tabindex, role=log, role=status e foco real; leitura assistiva manual não executada |

## Snapshot final e prontidão

Parecer do implementador: **FIX READY FOR REVIEW**.

- Worktree da correção: `/home/alvaroneto/Projects/nchat-quote-focus`.
- Branch: `fix/chat-1099-quote-navigation-focus`.
- HEAD/base: `9567adf7471fd427630df749bef40ddd2e39920a`.
- Modified: `apps/web/e2e/messaging/quote-history-navigation.spec.ts`.
- Untracked novo: `docs/testing/chat-1099-quote-navigation-focus.md`.
- Staged: zero. Nenhum commit foi criado.
- Worktree original: branch `feature/files-1129-scan-queue-fencing`,
  HEAD `640cc67bef04a68646e9c5cdfa89e53bdb8beb93`, zero modified e os dois
  untracked preexistentes. Todos os arquivos registrados no manifesto SHA-256
  inicial, incluindo esses dois arquivos, mantiveram seus conteúdos.

Somente o spec e este relatório integram a alteração adicional: o primeiro
estabelece a barreira e a cobertura de grupo/DM; este documento registra a causa,
evidências, comandos, resultados e limitações. `git diff --check` passou e não
há arquivos de produção, dependências ou configuração no diff.

Evidência bruta local (fora do Git): logs `/tmp/nchat-quote-*.log`, artefato do
CI em `/tmp/nchat-quote-ci-artifact`, sequência instrumentada em
`/tmp/nchat-quote-diagnostic-sequences.json`, resultados por comando em
`/tmp/nchat-quote-final-validation.json` e integridade em
`/tmp/nchat-quote-final-integrity.json`.

A correção está disponível para Code Quality Review e Security Review
direcionados. Essa prontidão não substitui os pareceres independentes.

## Integração e limites

Não se alteram Entrega C, migrations, Go, fencing, dependências, Trivy,
controles de CI ou correção da CVE-2026-78661. SEC-B-002 permanece independente.
O worktree original e seus dois arquivos untracked foram preservados.

Após revisão humana, integrar esta correção pela branch própria em develop e
atualizar a PR #1168 pelo processo autorizado, então reexecutar E2E / Web no SHA
resultante. Esta execução não cria commit, push, PR, merge ou deploy e não
representa aprovação independente de qualidade ou segurança.
