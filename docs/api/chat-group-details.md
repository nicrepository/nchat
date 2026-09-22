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
    "description": "O grupo que cuida da malha.",
    "creator_display_name": "Alvaro Neto",
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
- `description` e `chat.dm_conversations.description` (issue #894). **Omitido**
  quando a coluna e NULL ou vazia -- ausencia e o contrato para "nao ha
  descricao", e um grupo criado antes da migration 000053 nunca tem uma. Texto
  simples, renderizado pelo cliente como text node.
- `creator_display_name` e o nome de apresentacao de
  `chat.dm_conversations.created_by`, resolvido no servidor (issue #894).
  **Omitido** quando nao ha identidade historicamente confiavel: conta apagada ou
  desativada, ou quem criou o grupo nao e mais membro ativo do workspace. O
  cliente mostra um estado neutro nesse caso, nunca um identificador.
- `created_at` e RFC3339 em UTC, e vem do agregado -- nunca da primeira mensagem
  nem da membership.
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
(`public`/`private`), `slug` e categoria. O dominio nao tem nenhum deles para
conversas e nenhum e inventado aqui. Descricao, por outro lado, existe desde a
issue #894 e pertence a conversa -- a mesma coluna, a mesma semantica de
ausencia e o mesmo limite do lado dos canais.

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

## Descricao e criador (issue #894)

`chat.dm_conversations.description` existe desde a migration 000053: coluna
`TEXT` nullable, sem backfill, com CHECK de
`domain.MaxConversationDescriptionCodePoints` (500) code points, validado em 000054. Uma linha `direct` herda a coluna porque e a mesma tabela; nada a le para
uma DM 1:1, cujo painel e o perfil da outra pessoa.

**Nao existe rota de escrita de descricao nesta entrega.** A issue #894 implementa
persistencia, leitura e apresentacao; a mutation de edicao pertence a uma issue
propria. A leitura destes dois campos usa exatamente a autorizacao do
`GET .../details` descrita acima -- um chamador que recebe `404` nao aprende nem
que a conversa existe, nem que ela tem descricao, e uma conversa `direct` e
recusada antes de a descricao ser lida.

O criador e resolvido na mesma consulta que le a descricao, por LEFT JOIN em
`chat.workspace_members` + `auth.users`, com o mesmo
`COALESCE(full_name, display_name)` do resto do dominio. Nao ha rota `/creator`,
nem busca de perfil depois do details, nem consulta por campo: o painel faz
**uma** requisicao HTTP e o handler passou de duas para tres consultas SQL, todas
de custo constante. O UUID do criador nao e serializado.

`participant_count` permanece a unica fonte da contagem exibida no bloco
`SOBRE`, derivada da mesma consulta autoritativa dos participantes -- nunca de
`participants.length` e nunca de um contador mantido no cliente.

## Consultas

Tres, nesta ordem:

1. `GetVisibleConversationByID` -- o mesmo predicado de acesso usado pelo resto
   da superficie de DM (workspace ativo, participacao ativa no workspace,
   conversa ativa, `dm_members` ativo do chamador).
2. `ListParticipantProfiles` -- pagina limitada + total, em uma unica consulta,
   com `dm.status = 'active'` (quem saiu do grupo desaparece), `dc.workspace_id`
   (isolamento de tenant), `dm.conversation_id` (isolamento entre grupos) e o
   join em `auth.users` ativo/nao deletado.
3. `GetConversationAbout` -- descricao + nome do criador, em uma unica consulta,
   depois do gate. O filtro `workspace_id` ali e isolamento em profundidade, nao
   a permissao: quem decide o acesso e a etapa 1.

A presenca e lida **uma vez por requisicao**, em lote (`OnlineUserIDs`), e so
anota as linhas que a consulta ja selecionou -- nunca uma consulta por
participante. Ordenacao deterministica: `lower(u.display_name)` com `u.id` como
desempate.

## Arquivos do grupo

Ficam em file-service, na rota de anexos da conversa
(`GET /api/files/dm/{conversationID}/attachments`), documentada em
[file-attachments.md](./file-attachments.md). A autorizacao e a mesma do upload
em DM; um `channelID` nunca resolve na rota de conversa e vice-versa.

## Remover um participante (issue #469, backend #685)

| Metodo | Rota publica                                          | Descricao              |
| ------ | ----------------------------------------------------- | ---------------------- |
| DELETE | `/api/chat/dm/{conversationID}/participants/{userID}` | remove um participante |

Prefixo proprio, e nao `/members/{userID}`, para deixar claro que a rota nomeia
um alvo: `DELETE /api/chat/dm/{conversationID}/membership` continua sendo a
saida do proprio chamador.

**A autoridade e a criacao do grupo, nao a participacao.** Um grupo nao tem
papel a consultar -- `chat.dm_members.role` e fechado por CHECK ao valor
`'member'` --, entao o criador (`chat.dm_conversations.created_by`) e a unica
autoridade mais estreita que "qualquer participante". Isto **difere** da adicao,
que qualquer participante ativo pode fazer
([chat-group-members.md](./chat-group-members.md)): as duas capacidades nao
podem ser deduzidas uma da outra. A creatorship e re-derivada dentro da
transacao, sobre a linha da conversa travada (`FOR SHARE`).

- **Sem corpo.** Workspace e ator vem da sessao; a rota carrega apenas a
  conversa e o alvo.
- **Auto-remocao e recusada** com `400`: sair e `DELETE .../membership`.
- **Idempotente:** um alvo que ja nao participa responde `204` e nao publica
  evento algum.
- **Transacional:** o `UPDATE chat.dm_members SET status = 'left'` e o evento
  `conversation_member_removed` sao a mesma transacao, e o `conversation.event`
  so e publicado apos o commit. Ordem de locks: conversa, participacao e
  membership do ator, participacao do alvo.
- **Resposta:** `204` sem corpo; o cliente reconcilia pelo refetch de
  `GET .../details`.

| Status | Codigo                | Quando                                                     |
| ------ | --------------------- | ---------------------------------------------------------- |
| 204    | --                    | removido, ou o alvo ja nao participava                     |
| 400    | `bad_request`         | ID invalido, ou o alvo e o proprio chamador                |
| 401    | `unauthorized`        | token ausente/invalido ou sessao inativa                   |
| 403    | `forbidden`           | chamador nao e o criador, ou nao participa mais            |
| 404    | `not_found`           | conversa inexistente, arquivada, de outro workspace ou 1:1 |
| 429    | `rate_limited`        | orcamento de administracao de grupo excedido               |
| 503    | `service_unavailable` | handler nao conectado                                      |

### `can_remove_members` em `GET .../details`

Campo booleano, sempre enviado: `true` somente para o criador do grupo. E o
unico jeito de o painel saber que a acao existe sem reconstruir a regra, e e
deliberadamente diferente de `can_manage_members` -- que num grupo e `true` para
todo participante. Dica de renderizacao apenas: o DELETE reavalia a decisao.
