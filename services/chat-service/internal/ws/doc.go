// Package ws implements an in-process WebSocket hub for chat-service.
//
// # Current state
//
//   - ServeWS performs a real WebSocket upgrade using github.com/coder/websocket.
//   - Authentication is enforced server-side via BearerAuth middleware context;
//     clients never provide their own identity or workspaceID.
//   - Credentials in URL query strings are rejected case-insensitively before upgrade.
//   - Per-user connection limits are enforced before upgrade (429 on excess).
//   - writePump, startHeartbeat, and startConnectionPumps are wired to ServeWS.
//   - PresenceTracker is attached via WithPresence; presence updates on connect/disconnect.
//   - In-process hub only; no distributed fan-out (Valkey/pub-sub is future scope).
//   - Subscription authorization for channels and DM conversations.
//   - Authorization is re-checked before every broadcast delivery.
//   - Bounded per-client outbound queue; slow clients are dropped deterministically.
//   - Inbound message rate limiting (token bucket) and invalid-message flood protection
//     are applied per connection in the read loop using HandlerConfig limits.
//
// # Out of scope (future work)
//
//   - Browser reconnect / exponential backoff.
//   - Scroll-infinite pagination and cursor-based history.
//   - Presence broadcast to other clients.
//   - End-to-end encryption / MLS key service.
//   - Valkey or any distributed pub-sub.
//   - Sending chat messages over WebSocket (prefer HTTP REST for writes).
//
// # Auth invariants (permanent)
//
//   - The WS handler never upgrades unauthenticated connections.
//   - Access tokens must never appear in query strings; Authorization header only.
//   - Origin validation is handled by coder/websocket defaults (Origin == Host).
//   - Refresh tokens must not be used for WebSocket auth.
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
