package ws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"
)

// ── test helpers ─────────────────────────────────────────────────────────────

type fakeSender struct {
	mu     sync.Mutex
	closed bool
}

func (f *fakeSender) Send(_ []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return errors.New("connection closed")
	}
	return nil
}

func (f *fakeSender) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
}

func (f *fakeSender) isClosed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

// fakeAuthorizer is a thread-safe SubscriptionAuthorizer for tests.
type fakeAuthorizer struct {
	mu      sync.RWMutex
	entries map[string]bool
	errs    map[string]error
}

func (a *fakeAuthorizer) setAccess(userID, workspaceID string, tt TargetType, targetID string, allowed bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.entries == nil {
		a.entries = make(map[string]bool)
	}
	a.entries[fakeAuthKey(userID, workspaceID, tt, targetID)] = allowed
}

func (a *fakeAuthorizer) setErr(userID, workspaceID string, tt TargetType, targetID string, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.errs == nil {
		a.errs = make(map[string]error)
	}
	if err == nil {
		delete(a.errs, fakeAuthKey(userID, workspaceID, tt, targetID))
	} else {
		a.errs[fakeAuthKey(userID, workspaceID, tt, targetID)] = err
	}
}

func (a *fakeAuthorizer) CanAccess(_ context.Context, userID, workspaceID string, tt TargetType, targetID string) (bool, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	k := fakeAuthKey(userID, workspaceID, tt, targetID)
	if err, ok := a.errs[k]; ok {
		return false, err
	}
	return a.entries[k], nil
}

func fakeAuthKey(userID, workspaceID string, tt TargetType, targetID string) string {
	return userID + "|" + workspaceID + "|" + string(tt) + "|" + targetID
}

func newTestLogger() *slog.Logger { return slog.Default() }

// newTestHub creates a Hub for white-box tests without starting the run goroutine.
// Tests call handleSubscribe, handleBroadcast, and dropClient directly.
func newTestHub(auth SubscriptionAuthorizer) *Hub {
	return &Hub{
		authorizer:  auth,
		bus:         NopBus{},
		instanceID:  "test-instance",
		busCancel:   func() {},
		logger:      newTestLogger(),
		remoteBcast: make(chan broadcastReq, 256),
		clients:     make(map[string]*Client),
		subs:        make(map[string]map[string]struct{}),
		clientSubs:  make(map[string]map[string]struct{}),
	}
}

// registerInHub adds a client to hub state directly, as if Register had been called.
func registerInHub(t *testing.T, h *Hub, c *Client) {
	t.Helper()
	if !h.addClient(c) {
		t.Fatalf("register client %q: duplicate client ID", c.id)
	}
}

func hubHasClient(h *Hub, clientID string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	_, ok := h.clients[clientID]
	return ok
}

func hubGetClient(h *Hub, clientID string) *Client {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.clients[clientID]
}

func hubHasClientSubs(h *Hub, clientID string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	_, ok := h.clientSubs[clientID]
	return ok
}

func hubHasSubscription(h *Hub, key, clientID string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	_, ok := h.subs[key][clientID]
	return ok
}

func hubHasSubscriptionTarget(h *Hub, key string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	_, ok := h.subs[key]
	return ok
}

func makeEvent(workspaceID string, tt TargetType, targetID, messageID string) (Event, []byte) {
	evt := Event{
		Type:        EventTypeMessageCreated,
		WorkspaceID: workspaceID,
		TargetType:  tt,
		TargetID:    targetID,
		MessageID:   messageID,
		CreatedAt:   time.Now().UTC(),
	}
	data, _ := json.Marshal(evt)
	return evt, data
}

func mustSubscribe(t *testing.T, h *Hub, c *Client, tt TargetType, targetID string) {
	t.Helper()
	req := subscribeReq{
		ctx:        context.Background(),
		client:     c,
		targetType: tt,
		targetID:   targetID,
		resp:       make(chan error, 1),
	}
	if err := h.handleSubscribe(req); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
}

// ── hub unit tests (white-box, synchronous) ───────────────────────────────────

