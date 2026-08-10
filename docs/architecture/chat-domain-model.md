# Chat Service — Data Model

> **MVP foundation only.** Single/default workspace.
> Full multi-workspace (RF-68..RF-72) is out of scope.
> Full RBAC (RF-74) is out of scope; only minimal role/permission foundation.
> WebSocket, E2E/MLS, search, and admin UI are out of scope.

Migrations: `migrations/chat/000001_chat_domain_schema`,
`migrations/chat/000002_chat_enforce_channel_workspace_isolation`,
`migrations/chat/000003_chat_dm_conversations`,
`migrations/chat/000004_chat_messages`, and
`migrations/chat/000017_channel_category_constraints`
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

| Column       | Type        | Notes                                                     |
| ------------ | ----------- | --------------------------------------------------------- |
| id           | uuid        | PK                                                        |
| workspace_id | uuid        | FK → workspaces (CASCADE); also UNIQUE (workspace_id, id) |
| name         | text        | 1..60 chars, trimmed, no control chars, `Geral` reserved  |
| position     | int         | Sort order, 0..100000, server-derived                     |
| created_at   | timestamptz |                                                           |
| updated_at   | timestamptz |                                                           |

UNIQUE `(workspace_id, lower(btrim(name)))` — one category name per workspace,
case-insensitively; the same name in two workspaces is allowed. Index
`(workspace_id, position)` serves the ordered listing. See
"Channel categories (RF-17)".

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
| referenced_message_id     | uuid        | Nullable; opaque source ID for references (RF-09)          |
| edited_at                 | timestamptz | Nullable; set on first edit (RF-13)                        |
| deleted_at                | timestamptz | Nullable; set on soft delete (RF-14, RF-66)                |
| created_at                | timestamptz |                                                            |
| updated_at                | timestamptz |                                                            |

Exactly one of `channel_id` and `dm_conversation_id` must be non-null
(`CONSTRAINT messages_exactly_one_target`). Composite FKs ensure the referenced
channel or DM belongs to the same workspace as the message.

### message_attachments (RF-32)

| Column        | Type        | Notes                                             |
| ------------- | ----------- | ------------------------------------------------- |
| message_id    | uuid        | PK part, FK -> chat.messages (CASCADE)            |
| attachment_id | uuid        | PK part, UNIQUE; files.attachments ref (no FK)    |
| position      | smallint    | 0-9; orders several attachments under one message |
| created_at    | timestamptz |                                                   |

The edge lives in the `chat` schema because chat-service owns it and writes it in
the same statement that inserts the message, which is what makes "message created
but attachment not linked" unreachable. `attachment_id` has no foreign key for the
same reason `files.attachments` references `chat.*` without one: cross-schema FKs
are not used in this repository, so `migrations/chat` and `migrations/files` stay
independent of each other's ordering. `UNIQUE (attachment_id)` makes an upload
belong to at most one message, so an attachment id cannot be replayed into a
second message or a second destination.

Referential integrity is enforced at write time instead. The `invalid_attachments`
CTE in `PGXMessageStore.CreateMessage` re-reads `files.attachments` and requires,
for every candidate id: the row exists and is not soft-deleted, belongs to the
message's workspace, belongs to exactly the message's destination (channel _or_ DM),
was uploaded by the sender, is in `pending_scan` or `clean`, and is not already
linked. Any failure produces zero rows and therefore the same non-enumerating
`ErrNotFound` as every other invalid reference. `pending_scan` is deliberately
linkable: the antimalware scan is asynchronous and a message must be sendable while
it runs — content and preview delivery stay gated by file-service on every request.

Reads load attachments for a whole page in one query keyed by the primary key
(`loadAttachmentBatch`), alongside the existing reaction batch, so a page of
messages never becomes a per-message lookup. The exposed metadata is `id`,
`filename`, `content_type`, `size`, `status` and `preview_status`; storage keys,
key material and scanner detail never leave file-service.

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

