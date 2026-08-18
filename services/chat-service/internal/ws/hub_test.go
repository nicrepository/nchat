package ws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
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

func (f *fakeSender) Ping(_ context.Context) error {
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
	calls   int
	targets []string
}

type blockingTargetAuthorizer struct {
	blockedTarget string
	started       chan struct{}
	release       chan struct{}
	startedOnce   sync.Once
	releaseOnce   sync.Once
}

type blockingDenyAuthorizer struct {
	started chan struct{}
	release chan struct{}
}

type orderedBroadcastAuthorizer struct {
	mu            sync.Mutex
	calls         int
	firstStarted  chan struct{}
	releaseFirst  chan struct{}
	secondStarted chan struct{}
	releaseOnce   sync.Once
}

func (a *orderedBroadcastAuthorizer) release() {
	a.releaseOnce.Do(func() { close(a.releaseFirst) })
}

func newOrderedBroadcastAuthorizer() *orderedBroadcastAuthorizer {
	return &orderedBroadcastAuthorizer{
		firstStarted:  make(chan struct{}),
		releaseFirst:  make(chan struct{}),
		secondStarted: make(chan struct{}),
	}
}

func (a *orderedBroadcastAuthorizer) CanAccess(ctx context.Context, _, _ string, _ TargetType, _ string) (bool, error) {
	a.mu.Lock()
	a.calls++
	call := a.calls
	a.mu.Unlock()

	switch call {
	case 1:
		close(a.firstStarted)
		select {
		case <-a.releaseFirst:
			return true, nil
		case <-ctx.Done():
			return false, ctx.Err()
		}
	case 2:
		close(a.secondStarted)
	}
	return true, nil
}

func (a *blockingDenyAuthorizer) CanAccess(_ context.Context, _, _ string, _ TargetType, _ string) (bool, error) {
	close(a.started)
	<-a.release
	return false, nil
}

func newBlockingTargetAuthorizer(blockedTarget string) *blockingTargetAuthorizer {
	return &blockingTargetAuthorizer{
		blockedTarget: blockedTarget,
		started:       make(chan struct{}),
		release:       make(chan struct{}),
	}
}

func (a *blockingTargetAuthorizer) CanAccess(ctx context.Context, _, _ string, _ TargetType, targetID string) (bool, error) {
	if targetID != a.blockedTarget {
		return true, nil
	}
	a.startedOnce.Do(func() { close(a.started) })
	select {
	case <-a.release:
		return true, nil
	case <-ctx.Done():
		return false, ctx.Err()
	}
}

func (a *blockingTargetAuthorizer) unblock() {
	a.releaseOnce.Do(func() { close(a.release) })
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
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls++
	a.targets = append(a.targets, targetID)
	k := fakeAuthKey(userID, workspaceID, tt, targetID)
	if err, ok := a.errs[k]; ok {
		return false, err
	}
	return a.entries[k], nil
}

func (a *fakeAuthorizer) lastTargetID() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if len(a.targets) == 0 {
		return ""
	}
	return a.targets[len(a.targets)-1]
}

func (a *fakeAuthorizer) callCount() int {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.calls
}

func fakeAuthKey(userID, workspaceID string, tt TargetType, targetID string) string {
	return userID + "|" + workspaceID + "|" + string(tt) + "|" + targetID
}

func newTestLogger() *slog.Logger { return slog.Default() }

// newTestHub creates a Hub for white-box tests without starting the run goroutine.
// Tests call handleSubscribe, handleBroadcast, and dropClient directly.
func newTestHub(auth SubscriptionAuthorizer) *Hub {
	return &Hub{
		authorizer:              auth,
		bus:                     NopBus{},
		instanceID:              "test-instance",
		busCancel:               func() {},
		logger:                  newTestLogger(),
		remoteBcast:             make(chan broadcastReq, 256),
		presenceSignal:          make(chan struct{}, 1),
		presencePending:         make(map[presenceKey]presenceChange),
		clients:                 make(map[string]*Client),
		subs:                    make(map[string]map[string]struct{}),
		clientSubs:              make(map[string]map[string]struct{}),
		subscriptionGenerations: make(map[string]map[string]uint64),
		typingActive:            make(map[string]map[string]struct{}),
		deliveredRosters:        make(map[string]string),
		asserted:                make(map[presenceKey]map[string]uint64),
		assertionEpoch:          make(map[presenceKey]map[string]uint64),
		pendingAssertions:       make(map[presenceKey]map[string]struct{}),
		assertionLocks:          newAssertionSequencer(),
		// Unique per hub, like the real one: two hubs in one test are two
		// processes, and a shared origin would make each drop the other's events
		// as its own echo.
		presenceInstanceID: "runtime-" + uuid.NewString(),
		reconcileSignal:    make(chan struct{}, 1),
	}
}

// registerInHub adds a client to hub state directly, as if Register had been called.
func registerInHub(t *testing.T, h *Hub, c *Client) {
	t.Helper()
	if !h.addClient(c) {
		t.Fatalf("register client %q: duplicate client ID", c.id)
	}
}

func registerInRunningHub(t *testing.T, h *Hub, c *Client) {
	t.Helper()
	if !h.Register(c) {
		t.Fatalf("register client %q: hub rejected registration", c.id)
	}
}