func TestHub_Register_TracksClient(t *testing.T) {
	h := newTestHub(&fakeAuthorizer{})
	snd := &fakeSender{}
	c := newClient("c1", "user-1", "ws-1", snd)
	registerInHub(t, h, c)

	if !hubHasClient(h, "c1") {
		t.Fatal("client should be tracked after register")
	}
	if !hubHasClientSubs(h, "c1") {
		t.Fatal("clientSubs entry should exist after register")
	}
}

func TestHub_Register_DuplicateClientID_DropsNewClient(t *testing.T) {
	hub := NewHub(&fakeAuthorizer{}, newTestLogger(), NopBus{}, "test-inst")
	defer hub.Shutdown()

	originalSender := &fakeSender{}
	duplicateSender := &fakeSender{}
	original := newClient("c1", "user-1", "ws-1", originalSender)
	duplicate := newClient("c1", "user-2", "ws-1", duplicateSender)

	hub.Register(original)
	hub.Register(duplicate)

	if originalSender.isClosed() {
		t.Fatal("original client must remain connected after duplicate registration")
	}
	if !duplicateSender.isClosed() {
		t.Fatal("duplicate client must be closed instead of panicking")
	}

	if got := hubGetClient(hub, "c1"); got != original {
		t.Fatal("duplicate registration must not replace existing client")
	}
}

func TestHub_Unregister_RemovesClientAndSubscriptions(t *testing.T) {
	auth := &fakeAuthorizer{}
	auth.setAccess("user-1", "ws-1", TargetTypeChannel, "ch-1", true)

	h := newTestHub(auth)
	snd := &fakeSender{}
	c := newClient("c1", "user-1", "ws-1", snd)
	registerInHub(t, h, c)
	mustSubscribe(t, h, c, TargetTypeChannel, "ch-1")

	// Verify subscription was recorded.
	key := targetKey{workspaceID: "ws-1", targetType: TargetTypeChannel, targetID: "ch-1"}.String()
	if !hubHasSubscription(h, key, "c1") {
		t.Fatal("subscription should exist before unregister")
	}

	h.dropClient(c)

	if hubHasClient(h, "c1") {
		t.Fatal("client should be removed after unregister")
	}
	if hubHasSubscriptionTarget(h, key) {
		t.Fatal("subscription entry should be cleaned up after unregister")
	}
	if hubHasClientSubs(h, "c1") {
		t.Fatal("clientSubs entry should be cleaned up after unregister")
	}
}

func TestHub_Unregister_NotRegistered_IsNoop(t *testing.T) {
	h := newTestHub(&fakeAuthorizer{})
	snd := &fakeSender{}
	c := newClient("ghost", "user-1", "ws-1", snd)
	// Should not panic.
	h.dropClient(c)
}

func TestHub_Subscribe_Allowed_AddsSubscription(t *testing.T) {
	auth := &fakeAuthorizer{}
	auth.setAccess("user-1", "ws-1", TargetTypeChannel, "ch-pub", true)

	h := newTestHub(auth)
	snd := &fakeSender{}
	c := newClient("c1", "user-1", "ws-1", snd)
	registerInHub(t, h, c)

	req := subscribeReq{
		ctx:        context.Background(),
		client:     c,
		targetType: TargetTypeChannel,
		targetID:   "ch-pub",
		resp:       make(chan error, 1),
	}
	if err := h.handleSubscribe(req); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	key := targetKey{workspaceID: "ws-1", targetType: TargetTypeChannel, targetID: "ch-pub"}.String()
	if !hubHasSubscription(h, key, "c1") {
		t.Fatal("subscription should exist after allowed subscribe")
	}
}

func TestHub_Subscribe_Denied_ReturnsForbidden(t *testing.T) {
	auth := &fakeAuthorizer{}
	// No access set → denied

	h := newTestHub(auth)
	c := newClient("c1", "user-1", "ws-1", &fakeSender{})
	registerInHub(t, h, c)

	req := subscribeReq{
		ctx:        context.Background(),
		client:     c,
		targetType: TargetTypeChannel,
		targetID:   "ch-private",
		resp:       make(chan error, 1),
	}
	err := h.handleSubscribe(req)
	if !errors.Is(err, ErrSubscribeForbidden) {
		t.Fatalf("expected ErrSubscribeForbidden, got: %v", err)
	}
}

