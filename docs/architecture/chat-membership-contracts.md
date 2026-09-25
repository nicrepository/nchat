# Chat-service: contratos de membership (issue #881)

Diagnostico das divergencias da issue #877 e definicao dos contratos semanticos
autoritativos que as issues #882-#888 vao implementar.

> **Esta pagina nao muda comportamento.** Ela nomeia as populacoes que o dominio
> ja possui, prova onde o codigo atual as confunde, e atribui cada correcao a
> sua issue filha. Nenhuma regra de autorizacao foi alterada pela #881; onde o
> texto descreve a politica vigente, ele descreve o que o codigo faz hoje.

**Como ler esta pagina.** Tudo aqui e **CURRENT** — o comportamento observado
hoje — salvo onde o texto diz explicitamente **TARGET**, que e sempre uma
decisao ainda nao tomada, sempre atribuida a uma issue filha e nunca
implementada por esta issue. CURRENT nao significa correto: a secao 4 lista as
divergencias comprovadas. As decisoes de TARGET ainda em aberto incluem
as opcoes A/B de 1.4 (owner: #883) e a consolidacao RF-18 x RF-74 da secao 5
(owner: #882); ambas ficam deliberadamente em aberto.

Duas confusoes que esta pagina existe para desfazer, e que nao devem ser lidas
de volta nela: **visibilidade/acesso nao e roster membership** (1.3 x 1.2), e
**guest nao entra em `#geral` automaticamente** hoje (5, RF-74).

Documentos irmaos, que continuam sendo a autoridade sobre suas proprias rotas:
[chat-channel-details.md](../api/chat-channel-details.md),
[chat-group-details.md](../api/chat-group-details.md),
[chat-group-members.md](../api/chat-group-members.md),
[chat-domain-model.md](./chat-domain-model.md),
[rbac-matrix.md](../security/rbac-matrix.md).

---

## 1. Conceitos de membership

O defeito de fundo da #877 e um so: **"membro do canal" nomeia hoje duas
populacoes diferentes**, e superficies distintas escolhem uma ou outra sem que o
contrato diga qual. Os conceitos abaixo sao disjuntos e nomeados uma unica vez.

### 1.1 Workspace membership

`chat.workspace_members`. Linha `(workspace_id, user_id, role, status)`.
Autoridade sobre `role` (RF-74: owner, admin, moderator, member, guest) e sobre
`status` (active, suspended, left). Nenhuma outra tabela decide papel.

### 1.2 Explicit channel membership

`chat.channel_members`. Linha `(channel_id, user_id, role)`. **Nao possui coluna
de status**: a linha existe ou nao existe. E um fato deliberado — quem sai perde
a linha (`RemoveChannelMember`), e nao ha estado de convite pendente.

O papel `chat.channel_members.role` (`member` | `moderator`) e por canal e
**nunca** e lido como autoridade de workspace
(`domain.CanManageChannelMembers` consulta `chat.workspace_members.role`).

### 1.3 Channel visibility / access

`chat.channel_visible_to_user(channel_id, user_id)`, migration
`000022_workspace_moderator_and_guest_channel_scope.up.sql`. Definicao unica e
compartilhada por chat-service, file-service e media-service. Seu gemeo em Go e
`domain.CanReadChannel`, e os dois devem concordar exatamente.

```sql
cm.user_id IS NOT NULL                                   -- membership explicita
OR (wm.role IN ('owner','admin','moderator','member')     -- alcance de workspace
    AND c.type = 'public')
```

**Acesso nao e roster.** Um canal publico e legivel por todo papel que
`domain.CanReachPublicChannels` admite, com ou sem linha em
`chat.channel_members`. Um canal privado exige a linha, para todo papel — um
admin de workspace nao le um canal privado do qual nao participa.

### 1.4 Effective channel membership

**TARGET (#883, com a politica especial de `#geral` em #882).**

A populacao que as superficies de roster, contagem, mencao e elegibilidade
**deveriam** descrever. Ela nao e "`chat.channel_members`" e nao e
"`channel_visible_to_user`"; depende do tipo de canal:

| Canal            | Effective channel membership                                                           |
| ---------------- | -------------------------------------------------------------------------------------- |
| privado          | exatamente `chat.channel_members` (ativos no workspace, conta ativa)                   |
| `#geral`         | todo workspace member ativo com conta elegivel; guest incluido como excecao estrutural |
| publico (demais) | **indefinido hoje** — o codigo diz `chat.channel_members`, o acesso diz outra coisa    |

A ultima linha e a causa raiz da #877 e a decisao de politica pertence a
**#883**. As duas leituras possiveis sao:

- **A — roster implicito.** Effective = quem `channel_visible_to_user` admite.
  Alinha roster, contagem e mencao com quem realmente le o canal. Torna
  `member-candidates` num canal publico quase sempre vazio, e faz
  `POST .../members` num canal publico virar no-op semantico para papeis nao
  guest.
- **B — roster explicito.** Effective = `chat.channel_members`, e canal publico
  passa a materializar linhas (na criacao e/ou no primeiro acesso). Preserva o
  significado de "adicionar membro", ao custo de uma escrita de backfill e de
  decidir quem materializa.

Nenhuma das duas e escolhida aqui.

### 1.5 Presence

Estado transitorio de conexao, `internal/ws/presence.go`. Tres estados:
`online`, `away`, `offline`. **Presenca nunca concede membership** e nunca deve
ser fonte dela. Ausencia de presenca (tracker nao conectado, instancia que nao
conhece a conexao) nao e `offline` — e desconhecido, e o contrato correto e
sub-reportar, nunca inventar.

### 1.6 `online_members`

Previa de presenca do painel de detalhes de canal, limitada a
`domain.MaxChannelDetailsMembers` (30) e **filtrada por presenca antes do
limite**. Nunca e roster. O nome do campo e parte do contrato: um cliente que o
tratar como lista de membros esta errado, e a rota nao oferece roster completo.

### 1.7 `member_count`

**CURRENT:** o codigo conta `chat.channel_members`
(`ListOnlineChannelMemberProfiles`, CTE `active_members`). Na fixture, essa
populacao coincide com os leitores no privado apos adicao e em `#geral` apos
sync; nao no publico sem rows nem em `#geral` antes do sync.
**TARGET (#883):** alinhar a contagem a membership efetiva/roster de 1.4.

O bloco `SOBRE` do painel (issue #894) **consome** esta contagem e nao define
outra: nao ha contador local, nada e derivado de `online_members.length` nem da
previa de participantes, e a convergencia apos uma mudanca de membership
continua sendo o refetch existente. Quando #883 mudar a fonte, o painel muda com
ela sem alteracao propria.

### 1.8 Candidate eligibility

**CURRENT:** a rota exige a mesma capacidade de acesso do POST, revalidada no
SQL, e exclui o proprio chamador.

Os resultados sao `chat.workspace_members` ativos, menos
`chat.channel_members`, menos o chamador e contas inativas/deletadas. A exclusao
continua explicita; alinhar "membro efetivo" depende da decisao #883 (ver 1.4).

### 1.9 Mention eligibility

**TARGET (#887):** membership autoritativa definida por #883.
CURRENT: `chat.channel_members`, enquanto o **portao**
da rota de mencao e `PermissionService.CanRead` (acesso). Portao e fonte
discordam.

### 1.10 Realtime

`members.added` e **sinal de invalidacao**, nunca estado. Nao nomeia ninguem
(`MembersAddedPayload` carrega ator e duas contagens). O estado persistido e o
refetch autorizado continuam a autoridade. Um evento perdido custa uma visao
desatualizada ate o proximo refetch, nunca uma membership.

---

## 2. Mapa de fluxos ponta a ponta

### A. Channel details — `GET /api/chat/channels/{channelID}/details`

| Camada   | Arquivo / simbolo                                                                                                                         |
| -------- | ----------------------------------------------------------------------------------------------------------------------------------------- |
| rota     | `internal/http/routes.go` `RouteChannelDetails`; `router.go:430` (orcamento de leitura)                                                   |
| handler  | `internal/http/channel_handler.go` `(*ChannelHandler).Details`, `onlineUserIDs`, `channelDetailsBody`                                     |
| service  | `internal/service/channel_service.go` `(*ChannelService).GetChannelDetails`                                                               |
| store    | `internal/storage/member_store.go` `(*PGXMemberStore).ListOnlineChannelMemberProfiles`                                                    |
| SQL      | CTE `active_members` -> CTE `online_members` -> `page` (`ORDER BY` + `LIMIT`)                                                             |
| JSON     | `member_count`, `online_member_count`, `online_members[]`, `can_add_members`, `can_manage_members`, `description`, `creator_display_name` |
| chatApi  | `apps/web/src/chat/chatApi.ts` `fetchChannelDetails`                                                                                      |
| tipos    | `apps/web/src/chat/chatTypes.ts` `ChannelDetails`                                                                                         |
| hook     | `apps/web/src/chat/useConversationDetails.ts`                                                                                             |
| UI       | `apps/web/src/chat/ConversationDetailsPanel.tsx` `AboutMetadata`, `ChannelAboutSection`, `ChannelMembersSection`                          |
| contrato | `docs/api/chat-channel-details.md`                                                                                                        |

`description` e `creator_display_name` sao metadata do bloco `SOBRE`
(issue #894), lidos por `storage.(*PGXChannelStore).GetChannelAbout` depois do
gate. Nao pertencem a membership e nao participam de nenhuma contagem.

Ordem das decisoes: `requireActiveWorkspaceMember` -> `GetVisibleChannelByID`
(visibilidade, 404 uniforme) -> leitura de membros. Um chamador negado nunca
alcanca roster nem snapshot de presenca.

`CanManageMembers` = `!channel.IsGeneral && domain.CanManageChannelMembers(&member)`.

### B. Group baseline — `GET /api/chat/dm/{conversationID}/details`

| Camada   | Arquivo / simbolo                                                                                           |
| -------- | ----------------------------------------------------------------------------------------------------------- |
| rota     | `RouteDMDetails`                                                                                            |
| handler  | `internal/http/dm_handler.go` `(*DMHandler).GroupDetails`, `groupDetailsBody`                               |
| service  | `internal/service/dm_service.go` `(*DMService).GetGroupDetails`                                             |
| store    | `internal/storage/dm_store.go` `(*PGXDMStore).ListParticipantProfiles`                                      |
| SQL      | `chat.dm_members dm` (status `active`) + workspace ativo + conta ativa, `COUNT(*) OVER ()` antes do `LIMIT` |
| JSON     | `participant_count`, `participants[]` (com `presence` opcional)                                             |
| UI       | `GroupAboutSection`, `GroupParticipantsSection`                                                             |
| contrato | `docs/api/chat-group-details.md`, `docs/api/chat-group-members.md`                                          |

**Por que e a baseline saudavel:** `chat.dm_members` e simultaneamente a fonte
do acesso (`GetVisibleConversationByID`), do roster, da contagem e dos
candidatos de mencao. Uma unica populacao, um unico predicado. Presenca e
anotada **depois** da selecao (`groupDetailsBody` consulta
`OnlineUserIDs` e marca cada linha), portanto um participante offline nunca sai
da lista nem perde vaga.

### B2. Channel roster — `GET /api/chat/channels/{channelID}/members` (issue #469)

| Camada   | Arquivo / simbolo                                                                   |
| -------- | ----------------------------------------------------------------------------------- |
| rota     | `internal/http/routes.go` `RouteChannelMembers` (GET); orcamento de leitura         |
| handler  | `internal/http/channel_handler.go` `(*ChannelHandler).Members`, `channelRosterBody` |
| service  | `internal/service/channel_service.go` `(*ChannelService).ListChannelMembers`        |
| store    | `internal/storage/member_store.go` `(*PGXMemberStore).ListChannelMemberRoster`      |
| SQL      | CTE `active_members` (a mesma de B) -> `COUNT(*) OVER ()` + `ORDER BY` + `LIMIT`    |
| JSON     | `member_count`, `members[]` (`user_id`, `display_name`, `avatar_url`, `role`)       |
| UI       | `ConversationDetailsPanel.tsx` `channelRosterContent`, via `useConversationDetails` |
| contrato | `docs/api/chat-channel-details.md`                                                  |

**Populacao: 1.2, explicit channel membership** — exatamente a que
`RemoveChannelMemberByAdmin` apaga, e exatamente a que `member_count` (1.7)
conta. Nao ha predicado de presenca: e o mesmo `active_members` de B sem o
recorte de 1.6, porque uma remocao precisa alcancar quem esta offline.

Uma unica pagina de 30 por chamada, sem cursor, com o total real ao lado. A
navegacao da colecao inteira (compacto de cinco, `Ver todos`, lista
completa/paginada) pertence a **#895**, que consome esta rota; a #469 usa a
primeira pagina e diz na tela quanto dela tem.

Nao substitui `online_members` e nao altera a rota de detalhes: B continua
respondendo a todo leitor com a previa de presenca, e esta rota responde
**apenas** a quem pode mudar a membership — o gate e a propria politica de
remocao (`!IsGeneral && domain.CanManageChannelMembers`), verificada antes da
busca do canal, como em C. Um canal publico sem linhas explicitas responde
roster vazio: e a divergencia de 1.4, nao uma decisao desta rota, e o dia em que
#883 definir a membership efetiva esta rota a segue sem mudar de forma.

### C. Member candidates — `GET /api/chat/channels/{channelID}/member-candidates`

`channel_handler.go:701 MemberCandidates` -> `MemberService.SearchChannelMemberCandidates`
-> `PGXMemberStore.SearchChannelMemberCandidates` -> `chatApi.searchChannelMemberCandidates`
-> seletor do painel.

- **fonte:** `chat.workspace_members` ativos do workspace da sessao, com conta
  ativa e nao deletada;
- **elegibilidade:** prefixo de `display_name` (2..64 chars), exclui o proprio
  chamador; **nenhum filtro por papel** — guest aparece;
- **exclusao de membros atuais:** `NOT EXISTS` sobre `chat.channel_members` na
  mesma sentenca (nunca a previa do painel);
- **autorizacao:** `domain.CanAddChannelMembers`, a mesma capacidade do POST;
  a consulta revalida `chat.channel_visible_to_user` e a conta do chamador;
- **public/private/#geral:** segue o acesso canonico: membro alcanca canal
  publico implicitamente; privado e guest exigem membership explicita;
- **grupo:** `SearchGroupParticipantCandidates`, mesma forma, excluindo
  `chat.dm_members` ativos; a autorizacao e participacao ativa, nao papel.

### D. Add members — `POST /api/chat/channels/{channelID}/members`

`channel_handler.go:563 AddMembers` -> `MemberService.AddChannelMembers` ->
`PGXMemberStore.AddChannelMembers`.

| Propriedade        | Comportamento atual                                                                                                                                                                           |
| ------------------ | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| autorizacao        | `domain.CanAddChannelMembers` no service; acesso re-derivado na transacao por `chat.channel_visible_to_user`, com locks no canal e no estado ativo do ator                                    |
| elegibilidade      | `channelmembership.EligibleTargetsCTE` (lib compartilhada com admin-service): workspace ativo, membership ativa, canal ativo no mesmo workspace, conta ativa. **Guest e elegivel por design** |
| idempotencia       | PK `(channel_id, user_id)` + `ON CONFLICT DO NOTHING`; repeticao vira `already_members`                                                                                                       |
| atomicidade        | `eligible != len(userIDs)` -> rollback total; nao existe sucesso parcial                                                                                                                      |
| concorrencia       | `channelmembership.LockChannelSQL` (`FOR UPDATE` na linha do canal) como **primeira** sentenca                                                                                                |
| `#geral`           | reparo/no-op permitido; reutiliza a elegibilidade estrutural consolidada pela #882                                                                                                            |
| total retornado    | `member_count` lido **apos** o insert e **antes** do commit, dentro da mesma transacao                                                                                                        |
| eventos pos-commit | `members.added` (assinantes) + `conversation.available` (so para `AddedUserIDs`) + `conversation.event` quando houve `EventMessageID`                                                         |

Ordem canonica de locks: canal -> membership do ator -> linhas alvo -> mutacao
-> contagem.

### E. Mentions — `GET /api/chat/{channels|dm}/{id}/mentions`

`router.go:231` -> `MentionService.SearchMentions`:

- **canal:** portao `PermissionService.CanRead` (**acesso**); fonte
  `MemberService.SearchChannelMembers` -> `PGXMemberStore.SearchChannelMembers`
  -> `chat.channel_members` (**membership explicita**);
- **grupo:** portao `GetVisibleConversationByID` + `type = 'group'`; fonte
  `SearchDMConversationMembers` -> `chat.dm_members` ativos. Portao e fonte sao
  a mesma populacao;
- **canais mencionaveis:** `PermissionService.ListVisibleChannels` (acesso),
  o que e coerente — um canal e mencionavel por quem o ve;
- **frontend:** `chatApi.ts:1594` -> autocomplete do compositor.

### F. Presence

`ws.PresenceTracker` (`internal/ws/presence.go`) alimentado **apenas por
conexoes locais** (`hub.go:1216 h.presence.Connect`). `OnlineUserIDs` responde
somente `PresenceOnline` — `away` e um estado distinto e nao e dobrado em
online; `offline` nao tem entrada.

`ws.PresenceDirectory` (`presence_directory.go`, Valkey) e a resposta
**cluster-wide** para "quem esta presente neste alvo" e existe exatamente
porque uma instancia sozinha nao sabe responder isso. Os detalhes de canal
**nao** a consultam: `channel_handler.go:368` le o tracker local.

### G. Sidebar

`internal/http/sidebar_handler.go` -> `SidebarService` ->
`PGXChannelStore.ListVisibleChannelAccessByUser`, cuja clausula e
`chat.channel_visible_to_user(c.id, $2)`.

A sidebar reflete **acesso/visibilidade** e por construcao nao e roster. Ela
nao deve ser usada como fonte para candidates nem para mentions — e a linha
`LEFT JOIN chat.channel_members cm` que ela carrega existe para o papel do
proprio leitor, nao para compor uma lista de membros.

### G2. Remocao administrativa — `DELETE .../members/{userID}` e `DELETE /dm/{id}/participants/{userID}`

Issue #685 para as duas escritas, issue #469 para as superficies que as chamam.
Populacao afetada: 1.2 no canal (`DELETE FROM chat.channel_members`) e
`chat.dm_members` no grupo (`status = 'left'`). Autorizacao: canal por
`domain.CanManageChannelMembers`, separado da adicao por
`domain.CanAddChannelMembers`, mais a recusa de `#geral`; grupo por creatorship,
que **nao** e a politica da adicao (qualquer
participante). Por isso os detalhes de cada agregado carregam
`can_remove_members` separado de `can_manage_members`.

O ator nunca e alvo: as duas rotas recusam `callerID == targetUserID` com
`ErrInvalidInput`, porque sair da conversa e a rota de `membership`. Ambas sao
idempotentes, escrevem `conversation_member_removed` na mesma transacao e
publicam `conversation.event` apos o commit. Nenhum evento novo foi criado.

### H. Realtime — `members.added`

`ws/event.go:105 EventTypeMembersAdded` + `MembersAddedPayload` (ator,
`added_count`, `member_count`) -> `ws/hub.go:749 PublishMembersAdded` (fila de
broadcast local com reautorizacao por assinante no fan-out + `BroadcastBus`
best-effort) -> `hub.go:2854 canonicalizeMembersEvent` na borda de confianca do
barramento -> `useChatWebSocket.ts:905` -> `useMessages`/`useMessageRealtime`
(`useForwardedTargetEvent`, filtrado pelo alvo aberto) ->
`ChatMessageArea.tsx:202 onMembersAdded: reloadOpenDetails` ->
`useConversationDetails.reload` -> refetch de `/details`.

Nao existe mecanismo paralelo e nao deve existir: o unico caminho de
reconciliacao e o refetch, que substitui a secao inteira em vez de anexar.

---

## 3. Caracterizacao executavel CURRENT

Autoridade desta reproducao:
`services/chat-service/internal/storage/channel_membership_contract_postgres_test.go`.
A suite aplica as migrations reais e consulta a funcao SQL instalada e os stores
de producao. A fixture contem **cinco usuarios ativos**, um por papel: owner,
admin, moderator, member e guest. Ha um workspace ativo e tres canais ativos:
publico comum, privado e `#geral`. Nenhum tem `channel_members` no setup.

### 3.1 Acesso x membership persistida

| Canal / momento                                 | Leitores na fixture                           | Linhas explicitas     |
| ----------------------------------------------- | --------------------------------------------- | --------------------- |
| Publico comum                                   | owner, admin, moderator, member; guest nao le | nenhuma               |
| Privado antes da adicao                         | nenhum papel                                  | nenhuma               |
| Privado apos `AddChannelMember` do caso privado | somente member                                | member                |
| `#geral` antes do sync                          | owner, admin, moderator, member; guest nao le | nenhuma               |
| `#geral` apos `SyncGeneralMemberships`          | todos os cinco papeis                         | todos os cinco papeis |

A fixture publica sem rows corresponde ao caminho de criacao:
`ChannelService.CreateChannel` so define `EnsureCreatorMemberRole` para
`private`. A suite semeia os canais; nao executa a criacao pelo service.
No caso privado, ela adiciona member pelo store de producao.

### 3.2 Matriz por superficie

Os resultados abaixo sao os asserts CURRENT da suite, nao o TARGET.
A busca de candidates usa um leitor do canal, que e excluido da propria lista.

| Superficie                     | Publico comum sem rows                         | Privado apos adicao            | `#geral` apos sync                   | Owner do trabalho futuro                |
| ------------------------------ | ---------------------------------------------- | ------------------------------ | ------------------------------------ | --------------------------------------- |
| Acesso x membership persistida | quatro papeis leem implicitamente; nenhuma row | somente member le e tem row    | cinco papeis leem e tem row          | #883; especial de `#geral`: #882        |
| `member_count`                 | 0                                              | 1                              | 5                                    | #883                                    |
| `online_members`               | vazio mesmo fornecendo os leitores como online | member, fornecido como online  | cinco membros fornecidos como online | #886, consumindo roster de #883         |
| `member-candidates`            | owner, moderator, member e guest               | owner, admin, moderator, guest | nenhum                               | #885; adicao especial em `#geral`: #882 |
| Mentions                       | vazio                                          | member                         | todos os cinco papeis                | #887                                    |

No publico, owner/moderator/member aparecem como candidates embora ja leiam
implicitamente. **Guest aparece como candidato elegivel, mas nao le**: nao tem
membership explicita. Admin le, mas nao aparece na sua propria busca.
No privado, o membro atual e excluido dos candidates. Em `#geral`, a consulta
oferece guest apos sync, mas `MemberService.AddChannelMembers` rejeita
`is_general`; a diferenca com o caminho administrativo esta na secao 5.

### 3.3 Limites da evidencia

O teste publico nao adiciona guest: nao ha nesta suite um resultado observado
de seis leitores ou uma segunda fixture com dois members. O caso de
`#geral` comprova quatro inserts no primeiro sync e zero no segundo.
Antes do sync ha leitores implicitos sem rows; a concordancia de acesso,
contagem e mentions e observada **apos o sync**, nos estados dessa fixture.

`online_members` e uma previa filtrada por presenca, nao um roster.
Grupos nao fazem parte desta fixture PostgreSQL. Sua baseline permanece nas
suites existentes citadas na secao 8: participantes ativos, com presenca como
enriquecimento. Nenhuma implementacao de grupo foi alterada.

---

## 4. Causas raiz comprovadas

**R1 — Canal publico nasce sem roster.**
`ChannelService.CreateChannel` define `EnsureCreatorMemberRole` apenas para
`private`; `PGXChannelStore.CreateChannelForActiveMember` so escreve a linha
quando o campo esta definido. Nenhuma outra escrita de `chat.channel_members`
ocorre na criacao. Consequencia direta: `member_count = 0`, `online_members`
vazio, mencao vazia. Owner: **#883**.

**R2 — Nao existe caminho de auto-entrada.**
`MemberService.SelfJoinChannel` esta implementada e testada, mas nenhuma rota a
registra (`router.go` nao referencia nenhum handler de self-join) e nenhum
caller de producao existe. Sem R1 e sem R2, uma linha de `channel_members` num
canal publico so surge por acao administrativa. Owner: **#883**.

**R3 — Portao e fonte discordam na mencao.**
`MentionService.SearchMentions` autoriza com `PermissionService.CanRead`
(acesso, 1.3) e busca com `SearchChannelMembers` (`chat.channel_members`, 1.2).
Nas duas outras superficies de conversa — grupo e canal privado — as duas
populacoes coincidem na baseline, o que escondeu a divergencia. Owner: **#887**.

**R4 — A exclusao de `member-candidates` usa a populacao errada.**
O `NOT EXISTS` sobre `chat.channel_members` esta correto **em relacao ao
contrato que ele implementa** (1.8 com effective = explicita). Ele so produz o
sintoma porque 1.4 esta indefinido para canal publico. Corrigir `candidates`
sem decidir 1.4 apenas move a divergencia. Owner: **#885**, dependente de #883.

**R5 — A previa online de canal e por instancia.**
`(*ChannelHandler).onlineUserIDs` le `ws.PresenceTracker`, alimentado apenas por
`hub.Connect` de conexoes locais. `ws.PresenceDirectory` — que existe
precisamente para responder presenca cluster-wide — nao e consultada nesta
rota. `infra/k8s/overlays/k3s-prod/slots/workloads/runtime-patch.yaml` define
`replicas: 2`, portanto **producao ja roda multi-replica** e a previa
sub-reporta. A documentacao afirmava o contrario e foi corrigida junto com esta
issue. Owner: **#886**.

**R6 — `member_count` e acesso usam populacoes diferentes.**
O codigo conta 1.2; a #881 explicita essa populacao no contrato HTTP, mas nao
altera o resultado. Definir a membership efetiva de 1.4 e alinhar a contagem
continua pendente. Owner: **#883**.

**Nao e causa raiz:** o contrato de `online_members` (filtro de presenca antes
do limite) esta correto e nao deve ser alterado; `members.added` esta correto e
nao precisa de mecanismo paralelo; o canal privado com membro explicito e `#geral` apos sync concordam na fixture,
sem transformar `online_members` em roster.

---

## 5. `#geral`: RF-18 x RF-74

| Aspecto                        | Estado                                                                                                                          |
| ------------------------------ | ------------------------------------------------------------------------------------------------------------------------------- |
| identidade estrutural          | `chat.channels.is_general`, nunca o nome. Migration 000002: `CHECK (NOT is_general OR (type = 'public' AND status = 'active'))` |
| entrada automatica (1 usuario) | `PGXMemberStore.EnsureGeneralMembership` -> `ensureGeneralMembership` -> `addGeneralChannelMember`                              |
| backfill / reparo              | `PGXMemberStore.SyncGeneralMemberships` (so insere, **nunca remove**)                                                           |
| papeis admitidos               | todos os papeis validos do schema, com membership e conta ativas                                                                |
| guest                          | incluido no auto-join de `#geral`; continua sem alcance implicito nos demais canais publicos                                    |
| join explicito                 | `SelfJoinChannel` exige `CanReachPublicChannels`, excluindo guest; nao tem rota (R2)                                            |
| reativacao                     | `ActivateWorkspaceMember` e `AddWorkspaceMember` chamam `ensureGeneralMembership` na mesma transacao                            |
| leave                          | recusado: `domain.CanLeaveChannel` = `!IsGeneral`; `RemoveChannelMember` retorna `ErrCannotLeaveGeneralChannel`                 |
| add-members                    | chat-service e admin-service admitem alvo elegivel; row existente vira no-op                                                    |
| rename / mute                  | recusados pelo mesmo predicado estrutural (`ErrGeneralChannelImmutable`)                                                        |

RF-18 e RF-74 convivem pela excecao estrutural: todo membro elegivel recebe a
row de `#geral`, enquanto `CanReachPublicChannels` continua negando ao guest o
alcance implicito aos demais canais publicos.

Estado consolidado de `#geral`:

1. `ensureGeneralMembership` e `SyncGeneralMemberships` usam a elegibilidade
   compartilhada, incluindo guest ativo com conta valida, e travam o alvo antes
   da escrita.
2. `MemberService.AddChannelMembers` admite reparo/no-op em `#geral`; a PK e
   `ON CONFLICT DO NOTHING` impedem duplicacao de row, count ou evento.
3. A row estrutural permite ao guest ler `#geral`, mas nao muda
   `CanReachPublicChannels`: outros canais publicos continuam invite-only.
4. Remocao, rename e archive de `#geral` continuam recusados.

---

## 6. Threat model (proporcional)

| Vetor                                | Estado atual                                                                                                                                                                                                                                                      | Efeito da #881 |
| ------------------------------------ | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------------- |
| Cross-workspace / BOLA               | Todo predicado de membership junta `chat.channels`/`chat.dm_conversations` por `workspace_id`; o workspace vem da sessao, nunca do path ou body                                                                                                                   | nenhum         |
| Canal privado                        | Exige `chat.channel_members` para todo papel; `GetVisibleChannelByID` colapsa inexistente/arquivado/privado/outro tenant no mesmo `404`                                                                                                                           | nenhum         |
| Enumeracao via `member-candidates`   | A rota revela quem **nao** esta num canal — fato sobre a composicao de canal privado. Por isso a autorizacao e verificada **antes** da busca do canal, e o `query` tem minimo de 2 chars e limite clampado no servidor                                            | nenhum         |
| Enumeracao via erros de add-members  | `403` unico para "chamador sem permissao" e "alvo inelegivel"; nunca nomeia o alvo nem seu estado                                                                                                                                                                 | nenhum         |
| Guest / `#geral`                     | Guest sem acesso implicito e fora do auto-sync; row explicita pode conceder alcance. Secao 5 distingue chat-service (recusa adicao) e Admin API (admite alvos elegiveis)                                                                                          | documentado    |
| Acesso x membership                  | A divergencia e de **exibicao e elegibilidade**, nao de autorizacao: ninguem le um canal que `channel_visible_to_user` nega. O risco inverso e real — um `member_count` de 0 num canal com leitores implicitos pode levar um operador a tratar o canal como vazio | documentado    |
| Realtime para assinantes autorizados | `members.added` reautoriza por assinante no fan-out; envelopes do barramento sao canonicalizados e nunca republicados; `source_instance_id` descarta eco. O payload nao nomeia ninguem                                                                            | nenhum         |
| Logs / instrumentacao                | A #881 nao adiciona instrumentacao. Nenhum token, header, corpo de mensagem, e-mail, payload de busca ou lista de candidatos foi adicionado a log algum                                                                                                           | nenhum         |

**Nenhuma regra de autorizacao foi ampliada ou restringida por esta issue.**
As correcoes de documentacao abaixo alinham o texto ao codigo ja em producao
(RF-74), nao antecipam as decisoes futuras das #882–#889.

---

## 7. Pendencias atribuidas

| Issue | Owner / trabalho TARGET                                                                                                                                             |
| ----- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| #882  | Politica final de `#geral`, identidade estrutural, membership/add-members especial e rename/delete/archive especiais; consolidar RF-18/RF-74 e os fluxos da secao 5 |
| #883  | Effective membership, roster autoritativo, `memberCount` consistente e separacao roster x `online_members` (R1, R2, R6)                                             |
| #884  | Capability `CanAddChannelMembers`/equivalente, autorizacao de POST /members independente de remove/rename/delete, concorrencia e idempotencia do add                |
| #885  | Alinhar a exclusao de `member-candidates` a membership efetiva decidida em #883; autorizacao por acesso e picker ja usam o contrato de add (R4)                     |
| #886  | Painel com roster/count reais e presence existente como enriquecimento; a acao Add Members ja usa `can_add_members` separado (R5)                                   |
| #887  | SearchChannelMembers, autocomplete e mentions com membership autoritativa; rendering/identidade quando aplicavel (R3)                                               |
| #888  | members.added, refetch/invalidation, convergencia roster/count/candidates/mentions, multi-client/reconnect sem reload                                               |
| #889  | Integracao, Playwright, fluxo completo, BOLA/IDOR final, regressao de grupos e gate final da #877                                                                   |

---

## 8. Characterization tests que a #881 deixa no repositorio

Arquivo: `services/chat-service/internal/storage/channel_membership_contract_postgres_test.go`.

PostgreSQL real, com **todas** as migrations atuais aplicadas
(`readAllChatUpMigrations`). Mesmo gate ja adotado no repositorio —
`CHAT_TEST_DATABASE_URL`, recusa de banco cujo nome nao termina em `_test`,
`DROP SCHEMA chat CASCADE` entre casos. Nenhum bypass novo.

Toda afirmacao de visibilidade **executa** `chat.channel_visible_to_user` como
instalada (`SELECT chat.channel_visible_to_user($1, $2)`). O predicado nunca e
reescrito em Go nem em SQL de teste: a migration 000007 criou a funcao e a
000022 a substituiu, entao uma migration futura pode substitui-la de novo sem
editar nenhuma das duas, e so a funcao instalada conhece a politica ativa.

| Teste                                                       | O que registra                                                                                                                                                                               |
| ----------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `..._InstalledVisibilityMatchesTheDomainPredicate`          | **Invariante.** Sem rows, publico e `#geral` seguem `CanReachPublicChannels`; privado nega todos os papeis, independentemente dessa allowlist                                                |
| `..._PublicChannelVisibilityDivergesFromExplicitMembership` | **CURRENT.** Leitores implicitos sem linha; `TotalCount = 0`; mencao vazia; candidates incluem os leitores exceto o chamador, e guest que nao le. Membership: **#883**; candidates: **#885** |
| `..._PrivateChannelVisibilityMatchesExplicitMembership`     | **Baseline.** As quatro superficies ja concordam; nada aqui deve se mover                                                                                                                    |
| `..._GeneralChannelMaterializesMembershipForNonGuestRoles`  | **CURRENT.** `SyncGeneralMemberships` materializa os papeis cobertos, e nenhum guest; guest nao tem linha **e** nao le. Owner do TARGET: **#882**                                            |

Os nomes e comentarios dizem explicitamente que registram o estado atual
diagnosticado pela #881, e nenhum chama o comportamento divergente de
"desired"/"correct". Uma falha nesses testes depois que #882/#883 decidirem a
politica e o efeito pretendido — a issue que mudar o contrato atualiza a
caracterizacao junto.

Ja protegidas antes desta issue e deliberadamente nao duplicadas: a baseline de
grupo (`group_details_service_test.go`, `group_details_handler_test.go` —
presenca nunca filtra participantes, contagem nunca deriva da lista), a
distincao previa-online x contagem (`channel_details_service_test.go`), o
isolamento de workspace e a nao enumeracao (`add_members_postgres_test.go`,
`channel_details_handler_test.go`), e a matriz RF-74
(`domain/permission_test.go`).