func newRunningTestHub(t *testing.T, auth SubscriptionAuthorizer) *Hub {
	t.Helper()
	hub := NewHub(auth, newTestLogger(), NopBus{}, "running-test-instance")
	t.Cleanup(hub.Shutdown)
	return hub
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

func hubHasClientSubscription(h *Hub, clientID, key string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	_, ok := h.clientSubs[clientID][key]
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

func enqueueBroadcastForTest(t *testing.T, h *Hub, evt Event, data []byte) <-chan struct{} {
	t.Helper()
	done := make(chan struct{})
	select {
	case h.bcast <- broadcastReq{event: evt, data: data, done: done}:
	case <-time.After(testIOTimeout):
		t.Fatal("timed out enqueueing broadcast")
	}
	return done
}

func waitForBroadcast(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(testIOTimeout):
		t.Fatal("timed out waiting for broadcast")
	}
}

// eventually polls condition until it returns true or timeout elapses.
// It is the canonical polling helper for hub-state assertions that are
// inherently asynchronous: hub.Unregister is a non-blocking channel send
// processed by the hub run goroutine, so hub state may not yet reflect the
// unregister when the calling goroutine checks immediately after pump exit.
func eventually(t *testing.T, condition func() bool, timeout time.Duration, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out after %s: %s", timeout, msg)
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

func TestHub_SlowSubscribeAuthorizationDoesNotBlockRegister(t *testing.T) {
	auth := newBlockingTargetAuthorizer("slow-room")
	hub := NewHub(auth, newTestLogger(), NopBus{}, "slow-subscribe-register")
	defer hub.Shutdown()
	defer auth.unblock()

	clientA := newClient("client-a", "user-a", "workspace", &fakeSender{})
	registerInRunningHub(t, hub, clientA)
	subscribeResult := make(chan error, 1)
	go func() {
		subscribeResult <- hub.Subscribe(context.Background(), clientA, TargetTypeChannel, "slow-room")
	}()

	select {
	case <-auth.started:
	case <-time.After(testIOTimeout):
		t.Fatal("slow authorizer did not start")
	}

	clientB := newClient("client-b", "user-b", "workspace", &fakeSender{})
	registerResult := make(chan bool, 1)
	go func() { registerResult <- hub.Register(clientB) }()
	select {
	case registered := <-registerResult:
		if !registered {
			t.Fatal("hub rejected client B while client A authorization was pending")
		}
	case <-time.After(testIOTimeout):
		t.Fatal("register was blocked by another client's authorization")
	}

	auth.unblock()
	select {
	case err := <-subscribeResult:
		if err != nil {
			t.Fatalf("client A subscribe failed after authorization release: %v", err)
		}
	case <-time.After(testIOTimeout):
		t.Fatal("client A subscribe did not finish after authorization release")
	}
}

func TestHub_ClientCloseCancelsPendingSubscribeAuthorization(t *testing.T) {
	auth := newBlockingTargetAuthorizer("slow-room")
	hub := NewHub(auth, newTestLogger(), NopBus{}, "cancel-slow-subscribe")
	defer hub.Shutdown()
	defer auth.unblock()

	client := newClient("client-a", "user-a", "workspace", &fakeSender{})
	registerInRunningHub(t, hub, client)
	result := make(chan error, 1)
	go func() {
		result <- hub.Subscribe(context.Background(), client, TargetTypeChannel, "slow-room")
	}()

	select {
	case <-auth.started:
	case <-time.After(testIOTimeout):
		t.Fatal("slow authorizer did not start")
	}
	client.close()

	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected client close cancellation, got: %v", err)
		}
	case <-time.After(testIOTimeout):
		t.Fatal("client close did not cancel pending authorization")
	}
}

func TestHub_ShutdownCancelsPendingSubscribeAuthorization(t *testing.T) {
	auth := newBlockingTargetAuthorizer("slow-room")
	hub := NewHub(auth, newTestLogger(), NopBus{}, "shutdown-slow-subscribe")
	defer auth.unblock()

	client := newClient("client-a", "user-a", "workspace", &fakeSender{})
	registerInRunningHub(t, hub, client)
	result := make(chan error, 1)
	go func() {
		result <- hub.Subscribe(context.Background(), client, TargetTypeChannel, "slow-room")
	}()

	select {
	case <-auth.started:
	case <-time.After(testIOTimeout):
		t.Fatal("slow authorizer did not start")
	}

	shutdownDone := make(chan struct{})
	go func() {
		hub.Shutdown()
		close(shutdownDone)
	}()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) && !errors.Is(err, ErrHubShutdown) {
			t.Fatalf("pending subscribe after shutdown = %v, want cancellation", err)
		}
	case <-time.After(testIOTimeout):
		t.Fatal("hub shutdown did not cancel pending authorization")
	}
	select {
	case <-shutdownDone:
	case <-time.After(testIOTimeout):
		t.Fatal("hub shutdown did not finish after canceling authorization")
	}
}

func TestHub_SlowSubscribeAuthorizationDoesNotBlockBroadcast(t *testing.T) {
	auth := newBlockingTargetAuthorizer("slow-room")
	hub := NewHub(auth, newTestLogger(), NopBus{}, "slow-subscribe-broadcast")
	defer hub.Shutdown()
	defer auth.unblock()

	clientB := newClient("client-b", "user-b", "workspace", &fakeSender{})
	registerInRunningHub(t, hub, clientB)
	if err := hub.Subscribe(context.Background(), clientB, TargetTypeChannel, "ready-room"); err != nil {
		t.Fatalf("subscribe client B: %v", err)
	}

	clientA := newClient("client-a", "user-a", "workspace", &fakeSender{})
	registerInRunningHub(t, hub, clientA)
	subscribeResult := make(chan error, 1)
	go func() {
		subscribeResult <- hub.Subscribe(context.Background(), clientA, TargetTypeChannel, "slow-room")
	}()
	select {
	case <-auth.started:
	case <-time.After(testIOTimeout):
		t.Fatal("slow authorizer did not start")
	}

	hub.PublishMessageCreated(
		context.Background(),
		"workspace",
		TargetTypeChannel,
		"ready-room",
		MessagePayload{ID: "message-for-b"},
	)
	select {
	case data := <-clientB.outbox:
		var event Event
		if err := json.Unmarshal(data, &event); err != nil {
			t.Fatalf("decode broadcast for client B: %v", err)
		}
		if event.TargetID != "ready-room" || event.MessageID != "message-for-b" {
			t.Fatalf("unexpected broadcast for client B: %+v", event)
		}
	case <-time.After(testIOTimeout):
		t.Fatal("broadcast was blocked by another client's authorization")
	}

	auth.unblock()
	select {
	case err := <-subscribeResult:
		if err != nil {
			t.Fatalf("client A subscribe failed after release: %v", err)
		}
	case <-time.After(testIOTimeout):
		t.Fatal("client A subscribe did not finish after release")
	}
}

