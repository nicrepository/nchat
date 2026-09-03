# Notification outbox

Fundacao de notificacoes do NChat (issue #741, parent #678, RNF-25). Worker,
policy engine, Web Push, Service Worker, DND e UI **estao fora** desta camada e
nao existem ainda.

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

Durante a janela expand aberta pela migration `000042`, a UNIQUE legada
`(message_id, recipient_user_id, kind)` **continua existindo**: a release
anterior a nomeia no proprio `ON CONFLICT`. Ela e removida em uma release de
contract posterior, obrigatoriamente antes de conectar producers de reacao.

## Producers

| Event type        | Producer conectado                      |
| ----------------- | --------------------------------------- |
| `mention`         | sim                                     |
| `reply`           | sim (autor da mensagem respondida)      |
| `direct_message`  | sim (demais membros ativos da conversa) |
| `channel_message` | nao                                     |
| `reaction`        | nao                                     |
| `call`            | nao                                     |

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
