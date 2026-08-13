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

// Authorization revoked between a subscribe and the presence snapshot that
// answers it (SR-1, RF-58).
//
// The subscribe authorizes the subscription; the snapshot is built afterwards,
// and it is the single largest disclosure in this protocol — the roster of who
// is present in a target. A membership removed in between must not be answered
// with it, and the denial must leave no subscription behind either.

// ── revocation between subscribe and snapshot (SR-1) ─────────────────────────

// revokingAuthorizer allows the first question and refuses every one after it,
// which is exactly the shape of a membership revoked between the subscribe and
// the snapshot that follows it.
type revokingAuthorizer struct {
	mu    sync.Mutex
	calls int
}

func (a *revokingAuthorizer) CanAccess(context.Context, string, string, TargetType, string) (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls++
	return a.calls == 1, nil
}

func (a *revokingAuthorizer) callCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls
}

func TestSnapshotAuthorization_RevokedBetweenSubscribeAndSnapshot(t *testing.T) {
	tracker := NewPresenceTracker(time.Hour)
	defer tracker.Stop()

	auth := &revokingAuthorizer{}
	hub := NewHub(auth, newTestLogger(), NopBus{}, "snapshot-authz", WithPresence(tracker))
	defer hub.Shutdown()

	c := newClient("c-1", "user-1", "ws-1", &fakeSender{})
	registerInRunningHub(t, hub, c)

	// The subscribe is authorized and the subscription exists.
	key := targetKey{workspaceID: "ws-1", targetType: TargetTypeChannel, targetID: "chan-1"}.String()
	before := hub.subscriptionGeneration(c, key)
	if err := hub.Subscribe(context.Background(), c, TargetTypeChannel, "chan-1"); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if !hubHasClientSubscription(hub, c.id, key) {
		t.Fatal("expected the subscription to exist after an authorized subscribe")
	}

	// Access is gone by the time the snapshot would be built.
	hub.handleSubscribed(c, TargetTypeChannel, "chan-1", before)

	if auth.callCount() < 2 {
		t.Fatalf("expected authorization to be re-checked before the snapshot, got %d call(s)", auth.callCount())
	}
	if hubHasClientSubscription(hub, c.id, key) {
		t.Fatal("the subscription must not survive a denial")
	}

	// One drain, two questions: no roster was sent, and the client was told with
	// the protocol's existing non-enumerating code.
	frames := drainFrames(c)
	for _, frame := range frames {
		if strings.Contains(frame, PresenceSnapshotType) {
			t.Fatalf("a revoked client was sent a roster: %s", frame)
		}
	}
	if code := lastErrorCodeIn(frames); code != "room_access_denied" {
		t.Fatalf("expected the generic denial code, got %q", code)
	}

	// And nothing from that target reaches it afterwards.
	hub.PublishPinUpdated(context.Background(), "ws-1", TargetTypeChannel, "chan-1", "m-1", "user-2", true)
	eventually(t, func() bool { return true }, 100*time.Millisecond, "publish settled")
	for _, frame := range drainFrames(c) {
		if strings.Contains(frame, "pin.updated") {
			t.Fatal("a revoked subscription still received events")
		}
	}
}

func TestSnapshotAuthorization_StillAllowedSendsTheSnapshot(t *testing.T) {
	tracker := NewPresenceTracker(time.Hour)
	defer tracker.Stop()

	auth := &fakeAuthorizer{}
	auth.setAccess("user-1", "ws-1", TargetTypeChannel, "chan-1", true)
	hub := NewHub(auth, newTestLogger(), NopBus{}, "snapshot-authz-ok", WithPresence(tracker))
	defer hub.Shutdown()

	c := newClient("c-1", "user-1", "ws-1", &fakeSender{})
	registerInRunningHub(t, hub, c)

	key := targetKey{workspaceID: "ws-1", targetType: TargetTypeChannel, targetID: "chan-1"}.String()
	before := hub.subscriptionGeneration(c, key)
	if err := hub.Subscribe(context.Background(), c, TargetTypeChannel, "chan-1"); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	hub.handleSubscribed(c, TargetTypeChannel, "chan-1", before)

	if got := takeSnapshots(t, c); len(got) != 1 {
		t.Fatalf("expected one snapshot for a client that may still read, got %d", len(got))
	}
	if !hubHasClientSubscription(hub, c.id, key) {
		t.Fatal("an allowed subscription must survive")
	}
}

// A denial at subscribe time never reaches the snapshot at all: there is no
// subscription, so there is nothing to answer.
func TestSnapshotAuthorization_DeniedSubscribeHasNoSnapshot(t *testing.T) {
	tracker := NewPresenceTracker(time.Hour)
	defer tracker.Stop()

	hub := NewHub(&fakeAuthorizer{}, newTestLogger(), NopBus{}, "snapshot-authz-deny", WithPresence(tracker))
	defer hub.Shutdown()

	c := newClient("c-1", "user-1", "ws-1", &fakeSender{})
	registerInRunningHub(t, hub, c)

	key := targetKey{workspaceID: "ws-1", targetType: TargetTypeChannel, targetID: "chan-1"}.String()
	before := hub.subscriptionGeneration(c, key)
	if err := hub.Subscribe(context.Background(), c, TargetTypeChannel, "chan-1"); !errors.Is(err, ErrSubscribeForbidden) {
		t.Fatalf("expected the subscribe to be refused, got %v", err)
	}
	hub.handleSubscribed(c, TargetTypeChannel, "chan-1", before)

	if got := takeSnapshots(t, c); len(got) != 0 {
		t.Fatalf("expected no snapshot, got %+v", got)
	}
	if hubHasClientSubscription(hub, c.id, key) {
		t.Fatal("a refused subscribe must leave no subscription")
	}
}

func drainFrames(c *Client) []string {
	frames := make([]string, 0, 4)
	for {
		select {
		case data := <-c.outbox:
			frames = append(frames, string(data))
		default:
			return frames
		}
	}
}

func lastErrorCodeIn(frames []string) string {
	code := ""
	for _, frame := range frames {
		var payload struct {
			Type string `json:"type"`
			Code string `json:"code"`
		}
		if err := json.Unmarshal([]byte(frame), &payload); err != nil {
			continue
		}
		if payload.Type == "error" {
			code = payload.Code
		}
	}
	return code
}