func TestHub_SlowBroadcastAuthorizationDoesNotBlockCoordinator(t *testing.T) {
	auth := newBlockingTargetAuthorizer("broadcast-room")
	hub := NewHub(auth, newTestLogger(), NopBus{}, "slow-broadcast-auth")
	defer hub.Shutdown()
	defer auth.unblock()

	recipient := newClient("recipient", "recipient-user", "workspace", &fakeSender{})
	registerInRunningHub(t, hub, recipient)
	if err := hub.handleSubscribe(subscribeReq{
		ctx:        context.Background(),
		client:     recipient,
		targetType: TargetTypeChannel,
		targetID:   "broadcast-room",
		resp:       make(chan error, 1),
	}); err != nil {
		t.Fatalf("apply pre-authorized recipient subscription: %v", err)
	}

	hub.PublishMessageCreated(
		context.Background(),
		"workspace",
		TargetTypeChannel,
		"broadcast-room",
		MessagePayload{ID: "message-during-slow-auth"},
	)
	select {
	case <-auth.started:
	case <-time.After(testIOTimeout):
		t.Fatal("broadcast authorizer did not start")
	}

	newcomer := newClient("newcomer", "new-user", "workspace", &fakeSender{})
	registerResult := make(chan bool, 1)
	go func() { registerResult <- hub.Register(newcomer) }()
	select {
	case registered := <-registerResult:
		if !registered {
			t.Fatal("hub rejected newcomer during broadcast authorization")
		}
	case <-time.After(testIOTimeout):
		t.Fatal("coordinator was blocked by broadcast authorization")
	}

	auth.unblock()
	select {
	case <-recipient.outbox:
	case <-time.After(testIOTimeout):
		t.Fatal("recipient did not receive broadcast after authorization release")
	}
}

func TestHub_BroadcastDoesNotDeliverAfterCompletedUnsubscribe(t *testing.T) {
	auth := newBlockingTargetAuthorizer("room")
	hub := newRunningTestHub(t, auth)
	defer auth.unblock()

	client := newClient("client", "user", "workspace", &fakeSender{})
	registerInRunningHub(t, hub, client)
	mustSubscribe(t, hub, client, TargetTypeChannel, "room")

	evt, data := makeEvent("workspace", TargetTypeChannel, "room", "message")
	done := enqueueBroadcastForTest(t, hub, evt, data)
	select {
	case <-auth.started:
	case <-time.After(testIOTimeout):
		t.Fatal("broadcast authorization did not start")
	}

	err := hub.handleClientMessage(context.Background(), client, ClientMessage{
		Type: ClientMessageTypeUnsubscribe, TargetType: TargetTypeChannel, TargetID: "room",
	})
	if err != nil {
		t.Fatalf("unsubscribe: %v", err)
	}
	key := targetKey{workspaceID: "workspace", targetType: TargetTypeChannel, targetID: "room"}.String()
	if hubHasSubscription(hub, key, client.id) || hubHasClientSubscription(hub, client.id, key) {
		t.Fatal("unsubscribe returned before removing both subscription indexes")
	}

	auth.unblock()
	waitForBroadcast(t, done)
	if got := len(client.outbox); got != 0 {
		t.Fatalf("broadcast delivered %d event(s) after unsubscribe completed", got)
	}
	if hubHasSubscription(hub, key, client.id) || hubHasClientSubscription(hub, client.id, key) {
		t.Fatal("late authorization restored an unsubscribed room")
	}
}

func TestHub_BroadcastDoesNotDeliverAfterCompletedRevocation(t *testing.T) {
	auth := newBlockingTargetAuthorizer("room")
	hub := newRunningTestHub(t, auth)
	defer auth.unblock()

	client := newClient("client", "user", "workspace", &fakeSender{})
	registerInRunningHub(t, hub, client)
	mustSubscribe(t, hub, client, TargetTypeChannel, "room")
	key := targetKey{workspaceID: "workspace", targetType: TargetTypeChannel, targetID: "room"}.String()

	evt, data := makeEvent("workspace", TargetTypeChannel, "room", "message")
	done := enqueueBroadcastForTest(t, hub, evt, data)
	select {
	case <-auth.started:
	case <-time.After(testIOTimeout):
		t.Fatal("broadcast authorization did not start")
	}

	if err := hub.RevokeSubscription(context.Background(), client, key); err != nil {
		t.Fatalf("revoke subscription: %v", err)
	}
	auth.unblock()
	waitForBroadcast(t, done)

	if got := len(client.outbox); got != 0 {
		t.Fatalf("broadcast delivered %d event(s) after revocation completed", got)
	}
	if hubHasSubscription(hub, key, client.id) || hubHasClientSubscription(hub, client.id, key) {
		t.Fatal("late authorization restored a revoked room")
	}
}

func TestHub_BroadcastDoesNotDeliverAfterUnregister(t *testing.T) {
	auth := newBlockingTargetAuthorizer("room")
	hub := newRunningTestHub(t, auth)
	defer auth.unblock()

	client := newClient("client", "user", "workspace", &fakeSender{})
	registerInRunningHub(t, hub, client)
	mustSubscribe(t, hub, client, TargetTypeChannel, "room")
	key := targetKey{workspaceID: "workspace", targetType: TargetTypeChannel, targetID: "room"}.String()

	evt, data := makeEvent("workspace", TargetTypeChannel, "room", "message")
	done := enqueueBroadcastForTest(t, hub, evt, data)
	select {
	case <-auth.started:
	case <-time.After(testIOTimeout):
		t.Fatal("broadcast authorization did not start")
	}

	hub.Unregister(client)
	eventually(t, func() bool { return !hubHasClient(hub, client.id) }, testIOTimeout,
		"unregister during broadcast authorization")
	auth.unblock()
	waitForBroadcast(t, done)

	if got := len(client.outbox); got != 0 {
		t.Fatalf("broadcast delivered %d event(s) to unregistered client", got)
	}
	if hubHasClient(hub, client.id) || hubHasSubscriptionTarget(hub, key) || hubHasClientSubs(hub, client.id) {
		t.Fatal("late authorization restored unregistered client state")
	}
}