Channel **creation** takes active workspace membership and nothing else: every
active member — `owner`, `admin`, `member` or `guest` — may create a channel, and
the role is deliberately not consulted (BUG #393). Suspended, left, missing and
disabled-workspace callers are denied. The authorization is not a check followed
by a write: the pgx store inserts the channel with `INSERT ... SELECT` from a
row-locked authorized context (`chat.workspaces` + `chat.workspace_members`), so
a membership revoked concurrently leaves no row to insert from and no channel
behind.

Because a `GET /api/chat/sidebar` 200 already means an active membership, its
`can_create_channel` field is now always `true` and is **deprecated**: it is kept
only so clients that predate BUG #393 keep working during rollout, is never
derived from the caller's role, and is ignored by the current UI, which offers
"Nova conversa" as the single entry point. `POST /api/chat/channels` re-derives
the decision from the session on every call.

`display_name` is required, trimmed, and capped at 100 Unicode code points by
`domain.NormalizeChannelDisplayName` — the one helper every write path uses
(create, update, workspace bootstrap). The count is code points at all three
layers: `Array.from().length` in the browser, `utf8.RuneCountInString` in Go,
`char_length` in the `channels_display_name_length_check` constraint. The
constraint is `NOT VALID`, so it governs new writes without the deploy depending
on the state of existing rows.

Channel **update** and **archive** remain management operations for this MVP:
active workspace `owner` and `admin` only. Full RBAC (RF-74) remains out of
scope.

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

## Channel categories (RF-17)

Migration: `migrations/chat/000017_channel_category_constraints`.

`chat.channel_categories` and `chat.channels.category_id` have existed since
000001, and 000002 already made the association workspace-bound with a composite
FK. What 000017 adds is everything that bounds a category row — the name rules,
the position range, the case-insensitive uniqueness per workspace, the ordered
listing index — plus `idx_channels_workspace_category`, the referencing-side index
the composite FK never had. Without it, deleting a category made
`ON DELETE SET NULL` scan every channel.

Those constraints are added **validated**, not `NOT VALID` as in 000016: no code
path had ever inserted into this table, since its only writer was never wired to a
service or a handler. A unique index cannot be `NOT VALID` in any case, so a
partially validated migration would only have looked safer.

### "Geral" is a virtual group, not a row

Channels with `category_id IS NULL` are grouped under **Geral**. There is no
`Geral` row, no synthetic UUID standing in for one, and no per-workspace
bootstrap. The name is reserved case-insensitively, in
`domain.NormalizeChannelCategoryName` and again in
`channel_categories_name_not_reserved_check`, so a persisted category can never be
confused with the virtual one.

The listing distinguishes them explicitly: a persisted group carries
`kind:"category"` with `id` and `position`; the virtual group carries
`kind:"uncategorized"` and no `id` at all. It is always the first group and is
always present, so the response shape never varies.

The seeded `#geral` **channel** is a different object that happens to share the
word: an ordinary channel with no category, which therefore appears inside this
group.

### Ordering

`position` is the explicit sort key, scoped to the workspace and always
server-derived — `COALESCE(MAX(position) + 1, 0)` on create, the payload ordinal
on reorder. It is deliberately **not** unique: uniqueness would force retry loops
on create and temporary offsets on every reorder. The listing order is
`position, lower(name), id`, so two categories that tie still produce one stable
order for every reader.

Reordering submits the workspace's **complete** category set, each ID once. That
makes the result a total order rather than a patch whose outcome depends on what
the caller omitted, and it bounds the payload at
`domain.MaxCategoriesPerWorkspace` (100), which is also the per-workspace ceiling
on creation. The store runs it in one transaction: authorize and lock the
workspace and membership `FOR SHARE`, lock the category rows `FOR UPDATE` (this is
what serialises two concurrent reorders, and a concurrent create or delete),
verify the set against the locked rows, then one
`UPDATE ... FROM unnest(...) WITH ORDINALITY`. A duplicate ID, a missing one and
one from another workspace are all the same error, so the response cannot be used
to probe another workspace.

### Authorization

Reading is open to any active workspace member, guests included, and **cannot
widen channel access**: the channels in each group come from
`ListVisibleChannelAccessByUser` — the same single SQL policy `/api/chat/sidebar`
reads through — and are only grouped in memory. Two queries total regardless of
the number of categories; there is no per-category fetch.

Creating, renaming, reordering and deleting take active workspace `owner` or
`admin`, via `domain.CanManageChannelCategories`. The service checks it, and every
statement re-derives it from `chat.workspace_members` in the same statement as the
write, the way `UpdateEditWindow` does. Every mutation is scoped by
`workspace_id` together with `category_id`; none ever runs on a bare category ID.

RF-17 was specified as "Admin and Moderator". There is no workspace-level
moderator in this schema — `chat.workspace_members.role` is
owner/admin/member/guest, and `moderator` exists only on `chat.channel_members` as
a per-channel role. The divergence and its reasoning are recorded in
`SECURITY.md`.

### Deletion

Deleting a category never deletes a channel. The channels that referenced it
become uncategorized and appear under **Geral** on the next listing, and the
database does that as part of the same `DELETE` through the composite FK's
`ON DELETE SET NULL (category_id)` — so there is no window in which a channel
points at a category that is gone, and the invariant survives writers that never
go through this store. Deleting a category that does not exist is a 404, not a
silent success.

### HTTP contract

No route carries a workspace segment: the workspace is resolved server-side from
the authenticated session, like every other chat route. `position`, `id`,
timestamps and any actor or role field are absent from every request body, and the
decoder rejects unknown fields, so nothing a client sends can claim a privilege or
redirect an operation at another workspace.

| Method   | Path                                 | Who           | Success |
| -------- | ------------------------------------ | ------------- | ------- |
| `GET`    | `/api/chat/channel-categories`       | active member | 200     |
| `POST`   | `/api/chat/channel-categories`       | owner ∣ admin | 201     |
| `PATCH`  | `/api/chat/channel-categories/{id}`  | owner ∣ admin | 200     |
| `PUT`    | `/api/chat/channel-categories/order` | owner ∣ admin | 200     |
| `DELETE` | `/api/chat/channel-categories/{id}`  | owner ∣ admin | 204     |

Bodies: `POST`/`PATCH` take `{"name": "..."}`; `PUT .../order` takes
`{"category_ids": ["..."]}`. All four mutations share one rate-limit budget (20
per user per minute) so a caller cannot spend a separate allowance per operation,
and the routes are registered only when the limiter is configured — an
unthrottled write route is left unregistered rather than exposed.

Errors: 400 invalid name, malformed body, unknown field, non-UUID ID or invalid
order; 401 no usable session; 403 not a manager, or a workspace the caller has no
membership in; 404 category not in the caller's workspace — the same answer as one
that does not exist anywhere, so the status cannot be used to enumerate; 409
duplicate name or workspace ceiling reached; 415 wrong content type; 429 over
budget. No response carries SQL text, a constraint name or the rejected value.

`GET /api/chat/sidebar` is unchanged. The grouped listing is additive, so the
frontend task can adopt it without a migration of the existing contract.

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
represent quote-reply (RF-07), forwarding (RF-08), and cross-target references
(RF-09). Parent remains same-target. RF-09 requires a different channel or DM in
the same workspace and stores no content snapshot.

### Attachments on create (RF-32)

`POST .../messages` additionally accepts `attachment_ids`, an array of at most
`domain.MaxMessageAttachments` (1) canonical UUIDs. Unknown fields are still
rejected. A message with no body is valid **only** when it carries a valid
attachment; empty body with no attachment stays a `400`. The ids are candidate
references: the service canonicalises them and rejects malformed, empty and
duplicate values, and the storage layer re-validates each one against the database
in the same statement that inserts the message. `PATCH` (edit) does not accept
`attachment_ids` — editing text never changes a message's attachments.

RF-08 uses `POST /api/chat/channels/{destinationChannelID}/messages/forward` with
the strict body `{ "source_message_id": "<uuid>" }`. `Idempotency-Key` is optional
for compatibility and the web client always sends one per user action (maximum
128 safe characters). A first execution returns `201`; a same-fingerprint replay
returns the original message with `200`; reusing the key for another source in
the same user/workspace/destination scope returns `409`. Replay metadata and the
key are never serialized.

The authenticated user must be able to read the active source channel message
and write to the active destination channel in the same workspace. The single
storage CTE is the authoritative authorization and consistency control: it
revalidates source, destination, workspace and memberships in the same statement
that copies `body_text`/`body_format`, persists `forwarded_from_message_id`, and
arbitrates idempotency. Its zero-row `ErrNotFound` result is deliberately
non-enumerating; service-side validation must never replace or weaken these SQL
predicates.

The sidebar emits server-derived `can_write` for each visible active channel.
The service evaluates it through `CanWriteChannel` using the caller's real
workspace and optional channel memberships. During rolling deployment, a web
client receiving an older response without `can_write` keeps the channel visible
for reading but normalizes destination eligibility to `false`. Forwarding
authorization remains the final control. Forwarding has an independent budget
of 20 requests per minute per authenticated user.
`chat_message_forward_total` and `chat_message_forward_duration_seconds` record
only the fixed `result` classes; message, user, workspace, channel and
idempotency identifiers are never metric labels or forwarding log fields.

Responses, lists, pagination and new WebSocket events always include
`is_forwarded` (`false` for ordinary messages, `true` for forwarded snapshots).
The web HTTP and WebSocket decoders normalize an absent or invalid field to
`false` only for compatibility with pre-RF-08 servers; during a rolling deploy,
the only possible impact is a temporarily absent forwarded badge. Reactions,
favorites, pins, edit history, references and attachments are not copied. Later
edits or soft deletion of the source do not change the forwarded snapshot. DMs,
batch forwarding, comments, attachments and cross-workspace forwarding are out
of scope.

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

RF-09 previews are resolved at read time with the caller's current channel/DM
access. Inaccessible, deleted, missing, or cross-workspace origins expose only a
generic unavailable state. The opaque ID intentionally has no FK so a hard-deleted
origin remains distinguishable from a message that never had a reference without
retaining protected content.

Mounted clients periodically re-authorize displayed references through the
page-sized batch endpoints (maximum 100 destination IDs per request)
`POST /api/chat/channels/{channelID}/message-references`
and `POST /api/chat/dm/{conversationID}/message-references`. Each response is
scoped to the authenticated reader and returns only the destination message ID
plus its freshly resolved reference; unavailable origins contain only
`{"available":false}`. A newer batch cancels and supersedes an older one so an
out-of-order response cannot restore a revoked preview.

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

## Adding members to existing conversations (issue #398)

Membership is **definitive**: there is no invite table, `chat.channel_members`
has no status column, and `chat.dm_members.status` distinguishes only active from
left. Adding somebody therefore takes effect at commit; "convidar" is interface
wording, not a persisted state. No pending/accepted/declined/expired model was
introduced, because supporting one honestly would need a table, a lifecycle and
an expiry worker that nothing else in the domain has.

The two conversation aggregates keep their own route, because they are different
aggregates: `POST /api/chat/channels/{id}/members` and
`POST /api/chat/dm/{id}/members`. There is deliberately no third notion of a
"public group" or "private group" -- `chat.dm_conversations` has no visibility
column, and none was added to satisfy the issue's generic wording. A 1:1 direct
conversation refuses the operation: a third participant would convert it into a
group, and its `direct_pair_key` would then describe a conversation that is no
longer a pair.

Authorization differs between the two because the schema does. Channels use
`domain.CanManageChannelMembers` (active workspace `owner` or `admin`), the same
authority that already removes a channel member. Groups take active
participation, because `chat.dm_members.role` is closed by CHECK to `'member'`
and a group has no privileged participant to require. Both decisions and the
alternatives rejected are recorded in `SECURITY.md`.

No migration was needed. The primary keys `(channel_id, user_id)` and
`(conversation_id, user_id)` already give the uniqueness the idempotency relies
on, `idx_channel_members_user` and `idx_dm_members_conversation_active` already
serve the lookups, and the composite workspace FKs already bound the association.
Both writes are one `INSERT ... SELECT` that tests eligibility in the same
statement it writes, so the service's pre-check cannot be raced; a mismatch
between requested and eligible rows aborts the transaction, and no partial
membership survives. Group adds additionally lock the conversation row
(`FOR SHARE`), which pins the authorization context -- not a capacity, of which
there is none.

Events are published only after commit, carry counts and never identities, and
exist to tell subscribers to refetch -- the same shape `pin.updated` uses. The
refetch is the single reconciliation path, so the HTTP response and the event
arriving together cannot duplicate a member or double a counter.

`members.added` reaches _subscribers_, which by construction excludes the people
who were just added: they do not subscribe to a private channel or a group
before belonging to it. `conversation.available` closes that gap -- the one
user-scoped event in this protocol, delivered straight to the sessions of the
users the transaction actually inserted (`AddMembersResult.AddedUserIDs`, from
the statement's `RETURNING`, never from the request). Besides the conversation
it names only its own addressee, which is what lets another instance route it,
and it grants nothing: the client reacts by refetching the sidebar, which
re-derives membership server-side.

Both cross the `BroadcastBus`, because the session that needs the event is
usually not on the pod that performed the write, and each keeps its own scope
there: `members.added` re-enters the ordinary subscription fan-out on the
receiving node, while `conversation.available` is routed by `recipient_user_id`.
The bus is a trust boundary in both cases -- an envelope arriving over it
asserts its own workspace, target and addressee, none of which the receiver
decided. So a remote envelope is strictly canonicalized before anything else,
and access is then re-derived from the authoritative record: per subscriber at
fan-out for `members.added`, and for the named recipient before any frame is
sent for `conversation.available`. Denial, error and timeout all fail closed.
`source_instance_id` suppresses the publisher's own echo, and an event received
from the bus is never republished onto it.

### Authorization is re-derived inside the writing transaction

Both writes take the authenticated actor as an explicit argument and re-establish
their authority in the same transaction that inserts, under a row lock:

- channels re-read `chat.workspace_members` for the actor and require an active
  `owner`/`admin` row -- the SQL statement of `domain.CanManageChannelMembers`;
- groups re-read the actor's active `chat.dm_members` row, joined to an active
  workspace membership, because a participation row outlives the workspace
  membership that justified it.

The service still checks first, but only so a caller with no business here is
refused before the conversation lookup can leak whether an ID exists. It is not
the control: a role demoted, a membership suspended, or a participant removed
between that check and the write leaves the transaction with nothing to insert
from, and the whole thing rolls back with `ErrForbidden`. The service's
verdict is deliberately not passed down as a boolean; a boolean computed a moment
ago is exactly what the query exists to distrust.

Lock order is conversation/channel -> actor membership -> target rows -> insert,
the same order everywhere, so two of these cannot deadlock against each other.
No lock is held across a WebSocket publish: events go out after commit.

### Candidate search is conversation-scoped, not workspace-wide

"Who can still be added" depends on who is already in the target, and only the
database knows that. The details panels do not: a channel's member section is
filtered by presence before its cap, and a group's participant list is capped at
`domain.MaxDMDetailsParticipants`. Both are previews, and using them as the
eligibility rule offered existing members as selectable — an offline channel
member, or a group's 31st participant onwards.

`GET /api/chat/channels/{id}/member-candidates` and
`GET /api/chat/dm/{id}/member-candidates` exclude current members with a
`NOT EXISTS` in the same statement that filters for eligibility, under the same
authorization the corresponding write uses. The client sends only a query and an
optional limit; it never sends a membership list, and it does not filter by one.

The search is a snapshot and the write remains the authority: someone may join
between the two, and that race resolves as an idempotent `added: 0` rather than
an error.

### No fixed conversation capacity

Channels and groups have **no total participant limit**. A conversation may grow
without bound across successive requests, and there is no "full" state, no
capacity conflict and no ceiling check anywhere in the write path.

The only bound is `domain.MaxAddMembersPerRequest` (25), which caps how many IDs
a single HTTP request may carry — an operational and anti-abuse bound on the
payload. Exceeding it is `ErrTooManyMembersRequested`, which wraps
`ErrInvalidInput` and answers 400, because it describes the request rather than
the conversation. Reading multiple successive requests as a way around a limit
would be a category error: there is no limit to get around.

The store still opens a transaction and still locks, but only `FOR SHARE`, and
only to pin the authorization context while the write happens — archiving the
conversation or revoking the actor are UPDATEs that conflict with `FOR SHARE`.
With no ceiling to serialise, two people adding different users to the same
large conversation proceed in parallel instead of queueing.

What the transaction does compute is which of the requested users _newly_ became
participants. That is not a capacity check: it is what `AddedUserIDs` reports,
and therefore who the user-scoped fan-out addresses.

## Cross-schema identity

`workspace_members.user_id`, `channel_members.user_id`, `dm_members.user_id`,
and `messages.sender_id` reference `auth.users.id` **by convention only** — no
cross-schema FK constraint. This preserves service ownership boundaries and
avoids tight coupling between auth and chat schemas.
