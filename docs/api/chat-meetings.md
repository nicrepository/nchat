# Meeting lifecycle API — issue #1053

## Contrato HTTP

| Método | Rota                                    | Resultado                        |
| ------ | --------------------------------------- | -------------------------------- |
| POST   | `/api/chat/meetings`                    | `201` nova criação; `200` replay |
| PATCH  | `/api/chat/meetings/{meetingID}`        | `200` detalhes persistidos       |
| POST   | `/api/chat/meetings/{meetingID}/cancel` | `200` cancelamento ou no-op      |

Todas exigem Bearer JWT válido, sessão ativa e `Content-Type: application/json`.
O workspace é resolvido no servidor por `GetDefaultWorkspace`, como nas rotas de
canais e DMs. Não há workspace selecionável no path, query ou body. A autorização
transacional ainda exige membership elegível nesse workspace; o resolver não
concede acesso. O ID da reunião não concede acesso.

O envelope é o compartilhado: `{"data": {...}}` ou
`{"error": {"code": "...", "message": "..."}}`.

### Create

Exige exatamente um `Idempotency-Key` com 1–128 caracteres ASCII de
`[A-Za-z0-9._:-]`. Chave ausente, vazia, repetida ou fora desse formato recebe
`400`. A chave não é uma credencial.

```json
{
  "title": "Planejamento",
  "description": "Pauta da reunião",
  "starts_at": "2026-11-03T10:00:00-03:00",
  "ends_at": "2026-11-03T11:00:00-03:00",
  "timezone": "America/Sao_Paulo",
  "join_policy": "invited_only",
  "conversation": {
    "kind": "channel",
    "id": "33333333-3333-4333-8333-333333333333"
  },
  "participants": [
    {
      "user_id": "22222222-2222-4222-8222-222222222222",
      "role": "cohost"
    }
  ]
}
```

`description`, `join_policy`, `conversation` e `participants` são opcionais.
Join policy omitida ou vazia assume `invited_only`, conforme o domínio.
Conversation aceita `channel` e `dm`. `conversation_members` exige uma conversa.
O organizer é o actor autenticado, incluído uma única vez como `organizer` com
RSVP `accepted`. Participantes recebem RSVP `pending`; role omitida assume
`attendee`. Somente `attendee` e `cohost` podem ser atribuídos. Duplicatas,
organizer repetido e UUID zero de participante recebem `400`.

Todos os convidados devem ter conta ativa, membership ativa no mesmo workspace
e acesso à conversa quando associada. Unknown user, conta suspensa ou removida,
outro workspace e falta de acesso à conversa produzem o mesmo `400 invalid meeting`,
sem identificar o alvo. Uma falha desfaz reunião, organizer, convidados e claim.

O domínio normaliza título, descrição e timezone por trim; valida título com
1–120 code points e descrição com até 5000, controles, intervalo estritamente
positivo e timezone IANA. Não há teto de duração. Horários são RFC 3339 com offset
explícito ou `Z`; strings civis sem offset são recusadas. Instantes persistem em
UTC com precisão de microssegundos; timezone civil permanece independente.

Replay retorna a identidade **da reunião original** e sua representação atual,
como na criação de canais: uma reunião editada ou cancelada retorna seus dados
atuais e ETag atual. Não se restaura um snapshot anterior. A resposta inclui
`Idempotency-Replayed: true` apenas no replay e `Location` com a identidade do
recurso. Esta issue não implementa GET nem listagem nessa localização.

A mesma chave com intenção diferente recebe `409 idempotency_key_reused`.
O replay revalida acesso na transação e pode retornar `404` após revogação.
Ele não reinsere participantes nem altera versão ou timestamps.

### Update

Exige exatamente um ETag forte em `If-Match`, por exemplo `If-Match: "1"`.
A sintaxe é uma versão decimal positiva entre aspas, sem zeros iniciais, no
intervalo de `int64`. Wildcard, weak ETag, lista de ETags e cabeçalho repetido
são recusados. Ausência recebe `428`; malformação recebe `400`.

```json
{ "title": "Novo planejamento", "description": "" }
```