func TestHub_BroadcastFromRemovedClientDoesNotReachReplacement(t *testing.T) {
	auth := newBlockingTargetAuthorizer("room")
	hub := newRunningTestHub(t, auth)
	defer auth.unblock()

	oldClient := newClient("shared-id", "old-user", "workspace", &fakeSender{})
	registerInRunningHub(t, hub, oldClient)
	mustSubscribe(t, hub, oldClient, TargetTypeChannel, "room")

	evt, data := makeEvent("workspace", TargetTypeChannel, "room", "message")
	done := enqueueBroadcastForTest(t, hub, evt, data)
	select {
	case <-auth.started:
	case <-time.After(testIOTimeout):
		t.Fatal("broadcast authorization did not start")
	}

	hub.Unregister(oldClient)
	eventually(t, func() bool { return !hubHasClient(hub, oldClient.id) }, testIOTimeout,
		"old client unregister during broadcast authorization")
	replacement := newClient(oldClient.id, "new-user", "workspace", &fakeSender{})
	registerInRunningHub(t, hub, replacement)

	auth.unblock()
	waitForBroadcast(t, done)

	if got := len(oldClient.outbox); got != 0 {
		t.Fatalf("old client received %d stale event(s)", got)
	}
	if got := len(replacement.outbox); got != 0 {
		t.Fatalf("replacement client received %d event(s) from old authorization", got)
	}
	if got := hubGetClient(hub, replacement.id); got != replacement {
		t.Fatal("late broadcast changed the replacement connection")
	}
}

func TestHub_BroadcastGenerationDoesNotCrossUnsubscribeResubscribe(t *testing.T) {
	auth := newBlockingTargetAuthorizer("room")
	hub := newRunningTestHub(t, auth)
	defer auth.unblock()

	client := newClient("client", "user", "workspace", &fakeSender{})
	registerInRunningHub(t, hub, client)
	mustSubscribe(t, hub, client, TargetTypeChannel, "room")
	key := targetKey{workspaceID: "workspace", targetType: TargetTypeChannel, targetID: "room"}.String()

	evt, data := makeEvent("workspace", TargetTypeChannel, "room", "message")
	done := enqueueBroadcastForTest(t, hub, evt, data)
	select {
	case <-auth.started:
	case <-time.After(testIOTimeout):
		t.Fatal("broadcast authorization did not start")
	}

	if err := hub.RevokeSubscription(context.Background(), client, key); err != nil {
		t.Fatalf("revoke old generation: %v", err)
	}
	mustSubscribe(t, hub, client, TargetTypeChannel, "room")
	auth.unblock()
	waitForBroadcast(t, done)

	if got := len(client.outbox); got != 0 {
		t.Fatalf("old broadcast generation delivered %d event(s) to new subscription", got)
	}
	if !hubHasSubscription(hub, key, client.id) || !hubHasClientSubscription(hub, client.id, key) {
		t.Fatal("new subscription generation should remain active")
	}
}

func TestHub_SlowBroadcastAuthorizationDoesNotBlockAnotherBroadcast(t *testing.T) {
	auth := newBlockingTargetAuthorizer("slow-room")
	hub := newRunningTestHub(t, auth)
	defer auth.unblock()
	slowKey := broadcastTargetKey(Event{WorkspaceID: "workspace", TargetType: TargetTypeChannel, TargetID: "slow-room"})
	readyRoom := ""
	for candidate := 0; candidate < 100; candidate++ {
		targetID := fmt.Sprintf("ready-room-%d", candidate)
		readyKey := broadcastTargetKey(Event{WorkspaceID: "workspace", TargetType: TargetTypeChannel, TargetID: targetID})
		if broadcastPartition(slowKey, broadcastWorkerCount) != broadcastPartition(readyKey, broadcastWorkerCount) {
			readyRoom = targetID
			break
		}
	}
	if readyRoom == "" {
		t.Fatal("could not find a target in a different broadcast partition")
	}

	slowClient := newClient("slow-client", "slow-user", "workspace", &fakeSender{})
	readyClient := newClient("ready-client", "ready-user", "workspace", &fakeSender{})
	registerInRunningHub(t, hub, slowClient)
	registerInRunningHub(t, hub, readyClient)
	mustSubscribe(t, hub, slowClient, TargetTypeChannel, "slow-room")
	mustSubscribe(t, hub, readyClient, TargetTypeChannel, readyRoom)

	slowEvent, slowData := makeEvent("workspace", TargetTypeChannel, "slow-room", "slow-message")
	slowDone := enqueueBroadcastForTest(t, hub, slowEvent, slowData)
	select {
	case <-auth.started:
	case <-time.After(testIOTimeout):
		t.Fatal("slow broadcast authorization did not start")
	}

	readyEvent, readyData := makeEvent("workspace", TargetTypeChannel, readyRoom, "ready-message")
	readyDone := enqueueBroadcastForTest(t, hub, readyEvent, readyData)
	select {
	case data := <-readyClient.outbox:
		var event Event
		if err := json.Unmarshal(data, &event); err != nil {
			t.Fatalf("decode ready event: %v", err)
		}
		if event.MessageID != "ready-message" {
			t.Fatalf("ready client received unexpected event: %+v", event)
		}
	case <-time.After(testIOTimeout):
		t.Fatal("another broadcast was blocked by slow authorization")
	}
	waitForBroadcast(t, readyDone)

	auth.unblock()
	waitForBroadcast(t, slowDone)
}

