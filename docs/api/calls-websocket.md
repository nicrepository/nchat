# RF-23/RF-24/#622 — chamadas por WebSocket

O `chat-service` coordena o estado autoritativo. O cliente autentica o WebSocket
existente com o subprotocolo `nchat.v1`; `user_id` e `workspace_id` vêm da sessão,
nunca do payload.

Duas famílias de chamada compartilham o mesmo protocolo:

- **Direta (1:1)**: `target_user_id`, ringing/accept/decline/cancel/end. A
  issue #614 adiciona correlação opcional ao `call.sync`, preservando o
  comportamento legacy sem `sync_id`.
- **Recurso (canal ou DM em grupo)**: `target_type`/`target_id`, admissão
  multi-participante via lease. É aqui que a issue #622 adiciona discovery e
  join explícitos.

## Comandos — chamada direta

```json
{"type":"call.start","request_id":"<uuid>","target_user_id":"<uuid>","call_type":"audio"}
{"type":"call.accept","call_id":"<uuid>"}
{"type":"call.decline","call_id":"<uuid>"}
{"type":"call.cancel","call_id":"<uuid>"}
{"type":"call.end","call_id":"<uuid>"}
{"type":"call.sync"}
```

`call_type` aceita `audio` ou `video`. `request_id` torna o início idempotente
para o originador. Campos desconhecidos, identidade, participantes, sala, token
ou estado escolhidos pelo cliente são rejeitados. `call.start` direta continua
sem `call.admitted`; `response_to` só aparece no `call.error` de `call.sync`
quando o cliente fornece `sync_id`, conforme documentado abaixo.

### `call.sync` — resync autoritativo, `sync_id` opcional (issue #614)

`call_id` é opcional (vazio = "minha chamada atual"); ambas as formas
reautorizam pelo `CurrentCall`/`CurrentCallForUser` existente — identidade e
workspace continuam vindo exclusivamente da sessão, nunca do payload. Nenhum
participante, token, mídia ou outro dado novo é retornado em nenhuma das duas
formas; `call.sync` nunca vira broadcast.

**Legacy** (sem `sync_id` — todo cliente anterior à issue #614, incluindo o
resync automático de reconexão do `useCallSignaling`):

```json
{ "type": "call.sync", "call_id": "<uuid opcional>" }
```

Resposta: o mesmo formato de evento de lifecycle (`call.accepted`,
`call.ended`, ...) usado nos broadcasts abaixo — **sem** `sync_id` nem
`response_to`. Continua funcionando exatamente como antes; não é
correlacionável a uma requisição específica, por isso nunca deve ser usado
quando duas tentativas de sync da mesma chamada podem estar em voo ao mesmo
tempo.

**Correlacionado** (com `sync_id` — usado por `resolveCall` em
`apps/web/src/chat/resourceCallSignaling.ts`, por exemplo pelo fence de
recuperação de ownership em `CallSessionProvider.tsx`):

```json
{ "type": "call.sync", "call_id": "<uuid opcional>", "sync_id": "<uuid>" }
```

Resposta, **somente para quem pediu** (nunca broadcast):

```json
{
  "type": "call.synced",
  "sync_id": "<mesmo uuid>",
  "call": { "call_id": "<uuid>", "status": "active", "...": "..." }
}
```

`call.synced` nunca é enviado a observers nem a outra aba/conexão do mesmo
usuário. Duas chamadas a `call.sync` concorrentes, cada uma com seu próprio
`sync_id`, produzem duas respostas independentes — cada uma correlacionável
apenas pelo seu próprio `sync_id`, nunca pelo `call_id` (que pode ser o
mesmo nas duas). Um erro (`call_not_found`, por exemplo, quando a chamada já
não está mais `ringing`/`active`) carrega `response_to` igual ao `sync_id`
fornecido; se o cliente não enviou `sync_id`, o erro não carrega
`response_to`, igual ao comportamento legacy.

