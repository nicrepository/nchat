# #1047 — Ownership em invalidações externas

## Contrato e limites

Base oficial `upstream/develop`, commit `22decd466d202ad9e7772f69fa76be3c08028077`,
com a #1046 integrada. Branch `feature/chat-1047-ownership-invalidation`.

Os writers de suspensão global de auth e admin usam o coordinator compartilhado
em `conversationownership`. A seleção da #1046 foi extraída, preservando ADMIN
antes de MEMBER, `joined_at ASC`, UUID ASC e exclusão do participante afetado.
A consulta agrupa as conversas em lote; uma promoção é escrita por conversa que
precisa dela. Não existe consulta separada de candidatos para cada conversa.
Outro OWNER ativo dispensa promoção; nenhum participante restante permite a
invalidação; participantes restantes sem candidato elegível causam conflito.
Guests ativos continuam contando como participantes e owners, mas não são
candidatos automáticos.

A view `chat.active_ownership_participants` continua sendo a fonte de
elegibilidade. Conversas operacionais são grupos e canais privados ativos em
workspace ativo, com workspace membership ativa e usuário ativo sem `deleted_at`.
A invalidação não remove automaticamente a conversation membership ou sua role
histórica: ela deixa de participar da view de acesso. Reativação não restaura
sessões e segue os guards atuais.

## Eventos e authority

| Evento                                     | Authority e operação real                                                                  | Tratamento                                                                                            |
| ------------------------------------------ | ------------------------------------------------------------------------------------------ | ----------------------------------------------------------------------------------------------------- |
| Usuário suspenso                           | auth.users; writers UpdateUserStatus de auth/admin                                         | Coordinator antes do UPDATE, na mesma transação                                                       |
| Workspace membership suspended/left/DELETE | chat.workspace_members; não há endpoint desses eventos em auth/admin                       | Guards SQL existentes, testes reais; API N/A                                                          |
| Desativação de usuário                     | Não existe estado disabled ou transição correspondente                                     | N/A; locked é representável em SQL, sem inventar equivalência                                         |
| Delete/anonymize                           | auth.users suporta deleted/deleted_at/DELETE; não foi encontrado fluxo completo de erasure | Guards SQL testados; anonymize/API N/A                                                                |
| Revogação administrativa                   | Suspensão administrativa em admin-service                                                  | Mesmo coordinator; revogar sessão ou papel administrativo isoladamente não altera elegibilidade local |
| Workspace disabled                         | chat.workspaces.status; não há endpoint de lifecycle encontrado                            | Conversas operacionalmente inativas, sem promoção; reativação órfã é bloqueada pelos guards           |

Evidências: migrations auth 000001, chat 000001 e 000060–000063;
`ValidateStatusTransition` em auth e `ValidUserStatusTransition` em admin permitem
somente active ↔ suspended. O hook de avatar `PurgeForAnonymization` não constitui
uma operação completa de delete/anonymize. Admin remove memberships de canal;
esse writer é uma remoção local, não uma revogação global de workspace.

O domínio global pertence a auth, admin mantém sua autorização administrativa,
e o chat mantém membership, roles e sucessão de conversa. Os serviços usam os
schemas auth/chat no mesmo PostgreSQL. Não há chamada HTTP entre serviços nem
job assíncrono novo para coordenar estes writers.

## Transação, locks e retry

Sequência dos writers globais:

1. BEGIN e SERIALIZABLE antes de qualquer leitura de negócio.
2. `chat.lock_user_ownership_conversations`, ordenando `(kind, conversation_id)`.
3. Lock de auth.users e validação da transição; admin mantém seu lock de principal
   administrativo após o lock do usuário.
4. Consulta única das conversas em que o usuário é OWNER elegível, cálculo de
   owners restantes e sucessores, seguido de `chat.assign_ownership`.
5. UPDATE do usuário, revogação de sessões/refresh tokens e consumo de códigos
   OIDC pendentes, seguidos de COMMIT.

Os locks de conversa precedem os locks do registro de lifecycle e das memberships
alteradas pela promoção. Leave/transfer usam o mesmo lock de conversa, de modo que
os writers globais não invertem essa ordem. O admin anchor mantém a posição
relativa existente; não foi criado lock de workspace ou account adicional.

O coordinator aceita somente usuário e workspace interno confiável; não recebe
sucessor, roles forçadas, owner_count ou estado de lifecycle fornecido por cliente.
Descoberta, elegibilidade e promoção são rederivadas pelo banco. Scope vazio é
exclusivo da suspensão global interna; scope de workspace filtra toda a seleção.
Os adapters não fazem commit nem iniciam outra transação.

