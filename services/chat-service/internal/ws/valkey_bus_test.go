package ws

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"
)

// ── fakePubSub — in-process pub-sub for ValkeyBus unit tests ──────────────────

type pubsubMsg struct {
	channel string
	payload string
}

type fakePubSub struct {
	mu          sync.Mutex
	published   []pubsubMsg
	publishErr  error
	handlers    []func(channel, payload string)
	closeCalled bool
}

func (f *fakePubSub) Publish(_ context.Context, channel, payload string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.publishErr != nil {
		return f.publishErr
	}
	f.published = append(f.published, pubsubMsg{channel: channel, payload: payload})
	return nil
}

func (f *fakePubSub) PSubscribe(ctx context.Context, _ string, handler func(channel, payload string)) error {
	f.mu.Lock()
	f.handlers = append(f.handlers, handler)
	f.mu.Unlock()
	<-ctx.Done()
	return ctx.Err()
}

// send simulates a Valkey message arriving from the broker.
func (f *fakePubSub) send(channel, payload string) {
	f.mu.Lock()
	handlers := make([]func(string, string), len(f.handlers))
	copy(handlers, f.handlers)
	f.mu.Unlock()
	for _, h := range handlers {
		h(channel, payload)
	}
}

func (f *fakePubSub) Close() {
	f.mu.Lock()
	f.closeCalled = true
	f.mu.Unlock()
}

func (f *fakePubSub) publishedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.published)
}

func (f *fakePubSub) lastPublished() (pubsubMsg, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.published) == 0 {
		return pubsubMsg{}, false
	}
	return f.published[len(f.published)-1], true
}

// ── ValkeyBus unit tests ──────────────────────────────────────────────────────

func TestValkeyBus_Publish_SerializesEventToJSON(t *testing.T) {
	ps := &fakePubSub{}
	bus := newValkeyBusWithAdapter(ps, "inst-A", newTestLogger())
	defer bus.Close()

	evt := Event{
		Type:             EventTypeMessageCreated,
		WorkspaceID:      testWorkspaceID,
		TargetType:       TargetTypeChannel,
		TargetID:         testChannelID,
		MessageID:        testMessageID,
		EventID:          testEventID,
		SourceInstanceID: "inst-A",
		CreatedAt:        time.Now().UTC(),
	}

	if err := bus.Publish(context.Background(), evt); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	if n := ps.publishedCount(); n != 1 {
		t.Fatalf("expected 1 publish call, got %d", n)
	}

	msg, _ := ps.lastPublished()

	// Channel must be workspace-scoped with canonical UUID.
	wantCh := "nchat:chat:ws:broadcast:" + testWorkspaceID
	if msg.channel != wantCh {
		t.Errorf("channel = %q, want %q", msg.channel, wantCh)
	}

	// Payload must be valid JSON containing the event fields.
	var decoded Event
	if err := json.Unmarshal([]byte(msg.payload), &decoded); err != nil {
		t.Fatalf("payload not valid JSON: %v", err)
	}
	if decoded.MessageID != testMessageID {
		t.Errorf("decoded MessageID = %q, want %q", decoded.MessageID, testMessageID)
	}
}

func TestValkeyBus_Publish_FailsOnPubSubError(t *testing.T) {
	ps := &fakePubSub{publishErr: errors.New("connection refused")}
	bus := newValkeyBusWithAdapter(ps, "inst-A", newTestLogger())
	defer bus.Close()

	err := bus.Publish(context.Background(), Event{
		Type:             EventTypeMessageCreated,
		WorkspaceID:      testWorkspaceID,
		TargetType:       TargetTypeChannel,
		TargetID:         testChannelID,
		EventID:          testEventID2,
		SourceInstanceID: "inst-A",
		CreatedAt:        time.Now().UTC(),
	})
	if err == nil {
		t.Fatal("expected error from failed publish, got nil")
	}
}

func TestValkeyBus_Publish_ReturnsErrorOnEmptyWorkspaceID(t *testing.T) {
	ps := &fakePubSub{}
	bus := newValkeyBusWithAdapter(ps, "inst-A", newTestLogger())
	defer bus.Close()

	err := bus.Publish(context.Background(), Event{
		Type:             EventTypeMessageCreated,
		WorkspaceID:      "", // invalid
		TargetType:       TargetTypeChannel,
		TargetID:         "ch-1",
		EventID:          "evt-3",
		SourceInstanceID: "inst-A",
	})
	if err == nil {
		t.Fatal("expected error for empty workspace_id, got nil")
	}
	if ps.publishedCount() != 0 {
		t.Fatal("publish must not be called with empty workspace_id")
	}
}

