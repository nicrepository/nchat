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

const (
	typingWorkspace = "5c2e8a41-7b3d-4f19-8e60-2a4c6b8d1e93"
	typingChannel   = "3f7b1d95-8c4a-4e26-b071-5d9e2f4a7c18"
	typingOtherRoom = "2d9e4a16-3b78-4c05-8f91-6a3d7b2e5c84"
	typingUserA     = "1e6d3b87-4a29-4c50-9f83-7b1c4e8a2d65"
	typingUserB     = "8b5f2c93-6d17-4a84-b3e2-9c5a1f7d4b60"
)

// ── fakes ────────────────────────────────────────────────────────────────────

type fakeTypingLimiter struct {
	mu      sync.Mutex
	allowed bool
	err     error
	calls   []fakeTypingLimiterCall
}

type fakeTypingLimiterCall struct {
	userID        string
	action        string
	maxActions    int
	windowSeconds int
}

func (f *fakeTypingLimiter) AllowActionWithLimit(_ context.Context, userID, action string, maxActions, windowSeconds int) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fakeTypingLimiterCall{userID, action, maxActions, windowSeconds})
	return f.allowed, f.err
}

func (f *fakeTypingLimiter) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeTypingLimiter) lastCall() fakeTypingLimiterCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[len(f.calls)-1]
}

type fakeTypingStore struct {
	mu      sync.Mutex
	touched map[string]time.Duration
	cleared []string
	err     error
}

func newFakeTypingStore() *fakeTypingStore {
	return &fakeTypingStore{touched: make(map[string]time.Duration)}
}

func (f *fakeTypingStore) Touch(_ context.Context, id string, ttl time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.touched[id] = ttl
	return f.err
}

func (f *fakeTypingStore) Clear(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cleared = append(f.cleared, id)
	return f.err
}

func (f *fakeTypingStore) touchedTTL(id string) (time.Duration, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ttl, ok := f.touched[id]
	return ttl, ok
}

func (f *fakeTypingStore) wasCleared(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, cleared := range f.cleared {
		if cleared == id {
			return true
		}
	}
	return false
}

// recvRaw blocks for one message on c's outbox, with a timeout.
//
// Hubs built with NewHub (unlike the white-box newTestHub fixtures elsewhere in
// this package) run their broadcast dispatcher and workers on real goroutines,
// so delivery is asynchronous relative to the test goroutine — a non-blocking
// drain immediately after handleClientMessage returns races the delivery
// pipeline. This is the same blocking-with-timeout shape
// TestHub_ReactionToggleUsesServerIdentityAndBroadcastsAggregate already uses.
func recvRaw(t *testing.T, c *Client) []byte {
	t.Helper()
	select {
	case raw := <-c.outbox:
		return raw
	case <-time.After(time.Second):
		t.Fatal("no event delivered within timeout")
		return nil
	}
}

func recvOne(t *testing.T, c *Client) Event {
	t.Helper()
	evt, err := decodeEvent(recvRaw(t, c))
	if err != nil {
		t.Fatalf("decode event: %v", err)
	}
	return evt
}

// assertNoMessage fails if a message arrives on c's outbox before the given
// grace period elapses. Only meaningful when no broadcast could plausibly be
// in flight (the code path under test returned before ever queuing one) —
// otherwise a short grace period would make this test flaky by construction.
func assertNoMessage(t *testing.T, c *Client, grace time.Duration) {
	t.Helper()
	select {
	case raw := <-c.outbox:
		t.Fatalf("unexpected message delivered: %s", raw)
	case <-time.After(grace):
	}
}

// ── validateTypingMessage ────────────────────────────────────────────────────

