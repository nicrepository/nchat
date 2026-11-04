# WebSocket Valkey Broadcast Bus

**Status:** Distributed broadcast plumbing only. No real WebSocket upgrade yet.
**Service:** `chat-service`
**PR:** `feat/chat-valkey-pubsub-broadcast` → `develop`

---

## Scope

This PR adds distributed WebSocket broadcast to `chat-service` via Valkey Pub/Sub.
When a message is created, all chat-service instances receive the notification and
deliver it to their local WebSocket subscribers.

### What is included

| Component              | File                        | Notes                                                     |
| ---------------------- | --------------------------- | --------------------------------------------------------- |
| BroadcastBus interface | `internal/ws/bus.go`        | `BroadcastBus`, `NopBus` (local-only default)             |
| Valkey adapter         | `internal/ws/valkey_bus.go` | `ValkeyBus` wrapping `valkey-io/valkey-go`                |
| Hub wiring             | `internal/ws/hub.go`        | `PublishMessageCreated` stamps EventID + SourceInstanceID |
| Event envelope         | `internal/ws/event.go`      | Added `EventID`, `SourceInstanceID`                       |
| Config                 | `internal/config/config.go` | `ValkeyURL`, `ValkeyWSBroadcastEnabled`, `WSInstanceID`   |

### What is NOT included (future scope)

- **Real WebSocket upgrade**: `ServeWS` is still a non-upgrading 501 stub.
- **Frontend integration**: No client-side code.
- **Durable notifications**: No PostgreSQL `notification_outbox`, no Valkey Streams.
- **Message send over WebSocket**: Writes go through REST HTTP; WS is server-push only.
- **Presence / typing indicators**: Out of scope.
- **End-to-end encryption / MLS**: Out of scope.
- **Auth-service or notification-service changes**: None.

---

## Architecture

```
chat-service instance A                    chat-service instance B
└── internal/ws                            └── internal/ws
    ├── Hub (in-process subscriptions)         ├── Hub (in-process subscriptions)
    │    └── PublishMessageCreated()            │    └── handleBroadcast()
    │         ├── local bcast → Hub.run()       │         └── delivers to local clients
    │         └── bus.Publish(evt)              └── ValkeyBus.Subscribe
    └── ValkeyBus.Publish                           └── receives PSUBSCRIBE message
          └── PUBLISH nchat:chat:ws:broadcast:{workspace_id}
                           │
                    Valkey Pub/Sub
```

### In-process hub remains the source of local subscriptions

The `Hub` owns all client subscription state. Valkey carries the signal only;
it does not own subscription state. All subscription management (Register,
Unregister, Subscribe) is in-process only.

### Broadcast flow

1. The caller (future: `MessageService`) calls `hub.PublishMessageCreated(ctx, workspaceID, targetType, targetID, messageID)`.
   **Note:** `MessageService` wiring is not done in this PR — `hub.PublishMessageCreated` is the
   intended integration point for a future PR that connects the message creation flow.
2. Hub stamps `EventID` (UUID) and `SourceInstanceID` on the event.
3. Hub enqueues local broadcast to its own `run` goroutine — **always succeeds**.
4. Hub calls `bus.Publish(ctx, evt)` — best-effort; failure is logged, not propagated.
5. Valkey delivers the event to all chat-service instances via `PSUBSCRIBE nchat:chat:ws:broadcast:*`.
6. Receiving instances call `handleRemoteBusEvent` which:
   - Drops self-echo events (`SourceInstanceID == h.instanceID`).
   - Validates event type, target type, and all UUID fields (`event_id`, `workspace_id`,
     `target_id`, `message_id`) — unknown types and malformed UUIDs are dropped
     fail-secure; valid UUIDs are canonicalized to lowercase.
   - Validates `source_instance_id`: non-empty, bounded length, safe chars only.
   - Enqueues to `remoteBcast` channel for `run` goroutine processing.
7. `run` goroutine calls `handleBroadcast` (same path as local events):
   - Auth re-check per subscriber via `SubscriptionAuthorizer.CanAccess`.
   - Delivers to authorized subscribers only.

---

## Delivery Semantics

> **Valkey Pub/Sub is best-effort only.**

