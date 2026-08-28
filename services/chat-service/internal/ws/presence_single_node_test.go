package ws

import (
	"context"
	"testing"
	"time"
)

// Reconciliation without a shared directory (SR-444-04).
//
// A deployment with no directory is not a deployment with no authority: this hub
// holds every connection there is, so its own subscriptions and its own tracker
// *are* the roster. Reconciliation used to return early there, which meant the
// one correction that has no other trigger — a user who stops watching a
// conversation while staying connected elsewhere — never happened at all.
//
// Nothing here sends a global offline. The person is still connected; only their
// place in one target has changed, and that is what is republished.

// newSingleNodeHub is a hub that is the whole cluster: no bus, no directory.
func newSingleNodeHub(t *testing.T, tracker *PresenceTracker) *Hub {
	t.Helper()
	h := newPresenceTestHub(allowAllAuthorizer{}, tracker)
	if h.distributed || h.directory != nil {
		t.Fatal("fixture is not single-node")
	}
	return h
}

// The whole scenario from the report: B is visible online in X, B drops its last
// subscription to X while staying connected to Y, and A — still watching X — is
// corrected at once.
func TestSingleNode_LosingTheLastCoverageCorrectsTheTarget(t *testing.T) {
	clk := newFakeClock(time.Now())
	tracker := newTestPresenceTracker(5*time.Minute, clk)
	h := newSingleNodeHub(t, tracker)

	observer := newClient("c-a", "user-a", "ws-1", &fakeSender{})
	registerInHub(t, h, observer)
	tracker.Connect(observer.workspaceID, observer.userID, observer.id)
	subscribeInHubState(t, h, observer, TargetTypeChannel, "chan-x")

	subject := newClient("c-b", "user-b", "ws-1", &fakeSender{})
	registerInHub(t, h, subject)
	tracker.Connect(subject.workspaceID, subject.userID, subject.id)
	keyX := subscribeInHubState(t, h, subject, TargetTypeChannel, "chan-x")
	subscribeInHubState(t, h, subject, TargetTypeChannel, "chan-y")
	drainPresenceEvents(t, h)

	// A is told B is here.
	roster, complete := h.localRoster(mustParseKey(t, keyX), keyX)
	if !complete || len(roster) != 2 {
		t.Fatalf("expected a complete roster naming both, got complete=%v %+v", complete, roster)
	}

	if !h.revokeSubscription(subject, keyX) {
		t.Fatal("unsubscribe failed")
	}

	// No global departure was invented: B is still connected, and still online.
	for _, evt := range drainPresenceEvents(t, h) {
		if evt.Presence != nil && evt.Presence.UserID == "user-b" &&
			evt.Presence.State == string(PresenceOffline) {
			t.Fatalf("an unsubscribe published a false offline: %+v", evt.Presence)
		}
	}
	if got := tracker.Status("ws-1", "user-b"); got != PresenceOnline {
		t.Fatalf("an unsubscribe changed the user's presence: %q", got)
	}

	// The correction the unsubscribe asked for is target-scoped and complete.
	h.drainReconcileRequests()
	rosters := reconciledRosters(t, h)
	snapshot, ok := rosters["chan-x"]
	if !ok {
		t.Fatalf("chan-x was never corrected: %+v", rosters)
	}
	if !snapshot.Complete {
		t.Fatal("a single-node roster built from the whole cluster was marked incomplete")
	}
	for _, user := range snapshot.Users {
		if user.UserID == "user-b" {
			t.Fatalf("the subject is still in the corrected roster: %+v", snapshot.Users)
		}
	}
	if _, corrected := rosters["chan-y"]; corrected {
		t.Fatal("a target the subject still covers was reconciled")
	}
}

// A second connection still reading X is still coverage. Nothing changes and
// nothing is sent.
func TestSingleNode_OverlappingCoverageKeepsTheSubject(t *testing.T) {
	clk := newFakeClock(time.Now())
	tracker := newTestPresenceTracker(5*time.Minute, clk)
	h := newSingleNodeHub(t, tracker)

	observer := newClient("c-a", "user-a", "ws-1", &fakeSender{})
	registerInHub(t, h, observer)
	tracker.Connect(observer.workspaceID, observer.userID, observer.id)
	subscribeInHubState(t, h, observer, TargetTypeChannel, "chan-x")

	first := newClient("c-b1", "user-b", "ws-1", &fakeSender{})
	second := newClient("c-b2", "user-b", "ws-1", &fakeSender{})
	for _, c := range []*Client{first, second} {
		registerInHub(t, h, c)
		tracker.Connect(c.workspaceID, c.userID, c.id)
	}
	keyX := subscribeInHubState(t, h, first, TargetTypeChannel, "chan-x")
	subscribeInHubState(t, h, second, TargetTypeChannel, "chan-x")
	drainPresenceEvents(t, h)

	if !h.revokeSubscription(first, keyX) {
		t.Fatal("unsubscribe failed")
	}
	h.drainReconcileRequests()

	// Either nothing was sent, or what was sent still names the subject. Both are
	// correct; what would be wrong is a roster that dropped them.
	roster, complete := h.localRoster(mustParseKey(t, keyX), keyX)
	if !complete {
		t.Fatal("a single-node roster was marked incomplete")
	}
	found := false
	for _, user := range roster {
		if user.UserID == "user-b" {
			found = true
		}
	}
	if !found {
		t.Fatalf("a still-covered subject was dropped from the roster: %+v", roster)
	}
	for _, snapshot := range reconciledRosters(t, h) {
		for _, user := range snapshot.Users {
			if user.UserID == "user-b" {
				found = true
			}
		}
		if snapshot.TargetID == "chan-x" && !found {
			t.Fatalf("a corrected roster dropped a still-covered subject: %+v", snapshot)
		}
	}
}

