# Task: Chat Domain — Workspaces, Channels & Permissions

**Branch:** feat/chat-channel-crud
**Status:** MVP foundation + channel CRUD service/storage foundation

> **Historical runbook.** It records the state at the time of the original task.
> Two of its rules have since been superseded and are corrected in place below,
> marked "superseded": channel-creation authorization (BUG #393, then RF-74) and
> the `#geral` membership sync (RF-74, migration 000022 — guests are excluded).
> The current authority is
> [docs/security/rbac-matrix.md](../security/rbac-matrix.md) for roles and
> [docs/architecture/chat-domain-model.md](../architecture/chat-domain-model.md)
> for the schema; nothing else on this page was re-verified against today's code.

## What this implements

- `chat` PostgreSQL schema with 5 tables: `workspaces`, `channel_categories`,
  `channels`, `workspace_members`, `channel_members`.
- Seed: default workspace (`slug='default'`) and `#geral` channel (`is_general=true`).
- Domain structs, type constants, and permission helpers in `chat-service`.
- Storage layer (pgx) with interfaces: `WorkspaceStore`, `ChannelStore`, `MemberStore`.
- Service layer: `WorkspaceService`, `MemberService`, `PermissionService`.
- Service/storage channel CRUD foundation through `ChannelService`:
  create, list, read by ID/slug, update mutable fields, and archive.
- Workspace-bound channel authorization prevents cross-workspace ID access.
- User-visible channel lists enforce active workspace/member and private-channel
  visibility in SQL.
- Visibility-bound channel reads enforce active workspace/member and
  private-channel membership in SQL.
- Private-channel creation adds the creator as a channel member transactionally.
  Public-channel creation does not create channel membership rows for all
  workspace members.
- Public-to-private updates add the manager as a channel member in the same
  storage transaction. Private-to-public updates are allowed for managers.
- Archive is a status change (`status='archived'`), not a hard delete.
- `#geral` is immutable through CRUD: callers cannot create slug `geral`, set
  `is_general`, edit the general channel, or archive it.
- Mandatory `#geral` membership sync **(superseded by RF-74)**: active workspace
  members are inserted into that workspace's `#geral` `channel_members` row
  during join/reactivation, and `SyncGeneralMemberships(ctx, workspaceID)`
  backfills missing rows. Since migration 000022 the sync covers the roles
  `generalMembershipRoles` names — active `owner`, `admin`, `moderator` and
  `member` — and **excludes `guest`**: a guest's workspace membership grants no
  channel on its own, so it reaches only the channels it holds an explicit
  `channel_members` row for. The backfill never removes a row, so a guest that
  already holds one keeps it. RF-18 ("every user joins `#geral` automatically")
  and this RF-74 exclusion have not been reconciled; issue #882 owns that
  decision, and the current behaviour is characterized in
  [chat-membership-contracts.md](../architecture/chat-membership-contracts.md).
- Disabled workspaces deny channel list/read/write, category/channel creation,
  workspace membership auto-sync, and channel membership changes.
- Database constraints enforce workspace/category consistency and exactly one
  active public general channel per committed workspace.

## Channel CRUD service/storage contract

No HTTP CRUD endpoints are exposed in this PR. `chat-service` currently exposes
only health/readiness/version and has no authenticated user context or middleware.
Adding public REST handlers now would create misleading authorization behavior
or IDOR risk. The API surface below is deferred until the service can bind every
request to a verified caller:

- `POST /api/chat/workspaces/{workspace_id}/channels`
- `GET /api/chat/workspaces/{workspace_id}/channels`
- `GET /api/chat/workspaces/{workspace_id}/channels/{channel_id}`
- `PATCH /api/chat/workspaces/{workspace_id}/channels/{channel_id}`
- `DELETE /api/chat/workspaces/{workspace_id}/channels/{channel_id}`

Current callable backend foundation:

- `ChannelService.CreateChannel(ctx, input)` requires active workspace and active
  caller membership. The manager-role requirement stated here is **superseded**:
  BUG #393 removed it, so creation takes no management role, and RF-74 then
  narrowed it by exactly one role via `domain.CanCreateChannel` (which delegates
  to `domain.CanReachPublicChannels`) — active `owner`, `admin`, `moderator` and
  `member` may create; `guest` may not. The store re-derives the same allowlist
  inside the insert. The service validates slug/display name/type/category,
  reserves slug `geral`, sets `created_by` from the caller, and never accepts
  caller-provided `is_general` or `status`.
- `ChannelService.ListChannels(ctx, workspaceID, callerID)` requires active
  workspace membership and delegates visibility to
  `ChannelStore.ListVisibleChannelsByUser`.
- `ChannelService.GetChannel(ctx, input)` requires active workspace membership
  and reads by channel ID or slug through SQL visibility checks. Hidden private,
  archived, missing, and cross-workspace channels return not found from storage.
- `ChannelService.UpdateChannel(ctx, input)` requires `owner` or `admin`, rejects
  `#geral`, validates duplicate/reserved slugs and workspace-bound categories,
  and updates only mutable fields.
- `ChannelService.ArchiveChannel(ctx, workspaceID, channelID, callerID)` requires
  `owner` or `admin`, rejects `#geral`, and marks the channel archived.

Manager permission is intentionally minimal for this foundation. Full RBAC
(RF-74), full multi-workspace UX/workspace switching (RF-68..RF-72), frontend UI,
messages, WebSocket, search, notifications, gateway changes, and auth-service
changes remain out of scope.

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
4. under CURRENT RF-74, insert the `channel_members` row with
   `ON CONFLICT DO NOTHING` only for owner/admin/moderator/member; guest is
   excluded by `generalMembershipRoles`.

The join/reactivation and `#geral` insert are atomic in the pgx store. If the
general channel is missing, the service returns `ErrGeneralChannelMissing`; it
does not create `#geral` in the membership path. Unexpected database errors are
propagated, and newly active members eligible for sync are not silently left
unsynced. Guests remain excluded. Duplicate
membership conflicts remain idempotent.

Suspended and left workspace members are not synced into `#geral`. Disabled
workspaces deny the sync. The original access rule was refined by RF-74:
active workspace membership is necessary, but is not sufficient for every
channel and role. Currently, owners, admins, moderators and members have implicit
access to active public channels, including `#geral`; guests require explicit
`channel_members`. Private channels require explicit channel membership for every
role. Disabled workspaces and inactive workspace memberships still deny access.
See the [current membership contract](../architecture/chat-membership-contracts.md);
the future `#geral` decision remains with #882.

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
