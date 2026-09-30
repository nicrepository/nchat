# Search-service: busca global (RF-15, issue #900)

Busca por categoria usada pela Search View (`/chat/search`). Cada categoria e um
endpoint proprio; a aba **Tudo** chama os cinco em paralelo com `limit=5`, e as
abas especificas paginam por cursor. Nao existe agregador: as chamadas ja sao
paralelas, cada secao falha e se recupera sozinha, e um agregador seria uma
segunda copia da autorizacao.

## Contrato

| Metodo | Rota publica              | Resultado                                    |
| ------ | ------------------------- | -------------------------------------------- |
| GET    | `/api/search/v2/messages` | mensagens de conversas que o caller le       |
| GET    | `/api/search/messages`    | **legado**: mensagens de canais publicos     |
| GET    | `/api/search/users`       | pessoas ativas do workspace                  |
| GET    | `/api/search/channels`    | canais publicos e privados com membership    |
| GET    | `/api/search/groups`      | grupos com participacao ativa do caller      |
| GET    | `/api/search/files`       | anexos enviados em conversas que o caller le |

Todas exigem `Authorization: Bearer <access-token>` e sessao ativa. Parametros:
`q` (obrigatorio, 1-512 bytes apos trim), `limit` (1-100, padrao 20) e `cursor`
(opaco). Nenhum usuario ou workspace e lido da requisicao: o caller e o
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
`files`) e presos ao hash da consulta: um cursor de outra consulta ou de outro
tipo e recusado com `400`. O de `messages.v2` carrega o instante em que a
primeira pagina foi ranqueada, porque o score tem fator de recencia: sem ele, as
paginas seguintes seriam ranqueadas contra outro relogio e repetiriam ou
pulariam linhas. O de `messages` (legado) e byte a byte o formato anterior a
#900, para que servico antigo e novo aceitem os cursores um do outro enquanto
convivem; por isso ele mantem o ranking contra `now()` da versao anterior.
Nenhum dos dois aceita o cursor do outro.

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

O web da #900 chama o V2. Se o servico responder `404` -- o catch-all de um
search-service anterior a #900, que nao conhece a rota --, e somente nesse
caso, a mesma pagina e pedida uma vez ao legado e normalizada para o mesmo
`MessageSearchResult` (`kind: "channel"`, `type: "public"`). As paginas
seguintes dessa busca voltam ao legado com o cursor dele. `401`, `403`, `5xx`,
falha de rede e abort sao erros reais e nunca disparam o fallback; um cursor V2
nunca e reenviado ao legado. Tudo isso vive em `apps/web/src/search/searchApi.ts`;
nenhum componente sabe qual endpoint respondeu.

| Web | Search-service | Comportamento                                                       |
| --- | -------------- | ------------------------------------------------------------------- |
| old | old            | legado                                                              |
| old | new            | endpoint legado do servico novo: mesmo contrato, so canais publicos |
| new | old            | V2 indisponivel (404) -> fallback unico ao legado; sem DMs/grupos   |
| new | new            | V2                                                                  |

A migration `chat/000059` so expande o indice: a consulta do servico anterior
continua filtrando `type = 'public'` e segue correta sobre o schema novo
(verificado executando o SQL literal do servico anterior contra o banco migrado).

**O endpoint legado nao deve ser removido nesta issue.**
`TestLegacyMessagesRouteIsRetainedForRolloutCompatibility` falha se a rota
sumir ou mudar de forma. Remova-o, junto com o fallback do `searchApi.ts`, so
depois de uma janela de rollout em que nenhum web anterior a #900 possa mais ser
servido (slots blue/green, rollback e bundles em cache).

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