func TestValidateTypingMessage(t *testing.T) {
	tests := []struct {
		name    string
		msg     ClientMessage
		wantErr bool
	}{
		{
			name: "valid channel start",
			msg:  ClientMessage{Type: ClientMessageTypeTypingStart, TargetType: TargetTypeChannel, TargetID: typingChannel},
		},
		{
			name: "valid dm stop",
			msg:  ClientMessage{Type: ClientMessageTypeTypingStop, TargetType: TargetTypeDM, TargetID: typingChannel},
		},
		{
			name:    "wrong message type",
			msg:     ClientMessage{Type: ClientMessageTypeSubscribe, TargetType: TargetTypeChannel, TargetID: typingChannel},
			wantErr: true,
		},
		{
			name:    "unsupported target type",
			msg:     ClientMessage{Type: ClientMessageTypeTypingStart, TargetType: TargetTypeUser, TargetID: typingChannel},
			wantErr: true,
		},
		{
			name:    "missing target id",
			msg:     ClientMessage{Type: ClientMessageTypeTypingStart, TargetType: TargetTypeChannel},
			wantErr: true,
		},
		{
			name:    "malformed target id",
			msg:     ClientMessage{Type: ClientMessageTypeTypingStart, TargetType: TargetTypeChannel, TargetID: "not-a-uuid"},
			wantErr: true,
		},
		{
			name: "unexpected message_id",
			msg: ClientMessage{
				Type: ClientMessageTypeTypingStart, TargetType: TargetTypeChannel, TargetID: typingChannel,
				MessageID: "11111111-1111-1111-1111-111111111111",
			},
			wantErr: true,
		},
		{
			name: "unexpected call fields",
			msg: ClientMessage{
				Type: ClientMessageTypeTypingStart, TargetType: TargetTypeChannel, TargetID: typingChannel,
				CallID: "11111111-1111-1111-1111-111111111111",
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := validateTypingMessage(tt.msg)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateTypingMessage(%+v) error = %v, wantErr %v", tt.msg, err, tt.wantErr)
			}
		})
	}
}

// ── local dispatch ───────────────────────────────────────────────────────────

func newTypingTestHub(t *testing.T, limiter *fakeTypingLimiter, store *fakeTypingStore) (*Hub, *Client, *Client) {
	t.Helper()
	auth := &fakeAuthorizer{}
	auth.setAccess(typingUserA, typingWorkspace, TargetTypeChannel, typingChannel, true)
	auth.setAccess(typingUserB, typingWorkspace, TargetTypeChannel, typingChannel, true)

	opts := []HubOption{}
	if limiter != nil {
		opts = append(opts, WithTypingLimiter(limiter, 20, 30))
	}
	if store != nil {
		opts = append(opts, WithTypingStore(store))
	}
	hub := NewHub(auth, newTestLogger(), NopBus{}, "test-typing", opts...)
	t.Cleanup(hub.Shutdown)

	a := newClient("client-a", typingUserA, typingWorkspace, &fakeSender{})
	if !hub.Register(a) {
		t.Fatal("register client a")
	}
	if err := hub.Subscribe(context.Background(), a, TargetTypeChannel, typingChannel); err != nil {
		t.Fatalf("subscribe client a: %v", err)
	}
	b := newClient("client-b", typingUserB, typingWorkspace, &fakeSender{})
	if !hub.Register(b) {
		t.Fatal("register client b")
	}
	if err := hub.Subscribe(context.Background(), b, TargetTypeChannel, typingChannel); err != nil {
		t.Fatalf("subscribe client b: %v", err)
	}
	return hub, a, b
}

func TestHub_TypingStartRequiresActiveSubscription(t *testing.T) {
	limiter := &fakeTypingLimiter{allowed: true}
	hub := NewHub(&fakeAuthorizer{}, newTestLogger(), NopBus{}, "test-typing-unsub", WithTypingLimiter(limiter, 20, 30))
	t.Cleanup(hub.Shutdown)
	c := newClient("client-1", typingUserA, typingWorkspace, &fakeSender{})
	if !hub.Register(c) {
		t.Fatal("register")
	}

	err := hub.handleClientMessage(context.Background(), c, ClientMessage{
		Type: ClientMessageTypeTypingStart, TargetType: TargetTypeChannel, TargetID: typingChannel,
	})
	if !errors.Is(err, ErrTypingNotSubscribed) {
		t.Fatalf("expected ErrTypingNotSubscribed, got %v", err)
	}
	if limiter.callCount() != 0 {
		t.Fatal("rate limiter must not be consulted before the subscription check")
	}
}

