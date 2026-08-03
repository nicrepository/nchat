# Chat-service: detalhes do canal (issue #435)

Projecao somente leitura de um canal, usada pelo painel lateral **Detalhes do
canal** na area de conversa.

## Contrato

| Metodo | Rota publica                             | Descricao         |
| ------ | ---------------------------------------- | ----------------- |
| GET    | `/api/chat/channels/{channelID}/details` | detalhes do canal |

Exige `Authorization: Bearer <access-token>` e sessao ativa, como as demais
rotas de `/api/chat`. O workspace nao aparece na rota: e resolvido no servidor a
partir da sessao, exatamente como nas rotas de mensagens, DMs e categorias.

A rota compartilha o orcamento de leitura (`msgListRateLimit`, 30/min por
usuario), porque o painel refaz a consulta a cada troca de canal.

### Resposta `200`

```json
{
  "data": {
    "id": "11111111-1111-4111-8111-111111111111",
    "slug": "infraestrutura",
    "display_name": "Infraestrutura",
    "type": "private",
    "created_at": "2024-01-12T09:30:00Z",
    "member_count": 12,
    "online_member_count": 5,
    "online_members": [
      {
        "user_id": "22222222-2222-4222-8222-222222222222",
        "display_name": "Alvaro Neto",
        "avatar_url": "/media/avatars/alvaro.png",
        "role": "moderator",
        "presence": "online"
      }
    ]
  }
}
```

Campos:

- `type` e o valor de dominio `chat.channels.type` (`public` | `private`). O
  cliente nunca deve inferir visibilidade pelo nome do canal.
- `created_at` e RFC3339 em UTC.

As tres grandezas de membros respondem perguntas diferentes e **nenhuma deriva
da outra**:

- `member_count` -- todos os membros ativos do canal, online ou nao. E o tamanho
  do canal e nao muda quando alguem desconecta.
- `online_member_count` -- quantos desses estao online agora. Pode ser **maior**
  que `online_members.length` quando ha mais gente online do que a previa cabe.
- `online_members` -- a previa em si, limitada a `domain.MaxChannelDetailsMembers`
  (30, constante do servidor; o cliente nao escolhe o limite).

**Nunca** use `online_members.length` como nenhuma das duas contagens.

A lista se chama `online_members`, e nao `members`, porque nao e um roster: ela
e filtrada por presenca **antes** do limite (ver "Consultas"), entao um membro
offline nunca ocupa uma vaga da previa e um membro online nunca perde a vaga
para um offline. Um cliente que precise da lista completa de membros do canal
precisa de um contrato proprio -- este nao serve para isso.

Campos de cada entrada:

- `role` e o papel no canal (`member` | `moderator`) -- o unico atributo
  complementar que o dominio tem. Nao existe cargo/area em `auth.users`.
- `avatar_url` e omitido quando ausente.
- `presence` e sempre `online`: toda entrada de `online_members` esta online por
  construcao. O campo e enviado assim mesmo para que o cliente possa **verificar**
  a afirmacao em vez de deduzi-la do nome da lista.

Nao existe campo `description`: `chat.channels` nao tem coluna de descricao.
Quando o dominio ganhar uma, ela entra aqui e o estado vazio do painel deixa de
ser o unico resultado possivel.

Campos deliberadamente ausentes de `members[]`: e-mail, papel no workspace, data
de entrada, `auth_source` e qualquer outro atributo de perfil. Um painel de
detalhes nao e uma exportacao de diretorio.

### Presenca

A presenca vem do `PresenceTracker` que o hub WebSocket ja alimenta no mesmo
processo. O handler pede o conjunto de usuarios online do workspace em **uma
unica chamada em lote** (`OnlineUserIDs`) e o repassa a consulta como filtro --
nunca pergunta por membro, o que geraria N consultas ou, pior, filtragem depois
do limite.

Somente `PresenceOnline` conta. `PresenceAway` e um estado distinto por
definicao do dominio (`internal/ws/presence.go`: conexao ativa porem inativa
alem do timeout) e **nao** e tratado como online; `PresenceOffline` nao tem
entrada alguma no tracker. Nada e derivado de `last_seen` ou de atividade
indireta.

E estado por instancia: exato em deployment de instancia unica (todos os
overlays em `infra/k8s` rodam chat-service com `replicas: 1`) e, se isso mudar,
**sub**-reporta -- nunca super-reporta. Sem o tracker conectado, a previa volta
vazia: ausencia de presenca nunca e lida como "online".

### Erros

