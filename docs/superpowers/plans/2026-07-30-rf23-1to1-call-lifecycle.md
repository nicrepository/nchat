# RF-23 1:1 Call Lifecycle Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an authoritative, persistent one-to-one call signaling lifecycle and issue LiveKit tokens only for active participants.

**Architecture:** PostgreSQL stores and serializes call state, the existing WebSocket/Valkey path delivers user-targeted events, and media-service independently authorizes the active call from the shared database before signing. The React chat shell owns the minimal call UI and treats versioned server events as authoritative.

**Tech Stack:** Go 1.25, PostgreSQL 16, Valkey 8, WebSocket, React 19, TypeScript 5.9, Vitest.

---

### Task 1: Persist the call lifecycle

**Files:**

- Create: `migrations/chat/000019_call_lifecycle.up.sql`
- Create: `migrations/chat/000019_call_lifecycle.down.sql`
- Create: `services/chat-service/internal/domain/call.go`
- Create: `services/chat-service/internal/storage/call_store.go`
- Create: `services/chat-service/internal/storage/call_store_test.go`
- Modify: `services/chat-service/internal/storage/migration_invariants_test.go`

- [ ] **Step 1: Write failing migration and domain/store tests**

Cover the six statuses, audio/video types, participant indexes, unique start
request, due-call index, valid creation, self-call rejection, membership
authorization, idempotent start, busy participants, valid/invalid transitions,
terminal immutability, and concurrent transition winners.

- [ ] **Step 2: Run tests and verify RED**

Run:

```text
cd services/chat-service
go test ./internal/domain ./internal/storage
```

Expected: failures because `domain.Call`, `PGXCallStore`, and migration 000019 do
not exist.

- [ ] **Step 3: Implement the minimum schema and transactional store**

Create `chat.calls` with server-generated UUID/timestamps, `request_id`,
participants, `call_type`, `status`, `version`, `expires_at`, optional accepted
and ended timestamps, checks, and indexes. Implement:

```go
type CallStore interface {
    Create(context.Context, CreateCallInput) (domain.Call, bool, error)
    Transition(context.Context, TransitionCallInput) (domain.Call, bool, bool, error)
    CurrentForUser(context.Context, string, string) (domain.Call, error)
    ExpireDue(context.Context, int) ([]domain.Call, error)
}
```

`Transition` returns `(call, changed, timedOut, error)`, where `timedOut` means
the attempted command materialized an overdue ringing call.

- [ ] **Step 4: Run tests and verify GREEN**

Run:

```text
cd services/chat-service
go test -race ./internal/domain ./internal/storage
```

Expected: PASS.

### Task 2: Add the chat-service use case and expiry worker

**Files:**

- Create: `services/chat-service/internal/service/call_service.go`
- Create: `services/chat-service/internal/service/call_service_test.go`
- Modify: `services/chat-service/internal/config/config.go`
- Modify: `services/chat-service/internal/config/config_test.go`
- Modify: `services/chat-service/.env.example`

- [ ] **Step 1: Write failing service/config tests**

Cover input canonicalization, actor rules, all valid and invalid transitions,
duplicate commands, timeout materialization, expiry publication, default/configured
timeout, and cancellation of the worker context.

- [ ] **Step 2: Run tests and verify RED**

Run:

```text
cd services/chat-service
go test ./internal/service ./internal/config
```

Expected: failures because the call service and timeout setting do not exist.

- [ ] **Step 3: Implement the minimum service**

Expose `Start`, `Accept`, `Decline`, `Cancel`, `End`, `Current`, and
`ExpireDue`. Accept server-authenticated workspace/user IDs and typed command
data only. Add `CALL_RING_TIMEOUT_SECONDS`, default 30, and a context-cancelled
worker that calls `ExpireDue` on a short ticker without per-call timers.

- [ ] **Step 4: Run tests and verify GREEN**

Run:

```text
cd services/chat-service
go test -race ./internal/service ./internal/config
```

Expected: PASS.

### Task 3: Extend the existing WebSocket protocol

**Files:**

- Modify: `services/chat-service/internal/ws/event.go`
- Modify: `services/chat-service/internal/ws/errors.go`
- Modify: `services/chat-service/internal/ws/hub.go`
- Create: `services/chat-service/internal/ws/call_protocol_test.go`
- Modify: `services/chat-service/internal/ws/handler_test.go`
- Modify: `services/chat-service/internal/ws/bus_test.go`
- Modify: `services/chat-service/internal/app/app.go`
- Modify: `services/chat-service/internal/app/app_test.go`

- [ ] **Step 1: Write failing protocol, delivery, and integration tests**

Cover strict command bodies, server session identity, caller/callee delivery,
accept/decline/cancel/end/timeout delivery, stable errors, duplicates, stale
commands, reconnect sync, invalid frames remaining non-fatal, size limit, rate
limit, remote event canonicalization, and application shutdown.

- [ ] **Step 2: Run tests and verify RED**

Run:

```text
cd services/chat-service
go test ./internal/ws ./internal/app
```

Expected: failures because call commands/events and wiring do not exist.

- [ ] **Step 3: Implement the minimum protocol extension**

