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

| Situation                                         | Outcome                    |
| ------------------------------------------------- | -------------------------- |
| Caller + at least two other active members        | Group DM created           |
| Caller omitted from participant list              | Caller added automatically |
| Duplicate invited participant IDs                 | De-duplicated              |
| Fewer than three unique participants total        | `ErrInvalidInput`          |
| More than 50 participants total (caller included) | `ErrInvalidInput`          |
| Missing/suspended/left/cross-workspace member     | `ErrForbidden`             |
| Participant whose account is disabled or deleted  | `ErrForbidden`             |
| Title present and <= 120 characters after trim    | Stored                     |
| Title empty after trim                            | Stored as NULL             |

Callers cannot set `created_by`, membership role, conversation status, or member
status. Group DM membership role is always `member`.

Participant eligibility is the same rule the 1:1 flow uses
(`MemberStore.GetEligibleDMMember`): active workspace, active workspace
membership, and an `auth.users` row that is `status = 'active'` with
`deleted_at IS NULL`. A workspace membership row outlives the account it points
at, so checking membership alone would let a disabled or deleted user be pulled
into a new conversation.

The service check is not the only guard. `chat.dm_members` is written by a single
`INSERT ... SELECT` that re-tests the same eligibility inside the creating
transaction and requires the number of inserted rows to equal the number of
requested participants. An account that stops being eligible between the service
check and the write therefore produces no row, the mismatch aborts the
transaction, and neither an orphan conversation nor a partial membership list
survives. The same statement backs the 1:1 flow, so both obey one rule.

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

`POST /api/chat/dms/group` accepts `application/json` with the strict body
`{"participant_user_ids":["<uuid>", ...],"title":"<optional>"}` and returns
`201` with `{"data":{"conversation_id":"<uuid>"}}`. The caller is added
server-side and must not appear in the list (a duplicate is de-duplicated, not
rejected). `title` may be omitted; blank titles are stored as NULL and the
sidebar then computes the `Grupo DM` fallback name. An ineligible or unknown
participant is reported as `404` without distinguishing missing, inactive, or
cross-workspace users; an invalid list size, a non-UUID ID, or an over-long
title is `400`. The participant cap is 50 including the caller and is checked
before any membership look-up, so an oversized payload costs one comparison.
Conversation and membership rows are written in a single transaction.

All three routes require a valid Bearer access token and active session. Their
per-user fixed-window limits are stored atomically in Valkey and shared across
replicas: 30 searches, 10 get-or-create calls, and 5 group creations per 60
seconds, in independent namespaces — exhausting the group budget does not
consume the direct one. Valkey failure is fail-closed with `503`.
Client-provided caller/workspace identities, roles, and membership fields are
not accepted, and unknown JSON fields are rejected outright.

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
