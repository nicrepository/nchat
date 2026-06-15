package ws

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"
)

// ── fakeBus — test double for BroadcastBus ────────────────────────────────────

type fakeBus struct {
	mu         sync.Mutex
	published  []Event
	publishErr error
	handler    func(Event)
}

func (f *fakeBus) Publish(_ context.Context, evt Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.publishErr != nil {
		return f.publishErr
	}
	f.published = append(f.published, evt)
	return nil
}

func (f *fakeBus) Subscribe(_ context.Context, h func(Event)) {
	f.mu.Lock()
	f.handler = h
	f.mu.Unlock()
}

func (f *fakeBus) Close() {}

// inject simulates a remote event arriving from the bus.
func (f *fakeBus) inject(evt Event) {
	f.mu.Lock()
	h := f.handler
	f.mu.Unlock()
	if h != nil {
		h(evt)
	}
}

func (f *fakeBus) publishCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.published)
}

func (f *fakeBus) lastPublished() (Event, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.published) == 0 {
		return Event{}, false
	}
	return f.published[len(f.published)-1], true
}

// ── helpers ───────────────────────────────────────────────────────────────────

// newBusTestHub creates a Hub with the given bus and a known instanceID.
func newBusTestHub(auth SubscriptionAuthorizer, bus BroadcastBus) *Hub {
	h := &Hub{
		authorizer:  auth,
		bus:         bus,
		instanceID:  "instance-A",
		logger:      newTestLogger(),
		register:    make(chan registerReq, 64),
		unregister:  make(chan *Client, 64),
		subReq:      make(chan subscribeReq, 64),
		bcast:       make(chan broadcastReq, 256),
		remoteBcast: make(chan broadcastReq, 256),
		quit:        make(chan struct{}),
		done:        make(chan struct{}),
		clients:     make(map[string]*Client),
		subs:        make(map[string]map[string]struct{}),
		clientSubs:  make(map[string]map[string]struct{}),
	}
	ctx, cancel := context.WithCancel(context.Background())
	h.busCancel = cancel
	h.bus.Subscribe(ctx, h.handleRemoteBusEvent)
	go h.run()
	return h
}

// ── TDD: bus-integrated hub tests ────────────────────────────────────────────

// Local delivery must work even when bus is NopBus (disabled).
func TestHub_Bus_NopBus_LocalDeliveryStillWorks(t *testing.T) {
	auth := &fakeAuthorizer{}
	auth.setAccess("user-1", "ws-1", TargetTypeChannel, "ch-1", true)

	hub := newBusTestHub(auth, NopBus{})
	defer hub.Shutdown()

	c := newClient("c1", "user-1", "ws-1", &fakeSender{})
	hub.Register(c)
	if err := hub.Subscribe(context.Background(), c, TargetTypeChannel, "ch-1"); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	hub.PublishMessageCreated(context.Background(), "ws-1", TargetTypeChannel, "ch-1", "msg-1")

	// Wait for event to be processed.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if len(c.outbox) == 1 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("expected 1 event in outbox with NopBus, got %d", len(c.outbox))
}

// PublishMessageCreated must call bus.Publish exactly once per event.
func TestHub_Bus_PublishCallsBusOnce(t *testing.T) {
	auth := &fakeAuthorizer{}
	auth.setAccess("user-1", "ws-1", TargetTypeChannel, "ch-1", true)

	bus := &fakeBus{}
	hub := newBusTestHub(auth, bus)
	defer hub.Shutdown()

	c := newClient("c1", "user-1", "ws-1", &fakeSender{})
	hub.Register(c)
	if err := hub.Subscribe(context.Background(), c, TargetTypeChannel, "ch-1"); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	hub.PublishMessageCreated(context.Background(), "ws-1", TargetTypeChannel, "ch-1", "msg-1")

	// Allow time for async publish.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if bus.publishCount() >= 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	if n := bus.publishCount(); n != 1 {
		t.Fatalf("expected bus.Publish called once, got %d", n)
	}
}

// Bus publish failure must not prevent local delivery.
func TestHub_Bus_PublishFailure_LocalDeliveryStillWorks(t *testing.T) {
	auth := &fakeAuthorizer{}
	auth.setAccess("user-1", "ws-1", TargetTypeChannel, "ch-1", true)

	bus := &fakeBus{publishErr: errors.New("valkey unavailable")}
	hub := newBusTestHub(auth, bus)
	defer hub.Shutdown()

	c := newClient("c1", "user-1", "ws-1", &fakeSender{})
	hub.Register(c)
	if err := hub.Subscribe(context.Background(), c, TargetTypeChannel, "ch-1"); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	// Must not panic even if bus is down.
	hub.PublishMessageCreated(context.Background(), "ws-1", TargetTypeChannel, "ch-1", "msg-1")

	// Local delivery still happens.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if len(c.outbox) == 1 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("expected 1 event in outbox despite bus failure, got %d", len(c.outbox))
}

