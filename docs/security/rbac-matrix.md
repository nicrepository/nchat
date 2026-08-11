# Matriz de permissoes (RF-74)

Documento canonico de autorizacao do NChat. Vive em `docs/security/` e nao em
`docs/api/` porque descreve a politica, nao o contrato de um endpoint: os
documentos de `docs/api/` descrevem rotas individuais e referenciam esta matriz.

Escopo: os cinco papeis do RF-74 e as acoes que **existem hoje** no codigo.
Nenhuma linha desta tabela descreve um endpoint que nao esta implementado.

## Papeis e escopos

Autorizacao no NChat nao e uma ordenacao numerica unica. Um papel pode ter
autoridade num escopo e nenhuma em outro.

| Papel RF-74        | Onde vive                                                     | Escopo                               |
| ------------------ | ------------------------------------------------------------- | ------------------------------------ |
| Admin Master       | identidade de bootstrap `X-NChat-Admin-Token` no auth-service | plataforma (CLI)                     |
| Admin de Workspace | `chat.workspace_members.role IN ('owner','admin')`            | um workspace                         |
| Moderador          | `chat.workspace_members.role = 'moderator'`                   | um workspace                         |
| Usuario            | `chat.workspace_members.role = 'member'`                      | um workspace                         |
| Guest              | `chat.workspace_members.role = 'guest'`                       | apenas os canais em que foi incluido |

Distincoes que essa tabela existe para nao deixar colapsar:

- **`owner` nao e Admin Master.** `owner` e a autoridade maxima _dentro de um
  workspace_ e nao tem poder algum fora dele. Admin Master e um escopo de
  plataforma; hoje ele so existe como a identidade de bootstrap do auth-service
  (ver "Admin Master" abaixo). Nao ha papel global em `chat.workspace_members`,
  deliberadamente: colocar um la destruiria a separacao de escopo.
- **Moderador de workspace nao e `chat.channel_members.role = 'moderator'`.**
  O segundo e um papel por canal. Nenhum caminho de codigo le um como o outro, e
  nenhuma decisao de autorizacao consulta `chat.channel_members.role`.
- **Admin de Workspace nao e Moderador com mais permissoes.** Sao dois
  predicados diferentes: `domain.CanManageWorkspace` (owner/admin) e
  `domain.CanModerateWorkspace` (owner/admin/moderator). Moderador nao
  administra o workspace nem a API administrativa de usuarios.

## Onde a decisao acontece

Toda decisao efetiva e server-side. O JWT carrega `sub`, `sid`, `jti`, `iat` e
`nbf` — **nenhuma claim de papel**. O papel e lido de `chat.workspace_members`
a cada request, entao nao existe autorizacao decidida por uma claim antiga.
Workspace, canal e conversa sao resolvidos server-side; nenhum papel, workspace
ou ator vindo do cliente participa de qualquer decisao.

As capabilities sao predicados nomeados em `services/chat-service/internal/domain`:

| Predicado                    | Papeis ativos aceitos                                                                |
| ---------------------------- | ------------------------------------------------------------------------------------ |
| `CanReachPublicChannels`     | owner, admin, moderator, member                                                      |
| `CanReadChannel`             | membership de canal (qualquer papel) **ou** canal publico + `CanReachPublicChannels` |
| `CanWriteChannel`            | identico a `CanReadChannel`                                                          |
| `CanCreateChannel`           | owner, admin, moderator, member                                                      |
| `CanManageWorkspace`         | owner, admin                                                                         |
| `CanModerateWorkspace`       | owner, admin, moderator                                                              |
| `CanManageChannelCategories` | delega a `CanModerateWorkspace`                                                      |
| `CanManageChannelMembers`    | delega a `CanModerateWorkspace`                                                      |

Todos exigem membership **ativa**. Todos negam por padrao: a verificacao de
papel e uma allowlist, entao um papel nao reconhecido e recusado em vez de ser
tratado como membro comum.

A politica de leitura de canal tem uma unica definicao em SQL,
`chat.channel_visible_to_user(channel_id, user_id)` (migration 000022), usada
por chat-service, file-service e media-service. `domain.CanReadChannel` e a
mesma regra em Go e precisa concordar com ela exatamente. A funcao SQL e a
autoridade: e ela que a listagem, a leitura de mensagens, os pins, os
favoritos, as reacoes, os anexos e o autorizador de WebSocket realmente
executam.

