package ws

import (
	"errors"
	"testing"
	"time"
)

// A person's reach is the cluster's answer about the person (issue #798) — not
// the answer of whichever conversation one of their sessions happened to
// subscribe to. Every case here gives two replicas sessions of the same person
// that share no conversation, or none at all.

// observe subscribes an observer through the given replica and returns it.
func (m *clusterMember) observe(t *testing.T, clientID, target string) *Client {
	t.Helper()
	return m.join(t, clientID, "observer-"+clientID, target)
}

func TestPresenceClusterReach_IdleReplicaDoesNotHideAnActiveOneInADisjointTarget(t *testing.T) {
	cluster := newPresenceCluster(t)
	a, b := cluster.node("node-a"), cluster.node("node-b")
	observer := a.observe(t, "c-obs", "chan-1")
	a.join(t, "c-a", "user-1", "chan-1")
	cluster.clk.Advance(4 * time.Minute)
	b.join(t, "c-b", "user-1", "chan-2")

	cluster.clk.Advance(2 * time.Minute)
	a.tracker.checkAway()
	if got := availabilitiesFor(drainPresenceEvents(t, a.hub), "user-1"); len(got) != 0 {
		t.Fatalf("an idle session published %v while the same person is active elsewhere", got)
	}
	snapshot := snapshotFor(t, a.hub, observer, TargetTypeChannel, "chan-1")
	if !rosterNamesAs(snapshot.Users, "user-1", "available") {
		t.Fatalf("the idle replica's own target reads %+v, want available", snapshot.Users)
	}
}

func TestPresenceClusterReach_ASessionWithNoSubscriptionStillCounts(t *testing.T) {
	cluster := newPresenceCluster(t)
	a, b := cluster.node("node-a"), cluster.node("node-b")
	b.observe(t, "c-obs", "chan-2")
	a.join(t, "c-a", "user-1") // connected, subscribed to nothing yet
	cluster.clk.Advance(4 * time.Minute)
	b.join(t, "c-b", "user-1", "chan-2")

	// A's session is the active one; B's goes idle first.
	cluster.clk.Advance(2 * time.Minute)
	a.tracker.RecordActivity("ws-1", "user-1", "c-a")
	b.tracker.checkAway()
	if got := availabilitiesFor(drainPresenceEvents(t, b.hub), "user-1"); len(got) != 0 {
		t.Fatalf("away was published %v while a session with no subscription is active", got)
	}
}

func TestPresenceClusterReach_UnreadableAggregateConcludesNothing(t *testing.T) {
	cluster := newPresenceCluster(t)
	a, b := cluster.node("node-a"), cluster.node("node-b")
	a.observe(t, "c-obs", "chan-1")
	a.join(t, "c-a", "user-1", "chan-1")
	b.join(t, "c-b", "user-1", "chan-2")
	metrics := newRecordingPresenceMetrics()
	a.hub.presenceMetrics = metrics

	cluster.directory.failUserStore(errors.New("valkey unavailable"))
	a.leave(t, "c-a")
	if got := availabilitiesFor(drainPresenceEvents(t, a.hub), "user-1"); len(got) != 0 {
		t.Fatalf("a departure was published %v without knowing the cluster", got)
	}
	if cluster.source.seen("user-1") != 0 {
		t.Fatal("last seen was written on a departure nobody could confirm")
	}
	if metrics.deferredCount("reach_write") == 0 {
		t.Fatalf("the deferral was not observable: %+v", metrics.deferred)
	}

	// Recovery: the sweep retries the owed publication against the real answer.
	cluster.directory.failUserStore(nil)
	a.hub.sweepPresenceContexts()
	if got := presenceStatesFor(drainPresenceEvents(t, a.hub), "user-1"); len(got) != 0 {
		t.Fatalf("the retry published %v although another replica still serves the user", got)
	}
	if _, kept := a.hub.publishedState(presenceKey{workspaceID: "ws-1", userID: "user-1"}); kept {
		t.Fatal("a settled retry left memory behind")
	}

	// And when the other session goes too, the departure is real.
	b.leave(t, "c-b")
	if got := presenceStatesFor(drainPresenceEvents(t, b.hub), "user-1"); len(got) != 1 || got[0] != string(PresenceOffline) {
		t.Fatalf("the real departure published %v", got)
	}
	if cluster.source.seen("user-1") != 1 {
		t.Fatal("the real departure did not record last seen exactly once")
	}
}

func TestPresenceClusterReach_UnreadableAggregateOwesTheDepartureUntilItCanBeComposed(t *testing.T) {
	cluster := newPresenceCluster(t)
	a := cluster.node("node-a")
	a.observe(t, "c-obs", "chan-1")
	a.join(t, "c-a", "user-1", "chan-1")

	cluster.directory.failUserStore(errors.New("valkey unavailable"))
	a.leave(t, "c-a")
	if got := availabilitiesFor(drainPresenceEvents(t, a.hub), "user-1"); len(got) != 0 {
		t.Fatalf("published %v blind", got)
	}
	a.hub.sweepPresenceContexts()
	if got := availabilitiesFor(drainPresenceEvents(t, a.hub), "user-1"); len(got) != 0 {
		t.Fatalf("a retry against the same failure published %v", got)
	}

	cluster.directory.failUserStore(nil)
	a.hub.sweepPresenceContexts()
	events := drainPresenceEvents(t, a.hub)
	if got := presenceStatesFor(events, "user-1"); len(got) != 1 || got[0] != string(PresenceOffline) {
		t.Fatalf("the owed departure published %v", got)
	}
	if events[0].TargetID != "chan-1" {
		t.Fatalf("the owed departure lost its audience: %q", events[0].TargetID)
	}
	if cluster.source.seen("user-1") != 1 {
		t.Fatal("the confirmed departure did not record last seen")
	}
}

func TestPresenceClusterReach_DeadReplicaIsNotBelieved(t *testing.T) {
	cluster := newPresenceCluster(t)
	a, b := cluster.node("node-a"), cluster.node("node-b")
	a.observe(t, "c-obs", "chan-1")
	a.join(t, "c-a", "user-1", "chan-1")
	cluster.clk.Advance(4 * time.Minute)
	b.join(t, "c-b", "user-1", "chan-2")

	cluster.directory.killInstance("node-b")
	cluster.clk.Advance(2 * time.Minute)
	a.tracker.checkAway()
	if got := availabilitiesFor(drainPresenceEvents(t, a.hub), "user-1"); len(got) != 1 || got[0] != "away" {
		t.Fatalf("with the active replica dead the idle one published %v, want away", got)
	}
}

func TestPresenceClusterReach_WorkspacesNeverMeet(t *testing.T) {
	cluster := newPresenceCluster(t)
	a, b := cluster.node("node-a"), cluster.node("node-b")
	a.observe(t, "c-obs", "chan-1")
	a.join(t, "c-a", "user-1", "chan-1")
	other := newClient("c-b", "user-1", "ws-2", &fakeSender{})
	registerInHub(t, b.hub, other)
	b.hub.connectPresence(other)
	drainPresenceEvents(t, b.hub)

	a.leave(t, "c-a")
	if got := presenceStatesFor(drainPresenceEvents(t, a.hub), "user-1"); len(got) != 1 || got[0] != string(PresenceOffline) {
		t.Fatalf("a session in another workspace kept the user present: %v", got)
	}
}

func rosterNamesAs(users []PresencePayload, userID, availability string) bool {
	for _, user := range users {
		if user.UserID == userID {
			return user.Availability == availability
		}
	}
	return false
}