PATCH aceita somente `title`, `description`, `starts_at`, `ends_at`, `timezone`
e `join_policy`. Campo omitido preserva seu valor; string vazia é um valor
fornecido (sujeito à validação do domínio); `description: ""` limpa a descrição.
`null`, corpo `null` e patch vazio são recusados. Conversation e roster são
imutáveis nessa operação. Cohost edita detalhes, mas somente organizer pode
alterar join policy, por `AuthorizeMeetingUpdate`.

O service encaminha o PATCH sem ler a reunião ou o roster. O storage obtém a
reunião com `FOR UPDATE`, verifica acesso e capability básica de update e compara
a versão esperada. Só então mescla os campos presentes sobre os detalhes dessa
linha bloqueada, valida a join policy resultante com `AuthorizeMeetingUpdate`,
aplica `Meeting.Update` e escreve com `WHERE version = $expected`, tudo na mesma
transação. Actor invisível recebe `404`; actor visível sem capability recebe
`403`; actor autorizado com versão antiga recebe `412 stale_version` antes da
validação da alteração de join policy. A leitura, a mesclagem e o CAS usam a
mesma versão persistida.
Dois updates com a mesma versão permitem uma única escrita. Estado terminal
não permite edição. Retry após um commit pode receber `412`; update não possui
ledger de idempotência.

### Cancel

Exige `If-Match` no mesmo formato e corpo JSON vazio `{}`.
Somente organizer autorizado cancela, pela capability de domínio. A transição
permitida é `scheduled → cancelled`. Reunião em andamento ou encerrada não pode
ser cancelada (`409`). Não há hard delete ou remoção de roster.

Uma transição real exige a versão corrente. Se já estiver `cancelled`, o domínio
retorna no-op inclusive com a versão antiga enviada na primeira tentativa.
A autorização é reavaliada antes desse no-op. Versão e `updated_at` permanecem
iguais após o primeiro cancelamento.

### Respostas e erros

Sucessos incluem `id`, `workspace_id`, `organizer_user_id`, `conversation`
(objeto ou null), `title`, `description`, `starts_at`, `ends_at`, `timezone`,
`join_policy`, `status`, `version`, `created_at` e `updated_at`.
Não incluem `room_key`, hash, chave de idempotência ou colunas internas.
`ETag: "N"` representa a versão da linha Meeting; roster/RSVP não são incluídos
nessa representação. `Cache-Control: no-store` protege respostas privadas.

| HTTP | Código                   | Condição                                                   |
| ---- | ------------------------ | ---------------------------------------------------------- |
| 400  | `bad_request`            | JSON, campo desconhecido, UUID, chave ou dados inválidos   |
| 401  | `unauthorized`           | autenticação/sessão inválida ou ausente                    |
| 403  | `forbidden`              | recurso visível, capability ausente; create não autorizado |
| 404  | `not_found`              | recurso inexistente, de outro workspace ou invisível       |
| 409  | `idempotency_key_reused` | chave de create com intenção diferente                     |
| 409  | `conflict`               | conflito de estado do domínio                              |
| 412  | `stale_version`          | versão obsoleta para uma escrita real                      |
| 415  | `bad_request`            | Content-Type diferente de application/json                 |
| 428  | `precondition_required`  | If-Match ausente no update/cancel                          |
| 429  | `rate_limited`           | orçamento excedido; Retry-After: 60                        |
| 500  | `internal_error`         | falha interna, sem mensagem SQL                            |
| 503  | `service_unavailable`    | dependência necessária indisponível                        |

O limite compartilhado existente aplica 10 creates/minuto e 20 updates/minuto,
20 cancels/minuto por usuário, com contadores distintos por operação e iguais
em todas as réplicas. Falha do limiter recusa a operação. O body tem o teto
operacional já usado no chat, **64 KiB**. Não foi criada cota de participantes:
o teto de parsing também limita a quantidade de identidades recebidas.

## Persistência e decisões

