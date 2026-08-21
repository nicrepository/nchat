# Admin Console — Gestao operacional (issue #579)

**Branch:** `feature/admin-579-management`
**Depende de:** issue #578 (fundacao do Admin Console)
**Implementa:** gestao de usuarios, canais/grupos, metadados de DM e as politicas
operacionais que existem em runtime.

---

## O que foi entregue

| Slice | Escopo                                                                                                                                                                                              |
| ----- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| A     | Diretorio de usuarios: listagem paginada, busca, filtros (incluindo `workspace_role`), ativacao/desativacao, revogacao de sessoes, concessao/revogacao de papel administrativo com confirmacao      |
| B     | Diretorio de canais: listagem paginada, filtros (incluindo `administered_by`), detalhe com moderadores, admins de workspace e preview de membership, arquivar/desarquivar, adicionar/remover membro |
| C     | Metadados de conversas privadas — e somente metadados                                                                                                                                               |
| D     | Politica anti-spam (RF-19) por workspace                                                                                                                                                            |
| E     | Limite de upload (RF-32) por workspace                                                                                                                                                              |

Tudo vive no console separado (`apps/admin-web`) e na Admin API
(`services/admin-service`). Nenhuma operacao privilegiada nova foi publicada no
host do chat: a fronteira de origem da #578 permanece intacta, e
`scripts/ci/admin-route-contract-check.sh` continua sendo o gate que prova isso
contra os manifests renderizados.

O contrato completo esta em [`docs/api/admin-endpoints.md`](../api/admin-endpoints.md);
a politica esta em [`docs/security/rbac-matrix.md`](../security/rbac-matrix.md).

---

## Decisoes que valem registrar

### O admin-service escreve no banco, nao chama outro servico

`admin-service` compartilha o `DATABASE_URL` com `auth-service` e `chat-service`
e le/escreve `auth.*` e `chat.*` diretamente. E o padrao que a plataforma ja usa
entre servicos — `file-service`, `media-service` e `search-service` leem o schema
`chat` do mesmo jeito. A alternativa, uma chamada HTTP privilegiada
servico-a-servico, exigiria inventar uma segunda credencial administrativa e uma
segunda cadeia de guards: mais superficie de ataque do que a que substituiria.

A consequencia e que a semantica de suspensao existe em dois escritores
(`auth-service/internal/storage/user_store.go:UpdateUserStatus` e
`admin-service/internal/storage/user_store.go:UpdateUserStatus`). Os dois fazem a
mesma transacao — lock da linha, validacao da transicao sob o lock, revogacao de
sessoes, invalidacao de codigos OIDC pendentes — e ambos tem teste. Uma mudanca
em um precisa acompanhar o outro.

### Membership de canal: uma regra, duas metades

O console adiciona e remove membros de canal, e a regra **nao** foi duplicada. A
decisao tem duas metades e so uma delas e compartilhavel:

- **quem pode pedir** e legitimamente diferente. No chat-service e um papel de
  workspace re-derivado dentro da transacao, porque la a autoridade pode ser
  revogada no meio do request. No admin-service e a capability de plataforma
  `admin.channels.manage`, relida do banco pelo guard de sessao, e o ator
  normalmente nem e membro do workspace. Copiar o predicado do chat teria
  recusado toda operacao legitima; copia-lo e relaxa-lo teria criado a segunda
  regra divergente que se queria evitar.
- **quem pode ser adicionado** e um fato sobre o canal e a pessoa, e virou
  `libs/go/platform/channelmembership.EligibleTargetsCTE` — uma string embutida
  verbatim pelos dois escritores de `chat.channel_members`. A extracao no
  chat-service e whitespace-only: o SQL efetivo nao mudou, e a suite do
  `internal/storage` dele continua passando.

`TestAddChannelMembersQuery_EmbedsTheSharedEligibilityRule` falha se o
admin-service parar de embutir a constante, e nomeia cada join da regra, de modo
que um enfraquecimento silencioso da constante tambem quebra este servico.

### Adicionar e remover tem regras diferentes

| Estado do canal  | Adicionar | Remover |
| ---------------- | --------- | ------- |
| Normal ativo     | sim       | sim     |
| Normal arquivado | nao       | **sim** |
| `#geral`         | sim       | nao     |

Arquivado bloqueia so a adicao, porque a regra de elegibilidade compartilhada
exige `c.status = 'active'`; a remocao nao le o status, igual ao
`RemoveChannelMember` do chat-service. `#geral` e o espelho: aceita adicao (um
guest nao entra nele automaticamente, entao adicionar um e operacao real) e
recusa remocao com 403.

