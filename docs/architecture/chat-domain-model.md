# Chat Service — Data Model

> **MVP foundation only.** Single/default workspace.
> Full multi-workspace (RF-68..RF-72) is out of scope.
> Full RBAC (RF-74) is out of scope; only minimal role/permission foundation.
> WebSocket, E2E/MLS, search, and admin UI are out of scope.

Migrations: `migrations/chat/000001_chat_domain_schema`,
`migrations/chat/000002_chat_enforce_channel_workspace_isolation`,
`migrations/chat/000003_chat_dm_conversations`, and
`migrations/chat/000004_chat_messages`
Schema: `chat`

## Table summary

    chat.workspaces
    ├── chat.channel_categories  (workspace_id FK, CASCADE delete)
    ├── chat.channels            (workspace_id FK, CASCADE delete; workspace-bound category FK)
    │   └── chat.channel_members (channel_id FK, CASCADE delete)
    ├── chat.dm_conversations    (workspace_id FK, CASCADE delete)
    │   └── chat.dm_members      (conversation_id FK, CASCADE delete)
    ├── chat.workspace_members   (workspace_id FK, CASCADE delete)
    └── chat.messages            (workspace_id FK, CASCADE delete; composite channel/DM FKs)

## Seed data

| Table           | id                                   | notes                         |
| --------------- | ------------------------------------ | ----------------------------- |
| chat.workspaces | 00000000-0000-0000-0000-000000000001 | slug='default', name='NChat'  |
| chat.channels   | 00000000-0000-0000-0000-000000000002 | slug='geral', is_general=true |

Seed uses `ON CONFLICT (id) DO NOTHING` — idempotent on repeated migration runs.
`#geral` is mandatory for each active workspace.

## Schema reference

### workspaces

| Column     | Type        | Notes                 |
| ---------- | ----------- | --------------------- |
| id         | uuid        | PK, gen_random_uuid() |
| slug       | text        | UNIQUE, lowercase     |
| name       | text        | Display name          |
| status     | text        | active / disabled     |
| created_at | timestamptz |                       |
| updated_at | timestamptz |                       |

### channel_categories

| Column       | Type | Notes                     |
| ------------ | ---- | ------------------------- |
| id           | uuid | PK                        |
| workspace_id | uuid | FK → workspaces (CASCADE) |
| name         | text |                           |
| position     | int  | Sort order, DEFAULT 0     |

### channels

| Column       | Type    | Notes                                              |
| ------------ | ------- | -------------------------------------------------- |
| id           | uuid    | PK                                                 |
| workspace_id | uuid    | FK → workspaces (CASCADE)                          |
| category_id  | uuid    | Composite FK with workspace_id; SET NULL, nullable |
| slug         | text    | UNIQUE per workspace                               |
| display_name | text    |                                                    |
| type         | text    | public / private                                   |
| status       | text    | active / archived                                  |
| is_general   | boolean | Exactly one active public channel per workspace    |
| position     | int     | Sort order, DEFAULT 0                              |
| created_by   | uuid    | auth.users ref (no FK), nullable                   |

### workspace_members

| Column       | Type        | Notes                              |
| ------------ | ----------- | ---------------------------------- |
| workspace_id | uuid        | PK part, FK → workspaces (CASCADE) |
| user_id      | uuid        | PK part, auth.users ref (no FK)    |
| role         | text        | owner / admin / member / guest     |
| status       | text        | active / suspended / left          |
| joined_at    | timestamptz |                                    |

### channel_members

| Column     | Type        | Notes                            |
| ---------- | ----------- | -------------------------------- |
| channel_id | uuid        | PK part, FK → channels (CASCADE) |
| user_id    | uuid        | PK part, auth.users ref (no FK)  |
| role       | text        | member / moderator               |
| joined_at  | timestamptz |                                  |

### dm_conversations

| Column       | Type        | Notes                        |
| ------------ | ----------- | ---------------------------- |
| id           | uuid        | PK, gen_random_uuid()        |
| workspace_id | uuid        | FK -> workspaces (CASCADE)   |
| type         | text        | direct / group               |
| title        | text        | Nullable, max 120 characters |
| status       | text        | active / archived            |
| created_by   | uuid        | auth.users ref (no FK)       |
| created_at   | timestamptz |                              |
| updated_at   | timestamptz |                              |

Direct 1:1 conversations also store an internal uniqueness column. It is a
database/service implementation detail only: callers do not provide it, domain
responses do not include it, and future HTTP responses must not expose it.

### dm_members