func TestHub_Subscribe_DM_Allowed(t *testing.T) {
	auth := &fakeAuthorizer{}
	auth.setAccess("user-1", "ws-1", TargetTypeDM, "dm-1", true)

	h := newTestHub(auth)
	c := newClient("c1", "user-1", "ws-1", &fakeSender{})
	registerInHub(t, h, c)
	mustSubscribe(t, h, c, TargetTypeDM, "dm-1")

	key := targetKey{workspaceID: "ws-1", targetType: TargetTypeDM, targetID: "dm-1"}.String()
	if !hubHasSubscription(h, key, "c1") {
		t.Fatal("DM subscription should exist")
	}
}

func TestHub_Subscribe_DM_NotMember_Denied(t *testing.T) {
	h := newTestHub(&fakeAuthorizer{})
	c := newClient("c1", "user-1", "ws-1", &fakeSender{})
	registerInHub(t, h, c)

	req := subscribeReq{
		ctx:        context.Background(),
		client:     c,
		targetType: TargetTypeDM,
		targetID:   "dm-1",
		resp:       make(chan error, 1),
	}
	if err := h.handleSubscribe(req); !errors.Is(err, ErrSubscribeForbidden) {
		t.Fatalf("expected ErrSubscribeForbidden for DM non-member, got: %v", err)
	}
}

func TestHub_Subscribe_PrivateChannel_NonMember_Denied_NonEnumerating(t *testing.T) {
	// Private channel non-member must get ErrSubscribeForbidden, not ErrNotFound,
	// so callers cannot enumerate whether the channel exists.
	h := newTestHub(&fakeAuthorizer{})
	c := newClient("c1", "user-1", "ws-1", &fakeSender{})
	registerInHub(t, h, c)

	req := subscribeReq{
		ctx:        context.Background(),
		client:     c,
		targetType: TargetTypeChannel,
		targetID:   "ch-private",
		resp:       make(chan error, 1),
	}
	err := h.handleSubscribe(req)
	if !errors.Is(err, ErrSubscribeForbidden) {
		t.Fatalf("private channel non-member must get ErrSubscribeForbidden, not %v", err)
	}
	// Must NOT be a domain.ErrNotFound (non-enumerating).
	if errors.Is(err, errors.New("not found")) {
		t.Fatal("error must not expose not-found semantics")
	}
}

func TestHub_Subscribe_ClientNotRegistered_ReturnsError(t *testing.T) {
	h := newTestHub(&fakeAuthorizer{})
	c := newClient("unregistered", "user-1", "ws-1", &fakeSender{})

	req := subscribeReq{
		ctx:        context.Background(),
		client:     c,
		targetType: TargetTypeChannel,
		targetID:   "ch-1",
		resp:       make(chan error, 1),
	}
	if err := h.handleSubscribe(req); err == nil {
		t.Fatal("expected error for unregistered client")
	}
}

func TestHub_Broadcast_DeliversToSubscriber(t *testing.T) {
	auth := &fakeAuthorizer{}
	auth.setAccess("user-1", "ws-1", TargetTypeChannel, "ch-1", true)

	h := newTestHub(auth)
	c := newClient("c1", "user-1", "ws-1", &fakeSender{})
	registerInHub(t, h, c)
	mustSubscribe(t, h, c, TargetTypeChannel, "ch-1")

	evt, data := makeEvent("ws-1", TargetTypeChannel, "ch-1", "msg-1")
	h.handleBroadcast(broadcastReq{event: evt, data: data})

	// enqueue writes to c.outbox; the write pump (deferred) drains it.
	if n := len(c.outbox); n != 1 {
		t.Fatalf("expected 1 event in outbox, got %d", n)
	}
}

