package ws

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Subject authorization at *delivery* (SR-444-03).
//
// publishPresence authorizes the subject when the event is queued. Delivery
// happens later, on a broadcast worker, and a membership can be revoked in
// between: the event was decided against a world that no longer exists, and the
// per-recipient re-check says nothing about the subject.
//
// The barrier in these tests is the queue itself. Nothing sleeps and nothing
// races: the event is published, taken off h.bcast, the revocation is applied,
// and only then is handleBroadcast called — which is exactly the interleaving a
// blocked worker produces, stated as a sequence instead of as a hope.

// presenceDeliveryFixture wires one subject and one observer into a private
// channel, both authorized, with the subject online.
func presenceDeliveryFixture(t *testing.T) (*Hub, *revocableAuthorizer, *Client, *Client, string) {
	t.Helper()
	clk := newFakeClock(time.Now())
	tracker := newTestPresenceTracker(5*time.Minute, clk)
	auth := newRevocableAuthorizer()
	h := newPresenceTestHub(auth, tracker)

	subject := newClient("c-subject", "user-b", "ws-1", &fakeSender{})
	registerInHub(t, h, subject)
	tracker.Connect(subject.workspaceID, subject.userID, subject.id)
	key := subscribeInHubState(t, h, subject, TargetTypeChannel, "chan-private")

	observer := newClient("c-observer", "user-a", "ws-1", &fakeSender{})
	registerInHub(t, h, observer)
	subscribeInHubState(t, h, observer, TargetTypeChannel, "chan-private")

	drainPresenceEvents(t, h)
	drain(subject)
	drain(observer)
	return h, auth, subject, observer, key
}

// observerOnlyFixture is one reader in a channel and nobody else, so a delivery
// loop can never be diverted by a second subscriber's authorization.
func observerOnlyFixture(t *testing.T) (*Hub, *revocableAuthorizer, *Client) {
	t.Helper()
	clk := newFakeClock(time.Now())
	tracker := newTestPresenceTracker(5*time.Minute, clk)
	auth := newRevocableAuthorizer()
	h := newPresenceTestHub(auth, tracker)

	observer := newClient("c-observer", "user-a", "ws-1", &fakeSender{})
	registerInHub(t, h, observer)
	tracker.Connect(observer.workspaceID, observer.userID, observer.id)
	subscribeInHubState(t, h, observer, TargetTypeChannel, "chan-private")
	drainPresenceEvents(t, h)
	drain(observer)
	return h, auth, observer
}

// queuePresenceUpdate publishes one away → online transition for the subject
// into one target and returns the broadcast request waiting to be delivered.
func queuePresenceUpdate(t *testing.T, h *Hub, subject *Client, key string) broadcastReq {
	t.Helper()
	h.enqueuePresenceChange(presenceChange{
		workspaceID: subject.workspaceID, userID: subject.userID,
		status: PresenceOnline, at: time.Now().UTC(), keys: []string{key},
	})
	for _, change := range h.takePresenceChanges() {
		h.publishPresence(change)
	}
	select {
	case req := <-h.bcast:
		if req.event.Type != EventTypePresenceUpdated || req.event.Presence == nil {
			t.Fatalf("expected a queued presence.updated, got %+v", req.event)
		}
		return req
	default:
		t.Fatal("no presence.updated was queued for delivery")
		return broadcastReq{}
	}
}

// The window this whole check exists for: authorized when queued, removed before
// the worker got to it.
func TestPresenceDelivery_SubjectRevokedWhileQueuedIsDiscarded(t *testing.T) {
	h, auth, subject, observer, key := presenceDeliveryFixture(t)

	req := queuePresenceUpdate(t, h, subject, key)

	// The membership disappears while the event sits in the queue.
	auth.revoke("user-b", "chan-private")

	h.handleBroadcast(req)

	if frames := drain(observer); len(frames) != 0 {
		t.Fatalf("a removed subject's presence reached an observer: %d frame(s)", len(frames))
	}
}