func TestHub_BroadcastsForSameTargetPreserveDispatcherOrder(t *testing.T) {
	auth := newOrderedBroadcastAuthorizer()
	hub := newRunningTestHub(t, auth)
	t.Cleanup(auth.release)

	client := newClient("client", "user", "workspace", &fakeSender{})
	registerInRunningHub(t, hub, client)
	mustSubscribe(t, hub, client, TargetTypeChannel, "ordered-room")

	firstEvent, firstData := makeEvent("workspace", TargetTypeChannel, "ordered-room", "message-1")
	firstDone := enqueueBroadcastForTest(t, hub, firstEvent, firstData)
	select {
	case <-auth.firstStarted:
	case <-time.After(testIOTimeout):
		t.Fatal("first event authorization did not start")
	}

	secondEvent, secondData := makeEvent("workspace", TargetTypeChannel, "ordered-room", "message-2")
	secondDone := enqueueBroadcastForTest(t, hub, secondEvent, secondData)
	thirdEvent, thirdData := makeEvent("workspace", TargetTypeChannel, "ordered-room", "message-3")
	thirdDone := enqueueBroadcastForTest(t, hub, thirdEvent, thirdData)

	select {
	case <-auth.secondStarted:
		t.Fatal("second event authorization started before the first event completed")
	case <-time.After(100 * time.Millisecond):
	}
	if got := len(client.outbox); got != 0 {
		t.Fatalf("delivered %d event(s) while the first event was still blocked", got)
	}

	auth.release()
	waitForBroadcast(t, firstDone)
	select {
	case <-auth.secondStarted:
	case <-time.After(testIOTimeout):
		t.Fatal("second event authorization did not start after the first completed")
	}
	waitForBroadcast(t, secondDone)
	waitForBroadcast(t, thirdDone)

	messageIDs := make([]string, 0, 3)
	for range 3 {
		select {
		case data := <-client.outbox:
			var event Event
			if err := json.Unmarshal(data, &event); err != nil {
				t.Fatalf("decode delivered event: %v", err)
			}
			messageIDs = append(messageIDs, event.MessageID)
		case <-time.After(testIOTimeout):
			t.Fatal("timed out waiting for ordered delivery")
		}
	}
	if fmt.Sprint(messageIDs) != "[message-1 message-2 message-3]" {
		t.Fatalf("delivery order = %v, want [message-1 message-2 message-3]", messageIDs)
	}
}

func TestBroadcastPartitionUsesCompleteStableTargetKey(t *testing.T) {
	channelEvent := Event{WorkspaceID: "workspace", TargetType: TargetTypeChannel, TargetID: "same-id"}
	dmEvent := Event{WorkspaceID: "workspace", TargetType: TargetTypeDM, TargetID: "same-id"}
	channelKey := broadcastTargetKey(channelEvent)

	if channelKey == broadcastTargetKey(dmEvent) {
		t.Fatal("channel and DM with the same textual ID must have distinct target keys")
	}
	if first, second := broadcastPartition(channelKey, broadcastWorkerCount), broadcastPartition(channelKey, broadcastWorkerCount); first != second {
		t.Fatalf("same target partition changed from %d to %d", first, second)
	}
}

func TestBroadcastPartitionReturnsStableIndexWithinRange(t *testing.T) {
	const key = "workspace\x00channel\x00room"

	for partitionCount := 1; partitionCount <= 16; partitionCount++ {
		first := broadcastPartition(key, partitionCount)
		second := broadcastPartition(key, partitionCount)
		if first < 0 || first >= partitionCount {
			t.Fatalf("broadcastPartition(%q, %d) = %d, want index in [0, %d)", key, partitionCount, first, partitionCount)
		}
		if second != first {
			t.Fatalf("broadcastPartition(%q, %d) changed from %d to %d", key, partitionCount, first, second)
		}
	}

	if got := broadcastPartition(key, 1); got != 0 {
		t.Fatalf("broadcastPartition(%q, 1) = %d, want 0", key, got)
	}
}

func TestBroadcastPartitionRejectsInvalidPartitionCount(t *testing.T) {
	invalidCounts := []int{0, -1}
	if strconv.IntSize > 32 {
		invalidCounts = append(invalidCounts, int(math.MaxInt32)*2+2)
	}

	const wantPanic = "ws: broadcast partition count must be between 1 and 4294967295"
	for _, partitionCount := range invalidCounts {
		t.Run(fmt.Sprintf("partition_count_%d", partitionCount), func(t *testing.T) {
			var recovered any
			func() {
				defer func() {
					recovered = recover()
				}()
				broadcastPartition("key", partitionCount)
			}()

			if recovered != wantPanic {
				t.Fatalf("panic = %v, want %q", recovered, wantPanic)
			}
		})
	}
}

func TestHub_SlowSubscribeAuthorizationDoesNotBlockAnotherSubscribe(t *testing.T) {
	auth := newBlockingTargetAuthorizer("slow-room")
	hub := NewHub(auth, newTestLogger(), NopBus{}, "slow-subscribe-other-subscribe")
	defer hub.Shutdown()
	defer auth.unblock()

	clientA := newClient("client-a", "user-a", "workspace", &fakeSender{})
	clientB := newClient("client-b", "user-b", "workspace", &fakeSender{})
	registerInRunningHub(t, hub, clientA)
	registerInRunningHub(t, hub, clientB)
	resultA := make(chan error, 1)
	go func() {
		resultA <- hub.Subscribe(context.Background(), clientA, TargetTypeChannel, "slow-room")
	}()
	select {
	case <-auth.started:
	case <-time.After(testIOTimeout):
		t.Fatal("slow authorizer did not start")
	}

	resultB := make(chan error, 1)
	go func() {
		resultB <- hub.Subscribe(context.Background(), clientB, TargetTypeChannel, "ready-room")
	}()
	select {
	case err := <-resultB:
		if err != nil {
			t.Fatalf("client B subscribe failed: %v", err)
		}
	case <-time.After(testIOTimeout):
		t.Fatal("client B subscribe was blocked by client A authorization")
	}

	auth.unblock()
	select {
	case err := <-resultA:
		if err != nil {
			t.Fatalf("client A subscribe failed after release: %v", err)
		}
	case <-time.After(testIOTimeout):
		t.Fatal("client A subscribe did not finish after release")
	}
}