O console tem um predicado por operacao — `canAddMember` e `canRemoveMember` —
justamente porque um boolean unico escondia a remocao que o backend aceita.

### member_count e exato sob concorrencia

`member_count` e o total **depois** da operacao, e isso vale com mutacoes
simultaneas. Todo writer de `chat.channel_members` — os dois do admin-service e
`AddChannelMembers`, `AddChannelMember` e `RemoveChannelMember` do chat-service —
toma `SELECT id FROM chat.channels WHERE id = $1 FOR UPDATE` como **primeira**
instrucao da transacao (`channelmembership.LockChannelSQL`), e conta depois de
escrever e antes de commitar.

Sem isso, duas adds partindo de dez contavam onze cada uma sob READ COMMITTED, e
o estado final era doze — uma das respostas mentia. Com o lock elas respondem
onze e doze, em qualquer ordem.

O lock e **por canal**: mutacoes em canais diferentes nao esperam umas pelas
outras. Ordem canonica: canal, depois ator/alvos, depois a mutacao, depois a
contagem.

`AddWorkspaceMember` do chat-service trava na ordem inversa (workspace_members e
depois `#geral` com `FOR SHARE`), mas nao ha ciclo alcancavel: os caminhos que
travam o canal primeiro ou recusam `#geral` (chat-service) ou nunca travam uma
linha de `workspace_members` (admin-service, cuja autoridade e capability de
plataforma). Mudar qualquer um desses dois fatos exige reavaliar isto.

`TestPostgreSQL_ConcurrentAddsReportConsecutiveTotals` prova a corrida: uma
terceira transacao segura a linha do canal, as duas operacoes ficam na fila, o
teste confirma por `pg_stat_activity` que ambas estao bloqueadas e so entao
libera. Com `FOR SHARE` o teste falha com `2 and 2`; com `FOR UPDATE`, passa.

### O operador escolhe pessoas, nunca identificadores

Duas telas precisavam de uma pessoa — adicionar membro a um canal e filtrar
canais por quem os administra — e as duas pediam um UUID digitado. Um console
administrativo e operado por gente que conhece colegas por nome.

Ambas usam agora `AdminUserSearchSelect`: um combobox que busca no servidor,
com debounce, protecao contra resposta velha, teclado, e estados separados de
carregando/vazio/erro. Ele abstrai a busca e a apresentacao de uma pessoa;
**nao** abstrai qual endpoint e chamado nem o que acontece na selecao, porque
essas sao perguntas diferentes nos dois lugares:

| Consumidor                | Fonte dos candidatos                                            | Capability              |
| ------------------------- | --------------------------------------------------------------- | ----------------------- |
| Adicionar membro          | `GET /channels/{id}/member-candidates` (workspace vem do canal) | `admin.channels.manage` |
| Filtro "Administrado por" | `GET /users` (a listagem de canais e global de plataforma)      | `admin.users.read`      |

A protecao contra resposta velha tem duas metades, porque staleness acontece nos
dois sentidos. Uma resposta que chega para uma requisicao ja abandonada e
descartada pelo `AbortSignal`. E enquanto o operador digita — entre a tecla e o
fim do debounce — a lista na tela ainda responde ao termo anterior: nesse
intervalo ela nao existe. Nao fica esmaecida nem desabilitada; nao ha opcao
nenhuma, e o painel diz que esta carregando. Clique, Enter e Seta+Enter ficam
seguros por construcao em vez de por tres guardas separadas, e nenhum deles pode
selecionar alguem que o operador nunca viu oferecido. Encurtar o termo abaixo do
minimo fecha a lista na tecla, nao 300ms depois.

O identificador so existe internamente: viaja em `UserOption.id`, chega a API e
para ali. Nenhuma tela o exibe. Texto digitado e **busca**, nunca valor — o botao
de adicionar so habilita com uma pessoa selecionada, e `administered_by` so e
enviado depois de uma selecao valida. `administered_by=abc` chamado direto na API
continua sendo `400`, que e o comportamento correto do boundary; o defeito era o
frontend mandar texto parcial como se fosse um ID final.

### Transicoes de status: so as que existem

`auth.users.status` tem cinco valores e esta feature governa um par:
`active <-> suspended`. `invited` pertence ao fluxo de convite, `locked` a
protecao contra forca bruta, `deleted` a exclusao. A UI derivava a acao de
`status === "active" ? ... : ...`, entao convidados e bloqueados ganhavam
"Ativar" — um botao cujo unico desfecho possivel era `409`.