| Decisão                                             | Razão                                                                      | Alternativa descartada                                      |
| --------------------------------------------------- | -------------------------------------------------------------------------- | ----------------------------------------------------------- |
| Rotas sem workspace no path                         | Convenção real de canais/DMs, workspace server-side                        | Nova convenção de workspace selecionável                    |
| Claim dedicada PostgreSQL                           | Reinício, réplicas e timeout dependem de autoridade durável                | Mutex/cache local e novo requisito Valkey para idempotência |
| PK `(workspace_id, actor_user_id, idempotency_key)` | Namespace da operação é a tabela de create Meeting                         | Chave global compartilhada entre usuários                   |
| `INSERT ON CONFLICT DO NOTHING RETURNING`           | Unique constraint arbitra requests simultâneas                             | SELECT seguido de INSERT e UPDATE artificial                |
| FK de meeting diferida                              | Claim antecede recurso; commit exige ambos                                 | Claim órfã ou commit separado                               |
| SHA-256 de intenção normalizada                     | Defaults, UUIDs, UTC/microssegundos e participantes ordenados são estáveis | Hash do JSON bruto                                          |
| If-Match e versão existente                         | CAS e estado já são autoridades da #1052                                   | Timestamp como controle de concorrência                     |
| Fatos de todos os convidados em lote                | Preserva ordem de locks entre tabelas e UUIDs                              | AddMeetingParticipant em transações posteriores             |
| Replay do recurso atual                             | Mesmo padrão de canais, representação coerente com estado commitado        | Snapshot HTTP duplicado no ledger                           |

A claim, reunião e roster compartilham uma transação. Claims não têm expiração
nem limpeza automática nesta issue: timeout de resposta é recuperável enquanto
recurso e claim existirem. A FK faz cascade somente quando a reunião é removida
pelos fluxos preexistentes; cancelar nunca remove nada.

Fingerprint usa JSON de uma **struct tipada**, não do payload HTTP: marcador
`meeting-create/v1`, MeetingDetails normalizados, conversation canônica e
convidados com UUID canônico/role default ordenados por user ID. A ordem dos
participantes não tem significado. IDs gerados, timestamps de execução, chave,
actor e workspace não entram no hash; os dois últimos compõem a namespace.

Lock order preservada: claim (somente create), Meeting (quando já existe),
workspace, conversation, workspace memberships por UUID, accounts por UUID,
conversation memberships por UUID. No create a identidade ainda não é visível;
após proteger os fatos, a transação insere sua própria reunião. Não há consulta
de rede, retry de deadlock ou chamada a outro serviço dentro da transação.

Reutiliza `NewMeeting`, `Meeting.Update`, `Meeting.Transition`,
`AuthorizeMeeting`, `AuthorizeMeetingUpdate`, `AuthorizeMeetingParticipantChange`,
`MeetingInviteeEligible` e `PGXMeetingStore`. Não altera as regras da #1052.

## Threat model e observabilidade

Metadados, roster, vínculos privados e versões atravessam client → auth middleware
→ handler → service → domínio/storage → PostgreSQL. Cada fronteira tem controles:

- Client → API: autenticação, parsing estrito, body limitado, rate limit e DTO
  por allowlist impedem mass assignment e consumo sem orçamento.
- Workspace/conversation → meeting: fatos protegidos na transação, FKs compostas
  existentes e erros uniformes impedem BOLA, cross-workspace e enumeração privada.
- Actor → alvo interno: elegibilidade de todos é decidida sob lock; a falha não
  identifica qual usuário existe ou perdeu acesso.
- Chave/versão → escrita: namespace, hash, unique constraint, FK, CAS e transição
  de domínio impedem replay com outra intenção, duplicação e lost update.

A instrumentação HTTP compartilhada mede método, rota parametrizada e status:
create novo/replay são 201/200, conflito de chave é 409, stale é 412 e falhas
transacionais são 500. Não se adicionam logs de body, título, descrição, chave,
convite, token ou SQL; nem IDs privados em labels. Não há infraestrutura nova
de dashboards/eventos/outbox.

Testes PostgreSQL dessa família pertencem ao gate **Tests / Go Coverage**, junto
à #1052, explicitamente nomeados em `link-safety-postgres-coverage.sh`.
Playwright é não aplicável: não há UI ou consumidor web dessas novas rotas.