`conversationownership.Retry` mantém até três tentativas para 40001/40P01. Cada
uma reabre a transação, relê lifecycle e conversas e recalcula sucessores. A
função SQL de promoção revalida a participação; SERIALIZABLE e os guards impedem
commit com candidatura obsoleta. Nenhum resultado de seleção é reutilizado.

Sem candidato, erro de leitura/escrita, falha de promoção ou falha no commit
reverte roles, lifecycle e efeitos transacionais existentes. A consulta fecha
seu cursor antes de começar as promoções. Conflitos P0953 mantêm o contrato de
erro já existente dos serviços. HTTP e tipos públicos de produto não mudam.

## Compatibilidade e limitações

Antes da expansão do schema, a ausência do helper de lock preserva o fallback
preexistente de auth/admin. Com helper disponível, erros são propagados; não há
fallback para falhas de consulta ou de promoção. Rollout desligado não promove.
Não há migrations, backfill, novos endpoints, audit/outbox ou realtime.

Os eventos representáveis via SQL continuam protegidos pelos triggers atuais.
SQL administrativo deve adquirir locks de conversas em ordem antes de atualizar
account/workspace membership, usando SERIALIZABLE e retry da transação inteira.
SQL arbitrário que ignora esse protocolo pode adquirir locks na ordem inversa e
receber deadlock; não ganhou garantia de liveness. Os guards continuam sendo a
proteção de coerência desses writes. Este delta não altera migrations históricas.

Todos os writers de invalidação encontrados são bloqueáveis no banco compartilhado.
Compensação distribuída ou operação global irreversível: N/A. Não foi criado um
contrato de rollback para um fluxo de erasure ausente. Uma arquitetura futura com
bancos separados precisará definir sua própria coordenação antes de usar o helper.
Workspace disabled não necessita sucessão; se usuários perderem elegibilidade
nesse período, uma reativação que causaria orfandade é recusada pelo guard existente.

## Validação

Banco descartável PostgreSQL 16: `nchat-1047-postgres`, porta local 5547,
database `ownership_953_test`. Os harnesses recriam schemas e devem rodar
sequencialmente, nunca em banco de desenvolvimento ou produção.

- PASS: eventos SQL representáveis, ADMIN/MEMBER, outra role OWNER restante,
  múltiplas conversas, workspace isolado e ausência de sucessor.
- PASS: erro após promoção reverte roles, membership, audit e outbox existentes.
- PASS: writers reais de auth/admin promovem antes do UPDATE; falha deferred no
  commit restaura account, sessão, OIDC e roles.
- PASS: concorrência com barreiras, nas duas ordens: leave+suspend,
  transfer+remoção da workspace membership do actor ou target, e suspensão do
  candidato durante invalidação. As operações que perdem acesso são recusadas;
  não aparecem órfãos ou transferência parcial.
- PASS: retry 40001/40P01 do writer auth relê lista de conversas e escolhe outro
  candidato; testes inferiores de retry e seleção da #1046 reutilizados.
- PASS: pacotes storage afetados; PostgreSQL dirigido de auth/admin/chat com race
  detector. Outras famílias PostgreSQL sem suas variáveis são puladas e não
  constituem evidência de integração.
- N/A: UI, E2E/Playwright e compensação distribuída.

Comandos de reprodução, com DATABASE_URL apontando exclusivamente ao banco
local descartável:

```sh
AUTH_TEST_DATABASE_URL="$DATABASE_URL" go test -race ./services/auth-service/internal/storage -run 'TestOwnershipAccountInvalidationPostgreSQL|TestPGXUserStore_OwnershipInvalidationRetry' -count=1
ADMIN_TEST_DATABASE_URL="$DATABASE_URL" go test -race ./services/admin-service/internal/storage -run TestOwnershipAccountInvalidationPostgreSQL -count=1
OWNERSHIP_TEST_DATABASE_URL="$DATABASE_URL" go test -race ./services/chat-service/internal/storage -run 'TestOwnership(Invalidation|Succession)' -count=1
make ci
```

Logs: `/tmp/nchat-1047-auth-pg.log`, `/tmp/nchat-1047-admin-pg.log`,
`/tmp/nchat-1047-chat-pg-race.log`, `/tmp/nchat-1047-packages.log`,
`/tmp/nchat-1047-lint.log`, `/tmp/nchat-1047-vet.log` e
`/tmp/nchat-1047-build.log`. As falhas iniciais de fixtures foram corrigidas;
somente a rodada final serve como evidência de aceite.