// The same event arriving over the bus takes the same path, so one check covers
// both fan-outs.
func TestPresenceDelivery_RemoteSubjectRevokedBeforeDeliveryIsDiscarded(t *testing.T) {
	h, auth, _, observer, _ := presenceDeliveryFixture(t)
	h.remoteBcast = make(chan broadcastReq, 4)

	remote := Event{
		SchemaVersion: CurrentEventSchemaVersion, Type: EventTypePresenceUpdated,
		WorkspaceID: uuid.NewString(), TargetType: TargetTypeChannel, TargetID: uuid.NewString(),
		Presence: &PresencePayload{
			UserID: uuid.NewString(), State: string(PresenceOnline),
			UpdatedAt: formatPresenceTime(time.Now().UTC()),
		},
		EventID: uuid.NewString(), SourceInstanceID: "runtime-other", CreatedAt: time.Now().UTC(),
	}
	// The observer watches the target the remote event names, so a delivered
	// event would have somewhere to land.
	observer.workspaceID = remote.WorkspaceID
	subscribeInHubState(t, h, observer, TargetTypeChannel, remote.TargetID)
	drain(observer)

	h.handleRemoteBusEvent(remote)
	req := <-h.remoteBcast

	auth.revoke(remote.Presence.UserID, remote.TargetID)

	h.handleBroadcast(req)

	if frames := drain(observer); len(frames) != 0 {
		t.Fatalf("a remote event about a removed subject was delivered: %d frame(s)", len(frames))
	}
}

// An authorization failure is not permission. Reconciliation repairs the roster
// from an authorized read; guessing here would publish on a question nobody
// answered.
func TestPresenceDelivery_SubjectAuthorizationErrorFailsClosed(t *testing.T) {
	clk := newFakeClock(time.Now())
	tracker := newTestPresenceTracker(5*time.Minute, clk)
	auth := &erroringAuthorizer{}
	h := newPresenceTestHub(auth, tracker)

	subject := newClient("c-subject", "user-b", "ws-1", &fakeSender{})
	registerInHub(t, h, subject)
	tracker.Connect(subject.workspaceID, subject.userID, subject.id)
	key := subscribeInHubState(t, h, subject, TargetTypeChannel, "chan-private")

	observer := newClient("c-observer", "user-a", "ws-1", &fakeSender{})
	registerInHub(t, h, observer)
	subscribeInHubState(t, h, observer, TargetTypeChannel, "chan-private")
	drainPresenceEvents(t, h)
	drain(observer)

	req := queuePresenceUpdate(t, h, subject, key)
	auth.fail(errors.New("database unavailable"))

	h.handleBroadcast(req)

	if frames := drain(observer); len(frames) != 0 {
		t.Fatalf("an unanswered authorization delivered %d frame(s)", len(frames))
	}
}

// One question about the subject, however many recipients there are. The
// subject's access does not vary by who is reading it.
func TestPresenceDelivery_SubjectIsAuthorizedOncePerEvent(t *testing.T) {
	clk := newFakeClock(time.Now())
	tracker := newTestPresenceTracker(5*time.Minute, clk)
	auth := &recordingAuthorizer{allowed: true}
	h := newPresenceTestHub(auth, tracker)

	subject := newClient("c-subject", "user-b", "ws-1", &fakeSender{})
	registerInHub(t, h, subject)
	tracker.Connect(subject.workspaceID, subject.userID, subject.id)
	var key string
	for _, id := range []string{"o-1", "o-2", "o-3"} {
		observer := newClient(id, "user-"+id, "ws-1", &fakeSender{})
		registerInHub(t, h, observer)
		key = subscribeInHubState(t, h, observer, TargetTypeChannel, "chan-1")
	}
	drainPresenceEvents(t, h)

	req := queuePresenceUpdate(t, h, subject, key)
	before := subjectChecks(auth, "user-b")

	h.handleBroadcast(req)

	if got := subjectChecks(auth, "user-b") - before; got != 1 {
		t.Fatalf("subject authorized %d time(s) for one event with three recipients, want 1", got)
	}
}

// subjectChecks counts how often the authorizer was asked about one user.
func subjectChecks(a *recordingAuthorizer, userID string) int {
	count := 0
	for _, call := range a.recorded() {
		if call.userID == userID {
			count++
		}
	}
	return count
}

