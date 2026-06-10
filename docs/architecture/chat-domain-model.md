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

The database enforces the general-channel invariant with a partial unique index,
an active/public `CHECK`, and deferred constraint triggers. This permits creating
a workspace and `#geral` in one transaction while rejecting a commit that leaves
an existing workspace without an active public general channel.

## Cross-schema identity

`workspace_members.user_id` and `channel_members.user_id` reference `auth.users.id`
**by convention only** — no cross-schema FK constraint. This preserves service
ownership boundaries and avoids tight coupling between auth and chat schemas.