| Column          | Type        | Notes                                  |
| --------------- | ----------- | -------------------------------------- |
| conversation_id | uuid        | PK part, FK -> dm_conversations        |
| user_id         | uuid        | PK part, auth.users ref (no FK)        |
| role            | text        | member                                 |
| status          | text        | active / left                          |
| joined_at       | timestamptz |                                        |
| left_at         | timestamptz | Nullable; set only when status is left |

Active workspace members are automatically synced into their workspace's
mandatory `#geral` channel. The pgx member store performs workspace
join/reactivation and `#geral` `channel_members` insertion in one transaction
where the general channel is loaded by the same `workspace_id`. Duplicate rows
are ignored with `ON CONFLICT DO NOTHING`; unexpected database errors propagate.
If `#geral` is missing, membership sync returns an explicit error instead of
creating the channel in this path.

### messages

| Column                    | Type        | Notes                                                      |
| ------------------------- | ----------- | ---------------------------------------------------------- |
| id                        | uuid        | PK, gen_random_uuid()                                      |
| workspace_id              | uuid        | FK → workspaces (CASCADE)                                  |
| channel_id                | uuid        | Nullable; composite FK (workspace_id, channel_id)          |
| dm_conversation_id        | uuid        | Nullable; composite FK (workspace_id, dm_conversation_id)  |
| sender_id                 | uuid        | auth.users ref (no FK)                                     |
| kind                      | text        | user / system                                              |
| body_text                 | text        | Max 40,000 characters; plain text (rich text future scope) |
| status                    | text        | active / deleted (soft delete)                             |
| parent_message_id         | uuid        | Nullable; self-ref FK for quote-reply (RF-07)              |
| forwarded_from_message_id | uuid        | Nullable; self-ref FK for forwarding (RF-08)               |
| referenced_message_id     | uuid        | Nullable; self-ref FK for references (RF-09)               |
| edited_at                 | timestamptz | Nullable; set on first edit (RF-13)                        |
| deleted_at                | timestamptz | Nullable; set on soft delete (RF-14, RF-66)                |
| created_at                | timestamptz |                                                            |
| updated_at                | timestamptz |                                                            |

Exactly one of `channel_id` and `dm_conversation_id` must be non-null
(`CONSTRAINT messages_exactly_one_target`). Composite FKs ensure the referenced
channel or DM belongs to the same workspace as the message.

## Permission rules

| Scenario                                              | Access |
| ----------------------------------------------------- | ------ |
| user_id not in workspace_members (or status ≠ active) | DENY   |
| workspace member + public channel                     | ALLOW  |
| workspace member + is_general=true channel            | ALLOW  |
| workspace member + private channel, no channel_member | DENY   |
| workspace member + private channel + channel_member   | ALLOW  |

Read/write checks use `PermissionService` with workspace-bound channel lookup;
global channel IDs are never sufficient for authorization. Visible channel lists
are filtered in SQL by active workspace, active workspace membership, channel
status/type, and private channel membership. Disabled workspaces deny channel
list, read, write, category creation, channel creation, and channel membership.
`#geral` authorization does not depend solely on the synced `channel_members`
row: any active workspace member may read/write the active public general
channel, even if a repair sync has not yet inserted the consistency row.

The database enforces the general-channel invariant with a partial unique index,
an active/public `CHECK`, and deferred constraint triggers. This permits creating
a workspace and `#geral` in one transaction while rejecting a commit that leaves
an existing workspace without an active public general channel.

## Channel CRUD foundation

`ChannelService` implements the backend service/storage foundation for public
and private channel CRUD. This PR intentionally does **not** expose HTTP CRUD
handlers because `chat-service` currently has no authenticated user context or
auth middleware. The REST API is deferred until the service can bind every
request to a verified caller without inventing gateway or auth-service behavior.

Implemented service/storage operations:

- create public/private channels in an active workspace;
- list channels visible to an active workspace member;
- read one active channel by `(workspace_id, channel_id)` or
  `(workspace_id, slug)` with SQL visibility checks;
- update mutable fields on non-general channels: slug, display name,
  category, position, and public/private type;
- archive non-general channels by setting `status='archived'`.

CRUD management uses the minimal role rule for this MVP: active workspace
`owner` and `admin` may create, update, or archive channels. `member`, `guest`,
suspended, left, missing, and disabled-workspace callers are denied. Full RBAC
(RF-74) remains out of scope.

