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

| Metodo | Rota publica                                    | Descricao                   |
| ------ | ----------------------------------------------- | --------------------------- |
| POST   | `/api/files/channels/{channelID}/attachments`   | upload em canal             |
| POST   | `/api/files/dm/{conversationID}/attachments`    | upload em DM                |
| GET    | `/api/files/channels/{channelID}/attachments`   | anexos recentes do canal    |
| GET    | `/api/files/dm/{conversationID}/attachments`    | anexos recentes da conversa |
| GET    | `/api/files/attachments/{attachmentID}`         | metadados                   |
| GET    | `/api/files/attachments/{attachmentID}/content` | download do conteudo        |

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
| 413    | `payload_too_large`      | conteudo acima do limite efetivo do workspace (ver "Limite de tamanho")                           |
| 415    | `unsupported_media_type` | corpo nao multipart ou multipart sem boundary                                                     |
| 429    | `rate_limited`           | limite de uploads por usuario                                                                     |
| 503    | `service_unavailable`    | uploads desabilitados ou dependencia indisponivel                                                 |

Destino inexistente e destino inacessivel retornam o mesmo `404`, sem indicar a
existencia do UUID.

## Limite de tamanho (RF-32, issue #458)

### Unidade e default

A unidade e **binaria**, como todo tamanho neste repositorio (`50 << 20` do
file-service antes desta issue, `5 << 20` do avatar). O default de "250 MB" do
RF-32 e portanto **250 MiB = 262144000 bytes**, que tambem e a leitura mais
permissiva: um arquivo que o usuario chama de 250 MB (250.000.000 bytes) cabe.

A unidade e escrita **MiB** em toda a interface e em toda a documentacao. "MB"
nao e usado para este valor: rotular 262144000 bytes como "250 MB" e exatamente
a ambiguidade que esta issue elimina.

Internamente o valor e sempre `int64` em **bytes**. A politica so aceita
**multiplos exatos de 1 MiB** (1048576 bytes) -- ver "Valores aceitos" abaixo --
entao a conversao entre bytes e MiB e exata nos dois sentidos e nunca arredonda.

### Valores aceitos

| Regra                    | Valor                                   |
| ------------------------ | --------------------------------------- |
| Minimo                   | 1048576 bytes (1 MiB)                   |
| Maximo                   | 536870912 bytes (512 MiB)               |
| Default                  | 262144000 bytes (250 MiB)               |
| Granularidade            | multiplo exato de 1048576 bytes         |
| Valor fora dessas regras | **rejeitado** com `400`, nunca ajustado |

Nao existe arredondamento em lugar nenhum: nem no handler, nem no banco, nem na
interface administrativa. Um valor como 1572864 (1,5 MiB) e recusado, e nao
convertido para 1 MiB nem para 2 MiB. A regra vive uma unica vez em
`uploadpolicy.Valid` (`libs/go/platform/uploadpolicy`), que o handler
administrativo e a configuracao do file-service reutilizam, e o `CHECK` da
migration `000020` repete as duas metades como backstop.

### Fonte de verdade

O limite e **administrativo e por workspace**:
`chat.workspaces.max_upload_bytes` (migration `migrations/chat/000020`),
`NOT NULL DEFAULT 262144000`, `CHECK (BETWEEN 1048576 AND 536870912)`.

As constantes canonicas vivem em `libs/go/platform/uploadpolicy` e sao
importadas pelo chat-service (que decide o valor) e pelo file-service (que o
aplica), entao nao existem duas copias dos mesmos numeros.

| Camada       | Papel                                                          |
| ------------ | -------------------------------------------------------------- |
| chat-service | dono do valor: endpoint administrativo + coluna + `CHECK`      |
| file-service | **autoridade**: recusa o upload, contando os bytes que le      |
| gateway      | defesa em profundidade e protecao de recursos                  |
| frontend     | UX: evita gastar banda em um upload que ja se sabe que falhara |

### Administracao

`GET | PATCH /api/chat/workspaces/{workspaceID}/upload-limit` -- ver
[chat-upload-limit.md](./chat-upload-limit.md). Exige `owner` ou `admin` ativo
do workspace, verificado no handler e novamente de forma atomica no `UPDATE`.

### Limite efetivo de um upload

