package ws

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

// Shared reach is fenced by lifecycle generation (issue #798, HIGH-1): a
// withdrawal decided for one lifecycle of a runtime can never retract the
// next. These cases hold the authority to that, on the fake server and on a
// real Valkey, and then hold the hub to the order a reconnection becomes
// public in.

// reachGeneration is the generation the store holds for instanceID's reach,
// as another instance reads it; 0 when there is none.
func reachGeneration(t *testing.T, reader UserPresenceStore, workspace, userID, instanceID string) uint64 {
	t.Helper()
	records, err := reader.ReadUsers(context.Background(), workspace, []string{userID})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	for _, entry := range records[userID].Reach {
		if entry.InstanceID == instanceID {
			return entry.Generation
		}
	}
	return 0
}

// assertReachFencing: a1 and a2 are two clients of one runtime ("runtime-a"),
// so a2 can deliver a write a1's lifecycle has already overtaken; b reads.
func assertReachFencing(t *testing.T, a1, a2, b UserPresenceStore, workspace string) {
	t.Helper()
	ctx := context.Background()
	at := time.Unix(1_790_000_000, 0).UTC()
	write := func(ok bool, err error) bool {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if !write(a1.AssertReach(ctx, workspace, "user-1", PresenceOnline, at, 10)) {
		t.Fatal("the first lifecycle was refused")
	}
	// The reconnection registers lifecycle 11 while 10's withdrawal is late.
	if !write(a1.AssertReach(ctx, workspace, "user-1", PresenceOnline, at, 11)) {
		t.Fatal("the new lifecycle was refused")
	}
	revision := revisionIn(t, b, workspace, "user-1")
	if write(a2.WithdrawReach(ctx, workspace, "user-1", 10)) {
		t.Fatal("a withdrawal of lifecycle 10 retracted lifecycle 11")
	}
	if write(a2.WithdrawReach(ctx, workspace, "user-1", 10)) {
		t.Fatal("a duplicate stale withdrawal was applied")
	}
	if write(a2.AssertReach(ctx, workspace, "user-1", PresenceAway, at, 10)) {
		t.Fatal("a stale assertion overwrote lifecycle 11")
	}
	if got := reachGeneration(t, b, workspace, "user-1", "runtime-a"); got != 11 {
		t.Fatalf("another instance reads lifecycle %d, want 11", got)
	}
	if revisionIn(t, b, workspace, "user-1") != revision {
		t.Fatal("refused writes moved the revision")
	}
	// The real departure of 11, twice: removed once, then nothing to remove.
	for range 2 {
		if !write(a2.WithdrawReach(ctx, workspace, "user-1", 11)) {
			t.Fatal("the current lifecycle's own withdrawal was refused")
		}
	}
	if got := reachGeneration(t, b, workspace, "user-1", "runtime-a"); got != 0 {
		t.Fatalf("the departed lifecycle %d is still read", got)
	}
	if revisionIn(t, b, workspace, "user-1") != revision+1 {
		t.Fatal("one departure did not move the revision exactly once")
	}
}

func TestReachFencing_StaleWithdrawalIsRefusedByTheAuthority(t *testing.T) {
	server := newFakeValkeyServer()
	server.putLiveness("runtime-a")
	url := server.start(t)
	open := func(id string) *ValkeyPresenceDirectory {
		directory, err := NewValkeyPresenceDirectory(url, id)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(directory.Close)
		return directory
	}
	assertReachFencing(t, open("runtime-a"), open("runtime-a"), open("runtime-b"), "ws-1")
}

// TEST A at the authority, on the real Lua: the stale CAS is refused, the
// field keeps lifecycle 11, and another runtime never reads it gone.
func TestReachFencingReal_StaleWithdrawalIsRefusedByTheAuthority(t *testing.T) {
	a1 := realValkeyDirectory(t, "runtime-a")
	a2, b := realValkeyDirectory(t, "runtime-a"), realValkeyDirectory(t, "runtime-b")
	if err := a1.Heartbeat(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertReachFencing(t, a1, a2, b, realWorkspace())
}

// A reach field the script cannot read is an error, never overwritten; and a
// runtime that restarted is a different field, its old one dead and reaped.
func TestReachFencingReal_MalformedFieldAndRestartedRuntime(t *testing.T) {
	old := realValkeyDirectory(t, "runtime-old-"+realWorkspace())
	reader := realValkeyDirectory(t, "runtime-reader")
	ctx, workspace := context.Background(), realWorkspace()
	key := userPresenceKey(workspace, "user-1")
	if err := old.client.Do(ctx, old.client.B().Hset().Key(key).FieldValue().
		FieldValue(userReachFieldPrefix+old.instanceID, "online|1|x").Build()).Error(); err != nil {
		t.Fatal(err)
	}
	if _, err := old.AssertReach(ctx, workspace, "user-1", PresenceOnline, time.Now(), 1); err == nil {
		t.Fatal("a malformed reach field was overwritten")
	}
	_ = old.client.Do(ctx, old.client.B().Del().Key(key).Build()).Error()

	_ = old.Heartbeat(ctx)
	if _, err := old.AssertReach(ctx, workspace, "user-1", PresenceOnline, time.Now(), 7); err != nil {
		t.Fatal(err)
	}
	restarted := realValkeyDirectory(t, "runtime-new-"+workspace)
	_ = restarted.Heartbeat(ctx)
	if _, err := restarted.AssertReach(ctx, workspace, "user-1", PresenceOnline, time.Now(), 1); err != nil {
		t.Fatal(err)
	}
	_ = old.client.Do(ctx, old.client.B().Del().Key(directoryLivePrefix+old.instanceID).Build()).Error()
	if got := reachGeneration(t, reader, workspace, "user-1", restarted.instanceID); got != 1 {
		t.Fatalf("the restarted runtime's lifecycle = %d", got)
	}
	if got := reachGeneration(t, reader, workspace, "user-1", old.instanceID); got != 0 {
		t.Fatalf("the dead runtime's reach is still read (lifecycle %d)", got)
	}
}

// The inverted order, at the hub: the departure's withdrawal reaches the
// authority before the reconnection's lifecycle is registered (the reconnection
// arrives while the withdrawal is on the wire). Until the new lifecycle is in
// the shared reach the reconnection is not established anywhere — this
// replica's own snapshot does not show the person either — and the departure,
// overtaken, publishes nothing and records no last seen. Then the
// reconnection's publication registers it and every replica agrees.
func TestReachFencing_ReconnectionIsPublicOnlyOnceRegistered(t *testing.T) {
	forEachStore(t, func(t *testing.T, w *raceWorld) {
		w.a.tracker.grace = testGrace
		observerA := w.a.observe(t, "c-obs-a", w.chanA)
		observerB := w.b.observe(t, "c-obs-b", w.chanA)
		w.a.join(t, "c-1", w.user, w.chanA)
		w.a.leave(t, "c-1")
		drainPresenceEvents(t, w.a.hub)
		w.clk.Advance(testGrace)
		w.a.tracker.expireGraces()

		faults := wrapFaults(w.a)
		var reconnected *Client
		faults.pauseBefore("withdraw", func() {
			reconnected = newClient("c-2", w.user, "ws-1", &fakeSender{})
			registerInHub(t, w.a.hub, reconnected)
			w.a.hub.connectPresence(reconnected)
			subscribeInHubState(t, w.a.hub, reconnected, TargetTypeChannel, w.chanA)
		})
		changes := w.a.hub.takePresenceChanges()
		for _, change := range changes {
			w.a.hub.publishPresence(change)
		}
		if offlineIn(takeBroadcasts(w.a.hub), w.user) {
			t.Fatal("the overtaken departure published offline")
		}
		if w.source.seen(w.user) != 0 {
			t.Fatal("the overtaken departure recorded a last seen")
		}
		for _, snapshot := range []PresenceSnapshotResponse{
			snapshotFor(t, w.a.hub, observerA, TargetTypeChannel, w.chanA),
			snapshotFor(t, w.b.hub, observerB, TargetTypeChannel, w.chanA),
		} {
			if rosterIncludes(snapshot.Users, w.user) {
				t.Fatalf("a reconnection not yet registered is shown: %+v", snapshot.Users)
			}
		}

		// The reconnected client subscribes, as every client does after
		// connecting; its publication registers the new lifecycle.
		w.a.hub.handleSubscribed(reconnected, TargetTypeChannel, w.chanA, 0)
		published := drainPresenceEvents(t, w.a.hub)
		if got := availabilitiesFor(published, w.user); len(got) != 1 || got[0] != "available" {
			t.Fatalf("the reconnection published %v", got)
		}
		for _, snapshot := range []PresenceSnapshotResponse{
			snapshotFor(t, w.a.hub, observerA, TargetTypeChannel, w.chanA),
			snapshotFor(t, w.b.hub, observerB, TargetTypeChannel, w.chanA),
		} {
			if !rosterNamesAs(snapshot.Users, w.user, "available") {
				t.Fatalf("after registration: %+v", snapshot.Users)
			}
		}
		if g := reachGeneration(t, w.b.hub.userPresence(), "ws-1", w.user, w.a.id); g != w.a.tracker.Lifecycle("ws-1", w.user).Generation {
			t.Fatalf("the shared reach holds lifecycle %d", g)
		}
	})
}

// takeBroadcasts takes whatever was broadcast without running the fan-out.
func takeBroadcasts(h *Hub) []Event {
	var events []Event
	for {
		select {
		case req := <-h.bcast:
			events = append(events, req.event)
		default:
			return events
		}
	}
}

// Reconnecting over and over: each lifecycle is new, and none of the old ones'
// withdrawals ever reaches the current one.
func TestReachFencing_RepeatedReconnectionsKeepOnlyTheCurrentLifecycle(t *testing.T) {
	cluster := newPresenceCluster(t)
	a := cluster.nodeWithGrace("node-a", testGrace)
	a.observe(t, "c-obs", "chan-1")
	var last uint64
	for i := range 3 {
		a.join(t, fmt.Sprintf("c-%d", i), "user-1", "chan-1")
		generation := a.tracker.Lifecycle("ws-1", "user-1").Generation
		if generation <= last {
			t.Fatalf("lifecycle %d does not follow %d", generation, last)
		}
		last = generation
		if got := reachGeneration(t, cluster.directory.view("node-b"), "ws-1", "user-1", "node-a"); got != generation {
			t.Fatalf("shared reach holds %d, want %d", got, generation)
		}
		leaveAndExpire(t, cluster, a, fmt.Sprintf("c-%d", i))
		a.tracker.expireGraces()
		drainPresenceEvents(t, a.hub)
	}
	if got := reachGeneration(t, cluster.directory.view("node-b"), "ws-1", "user-1", "node-a"); got != 0 {
		t.Fatalf("a departed lifecycle (%d) is still read", got)
	}
}

// MEDIUM-5: a facts change about somebody this process holds nothing for
// allocates nothing that outlives it, and still invalidates a composition in
// flight about them.
func TestLocalUserPresence_FactsChangesAllocateNothingWithoutAnOwner(t *testing.T) {
	store := newLocalUserPresence("self", time.Now)
	ctx := context.Background()
	for i := range 1000 {
		if err := touchFacts(store, ctx, "ws-1", fmt.Sprintf("remote-%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if store.size() != 0 {
		t.Fatalf("%d entries retained for people nobody here serves", store.size())
	}

	// A composition about X has begun (it read X's revision); a change about
	// X lands; the composition cannot commit.
	stale := revisionIn(t, store, "ws-1", "user-x")
	change := FactsChange{Token: "c", Lease: time.Hour}
	_ = store.BeginFactsChange(ctx, "ws-1", []string{"user-x"}, change)
	if record := store.records[presenceKey{workspaceID: "ws-1", userID: "user-x"}]; record != nil {
		t.Fatal("the change allocated a record")
	}
	fresh := revisionIn(t, store, "ws-1", "user-x")
	if _, outcome, _ := store.Project(ctx, "ws-1", "user-x", commitOf(domain.EffectivePresence{Availability: domain.PresenceAway}, fresh, time.Now())); outcome != projectionConflict {
		t.Fatalf("a composition while the change is in flight = %v", outcome)
	}
	_ = store.EndFactsChange(ctx, "ws-1", []string{"user-x"}, change)
	available := domain.EffectivePresence{Availability: domain.PresenceAvailable}
	if _, outcome, _ := store.Project(ctx, "ws-1", "user-x", commitOf(available, stale, time.Unix(1_790_000_000, 0))); outcome != projectionConflict {
		t.Fatalf("the composition from before the change = %v", outcome)
	}
	if store.size() != 0 {
		t.Fatalf("%d entries left after the change ended", store.size())
	}

	// Somebody held here: the change moves their own revision.
	_ = assertReach(store, ctx, "ws-1", "user-y", PresenceOnline, time.Unix(1_790_000_000, 0))
	held := revisionIn(t, store, "ws-1", "user-y")
	if err := touchFacts(store, ctx, "ws-1", "user-y"); err != nil {
		t.Fatal(err)
	}
	if revisionIn(t, store, "ws-1", "user-y") == held {
		t.Fatal("a change about a held person did not move their revision")
	}
	// They leave: nothing stays.
	_ = withdrawReach(store, ctx, "ws-1", "user-y")
	offline := domain.EffectivePresence{Availability: domain.PresenceOffline}
	_, _, _ = store.Project(ctx, "ws-1", "user-y", commitOf(offline, revisionIn(t, store, "ws-1", "user-y"), time.Unix(1_790_000_001, 0)))
	if store.size() != 0 {
		t.Fatalf("%d entries left after the owner left", store.size())
	}
}
