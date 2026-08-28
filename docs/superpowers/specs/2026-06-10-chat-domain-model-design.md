# Chat Domain Model — Design Spec

**Date:** 2026-06-10
**Branch:** feat/chat-domain-workspaces-channels-permissions
**Status:** Approved

---

## Scope

MVP foundation for workspaces, channels, channel categories, memberships, and
permissions in `chat-service`. This is a **single/default workspace foundation**;
full multi-workspace UX (RF-68..RF-72), full RBAC (RF-74), messaging, WebSocket,
search, and admin UI are all **out of scope**.

---

## Architecture

All domain tables live in the `chat` PostgreSQL schema, owned by `chat-service`.
`auth-service` retains sole ownership of `auth.users`; chat tables reference
`user_id` as plain `UUID` with no cross-schema foreign key. Validation that a
`user_id` exists is the service layer's responsibility.

```
migrations/chat/
  000001_chat_domain_schema.up.sql    ← DDL + idempotent seed
  000001_chat_domain_schema.down.sql  ← DROP in reverse order

services/chat-service/internal/
  domain/
    workspace.go    ← structs, constants, permission helpers
    errors.go       ← domain sentinel errors
  storage/
    db.go           ← OpenDB
    pool.go         ← Pool interface
    workspace_store.go
    channel_store.go
    member_store.go
  service/
    workspace_service.go
    member_service.go
    permission_service.go
    [_test.go for each]

docs/architecture/chat-domain-model.md
docs/runbooks/task-chat-domain-workspaces-channels-permissions.md
```

---

## Data Model

### `chat.workspaces`

| Column     | Type        | Notes                        |
| ---------- | ----------- | ---------------------------- |
| id         | UUID        | PK, gen_random_uuid()        |
| slug       | TEXT        | UNIQUE, lowercase snake_case |
| name       | TEXT        | Display name                 |
| status     | TEXT        | active / disabled            |
| created_at | TIMESTAMPTZ | DEFAULT now()                |
| updated_at | TIMESTAMPTZ | DEFAULT now()                |

Seeded: `id='00000000-0000-0000-0000-000000000001'`, `slug='default'`, `name='NChat'`, `status='active'`.

### `chat.channel_categories`

| Column       | Type        | Notes                 |
| ------------ | ----------- | --------------------- |
| id           | UUID        | PK, gen_random_uuid() |
| workspace_id | UUID        | FK → chat.workspaces  |
| name         | TEXT        | NOT NULL              |
| position     | INT         | Sort order, DEFAULT 0 |
| created_at   | TIMESTAMPTZ |                       |
| updated_at   | TIMESTAMPTZ |                       |

### `chat.channels`

| Column       | Type        | Notes                                                 |
| ------------ | ----------- | ----------------------------------------------------- |
| id           | UUID        | PK, gen_random_uuid()                                 |
| workspace_id | UUID        | FK → chat.workspaces                                  |
| category_id  | UUID        | FK → chat.channel_categories, nullable                |
| slug         | TEXT        | UNIQUE per workspace; UNIQUE(workspace_id, slug)      |
| display_name | TEXT        | NOT NULL                                              |
| type         | TEXT        | public / private; CHECK constraint                    |
| status       | TEXT        | active / archived; CHECK constraint                   |
| is_general   | BOOLEAN     | DEFAULT false; partial UNIQUE: one true per workspace |
| position     | INT         | Sort order, DEFAULT 0                                 |
| created_by   | UUID        | user_id (no FK), nullable for system/seed rows        |
| created_at   | TIMESTAMPTZ |                                                       |
| updated_at   | TIMESTAMPTZ |                                                       |

Seeded: `id='00000000-0000-0000-0000-000000000002'`, `workspace_id=default`, `slug='geral'`,
`display_name='Geral'`, `type='public'`, `is_general=true`.

### `chat.workspace_members`

| Column       | Type        | Notes                                      |
| ------------ | ----------- | ------------------------------------------ |
| workspace_id | UUID        | PK part, FK → chat.workspaces              |
| user_id      | UUID        | PK part, no FK (auth.users ref, app-layer) |
| role         | TEXT        | owner / admin / member / guest             |
| status       | TEXT        | active / suspended / left                  |
| joined_at    | TIMESTAMPTZ | NOT NULL DEFAULT now()                     |

### `chat.channel_members`

| Column     | Type        | Notes                                      |
| ---------- | ----------- | ------------------------------------------ |
| channel_id | UUID        | PK part, FK → chat.channels                |
| user_id    | UUID        | PK part, no FK (auth.users ref, app-layer) |
| role       | TEXT        | member / moderator                         |
| joined_at  | TIMESTAMPTZ | NOT NULL DEFAULT now()                     |

---

## Permission Rules

| Scenario                                             | Result                            |
| ---------------------------------------------------- | --------------------------------- |
| User is active workspace member → read public ch.    | ALLOW                             |
| User is not workspace member → any channel           | DENY                              |
| User is workspace member → read private channel      | DENY (unless channel_member)      |
| User is channel_member → read/write private          | ALLOW                             |
| `is_general=true` channel → active workspace members | ALLOW without channel_members row |

Access to `#geral` depends on active workspace membership, not on a
`channel_members` row. `EnsureGeneralMembership` inserts that row only when it is
called explicitly; joining a workspace does not call it automatically.

Functions: `CanReadChannel(wm *WorkspaceMember, cm *ChannelMember, ch Channel) bool`
and `CanWriteChannel(...)`.

---

## Go Layer

### `domain` package

- Structs: `Workspace`, `ChannelCategory`, `Channel`, `WorkspaceMember`, `ChannelMember`
- Constants: `ChannelTypePublic`, `ChannelTypePrivate`; `RoleOwner/Admin/Member/Guest`; `ChannelRoleMember/Moderator`
- Permission helpers: pure functions, no DB calls
- Errors: `ErrNotFound`, `ErrDuplicateSlug`, `ErrForbidden`, `ErrInvalidInput`, `ErrAlreadyMember`

### `storage` package

Storage interfaces with PGX implementations, following the Pool interface pattern from `auth-service`:

- `WorkspaceStore`: `GetDefaultWorkspace`, `GetWorkspaceBySlug`
- `ChannelStore`: `CreateCategory`, `CreateChannel`, `GetChannelByID`, `ListChannelsByWorkspace`
- `MemberStore`: `AddWorkspaceMember`, `GetWorkspaceMember`, `AddChannelMember`, `GetChannelMember`

### `service` package

Thin services wrapping store interfaces:

- `WorkspaceService`: `GetDefault`, `CreateCategory`, `CreateChannel` (validates slug, type)
- `MemberService`: `JoinWorkspace`, `JoinChannel`, `EnsureGeneralMembership`
- `PermissionService`: `ListVisibleChannels`, `CanRead`, `CanWrite`

---

## Tests

Unit tests with fake stores (no DB required), covering:

1. Idempotent seed — default workspace and `#geral` must not duplicate
2. Active workspace member sees public channels
3. Non-member sees no workspace channels
4. Private channel visible only to channel members
5. `#geral` accessible for workspace members
6. Unique slug constraint error propagation
7. Archived/disabled entities excluded
8. Permission helper: public/private/general cases

---

## Out of Scope

- Full multi-workspace UX, workspace switching, cross-workspace identity (RF-68..RF-72)
- Whitelabel / custom domains
- Full RBAC matrix (RF-74) — minimal role/permission foundation only
- Messages, WebSocket, E2E/MLS
- Channel moderation UI, admin UI changes
- Search indexing, notifications, file upload