| Status | Codigo                | Quando                                                                                                                                  |
| ------ | --------------------- | --------------------------------------------------------------------------------------------------------------------------------------- |
| 400    | `bad_request`         | `channelID` nao e UUID valido                                                                                                           |
| 401    | `unauthorized`        | token ausente/invalido ou sessao inativa                                                                                                |
| 404    | `not_found`           | canal inexistente, arquivado, privado sem participacao, ou de outro workspace; tambem quando o chamador nao e membro ativo do workspace |
| 429    | `rate_limited`        | orcamento de leitura excedido                                                                                                           |
| 503    | `service_unavailable` | handler nao conectado                                                                                                                   |

Todos os casos de "nao pode ver" colapsam no mesmo `404`, entao a rota nao pode
ser usada para descobrir quais UUIDs de canal existem. A autorizacao e resolvida
**antes** de qualquer leitura de membros: um chamador negado nunca alcanca a
lista de participantes.

## Consultas

Duas, nesta ordem:

1. `GetVisibleChannelByID` -- o mesmo predicado de visibilidade usado pelo resto
   da superficie de canais (workspace ativo, participacao ativa no workspace,
   canal ativo, publico ou com `channel_members`).
2. `ListOnlineChannelMemberProfiles` -- as duas contagens + a previa limitada, em
   uma unica consulta.

A ordem dentro dessa consulta e o ponto do contrato:

```text
active_members  -> membros ativos do canal (predicado unico)
online_members  -> active_members INTERSECT snapshot de presenca   <- filtro
page            -> online_members ORDER BY lower(display_name), user_id LIMIT 30
```

O filtro de presenca esta na CTE `online_members`, portanto roda **antes** de
`ORDER BY` e `LIMIT`. Foi exatamente a inversao disso -- limitar por nome e so
depois olhar presenca -- que fazia um membro online fora dos 30 primeiros nomes
nunca aparecer.

O predicado de participacao vive num lugar so (`active_members`) e e o mesmo de
`SearchChannelMembers` (autocomplete de mencao), entao autocomplete, contagem
total e previa nao podem divergir sobre quem esta no canal. O filtro
`chat.channels.workspace_id` impede um UUID de canal de outro tenant de resolver
aqui, e o `user_id = ANY(...)` e uma **intersecao**: uma entrada de presenca de
quem nao e membro deste canal nao seleciona nada.

Sem N+1: uma consulta por requisicao, uma leitura de presenca por requisicao,
ordenacao deterministica (`lower(display_name)` com `user_id` como desempate) e
limite constante do servidor. O `LEFT JOIN LATERAL` sobre uma linha unica
garante que as contagens voltem mesmo quando ninguem esta online.

## Adicionar membros (issue #398)

| Metodo | Rota publica                             | Descricao              |
| ------ | ---------------------------------------- | ---------------------- |
| POST   | `/api/chat/channels/{channelID}/members` | adiciona participantes |

Exige `Authorization: Bearer <access-token>` e sessao ativa. O workspace nao
aparece na rota: e resolvido no servidor a partir da sessao, como nas demais.

### Autorizacao

`owner` ou `admin` ativo do workspace, via `domain.CanManageChannelMembers` --
que delega a `CanManageWorkspace`, o mesmo gate de update/archive de canal e de
categorias. **Nao** e "qualquer membro do canal": isso permitiria a quem apenas
le um canal privado ampliar a audiencia dele, que e exatamente a propriedade que
um canal privado tem. O papel `moderator` de `chat.channel_members` nao e
consultado porque nenhum caminho de codigo o atribui; a divergencia esta
registrada em `SECURITY.md`.

A autorizacao e verificada **antes** de o canal ser lido, entao um chamador sem
permissao nao descobre pela resposta se um UUID de canal existe.

O campo `can_manage_members` da resposta de `GET .../details` carrega a mesma
decisao para o painel decidir se mostra a acao. E uma dica de renderizacao, nunca
o controle: este endpoint reavalia a decisao a cada chamada. Ausente ou invalido
e lido como `false` pelo cliente.

### Corpo

```json
{ "user_ids": ["22222222-2222-4222-8222-222222222222"] }
```

`user_ids` e o **unico** campo aceito. `workspace_id`, `actor_id`, `role`,
`created_by`, `status` e qualquer afirmacao de elegibilidade sao derivados no
servidor e o decoder estrito responde `400` a um corpo que os carregue. O papel
do novo membro e sempre `member`.

### Limites

> **Canais e grupos nao possuem limite total fixo de participantes.** O endpoint
> aceita no maximo 25 IDs por requisicao por motivo operacional e de protecao
> contra abuso. Requisicoes sucessivas podem adicionar outros membros, sem teto
> de composicao. Nao existe estado "conversa cheia" e nenhuma resposta reporta
> capacidade esgotada.