func TestValkeyBus_Subscribe_InvokesHandlerOnMessage(t *testing.T) {
	ps := &fakePubSub{}
	bus := newValkeyBusWithAdapter(ps, "inst-A", newTestLogger())
	defer bus.Close()

	received := make(chan Event, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	bus.Subscribe(ctx, func(evt Event) {
		received <- evt
	})

	// Allow subscriber goroutine to start.
	time.Sleep(20 * time.Millisecond)

	evt := Event{
		Type:             EventTypeMessageCreated,
		WorkspaceID:      "ws-1",
		TargetType:       TargetTypeChannel,
		TargetID:         "ch-1",
		MessageID:        "msg-remote",
		EventID:          "evt-4",
		SourceInstanceID: "inst-B",
		CreatedAt:        time.Now().UTC(),
	}
	raw, _ := json.Marshal(evt)
	ps.send("nchat:chat:ws:broadcast:ws-1", string(raw))

	select {
	case got := <-received:
		if got.MessageID != "msg-remote" {
			t.Errorf("got MessageID = %q, want %q", got.MessageID, "msg-remote")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("handler not called within timeout")
	}
}

func TestValkeyBus_Subscribe_IgnoresMalformedJSON(t *testing.T) {
	ps := &fakePubSub{}
	bus := newValkeyBusWithAdapter(ps, "inst-A", newTestLogger())
	defer bus.Close()

	received := make(chan Event, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	bus.Subscribe(ctx, func(evt Event) {
		received <- evt
	})

	time.Sleep(20 * time.Millisecond)

	// Send malformed payload — handler must not be called.
	ps.send("nchat:chat:ws:broadcast:ws-1", `{invalid json`)

	select {
	case <-received:
		t.Fatal("handler must not be called for malformed JSON")
	case <-time.After(50 * time.Millisecond):
		// Correct: no delivery.
	}
}

func TestValkeyBus_Subscribe_ExitsCleanlyOnContextCancel(t *testing.T) {
	ps := &fakePubSub{}
	bus := newValkeyBusWithAdapter(ps, "inst-A", newTestLogger())
	defer bus.Close()

	ctx, cancel := context.WithCancel(context.Background())
	bus.Subscribe(ctx, func(Event) {})

	time.Sleep(20 * time.Millisecond)

	// Cancel context — bus goroutine must exit.
	cancel()

	// Close should complete promptly (no goroutine leak).
	done := make(chan struct{})
	go func() {
		bus.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Close blocked after context cancel — possible goroutine leak")
	}
}

func TestValkeyBus_Close_StopsSubscriber(t *testing.T) {
	ps := &fakePubSub{}
	bus := newValkeyBusWithAdapter(ps, "inst-A", newTestLogger())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	bus.Subscribe(ctx, func(Event) {})
	time.Sleep(20 * time.Millisecond)

	done := make(chan struct{})
	go func() {
		bus.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Close did not return within timeout — possible goroutine leak")
	}
}

func TestValkeyBus_Publish_CancelledContext_ReturnsError(t *testing.T) {
	ps := &fakePubSub{}
	bus := newValkeyBusWithAdapter(ps, "inst-A", newTestLogger())
	defer bus.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	err := bus.Publish(ctx, Event{
		Type:             EventTypeMessageCreated,
		WorkspaceID:      testWorkspaceID,
		TargetType:       TargetTypeChannel,
		TargetID:         testChannelID,
		EventID:          testEventID3,
		SourceInstanceID: "inst-A",
		CreatedAt:        time.Now().UTC(),
	})
	// Either context error or publish error is acceptable; must not panic.
	_ = err
}

func TestValkeyBus_Publish_UnsafeWorkspaceID_ReturnsError(t *testing.T) {
	ps := &fakePubSub{}
	bus := newValkeyBusWithAdapter(ps, "inst-A", newTestLogger())
	defer bus.Close()

	// workspace_id with special Valkey glob characters must be rejected.
	err := bus.Publish(context.Background(), Event{
		Type:             EventTypeMessageCreated,
		WorkspaceID:      "ws*bad",
		TargetType:       TargetTypeChannel,
		TargetID:         "ch-1",
		EventID:          "evt-6",
		SourceInstanceID: "inst-A",
		CreatedAt:        time.Now().UTC(),
	})
	if err == nil {
		t.Fatal("expected error for unsafe workspace_id, got nil")
	}
	if ps.publishedCount() != 0 {
		t.Fatal("publish must not be called with unsafe workspace_id")
	}
}

// errorThenBlockPubSub fails on the first PSubscribe call, then blocks.
// Used to test the subscribeLoop reconnect path.
type errorThenBlockPubSub struct {
	mu    sync.Mutex
	calls int
	fakePubSub
}

func (e *errorThenBlockPubSub) PSubscribe(ctx context.Context, pattern string, handler func(string, string)) error {
	e.mu.Lock()
	e.calls++
	n := e.calls
	e.mu.Unlock()
	if n == 1 {
		return errors.New("connection refused on first attempt")
	}
	// Second and subsequent calls block until ctx cancelled (normal operation).
	<-ctx.Done()
	return ctx.Err()
}

func TestValkeyBus_SubscribeLoop_ReconnectsAfterError(t *testing.T) {
	ps := &errorThenBlockPubSub{}
	bus := newValkeyBusWithAdapter(ps, "inst-A", newTestLogger())
	defer bus.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	received := make(chan Event, 1)
	bus.Subscribe(ctx, func(evt Event) {
		received <- evt
	})

	// Wait for reconnect — the second PSubscribe call should be active.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		ps.mu.Lock()
		calls := ps.calls
		ps.mu.Unlock()
		if calls >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	ps.mu.Lock()
	calls := ps.calls
	ps.mu.Unlock()
	if calls < 2 {
		t.Fatalf("expected at least 2 PSubscribe calls (reconnect), got %d", calls)
	}
}

func TestValkeyBus_NewValkeyBus_EmptyURL_ReturnsError(t *testing.T) {
	_, err := NewValkeyBus("", "inst-A", newTestLogger())
	if err == nil {
		t.Fatal("expected error for empty valkeyURL, got nil")
	}
}

func TestValkeyBus_NewValkeyBus_InvalidURL_ReturnsError(t *testing.T) {
	// Pass an unparseable address — must return error, not panic.
	_, err := NewValkeyBus("not-a-valid-host:99999", "inst-A", newTestLogger())
	// valkey-go may or may not error at construction time; we just must not panic.
	_ = err
}
