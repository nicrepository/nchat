package ws

import (
	"slices"
	"sync"
	"testing"
	"time"
)

// A decision taken under the tracker's lock is acted on after the lock is
// released (issue #798, HIGH-A). Every test here holds that gap open with a
// barrier — never a sleep — and lets a connection arrive inside it: the stale
// departure must not withdraw the new connection's reach, publish offline, or
// record a last seen.

// parkTrackerObserver makes the tracker's next report stop before the hub
// sees it, and returns a channel signalled once it has, and the release.
func parkTrackerObserver(tracker *PresenceTracker) (parked <-chan struct{}, release func()) {
	tracker.mu.RLock()
	original := tracker.observer
	tracker.mu.RUnlock()
	reached, resume := make(chan struct{}), make(chan struct{})
	var once sync.Once
	tracker.SetObserver(func(workspaceID, userID string, status PresenceStatus, at time.Time, generation uint64) {
		once.Do(func() {
			close(reached)
			<-resume
		})
		original(workspaceID, userID, status, at, generation)
	})
	return reached, func() { close(resume) }
}

// reachOf is every instance's per-user reach for a person, as stored.
func (d *fakeDirectory) reachOf(userID string) map[string]PresenceStatus {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := map[string]PresenceStatus{}
	for instanceID, entry := range d.users[presenceKey{workspaceID: "ws-1", userID: userID}] {
		out[instanceID] = entry.State
	}
	return out
}

func offlineIn(events []Event, userID string) bool {
	for _, state := range presenceStatesFor(events, userID) {
		if state == string(PresenceOffline) {
			return true
		}
	}
	return false
}

// leaveAndExpire drops a session and lets its grace run out on the clock,
// without sweeping.
func leaveAndExpire(t *testing.T, cluster *presenceCluster, member *clusterMember, clientID string) {
	t.Helper()
	member.leave(t, clientID)
	if events := drainPresenceEvents(t, member.hub); len(events) != 0 {
		t.Fatalf("a departure inside its grace published %+v", events)
	}
	cluster.clk.Advance(testGrace)
}

// TEST A: the grace expires, the tracker decides offline and releases its
// lock; the person reconnects before the hub hears of it.
func TestStaleGrace_ReconnectBetweenTheTrackerAndItsCallback(t *testing.T) {
	cluster := newPresenceCluster(t)
	a := cluster.nodeWithGrace("node-a", testGrace)
	observer := a.observe(t, "c-obs", "chan-1")
	a.join(t, "c-1", "user-1", "chan-1")
	leaveAndExpire(t, cluster, a, "c-1")

	parked, release := parkTrackerObserver(a.tracker)
	expired := make(chan struct{})
	go func() {
		a.tracker.expireGraces()
		close(expired)
	}()
	<-parked
	a.join(t, "c-2", "user-1", "chan-1")
	release()
	<-expired

	events := slices.Concat(a.lastEvents, drainPresenceEvents(t, a.hub))
	if offlineIn(events, "user-1") {
		t.Fatalf("a stale departure published offline: %+v", events)
	}
	if reach := cluster.directory.reachOf("user-1"); reach["node-a"] != PresenceOnline {
		t.Fatalf("the new connection's reach was withdrawn: %v", reach)
	}
	if cluster.source.seen("user-1") != 0 {
		t.Fatal("a stale departure recorded a last seen")
	}
	if !rosterNamesAs(snapshotFor(t, a.hub, observer, TargetTypeChannel, "chan-1").Users, "user-1", "available") {
		t.Fatal("the reconnected person is not available")
	}
	// Nothing of the old departure stays behind once the resume window ends.
	cluster.clk.Advance(presenceResumeWindow + time.Second)
	a.hub.sweepPresenceContexts()
	if keys := a.hub.lingeringKeysFor(presenceKey{workspaceID: "ws-1", userID: "user-1"}); keys != nil {
		t.Fatalf("the old grace's cover leaked: %v", keys)
	}
	if entry, _ := a.hub.publishedState(presenceKey{workspaceID: "ws-1", userID: "user-1"}); entry.owed {
		t.Fatal("the stale departure left a publication owed")
	}
}

// The departure was decided and queued while the person was gone, the fan-out
// took it, and only then did they reconnect.
func TestStaleGrace_QueuedDepartureTakenBeforeTheReconnection(t *testing.T) {
	cluster := newPresenceCluster(t)
	a := cluster.nodeWithGrace("node-a", testGrace)
	a.observe(t, "c-obs", "chan-1")
	a.join(t, "c-1", "user-1", "chan-1")
	leaveAndExpire(t, cluster, a, "c-1")
	a.tracker.expireGraces()

	stale := a.hub.takePresenceChanges()
	a.join(t, "c-2", "user-1", "chan-1")
	for _, change := range stale {
		a.hub.publishPresence(change)
	}
	events := slices.Concat(a.lastEvents, drainPresenceEvents(t, a.hub))
	if offlineIn(events, "user-1") {
		t.Fatalf("a queued stale departure published offline: %+v", events)
	}
	if reach := cluster.directory.reachOf("user-1"); reach["node-a"] != PresenceOnline {
		t.Fatalf("reach = %v", reach)
	}
	if cluster.source.seen("user-1") != 0 {
		t.Fatal("a stale departure recorded a last seen")
	}
}

