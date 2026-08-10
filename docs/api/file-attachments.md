# File-service: anexos de canal e DM (RF-30, RF-32, RF-33)

Upload, consulta e download autenticados de arquivos e imagens em canais e DMs.
O conteudo e cifrado com envelope encryption antes de chegar ao SeaweedFS.

Fora de escopo nesta fase: politica de retencao (RF-34), URLs publicas, upload
resumivel, deduplicacao e criptografia E2E MLS de anexos. O worker assincrono de
antimalware (RF-22) esta implementado -- ver "Scan assincrono de malware". O RF-31 implementado aqui cobre **thumbnail de imagem, primeira pagina
de PDF e preview inline de video com HTTP Range**.

## Habilitacao

As rotas existem sempre. Enquanto `FILE_UPLOADS_ENABLED=false` (default) elas
respondem `503 service_unavailable` -- nunca `404`, para que uma configuracao
incompleta nao seja confundida com rota inexistente.

Com `FILE_UPLOADS_ENABLED=true` o servico so inicia se `DATABASE_URL`,
`AUTH_JWT_HMAC_SECRET`, `FILE_ENCRYPTION_MASTER_KEY`,
`FILE_ENCRYPTION_MASTER_KEY_ID` e `SEAWEEDFS_FILER_URL` estiverem presentes e
validos. Ver `services/file-service/.env.example`.

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
| GET    | `/api/files/attachments/{attachmentID}/preview` | preview inline (RF-31)      |

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
    "previewStatus": "pending",
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
        "previewStatus": "ready",
        "destinationKind": "channel",
        "createdAt": "2026-07-28T12:00:00Z"
      }
    ]
  }
}
```

Campos: `id`, `filename` (nome normalizado, so para exibicao), `contentType`
(tipo **detectado** no upload), `size` (plaintext, bytes), `status` (estado do
scan), `previewStatus` (estado do preview, ver "Preview inline") e `createdAt`
(RFC3339 UTC). `destinationKind` e `channel` ou `dm`, conforme a rota.

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
aparecem na lista com o seu estado e respondem `403 file_not_scanned` no
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
- `Accept-Ranges: bytes`;
- `Cache-Control: private, no-store` -- inclusive nas respostas `206`, porque a
  visibilidade e reavaliada a cada request e nenhum cache compartilhado pode
  tomar essa decisao;
- `Content-Length`: tamanho autenticado do plaintext.

Nada e servido inline. HTML, SVG e JavaScript enviados como anexo sao baixados,
nunca renderizados na origem da API.

| Status | Codigo                | Quando                                         |
| ------ | --------------------- | ---------------------------------------------- |
| 200    | -                     | anexo `clean` e visivel ao usuario             |
| 206    | -                     | `Range` valido e satisfazivel                  |
| 401    | `unauthorized`        | token invalido ou sessao inativa               |
| 403    | `file_not_scanned`    | anexo existe e e visivel, mas nao esta `clean` |
| 404    | `not_found`           | anexo inexistente, removido ou fora do alcance |
| 416    | `range_not_supported` | mais de um intervalo por request               |
| 416    | -                     | intervalo unico invalido ou fora do arquivo    |
| 503    | `service_unavailable` | storage indisponivel ou objeto ausente         |

Se a integridade falhar no meio do stream (objeto alterado, truncado,
reordenado ou substituido), a resposta e abortada com corpo menor que o
`Content-Length` declarado. O cliente ve uma transferencia quebrada, nunca um
arquivo parcial tratado como completo.

### HTTP Range (RF-31)

Esta rota aceita `Range: bytes=...`, que e o que permite reproduzir e navegar um
video sem baixar o arquivo inteiro.

```bash
curl -r 1048576-1049599 https://nchat.local:8443/api/files/attachments/$ID/content \
  -H "Authorization: Bearer $ACCESS_TOKEN"
```

Formas aceitas: `bytes=inicio-fim`, `bytes=inicio-` e `bytes=-sufixo`. Um
intervalo valido responde `206` com `Content-Range: bytes inicio-fim/total`,
`Content-Length` apenas do segmento e o `Content-Type` original -- os mesmos
headers de seguranca da resposta completa, incluindo `Content-Disposition:
attachment` e `nosniff`.

O parsing e o de `net/http` (`http.ServeContent`), nao um parser proprio, entao
limites invertidos, numeros fora de `int64`, unidades desconhecidas e intervalos
que comecam depois do fim do arquivo sao tratados la. Consequencias observaveis:

- intervalo que comeca em/apos o tamanho: `416` com `Content-Range: bytes */N`;
- intervalo malformado: `416` sem `Content-Range`, corpo `text/plain` -- nao o
  envelope `{error:{code,message}}` do restante da API;
- `bytes=` sem nenhum intervalo, e qualquer `Range` em arquivo de tamanho zero:
  header ignorado, resposta `200` normal;
- `bytes=-0`: `206` com corpo vazio.

**Multiplos intervalos nao sao suportados.** Cada intervalo custa uma leitura
propria no storage e uma decifragem propria, entao uma lista permitiria que um
request barato virasse muitos caros. `Range` contendo virgula responde `416
range_not_supported` com `Content-Range: bytes */N`; nenhuma resposta
`multipart/byteranges` e produzida. Players de midia pedem um intervalo por vez.

#### Range e o gate de malware scan

`Range` nao e um caminho alternativo de acesso. Autenticacao, autorizacao do
anexo e o gate de scan sao resolvidos **antes** de o header ser lido: um
`Range: bytes=0-1` sobre anexo `pending_scan` responde `409`, sobre anexo
invisivel responde `404`, e sobre requisicao sem token responde `401` -- nenhum
byte sai em qualquer um dos casos.

#### Range e envelope encryption

O formato NCF1 permite acesso aleatorio sem mudanca alguma: todo chunk exceto o
ultimo tem exatamente 64 KiB de plaintext e ocupa um numero constante de bytes
armazenados, e nenhum chunk depende do anterior (nonce = `baseNonce||indice`, e
o indice entra na associated data). Logo o chunk que contem um offset, e onde
ele comeca no objeto, sao aritmetica pura.

Servir um intervalo custa **uma leitura ranged no SeaweedFS** (mais uma unica
leitura de 12 bytes do header por download, reaproveitada nos seeks seguintes) e
a decifragem de no maximo 64 KiB extras -- os bytes do chunk que precedem o
offset. Nada antes do offset e lido ou decifrado.

Garantias preservadas: cada chunk entregue continua autenticado contra a
identidade do objeto, o proprio indice e a versao do formato, e o
`plaintextSize` continua autenticado no wrapped DEK antes de qualquer header
sair. Uma unica garantia nao se aplica a leitura parcial, por construcao: um
intervalo que nao alcanca o frame final nao detecta truncamento da cauda do
objeto. Os bytes servidos continuam autenticos e na posicao correta.

#### Limitacao conhecida: o cliente web ainda nao usa Range

O `<video>` do navegador nao consegue enviar `Authorization: Bearer`, e as
alternativas que permitiriam isso sao todas piores -- token na URL vaza por
historico, logs e referrer, e uma rota publica perderia a autorizacao por
request e o gate de scan. O cliente web portanto busca o arquivo pelo cliente
autenticado normal e reproduz de um blob URL, o que carrega o arquivo inteiro
antes de comecar.

Por isso o player inline e limitado a **50 MiB** (`MAX_INLINE_VIDEO_BYTES` em
`apps/web/src/chat/attachmentVideo.ts`); acima disso nao ha player e o arquivo
continua sendo apenas um arquivo. O limite e conforto de cliente, nao controle:
o servidor decide o que pode ser lido, com ou sem `Range`.

O caminho de evolucao e um service worker na frente da rota de conteudo, que
adicionaria o header e deixaria o `<video>` falar `Range` diretamente com o
servico. O suporte a `Range` documentado acima ja esta pronto para isso.

## Preview inline (RF-31, issue #464)

Thumbnail de imagem e **primeira pagina** de PDF, gerados de forma assincrona e
armazenados como objeto proprio, cifrado do mesmo jeito que o anexo original.

### O upload nao espera pela renderizacao

O `POST` de upload responde assim que o objeto esta duravel. A mesma `UPDATE`
que finaliza o anexo grava `preview_status`, entao o job ja nasce agendado: nao
existe segunda escrita que um restart possa perder, nem fila separada que possa
divergir da linha.

Um worker em cada replica faz claim das linhas devidas
(`FOR UPDATE SKIP LOCKED` + lease em `preview_next_attempt_at`), le o anexo pelo
mesmo caminho que o download usa, renderiza com limites, cifra o resultado e
grava o estado com `UPDATE ... WHERE preview_status = 'pending'`. Duas
tentativas simultaneas nao produzem dois previews: a perdedora apaga o proprio
objeto.

### Estados

`previewStatus` aparece no upload, nos metadados e na listagem:

| Estado        | Significado                             | O que o cliente faz       |
| ------------- | --------------------------------------- | ------------------------- |
| `pending`     | aguardando o scan, ou sendo gerado      | icone; reler metadado     |
| `ready`       | preview existe e pode ser pedido        | requisitar `/preview`     |
| `unsupported` | nunca havera preview para este conteudo | icone + botao de download |
| `failed`      | a geracao falhou                        | icone + botao de download |

`unsupported` e `failed` sao ausencias com o **mesmo fallback** para o usuario e
significados diferentes para operacao: a primeira e esperada, a segunda e
incidente. O anexo continua integro e baixavel nos dois casos.

`pending` cobre duas situacoes que o cliente trata igual: o scan ainda nao
aprovou o arquivo (ver "Relacao com o scan de malware") ou a renderizacao ainda
nao rodou. Em nenhuma das duas ha o que exibir, entao o painel mostra o icone.

### Como o PDF e renderizado

PDFium compilado para **WebAssembly**, executado pelo wazero
(`github.com/klippa-app/go-pdfium`, MIT; `github.com/tetratelabs/wazero`,
Apache-2.0). Nao ha binario de sistema, subprocesso, shell nem caminho de
arquivo em lugar nenhum do fluxo.

A escolha e de contencao, nao de conveniencia: parser de PDF e superficie
historica de bugs de memoria, e as alternativas (pdftoppm/ImageMagick no
container, ou PDFium via cgo) exigiriam abandonar
`CGO_ENABLED=0` + `gcr.io/distroless/static` e ainda deixariam o parser rodando
no mesmo espaco de memoria do servico. No sandbox WebAssembly ele tem memoria
linear propria (teto de 128 MiB), nao alcanca a memoria do host, e um crash e
um valor de erro em vez de um sinal. O contexto do job e o contexto do modulo,
entao o timeout realmente interrompe a renderizacao.

O runtime usado e o **interpretador** do wazero, nao o compilador: para uma
pagina, interpretar custa ~180 MiB e ~0,5 s, contra ~280 MiB e ~3,5 s
compilando. O sandbox e criado e destruido por render, entao o custo so existe
quando ha PDF e o consumo em regime permanente do file-service nao muda.

**Impacto operacional:** o `limits.memory` do Deployment do file-service passou
de 256Mi para 512Mi (`infra/k8s/base/services/file-service/deployment.yaml`) por
causa desse pico transitorio. Uma replica renderiza um preview por vez, entao o
pico nao cresce com a fila.

### Leitura do preview

```bash
curl https://nchat.local:8443/api/files/attachments/$ID/preview \
  -H "Authorization: Bearer $ACCESS_TOKEN"