// Nobody left watching means nobody to correct, so nothing is queued at all.
func TestSingleNode_UnwatchedTargetIsNotReconciled(t *testing.T) {
	clk := newFakeClock(time.Now())
	tracker := newTestPresenceTracker(5*time.Minute, clk)
	h := newSingleNodeHub(t, tracker)

	subject := newClient("c-b", "user-b", "ws-1", &fakeSender{})
	registerInHub(t, h, subject)
	tracker.Connect(subject.workspaceID, subject.userID, subject.id)
	key := subscribeInHubState(t, h, subject, TargetTypeChannel, "chan-x")
	drainPresenceEvents(t, h)

	if !h.revokeSubscription(subject, key) {
		t.Fatal("unsubscribe failed")
	}
	h.drainReconcileRequests()

	if rosters := reconciledRosters(t, h); len(rosters) != 0 {
		t.Fatalf("a target nobody watches was reconciled: %+v", rosters)
	}
}

// A disconnect that leaves the user connected elsewhere moves coverage without
// moving presence, and the rooms only that connection was reading must be
// corrected too.
func TestSingleNode_PartialDisconnectCorrectsTheTargetsItCovered(t *testing.T) {
	clk := newFakeClock(time.Now())
	tracker := newTestPresenceTracker(5*time.Minute, clk)
	h := newSingleNodeHub(t, tracker)

	observer := newClient("c-a", "user-a", "ws-1", &fakeSender{})
	registerInHub(t, h, observer)
	tracker.Connect(observer.workspaceID, observer.userID, observer.id)
	subscribeInHubState(t, h, observer, TargetTypeChannel, "chan-x")

	reading := newClient("c-b1", "user-b", "ws-1", &fakeSender{})
	staying := newClient("c-b2", "user-b", "ws-1", &fakeSender{})
	for _, c := range []*Client{reading, staying} {
		registerInHub(t, h, c)
		tracker.Connect(c.workspaceID, c.userID, c.id)
	}
	subscribeInHubState(t, h, reading, TargetTypeChannel, "chan-x")
	subscribeInHubState(t, h, staying, TargetTypeChannel, "chan-y")
	drainPresenceEvents(t, h)

	h.dropClient(reading)
	drainPresenceEvents(t, h)
	h.drainReconcileRequests()

	if got := tracker.Status("ws-1", "user-b"); got != PresenceOnline {
		t.Fatalf("closing one of two sessions changed the user's presence: %q", got)
	}
	snapshot, ok := reconciledRosters(t, h)["chan-x"]
	if !ok {
		t.Fatal("the room the closed connection was reading was never corrected")
	}
	for _, user := range snapshot.Users {
		if user.UserID == "user-b" {
			t.Fatalf("the subject survived in a room they no longer cover: %+v", snapshot.Users)
		}
	}
}

// ── activity semantics end to end (SR-444-06) ────────────────────────────────

// The property the frontend's activity manager exists to produce, asserted on
// the server that decides it: real activity keeps a user online indefinitely,
// silence makes them away, and the next activity brings them back at once.
//
// Every instant here comes from the fake clock, so "past five minutes" costs
// nothing and means exactly what it says.
func TestPresenceActivity_ActivityKeepsAUserOnlineAndSilenceDoesNot(t *testing.T) {
	const awayTimeout = 5 * time.Minute
	clk := newFakeClock(time.Now())
	tracker := newTestPresenceTracker(awayTimeout, clk)
	h := newSingleNodeHub(t, tracker)

	c := newClient("c-1", "user-1", "ws-1", &fakeSender{})
	registerInHub(t, h, c)
	tracker.Connect(c.workspaceID, c.userID, c.id)
	subscribeInHubState(t, h, c, TargetTypeChannel, "chan-1")
	drainPresenceEvents(t, h)

	// Activity every four minutes for twenty-four minutes: well past the timeout
	// in total, never past it in a gap.
	for range 6 {
		clk.Advance(4 * time.Minute)
		tracker.checkAway()
		if err := h.handleClientMessage(context.Background(), c, ClientMessage{Type: ClientMessageTypePing}); err != nil {
			t.Fatalf("activity frame: %v", err)
		}
		if got := tracker.Status("ws-1", "user-1"); got != PresenceOnline {
			t.Fatalf("an active user went %q after %v of continuous use", got, awayTimeout)
		}
	}

	// Silence. One gap longer than the timeout is all it takes.
	clk.Advance(awayTimeout + time.Second)
	tracker.checkAway()
	if got := tracker.Status("ws-1", "user-1"); got != PresenceAway {
		t.Fatalf("an idle user is %q, want away", got)
	}

	// And the next real activity is enough to come back, with an event for it.
	drainPresenceEvents(t, h)
	clk.Advance(time.Second)
	if err := h.handleClientMessage(context.Background(), c, ClientMessage{Type: ClientMessageTypePing}); err != nil {
		t.Fatalf("activity frame: %v", err)
	}
	if got := tracker.Status("ws-1", "user-1"); got != PresenceOnline {
		t.Fatalf("activity left the user %q, want online", got)
	}
	announced := false
	for _, evt := range drainPresenceEvents(t, h) {
		if evt.Presence != nil && evt.Presence.UserID == "user-1" &&
			evt.Presence.State == string(PresenceOnline) {
			announced = true
		}
	}
	if !announced {
		t.Fatal("coming back from away was never announced to the room")
	}
}

func mustParseKey(t *testing.T, key string) targetKey {
	t.Helper()
	parsed, ok := parseTargetKey(key)
	if !ok {
		t.Fatalf("unparseable target key %q", key)
	}
	return parsed
}