Public/private visibility rules are enforced by SQL in the pgx channel store:
the query joins `chat.workspaces`, active `chat.workspace_members`, and
`chat.channel_members` for private channels. Public and `#geral` channels are
visible to active workspace members; private channels are visible only to active
workspace members who also have a `channel_members` row for that channel. Stale
private membership alone grants nothing when the workspace is disabled or the
workspace membership is inactive.

Create/update inputs do not accept `is_general`, `status`, or `created_by` from
clients/callers. The service sets `created_by` from the caller on create,
always creates CRUD channels with `is_general=false`, and leaves archive as the
only status mutation. Private-channel creation inserts the creator into
`channel_members` in the same storage transaction. Public-channel creation does
not fan out `channel_members` to every workspace member. Changing a public
channel to private inserts the manager into `channel_members` in the same update
transaction so the caller does not lose access.

Categories remain workspace-bound: `category_id` is accepted only when the
category belongs to the same workspace, and the composite FK remains the
database backstop. Duplicate slugs map to `ErrDuplicateSlug`.

`#geral` is immutable through CRUD. The service rejects attempts to create a
regular channel with slug `geral`, rejects any update/archive of the general
channel, and never exposes `is_general` as caller-controlled input.

Full multi-workspace workflows (RF-68..RF-72) and the full RBAC matrix (RF-74)
remain out of scope for this MVP foundation.

## DM conversation foundation

`DMService` implements the backend service/storage foundation for direct 1:1
DMs and ad-hoc group DMs inside a workspace. This is conversation and membership
foundation only. It intentionally does **not** add a messages table, message
body delivery, WebSocket delivery, notifications, search, file upload, HTTP
handlers, frontend UI, or E2E/MLS key-service work. E2E/MLS remains V1.0 future
scope.

User IDs supplied to `CreateDirectConversation` and `CreateGroupConversation` are
parsed and canonicalized as UUIDs before self-DM checks, de-duplication,
`direct_pair_key` generation, and storage. `direct_pair_key` remains an internal
database implementation detail; it is not part of any API contract.

Implemented service/storage operations:

- create or return the canonical direct DM for one unordered pair of active
  workspace members;
- create ad-hoc group DMs with the caller automatically added;
- list active DM conversations visible to the caller;
- read one active DM conversation only when the caller is an active participant.

Direct DM uniqueness is enforced in the database per `(workspace, unordered
pair)`, not by Go-only lookup. The uniqueness rule intentionally includes
archived direct conversations. When direct creation finds an archived direct DM
for the same pair, storage reactivates that row and active member rows instead
of creating a second conversation. The same pair in another workspace is a
separate conversation.

DM creation requires an active workspace and active workspace membership for
every participant. Suspended, left, missing, or cross-workspace participants are
denied. The caller cannot create a self-DM. Group DMs require at least three
unique participants after adding the caller, because 1:1 conversations use the
direct DM path. Group roles are always `member`; callers cannot set role, status,
or `created_by`.

Visible DM reads and lists are enforced by SQL in the pgx DM store. Queries join
`chat.workspaces`, active `chat.workspace_members`, active `chat.dm_members`,
and active `chat.dm_conversations`. A stale `dm_members` row does not grant
access when the workspace is disabled or the workspace membership is inactive.

DM reads are non-enumerating: missing conversation IDs, cross-workspace IDs,
archived conversations, and non-participant access all return `ErrNotFound`.
There are no HTTP endpoints yet because `chat-service` still lacks authenticated
user context/middleware.

## Message model foundation

`MessageService` implements the backend service/storage foundation for messages
in channels and DM conversations. This is message model foundation only.

**Not implemented in this PR:**

- No HTTP endpoints (service lacks authenticated user context/middleware).
- No WebSocket / realtime delivery.
- No reactions (RF-03), mentions notifications (RF-04), pinned (RF-05), or
  favorites (RF-06) implementation — schema placeholders only.
- No attachments / file upload.
- No E2E/MLS encryption.
- No auth-service changes or FK to `auth.users`.
- No full retention worker / trash lifecycle (RF-64..RF-67); model-ready only.
- No edit history table (RF-13); `edited_at` column is present for future use.
- No delete operation (RF-14, RF-66); `deleted_at` and `status='deleted'` columns
  are present as placeholders.

**Implemented service/storage operations:**

- post a message to a public or private channel;
- post a message to a direct or group DM conversation;
- list messages for a channel visible to the caller (SQL-enforced, up to 100);
- list messages for a DM conversation visible to the caller (SQL-enforced, up to 100).

### Message targets and exactly-one-target constraint

