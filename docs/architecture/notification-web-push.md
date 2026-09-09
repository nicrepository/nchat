# Entrega Web Push

Camada de entrega do pipeline de notificacoes (issue #746, parent #678).
Codigo em `services/notification-service/internal/worker/webpush_*.go`,
`internal/storage/push_delivery_store.go` e `internal/config/webpush.go`.

Reconcile do browser, digest pos-expediente e a UI de Perfil > Notificacoes
**estao fora** desta camada e continuam nao existindo. Service Worker e
`notificationclick` tambem estao fora dela, e passaram a existir na #747 — ver
[notification-service-worker.md](notification-service-worker.md), que consome
o payload descrito abaixo.

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

Seis campos, e nada mais e possivel: `chat.notification_outbox` nao guarda corpo
de mensagem, remetente nem preview (ver
[notification-outbox.md](notification-outbox.md)), entao esta camada nao teria de
onde tirar um sem derrotar a contencao com que a outbox foi construida. Quando o
produto decidir que remetente e preview podem sair, a struct ganha um campo e
`v` ganha um numero.

`source_type` e `source_id` **nomeiam** um recurso, e nomear nao concede nada. O
browser abre a aplicacao naquela referencia e a aplicacao pede o recurso ao
servidor exatamente como pediria a partir de um clique na sidebar. Deep-link nao
e autorizacao.

Limite de 3072 bytes, verificado antes de qualquer chamada ao provider.
Serializacao deterministica: duas codificacoes da mesma notificacao produzem
bytes identicos, entao um endpoint que recebe uma repeticao recebe a mesma
mensagem.

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

| Variavel                         | Formato                                          |
| -------------------------------- | ------------------------------------------------ |
| `NOTIFICATION_VAPID_PUBLIC_KEY`  | base64url, ponto P-256 nao comprimido (65 bytes) |
| `NOTIFICATION_VAPID_PRIVATE_KEY` | base64url, escalar P-256 (32 bytes)              |
| `NOTIFICATION_VAPID_SUBJECT`     | `mailto:...` ou `https://...`                    |
| `NOTIFICATION_PUSH_TTL_SECONDS`  | inteiro, default 14400                           |

Nao existe `NOTIFICATION_WEBPUSH_ENABLED`. Um segundo interruptor permitiria um
deployment "habilitado" sem chaves usaveis, que e uma configuracao que so falha
no momento em que importa. **Configurado e habilitado.**

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

As chaves seguem o padrao de secret ja adotado (Sealed Secrets); nenhum valor
real esta neste repositorio, e os testes geram o par que usam.

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
