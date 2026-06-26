package ws

import (
	"context"
	"log/slog"
	"testing"
	"time"
)

// TestHubClientAbruptDisconnect verifies that an abrupt client disconnect
// (TCP close without a WebSocket close frame) causes both connection pump
// goroutines to exit cleanly and the hub to remove all client state — no
// goroutine leaks, no stale subscription entries.
//
// Simulated by: closing the underlying sender while pumps are running, then
// enqueuing a message that triggers a Send error in writePump.
func TestHubClientAbruptDisconnect(t *testing.T) {
	auth := &fakeAuthorizer{}
	auth.setAccess("user-1", "ws-1", TargetTypeChannel, "ch-1", true)

	hub := NewHub(auth, slog.Default(), NopBus{}, "test-inst-abrupt")
	defer hub.Shutdown()

	snd := &controllableSender{}
	c := newClient("c-abrupt", "user-1", "ws-1", snd)
	registerInRunningHub(t, hub, c)

	if err := hub.Subscribe(context.Background(), c, TargetTypeChannel, "ch-1"); err != nil {
		t.Fatalf("subscribe before abrupt disconnect: %v", err)
	}

	// Long heartbeat so it never fires during the test; only the send-error
	// path is exercised here.
	done, stop := startConnectionPumps(
		context.Background(), c, hub, slog.Default(),
		time.Hour, 5*time.Second,
	)
	defer stop()

	// Simulate TCP drop: underlying connection disappears.
	// The sender now returns an error on every Send or Ping.
	snd.Close()

	// Deliver a message so writePump attempts Send, detects the error, and exits.
	c.enqueue([]byte(`{"type":"test"}`))

	// Both goroutines must exit — no goroutine leak.
	awaitGoroutineExit(t, done, "connection pumps after abrupt disconnect")

	// Hub must have removed client and subscription state after writePump's
	// deferred hub.Unregister fires.
	eventually(t, func() bool { return !hubHasClient(hub, "c-abrupt") }, 2*time.Second,
		"hub must remove client after abrupt disconnect")

	key := targetKey{workspaceID: "ws-1", targetType: TargetTypeChannel, targetID: "ch-1"}.String()
	if hubHasSubscriptionTarget(hub, key) {
		t.Fatal("hub must remove subscription after abrupt disconnect")
	}
}

// TestHubReconnectAfterDisconnect verifies that when a client disconnects and
// a new connection arrives for the same user, the hub:
//   - removes all state from the first connection before the reconnect,
//   - accepts the new registration without error,
//   - allows the new client to subscribe to the same targets it used before.
func TestHubReconnectAfterDisconnect(t *testing.T) {
	auth := &fakeAuthorizer{}
	auth.setAccess("user-1", "ws-1", TargetTypeChannel, "ch-1", true)

	hub := NewHub(auth, slog.Default(), NopBus{}, "test-inst-reconnect")
	defer hub.Shutdown()

	key := targetKey{workspaceID: "ws-1", targetType: TargetTypeChannel, targetID: "ch-1"}.String()

	// ── first connection ──────────────────────────────────────────────────────

	snd1 := &controllableSender{}
	c1 := newClient("conn-first", "user-1", "ws-1", snd1)
	registerInRunningHub(t, hub, c1)

	if err := hub.Subscribe(context.Background(), c1, TargetTypeChannel, "ch-1"); err != nil {
		t.Fatalf("c1 subscribe: %v", err)
	}

	// Disconnect c1 by cancelling its pump context; writePump defers hub.Unregister.
	ctx1, cancel1 := context.WithCancel(context.Background())
	done1, stop1 := startConnectionPumps(ctx1, c1, hub, slog.Default(), time.Hour, 5*time.Second)
	defer stop1()

	cancel1()
	awaitGoroutineExit(t, done1, "c1 connection pumps")

	// Hub processes unregister asynchronously via the run goroutine.
	eventually(t, func() bool { return !hubHasClient(hub, "conn-first") }, 2*time.Second,
		"hub must remove first connection before reconnect")
	if hubHasSubscriptionTarget(hub, key) {
		t.Fatal("hub must remove c1 subscription state before reconnect")
	}

	// ── reconnect: new connection, same user ──────────────────────────────────

	snd2 := &controllableSender{}
	c2 := newClient("conn-second", "user-1", "ws-1", snd2)
	registerInRunningHub(t, hub, c2)

	if err := hub.Subscribe(context.Background(), c2, TargetTypeChannel, "ch-1"); err != nil {
		t.Fatalf("c2 subscribe after reconnect: %v", err)
	}

	if !hubHasClient(hub, "conn-second") {
		t.Fatal("reconnected client must be tracked by hub")
	}
	if !hubHasSubscription(hub, key, "conn-second") {
		t.Fatal("reconnected client must have subscription")
	}
	// Previous connection's state must not have leaked.
	if hubHasClient(hub, "conn-first") {
		t.Fatal("first connection must not remain in hub after reconnect")
	}
}

// TestHubHeartbeatEviction verifies that a client which stops responding to
// ping frames is evicted from the hub within the configured heartbeat timeout.
//
// The sender's Ping blocks until the ping context's deadline expires (no pong
// reply from the client). This causes startHeartbeat to treat the timeout as a
// dead peer and call hub.Unregister, which removes all hub state for that client.
func TestHubHeartbeatEviction(t *testing.T) {
	auth := &fakeAuthorizer{}
	auth.setAccess("user-1", "ws-1", TargetTypeChannel, "ch-1", true)

	hub := NewHub(auth, slog.Default(), NopBus{}, "test-inst-evict")
	defer hub.Shutdown()

	// blockingSender.Ping blocks until its block channel is closed or ctx.Done().
	// By never closing the block channel we simulate a client that never returns
	// a pong, so Ping returns only when the ping context times out.
	snd := &blockingSender{
		block:   make(chan struct{}), // intentionally never closed
		started: make(chan struct{}),
	}
	c := newClient("c-hb-evict", "user-1", "ws-1", snd)
	registerInRunningHub(t, hub, c)

	if err := hub.Subscribe(context.Background(), c, TargetTypeChannel, "ch-1"); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	const (
		heartbeatInterval = 5 * time.Millisecond
		pingTimeout       = 20 * time.Millisecond
	)

	done, stop := startConnectionPumps(
		context.Background(), c, hub, slog.Default(),
		heartbeatInterval, pingTimeout,
	)
	defer stop()

	// Pumps must exit after the ping times out and heartbeat evicts the client.
	awaitGoroutineExit(t, done, "connection pumps after heartbeat eviction")

	// Client must be fully removed from hub — eviction is confirmed by state cleanup.
	eventually(t, func() bool { return !hubHasClient(hub, "c-hb-evict") }, 2*time.Second,
		"client must be evicted from hub after heartbeat timeout")

	key := targetKey{workspaceID: "ws-1", targetType: TargetTypeChannel, targetID: "ch-1"}.String()
	if hubHasSubscriptionTarget(hub, key) {
		t.Fatal("hub must remove subscription after heartbeat eviction")
	}
}
