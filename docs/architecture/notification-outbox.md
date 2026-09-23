# Notification outbox

Fundacao de notificacoes do NChat (issue #741, parent #678, RNF-25). Service
Worker, DND e UI **estao fora** desta camada. Quem decide se um evento vira
alerta e o Policy Engine (#744, `libs/go/platform/notificationpolicy`,
[notification-policy.md](notification-policy.md)); quem entrega e o worker
(#742) atraves da camada Web Push (#746,
[notification-web-push.md](notification-web-push.md)).

## Duas outboxes, propositos diferentes

| Tabela                     | Dono                               | O que carrega                                      |
| -------------------------- | ---------------------------------- | -------------------------------------------------- |
| `auth.email_outbox`        | notification-service (worker SMTP) | e-mails transacionais de auth, com payload cifrado |
| `chat.notification_outbox` | evento de produto                  | referencias a eventos notificaveis do chat         |

Nao sao a mesma coisa e nenhuma substitui a outra. `auth.email_outbox` e um
canal de entrega; `chat.notification_outbox` e o registro do evento, anterior a
qualquer canal.

## Por que fica no schema `chat`

A linha e escrita pela **mesma statement** que insere a mensagem
(`PGXMessageStore.CreateMessage`) e pela mesma que promove uma mensagem retida
por link scan (`ResolveDecidedMessages`). E isso que garante que nao existe
commit com mensagem sem notificacao, nem notificacao orfa apos rollback.

Uma tabela em um schema `notifications` teria de reproduzir esse boundary e
deixaria duas tabelas respondendo a mesma pergunta — a dupla fonte de verdade
que a issue proibe. O banco e um so (`nchat`); o worker do notification-service
vai ler esta tabela entre schemas exatamente como o worker SMTP ja le
`auth.email_outbox`.

## Estados

```text
pending ──► eligible ──► processing ──► sent
   │            │            │      └──► retrying ──► processing
   │            │            │                    └──► failed
   └────────────┴──► suppressed
```

- `suppressed`, `sent` e `failed` sao terminais e **mutuamente distintos**.
  "Ninguem foi avisado, de proposito", "alguem foi avisado" e "tentamos e nao
  deu" sao tres fatos diferentes.
- `evaluated` do diagrama da issue e a transicao, nao um estado em repouso: a
  linha sai de `pending` como `eligible` ou como `suppressed`.
- `suppressed_reason` existe se e somente se o estado e `suppressed`, e e
  limitado a 200 caracteres — e um codigo operacional (`quiet_hours`,
  `conversation_muted`), nao um lugar para texto. Ambas as metades estao na
  constraint `notification_outbox_suppressed_reason_check`; o lado Go e
  `notificationevent.SuppressedReasonMaxLen` / `ValidateSuppressedReason`.

O contrato em Go, com as transicoes validas, esta em
`libs/go/platform/notificationevent`. Fica em `libs` porque tem dois donos que
nao podem divergir: chat-service produz, notification-service vai consumir.

### Quem pode mudar o estado

Duas camadas, porque elas defendem coisas diferentes.

**`storage.PGXNotificationOutboxStore.TransitionState`** e o unico caminho
suportado da aplicacao. Ele valida `From`/`To` pelo dominio antes de escrever e
usa **compare-and-set**: o estado esperado entra no `WHERE`, entao duas replicas
avaliando a mesma linha nao aplicam as duas transicoes — a segunda casa zero
linhas e recebe `ErrNotificationStateConflict`. Nao ha leitura antes da escrita,
logo nao ha janela de corrida. Os dois producers so fazem `INSERT`; nada mais no
servico escreve `status`.

**O trigger `notification_outbox_enforce_transition`** e a autoridade final. Uma
CHECK constraint nao consegue expressar a regra porque ela e sobre a linha
anterior, entao a tabela de transicoes vive num trigger `BEFORE UPDATE OF status`
que espelha `stateTransitions` do pacote Go. E ele que faz um estado terminal ser
terminal de verdade: nenhum worker com bug, script de reparo ou sessao psql
transforma um `suppressed` em `sent`. Sem ele, os tres estados terminais seriam
apenas convencao de nomenclatura.

`TestNotificationOutboxStateMachine*PostgreSQL` exercita os dois lados, entao
Go e SQL nao podem divergir sem quebrar teste.

## Dedupe / idempotencia

`dedupe_key` = `<source_type>:<source_id>:<event_type>[:<discriminator>]`, com
`UNIQUE (workspace_id, recipient_user_id, dedupe_key)`.

- A autoridade e o indice, nao um `SELECT` anterior a um `INSERT`.
- O tenant qualifica a unicidade: dois workspaces nunca colidem.
- O `discriminator` impede chave larga demais: duas reacoes distintas na mesma
  mensagem compartilham o `message_id`, entao ator e emoji e que fazem delas dois
  eventos. Nenhum segmento pode conter `:`.

A UNIQUE legada `(message_id, recipient_user_id, kind)`, mantida pela janela
expand da `000042`, foi **removida pela `000050`** (issue #825). Ela expressava
"uma notificacao deste tipo por destinatario por mensagem", que e exatamente o
que um lembrete nao pode ser: o quinto lembrete de uma mensagem e uma quinta
linha, distinguida da quarta pelo numero da ocorrencia no `dedupe_key`. Mante-la
limitaria silenciosamente cada mensagem a um unico lembrete. Nenhum codigo da
release corrente a nomeia — os dois producers usam `ON CONFLICT DO NOTHING` sem
arbiter — e `notification_outbox_dedupe_uq` e estritamente mais fina, porque
qualifica por tenant.

## Producers

| Event type        | Producer conectado                      |
| ----------------- | --------------------------------------- |
| `mention`         | sim                                     |
| `reply`           | sim (autor da mensagem respondida)      |
| `direct_message`  | sim (demais membros ativos da conversa) |
| `channel_message` | nao                                     |
| `reaction`        | nao                                     |
| `call`            | nao                                     |
| `urgent_reminder` | sim (worker do notification-service)    |

Os destinatarios sao derivados **no servidor, em SQL**, nunca do request. Um
destinatario alcancado por mais de uma regra recebe **uma** linha, com a
classificacao mais forte (mention > reply > direct_message).

`channel_message` nao e produzido de proposito: fan-out sincrono para todos os
membros de um canal grande e o risco de amplificacao que a issue manda avaliar
antes, e quem quer notificacao de canal e questao de policy, nao de persistencia.

O fan-out e set-based (`INSERT ... SELECT`): o numero de statements nao cresce
com o numero de destinatarios.

## O que nao e persistido

Nenhum corpo de mensagem, nenhum token, nenhuma `PushSubscription`, nenhuma
chave push, nenhum payload de entrega. Apenas referencias. O que a entrega
precisa renderizar e lido depois, pelas mesmas projecoes autorizadas de sempre.
Logs da outbox nunca carregam conteudo de mensagem.

Isso continua valendo depois da #870, que deu banner com remetente e preview ao
Web Push. Ela **nao** adicionou coluna nenhuma aqui e nao tem migration: o
preview e resolvido por `presentationProjection` dentro do mesmo `ClaimDue` que
ja acontecia, contra o acesso do destinatario **no snapshot do statement ClaimDue**, e vive
somente na memoria do worker ate virar bytes de payload. Um snapshot gravado no
envio foi recusado por tres motivos, o ultimo decisivo: duplicaria texto numa
tabela com retencao propria, contradiria este invariante, e congelaria o preview
— uma mensagem apagada entre o envio e o push ainda enviaria o texto antigo. Ver
[notification-web-push.md](notification-web-push.md), "De onde vem o titulo e o
preview".

## Retencao (documentada, nao implementada)

Apenas linhas terminais (`sent`, `suppressed`, `failed`) acumulam. A politica
pretendida e delete periodico de terminais com mais de 30 dias, conduzido pelo
worker do notification-service quando existir, limitado por passo para nunca
segurar lock longo. `idx_notification_outbox_open` e parcial sobre os estados
nao terminais, entao nao cresce com o que a retencao removeria.

## Limites conhecidos

- `message_id` continua `NOT NULL`. Todo producer conectado hoje e originado em
  mensagem, e o FK com `ON DELETE CASCADE` garante que a notificacao nunca
  sobrevive a mensagem que a nomeia. Um producer de `call` — que nao tem
  mensagem — precisa de um `DROP NOT NULL`, que e uma operacao expand trivial.
- `chat.message_pending_mentions` guarda notificacoes de qualquer tipo desde a
  `000042`, apesar do nome — inclusive `kind` e `priority`, para que uma mensagem
  retida por link scan seja liberada como o que ela era e nao como mention.
  Renomear a tabela quebraria o slot anterior sob Blue/Green.
- `TransitionState` ainda nao tem chamador: o worker esta fora desta issue. Ele
  existe agora porque a alternativa era deixar o estado sem caminho definido ate
  la, e foi exatamente isso que a review apontou.

## Lembretes persistentes de mensagens urgentes (#825)

Uma mensagem `urgent` com `persistent_notifications=true` continua perguntando
ate cada destinatario confirmar, responder, ser cancelado pelo remetente ou os
lembretes acabarem. O primeiro alerta segue o fluxo normal acima; o que esta
secao descreve e a repeticao.

**Nao existe um segundo scheduler.** Um lembrete e uma linha comum de
`chat.notification_outbox`, com `kind = 'urgent_reminder'`: mesmo claim, mesma
lease, mesmo backoff, mesma policy de entrega, mesmas metricas. O que a #825
acrescenta e apenas a decisao de quando mais uma dessas linhas deve existir.

### Fonte de verdade

PostgreSQL, em duas tabelas que ja existiam:

- `chat.messages.persistent_notifications` — a intencao do autor. Como
  `priority` (#821) e `acknowledgement_required` (#824), nao autoriza nada.
  `messages_persistent_notifications_priority_check` recusa a combinacao em uma
  mensagem que nao seja `urgent`.
- `chat.message_acknowledgements` — o estado por destinatario, com as colunas
  `next_reminder_at` e `reminder_count`. **Nao e uma tabela nova**: a maquina de
  estados que a #825 precisa — `pending / acknowledged / responded / expired /
cancelled`, so `pending` elegivel — ja e dessa tabela, e a `000049` declarou o
  valor `expired` justamente para este worker. Uma segunda tabela seria uma
  segunda resposta para "esta pessoa ainda esta esperando", e toda transicao que
  a #824 ja escreve teria de ser espelhada nela.

Valkey nao participa. O estado nao vive em memoria, e nada aqui se perde quando
o notification-service reinicia: `next_reminder_at` **e** a fila.

Os endpoints da #824 nao enxergam linhas que nao sao suas — todas as leituras e
o `UPDATE` de acknowledge aplicam `a.acknowledgement_required` —, entao uma
mensagem que so pediu lembretes responde aquele endpoint exatamente como antes:
contagens zeradas, sem `viewer_state`, nada a confirmar.

### Intervalo, teto e duracao

| Decisao           | Valor               | Onde                                       |
| ----------------- | ------------------- | ------------------------------------------ |
| Intervalo nominal | 5 minutos           | `notificationevent.UrgentReminderInterval` |
| Teto de lembretes | 12                  | `notificationevent.MaxUrgentReminders`     |
| Duracao maxima    | 1 hora (12 x 5 min) | consequencia dos dois acima                |

O instante de referencia da regra temporal e **parametro**, nao `now()` do
banco: `ScheduleDueReminders(ctx, now, batchSize)` recebe o momento sobre o qual
raciocina — quais lembretes venceram, quando cai o proximo, e quando um
destinatario que esgotou foi resolvido. O worker passa `time.Now().UTC()` uma
vez por passada, entao producao nao muda; o teste fixa T0 e afirma a fronteira
dos 5 minutos com igualdade em vez de tolerancia. Um unico instante para os tres
usos, deliberadamente: ler `due` contra o parametro e calcular a proxima janela
com `now()` colocaria dois relogios no mesmo statement.

Ambos sao constantes de `libs/go/platform/notificationevent`, lidas pelos dois
servicos e **nao configuraveis pelo cliente nem pelo operador**: a #820 poe
intervalo customizavel fora de escopo, e nao existe campo de request, coluna ou
variavel de ambiente para nenhum dos dois.

O intervalo e nominal, nao garantia. Um lembrete fica _devido_ nesse intervalo e
e entregue pelo worker no proximo passe, entao o espacamento observado e esse
valor mais um poll interval, mais o que a policy e o push service acrescentarem.

Um teto (contagem) em vez de um deadline porque nao exige um segundo timestamp
nem uma segunda comparacao com relogio: o limite e exato, e alcanca-lo e o que
produz o `expired`. Doze lembretes e uma hora de insistencia — depois disso,
quem nao respondeu nao vai ser convencido por um decimo terceiro push.

### Ciclo de vida

```text
envio ─► next_reminder_at = created_at + 5min        (mensagem publicada)
        next_reminder_at = NULL                      (mensagem retida por link scan;
                                                      a promocao inicia o relogio)

a cada passe do worker, para cada linha devida:
  1. WITH due AS (... state='pending' ... FOR UPDATE SKIP LOCKED)
  2. INSERT no outbox, dedupe_key '<msg>:urgent_reminder:<ocorrencia>'
  3. reminder_count += 1
     next_reminder_at = now() + 5min   ou  NULL se atingiu o teto
     state = 'expired'                 se atingiu o teto E a mensagem nao pedia
                                       confirmacao

pending ─ acknowledge ─► acknowledged   \
        ─ reply ───────► responded       |  next_reminder_at = NULL
        ─ teto ────────► expired         |  na mesma escrita do estado
        ─ cancel ──────► cancelled      /
```

Os tres passos do ciclo sao **um unico statement**. Separados, uma queda entre
eles produziria ou um lembrete que ninguem avanca — o mesmo push a cada cinco
minutos, para sempre — ou um agendamento avancado sem lembrete nenhum.

### Dedupe e idempotencia

`dedupe_key = message:<message_id>:urgent_reminder:<ocorrencia>`, sob o mesmo
`UNIQUE (workspace_id, recipient_user_id, dedupe_key)` de sempre. A ocorrencia e
o que torna o n-esimo lembrete um evento logico diferente do (n-1)-esimo; sem
ela, o indice absorveria todos os lembretes como duplicata do primeiro.

A autoridade e o indice, nunca um `SELECT` anterior nem um flag em memoria — o
que faz a invariante valer entre processos e apos restart. Um passe repetido
depois de uma queda recalcula a mesma chave, nao insere nada e avanca o
agendamento mesmo assim, porque o lembrete que ele ia criar ja esta na fila.

Dois schedulers concorrentes recebem linhas disjuntas (`FOR UPDATE SKIP
LOCKED`), e o indice unico e a segunda linha de defesa atras disso.

### Estado terminal ganha do lembrete

O ponto de linearizacao e a **linha de `chat.message_acknowledgements`**, e e
`claimDueQuery` que o estabelece: o sublink de elegibilidade usa
`FOR UPDATE SKIP LOCKED`, entao o claim e uma transicao `PENDING -> terminal`
sao serializados sobre o mesmo estado persistido.

```text
CASO A  terminal vence
        ack/reply/cancel commita primeiro -> o claim reavalia sob o lock,
        ve o estado terminal, e NAO reivindica o lembrete.
        (ou: a transicao esta em voo e segura a linha -> SKIP LOCKED ->
         o lembrete nao e reivindicado nesta passada.)

CASO B  claim vence
        o claim trava a linha enquanto ela ainda esta PENDING; a transicao
        terminal espera esse lock e so aplica depois do commit do claim.
```

Nao existe ordem em que `PENDING` e observado, um estado terminal commita
concorrentemente, e o claim ainda e confirmado. Sem o lock isso existia: a
leitura era um `EXISTS` sem trava, o claim decidia pelo proprio snapshot, e o
lembrete acabava em `processing` contra alguem que ja tinha respondido.

**Ordem de locks: linha de recipient -> linha de outbox.** O sublink e avaliado
no filtro do scan, abaixo do `LockRows` externo, entao a acknowledgement e
sempre travada primeiro. `ScheduleDueReminders` usa a mesma ordem (trava a
acknowledgement, depois insere no outbox) e todas as transicoes do chat-service
travam apenas a linha de recipient. Nenhum caminho usa a ordem inversa, entao
nao ha ciclo.

O lock dura **um statement**. `ClaimDue` roda fora de qualquer transacao do
chamador e a entrega acontece depois que ele retorna, entao nada fica travado
durante uma chamada ao push provider.

`SuppressResolvedReminders` e a limpeza, nao a garantia: retira do backlog os
lembretes que nunca serao reivindicados, com
`suppressed_reason = 'recipient_resolved'`.

Uma linha ja em `processing` esta fora do alcance dela, e isso e correto e nao
uma lacuna: o claim e o instante em que o lembrete passa a estar _logicamente
adquirido para entrega_, e o lock prova que o destinatario ainda estava pendente
ali. Um acknowledge posterior e indistinguivel, do lado do destinatario, de um
que chegou depois do push ja ter sido entregue ao provider. Fechar essa janela
exigiria segurar uma transacao PostgreSQL durante o I/O externo, o que este
worker nao faz para notificacao nenhuma.

### Cancelamento pelo remetente

`DELETE /api/chat/messages/{messageID}/persistent-notifications`.

Autorizacao e `sender_id = <principal autenticado>` como predicado do proprio
`UPDATE`, com `workspace_id` vindo do workspace resolvido. Mensagem inexistente,
de outro tenant, de outro remetente ou que nunca pediu lembretes respondem todas
404 — nao da para descobrir qual das quatro.

O predicado de acesso a conversa e deliberadamente **nao** aplicado: um
remetente que saiu do canal continua sendo a pessoa cuja mensagem esta paginando
gente a cada cinco minutos, e recusar o botao de desligar seria o unico
resultado que ninguem aceitaria.

Cancelar nao apaga a mensagem e nao apaga acknowledgements. Em uma mensagem que
tambem pediu confirmacao, o estado permanece `pending` e so o agendamento e
limpo: retirar a pergunta que o remetente nao retirou seria dizer "cancelado"
para quem ainda ia responder. Onde o lembrete e a unica razao da linha existir, o
destinatario vira `cancelled`, como a maquina da #820 define.

### Policy de entrega

Cada lembrete atravessa `libs/go/platform/notificationpolicy` outra vez, com o
mute relido por linha pela projecao do outbox. `persistent_notifications` **nao
e bypass**: mute, origem, horario e canais decidem cada ocorrencia como decidiram
a primeira.

### Observabilidade

Valores do label fechado `result` de `nchat_notification_events_total`:
`reminder_scheduled`, `reminder_deduplicated`, `reminder_superseded`,
`reminder_expired`. Nenhum identificador vira label. O worker registra **uma
linha por passe**, com contagens — nunca uma por destinatario, senao uma mensagem
para duzentas pessoas escreveria duzentas linhas a cada cinco minutos.

### Consultas e indices

`idx_message_acknowledgements_due` e parcial sobre exatamente o predicado do
scheduler (`state = 'pending' AND next_reminder_at IS NOT NULL`), entao seu
tamanho e o numero de lembretes vivos e nao o de acknowledgements ja registrados.
Sem ele, procurar lembretes devidos a cada poll seria varredura sequencial numa
tabela que so cresce.