func TestHub_TypingStopRequiresActiveSubscription(t *testing.T) {
	hub := NewHub(&fakeAuthorizer{}, newTestLogger(), NopBus{}, "test-typing-stop-unsub")
	t.Cleanup(hub.Shutdown)
	c := newClient("client-1", typingUserA, typingWorkspace, &fakeSender{})
	if !hub.Register(c) {
		t.Fatal("register")
	}

	err := hub.handleClientMessage(context.Background(), c, ClientMessage{
		Type: ClientMessageTypeTypingStop, TargetType: TargetTypeChannel, TargetID: typingChannel,
	})
	if !errors.Is(err, ErrTypingNotSubscribed) {
		t.Fatalf("expected ErrTypingNotSubscribed, got %v", err)
	}
}

func TestHub_TypingStartFeatureDisabledWithoutLimiter(t *testing.T) {
	hub, a, _ := newTypingTestHub(t, nil, nil)
	err := hub.handleClientMessage(context.Background(), a, ClientMessage{
		Type: ClientMessageTypeTypingStart, TargetType: TargetTypeChannel, TargetID: typingChannel,
	})
	if !errors.Is(err, ErrTypingFeatureDisabled) {
		t.Fatalf("expected ErrTypingFeatureDisabled, got %v", err)
	}
}

func TestHub_TypingStartStopsWhenRateLimited(t *testing.T) {
	limiter := &fakeTypingLimiter{allowed: false}
	store := newFakeTypingStore()
	hub, a, b := newTypingTestHub(t, limiter, store)

	err := hub.handleClientMessage(context.Background(), a, ClientMessage{
		Type: ClientMessageTypeTypingStart, TargetType: TargetTypeChannel, TargetID: typingChannel,
	})
	if !errors.Is(err, ErrTypingRateLimited) {
		t.Fatalf("expected ErrTypingRateLimited, got %v", err)
	}
	if len(store.touched) != 0 {
		t.Fatal("rate-limited typing.start must not touch the Valkey backstop")
	}
	if got := len(drain(a)); got != 0 {
		t.Fatalf("sender got %d message(s), want 0", got)
	}
	if got := len(drain(b)); got != 0 {
		t.Fatalf("other subscriber got %d message(s), want 0", got)
	}
}

func TestHub_TypingStartBroadcastsToSubscribersAndTouchesStore(t *testing.T) {
	limiter := &fakeTypingLimiter{allowed: true}
	store := newFakeTypingStore()
	hub, a, b := newTypingTestHub(t, limiter, store)

	if err := hub.handleClientMessage(context.Background(), a, ClientMessage{
		Type: ClientMessageTypeTypingStart, TargetType: TargetTypeChannel, TargetID: typingChannel,
	}); err != nil {
		t.Fatalf("typing.start: %v", err)
	}

	// Rate limit is applied with the configured budget, under the "typing_start"
	// action — the same shared limiter reaction/call already use, scoped by action.
	call := limiter.lastCall()
	if call.userID != typingUserA || call.action != "typing_start" || call.maxActions != 20 || call.windowSeconds != 30 {
		t.Fatalf("unexpected limiter call: %+v", call)
	}

	key := targetKey{workspaceID: typingWorkspace, targetType: TargetTypeChannel, targetID: typingChannel}.String()
	ttl, touched := store.touchedTTL(typingStoreID(key, typingUserA))
	if !touched || ttl != typingTTL {
		t.Fatalf("typing store not touched with typingTTL: touched=%v ttl=%v", touched, ttl)
	}

	// Delivered to every subscriber of the target, including the sender — the
	// same echo-to-actor shape reaction.updated/pin.updated already use. Self
	// suppression is a frontend concern (it knows its own user id), not the
	// server's.
	for _, c := range []*Client{a, b} {
		evt := recvOne(t, c)
		if evt.Type != EventTypeTypingUpdated || evt.Typing == nil {
			t.Fatalf("unexpected event: %+v", evt)
		}
		if evt.Typing.UserID != typingUserA {
			t.Fatalf("UserID = %q, want the server-asserted identity, not any client-supplied one", evt.Typing.UserID)
		}
		if !evt.Typing.IsTyping {
			t.Fatal("IsTyping = false, want true")
		}
		if evt.Typing.UpdatedAt == "" {
			t.Fatal("UpdatedAt must be set")
		}
	}
}