## Comandos — chamada de recurso (canal / DM em grupo)

```json
{"type":"call.start","request_id":"<uuid>","target_type":"channel","target_id":"<uuid>","call_type":"audio"}
{"type":"call.presence","call_id":"<uuid>","participation_id":"<uuid>"}
{"type":"call.leave","request_id":"<uuid>","call_id":"<uuid>","participation_id":"<uuid>"}
{"type":"call.end","call_id":"<uuid>"}
```

`call.start` cria a chamada canônica do recurso se não existir, ou reutiliza a
já ativa, e sempre faz uma nova admissão. `call.presence` renova somente o lease
identificado pelo fencing token; nunca insere nem ressuscita uma linha.
`call.leave` libera somente a admissão identificada pelo mesmo token, sem
encerrar a chamada para os demais; `call.end` só é aceito do `caller_id`
original e encerra para todos.

### `call.resource.sync` — discovery autoritativa (issue #622)

Pergunta "existe uma chamada ativa neste canal/DM agora?", sem participar, sem
lease e sem token. Serve para o carregamento inicial da tela e para reconexão
(reload, troca de aba, retomada de rede) — nunca é polling.

```json
{
  "type": "call.resource.sync",
  "sync_id": "<uuid>",
  "target_type": "channel",
  "target_id": "<uuid>"
}
```

Resposta, **somente para quem pediu** (nunca broadcast):

```json
{
  "type": "call.resource.synced",
  "sync_id": "<mesmo uuid>",
  "target_type": "channel",
  "target_id": "<uuid>",
  "call": null,
  "observed_at": "2026-08-10T12:00:00.123456Z"
}
```

`call` é a chamada ativa autoritativa (mesmo formato de `call` nos eventos
abaixo) ou `null` — e `null` é uma resposta válida e esperada, não um erro. Um
usuário sem acesso ao canal/DM recebe exatamente essa mesma resposta (`call:
null`): a ausência de acesso nunca é revelada por um formato diferente.
`authorized` e `found` são distintos apenas no storage do servidor; no
protocolo os dois casos colapsam no mesmo `call: null`.

`authorized + ativa`: `{"call": {...}, "observed_at": "..."}`.
`authorized + nenhuma`: `{"call": null, "observed_at": "..."}`.
`não autorizado`: exatamente a mesma resposta de "nenhuma" — indistinguível no
wire.

`observed_at` vem de uma única leitura autoritativa no banco (autorização +
busca da chamada num único statement, uma única snapshot — nunca duas leituras
que poderiam discordar entre si) e usa o instante em que essa leitura começou,
nunca o relógio da aplicação nem o relógio do navegador. Isso resolve uma race
concreta: uma resposta `null` que estava "em voo" no momento em que uma
chamada nova nasceu não pode, ao chegar no cliente, parecer mais recente do
que essa chamada.

**Regra de ordenação para o cliente, em toda comparação — nunca usar o relógio
do navegador:**

- Para uma resposta `call: null`, use `observed_at` como o instante da
  observação. Se o cliente já tem, em memória, uma chamada com
  `created_at`/`occurred_at` posterior a esse `observed_at`, essa chamada é
  mais nova que o `null` e **não deve ser apagada** por ele — o `null` estava
  "em voo" antes dela existir. Se não há chamada conhecida com timestamp
  posterior, o `null` é a palavra final até o próximo sinal.
- Para uma `Call` existente (em `call.resource.synced` ou em qualquer evento
  de lifecycle), use `version` para decidir entre duas visões da MESMA
  `call_id` — versão estritamente crescente vence, exatamente como já vale
  para os eventos de lifecycle hoje. `occurred_at`/`created_at` desempatam
  entre `call_id`s diferentes (por exemplo, ao comparar a chamada de um sync
  contra uma chamada nova de um `call_id` diferente que apareceu via
  broadcast).