Add call fields to the existing strict `ClientMessage`, call payload/error fields
to the existing `Event`, `CallHandler`/`CallLimiter` hub options, user-targeted
broadcast, strict remote canonicalization, and application adapters. Reuse
`ValkeyReactionLimiter.AllowActionWithLimit` for `call.start`; do not add another
rate-limit implementation.

- [ ] **Step 4: Run tests and verify GREEN**

Run:

```text
cd services/chat-service
go test -race ./internal/ws ./internal/app
```

Expected: PASS.

### Task 4: Restrict media tokens to active calls

**Files:**

- Modify: `services/media-service/internal/domain/room.go`
- Modify: `services/media-service/internal/domain/room_test.go`
- Modify: `services/media-service/internal/service/token_service.go`
- Modify: `services/media-service/internal/service/token_service_test.go`
- Modify: `services/media-service/internal/service/livekit_signer.go`
- Modify: `services/media-service/internal/service/livekit_signer_test.go`
- Modify: `services/media-service/internal/storage/authorizer.go`
- Modify: `services/media-service/internal/storage/authorizer_test.go`
- Modify: `services/media-service/internal/http/livekit_token_handler.go`
- Modify: `services/media-service/internal/http/livekit_token_handler_test.go`

- [ ] **Step 1: Replace channel/DM expectations with failing call tests**

Require `call_id`, reject all client-controlled identity/room/grant fields, deny
ringing/terminal calls, deny non-participants, and derive `call:<uuid>` only for
an active call and active authenticated session.

- [ ] **Step 2: Run tests and verify RED**

Run:

```text
cd services/media-service
go test ./internal/domain ./internal/service ./internal/storage ./internal/http
```

Expected: failures because the current contract authorizes channel/DM resources.

- [ ] **Step 3: Implement the call-only authorization path**

Replace `ResourceKind` input with canonical `CallID`, query `chat.calls` with
participant and active-state predicates, and retain existing session expiry,
token TTL, grants, rate limit, and secret-safe error behavior.

- [ ] **Step 4: Run tests and verify GREEN**

Run:

```text
cd services/media-service
go test -race ./...
```

Expected: PASS.

### Task 5: Add the minimal React call experience

**Files:**

- Create: `apps/web/src/chat/callTypes.ts`
- Create: `apps/web/src/chat/callApi.ts`
- Create: `apps/web/src/chat/useCallLifecycle.ts`
- Create: `apps/web/src/chat/useCallLifecycle.test.ts`
- Create: `apps/web/src/chat/CallPanel.tsx`
- Create: `apps/web/src/chat/CallPanel.test.tsx`
- Modify: `apps/web/src/chat/useChatWebSocket.ts`
- Modify: `apps/web/src/chat/useChatWebSocket.test.ts`
- Modify: `apps/web/src/chat/ChatShell.tsx`
- Modify: `apps/web/src/chat/ChatShell.css`
- Modify: `apps/web/src/chat/ChatMessageArea.tsx`
- Modify: `apps/web/src/chat/ChatMessageArea.test.tsx`
- Modify: `apps/web/src/chat/ChatMessageArea.css`

- [ ] **Step 1: Write failing hook/component tests**

Cover incoming rendering, audio/video start, accept, decline, cancel, end,
timeout, backend errors, duplicate/stale versions, terminal cleanup,
double-click suppression, media-token failure/retry, and unchanged message
event routing.

- [ ] **Step 2: Run tests and verify RED**

Run:

```text
pnpm --filter @nchat/web test -- useCallLifecycle CallPanel ChatMessageArea useChatWebSocket
```

Expected: failures because the hook, panel, and call protocol are absent.

- [ ] **Step 3: Implement the minimum UI and reducer**

Use one global call-signaling connection in `ChatShell`, add audio/video buttons
only for direct DMs with a server-provided counterpart, render the existing
purple token palette, disable in-flight actions, compare event versions, clear
terminal state, and request `/api/media/media/livekit/token` only after active.
Keep returned tokens in memory only and never log them.

- [ ] **Step 4: Run tests and verify GREEN**

Run:

```text
pnpm --filter @nchat/web test -- useCallLifecycle CallPanel ChatMessageArea useChatWebSocket
```

Expected: PASS.

### Task 6: Document, format, verify, and inspect

**Files:**

- Create: `docs/api/calls-websocket.md`
- Modify: `docs/api/media-livekit-token.md`
- Modify: `docs/architecture/chat-domain-model.md`

- [ ] **Step 1: Document the final contracts and operational limits**

Record the state machine, commands/events, timeout setting, database-backed
concurrency, call-only media authorization, token retry behavior, and the
absence of RF-24 through RF-29 features.

- [ ] **Step 2: Format changed files**

Run `gofmt` on changed Go files and the repository's Prettier format check.

- [ ] **Step 3: Execute focused and full gates**

Run the requested Go race tests, vet, frontend lint/typecheck/test/build, then
the available Make gates. Record every command and any environment-dependent
failure exactly.

- [ ] **Step 4: Review the complete diff**

Run `git status`, `git diff --stat`, and `git diff`. Confirm no prototype,
infrastructure, unrelated refactor, secret, token, temporary log, dead code, or
untracked generated artifact was introduced.