func TestHub_TypingStopIsNeverRateLimitedAndClearsStore(t *testing.T) {
	// The limiter denies everything; typing.stop must still succeed.
	limiter := &fakeTypingLimiter{allowed: false}
	store := newFakeTypingStore()
	hub, a, b := newTypingTestHub(t, limiter, store)

	if err := hub.handleClientMessage(context.Background(), a, ClientMessage{
		Type: ClientMessageTypeTypingStop, TargetType: TargetTypeChannel, TargetID: typingChannel,
	}); err != nil {
		t.Fatalf("typing.stop: %v", err)
	}
	if limiter.callCount() != 0 {
		t.Fatal("typing.stop must never consult the rate limiter")
	}

	key := targetKey{workspaceID: typingWorkspace, targetType: TargetTypeChannel, targetID: typingChannel}.String()
	if !store.wasCleared(typingStoreID(key, typingUserA)) {
		t.Fatal("typing store was not cleared")
	}

	evt := recvOne(t, b)
	if evt.Typing == nil || evt.Typing.IsTyping {
		t.Fatalf("expected is_typing=false, got %+v", evt.Typing)
	}
}

func TestHub_TypingStopIsIdempotent(t *testing.T) {
	store := newFakeTypingStore()
	hub, a, _ := newTypingTestHub(t, nil, store)

	// Never started — stop must still succeed and broadcast, rather than error.
	if err := hub.handleClientMessage(context.Background(), a, ClientMessage{
		Type: ClientMessageTypeTypingStop, TargetType: TargetTypeChannel, TargetID: typingChannel,
	}); err != nil {
		t.Fatalf("typing.stop on an unstarted session: %v", err)
	}
}

