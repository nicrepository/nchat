# File-service: anexos de canal e DM (RF-30, RF-32, RF-33)

Upload, consulta e download autenticados de arquivos e imagens em canais e DMs.
O conteudo e cifrado com envelope encryption antes de chegar ao SeaweedFS.

Fora de escopo nesta fase: preview/thumbnail (RF-31), worker completo do ClamAV,
politica de retencao (RF-34), URLs publicas, upload resumivel, deduplicacao,
criptografia E2E MLS de anexos e Range requests.

## Habilitacao

As rotas existem sempre. Enquanto `FILE_UPLOADS_ENABLED=false` (default) elas
respondem `503 service_unavailable` -- nunca `404`, para que uma configuracao
incompleta nao seja confundida com rota inexistente.

Com `FILE_UPLOADS_ENABLED=true` o servico so inicia se `DATABASE_URL`,
`AUTH_JWT_HMAC_SECRET`, `FILE_ENCRYPTION_MASTER_KEY` e `SEAWEEDFS_FILER_URL`
estiverem presentes e validos. Ver `services/file-service/.env.example`.

## Contrato

Prefixo publico `/api/files`, removido pelo gateway antes de chegar ao servico
(`strip-files-prefix` em `infra/traefik/local/dynamic.yml`). Todas as rotas
exigem `Authorization: Bearer <access-token>`.

| Metodo | Rota publica                                    | Descricao            |
| ------ | ----------------------------------------------- | -------------------- |
| POST   | `/api/files/channels/{channelID}/attachments`   | upload em canal      |
| POST   | `/api/files/dm/{conversationID}/attachments`    | upload em DM         |
| GET    | `/api/files/attachments/{attachmentID}`         | metadados            |
| GET    | `/api/files/attachments/{attachmentID}/content` | download do conteudo |

O destino vem da rota, nunca do corpo. Nao existe forma de request que nomeie
canal e DM ao mesmo tempo, e o workspace nunca e aceito do cliente: ele e
derivado do proprio recurso de destino durante a autorizacao.

### Upload

`multipart/form-data` com exatamente uma parte de arquivo no campo `file`:

```bash
curl -X POST https://nchat.local:8443/api/files/channels/$CHANNEL/attachments \
  -H "Authorization: Bearer $ACCESS_TOKEN" \
  -F "file=@relatorio.pdf"
```

Resposta `201`:

```json
{
  "data": {
    "id": "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
    "filename": "relatorio.pdf",
    "contentType": "application/pdf",
    "size": 184320,
    "status": "pending_scan",
    "destinationKind": "channel",
    "createdAt": "2026-07-28T12:00:00Z"
  }
}
```

`contentType` e o tipo **detectado** a partir do conteudo real. O tipo declarado
pelo cliente e guardado apenas para auditoria e nunca decide como o arquivo e
servido. `filename` e o nome normalizado (sem separadores de caminho, sem
caracteres de controle, limitado a 255 bytes) e existe apenas para exibicao.

A resposta nao expoe chave do SeaweedFS, FID, endpoint interno, KEK, DEK, nonce
nem qualquer detalhe de topologia.

### Erros de upload

| Status | Codigo                   | Quando                                                                                            |
| ------ | ------------------------ | ------------------------------------------------------------------------------------------------- |
| 400    | `bad_request`            | destino invalido, parte ausente, arquivo vazio, mais de um arquivo, campo extra, request truncada |
| 401    | `unauthorized`           | token ausente/invalido ou sessao inativa                                                          |
| 404    | `not_found`              | destino inexistente ou fora do alcance do usuario                                                 |
| 413    | `payload_too_large`      | conteudo acima de `FILE_MAX_UPLOAD_BYTES`                                                         |
| 415    | `unsupported_media_type` | corpo nao multipart ou multipart sem boundary                                                     |
| 429    | `rate_limited`           | limite de uploads por usuario                                                                     |
| 503    | `service_unavailable`    | uploads desabilitados ou dependencia indisponivel                                                 |

Destino inexistente e destino inacessivel retornam o mesmo `404`, sem indicar a
existencia do UUID.

### Download

```bash
curl -OJ https://nchat.local:8443/api/files/attachments/$ID/content \
  -H "Authorization: Bearer $ACCESS_TOKEN"
```

Headers da resposta:

- `Content-Type`: tipo detectado no upload;
- `Content-Disposition: attachment; filename="..."; filename*=UTF-8''...`;
- `X-Content-Type-Options: nosniff`;
- `Accept-Ranges: none`;
- `Content-Length`: tamanho autenticado do plaintext.