## Code Quality Review

Revisão separada: extração localizada da seleção da #1046, um coordinator para
os writers globais, adapters pequenos e ausência de dependência do driver no
pacote compartilhado. SRP preservado; nenhuma seleção Go duplicada ou novos
smells confirmados. A seleção em lote é independente por workspace/conversa;
seu resultado inteiro é validado antes de qualquer promoção.

Complexidade medida com gocyclo 0.6.0 e gocognit 1.2.1: writers globais com
ciclomática 10; cognitiva auth 10, admin 9. `selectSuccessions`: 7/9;
`succeed`: 4/4. Helpers e testes alterados ficam em até 10. Funções preexistentes
fora do delta acima desse limite não foram alteradas. Nenhuma medição SonarQube
foi feita. Formatter, lint dos quatro módulos, vet e builds afetados: PASS.

## Security Review

Revisão separada: endpoints conservam autorização e contexto de identidade.
Nenhum payload externo escolhe sucessor ou role. Todas as entradas SQL são
parametrizadas, e a seleção está limitada ao workspace interno ou ao usuário
global explicitamente invalidado. SERIALIZABLE, locks de conversa e retry completo
protegem contra TOCTOU; erros não liberam a invalidação. Os testes comprovam
isolamento, rollback e revalidação de actor/target. Sem novas dependências ou
logs de credenciais. Gosec via golangci-lint: PASS. Zero achados novos confirmados.
A revisão não constitui pentest da aplicação inteira.

## Entrega

- PASS: `git diff --check`, formatter, lint, vet e builds dos serviços afetados.
- PASS: EXPLAIN (ANALYZE, BUFFERS, VERBOSE) da consulta extraída de produção:
  três conversas no workspace, quatro no escopo global. Execução de 0,887 ms e
  0,398 ms nas fixtures pequenas; índices existentes utilizados. Sem N+1 de
  descoberta/seleção introduzido e sem evidência para adicionar índice. Isso não
  equivale a teste de carga. Log `/tmp/nchat-1047-explain.log`.
- FAIL: gate global oficial `make ci`, executado uma vez, no threshold de
  cobertura do chat-service: 86,2%, exigência 90%. Checks estáticos, infraestrutura,
  release, testes Go e cobertura web/admin passaram antes dele. Web: 239 arquivos,
  6.530 testes; admin-web: 43 arquivos, 536 testes. Os builds globais posteriores
  à cobertura são NÃO EXECUTADOS; os builds direcionados dos serviços afetados
  passaram separadamente. Log `/tmp/nchat-1047-ci.log`.
- PASS: comparação com a base oficial, medida com os mesmos pacotes não-cmd e
  covermode atomic do checker. A worktree #1046 usada para medição tem conteúdo
  idêntico ao upstream/develop em chat-service e libs/go/platform, confirmado
  por git diff antes da medição. Base: 11.562/13.409 statements, 86,226%.
  #1047: 11.560/13.407, 86,224%. Ambos arredondam a 86,2%; diferença de
  −0,002 ponto percentual após a extração de código para o pacote compartilhado.
  Perfis: `/tmp/nchat-1047-base-chat.cover` e
  `coverage/go/services_chat-service.threshold.out`. A falha de threshold existe
  na base; não foi reduzido o gate nem ampliada a matriz para elevar cobertura.
- PASS: coverage gates de platform (92,6%), admin (91,0%) e auth (90,8%).
- Commit local: `feat(chat): preserve ownership on eligibility invalidation`.
- Push: NÃO REALIZADO. PR e merge não realizados.

O container descartável foi parado e preservado para reprodução. Pendência de
entrega: gate global permanece FAIL pelo threshold descrito acima. A validação
funcional e concorrente da #1047 passou, sem estado órfão observado. Os checks de
famílias PostgreSQL não selecionadas não são evidência de integração desta task.

## Revisão e testes unitários após o commit inicial

Escopo: revisão do delta `69a792c0b4b84f83ba129297e9361ec9cca55e9b`,
sem mudança em código de produção, contratos, migrations ou threshold.
Nenhum bug real encontrado; nenhuma feature de disable/delete implementada.

### Transaction boundary