O file-service le `max_upload_bytes` **na mesma consulta que autoriza o
destino** (`storage/authorizer.go`), entao a politica e carregada uma vez por
request, antes do primeiro byte, sem consulta extra e sem cache.

O limite efetivo e:

```text
min(politica do workspace, FILE_MAX_UPLOAD_BYTES)
```

`FILE_MAX_UPLOAD_BYTES` deixou de ser o limite do RF-32 e passou a ser um
**teto de deployment**: um controle do operador para um cluster que nao absorve
a politica configurada. O default dele e o teto do dominio (536870912), entao
por padrao ele nunca limita e o valor administrativo e o que vale. Nenhum dos
dois reescreve o outro em disco -- o estreitamento acontece por request e esta
documentado aqui.

Uma politica ausente (linha anterior a migration `000020`, lida como `0`) ou
fora do intervalo resolve para o **default de 250 MiB**, nunca para "sem
limite".

### Como o limite e aplicado

1. O handler limita o corpo bruto com `http.MaxBytesReader` em
   `FILE_MAX_UPLOAD_BYTES + 8 KiB`. Esse teto **nao** e o limite do RF-32: o
   destino ainda nao foi lido do corpo. Ele so garante que nenhum corpo cresca
   sem limite. Os 8 KiB sao o overhead de framing multipart (boundaries e
   headers de parte), reservados para que um arquivo exatamente no limite nao
   seja recusado por bytes que o usuario nao enviou.
2. Autorizacao resolve destino, workspace e politica em uma consulta.
3. `boundedSource` conta os **bytes reais do arquivo** e falha no primeiro byte
   acima do limite. `Content-Length` nunca decide nada: um valor ausente, um
   valor mentiroso e um corpo `chunked` batem todos na mesma parede.

Arquivo exatamente no limite e aceito; um byte acima e recusado.

### O que acontece com um upload recusado

O tamanho nao e conhecido antecipadamente -- `Content-Length` nao e confiavel e
um corpo `chunked` nao tem nenhum --, entao o excesso e descoberto enquanto o
stream cifrado ja esta sendo escrito. O que e garantido e o **estado final**, em
todos os caminhos:

- o objeto parcial e removido do SeaweedFS (`compensatePersistedObject`);
- a linha termina em `failed` (ou permanece `pending_upload` e recuperavel, se o
  `Delete` falhar -- ver "Consistencia" acima);
- a linha **nunca** avanca para `pending_scan`, que e o unico estado que o
  worker antimalware observa, entao um arquivo recusado nunca entra na fila do
  ClamAV;
- a linha nunca fica baixavel.

Quando o excesso e visivel dentro da janela de deteccao (512 bytes), a recusa
acontece antes de qualquer chamada a storage.

### Gateway

O gateway aplica **um teto tecnico estatico** ao corpo HTTP e nada alem disso.
Ele nao conhece — e nao aplica — a politica dinamica por workspace.

Sao dois controles distintos, de proposito:

| Controle           | Onde         | Escopo                 | Valor                               |
| ------------------ | ------------ | ---------------------- | ----------------------------------- |
| Teto tecnico       | upload guard | corpo HTTP inteiro     | `536879104` bytes, estatico         |
| Politica funcional | file-service | bytes reais do arquivo | 1 a 512 MiB inteiros, por workspace |

`536879104` = 512 MiB (o maior valor que um admin pode gravar) + 8 KiB de
overhead multipart. Os 8 KiB sao folga para boundaries e headers de parte, nunca
tamanho extra de arquivo. Estando no teto, o gateway e por construcao maior ou
igual a qualquer politica, entao **nunca** recusa um upload que o file-service
aceitaria e nenhuma recarga precisa ser coordenada com uma alteracao
administrativa. Tentar sincronizar um valor dinamico por workspace dentro de uma
configuracao estatica de proxy garantiria divergencia.

O numero canonico e `uploadpolicy.GatewayHardCapBytes`
(`libs/go/platform/uploadpolicy`); `scripts/ci/gateway-config-check.sh` compara
as constantes Go com a configuracao do guard.

#### Por que nao o Buffering do Traefik

