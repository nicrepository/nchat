package ws

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Reachability has one authority (issue #798, HIGH-B): the person's own
// record. A target roster says who to deliver to in a conversation; an entry
// in it written by an instance that also keeps per-user reach is a repeat, and
// a stale repeat must never outvote the record. The bridge to instances from
// before #798 reads only their entries, and only while it is on.

func chan1Key() string {
	return targetKey{workspaceID: "ws-1", targetType: TargetTypeChannel, targetID: "chan-1"}.String()
}

// rosterState is what the target roster holds for one instance's assertion.
func (d *fakeDirectory) rosterState(key, userID, instanceID string) PresenceStatus {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.rosters[key][directoryField(userID, instanceID)].State
}

// legacyServes makes a live instance from before #798 assert userID into
// chan-1 — the only place such an instance records anything.
func legacyServes(t *testing.T, cluster *presenceCluster, instanceID, userID string, state PresenceStatus) {
	t.Helper()
	cluster.directory.markLegacy(instanceID)
	view := cluster.directory.view(instanceID)
	_ = view.Heartbeat(context.Background())
	if err := view.Record(context.Background(), DirectoryEntry{UserID: userID, State: state, At: cluster.clk.Now()}, []string{chan1Key()}); err != nil {
		t.Fatalf("legacy record: %v", err)
	}
}

// TEST B, exactly as reproduced: the per-user write of Away succeeds, the
// target write fails and leaves Online behind, another replica snapshots.
func TestReachAuthority_StaleTargetRosterDoesNotOutvoteTheRecord(t *testing.T) {
	cluster := newPresenceCluster(t)
	a, b := cluster.node("node-a"), cluster.node("node-b")
	observer := b.observe(t, "c-obs", "chan-1")
	a.join(t, "c-a", "user-1", "chan-1")

	cluster.clk.Advance(6 * time.Minute)
	cluster.directory.failNextRecord(1)
	a.tracker.checkAway()
	drainPresenceEvents(t, a.hub)
	if state := cluster.directory.rosterState(chan1Key(), "user-1", "node-a"); state != PresenceOnline {
		t.Fatalf("the setup needs a stale roster, it holds %q", state)
	}
	if reach := cluster.directory.reachOf("user-1"); reach["node-a"] != PresenceAway {
		t.Fatalf("the setup needs the record at away: %v", reach)
	}

	if users := snapshotFor(t, b.hub, observer, TargetTypeChannel, "chan-1").Users; !rosterNamesAs(users, "user-1", "away") {
		t.Fatalf("snapshot = %+v, want the record's away", users)
	}
}

// A: modern Online, a live legacy instance's Away for the same person — the
// person is reachable and active, so Available.
func TestReachAuthority_ModernOnlineWithLegacyAway(t *testing.T) {
	cluster := newPresenceCluster(t)
	a, b := cluster.node("node-a"), cluster.node("node-b")
	observer := b.observe(t, "c-obs", "chan-1")
	a.join(t, "c-a", "user-1", "chan-1")
	legacyServes(t, cluster, "node-old", "user-1", PresenceAway)
	if users := snapshotFor(t, b.hub, observer, TargetTypeChannel, "chan-1").Users; !rosterNamesAs(users, "user-1", "available") {
		t.Fatalf("snapshot = %+v", users)
	}
}

// B: modern Away, and Online evidence that is stale — written by a legacy
// instance that has since died. Away.
func TestReachAuthority_ModernAwayWithStaleLegacyOnline(t *testing.T) {
	cluster := newPresenceCluster(t)
	a, b := cluster.node("node-a"), cluster.node("node-b")
	observer := b.observe(t, "c-obs", "chan-1")
	a.join(t, "c-a", "user-1", "chan-1")
	legacyServes(t, cluster, "node-old", "user-1", PresenceOnline)
	cluster.directory.killInstance("node-old")
	cluster.clk.Advance(6 * time.Minute)
	a.tracker.checkAway()
	drainPresenceEvents(t, a.hub)
	if users := snapshotFor(t, b.hub, observer, TargetTypeChannel, "chan-1").Users; !rosterNamesAs(users, "user-1", "away") {
		t.Fatalf("snapshot = %+v", users)
	}
}

// A live legacy instance that does serve an active session of the person is
// another device, and the multi-device rule holds across versions: an idle
// modern session does not hide it. It adds a session; it replaces none.
func TestReachAuthority_LiveLegacySessionIsAnotherDevice(t *testing.T) {
	cluster := newPresenceCluster(t)
	a, b := cluster.node("node-a"), cluster.node("node-b")
	observer := b.observe(t, "c-obs", "chan-1")
	a.join(t, "c-a", "user-1", "chan-1")
	legacyServes(t, cluster, "node-old", "user-1", PresenceOnline)
	cluster.clk.Advance(6 * time.Minute)
	_ = cluster.directory.view("node-old").Heartbeat(context.Background())
	a.tracker.checkAway()
	if events := drainPresenceEvents(t, a.hub); len(presenceStatesFor(events, "user-1")) != 0 {
		t.Fatalf("an idle modern session hid an active legacy one: %+v", events)
	}
	if users := snapshotFor(t, b.hub, observer, TargetTypeChannel, "chan-1").Users; !rosterNamesAs(users, "user-1", "available") {
		t.Fatalf("snapshot = %+v", users)
	}
}

// C and E: somebody only a legacy instance serves is shown through the
// bridge during the mixed phase, and not at all once it is off.
func TestReachAuthority_LegacyOnlyPersonThroughTheBridge(t *testing.T) {
	for _, bridge := range []bool{true, false} {
		cluster := newPresenceCluster(t)
		b := cluster.node("node-b")
		WithLegacyPresenceBridge(bridge)(b.hub)
		observer := b.observe(t, "c-obs", "chan-1")
		legacyServes(t, cluster, "node-old", "user-2", PresenceOnline)
		users := snapshotFor(t, b.hub, observer, TargetTypeChannel, "chan-1").Users
		if shown := rosterNamesAs(users, "user-2", "available"); shown != bridge {
			t.Fatalf("bridge %v: legacy-only person shown = %v (%+v)", bridge, shown, users)
		}
	}
}

// D: a modern session and a legacy one at once. The modern instance leaves and
// its roster entry is left behind (the forget failed); the legacy instance
// dies. Nothing modern is counted twice: the person is gone.
func TestReachAuthority_ModernAndLegacyTogetherCountEachSessionOnce(t *testing.T) {
	cluster := newPresenceCluster(t)
	a, b := cluster.node("node-a"), cluster.node("node-b")
	observer := b.observe(t, "c-obs", "chan-1")
	a.join(t, "c-a", "user-1", "chan-1")
	legacyServes(t, cluster, "node-old", "user-1", PresenceOnline)

	cluster.directory.failForgetWith(errors.New("valkey unavailable"))
	a.leave(t, "c-a")
	drainPresenceEvents(t, a.hub)
	cluster.directory.failForgetWith(nil)
	cluster.directory.killInstance("node-old")
	if state := cluster.directory.rosterState(chan1Key(), "user-1", "node-a"); state != PresenceOnline {
		t.Fatalf("the setup needs node-a's stale roster entry, it holds %q", state)
	}
	if users := snapshotFor(t, b.hub, observer, TargetTypeChannel, "chan-1").Users; rosterIncludes(users, "user-1") {
		t.Fatalf("a stale modern roster entry kept the person present: %+v", users)
	}
}

// G: the roster a publication would consult for legacy evidence does not
// exist; the modern aggregate is unaffected.
func TestReachAuthority_MissingRosterLeavesTheModernAggregateAlone(t *testing.T) {
	cluster := newPresenceCluster(t)
	a, b := cluster.node("node-a"), cluster.node("node-b")
	a.join(t, "c-a", "user-1", "chan-1")
	cluster.clk.Advance(4 * time.Minute)
	b.join(t, "c-b", "user-1", "chan-2")
	cluster.directory.mu.Lock()
	delete(cluster.directory.rosters, chan1Key())
	cluster.directory.mu.Unlock()

	cluster.clk.Advance(2 * time.Minute)
	a.tracker.checkAway()
	if got := presenceStatesFor(drainPresenceEvents(t, a.hub), "user-1"); len(got) != 0 {
		t.Fatalf("published %v while the person is active on another replica", got)
	}
}
