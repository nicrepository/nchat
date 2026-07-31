# RF-23 1:1 Call Lifecycle Design

## Goal

Implement the authoritative signaling lifecycle for one-to-one audio and video
calls while keeping LiveKit credential issuance outside chat-service.

## Scope

The lifecycle supports start, accept, decline, cancel, end, timeout, reconnect
sync, and call-scoped LiveKit token issuance. Channel calls, group calls, screen
sharing, recording, moderation, push notifications, and call history are out of
scope.

## Architecture

PostgreSQL is the source of truth. `chat.calls` stores the participants, type,
status, monotonic version, server timestamps, timeout deadline, and the
originating `request_id`. Chat-service applies every transition in a transaction
that locks the call row; participant advisory locks serialize creation so one
user cannot enter two concurrent ringing/active calls.

Chat-service publishes authoritative call snapshots through the existing
WebSocket envelope and Valkey broadcast bus. Call events are user-targeted and
delivered only when the connection's authenticated user matches the server
generated target. Remote events are strictly canonicalized before delivery.

Media-service keeps the existing authenticated HTTP endpoint but accepts only a
`call_id`. Its PostgreSQL authorization joins the active authenticated session
to `chat.calls`, requires the user to be a participant and the status to be
`active`, and derives the LiveKit room as `call:<canonical-call-id>`.

The web application opens a global call-signaling connection within the chat
shell using the existing WebSocket endpoint and subprotocol. It renders a small
call panel, sends one command at a time, ignores duplicate or older versions,
clears terminal calls locally, and requests a media token only after an
authoritative active event.

## State Machine

- creation -> `ringing`
- `ringing` -> `active`, `declined`, `cancelled`, or `timed_out`
- `active` -> `ended`

Only the callee may accept or decline. Only the caller may cancel while
ringing. Either participant may end an active call. Terminal states are
immutable. Repeating the command that produced the current state is idempotent
for the same authorized actor and does not publish another event.

## Timeout

`CALL_RING_TIMEOUT_SECONDS` configures the persisted `expires_at` deadline and
defaults to 30 seconds. A background worker periodically claims expired ringing
rows with `FOR UPDATE SKIP LOCKED`, updates each row once, and publishes
`call.timed_out`. A command arriving after the deadline first materializes the
timeout under the same row lock, so a late accept cannot win. There are no
per-call process timers to orphan, and a restarted instance resumes from the
database.

## Concurrency and Idempotency

The row lock makes accept/decline/cancel/timeout deterministic: the first valid
transition commits and every later incompatible transition observes the new
state and fails with `invalid_call_state`. `version` increments on every real
transition and lets clients ignore duplicates or stale events.

Start requires a client-generated UUID `request_id`, unique per workspace and
caller. Replaying the same start returns the existing call when its target and
type match. A different payload with the same key returns a conflict.

## WebSocket Contract

Commands:

- `call.start`: `request_id`, `target_user_id`, `call_type`
- `call.accept`, `call.decline`, `call.cancel`, `call.end`: `request_id`, `call_id`
- `call.sync`: no identity or call state fields

Events:

- `call.ringing`, `call.accepted`, `call.declined`, `call.cancelled`,
  `call.timed_out`, `call.ended`
- `call.error`: stable `code`, `operation`, optional `request_id` and `call_id`

The call snapshot contains only server-derived IDs, type, status, version, and
timestamps. Actor identity, caller identity, participant lists, room names,
tokens, and desired state are never accepted from the client.

## Media Token Contract

`POST /media/livekit/token` accepts:

```json
{ "call_id": "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa" }
```

The authenticated JWT supplies user and session identity. PostgreSQL supplies
the call, participants, active state, and canonical room ID. Existing short TTL
bounds, grants, rate limiting, and secret-safe logging remain in place. Token
failure leaves the accepted call authoritative and returns a stable error; the
same participant may retry without creating a new call.

## Abuse Controls

The existing WebSocket frame size, connection count, malformed-message budget,
and per-connection token bucket remain active. `call.start` also uses the
existing shared Valkey action limiter with a dedicated bounded key, and the
database rejects a new call while either participant already has a ringing or
active call.

## Testing

Domain/service/storage tests cover every transition, authorization failure,
idempotency, timeout, and transition race. WebSocket tests cover two
authenticated clients, duplicate/out-of-order commands, invalid payloads,
reconnect behavior, payload limits, and unchanged rate limiting. Media-service
tests prove tokens cannot be issued before acceptance or to outsiders.
Frontend tests cover every visible lifecycle state, duplicate/out-of-order
events, double-click suppression, token failure/retry, and cleanup.