- maximo de `domain.MaxAddMembersPerRequest` (25) IDs por requisicao, verificado
  na lista crua antes de qualquer parse. **E o tamanho de um lote HTTP, nao a
  capacidade da conversa**: exceder resulta em `400`, porque e uma propriedade
  da requisicao, nao do canal;
- lista vazia e recusada -- uma adicao que nao adiciona ninguem e erro de
  cliente, nao sucesso;
- IDs duplicados sao normalizados (canonicalizados e deduplicados), nao
  recusados;
- corpo limitado a 64 KiB pelo `maxBodyBytes` compartilhado;
- orcamento de 10 chamadas por usuario por 60s, **compartilhado com a rota de
  grupo** (acao `add_members`), para que trocar o tipo de conversa nao renda uma
  segunda cota.

### Semantica

**Inclusao imediata.** O dominio nao tem estado de convite pendente -- nao ha
tabela de convites e `chat.channel_members` nao tem coluna de status -- entao a
pessoa passa a participar no commit. "Convidar" e apenas linguagem de interface.

**Atomica.** Um unico `INSERT ... SELECT` decide elegibilidade e escreve na mesma
sentenca; se qualquer ID nao for elegivel, a transacao inteira e revertida e
ninguem e adicionado. Nao existe sucesso parcial.

**Idempotente.** A PK `(channel_id, user_id)` com `ON CONFLICT DO NOTHING` e o
arbitro: duplo clique, retry apos timeout e dois gestores adicionando a mesma
pessoa simultaneamente convergem em uma linha. Quem ja participava e contado em
`already_members`, nao tratado como erro.

**Autorizacao reavaliada na transacao.** O ator autenticado e passado ate o
store, que relê e bloqueia sua linha de `chat.workspace_members` exigindo
`owner`/`admin` ativo **dentro da mesma transacao que insere**. A verificacao no
service continua existindo apenas para recusar um chamador antes que a busca do
canal revele se um ID existe; ela nao e o controle. Um papel rebaixado, uma
membership suspensa ou removida entre as duas etapas resulta em `403`, sem
persistir nenhuma membership e sem publicar evento.

`#geral` e recusado: a participacao la e mantida pela sincronizacao de workspace.

### Resposta `200`

```json
{ "data": { "added": 2, "already_members": 1, "member_count": 14 } }
```

`member_count` e o total apos o commit, lido na mesma transacao. O cliente
atualiza o contador com esse valor em vez de incrementar um numero local, para
nao divergir de uma adicao concorrente.

### Erros

| Status | Codigo                | Quando                                                                                           |
| ------ | --------------------- | ------------------------------------------------------------------------------------------------ |
| 400    | `bad_request`         | `channelID` invalido, JSON malformado, campo desconhecido, lista vazia, ID nao-UUID, acima de 25 |
| 401    | `unauthorized`        | token ausente/invalido ou sessao inativa                                                         |
| 403    | `forbidden`           | chamador nao e owner/admin, **ou** algum usuario nao e elegivel                                  |
| 404    | `not_found`           | canal inexistente, arquivado, de outro workspace                                                 |
| 415    | `bad_request`         | content type diferente de `application/json`                                                     |
| 429    | `rate_limited`        | orcamento excedido                                                                               |
| 503    | `service_unavailable` | handler nao conectado, ou limitador indisponivel (fail-closed)                                   |

O `403` **nao** distingue "chamador sem permissao" de "usuario inelegivel", nem
diz qual usuario, nem se ele esta suspenso, deletado, em outro workspace ou nao
existe. Distinguir isso transformaria a rota em um oraculo de contas. Nenhuma
resposta carrega SQL, nome de constraint, ID de usuario ou o valor recusado.

### Busca contextual de candidatos

| Metodo | Rota publica                                       |
| ------ | -------------------------------------------------- |
| GET    | `/api/chat/channels/{channelID}/member-candidates` |

Parametros: `query` (2 a 64 caracteres) e `limit` opcional (padrao 20, maximo
50, sempre clampado no servidor). Nada mais e aceito — workspace e ator vem da
sessao, e um `workspace_id` na query string e simplesmente ignorado.

Autorizacao: a mesma de `POST .../members` (`owner`/`admin` ativo), verificada
**antes** de o canal ser lido. Isso e deliberado: a rota revela quem **nao** esta
num canal, o que e um fato sobre a composicao de um canal privado.

A resposta traz apenas `user_id` e `display_name`:

```json
{ "data": { "candidates": [{ "user_id": "...", "display_name": "Alvaro" }] } }
```

