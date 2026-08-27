# Media-service: token de participante LiveKit

O endpoint emite o token de mídia somente para participante autenticado de uma
chamada (direta RF-23 ou de recurso RF-24) que o `chat-service` já tenha
colocado em `active`. Ele não cria, atende, recusa, encerra ou admite ninguém
em chamada nenhuma — a admissão é sempre feita antes, via `call.start` ou
`call.join` no WebSocket (ver `docs/api/calls-websocket.md`).

## Contrato

`POST /media/livekit/token`

Requer `Authorization: Bearer <access-token>` e `Content-Type: application/json`.
O corpo tem limite de 4 KiB e rejeita campos desconhecidos.

```json
{
  "call_id": "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
  "participation_id": "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
}
```

Resposta:

```json
{
  "data": {
    "token": "<livekit-token>",
    "expiresAt": "2026-07-21T15:04:05Z",
    "serverUrl": "wss://livekit-dev.nic-labs.com"
  }
}
```

`identity`, `room`, `grants`, TTL e `serverUrl` nunca sao aceitos do cliente.
Com `APP_ENV=<environment>`, a identidade é
`<environment>:<canonical-sub-uuid>` e a sala é derivada exclusivamente do UUID
persistido como `<environment>:<kind>:<canonical-resource-uuid>`. Os kinds
permitidos continuam sendo `call`, `channel` e `dm`. O `serverUrl` vem de
`LIVEKIT_API_URL`.

## Autenticacao e autorizacao

O media-service valida a assinatura HS256, issuer, audience e claims obrigatorios
do access token. A mesma consulta PostgreSQL valida usuario ativo, vinculo entre
`sub` e `sid`, revogacao, expiracao idle e expiracao absoluta da sessao.

Na mesma consulta, o serviço exige workspace e membership ativos e chamada com
`status = 'active'`. A partir daí, a regra depende do tipo de alvo da chamada:

- **Direta** (`target_type = 'user'`): `sub` precisa ser `caller_id` ou
  `callee_id`. `participation_id` não é necessário. Sem mudança nesta issue.