A regra virou `lib/userStatus.ts`, uma funcao pura que devolve `null` para tudo
fora do par suportado, e a tabela renderiza a partir dela. Estado desconhecido
falha fechado: nenhum botao, e um texto dizendo qual fluxo e o dono daquele
estado. Nenhuma transicao nova foi criada no backend.

### `status=deleted` saiu do contrato

Era aceito pela allowlist, documentado, e impossivel: `listUsersQuery` filtra
`u.deleted_at IS NULL` incondicionalmente, como todo leitor de `auth.users` na
plataforma, e nenhum caminho de codigo do repositorio sequer escreve esse status.
Agora e `400`. Suporta-lo seria criar gestao de usuarios apagados/anonimizados —
trabalho de retencao/LGPD que a issue exclui.

### Historico de auditoria por pessoa

A tela de detalhe de um usuario tem **Ver historico de auditoria**, que leva para
`/audit?user=<uuid>` e produz `GET /api/admin/audit/events?user_id=<uuid>`.

O filtro compara a chave canonica `admin.user:<uuid>` contra a coluna `resource`,
que os quatro produtores que agem sobre uma conta ja escreviam igual — status,
revogacao de sessao, grant e revoke de role. Ele roda no `WHERE`, **antes** do
`ORDER BY` e do `LIMIT`: um evento que e o duocentesimo mais recente da
plataforma ainda e o primeiro do historico dele, e um filtro aplicado depois do
limite (ou no browser) nunca o encontraria.
`TestPostgreSQL_UserAuditHistoryFiltersBeforeTheLimit` prova isso enterrando o
evento sob 120 outros com `limit=50`.

- **Capability:** `admin.audit.read`, a mesma da pagina global. Poder abrir o
  registro de alguem nao e poder ler a trilha, e o botao so aparece para quem
  tem as duas.
- **Semantica:** eventos feitos **sobre** a conta. `actor_user_id` responde
  outra pergunta e nao participa do filtro.
- **Membership de canal** fica no historico do **canal** (`admin.channel:<uuid>`),
  porque foi a membership do canal que mudou; a pessoa afetada continua no
  metadata. Re-chavea-la para o usuario diria que um registro de usuario foi
  alterado quando nao foi.
- **Sem busca em metadata:** nao ha filtro JSON, path, padrao nem expressao. A
  API expressa uma intencao so.
- **Indice:** `idx_admin_audit_events_resource (resource, occurred_at DESC, id DESC)`,
  migration `auth/000011`. Nenhum dos dois indices da 000008 serve: um lidera por
  `occurred_at` e nao cobre a igualdade, o outro por `actor_user_id`, que e outro
  fato.

O nome exibido no cabecalho e resolvido a parte, pelo diretorio. Se ele nao puder
ser lido — auditor sem `admin.users.read`, conta ja apagada — a pagina cai para
um rotulo neutro e **a trilha continua sendo exibida**: os eventos existem
independentemente de o nome poder ser obtido.

### Indices de paginacao: a listagem global nao e a listagem por workspace

Toda listagem do console pagina por keyset — `(created_at DESC, id DESC)` ou
`(updated_at DESC, id DESC)` — e nenhum request nomeia workspace. Isso e o que
distingue essas leituras das do chat-service: um B-tree que lidera por
`workspace_id` nao consegue produzir a ordem global, teria que ser percorrido uma
vez por workspace e mesclado.

Tres migrations cobrem as cinco listagens:

| Migration     | Indice                                                           | Serve                              |
| ------------- | ---------------------------------------------------------------- | ---------------------------------- |
| `auth/000010` | `idx_users_directory_page (created_at DESC, id DESC)`            | `GET /users`                       |
| `chat/000032` | `idx_channels_directory_page (created_at DESC, id DESC)`         | `GET /channels`                    |
| `chat/000033` | `idx_dm_conversations_directory_page (updated_at DESC, id DESC)` | `GET /conversations`               |
| `chat/000033` | `idx_workspaces_directory_page (created_at DESC, id DESC)`       | `GET /policies/{anti-spam,upload}` |

Nenhum e parcial. `workspace_id`, `type` e `status` sao todos filtros opcionais
em `/conversations`, e as duas listagens de politica precisam mostrar workspace
desabilitado — nao existe predicado que toda query carregue, e um indice parcial
deixaria de cobrir a listagem no instante em que o operador limpasse um filtro.