// Remote event received from bus delivers locally but must NOT re-publish to bus.
func TestHub_Bus_RemoteEvent_DeliversLocally_NoBusRepublish(t *testing.T) {
	auth := &fakeAuthorizer{}
	auth.setAccess("user-1", "ws-1", TargetTypeChannel, "ch-1", true)

	bus := &fakeBus{}
	hub := newBusTestHub(auth, bus)
	defer hub.Shutdown()

	c := newClient("c1", "user-1", "ws-1", &fakeSender{})
	hub.Register(c)
	if err := hub.Subscribe(context.Background(), c, TargetTypeChannel, "ch-1"); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	// Inject a remote event (from another instance).
	remoteEvt := Event{
		Type:             EventTypeMessageCreated,
		WorkspaceID:      "ws-1",
		TargetType:       TargetTypeChannel,
		TargetID:         "ch-1",
		MessageID:        "msg-remote-1",
		EventID:          "evt-id-1",
		SourceInstanceID: "instance-B", // different instance
		CreatedAt:        time.Now().UTC(),
	}
	bus.inject(remoteEvt)

	// Event should be delivered locally.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if len(c.outbox) == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if n := len(c.outbox); n != 1 {
		t.Fatalf("expected 1 event from remote delivery, got %d", n)
	}

	// Bus must NOT have been called (no re-publish).
	if n := bus.publishCount(); n != 0 {
		t.Fatalf("remote event must not trigger bus re-publish, got %d publishes", n)
	}
}

// Self-origin event (SourceInstanceID == h.instanceID) must be ignored.
func TestHub_Bus_SelfOriginEvent_Ignored(t *testing.T) {
	auth := &fakeAuthorizer{}
	auth.setAccess("user-1", "ws-1", TargetTypeChannel, "ch-1", true)

	bus := &fakeBus{}
	hub := newBusTestHub(auth, bus)
	defer hub.Shutdown()

	c := newClient("c1", "user-1", "ws-1", &fakeSender{})
	hub.Register(c)
	if err := hub.Subscribe(context.Background(), c, TargetTypeChannel, "ch-1"); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	// Inject a self-origin event (same instance ID as hub).
	selfEvt := Event{
		Type:             EventTypeMessageCreated,
		WorkspaceID:      "ws-1",
		TargetType:       TargetTypeChannel,
		TargetID:         "ch-1",
		MessageID:        "msg-echo",
		EventID:          "evt-id-2",
		SourceInstanceID: "instance-A", // same as hub.instanceID
		CreatedAt:        time.Now().UTC(),
	}
	bus.inject(selfEvt)

	// Give time for any (unwanted) delivery.
	time.Sleep(50 * time.Millisecond)

	if n := len(c.outbox); n != 0 {
		t.Fatalf("self-origin event must be ignored, got %d events in outbox", n)
	}
}

// Malformed/unknown remote events must be dropped fail-secure.
func TestHub_Bus_MalformedRemoteEvent_Ignored(t *testing.T) {
	auth := &fakeAuthorizer{}
	auth.setAccess("user-1", "ws-1", TargetTypeChannel, "ch-1", true)

	bus := &fakeBus{}
	hub := newBusTestHub(auth, bus)
	defer hub.Shutdown()

	c := newClient("c1", "user-1", "ws-1", &fakeSender{})
	hub.Register(c)
	if err := hub.Subscribe(context.Background(), c, TargetTypeChannel, "ch-1"); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	// Unknown event type.
	bus.inject(Event{
		Type:             "unknown.event",
		WorkspaceID:      "ws-1",
		TargetType:       TargetTypeChannel,
		TargetID:         "ch-1",
		EventID:          "evt-id-3",
		SourceInstanceID: "instance-B",
	})

	// Unknown target type.
	bus.inject(Event{
		Type:             EventTypeMessageCreated,
		WorkspaceID:      "ws-1",
		TargetType:       "unknown_target",
		TargetID:         "ch-1",
		EventID:          "evt-id-4",
		SourceInstanceID: "instance-B",
	})

	// Empty WorkspaceID.
	bus.inject(Event{
		Type:             EventTypeMessageCreated,
		WorkspaceID:      "",
		TargetType:       TargetTypeChannel,
		TargetID:         "ch-1",
		EventID:          "evt-id-5",
		SourceInstanceID: "instance-B",
	})

	// Empty SourceInstanceID (not properly stamped).
	bus.inject(Event{
		Type:        EventTypeMessageCreated,
		WorkspaceID: "ws-1",
		TargetType:  TargetTypeChannel,
		TargetID:    "ch-1",
		EventID:     "evt-id-6",
		// SourceInstanceID intentionally empty
	})

	time.Sleep(50 * time.Millisecond)

	if n := len(c.outbox); n != 0 {
		t.Fatalf("malformed events must be ignored, got %d events in outbox", n)
	}
}