- Um broadcast de lifecycle (`call.accepted`, `call.ended`) que chega
  **depois** que um `call.resource.sync` já estava em voo sempre vence: seu
  `occurred_at`/`version` é posterior a qualquer `observed_at` que o sync
  possa ter carregado, porque o broadcast só é publicado depois que a escrita
  que o causou already commitou — e o sync, por definição, leu um estado
  anterior ou concorrente a essa escrita. Um sync antigo nunca pode
  sobrescrever um evento mais novo porque o cliente aplica a mesma regra de
  versão/timestamp a ambos, não trata o sync como uma fonte especial que
  ignora a ordenação normal.

`call.resource.sync` nunca lista o workspace inteiro; é sempre escopado a um
único `target_type`/`target_id`, e esse alvo é reautorizado no momento da
chamada, nunca herdado de uma assinatura anterior.

### `call.join` — admissão explícita em chamada conhecida (issue #622)

Usado quando o cliente já sabe (via `call.resource.sync` ou via evento
recebido) que existe uma chamada ativa e quer entrar nela — sem criar uma nova
chamada.

```json
{
  "type": "call.join",
  "request_id": "<uuid>",
  "call_id": "<uuid>",
  "target_type": "channel",
  "target_id": "<uuid>"
}
```

O servidor revalida tudo a partir do zero antes de admitir: a chamada precisa
existir, ser uma chamada de recurso, estar `active`, e o
`target_type`/`target_id` enviado precisa bater com o que está persistido na
chamada — um `call_id` certo com um alvo errado falha exatamente como um
`call_id` inexistente, sem detalhe que distinga os dois casos. Entrar de novo
na mesma chamada (reconexão, segunda aba) é idempotente: sempre no máximo um
lease por (chamada, usuário). Uma chamada que já terminou não pode receber
novos joins; o cliente deve fazer um novo `call.resource.sync` e agir sobre o
resultado.

## `call.admitted` — confirmação correlacionada (issue #622)

`call.join` e o `call.start` de uma chamada de recurso respondem, **somente ao
requisitante**, com uma confirmação correlacionada por `request_id` — distinta
do broadcast de lifecycle (`call.accepted`) que os demais participantes/
observadores recebem via assinatura do canal/DM:

```json
{
  "type": "call.admitted",
  "operation": "call.join",
  "response_to": "<request_id do comando>",
  "call": { "call_id": "<uuid>", "status": "active", "version": 3, "...": "..." },
  "participation_id": "<uuid>"
}
```

`participation_id` é um UUID opaco, novo e não reutilizado para cada admissão
de recurso, inclusive rejoin, reconnect e handoff. É um fencing token privado
do requisitante: não é `Call.request_id`, não pertence a `Call`, não aparece em
`call.accepted`, `call.ended`, `call.resource.synced` nem é enviado a observers.
O cliente deve armazená-lo junto da tentativa que recebeu este ACK e usá-lo em
presence, leave e token de mídia.

`operation` é `"call.start"` ou `"call.join"`. O broadcast `call.accepted`
continua existindo para quem apenas observa o canal/DM (compatibilidade e
discovery via assinatura) — um cliente não deve tratá-lo como confirmação do
seu próprio comando; `call.admitted` é o único ACK. `call.start` de chamada
**direta** não muda: continua sem `call.admitted`, só o lifecycle broadcast
já existente (RF-23).

## ACK de `call.leave` e fencing stale

Um leave que remove a admissão atual responde somente ao requisitante:

```json
{
  "type": "call.left",
  "operation": "call.leave",
  "response_to": "<request_id>",
  "released": true,
  "call": { "call_id": "<uuid>", "status": "active", "...": "..." }
}
```

Se o token já foi substituído, nenhuma lease é removida e a resposta é
`call.error` com `code: "call_participation_stale"`, correlacionada pelo mesmo
`response_to`. O servidor não revela qual token é atual. Esse resultado não é
um lifecycle broadcast e não autoriza a aba stale a anunciar globalmente que
a participação mais nova saiu.

