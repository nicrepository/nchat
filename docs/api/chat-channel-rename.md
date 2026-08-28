# Chat-service: renomear canal (issue #527)

Altera apenas o `display_name` de um canal existente. Usado pelo item
**Renomear canal** do menu de acoes da sidebar.

> Grupos e DMs 1:1 nao tem esta rota e nao devem ser renomeados por ela: um
> grupo e uma linha de `chat.dm_conversations`, nao um canal.

## Contrato

| Metodo | Rota publica                     | Descricao        |
| ------ | -------------------------------- | ---------------- |
| PATCH  | `/api/chat/channels/{channelID}` | renomeia o canal |

Exige `Authorization: Bearer <access-token>`, sessao ativa e
`Content-Type: application/json`, como as demais rotas de `/api/chat`. O
workspace **nao aparece na rota**: e resolvido no servidor a partir da sessao,
exatamente como em `/details`, `/members` e nas categorias.

A rota tem orcamento proprio de escrita (`channel_update`, 20/min por usuario),
aplicado dentro do handler pelo limitador compartilhado — portanto por usuario,
nao por replica.

### Corpo aceito

```json
{ "display_name": "Plataforma" }
```

Um unico campo, e as ausencias sao o contrato. Nao ha `workspace_id`, `slug`,
`type`, `category_id`, `position`, papel ou ator: o decodificador e estrito
(`DisallowUnknownFields`) e responde `400` a qualquer corpo que carregue um
deles. Nao existe payload pelo qual um cliente possa reivindicar um privilegio,
apontar a escrita para outro workspace, ou transformar uma renomeacao em mudanca
de visibilidade.

### Resposta `200`

```json
{
  "data": {
    "id": "11111111-1111-4111-8111-111111111111",
    "display_name": "Plataforma"
  }
}
```

O `id` volta inalterado porque e exatamente essa a garantia: renomear **nao**
cria canal novo, nao recria membership, nao mexe em pins, unread, historico,
permissoes, workspace nem `type`. O `slug` tambem nao muda — a rota nao o aceita.

## Autorizacao

`domain.CanManageWorkspace` — **owner ou admin** com membership ativa em
workspace ativo. E o mesmo predicado que ja governa `UpdateChannel` e o
arquivamento de canal, alcancado pelo mesmo `requireManagePermission`.

O `moderator` de workspace (RF-74) **nao** renomeia. Isso e deliberado e esta
documentado em `domain.CanManageWorkspace`: um moderador modera estrutura e
membership de canal; mudar o que um canal _e_ continua sendo administracao.
Renomear pela sidebar nao e caminho para ampliar essa autoridade.

O servidor e a autoridade. Nada e decidido a partir de papel, capability,
`channel.type` ou workspace enviados pelo cliente, nem do fato de a opcao estar
escondida na UI.

### Ordem das verificacoes

1. `channelID` precisa ser UUID valido (`400` caso contrario);
2. sessao/ator autenticados (`401`);
3. orcamento de escrita (`429` com `Retry-After`);
4. `Content-Type` JSON e corpo estrito (`400`);
5. nome normalizado por `domain.NormalizeChannelDisplayName` (`400`);
6. autorizacao de gestao do workspace (`403`);
7. canal existente, ativo e **daquele** workspace (`404`);
8. `is_general = false`, tambem reafirmado no `WHERE` do UPDATE (`400`).

Como a autorizacao roda antes de o canal ser lido, nem um nao-administrador nem
um administrador de outro workspace conseguem usar o status para descobrir se um
UUID existe: canal de outro workspace, canal arquivado e canal inexistente
respondem todos `404`.

## Validacao do nome

Uma unica regra, a mesma da criacao: `domain.NormalizeChannelDisplayName`.

- `TrimSpace` aplicado antes de persistir;
- vazio ou so espaco em branco → `ErrChannelDisplayNameRequired`;
- maximo de 100 **code points** Unicode
  (`domain.MaxChannelDisplayNameCodePoints`), contados como
  `utf8.RuneCountInString` e como `char_length` no PostgreSQL — entao 100 emojis
  passam e 101 nao;
- nunca trunca: armazenar algo diferente do que o cliente enviou seria pior do
  que recusar.

Nao ha unicidade de `display_name`: a unica constraint UNIQUE de
`chat.channels` e `(workspace_id, slug)`, e o slug nao e tocado aqui. Duas
renomeacoes concorrentes sao um UPDATE cada, na mesma linha — a ultima vence,
nao ha lock adicional, e nao existe caminho por onde o mesmo `channel_id` ganhe
duas identidades. O cliente reconcilia com o estado persistido pelo refetch.

O erro nunca repete o valor recusado nem a mensagem do banco: um nome recusado e
texto controlado pelo chamador, que de outro modo chegaria a todo corpo de erro
e a toda linha de log.

## Erros

| Status | Quando                                                                             |
| ------ | ---------------------------------------------------------------------------------- |
| `400`  | UUID malformado, corpo invalido ou com campo desconhecido, nome invalido, `#geral` |
| `401`  | sem sessao utilizavel                                                              |
| `403`  | membership ativa sem `CanManageWorkspace` (member, guest, moderator)               |
| `404`  | canal inexistente, arquivado ou de outro workspace                                 |
| `429`  | orcamento de escrita excedido (`Retry-After: 60`)                                  |

## Capability na sidebar

`GET /api/chat/sidebar` passa a publicar `can_rename` por canal:

```json
{ "id": "…", "display_name": "Infra", "can_write": true, "can_rename": true }
```

E `domain.CanManageWorkspace(member) && !channel.is_general`, derivado da mesma
membership que a resposta ja carregava — nao uma segunda regra que possa
divergir. Serve **apenas** para o menu da sidebar omitir uma acao que o servidor
recusaria; o PATCH re-deriva a decisao a cada chamada, e um cliente que ignore o
campo recebe `403`, nao uma renomeacao.

O campo e sempre serializado. Um cliente que antecede o campo le a ausencia como
"nao", o que esconde uma acao — a falha segura — em vez de oferecer uma que
retorna `403`.

## Realtime

Apos o commit, o hub publica `channel.updated` para os assinantes do canal:

```json
{
  "schema_version": 1,
  "type": "channel.updated",
  "workspace_id": "…",
  "target_type": "channel",
  "target_id": "…",
  "event_id": "…",
  "created_at": "…"
}
```

Sem payload — nem o nome novo. E um sinal de invalidacao, como `pin.updated` e
`members.added`: quem recebe refaz `GET /api/chat/sidebar`, que re-autoriza por
si mesmo. Isso e o que torna o evento idempotente de graca (uma repeticao custa
um refetch a mais e nunca duplica uma linha) e o que impede que o nome de um
canal privado viaje por um broadcast.

Roteamento por `(workspace, target)`, com re-autorizacao de cada assinante no
fan-out — igual a todos os demais eventos de sala. O evento nunca amplia
subscription alguma. Um evento recusado no `canonicalizeRemoteEvent` (envelope
malformado, `message_id` presente, payload de outro tipo) e descartado.

## Auditoria

Chat-service nao possui trilha de auditoria para alteracoes administrativas de
canal, e esta issue nao criou uma. Quando existir, esta rota deve registrar
apenas ator, canal, nome anterior, nome novo, resultado, timestamp e
request/correlation ID — nunca conteudo de mensagem, `Authorization`, token,
cookie ou segredo.