Nada e servido inline. HTML, SVG e JavaScript enviados como anexo sao baixados,
nunca renderizados na origem da API.

Range requests sao recusadas com `416 range_not_supported`. Servir um intervalo
exigiria pular para o meio de um stream autenticado por chunks, o que esta
versao do envelope nao suporta; responder com o corpo inteiro ou com uma fatia
nao verificada seria pior do que recusar.

| Status | Codigo                | Quando                                         |
| ------ | --------------------- | ---------------------------------------------- |
| 200    | -                     | anexo `clean` e visivel ao usuario             |
| 401    | `unauthorized`        | token invalido ou sessao inativa               |
| 404    | `not_found`           | anexo inexistente, removido ou fora do alcance |
| 409    | `file_not_scanned`    | anexo existe e e visivel, mas nao esta `clean` |
| 416    | `range_not_supported` | header `Range` presente                        |
| 503    | `service_unavailable` | storage indisponivel ou objeto ausente         |

Se a integridade falhar no meio do stream (objeto alterado, truncado,
reordenado ou substituido), a resposta e abortada com corpo menor que o
`Content-Length` declarado. O cliente ve uma transferencia quebrada, nunca um
arquivo parcial tratado como completo.

## Autorizacao

A autorizacao acontece inteiramente no servidor, em uma unica consulta SQL que
valida sessao e recurso ao mesmo tempo (mesmo padrao de
`services/media-service/internal/storage/authorizer.go`).

Canal: workspace ativo, membership ativa no workspace, canal ativo naquele
workspace e (canal publico ou membership no canal). E a mesma politica que o
chat-service aplica ao criar uma mensagem de canal -- anexar um arquivo e uma
escrita no canal.

DM: workspace ativo, membership ativa no workspace e participacao ativa na
conversa.

O download reavalia essa mesma politica a cada requisicao, entao remover alguem
de um canal ou de uma DM passa a valer no proximo download, sem cache.

## Estados

`pending_upload` -> `pending_scan` -> `clean` | `rejected`; `failed` e `deleted`
sao terminais. O cliente nunca define o estado. Somente `clean` pode ser baixado.

Com `FILE_MALWARE_SCAN_REQUIRED=true` (default, exigido por SECURITY.md) um
upload termina em `pending_scan` e o download responde `409` ate o worker
antimalware (fora do escopo desta issue) marcar `clean`.

O valor `false` e recusado na inicializacao quando `APP_ENV` nao e um valor de
desenvolvimento (`development`, `dev`, `local`, `test`, `nchat-dev`), que e
exatamente o que os overlays Kubernetes definem em
`infra/k8s/overlays/*/configmap-patch.yaml`. A verificacao falha fechada: um
`APP_ENV` desconhecido e tratado como ambiente implantado e o valor e rejeitado.

## Envelope encryption

O conteudo e cifrado no file-service antes de ir para o SeaweedFS:

- DEK de 32 bytes aleatoria por objeto (`crypto/rand`), nunca derivada de nome,
  id, timestamp ou senha;
- DEK protegida com AES-256-GCM sob a KEK de `FILE_ENCRYPTION_MASTER_KEY`, com
  AAD ligada ao id do anexo; so a forma protegida e persistida;
- conteudo em chunks de 64 KiB, cada um selado com AES-256-GCM sob a DEK;
- nonce = 8 bytes aleatorios por objeto + contador big-endian de 32 bits por
  chunk, entao nenhum par (chave, nonce) se repete;
- AAD de cada chunk liga versao do formato, id do anexo, indice do chunk e
  marcador de ultimo chunk.

Isso detecta alteracao, truncamento, reordenacao, duplicacao e substituicao de
chunks, e tambem substituicao do objeto inteiro por outro anexo. O formato e
versionado (`envelope_version` na tabela) para permitir evolucao sem recifrar o
que ja existe.

Detalhes e referencia da construcao em
`services/file-service/internal/crypto/envelope.go`.

## Persistencia

`files.attachments` (migration `migrations/files/000001_file_attachments`). O id
publico e um UUID aleatorio (nao enumeravel). A chave do objeto no SeaweedFS e
derivada apenas desse UUID e nunca sai do servidor. Exatamente um destino por
linha e garantido por CHECK, nao por convencao de aplicacao.

## Consistencia entre PostgreSQL e SeaweedFS