// Remote event must still go through authorization re-check before delivery.
func TestHub_Bus_RemoteEvent_ReChecksAuth_Allowed(t *testing.T) {
	auth := &fakeAuthorizer{}
	auth.setAccess("user-1", "ws-1", TargetTypeChannel, "ch-1", true)

	bus := &fakeBus{}
	hub := newBusTestHub(auth, bus)
	defer hub.Shutdown()

	c := newClient("c1", "user-1", "ws-1", &fakeSender{})
	hub.Register(c)
	if err := hub.Subscribe(context.Background(), c, TargetTypeChannel, "ch-1"); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	// Auth still allows → delivery happens.
	bus.inject(Event{
		Type:             EventTypeMessageCreated,
		WorkspaceID:      "ws-1",
		TargetType:       TargetTypeChannel,
		TargetID:         "ch-1",
		MessageID:        "msg-r2",
		EventID:          "evt-id-7",
		SourceInstanceID: "instance-B",
		CreatedAt:        time.Now().UTC(),
	})

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if len(c.outbox) == 1 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("expected 1 event when auth allows, got %d", len(c.outbox))
}

// Remote event with auth denied must revoke subscription.
func TestHub_Bus_RemoteEvent_AuthDenied_RevokesSubscription(t *testing.T) {
	auth := &fakeAuthorizer{}
	auth.setAccess("user-1", "ws-1", TargetTypeChannel, "ch-1", true)

	bus := &fakeBus{}
	hub := newBusTestHub(auth, bus)
	defer hub.Shutdown()

	c := newClient("c1", "user-1", "ws-1", &fakeSender{})
	hub.Register(c)
	if err := hub.Subscribe(context.Background(), c, TargetTypeChannel, "ch-1"); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	// Revoke access.
	auth.setAccess("user-1", "ws-1", TargetTypeChannel, "ch-1", false)

	bus.inject(Event{
		Type:             EventTypeMessageCreated,
		WorkspaceID:      "ws-1",
		TargetType:       TargetTypeChannel,
		TargetID:         "ch-1",
		MessageID:        "msg-r3",
		EventID:          "evt-id-8",
		SourceInstanceID: "instance-B",
		CreatedAt:        time.Now().UTC(),
	})

	time.Sleep(100 * time.Millisecond)

	if n := len(c.outbox); n != 0 {
		t.Fatalf("revoked client must not receive remote event, got %d", n)
	}
}

// Remote event with transient auth error must skip delivery but keep subscription.
func TestHub_Bus_RemoteEvent_AuthError_SkipsDeliveryKeepsSubscription(t *testing.T) {
	auth := &fakeAuthorizer{}
	auth.setAccess("user-1", "ws-1", TargetTypeChannel, "ch-1", true)

	bus := &fakeBus{}
	hub := newBusTestHub(auth, bus)
	defer hub.Shutdown()

	c := newClient("c1", "user-1", "ws-1", &fakeSender{})
	hub.Register(c)
	if err := hub.Subscribe(context.Background(), c, TargetTypeChannel, "ch-1"); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	// Set transient auth error.
	auth.setErr("user-1", "ws-1", TargetTypeChannel, "ch-1", errors.New("db timeout"))

	bus.inject(Event{
		Type:             EventTypeMessageCreated,
		WorkspaceID:      "ws-1",
		TargetType:       TargetTypeChannel,
		TargetID:         "ch-1",
		MessageID:        "msg-r4",
		EventID:          "evt-id-9",
		SourceInstanceID: "instance-B",
		CreatedAt:        time.Now().UTC(),
	})

	time.Sleep(100 * time.Millisecond)

	// No delivery.
	if n := len(c.outbox); n != 0 {
		t.Fatalf("auth error must skip delivery, got %d", n)
	}

	// Subscription kept. We verify by clearing the error and checking delivery works.
	auth.setErr("user-1", "ws-1", TargetTypeChannel, "ch-1", nil)

	bus.inject(Event{
		Type:             EventTypeMessageCreated,
		WorkspaceID:      "ws-1",
		TargetType:       TargetTypeChannel,
		TargetID:         "ch-1",
		MessageID:        "msg-r5",
		EventID:          "evt-id-10",
		SourceInstanceID: "instance-B",
		CreatedAt:        time.Now().UTC(),
	})

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if len(c.outbox) == 1 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("subscription must be kept after auth error; expected delivery on recovery, got %d", len(c.outbox))
}

