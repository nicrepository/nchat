# Chat-service: detalhes do grupo (issue #441)

Projecao somente leitura de uma conversa em grupo ad-hoc, usada pelo painel
lateral **Detalhes do grupo**.

## Por que nao e a rota de canais

Um grupo e uma linha de `chat.dm_conversations` com `type = 'group'` -- o mesmo
agregado das DMs, nao o de `chat.channels`. Reaproveitar
`/api/chat/channels/{id}/details` passando um `conversationID` nomearia o
agregado errado, e um prefixo `/groups/` inventaria um recurso que o dominio nao
tem. A rota vive portanto sob o prefixo de DM, ao lado de `messages`, `pins` e
`message-references` da mesma conversa.

## Contrato

| Metodo | Rota publica                            | Descricao         |
| ------ | --------------------------------------- | ----------------- |
| GET    | `/api/chat/dm/{conversationID}/details` | detalhes do grupo |

Exige `Authorization: Bearer <access-token>` e sessao ativa. O workspace nao
aparece na rota: e resolvido no servidor a partir da sessao. Compartilha o
orcamento de leitura (`msgListRateLimit`, 30/min por usuario).

### Resposta `200`

```json
{
  "data": {
    "id": "44444444-4444-4444-8444-444444444444",
    "type": "group",
    "name": "Time de Infra",
    "created_at": "2024-03-04T15:00:00Z",
    "participant_count": 12,
    "participants": [
      {
        "user_id": "22222222-2222-4222-8222-222222222222",
        "display_name": "Alvaro Neto",
        "avatar_url": "/media/avatars/alvaro.png",
        "presence": "online"
      }
    ]
  }
}
```

Campos:

- `name` e `chat.dm_conversations.title`. Pode vir vazio -- um grupo ad-hoc nao
  exige titulo -- e o cliente mostra um rotulo neutro nesse caso.
- `created_at` e RFC3339 em UTC.
- `participant_count` e o total de participantes ativos, via `COUNT(*) OVER ()`
  na mesma consulta da pagina. **Nunca** use `participants.length` como total:
  `participants` e uma previa limitada a `domain.MaxDMDetailsParticipants` (30),
  e o cliente nao escolhe o limite.
- `participants[].presence` (`online` | `offline`) e **decoracao**, nao filtro.
  Ao contrario do painel de canal -- cuja lista e definida como "os membros
  online" -- um grupo lista **todos** os participantes ativos, e estar offline
  nunca remove ninguem da lista. O campo e omitido quando o servidor nao
  rastreia presenca, para o cliente distinguir "nao rastreado" de "offline".
- `participants[].avatar_url` e omitido quando ausente.

Deliberadamente **ausentes**, porque um grupo nao e um canal: visibilidade
(`public`/`private`), `slug`, categoria e descricao. O dominio nao tem nenhum
deles para conversas e nenhum e inventado aqui.

Tambem ausente: `role`. `chat.dm_members.role` e fechado por CHECK ao unico
valor `'member'`, entao um grupo nao tem papel a exibir. E-mail, papel no
workspace e data de entrada nunca sao serializados.

### Erros

| Status | Codigo                | Quando                                                                                                           |
| ------ | --------------------- | ---------------------------------------------------------------------------------------------------------------- |
| 400    | `bad_request`         | `conversationID` nao e UUID valido                                                                               |
| 401    | `unauthorized`        | token ausente/invalido ou sessao inativa                                                                         |
| 404    | `not_found`           | conversa inexistente, arquivada, de outro workspace, sem participacao ativa do chamador, **ou do tipo `direct`** |
| 429    | `rate_limited`        | orcamento de leitura excedido                                                                                    |
| 503    | `service_unavailable` | handler nao conectado                                                                                            |

Todos os casos colapsam no mesmo `404`, entao a rota nao pode ser usada para
descobrir quais UUIDs de conversa existem, nem para distinguir um grupo de uma
DM 1:1 que o chamador nao ve.

DM 1:1 esta **fora do escopo** da issue #441 e e recusada mesmo quando o
chamador participa dela: o tipo e verificado contra a linha que o banco
devolveu, nunca contra algo que o cliente afirmou.

## Consultas

Duas, nesta ordem:

1. `GetVisibleConversationByID` -- o mesmo predicado de acesso usado pelo resto
   da superficie de DM (workspace ativo, participacao ativa no workspace,
   conversa ativa, `dm_members` ativo do chamador).
2. `ListParticipantProfiles` -- pagina limitada + total, em uma unica consulta,
   com `dm.status = 'active'` (quem saiu do grupo desaparece), `dc.workspace_id`
   (isolamento de tenant), `dm.conversation_id` (isolamento entre grupos) e o
   join em `auth.users` ativo/nao deletado.

A presenca e lida **uma vez por requisicao**, em lote (`OnlineUserIDs`), e so
anota as linhas que a consulta ja selecionou -- nunca uma consulta por
participante. Ordenacao deterministica: `lower(u.display_name)` com `u.id` como
desempate.

## Arquivos do grupo

Ficam em file-service, na rota de anexos da conversa
(`GET /api/files/dm/{conversationID}/attachments`), documentada em
[file-attachments.md](./file-attachments.md). A autorizacao e a mesma do upload
em DM; um `channelID` nunca resolve na rota de conversa e vice-versa.