| Property              | Guarantee                                        |
| --------------------- | ------------------------------------------------ |
| At-least-once         | ❌ No — fire-and-forget                          |
| Durability            | ❌ No — events are not persisted                 |
| Replay                | ❌ No — missed events during disconnect are lost |
| Missed-event recovery | ✅ Client-side, from PostgreSQL (issue #947)     |
| In-process delivery   | Best-effort; independent of Valkey state         |

Events are still never replayed. What converges is the client's view: every
(re)subscription — the first one of a conversation, and every one after a
reconnect — reads the open conversation back through the authorized message
listing and the sidebar endpoint, and a delivery the hub had to withhold is
followed by `conversation.sync_required` once access is proven again (see
Authorization). The database stays the only source of truth.

The read covers every message the client holds, not a recent window, so an old
edit, deletion or reaction converges too. A gap of new messages longer than a
few pages replaces the timeline with what was read, and older history is then
paged in from the server. Events applied while the read is in flight are
rebased on top of it, because reactions carry no version a timestamp could
order. See `apps/web/src/chat/messages/useTimelineCatchUp.ts` and
`catchUpReducer.ts`.

Durable notification delivery is a separate future concern:

- PostgreSQL `notification_outbox` pattern: not in this PR.
- Valkey Streams with consumer groups: not in this PR.

This PR is **distributed WS fan-out plumbing**, not a durable notification system.

---

## Channel Naming

```
nchat:chat:ws:broadcast:{workspace_id}
```

- **Workspace-scoped**: Each workspace has its own Valkey channel.
- **Pattern subscribe**: Instances subscribe with `PSUBSCRIBE nchat:chat:ws:broadcast:*`.
  This is simpler than per-workspace dynamic subscribe for the MVP.
  Trade-off: an instance receives events for all workspaces, not just ones with
  local subscribers. Auth re-check prevents cross-workspace delivery.
  Per-workspace dynamic subscribe is future optimization scope.
- **Deterministic and safe**: Channel names contain only the `workspace_id`
  (server-generated UUID; alphanumeric + hyphens). User-controlled raw values
  are never used in channel names.
- **No secrets or tokens** in channel names.

---

## Authorization

Every event received from Valkey undergoes the **same authorization re-check**
as locally published events via `SubscriptionAuthorizer.CanAccess`:

- Pub/Sub messages are **not trusted**. `workspace_id`/`target_id` from Valkey
  do not bypass authorization.
- Stale `channel_members`/`dm_members` are rejected because the SQL queries
  enforce active workspace membership on every call.
- Auth error or timeout (transient DB issue): skip delivery, keep
  subscription, and record a **gap** for that connection and target (issue
  #947). One entry per subscription however many events were skipped; it is
  dropped on unsubscribe, revocation or disconnect.
- Auth denied (`allowed=false`, no error): revoke subscription.
- Auth allowed: deliver event to subscriber's outbox.
- A check that fails only because the connection is already closing is
  `client_closed`: no gap, no warning — its successor reconciles on open.

A gap is closed when access is proven again for that subscription — by its next
delivery or, if nobody posts, by one background recovery pass per hub
(sequential, 500 ms then exponential backoff up to 30 s). The connection is then
sent, alone and never over the bus:

```json
{ "type": "conversation.sync_required", "target_type": "dm", "target_id": "…" }
```

It is route-only and sent only after a successful re-check; a denial found by
recovery revokes the subscription instead. Clients that predate it ignore the
unknown frame. Queuing it is not proof the client reconciled: if it cannot be
queued the client is dropped as slow, and every new connection reconciles on
open anyway.

Metrics (closed label sets, no user/target/connection labels):
`chat_ws_auth_recheck_total{target_type,outcome}`,
`chat_ws_sync_recovery_total{outcome}`, `chat_ws_disconnect_total{reason}`.

---

## Self-Echo Suppression

Each event is stamped with `SourceInstanceID` (the publishing instance's ID).
Remote instances drop events whose `SourceInstanceID` matches their own
`Hub.instanceID`, preventing duplicate local delivery.

`SourceInstanceID` is used for echo-suppression only — it is not a security
boundary. Authorization re-check is the security mechanism.

---

## Failure Behavior when Valkey is Unavailable

| Scenario                            | Behavior                                                       |
| ----------------------------------- | -------------------------------------------------------------- |
| Valkey publish fails                | Error logged; local delivery unaffected; no panic              |
| Valkey subscribe disconnects        | Reconnect loop with bounded backoff (100ms → 30s)              |
| Valkey permanently unavailable      | In-process delivery continues normally; no distributed fan-out |
| `VALKEY_WS_BROADCAST_ENABLED=false` | `NopBus` used; local-only delivery, no Valkey connection       |

---

## Testing across replicas

`internal/ws/sync_recovery_valkey_test.go` runs two hubs — two pods, each with
its own `ValkeyBus` connection — on a real Valkey: cross-pod delivery, a
re-check failure on the receiving pod, recovery without a new broadcast that
reaches only the affected connection, unsubscribe and reconnect during
recovery, and goroutine cleanup after shutdown.

What it does not cover: the authorizer and the client connections are test
doubles (the transient authorization failure is injected), and there is no HTTP
request, PostgreSQL or browser in the path. The browser half of the contract is
covered by `apps/web/e2e/messaging/realtime-reconnect-convergence.spec.ts`
against the mocked backend.

It is owned by `Tests / Go Integration` (`scripts/ci/go-integration-test.sh`),
whose job provisions the same Valkey image as the production overlay. The
script refuses to run without `CHAT_TEST_VALKEY_URL`, and the suite fails —
it does not skip — when that Valkey is unreachable. Locally:

```sh
docker run -d --rm --name valkey-test -p 56379:6379 valkey/valkey:8.1.3-alpine
CHAT_TEST_VALKEY_URL=redis://127.0.0.1:56379 \
  go test ./internal/ws -race -run '^TestSyncRecoveryValkey_' -count=1
```

---

## Configuration

| Env var                       | Default    | Description                                            |
| ----------------------------- | ---------- | ------------------------------------------------------ |
| `VALKEY_URL`                  | `""`       | Valkey address (e.g. `valkey://localhost:6379`)        |
| `VALKEY_WS_BROADCAST_ENABLED` | `false`    | Enable distributed broadcast; `false` = NopBus (safe)  |
| `WS_INSTANCE_ID`              | (auto-gen) | Stable instance ID for echo-suppression; UUID if unset |

Default `VALKEY_WS_BROADCAST_ENABLED=false` is safe for local development and
test environments — in-process delivery works without any Valkey configuration.

---

## No Frontend / No Real WS Endpoint

`ServeWS` remains a non-upgrading 501 stub. No WebSocket upgrade occurs in this
PR. The broadcast bus distributes events between service instances; client
connectivity is deferred to a future PR that adds authenticated WS upgrade.