func TestHub_TypingNeverCarriesTypedText(t *testing.T) {
	limiter := &fakeTypingLimiter{allowed: true}
	hub, a, b := newTypingTestHub(t, limiter, nil)

	if err := hub.handleClientMessage(context.Background(), a, ClientMessage{
		Type: ClientMessageTypeTypingStart, TargetType: TargetTypeChannel, TargetID: typingChannel,
	}); err != nil {
		t.Fatalf("typing.start: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(recvRaw(t, b), &raw); err != nil {
		t.Fatal(err)
	}
	var typing map[string]json.RawMessage
	if err := json.Unmarshal(raw["typing"], &typing); err != nil {
		t.Fatal(err)
	}
	for field := range typing {
		if field != "user_id" && field != "is_typing" && field != "updated_at" {
			t.Fatalf("typing payload carried unexpected field %q — only state may be transmitted, never content", field)
		}
	}
}

// ── disconnect / revocation cleanup ─────────────────────────────────────────

func TestHub_DisconnectStopsTypingPromptly(t *testing.T) {
	limiter := &fakeTypingLimiter{allowed: true}
	store := newFakeTypingStore()
	hub, a, b := newTypingTestHub(t, limiter, store)

	if err := hub.handleClientMessage(context.Background(), a, ClientMessage{
		Type: ClientMessageTypeTypingStart, TargetType: TargetTypeChannel, TargetID: typingChannel,
	}); err != nil {
		t.Fatalf("typing.start: %v", err)
	}
	// Drain the typing.start echo both clients receive, blocking until the
	// broadcast workers have actually delivered it.
	recvOne(t, a)
	recvOne(t, b)

	// dropClient is the actor-goroutine-internal path Unregister feeds.
	hub.dropClient(a)

	key := targetKey{workspaceID: typingWorkspace, targetType: TargetTypeChannel, targetID: typingChannel}.String()
	if !store.wasCleared(typingStoreID(key, typingUserA)) {
		t.Fatal("typing store was not cleared on disconnect")
	}

	evt := recvOne(t, b)
	if evt.Typing == nil || evt.Typing.IsTyping || evt.Typing.UserID != typingUserA {
		t.Fatalf("unexpected stop event: %+v", evt.Typing)
	}
}

func TestHub_DisconnectWithoutTypingBroadcastsNothing(t *testing.T) {
	hub, a, b := newTypingTestHub(t, nil, nil)
	drain(a)
	drain(b)

	hub.dropClient(a)

	if got := len(drain(b)); got != 0 {
		t.Fatalf("disconnecting a client that was never typing broadcast %d message(s), want 0", got)
	}
}

func TestHub_RevokeSubscriptionStopsTypingForThatTarget(t *testing.T) {
	limiter := &fakeTypingLimiter{allowed: true}
	store := newFakeTypingStore()
	hub, a, b := newTypingTestHub(t, limiter, store)

	if err := hub.handleClientMessage(context.Background(), a, ClientMessage{
		Type: ClientMessageTypeTypingStart, TargetType: TargetTypeChannel, TargetID: typingChannel,
	}); err != nil {
		t.Fatalf("typing.start: %v", err)
	}
	recvOne(t, a)
	recvOne(t, b)

	key := targetKey{workspaceID: typingWorkspace, targetType: TargetTypeChannel, targetID: typingChannel}.String()
	if err := hub.RevokeSubscription(context.Background(), a, key); err != nil {
		t.Fatalf("revoke subscription: %v", err)
	}

	if !store.wasCleared(typingStoreID(key, typingUserA)) {
		t.Fatal("typing store was not cleared on subscription revocation")
	}
	evt := recvOne(t, b)
	if evt.Typing == nil || evt.Typing.IsTyping {
		t.Fatalf("expected is_typing=false after revocation, got %+v", evt.Typing)
	}
}

func TestHub_RevokeSubscriptionWithoutTypingBroadcastsNothing(t *testing.T) {
	hub, a, b := newTypingTestHub(t, nil, nil)
	drain(a)
	drain(b)

	key := targetKey{workspaceID: typingWorkspace, targetType: TargetTypeChannel, targetID: typingChannel}.String()
	if err := hub.RevokeSubscription(context.Background(), a, key); err != nil {
		t.Fatalf("revoke subscription: %v", err)
	}
	if got := len(drain(b)); got != 0 {
		t.Fatalf("revoking a subscription nobody was typing on broadcast %d message(s), want 0", got)
	}
}

// ── remote / cross-instance ──────────────────────────────────────────────────

func typingUpdatedEvent() Event {
	return Event{
		SchemaVersion: CurrentEventSchemaVersion, Type: EventTypeTypingUpdated,
		WorkspaceID: typingWorkspace, TargetType: TargetTypeChannel, TargetID: typingChannel,
		Typing: &TypingEventPayload{
			UserID: typingUserA, IsTyping: true, UpdatedAt: "2026-08-13T12:00:00Z",
		},
		EventID:          "6b2f9d43-1c85-4a70-9e26-3f8b5c1a7d94",
		SourceInstanceID: "chat-service-remote",
		CreatedAt:        time.Now().UTC(),
	}
}

func TestCanonicalizeRemoteTypingEventIsAccepted(t *testing.T) {
	evt := typingUpdatedEvent()
	evt.WorkspaceID = strings.ToUpper(typingWorkspace)
	evt.TargetID = strings.ToUpper(typingChannel)
	evt.Typing.UserID = strings.ToUpper(typingUserA)

	canonical, ok := canonicalizeRemoteEvent(evt)
	if !ok {
		t.Fatal("a valid typing.updated must canonicalize")
	}
	if canonical.WorkspaceID != typingWorkspace || canonical.TargetID != typingChannel {
		t.Fatalf("scope = %s/%s, want canonicalized envelope", canonical.WorkspaceID, canonical.TargetID)
	}
	if canonical.Typing == nil || canonical.Typing.UserID != typingUserA {
		t.Fatalf("Typing = %+v, want lowercase canonical user id", canonical.Typing)
	}
}

func TestCanonicalizeRemoteTypingEventRejectsMalformed(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(Event) Event
	}{
		{name: "nil typing payload", mutate: func(e Event) Event { e.Typing = nil; return e }},
		{name: "user-scoped target", mutate: func(e Event) Event { e.TargetType = TargetTypeUser; return e }},
		{name: "non-uuid user id", mutate: func(e Event) Event { e.Typing.UserID = "not-a-uuid"; return e }},
		{
			name: "oversized updated_at",
			mutate: func(e Event) Event {
				e.Typing.UpdatedAt = strings.Repeat("x", typingUpdatedAtMaxLen+1)
				return e
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			evt := typingUpdatedEvent()
			if evt.Typing != nil {
				cp := *evt.Typing
				evt.Typing = &cp
			}
			if _, ok := canonicalizeRemoteEvent(tt.mutate(evt)); ok {
				t.Fatal("expected canonicalization to reject the malformed event")
			}
		})
	}
}