func TestHub_UnregisterIsProcessedDuringPendingAuthorization(t *testing.T) {
	auth := newBlockingTargetAuthorizer("slow-room")
	hub := NewHub(auth, newTestLogger(), NopBus{}, "unregister-during-subscribe")
	defer hub.Shutdown()
	defer auth.unblock()

	client := newClient("client-a", "user-a", "workspace", &fakeSender{})
	registerInRunningHub(t, hub, client)
	result := make(chan error, 1)
	go func() {
		result <- hub.Subscribe(context.Background(), client, TargetTypeChannel, "slow-room")
	}()
	select {
	case <-auth.started:
	case <-time.After(testIOTimeout):
		t.Fatal("slow authorizer did not start")
	}

	hub.Unregister(client)
	eventually(t, func() bool { return !hubHasClient(hub, client.id) }, testIOTimeout, "real unregister during authorization")
	if hubHasClientSubs(hub, client.id) {
		t.Fatal("unregister must remove clientSubs before authorization finishes")
	}

	select {
	case err := <-result:
		if err == nil {
			t.Fatal("late authorization result must not register removed client")
		}
	case <-time.After(testIOTimeout):
		t.Fatal("pending authorization did not stop after unregister closed the client")
	}

	key := targetKey{workspaceID: "workspace", targetType: TargetTypeChannel, targetID: "slow-room"}.String()
	if hubHasSubscriptionTarget(hub, key) {
		t.Fatal("late authorization result created a room for removed client")
	}
}

func TestHub_LateDenialFromRemovedClientDoesNotRevokeReplacement(t *testing.T) {
	auth := &blockingDenyAuthorizer{started: make(chan struct{}), release: make(chan struct{})}
	hub := newRunningTestHub(t, auth)
	oldClient := newClient("shared-client-id", "old-user", "workspace", &fakeSender{})
	registerInRunningHub(t, hub, oldClient)

	result := make(chan error, 1)
	go func() {
		result <- hub.Subscribe(context.Background(), oldClient, TargetTypeChannel, "room")
	}()

	select {
	case <-auth.started:
	case <-time.After(testIOTimeout):
		t.Fatal("old client authorization did not start")
	}

	hub.Unregister(oldClient)
	eventually(t, func() bool { return !hubHasClient(hub, oldClient.id) }, testIOTimeout,
		"old client unregister should be processed")

	replacement := newClient(oldClient.id, "new-user", "workspace", &fakeSender{})
	registerInRunningHub(t, hub, replacement)
	key := targetKey{workspaceID: replacement.workspaceID, targetType: TargetTypeChannel, targetID: "room"}.String()
	mustSubscribe(t, hub, replacement, TargetTypeChannel, "room")

	close(auth.release)
	select {
	case err := <-result:
		if !errors.Is(err, ErrSubscribeForbidden) {
			t.Fatalf("late authorization result = %v, want forbidden", err)
		}
	case <-time.After(testIOTimeout):
		t.Fatal("late denial did not finish")
	}

	if got := hubGetClient(hub, replacement.id); got != replacement {
		t.Fatal("late result changed the replacement client")
	}
	if !hubHasSubscription(hub, key, replacement.id) {
		t.Fatal("late denial from old connection revoked replacement subscription")
	}
}

