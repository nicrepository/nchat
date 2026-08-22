# Call Participation Fencing Implementation Plan

> **For agentic workers:** Execute inline, task-by-task. Do not commit, push, reset, rebase, or discard the existing dirty worktree.

**Goal:** Fence every resource-call admission with a fresh requester-only UUID so stale tabs cannot mutate or authorize against a newer lease.

**Architecture:** Add a nullable rollout column whose `NULL` value is legacy-only. Admission atomically rotates a non-null UUID; presence and leave only mutate an existing row matching that UUID. Propagate the UUID through requester-only WebSocket admission/leave contracts and require it for resource-call LiveKit authorization, while direct calls and discovery remain unchanged.

**Tech Stack:** PostgreSQL migrations, Go/pgx services, WebSocket JSON, React/TypeScript, Vitest.

---

### Task 1: Chat migration and storage contract

**Files:**

- Create/finish: `migrations/chat/000035_call_participant_lease_identity.up.sql`
- Create/finish: `migrations/chat/000035_call_participant_lease_identity.down.sql`
- Modify: `services/chat-service/internal/storage/call_store.go`
- Test: `services/chat-service/internal/storage/call_migration_test.go`
- Test: `services/chat-service/internal/storage/call_join_postgres_test.go`
- Test: `services/chat-service/internal/storage/call_leave_postgres_test.go`

- [ ] Add migration assertions: nullable UUID, no redundant index, schema-qualified rollback.
- [ ] Run focused migration test and confirm RED.
- [ ] Add PostgreSQL tests for P1→P2 rotation, stale/current leave, stale/current presence, no resurrection, no ABA, legacy-null fail-closed, and concurrent join/leave serialization.
- [ ] Run focused storage tests and confirm RED/build failures identify the missing contract.
- [ ] Implement the minimum atomic admission, fenced update, and fenced delete while preserving participant-advisory-lock → call-row-lock order.
- [ ] Run focused storage tests and confirm GREEN.

### Task 2: Chat service and WebSocket requester contract

**Files:**

- Modify: `services/chat-service/internal/domain/errors.go`
- Modify: `services/chat-service/internal/service/call_service.go`
- Modify: `services/chat-service/internal/app/call_adapter.go`
- Modify: `services/chat-service/internal/ws/event.go`
- Modify: `services/chat-service/internal/ws/call_protocol.go`
- Test: `services/chat-service/internal/service/call_service_test.go`
- Test: `services/chat-service/internal/app/call_adapter_test.go`
- Test: `services/chat-service/internal/ws/call_protocol_test.go`
- Test: `services/chat-service/internal/ws/call_join_sync_protocol_test.go`

- [ ] Add tests that admission returns the same transaction-generated canonical UUID without placing it in `Call`.
- [ ] Add tests that resource `call.admitted` requires requester-only `participation_id`, direct stays unchanged, and discovery/lifecycle never leak it.
- [ ] Add tests for `call.presence`/`call.leave` validation, `call_participation_stale`, and explicit requester-only successful-leave ACK.
- [ ] Run focused tests and confirm RED.
- [ ] Propagate the minimal typed results through storage → service → adapter → WS.
- [ ] Run focused tests and confirm GREEN.

### Task 3: Media token fencing

**Files:**

- Modify: `services/media-service/internal/http/livekit_token_handler.go`
- Modify: `services/media-service/internal/service/token_service.go`
- Modify: `services/media-service/internal/storage/authorizer.go`
- Test: `services/media-service/internal/http/livekit_token_handler_test.go`
- Test: `services/media-service/internal/service/token_service_test.go`
- Test: `services/media-service/internal/storage/call_authorizer_test.go`
- Test: `services/media-service/internal/storage/authorizer_postgres_integration_test.go`

- [ ] Add RED tests for canonical resource participation UUID propagation and direct-call omission.
- [ ] Add RED PostgreSQL cases for current/stale/wrong-user/wrong-call/expired participation across channel and group DM, plus unchanged direct caller/callee.
- [ ] Require matching `participation_id` only on resource-call branches; missing ID may match only a legacy `NULL` lease.
- [ ] Run focused unit/integration tests and confirm GREEN.

### Task 4: Frontend signaling and session fencing

**Files:**

- Modify: `apps/web/src/chat/callApi.ts`
- Modify: `apps/web/src/chat/resourceCallSignaling.ts`
- Modify: `apps/web/src/chat/useResourceCallSession.ts`
- Modify only as needed: `apps/web/src/calls/CallSessionProvider.tsx`
- Test: `apps/web/src/chat/callApi.test.ts`
- Test: `apps/web/src/chat/resourceCallSignaling.test.ts`
- Test: `apps/web/src/chat/useResourceCallSession.test.ts`
- Test only as needed: `apps/web/src/calls/CallSessionProvider.test.tsx`

- [ ] Add RED signaling tests: admitted UUID required, start/join return `{call, participationId}`, leave/presence send P, unrelated replies ignored, stale leave distinct.
- [ ] Add RED session tests: token uses captured P, compensation uses attempt-local P, explicit leave uses current P, stale leave suppresses global `left`, stale presence stops heartbeat, reconnect/handoff re-admit P1→P2→P3.
- [ ] Store `participationIdRef` beside `callIdRef`, atomically update both after admission, and clear both together.
- [ ] Change reconnect/reclaim to `call.join` before every new token; never reuse an old P.
- [ ] Return a leave outcome so the provider publishes `left` only when the server actually released that P.
- [ ] Run focused and broader calls/chat tests and confirm GREEN.

### Task 5: Documentation and verification

**Files:**

- Modify: `docs/api/calls-websocket.md`
- Modify: `docs/api/media-livekit-token.md`

- [ ] Document requester-only fencing, per-admission rotation, fail-closed legacy rollout, stale semantics, direct-call compatibility, and non-leak guarantees.
- [ ] Run Go tests/vet/build for chat and media, including PostgreSQL integration when test DSNs are available.
- [ ] Run focused/broader frontend tests, typecheck, lint, official format checks, and build.
- [ ] Run `git diff --check`, adversarially inspect all listed race/non-leak/lock-order/ABA cases, then capture `git diff --stat` and `git status --short`.
