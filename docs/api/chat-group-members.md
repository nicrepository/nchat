# Chat-service: adicionar participantes a um grupo (issue #398)

Inclusao de membros ativos do workspace em uma conversa em grupo ja existente.

## Por que nao e a rota de canais

Um grupo e uma linha de `chat.dm_conversations` com `type = 'group'` -- o mesmo
agregado das DMs, nao o de `chat.channels`. Apontar um `conversationID` para
`/api/chat/channels/{id}/members` nomearia o agregado errado, e um prefixo
`/groups/` inventaria um recurso que o dominio nao tem. A rota vive sob o prefixo
de DM, ao lado de `messages` e `pins` da mesma conversa.

## Contrato

| Metodo | Rota publica                            | Descricao              |
| ------ | --------------------------------------- | ---------------------- |
| POST   | `/api/chat/dm/{conversationID}/members` | adiciona participantes |

Corpo, limites, formato de resposta e mapa de erros sao **identicos** aos da rota
de canal, documentados em [chat-channel-details.md](./chat-channel-details.md).
O orcamento de rate limit e o mesmo (acao `add_members`), de proposito: trocar o
tipo de conversa nao rende uma segunda cota para a mesma capacidade de abuso.

## Tipos de conversa suportados

Apenas `type = 'group'`.

**DM 1:1 e recusada com `404`**, mesmo quando o chamador participa dela. Adicionar
uma terceira pessoa converteria silenciosamente a conversa em grupo, e essa
conversao esta fora do escopo: a unicidade de DM direta e chaveada no par nao
ordenado, entao a linha ficaria com um `direct_pair_key` descrevendo uma conversa
que nao e mais um par. O tipo e verificado contra a linha que o banco devolveu,
nunca contra algo que o cliente afirmou.

Nao existe "grupo publico" e "grupo privado": `chat.dm_conversations` nao tem
coluna de visibilidade, e nenhuma foi criada aqui.

## Autorizacao

**Qualquer participante ativo da conversa.**

Nao ha gestor a exigir: `chat.dm_members.role` e fechado por CHECK ao unico valor
`'member'`, entao um grupo nao tem owner, admin ou moderador. As alternativas
foram avaliadas e recusadas:

- `CanManageWorkspace` (owner/admin) seria **errado e mais permissivo, nao menos**:
  um admin de workspace nao e participante, nao enxerga a conversa pela politica
  SQL de DM, e dar-lhe autoridade sobre uma conversa privada entre pares que ele
  nao pode ler seria escalacao de privilegio, nao controle;
- `created_by` congelaria o grupo quando o criador sai, e essa coluna nunca e
  usada para autorizacao em lugar nenhum deste servico.

Participar ja permite criar um grupo novo com qualquer pessoa do workspace, entao
"participante pode adicionar participante" nao concede poder que ja nao exista.
A divergencia em relacao a redacao generica da issue esta registrada em
`SECURITY.md`.

O acesso e resolvido pelo mesmo predicado `GetVisibleConversationByID` que o
resto da superficie de DM usa -- workspace ativo, participacao ativa no
workspace, conversa ativa e linha `dm_members` ativa do chamador. Quem nao
participa nao distingue um grupo do qual foi excluido de um que nao existe: ambos
sao o mesmo `404`.

## Sem limite total de participantes

**Grupos nao possuem limite total fixo de participantes.** Um grupo pode crescer
indefinidamente por requisicoes sucessivas; nao existe capacidade maxima, estado
"grupo cheio", nem resposta `409` de capacidade.

O unico bound e o da requisicao: no maximo 25 IDs por chamada
(`domain.MaxAddMembersPerRequest`), por motivo operacional e de protecao contra
abuso. Exceder isso e `400`, porque e uma propriedade do payload e nao do grupo.

A transacao mantem `SELECT ... FOR SHARE` sobre a conversa e sobre a
participacao do ator, mas apenas para fixar o **contexto de autorizacao**
enquanto a escrita acontece — arquivar a conversa ou remover o ator sao UPDATEs
que conflitam com `FOR SHARE`. Como nao ha teto a serializar, duas pessoas
adicionando usuarios diferentes ao mesmo grupo grande prosseguem em paralelo.