func TestHub_Register_DuplicateClientID_DropsNewClient(t *testing.T) {
	hub := NewHub(&fakeAuthorizer{}, newTestLogger(), NopBus{}, "test-inst")
	defer hub.Shutdown()

	originalSender := &fakeSender{}
	duplicateSender := &fakeSender{}
	original := newClient("c1", "user-1", "ws-1", originalSender)
	duplicate := newClient("c1", "user-2", "ws-1", duplicateSender)

	if !hub.Register(original) {
		t.Fatal("original client registration should succeed")
	}
	if hub.Register(duplicate) {
		t.Fatal("duplicate client registration should report failure")
	}

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

func TestHub_RegisterAfterShutdown_ReturnsFalse(t *testing.T) {
	hub := NewHub(&fakeAuthorizer{}, newTestLogger(), NopBus{}, "test-inst")
	hub.Shutdown()

	snd := &fakeSender{}
	c := newClient("after-shutdown", "user-1", "ws-1", snd)

	if hub.Register(c) {
		t.Fatal("registration must fail when hub is already shut down")
	}
	if hubHasClient(hub, "after-shutdown") {
		t.Fatal("client must not be tracked after failed registration")
	}
	if snd.isClosed() {
		t.Fatal("Hub.Register should only report failure; connection cleanup belongs to caller")
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

	h := newRunningTestHub(t, auth)
	c := newClient("c1", "user-1", "ws-1", &fakeSender{})
	registerInRunningHub(t, h, c)

	err := h.Subscribe(context.Background(), c, TargetTypeChannel, "ch-private")
	if !errors.Is(err, ErrSubscribeForbidden) {
		t.Fatalf("expected ErrSubscribeForbidden, got: %v", err)
	}
	key := targetKey{workspaceID: "ws-1", targetType: TargetTypeChannel, targetID: "ch-private"}.String()
	if hubHasSubscriptionTarget(h, key) {
		t.Fatal("denied subscribe must not create a room subscription")
	}
}

func TestHub_Subscribe_AuthorizerErrorFailsClosed(t *testing.T) {
	auth := &fakeAuthorizer{}
	auth.setErr("user-1", "ws-1", TargetTypeChannel, "ch-private", errors.New("database unavailable"))

	h := newRunningTestHub(t, auth)
	c := newClient("c1", "user-1", "ws-1", &fakeSender{})
	registerInRunningHub(t, h, c)

	err := h.Subscribe(context.Background(), c, TargetTypeChannel, "ch-private")
	if err == nil {
		t.Fatal("authorization storage error must fail closed")
	}
	key := targetKey{workspaceID: "ws-1", targetType: TargetTypeChannel, targetID: "ch-private"}.String()
	if hubHasSubscriptionTarget(h, key) {
		t.Fatal("authorization storage error must not create a room subscription")
	}
}

func TestHub_Subscribe_RepeatedAuthorizedJoinIsIdempotent(t *testing.T) {
	auth := &fakeAuthorizer{}
	auth.setAccess("user-1", "ws-1", TargetTypeChannel, "ch-1", true)

	h := newTestHub(auth)
	c := newClient("c1", "user-1", "ws-1", &fakeSender{})
	registerInHub(t, h, c)

	mustSubscribe(t, h, c, TargetTypeChannel, "ch-1")
	mustSubscribe(t, h, c, TargetTypeChannel, "ch-1")

	key := targetKey{workspaceID: "ws-1", targetType: TargetTypeChannel, targetID: "ch-1"}.String()
	h.mu.RLock()
	subscriberCount := len(h.subs[key])
	clientRoomCount := len(h.clientSubs[c.id])
	h.mu.RUnlock()
	if subscriberCount != 1 || clientRoomCount != 1 {
		t.Fatalf("repeated subscribe must remain idempotent: subscribers=%d client rooms=%d", subscriberCount, clientRoomCount)
	}
}

func TestHub_Subscribe_ReauthorizationDeniedRevokesExistingRoom(t *testing.T) {
	auth := &fakeAuthorizer{}
	auth.setAccess("user-1", "ws-1", TargetTypeChannel, "ch-1", true)

	h := newRunningTestHub(t, auth)
	c := newClient("c1", "user-1", "ws-1", &fakeSender{})
	registerInRunningHub(t, h, c)
	if err := h.Subscribe(context.Background(), c, TargetTypeChannel, "ch-1"); err != nil {
		t.Fatalf("initial subscribe: %v", err)
	}

	auth.setAccess("user-1", "ws-1", TargetTypeChannel, "ch-1", false)
	err := h.Subscribe(context.Background(), c, TargetTypeChannel, "ch-1")
	if !errors.Is(err, ErrSubscribeForbidden) {
		t.Fatalf("removed member must be denied on resubscribe, got: %v", err)
	}

	key := targetKey{workspaceID: "ws-1", targetType: TargetTypeChannel, targetID: "ch-1"}.String()
	if hubHasSubscriptionTarget(h, key) {
		t.Fatal("denied reauthorization must revoke the existing room subscription")
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
	h := newRunningTestHub(t, &fakeAuthorizer{})
	c := newClient("c1", "user-1", "ws-1", &fakeSender{})
	registerInRunningHub(t, h, c)

	if err := h.Subscribe(context.Background(), c, TargetTypeDM, "dm-1"); !errors.Is(err, ErrSubscribeForbidden) {
		t.Fatalf("expected ErrSubscribeForbidden for DM non-member, got: %v", err)
	}
}

func TestHub_Subscribe_PrivateChannel_NonMember_Denied_NonEnumerating(t *testing.T) {
	// Private channel non-member must get ErrSubscribeForbidden, not ErrNotFound,
	// so callers cannot enumerate whether the channel exists.
	h := newRunningTestHub(t, &fakeAuthorizer{})
	c := newClient("c1", "user-1", "ws-1", &fakeSender{})
	registerInRunningHub(t, h, c)

	err := h.Subscribe(context.Background(), c, TargetTypeChannel, "ch-private")
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

	h := newRunningTestHub(t, auth)
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

	h := newRunningTestHub(t, auth)
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

	h := newRunningTestHub(t, auth)
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

	h := newRunningTestHub(t, auth)
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
	h := newRunningTestHub(t, &fakeAuthorizer{})
	evt, data := makeEvent("ws-1", TargetTypeChannel, "ch-1", "msg-1")
	// Must not panic or error.
	h.handleBroadcast(broadcastReq{event: evt, data: data})
}

func TestHub_Broadcast_SlowClient_DropsConnection(t *testing.T) {
	auth := &fakeAuthorizer{}
	auth.setAccess("user-1", "ws-1", TargetTypeChannel, "ch-1", true)

	h := newRunningTestHub(t, auth)
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

	eventually(t, func() bool { return !hubHasClient(h, "c1") }, testIOTimeout,
		"slow client should be removed from hub after overflow")
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

	h := newRunningTestHub(t, auth)
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

	h := newRunningTestHub(t, auth)

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

func TestHub_MessageUpdated_DeliveredOnlyToAuthorizedChannelMembers(t *testing.T) {
	auth := &fakeAuthorizer{}
	auth.setAccess("member", "ws-1", TargetTypeChannel, "ch-1", true)
	auth.setAccess("removed", "ws-1", TargetTypeChannel, "ch-1", true)
	hub := newRunningTestHub(t, auth)

	member := newClient("member-client", "member", "ws-1", &fakeSender{})
	removed := newClient("removed-client", "removed", "ws-1", &fakeSender{})
	registerInHub(t, hub, member)
	registerInHub(t, hub, removed)
	mustSubscribe(t, hub, member, TargetTypeChannel, "ch-1")
	mustSubscribe(t, hub, removed, TargetTypeChannel, "ch-1")
	auth.setAccess("removed", "ws-1", TargetTypeChannel, "ch-1", false)

	event := Event{
		Type: EventTypeMessageUpdated, WorkspaceID: "ws-1", TargetType: TargetTypeChannel,
		TargetID: "ch-1", MessageID: "msg-1",
		MessageUpdate: &MessageUpdatedPayload{MessageID: "msg-1", ChannelID: "ch-1", Body: "edited", BodyFormat: "v1", EditCount: 1, IsEdited: true},
	}
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal message.updated: %v", err)
	}
	hub.handleBroadcast(broadcastReq{event: event, data: data})

	if len(member.outbox) != 1 || len(removed.outbox) != 0 {
		t.Fatalf("message.updated delivery member=%d removed=%d", len(member.outbox), len(removed.outbox))
	}
}

func TestHub_PublishMessageUpdated_DeliversCompletePayloadOnlyToAuthorizedSubscribers(t *testing.T) {
	for _, tt := range []struct {
		name       string
		targetType TargetType
		targetID   string
		otherID    string
	}{
		{name: "channel", targetType: TargetTypeChannel, targetID: "ch-1", otherID: "ch-2"},
		{name: "dm", targetType: TargetTypeDM, targetID: "dm-1", otherID: "dm-2"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			auth := &fakeAuthorizer{}
			auth.setAccess("member", "ws-1", tt.targetType, tt.targetID, true)
			auth.setAccess("other", "ws-1", tt.targetType, tt.otherID, true)
			hub := NewHub(auth, newTestLogger(), NopBus{}, "test-message-update-"+tt.name)
			t.Cleanup(hub.Shutdown)

			member := newClient("member-"+tt.name, "member", "ws-1", &fakeSender{})
			other := newClient("other-"+tt.name, "other", "ws-1", &fakeSender{})
			outsider := newClient("outsider-"+tt.name, "outsider", "ws-1", &fakeSender{})
			registerInRunningHub(t, hub, member)
			registerInRunningHub(t, hub, other)
			registerInRunningHub(t, hub, outsider)
			if err := hub.Subscribe(context.Background(), member, tt.targetType, tt.targetID); err != nil {
				t.Fatalf("member subscribe: %v", err)
			}
			if err := hub.Subscribe(context.Background(), other, tt.targetType, tt.otherID); err != nil {
				t.Fatalf("other target subscribe: %v", err)
			}
			if err := hub.Subscribe(context.Background(), outsider, tt.targetType, tt.targetID); !errors.Is(err, ErrSubscribeForbidden) {
				t.Fatalf("outsider subscribe: expected forbidden, got %v", err)
			}

			editedAt := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
			payload := MessageUpdatedPayload{
				MessageID: "msg-1", Body: "edited", BodyFormat: "v3",
				EditedAt: editedAt, EditCount: 2, IsEdited: true,
			}
			if tt.targetType == TargetTypeChannel {
				payload.ChannelID = tt.targetID
			} else {
				payload.DMID = tt.targetID
			}
			hub.PublishMessageUpdated(context.Background(), "ws-1", tt.targetType, tt.targetID, payload)

			select {
			case raw := <-member.outbox:
				event, err := decodeEvent(raw)
				if err != nil {
					t.Fatalf("decode message.updated: %v", err)
				}
				if event.Type != EventTypeMessageUpdated || event.TargetType != tt.targetType || event.TargetID != tt.targetID || event.MessageUpdate == nil {
					t.Fatalf("unexpected event route: %+v", event)
				}
				got := event.MessageUpdate
				if got.MessageID != "msg-1" || got.ChannelID != payload.ChannelID || got.DMID != payload.DMID || got.Body != "edited" || got.BodyFormat != "v3" || !got.EditedAt.Equal(editedAt) || got.EditCount != 2 || !got.IsEdited {
					t.Fatalf("incomplete message.updated payload: %+v", got)
				}
			case <-time.After(time.Second):
				t.Fatal("authorized subscriber did not receive message.updated")
			}
			if len(other.outbox) != 0 || len(outsider.outbox) != 0 {
				t.Fatalf("unexpected delivery: other=%d outsider=%d", len(other.outbox), len(outsider.outbox))
			}
		})
	}
}

func TestHub_PublishMessageUpdated_DeliversLocallyWhenBusFails(t *testing.T) {
	auth := &fakeAuthorizer{}
	auth.setAccess("member", testWorkspaceID, TargetTypeChannel, testChannelID, true)
	hub := newBusTestHub(auth, &fakeBus{publishErr: errors.New("valkey unavailable")})
	t.Cleanup(hub.Shutdown)
	member := newClient("message-update-member", "member", testWorkspaceID, &fakeSender{})
	registerInRunningHub(t, hub, member)
	if err := hub.Subscribe(context.Background(), member, TargetTypeChannel, testChannelID); err != nil {
		t.Fatalf("member subscribe: %v", err)
	}

	hub.PublishMessageUpdated(context.Background(), testWorkspaceID, TargetTypeChannel, testChannelID, MessageUpdatedPayload{
		MessageID: testMessageID, ChannelID: testChannelID, Body: "edited",
		BodyFormat: "v1", EditedAt: time.Now().UTC(), EditCount: 1, IsEdited: true,
	})

	if !waitForOutbox(member, 1) {
		t.Fatal("local subscriber did not receive message.updated after bus failure")
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

	h := newRunningTestHub(t, auth)
	c := newClient("c1", "user-1", "ws-2", &fakeSender{}) // client is in ws-2
	registerInRunningHub(t, h, c)

	err := h.Subscribe(context.Background(), c, TargetTypeChannel, "ch-1")
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
		registerInRunningHub(t, hub, c) // synchronous: client is in hub state when this returns
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
			registerInRunningHub(t, hub, c) // synchronous: safe to Subscribe immediately after
			_ = hub.Subscribe(context.Background(), c, TargetTypeChannel, "ch-1")
			hub.PublishMessageCreated(context.Background(), "ws-1", TargetTypeChannel, "ch-1", MessagePayload{
				ID: fmt.Sprintf("44444444-0000-0000-0000-%012d", n), WorkspaceID: "ws-1",
				ChannelID: "ch-1", SenderID: "user-1", Kind: "user", Status: "active",
				CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
			})
			hub.Unregister(c)
		}(i)
	}
	wg.Wait()
}

func TestHub_ConcurrentBroadcastAndUnregister_NoRace(t *testing.T) {
	// This test validates the map locking contract when run with -race.
	auth := &fakeAuthorizer{}
	auth.setAccess("user-1", "ws-1", TargetTypeChannel, "ch-1", true)

	h := newRunningTestHub(t, auth)
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
	registerInRunningHub(t, hub, c1)
	registerInRunningHub(t, hub, c2)

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

	h := newRunningTestHub(t, auth)
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

	h := newRunningTestHub(t, auth)
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

	h := newRunningTestHub(t, auth)
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

func TestHub_Subscribe_DuringShutdown_ReturnsError(t *testing.T) {
	// Verifies that Subscribe does not block indefinitely when Shutdown has
	// closed h.quit — the second select (waiting for resp) must observe <-h.quit
	// and return ErrHubShutdown rather than blocking forever.
	auth := &fakeAuthorizer{}
	auth.setAccess("user-1", "ws-1", TargetTypeChannel, "ch-1", true)
	hub := NewHub(auth, newTestLogger(), NopBus{}, "test-inst")

	c := newClient("c-shutdown", "user-1", "ws-1", &fakeSender{})
	registerInRunningHub(t, hub, c)

	// Shut down the hub so h.quit is closed.
	hub.Shutdown()

	// Subscribe must return promptly — both selects observe <-h.quit.
	// Use background context so any block is detectable via the timeout below.
	errCh := make(chan error, 1)
	go func() {
		errCh <- hub.Subscribe(context.Background(), c, TargetTypeChannel, "ch-1")
	}()

	select {
	case err := <-errCh:
		if !errors.Is(err, ErrHubShutdown) {
			t.Fatalf("Subscribe after Shutdown must return ErrHubShutdown, got: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Subscribe blocked indefinitely after Shutdown — <-h.quit missing in select")
	}
}
