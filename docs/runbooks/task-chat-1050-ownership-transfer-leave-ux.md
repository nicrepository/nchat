# Task #1050 — aceite da UX de transferência e saída

## Identificação

- Issue: [#1050](https://github.com/nicrepository/nchat/issues/1050).
- Branch: `feature/chat-1050-ownership-transfer-leave-ux` no worktree `nchat-1050`.
- Implementação: `7dde2c8`, originalmente baseada em `39cb62c`.
- Base atualizada: `upstream/develop` em `73ca036`.
- Merge solicitado pelo usuário: `ab763a1`, sem conflitos.
- Rodada final de revisão: 2026-10-08.

## Critérios de aceite

| Critério da issue                                      | Resultado | Evidência                                                                                                                                                          |
| ------------------------------------------------------ | --------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| Transfer e promote possuem textos/semânticas distintas | PASS      | `OwnershipFlow.test.tsx`: promoção usa PATCH role; transfer usa POST transfer, com papel ADMIN/MEMBER escolhido. Playwright confirma menu e dialog distintos.      |
| Último owner vê consequência antes de sair             | PASS      | Preview usa sucessor retornado pelo servidor; testes cobrem preview automático, múltiplos owners e último participante.                                            |
| Manual successor pode ser escolhido                    | PASS      | Picker permite busca e seleção; override chama a operação única transfer-and-leave.                                                                                |
| Target stale gera conflito sem saída parcial           | PASS      | 409 mantém dialog aberto, recarrega projeção e limpa candidato inelegível; nenhuma chamada DELETE separada. Confirmado em componentes e Playwright.                |
| Double submit não duplica                              | PASS      | Testes disparam click duplo e submit adicional antes da conclusão; somente uma chamada é feita. Retry incerto conserva chave e payload.                            |
| Draft completo é preservado                            | PASS      | `ChatComposerDraftRoundTrip.test.tsx` cobre voz finalizada, reply e editor no conflito/cancelamento. Playwright cobre texto, reply e attachment nos dois clientes. |
| UX acessível e mobile                                  | PASS      | Chromium desktop e 390×844: foco inicial, trap Tab/Shift+Tab, radio por setas, Escape, retorno de foco e dialog sem overflow horizontal.                           |
| UI não reimplementa escolha automática server-side     | PASS      | O sucessor exibido vem de `leavePreview.successorUserId`; saída automática usa a API DELETE existente.                                                             |

Os testes de navegador também verificam identidade dos nós de composer/timeline,
scroll inalterado e contadores/frames WebSocket preservados. O backend existente
continua sendo responsável por autoridade, atomicidade e sucessão.

## Revisões separadas

- [Code Quality Review](../reviews/task-chat-1050-code-quality.md): APROVADO;
  sem achados relevantes, complexidade cognitiva máxima 8 e ciclomática máxima 10.
- [Security Review](../reviews/task-chat-1050-security.md): APROVADO;
  sem vulnerabilidades confirmadas no delta.

## Validação pós-merge

O CI global foi iniciado com `pnpm run ci`. A execução inicial no sandbox parou
no carregamento de módulos do golangci-lint; foi reiniciada fora do sandbox.
Resultado final: FAIL global (exit 1), por cobertura de `services/chat-service`
em 86,3%, abaixo do threshold 90%. Os gates estáticos, infraestrutura, migrations,
release-safety, teste do gate govulncheck e testes Go anteriores passaram.
O CI não alcançou os builds; estes foram executados separadamente.

PASS: web, 245 arquivos e 6.694 testes; admin, 43 arquivos e 536 testes.
Os thresholds de cobertura frontend passaram:

| Aplicação | Statements | Branches | Functions |  Lines |
| --------- | ---------: | -------: | --------: | -----: |
| Web       |     95,58% |   91,52% |    96,01% | 97,48% |
| Admin     |     96,70% |   93,25% |    95,73% | 98,12% |

O comando `git diff --exit-code upstream/develop -- services libs
scripts/ci/go-coverage.sh scripts/ci/go-coverage-check.sh` retornou zero: backend,
bibliotecas Go e scripts de cobertura são idênticos à base. A falha de cobertura
é externa ao delta da #1050 e permanece pendente; não foram adicionados testes
de backend fora do escopo nem reduzido o threshold para contorná-la.

PASS: formatter, lint e typecheck web/admin; três warnings preexistentes no web
fora do delta. PASS: `git diff --check`. PASS: builds web e admin executados
separadamente com `pnpm build` (exit 0).

Os dois cenários de `e2e/messaging/ownership.spec.ts` passaram (6,6s) usando
servidor exclusivo na porta 5180. A primeira execução padrão havia reutilizado
o servidor de `nchat-upstream-develop` na porta 5173; seus resultados foram
descartados como validação da branch. A configuração isolada exige porta livre
e não reutiliza servidor existente.

As evidências anteriores de 231 testes direcionados e build constam do plano de
handoff; os resultados novos devem prevalecer para o estado após o merge.

## Artefatos e limites

### Refinamento visual solicitado após o aceite

Os dialogs receberam hierarquia de título e descrição, busca com ícone e label
explícito, opções de participante com seleção visível, cards de ADMIN/MEMBER com
descrições acessíveis e um card destacado para o futuro proprietário na saída.
Os avatares do picker/preview agora têm tamanho fixo de 36px, incluindo o fallback;
a inspeção visual identificou que a imagem podia ocupar a largura disponível e
comprimir o nome. O layout mantém os tokens do tema chat e adapta os cards para
uma coluna em mobile.

Validação do refinamento: 42 testes de componentes em três arquivos, dois
cenários Playwright, inspeção das capturas desktop 1280px/mobile 390px, ESLint
com limites de complexidade 10, formatter e build web. Nenhuma alteração nos
hooks de estado, idempotência, API ou backend. O CI completo da rodada anterior
não foi repetido para esse ajuste visual; a pendência de cobertura Go permanece.

Logs: `/tmp/nchat1050-front-tests.log`, `/tmp/nchat1050-front-e2e.log` e
`/tmp/nchat1050-front-build.log`. Capturas: `/tmp/nchat1050-front-transfer-390.png`,
`/tmp/nchat1050-front-transfer-1280.png` e `/tmp/nchat1050-front-leave-390.png`.

### Evidências da rodada original

- CI: `/tmp/nchat1050-postmerge-ci-unrestricted.log`.
- Tentativa inicial no sandbox: `/tmp/nchat1050-postmerge-ci.log`.
- E2E válido: `/tmp/nchat1050-postmerge-e2e-isolated.log`.
- Configuração de E2E: `/tmp/nchat1050-postmerge-playwright.config.mjs`.
- Métricas: `/tmp/nchat1050-postmerge-complexity.json`.
- Build: `/tmp/nchat1050-postmerge-build.log`.

Não houve alteração das regras funcionais nesta rodada. Não foram feitos push, PR,
merge em develop, deploy ou QA manual em ambiente implantado. O merge realizado
foi de `upstream/develop` para a branch da task, conforme solicitado.
