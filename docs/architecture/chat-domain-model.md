# Chat Service — Data Model

> **MVP foundation only.** Single/default workspace.
> Full multi-workspace (RF-68..RF-72) is out of scope.
> Full RBAC (RF-74) is out of scope; only minimal role/permission foundation.
> Messaging, WebSocket, E2E/MLS, search, and admin UI are out of scope.

Migrations: `migrations/chat/000001_chat_domain_schema` and
`migrations/chat/000002_chat_enforce_channel_workspace_isolation` and
`migrations/chat/000003_chat_dm_conversations`
Schema: `chat`

## Table summary

    chat.workspaces
    ├── chat.channel_categories  (workspace_id FK, CASCADE delete)
    ├── chat.channels            (workspace_id FK, CASCADE delete; workspace-bound category FK)
    │   └── chat.channel_members (channel_id FK, CASCADE delete)
    ├── chat.dm_conversations    (workspace_id FK, CASCADE delete)
    │   └── chat.dm_members      (conversation_id FK, CASCADE delete)
    └── chat.workspace_members   (workspace_id FK, CASCADE delete)

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

## Cross-schema identity

`workspace_members.user_id`, `channel_members.user_id`, and `dm_members.user_id`
reference `auth.users.id` **by convention only** — no cross-schema FK
constraint. This preserves service ownership boundaries and avoids tight
coupling between auth and chat schemas.