A message belongs to exactly one target: a `channel_id` or a
`dm_conversation_id`, never both, never neither. This is enforced at three
layers:

1. **Database** — `CONSTRAINT messages_exactly_one_target CHECK ((channel_id IS NULL) <> (dm_conversation_id IS NULL))`.
2. **Database** — composite FKs `(workspace_id, channel_id)` and
   `(workspace_id, dm_conversation_id)` enforce that the referenced channel/DM
   belongs to the same workspace as the message.
3. **Service** — `MessageService.CreateChannelMessage` only sets `channel_id`;
   `CreateDMMessage` only sets `dm_conversation_id`.

### Visibility and access control

Channel message creation and listing enforce visibility via the SQL JOIN on
`chat.channels`, `chat.workspaces`, `chat.workspace_members`, and
`chat.channel_members` (for private channels), following the same pattern as
`ListVisibleChannelsByUser`. DM message creation and listing enforce visibility
via the same SQL JOINs used by `GetVisibleConversationByID`.

**Atomic authorization in `CreateMessage`**: `PGXMessageStore.CreateMessage` uses
a single `INSERT … SELECT … FROM (auth subquery)` CTE so that authorization is
re-evaluated at write time, not only at service pre-check time. The auth subquery
contains two branches joined by `UNION ALL`:

- _Channel branch_ — active `chat.workspaces` + active `chat.workspace_members` +
  active `chat.channels` in the same workspace + (public **or** active
  `chat.channel_members` row for the sender).
- _DM branch_ — active `chat.workspaces` + active `chat.workspace_members` +
  active `chat.dm_conversations` in the same workspace + active `chat.dm_members`
  row for the sender.

If the sender is suspended or removed from the workspace, or the channel/DM is
archived, or the sender loses private-channel/DM membership between the service
pre-check and the INSERT, the auth subquery returns no rows and the INSERT inserts
nothing. The zero-row result maps to `ErrNotFound` (non-enumerating TOCTOU backstop).
The service layer performs typed pre-validation and returns specific errors;
`ErrNotFound` from `CreateMessage` surfaces only on TOCTOU race conditions.

Non-enumerating behavior: suspended/left workspace members, non-channel-members
on private channels, non-DM-participants, archived targets, and cross-workspace
target IDs all yield `ErrNotFound`.

### Reference messages (parent, forwarded_from, referenced)

`parent_message_id`, `forwarded_from_message_id`, and `referenced_message_id`
are nullable placeholders for quote-reply (RF-07), forwarding (RF-08), and
references (RF-09). In this foundation PR all three fields are **same-target only**:
the referenced message must belong to the same workspace and the same target
(channel or DM conversation) as the new message.

Validation is **non-enumerating**: missing, cross-workspace, cross-channel,
and cross-DM references all return the same `domain.ErrInvalidMessageReference`
sentinel. Callers cannot determine whether a referenced message exists.

The storage layer enforces this as a backstop via a CTE in `CreateMessage`:
the INSERT selects zero rows when any reference fails the same-workspace and
same-target check. Because auth and reference checks share the same INSERT,
both failure modes return `ErrNotFound` at the storage level (non-enumerating
TOCTOU backstop). `ErrInvalidMessageReference` is returned by the service's
pre-validation step (`ValidateRefMessageInTarget`) before `CreateMessage` is
ever called.

Cross-target references (channel → DM or DM → channel) are invalid in this PR.
Allowing them is future scope and must be explicitly documented and tested if added.

### Soft delete and message lifecycle

`DELETE /api/chat/messages/{messageID}` lets the authenticated author soft-delete
an active user message. The transaction locks the message row, verifies current
workspace and target access, sets `status='deleted'`, and assigns `deleted_at`
and `updated_at` from the database clock. Repeated deletion by the same author is
idempotent; unauthorized and inaccessible messages are externally indistinguishable.

The original body remains in PostgreSQL. A future retention, purge, or audit
lifecycle remains outside this change (RF-64 through RF-67). Normal HTTP
responses, references, and `message.updated` WebSocket events are centrally
redacted: deleted messages preserve identity, author, target, and chronology,
but expose an empty body and no quote. Clients render the localized
removed-message placeholder and suppress content actions and edit history.

## Cross-schema identity

`workspace_members.user_id`, `channel_members.user_id`, `dm_members.user_id`,
and `messages.sender_id` reference `auth.users.id` **by convention only** — no
cross-schema FK constraint. This preserves service ownership boundaries and
avoids tight coupling between auth and chat schemas.