O Traefik v3.6 tem exatamente um mecanismo nativo de teto de corpo, o middleware
`buffering`, e ele funciona **lendo o corpo inteiro antes de encaminhar** — em
memoria ate `memRequestBodyBytes` e em disco acima disso. Nas rotas de anexo isso
aconteceria _antes_ de o file-service autenticar, autorizar ou aplicar qualquer
controle de concorrencia, entao um cliente **sem credencial nenhuma** poderia
encher o armazenamento temporario do gateway a vontade. Ele esta banido, e o gate
de CI falha se `buffering:`, `maxRequestBodyBytes` ou `memRequestBodyBytes`
reaparecerem em `infra/traefik/local/` ou `infra/k8s/`.

#### upload guard

O teto e aplicado por um nginx interno que limita o corpo **enquanto o
transmite** — a propriedade que o Traefik nao oferece:

```text
cliente -> Traefik -> upload-guard (nginx) -> file-service
```

Apenas `POST /api/files/(channels|dm)/{id}/attachments` passa por ele. Health,
readiness, download, listagem, metadados e rotas administrativas continuam indo
direto ao file-service. A rota do guard e fixada por metodo e por formato exato
de path, entao nao existe caminho de upload que alcance o file-service por fora
do teto.

Propriedades obrigatorias, todas verificadas pelo gate de CI:
`client_max_body_size 536879104`, `proxy_request_buffering off`,
`proxy_http_version 1.1` (para que um corpo `chunked` siga chunked),
`proxy_next_upstream off` (um POST reenviado exigiria replay do corpo e poderia
criar o anexo duas vezes), sem cache, sem log de corpo, de `Authorization` ou de
`Cookie`, `server_tokens off`. O container roda sem root, com filesystem raiz
somente leitura e imagem pinada. Ver
`infra/k8s/base/services/upload-guard/README.md`.

O 413 do guard usa o mesmo envelope `{"error":{"code":"payload_too_large",…}}`
dos servicos e nao expoe versao, host, path interno nem configuracao.

#### inFlightReq

O router de upload carrega um `inFlightReq` conservador — um teto **total por
router**, nao por IP: as origens aqui ficam atras de NAT corporativo e outros
proxies, entao um limite por endereco puniria um escritorio inteiro ou seria
espalhado por um prefixo IPv6. Ele fica acima do numero de slots do file-service
para nunca ser o limite que morde, e e defesa de infraestrutura apenas: nao
substitui autenticacao, autorizacao, o limite por usuario, o admission control
distribuido nem o limite real do stream.

### Uploads simultaneos (admission control)

O rate limiter por minuto conta **inicios** de request e vive em um processo, o
que deixa duas lacunas: N replicas concedem N orcamentos, e um cliente que
mantem varias transferencias lentas abertas nunca e contado. O que precisa ser
limitado e quanto esta **em voo**, no cluster inteiro.

O file-service reserva, antes de ler qualquer byte:

- uma vaga **global** (`FILE_UPLOAD_MAX_CONCURRENT`, default 4);
- uma vaga **por usuario** (`FILE_UPLOAD_MAX_CONCURRENT_PER_USER`, default 2).

As vagas sao _session advisory locks_ do PostgreSQL, o mesmo mecanismo ja usado
em `chat-service` e `auth-service`. Cada vaga ocupada e uma conexao reservada
pela duracao da transferencia — **sem transacao aberta**, que prenderia o xmin e
travaria o vacuum. Uma replica que morre derruba suas conexoes e o PostgreSQL
libera os locks junto: nao ha lease para renovar, tabela para varrer nem job de
limpeza.

Ordem exata no handler, e ela importa:

1. autenticar token e sessao;
2. resolver o destino a partir do **path**;
3. autorizar o chamador nesse destino e ler a politica do workspace — uma
   consulta, nenhum byte de corpo;
4. reservar vaga global e vaga do usuario;
5. so entao aplicar `MaxBytesReader` e comecar a ler.

Nada antes do passo 5 toca `r.Body`. Um chamador sem acesso, ou que chega com o
cluster cheio, e respondido enquanto a request ainda e so cabecalho — e um
chamador nao autorizado nunca consome uma vaga.

| Situacao                  | Status | Codigo                | Retry-After |
| ------------------------- | ------ | --------------------- | ----------- |
| Usuario no proprio limite | `429`  | `rate_limited`        | sim         |
| Cluster sem vagas         | `503`  | `service_unavailable` | sim         |
| Admission indisponivel    | `503`  | `service_unavailable` | sim         |