## Workspace

| Acao                                       | Admin Master | Admin de Workspace | Moderador | Usuario | Guest | Condicao                                                                            |
| ------------------------------------------ | ------------ | ------------------ | --------- | ------- | ----- | ----------------------------------------------------------------------------------- |
| Ver o workspace / sidebar                  | nao          | sim                | sim       | sim     | sim   | conteudo filtrado pela politica de canal                                            |
| Alterar `edit_window_seconds`              | nao          | sim                | **nao**   | nao     | nao   | `CanManageWorkspace`, re-verificado no `UPDATE`                                     |
| Alterar limite anti-spam (RF-19)           | nao          | sim                | **nao**   | nao     | nao   | idem                                                                                |
| Alterar limite de upload (RF-32)           | nao          | sim                | **nao**   | nao     | nao   | idem                                                                                |
| Listar/convidar/suspender usuarios         | sim          | sim                | **nao**   | nao     | nao   | `/auth/admin/*` resolve o workspace pela sessao; `/admin/*` pelo token de bootstrap |
| Criar/renomear/reordenar/excluir categoria | nao          | sim                | **sim**   | nao     | nao   | `CanManageChannelCategories`, re-verificado no `UPDATE`/`INSERT`                    |
| Ler a listagem agrupada de canais          | nao          | sim                | sim       | sim     | sim   | os canais de cada grupo vem da politica de leitura                                  |
| Alterar papel de um membro                 | —            | —                  | —         | —       | —     | **nenhum endpoint existe** (ver Limitacoes)                                         |

## Canais

| Acao                              | Admin de Workspace       | Moderador          | Usuario            | Guest                 | Condicao                                                               |
| --------------------------------- | ------------------------ | ------------------ | ------------------ | --------------------- | ---------------------------------------------------------------------- |
| Listar canais                     | sim                      | sim                | sim                | **so os que integra** | `chat.channel_visible_to_user`                                         |
| Ler canal publico                 | sim                      | sim                | sim                | **so se incluido**    | idem                                                                   |
| Ler canal privado                 | **so se incluido**       | **so se incluido** | **so se incluido** | **so se incluido**    | membership de canal, para todos os papeis                              |
| Acesso direto por ID/slug         | mesma regra da listagem  | idem               | idem               | idem                  | `ErrNotFound` nao-enumerante                                           |
| Receber eventos por WebSocket     | mesma regra              | idem               | idem               | idem                  | `serviceAuthorizer` usa a mesma consulta                               |
| Publicar mensagem                 | segue leitura            | segue leitura      | segue leitura      | segue leitura         | `CanWriteChannel` = `CanReadChannel`                                   |
| Encaminhar mensagem               | segue leitura no destino | idem               | idem               | idem                  | avaliado no proprio `INSERT`                                           |
| Reagir / favoritar                | segue leitura            | idem               | idem               | idem                  | mesma politica                                                         |
| **Pin/unpin (RF-05)**             | segue leitura            | segue leitura      | segue leitura      | **segue leitura**     | sem RBAC adicional; Guest incluido no canal pode fixar                 |
| Criar canal                       | sim                      | sim                | sim                | **nao**               | `CanCreateChannel`; re-derivado no `INSERT`                            |
| Editar / arquivar canal           | sim                      | **nao**            | nao                | nao                   | `CanManageWorkspace`; `#geral` e imutavel                              |
| Entrar sozinho em canal publico   | sim                      | sim                | sim                | **nao**               | `CanReachPublicChannels`                                               |
| Adicionar membros a um canal      | sim                      | **sim**            | nao                | nao                   | `CanManageChannelMembers`; re-derivado na transacao; `#geral` recusado |
| Remover membro de um canal        | sim                      | **sim**            | nao                | nao                   | mesmo predicado da adicao                                              |
| Buscar candidatos a membro        | sim                      | **sim**            | nao                | nao                   | mesmo predicado da escrita                                             |
| Sair de um canal                  | sim                      | sim                | sim                | sim                   | proprio; `#geral` recusado                                             |
| Membership automatica em `#geral` | sim                      | sim                | sim                | **nao**               | Guest chega a `#geral` como a qualquer outro canal: sendo adicionado   |