```

Headers da resposta: `Content-Type: image/jpeg`, `Content-Disposition: inline`,
`X-Content-Type-Options: nosniff`, `Accept-Ranges: none`,
`Cache-Control: private, no-store`, `Content-Length`.

`inline` aqui e seguro **porque os bytes nao sao o arquivo enviado**: sao um
raster que o proprio servico codificou a partir de pixels que ele decodificou.
Um upload de HTML, SVG ou script nao tem caminho para virar preview -- nao esta
na allowlist -- entao nao existe conteudo do usuario sendo servido inline. O
nome do arquivo tambem nao e ecoado: a resposta nao e o arquivo.

Nao ha URL publica, URL assinada nem token: a rota exige `Bearer` e reautoriza a
cada chamada. Esta rota nao aceita `Range` (`Accept-Ranges: none`): o preview e
um JPEG pequeno e nao ha caso de uso para servi-lo em partes. `no-store` existe porque a visibilidade e reavaliada por request
-- perder acesso ao canal precisa apagar tambem as miniaturas, e um cache
compartilhado nao sabe decidir isso.

| Status | Codigo                  | Quando                                            |
| ------ | ----------------------- | ------------------------------------------------- |
| 200    | -                       | anexo `clean`, visivel, com preview `ready`       |
| 401    | `unauthorized`          | token invalido ou sessao inativa                  |
| 403    | `file_not_scanned`      | anexo existe e e visivel, mas nao esta `clean`    |
| 404    | `not_found`             | anexo inexistente, removido ou fora do alcance    |
| 409    | `preview_not_available` | sem preview servivel (pending/unsupported/failed) |
| 503    | `service_unavailable`   | storage indisponivel ou objeto ausente            |

As duas recusas tem status e mensagens diferentes de proposito: `403` diz que o
anexo nao foi aprovado pelo antimalware e que nada que o cliente faca muda isso;
`409` diz que nao ha preview servivel agora. Um cliente que pediu preview nao
pode ser informado de que o arquivo esta aguardando scan quando nao esta. Qual
das tres ausencias e a real vem do `previewStatus` do metadado, nao desta rota
-- ela nunca descreve estado interno.

### Relacao com o scan de malware

O worker de preview e o unico componente que **descriptografa um anexo e entrega
os bytes a um parser**. O gate de scan aqui protege, portanto, **o proprio
servidor que roda o parser** -- nao apenas quem baixaria o arquivo. Por isso a
regra e uma so e vale nos dois momentos:

- **Geracao:** o claim exige `status = 'clean'`. `pending_scan` (ainda sem
  veredito), `rejected` (veredito contrario), `pending_upload` e `failed`
  (uploads que nao terminaram) nao sao elegiveis. A condicao esta **dentro da
  UPDATE atomica do claim**, nao em uma checagem anterior que poderia ficar
  obsoleta entre a leitura e a descriptografia.
- **Entrega:** a rota de preview aplica o mesmo gate do download. Um anexo que
  virar `rejected` depois de o preview existir deixa de servi-lo no mesmo
  instante, porque a autorizacao consulta o `status` atual a cada requisicao.

Entre o claim e a publicacao existe uma janela -- a renderizacao leva tempo e um
veredito pode chegar no meio dela. Por isso o `UPDATE` que publica **reafirma**
`status = 'clean'`: se o scan condenou o arquivo durante a renderizacao, nenhuma
linha e atualizada, o worker e informado disso e o objeto intermediario e
apagado. A janela e fechada, nao apenas estreita.

#### Como os vereditos sao gravados

Um veredito de rejeicao **nao** e um `UPDATE` de `status`. Ele e uma operacao de
dominio -- `MarkScanRejected`, em `PGXAttachmentStore` -- que grava, em **uma
unica statement PostgreSQL**:

- `status = 'rejected'`;
- `preview_status = 'unsupported'`, **somente** se era `pending`;
- `preview_next_attempt_at = NULL`, pelo mesmo criterio.

As duas coisas andam juntas porque separa-las produz uma linha que nenhum codigo
consegue concluir: o claim exige `clean`, entao o worker nunca mais olha para
ela, e o preview continua `pending`, entao nada nunca a finaliza. A linha ficaria
agendada para sempre. `status` e as colunas de preview vivem na mesma linha, logo
uma statement basta -- nao ha transacao nem CTE, e nao existe janela entre as
duas escritas porque nao existem duas escritas.

Detalhes do contrato:

- **estados aceitos:** `pending_scan` (veredito comum), `clean` (rescan, ou
  veredito durante a renderizacao) e `rejected` (repeticao do veredito, e
  reparo de linha deixada em `rejected + pending` por build anterior).
  `pending_upload`, `failed` e `deleted` nao aceitam veredito;
- **escopo:** id **e** workspace; um veredito nunca atravessa tenant;
- **zero linhas** e erro (`not_found`), nunca sucesso silencioso;
- **idempotente:** repetir o veredito deixa o mesmo estado e o reporta;
- **preview terminal e preservado:** `ready`, `failed` e `unsupported` nao sao
  reescritos. Um preview `ready` continua existindo internamente e **deixa de
  ser entregavel**, porque a entrega le o `status` atual do anexo, nao o do
  preview -- ver "`ready` interno nao significa entregavel";
- **`preview_attempts` e preservado:** e historico de auditoria, e um veredito
  nao desfaz trabalho que aconteceu.

O veredito **limpo** tem a operacao simetrica, `MarkScanClean`, tambem em uma
unica statement:

- **estados aceitos:** `pending_scan` (a transicao real) e `clean` (repeticao
  idempotente do mesmo veredito);
- **`rejected` esta fora:** uma aprovacao atrasada, repetida ou forjada **nunca**
  reabre um arquivo condenado. A rejeicao e final nessa direcao;
- **`deleted_at IS NULL`:** um anexo removido nao volta a circular por um
  veredito que chegou tarde;
- **`pending_upload` e `failed` estao fora:** nao existe objeto armazenado que
  possa ter sido escaneado;
- **nao toca nenhuma coluna de preview:** o upload ja deixou o job agendado
  (`preview_status = 'pending'`, `preview_next_attempt_at = now()`), entao a
  linha se torna reivindicavel no instante em que o status vira `clean` -- sem
  segunda escrita e sem janela entre os dois fatos;
- **escopo, zero linhas e idempotencia:** identicos aos da rejeicao.

`MarkScanClean` e a **unica** forma de um anexo virar `clean`. Ela nao e
alcancavel por nenhuma rota HTTP: nao existe endpoint, campo de request ou valor
vindo do cliente que decida veredito de scan, nem para o dono do arquivo, nem
para admin de workspace. Quem pudesse escolher `clean` poderia colocar um
arquivo nao verificado na frente do parser de PDF.

> **O produtor do veredito e o worker do RF-22.** Ele e a unica coisa que chama
> `MarkScanClean` e `MarkScanRejected`, roda dentro do proprio file-service e nao
> tem superficie HTTP. Ver "Scan assincrono de malware" para a fila, o retry e o
> comportamento quando o daemon esta indisponivel. Sem um scanner configurado
> (`FILE_MALWARE_SCANNER_ADDRESS` vazio) o worker nao inicia e, com scan
> obrigatorio, **nenhum anexo e aprovado e nenhum preview e gerado** -- falha
> fechada, nao regressao.

#### Como a remocao e gravada

Remover um anexo tambem finaliza o preview na **mesma statement**
(`MarkAttachmentDeleted`), pelo mesmo motivo: gravar so `deleted_at` deixaria um
job agendado que nenhum claim consegue selecionar (o claim exige
`deleted_at IS NULL`) e que nada nunca conclui.

- `deleted_at = COALESCE(deleted_at, now())` -- repetir a remocao converge sem
  mover o instante em que o anexo foi removido, que e a data de onde uma
  politica de retencao vai contar. Como a statement tambem alcanca uma linha ja
  removida, ela **repara** uma que tenha ficado com preview `pending`;
- `preview_status = 'pending'` vira `unsupported` e o agendamento e limpo;
- preview terminal (`ready`, `failed`, `unsupported`) e preservado como
  **registro** de que um preview existiu;
- se o preview era `ready`, o **objeto** dele e enfileirado para remocao na mesma
  statement (ver "Objeto do preview e reclamado na invalidacao");
- `status` **nao** e reescrito: remocao e veredito sao fatos diferentes, e um
  arquivo rejeitado que foi removido continua sendo um arquivo condenado. Todos
  os caminhos de leitura ja filtram por `deleted_at`.

Nao existe rota de remocao neste servico ainda -- a operacao vive aqui porque o
_lifecycle_ e responsabilidade deste pacote, e o consumidor futuro (retencao,
RF-34, ou uma acao de remocao) deve grava-la por ela.

#### Fence entre scan e render

O claim exige `clean`, mas um claim e um instante e uma renderizacao e um
intervalo. Entre os dois um veredito pode chegar -- e reler o status logo antes
do parser nao resolve, so estreita a janela para os microssegundos entre a
leitura e a chamada.

A exclusao e feita com **advisory lock do PostgreSQL**, por anexo:

| Lado               | Lock                                | Duracao              |
| ------------------ | ----------------------------------- | -------------------- |
| preview worker     | `pg_advisory_lock` (sessao)         | revalidacao + render |
| rejeicao / remocao | `pg_advisory_xact_lock` (transacao) | um `UPDATE`          |

Os dois disputam o **mesmo** lock: o PostgreSQL nao distingue quem o tomou. O
worker usa lock de sessao em uma conexao dedicada porque uma renderizacao dura
dezenas de segundos e nenhuma transacao deveria; as invalidacoes usam lock de
transacao porque sao uma statement so -- e porque tomar lock de sessao ali
exigiria uma segunda conexao do pool enquanto se segura um lock, que e
exatamente a forma de travar o pool quando varias invalidacoes concorrem.

Dentro da fence o worker **revalida** `clean`, `deleted_at IS NULL`,
`preview_status = 'pending'` e o token do claim antes de abrir e descriptografar
qualquer coisa. Como nada pode mudar a linha enquanto a fence estiver tomada,
essa revalidacao nao envelhece.

As duas ordens possiveis, e nenhuma terceira:

1. **invalidacao primeiro** -- ela commita, o worker pega a fence depois, a
   revalidacao falha, nada e descriptografado e o parser nao e chamado;
2. **render primeiro** -- o worker segura a fence, a invalidacao espera, o
   render termina com o anexo ainda logicamente `clean`, e o veredito commita em
   seguida. O preview eventualmente produzido deixa de ser entregavel pelo gate
   de malware, e o **objeto** dele e reclamado pela propria statement do
   veredito (ver "Objeto do preview e reclamado na invalidacao").

A fence e liberada em sucesso, erro, timeout e cancelamento. Uma conexao que nao
confirma o unlock e descartada, entao o PostgreSQL solta o lock quando o backend
morre -- um worker que caiu nunca deixa um anexo travado.

##### As duas ordens convergem

Vale ser explicito sobre o que a fence **nao** garante, porque a leitura oposta
ja causou confusao: como o worker segura a fence durante o render **e** a
publicacao, e a rejeicao espera essa mesma fence, nao existe intercalacao em que
o veredito commite entre "render terminou" e "`MarkPreviewReady` gravou". Na
ordem 2 o worker **publica** e so entao a rejeicao commita, deixando
`rejected + ready`.

Isso nao e um furo, e a garantia nao depende de qual lado ganha: as duas ordens
terminam no mesmo lugar.

| Ordem                                    | `MarkPreviewReady` | Objeto                    |
| ---------------------------------------- | ------------------ | ------------------------- |
| veredito antes do claim/fence            | nem e chamado      | nunca existiu             |
| worker publica, veredito commita depois  | grava              | enfileirado pelo veredito |
| fence perdida, veredito durante o render | **recusado**       | apagado na compensacao    |

A terceira linha e a unica em que a re-assercao de `status = 'clean'` dentro do
`UPDATE` de publicacao decide algo -- e ela e alcancavel em producao: o lock de
sessao vive em uma conexao, entao perder essa conexao devolve a fence enquanto o
worker continua renderizando, sem ter como perceber. E por isso que a
re-assercao e defesa em profundidade real, e nao decoracao.

Os testes correspondentes rodam pelo `PreviewService.ProcessDue` -- o caminho de
producao -- e nao chamando `ClaimDuePreviews` e `MarkPreviewReady` direto. A
distincao importa: um teste que reivindica e publica na mao prova que as
_statements_ recusam o que devem, mas nao diz nada sobre o fluxo conseguir
chegar la.

#### Fencing de claim

Lease e uma promessa sobre tempo, e tempo e justamente o que um worker travado
nao garante. Por isso `preview_attempts` -- incrementado atomicamente pelo claim
e devolvido no job -- e tambem o **token de fencing**: publicar e concluir
exigem que ele ainda seja o atual.

Sem isso, a tentativa 1 cujo lease expirou poderia gravar `failed` depois de a
tentativa 2 ter comecado, encerrando o job dela e fazendo-a descartar um preview
valido. Com isso, a tentativa antiga nao encontra linha, e aprende que perdeu a
posse.

Uma invalidacao (rejeicao ou remocao) invalida todo token pendente sem precisar
conhecer nenhum: a linha deixa de estar `pending`, e nenhuma conclusao encontra
linha.

#### Cleanup duravel de objetos

O objeto do preview e enviado ao SeaweedFS **antes** de a linha que aponta para
ele poder ser gravada -- a chave protegida autentica o objeto. Quando a
publicacao e recusada, o objeto precisa ser removido; e quando esse `Delete`
falha, a chave nao pode viver so em um log.

A chave vai para `files.object_cleanup_jobs` (migration `000004`), com
`object_key` **UNIQUE** (enfileirar de novo nao cria job novo). Um worker drena
a fila a cada 30 s com o mesmo padrao do preview -- claim com
`FOR UPDATE SKIP LOCKED`, lease, e token de tentativa para concluir. O job so e
apagado **depois** de o objeto sumir.

- objeto ja ausente e sucesso idempotente;
- objeto **entregavel** nunca e apagado -- o worker checa antes e encerra o job
  por estar errado, nao por ter dado certo. Ver a definicao de "referenciado"
  logo abaixo;
- nao ha teto de tentativas: desistir nao removeria o objeto, so faria ninguem
  mais saber dele;
- a metrica de orfao passa a significar o unico vazamento restante: storage
  recusou o delete **e** o banco recusou a fila.

O worker tem contador proprio, `nchat_file_object_cleanups_total`, com rotulo
`result` em `removed` / `referenced` / `retry`. Ele **nao** escreve em
`nchat_file_previews_total`: as duas series respondem perguntas diferentes
(previews sendo produzidos vs. storage sendo recuperado) e a palavra `retry`
existe nas duas com sentidos distintos -- no cleanup e um `Delete` recusado pelo
storage, no preview e uma renderizacao que sera tentada de novo. Compartilhar o
contador fazia uma indisponibilidade de storage aparecer como preview falhando
a renderizar.

##### Objeto do preview e reclamado na invalidacao

"Referenciado" e o **gate de entrega**, nao "apontado por alguma linha":

```
preview_status = 'ready' AND status = 'clean' AND deleted_at IS NULL
```

Um preview `ready` sob um anexo `rejected` ou removido e inalcancavel por
construcao -- todo caminho de leitura filtra pelo `status` e pela visibilidade do
anexo -- mas a linha continuava apontando para a chave. Com a definicao antiga o
worker classificava o job como `referenced` e o encerrava **sem apagar nada**: o
objeto ficava sem dono e sem job, um vazamento permanente.

Por isso as duas transicoes que tornam um preview inentregavel enfileiram a
chave dele **na mesma statement** que as grava:

- `MarkScanRejected` -- um rescan pode condenar um arquivo cujo preview ja foi
  publicado;
- `MarkAttachmentDeleted` -- o anexo sumiu, e um preview e artefato derivado sem
  valor proprio.

Mesma statement, nao apenas mesma transacao: a mudanca de estado e o job que a
compensa ficam visiveis juntos ou nao ficam. Nao existe janela em que o objeto
esteja inentregavel e sem job, nem em que exista job para um preview ainda sendo
servido. `ON CONFLICT DO NOTHING` faz um veredito repetido enfileirar uma vez so.

O objeto **do anexo** nao segue essa regra e a diferenca e deliberada: ele e o
arquivo do usuario, e por quanto tempo um arquivo removido e retido e decisao de
politica (RF-34). Um preview e derivado -- sem o anexo, nada pode servi-lo nem
regenerar um uso para ele, e mante-lo e acumulo puro.

A expressao que deriva a chave em SQL e um unico fragmento
(`previewObjectKeyExpr`), usado pelas tres statements, porque uma copia que
divergisse de `domain.PreviewObjectKey` nao falharia alto: enfileiraria chaves
que nao casam com objeto nenhum e leria previews vivos como nao referenciados.
`TestIntegrationPreviewObjectKeyExprMatchesDomain` compara as duas contra o banco
real.

#### Estado do fluxo padrao

| Etapa                            | Existe? |
| -------------------------------- | ------- |
| upload -> `pending_scan`         | sim     |
| enfileiramento do scan           | sim     |
| worker ClamAV que decide (RF-22) | sim     |
| operacao canonica `clean`        | sim     |
| operacao canonica `rejected`     | sim     |
| operacao canonica de remocao     | sim     |
| claim -> render -> `ready`       | sim     |

A cadeia e completa quando ha um daemon configurado. **Sem
`FILE_MALWARE_SCANNER_ADDRESS`** o worker nao inicia e a cadeia para na segunda
linha: os anexos sao enviados, listados e ficam em `pending_scan`, o download
responde `403 file_not_scanned` e o preview permanece `pending`.

**Consequencia operacional, explicita:** com `FILE_MALWARE_SCAN_REQUIRED=true`
(default) e sem scanner configurado, todo upload termina em `pending_scan` e
**nenhum preview e gerado**. Isso e falha fechada, nao regressao: a alternativa
seria mandar conteudo nao verificado para um parser de PDF, que e exatamente o
risco que este desenho existe para conter. Os anexos continuam sendo enviados,
listados e -- quando o scan aprovar -- baixados normalmente. O mesmo vale para um
daemon que esta configurado mas fora do ar: erro de scan nunca vira aprovacao.

Em ambiente de desenvolvimento explicitamente declarado
(`FILE_MALWARE_SCAN_REQUIRED=false` com `APP_ENV` na allowlist fechada), o
upload ja finaliza em `clean` e o preview e gerado. Nao existe chave separada
para o preview: ele herda a mesma politica, e um `APP_ENV` ausente, vazio ou
desconhecido continua sendo tratado como ambiente implantado e recusado na
inicializacao.

> O scan de malware reduz o risco de conteudo conhecido, mas **nao** substitui
> sandbox, limites de recursos, atualizacao de dependencias e isolamento do
> container. Um arquivo `clean` nao e um arquivo comprovadamente seguro: e um
> arquivo em que o scanner nao reconheceu nada. Por isso as defesas abaixo
> valem mesmo depois da aprovacao.

### Isolamento do renderizador

| Propriedade                     | Valor                                                              |
| ------------------------------- | ------------------------------------------------------------------ |
| PDF                             | PDFium compilado para WebAssembly, sobre wazero                    |
| Imagem                          | decoders da biblioteca padrao do Go                                |
| cgo                             | ausente (`CGO_ENABLED=0` em toda a imagem)                         |
| Subprocesso / shell             | ausente -- nenhum `exec`, nenhum `sh`                              |
| Filesystem do modulo WASM       | **nenhum** -- `FSConfig` vazio, zero preopens                      |
| Rede do modulo WASM             | nenhuma -- nenhum socket concedido                                 |
| stdout/stderr do modulo         | descartados (nao vao para o log do servico)                        |
| Variaveis de ambiente no modulo | nenhuma                                                            |
| Memoria linear maxima           | 2048 paginas = 128 MiB, teto rigido                                |
| Timeout por job                 | 45 s, propagado como contexto do modulo                            |
| Concorrencia                    | 1 render por replica (claim de 1 job por passagem)                 |
| Ciclo de vida                   | sandbox criado e destruido por render, inclusive em erro e timeout |

`FSConfig` merece destaque: o `go-pdfium` **monta a raiz do host (`/`) em modo
leitura e escrita** quando esse campo fica nulo. O servico passa um `FSConfig`
vazio explicitamente, e existe teste de regressao para isso -- um "simplificar o
struct" reabriria o buraco silenciosamente.

O modulo WASM e o embutido no proprio go-pdfium, com versao fixada em
`go.mod`/`go.sum`. Nada e baixado em runtime e o cliente nunca escolhe qual
modulo roda.

### Limites de conteudo

| Limite                    | Valor                               |
| ------------------------- | ----------------------------------- |
| Bytes lidos da fonte      | 20 MiB (acima disso: `unsupported`) |
| Pixels da imagem de fonte | 40 MP, verificados **no header**    |
| Dimensao de saida         | 512 px na maior aresta              |
| Paginas de PDF            | somente a primeira                  |
| Formato de saida          | JPEG reencodado pelo servidor       |
| Tentativas do renderer    | 3 por anexo, no total               |

Formatos **aceitos**: `image/jpeg`, `image/png`, `image/gif`, `application/pdf`.

Formatos **explicitamente nao aceitos**: SVG (e markup com script, nao imagem),
HTML, XML, documentos do Office, arquivos compactados, video, audio, `image/webp`
e `image/tiff`. Nao ha decoder para eles no binario: um tipo fora da allowlist
nunca chega a um parser, e a decisao usa o tipo **detectado do conteudo** --
extensao e `Content-Type` do cliente nao decidem nada.

### Recuperacao e concorrencia

| Constante              | Valor | Papel                                    |
| ---------------------- | ----- | ---------------------------------------- |
| Jobs por claim         | 1     | uma renderizacao por replica de cada vez |
| Timeout do job         | 45 s  | teto de uma renderizacao                 |
| Margem do lease        | 30 s  | cleanup e escrita terminal desacoplados  |
| Lease                  | 75 s  | derivado: timeout + margem               |
| Tentativas do renderer | 3     | orcamento de CPU por anexo               |

O lease e **derivado** do timeout, nunca escolhido ao lado dele, e existe uma
verificacao em tempo de compilacao que quebra o build se essa relacao for
violada. Um lease menor que o trabalho que protege entregaria a mesma linha a
dois workers.

O claim **nao** tem teto de tentativas, e isso e deliberado: o estado que diz
"desisti" e ele proprio uma escrita no PostgreSQL. Se o banco estiver
indisponivel exatamente nesse momento, uma elegibilidade limitada por tentativas
deixaria a linha presa em `pending` para sempre. Quem tem orcamento e o
**renderizador**: passado o limite, o claim ainda acontece, mas nao
descriptografa nada -- so finaliza a linha. Uma passagem de recuperacao custa um
`UPDATE`, nao uma renderizacao.

### Container

O `Deployment` do file-service roda sem root (uid/gid 65532), com
`allowPrivilegeEscalation: false`, `capabilities: drop: [ALL]`,
`seccompProfile: RuntimeDefault`, `readOnlyRootFilesystem: true` e
`automountServiceAccountToken: false`. O unico caminho gravavel e um `emptyDir`
em `/tmp` com `sizeLimit: 64Mi` -- nada no servico escreve la, mas a raiz e
somente leitura e o runtime precisa que o caminho exista. Ha `requests`/`limits`
de CPU, memoria e `ephemeral-storage`.

**Risco residual registrado:** o repositorio ainda nao tem base de
`NetworkPolicy` de **egress** (as policies atuais so tratam ingress), entao o
egress do file-service nao esta restrito a PostgreSQL, SeaweedFS e DNS. Criar
uma policy isolada sem cluster para validar quebraria o ambiente, entao isso
fica como hardening separado. Nenhum componente do preview abre conexao de rede
-- o modulo WASM nao tem socket algum --, mas a contencao pos-comprometimento do
processo Go permanece incompleta ate essa policy existir.

### Persistencia

Colunas `preview_*` em `files.attachments` (migration
`migrations/files/000003_attachment_previews`). Todas nullable ou com `DEFAULT`,
entao a migration e puramente aditiva: um file-service anterior a ela continua
inserindo e finalizando linhas normalmente, e por isso ela nao precisa de lock
nem de guarda de tabela vazia como a `000002`.

O objeto do preview e cifrado com **DEK propria** e binding proprio: o
`preview_object_id` e um UUID aleatorio novo, e e ele que entra na AAD do
envelope e da chave. Preview e original sao, portanto, criptograficamente
disjuntos -- nenhum dos dois pode ser aberto como o outro ou substituido por
ele. A chave do objeto (`nchat/previews/<preview_object_id>`) e derivada desse
UUID e nunca sai do servidor.

Um `CHECK` exige que `preview_status = 'ready'` implique objeto, tamanho, chave
protegida, id da KEK e as duas versoes de formato presentes: "ready mas
inabrivel" nao e um estado representavel.

**Rollback:** a `down` da `000003` roda com a tabela populada, porque so remove
dado **derivado** -- anexos, objetos originais e chaves ficam intactos e o
download continua funcionando byte a byte. Os objetos sob `nchat/previews/`
sobrevivem e ficam sem referencia; limpar o prefixo e passo operacional apos o
rollback, deliberadamente fora da migration.

### Observabilidade

`nchat_file_previews_total{result}` com o conjunto fechado `ready`,
`unsupported`, `failed`, `retry`. `failed` subindo e incidente; `unsupported`
subindo e so usuario enviando formato que ninguem renderiza.

Log por job: `attachment_id`, `result`, `attempt`, `duration_ms`. Nunca
filename, conteudo, tipo de um arquivo especifico, chave de objeto, mensagem do
renderizador ou material de chave.

### Frontend

O painel de arquivos (`apps/web/src/chat/ConversationDetailsPanel.tsx`) mostra a
miniatura no lugar do icone quando -- e somente quando -- o anexo esta `clean`
**e** `previewStatus` e `ready`. Qualquer outra combinacao mantem o icone: e o
mesmo fallback para `pending`, `unsupported`, `failed` e para erro HTTP.

Toda a logica vive em `AttachmentThumbnail.tsx`, um componente pequeno com o
hook que possui o ciclo de vida da URL:

- os bytes sao buscados com `authenticatedFetch`, isto e, com o token no
  **header**; nunca em query string, onde ele entraria em historico, logs e
  referrers;
- a resposta vem como `Blob` e vira um object URL valido apenas neste documento
  -- nao existe URL publica, assinada ou reutilizavel;
- a URL e revogada quando o anexo muda, quando o componente desmonta, quando uma
  resposta nova substitui a anterior e quando o `<img>` falha ao decodificar;
- uma resposta que chega depois da troca de anexo e descartada sem virar URL;
- falha de rede ou HTTP nao e repetida: um `409` responderia igual todas as
  vezes, e re-tentar a cada render seria um laco;
- o nome do arquivo entra apenas como `alt`, como texto que o React escapa --
  nunca markup, nunca parte de uma URL.

`filesApi.ts` valida `previewStatus` contra o conjunto fechado e degrada
qualquer valor desconhecido -- e a ausencia do campo -- para `unsupported`, entao
um servidor antigo resulta em icone, nunca em uma tentativa de carregar preview
inexistente.

**Reconciliacao ate `clean` + `ready`.** O preview e produzido depois que o
upload responde, entao um painel ja aberto veria o estado inicial
indefinidamente. Esse estado inicial e `status: "pending_scan"` com
`previewStatus: "pending"` -- e o scan de malware e obrigatorio, entao **todo**
upload passa por ele. O painel reconsulta **somente a listagem de anexos**
enquanto houver ao menos um anexo cujo estado ainda possa mudar sozinho:

| `status`       | `previewStatus`        | Reconcilia | Por que                                          |
| -------------- | ---------------------- | ---------- | ------------------------------------------------ |
| `pending_scan` | `pending`              | sim        | o scan pode aprovar, e entao o worker renderiza  |
| `clean`        | `pending`              | sim        | o worker ainda pode terminar                     |
| `clean`        | `ready`                | nao        | e o destino                                      |
| `clean`        | `failed`/`unsupported` | nao        | nao ha o que esperar                             |
| `rejected`     | qualquer               | nao        | nunca sera reivindicado pelo worker              |
| `pending_scan` | terminal               | nao        | o preview ja acabou; so a entrega aguarda o scan |

O predicado cobre todas as combinacoes, inclusive as que o servico nao produz
(ver a tabela canonica em "Estados"): decidir "nao ha nada a esperar" a partir
do par completo e o que impede um estado inesperado de virar um timer eterno.

Esse predicado e `isPreviewWorkPending` (`useAttachmentPreview.ts`), separado de
`canShowPreview`, que continua exigindo `clean` **e** `ready`. Os dois nunca sao
verdadeiros ao mesmo tempo: **a reconciliacao nunca pede os bytes**. Enquanto o
anexo estiver em `pending_scan`, nenhuma requisicao a `/preview` e emitida --
seria respondida `409` e violaria o gate documentado em "Relacao com o scan de
malware".

O ciclo:

- e unico por painel -- um timer, nunca um por anexo;
- so agenda a proxima consulta quando a anterior termina, entao requisicoes nao
  se sobrepoem nem se acumulam;
- para assim que nada mais puder mudar sozinho, no unmount e na troca de
  conversa (a requisicao em voo e abortada);
- em falha transitoria mantem a lista visivel e tenta de novo no proximo passo
  da cadencia, sem transformar o painel em estado de erro.

**A janela e finita.** Renderizar e uma espera limitada (o worker reivindica em
ate 10 s e renderiza em ~1 s); _esperar pelo scan_ nao e -- um scanner parado,
inalcancavel ou ainda nao implantado (ver "Estado do fluxo padrao") nunca da
veredito. Por isso:

- a cadencia comeca em 5 s (`previewReconcileIntervalMs`), **dobra** a cada
  consulta que nao observa mudanca e para em 30 s
  (`previewReconcileMaxIntervalMs`);
- a janela termina depois de 12 consultas sem mudanca
  (`previewReconcileMaxAttempts`), aproximadamente 5 minutos de observacao;
- o contador e por **estado observado**, nao por painel: qualquer mudanca na
  lista -- `pending_scan` virando `clean`, `pending` virando `ready`, um anexo
  saindo da lista -- reinicia a cadencia, entao uma espera longa pelo scan nao
  consome o orcamento da renderizacao seguinte.

Atingir o limite nao e erro: o icone de fallback ja e o que esta na tela, o
arquivo continua listado e baixavel quando liberado, e ha dois caminhos de volta
-- uma atualizacao explicita (`reload`, o mesmo usado apos mudanca de membros) ou
fechar e reabrir o painel. Ambos abrem uma janela nova.

Quando a listagem volta com `clean` + `ready`, o fluxo existente busca o Blob uma
unica vez para aquele anexo.

**Revogacao de uma thumbnail ja exibida.** Um rescan pode condenar um arquivo
que **ja tem preview** e que o usuario **ja esta vendo**. O backend continua
correto -- a rota reaplica o gate e responde `409` -- mas isso vale para a
_proxima_ requisicao: os bytes que ele ja entregou estao na pagina, dentro de
uma object URL viva, e nenhuma decisao do servidor os tira de la. Somente o
cliente pode remover o que ele mesmo esta exibindo, e so consegue perceber isso
se continuar perguntando.

Por isso o mesmo agendador tem **dois modos**, e nunca dois timers:

| Modo        | Enquanto                     | Cadencia                             | Limite       |
| ----------- | ---------------------------- | ------------------------------------ | ------------ |
| _geracao_   | algum `isPreviewWorkPending` | 5 s dobrando ate 30 s                | 12 consultas |
| _revogacao_ | algum `canShowPreview`       | 30 s (`previewRevalidateIntervalMs`) | nenhum       |

Os limites sao diferentes porque o custo de desistir e diferente. Desistir da
geracao atrasa uma miniatura. Desistir da revogacao devolve a tela para conteudo
que ja foi revogado -- entao esse ciclo **nao tem orcamento de tentativas** e nao
para enquanto houver algo exibido. O modo de geracao tem precedencia enquanto
tiver trabalho, e quando o orcamento dele se esgota o painel **cai para o modo de
revogacao** em vez de parar: um upload travado em `pending_scan` na mesma lista
nao pode desligar a vigilancia de uma thumbnail exibida.

O que mantem o custo baixo nao e um limite, e a cadencia e a visibilidade: com a
aba oculta o ciclo e desarmado por completo (`visibilitychange`), porque uma aba
oculta nao esta exibindo nada; ao voltar, o timer e rearmado e a defasagem
maxima e a propria cadencia. E o mesmo padrao de revalidacao que
`useMessages` ja usa para mensagens referenciadas.

Ao observar a listagem atualizada, a thumbnail e removida **imediatamente**
quando o anexo deixa de satisfazer `clean` + `ready`, o que inclui:

- `rejected`, em qualquer `previewStatus` -- **inclusive `rejected + ready`**,
  que e um estado interno valido e **nunca** entregavel (ver "`ready` interno nao
  significa entregavel");
- volta para `pending_scan`;
- `previewStatus` virando `failed` ou `unsupported`;
- ausencia do anexo na listagem atual -- que e como o cliente enxerga um
  soft-delete, ja que a listagem nao inclui linhas removidas. A resposta
  **substitui** a lista inteira, nunca faz merge, entao um anexo removido nao
  sobrevive no estado renderizado.

Em todos esses casos, no unmount e na troca de conversa: a object URL e revogada,
o estado local do preview e limpo, o fallback volta, uma requisicao em voo e
abortada e uma resposta que chegue depois e descartada sem virar URL. Nenhuma
nova leitura de `/preview` e feita -- perder elegibilidade nunca e motivo para
pedir de novo. Enquanto o anexo permanecer `clean + ready`, a vigilancia **nao**
rebusca os bytes: a listagem e relida, os metadados sao iguais, e a mesma object
URL continua valendo.

Isso nao transforma o frontend em controle de autorizacao. A autoridade continua
sendo o backend, que bloqueia toda leitura nova de um anexo rejeitado; o que o
cliente adiciona e a unica coisa que o backend nao pode fazer sozinho, que e
parar de exibir bytes que ele proprio ja recebeu enquanto o acesso era valido.
`clean` tambem nao significa seguranca absoluta -- significa que o scanner
aprovou naquele momento, e e exatamente por isso que um rescan posterior precisa
chegar a tela.

## Scan assincrono de malware (RF-22)

O upload nao espera pelo scanner. Ele termina quando o objeto esta durAvel e a
linha esta finalizada em `pending_scan`; o veredito chega depois, por um worker
que roda dentro do proprio file-service.

### Estados

Sao tres, e sao os que a migration 000001 ja fechou em `attachments_status_check`
-- este requisito nao introduziu nenhum estado novo:

| Estado         | Significado para o usuario | Baixavel? |
| -------------- | -------------------------- | --------- |
| `pending_scan` | em analise                 | nao       |
| `clean`        | aprovado                   | **sim**   |
| `rejected`     | bloqueado                  | nao       |

A maquina de estados tem exatamente duas transicoes de veredito:

```
pending_scan --(clean)----> clean
pending_scan --(infected)-> rejected
pending_scan --(erro)-----> pending_scan   (nada e gravado)
```

A terceira linha e o requisito central: **erro de infraestrutura nao e resultado
de seguranca.** Daemon fora do ar, timeout, resposta que o cliente nao consegue
interpretar, storage indisponivel, falha de decrypt -- todos deixam a linha
exatamente onde estava. Nao existe caminho de `erro` para `clean`, e o cliente
nunca decide veredito: nao ha rota, campo de request ou header que alcance
`MarkScanClean`.

### A fila

A fila **e a coluna `status`**. Nao existe tabela de jobs: a migration 000005
adiciona apenas `scan_attempts` e `scan_next_attempt_at` a `files.attachments`, e
o agendamento e escrito pelo **mesmo `UPDATE`** que finaliza o upload
(`MarkUploaded`). Nao ha dual write -- um anexo que existe em `pending_scan` esta,
por essa mesma statement, enfileirado -- entao um crash entre duas gravacoes nao
pode perder o job, porque nao ha duas gravacoes.

O claim e o mesmo primitivo do preview e do outbox de e-mail:

```sql
SELECT ... WHERE status = 'pending_scan' AND deleted_at IS NULL ...
FOR UPDATE SKIP LOCKED
```

seguido de um `UPDATE` que empurra `scan_next_attempt_at` para o futuro (o lease)
e incrementa `scan_attempts`. Consequencias:

- **varias replicas** rodam o mesmo loop e passam por cima das linhas umas das
  outras em vez de bloquear; uma linha nunca e entregue duas vezes ao mesmo tempo;
- **crash e recuperavel**: nao ha estado `processing` para vazar. O lease expira
  e a linha volta a estar devida, sem janitor;
- **restart nao perde nada**: o agendamento esta no banco, nao em memoria.

Nenhuma transacao fica aberta enquanto o arquivo e transmitido. Sao tres passos
separados: claim curto e atomico, operacao externa (streaming para o clamd), e
gravacao curta e atomica do resultado.

### Retry e backoff

O lease **e** a agenda de retry -- um mecanismo, nao dois. A n-esima tentativa
agenda a proxima para `lease * min(n, 8)` segundos adiante, deterministico e
limitado. Um daemon fora do ar custa um `UPDATE` a cada poucos minutos por anexo,
nunca uma tempestade de conexoes contra o clamd.

Nao ha teto de tentativas e isso e deliberado: um estado que significasse
"desisti" seria um quarto estado externo, indistinguivel de "ainda em analise"
para o cliente, e exigiria uma varredura manual para sair dele. Uma linha que
ninguem consegue escanear fica onde pertence -- nao aprovada -- e volta a ser
tentada quando o daemon voltar.

`scan_attempts` e saturado (`LEAST`) para nao estourar o `SMALLINT` e travar a
statement de claim atras de uma linha em falha permanente.

### Integracao com o ClamAV

`services/file-service/internal/scanner` fala `INSTREAM` do clamd sobre TCP.

- **nada de shell.** Nao ha `exec`, nao ha `clamscan`, nao ha linha de comando.
  Filename, MIME e extensao nunca chegam ao daemon: o protocolo carrega bytes
  com prefixo de tamanho e mais nada;
- **streaming.** O objeto e aberto exatamente como um download o abre -- mesmo
  key ring, mesmo binding, mesmo reader verificador -- e enviado em blocos de
  64 KiB. Um anexo de 512 MiB custa 64 KiB de RAM. **Nenhum plaintext e
  escrito em disco**, nem em `/tmp`, nem em lugar nenhum;
- **timeouts** no connect, no socket e no job inteiro; cancelamento do contexto
  fecha a conexao por baixo do I/O, entao um shutdown interrompe um scan em
  andamento;
- **resposta validada contra um conjunto fechado.** So o `OK` exato produz
  `clean`. `FOUND` produz `rejected`. Qualquer outra coisa -- inclusive uma
  resposta sem o terminador `NUL`, que seria indistinguivel de uma truncada -- e
  falha de scan;
- **todo arquivo e escaneado.** Nada consulta o tipo detectado para decidir que
  algum conteudo dispensa analise: o tipo e um palpite sobre o conteudo que o
  scanner existe para contradizer;
- a assinatura casada **nao** e propagada: nao vai para o cliente, nem para o
  evento, nem para o log.

### Limites de tamanho

`uploadpolicy.MaxMaxUploadBytes` e 512 MiB, entao o `StreamMaxLength` do clamd
precisa ser **pelo menos** isso. Um daemon com limite menor recusaria
sistematicamente os maiores anexos, o scan falharia, e o arquivo ficaria
eternamente sem veredito -- que e exatamente a combinacao que nao pode existir.

`infra/compose/clamav/clamd.conf` fixa `StreamMaxLength 512M` e `MaxFileSize
512M` por esse motivo, e o comentario no arquivo aponta para a origem do numero.
Quando o daemon recusa por tamanho, o worker classifica o resultado como
`too_large` (metrica e log em nivel `error`), separado de um retry comum, porque
e o unico caso que se corrige mexendo em configuracao.

### Propagacao para os clientes

Depois que o veredito **ja esta persistido**, o worker publica um evento
`attachment.status` no mesmo barramento Valkey que o chat-service ja consome
(`nchat:chat:ws:broadcast:{workspace_id}`). O hub canonicaliza o envelope, recusa
tipo/identificador desconhecido, e entrega aos assinantes do canal ou da conversa
-- **reavaliando a autorizacao de cada assinante no fan-out**, como faz com
`pin.updated`.

O payload e minimo:

```json
{ "attachment_id": "...", "status": "clean", "updated_at": "..." }
```

Sem filename, sem tamanho, sem tipo, sem uploader, sem assinatura de malware. O
evento roteia por `(workspace, target)` e nunca por usuario, entao ele nao pode
ser usado para descobrir anexos, canais privados, DMs ou membros: quem ja podia
ler o destino recebe, e mais ninguem.

**Persistencia e a fonte de verdade.** O evento e best-effort: um publish que
falha nao desfaz nem repete o veredito, e um cliente que perdeu o evento obtem o
estado correto no proximo `GET`. Nao ha dependencia de entrega exactly-once.

### Configuracao

| Variavel                            | Default | Efeito                                      |
| ----------------------------------- | ------- | ------------------------------------------- |
| `FILE_MALWARE_SCAN_REQUIRED`        | `true`  | upload finaliza em `pending_scan`           |
| `FILE_MALWARE_SCANNER_ADDRESS`      | vazio   | `host:port` do clamd; vazio = sem worker    |
| `FILE_MALWARE_SCAN_TIMEOUT_SECONDS` | `300`   | orcamento de uma troca com o daemon         |
| `VALKEY_URL`                        | vazio   | barramento do evento; vazio = sem broadcast |
| `WS_INSTANCE_ID`                    | gerado  | identidade deste processo no barramento     |

Um endereco vazio **nao** afrouxa nada: sem scanner o worker nao inicia e todo
upload fica em `pending_scan`, nao baixavel. Um endereco malformado (com esquema,
com path, com porta nao numerica) e recusado na inicializacao, para nao virar um
worker que tenta um alvo inalcancavel a cada poll.

`FILE_MALWARE_SCAN_REQUIRED=false` continua sendo aceito **somente** com
`APP_ENV` explicitamente em `development`, `dev`, `local`, `test` ou `nchat-dev`.
Ausente, vazio, desconhecido ou com erro de digitacao e tratado como ambiente
implantado e recusado.

### `clean` significa scan completo (issue #483)

Um veredito `clean` so pode ser gravado quando o daemon terminou de olhar. Duas
coisas garantem isso, e elas resolvem problemas diferentes.

**Limites de conteudo — `AlertExceedsMax yes`.** Sem essa diretiva, atingir
`MaxFileSize`, `MaxScanSize`, `MaxFiles` ou `MaxRecursion` faz o clamd parar de
inspecionar e responder `OK`. Isso seria uma aprovacao sobre um arquivo
examinado pela metade. Com ela, o mesmo evento responde
`Heuristics.Limits.Exceeded FOUND` e o anexo vai para `rejected`. Medido na
imagem fixada, com um arquivo de 2 MiB sob `MaxFileSize 1M`:

```text
AlertExceedsMax no   ->  arquivo: OK
AlertExceedsMax yes  ->  arquivo: Heuristics.Limits.Exceeded.MaxFileSize FOUND
```

Falso positivo por esse heuristico e aceito de proposito: este gate prefere
recusar arquivo legitimo a aprovar arquivo nao inspecionado. A correcao de um
falso positivo e elevar o limite especifico, com justificativa — nunca voltar a
diretiva para `no`.

**Limite de tempo — ordem dos deadlines.** `AlertExceedsMax` nao cobre o limite
de tempo do engine. Esse e resolvido garantindo que o file-service perca o
controle da decisao para ninguem:

| Ordem | Prazo | Valor | Papel |
| ----- | ----- | ----- | ----- |
| 1 | deadline de socket e de job do file-service | 300 s | **autoridade externa fail-closed** |
| 2 | lease do claim do worker | 330 s | linha volta a ficar devida |
| 3 | `MaxScanTime` do clamd | 420 s | backstop interno, nunca decide |

Quando o deadline externo vence primeiro, a conexao e fechada, o cliente
devolve erro, nada e gravado e o anexo continua em `pending_scan`. O default de
`MaxScanTime` (120000 ms) invertia essa ordem e entregava o desfecho ao engine.
`scripts/ci/k8s-manifests-check.sh` prova a desigualdade
`MaxScanTime >= (FILE_MALWARE_SCAN_TIMEOUT_SECONDS + 60) * 1000` a cada build.

Toda a politica vive em `infra/k8s/base/services/clamav/clamd.conf`, um unico
arquivo versionado que o Compose e o ConfigMap do Kubernetes consomem.

### Stack Kubernetes (nchat-dev)

```text
Traefik → upload-guard:8080 → file-service:8083 → PostgreSQL:5432
                                                → SeaweedFS filer:8888
                                                → Valkey:6379
                                                → ClamAV:3310
