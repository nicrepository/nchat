# Entrega Web Push

Camada de entrega do pipeline de notificacoes (issue #746, parent #678).
Codigo em `services/notification-service/internal/worker/webpush_*.go`,
`internal/storage/push_delivery_store.go` e `internal/config/webpush.go`.

Digest pos-expediente e a UI de Perfil > Notificacoes **estao fora** desta
camada. O digest continua nao existindo; a UI passou a existir e, desde a #862,
le a saude real do Web Push pelo reconcile do browser, nunca por esta camada.
Service Worker e `notificationclick` tambem estao fora dela, e passaram a
existir na #747 — ver
[notification-service-worker.md](notification-service-worker.md), que consome
o payload descrito abaixo. O reconcile do browser tambem esta fora, e passou a
existir na #748 — ver
[notification-web-push-reconcile.md](notification-web-push-reconcile.md).

## Onde encaixa

```text
chat.notification_outbox (#741)
  -> NotificationWorker (#742): claim, lease, backoff, teto de tentativas
  -> Policy Engine (#744): decide se o canal web_push e permitido
  -> WebPushDeliverer (#746): quais browsers, TTL, agregacao, dedupe
  -> VAPIDSender (#746): RFC 8291 + RFC 8292, HTTP, classificacao
  -> push service (FCM, Mozilla autopush, WNS)
```

Cada camada decide uma coisa e nenhuma decide a da vizinha.

| Camada             | Decide                                      | Nao decide               |
| ------------------ | ------------------------------------------- | ------------------------ |
| Policy Engine      | horario, DND, mute, origem, elegibilidade   | nada de entrega          |
| NotificationWorker | quando, claim, backoff, teto de tentativas  | nada de push             |
| `WebPushDeliverer` | quais endpoints, TTL, agregacao, dedupe     | nenhuma regra de negocio |
| `VAPIDSender`      | criptografia, HTTP, classificacao do status | nada alem disso          |

## Por que o provider nao pode ser alcancado por fora da policy

Nao por convencao — por construcao. O unico chamador de `Deliverer.Deliver` e
`NotificationWorker.deliverOne`, e a unica origem dos eventos que ele entrega e
`ClaimDue`, cujo statement seleciona linhas em `eligible`, `retrying` ou
`processing`. Uma linha chega a `eligible` exatamente uma vez, por
`MarkEvaluated`, a partir de um veredito cujo canal `web_push` foi permitido;
uma linha suprimida vai para `suppressed`, que e terminal e que o claim nao
seleciona.

Logo um evento negado pela policy, ou suprimido por estar fora do expediente,
nao e "pulado" na camada de entrega — ele **nunca se torna clamavel**, e nao
existe caminho de um handler HTTP ou de um frame WebSocket ate esta camada.

`TestASuppressedEventNeverReachesTheProvider` roda o worker real contra a outbox
falsa com um sender que conta chamadas, e a contagem e zero para os seis motivos
de supressao.

## O contrato do adapter

`PushMessage` tem cinco campos e nenhum e identificador:

```text
PushMessage { Endpoint, P256dh, Auth, Payload, TTL }
```

O sender nao sabe nomear a notificacao, a subscription, o destinatario nem o
workspace — logo nao pode registrar um em log, nao pode tomar decisao que dependa
de um, e nao pode virar um segundo lugar onde identidade e raciocinada.

`PushResult` volta com fatos operacionais apenas:

```text
PushResult { Class, StatusCode, RetryAfter, Latency }
```

Deliberadamente ausentes: o corpo da resposta, o texto de erro do provider,
qualquer header alem do `Retry-After`, e o endpoint. O corpo de erro de um push
service cita a requisicao de que reclama — ou seja, o endpoint, que e uma
capability URL.

## Classificacao

`classifyPushStatus` e a autoridade unica, uma funcao e um `switch`.

| Resposta         | Classe                             | Efeito                                      |
| ---------------- | ---------------------------------- | ------------------------------------------- |
| 2xx              | `delivered`                        | grava no ledger; endpoint nao e reenviado   |
| 404, 410         | `permanent_subscription_failure`   | invalida a subscription; nunca mais tentada |
| 429              | `rate_limited`                     | retry, respeitando `Retry-After`            |
| 5xx              | `provider_unavailable`             | retry pelo backoff do worker                |
| sem resposta     | `transient_failure`                | retry pelo backoff do worker                |
| demais 4xx e 3xx | `invalid_payload_or_configuration` | terminal; nunca entra em retry              |

Um 3xx chega em `invalid_payload_or_configuration` porque redirect **nao e
seguido**: um push service nao emite um, e um endpoint que emite esta tentando
mandar este cliente para outro lugar.

Qual status aposenta uma subscription continua sendo `domain.ClassifyDeliveryStatus`
(#745) — chamado, nunca reimplementado. Um lugar so decide que 404 e 410 sao os
dois unicos vereditos que podem desinscrever uma pessoa real.

## Fan-out e sucesso parcial

Uma notificacao vai para todos os browsers do destinatario; a linha da outbox
tem um estado so. A regra que reconcilia os dois:

```text
algum endpoint ainda merece retry  -> a linha entra em retry
senao, ao menos um entregue        -> a linha vira sent
senao                              -> a linha vira failed, definitivamente
```

A primeira clausula e o que torna o ledger necessario. Uma linha que entra em
retry porque um endpoint respondeu 5xx **nao pode** reenviar para o endpoint que
respondeu 2xx no mesmo passe — e nao reenvia: o proximo fan-out exclui todo
endpoint ja registrado como entregue.

Uma linha que termina `failed` depois de alguns endpoints terem recebido e
possivel, e e honesto: a linha da outbox registra o que aconteceu com o lote, o
ledger registra o que aconteceu com cada browser, e nenhum dos dois e obrigado a
responder a pergunta do outro.

Destinatario sem nenhum browser ativo e falha permanente, nao `sent`. `sent`
significa "alguem foi avisado", e ninguem foi.

`ListDeliverable` tambem exige, desde o SR-001, que a conta global do
destinatario esteja ativa e nao soft-deletada — o mesmo predicado e o mesmo alias
da projecao. Isso e **defense-in-depth**, nao o controle principal: o controle
que importa para o SR-001 esta em `presentationProjection`, porque um filtro aqui
chegaria tarde demais para impedir a materializacao do conteudo. O que este
segundo guard resolve e outra pergunta — uma conta suspensa nao deve receber nem
o banner generico da v1, que ainda revelaria que existe atividade a respeito dela
e quando. Uma conta filtrada aqui nao devolve target, o que o deliverer ja trata
como `errPushNoTarget`: permanente, logado, terminal.

O fan-out e **sequencial**. Uma pessoa tem um punhado de browsers, o worker ja
roda varias notificacoes concorrentemente, e uma segunda camada de goroutines
compraria nada mensuravel ao custo de cancelamento, ordenacao e um lugar para um
leak se esconder.

## Idempotencia, e onde ela para

A identidade logica de uma entrega e `(notification_id, subscription_id)`.

`chat.notification_push_deliveries` (migration `000046`) tem exatamente essa
chave primaria e uma linha **por entrega bem-sucedida**. Falha permanente nao
precisa de linha: o 404/410 aposenta a subscription (#745) e uma subscription
aposentada nao esta no fan-out. Falha transitoria precisa ser retentada, entao
registra-la nao mudaria nada.

Disso decorre, sem nenhum mecanismo adicional:

- um retry nao cria uma segunda entrega logica;
- um endpoint ja entregue nunca e selecionado de novo;
- um endpoint aposentado nunca e selecionado de novo;
- um replay escreve nada (`ON CONFLICT DO NOTHING`), entao a tabela nao cresce
  com repeticoes;
- dois workers disputando um claim nao produzem duas linhas.

### Um 410 vale para a geracao que respondeu, nao para a subscription

Um envio e sua resposta nao sao simultaneos. Enquanto um push esta em voo o
browser pode se re-registrar, e a subscription passa a ter a mesma identidade
logica (`id`) com uma **geracao** nova e um endpoint novo, ativo.

Se o 410 chega depois disso, ele fala de um endpoint que nao existe mais. O
compare-and-set em `(id, generation, status)` corretamente nao casa nada — e o
que esse "nada" significa e a informacao inteira, nao um detalhe. `RecordDelivery`
por isso devolve `domain.DeliveryApplication`, nao um booleano:

| Resultado do CAS        | O que aconteceu                            | Efeito na entrega                                                                           |
| ----------------------- | ------------------------------------------ | ------------------------------------------------------------------------------------------- |
| `ApplicationRecorded`   | aposentou a geracao que respondeu          | terminal para aquele target                                                                 |
| `ApplicationSuperseded` | nao casou, e a subscription esta **ativa** | **nao e terminal**: existe endpoint vivo sem a notificacao; o evento volta para reavaliacao |
| `ApplicationInactive`   | nao casou, e a subscription nao esta ativa | terminal: nada a aposentar, nada a entregar                                                 |
| `ApplicationMissing`    | a linha nao existe mais                    | terminal                                                                                    |

A classificacao e **um unico statement**: o CAS de sempre dentro de um CTE, com a
leitura do status no mesmo comando. Um `SELECT` separado depois de um `UPDATE`
de zero linhas deixaria uma janela em que a subscription muda entre os dois, e o
caller estaria classificando uma linha que nunca viu.

E deliberadamente **conservadora**: se a linha le como ativa, o resultado e
`Superseded`, independentemente da geracao. Errar nessa direcao custa uma
reavaliacao; errar na outra aposenta um endpoint vivo e perde a notificacao.

Consequencia no fan-out: um 410 superseded conta como retryable, o evento nao
termina `failed`, o endpoint antigo nao e chamado de novo (ele nao esta mais na
tabela) e a proxima passada entrega para a geracao nova. Endpoints ja entregues
no mesmo lote continuam fora do fan-out pelo ledger.

### Escrita duravel vs. historico operacional

Tudo isso depende de as escritas realmente acontecerem, entao a camada separa
duas classes e trata cada uma como merece:

| Escrita                        | Classe      | Se falhar                      |
| ------------------------------ | ----------- | ------------------------------ |
| ledger de um 2xx               | **duravel** | retentada; se nao landar, erro |
| invalidacao de um 404/410      | **duravel** | retentada; se nao landar, erro |
| `last_success_at` de um 2xx    | historico   | logada e seguida adiante       |
| `failure_count` de transitoria | historico   | logada e seguida adiante       |

As duas primeiras sao as unicas que **decidem** alguma coisa: sem a linha do
ledger o proximo retry reenvia para quem ja recebeu, e sem a invalidacao um
endpoint morto continua sendo tentado para sempre. Uma resposta terminal do
provider so e tratada como terminal depois que a escrita correspondente
commitou.

O que e retentado e **a escrita**, nunca o push: o provider ja aceitou ou ja
recusou, entao repetir a requisicao avisaria alguem duas vezes para consertar um
soluco de banco. Sao tres tentativas a 50 ms, canceladas junto com o contexto.

Se mesmo assim nao landar, o erro sobe para o worker como falha de storage — nunca
disfarcado de `PushProviderUnavailable`, que seria simplesmente falso — e a
linha da outbox volta para retry em vez de ser declarada `sent` pela palavra do
provider. O fan-out para naquele ponto: continuar geraria novas chamadas
externas com o estado interno ja sabidamente divergente, e parar nao custa nada,
porque todo endpoint ja gravado esta commitado e sai do proximo fan-out.

As duas classes ficam em metricas separadas: `unrecorded` no
`push_fanout_total` e a causa e o banco deste deployment, nao o push service de
ninguem.

### O limite real da garantia

Isso **nao** e exactly-once, e Web Push nao pode ser: o protocolo nao tem
idempotency key, entao nao ha nada para entregar a um push service que o
permitisse colapsar dois POSTs identicos.

A garantia depende inteiramente de existir prova **duravel** do accept. Sao
quatro casos, e cada um tem um teste:

**1. 2xx com o ledger gravado — o caso normal.**
A linha existe, a proxima avaliacao exclui aquela subscription, e nao ha reenvio
logico para aquele `notification + subscription`. Zero duplicatas.

**2. 2xx com soluco de storage recuperado na mesma execucao.**
O primeiro `MarkDelivered` falha, o `retryWrite` recupera. O provider e chamado
**uma vez**, o ledger fica terminal, e nao ha duplicata nenhuma. Retenta-se a
escrita, nunca o push.

**3. 2xx com o ledger falhando de forma persistente.**
As tres tentativas se esgotam e nada duravel registra o accept. Nenhuma execucao
futura tem como saber se o provider aceitou, se a requisicao saiu com resultado
desconhecido, ou se nunca chegou — entao a linha permanece retryable e a proxima
passada **envia de novo**.

Cada tentativa restante da outbox pode produzir mais uma entrega. O limite e o
orcamento de retry do worker — `NOTIFICATION_WORKER_MAX_ATTEMPTS`, default 6,
limitado a [1, 20] — e nao qualquer garantia do provider. Ao esgota-lo a linha
vira `failed` como qualquer outra falha. Nao ha multiplicacao ilimitada, e nao
ha exactly-once.

**4. Crash entre o accept e o commit.**

```text
push service aceita a mensagem
        |
        |  <- o processo morre aqui
        v
MarkDelivered faz commit
```

Mesma incerteza do caso 3: o lease expira, o evento e reclamado, e aquele
endpoint pode receber de novo. Limitado pelo mesmo orcamento de tentativas.

A janela **nao** e fechada escrevendo o ledger antes do envio. Isso registraria
uma entrega que nunca aconteceu e perderia a notificacao em silencio, que e
estritamente pior que uma duplicata.

> **Garantia oferecida: at-least-once por endpoint.** Uma entrega cujo accept
> nao consiga ser registrado de forma duravel pode ser reenviada uma vez por
> tentativa restante da outbox. O numero de duplicatas e governado por
> `MAX_ATTEMPTS`, nao por uma promessa do provider — e nunca e ilimitado.

## TTL

`NOTIFICATION_PUSH_TTL_SECONDS`, default 14400 (4h), limitado a [60, 86400].

```text
restante = TTL - (agora - occurred_at)
restante <= 0  ->  falha permanente, zero chamadas ao provider
```

Medido a partir de `occurred_at`, nunca da tentativa: uma notificacao retentada
por uma hora esta uma hora mais velha, e reiniciar o relogio a cada retry
deixaria um endpoint com defeito manter uma notificacao obsoleta viva
indefinidamente.

A fronteira e exclusiva — exatamente no TTL nao ha mais nada a entregar. O que
sobra vai no header `TTL:` do push service, em segundos inteiros, nunca negativo.
Um resto abaixo de um segundo vira zero, que e o jeito do proprio protocolo de
dizer "entregue agora ou descarte".

## Payload

Duas versoes existem e as duas sao validas no ar ao mesmo tempo. Qual delas este
deployment emite e `NOTIFICATION_PUSH_PREVIEW_ENABLED`; as duas o browser
aceita.

### v1 (#746) — referencias

```json
{
  "v": 1,
  "id": "<notification uuid>",
  "type": "mention",
  "source_type": "message",
  "source_id": "<uuid>",
  "occurred_at": "2026-09-08T14:30:00Z"
}
```

Seis campos. O Service Worker escolhe um titulo generico por `type` e nao mostra
corpo nenhum: "Voce foi mencionado no NChat".

### v2 (#870) — referencias mais titulo e preview

```json
{
  "v": 2,
  "id": "<notification uuid>",
  "type": "direct_message",
  "source_type": "message",
  "source_id": "<uuid>",
  "occurred_at": "2026-09-08T14:30:00Z",
  "title": "Ana Ribeiro",
  "body_preview": "consegue revisar o PR hoje?"
}
```

Os seis campos da v1, identicos, mais dois. Nada da v1 mudou de nome, de tipo ou
de significado — e o que permite um unico Service Worker ler as duas.

`title` e `body_preview` sao **opcionais** e `omitempty`. Ausente e um
significado, nao um acidente: quer dizer "o servidor nao tem nada que possa
mostrar desta vez", e o browser responde com exatamente o mesmo banner generico
da v1. Um payload v2 pode carregar titulo sem preview; nunca preview sem titulo.

Limite global de 3072 bytes, verificado antes de qualquer chamada ao provider.
Os dois campos novos tem limites proprios e muito menores (abaixo), entao o
limite global continua sendo protecao contra um campo futuro, nao um teto que
algo alcance.

Serializacao deterministica em ambas as versoes: duas codificacoes da mesma
notificacao e da mesma apresentacao produzem bytes identicos. Um novo claim
pode refletir edicoes ou revogacoes e gerar apresentacao diferente.

`source_type` e `source_id` **nomeiam** um recurso, e nomear nao concede nada. O
browser abre a aplicacao naquela referencia e a aplicacao pede o recurso ao
servidor exatamente como pediria a partir de um clique na sidebar. Deep-link nao
e autorizacao. A v2 nao adicionou referencia nenhuma: `title` carrega o _nome_ de
uma conversa, que e texto e nao referencia.

## De onde vem o titulo e o preview (#870)

```text
ClaimDue (um statement)
  -> presentationProjection  (storage/notification_outbox_store.go)
  -> NotificationEvent.Presentation
  -> presentationFor()       (worker/webpush_preview.go)  sanitiza, trunca
  -> pushPayload.Title / .BodyPreview
```

### Nada disso e persistido

`chat.notification_outbox` continua guardando **apenas referencias**: nenhum
corpo de mensagem, nenhum remetente, nenhum preview. A #870 nao adicionou coluna
nenhuma e nao tem migration.

O que a #870 usa e a regra que a propria
[notification-outbox.md](notification-outbox.md) ja enunciava: "o que a entrega
precisa renderizar e lido depois, pelas mesmas projecoes autorizadas de sempre".
`presentationProjection` e essa leitura, resolvida como subquery correlacionada
**dentro do claim que ja acontecia** — a mesma forma que a #744 usa para mute e a
#136 para o nivel de notificacao. Um batch de 50 notificacoes continua custando
um statement.

### Autorizacao e garantia temporal: claim-time

A projecao usa o snapshot PostgreSQL do statement `ClaimDue`, depois da policy.
Nao oferece autorizacao no instante de envio ao provider nem de exibicao no SO.

A autorizacao claim-time e a conjuncao de seis coisas, todas resolvidas no mesmo
statement:

| O que                               | Predicado                                                                                                                                                                                      |
| ----------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| workspace ativo                     | `w.status = 'active'`                                                                                                                                                                          |
| membership ativa no workspace       | `wm.user_id = o.recipient_user_id AND wm.status = 'active'`                                                                                                                                    |
| **conta global do recipient ativa** | `recipient_user.status = 'active'`                                                                                                                                                             |
| **recipient nao soft-deleted**      | `recipient_user.deleted_at IS NULL`                                                                                                                                                            |
| conversa/canal autorizado           | canal: mesmo workspace, `c.status='active'`, `chat.channel_visible_to_user(channel_id, o.recipient_user_id)`. DM: mesmo workspace, `d.status='active'`, `dm_members` ativo para esse recipient |
| mensagem publicavel                 | `m.workspace_id = o.workspace_id`, `kind='user'`, `status='active'`, `deleted_at IS NULL`, `link_safety_state <> 'malicious'`                                                                  |

As duas linhas em negrito sao a correcao do **SR-001**. Membership e conta sao
fatos diferentes: suspender alguem globalmente revoga as sessoes — todo read
deste produto passa por `authsession.ActiveSessionCTE`, que exige
`u.status='active' AND u.deleted_at IS NULL` — mas **nao** remove
`chat.workspace_members` nem `chat.push_subscriptions`, e nao apaga as linhas de
outbox ja escritas. Sem esses dois predicados um claim continuava materializando
remetente, contexto e corpo para uma conta que a UI autenticada ja recusava.

O predicado usa o alias `recipient_user` de proposito. O outro `auth.users` deste
statement e o **remetente** (`sender_user`), e ler um pelo outro foi exatamente o
defeito: o JOIN que existia validava quem escreveu, nunca quem ia receber.

Estes guards devem permanecer alinhados a `ListChannelMessages` e
`ListDMMessages` do chat-service, e a conta global alinhada a
`ActiveSessionCTE`. O helper SQL de canal nao verifica status do alvo/workspace;
nao existe helper SQL comum para DM nem para conta global.

### Revogacao antes do claim, e revogacao depois

A distincao e o que separa um bug de um limite documentado:

1. **Revogada ANTES do claim** — workspace desativado, membership removida ou
   suspensa, **conta global suspensa ou soft-deletada**, canal/DM arquivado,
   acesso ao canal revogado, mensagem apagada ou retida. A projecao devolve
   NULL e o preview **nao pode ser materializado**. Nada disso e TOCTOU: e
   simplesmente a autorizacao, e cada caso tem teste PostgreSQL real.
2. **Revogada DEPOIS do claim** — a linha ja foi lida. Isso permanece dentro do
   TOCTOU residual descrito logo abaixo, e cada retry faz um novo claim e
   reavalia tudo da tabela acima, inclusive a conta global.

A garantia continua sendo **claim-time**. O SR-001 nao a ampliou: ele acrescentou
um predicado que faltava ao conjunto avaliado nesse instante.

Todos os campos (`sender`, `context`, `group_dm`, `body`, `attachment`) usam o
mesmo predicado. Sem acesso ou mensagem publicavel, a projecao retorna NULL.

**Race residual:** apagar a mensagem, revogar acesso ou mudar seu estado depois
do snapshot nao retira o conteudo ja materializado. O batch pode aguardar vaga
nos workers e realizar fan-out por subscriptions. O contexto da passada comeca
antes do claim e tem budget `ceil(batch/concurrency) * deliveryTimeout + 5s`
(25s nos defaults: 10/5, timeout 10s); cancelamento e cooperativo, nao uma
promessa de prazo absoluto. A configuracao exige lease maior que esse budget.
Depois de aceito, o provider ainda pode aguardar o dispositivo ate o TTL restante
(default 4h, maximo 24h desde o evento); nao existe revogacao desse payload.
Cada retry faz um novo claim e reavalia acesso/estado.

Nao se mantem transacao ou locks durante IO externo. Uma segunda consulta logo
apos o claim nao eliminaria espera no batch, fan-out ou fila do provider.
A garantia deliberada e claim-time; consumidores que exigem revogacao imediata
nao devem ativar previews. Testes reais cobrem revogacao antes do claim e entre
um claim e seu retry.

### Custo com preview desligado

`ListPending` nunca le apresentacao. `ClaimDue` recebe explicitamente a mesma
flag do deliverer e usa `CASE WHEN $4::boolean THEN (...) ELSE NULL::jsonb END`.
Com a flag false, PostgreSQL nao executa a subquery de apresentacao; o payload
continua v1. Policy, SKIP LOCKED, lease e dedupe continuam no mesmo pipeline.
O teste `TestNotificationPreviewClaimPlanPostgreSQL` executa a query real com
`EXPLAIN (ANALYZE, BUFFERS)` e rollback, para 50 recipients, comparando off/on e
verificando zero acessos ao remetente no caminho off.

### Regras de titulo e corpo

| Caso                                          | `title`               | `body_preview`    |
| --------------------------------------------- | --------------------- | ----------------- |
| DM 1:1                                        | nome do remetente     | texto da mensagem |
| DM em grupo (com titulo)                      | `Remetente · Titulo`  | texto da mensagem |
| DM em grupo (sem titulo)                      | `Remetente · Grupo`   | texto da mensagem |
| Canal (`mention`, `reply`, `channel_message`) | `Remetente · #Canal`  | texto da mensagem |
| `urgent_reminder` (#825)                      | `Urgente: ` + o acima | texto da mensagem |
| So anexo                                      | como acima            | `Enviou um anexo` |
| Sem texto e sem anexo                         | como acima            | ausente           |
| Nao publicavel / sem acesso                   | ausente               | ausente           |

`reaction` e `call` nao tem produtor de mensagem hoje; se ganharem um, caem na
mesma regra pelo `source_type`.

`Grupo` e um fallback fixo do servidor quando o titulo do grupo e NULL, vazio
ou fica vazio apos sanitizacao (inclusive whitespace). A flag interna `group_dm`
preserva esse contexto; nao e um valor fornecido pelo cliente nem um campo novo
do payload publico.

O nome do arquivo de um anexo **nao** entra: um nome de arquivo pode ser tao
revelador quanto um corpo, e nunca foi o que a #870 autorizou.

### Sanitizacao e truncamento

`showNotification()` renderiza **texto**, nao markup — nao ha elemento, nao ha
`innerHTML`, nao ha parser. O risco de um preview nao e injecao numa pagina; e o
que os caracteres _dizem_ e onde acabam. Entao:

1. UTF-8 invalido e removido.
2. Caracteres de controle e da categoria Unicode `Cf` sao removidos. Os overrides
   bidirecionais (`U+202A`..`U+202E`, `U+2066`..`U+2069`) sao o motivo de isso ser
   seguranca e nao arrumacao: eles fazem o texto renderizar numa ordem diferente
   da que foi escrita, e um remetente poderia compor uma mensagem que o banner
   exibe como algo que ele nao disse.
3. Sequencias de espaco em branco viram um espaco; as pontas sao aparadas.
4. Trunca por **caracteres e bytes ao mesmo tempo**, sempre em fronteira de rune,
   marcando com `…`. A marca conta contra os dois orcamentos.

Markdown **nao** e removido: `**negrito**` chega como os caracteres que e, porque
nao ha parser do outro lado, e remove-lo seria uma decisao de renderizacao que
esta camada teria de manter em sincronia com o renderizador de mensagens para
sempre.

Sao dois conjuntos de limites, e confundi-los leva a conclusoes erradas sobre o
que sai da UI autenticada.

**Limites finais de apresentacao** — o que o banner mostra, aplicados em Go por
`webpush_preview.go` depois da sanitizacao:

| Limite          | Valor                 |
| --------------- | --------------------- |
| titulo          | 80 runes / 200 bytes  |
| preview         | 140 runes / 320 bytes |
| payload inteiro | 3072 bytes            |

**Tetos de transporte da projection** — quanto `presentationProjection` le do
banco e traz na linha do claim. Sao `left()` na lista do `SELECT`, nunca em
predicado, entao nao mudam quais linhas qualificam nem o plano da query:

| Campo             | Teto de leitura                    |
| ----------------- | ---------------------------------- |
| remetente         | `left(u.display_name, 100)`        |
| nome do canal     | `left(c.display_name, 100)`        |
| titulo do grupo   | `left(COALESCE(d.title, ''), 120)` |
| corpo da mensagem | `left(m.body_text, 500)`           |

Estes **nao** sao os limites apresentados no Web Push. Um valor que passa por
eles ainda e sanitizado e truncado em Go para 80 runes / 200 bytes (titulo) e
140 runes / 320 bytes (preview), que continuam sendo o contrato. O teto de
leitura existe so para nao transportar, por linha e por lote, um valor que a
etapa seguinte vai jogar fora quase inteiro.

De onde vem cada numero, e por que um deles e diferente dos outros:

- `chat.channels.display_name` tem limite de schema de **100**
  (`channels_display_name_length_check`, 1..100);
- `chat.dm_conversations.title` tem limite de schema de **120**
  (`dm_conversations_title_length_check`, `<= 120`);
- `auth.users.display_name` **nao tem CHECK equivalente**. O caminho
  self-service limita a 80 runes (`selfDisplayNameMaxLen` do auth-service), mas
  esse e um escritor entre varios e o banco nao impoe nada. Logo o
  `left(..., 100)` ali nao esta apenas repetindo um limite que ja existe: e
  tambem a defesa contra um valor patologicamente grande chegar ao claim.

Nos dois primeiros casos o teto e igual ao do schema de proposito, para que um
nome que alguem escolheu legitimamente chegue inteiro;
`TestNotificationPresentationBoundsTheSenderAndContextPostgreSQL` assere as duas
direcoes — o display name sem CHECK e cortado, e um nome de canal de 100 ou um
titulo de grupo de 120 chegam sem corte.

### O que nunca entra no payload

Token, sessao, capability, chave VAPID, `p256dh`, `auth`, endpoint, e-mail do
remetente, id interno desnecessario, detalhe de infraestrutura. A struct
`pushPayload` e fechada e cada campo e um valor que o destinatario ja tem direito
de ver — garantia mais forte do que uma etapa de redacao que alguem precisa
lembrar de aplicar.

### Privacidade: o que passou a poder sair da UI autenticada

Isto e o ponto da issue e nao um detalhe. Antes da #870 um push que chegasse ao
dispositivo errado revelava que _uma_ notificacao existe. Depois da #870 ele
revela tambem **um nome, um lugar e ate 140 caracteres do que foi dito** — numa
tela de bloqueio, por cima do ombro de alguem, num screenshot.

Esse e o custo deliberado do recurso, e e a razao de tudo que o limita: o preview
e curto, e texto puro, e resolvido por destinatario, e ausente para qualquer
coisa que o destinatario nao possa ver no snapshot do claim, e a versao inteira fica desligada a menos
que um operador ligue.

**Nao existe preferencia por usuario de "ocultar conteudo nas notificacoes"** no
NChat, nem antes nem depois desta issue. A #870 deliberadamente nao inventou uma:
uma preferencia nova precisa de coluna, de endpoint, de UI em Perfil >
Notificacoes e de uma decisao de produto sobre o default, e nada disso cabia
aqui. A politica MVP e portanto **por deployment**: ligado ou desligado para
todo mundo, desligado por padrao. Uma preferencia por usuario e o proximo passo
obvio e ela nao invalida nada do que esta aqui — o preview ja e produzido por
destinatario, entao seria mais um predicado na mesma projecao.

### Titulo e preview jamais aparecem em log, metrica ou trace

`logOutcome`, `logAttempt`, `logDecision` e `logStoreFailure` carregam
identificadores e categorias fechadas — `notification_id`, `subscription_id`,
`attempt`, `result`, `error_category`, `worker_id` — e nada mais. A mesma
contencao que a camada ja aplicava a corpo de resposta de provider vale para o
preview. `TestPushLogsExcludeSensitiveValuesAcrossOutcomes` procura os valores completos
das sentinelas de remetente, contexto, titulo e corpo no buffer bruto de logs,
sem depender de chaves ou serializacao. Tambem verifica endpoint, p256dh e auth
em sucesso, 410, 429 e falha transitoria. A fixture nao carrega email.

## Retry-After

O unico caso em que o adapter sabe algo que `RetryPolicy` nao consegue deduzir.

```text
delay = max(backoff(attempt), Retry-After), limitado a RetryMaxSeconds
```

Os dois limites carregam peso. Adotar o valor do provider diretamente deixaria um
429 respondido com `1` substituir o backoff exponencial por um loop apertado
contra algo que ja esta nos limitando. Honra-lo sem teto deixaria um header
estacionar a notificacao alem do proprio TTL. Ausente, malformado, negativo ou
absurdamente distante: descartado, e o backoff decide.

`Retry-After` so e lido em um 429. Um push service que o marca num 503 nao esta
pedindo uma espera especifica.

## Configuracao

| Variavel                            | Formato                                          |
| ----------------------------------- | ------------------------------------------------ |
| `NOTIFICATION_VAPID_PUBLIC_KEY`     | base64url, ponto P-256 nao comprimido (65 bytes) |
| `NOTIFICATION_VAPID_PRIVATE_KEY`    | base64url, escalar P-256 (32 bytes)              |
| `NOTIFICATION_VAPID_SUBJECT`        | `mailto:...` ou `https://...`                    |
| `NOTIFICATION_PUSH_TTL_SECONDS`     | inteiro, default 14400                           |
| `NOTIFICATION_PUSH_PREVIEW_ENABLED` | booleano, default **false** (#870)               |

Nao existe `NOTIFICATION_WEBPUSH_ENABLED`. Um segundo interruptor permitiria um
deployment "habilitado" sem chaves usaveis, que e uma configuracao que so falha
no momento em que importa. **Configurado e habilitado.**

### `NOTIFICATION_PUSH_PREVIEW_ENABLED`, e por que ele existe (#870)

`false` emite v1, `true` emite v2. Dois motivos independentes, e cada um sozinho
justificaria o interruptor.

**Rollout.** O ciclo de vida do Service Worker e deliberadamente o default do
browser — sem `skipWaiting()`, sem `clients.claim()` (#747) — entao um worker
novo so ativa depois que **toda** aba da origem fechou, o que pode levar dias. Um
worker anterior a #870 **recusa** um payload v2 e nao mostra nada, porque falhar
fechado numa versao desconhecida e a regra que torna o contrato seguro. Subir a
versao incondicionalmente silenciaria o push exatamente para quem nao reiniciou o
browser.

A ordem, portanto:

```text
1. deploy do Service Worker novo   (aceita v1 e v2; o servidor ainda manda v1)
2. janela de ativacao              (dias; nada muda para ninguem)
3. NOTIFICATION_PUSH_PREVIEW_ENABLED=true
```

Reverter e so voltar a variavel para `false`: nao ha migration, nao ha estado
persistido e nenhuma notificacao ja na fila fica invalida.

**Privacidade.** Este e o interruptor que decide se texto de mensagem pode sair
da UI autenticada e aparecer na tela de bloqueio de um sistema operacional. Ver
"Privacidade" acima para a politica MVP e por que ela e por deployment.

### Validacao semantica, nao estrutural

Base64 e tamanho nao provam nada. Um escalar de 32 bytes pode ser zero ou maior
que a ordem da curva; 65 bytes comecando em `0x04` podem nomear um ponto que nao
esta na P-256; e duas chaves individualmente validas podem ser de pares
diferentes. Cada um desses casos passava por uma checagem estrutural, subia um
worker e falhava na primeira notificacao — onde a falha era classificada como
erro permanente de entrega e a linha da outbox era aposentada. Erro de
configuracao virava notificacao perdida.

`crypto/ecdh` responde tudo isso e nada disso e escrito aqui:
`NewPrivateKey` recusa tamanho errado, escalar zero e qualquer valor a partir da
ordem; `NewPublicKey` recusa tamanho errado, encoding comprimido e ponto fora da
curva; `PublicKey.Equal` decide se as duas sao um par. **Nao ha aritmetica de
curva neste pacote e nao pode haver.**

O alfabeto aceito e base64url apenas, padded ou nao — nao por rigor, mas por
compatibilidade: `webpush-go` decodifica chaves VAPID so com `URLEncoding` e
`RawURLEncoding`, entao uma chave no alfabeto padrao seria aceita aqui e
recusada la, que e exatamente a classe de defeito que esta validacao existe para
eliminar.

### Fail-safe

Configuracao invalida e fail-safe por dois caminhos, e o segundo e o que fecha
o buraco:

- `newWebPushDeliverer` devolve `nil` e o worker se recusa a subir;
- `Config.NotificationWorkerReady` consulta o canal, entao o readiness probe
  reprova. Sem isso o pod ficava verde com o worker parado — uma replica
  aparentemente saudavel e um backlog que ninguem drena.

Worker desabilitado (o default) continua pronto sem nenhuma chave: Web Push e
opt-in e sua ausencia nao e defeito. Uma vez habilitado, ausente, parcial,
invalido e par trocado sao todos reprovados no startup, antes de qualquer evento
ser claimado.

O motivo nomeia a variavel e nunca o valor — esta e a ultima camada onde uma
chave VAPID poderia virar linha de log.

As chaves seguem o padrao de secret ja adotado (Sealed Secrets), no Secret
proprio `nchat-webpush` montado so por este servico (#862); nenhum valor real
esta neste repositorio, e os testes geram o par que usam. Procedimento:
[sealed-secrets-rotation.md](../runbooks/sealed-secrets-rotation.md).

### A chave publica no browser (#862)

O browser nao recebe a chave pelo build. `GET /api/notifications/push/config`,
autenticada e montada junto com as rotas de subscription, devolve
`{"vapid_public_key": "<base64url sem padding>"}` — ou `null` quando o worker
esta desligado ou `NotificationWorkerReady` reprova. Dois motivos:

- **coerencia por construcao**: quem serve a chave publica e o processo que
  assina com a privada, entao "frontend com a chave A, backend assinando com B"
  nao e uma configuracao que se consiga escrever;
- **uma imagem por SHA**: o `web` e construido uma vez e promovido por digest;
  um `VITE_*` por ambiente exigiria uma imagem por ambiente.

`null` e o `not_configured` do cliente. Um deployment com as chaves certas e o
worker desligado tambem responde `null`: nenhuma subscription e criada para um
canal que ninguem drena.

Configurado nao e o mesmo que rodando. Um worker habilitado e com configuracao
valida pode parar depois do boot (lease recusado, contexto encerrado), e para
esse caso a rota responde **503 `push_delivery_unavailable`**, sem chave e sem
detalhe. O que decide e `notificationWorkerAlive` — o mesmo probe
(`App.NotificationWorkerRunning`) que o check `notification-worker-running` do
`/readyz` le —, entao readiness e `/push/config` nao conseguem discordar sobre o
mesmo worker. O cliente trata o 503 como `error` de backend e oferece tentar de
novo; "este ambiente nao entrega push" continua reservado ao `null`.

| Situacao                                 | Resposta                        |
| ---------------------------------------- | ------------------------------- |
| worker desligado                         | 200 `vapid_public_key: null`    |
| worker habilitado, configuracao invalida | 200 `vapid_public_key: null`    |
| worker habilitado, configurado e rodando | 200 com a chave                 |
| worker habilitado, configurado e parado  | 503 `push_delivery_unavailable` |

## Observabilidade

Metricas, todas com um unico label `result` de conjunto fechado:

- `nchat_notification_push_attempts_total{result}` — uma tentativa contra um browser;
- `nchat_notification_push_duration_seconds{result}` — latencia do provider;
- `nchat_notification_push_fanout_total{result}` — `delivered`, `partial`,
  `failed`, `no_target`, `expired`.

Notificacao, subscription e host do endpoint **nao** sao labels: os dois
primeiros sao identificadores que este servico existe para manter privados, e o
terceiro cresce com o mercado de browsers.

Log por tentativa: `notification_id`, `subscription_id`, `attempt`, `result`,
`status_code`, `latency_ms`, `invalidation_reason`. Endpoint, `p256dh`, `auth`,
`Authorization` e o payload nao estao la e nao podem estar — `PushResult` nao
carrega nenhum deles, entao nao ha o que registrar nem por engano.

## SSRF

O endpoint e a unica entrada controlavel pelo usuario nesta camada.

A validacao de registro (#745) ja exige https absoluto, com host, sem userinfo e
sem fragment, ate 2048 bytes. Esta camada acrescenta defesa em profundidade:

- `domain.ValidateEndpoint` roda de novo, no ultimo ponto antes de uma
  requisicao sair — redundante por construcao, e barato o bastante para garantir
  que uma linha que chegou a tabela por outro caminho (um dump restaurado, um
  script de reparo) nao mande este cliente a lugar nenhum;
- `CheckRedirect` devolve `http.ErrUseLastResponse`: redirects nao sao seguidos;
- `net.Dialer.Control` recusa destino que nao seja internet publica —
  loopback, privado, link-local, ULA, multicast, unspecified, e IPv4 privado em
  forma 4-em-6. Roda **depois** da resolucao e **antes** do connect, sobre o
  literal que sera usado, entao nao sobra nome para re-resolver e DNS rebinding
  fica inaplicavel, nao apenas improvavel;
- `TLSClientConfig` fica no default: certificados sao verificados contra o
  hostname da URL. Nada aqui enfraquece isso.

Todo push service real e publicamente roteavel, entao nada legitimo se perde.

No cluster, a camada de fora e a NetworkPolicy
`nchat-allow-notification-webpush-egress` (#862, em
`infra/k8s/components/least-privilege-network-policies`): so egress, so
`component: notification`, so TCP/443, para a internet publica menos RFC 1918
(que contem os CIDRs de pod e service do k3s), CGNAT, loopback, link-local
(metadata de cloud), benchmarking, multicast e reservados — a mesma lista da
policy do RF-21, numa policy propria. `scripts/ci/lib/k8s-public-egress.sh`
nomeia as tres unicas policies autorizadas a citar `0.0.0.0/0` e reprova
qualquer outra; `scripts/ci/test_k8s_public_egress.sh` prova que cada regra
rejeita o que diz rejeitar.

Nenhum endpoint HTTP novo foi criado. A #746 nao precisa de um, e um "endpoint de
teste" seria superficie de relay que a issue manda evitar.

## Dependencia

`github.com/SherClockHolmes/webpush-go v1.4.0` (MIT).

RFC 8291 e RFC 8292 sao protocolos criptograficos com implementacao Go madura e
amplamente usada. Compor ECDH, HKDF e AES-GCM a mao aqui para evitar uma
dependencia seria escrever codigo criptografico novo para economizar uma linha de
`go.mod`.

As duas dependencias transitivas ja estavam no grafo deste modulo:
`golang-jwt/jwt/v5`, que o servico ja usa para validar access tokens, e
`golang.org/x/crypto`.

A biblioteca e usada para exatamente uma coisa — cifrar e enviar. Qual cliente
HTTP, quais destinos sao permitidos, o que um status significa e o que pode sair
num resultado sao decisoes desta camada.

## Limites conhecidos

- Nao ha digest pos-expediente. Eventos suprimidos por horario ficam
  `suppressed`, que e terminal: eles nao viram avalanche tardia porque nunca
  viram push nenhum.
- `WebPushAvailable` continua `true` no adapter da policy (#744). O worker nao
  consulta subscriptions antes de avaliar, e passar a consultar seria uma query
  por evento no caminho da policy. O resultado pratico e o mesmo: um
  destinatario sem browser produz uma linha `failed` em vez de uma `suppressed`.
- Nao ha retencao propria: o ledger cai junto com a linha da outbox e junto com
  a subscription, pelos dois `ON DELETE CASCADE`.