Caso A para suspensão global via auth/admin: `updateUserStatusOnce` abre uma
única `pgx.Tx`; `ownershipSuccessionSession` captura exatamente essa transação
em ambos os callbacks. Seleção, promoção, UPDATE de status, revogação de
sessões/refresh e consumo dos códigos OIDC precedem o mesmo Commit. O coordinator
não abre outra transação. Não existe chamada entre serviços nessa operação;
compensação não é necessária. Compartilhar PostgreSQL, isoladamente, não seria
prova: a prova é a identidade da transação no código, a ordem das expectativas
unitárias e o rollback real dos testes PostgreSQL, inclusive em falha no commit.

A invalidação de membership, sem API de serviço própria, é protegida pelos
triggers no mesmo transaction do comando SQL. Os guards continuam protegendo
writers diretos. A ordem de locks está explícita em
`000061_conversation_ownership_guards.up.sql`: `(kind, id)`; o selector compartilhado
ordena `(kind, conversation_id)`. Não há ordenação pura em Go para testar.

### Testes úteis

- Dois testes table-driven do coordinator: snapshots sem conversa afetada,
  outro owner, conversa sem participantes restantes, sucessor retornado pelo
  banco, várias conversas e C1 com candidato/C2 com owner/C3 sem candidato.
  Esse último snapshot falha antes de qualquer promoção. Erros de query, Scan,
  Rows.Err e promoção preservam o erro e impedem writes posteriores. O cursor
  fecha antes de escrever na mesma conexão transacional.
- Um teste table-driven de admin: falha na revogação de sessão, na invalidação
  OIDC ou no Commit retorna resultado vazio e solicita rollback, sem relatar
  revogação como sucesso. O mock prova coordenação; PostgreSQL prova rollback.
- Testes existentes de suspensão/reativação adaptados com ownership disponível:
  promoção antes da suspensão; reativação sem sucessão nem restauração de acesso.
  O teste existente de session-only revoke verifica as expectativas completas,
  sem chamada ao coordinator.
- API admin: cinco casos adicionados à tabela de campos desconhecidos, recusando
  `successor_user_id`, `force_role`, `owner_count`, `conversation_role`,
  `current_owner` antes do serviço. API auth bootstrap: um teste table-driven
  prova que esses campos são ignorados e somente caller/target/status chegam ao
  contrato existente, que não aceita autoridade de ownership.

Não foram duplicadas as provas SQL de seleção ADMIN/MEMBER, workspace incorreto,
cross-workspace, workspace disabled ou locks reais. Retry 40001/40P01,
recarregamento de estado e erro não retryable já tinham testes diretos.
Sem participante restante, o coordinator preserva a semântica de conversa vazia;
sem candidato com participantes restantes, retorna P0953/fail-closed.

### Coverage e validação

Pacotes storage medidos antes/depois com covermode atomic: admin 84,0% → 85,2%;
auth 89,6% → 89,6%; chat 70,1% → 70,1%. Pacote conversationownership:
33,3% → 97,6%; Succeed, Invalidate, succeed e selectSuccessions: 100%.
O único statement restante no coordinator é Error(), deliberadamente sem teste
trivial. Perfis `/tmp/nchat-1047-review-before.cover`,
`/tmp/nchat-1047-review-after.cover` e perfis finais de platform/admin.

Chat inteiro, mesmos pacotes não-cmd do gate oficial: 11.560/13.407 = 86,224%,
inalterado por estes testes. Base oficial: 11.562/13.409 = 86,226%; delta
−0,002 p.p., sem regressão material. Perfil `/tmp/nchat-1047-review-chat.cover`.
`make ci` não foi repetido nesta revisão: a última execução documentada acima
continua FAIL exclusivamente pelo threshold global preexistente de 90%.

PASS: testes direcionados, pacotes afetados, PostgreSQL auth/admin/chat existentes
com race detector, concorrência e rollback; race dos pacotes com testes novos;
formatter; lint platform/auth/admin; vet dos quatro módulos e build dos três
serviços. Logs `/tmp/nchat-1047-review-{auth-pg,admin-pg,chat-pg,units-race}.log`.

Code quality review separado: testes table-driven legíveis, fixtures reutilizadas,
sem simular seleção SQL/locks nem duplicar integração; SRP preservado; zero novos
smells confirmados. gocyclo/gocognit: novas funções com máximos 5/10; funções
existentes alteradas também ≤10. Nenhuma refatoração de produção.
Security review separado: escopo SQL e guards preservam cross-workspace,
fail-closed e autoridade de seleção; transação/locks/retry preservam stale state,
TOCTOU e rollback; input HTTP não escolhe sucessor nem força papel. PASS.

Push: NÃO REALIZADO. Pendência única: threshold global de coverage preexistente.