func TestHub_Broadcast_DoesNotDeliverToUnsubscribed(t *testing.T) {
	auth := &fakeAuthorizer{}
	auth.setAccess("user-1", "ws-1", TargetTypeChannel, "ch-1", true)

	h := newTestHub(auth)
	c := newClient("c1", "user-1", "ws-1", &fakeSender{})
	registerInHub(t, h, c)
	// Client is registered but NOT subscribed to ch-1.

	evt, data := makeEvent("ws-1", TargetTypeChannel, "ch-1", "msg-1")
	h.handleBroadcast(broadcastReq{event: evt, data: data})

	if n := len(c.outbox); n != 0 {
		t.Fatalf("unsubscribed client must not receive events, got %d in outbox", n)
	}
}

func TestHub_Broadcast_AuthRevoked_AfterSubscribe_NoDelivery(t *testing.T) {
	auth := &fakeAuthorizer{}
	auth.setAccess("user-1", "ws-1", TargetTypeChannel, "ch-1", true)

	h := newTestHub(auth)
	snd := &fakeSender{}
	c := newClient("c1", "user-1", "ws-1", snd)
	registerInHub(t, h, c)
	mustSubscribe(t, h, c, TargetTypeChannel, "ch-1")

	// Revoke access (e.g., user removed from channel or workspace).
	auth.setAccess("user-1", "ws-1", TargetTypeChannel, "ch-1", false)

	evt, data := makeEvent("ws-1", TargetTypeChannel, "ch-1", "msg-1")
	h.handleBroadcast(broadcastReq{event: evt, data: data})

	if n := len(c.outbox); n != 0 {
		t.Fatalf("revoked client must not receive events, got %d in outbox", n)
	}

	// Subscription should also be removed.
	key := targetKey{workspaceID: "ws-1", targetType: TargetTypeChannel, targetID: "ch-1"}.String()
	if hubHasSubscriptionTarget(h, key) {
		t.Fatal("subscription should be removed after auth revocation")
	}
}

func TestHub_Broadcast_StaleDMMembership_NoDelivery(t *testing.T) {
	// Simulates a user removed from a DM after subscribing. The authorizer
	// returns false on re-check (stale dm_members bypassed by active workspace
	// membership check in SQL), so no event is delivered.
	auth := &fakeAuthorizer{}
	auth.setAccess("user-1", "ws-1", TargetTypeDM, "dm-1", true)

	h := newTestHub(auth)
	c := newClient("c1", "user-1", "ws-1", &fakeSender{})
	registerInHub(t, h, c)
	mustSubscribe(t, h, c, TargetTypeDM, "dm-1")

	// Simulate DM membership becoming stale.
	auth.setAccess("user-1", "ws-1", TargetTypeDM, "dm-1", false)

	evt, data := makeEvent("ws-1", TargetTypeDM, "dm-1", "msg-2")
	h.handleBroadcast(broadcastReq{event: evt, data: data})

	if n := len(c.outbox); n != 0 {
		t.Fatalf("stale DM member must not receive events, got %d in outbox", n)
	}
}

func TestHub_Broadcast_NoSubscribers_NoOp(t *testing.T) {
	h := newTestHub(&fakeAuthorizer{})
	evt, data := makeEvent("ws-1", TargetTypeChannel, "ch-1", "msg-1")
	// Must not panic or error.
	h.handleBroadcast(broadcastReq{event: evt, data: data})
}

func TestHub_Broadcast_SlowClient_DropsConnection(t *testing.T) {
	auth := &fakeAuthorizer{}
	auth.setAccess("user-1", "ws-1", TargetTypeChannel, "ch-1", true)

	h := newTestHub(auth)
	snd := &fakeSender{}
	c := newClient("c1", "user-1", "ws-1", snd)
	registerInHub(t, h, c)
	mustSubscribe(t, h, c, TargetTypeChannel, "ch-1")

	// Fill outbox to capacity.
	for i := 0; i < outboxSize; i++ {
		if !c.enqueue([]byte(`"x"`)) {
			t.Fatalf("enqueue failed early at %d", i)
		}
	}

	// Next broadcast must detect the full outbox and drop the client.
	evt, data := makeEvent("ws-1", TargetTypeChannel, "ch-1", "msg-overflow")
	h.handleBroadcast(broadcastReq{event: evt, data: data})

	if hubHasClient(h, "c1") {
		t.Fatal("slow client should be removed from hub after overflow")
	}
	if !snd.isClosed() {
		t.Fatal("slow client connection should be closed after overflow")
	}
	key := targetKey{workspaceID: "ws-1", targetType: TargetTypeChannel, targetID: "ch-1"}.String()
	if hubHasSubscriptionTarget(h, key) {
		t.Fatal("slow client subscriptions should be cleaned up after drop")
	}
}