## DMs

RBAC de workspace **nao concede acesso a conversas privadas**. Participacao
(`chat.dm_members`) e a unica pergunta feita; `chat.workspace_members.role` nao
e consultada em nenhum caminho de DM.

| Acao                                | Admin Master | Admin de Workspace  | Moderador           | Usuario         | Guest           |
| ----------------------------------- | ------------ | ------------------- | ------------------- | --------------- | --------------- |
| Ler DM 1:1                          | **nao**      | **so se participa** | **so se participa** | so se participa | so se participa |
| Ler grupo                           | **nao**      | **so se participa** | **so se participa** | so se participa | so se participa |
| Ver detalhes/participantes de grupo | **nao**      | so se participa     | so se participa     | so se participa | so se participa |
| Adicionar participante em grupo     | **nao**      | so se participa     | so se participa     | so se participa | so se participa |
| Pin/unpin em DM                     | **nao**      | so se participa     | so se participa     | so se participa | so se participa |

Aplicar `CanManageWorkspace` a grupos privados seria **mais permissivo, nao
menos**: um admin de workspace nao e participante, nao enxerga a conversa pela
politica SQL de DM, e lhe dar autoridade sobre uma conversa que ele nao pode ler
seria escalacao de privilegio. `chat.dm_members.role` e fechado por CHECK ao
unico valor `'member'` — um grupo nao tem gestor a exigir. DM 1:1 nao aceita a
operacao de adicionar participante.

## Admin Master

O sistema **nao possui** hoje tabela, coluna, flag ou papel global de
administrador de plataforma. O unico mecanismo de escopo global existente e a
identidade de bootstrap do auth-service:

- guard: `AdminBootstrapGuard` (`services/auth-service/internal/http/admin_middleware.go`);
- header: `X-NChat-Admin-Token`, comparado em tempo constante contra
  `ADMIN_BOOTSTRAP_TOKEN`; ausente da configuracao ⇒ `503`, errado ⇒ `401`;
- rotas protegidas: `POST /admin/users`, `POST /admin/invites`,
  `PATCH /admin/users/{id}/status`.

O RF-74 nao criou um papel global novo. Criar um exigiria uma tabela ou coluna
que nenhum endpoint sabe escrever — exatamente o "codigo morto alargando uma
constraint de seguranca" que o SECURITY.md ja recusou uma vez para o moderador.
Admin Master, portanto, tem poder **somente** nas acoes acima e em nenhuma
outra: nao le canal, nao le DM, nao altera settings de workspace, nao aparece
em nenhuma consulta de conteudo.

O equivalente browser-side dessas operacoes ja existe e **nao** usa o token:
`/auth/admin/users` e `/auth/admin/invites` passam por `BearerAuth` +
`RequireActiveSession` + `RequireWorkspaceAdmin`, que resolve o workspace a
partir da sessao (`PGXUserStore.GetAdminWorkspaceID`, `role IN ('owner','admin')`)
e nao aceita nenhum valor do cliente. Um Moderador recebe `403` ali.

## Limitacoes conhecidas

1. **Nao existe endpoint de atribuicao de papel.** Nenhum caminho de codigo
   escreve `chat.workspace_members.role` — nem para `owner`, nem para `admin`,
   nem para os papeis que o RF-74 acrescenta. A atribuicao e feita diretamente
   no banco. Criar essa rota e trabalho proprio, com sua propria superficie de
   abuso (auto-promocao, mass assignment, promocao a `owner`), e nao foi
   introduzida aqui de carona.
2. **O bootstrap por `X-NChat-Admin-Token` continua existindo** em paralelo a
   autorizacao por sessao, nas tres rotas `/admin/*` listadas acima. Ele
   permanece porque e o unico caminho que funciona antes de existir um primeiro
   administrador. Substitui-lo exige decidir como o primeiro admin nasce, o que
   esta fora do RF-74.
3. **Guest e o diretorio do workspace.** A busca de candidatos a DM continua
   visivel a um Guest. Isso e comportamento pre-existente e nao foi alterado: o
   RF-74 restringe o alcance de **canais**, e estreitar o diretorio e uma
   decisao de produto separada.