## Semantica

Inclusao imediata e atomica, como na rota de canal. A escrita reutiliza
`upsertEligibleDMMembers`, a mesma sentenca por onde a criacao de grupo passa,
entao os dois caminhos obedecem a uma unica regra sobre quem pode participar.
Seu `ON CONFLICT DO UPDATE` reativa quem havia saido (`status = 'left'`), que e o
comportamento desejado: a pessoa volta e nenhuma segunda linha aparece.

## Busca contextual de candidatos

`GET /api/chat/dm/{conversationID}/member-candidates`, com os mesmos parametros,
limites e formato de resposta da rota de canal.

Autorizacao: participacao ativa, a mesma da escrita. DM 1:1 responde `404`.

**Participantes atuais sao excluidos por `NOT EXISTS` na consulta.** Aqui o
motivo e ainda mais direto que no canal: `participants` do painel e limitado a
`domain.MaxDMDetailsParticipants` (30), entao num grupo maior todo mundo a partir
do 31º era invisivel para a exclusao no cliente e voltava como selecionavel.

Apenas `status = 'active'` exclui alguem. Quem saiu do grupo **e** ofertado
novamente — adiciona-lo reativa a linha, que e a semantica ja existente do
dominio para um retorno.

## Evento em tempo real

`members.added` com `target_type: "dm"`, publicado apenas apos o commit e apenas
quando alguem foi de fato adicionado. Payload e regra de reconciliacao identicos
aos da rota de canal.

Alem dele, `conversation.available` e enviado **diretamente as sessoes dos novos
participantes** — que ainda nao assinam a conversa e portanto nao receberiam o
evento acima. Destinatarios, payload e garantias sao os documentados em
[chat-channel-details.md](./chat-channel-details.md).

## Painel de detalhes do grupo

A leitura que alimenta o painel e
`GET /api/chat/dm/{conversationID}/details`, com o mesmo contrato ja definido
para a issue #441:

```json
{
  "data": {
    "id": "...",
    "type": "group",
    "name": "Time de Infra",
    "created_at": "2024-03-04T15:00:00Z",
    "participant_count": 12,
    "participants": [{ "user_id": "...", "display_name": "Alvaro", "presence": "offline" }],
    "can_manage_members": true
  }
}
```

- `participant_count` e o total de participantes ativos; `participants` e uma
  previa limitada a `domain.MaxDMDetailsParticipants` (30) e seu tamanho **nunca**
  deve ser exibido como contagem.
- `presence` e **decoracao, nao filtro**: um grupo lista todos os participantes
  ativos e estar offline nunca remove ninguem. O campo e omitido quando o
  servidor nao rastreia presenca, para o cliente distinguir "nao rastreado" de
  "offline".
- `can_manage_members` e sempre `true` quando o payload existe, e isso nao e
  atalho: a politica **e** participacao ativa, e receber o payload ja significa
  ser participante ativo. O campo existe para o painel ler uma forma so em
  canais e grupos, e continua sendo dica de renderizacao — a rota de escrita
  reavalia a decisao dentro da propria transacao.
- Ausentes de proposito, porque um grupo nao e um canal: visibilidade
  (`public`/`private`), `slug`, categoria e descricao. `role` tambem: o CHECK
  fecha `chat.dm_members.role` em `'member'`.

Erros: `400` ID invalido; `401` sem sessao; `404` conversa inexistente,
arquivada, de outro workspace, sem participacao ativa **ou do tipo `direct`** —
todos colapsam na mesma resposta, entao a rota nao serve para descobrir quais
UUIDs existem nem para distinguir um grupo de uma DM 1:1 que o chamador nao ve.

No frontend, `GroupDetailsPanel` renderiza esse payload e integra a acao
**Adicionar membros**, que abre o mesmo `AddMembersDialog` usado por canais. DM
1:1 nao tem painel nem acao.