```

- **SeaweedFS**: um unico processo `weed server -filer=true` serve master,
  volume e filer. O filer em `:8888` e o endpoint que `SeaweedFSStore.Ping`
  consulta, e por isso e ele — nao o master — que a readiness do StatefulSet
  verifica.
- **ClamAV**: `Deployment` + `Service` ClusterIP em 3310, renderizado **somente**
  pelo overlay `nchat-dev-server`. Sem Ingress, NodePort, LoadBalancer ou
  `hostPort`, e sem egress algum — inclusive sem DNS, porque freshclam esta
  desligado. As assinaturas vem da imagem fixada por digest, copiadas por um
  initContainer para um `emptyDir`.
- **upload-guard**: fronteira **L7**. Ver abaixo.

### O upload-guard e uma fronteira L7, nao de rede

A propriedade "todo POST de anexo passa pelo upload-guard" **nao** e garantida
por NetworkPolicy, e o desenho nao deve sugerir que seja. Uma NetworkPolicy
casa pods e portas, nunca metodo HTTP nem caminho — e o Traefik precisa
legitimamente alcancar o file-service no mesmo prefixo `/api/files` para
download, Range, preview e listagem.

A garantia vive na tabela de roteamento do Traefik: uma `IngressRoute` dedicada,
restrita a `Method(POST)` e ao `PathRegexp` dos dois caminhos de anexo, com
`priority` acima da regra generica de `/api/files`, os middlewares
`strip-files-prefix` e `upload-inflight`, e `upload-guard:8080` como backend.
`scripts/ci/gateway-config-check.sh` valida isso **no manifest renderizado**,
incluindo a comparacao de prioridade contra a regra generica.

Isso nao enfraquece nada: o file-service continua sendo a unica fronteira de
tamanho por politica, e o guard e defesa em profundidade contra exaustao de
disco do gateway antes da autenticacao.

### Health e readiness

O ClamAV **nao** entra em liveness nem em readiness. Indisponibilidade do daemon
nao deve provocar restart loop nem tirar o servico do balanceador: uploads
continuam sendo aceitos com seguranca, os jobs ficam duravelmente em
`pending_scan`, e os downloads continuam falhando fechado. Tornar o servico
inteiro indisponivel nao tornaria nenhum arquivo mais seguro.

### Como rodar localmente

```bash
# clamd escuta em 127.0.0.1:3310; a primeira subida carrega a base de
# assinaturas e leva alguns minutos ate ficar healthy.
docker compose -f infra/compose/compose.dev.yml up -d clamav