func TestHub_Broadcast_SlowClient_HubNotBlocked(t *testing.T) {
	// Verify that a full outbox causes a drop (not a block). The broadcast must
	// return promptly even when a client's outbox is full.
	auth := &fakeAuthorizer{}
	auth.setAccess("user-1", "ws-1", TargetTypeChannel, "ch-1", true)

	h := newTestHub(auth)
	c := newClient("c1", "user-1", "ws-1", &fakeSender{})
	registerInHub(t, h, c)
	mustSubscribe(t, h, c, TargetTypeChannel, "ch-1")

	for i := 0; i < outboxSize; i++ {
		c.outbox <- []byte(`"x"`)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		evt, data := makeEvent("ws-1", TargetTypeChannel, "ch-1", "msg")
		h.handleBroadcast(broadcastReq{event: evt, data: data})
	}()

	select {
	case <-done:
		// OK: returned promptly.
	case <-time.After(500 * time.Millisecond):
		t.Fatal("handleBroadcast blocked on slow client")
	}
}

func TestHub_Broadcast_MultipleSubscribers_OnlyAuthorized(t *testing.T) {
	auth := &fakeAuthorizer{}
	auth.setAccess("allowed", "ws-1", TargetTypeChannel, "ch-1", true)
	// "denied" user has no access set.

	h := newTestHub(auth)

	sndA := &fakeSender{}
	cA := newClient("cA", "allowed", "ws-1", sndA)
	registerInHub(t, h, cA)
	mustSubscribe(t, h, cA, TargetTypeChannel, "ch-1")

	sndD := &fakeSender{}
	cD := newClient("cD", "denied", "ws-1", sndD)
	registerInHub(t, h, cD)
	// cD subscribes while allowed; then revoked before broadcast.
	auth.setAccess("denied", "ws-1", TargetTypeChannel, "ch-1", true)
	mustSubscribe(t, h, cD, TargetTypeChannel, "ch-1")
	auth.setAccess("denied", "ws-1", TargetTypeChannel, "ch-1", false)

	evt, data := makeEvent("ws-1", TargetTypeChannel, "ch-1", "msg-1")
	h.handleBroadcast(broadcastReq{event: evt, data: data})

	// enqueue writes to outbox; write pump (deferred) drains it.
	if n := len(cA.outbox); n != 1 {
		t.Fatalf("allowed client should have 1 event in outbox, got %d", n)
	}
	if n := len(cD.outbox); n != 0 {
		t.Fatalf("denied client must have 0 events in outbox, got %d", n)
	}
}

func TestHub_MalformedClientMessage_NoError(t *testing.T) {
	inputs := [][]byte{
		[]byte(`{invalid`),
		[]byte(`null`),
		[]byte(`[]`),
		[]byte(`"string"`),
		[]byte(``),
	}
	for _, raw := range inputs {
		var msg ClientMessage
		// Must not panic; errors are acceptable and expected for invalid JSON.
		_ = json.Unmarshal(raw, &msg)
	}
}

func TestHub_Subscribe_CrossWorkspace_DeniedByServerIdentity(t *testing.T) {
	// workspaceID on the client is server-asserted from the auth context.
	// Even if the target exists in another workspace, the client's workspaceID
	// scopes all authorization checks.
	auth := &fakeAuthorizer{}
	// Access is granted only for ws-1; client is in ws-2.
	auth.setAccess("user-1", "ws-1", TargetTypeChannel, "ch-1", true)

	h := newTestHub(auth)
	c := newClient("c1", "user-1", "ws-2", &fakeSender{}) // client is in ws-2
	registerInHub(t, h, c)

	req := subscribeReq{
		ctx:        context.Background(),
		client:     c,
		targetType: TargetTypeChannel,
		targetID:   "ch-1",
		resp:       make(chan error, 1),
	}
	err := h.handleSubscribe(req)
	if !errors.Is(err, ErrSubscribeForbidden) {
		t.Fatalf("cross-workspace subscription must be denied, got: %v", err)
	}
}

