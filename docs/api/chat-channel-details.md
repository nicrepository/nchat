# Chat-service: detalhes do canal (issue #435)

Projecao somente leitura de um canal, usada pelo painel lateral **Detalhes do
canal** na area de conversa.

> Grupos ad-hoc tem contrato proprio, em
> [chat-group-details.md](./chat-group-details.md). Um grupo nao e um canal e
> nao deve ser consultado por esta rota.

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