A ultima linha e deliberada: **falha fechada**. Nao conseguir decidir nunca vira
"admitir mesmo assim". Nenhuma resposta informa quantas vagas existem, quantas
estao em uso, quem as detem ou quantas replicas ha.

A vaga e devolvida em `defer` imediatamente apos a aquisicao, entao sucesso,
erro de leitura, falha de storage, panico ou cliente que desliga liberam igual; a
liberacao e idempotente.

#### Orcamento de recursos

```text
bytes simultaneos aceitos <= FILE_UPLOAD_MAX_CONCURRENT x maximo administrativo
```

Com o default de 4 vagas e teto de 512 MiB, o pior caso teorico e **2 GiB** de
streams aceitos ao mesmo tempo no cluster. Nada disso e alocado em memoria — o
conteudo e transmitido, cifrado e encaminhado em chunks —, mas o numero deve ser
revisto junto com os recursos do file-service, do SeaweedFS e da criptografia
sempre que os defaults mudarem.

#### Pool PostgreSQL

Cada vaga global ocupada e uma conexao reservada, entao
`FILE_DB_MAX_CONNECTIONS` precisa ficar pelo menos 4 acima de
`FILE_UPLOAD_MAX_CONCURRENT` — o servico recusa iniciar caso contrario. Sem essa
folga, um servico lotado deixaria de conseguir autorizar destinos e gravar
metadados, travando contra o proprio pool em vez de responder 503.

### Frontend

`GET /api/chat/sidebar` publica `workspace.max_upload_bytes` para qualquer
membro, porque o cliente precisa dele para avisar o usuario antes de gastar
banda. E numero de politica, nao capacidade: o file-service rele o valor da
propria linha do destino em todo upload.

O composer busca a politica **a cada tentativa** de upload: nao ha cache, nem por
montagem do componente, nem por TTL, nem global. Um admin pode alterar o limite a
qualquer momento, e um composer aberto desde antes da mudanca validaria contra um
limite que nao existe mais. Uma leitura por tentativa e barata perto dos bytes
que a tentativa esta prestes a mover.

O composer (`apps/web/src/chat/ChatComposer.tsx`) e o ponto de integracao: ele ja
serve canais e DMs, entao o botao de anexar, o `input type="file"` e o
drag-and-drop sobre a caixa do composer chamam todos o mesmo
`useAttachmentUpload().selectFile`, que faz uma unica vez a leitura do limite, a
validacao, o POST e a normalizacao do erro. Um upload concluido nao cria
mensagem: ele adiciona um arquivo ao destino, e o painel de detalhes e
recarregado para refleti-lo.

`uploadAttachment` (`apps/web/src/chat/filesApi.ts`) compara `File.size` com o
limite antes de enviar -- comparacao `>` entre inteiros, entao um arquivo
exatamente no limite passa -- e normaliza a rejeicao do backend em um erro
tipado. A normalizacao usa o **status HTTP** primeiro e o codigo so como
refinamento, porque um `413` pode chegar sem o envelope
`{error:{code,message}}` do servico. Nenhum texto do servidor e exibido.

Quando o servidor nao publica limite algum, o cliente trata como
**desconhecido** e nao substitui por 250 MiB: a checagem local e pulada e o
file-service decide sozinho. Inventar um default aqui mostraria o numero errado
para um workspace com politica diferente.

Mensagens: `O arquivo excede o limite permitido de 250 MiB.` e, sem limite
conhecido, `O arquivo excede o limite permitido.`

### Listagem de anexos de um destino (issues #435 e #441)

| Metodo | Rota publica                                  | Identificador              |
| ------ | --------------------------------------------- | -------------------------- |
| GET    | `/api/files/channels/{channelID}/attachments` | `chat.channels.id`         |
| GET    | `/api/files/dm/{conversationID}/attachments`  | `chat.dm_conversations.id` |

As duas rotas sao o mesmo caso de uso com destinos diferentes. O tipo do destino
vem da **rota**, nunca do corpo nem de um parametro, entao um `channelID` nunca
seleciona anexos de conversa e um `conversationID` nunca seleciona os de canal:
sao duas consultas estaticas distintas, cada uma comparando a sua propria coluna
(`channel_id` ou `conversation_id`) e fixando o seu proprio `destination_kind`.

