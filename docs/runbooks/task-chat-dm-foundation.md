# Task: Chat DM — Conversation Foundation

**Branch:** feat/chat-dm-foundation
**Status:** Foundation + authenticated direct-DM HTTP contracts

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

`GET /api/chat/dm-candidates?query=<prefix>&limit=<n>` returns active users from
the caller's default workspace. The caller and workspace come from the validated
session/server-side workspace resolver. Queries contain 2–64 characters; results
default to 20 and are capped at 50. The response exposes only `user_id` and
`display_name` and excludes the caller.

`POST /api/chat/dms` accepts `application/json` with the strict body
`{"other_user_id":"<uuid>"}`. It returns the canonical direct conversation as
`{"data":{"conversation_id":"<uuid>","created":true|false}}`. Repeated or
concurrent requests for the same pair return the same ID; an ineligible target is
reported as `404` without distinguishing missing, inactive, or cross-workspace
users.

Both routes require a valid Bearer access token and active session. Their
per-user fixed-window limits are stored atomically in Valkey and shared across
replicas: 30 searches and 10 get-or-create calls per 60 seconds, in independent
namespaces. Valkey failure is fail-closed with `503`. Client-provided
caller/workspace identities, participant lists, roles, and membership fields
are not accepted.

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