# no ambiente do file-service
FILE_MALWARE_SCANNER_ADDRESS=localhost:3310
```

Para verificar a deteccao sem malware real, envie o arquivo de teste EICAR: o
anexo deve ir para `rejected` dentro de um ciclo de poll (10 s) e o download
passar a responder `403 file_not_scanned`. Parar o container e enviar outro
arquivo demonstra o caminho de falha: o anexo fica em `pending_scan`, o log do
worker registra `result=retry`, e subir o container de volta faz o proximo claim
aprova-lo.

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

O `previewStatus` e um eixo **separado**, mas nao independente: ele e
subordinado ao `status` (ver "Preview inline"). Em ambientes com malware scan
obrigatorio, o worker de preview somente reivindica anexos cujo `status` seja
`clean`, e o `UPDATE` que publica o preview revalida `clean` atomicamente. Um
preview, portanto, **so pode ser criado enquanto o anexo esta aprovado** -- essa
e a invariavel, e ela e sobre o momento da criacao.

O que acontece **depois** e diferente e precisa ser dito com precisao: um rescan
pode condenar um arquivo que ja tinha preview. Nesse caso o `previewStatus`
continua `ready` internamente, porque a rejeicao preserva estados terminais de
preview. `ready` interno **nao** significa entregavel -- ver "`ready` interno
nao significa entregavel", em "Preview inline".

Enquanto o scan nao aprovar, o anexo nao e elegivel para processamento e o
`previewStatus` permanece `pending` (ou `unsupported`, quando o tipo detectado
nunca teria preview). Uma **rejeicao** finaliza atomicamente um preview
`pending` como `unsupported` e limpa o agendamento, e uma **remocao** faz o
mesmo (ver "Como os vereditos sao gravados" e "Como a remocao e gravada"), entao
`rejected + pending` e `deleted + pending` nao sao estados que o servico
produza. As combinacoes possiveis sao:

| `status`       | `previewStatus` possivel                    | Preview entregavel? |
| -------------- | ------------------------------------------- | ------------------- |
| `pending_scan` | `pending` ou `unsupported`                  | nao                 |
| `rejected`     | `unsupported`, `failed` ou `ready`          | **nao**             |
| `clean`        | `pending`, `ready`, `unsupported`, `failed` | so com `ready`      |

Uma linha removida (`deleted_at` preenchido) nao entrega preview em nenhuma
combinacao: ela nao e visivel para o chamador.

#### `ready` interno nao significa entregavel

`previewStatus` descreve o **estado interno** do preview, nao a permissao de
acesso a ele. As duas coisas sao decididas em lugares diferentes, e confundi-las
e o erro que esta secao existe para evitar.

Um preview `ready` sob um anexo `rejected` **pode existir**, e o servico nao o
apaga: o anexo estava `clean` quando o preview foi gerado, um rescan o condenou
depois, e a rejeicao preserva estados terminais de preview (ver "Como os
vereditos sao gravados"). O mesmo vale para um anexo removido.

Isso **nao** viola scan-before-render: o preview so foi produzido enquanto o
anexo estava aprovado, e nenhum byte chegou ao parser antes disso. O que muda
depois e a **entrega**:

- `GET /api/files/attachments/{id}/preview` reaplica o gate de malware e a
  visibilidade -- anexo `rejected` responde `409`, anexo removido responde `404`;
- o download responde igual;
- o frontend so pede preview para `clean + ready`, entao nem chega a tentar;
- e se a thumbnail **ja estava na tela** quando a rejeicao aconteceu, o frontend
  a remove e revoga a object URL ao observar o novo estado -- ver "Revogacao de
  uma thumbnail ja exibida". Esta e a unica parte que o backend nao pode fazer
  sozinho: ele bloqueia toda leitura nova, mas nao recolhe bytes que ja
  entregou;
- o objeto **do anexo** continua no SeaweedFS conforme a politica de lifecycle e
  retencao (RF-34), que e quem decide destruicao -- nao o veredito;
- o objeto **do preview** nao: ele e enfileirado para remocao pela propria
  statement do veredito, porque um preview e artefato derivado que ninguem mais
  pode servir (ver "Objeto do preview e reclamado na invalidacao"). Por isso
  `preview_status` continuar `ready` e um **registro** de que um preview
  existiu, nao a promessa de que os bytes ainda estao la.

Em resumo: **`rejected + ready` e um estado interno possivel e inofensivo**;
`rejected` **nunca** e entregavel. A afirmacao "ready nunca coexiste com
rejected" seria falsa e nao esta neste documento.

Com `FILE_MALWARE_SCAN_REQUIRED=true` (default, exigido por SECURITY.md) um
upload termina em `pending_scan` e o download responde `409` ate o worker
antimalware (fora do escopo desta issue) marcar `clean`.

O valor `false` so e aceito quando `APP_ENV` **declara explicitamente** um
ambiente de desenvolvimento. A regra e uma correspondencia positiva contra uma
allowlist fechada -- `development`, `dev`, `local`, `test`, `nchat-dev` --
ignorando maiusculas e espacos ao redor, e nada mais e inferido.

`APP_ENV` **nao tem default**. Ausente, vazio, so espacos, desconhecido ou com
erro de digitacao (`developmnt`) sao todos tratados como ambiente implantado e o
valor e recusado na inicializacao. Isso e deliberado: assumir `development` na
ausencia da variavel entregaria a excecao a qualquer deploy que simplesmente
esquecesse de defini-la.

Todos os overlays ja definem a variavel (`infra/k8s/base/configmap.yaml` e
`infra/k8s/overlays/*/configmap-patch.yaml`), e os `.env` locais precisam
declarar `APP_ENV=development`. Com `FILE_MALWARE_SCAN_REQUIRED=true` (default)
nada disso e exigido.

## Envelope encryption

O conteudo e cifrado no file-service antes de ir para o SeaweedFS:

- DEK de 32 bytes aleatoria por objeto (`crypto/rand`), nunca derivada de nome,
  id, timestamp ou senha;
- DEK protegida com AES-256-GCM sob a KEK ativa, com AAD binaria de campos de
  largura fixa (80 bytes) ligando versao do wrapping, versao do conteudo, id do
  anexo, id do workspace, digest do id da KEK e **tamanho plaintext**; so a
  forma protegida e persistida;
- conteudo em chunks de 64 KiB, cada um selado com AES-256-GCM sob a DEK;
- nonce = 8 bytes aleatorios por objeto + contador big-endian de 32 bits por
  chunk, entao nenhum par (chave, nonce) se repete;
- AAD de cada chunk liga versao do formato, id do anexo, indice do chunk e
  marcador de ultimo chunk.

Isso detecta alteracao, truncamento, reordenacao, duplicacao e substituicao de
chunks, e tambem substituicao do objeto inteiro por outro anexo. Como o
workspace entra na AAD da DEK, mover uma linha de tenant tambem torna o objeto
ilegivel. O formato do conteudo e versionado (`envelope_version` na tabela) para
permitir evolucao sem recifrar o que ja existe.

### Tamanho autenticado

`size_bytes` faz parte da AAD da DEK protegida. A consequencia operacional:

- o tamanho e **contado** durante o streaming, a partir dos bytes realmente
  lidos do corpo. `Content-Length` da requisicao nunca alimenta esse numero;
- ele e plaintext, nao ciphertext;
- a DEK so e protegida **depois** que o envelope NCF1 foi fechado e o tamanho e
  um fato, nunca uma promessa. Ate esse momento a linha esta em
  `pending_upload` com `wrapped_dek` NULL e nao pode ser aberta;
- reduzir `size_bytes` diretamente no PostgreSQL faz o unwrap falhar **antes**
  de qualquer header. Sem isso, o servidor publicaria um `Content-Length` menor
  e entregaria um prefixo que o cliente aceitaria como o arquivo inteiro;
- `Content-Length` so e emitido depois de um unwrap bem-sucedido, isto e, depois
  de o proprio tamanho ter sido autenticado;
- o reader de download carrega o mesmo tamanho como invariante independente: ele
  nao para de ler ao atingi-lo, continua ate o frame final autenticado e exige
  que os totais coincidam. Um `EOF` em `expectedSize` seria justamente a
  truncagem silenciosa que o binding existe para impedir.

A versao do wrapping (`dek_wrap_version`) e independente de `envelope_version`:
adicionar um campo a AAD da chave nao altera nenhum byte dos objetos ja
armazenados. Uma versao desconhecida e recusada; nao ha tentativa em sequencia
nem fallback para a AAD anterior.

Nao existe fallback para plaintext em nenhum caminho. Falha de autenticacao,
KEK ausente, DEK corrompida ou envelope malformado terminam em erro; o cliente
recebe sempre a mesma falha generica e nunca aprende qual etapa falhou.

Detalhes e referencia da construcao em
`services/file-service/internal/crypto/envelope.go`.

### Chaves e rotacao

A KEK nunca esta no banco nem no SeaweedFS. Ela vem do Secret
`nchat-file-encryption`, injetado por `envFrom` **somente** no Deployment do
file-service -- deliberadamente fora de `nchat-secrets`, que todos os servicos
montam. Os templates vazios estao em `infra/k8s/secrets/templates/`; valores
reais so existem como SealedSecret.

O processo mantem um chaveiro: uma chave ativa, usada em todo upload novo, e
zero ou mais chaves anteriores mantidas apenas para leitura
(`FILE_ENCRYPTION_PREVIOUS_KEYS`). Cada linha guarda em `kek_key_id` o
identificador **nao secreto** da chave que protegeu sua DEK, entao o download
seleciona a chave por esse id. Um id desconhecido e recusado; nenhuma chave e
testada especulativamente, e `kek_key_id` nulo (linha ainda em `pending_upload`)
falha fechado em vez de assumir a chave ativa.

Nao existe compatibilidade com o formato de wrapping anterior a migration
`000002`, por escolha: um parser antigo seria um caminho de downgrade. A propria
migration recusa rodar se `files.attachments` tiver qualquer linha, entao esse
estado nao chega a existir em producao.

Rotacionar troca a KEK e re-protege apenas a DEK de 32 bytes: o objeto no
SeaweedFS nao e lido nem reescrito. O procedimento, as limitacoes (o job de
rewrap em lote ainda nao existe) e a validacao de arquivo grande estao em
`docs/runbooks/file-service-envelope-encryption.md`.

## Persistencia

`files.attachments` (migrations `migrations/files/000001_file_attachments` e
`000002_attachment_dek_binding`). A `000002` adquire `ACCESS EXCLUSIVE` antes de
qualquer verificacao e **aborta se a tabela nao estiver vazia**; alem disso
`dek_wrap_version` e `NOT NULL` sem `DEFAULT` e e preenchida ja no INSERT
pendente, entao um binario anterior a migration nao consegue inserir depois do
commit -- ver o runbook.

O **rollback da `000002` tambem exige a tabela vazia**, pela mesma verificacao
sob `ACCESS EXCLUSIVE`. Remover `kek_key_id` e `dek_wrap_version` nao converte
DEK nenhuma: as chaves continuam protegidas sob o binding atual, e o formato
anterior nao consegue reconstruir a AAD. Um rollback com attachments existentes
concluiria no schema e deixaria todos os downloads indisponiveis, entao ele falha
fechado. Rewrap reverso nao foi implementado e permanece fora de escopo.

Esta protecao nao altera o formato criptografico: NCF1, a AAD atual e o binding
de `size_bytes` seguem exatamente como descritos acima.

O id
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
`nchat_file_downloads_total{result}`, `nchat_file_previews_total{result}`,
`nchat_file_object_cleanups_total{result}`, `nchat_file_orphaned_objects_total`,
`nchat_file_malware_scans_total{result}` e
**`nchat_file_malware_scan_queue_depth`** (RF-22). Nenhum label carrega id,
filename ou caminho.

`nchat_file_malware_scan_queue_depth` e um gauge **sem labels**: quantos anexos
aguardam veredito no cluster. Ele e reescrito a cada passada do worker, inclusive
nas que nao encontram trabalho, porque um gauge escrito so quando ha backlog
continuaria reportando o ultimo valor para sempre depois que a fila esvaziasse.
Nao ha recorte por workspace, canal ou usuario: a cardinalidade cresceria com o
produto e a pergunta que ele responde -- a fila esta drenando? -- nao precisa de
um.

`nchat_file_malware_scans_total{result}` usa o conjunto fechado `clean`,
`infected`, `retry`, `too_large`, `superseded`. `retry` subindo e um daemon que
nao responde; `too_large` subindo e configuracao (ver "Limites de tamanho").

Os logs do worker carregam `attachment_id`, `result`, `attempt` e `duration_ms`.
Nunca carregam filename, tipo de conteudo, chave de objeto, endereco do daemon,
assinatura de malware, texto de erro do socket, DEK ou qualquer byte do arquivo.

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

A mesma invocacao cobre a geracao de preview (RF-31): `-run Integration` inclui
`preview_integration_test.go`, que sobe uma imagem e um PDF reais, roda o job
contra a fila do PostgreSQL, e reabre o objeto de preview do SeaweedFS usando
apenas o binding persistido.

O DSN precisa apontar para um banco `*_test` -- mesma guarda das suites de
integracao do chat-service. Cada execucao inventa o proprio workspace UUID e
remove no final apenas as linhas e os objetos que criou.
