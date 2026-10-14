package ws

import (
	"testing"
	"time"
)

// Lifecycle of a departure (issue #798): the disconnect grace, the context
// sweep that runs beside it, and the memory a replica keeps about the people it
// serves. The sweeps are driven by hand in both orders, so nothing here depends
// on which ticker happens to fire first.

func graceFixtureWithObserver(t *testing.T) (*composeFixture, *Client) {
	t.Helper()
	f := newComposeFixture(t, testGrace)
	f.connect("c-obs", "observer", "chan-1")
	leaving, _ := f.connect("c-1", "user-1", "chan-1")
	f.hub.dropClient(leaving)
	if events := drainPresenceEvents(t, f.hub); len(events) != 0 {
		t.Fatalf("a dropped socket published at once: %+v", events)
	}
	return f, leaving
}

func assertOneDeparture(t *testing.T, f *composeFixture) {
	t.Helper()
	events := drainPresenceEvents(t, f.hub)
	if got := presenceStatesFor(events, "user-1"); len(got) != 1 || got[0] != string(PresenceOffline) {
		t.Fatalf("departure published %v, want exactly one offline", got)
	}
	if events[0].TargetID != "chan-1" {
		t.Fatalf("offline addressed to %q, want the departed room", events[0].TargetID)
	}
	if keys := f.hub.lingeringKeysFor(presenceKey{workspaceID: "ws-1", userID: "user-1"}); keys != nil {
		t.Fatalf("the consumed grace left its cover behind: %v", keys)
	}
	// Nothing is published twice, however many sweeps follow.
	f.hub.sweepPresenceContexts()
	f.tracker.expireGraces()
	if again := availabilitiesFor(drainPresenceEvents(t, f.hub), "user-1"); len(again) != 0 {
		t.Fatalf("the departure was published again: %v", again)
	}
}

func TestPresenceGraceOrder_ContextSweepBeforeExpiry(t *testing.T) {
	f, _ := graceFixtureWithObserver(t)
	f.clk.Advance(testGrace + time.Second)

	f.hub.sweepPresenceContexts()
	f.tracker.expireGraces()

	assertOneDeparture(t, f)
}

func TestPresenceGraceOrder_ExpiryBeforeContextSweep(t *testing.T) {
	f, _ := graceFixtureWithObserver(t)
	f.clk.Advance(testGrace + time.Second)

	f.tracker.expireGraces()
	f.hub.sweepPresenceContexts()

	assertOneDeparture(t, f)
}

func TestPresenceGraceOrder_ReconnectRightBeforeTheSweep(t *testing.T) {
	f, _ := graceFixtureWithObserver(t)
	f.clk.Advance(testGrace - time.Second)
	f.connect("c-2", "user-1", "chan-1")

	f.clk.Advance(2 * time.Second)
	f.hub.sweepPresenceContexts()
	f.tracker.expireGraces()

	if got := availabilitiesFor(drainPresenceEvents(t, f.hub), "user-1"); len(got) != 0 {
		t.Fatalf("a reconnected user produced %v", got)
	}
	if f.source.seen("user-1") != 0 {
		t.Fatal("a reconnect inside the grace wrote a last seen")
	}
	// The resume cover is residue: once its window passes the sweep drops it.
	f.clk.Advance(presenceResumeWindow)
	f.hub.sweepPresenceContexts()
	if keys := f.hub.lingeringKeysFor(presenceKey{workspaceID: "ws-1", userID: "user-1"}); keys != nil {
		t.Fatalf("a recovered grace kept its cover: %v", keys)
	}
}

func TestPresenceGraceOrder_SecondSessionKeepsTheUserWithoutGrace(t *testing.T) {
	f := newComposeFixture(t, testGrace)
	f.connect("c-obs", "observer", "chan-1")
	first, _ := f.connect("c-1", "user-1", "chan-1")
	f.connect("c-2", "user-1", "chan-1")

	f.hub.dropClient(first)
	f.clk.Advance(testGrace + time.Second)
	f.hub.sweepPresenceContexts()
	f.tracker.expireGraces()

	if got := availabilitiesFor(drainPresenceEvents(t, f.hub), "user-1"); len(got) != 0 {
		t.Fatalf("closing one of two sessions published %v", got)
	}
	if keys := f.hub.lingeringKeysFor(presenceKey{workspaceID: "ws-1", userID: "user-1"}); keys != nil {
		t.Fatalf("a session that is not in a grace left a cover: %v", keys)
	}
}

// A replica that no longer serves somebody forgets them, even while another
// replica still does: that other replica answers for its own sessions.
func TestPresenceMemory_ReleasedWhenTheReplicaStopsServingTheUser(t *testing.T) {
	cluster := newPresenceCluster(t)
	a := cluster.node("node-a")
	b := cluster.node("node-b")
	onA := a.join(t, "c-a", "user-1", "chan-1")
	b.join(t, "c-b", "user-1", "chan-2")

	a.hub.dropClient(onA)
	events := drainPresenceEvents(t, a.hub)
	for _, state := range presenceStatesFor(events, "user-1") {
		if state == string(PresenceOffline) {
			t.Fatalf("one replica losing its session published offline while another serves the user: %+v", events)
		}
	}
	pk := presenceKey{workspaceID: "ws-1", userID: "user-1"}
	if _, kept := a.hub.publishedState(pk); kept {
		t.Fatal("a replica kept memory of a user it no longer serves")
	}
	reads := cluster.source.readCount()
	for range 3 {
		a.hub.sweepPresenceContexts()
	}
	if cluster.source.readCount() != reads {
		t.Fatal("the sweep kept reading a user this replica no longer serves")
	}
	if cluster.source.seen("user-1") != 0 {
		t.Fatal("a departure from one replica wrote a last seen while another serves the user")
	}

	// The remaining replica still answers, and its own departure is the real one.
	b.leave(t, "c-b")
	if got := presenceStatesFor(drainPresenceEvents(t, b.hub), "user-1"); len(got) != 1 || got[0] != string(PresenceOffline) {
		t.Fatalf("the last session cluster-wide published %v, want offline", got)
	}
	if cluster.source.seen("user-1") != 1 {
		t.Fatal("the real departure did not record last seen")
	}

	// Coming back to A starts from nothing left behind.
	a.join(t, "c-a2", "user-1", "chan-1")
	if got := availabilitiesFor(a.lastEvents, "user-1"); len(got) == 0 || got[len(got)-1] != "available" {
		t.Fatalf("reconnect on A published %v", got)
	}
}