// Published event must carry SourceInstanceID and EventID.
func TestHub_Bus_PublishedEvent_HasInstanceIDAndEventID(t *testing.T) {
	auth := &fakeAuthorizer{}
	bus := &fakeBus{}
	hub := newBusTestHub(auth, bus)
	defer hub.Shutdown()

	hub.PublishMessageCreated(context.Background(), "ws-1", TargetTypeChannel, "ch-1", "msg-1")

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if bus.publishCount() >= 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	evt, ok := bus.lastPublished()
	if !ok {
		t.Fatal("expected bus to have received a published event")
	}
	if evt.SourceInstanceID != "instance-A" {
		t.Errorf("expected SourceInstanceID 'instance-A', got %q", evt.SourceInstanceID)
	}
	if evt.EventID == "" {
		t.Error("expected non-empty EventID")
	}
}

// NewHub with NopBus must not leak goroutines on Shutdown.
func TestHub_Bus_Shutdown_NoGoroutineLeak(t *testing.T) {
	hub := NewHub(&fakeAuthorizer{}, newTestLogger(), NopBus{}, "test-inst")
	hub.Shutdown() // must return promptly
}

// Hub with remote broadcast queue full drops the event without blocking.
func TestHub_Bus_RemoteBcastQueueFull_DropsWithoutBlock(t *testing.T) {
	auth := &fakeAuthorizer{}
	bus := &fakeBus{}

	// Build a hub with a small remoteBcast queue and don't start the run goroutine.
	h := &Hub{
		authorizer:  auth,
		bus:         bus,
		instanceID:  "instance-A",
		busCancel:   func() {},
		logger:      newTestLogger(),
		remoteBcast: make(chan broadcastReq, 1), // tiny queue
		clients:     make(map[string]*Client),
		subs:        make(map[string]map[string]struct{}),
		clientSubs:  make(map[string]map[string]struct{}),
	}

	// Fill the remote queue.
	h.remoteBcast <- broadcastReq{}

	// Injecting another valid remote event must not block.
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.handleRemoteBusEvent(Event{
			Type:             EventTypeMessageCreated,
			WorkspaceID:      "ws-1",
			TargetType:       TargetTypeChannel,
			TargetID:         "ch-1",
			EventID:          "evt-drop-1",
			SourceInstanceID: "instance-B",
			CreatedAt:        time.Now().UTC(),
		})
	}()

	select {
	case <-done:
		// correct: did not block
	case <-time.After(200 * time.Millisecond):
		t.Fatal("handleRemoteBusEvent blocked on full remoteBcast queue")
	}
}

// Injecting a valid remote event encodes correctly to JSON for client delivery.
func TestHub_Bus_RemoteEvent_ClientReceivesValidJSON(t *testing.T) {
	auth := &fakeAuthorizer{}
	auth.setAccess("user-1", "ws-1", TargetTypeChannel, "ch-1", true)

	bus := &fakeBus{}
	hub := newBusTestHub(auth, bus)
	defer hub.Shutdown()

	c := newClient("c1", "user-1", "ws-1", &fakeSender{})
	hub.Register(c)
	if err := hub.Subscribe(context.Background(), c, TargetTypeChannel, "ch-1"); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	remoteEvt := Event{
		Type:             EventTypeMessageCreated,
		WorkspaceID:      "ws-1",
		TargetType:       TargetTypeChannel,
		TargetID:         "ch-1",
		MessageID:        "msg-remote-json",
		EventID:          "evt-json-1",
		SourceInstanceID: "instance-B",
		CreatedAt:        time.Now().UTC(),
	}
	bus.inject(remoteEvt)

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if len(c.outbox) == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(c.outbox) == 0 {
		t.Fatal("no event delivered")
	}

	raw := <-c.outbox
	var decoded Event
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("delivered payload is not valid JSON: %v", err)
	}
	if decoded.MessageID != "msg-remote-json" {
		t.Errorf("decoded MessageID = %q, want %q", decoded.MessageID, "msg-remote-json")
	}
}