Os indices por workspace da `chat/000003` **continuam**: eles atendem as leituras
do chat-service, que sao um padrao de acesso diferente e nao um superado.
`TestChatMigration_KeepsWorkspaceScopedConversationIndexes` impede que alguem os
remova achando que a 000033 os substituiu.

Medido com 50 mil linhas em cada tabela: `EXPLAIN (ANALYZE, BUFFERS)` da primeira
pagina passa de `Seq Scan + top-N heapsort`, 663 e 721 buffers, para `Index Scan`
e `Index Only Scan`, 3 buffers cada.

### `next_cursor` na ultima pagina e `null`, nunca `""`

Um `paginationPayload` com `NextCursor string` serializava a ultima pagina como
`"next_cursor": ""`, enquanto o contrato publicado dizia `null`. Enquanto os dois
lados so testam truthiness ninguem percebe — e e exatamente por isso que vale
corrigir: a divergencia sobrevive ate alguem comparar por igualdade, ou tratar a
string vazia como cursor de verdade e paginar em circulo.

O campo virou `*string`, sem `omitempty`: presente e explicitamente nulo. Ter que
distinguir "acabou" de "o servidor esqueceu a chave" seria ler a ausencia de um
campo, e JSON tem valor para isso. `newPagination` e o unico ponto que constroi o
bloco, entao as cinco listagens mudam juntas, e
`TestListings_PublishNullCursorOnTheLastPage` le tambem os bytes da resposta —
depois de decodificar para `any`, `""` e `null` nao sao mais distinguiveis.

No frontend `parsePagination` passou a **recusar** `""` em vez de tolera-lo. Ela
e o boundary: aceitar as duas grafias e o que deixaria os dois lados divergirem
em silencio de novo.

### Filtros que seguem o modelo real

`administered_by` em canais e **criador ou moderador do canal**, e nao owner ou
admin do workspace. O dominio do chat nao tem owner nem admin de canal; incluir
os do workspace faria todo canal casar com o owner dele, que e outra pergunta.

`workspace_role` em usuarios e um papel de `chat.workspace_members`, aplicado com
`EXISTS` para nao duplicar a linha de quem tem varias memberships. A tela e de
plataforma e nenhum request nomeia workspace, entao o filtro le como "tem pelo
menos uma membership ativa com esse papel". E questao separada de
`platform_admin`, e as duas combinam.

### Bounds compartilhados em vez de repetidos

Os limites do RF-19 (1/60/600) sairam de `chat-service/internal/domain` para
`libs/go/platform/antispampolicy`, e o domain do chat passou a reexporta-los —
exatamente como ja fazia com `uploadpolicy` para o RF-32. Sem isso o
`admin-service` teria uma segunda copia dos numeros, livre para divergir do
`CHECK` da migration.

### Advisory lock para a invariante de ultimo administrador

A regra "a plataforma nunca fica sem administrador" e uma contagem **entre
linhas**, e nenhum row lock a expressa: sob READ COMMITTED, duas transacoes
deletando linhas diferentes veem a linha da outra ainda presente e ambas
commitam. A revogacao de papel toma `pg_advisory_xact_lock` antes de qualquer
coisa e conta os remanescentes depois do delete, dentro da transacao.

E um lock global para todas as mudancas de papel. Concessao de papel acontece
um punhado de vezes na vida de um deployment; se isso deixar de ser verdade, o
upgrade e um lock por capability, nao um check mais fino.

### O que a Slice C nao expoe, e por que

`GET /api/admin/conversations` devolve identificador, workspace, tipo, estado,
contagem de participantes, volume agregado e timestamps. Nao devolve corpo,
titulo, participante nominal, anexo, reacao, prévia nem "mensagem mais recente",
e nao existe endpoint de leitura de mensagem em lugar nenhum deste servico.

Ser Admin Master nao torna ninguem participante. `chat.dm_members` continua sendo
a unica pergunta feita para decidir quem le uma conversa, e nenhuma consulta
introduzida por esta issue le `chat.messages.body_text` —
`TestConversationQuery_NeverSelectsMessageContent` afirma isso contra o texto do
SQL, nao contra o handler.

### Politicas: so o que e configuravel de verdade

Apenas duas politicas sao alteraveis em runtime: as colunas
`chat.workspaces.message_rate_limit_per_minute` e
`chat.workspaces.max_upload_bytes`. Todo o resto que a issue lista — burst,
reacoes/minuto, uploads/intervalo, criacao de conversas, chamadas, webhooks,
tentativas invalidas, duracao de bloqueio, MIME, comportamento do scanner — e lido
do ambiente no boot pelo servico que aplica, ou nao existe no dominio.

