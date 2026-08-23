# Matriz de permissoes (RF-74)

Documento canonico de autorizacao do NChat. Vive em `docs/security/` e nao em
`docs/api/` porque descreve a politica, nao o contrato de um endpoint: os
documentos de `docs/api/` descrevem rotas individuais e referenciam esta matriz.

Escopo: os cinco papeis do RF-74 e as acoes que **existem hoje** no codigo.
Nenhuma linha desta tabela descreve um endpoint que nao esta implementado.

## Papeis e escopos

Autorizacao no NChat nao e uma ordenacao numerica unica. Um papel pode ter
autoridade num escopo e nenhuma em outro.

| Papel RF-74        | Onde vive                                                                                                               | Escopo                               |
| ------------------ | ----------------------------------------------------------------------------------------------------------------------- | ------------------------------------ |
| Admin Master       | `auth.admin_principals` + `auth.admin_principal_roles` (issue #578); `X-NChat-Admin-Token` permanece como bootstrap CLI | plataforma                           |
| Admin de Workspace | `chat.workspace_members.role IN ('owner','admin')`                                                                      | um workspace                         |
| Moderador          | `chat.workspace_members.role = 'moderator'`                                                                             | um workspace                         |
| Usuario            | `chat.workspace_members.role = 'member'`                                                                                | um workspace                         |
| Guest              | `chat.workspace_members.role = 'guest'`                                                                                 | apenas os canais em que foi incluido |

Distincoes que essa tabela existe para nao deixar colapsar:

- **`owner` nao e Admin Master.** `owner` e a autoridade maxima _dentro de um
  workspace_ e nao tem poder algum fora dele. Admin Master e um escopo de
  plataforma e vive em outra tabela, em outro schema logico de autorizacao (ver
  "Admin Master" abaixo). Continua nao havendo papel global em
  `chat.workspace_members`, deliberadamente: colocar um la destruiria a
  separacao de escopo. Ser `owner` de um workspace nao concede capability
  administrativa alguma, e nenhuma consulta le uma coisa como a outra.
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

| Acao                                        | Admin Master | Admin de Workspace | Moderador | Usuario | Guest | Condicao                                                                            |
| ------------------------------------------- | ------------ | ------------------ | --------- | ------- | ----- | ----------------------------------------------------------------------------------- |
| Ver o workspace / sidebar                   | nao          | sim                | sim       | sim     | sim   | conteudo filtrado pela politica de canal                                            |
| Alterar `edit_window_seconds`               | nao          | sim                | **nao**   | nao     | nao   | `CanManageWorkspace`, re-verificado no `UPDATE`                                     |
| Alterar limite anti-spam (RF-19)            | nao          | sim                | **nao**   | nao     | nao   | idem                                                                                |
| Alterar limite de upload (RF-32)            | nao          | sim                | **nao**   | nao     | nao   | idem                                                                                |
| Listar/convidar/suspender usuarios          | sim          | sim                | **nao**   | nao     | nao   | `/auth/admin/*` resolve o workspace pela sessao; `/admin/*` pelo token de bootstrap |
| Abrir o Admin Console                       | **sim**      | nao                | **nao**   | nao     | nao   | `auth.admin_principals` ativo com ao menos uma capability                           |
| Ler a auditoria administrativa              | **sim**      | nao                | **nao**   | nao     | nao   | capability `admin.audit.read`                                                       |
| Listar usuarios da plataforma               | **sim**      | nao                | **nao**   | nao     | nao   | capability `admin.users.read`; escopo de plataforma, nao de workspace               |
| Ativar/desativar conta pela plataforma      | **sim**      | nao                | **nao**   | nao     | nao   | `admin.users.manage`; nunca a propria conta                                         |
| Conceder/remover papel administrativo       | **sim**      | nao                | **nao**   | nao     | nao   | `admin.superuser`; nunca os proprios papeis; nunca o ultimo administrador           |
| Alterar anti-spam pelo Admin Console        | **sim**      | sim                | **nao**   | nao     | nao   | `admin.security.manage` (plataforma) ou `CanManageWorkspace` (workspace)            |
| Alterar limite de upload pelo Admin Console | **sim**      | sim                | **nao**   | nao     | nao   | `admin.infrastructure.manage` (plataforma) ou `CanManageWorkspace` (workspace)      |
| Criar/renomear/reordenar/excluir categoria  | nao          | sim                | **sim**   | nao     | nao   | `CanManageChannelCategories`, re-verificado no `UPDATE`/`INSERT`                    |
| Ler a listagem agrupada de canais           | nao          | sim                | sim       | sim     | sim   | os canais de cada grupo vem da politica de leitura                                  |
| Alterar papel de um membro                  | —            | —                  | —         | —       | —     | **nenhum endpoint existe** (ver Limitacoes)                                         |

## Canais

| Acao                              | Admin de Workspace       | Moderador          | Usuario            | Guest                 | Condicao                                                                                                                                                                        |
| --------------------------------- | ------------------------ | ------------------ | ------------------ | --------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Listar canais                     | sim                      | sim                | sim                | **so os que integra** | `chat.channel_visible_to_user`                                                                                                                                                  |
| Ler canal publico                 | sim                      | sim                | sim                | **so se incluido**    | idem                                                                                                                                                                            |
| Ler canal privado                 | **so se incluido**       | **so se incluido** | **so se incluido** | **so se incluido**    | membership de canal, para todos os papeis; o Admin Master **lista** canais privados com `admin.channels.read` mas nao le nenhum                                                 |
| Acesso direto por ID/slug         | mesma regra da listagem  | idem               | idem               | idem                  | `ErrNotFound` nao-enumerante                                                                                                                                                    |
| Receber eventos por WebSocket     | mesma regra              | idem               | idem               | idem                  | `serviceAuthorizer` usa a mesma consulta                                                                                                                                        |
| Publicar mensagem                 | segue leitura            | segue leitura      | segue leitura      | segue leitura         | `CanWriteChannel` = `CanReadChannel`                                                                                                                                            |
| Encaminhar mensagem               | segue leitura no destino | idem               | idem               | idem                  | avaliado no proprio `INSERT`                                                                                                                                                    |
| Reagir / favoritar                | segue leitura            | idem               | idem               | idem                  | mesma politica                                                                                                                                                                  |
| **Pin/unpin (RF-05)**             | segue leitura            | segue leitura      | segue leitura      | **segue leitura**     | sem RBAC adicional; Guest incluido no canal pode fixar                                                                                                                          |
| Criar canal                       | sim                      | sim                | sim                | **nao**               | `CanCreateChannel`; re-derivado no `INSERT`                                                                                                                                     |
| Editar / arquivar canal           | sim                      | **nao**            | nao                | nao                   | `CanManageWorkspace`; `#geral` e imutavel. O Admin Master arquiva/desarquiva com `admin.channels.manage`, e `#geral` continua recusado                                          |
| Entrar sozinho em canal publico   | sim                      | sim                | sim                | **nao**               | `CanReachPublicChannels`                                                                                                                                                        |
| Adicionar membros a um canal      | sim                      | **sim**            | nao                | nao                   | `CanManageChannelMembers`; re-derivado na transacao; `#geral` recusado. O Admin Master tambem adiciona, com `admin.channels.manage`, sob a mesma regra de elegibilidade do alvo |
| Remover membro de um canal        | sim                      | **sim**            | nao                | nao                   | mesmo predicado da adicao; idem para o Admin Master, e `#geral` continua recusado                                                                                               |
| Buscar candidatos a membro        | sim                      | **sim**            | nao                | nao                   | mesmo predicado da escrita                                                                                                                                                      |
| Sair de um canal                  | sim                      | sim                | sim                | sim                   | proprio; `#geral` recusado                                                                                                                                                      |
| Membership automatica em `#geral` | sim                      | sim                | sim                | **nao**               | Guest chega a `#geral` como a qualquer outro canal: sendo adicionado                                                                                                            |

## DMs

RBAC de workspace **nao concede acesso a conversas privadas**. Participacao
(`chat.dm_members`) e a unica pergunta feita; `chat.workspace_members.role` nao
e consultada em nenhum caminho de DM.

| Acao                                | Admin Master | Admin de Workspace  | Moderador           | Usuario         | Guest           |
| ----------------------------------- | ------------ | ------------------- | ------------------- | --------------- | --------------- |
| Ver metadados operacionais de DM    | **sim**      | nao                 | nao                 | nao             | nao             |
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

Desde a issue #578 o Admin Master **existe no banco**, com capabilities
granulares. O modelo tem quatro tabelas no schema `auth` (migration 000008):

| Tabela                         | Responde                            |
| ------------------------------ | ----------------------------------- |
| `auth.admin_principals`        | quem e administrador de plataforma  |
| `auth.admin_roles`             | quais papeis existem                |
| `auth.admin_role_capabilities` | o que cada papel concede            |
| `auth.admin_principal_roles`   | quem tem qual papel, e desde quando |

Papeis semeados pela migration: `platform-superuser` (concede
`admin.superuser`) e `platform-auditor` (somente leitura + auditoria).

Capabilities definidas, fechadas por `CHECK` em `auth.admin_role_capabilities`:

`admin.superuser`, `admin.users.read`, `admin.users.manage`,
`admin.channels.read`, `admin.channels.manage`, `admin.security.read`,
`admin.security.manage`, `admin.integrations.read`, `admin.integrations.manage`,
`admin.infrastructure.read`, `admin.infrastructure.manage`, `admin.audit.read`,
`admin.config.read`, `admin.config.manage`.

Regras de avaliacao (`services/admin-service/internal/domain/capability.go`):

- deny by default: conjunto vazio nao concede nada;
- `admin.superuser` implica as demais;
- uma capability que a plataforma nao define e recusada **inclusive** para um
  superuser, entao um guard escrito errado falha fechado;
- a leitura acontece a cada requisicao, contra o banco, entao remover um papel
  vale na requisicao seguinte.

Onde essas capabilities decidem hoje (issue #579 fechou a maior parte da lacuna):

| Capability                         | Rotas que ela decide                                                                                                                                     |
| ---------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `admin.audit.read`                 | `GET /api/admin/audit/events` — a trilha global, e com `user_id` o historico de uma conta                                                                |
| `admin.users.read`                 | `GET /api/admin/users`, `GET /api/admin/users/{id}` — inclui o filtro `workspace_role`                                                                   |
| `admin.users.manage`               | `PATCH /api/admin/users/{id}/status`, `DELETE /api/admin/users/{id}/sessions`                                                                            |
| `admin.superuser`                  | `POST` e `DELETE /api/admin/users/{id}/admin-roles[/{slug}]` — alem de implicar as demais                                                                |
| `admin.channels.read`              | `GET /api/admin/channels` (com o filtro `administered_by`), `GET /api/admin/channels/{id}`, `GET /api/admin/conversations`                               |
| `admin.channels.manage`            | `PATCH /api/admin/channels/{id}/status`, `POST` e `DELETE /api/admin/channels/{id}/members[/{userID}]`, `GET /api/admin/channels/{id}/member-candidates` |
| `admin.security.read/manage`       | `GET` e `PATCH /api/admin/policies/anti-spam[/{workspaceID}]`                                                                                            |
| `admin.infrastructure.read/manage` | `GET` e `PATCH /api/admin/policies/upload[/{workspaceID}]`                                                                                               |
| `admin.config.read`                | `GET /api/admin/config`, `POST /api/admin/config/preview`, `GET /api/admin/config/versions`                                                              |
| `admin.config.manage`              | `POST /api/admin/config/apply`, `POST /api/admin/config/versions/{versionID}/rollback`                                                                   |
| `admin.integrations.*`             | ainda sem endpoint                                                                                                                                       |

**Uma alteracao de configuracao que enfraquece a plataforma exige
`admin.superuser`, alem de `admin.config.manage`.** A decisao e sobre o valor
resultante, nao sobre o campo: baixar o tamanho minimo de senha abaixo do padrao,
desligar um requisito de complexidade, elevar o limite de tentativas de login,
encurtar o bloqueio ou esticar a sessao ociosa mudam quem consegue entrar na
plataforma, e a capability que confere toda a autoridade e a que responde por
isso. A regra vale igual no rollback: desfazer um endurecimento e produzir um
enfraquecimento. O predicado por definicao esta em
`internal/domain/config_catalog.go`; o inventario completo esta em
[`config-inventory.md`](config-inventory.md).

A configuracao editavel pelo console e apenas a **classe A** —
`auth.auth_policy_settings`, lida pelo auth-service na propria requisicao que a
aplica. Nao existe classe B (credencial editavel em runtime): credenciais vem de
Sealed Secrets e nao ha backend de secret que a Admin API possa escrever. Um
secret ja armazenado nunca e devolvido: a API responde apenas `configured`.

**Alterar quem administra a plataforma exige `admin.superuser`, e nao
`admin.users.manage`.** Um principal so pode conferir autoridade que ja detem por
inteiro; qualquer coisa mais estreita concedendo um papel seria escalacao
horizontal — um administrador com `admin.users.manage` entregando
`admin.security.manage` a alguem. As invariantes acompanham a regra:

- **auto-escalacao e auto-bloqueio** — nenhum administrador altera os proprios
  papeis, o proprio status ou as proprias sessoes (`403`, registrado como
  `denied`);
- **alvo valido** — a conta precisa existir, nao estar soft-deleted e estar
  ativa; um principal suspenso fora de banda nao e reativado de carona por uma
  concessao;
- **a plataforma nunca fica sem administracao** — a revogacao conta os principals
  restantes que alcancam `admin.superuser` _depois_ do delete e _dentro_ da
  transacao, sob advisory lock transacional, e faz rollback se o numero chegaria
  a zero.

Escopo do Admin Master permanece estritamente de plataforma: ele **nao** le
canal, **nao** le DM, **nao** altera settings de workspace pela API do chat e
**nao** aparece em nenhuma consulta de conteudo. Nenhuma capability
administrativa concede acesso a mensagem.

### O que a #579 acrescentou, e o que deliberadamente nao acrescentou

`admin.channels.read` **lista** canais privados: a linha diz que o canal existe,
qual o tamanho e quem o administra. Isso nao amplia a politica de leitura de
canal — nenhuma mensagem e nenhum nome de membro de canal privado sai da
listagem, e `chat.channel_visible_to_user` continua sendo a unica coisa que
decide quem pode ler um.

`GET /api/admin/conversations` devolve **somente metadados** de DM: identificador,
workspace, tipo, estado, contagem de participantes, volume agregado e
timestamps. Sem corpo, sem titulo, sem participante nominal, sem anexo, sem
prévia, sem "mensagem mais recente" e sem busca de conteudo. Nao existe endpoint
administrativo de leitura de mensagem em lugar nenhum deste servico, e nenhuma
consulta introduzida pela issue le `chat.messages.body_text`. `chat.dm_members`
continua sendo a unica pergunta feita para decidir quem le uma conversa.

O detalhe de canal devolve **duas listas separadas** — moderadores do canal
(`chat.channel_members.role`) e owners/admins do workspace
(`chat.workspace_members.role`). Sao autoridades diferentes e o payload as mantem
em campos diferentes justamente para nao ensinar ao console o modelo que esta
matriz existe para impedir.

As duas politicas operacionais (`anti-spam`, `upload`) escrevem as mesmas colunas
de `chat.workspaces` que os endpoints de admin de workspace do chat-service
escrevem, sob o mesmo `CHECK` e com os mesmos bounds compartilhados
(`libs/go/platform/antispampolicy`, `libs/go/platform/uploadpolicy`). O que muda e
a autorizacao: o chat-service pergunta "esta pessoa administra este workspace", o
admin-service pergunta "este principal detem a capability de plataforma". Nenhuma
das duas e a outra, e nenhuma e pulada.

Nao ha exclusao administrativa de canal e nao ha apagamento de historico.

### Membership de canal administrada pela plataforma

O console adiciona e remove membros de canal com `admin.channels.manage`. Isso
**nao** e uma segunda copia da regra do chat-service, e a divisao entre as duas
metades da decisao e o motivo:

| Metade da decisao            | Onde vive                                                                                                                                                                   | Compartilhada?             |
| ---------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------- |
| Quem **pode pedir**          | chat-service: `wm.role IN ('owner','admin','moderator')` re-derivado dentro da transacao. admin-service: capability `admin.channels.manage`, relida do banco a cada request | **nao**, e nem deveria     |
| Quem **pode ser adicionado** | `libs/go/platform/channelmembership.EligibleTargetsCTE`                                                                                                                     | **sim**, verbatim nos dois |

A metade do ator e legitimamente diferente: no chat-service a autoridade e um
papel de workspace que pode ser revogado no meio do request; no admin-service e
uma capability de plataforma, e o ator normalmente **nao** e membro do workspace.
Copiar o predicado do ator teria recusado toda operacao administrativa legitima;
copia-lo e relaxa-lo teria criado exatamente a segunda regra divergente que essa
extracao evita.

A metade do alvo e um fato sobre o canal e a pessoa, nao sobre quem pergunta, e
por isso e uma string so, embutida pelos dois escritores de
`chat.channel_members`. Ela exige: membership **ativa** do workspace do canal,
workspace ativo, canal ativo, conta ativa e nao soft-deleted. Guest **e**
elegivel — ser adicionado a um canal e o unico caminho pelo qual um Guest chega
a qualquer canal.

Invariantes preservadas: o papel inserido e sempre `member` (nenhum dos dois
servicos cria moderador de canal por essa via), a adicao e tudo-ou-nada, a
repeticao e idempotente, e `#geral` recusa remocao como no chat-service.

O console escolhe a pessoa por um seletor de busca
(`GET /api/admin/channels/{id}/member-candidates`), que exige a **mesma**
capability da mutacao — ver quem existe num workspace nao e consequencia de
poder ver que um canal existe. O workspace do seletor vem do canal, dentro da
query, entao a busca nunca alcanca outro tenant. Ela e conveniencia e nao
controle: a rota de adicao redecide a elegibilidade de quem for realmente
submetido, com a regra compartilhada, na mesma statement que escreve.

Membership de **grupo privado de DM** continua fora: administrador de plataforma
nao tem autoridade sobre uma conversa que nao pode ler, e `chat.dm_members`
segue sendo a unica pergunta feita. "Grupos" no console significa os canais do
workspace.

O primeiro administrador nasce de uma concessao no banco, executada por quem ja
tem acesso ao PostgreSQL — nunca de um segredo estatico no navegador. O
procedimento esta em `docs/runbooks/task-admin-console-foundation.md`.

### Bootstrap CLI que permanece

- guard: `AdminBootstrapGuard` (`services/auth-service/internal/http/admin_middleware.go`);
- header: `X-NChat-Admin-Token`, comparado em tempo constante contra
  `ADMIN_BOOTSTRAP_TOKEN`; ausente da configuracao ⇒ `503`, errado ⇒ `401`;
- rotas protegidas: `POST /admin/users`, `POST /admin/invites`,
  `PATCH /admin/users/{id}/status`.

Ele nao participa de nenhuma rota da Admin API e nao pode ser enviado ao
navegador. Continua existindo porque e o caminho de provisionamento de contas
anterior ao console, e substitui-lo e trabalho de outra issue.

O equivalente browser-side dessas operacoes ja existe e **nao** usa o token:
`/auth/admin/users` e `/auth/admin/invites` passam por `BearerAuth` +
`RequireActiveSession` + `RequireWorkspaceAdmin`, que resolve o workspace a
partir da sessao (`PGXUserStore.GetAdminWorkspaceID`, `role IN ('owner','admin')`)
e nao aceita nenhum valor do cliente. Um Moderador recebe `403` ali.

## Limitacoes conhecidas

1. **Nao existe endpoint de atribuicao de papel _de workspace_.** Nenhum caminho
   de codigo escreve `chat.workspace_members.role` — nem para `owner`, nem para
   `admin`, nem para os papeis que o RF-74 acrescenta. A atribuicao e feita
   diretamente no banco. A issue #579 acrescentou concessao e revogacao de papel
   **administrativo de plataforma** (`auth.admin_principal_roles`), que e outro
   escopo e outra tabela; o papel de workspace continua sem rota, com sua propria
   superficie de abuso, e nao foi introduzido de carona.
2. **O bootstrap por `X-NChat-Admin-Token` continua existindo** em paralelo a
   autorizacao por sessao, nas tres rotas `/admin/*` listadas acima. A issue #578
   decidiu como o primeiro administrador de plataforma nasce (concessao no banco,
   ver acima), mas nao substituiu essas tres rotas de provisionamento de contas —
   isso e trabalho proprio.
3. **`admin.integrations.*` e `admin.config.*` ainda nao tem endpoint.** As
   demais capabilities passaram a decidir rotas reais com a issue #579 (ver a
   tabela acima). Essas duas continuam definidas para que o modelo de papeis
   permaneca estavel entre as issues administrativas seguintes; ate la, conceder
   uma nao abre nenhuma rota.

4. **A auditoria filtra por pessoa, mas nao por ator.**
   `GET /api/admin/audit/events?user_id=<uuid>` devolve os eventos realizados
   **sobre** aquela conta, comparando a chave canonica `admin.user:<uuid>` na
   coluna `resource` (indice `idx_admin_audit_events_resource`, migration
   000011). O filtro roda no `WHERE`, antes do `ORDER BY` e do `LIMIT`, entao
   alcanca um evento que ja saiu da janela global.

   "Acoes feitas **por** alguem" continua sem filtro proprio: `actual_user_id`
   responde outra pergunta, e a tela de usuarios pede o historico da conta. Nao
   ha filtro por metadata, por JSON nem por expressao — a API expressa uma
   intencao so.

5. **Guest e o diretorio do workspace.** A busca de candidatos a DM continua
   visivel a um Guest. Isso e comportamento pre-existente e nao foi alterado: o
   RF-74 restringe o alcance de **canais**, e estreitar o diretorio e uma
   decisao de produto separada.
