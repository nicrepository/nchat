# Participantes de reuniões — issue #1054

Complementa [Meeting lifecycle](chat-meetings.md). Não implementa interface de
agendamento, RSVP, notificações, realtime ou integrações externas.

## Contratos

Todas as rotas usam Bearer JWT e validação da sessão pelos middlewares existentes.
Workspace vem de `GetDefaultWorkspace`; actor vem da sessão. Query/body não podem
escolher essas autoridades. Sucesso usa `{"data": ...}`; erro usa o envelope
compartilhado. Respostas do handler incluem `Cache-Control: no-store`.

| Método | Rota                                                     | Sucesso                      |
| ------ | -------------------------------------------------------- | ---------------------------- |
| GET    | `/api/chat/meeting-candidates/{kind}/{conversationID}`   | 200, página de candidatos    |
| GET    | `/api/chat/meetings/{meetingID}/participants`            | 200, página do roster        |
| POST   | `/api/chat/meetings/{meetingID}/participants`            | 201, participante criado     |
| PATCH  | `/api/chat/meetings/{meetingID}/participants/{userID}`   | 200, participante persistido |
| DELETE | `/api/chat/meetings/{meetingID}/participants/{userID}`   | 204, sem corpo               |
| POST   | `/api/chat/meetings/{meetingID}/participants/invite-all` | 200, quantidade adicionada   |

IDs são UUIDs não zero. `kind` aceita somente `dm` e `channel`. Corpos POST/PATCH
exigem `application/json`, parsing estrito e limite existente de 64 KiB. Campos
não previstos são recusados. GET/DELETE não exigem Content-Type.

### Descoberta contextual

Exemplo: `GET /api/chat/meeting-candidates/channel/{conversationID}?q=ana&limit=25`.

```json
{
  "data": {
    "items": [{ "user_id": "22222222-2222-4222-8222-222222222222", "display_name": "Ana" }],
    "next_cursor": "22222222-2222-4222-8222-222222222222"
  }
}
```

Requer elegibilidade para `meeting:create` **na conversa resolvida pelo servidor**.
Não depende de uma reunião existente, nem concede acesso por join policy. Conta,
workspace e membership do ator devem estar ativos, e ele deve ter acesso atual
à conversa. Canal privado/DM sem acesso e conversa de outro workspace recebem 404. Guest não pode descobrir candidatos para criar reuniões (403).

A composição vem de `channel_members` ou `dm_members` ativos, combinada com
membership ativa no workspace e conta ativa/não excluída. Somente
owner/admin/moderator/member são convidáveis. O ator é excluído, pois será o
organizer. Nenhum e-mail, avatar, status administrativo ou outro campo pessoal
é retornado. Nome prefere `full_name` não vazio, com fallback para `display_name`.

- DM individual: retorna o outro membro elegível persistido; removido/inativo
  não aparece. O cliente da #1055 pode pré-selecioná-lo.
- Grupo: retorna membros elegíveis e permite seleção parcial pelo cliente.
- Canal: retorna membros explícitos elegíveis. Acesso implícito a canal público
  autoriza a consulta pelo predicado canônico, mas não seleciona todo o workspace.
  A consulta nunca convida nem pré-seleciona todos automaticamente.

`q` é prefixo case insensitive de `display_name` ou `full_name`, com trim e até
100 caracteres, sem controles. Segue o padrão de prefixo de `member_store.go`:
`%` e `_` são literais; não há wildcard nem nova normalização de acentos.

### Paginação e roster

Ambos os GETs aceitam `limit` (default 50, intervalo 1–100) e `cursor` (UUID do
último usuário devolvido). A ordem é `user_id ASC`, com keyset estrito `>`;
`next_cursor` só aparece quando existe outra linha. `items` vazio é `[]`.
Roster não aceita pesquisa não vazia. Parâmetros desconhecidos, repetidos,
limites inválidos e cursor inválido recebem 400.

Cada página reautoriza o ator. Não existe snapshot entre requests: inserções
com ID anterior ao cursor exigem reiniciar a listagem; remoções não deslocam
os demais itens. Trocar conversa ou prefixo exige reiniciar o cursor.

Roster exige `meeting:view`, independentemente da permissão para descobrir a
conversa. Retorna `user_id`, `role`, `rsvp`, `invited_at`, `updated_at`. Ele é
histórico: perder elegibilidade não remove silenciosamente linhas existentes.
A consulta HTTP é limitada; o `GetMeeting` interno legado continua compatível.

### Mutação individual

Adicionar: `POST .../participants`:

```json
{ "user_id": "22222222-2222-4222-8222-222222222222", "role": "attendee" }
```

Role omitida assume `attendee`; somente `attendee` e `cohost` são atribuíveis.
Sucesso retorna os mesmos campos de uma linha do roster, com RSVP `pending`.
Duplicidade retorna 409, sem sobrescrever role, RSVP ou timestamps. Após timeout,
o cliente pode consultar o roster; repetir o POST não cria outra linha.