// Every other event keeps the authorization it had: the recipient's, and only
// the recipient's.
func TestPresenceDelivery_OtherEventTypesAreNotSubjectChecked(t *testing.T) {
	h, auth, observer := observerOnlyFixture(t)

	// Somebody else has lost access to this target. A message is not about them,
	// so their access is not a reason to withhold it from a reader who has it.
	auth.revoke("user-b", "chan-private")

	evt := Event{
		SchemaVersion: CurrentEventSchemaVersion, Type: EventTypeMessageCreated,
		WorkspaceID: "ws-1", TargetType: TargetTypeChannel, TargetID: "chan-private",
		MessageID: uuid.NewString(), EventID: uuid.NewString(),
		SourceInstanceID: h.presenceInstanceID, CreatedAt: time.Now().UTC(),
	}
	h.handleBroadcast(broadcastReq{event: evt, data: []byte(`{"type":"message.created"}`)})

	if frames := drain(observer); len(frames) != 1 {
		t.Fatalf("a message was gated on somebody else's access: %d frame(s), want 1", len(frames))
	}
}

// A snapshot travels as a presence.updated envelope with no Presence block: its
// roster was filtered person by person before it was built, so there is no
// single subject to re-check and it must not be dropped for lack of one.
func TestPresenceDelivery_SnapshotIsNotSubjectChecked(t *testing.T) {
	h, auth, observer := observerOnlyFixture(t)
	auth.revoke("user-b", "chan-private")

	evt := Event{
		SchemaVersion: CurrentEventSchemaVersion, Type: EventTypePresenceUpdated,
		WorkspaceID: "ws-1", TargetType: TargetTypeChannel, TargetID: "chan-private",
		EventID: uuid.NewString(), SourceInstanceID: h.presenceInstanceID, CreatedAt: time.Now().UTC(),
	}
	h.handleBroadcast(broadcastReq{event: evt, data: []byte(`{"type":"presence.snapshot"}`)})

	if frames := drain(observer); len(frames) != 1 {
		t.Fatalf("a reconciled roster was discarded: %d frame(s), want 1", len(frames))
	}
}

// ── echo suppression (SR-444-05) ─────────────────────────────────────────────

// Two pods handed the same WS_INSTANCE_ID must not mistake each other's events
// for their own. A Deployment with a fixed environment variable does exactly
// that, and nothing in the configuration path can detect it — so the origin an
// event carries is the runtime identity, which no two processes share.
func TestEchoSuppression_ProcessesSharingAConfiguredIDStillHearEachOther(t *testing.T) {
	const configured = "same-id"

	nodeA := newTestHub(allowAllAuthorizer{})
	nodeA.instanceID = configured
	nodeA.bcast = make(chan broadcastReq, 4)
	nodeA.quit = make(chan struct{})
	busA := &fakeBus{}
	nodeA.bus = busA

	nodeB := newTestHub(allowAllAuthorizer{})
	nodeB.instanceID = configured
	nodeB.remoteBcast = make(chan broadcastReq, 4)

	if nodeA.presenceInstanceID == nodeB.presenceInstanceID {
		t.Fatal("two processes ended up with the same bus origin")
	}

	nodeA.PublishMembersAdded(
		context.Background(), uuid.NewString(), TargetTypeChannel, uuid.NewString(),
		uuid.NewString(), 1, 3,
	)
	published, ok := busA.lastPublished()
	if !ok {
		t.Fatal("nothing reached the bus")
	}
	if published.SourceInstanceID == configured {
		t.Fatalf("the configured id is being used as the bus origin: %q", published.SourceInstanceID)
	}

	// A suppresses its own echo.
	nodeA.remoteBcast = make(chan broadcastReq, 4)
	nodeA.handleRemoteBusEvent(published)
	if got := len(nodeA.remoteBcast); got != 0 {
		t.Fatalf("a hub failed to suppress its own echo: %d queued", got)
	}

	// B does not, even though it was configured identically.
	nodeB.handleRemoteBusEvent(published)
	if got := len(nodeB.remoteBcast); got != 1 {
		t.Fatalf("a sibling pod discarded a real event as its own echo: %d queued, want 1", got)
	}
}
