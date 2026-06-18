package ws

// hub_presence_test.go — integration tests for Hub + PresenceTracker.
//
// These tests exercise the full connect→online, timeout→away, activity→online
// cycle through the real handleClientMessage path, using a fake clock and
// synchronous checkAway calls for determinism.

import (
	"context"
	"testing"
	"time"
)

// TestHub_HandleClientMessage_Ping_RecordsActivity verifies that a ping message
// processed by handleClientMessage calls RecordActivity, which restores the
// user from away to online.
func TestHub_HandleClientMessage_Ping_RecordsActivity(t *testing.T) {
	const awayTimeout = 5 * time.Minute

	clk := newFakeClock(time.Now())
	// newTestPresenceTracker has no background goroutine; do NOT call Stop().
	p := newTestPresenceTracker(awayTimeout, clk)

	h := newTestHub(&fakeAuthorizer{})
	h.presence = p

	snd := &fakeSender{}
	c := newClient("c1", "user-1", "ws-1", snd)
	registerInHub(t, h, c)

	// newTestHub does not run the hub goroutine, so Connect must be called directly.
	p.Connect(c.workspaceID, c.userID, c.id)

	if got := p.Status("ws-1", "user-1"); got != PresenceOnline {
		t.Fatalf("connect: expected online, got %q", got)
	}

	// Advance clock and trigger away transition.
	clk.Advance(awayTimeout + time.Second)
	p.checkAway()
	if got := p.Status("ws-1", "user-1"); got != PresenceAway {
		t.Fatalf("timeout: expected away, got %q", got)
	}

	// Activity via the real client message path.
	if err := h.handleClientMessage(context.Background(), c, ClientMessage{Type: ClientMessageTypePing}); err != nil {
		t.Fatalf("handleClientMessage(ping): %v", err)
	}

	if got := p.Status("ws-1", "user-1"); got != PresenceOnline {
		t.Fatalf("activity: expected online, got %q", got)
	}
}

// TestHub_WithPresence_ActivityViaClientMessage_RestoresOnline tests the full
// Hub lifecycle: Register calls presence.Connect via the run goroutine, inactivity
// triggers away, and a ping via handleClientMessage restores online.
//
// This test uses NewHub (with the real run goroutine) so that Register → Connect
// flows through the same code path used in production.
func TestHub_WithPresence_ActivityViaClientMessage_RestoresOnline(t *testing.T) {
	const awayTimeout = 5 * time.Minute

	clk := newFakeClock(time.Now())
	// newTestPresenceTracker has no background goroutine; do NOT call Stop().
	p := newTestPresenceTracker(awayTimeout, clk)

	auth := &fakeAuthorizer{}
	hub := NewHub(auth, newTestLogger(), NopBus{}, "test-inst", WithPresence(p))
	defer hub.Shutdown()

	snd := &fakeSender{}
	c := newClient("c1", "user-1", "ws-1", snd)

	// Register is synchronous; the run goroutine calls p.Connect before returning.
	hub.Register(c)
	if got := p.Status("ws-1", "user-1"); got != PresenceOnline {
		t.Fatalf("connect: expected online, got %q", got)
	}

	// Simulate inactivity timeout using the fake clock.
	clk.Advance(awayTimeout + time.Second)
	p.checkAway()
	if got := p.Status("ws-1", "user-1"); got != PresenceAway {
		t.Fatalf("timeout: expected away, got %q", got)
	}

	// Activity via the real client message path — the entry point the read pump calls.
	if err := hub.handleClientMessage(context.Background(), c, ClientMessage{Type: ClientMessageTypePing}); err != nil {
		t.Fatalf("handleClientMessage(ping): %v", err)
	}

	if got := p.Status("ws-1", "user-1"); got != PresenceOnline {
		t.Fatalf("activity: expected online, got %q", got)
	}
}

// TestHub_HandleClientMessage_Subscribe_RecordsActivity verifies that subscribe
// messages also record activity in addition to registering the subscription.
// Uses NewHub (real run goroutine) since Subscribe routes through the hub channel.
func TestHub_HandleClientMessage_Subscribe_RecordsActivity(t *testing.T) {
	const awayTimeout = 5 * time.Minute

	clk := newFakeClock(time.Now())
	// newTestPresenceTracker has no background goroutine; do NOT call Stop().
	p := newTestPresenceTracker(awayTimeout, clk)

	auth := &fakeAuthorizer{}
	auth.setAccess("user-1", "ws-1", TargetTypeChannel, "ch-1", true)

	hub := NewHub(auth, newTestLogger(), NopBus{}, "test-inst", WithPresence(p))
	defer hub.Shutdown()

	c := newClient("c1", "user-1", "ws-1", &fakeSender{})
	hub.Register(c) // synchronous; run goroutine calls p.Connect → online

	// Go away.
	clk.Advance(awayTimeout + time.Second)
	p.checkAway()
	if got := p.Status("ws-1", "user-1"); got != PresenceAway {
		t.Fatalf("expected away, got %q", got)
	}

	// Subscribe message should also record activity and restore online.
	if err := hub.handleClientMessage(context.Background(), c, ClientMessage{
		Type:       ClientMessageTypeSubscribe,
		TargetType: TargetTypeChannel,
		TargetID:   "ch-1",
	}); err != nil {
		t.Fatalf("handleClientMessage(subscribe): %v", err)
	}

	if got := p.Status("ws-1", "user-1"); got != PresenceOnline {
		t.Fatalf("subscribe activity: expected online, got %q", got)
	}
}

// TestHub_HandleClientMessage_NoPresence_Noop verifies that handleClientMessage
// is safe when no PresenceTracker is attached (nil-safe path).
func TestHub_HandleClientMessage_NoPresence_Noop(t *testing.T) {
	h := newTestHub(&fakeAuthorizer{})
	c := newClient("c1", "user-1", "ws-1", &fakeSender{})
	registerInHub(t, h, c)

	// Must not panic when presence is nil.
	if err := h.handleClientMessage(context.Background(), c, ClientMessage{Type: ClientMessageTypePing}); err != nil {
		t.Fatalf("handleClientMessage without presence: %v", err)
	}
}

// TestHub_HandleClientMessage_UnknownType_ReturnsError verifies that unknown
// client message types return an error and do not panic.
func TestHub_HandleClientMessage_UnknownType_ReturnsError(t *testing.T) {
	h := newTestHub(&fakeAuthorizer{})
	c := newClient("c1", "user-1", "ws-1", &fakeSender{})
	registerInHub(t, h, c)

	err := h.handleClientMessage(context.Background(), c, ClientMessage{Type: "unknown_type"})
	if err == nil {
		t.Fatal("expected error for unknown message type, got nil")
	}
}
