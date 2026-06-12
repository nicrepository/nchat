# Task: Chat DM — Conversation Foundation

**Branch:** feat/chat-dm-foundation
**Status:** Foundation — service/storage/tests/docs only; no HTTP API

## What this implements

- `DMService.CreateDirectConversation(ctx, input)` creates or returns the
  canonical direct 1:1 DM for two active workspace members.
- `DMService.CreateGroupConversation(ctx, input)` creates an ad-hoc group DM,
  adds the caller automatically, and stores all participants as `member`.
- `DMService.ListConversations(ctx, workspaceID, callerID)` returns only active
  DM conversations visible to the caller.
- `DMService.GetConversation(ctx, input)` reads one active visible DM
  conversation with non-enumerating `ErrNotFound` behavior.
- `PGXDMStore` persists conversations in `chat.dm_conversations` and
  memberships in `chat.dm_members`.

## Direct 1:1 semantics

User-provided caller and participant IDs are validated and canonicalized as UUIDs
(lowercase, hyphenated) before any comparison, de-duplication, pair-key computation,
or storage call. Non-UUID input returns `ErrInvalidInput` immediately, before any
membership or workspace look-up.

| Situation                                          | Outcome                                     |
| -------------------------------------------------- | ------------------------------------------- |
| Two active members in same workspace               | Create or return canonical direct DM        |
| Same pair, reversed caller/order                   | Same conversation                           |
| Same pair in different workspace                   | Separate conversation                       |
| Existing archived direct conversation              | Reactivated; no duplicate direct DM created |
| Caller attempts self-DM                            | `ErrInvalidInput`                           |
| Missing/suspended/left/cross-workspace participant | `ErrForbidden`                              |
| Disabled or missing workspace                      | `ErrForbidden`                              |

The direct pair uniqueness key is internal only. It is computed by the service,
stored only to support database uniqueness, and must not be accepted from callers
or exposed through future API responses.

## Group DM semantics

| Situation                                      | Outcome                    |
| ---------------------------------------------- | -------------------------- |
| Caller + at least two other active members     | Group DM created           |
| Caller omitted from participant list           | Caller added automatically |
| Duplicate invited participant IDs              | De-duplicated              |
| Fewer than three unique participants total     | `ErrInvalidInput`          |
| Missing/suspended/left/cross-workspace member  | `ErrForbidden`             |
| Title present and <= 120 characters after trim | Stored                     |
| Title empty after trim                         | Stored as NULL             |

Callers cannot set `created_by`, membership role, conversation status, or member
status. Group DM membership role is always `member`.

## Visibility and non-enumeration

Read/list visibility is enforced in SQL, not by Go-only filtering. The storage
queries require:

- active workspace;
- active caller workspace membership;
- active DM conversation;
- active `dm_members` row for the caller.

Missing conversations, cross-workspace IDs, archived conversations, and
non-participant reads all return the same not-found style error:
`domain.ErrNotFound`.

## HTTP API

No HTTP endpoints are exposed. `chat-service` has no authenticated user context
or JWT middleware, so DM endpoints are deferred until every request can be bound
to a verified caller identity.

## Out of scope

- Message table/body delivery
- WebSocket delivery
- Notifications
- File upload
- Search
- Frontend UI
- E2E/MLS and key-service integration
- auth-service changes
- FK constraints to `auth.users`
- Full RBAC (RF-74)
- Full multi-workspace UX (RF-68..RF-72)