// The narrowest gap: the departure has been revalidated and is composing when
// the person reconnects. It may still land its projection, but it publishes
// nothing and records nothing, and the reconnection's own change — queued
// behind it on the same fan-out — puts reach and projection right.
func TestStaleGrace_ReconnectWhileTheDepartureIsComposing(t *testing.T) {
	cluster := newPresenceCluster(t)
	a := cluster.nodeWithGrace("node-a", testGrace)
	observer := a.observe(t, "c-obs", "chan-1")
	_, joined := a.join(t, "c-1", "user-1", "chan-1"), a.lastEvents
	before := presenceVersionOf(t, joined, "user-1")
	leaveAndExpire(t, cluster, a, "c-1")
	a.tracker.expireGraces()

	var once sync.Once
	cluster.directory.mu.Lock()
	cluster.directory.beforeProject = func() {
		once.Do(func() {
			c := newClient("c-2", "user-1", "ws-1", &fakeSender{})
			registerInHub(t, a.hub, c)
			a.hub.connectPresence(c)
			subscribeInHubState(t, a.hub, c, TargetTypeChannel, "chan-1")
		})
	}
	cluster.directory.mu.Unlock()

	if events := drainPresenceEvents(t, a.hub); offlineIn(events, "user-1") {
		t.Fatalf("a departure overtaken while composing published offline: %+v", events)
	}
	if cluster.source.seen("user-1") != 0 {
		t.Fatal("a departure overtaken while composing recorded a last seen")
	}
	cluster.directory.mu.Lock()
	cluster.directory.beforeProject = nil
	cluster.directory.mu.Unlock()

	settled := drainPresenceEvents(t, a.hub)
	if reach := cluster.directory.reachOf("user-1"); reach["node-a"] != PresenceOnline {
		t.Fatalf("the reconnection did not put its reach back: %v", reach)
	}
	snapshot := snapshotFor(t, a.hub, observer, TargetTypeChannel, "chan-1")
	_, version := presenceVersion(t, snapshot.Users, "user-1")
	if !rosterNamesAs(snapshot.Users, "user-1", "available") || version <= before {
		t.Fatalf("after settling: %+v (events %+v), want available newer than %s", snapshot.Users, settled, before)
	}
}

func presenceVersionOf(t *testing.T, events []Event, userID string) string {
	t.Helper()
	for i := len(events) - 1; i >= 0; i-- {
		if p := events[i].Presence; p != nil && p.UserID == userID {
			return p.UpdatedAt
		}
	}
	t.Fatalf("no event about %s in %+v", userID, events)
	return ""
}

// A person with two sessions here loses one: no grace, no departure.
func TestStaleGrace_TwoLocalSessionsOneLeaves(t *testing.T) {
	cluster := newPresenceCluster(t)
	a := cluster.nodeWithGrace("node-a", testGrace)
	a.observe(t, "c-obs", "chan-1")
	a.join(t, "c-1", "user-1", "chan-1")
	a.join(t, "c-2", "user-1", "chan-1")
	a.leave(t, "c-1")
	cluster.clk.Advance(testGrace)
	a.tracker.expireGraces()
	if events := drainPresenceEvents(t, a.hub); offlineIn(events, "user-1") {
		t.Fatalf("a remaining local session was declared offline: %+v", events)
	}
}

// A session on another replica, and a local reconnection inside the gap: the
// remote reach is untouched and nothing about a departure is published.
func TestStaleGrace_RemoteSessionAndLocalReconnection(t *testing.T) {
	cluster := newPresenceCluster(t)
	a, b := cluster.nodeWithGrace("node-a", testGrace), cluster.node("node-b")
	a.observe(t, "c-obs", "chan-1")
	a.join(t, "c-1", "user-1", "chan-1")
	b.join(t, "c-b", "user-1", "chan-2")
	leaveAndExpire(t, cluster, a, "c-1")

	parked, release := parkTrackerObserver(a.tracker)
	expired := make(chan struct{})
	go func() {
		a.tracker.expireGraces()
		close(expired)
	}()
	<-parked
	a.join(t, "c-2", "user-1", "chan-1")
	release()
	<-expired
	events := slices.Concat(a.lastEvents, drainPresenceEvents(t, a.hub))
	if offlineIn(events, "user-1") {
		t.Fatalf("published offline: %+v", events)
	}
	if reach := cluster.directory.reachOf("user-1"); reach["node-a"] != PresenceOnline || reach["node-b"] != PresenceOnline {
		t.Fatalf("reach = %v", reach)
	}
}

// The same expiry reported twice is one departure; a report that arrives after
// a reconnection is none.
func TestStaleGrace_DuplicateCallbacks(t *testing.T) {
	cluster := newPresenceCluster(t)
	a := cluster.nodeWithGrace("node-a", testGrace)
	a.observe(t, "c-obs", "chan-1")
	a.join(t, "c-1", "user-1", "chan-1")
	ended := a.tracker.Lifecycle("ws-1", "user-1").Generation
	leaveAndExpire(t, cluster, a, "c-1")
	a.tracker.mu.RLock()
	report := a.tracker.observer
	a.tracker.mu.RUnlock()

	a.tracker.expireGraces()
	report("ws-1", "user-1", PresenceOffline, cluster.clk.Now(), ended)
	events := drainPresenceEvents(t, a.hub)
	if got := presenceStatesFor(events, "user-1"); len(got) != 1 || got[0] != string(PresenceOffline) {
		t.Fatalf("one expiry reported twice published %v", got)
	}
	if cluster.source.seen("user-1") != 1 {
		t.Fatalf("one departure recorded %d last seen(s)", cluster.source.seen("user-1"))
	}

	a.join(t, "c-2", "user-1", "chan-1")
	report("ws-1", "user-1", PresenceOffline, cluster.clk.Now(), ended)
	if events := drainPresenceEvents(t, a.hub); offlineIn(events, "user-1") {
		t.Fatalf("a late duplicate after the reconnection published %+v", events)
	}
	if reach := cluster.directory.reachOf("user-1"); reach["node-a"] != PresenceOnline {
		t.Fatalf("reach = %v", reach)
	}
}