**Autenticacao:** `Authorization: Bearer <access-token>` e sessao ativa.

**Autorizacao:** identica a do upload no mesmo destino -- `AuthorizeDestination`
resolve o destino e o workspace canonico dele em uma consulta:

- canal: workspace ativo, membership ativa no workspace, canal ativo, e canal
  publico **ou** com `chat.channel_members` do chamador;
- conversa: workspace ativo, membership ativa no workspace, conversa ativa, e
  `chat.dm_members` ativo do chamador.

O workspace nunca vem do cliente: e lido da linha do destino e e o que amarra a
listagem. Destino inexistente, arquivado, fora do alcance do chamador ou de
outro workspace respondem o mesmo `404`, sem revelar a existencia do UUID.

#### `conversationID` aceita `direct` e `group`

A rota de conversa e **generica**: `conversationID` pode ser uma DM 1:1
(`type = 'direct'`) ou um grupo ad-hoc (`type = 'group'`). Nao ha checagem de
`type` aqui, e isso e deliberado -- upload
(`POST /api/files/dm/{conversationID}/attachments`) e download ja tratam
`destination_kind = 'dm'` genericamente, entao restringir apenas a listagem
criaria a divergencia de permitir anexar um arquivo a uma DM 1:1 e depois
recusar lista-lo.

A issue #441 apenas **consome** esta rota para grupos, porque o painel de
detalhes de DM 1:1 esta fora do escopo dela. "Painel fora do escopo" nao
significa "API de anexos proibida": o controle de acesso continua sendo
participacao ativa na conversa, igual ao upload.

> Contraste deliberado com `GET /api/chat/dm/{conversationID}/details`
> ([chat-group-details.md](./chat-group-details.md)), que **exige**
> `type = 'group'`. Aquela projecao foi criada para o painel de grupo e nao tem
> irmao generico preexistente; esta rota tem, e segue o comportamento dele.

#### Requisicao

```bash
curl "https://nchat.local:8443/api/files/channels/$CHANNEL/attachments?limit=5" \
  -H "Authorization: Bearer $ACCESS_TOKEN"

curl "https://nchat.local:8443/api/files/dm/$CONVERSATION/attachments?limit=5" \
  -H "Authorization: Bearer $ACCESS_TOKEN"
```

Resposta `200` (mesma forma nas duas rotas; `destinationKind` reflete a rota):

```json
{
  "data": {
    "attachments": [
      {
        "id": "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
        "filename": "relatorio.pdf",
        "contentType": "application/pdf",
        "size": 184320,
        "status": "clean",
        "destinationKind": "channel",
        "createdAt": "2026-07-28T12:00:00Z"
      }
    ]
  }
}
```

Campos: `id`, `filename` (nome normalizado, so para exibicao), `contentType`
(tipo **detectado** no upload), `size` (plaintext, bytes), `status` (estado do
scan) e `createdAt` (RFC3339 UTC). `destinationKind` e `channel` ou `dm`,
conforme a rota.

#### Ordenacao, limite e estados

- ordem fixa no servidor: `created_at DESC, id DESC` (desempate deterministico);
  o cliente nao escolhe a ordenacao;
- `limit` e opcional, default 20 e teto 50, ambos aplicados no servidor; valor
  nao inteiro ou <= 0 responde `400 bad_request` em vez de virar o default
  silenciosamente;
- so aparecem anexos com status `pending_scan`, `clean` ou `rejected` --
  `pending_upload`, `failed` e `deleted` sao uploads incompletos ou removidos,
  nunca arquivos do destino;
- destino sem anexos responde `200` com `attachments: []`.

#### A listagem nao autoriza download

A resposta e **apenas metadado**. Nao ha URL de conteudo, nem link assinado, nem
token: chave do objeto no SeaweedFS, DEK, versao do envelope e workspace nunca
sao serializados.

Aparecer na listagem **nao** concede permissao de download. O conteudo continua
em `GET /api/files/attachments/{attachmentID}/content`, que reautoriza a cada
chamada e so serve anexo com `status = clean`; `pending_scan` e `rejected`
aparecem na lista com o seu estado e respondem `409 file_not_scanned` no
download.

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
