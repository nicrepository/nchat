# WebSocket Hub Foundation

**Status:** In-process foundation only. No client-facing endpoint yet.
**Service:** `chat-service`
**PR:** `feat/chat-websocket-hub` → `develop`

---

## Scope

This PR adds the in-process WebSocket hub package (`internal/ws`) to `chat-service`. It is the infrastructure foundation for realtime message delivery, not the completed realtime product.

### What is included

| Component               | File              | Notes                                                                |
| ----------------------- | ----------------- | -------------------------------------------------------------------- |
| Event envelope types    | `event.go`        | `Event`, `ClientMessage`, `TargetType`, `EventType`                  |
| Hub                     | `hub.go`          | Register / Unregister / Subscribe / PublishMessageCreated / Shutdown |
| Client                  | `client.go`       | Bounded outbox, slow-client drop, `sender` interface                 |
| Subscription authorizer | `subscription.go` | `SubscriptionAuthorizer` interface + `serviceAuthorizer` impl        |
| WS handler stub         | `handler.go`      | Returns 501; no upgrade until auth middleware exists                 |

### What is NOT included (future scope)

- **Distributed fan-out**: No Valkey, Redis, or pub-sub. This hub is single-instance only.
- **Frontend integration**: No client-side code.
- **Notifications, typing indicators**: Out of scope. Presence has since been added
  on top of this hub — see [presence over WebSocket](api/presence-websocket.md).
- **End-to-end encryption / MLS key service**: Out of scope.
- **Message send over WebSocket**: Writes go through REST HTTP; WS is server-push only.
- **Full load test framework**: Performance baselines to be measured later.
- **Delivery guarantees**: Best-effort in-process only (see Delivery Semantics below).

---

## Architecture

```
chat-service (single instance)
└── internal/ws
    ├── Hub  ──── run goroutine (single writer for all state)
    │    ├── clients:    map[clientID]*Client
    │    ├── subs:       map[targetKey]set[clientID]
    │    └── clientSubs: map[clientID]set[targetKey]
    ├── Client  (per connection)
    │    ├── outbox: chan []byte  (bounded, outboxSize=256)
    │    └── sender  (interface; coder/websocket impl when handler is enabled)
    └── SubscriptionAuthorizer  (interface; serviceAuthorizer in production)
         ├── channelReadChecker → PermissionService.CanRead (SQL)
         └── DMStore.GetVisibleConversationByID (SQL)
```

### WebSocket dependency

No WebSocket dependency is added in this foundation PR. The `ServeWS` handler is a non-upgrading stub (returns 501) and no compiled code calls `websocket.Accept`, so no library is needed yet.

`github.com/coder/websocket` is the preferred dependency when authenticated upgrade support is implemented, because:

- **Actively maintained**: formerly `nhooyr.io/websocket`; `gorilla/websocket` is in maintenance mode.
- **Pure Go**: no CGO; compiles to WASM.
- **Context-native**: `Accept`, `Read`, `Write` all accept `context.Context`.
- **Minimal API**: no protocol wrappers (Socket.IO etc.).

---

## Auth and Origin Assumptions

The `ServeWS` handler currently returns **501 Not Implemented** because `chat-service` has no authenticated request context. The following invariants **must** hold once the handler is fully implemented:

1. **No unauthenticated upgrades.** `websocket.Accept` must only be called after userID is verified.
2. **No credentials in query strings.** The handler rejects the `token`, `access_token`, and `authorization` query parameters now and in the future. Credentials must travel in cookies or headers only.
3. **No refresh token use for WebSocket auth.** The WS connection must use a short-lived access token verified server-side.
4. **Origin validation.** `websocket.AcceptOptions.OriginPatterns` must be set to the configured allowed-origins list before upgrade.

**Integration checklist** (for the PR that enables the endpoint):

1. Add auth middleware that extracts and verifies userID, stores it in `r.Context()`.
2. In `ServeWS`, read userID from context.
3. Validate `Origin` header against allowed origins.
4. Call `websocket.Accept` with `InsecureSkipVerify: false`.
5. Create `Client` with `newClient(uuid, userID, workspaceID, wsSender{conn})`.
6. Register with hub; defer Unregister and conn close.
7. Start read pump goroutine for inbound control messages.

---

## Authorization

Subscription authorization is enforced at two points:

