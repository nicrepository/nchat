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

## Running tests

```bash
cd services/chat-service && go test -count=1 ./...
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
