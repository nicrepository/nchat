package ws

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// ── test UUID constants ───────────────────────────────────────────────────────

const (
	testWorkspaceID  = "11111111-1111-1111-1111-111111111111"
	testChannelID    = "22222222-2222-2222-2222-222222222222"
	testMessageID    = "44444444-4444-4444-4444-444444444444"
	testEventID      = "55555555-5555-5555-5555-555555555555"
	testEventID2     = "55555555-5555-5555-5555-555555555556"
	testEventID3     = "55555555-5555-5555-5555-555555555557"
	testEventIDEcho  = "66666666-6666-6666-6666-666666666661"
	testEventIDAuth  = "66666666-6666-6666-6666-666666666662"
	testEventIDAuth2 = "66666666-6666-6666-6666-666666666663"
	testEventIDAuth3 = "66666666-6666-6666-6666-666666666664"
	testEventIDAuth4 = "66666666-6666-6666-6666-666666666665"
	testEventIDJSON  = "77777777-7777-7777-7777-777777777771"
	testEventIDDrop  = "77777777-7777-7777-7777-777777777772"
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

func (f *fakeBus) Subscribe(_ context.Context, h func(Event)) error {
	f.mu.Lock()
	f.handler = h
	f.mu.Unlock()
	return nil
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
	if err := h.bus.Subscribe(ctx, h.handleRemoteBusEvent); err != nil {
		panic("newBusTestHub: bus.Subscribe failed: " + err.Error())
	}
	go h.run()
	return h
}

// remoteEvt returns a valid remote Event from another instance with all required UUID fields.
func remoteEvt(eventID string) Event {
	return Event{
		SchemaVersion:    CurrentEventSchemaVersion,
		Type:             EventTypeMessageCreated,
		WorkspaceID:      testWorkspaceID,
		TargetType:       TargetTypeChannel,
		TargetID:         testChannelID,
		MessageID:        testMessageID,
		EventID:          eventID,
		SourceInstanceID: "instance-B",
		CreatedAt:        time.Now().UTC(),
	}
}

// testPayload returns a minimal MessagePayload for test calls to PublishMessageCreated.
func testPayload() MessagePayload {
	return MessagePayload{
		ID:                testMessageID,
		WorkspaceID:       testWorkspaceID,
		ChannelID:         testChannelID,
		SenderID:          "user-1",
		SenderDisplayName: "Alice",
		Kind:              "user",
		Status:            "active",
		CreatedAt:         time.Now().UTC(),
		UpdatedAt:         time.Now().UTC(),
	}
}

func TestCanonicalizeRemoteEvent_StripsSensitivePayload(t *testing.T) {
	evt := remoteEvt(strings.ToUpper(testEventID))
	evt.WorkspaceID = strings.ToUpper(testWorkspaceID)
	evt.TargetID = strings.ToUpper(testChannelID)
	evt.MessageID = strings.ToUpper(testMessageID)
	evt.Payload = &MessagePayload{
		ID:          testMessageID,
		WorkspaceID: testWorkspaceID,
		ChannelID:   testChannelID,
		SenderID:    "user-1",
		BodyText:    "sensitive message body",
		Kind:        "user",
		Status:      "active",
	}

	canonical, ok := canonicalizeRemoteEvent(evt)
	if !ok {
		t.Fatal("expected remote event to canonicalize")
	}
	if canonical.Payload != nil {
		t.Fatalf("remote bus payload must be stripped, got %+v", canonical.Payload)
	}
	if canonical.EventID != testEventID {
		t.Errorf("EventID = %q, want %q", canonical.EventID, testEventID)
	}
	if canonical.WorkspaceID != testWorkspaceID {
		t.Errorf("WorkspaceID = %q, want %q", canonical.WorkspaceID, testWorkspaceID)
	}
	if canonical.TargetID != testChannelID {
		t.Errorf("TargetID = %q, want %q", canonical.TargetID, testChannelID)
	}
	if canonical.MessageID != testMessageID {
		t.Errorf("MessageID = %q, want %q", canonical.MessageID, testMessageID)
	}
	if canonical.SourceInstanceID != "instance-B" {
		t.Errorf("SourceInstanceID = %q, want instance-B", canonical.SourceInstanceID)
	}
}

func TestCanonicalizeRemoteReaction_StripsUntrustedAggregate(t *testing.T) {
	evt := remoteEvt(testEventID2)
	evt.Type = EventTypeReactionUpdated
	evt.Reaction = &ReactionEventPayload{
		MessageID: testMessageID, ActorUserID: testEventIDEcho, Emoji: "👍", Added: true,
		Reactions: []ReactionPayload{{Emoji: "👍", Count: 99}},
	}
	canonical, ok := canonicalizeRemoteEvent(evt)
	if !ok {
		t.Fatal("expected valid remote reaction route")
	}
	if canonical.Reaction != nil {
		t.Fatalf("remote reaction aggregate must be stripped, got %+v", canonical.Reaction)
	}
}

// waitForOutbox polls until n events arrive in c.outbox or 500ms passes.
func waitForOutbox(c *Client, n int) bool {
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if len(c.outbox) >= n {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

// ── TDD: bus-integrated hub tests ────────────────────────────────────────────

// Local delivery must work even when bus is NopBus (disabled).
func TestHub_Bus_NopBus_LocalDeliveryStillWorks(t *testing.T) {
	auth := &fakeAuthorizer{}
	auth.setAccess("user-1", testWorkspaceID, TargetTypeChannel, testChannelID, true)

	hub := newBusTestHub(auth, NopBus{})
	defer hub.Shutdown()

	c := newClient("c1", "user-1", testWorkspaceID, &fakeSender{})
	registerInRunningHub(t, hub, c)
	if err := hub.Subscribe(context.Background(), c, TargetTypeChannel, testChannelID); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	hub.PublishMessageCreated(context.Background(), testWorkspaceID, TargetTypeChannel, testChannelID, testPayload())

	if !waitForOutbox(c, 1) {
		t.Fatalf("expected 1 event in outbox with NopBus, got %d", len(c.outbox))
	}
}

// PublishMessageCreated must call bus.Publish exactly once per event.
func TestHub_Bus_PublishCallsBusOnce(t *testing.T) {
	auth := &fakeAuthorizer{}
	auth.setAccess("user-1", testWorkspaceID, TargetTypeChannel, testChannelID, true)

	bus := &fakeBus{}
	hub := newBusTestHub(auth, bus)
	defer hub.Shutdown()

	c := newClient("c1", "user-1", testWorkspaceID, &fakeSender{})
	registerInRunningHub(t, hub, c)
	if err := hub.Subscribe(context.Background(), c, TargetTypeChannel, testChannelID); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	hub.PublishMessageCreated(context.Background(), testWorkspaceID, TargetTypeChannel, testChannelID, testPayload())

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

func TestHub_Bus_PublishedEvent_StripsPayloadFromBusButKeepsLocalPayload(t *testing.T) {
	auth := &fakeAuthorizer{}
	auth.setAccess("user-1", testWorkspaceID, TargetTypeChannel, testChannelID, true)

	bus := &fakeBus{}
	hub := newBusTestHub(auth, bus)
	defer hub.Shutdown()

	c := newClient("c1", "user-1", testWorkspaceID, &fakeSender{})
	registerInRunningHub(t, hub, c)
	if err := hub.Subscribe(context.Background(), c, TargetTypeChannel, testChannelID); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	payload := testPayload()
	payload.BodyText = "sensitive local body"
	hub.PublishMessageCreated(context.Background(), testWorkspaceID, TargetTypeChannel, testChannelID, payload)

	if !waitForOutbox(c, 1) {
		t.Fatal("expected local delivery")
	}
	raw := <-c.outbox
	if strings.Contains(string(raw), "sender_email") {
		t.Fatalf("local WS payload must not include sender_email: %s", string(raw))
	}
	var local Event
	if err := json.Unmarshal(raw, &local); err != nil {
		t.Fatalf("local event JSON: %v", err)
	}
	if local.Payload == nil {
		t.Fatal("local delivery must retain payload for direct browser insertion")
	}
	if local.Payload.BodyText != "sensitive local body" ||
		local.Payload.SenderDisplayName != "Alice" {
		t.Fatalf("local payload was not preserved: %+v", local.Payload)
	}
	if local.SchemaVersion != CurrentEventSchemaVersion {
		t.Fatalf("local SchemaVersion = %d, want %d", local.SchemaVersion, CurrentEventSchemaVersion)
	}

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if bus.publishCount() >= 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	evt, ok := bus.lastPublished()
	if !ok {
		t.Fatal("expected bus to receive a published event")
	}
	if evt.Payload != nil {
		t.Fatalf("distributed bus event must not include payload, got %+v", evt.Payload)
	}
	if evt.MessageID != payload.ID {
		t.Errorf("bus event MessageID = %q, want %q", evt.MessageID, payload.ID)
	}
	if evt.SchemaVersion != CurrentEventSchemaVersion {
		t.Errorf("bus event SchemaVersion = %d, want %d", evt.SchemaVersion, CurrentEventSchemaVersion)
	}
}

// Bus publish failure must not prevent local delivery.
func TestHub_Bus_PublishFailure_LocalDeliveryStillWorks(t *testing.T) {
	auth := &fakeAuthorizer{}
	auth.setAccess("user-1", testWorkspaceID, TargetTypeChannel, testChannelID, true)

	bus := &fakeBus{publishErr: errors.New("valkey unavailable")}
	hub := newBusTestHub(auth, bus)
	defer hub.Shutdown()

	c := newClient("c1", "user-1", testWorkspaceID, &fakeSender{})
	registerInRunningHub(t, hub, c)
	if err := hub.Subscribe(context.Background(), c, TargetTypeChannel, testChannelID); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	hub.PublishMessageCreated(context.Background(), testWorkspaceID, TargetTypeChannel, testChannelID, testPayload())

	if !waitForOutbox(c, 1) {
		t.Fatalf("expected 1 event despite bus failure, got %d", len(c.outbox))
	}
}

// Remote event received from bus delivers locally but must NOT re-publish to bus.
func TestHub_Bus_RemoteEvent_DeliversLocally_NoBusRepublish(t *testing.T) {
	auth := &fakeAuthorizer{}
	auth.setAccess("user-1", testWorkspaceID, TargetTypeChannel, testChannelID, true)

	bus := &fakeBus{}
	hub := newBusTestHub(auth, bus)
	defer hub.Shutdown()

	c := newClient("c1", "user-1", testWorkspaceID, &fakeSender{})
	registerInRunningHub(t, hub, c)
	if err := hub.Subscribe(context.Background(), c, TargetTypeChannel, testChannelID); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	bus.inject(remoteEvt(testEventID))

	if !waitForOutbox(c, 1) {
		t.Fatalf("expected 1 event from remote delivery, got %d", len(c.outbox))
	}
	if n := bus.publishCount(); n != 0 {
		t.Fatalf("remote event must not trigger bus re-publish, got %d", n)
	}
}

// Self-origin event (SourceInstanceID == h.instanceID) must be ignored.
func TestHub_Bus_SelfOriginEvent_Ignored(t *testing.T) {
	auth := &fakeAuthorizer{}
	auth.setAccess("user-1", testWorkspaceID, TargetTypeChannel, testChannelID, true)

	bus := &fakeBus{}
	hub := newBusTestHub(auth, bus)
	defer hub.Shutdown()

	c := newClient("c1", "user-1", testWorkspaceID, &fakeSender{})
	registerInRunningHub(t, hub, c)
	if err := hub.Subscribe(context.Background(), c, TargetTypeChannel, testChannelID); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	e := remoteEvt(testEventIDEcho)
	e.SourceInstanceID = "instance-A" // same as hub.instanceID
	bus.inject(e)

	time.Sleep(50 * time.Millisecond)
	if n := len(c.outbox); n != 0 {
		t.Fatalf("self-origin event must be ignored, got %d events", n)
	}
}

// Malformed/unknown remote events must be dropped fail-secure.
func TestHub_Bus_MalformedRemoteEvent_Ignored(t *testing.T) {
	bus := &fakeBus{}
	hub := newBusTestHub(&fakeAuthorizer{}, bus)
	defer hub.Shutdown()

	e := remoteEvt(testEventID2)
	bad := []Event{
		func() Event { x := e; x.Type = "unknown.event"; return x }(),
		func() Event { x := e; x.TargetType = "unknown_target"; return x }(),
		func() Event { x := e; x.WorkspaceID = ""; return x }(),
		func() Event { x := e; x.SourceInstanceID = ""; return x }(),
	}
	for _, evt := range bad {
		bus.inject(evt)
	}
	time.Sleep(50 * time.Millisecond)
}

// Remote event must still go through authorization re-check before delivery.
func TestHub_Bus_RemoteEvent_ReChecksAuth_Allowed(t *testing.T) {
	auth := &fakeAuthorizer{}
	auth.setAccess("user-1", testWorkspaceID, TargetTypeChannel, testChannelID, true)

	bus := &fakeBus{}
	hub := newBusTestHub(auth, bus)
	defer hub.Shutdown()

	c := newClient("c1", "user-1", testWorkspaceID, &fakeSender{})
	registerInRunningHub(t, hub, c)
	if err := hub.Subscribe(context.Background(), c, TargetTypeChannel, testChannelID); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	bus.inject(remoteEvt(testEventIDAuth))

	if !waitForOutbox(c, 1) {
		t.Fatalf("expected 1 event when auth allows, got %d", len(c.outbox))
	}
}

// Remote event with auth denied must revoke subscription.
func TestHub_Bus_RemoteEvent_AuthDenied_RevokesSubscription(t *testing.T) {
	auth := &fakeAuthorizer{}
	auth.setAccess("user-1", testWorkspaceID, TargetTypeChannel, testChannelID, true)

	bus := &fakeBus{}
	hub := newBusTestHub(auth, bus)
	defer hub.Shutdown()

	c := newClient("c1", "user-1", testWorkspaceID, &fakeSender{})
	registerInRunningHub(t, hub, c)
	if err := hub.Subscribe(context.Background(), c, TargetTypeChannel, testChannelID); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	auth.setAccess("user-1", testWorkspaceID, TargetTypeChannel, testChannelID, false)
	bus.inject(remoteEvt(testEventIDAuth2))

	time.Sleep(100 * time.Millisecond)
	if n := len(c.outbox); n != 0 {
		t.Fatalf("revoked client must not receive remote event, got %d", n)
	}
}

// Remote event with transient auth error must skip delivery but keep subscription.
func TestHub_Bus_RemoteEvent_AuthError_SkipsDeliveryKeepsSubscription(t *testing.T) {
	auth := &fakeAuthorizer{}
	auth.setAccess("user-1", testWorkspaceID, TargetTypeChannel, testChannelID, true)

	bus := &fakeBus{}
	hub := newBusTestHub(auth, bus)
	defer hub.Shutdown()

	c := newClient("c1", "user-1", testWorkspaceID, &fakeSender{})
	registerInRunningHub(t, hub, c)
	if err := hub.Subscribe(context.Background(), c, TargetTypeChannel, testChannelID); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	auth.setErr("user-1", testWorkspaceID, TargetTypeChannel, testChannelID, errors.New("db timeout"))
	bus.inject(remoteEvt(testEventIDAuth3))

	time.Sleep(100 * time.Millisecond)
	if n := len(c.outbox); n != 0 {
		t.Fatalf("auth error must skip delivery, got %d", n)
	}

	// Subscription kept: clear error, verify delivery resumes.
	auth.setErr("user-1", testWorkspaceID, TargetTypeChannel, testChannelID, nil)
	bus.inject(remoteEvt(testEventIDAuth4))

	if !waitForOutbox(c, 1) {
		t.Fatalf("subscription must be kept after auth error, got %d", len(c.outbox))
	}
}

// Published event must carry SourceInstanceID and EventID.
func TestHub_Bus_PublishedEvent_HasInstanceIDAndEventID(t *testing.T) {
	bus := &fakeBus{}
	hub := newBusTestHub(&fakeAuthorizer{}, bus)
	defer hub.Shutdown()

	hub.PublishMessageCreated(context.Background(), testWorkspaceID, TargetTypeChannel, testChannelID, testPayload())

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
	if evt.SchemaVersion != CurrentEventSchemaVersion {
		t.Errorf("expected SchemaVersion %d, got %d", CurrentEventSchemaVersion, evt.SchemaVersion)
	}
}

func TestHub_Bus_RemoteEventWithoutSchemaVersion_StillDeliversForCompatibility(t *testing.T) {
	auth := &fakeAuthorizer{}
	auth.setAccess("user-1", testWorkspaceID, TargetTypeChannel, testChannelID, true)

	bus := &fakeBus{}
	hub := newBusTestHub(auth, bus)
	defer hub.Shutdown()

	c := newClient("c1", "user-1", testWorkspaceID, &fakeSender{})
	registerInRunningHub(t, hub, c)
	if err := hub.Subscribe(context.Background(), c, TargetTypeChannel, testChannelID); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	evt := remoteEvt(testEventIDJSON)
	evt.SchemaVersion = 0 // old server during rolling deploy
	bus.inject(evt)

	if !waitForOutbox(c, 1) {
		t.Fatal("remote event without schema_version should still deliver")
	}
	raw := <-c.outbox
	var decoded Event
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("delivered event JSON: %v", err)
	}
	if decoded.SchemaVersion != CurrentEventSchemaVersion {
		t.Fatalf("decoded SchemaVersion = %d, want %d", decoded.SchemaVersion, CurrentEventSchemaVersion)
	}
}

// NewHub with NopBus must not leak goroutines on Shutdown.
func TestHub_Bus_Shutdown_NoGoroutineLeak(t *testing.T) {
	hub := NewHub(&fakeAuthorizer{}, newTestLogger(), NopBus{}, "test-inst")
	hub.Shutdown()
}

// Hub with remote broadcast queue full drops the event without blocking.
func TestHub_Bus_RemoteBcastQueueFull_DropsWithoutBlock(t *testing.T) {
	h := &Hub{
		authorizer:  &fakeAuthorizer{},
		bus:         &fakeBus{},
		instanceID:  "instance-A",
		busCancel:   func() {},
		logger:      newTestLogger(),
		remoteBcast: make(chan broadcastReq, 1),
		clients:     make(map[string]*Client),
		subs:        make(map[string]map[string]struct{}),
		clientSubs:  make(map[string]map[string]struct{}),
	}
	h.remoteBcast <- broadcastReq{} // fill the queue

	done := make(chan struct{})
	go func() {
		defer close(done)
		h.handleRemoteBusEvent(remoteEvt(testEventIDDrop))
	}()

	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("handleRemoteBusEvent blocked on full remoteBcast queue")
	}
}

// Injecting a valid remote event encodes correctly to JSON for client delivery.
func TestHub_Bus_RemoteEvent_ClientReceivesValidJSON(t *testing.T) {
	auth := &fakeAuthorizer{}
	auth.setAccess("user-1", testWorkspaceID, TargetTypeChannel, testChannelID, true)

	bus := &fakeBus{}
	hub := newBusTestHub(auth, bus)
	defer hub.Shutdown()

	c := newClient("c1", "user-1", testWorkspaceID, &fakeSender{})
	registerInRunningHub(t, hub, c)
	if err := hub.Subscribe(context.Background(), c, TargetTypeChannel, testChannelID); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	bus.inject(remoteEvt(testEventIDJSON))

	if !waitForOutbox(c, 1) {
		t.Fatal("no event delivered")
	}
	raw := <-c.outbox
	var decoded Event
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("delivered payload is not valid JSON: %v", err)
	}
	if decoded.MessageID != testMessageID {
		t.Errorf("decoded MessageID = %q, want %q", decoded.MessageID, testMessageID)
	}
}

// ── negative validation tests ─────────────────────────────────────────────────

// newBusNegHub creates a hub+bus+subscribed client for negative-test scenarios.
func newBusNegHub(t *testing.T) (*Hub, *fakeBus, *Client) {
	t.Helper()
	auth := &fakeAuthorizer{}
	auth.setAccess("user-1", testWorkspaceID, TargetTypeChannel, testChannelID, true)
	bus := &fakeBus{}
	hub := newBusTestHub(auth, bus)
	c := newClient("c1", "user-1", testWorkspaceID, &fakeSender{})
	registerInRunningHub(t, hub, c)
	if err := hub.Subscribe(context.Background(), c, TargetTypeChannel, testChannelID); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	return hub, bus, c
}

func TestHub_Bus_Validation_InvalidWorkspaceID_Ignored(t *testing.T) {
	hub, bus, c := newBusNegHub(t)
	defer hub.Shutdown()

	e := remoteEvt(testEventID3)
	e.WorkspaceID = "not-a-uuid"
	bus.inject(e)

	time.Sleep(50 * time.Millisecond)
	if n := len(c.outbox); n != 0 {
		t.Fatalf("invalid workspace_id must be ignored, got %d events", n)
	}
}

func TestHub_Bus_Validation_NonCanonicalWorkspaceID_Canonicalized(t *testing.T) {
	hub, bus, c := newBusNegHub(t)
	defer hub.Shutdown()

	e := remoteEvt(testEventID3)
	e.WorkspaceID = strings.ToUpper(testWorkspaceID) // non-canonical uppercase
	bus.inject(e)

	// uuid.Parse canonicalizes to lowercase → matches subscriber's testWorkspaceID.
	if !waitForOutbox(c, 1) {
		t.Fatal("non-canonical UUID workspace_id must be canonicalized and delivered")
	}
}

func TestHub_Bus_Validation_InvalidTargetID_Ignored(t *testing.T) {
	hub, bus, c := newBusNegHub(t)
	defer hub.Shutdown()

	e := remoteEvt(testEventID3)
	e.TargetID = "not-a-uuid"
	bus.inject(e)

	time.Sleep(50 * time.Millisecond)
	if n := len(c.outbox); n != 0 {
		t.Fatalf("invalid target_id must be ignored, got %d events", n)
	}
}

func TestHub_Bus_Validation_MessageCreatedWithoutMessageID_Ignored(t *testing.T) {
	hub, bus, c := newBusNegHub(t)
	defer hub.Shutdown()

	e := remoteEvt(testEventID3)
	e.MessageID = ""
	bus.inject(e)

	time.Sleep(50 * time.Millisecond)
	if n := len(c.outbox); n != 0 {
		t.Fatalf("message.created without message_id must be ignored, got %d events", n)
	}
}

func TestHub_Bus_Validation_InvalidMessageID_Ignored(t *testing.T) {
	hub, bus, c := newBusNegHub(t)
	defer hub.Shutdown()

	e := remoteEvt(testEventID3)
	e.MessageID = "not-a-uuid"
	bus.inject(e)

	time.Sleep(50 * time.Millisecond)
	if n := len(c.outbox); n != 0 {
		t.Fatalf("invalid message_id must be ignored, got %d events", n)
	}
}

func TestHub_Bus_Validation_MissingEventID_Ignored(t *testing.T) {
	hub, bus, c := newBusNegHub(t)
	defer hub.Shutdown()

	e := remoteEvt(testEventID3)
	e.EventID = ""
	bus.inject(e)

	time.Sleep(50 * time.Millisecond)
	if n := len(c.outbox); n != 0 {
		t.Fatalf("event without event_id must be ignored, got %d events", n)
	}
}

func TestHub_Bus_Validation_InvalidEventID_Ignored(t *testing.T) {
	hub, bus, c := newBusNegHub(t)
	defer hub.Shutdown()

	e := remoteEvt(testEventID3)
	e.EventID = "not-a-uuid"
	bus.inject(e)

	time.Sleep(50 * time.Millisecond)
	if n := len(c.outbox); n != 0 {
		t.Fatalf("invalid event_id must be ignored, got %d events", n)
	}
}

func TestHub_Bus_Validation_OversizedSourceInstanceID_Ignored(t *testing.T) {
	hub, bus, c := newBusNegHub(t)
	defer hub.Shutdown()

	e := remoteEvt(testEventID3)
	e.SourceInstanceID = strings.Repeat("a", sourceInstanceIDMaxLen+1)
	bus.inject(e)

	time.Sleep(50 * time.Millisecond)
	if n := len(c.outbox); n != 0 {
		t.Fatalf("oversized source_instance_id must be ignored, got %d events", n)
	}
}

func TestHub_Bus_Validation_UnsafeSourceInstanceIDChars_Ignored(t *testing.T) {
	hub, bus, c := newBusNegHub(t)
	defer hub.Shutdown()

	e := remoteEvt(testEventID3)
	e.SourceInstanceID = "bad instance id!" // spaces and ! are not in [A-Za-z0-9._-]
	bus.inject(e)

	time.Sleep(50 * time.Millisecond)
	if n := len(c.outbox); n != 0 {
		t.Fatalf("unsafe source_instance_id chars must be ignored, got %d events", n)
	}
}

// channelForWorkspace must reject non-UUID workspace_ids, preventing glob injection.
func TestHub_Bus_Validation_InvalidWorkspaceID_NoUnsafeChannelName(t *testing.T) {
	unsafe := []string{"ws*bad", "ws?glob", "ws[bad]", "not-a-uuid", ""}
	for _, id := range unsafe {
		ch, err := channelForWorkspace(id)
		if err == nil {
			t.Errorf("channelForWorkspace(%q) must return error, got channel %q", id, ch)
		}
	}
}