1. **On subscribe**: `Hub.Subscribe` calls `SubscriptionAuthorizer.CanAccess` and returns `ErrSubscribeForbidden` (non-enumerating) if denied.
2. **On every broadcast**: Before enqueuing an event for a subscribed client, `handleBroadcast` re-checks authorization. If revoked (e.g., user removed from channel after subscribing), the subscription is silently removed and the event is not delivered.

This double-check prevents stale memberships from receiving future events after access is revoked. SQL visibility is the authoritative source of truth; the hub state is a cache of authorizations subject to staleness.

### Authorization rules

**Channel subscription requires:**

- Active workspace membership (`workspace_members.status = 'active'`)
- Active channel (`channels.status = 'active'`)
- Private channels additionally require active channel membership

**DM subscription requires:**

- Active workspace membership
- Active DM conversation membership (`GetVisibleConversationByID` enforces both in SQL)

**Fail-secure defaults:**

- Unknown target types → denied
- Missing, inaccessible, archived, or cross-workspace targets → denied (ErrSubscribeForbidden, non-enumerating)

---

## Backpressure / Slow Client Policy

- Each client has a bounded outbound queue (`outboxSize = 256` JSON-encoded events).
- `handleBroadcast` uses a non-blocking `select` to enqueue: if the outbox is full, the client is dropped immediately.
- The hub goroutine **never blocks** on a slow client.
- Dropped clients have their connection closed and all subscriptions removed atomically within the hub goroutine.

This prevents one slow or stalled client from blocking event delivery to all other clients.

---

## Inbound Rate Limiting

Each connection gets a per-connection token bucket (`readLoop` in `handler.go`) guarding inbound _control_ messages (subscribe, unsubscribe, ping, reaction.toggle, call.\*). Two independent settings control it:

| Env var                          | Default | Meaning                                                     |
| -------------------------------- | ------- | ----------------------------------------------------------- |
| `WS_INBOUND_MESSAGES_PER_MINUTE` | `60`    | Sustained rate: tokens refilled per minute.                 |
| `WS_INBOUND_BURST`               | `10`    | Bucket capacity: how many messages can arrive back-to-back. |

Exceeding the bucket closes the connection with `StatusPolicyViolation` (close code 1008) and reason `"rate limit exceeded"`.

**`nchat-dev-server` sets `WS_INBOUND_BURST=60`** (see `infra/k8s/overlays/nchat-dev-server/configmap-patch.yaml`). The web client's bootstrap sends 1 `call.sync` + one `subscribe` per sidebar item (12 observed) immediately after `open`, all counted against the same bucket; with the old default burst of 10 the server closed the connection with 1008 mid-bootstrap and the client reconnected and repeated the same burst, looping. Raising the burst does **not** change `WS_INBOUND_MESSAGES_PER_MINUTE`, so the sustained-rate protection against flooding is unchanged — only the initial capacity for a legitimate burst is larger (see issue #455).

On the client, `chatSocket.ts` treats close code 1008 as a permanent rejection: it stops reconnecting and moves to the `"failed"` status instead of retrying with the same burst. Reconnection resumes only on an explicit new session (login) or a fresh `acquireChatSocket` call, never automatically.

---

## Delivery Semantics

**Best-effort, in-process, not durable.**

- Events are delivered only to clients connected at the time of broadcast.
- Clients that reconnect will not receive events they missed.
- There is no replay, no persistence, and no at-least-once guarantee.
- If the `chat-service` instance restarts, all in-progress connections and subscriptions are lost.

**Future: Valkey outbox fan-out**
For durable delivery across multiple `chat-service` instances and for clients that reconnect, a Valkey (or Redis) pub-sub outbox pattern is the planned approach. That is explicitly out of scope for this PR.

---

## Internal Wiring: PublishMessageCreated

`Hub.PublishMessageCreated` is the hook for `MessageService` to call after a message is persisted:

```go
hub.PublishMessageCreated(ctx, workspaceID, ws.TargetTypeChannel, channelID, message.ID)
```

It is not yet called from `MessageService` in this PR. The next step is to wire it up once the hub is injectable into the service layer.

---

## No-log invariants

- Message body text (`BodyText`) is never included in `Event.Payload`.
- Authorization header values, tokens, and cookies are never logged.
- The `slog` logger only records user_id and target metadata at DEBUG/WARN level; no message content.

---

## Running tests

```bash
cd services/chat-service
go test -race -count=1 ./internal/ws/...
go test -count=1 -coverprofile=ws.out ./internal/ws/... && go tool cover -func=ws.out | tail -1
```

Expected: all pass, coverage ≥ 92%.
