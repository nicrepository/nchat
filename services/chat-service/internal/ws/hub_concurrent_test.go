package ws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const concurrentSenders = 20

// drainOutbox reads exactly n events from c.outbox without blocking.
// Must be called only after eventually() has confirmed len(c.outbox) >= n,
// so the channel is stable and no additional writers are active.
func drainOutbox(t *testing.T, c *Client, n int) []Event {
	t.Helper()
	events := make([]Event, 0, n)
	for i := 0; i < n; i++ {
		select {
		case raw := <-c.outbox:
			var evt Event
			if err := json.Unmarshal(raw, &evt); err != nil {
				t.Fatalf("drainOutbox: json.Unmarshal: %v", err)
			}
			events = append(events, evt)
		default:
			t.Fatalf("drainOutbox: outbox empty after %d events; expected %d", i, n)
		}
	}
	return events
}

// assertEventSet verifies that events contains exactly the expected message IDs
// and that every event carries the correct type, targetType, targetID, and a
// non-nil payload. It is the canonical post-drain assertion for fan-out tests.
func assertEventSet(t *testing.T, events []Event, wantType EventType, wantTargetType TargetType, wantTargetID string, wantMsgIDs map[string]struct{}) {
	t.Helper()
	gotMsgIDs := make(map[string]struct{}, len(events))
	for _, evt := range events {
		if evt.Type != wantType {
			t.Errorf("event.Type: got %q want %q", evt.Type, wantType)
		}
		if evt.TargetType != wantTargetType {
			t.Errorf("event.TargetType: got %q want %q", evt.TargetType, wantTargetType)
		}
		if evt.TargetID != wantTargetID {
			t.Errorf("event.TargetID: got %q want %q", evt.TargetID, wantTargetID)
		}
		if evt.Payload == nil {
			t.Error("event.Payload must be non-nil for message.created events")
			continue
		}
		gotMsgIDs[evt.Payload.ID] = struct{}{}
	}
	if len(gotMsgIDs) != len(wantMsgIDs) {
		t.Fatalf("message ID set size: got %d want %d; received=%v", len(gotMsgIDs), len(wantMsgIDs), gotMsgIDs)
	}
	for id := range wantMsgIDs {
		if _, ok := gotMsgIDs[id]; !ok {
			t.Errorf("expected message ID %q not found in received events", id)
		}
	}
}

