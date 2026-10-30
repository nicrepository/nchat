# Issue #1052 — domínio de reuniões

`chat.meetings` é a fonte de verdade das reuniões criadas no NChat. Calendários
externos, o card da timeline e salas LiveKit são projeções futuras (épica #681)
e nunca definem a identidade ou a existência de uma reunião.

## Onde vive

No `chat-service`, porque reuniões dependem de workspaces, conversas e
memberships que já pertencem a ele.

| Responsabilidade               | Local                                    |
| ------------------------------ | ---------------------------------------- |
| Entidade, validação, estados   | `internal/domain/meeting.go`             |
| Capabilities (decisão única)   | `internal/domain/meeting_policy.go`      |
| Persistência, transação, fatos | `internal/storage/meeting_store.go`      |
| Schema                         | `migrations/chat/000070_meetings.up.sql` |

O store lê a reunião e os fatos do ator na mesma transação e delega a decisão a
`domain.AuthorizeMeeting`. Ele não decide regra de negócio. A #1053 acrescentou serviço e HTTP para create/update/cancel; a #1054 expõe
gestão e seleção contextual sobre estes contratos.

## Modelo

`Meeting`: `id` (UUID interno), `workspace_id`, conversa opcional (`channel` ou
`dm`), `organizer_user_id`, `title`, `description`, `starts_at`, `ends_at`,
`timezone`, `status`, `join_policy`, `room_key`, `version`, `created_at`,
`updated_at`.

`MeetingParticipant`: `meeting_id`, `user_id`, `role`
(`organizer`/`cohost`/`attendee`), `rsvp`
(`pending`/`accepted`/`declined`/`tentative`), `invited_at`, `updated_at`.
Apenas usuários internos; não há convidados externos.

Invariantes garantidas pelo banco, além da validação no domínio:

- a conversa pertence ao mesmo workspace (FKs compostas
  `(workspace_id, channel_id|dm_conversation_id)`);
- existe exatamente um organizer, que também é a linha `organizer` do roster
  (FK diferida `(id, organizer_user_id, 'organizer')` + índice único parcial).
  Essa linha não pode ser removida nem rebaixada, e uma reunião sem ela não é
  commitada;
- o RSVP do organizer é sempre `accepted`;
- `conversation_members` exige uma conversa associada;
- `ends_at > starts_at`, sem teto de duração (nenhum requisito o define); título
  com 1–120 code points; descrição com até 5000; status, policy, role e RSVP vêm
  de conjuntos fechados.

Campos mutáveis (`MeetingDetails`): título, descrição, horários, timezone e join
policy. São imutáveis: id, workspace, organizer, conversa, `room_key` e
`created_at`. Transferir o papel de organizer está fora de escopo.

`room_key = "meeting:<id>"` é estável e não é segredo. Ele não autoriza nada. A
#1060 deriva a sala LiveKit a partir dele, com namespace de ambiente no
media-service.

## Timezone

`starts_at`/`ends_at` são instantes absolutos (`timestamptz`), normalizados para
UTC com precisão de microssegundos, que é o que o PostgreSQL guarda. Assim, o
valor validado é idêntico ao persistido. `timezone` é um nome IANA validado
contra a base embutida (`time/tzdata`). São recusados `""`, `Local`, offsets
(`-03:00`) e nomes inexistentes. O timezone é metadado de apresentação: nunca é
derivado de um offset e nunca reinterpreta os instantes.

Esta camada recebe somente instantes, então horários civis inexistentes ou
ambíguos (gap e overlap de DST) não chegam aqui. O instante já define qual
ocorrência é a correta. Se uma API futura aceitar horário civil, ela deverá
rejeitar horários inexistentes e exigir desambiguação nos ambíguos antes de
chamar o domínio. Os valores lidos do banco são devolvidos em UTC,
independentemente do `TimeZone` da sessão ou do processo.

## Estados

```text
scheduled ──start──▶ in_progress ──end──▶ ended
    └──────cancel──▶ cancelled
```

Somente transições explícitas mudam o status. Alcançar `ends_at` não encerra
a reunião, e não há scheduler. `in_progress → cancelled` é recusado (a reunião
deve ser encerrada). Estados terminais não têm saída nem aceitam edição,
convite, RSVP ou ingresso. Repetir a transição já aplicada é um no-op idempotente,
mesmo com versão antiga: o estado pedido já vale e nada é escrito. Qualquer
mudança real exige a versão atual.

## Versionamento

`version` começa em 1 e só avança em escrita da linha da reunião (edição e
transição). A escrita bloqueia a linha (`FOR UPDATE`), confere a versão no
domínio e repete o predicado `version = $n` no `UPDATE`. Uma versão obsoleta
resulta em `ErrMeetingStaleVersion`, sem efeito. Mudanças no roster serializam
na mesma trava e não alteram `version`. O RSVP usa `FOR SHARE`, então respostas
concorrentes não se bloqueiam, mas não passam por um cancelamento em curso.

`version` é a versão da linha da reunião, não uma revisão do agregado: roster e
RSVP mudam sem alterá-la.

## Concorrência com revogação e leitura coerente

Uma escrita (criação, edição, transição, roster, RSVP) trava `FOR SHARE` os
fatos em que a decisão se apoia, do ator e, ao convidar ou mudar alguém, do alvo:
workspace, membership, conta e, se a reunião tem conversa, o canal e o
`channel_members` ou a DM e o `dm_members`. A decisão usa só as linhas que esses
mesmos statements devolveram: uma linha que surge depois do seu statement não foi
travada e não concede nada (a escrita é recusada; a #1052 não exige aceitá-la).
Revogar é `UPDATE` ou `DELETE` de uma linha travada, o que conflita com
`FOR SHARE` (`FOR KEY SHARE` deixaria passar uma mudança de status ou de role).
Assim, uma revogação que commita antes é a versão que o lock devolve, e a escrita
é recusada sem efeito; uma que chega depois espera o commit da escrita.

A ordem é a da migration 000061, seguida pela invalidação de conta do
auth/admin (`chat.lock_user_ownership_conversations`): reunião → workspace →
conversa → memberships de workspace → contas → participações na conversa. Dentro
de uma tabela, ator e alvo são travados em ordem de id, não ator primeiro, para
que duas operações com papéis trocados usem a mesma ordem. Travar a conta antes
da conversa formava ciclo com a suspensão, que trava as conversas privadas do
usuário e depois a conta. Inserir um `channel_members` ou `dm_members` já trava a
conversa no commit, então espera a escrita; a janela de membership nova vale
para a de workspace, que numa reunião sem conversa é o único fato.

Leituras (`GetMeeting`, `AuthorizeMeetingJoin`) não travam esses fatos: devolvem
o que um ator autorizado via naquele instante. Elas travam a linha da reunião
`FOR SHARE` até terminar. Como toda mudança de metadados ou de roster pega
`FOR UPDATE` nessa linha, a reunião e o roster devolvidos são do mesmo instante.

## Join policies e capabilities

Ver a reunião é a precondição de toda capability sobre uma reunião existente e
exige membership elegível (workspace ativo, membro ativo, conta ativa e não
removida). Sobre isso:

| Policy                        | Quem vê/ingressa além do roster                     |
| ----------------------------- | --------------------------------------------------- |
| `invited_only` (default)      | ninguém                                             |
| `conversation_members`        | quem lê a conversa associada agora                  |
| `workspace_members_with_link` | membros do workspace (não guests) que têm o id/link |

O link é o id da reunião. A policy não concede listagem nem token de convite. Os
mecanismos de distribuição ficam com as sub-issues de UX.

| Capability                    | Organizer | Cohost     | Attendee | Admitido pela policy |
| ----------------------------- | --------- | ---------- | -------- | -------------------- |
| `meeting:update` (+start/end) | sim       | sim¹       | não      | não                  |
| `meeting:cancel`              | sim       | não        | não      | não                  |
| `meeting:manage_participants` | sim       | attendees² | não      | não                  |
| `meeting:join`                | sim       | sim        | sim      | sim                  |
| `meeting:rsvp` (só a própria) | não³      | sim        | sim      | não                  |

¹ Somente o organizer altera a join policy. ² Somente o organizer concede ou
revoga cohost. Ninguém remove nem rebaixa o organizer, e o papel `organizer`
nunca é atribuído. ³ O RSVP do organizer é fixo em `accepted`.

`meeting:create` exige papel workspace-wide (guests excluídos, como na criação de
canais) e acesso à conversa, quando houver. Workspace e organizer vêm da sessão,
nunca do payload. Admins de workspace não têm poder implícito sobre reuniões
alheias. Convidados precisam ser membros elegíveis do workspace e, se a reunião
tem conversa, ler essa conversa, para que o convite não revele a conversa a quem
não a vê.

## Erros e não enumeração

Reunião inexistente, de outro workspace ou invisível para o ator: todos geram
`ErrNotFound`. `ErrForbidden` só ocorre para quem já vê a reunião. Conflitos
(`ErrMeetingStaleVersion`, `ErrMeetingInvalidState`,
`ErrMeetingParticipantExists`) envolvem `ErrConflict`. Validação
(`ErrMeetingParticipantNotEligible` e os erros de campo) envolve
`ErrInvalidInput`. Nenhuma mensagem repete título, descrição, participantes ou
horários.

## Contratos para as próximas sub-issues

`domain.NewMeeting` / `Meeting.Update` / `Meeting.Transition`,
`domain.AuthorizeMeeting*`, `domain.MeetingInviteeEligible` e
`storage.PGXMeetingStore` (`CreateMeeting`, `GetMeeting`, `UpdateMeeting`,
`TransitionMeeting`, `Add/ChangeRole/RemoveMeetingParticipant`, `SetMeetingRSVP`,
`AuthorizeMeetingJoin`), sempre com `MeetingScope` derivado da sessão.

Estes pontos ficam para as próximas sub-issues: recorrência e exceções (a
identidade da reunião não muda, então uma série pode referenciá-la), RSVP externo
(o RSVP é um valor por participante, sem acoplamento a provider), outbox/eventos
(nenhum consumidor existe ainda), paginação do roster (#1054), convites no
create (#1053), transferência de organizer e idempotência do RSVP (#1059: repetir
a mesma resposta hoje regrava `updated_at`).

## Evolução #1054 — participantes internos

[Contrato HTTP, limites e threat model](../api/chat-meeting-participants.md).
Seleção contextual e roster usam keyset por UUID, até 100 linhas por página.
Bulk explícito resolve até 500 candidatos, revalida fatos com
`lockMeetingAuthority` e insere em um statement atômico. Primeiro trava reunião,
workspace e conversa; só depois resolve IDs e trava memberships/contas de todos
os usuários, por tabela e ID, preservando a ordem histórica. Nenhuma mutation
roster incrementa version. Não há migration nem alteração de join policy.

A #1054 exclui guests de novos convites pela `MeetingInviteeEligible`, inclusive
no create. Esta restrição deliberada substitui a elegibilidade anterior de guest
explicitamente convidado; preserva leitura/join de registros históricos.
Descoberta usa autorização de criação com acesso atual à conversa, sem usar
policy de join como autorização para enumerar membros. Roster usa meeting:view.