Alterar: `PATCH .../participants/{userID}` com `{"role":"cohost"}`. Role é
obrigatória. Repetição da role atual é no-op e preserva RSVP e timestamps, após
reautorizar. Participante ausente recebe 404.

Remover: `DELETE .../participants/{userID}`. Ausente, inclusive numa repetição,
recebe 404. Recurso invisível usa o mesmo erro. Nenhuma operação muda membership
de canal/DM. Não há sucesso parcial.

Todas exigem `meeting:manage_participants` e estado scheduled/in_progress.
Organizer administra attendees e cohosts; cohost administra somente attendees.
Somente organizer concede/revoga cohost. Organizer nunca é removido, rebaixado,
substituído ou atribuído por essas rotas. Sua linha e RSVP accepted permanecem.

### Convidar todos explicitamente

`POST .../participants/invite-all` com corpo `{}`. Retorna:

```json
{ "data": { "added": 12 } }
```

Exige gestão de participantes e acesso atual à conversa associada. Ator que vê
a reunião mas perdeu acesso à conversa recebe 403; a descoberta direta da
conversa continua respondendo 404. Não funciona
em reunião sem conversa. Resolve membros no servidor, exclui organizer e
inelegíveis, convida como attendees e preserva todos os participantes existentes,
inclusive cohosts. Não recebe IDs, role ou filtro do cliente.

O limite operacional é **500 candidatos elegíveis além do organizer**, incluindo
membros já convidados. O servidor lê no máximo 501 candidatos; excesso retorna
400 sem escrever. O cliente deve oferecer seleção parcial nesse caso. Não deve
coletar páginas ilimitadas para contornar esse limite. Listagem continua
paginada em canais maiores. A operação é uma única transação e um único INSERT
em lote, sem sucesso parcial ou divisão silenciosa em lotes.

Repetir reavalia a composição atual e adiciona apenas os ausentes (`added: 0`
se nada mudou). Novos membros podem ser incluídos numa repetição posterior;
esta é semântica de conjunto atual, não replay de snapshot/idempotency ledger.
Um erro interno/timeout não implica que o commit falhou: consulte o roster ou
repita a intenção de convidar os membros atuais.

## Erros e limites

| HTTP | Código              | Condição                                                           |
| ---- | ------------------- | ------------------------------------------------------------------ |
| 400  | bad_request         | JSON, UUID, role, parâmetros, inelegibilidade ou bulk excessivo    |
| 401  | unauthorized        | autenticação/sessão ausente ou inválida                            |
| 403  | forbidden           | reunião visível, mas ação não autorizada                           |
| 404  | not_found           | recurso inexistente, invisível ou de outro workspace; alvo ausente |
| 409  | conflict            | participante duplicado ou reunião terminal                         |
| 415  | bad_request         | Content-Type incorreto em POST/PATCH                               |
| 429  | rate_limited        | orçamento excedido, Retry-After: 60                                |
| 500  | internal_error      | falha interna, sem detalhes de banco                               |
| 503  | service_unavailable | dependência de admissão indisponível                               |

O rate limiter compartilhado entre réplicas aplica limites por usuário e por
chave de ação, com orçamentos independentes:

| Ação                     | Chave                        | Limite por usuário    |
| ------------------------ | ---------------------------- | --------------------- |
| Descoberta de candidatos | `meeting_candidates`         | 60 requisições/minuto |
| Leitura do roster        | `meeting_roster`             | 20 requisições/minuto |
| Adição de participante   | `meeting_participant_add`    | 20 requisições/minuto |
| Alteração de papel       | `meeting_participant_role`   | 20 requisições/minuto |
| Remoção de participante  | `meeting_participant_remove` | 20 requisições/minuto |
| Convite de todos         | `meeting_participants_all`   | 20 requisições/minuto |

As cinco ações de roster não compartilham um único orçamento de 20
requisições/minuto; consumir o limite de uma ação não consome o das demais.
Falha do limiter recusa o request. O teto HTTP de 64 KiB também se aplica às
novas escritas. UUIDs não concedem autorização; erros de inelegibilidade não
identificam qual condição privada falhou.

## Consistência, segurança e implementação

Reutiliza `AuthorizeMeeting`, `AuthorizeMeetingParticipantChange`,
`MeetingInviteeEligible`, `withMeeting` e `lockMeetingAuthority`. Não há novo
domínio, mecanismo de autorização ou lock distribuído. Não há migration:
PKs `(meeting_id,user_id)`, `(workspace_id,user_id)` e memberships por conversa
atendem às consultas por igualdade e à paginação por UUID.

Todas as mutações do roster serializam na reunião `FOR UPDATE`. Ordem dos fatos:
reunião → workspace → conversa → memberships de workspace → contas → memberships
da conversa, com usuários em ordem determinística dentro de cada tabela.