Nao existe transacao unica entre os dois. A ordem e: autorizar -> recusar
arquivo vazio -> preparar chaves e stream cifrado -> inserir linha
`pending_upload` -> gravar o objeto cifrado -> so entao avancar a linha.

A linha e inserida apenas quando a escrita esta prestes a comecar. Isso torna
"linha existe sem tentativa de escrita" irrepresentavel: toda falha e ou
anterior a linha (nada a desfazer) ou posterior a uma tentativa de escrita
(cleanup necessario).

Quando uma etapa posterior a escrita falha, a compensacao e condicional e as
duas operacoes sao dependentes, nunca independentes:

| Situacao                        | Objeto        | Estado da linha  | Metrica de orfao |
| ------------------------------- | ------------- | ---------------- | ---------------- |
| `Delete` OK, `MarkFailed` OK    | removido      | `failed`         | nao incrementada |
| `Delete` OK, `MarkFailed` falha | removido      | `pending_upload` | nao incrementada |
| `Delete` falha                  | **permanece** | `pending_upload` | incrementada     |

Quando `Delete` falha, `MarkFailed` **nao e chamado**. Avancar a linha para
`failed` tiraria do indice `idx_attachments_pending` o unico ponteiro
persistido para um objeto que ainda existe, e o objeto deixaria de ser
encontravel por consulta. Mantendo `pending_upload`, a linha continua coberta
pelo indice e recuperavel por query.

Em todos os casos de falha o erro retornado preserva a causa original e a falha
de cleanup (`errors.Join`), e nenhum caminho retorna sucesso.

Nao existe sweep automatico ainda. A recuperacao hoje e manual: as linhas
pendentes antigas sao listadas pelo indice parcial

```sql
SELECT id, storage_object_key, created_at
FROM files.attachments
WHERE status = 'pending_upload'
  AND created_at < now() - interval '1 hour'
ORDER BY created_at;
```

e a metrica `nchat_file_orphaned_objects_total` sinaliza quando ha objeto a
remover no SeaweedFS. O log estruturado correspondente
(`attachment object cleanup pending`) traz `attachment_id`, `failure_code`,
`object_stored`, `cleanup_attempted`, `cleanup_failed`, `state_advanced` e
`recoverable_state` -- nunca filename, chave de objeto ou texto do driver.

## Observabilidade

Metricas: `nchat_file_uploads_total{result}`,
`nchat_file_downloads_total{result}`, `nchat_file_orphaned_objects_total`.
Nenhum label carrega id, filename ou caminho.

As metricas HTTP transversais (`nchat_http_requests_total`,
`nchat_http_request_duration_seconds`) usam o label `route` com o template da
rota vindo do roteador (`/attachments/{attachmentID}`), nunca o path da request.
As rotas de anexo tem segmentos controlados pelo cliente e o middleware roda
antes da autenticacao, entao usar o path bruto permitiria a um chamador nao
autenticado criar series ilimitadas variando um UUID. Requests sem rota
correspondente caem no valor fechado `unmatched`. Os spans seguem a mesma regra:
nome `<METODO> <template>` e atributo `http.route` com o template.

Logs por requisicao: `request_id`, `attachment_id`, `destination_kind`,
`result`, `status`, `duration_ms` e bytes. Conteudo, filename, tokens, cookies,
chaves e a chave do objeto nunca sao logados.

## Teste de integracao opt-in

`services/file-service/internal/service/upload_integration_test.go` exercita o
pipeline contra PostgreSQL e SeaweedFS reais: o SQL de `PGXAttachmentStore`, o
cliente do filer, a compensacao e o estado persistido lido de volta do banco.
Ele e ignorado por padrao e so roda quando as duas variaveis estao definidas,
entao a suite normal nao depende de servico externo.

```bash
make dev-env-up
MIGRATIONS_DATABASE_URL='postgresql://nchat:<senha>@localhost:5432/nchat_test?sslmode=disable' \
  pnpm migrations:up
FILE_TEST_DATABASE_URL='postgresql://nchat:<senha>@localhost:5432/nchat_test?sslmode=disable' \
FILE_TEST_SEAWEEDFS_URL='http://localhost:8888' \
  go test ./services/file-service/internal/service/ -run Integration -v
```

O DSN precisa apontar para um banco `*_test` -- mesma guarda das suites de
integracao do chat-service. Cada execucao inventa o proprio workspace UUID e
remove no final apenas as linhas e os objetos que criou.
