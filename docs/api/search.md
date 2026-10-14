# Search-service: busca global (RF-15, issues #900 e #1081)

Busca por categoria usada pela Search View (`/chat/search`). Cada categoria e um
endpoint proprio; a aba **Tudo** chama os seis em paralelo com `limit=5`, e as
abas especificas paginam por cursor. Nao existe agregador: as chamadas ja sao
paralelas, cada secao falha e se recupera sozinha, e um agregador seria uma
segunda copia da autorizacao.

## Contrato

| Metodo     | Rota publica              | Resultado                                    |
| ---------- | ------------------------- | -------------------------------------------- |
| POST / GET | `/api/search/v2/messages` | mensagens de conversas que o caller le       |
| GET        | `/api/search/messages`    | **legado**: mensagens de canais publicos     |
| POST / GET | `/api/search/users`       | pessoas ativas do workspace                  |
| POST / GET | `/api/search/channels`    | canais publicos e privados com membership    |
| POST / GET | `/api/search/groups`      | grupos com participacao ativa do caller      |
| POST / GET | `/api/search/files`       | anexos enviados em conversas que o caller le |
| POST       | `/api/search/links`       | links em mensagens que o caller le (#1081)   |

Todas exigem `Authorization: Bearer <access-token>` e sessao ativa. Parametros:
`q` (obrigatorio, 1-512 bytes apos trim), `limit` (1-100, padrao 20) e `cursor`
(opaco). Num `POST` eles vem so no corpo JSON `{"q", "limit"?, "cursor"?}` --
query string e ignorada; num `GET`, na query string (veja "Transporte da
consulta"). Corpo com campo desconhecido, dado sobrando ou acima de 8 KiB e
`400`; metodo fora da tabela e `405`. Nenhum usuario ou workspace e lido da requisicao: o caller e o
principal autenticado e o workspace e resolvido no servidor.

Resposta: `{"data": {"data": [...], "pagination": {"limit", "next_cursor", "has_more"}}}`.
Nao ha contagem total: `has_more` vem de `limit + 1` linhas, sem `COUNT`. A UI
mostra "N resultados" apenas quando a lista esta completa (`has_more = false`).

### Itens

- **v2/messages**: `id`, `conversation_kind` (`channel` | `dm`), `conversation_id`,
  `conversation_type` (`public` | `private` | `direct` | `group`),
  `conversation_name`, `sender_id`, `sender_display_name`, `sender_avatar_url?`,
  `body_text`, `created_at`, `score`. A rota do cliente e
  `/chat/{conversation_kind}/{conversation_id}?message={id}`.
- **messages** (legado, contrato anterior a #900): `id`, `channel_id`,
  `channel_name`, `sender_id`, `sender_display_name`, `body_text`,
  `created_at`, `score`. Veja "Mensagens: legado e V2".
- **users**: `id`, `display_name`, `avatar_url?`.
- **channels**: `id`, `slug`, `display_name`, `type`, `description?`,
  `member_count`, `is_general`.
- **groups**: `id`, `title`, `participant_count`, `last_message_at?`. Abre em
  `/chat/dm/{id}`.
- **files**: `id`, `filename`, `content_type`, `size`, `status`
  (`pending_scan` | `clean` | `rejected`), `preview_status`, `message_id` e a
  mesma referencia de conversa das mensagens. Nunca chave de storage, URL ou
  bytes: o arquivo abre pelo Attachment Viewer, que busca no file-service.
- **links**: `message_id`, `target_key` (o `LinkTargetKey` do chat-service),
  `url` (canonica), `hostname`, a mesma referencia de conversa das mensagens,
  `sender_id`, `sender_display_name`, `sender_avatar_url?`, `created_at`. Uma
  linha por ocorrencia (mensagem + URL): a mesma URL em duas mensagens sao dois
  resultados, cada um com sua conversa, autor e autorizacao. Abre em
  `/chat/{conversation_kind}/{conversation_id}?message={message_id}`.

## Autorizacao

Uma unica definicao, em `internal/storage/search_store.go`, usada por todas as
categorias:

- **canal visivel**: `chat.channel_visible_to_user` restrito a canais ativos --
  membro do canal, ou membro nao-guest do workspace para canal publico;
- **conversa visivel**: `dm_conversations` ativa com `dm_members` ativo do caller.

Mensagens e arquivos so sao alcancados por esses dois conjuntos (o arquivo pela
mensagem que o enviou), entao snippet, arquivo e contagem nunca veem mais do que
a conversa. Isso e provado contra PostgreSQL real em
`search_store_postgres_test.go` (`SEARCH_TEST_DATABASE_URL`), incluido em
`scripts/ci/go-integration-test.sh`.

Desde a migration `chat/000059`, todo `chat.messages` ativo tem `search_vector`;
a fronteira de acesso e a consulta, nao o indice.

## Cursores

Versionados, tipados (`messages`, `messages.v2`, `users`, `channels`, `groups`,
`files`, `links`) e presos ao hash da consulta: um cursor de outra consulta ou de outro
tipo e recusado com `400`. O de `messages.v2` carrega o instante em que a
primeira pagina foi ranqueada, porque o score tem fator de recencia: sem ele, as
paginas seguintes seriam ranqueadas contra outro relogio e repetiriam ou
pulariam linhas. O de `messages` (legado) e byte a byte o formato anterior a
#900, para que servico antigo e novo aceitem os cursores um do outro enquanto
convivem; por isso ele mantem o ranking contra `now()` da versao anterior.
Nenhum dos dois aceita o cursor do outro. O de `links` carrega
`(rank, created_at, message_id, target_key)` -- a chave do alvo, nunca a URL.

## Mensagens: legado e V2

A #900 passou a buscar mensagens de canais privados, DMs e grupos, o que exige
dizer a qual conversa cada resultado pertence (`conversation_kind`/`_id`). O
contrato anterior so sabia dizer `channel_id`, e o web anterior roteia todo
resultado para `/chat/channel/:channel_id`. Mudar `/api/search/messages` no
lugar quebraria as duas combinacoes mistas de uma implantacao blue/green ou de
um rollback parcial; acrescentar os campos novos ao lado dos antigos tambem nao
resolveria, porque um cliente antigo ainda receberia uma DM e a abriria como
canal. Por isso os dois contratos coexistem:

- **`GET /api/search/messages` (legado, deprecated)**: exatamente o contrato
  anterior -- campos, paginacao, cursor, status e ranking. So devolve mensagens
  de canais publicos ativos que o caller ve (o mesmo predicado de visibilidade
  das demais categorias), nunca DM, grupo ou canal privado. Unica diferenca
  deliberada: um guest deixa de ver canal publico do qual nao e membro, como ja
  e a regra do chat.
- **`GET /api/search/v2/messages`**: o contrato da #900, todos os tipos de
  conversa autorizados.

O web da #900 chamava o V2 por `GET` e, diante de `404` (o catch-all de um
search-service anterior a #900), pedia a mesma pagina ao legado. **O web da
#1081 nao faz mais isso:** ele so envia `POST /api/search/v2/messages` (veja
"Transporte da consulta"), e um servico sem esse `POST` deixa Mensagens
indisponivel -- nem `GET` V2, nem legado. E uma decisao de seguranca: o
downgrade levaria a consulta, que pode ser um segredo, para a URL. O endpoint
legado continua no backend para os webs anteriores a #900.

| Web      | Search-service | Comportamento                                                       |
| -------- | -------------- | ------------------------------------------------------------------- |
| pre-#900 | old            | legado                                                              |
| pre-#900 | new            | endpoint legado do servico novo: mesmo contrato, so canais publicos |
| #900     | pre-#900       | V2 indisponivel (404) -> fallback unico ao legado; sem DMs/grupos   |
| #900     | #900 ou novo   | `GET` V2                                                            |
| #1081    | qualquer       | so `POST` V2 (veja "Links (#1081) / Rollout")                       |

A migration `chat/000059` so expande o indice: a consulta do servico anterior
continua filtrando `type = 'public'` e segue correta sobre o schema novo
(verificado executando o SQL literal do servico anterior contra o banco migrado).

**O endpoint legado nao deve ser removido nesta issue.**
`TestLegacyMessagesRouteIsRetainedForRolloutCompatibility` falha se a rota
sumir ou mudar de forma. Remova-o so depois de uma janela de rollout em que
nenhum web anterior a #900 possa mais ser servido (slots blue/green, rollback e
bundles em cache); o web atual ja nao o chama.

## Desempenho medido (revisao de Code Quality da #900)

PostgreSQL 16 com todas as migrations, `ANALYZE` e 50 mil grupos + 50 mil
anexos (o mesmo volume das provas de indice de `task-admin-management.md`). O
caller participa de 2.000 grupos e le 21 canais. Primeira pagina (`limit=20`,
21 linhas pedidas), `EXPLAIN (ANALYZE, BUFFERS)` sobre o SQL real do store:

| Consulta | Termo                  | Antes                                               | Depois                                              |
| -------- | ---------------------- | --------------------------------------------------- | --------------------------------------------------- |
| grupos   | comum (`trabalho`)     | Seq Scan em `dm_conversations`, 154.233 buf, 97,8ms | escopo do caller primeiro, 1.263 buf, 11,9ms        |
| grupos   | seletivo / inexistente | Seq Scan, 863 buf, ~20ms                            | 891 buf, ~9ms                                       |
| arquivos | comum (`documento`)    | Seq Scan em `attachments`, 495.611 buf, 278,5ms     | Index Scan por conversa visivel, 46.943 buf, 50,5ms |
| arquivos | seletivo / inexistente | Seq Scan, 1.613 buf, 17-26ms                        | 10.943 buf, 15-17ms                                 |

A causa era a forma do plano, nao a falta de indice: o planner estima
`LIKE '%termo%'` em poucas linhas, comeca pela tabela inteira do workspace e
testa a visibilidade linha a linha. Um indice trigram so ajudaria termos
seletivos; um termo comum continuaria testando ~50 mil linhas. A correcao nao
cria indice nem extensao: em grupos e arquivos os CTEs de autorizacao sao
`MATERIALIZED`, e arquivos
sao lidos por conversa visivel (`LATERAL ... OFFSET 0`) pelos indices parciais
`idx_attachments_channel` / `idx_attachments_conversation`, que ja existiam.
O custo passa a acompanhar o que o caller le, nao o tamanho do workspace; o
preco e ~1 sondagem de indice por conversa visivel, por isso termos seletivos
leem mais buffers que antes, em tempo igual ou menor. Resta um Seq Scan limitado
em `dm_conversations` dentro de `visible_dms` (~4-5ms a 50 mil conversas).

Mensagens mantem os CTEs inline: o indice GIN ja estreita as linhas, e com
`MATERIALIZED` o termo comum mediu 75ms contra 51ms inline (seletivo: igual).

## Links (#1081)

### Fonte

Links nao sao extraidos de novo do corpo. A busca le o que o chat-service gravou
ao escrever a mensagem: `chat.message_link_scans` (mensagem -> URL canonica,
`urlsafety.CanonicalizeURL`: esquema e host normalizados, fragmento descartado,
path e query como escritos) e `chat.link_scans` (o alvo e seu veredito). Nenhuma
tabela, indice, migration, regex ou job novo. Pesquisa-se URL completa, host e
path (`LIKE` sem diferenciar maiusculas, curingas literais); fragmento nao e
pesquisavel porque nao e persistido, e metadata de preview nao entra.

So vale a associacao de uma **projecao atual confiavel**:
`m.link_safety_projection_version > 0`, `m.link_safety_fingerprint <> ''` (o que
tambem recusa `NULL`) e `mls.fingerprint = m.link_safety_fingerprint`. E
fail-closed como `chat/000031` define:

- `projection_version = 0` nunca e pesquisavel: e corpo escrito antes de
  `chat/000029`, ou reescrito por um pod antigo -- o trigger zera versao e
  fingerprint, e as associacoes antigas continuam fisicamente na tabela;
- fingerprint `NULL` ou vazio nunca certifica uma associacao (inclusive linhas
  `''` anteriores ao fingerprint);
- fingerprint antigo diferente do atual nao casa.

Os writers atuais (criar e encaminhar com versao 1, editar incrementando) ja
produzem essa forma; uma mensagem sem URL nao tem associacao e nao muda. A edicao
troca associacoes e fingerprint na mesma transacao, entao a URL A some e a B
aparece. Mensagem `deleted` ou `pending_link_scan` nao aparece.

### Autorizacao

Os mesmos CTEs `search_scope` / `visible_channels` / `visible_dms` das demais
categorias, com o principal autenticado como `$1`. Provado em
`search_links_postgres_test.go`: canal publico, privado com e sem membership,
DM e grupo proprios e alheios, guest (so por membership), caller inativo, outro
workspace, mensagem removida, edicao A -> B, associacao de fingerprint antigo,
cada estado de projecao (versao 0, fingerprint `NULL`, vazio, antigo, reescrita
por writer legado), estados do Link Safety e paginacao.

### Link Safety

A busca e uma superficie de leitura: nao consulta sites, nao busca preview, nao
cria scanner nem altera veredito.

| Alvo (`link_scans.status`)                  | Na busca                             |
| ------------------------------------------- | ------------------------------------ |
| `pending`, `safe`, `inconclusive`/`unknown` | aparece (a timeline ja mostra a URL) |
| `malicious`                                 | nunca aparece                        |
| URL em `files.link_fetch_denylist`          | nunca aparece                        |

Um alvo condenado tem URL, texto e host retidos de todo leitor; devolve-lo, ou
deixar que a busca por ele responda algo, revelaria o que a redacao esconde. A
lista de bloqueio e a mesma evidencia que a projecao do historico de edicao ja
trata como condenacao. O card do resultado **nao abre a URL**: abre a mensagem
de origem pelo `MESSAGE_TARGET`, e a navegacao para fora continua no chip da
timeline, onde a policy decide `direct` / `interstitial` / `none`. Nenhum `href`
e derivado na busca.

### Transporte da consulta

O campo de busca e um so para todas as categorias, e a consulta pode ser uma URL
inteira, com token, assinatura ou identificador. Num `GET` ela iria para a linha
da requisicao -- access log do Traefik, traces, proxies, historico. Por isso o
web da #1081 envia **toda** categoria por `POST` com a consulta no corpo: a aba
Tudo, cada aba, a proxima pagina e o retry -- e **so** `POST`. Os `GET` da #900
continuam registrados no servidor, sem mudanca, exclusivamente para webs
anteriores (rollout e rollback); o web atual nunca os chama. Links nunca teve nem
tem `GET`. A Search View guarda consulta e aba so no estado de
historico (nunca na URL), e nada novo e persistido. O search-service nao loga a
consulta nem a URL; a metrica HTTP usa o template de rota, conjunto fechado.

**Sem fallback para `GET`.** Nao existe classificacao de consulta "segura para
URL": formato e tamanho nao dizem se uma string e confidencial -- uma URL
assinada e um token curto como `ABCDEF1234567890` sao igualmente segredos.
Quando um search-service anterior responde ao `POST` com `405` (rota da #900, so
`GET`) ou `404` (rota inexistente), a categoria fica **indisponivel** ("Esta
busca ainda nao esta disponivel.", com "Tentar novamente", que repete o mesmo
`POST`) -- nunca "nenhum resultado", nunca `GET`, nunca o legado de mensagens.
`401`, `403`, `5xx`, falha de rede e abort tambem nunca levam a um `GET`. Tudo
isso vive em `apps/web/src/search/searchApi.ts` e e provado em
`searchApi.test.ts`, `searchTransport.test.tsx` e no E2E, com uma URL assinada
e com um token curto, verificando que toda request e `POST` e que `URL.search`
e vazio.

### Ranking e paginacao

`rank`: 0 URL exata (com ou sem `/` final), 1 host exato, 2 prefixo da URL ou de
`host/path`, 3 em qualquer ponto; depois a mensagem mais recente, `message_id` e
`target_key`. Deterministico, sem `COUNT`; `has_more` vem de `limit + 1`.

### Rollout

Os endpoints e metodos novos sao aditivos; nenhum contrato `GET` da #900 muda.
A degradacao com servico antigo e deliberadamente fail-closed.

| Web | Search-service | Comportamento                                                    |
| --- | -------------- | ---------------------------------------------------------------- |
| old | new            | web antigo usa os `GET` de sempre; nao conhece Links             |
| new | new            | tudo por `POST`                                                  |
| new | old            | `POST` -> `405`/`404`: cada categoria indisponivel; nenhum `GET` |

Indisponivel e o erro explicito "Esta busca ainda nao esta disponivel." com
"Tentar novamente" na secao e na aba -- nunca "nenhum resultado".

### Desempenho medido (#1081)

PostgreSQL 16 com todas as migrations e `ANALYZE`: 200 mil mensagens, cada uma
com um link, 50 mil URLs distintas em 500 hosts; o caller le 103 canais e 2.002
grupos. Primeira pagina (`limit=20`), `EXPLAIN (ANALYZE, BUFFERS)` sobre o SQL
real do store:

| Termo                                            | Linhas que casam | Tempo  | Buffers |
| ------------------------------------------------ | ---------------- | ------ | ------- |
| URL exata (`host42.example.com/docs/page-4542`)  | 4 associacoes    | 21,9ms | 965     |
| host (`host42.example.com`)                      | 400 associacoes  | 25,3ms | 3.293   |
| inexistente                                      | 0                | 25,0ms | 863     |
| comum a todas as URLs (`example.com`, pior caso) | 200 mil (100%)   | 889ms  | 1,15 mi |

O plano parte das URLs (`Seq Scan` em `link_scans`, ~31ms a 50 mil URLs), chega
as mensagens por `idx_message_link_scans_url` e pela PK de `chat.messages`, e
testa a visibilidade com `hashed SubPlan` sobre os CTEs materializados; a
conversa so e juntada as linhas da pagina. A primeira versao juntava a
visibilidade por linha e o planner -- que estima `LIKE '%termo%'` em ~5 linhas
-- fazia nested loop sobre `visible_channels` para cada associacao: 5,8s no pior
caso, agora 889ms. O custo restante e linear nas associacoes que casam, porque o
ranking exige ordenar todas antes de cortar a pagina; um indice trigram so
ajudaria termos seletivos, que ja ficam em ~25ms, entao nenhum indice foi
criado.

A exigencia de projecao confiavel (`projection_version > 0` e fingerprint nao
vazio) e so mais um filtro na mesma `Index Scan using messages_pkey`: o plano nao
muda e os buffers ficam iguais (1.151.194 contra 1.151.156 no termo comum; 976
contra 965 no seletivo). Remedido no mesmo volume, com as mensagens na forma que
os writers atuais gravam (versao 1), alternando o predicado anterior e o novo no
mesmo container: termo comum 1,41-1,73s antes e 1,65-1,69s depois; seletivo
33-36ms antes e 39-45ms depois. A diferenca fica no ruido -- o termo inexistente,
em que o filtro nem e avaliado, variou igual (30 contra 37-41ms) -- e esse
ambiente estava mais lento que o da medicao acima.