**Membros atuais sao excluidos por `NOT EXISTS` na propria consulta.** Isso
importa porque `online_members` do painel e uma previa _filtrada por presenca_:
um membro offline nao aparece nela. Usar essa lista para decidir elegibilidade
fazia um membro existente ser ofertado como selecionavel. O cliente nao envia
lista de membros e nao filtra por ela.

A busca e um snapshot: alguem pode entrar entre a pesquisa e a confirmacao.
`POST .../members` continua sendo a autoridade final e permanece idempotente —
uma corrida legitima resulta em `added: 0`, nao em erro.

### Evento em tempo real

Apos o commit -- e somente apos -- o hub publica `members.added` na sala do
canal:

```json
{
  "type": "members.added",
  "target_type": "channel",
  "target_id": "...",
  "members": { "actor_user_id": "...", "added_count": 2, "member_count": 14 }
}
```

O evento **nao nomeia ninguem**: quem pode ver um roster e decisao por leitor,
tomada pelo endpoint de detalhes, e nao por um broadcast enviado a todos os
inscritos de uma vez. O cliente o trata como "sua visao esta desatualizada" e
refaz o fetch, igual ao `pin.updated`.

#### `conversation.available` (user-scoped)

`members.added` vai para os **assinantes** do alvo — e quem acabou de ser
adicionado nao e assinante ainda, entao nao o receberia. Por isso, apos o commit,
o hub envia tambem:

```json
{ "type": "conversation.available", "target_type": "channel", "target_id": "..." }
```

diretamente as sessoes dos usuarios **efetivamente inseridos**, sem depender de
subscription alguma.

- Destinatarios vem do `RETURNING` da transacao (`AddedUserIDs`), nunca do
  payload da requisicao: um `user_ids` que nomeia alguem que ja era membro nao
  gera sinal para essa pessoa, e um retry com `added: 0` nao gera sinal nenhum.
- Entregue apenas a sessoes do mesmo workspace, e a todas as sessoes vivas do
  destinatario.
- Nao carrega nomes, e-mails, papeis, conteudo nem qualquer identidade de
  terceiros — em particular, nao nomeia as outras pessoas adicionadas na mesma
  operacao. O unico ID presente e `recipient_user_id`, o proprio destinatario:
  ele e a chave de roteamento entre instancias, e nomear o destinatario para o
  destinatario nao revela nada. E um evento por destinatario, exatamente para
  que nenhum deles aprenda sobre os outros.
- **Nao concede acesso**: o cliente reage recarregando `GET /api/chat/sidebar`,
  que reavalia a participacao no servidor.
- Uma falha de publicacao pos-commit nao desfaz a membership; a sidebar fica
  correta no proximo carregamento.

E por isso que nao ha duplicacao entre HTTP e WebSocket: **ha um unico caminho de
reconciliacao**, o refetch, que substitui a secao inteira em vez de anexar. A
resposta HTTP e o evento chegando juntos custam um fetch a mais e nada mais.
Nenhum evento e publicado quando a operacao falha ou quando ninguem foi
adicionado.

#### Entrega entre instancias

Os dois eventos atravessam o `BroadcastBus`, porque a sessao interessada
raramente esta no pod que executou a escrita. Cada um mantem seu escopo:

|                              | `members.added`                     | `conversation.available`          |
| ---------------------------- | ----------------------------------- | --------------------------------- |
| Escopo                       | alvo (sala)                         | usuario                           |
| Roteamento remoto            | assinantes do `(workspace, target)` | `recipient_user_id`               |
| Exige subscription           | sim                                 | nao                               |
| Reautorizacao no no receptor | por assinante, no fan-out           | do destinatario, antes da entrega |

O barramento e uma fronteira de confianca: um envelope que chega por ele nomeia
o proprio escopo, e nada disso foi decidido por quem o recebe. Por isso:

- **Canonicalizacao estrita.** Tipo, workspace, alvo, tipo de alvo, `event_id` e
  `source_instance_id` sao validados; UUIDs sao normalizados; campos de outros
  tipos de evento sao descartados. Envelope invalido e derrubado sem entrega.
- **Reautorizacao.** `members.added` reusa o caminho normal de broadcast, que ja
  reavalia o acesso de cada assinante no fan-out. `conversation.available` e
  reautorizado contra o alvo antes de chegar a qualquer sessao — a API da
  sidebar nao e a unica barreira. Negacao, erro e timeout **falham fechado**.
- **Sem eco e sem republicacao.** `source_instance_id` descarta a copia que
  volta para quem publicou, e um evento recebido do barramento nunca e
  republicado nele.

Uma falha de publicacao no barramento e best-effort, como nos demais eventos:
custa uma visao desatualizada ate o proximo refetch, nunca a membership que ja
foi commitada.
