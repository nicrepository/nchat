# Task: Chat Domain — Workspaces, Channels & Permissions

**Branch:** feat/chat-domain-workspaces-channels-permissions
**Status:** MVP foundation

## What this implements

- `chat` PostgreSQL schema with 5 tables: `workspaces`, `channel_categories`,
  `channels`, `workspace_members`, `channel_members`.
- Seed: default workspace (`slug='default'`) and `#geral` channel (`is_general=true`).
- Domain structs, type constants, and permission helpers in `chat-service`.
- Storage layer (pgx) with interfaces: `WorkspaceStore`, `ChannelStore`, `MemberStore`.
- Service layer: `WorkspaceService`, `MemberService`, `PermissionService`.
- Workspace-bound channel authorization prevents cross-workspace ID access.
- User-visible channel lists enforce active workspace/member and private-channel
  visibility in SQL.
- Mandatory `#geral` membership sync: active workspace members are inserted into
  that workspace's `#geral` `channel_members` row during join/reactivation.
  `SyncGeneralMemberships(ctx, workspaceID)` backfills missing rows for active
  members only.
- Disabled workspaces deny channel list/read/write, category/channel creation,
  workspace membership auto-sync, and channel membership changes.
- Database constraints enforce workspace/category consistency and exactly one
  active public general channel per committed workspace.

## What this does NOT implement (out of scope)

- Full multi-workspace UX / workspace switching (RF-68..RF-72)
- Cross-workspace identity or separate identity per workspace
- Whitelabel / custom domains
- Full RBAC matrix (RF-74) — only minimal role/permission foundation
- Messaging, WebSocket, E2E/MLS
- Channel moderation UI or admin UI changes
- Search indexing, notifications, file upload

## Applying the migration

```bash
pnpm migrations:up
```

To roll back:

```bash
pnpm migrations:down
```

## Verifying seed data

After applying the migration:

```sql
SELECT id, slug, name, status FROM chat.workspaces;
SELECT id, slug, display_name, is_general FROM chat.channels;
```

Expected rows:

- `workspaces`: 1 row with `slug='default'`
- `channels`: 1 row with `slug='geral'`, `is_general=true`

The seed insert is idempotent. The schema rejects a second general channel,
private or archived general channels, cross-workspace category references, and
workspace commits that do not include an active public general channel.

## `#geral` membership sync

`#geral` is mandatory for every active workspace. When `MemberService` joins or
reactivates a workspace member, `chat-service` uses the storage transaction to:

1. verify the workspace is active;
2. create or activate the `workspace_members` row;
3. load the active public general channel by the same `workspace_id`;
4. insert the `channel_members` row with `ON CONFLICT DO NOTHING`.

The join/reactivation and `#geral` insert are atomic in the pgx store. If the
general channel is missing, the service returns `ErrGeneralChannelMissing`; it
does not create `#geral` in the membership path. Unexpected database errors are
propagated, and newly active members are not silently left unsynced. Duplicate
membership conflicts remain idempotent.

Suspended and left workspace members are not synced into `#geral`. Disabled
workspaces deny the sync. Authorization does not rely solely on the
`channel_members` row: active workspace membership still grants access to
`#geral`, so the sync row is a consistency aid rather than the only permission
source.

## Running tests

```bash
cd services/chat-service && go test -count=1 ./... -cover
```

## Validation checklist

```bash
pnpm migrations:check
pnpm fmt:go
pnpm lint:go
pnpm vet:go
pnpm test:go
pnpm format:check:docs
```