// ── goroutine / race safety test ─────────────────────────────────────────────

func TestHub_Register_ThenSubscribe_AlwaysSucceeds(t *testing.T) {
	// Register is now synchronous (ack-based). After Register returns, Subscribe
	// must never fail with "client not registered".
	auth := &fakeAuthorizer{}
	auth.setAccess("user-1", "ws-1", TargetTypeChannel, "ch-1", true)
	hub := NewHub(auth, newTestLogger(), NopBus{}, "test-inst")
	defer hub.Shutdown()

	const iterations = 50
	for i := 0; i < iterations; i++ {
		c := newClient(fmt.Sprintf("c%d", i), "user-1", "ws-1", &fakeSender{})
		hub.Register(c) // synchronous: client is in hub state when this returns
		err := hub.Subscribe(context.Background(), c, TargetTypeChannel, "ch-1")
		if err != nil {
			t.Fatalf("iteration %d: Subscribe after Register must not fail, got: %v", i, err)
		}
		hub.Unregister(c)
	}
}

func TestHub_Concurrent_RegisterSubscribeBroadcast_NoRace(t *testing.T) {
	auth := &fakeAuthorizer{}
	auth.setAccess("user-1", "ws-1", TargetTypeChannel, "ch-1", true)
	hub := NewHub(auth, newTestLogger(), NopBus{}, "test-inst")
	defer hub.Shutdown()

	const goroutines = 20
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func(n int) {
			defer wg.Done()
			c := newClient(fmt.Sprintf("c%d", n), "user-1", "ws-1", &fakeSender{})
			hub.Register(c) // synchronous: safe to Subscribe immediately after
			_ = hub.Subscribe(context.Background(), c, TargetTypeChannel, "ch-1")
			hub.PublishMessageCreated(context.Background(), "ws-1", TargetTypeChannel, "ch-1", fmt.Sprintf("msg-%d", n))
			hub.Unregister(c)
		}(i)
	}
	wg.Wait()
}

func TestHub_ConcurrentBroadcastAndUnregister_NoRace(t *testing.T) {
	// This test validates the map locking contract when run with -race.
	auth := &fakeAuthorizer{}
	auth.setAccess("user-1", "ws-1", TargetTypeChannel, "ch-1", true)

	h := newTestHub(auth)
	clients := make([]*Client, 64)
	for i := range clients {
		c := newClient(fmt.Sprintf("c%d", i), "user-1", "ws-1", &fakeSender{})
		registerInHub(t, h, c)
		mustSubscribe(t, h, c, TargetTypeChannel, "ch-1")
		clients[i] = c
	}

	evt, data := makeEvent("ws-1", TargetTypeChannel, "ch-1", "msg-1")
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < 200; i++ {
			h.handleBroadcast(broadcastReq{event: evt, data: data})
		}
	}()

	go func() {
		defer wg.Done()
		<-start
		for _, c := range clients {
			h.dropClient(c)
		}
	}()

	close(start)
	wg.Wait()
}

func TestHub_Shutdown_ClosesAllClients(t *testing.T) {
	hub := NewHub(&fakeAuthorizer{}, newTestLogger(), NopBus{}, "test-inst")

	snd1 := &fakeSender{}
	snd2 := &fakeSender{}
	c1 := newClient("c1", "u1", "ws-1", snd1)
	c2 := newClient("c2", "u2", "ws-1", snd2)
	// Register is synchronous; both clients are in hub state when Shutdown runs.
	hub.Register(c1)
	hub.Register(c2)

	hub.Shutdown()

	if !snd1.isClosed() {
		t.Error("c1 connection should be closed after Shutdown")
	}
	if !snd2.isClosed() {
		t.Error("c2 connection should be closed after Shutdown")
	}
}

