# Media-service: token de participante LiveKit

O endpoint emite o token de mídia somente para participante autenticado de uma
chamada RF-23 que o `chat-service` já tenha colocado em `active`. Ele não cria,
atende, recusa ou encerra chamadas.

## Contrato

`POST /media/livekit/token`

Requer `Authorization: Bearer <access-token>` e `Content-Type: application/json`.
O corpo tem limite de 4 KiB e rejeita campos desconhecidos.

```json
{
  "call_id": "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
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

`identity`, `room`, `grants`, TTL e `serverUrl` nunca sao aceitos do cliente. A
identidade e o UUID `sub` do access token. A sala é derivada exclusivamente do
UUID persistido: `call:<uuid>`. O `serverUrl` vem de `LIVEKIT_API_URL`.

## Autenticacao e autorizacao

O media-service valida a assinatura HS256, issuer, audience e claims obrigatorios
do access token. A mesma consulta PostgreSQL valida usuario ativo, vinculo entre
`sub` e `sid`, revogacao, expiracao idle e expiracao absoluta da sessao.

Na mesma consulta, o serviço exige workspace e membership ativos, chamada com
`status = 'active'` e `sub` igual a `caller_id` ou `callee_id`. Uma chamada
`ringing`, expirada, recusada, cancelada ou encerrada nunca autoriza token.

Chamada ausente e chamada inacessivel retornam o mesmo `404`, sem indicar a
existencia do UUID. Falha ou inatividade de sessao retorna `401`.

## Token

O SDK oficial do LiveKit para Go assina o token. O grant permite somente entrar na
sala derivada, publicar camera/microfone e assinar midia. Publicacao de data,
administracao de sala, gravacao, ingress/egress e alteracao de metadata nao sao
concedidos.

O TTL configurado usa default de 300 segundos, minimo de 60 e maximo de 600. A
validade emitida tambem e limitada pela validade restante do access token e da
sessao PostgreSQL. O endpoint possui limite local de 30 emissoes por usuario por
minuto; deployments com varias replicas devem manter rate limiting adicional no
gateway.

## Codigos HTTP

| Status | Condicao                                               |
| -----: | ------------------------------------------------------ |
|    200 | token emitido                                          |
|    400 | JSON ou `call_id` invalido; campo desconhecido         |
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
| `DATABASE_URL`               | sessoes e autorizacao no PostgreSQL    |
| `DB_CONNECT_TIMEOUT_SECONDS` | timeout de conexao, default 5          |
| `AUTH_JWT_HMAC_SECRET`       | validacao do access token, sem default |
| `AUTH_JWT_ISSUER`            | default `nchat-auth`                   |
| `AUTH_JWT_AUDIENCE`          | default `nchat-api`                    |
| `LIVEKIT_API_URL`            | URL do SDK web e readiness do LiveKit  |
| `LIVEKIT_API_KEY`            | assinatura server-side, sem default    |
| `LIVEKIT_API_SECRET`         | assinatura server-side, sem default    |
| `LIVEKIT_TOKEN_TTL_SECONDS`  | default 300, faixa 60-600              |

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

## Teste local

```bash
cd services/media-service
go test ./...
go vet ./...
go run ./cmd/media-service
```

Os predicados reais de sessao e autorizacao podem ser validados em um PostgreSQL
descartavel. O banco informado deve ter nome terminado em `_test`; a suite aplica
as migrations reais, incluindo `chat/000019_call_lifecycle`:

```bash
MEDIA_TEST_DATABASE_URL='postgres://user:password@127.0.0.1:5432/nchat_media_test?sslmode=disable' go test -tags=integration -count=1 ./internal/storage -run TestPGXResourceAuthorizerPostgreSQLPredicates
```