Presence com token stale usa o mesmo código e não altera expiry. Depois que a
lease atual sai, um presence stale continua sem inserir qualquer linha.

## Eventos (broadcast de lifecycle)

Uma chamada direta notifica os dois participantes; uma chamada de recurso
notifica os assinantes autorizados do canal/DM. Tipos:
`call.ringing`, `call.accepted`, `call.declined`, `call.cancelled`,
`call.timed_out`, `call.ended`. O envelope usa `schema_version`, `event_id`,
`target_type`, `target_id` e `call`:

```json
{
  "schema_version": 1,
  "type": "call.accepted",
  "event_id": "<uuid>",
  "target_type": "channel",
  "target_id": "<uuid-do-canal>",
  "call": {
    "call_id": "<uuid>",
    "request_id": "<uuid>",
    "caller_id": "<uuid>",
    "target_type": "channel",
    "target_id": "<uuid-do-canal>",
    "call_type": "video",
    "status": "active",
    "version": 2,
    "created_at": "2026-07-30T12:00:00Z",
    "occurred_at": "2026-07-30T12:00:04Z",
    "expires_at": "2026-07-30T12:00:30Z"
  }
}
```

Uma chamada direta carrega `callee_id` e `target_type: "user"`; uma chamada de
recurso nunca carrega `callee_id`. Estes eventos atravessam múltiplas réplicas
pelo barramento compartilhado — cada instância revalida o envelope (IDs,
correspondência de alvo, status) antes de repassar aos seus próprios clientes,
e a entrega em si segue a autorização normal de assinatura do alvo: observar
uma chamada de recurso (receber estes eventos) nunca equivale a participar
dela — participação é sempre e somente o lease de `chat.call_participant_leases`,
o único fato que autoriza um token de mídia (veja
`docs/api/media-livekit-token.md`).

O cliente aplica somente versões crescentes para o mesmo `call_id`.

## Erros correlacionados

Falhas usam `call.error` com `operation`, `call_id` quando aplicável, e
códigos estáveis: `call_invalid`, `call_not_found`, `call_invalid_state`,
`call_participant_busy`, `call_participation_stale`, `call_rate_limited` ou `call_unavailable`. Elas não
fecham a conexão.

Para `call.join`, `call.resource.sync`, `call.sync` com `sync_id` e o
`call.start` de uma chamada de recurso, o erro também carrega `response_to` —
o `request_id` do comando (ou o `sync_id`, no caso de `call.resource.sync` e
de `call.sync`) — para o cliente correlacionar sem ambiguidade. `call.start`
de chamada **direta**, `call.sync` sem `sync_id` (legacy) e os demais comandos
pré-existentes continuam sem `response_to`, inalterados por esta issue.

## Rollout fail-closed

A migration `chat/000035` adiciona `participation_id UUID` nullable. `NULL`
significa exclusivamente uma lease criada antes do protocolo fenced. Comandos
legacy sem `participation_id` só podem renovar/remover uma linha cujo valor
também seja `NULL`; nunca tocam uma lease fenced não nula. Toda nova admissão
escreve UUID não nulo. Depois que não houver clientes antigos nem linhas NULL,
uma migration futura poderá aplicar `NOT NULL`. Chamadas diretas permanecem
inalteradas e não usam este token.

`call_participant_busy` é específico de admissão. Em uma chamada direta, o
autor ou o destinatário pode estar ocupado por outra chamada incompatível. Ao
iniciar ou entrar em uma chamada de canal/DM em grupo, o próprio ator pode
estar ocupado por outra chamada incompatível. Entrar novamente na mesma
chamada de recurso ativa não conta como "outra chamada" e continua válido.
`call_invalid_state` permanece reservado para conflitos reais de
lifecycle/estado (por exemplo, `call.join` numa chamada que já terminou).