// ── auth error behavior during broadcast ─────────────────────────────────────

func TestHub_Broadcast_AuthError_SkipsDeliveryKeepsSubscription(t *testing.T) {
	// If the auth re-check returns an error (transient DB issue), the event
	// must not be delivered but the subscription must be kept. The next
	// broadcast should retry.
	auth := &fakeAuthorizer{}
	auth.setAccess("user-1", "ws-1", TargetTypeChannel, "ch-1", true)

	h := newTestHub(auth)
	c := newClient("c1", "user-1", "ws-1", &fakeSender{})
	registerInHub(t, h, c)
	mustSubscribe(t, h, c, TargetTypeChannel, "ch-1")

	// Simulate a transient auth error.
	auth.setErr("user-1", "ws-1", TargetTypeChannel, "ch-1", fmt.Errorf("db unavailable"))

	evt, data := makeEvent("ws-1", TargetTypeChannel, "ch-1", "msg-1")
	h.handleBroadcast(broadcastReq{event: evt, data: data})

	// Event must not be delivered.
	if n := len(c.outbox); n != 0 {
		t.Fatalf("expected 0 events on auth error, got %d", n)
	}

	// Subscription must be kept.
	key := targetKey{workspaceID: "ws-1", targetType: TargetTypeChannel, targetID: "ch-1"}.String()
	if !hubHasSubscription(h, key, "c1") {
		t.Fatal("subscription must be kept after transient auth error")
	}
}

func TestHub_Broadcast_AuthError_ThenRecovery_Delivers(t *testing.T) {
	// After a transient error clears, the next broadcast delivers normally.
	auth := &fakeAuthorizer{}
	auth.setAccess("user-1", "ws-1", TargetTypeChannel, "ch-1", true)

	h := newTestHub(auth)
	c := newClient("c1", "user-1", "ws-1", &fakeSender{})
	registerInHub(t, h, c)
	mustSubscribe(t, h, c, TargetTypeChannel, "ch-1")

	// First broadcast: auth error → no delivery, sub kept.
	auth.setErr("user-1", "ws-1", TargetTypeChannel, "ch-1", fmt.Errorf("timeout"))
	evt, data := makeEvent("ws-1", TargetTypeChannel, "ch-1", "msg-1")
	h.handleBroadcast(broadcastReq{event: evt, data: data})

	// Clear error: second broadcast delivers.
	auth.setErr("user-1", "ws-1", TargetTypeChannel, "ch-1", nil)
	evt2, data2 := makeEvent("ws-1", TargetTypeChannel, "ch-1", "msg-2")
	h.handleBroadcast(broadcastReq{event: evt2, data: data2})

	if n := len(c.outbox); n != 1 {
		t.Fatalf("expected 1 event after recovery, got %d", n)
	}
}

func TestHub_Broadcast_TransientAuthError_KeepsSubscription(t *testing.T) {
	// handleBroadcast uses a fresh background context for auth re-checks, not the
	// caller's publish context. A transient auth error (store unavailable, context
	// timeout) must skip delivery but keep the subscription intact — it must not
	// be treated as a revocation.
	auth := &fakeAuthorizer{}
	auth.setAccess("user-1", "ws-1", TargetTypeChannel, "ch-1", true)

	h := newTestHub(auth)
	c := newClient("c1", "user-1", "ws-1", &fakeSender{})
	registerInHub(t, h, c)
	mustSubscribe(t, h, c, TargetTypeChannel, "ch-1")

	// handleBroadcast uses its own bounded background context for re-checks.
	evt, data := makeEvent("ws-1", TargetTypeChannel, "ch-1", "msg-1")
	h.handleBroadcast(broadcastReq{event: evt, data: data})

	// Subscription must still exist; transient error is not a revocation.
	key := targetKey{workspaceID: "ws-1", targetType: TargetTypeChannel, targetID: "ch-1"}.String()
	if !hubHasSubscription(h, key, "c1") {
		t.Fatal("subscription must not be revoked on transient auth error")
	}
}