// TestConcurrentChannelBroadcast verifies that 20 goroutines publishing to the
// same channel concurrently results in every subscriber receiving exactly 20
// messages with correct fields. A non-member outsider's outbox must stay empty.
func TestConcurrentChannelBroadcast(t *testing.T) {
	const (
		wsID  = "ws-concurrent-ch"
		chID  = "ch-concurrent"
		nSubs = 3
	)

	auth := &fakeAuthorizer{}
	for i := 0; i < nSubs; i++ {
		auth.setAccess(fmt.Sprintf("user-%d", i), wsID, TargetTypeChannel, chID, true)
	}
	// "non-member" has no access set — subscribe must return ErrSubscribeForbidden.

	hub := NewHub(auth, newTestLogger(), NopBus{}, "test-concurrent-ch")
	defer hub.Shutdown()

	clients := make([]*Client, nSubs)
	for i := 0; i < nSubs; i++ {
		c := newClient(fmt.Sprintf("sub-%d", i), fmt.Sprintf("user-%d", i), wsID, &fakeSender{})
		registerInRunningHub(t, hub, c)
		if err := hub.Subscribe(context.Background(), c, TargetTypeChannel, chID); err != nil {
			t.Fatalf("subscriber %d: %v", i, err)
		}
		clients[i] = c
	}

	// Outsider: registered but subscription must be denied.
	outsider := newClient("ch-outsider", "non-member", wsID, &fakeSender{})
	registerInRunningHub(t, hub, outsider)
	if err := hub.Subscribe(context.Background(), outsider, TargetTypeChannel, chID); !errors.Is(err, ErrSubscribeForbidden) {
		t.Fatalf("outsider subscribe must return ErrSubscribeForbidden, got %v", err)
	}

	// Build the expected message ID set before launching publishers.
	wantMsgIDs := make(map[string]struct{}, concurrentSenders)
	for i := 0; i < concurrentSenders; i++ {
		wantMsgIDs[fmt.Sprintf("msg-ch-%d", i)] = struct{}{}
	}

	var wg sync.WaitGroup
	wg.Add(concurrentSenders)
	for i := 0; i < concurrentSenders; i++ {
		go func(n int) {
			defer wg.Done()
			hub.PublishMessageCreated(
				context.Background(),
				wsID, TargetTypeChannel, chID,
				MessagePayload{
					ID:          fmt.Sprintf("msg-ch-%d", n),
					WorkspaceID: wsID,
					ChannelID:   chID,
					SenderID:    "sender-1",
					Kind:        "user",
					Status:      "active",
					CreatedAt:   time.Now().UTC(),
					UpdatedAt:   time.Now().UTC(),
				},
			)
		}(i)
	}
	wg.Wait()

	for i, c := range clients {
		i, c := i, c
		eventually(t,
			func() bool { return len(c.outbox) == concurrentSenders },
			5*time.Second,
			fmt.Sprintf("subscriber %d must receive exactly %d messages", i, concurrentSenders),
		)
		events := drainOutbox(t, c, concurrentSenders)
		assertEventSet(t, events, EventTypeMessageCreated, TargetTypeChannel, chID, wantMsgIDs)
	}

	// Non-member isolation: outsider must not receive any events.
	if n := len(outsider.outbox); n != 0 {
		t.Fatalf("non-member outsider must receive 0 events during concurrent channel broadcast, got %d", n)
	}
}

// TestConcurrentDMSend verifies that 20 goroutines sending to a DM conversation
// concurrently results in both DM participants receiving exactly 20 correctly
// formed messages. A non-member (carol) must receive nothing.
func TestConcurrentDMSend(t *testing.T) {
	const (
		wsID = "ws-concurrent-dm"
		dmID = "dm-concurrent"
	)

	auth := &fakeAuthorizer{}
	auth.setAccess("user-alice", wsID, TargetTypeDM, dmID, true)
	auth.setAccess("user-bob", wsID, TargetTypeDM, dmID, true)
	// "user-carol" has no access.

	hub := NewHub(auth, newTestLogger(), NopBus{}, "test-concurrent-dm")
	defer hub.Shutdown()

	alice := newClient("dm-alice", "user-alice", wsID, &fakeSender{})
	bob := newClient("dm-bob", "user-bob", wsID, &fakeSender{})
	for _, c := range []*Client{alice, bob} {
		registerInRunningHub(t, hub, c)
		if err := hub.Subscribe(context.Background(), c, TargetTypeDM, dmID); err != nil {
			t.Fatalf("subscribe %s: %v", c.id, err)
		}
	}

	// Non-member carol: subscribe must be denied.
	carol := newClient("dm-carol", "user-carol", wsID, &fakeSender{})
	registerInRunningHub(t, hub, carol)
	if err := hub.Subscribe(context.Background(), carol, TargetTypeDM, dmID); !errors.Is(err, ErrSubscribeForbidden) {
		t.Fatalf("non-member carol must get ErrSubscribeForbidden, got %v", err)
	}

	wantMsgIDs := make(map[string]struct{}, concurrentSenders)
	for i := 0; i < concurrentSenders; i++ {
		wantMsgIDs[fmt.Sprintf("msg-dm-%d", i)] = struct{}{}
	}

	var wg sync.WaitGroup
	wg.Add(concurrentSenders)
	for i := 0; i < concurrentSenders; i++ {
		go func(n int) {
			defer wg.Done()
			hub.PublishMessageCreated(
				context.Background(),
				wsID, TargetTypeDM, dmID,
				MessagePayload{
					ID:               fmt.Sprintf("msg-dm-%d", n),
					WorkspaceID:      wsID,
					DMConversationID: dmID,
					SenderID:         "user-alice",
					Kind:             "user",
					Status:           "active",
					CreatedAt:        time.Now().UTC(),
					UpdatedAt:        time.Now().UTC(),
				},
			)
		}(i)
	}
	wg.Wait()

	for _, c := range []*Client{alice, bob} {
		c := c
		eventually(t,
			func() bool { return len(c.outbox) == concurrentSenders },
			5*time.Second,
			fmt.Sprintf("%s must receive exactly %d messages", c.id, concurrentSenders),
		)
		events := drainOutbox(t, c, concurrentSenders)
		assertEventSet(t, events, EventTypeMessageCreated, TargetTypeDM, dmID, wantMsgIDs)
	}

	// Non-member isolation.
	if n := len(carol.outbox); n != 0 {
		t.Fatalf("non-member carol must receive 0 DM events, got %d", n)
	}
}

