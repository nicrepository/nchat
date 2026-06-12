// Package ws implements an in-process WebSocket hub for chat-service.
//
// # Scope (this PR)
//
//   - In-process hub only; no distributed fan-out (Valkey/pub-sub is future scope).
//   - Subscription authorization for channels and DM conversations.
//   - Authorization is re-checked before every broadcast delivery to protect against stale membership.
//   - Bounded per-client outbound queue; slow clients are dropped deterministically.
//   - ServeWS handler is a stub (returns 501); WS upgrade is deferred until an
//     authenticated request context pattern is available in chat-service.
//
// # Out of scope
//
//   - Notifications, presence, typing indicators.
//   - End-to-end encryption / MLS key service.
//   - Valkey or any distributed pub-sub.
//   - Frontend integration.
//   - Sending chat messages over WebSocket (deferred; prefer HTTP REST for writes).
//
// # Auth and origin assumptions
//
//   - The WS handler MUST NOT upgrade unauthenticated connections.
//   - Access tokens MUST NOT be passed in query strings; cookie or header only.
//   - Origin validation MUST be enforced before websocket.Accept is called.
//   - Refresh tokens MUST NOT be used for WebSocket auth.
//   - All of the above are enforced once the handler is fully implemented.
//
// # Backpressure / slow client policy
//
//   - Each client has a bounded outbox channel (see [outboxSize]).
//   - If the outbox is full during a broadcast, the client is dropped immediately.
//   - The hub goroutine never blocks on a slow client.
//
// # Delivery semantics
//
//   - Best-effort in-process delivery only.
//   - No message durability, no guaranteed delivery, no at-least-once semantics.
//   - Clients that reconnect will not receive missed events (no replay).
//   - Valkey outbox / fan-out for durability is future scope.
package ws