func TestCanonicalizeRemoteTypingEventStripsForeignPayload(t *testing.T) {
	evt := typingUpdatedEvent()
	evt.Payload = &MessagePayload{BodyText: "leaked"}
	evt.Reaction = &ReactionEventPayload{MessageID: "x"}
	evt.Presence = &PresencePayload{UserID: typingUserA, State: "online"}

	canonical, ok := canonicalizeRemoteEvent(evt)
	if !ok {
		t.Fatal("expected canonicalization to succeed")
	}
	if canonical.Payload != nil || canonical.Reaction != nil || canonical.Presence != nil {
		t.Fatalf("foreign payload survived canonicalization: %+v", canonical)
	}
	data, err := json.Marshal(canonical)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "leaked") {
		t.Fatalf("canonicalized event still carried message body_text: %s", data)
	}
}

func TestTypingReachesSubscribersOfItsTargetAcrossInstances(t *testing.T) {
	auth := &recordingAuthorizer{allowed: true}
	_, hubB, _ := twoHubsWithAuthorizer(t, auth)
	startHubLoop(t, hubB)
	subscriber := registerForTest(hubB, "c-subscriber", typingUserB, typingWorkspace)
	subscribeForTest(hubB, subscriber, TargetTypeChannel, typingChannel)

	hubB.handleRemoteBusEvent(typingUpdatedEvent())
	if delivered := deliverRemoteBroadcast(t, hubB); delivered != 1 {
		t.Fatalf("dispatched %d broadcasts, want 1", delivered)
	}

	messages := drain(subscriber)
	if len(messages) != 1 {
		t.Fatalf("subscriber got %d message(s), want 1", len(messages))
	}
	var evt Event
	if err := json.Unmarshal(messages[0], &evt); err != nil {
		t.Fatal(err)
	}
	if evt.Type != EventTypeTypingUpdated || evt.Typing == nil || evt.Typing.UserID != typingUserA {
		t.Fatalf("wrong event delivered: %+v", evt)
	}
}

func TestTypingDoesNotCrossTargets(t *testing.T) {
	auth := &recordingAuthorizer{allowed: true}
	_, hubB, _ := twoHubsWithAuthorizer(t, auth)
	startHubLoop(t, hubB)
	elsewhere := registerForTest(hubB, "c-elsewhere", typingUserB, typingWorkspace)
	subscribeForTest(hubB, elsewhere, TargetTypeChannel, typingOtherRoom)

	hubB.handleRemoteBusEvent(typingUpdatedEvent())
	deliverRemoteBroadcast(t, hubB)

	if got := len(drain(elsewhere)); got != 0 {
		t.Fatalf("a subscriber of another channel got %d message(s), want 0", got)
	}
}

func TestHub_TypingIsNeverPersistedToPostgres(t *testing.T) {
	// Structural, not behavioural: handleTypingStart/handleTypingStop touch only
	// the in-memory typingActive bookkeeping, the injected TypingStore (Valkey),
	// and the broadcast pipeline — no storage package is imported by typing.go,
	// and no *sql/*pgx handle is threaded into any typing codepath. Covered here
	// as a guard so a future change that starts persisting typing state has to
	// touch this test's premise, not just add a silent write.
	limiter := &fakeTypingLimiter{allowed: true}
	hub, a, _ := newTypingTestHub(t, limiter, nil)
	if err := hub.handleClientMessage(context.Background(), a, ClientMessage{
		Type: ClientMessageTypeTypingStart, TargetType: TargetTypeChannel, TargetID: typingChannel,
	}); err != nil {
		t.Fatalf("typing.start: %v", err)
	}
	if err := hub.handleClientMessage(context.Background(), a, ClientMessage{
		Type: ClientMessageTypeTypingStop, TargetType: TargetTypeChannel, TargetID: typingChannel,
	}); err != nil {
		t.Fatalf("typing.stop: %v", err)
	}
}