// TestConcurrentSubscribeUnsubscribe verifies that goroutines subscribing and
// unsubscribing concurrently while broadcasts are in flight cause no panic,
// no deadlock, and no unexpected subscribe errors. An atomic success counter
// provides an observable effect: at least one subscribe/unregister cycle must
// complete without error within the 5-second budget.
func TestConcurrentSubscribeUnsubscribe(t *testing.T) {
	const (
		wsID         = "ws-concurrent-sub"
		chID         = "ch-concurrent-sub"
		workers      = 20
		broadcasters = 20
	)

	auth := &fakeAuthorizer{}
	for i := 0; i < workers; i++ {
		auth.setAccess(fmt.Sprintf("wuser-%d", i), wsID, TargetTypeChannel, chID, true)
	}

	hub := NewHub(auth, newTestLogger(), NopBus{}, "test-concurrent-sub")
	defer hub.Shutdown()

	start := make(chan struct{})
	errCh := make(chan error, workers)
	var subscribedCount int64

	var wg sync.WaitGroup
	wg.Add(workers + broadcasters)

	for i := 0; i < workers; i++ {
		go func(n int) {
			defer wg.Done()
			c := newClient(fmt.Sprintf("wsub-%d", n), fmt.Sprintf("wuser-%d", n), wsID, &fakeSender{})
			<-start
			if !hub.Register(c) {
				return // hub shutting down — not a test failure
			}
			if err := hub.Subscribe(context.Background(), c, TargetTypeChannel, chID); err != nil {
				errCh <- err
				hub.Unregister(c)
				return
			}
			atomic.AddInt64(&subscribedCount, 1)
			hub.Unregister(c)
		}(i)
	}

	for i := 0; i < broadcasters; i++ {
		go func(n int) {
			defer wg.Done()
			<-start
			hub.PublishMessageCreated(
				context.Background(),
				wsID, TargetTypeChannel, chID,
				MessagePayload{
					ID:          fmt.Sprintf("bcast-%d", n),
					WorkspaceID: wsID,
					ChannelID:   chID,
					SenderID:    "system",
					Kind:        "user",
					Status:      "active",
					CreatedAt:   time.Now().UTC(),
					UpdatedAt:   time.Now().UTC(),
				},
			)
		}(i)
	}

	close(start)

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent subscribe/unsubscribe: deadlock or timeout after 5s")
	}

	// All goroutines are done — no more sends to errCh.
	close(errCh)
	for err := range errCh {
		t.Errorf("unexpected subscribe error from authorized worker: %v", err)
	}

	// Observable effect: at least one subscribe+unregister cycle must have completed.
	if n := atomic.LoadInt64(&subscribedCount); n == 0 {
		t.Fatal("no successful subscribe/unregister cycle observed — possible deadlock or mis-wired auth")
	}
}