Bulk trava primeiro reunião, workspace e conversa; resolve até 501 candidatos;
só então trava os fatos de todos os usuários em lote na ordem canônica e
reautoriza. Nunca trava a conta do ator antes de selecionar/travar os demais
membros. Revogações anteriores são observadas; posteriores esperam o commit.
Se um candidato perde elegibilidade antes do lock, é excluído do bulk. O ator
que perde autoridade faz a transação falhar. A seleção contextual usa as mesmas
travas de fatos do ator para manter autorização durante sua consulta.

`meetings.version` continua sendo versão de metadados. Roster não altera versão
nem updated_at da reunião, não usa If-Match e não emite ETag. Create/update/cancel
mantêm seus contratos. A mudança deliberada da #1054 é restringir **novos
convites de guests**, inclusive no create pela policy compartilhada; leitura e
join de convidados históricos continuam com a semântica da #1052.

### Threat model

| Ameaça / ativo                                             | Defesa no fluxo                                                                                |
| ---------------------------------------------------------- | ---------------------------------------------------------------------------------------------- |
| BOLA, cross-workspace, enumeração de reunião/conversa      | sessão server-side, reunião scoped, acesso canônico, 404 privado                               |
| BFLA, escalada para cohost, alteração de organizer         | capabilities e transições do domínio dentro da transação, constraints existentes               |
| Guest, conta inativa/excluída, membership revogada         | elegibilidade compartilhada, filtros SQL e revalidação dos fatos bloqueados                    |
| TOCTOU, suspensão, cancel concorrente, convites duplicados | locks PostgreSQL na ordem canônica, PK e serialização da reunião                               |
| Abuso de busca, payloads excessivos, canal grande          | rate limiter fechado, body limitado, prefixo literal, keyset 100, bulk 500                     |
| Exposição de identidade e dados privados                   | somente ID/nome na descoberta, erros genéricos, no-store, instrumentação existente sem payload |
| Falha no meio do lote / resposta perdida                   | rollback atômico; repetição de conjunto não sobrescreve RSVP/roles                             |

### Evidências reproduzíveis

`TestMeetingSelectionConstantQueriesPostgreSQL` conta statements reais, incluindo
os statements dentro de pgx.Batch, comparando 2 e 322 candidatos. A contagem
é de **7 statements por página e 12 por bulk** em ambos os conjuntos; não é um
benchmark de latência. Os testes de escala
percorrem páginas de candidatos e roster, verificam limite e unicidade e recusam
bulk de mais de 500 candidatos sem alterar estado.

A família `TestMeetingSelection*PostgreSQL` está registrada no owner
`Tests / Go Coverage` em `scripts/ci/link-safety-postgres-coverage.sh`. Reutiliza
as fixtures isoladas de Meeting e o grafo de locks PostgreSQL para sincronizar
concorrência, sem sleeps artificiais. Inclui rollback após INSERT real, revogação,
perda de acesso, bulk simultâneo e cancel em ambas as ordens. Testes existentes
das #1052/#1053 permanecem na regressão.

Para #1055: carregar candidatos por página, guardar seleção parcial no draft e
enviar os IDs escolhidos no create existente. O organizer é implícito. A ação de
convidar todos é explícita sobre uma reunião criada; create e bulk são duas
transações distintas. Falha do bulk mantém a reunião criada para correção/retry;
não apresentar o agendamento com todos como concluído antes do sucesso do bulk.

### Rastreabilidade dos critérios de aceite

| Critérios da #1054 | Evidência                                                                                                               |
| ------------------ | ----------------------------------------------------------------------------------------------------------------------- |
| 1–4, 7             | SelectionContexts, SelectionDirectDM, BulkSemantics; service/HTTP e prefixo                                             |
| 5–6                | SelectionConstantQueries, SelectionLargeChannel, BulkLimit                                                              |
| 8                  | ParticipantRoutes, ParticipantInputs, ParticipantsPostgreSQL                                                            |
| 9–11               | SelectionContexts, CurrentEligibility, APICreateRefusalsAreAtomic                                                       |
| 12–14              | ParticipantsPostgreSQL, ConcurrentParticipantChanges, policies de domínio                                               |
| 15–16              | BulkConcurrent, BulkSimultaneous, BulkRevocation, BulkRollback, RoleSerialization e regressão de revogações #1052/#1053 |
| 17                 | PrivateErrors, BulkLostConversation, SelectionContexts e policies                                                       |
| 18, 22             | somente backend, contratos e testes de #1054; nenhuma UI ou integração                                                  |
| 19–20              | regressão TestMeeting\*, unit, PostgreSQL real e race detector                                                          |
| 21                 | gates locais e limitações de ferramentas registradas no relatório de entrega                                            |