O console **nomeia** os controles que sao reais e fixos (teto do gateway,
verificacao de malware, concorrencia de upload) em vez de oferecer um campo que
gravaria um numero que ninguem le. Nao foi criado editor generico de
configuracao, e nenhuma variavel de ambiente e exposta.

---

## Operacao

### Conceder o primeiro papel administrativo pelo console

Nao da: `POST /api/admin/users/{id}/admin-roles` exige `admin.superuser`. O
primeiro administrador continua nascendo de uma concessao no banco, feita por
quem ja tem acesso ao PostgreSQL — o procedimento esta em
[`task-admin-console-foundation.md`](./task-admin-console-foundation.md). A partir
do segundo, o console resolve.

### Desativar uma conta que vem do Keycloak

Desativar no console bloqueia o acesso **ao NChat** e encerra as sessoes ativas.
Nao desativa a conta no provedor de identidade, e o console diz isso na tela. Nao
existe reset de senha do Keycloak aqui, nao existe edicao de atributo cuja fonte
da verdade seja o IdP, e nao existe criacao de usuario no IdP. Sincronizacao
destrutiva bidirecional nao foi implementada e nao deve ser.

### Propagacao de uma mudanca de politica

- **Anti-spam:** o chat-service cacheia a politica por workspace por 5 segundos.
  A instancia que serviu a mudanca so e invalidada quando a alteracao vem por ela
  — uma alteracao feita pelo Admin Console chega as instancias do chat dentro do
  TTL. Sem restart.
- **Upload:** sem cache. O file-service le a coluna na mesma consulta que
  autoriza o destino do upload, uma vez por request. Vale no proximo upload.

### Quando uma tela responde 409

A operacao perdeu uma corrida ou a invariante recusou: o objeto ja estava no
estado pedido, ou a revogacao deixaria a plataforma sem administrador, ou a conta
alvo esta suspensa. Recarregar a listagem mostra o estado real.

---

## Validacao

A semantica do `ILIKE` — `%`, `_` e `\` tratados literalmente — e provada contra
um PostgreSQL real, com registros-isca que seriam falsos positivos se qualquer um
deles ainda fosse curinga. Um teste unitario da string produzida nao basta: o
`%q` do Go renderiza um unico `0x5c` como dois caracteres, entao a comparacao de
strings parece provar qualquer uma das duas hipoteses e nao prova nenhuma. Para
rodar:

```bash
docker compose --env-file infra/compose/.env.dev -f infra/compose/compose.dev.yml up -d postgres
psql -h localhost -U nchat -d postgres -c "CREATE DATABASE nchat_test"
psql -h localhost -U nchat -d nchat_test \
  -c "CREATE EXTENSION IF NOT EXISTS citext; CREATE EXTENSION IF NOT EXISTS pgcrypto"
cd services/admin-service && \
  ADMIN_TEST_DATABASE_URL='postgresql://nchat:nchat_dev_password_change_me@localhost:5432/nchat_test?sslmode=disable' \
  go test ./internal/storage/ -run PostgreSQL -count=1
```

A suite e opt-in: sem `ADMIN_TEST_DATABASE_URL` ela e pulada, e um `go test ./...`
verde sem a variavel nao prova nada sobre o `ILIKE`.

```bash
# focado
cd services/admin-service && go test ./...
cd apps/admin-web && pnpm test && pnpm typecheck && pnpm lint

# gates do repositorio
make format-check
make lint
make test
make build
make ci
```

`pnpm test:coverage:go:check` aplica 90% por modulo Go; `admin-web` aplica 90%
nos quatro eixos pelo `vite.config.ts`.

O suite de link safety do `chat-service` so roda contra um PostgreSQL real
(`LINK_SAFETY_TEST_DATABASE_URL`). Sem ele, a cobertura do `chat-service` fica em
~86% — condicao pre-existente e independente desta issue.

---

## Fora de escopo

- SMTP, Keycloak, LiveKit, secrets management, Health Center;
- editor generico de configuracao e exposicao de variavel de ambiente;
- criacao de usuario no Keycloak e reset de senha do IdP;
- leitura administrativa de mensagens, em qualquer forma;
- hard delete de canal, apagamento de historico;
- mutacao de membership de **grupo privado de DM** (o console nao tem autoridade
  sobre conversa que nao pode ler; `chat.dm_members` continua sendo a unica
  pergunta feita);
- criacao de moderador de canal (o papel inserido e sempre `member`);
- framework de integrations/webhooks — nao existe dominio seguro para isso hoje.
