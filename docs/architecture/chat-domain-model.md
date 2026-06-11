# Chat Service — Data Model

> **MVP foundation only.** Single/default workspace.
> Full multi-workspace (RF-68..RF-72) is out of scope.
> Full RBAC (RF-74) is out of scope; only minimal role/permission foundation.
> Messaging, WebSocket, E2E, search, and admin UI are out of scope.

Migrations: `migrations/chat/000001_chat_domain_schema` and
`migrations/chat/000002_chat_enforce_channel_workspace_isolation`
Schema: `chat`

## Table summary

    chat.workspaces
    ├── chat.channel_categories  (workspace_id FK, CASCADE delete)
    ├── chat.channels            (workspace_id FK, CASCADE delete; workspace-bound category FK)
    │   └── chat.channel_members (channel_id FK, CASCADE delete)
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

## Cross-schema identity

`workspace_members.user_id` and `channel_members.user_id` reference `auth.users.id`
**by convention only** — no cross-schema FK constraint. This preserves service
ownership boundaries and avoids tight coupling between auth and chat schemas.
