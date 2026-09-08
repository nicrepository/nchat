# Notification-service: PushSubscription (Web Push)

A `PushSubscription` e uma entidade operacional persistida no backend, nao um
estado efemero do browser. Estas rotas registram, reconciliam e desligam a
subscription **do proprio usuario autenticado**.

Esta issue (#745) nao entrega envio de Web Push, Service Worker, reconcile de
frontend nem diagnostico. Ela entrega o modelo, a persistencia e os contratos
HTTP que essas tarefas vao consumir.

## Rotas

Todas exigem `Authorization: Bearer <access-token>`.

| Metodo   | Rota                                         | Efeito                                |
| -------- | -------------------------------------------- | ------------------------------------- |
| `POST`   | `/api/notifications/push/subscriptions`      | registra ou re-registra (idempotente) |
| `GET`    | `/api/notifications/push/subscriptions`      | estado para reconcile do cliente      |
| `DELETE` | `/api/notifications/push/subscriptions/{id}` | desliga uma subscription propria      |

O prefixo `/api/notifications` ja e roteado para o `notification-service` pelo
ingress; nenhuma mudanca de gateway foi necessaria.

## POST — registrar

Exige `Content-Type: application/json`. O corpo tem limite de 3 KiB e **rejeita
qualquer campo desconhecido**.

```json
{
  "device_id": "5f8a1c2e-0000-4000-8000-000000000001",
  "endpoint": "https://push.example.com/subscription/abc123",
  "p256dh": "<chave publica P-256, base64>",
  "auth": "<segredo de autenticacao, base64>"
}
```

Resposta `200 OK` — no primeiro registro e em todo retry:

```json
{
  "data": {
    "id": "6f0c...",
    "device_id": "5f8a1c2e-0000-4000-8000-000000000001",
    "status": "active",
    "created_at": "2026-09-08T12:00:00Z",
    "last_seen_at": "2026-09-08T12:00:00Z"
  }
}
```

Validacao server-side:

| Campo       | Regra                                                                       |
| ----------- | --------------------------------------------------------------------------- |
| `device_id` | `^[A-Za-z0-9_.:-]{1,128}$`                                                  |
| `endpoint`  | URL `https` absoluta com hostname, sem userinfo e sem fragmento, ate 2048 B |
| `p256dh`    | base64 (url-safe ou padrao) de exatamente 65 bytes, iniciando em `0x04`     |
| `auth`      | base64 (url-safe ou padrao) de exatamente 16 bytes                          |

Os tamanhos das chaves vem da RFC 8291 (§3 e §4). O teto de 2048 bytes do
endpoint e o mesmo limite que o repositorio ja aplica a URL enviada por cliente
(`linkpreview.MaxURLLength`) e cabe com folga numa entrada de indice btree.

## Identidade e idempotencia

A identidade logica e **(workspace, usuario, device)**. Nao existe "uma
subscription por usuario": varios browsers e varios devices coexistem, e cada um
continua funcionando por conta propria.

| Situacao                                                  | Resultado                                     |
| --------------------------------------------------------- | --------------------------------------------- |
| mesma instance reapresenta o mesmo endpoint e as chaves   | `200`, mesma linha, mesmo `id`, mesma geracao |
| mesma instance reapresenta um endpoint novo               | `200`, mesma linha, **nova geracao**          |
| mesma instance reapresenta chaves diferentes              | `200`, mesma linha, **nova geracao**          |
| instance nao ativa (`invalid`/`disabled`) e reapresentada | `200`, volta a `active`, **nova geracao**     |
| endpoint ja pertence a outra instance/usuario/workspace   | `409 push_endpoint_conflict`                  |

Nao existe teto de devices por usuario. Uma pessoa pode registrar quantos
browsers e aparelhos tiver.

O `endpoint` tem indice unico proprio, e ele **nunca** e alvo de `ON CONFLICT`.
Um endpoint de Web Push e uma URL de capacidade: quem tem a linha tem o direito
de enviar push para aquele browser. Um conflito de endpoint falha; nada reescreve
`user_id` ou `workspace_id`. A recuperacao do cliente e cancelar a subscription
no browser e registrar a nova que isso produz.

### Geracao

A subscription tem um `id` estavel por toda a vida, mas o endpoint e as chaves
por tras dele sao substituidos sempre que o browser se re-inscreve. A **geracao**
nomeia exatamente isso: um tempo de vida de endpoint/chaves. Ela e interna, um
token de concorrencia, e por isso **nao aparece na API HTTP** — o cliente nao tem
o que fazer com ela.

Ela existe porque uma tentativa de entrega e a sua resposta nao sao simultaneas.
A tentativa comeca contra o endpoint que a linha tinha; enquanto ela esta em voo
o browser pode se re-inscrever e trocar aquele endpoint; a resposta entao
descreve um endpoint que a linha nao tem mais.

Regras:

- retry identico de uma subscription `active` preserva a geracao;
- trocar `endpoint`, `p256dh` ou `auth` cria uma nova geracao;
- reativar uma subscription `invalid` ou `disabled` cria uma nova geracao,
  mesmo que os bytes reapresentados sejam iguais, porque resultados pendentes da
  geracao anterior nao podem atingir o novo ciclo de vida;
- `last_seen_at` e bookkeeping e nunca cria geracao.

Metadata de sucesso **nao atravessa geracoes**. Um endpoint novo comeca limpo:
`last_success_at` volta a nulo e `failure_count` a zero, porque ambos descrevem o
endpoint que acabou de ser substituido. Um retry identico, ao contrario, preserva
os dois: o historico continua descrevendo o que esta no arquivo.

## GET — reconcile

```json
{
  "data": {
    "subscriptions": [
      {
        "id": "6f0c...",
        "device_id": "5f8a1c2e-0000-4000-8000-000000000001",
        "status": "active",
        "created_at": "2026-09-08T12:00:00Z",
        "last_seen_at": "2026-09-08T12:31:00Z"
      }
    ]
  }
}
```

Sempre e somente as subscriptions do proprio chamador. Linhas `invalid` e
`disabled` aparecem: um cliente que nao enxerga que seu device foi invalidado
nunca saberia que precisa registrar de novo.

### O que deliberadamente nao e devolvido

- `auth` — nunca, em nenhuma rota;
- `p256dh` — nunca, em nenhuma rota;
- `endpoint` — nunca; o cliente ja o tem, e repeti-lo colocaria a capacidade de
  enviar push em todo log de proxy e relatorio de erro que capture uma resposta;
- `failure_count`, `last_success_at`, `invalidated_at`, `invalidation_reason` —
  historico operacional, sem uso para o cliente.

Esses campos tambem nao aparecem em log, span, metrica ou mensagem de erro. O
corpo cru da requisicao nunca e logado, e o rotulo Prometheus da rota de DELETE e
o template `/api/notifications/push/subscriptions/{subscriptionID}`, nunca o
identificador concreto.

## DELETE — desligar

`204 No Content`. Idempotente.

Nao apaga a linha: `status` vira `disabled` e a linha continua para diagnostico,
reconcile e uma limpeza controlada futura. Se o provider ja tinha invalidado a
subscription, o motivo e o instante originais sao preservados e apenas o status
passa a `disabled`.

`404` cobre igualmente "nao existe" e "nao e sua", para que identificadores nao
possam ser enumerados. Um `id` que nao seja UUID responde `400`.

## Autenticacao e autorizacao

O token de acesso HS256 e validado como nos demais servicos (assinatura, issuer,
audience, `sub`, `sid`, `jti`, `iat`, `nbf`, `exp`). Isso prova apenas que o
token foi emitido.

Uma unica consulta PostgreSQL entao resolve, server-side:

- a sessao existe, nao foi revogada, nao expirou (idle nem absoluta), e o usuario
  esta ativo e nao deletado;
- o chamador e membro ativo do workspace canonico (`slug = 'default'`,
  `status = 'active'`).

O `user_id` usado em toda escrita vem **da linha da sessao**, nunca do corpo, da
query ou do path. Nao existe campo de corpo que nomeie usuario ou workspace: o
decoder rejeita qualquer campo fora do contrato, o que torna mass assignment
impossivel em vez de apenas nao implementado.

| Condicao                                     | Status |
| -------------------------------------------- | ------ |
| sem `Authorization: Bearer`                  | `401`  |
| token invalido, expirado ou fora do contrato | `401`  |
| sessao revogada/expirada, usuario inativo    | `401`  |
| sessao viva sem membership ativa             | `403`  |
| subscription de outro usuario ou workspace   | `404`  |
| dependencia de autorizacao indisponivel      | `500`  |

Falha da dependencia de autorizacao nunca vira allow. Sem banco ou sem segredo
utilizavel, as rotas **nao sao montadas**: a requisicao cai no catch-all e recebe
`404`, e o motivo fica no log do processo, nao no status devolvido ao cliente.

## Lifecycle e resultado de entrega

Estados: `active` (entregavel), `invalid` (o provider disse que o endpoint
acabou), `disabled` (o dono desligou).

A transicao a partir de uma tentativa de entrega e uma API de dominio/storage,
nao uma rota HTTP — expor isso publicamente seria distribuir uma primitiva de
"cancele a subscription de alguem".

Ela recebe **`id` e geracao**. Quem faz a tentativa captura os dois quando o
envio comeca e devolve os dois junto com o resultado. Cada statement e um
compare-and-set em `(id, generation, status = 'active')`.

| Resposta do provider   | Efeito                                                         |
| ---------------------- | -------------------------------------------------------------- |
| `2xx`                  | `last_success_at = now()`, `failure_count = 0`, segue `active` |
| `404`                  | `invalid`, motivo `not_found`                                  |
| `410`                  | `invalid`, motivo `gone`                                       |
| `429`                  | `failure_count + 1`, **segue `active`**                        |
| `5xx`                  | `failure_count + 1`, **segue `active`**                        |
| timeout / sem resposta | `failure_count + 1`, **segue `active`**                        |

Somente `404` e `410` retiram uma subscription. Descartar uma por rate limit,
incidente do provider ou timeout e uma pessoa real que silenciosamente para de
ser notificada, e nada na resposta distingue "seu endpoint morreu" de "estamos
com um dia ruim" alem desses dois codigos.

Invalidar uma subscription nao afeta as outras do mesmo usuario.

Um resultado atrasado nao escreve nada. Se a geracao que ele carrega nao for mais
a da linha — o browser se re-inscreveu, o dono desligou, ou ela ja foi retirada —
a transicao e um no-op e a chamada devolve "nao aplicado". Nao e erro: em um
sistema onde envio e resposta nao sao simultaneos, resposta atrasada e esperada.
Concretamente, um `410` atrasado da geracao anterior **nao** invalida a geracao
atual, e um sucesso ou uma falha atrasada nao mexem em `last_success_at`,
`failure_count`, `invalidated_at`, `invalidation_reason` nem `status` dela.

Retry e backoff pertencem a camada de delivery/worker e nao existem aqui.

## Persistencia

`chat.push_subscriptions` (migration `chat/000045`). Fica no schema `chat` porque
e ali que o contrato de banco do notification-service ja vive
(`chat.notification_outbox`, `chat.conversation_notification_prefs`) e porque
`scripts/db/grant-runtime.sql` e o bootstrap de producao reconciliam permissao e
ownership apenas para `auth`, `chat` e `files`.

Invariantes garantidas pelo banco: `failure_count >= 0`; `generation >= 1`;
`status` num conjunto fechado; `invalidation_reason` num conjunto fechado, de
modo que a coluna nunca possa carregar texto vindo de um provider; e
`status <> 'active'` se e somente se `invalidated_at` e `invalidation_reason`
estiverem ambos preenchidos.