- **Recurso** (`target_type` `channel` ou `dm`, issue #622/#609): além de
  membership/visibilidade do canal ou DM, o `sub` precisa segurar um
  **lease de participante vivo** — uma linha em
  `chat.call_participant_leases` para esta chamada e este usuário, com o
  **mesmo `participation_id`** e `expires_at > clock_timestamp()`. Um token
  stale nunca pode aproveitar a lease atual de outra aba. Ver membership sozinha já não é
  suficiente: um membro do canal que nunca deu `call.start`/`call.join`
  naquela chamada (ou cujo lease expirou) não recebe token, mesmo enxergando
  a chamada acontecer. **Observar** uma chamada de recurso — por exemplo via
  `call.resource.sync` ou por receber o broadcast `call.accepted` — nunca
  cria lease e nunca equivale a participar dela.

Durante o rollout, a ausência de `participation_id` só pode autorizar uma lease
legacy cujo valor no banco também seja `NULL`; nunca significa "qualquer lease
do mesmo usuário". Novas admissões sempre recebem e persistem UUID não nulo.
O token é requester-only e não faz parte da representação pública de `Call`.

Uma chamada `ringing`, expirada, recusada, cancelada ou encerrada nunca
autoriza token, em nenhum dos dois casos.

Chamada ausente, chamada inacessivel e chamada de recurso sem lease vivo
retornam o mesmo `404`, sem indicar a existencia do UUID nem se a causa foi
falta de acesso ao canal/DM ou apenas ausência de lease. Falha ou inatividade
de sessao retorna `401`.

## Token

O SDK oficial do LiveKit para Go assina o token. O grant permite somente entrar na
sala derivada, publicar camera/microfone e assinar midia. Publicacao de data,
administracao de sala, gravacao, ingress/egress e alteracao de metadata nao sao
concedidos.

O signer valida novamente room e identity contra o `APP_ENV` configurado antes
de emitir o JWT. O formato antigo sem namespace não é aceito. Um futuro
consumidor de webhooks LiveKit deve aplicar a mesma validação de namespace antes
de rotear eventos; esta release não consome webhooks.

O TTL configurado usa default de 300 segundos, minimo de 60 e maximo de 600. A
validade emitida tambem e limitada pela validade restante do access token e da
sessao PostgreSQL. O endpoint possui limite local de 30 emissoes por usuario por
minuto; deployments com varias replicas devem manter rate limiting adicional no
gateway.

## Codigos HTTP

| Status | Condicao                                               |
| -----: | ------------------------------------------------------ |
|    200 | token emitido                                          |
|    400 | JSON, `call_id` ou `participation_id` invalido         |
|    401 | Bearer invalido ou sessao inativa                      |
|    404 | chamada não ativa, ausente ou usuário não participante |
|    413 | corpo acima de 4 KiB                                   |
|    415 | Content-Type diferente de JSON                         |
|    429 | limite por usuario excedido                            |
|    503 | integracao desabilitada ou dependencias nao compostas  |
|    500 | falha interna ou de banco, sem detalhes sensiveis      |

## Configuracao

`LIVEKIT_ENABLED` e `false` por default. Se a variavel estiver presente, seu valor
deve ser um booleano valido; valor invalido interrompe o startup. Quando habilitado,
sao obrigatorios:

| Variavel                     | Uso                                    |
| ---------------------------- | -------------------------------------- |
| `APP_ENV`                    | namespace de room e identity           |
| `DATABASE_URL`               | sessoes e autorizacao no PostgreSQL    |
| `DB_CONNECT_TIMEOUT_SECONDS` | timeout de conexao, default 5          |
| `AUTH_JWT_HMAC_SECRET`       | validacao do access token, sem default |
| `AUTH_JWT_ISSUER`            | default `nchat-auth`                   |
| `AUTH_JWT_AUDIENCE`          | default `nchat-api`                    |
| `LIVEKIT_API_URL`            | URL do SDK web e readiness do LiveKit  |
| `LIVEKIT_API_KEY`            | assinatura server-side, sem default    |
| `LIVEKIT_API_SECRET`         | assinatura server-side, sem default    |
| `LIVEKIT_TOKEN_TTL_SECONDS`  | default 300, faixa 60-600              |

`APP_ENV` é trimado e, quando LiveKit está habilitado, deve casar com
`^[a-z][a-z0-9-]{0,31}$`. Valores vazios, com `:`, uppercase ou longos demais
interrompem o startup; não há normalização silenciosa. Dev usa `development` e
Blue/Green de produção herdam o mesmo valor `production`.

Chaves e secrets devem ser fornecidos por Secret/SealedSecret. Eles nao aparecem
em health, readiness, version, logs, traces ou metricas. O token emitido tambem nao
e registrado.

`LIVEKIT_API_URL` aceita HTTP/HTTPS para desenvolvimento local e WSS para a
conexao segura do SDK web. No caso WSS, o readiness consulta a origem HTTPS
equivalente; a resposta do token preserva e entrega a URL WSS sem alteracao.

Com `LIVEKIT_ENABLED=false`, `/readyz` nao abre nem verifica PostgreSQL ou LiveKit.
Com `LIVEKIT_ENABLED=true`, os checks criticos `postgres` e `livekit-api` devem
passar; falha ou timeout produz `unready` com HTTP `503`, sem expor DSN, host ou
erro interno. `/healthz` continua sem dependencias externas.

## Compatibilidade de rollout

Esta mudança renomeia as rooms de dev. Chamadas LiveKit em andamento durante o
rollout do media-service de dev podem cair ou reconectar em uma nova room.
Produção ainda não existe, portanto não há migração de room em produção. Não há
compatibilidade dual entre `call:<uuid>` e `production:call:<uuid>`, pois aceitar
os dois formatos reabriria a fronteira de isolamento.

## Teste local

```bash
cd services/media-service
go test ./...
go vet ./...
go run ./cmd/media-service
```

Os predicados reais de sessao e autorizacao podem ser validados em um PostgreSQL
descartavel. O banco informado deve ter nome terminado em `_test`; a suite aplica
as migrations reais, incluindo `chat/000019_call_lifecycle` (chamada 1:1),
`chat/000022_workspace_moderator_and_guest_channel_scope` (a função
`chat.channel_visible_to_user` que a autorização de canal usa) e
`chat/000028_resource_call_lifecycle` (`target_type`/`target_id` e
`chat.call_participant_leases`, o lease que a autorização de chamada de
recurso agora exige) e `chat/000035_call_participant_lease_identity` (fencing
por admissão):

```bash
MEDIA_TEST_DATABASE_URL='postgres://user:password@127.0.0.1:5432/nchat_media_test?sslmode=disable' go test -tags=integration -count=1 ./internal/storage -run TestPGXResourceAuthorizerPostgreSQLPredicates
```
