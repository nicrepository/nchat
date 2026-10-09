# Task #1048 — Ownership realtime outbox

Base: `feature/chat-1047-ownership-invalidation`, commit `83c3ce0`.

## Pipeline e responsabilidade

Mutation, audit e enqueue permanecem na mesma transação. A migration 000067
completa a metadata da auditoria sem modificar os writers ou as regras de
ownership. Colunas geradas derivam tipo, operação e source do contrato anterior;
`result=success` identifica efeitos commitados. Inserts antigos continuam válidos.
`manual` e `transfer` usam source `manual`; `succession` usa
`automatic_successor`; `invalidation` usa `workspace_invalidation`; `backfill`
usa `backfill`. Promoção causada por invalidação mantém essa origem e operação
`promote_successor`. Eventos de auditoria de backfill também enfileiram durante
rollout desativado; invalidações somente de membership mantêm o gate existente.

O dispatcher lê exclusivamente rows commitadas. Processa até 50 por ciclo do
worker existente, com uma transação por row e `FOR UPDATE SKIP LOCKED`. O lock
permanece durante o publish, limitado a cinco segundos. Sucesso marca
`published_at`; falha persiste tentativa, classificação limitada e retry na mesma
row. O backoff começa após a falha: 1, 2, 4, 8, 16, 32 e 60 segundos, mantendo
60 segundos nas tentativas seguintes. Não há limite de tentativas nem execução
do domínio pelo publisher. Cancelamento/erro de banco deixa a row disponível
para recuperação; crash após publish antes de commit permite replay.

A invalidação `conversation.updated` mantém seu envelope e não transporta roles
ou capabilities. Duplicates refazem a leitura autorizada. O fan-out existente
revalida cada subscription local/remota; endpoints de detalhes revalidam acesso.
O sidebar sinaliza todos os painéis abertos após o `onSubscribed` da #947 e após
`room_access_denied`. Eventos ordinários continuam invalidando apenas seu alvo.
A leitura usa o hook existente, abortando/ignorando respostas obsoletas. Nenhum
socket, poller ou store de ownership foi acrescentado.

O bus continua Pub/Sub: publish reconhecido não comprova recebimento individual.
Clientes desconectados convergem após recuperação das subscriptions. Falhas de
publish preservam backlog; nenhuma garantia exactly-once é introduzida.

## Operação e compatibilidade

Aplique a migration expansiva antes de iniciar a nova versão. Writers antigos
continuam gravando audit/outbox; publishers antigos continuam reconhecendo
`published_at` e usando o mesmo row lock. Durante convivência, a versão antiga
não respeita `next_attempt_at` nem preenche metadata de tentativa, mas mantém
segurança do claim e entrega at-least-once. O backoff completo passa a valer
quando todos os publishers forem atualizados. Down migration exige retornar os
publishers à versão anterior antes de remover as colunas.

Métricas sem labels específicos de recursos:

- `chat_ownership_outbox_pending`: todas as rows unpublished, inclusive retries.
- `chat_ownership_outbox_oldest_pending_age_seconds`: idade da mais antiga; zero
  quando backlog vazio.
- `chat_ownership_outbox_publish_failures_total`: falhas reais de publish nesta
  réplica, inclusive quando a gravação do resultado falha.
- `chat_ownership_outbox_retries_total`: publishes nesta réplica com tentativa
  anterior persistida. Crash pode perder metadata da tentativa anterior.

Gauges são atualizados antes e após dispatch e refletem o banco compartilhado:
use `max`, nunca `sum`, entre réplicas. Para counters, agregue `rate` por réplica.
Backlog/idade crescentes e taxa de falhas indicam verificar bus e conectividade
com PostgreSQL. Logs de dispatch não incluem payload, IDs ou erro bruto do bus.

## Evidência e reviews

Harness PostgreSQL exclusivo: container `nchat-1048-postgres`, porta `5548`, base
`ownership_953_test`. O harness recusa outro nome de database e recria schemas;
execute casos sequencialmente. O teste de duas réplicas coordena duas instâncias
com canais, mantendo um claim enquanto a segunda processa a outra row.

Testes concentram transação, failure/retry/backoff, crash e concorrência no
storage; métricas no registry existente; grupo/private channel, duplicate,
reconnect e acesso revogado em hooks com socket controlado e roteamento real.
Os testes existentes da #947, fan-out, payload e projeção complementam o fluxo.
Playwright e QA manual não são necessários para esses contratos.

Code Quality Review: responsabilidades separadas entre domínio, entrega,
observação e refetch. Helpers de teste dividem fases observáveis. Complexidade
Go medida com `gocyclo` e `gocognit`; callbacks frontend revisados e lintados.
Sem uso de SonarQube. Security Review separado: locks, replay, payload mínimo,
subscription obsoleta, revalidação server-side e isolamento por workspace.

## Resultado da validação

- PASS: transação/rollback, committed-only, retry/backoff, crash replay e duas
  réplicas com PostgreSQL real. Race detector passou nos blocos afetados e nos
  testes existentes de ownership após o ajuste de enqueue de backfill.
- PASS: refetch autorizado, cliente removido, cross-workspace e recurso inexistente
  para grupo e private channel; contrato HTTP existente usa 404 para recursos
  privados indisponíveis. Testes existentes de fan-out e payload mínimo passaram.
- PASS: realtime integrado com hooks e transporte controlado, duplicate e reconnect
  após confirmação de todas as subscriptions; nenhuma conexão adicional criada.
- PASS: métricas exercitadas pela iteração real do worker em failure/retry/success,
  incluindo backlog e idade em `/metrics`, sem labels de recursos.
- PASS: formatter, lint, vet/typecheck, migrations-check, build do chat-service e
  build web. Code Quality Review e Security Review feitos separadamente, sem
  bloqueios novos confirmados. Semgrep: 142 regras aplicáveis, seis arquivos de
  produção, zero findings e zero erros; SQL revisado manualmente.
- PASS: complexidades medidas de produção Go, máximo ciclomático 6 e cognitivo 7.
  Funções novas de teste também ficaram até 10; callbacks TS revisados no delta.
  Dois testes preexistentes do arquivo `ownership_store_test.go` têm complexidade
  cognitiva 11, sem aumento no delta.
- PASS: suíte frontend global, 240 arquivos/6.534 testes web e 43 arquivos/536 testes
  admin. Gates de cobertura frontend passaram; web: statements 95,53%, branches
  91,46%, functions 95,77%, lines 97,46%.
- FAIL: gate global `pnpm run ci`, executado uma única vez, no threshold de coverage
  do chat-service: 86,3%, exigência 90%. Etapas estáticas, infraestrutura, release,
  testes Go e cobertura frontend passaram antes dessa falha. Os builds globais
  posteriores não foram executados; builds afetados passaram separadamente.

A comparação dos perfis do checker mostra que a pendência já existia na base
#1047: 11.560/13.407 statements (86,2236%). #1048: 11.600/13.445 statements
(86,2774%). Não houve redução da cobertura e nenhum threshold foi alterado.
Perfis em `coverage/go/services_chat-service.threshold.out` de cada worktree;
log global em `/tmp/nchat-1048-ci.log`. Resultado Semgrep em
`/tmp/nchat-1048-semgrep.json`. Não foi executado CI remoto, Playwright ou QA manual.

Pendência de entrega: threshold global preexistente de 90%. Push, PR e merge
não realizados. O container de PostgreSQL exclusivo é parado após a validação
para preservar a reprodução sem manter o serviço em execução.
