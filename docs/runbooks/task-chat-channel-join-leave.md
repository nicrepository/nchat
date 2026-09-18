# Task: Chat Channel — Join/Leave Foundation

**Branch:** feat/chat-channel-join-leave
**Status:** Foundation — service/storage/tests only; no HTTP API

> Historical foundation; CURRENT RF-18/RF-74 updates: structural `#geral`
> (`channels.is_general`, never the display name) automatically materializes
> active owner/admin/moderator/member/guest with active undeleted accounts.
> Guest still cannot self-join ordinary public channels. Chat add-members permits
> idempotent membership/repair in `#geral` under owner/admin/moderator; Admin API
> uses its separate `admin.channels.manage` gate and the same target eligibility.
> Rename/archive/leave/remove remain forbidden structurally; mute is preserved.
> See [current contracts](../architecture/chat-membership-contracts.md) (#882).

## What this implements

- `MemberService.SelfJoinChannel(ctx, workspaceID, channelID, userID)` — active
  workspace member self-joins a **public** active channel. Private channel self-join returns
  `ErrNotFound` (non-enumerating — private channel existence is not disclosed).
  #geral explicit join is idempotent for owner/admin/moderator/member; guest
  membership is materialized automatically and guest self-join remains forbidden.
- `MemberService.LeaveChannel(ctx, workspaceID, channelID, userID)` — removes the
  caller from a channel. Idempotent for non-members. #geral cannot be left.
  Archived channels return `ErrNotFound`.
- `MemberService.RemoveMemberFromChannel(ctx, workspaceID, channelID, callerID, targetUserID)`
  — workspace owner, admin or moderator removes any member from a non-#geral channel.
- `MemberStore.RemoveChannelMember(ctx, workspaceID, channelID, userID)` — workspace-scoped
  DELETE with defensive `is_general` guard; idempotent when the row does not exist.
- `domain.ErrCannotLeaveGeneralChannel` — new sentinel for explicit #geral leave/remove attempts.

## Removed

- `MemberService.JoinChannel(ctx, channelID, userID, role)` — removed. This method
  used a global `GetChannelByID` lookup (not workspace-scoped), creating a potential
  authorization footgun. `SelfJoinChannel` supersedes it with workspace-bound lookup,
  non-enumerating private-channel denial (ErrNotFound), archived-channel denial, and no role parameter.

## Join semantics

| Caller state                        | Public channel | Private channel                   | #geral                        |
| ----------------------------------- | -------------- | --------------------------------- | ----------------------------- |
| Active owner/admin/moderator/member | Allowed        | **ErrNotFound** (non-enumerating) | Idempotent (returns existing) |
| Suspended member                    | ErrForbidden   | ErrNotFound                       | ErrForbidden                  |
| Left member                         | ErrForbidden   | ErrNotFound                       | ErrForbidden                  |
| Non-workspace member                | ErrForbidden   | ErrNotFound                       | ErrForbidden                  |
| Any — disabled workspace            | ErrForbidden   | ErrNotFound                       | ErrForbidden                  |
| Any — archived channel              | ErrNotFound    | ErrNotFound                       | ErrNotFound                   |
| Any — cross-workspace channel ID    | ErrNotFound    | ErrNotFound                       | ErrNotFound                   |

- Channel role is always `ChannelRoleMember`; callers cannot set it.
- Channel lookup uses `GetChannelByIDInWorkspace` — workspace-scoped, no IDOR possible.

## Leave semantics

| Situation                            | Outcome                                                         |
| ------------------------------------ | --------------------------------------------------------------- |
| Member leaves public/private channel | nil; channel_members row deleted                                |
| Attempt to leave #geral              | `ErrCannotLeaveGeneralChannel`                                  |
| Non-member leave                     | nil (idempotent)                                                |
| Archived channel                     | `ErrNotFound` (GetChannelByIDInWorkspace filters status=active) |
| Cross-workspace channel ID           | `ErrNotFound`                                                   |

- Only the `channel_members` row is deleted; workspace membership is not altered.
- Workspace membership status (suspended/left) does not affect leave — the row
  is removed if present. Stale `channel_members` rows grant no access regardless
  (enforced by `domain.CanReadChannel` via workspace member status check).

## Storage defence-in-depth

`RemoveChannelMember` in `PGXMemberStore` performs a two-step operation:

1. `SELECT is_general FROM chat.channels WHERE id = $1 AND workspace_id = $2`
   — returns `ErrCannotLeaveGeneralChannel` if `is_general = true`, preventing
   #geral membership removal even if the service-level guard is bypassed.
2. `DELETE FROM chat.channel_members WHERE channel_id = $1 AND user_id = $2`
   — workspace scoped by the prior SELECT; idempotent (0 rows deleted = nil).

## Manager remove

`RemoveMemberFromChannel` currently requires active workspace owner, admin or
moderator in the same workspace. Returns `ErrForbidden` for member/guest callers.
Cannot be used on #geral (returns `ErrForbidden`). Idempotent for non-members.

## Private channel invitations / approval flow

Private channel self-join returns `ErrNotFound` (non-enumerating). Adding a member to a private channel
requires a manager-add flow via `RemoveMemberFromChannel`'s inverse — an explicit
invitation or approval model — which is **future scope**. There is no invite model
in this foundation.

## HTTP API

No HTTP endpoints are exposed. `chat-service` has no authenticated user context or
JWT middleware. All channel membership endpoints are deferred until the service can
bind each request to a verified caller identity.

## Out of scope

- Private channel invitation / approval flow
- Full RBAC (RF-74)
- Full multi-workspace UX (RF-68..RF-72)
- Frontend, messages, WebSocket, search, notifications
- auth-service changes
- FK constraints to auth.users
