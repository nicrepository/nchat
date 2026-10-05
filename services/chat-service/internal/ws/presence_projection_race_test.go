package ws

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

// A projection is committed against the revision its facts were read at
// (issue #798, HIGH-C). Each test parks one composition after it has read its
// facts, lands a newer fact through another path, and resumes it: the stale
// composition must conflict, recompose, and agree — never win with a newer
// version.

// raceWorld is two replicas over one store, the fake one or a real Valkey.
type raceWorld struct {
	clk    *fakeClock
	source *fakePresenceContext
	a, b   *clusterMember
	user   string
	chanA  string
	chanB  string
	// kill makes an instance dead in the store's eyes.
	kill func(instanceID string)
}

func newRaceWorld(t *testing.T, directory func(id string) PresenceDirectory, user, chanA, chanB string) *raceWorld {
	t.Helper()
	// The real clock's instant: a world on a real Valkey then agrees with the
	// authority's clock, which judges every deadline at the commit.
	return newRaceWorldAt(t, time.Now().UTC(), directory, user, chanA, chanB)
}

// newRaceWorldAt is a world whose application clock — the hubs' — starts at
// origin, however far that is from the stores' and the database's clocks.
func newRaceWorldAt(
	t *testing.T, origin time.Time, directory func(id string) PresenceDirectory, user, chanA, chanB string,
) *raceWorld {
	t.Helper()
	w := &raceWorld{
		clk:    newFakeClock(origin),
		source: newFakePresenceContext(),
		user:   user, chanA: chanA, chanB: chanB,
	}
	w.source.clock = w.clk.Now
	member := func(id string) *clusterMember {
		dir := directory(id)
		_ = dir.Heartbeat(context.Background())
		tracker := newTestPresenceTracker(5*time.Minute, w.clk)
		hub := newClusterNode(id, dir, tracker)
		hub.presenceContext = w.source
		return &clusterMember{id: id, hub: hub, tracker: tracker, clients: map[string]*Client{}}
	}
	w.a, w.b = member("node-a-"+user), member("node-b-"+user)
	return w
}

// runStaleCompositionRace is TEST 1: A reads Available and stops; B writes DND
// and publishes it; A resumes with its stale facts.
func runStaleCompositionRace(t *testing.T, w *raceWorld) {
	t.Helper()
	w.a.observe(t, "c-obs-a", w.chanA)
	w.a.join(t, "c-a", w.user, w.chanA)
	w.b.join(t, "c-b", w.user, w.chanB)
	before := presenceVersionOf(t, w.a.lastEvents, w.user)

	parked, release := w.source.parkNextRead()
	w.a.hub.RefreshPresence("ws-1", w.user)
	resumed := make(chan []Event)
	go func() { resumed <- drainPresenceEvents(t, w.a.hub) }()
	<-parked

	w.clk.Advance(time.Second)
	w.source.set(w.user, manual(domain.PresenceManualDoNotDisturb, w.clk.Now().Add(time.Hour)))
	w.b.hub.PublishPresenceSettingsChanged(context.Background(), "ws-1", w.user)
	byB := drainPresenceEvents(t, w.b.hub)
	if got := availabilitiesFor(byB, w.user); len(got) != 1 || got[0] != "dnd" {
		t.Fatalf("B published %v, want dnd", got)
	}
	release()
	byA := <-resumed

	if got := availabilitiesFor(byA, w.user); len(got) != 1 || got[0] != "dnd" {
		t.Fatalf("A resumed with stale facts and published %v, want only dnd", got)
	}
	if byA[0].Presence.UpdatedAt != byB[0].Presence.UpdatedAt || byB[0].Presence.UpdatedAt <= before {
		t.Fatalf("versions: before %s, B %s, A %s — want A to carry B's, after before",
			before, byB[0].Presence.UpdatedAt, byA[0].Presence.UpdatedAt)
	}
	records, err := w.a.hub.userPresence().ReadUsers(context.Background(), "ws-1", []string{w.user})
	if err != nil || records[w.user].Projection == nil || records[w.user].Projection.Effective.Availability != domain.PresenceDoNotDisturb {
		t.Fatalf("committed = %+v %v, want dnd", records[w.user].Projection, err)
	}
}

func TestStaleProjection_ResumedCompositionRecomposes(t *testing.T) {
	shared := newFakeDirectory()
	w := newRaceWorld(t, func(id string) PresenceDirectory { return shared.view(id) }, "user-1", "chan-1", "chan-2")
	shared.clock = w.clk.Now
	runStaleCompositionRace(t, w)
}

// The same race against the real Lua and a real Valkey.
func TestStaleProjectionReal_ResumedCompositionRecomposes(t *testing.T) {
	url := os.Getenv("CHAT_TEST_VALKEY_URL")
	if url == "" {
		t.Skip("CHAT_TEST_VALKEY_URL is not set")
	}
	run := fmt.Sprintf("%d", time.Now().UnixNano())
	w := newRaceWorld(t, func(id string) PresenceDirectory {
		return realValkeyDirectory(t, id)
	}, "user-"+run, "chan-a-"+run, "chan-b-"+run)
	runStaleCompositionRace(t, w)
}

// TEST 4: a call starts while an away transition is composing with the facts
// from before it. The call prevents away; away is never published.
func TestStaleProjection_CallEnterRacingAnAwayTransition(t *testing.T) {
	f := newComposeFixture(t, 0)
	f.connect("c-obs", "observer", "chan-1")
	f.connect("c-1", "user-1", "chan-1")

	f.clk.Advance(6 * time.Minute)
	f.tracker.RecordActivity("ws-1", "observer", "c-obs") // only user-1 goes idle
	parked, release := f.source.parkNextRead()
	f.tracker.checkAway()
	resumed := make(chan []Event)
	go func() { resumed <- drainPresenceEvents(t, f.hub) }()
	<-parked
	// The call command's own path: the participation is written inside the
	// presence facts boundary, then the participant is refreshed.
	if err := f.hub.ChangePresenceFacts(t.Context(), "ws-1", []string{"user-1"}, func(context.Context) error {
		f.source.set("user-1", domain.PresenceContext{Activity: domain.PresenceActivityInCall})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	f.hub.refreshCallParticipants(domain.Call{WorkspaceID: "ws-1", CallerID: "user-1"})
	release()
	events := append(<-resumed, drainPresenceEvents(t, f.hub)...)

	got := availabilitiesFor(events, "user-1")
	for _, availability := range got {
		if availability == "away" {
			t.Fatalf("a stale away was published: %v", got)
		}
	}
	if len(got) == 0 || got[len(got)-1] != "busy" {
		t.Fatalf("published %v, want busy", got)
	}
}

// TEST 3: a manual state expires and a snapshot is the first to compose it.
// The serving replica's sweep then agrees with the snapshot's version instead
// of minting another.
func TestStaleProjection_ExpiryFirstSeenByASnapshot(t *testing.T) {
	f := newComposeFixture(t, 0)
	observer, _ := f.connect("c-obs", "observer", "chan-1")
	f.source.set("user-1", manual(domain.PresenceManualDoNotDisturb, f.clk.Now().Add(time.Minute)))
	f.connect("c-1", "user-1", "chan-1")

	f.clk.Advance(2 * time.Minute)
	availability, version := presenceVersion(t, snapshotFor(t, f.hub, observer, TargetTypeChannel, "chan-1").Users, "user-1")
	if availability != "available" {
		t.Fatalf("snapshot after expiry = %s", availability)
	}
	f.source.clear("user-1")
	f.hub.sweepPresenceContexts()
	events := drainPresenceEvents(t, f.hub)
	if got := availabilitiesFor(events, "user-1"); len(got) != 1 || got[0] != "available" || events[0].Presence.UpdatedAt != version {
		t.Fatalf("sweep published %+v, want available at the snapshot's %s", events, version)
	}
}

// TEST 5 / TEST D: a person chooses to appear offline and no hint reaches this
// replica. The snapshot commits their public offline before leaving them out,
// so the projection, the public last seen and the next event all agree.
func TestStaleProjection_HiddenSnapshotCommitsBeforeOmitting(t *testing.T) {
	f := newComposeFixture(t, 0)
	observer, _ := f.connect("c-obs", "observer", "chan-1")
	_, joined := f.connect("c-1", "user-1", "chan-1")
	shown := presenceVersionOf(t, joined, "user-1")

	f.clk.Advance(time.Second)
	f.source.set("user-1", manual(domain.PresenceManualAppearOffline, f.clk.Now().Add(time.Hour)))
	snapshot := snapshotFor(t, f.hub, observer, TargetTypeChannel, "chan-1")
	if rosterIncludes(snapshot.Users, "user-1") {
		t.Fatalf("a hidden person was named: %+v", snapshot.Users)
	}
	at, offline, err := f.hub.PublicLastSeen(t.Context(), "ws-1", "user-1")
	if err != nil || !offline || formatPresenceTime(at) <= shown {
		t.Fatalf("public last seen = %v %v %v, want an offline newer than %s", at, offline, err, shown)
	}
	f.hub.RefreshPresence("ws-1", "user-1")
	events := drainPresenceEvents(t, f.hub)
	if got := availabilitiesFor(events, "user-1"); len(got) != 1 || got[0] != "offline" || events[0].Presence.UpdatedAt != formatPresenceTime(at) {
		t.Fatalf("the replica's own publication = %+v, want offline at %s", events, formatPresenceTime(at))
	}
}

// TEST 6 at the hub: two replicas composing the same change publish one
// version between them.
func TestStaleProjection_IdenticalCompositionsShareOneVersion(t *testing.T) {
	cluster := newPresenceCluster(t)
	a, b := cluster.node("node-a"), cluster.node("node-b")
	a.join(t, "c-a", "user-1", "chan-1")
	b.join(t, "c-b", "user-1", "chan-2")
	cluster.source.set("user-1", manual(domain.PresenceManualBusy, cluster.clk.Now().Add(time.Hour)))
	a.hub.RefreshPresence("ws-1", "user-1")
	b.hub.RefreshPresence("ws-1", "user-1")
	fromA, fromB := drainPresenceEvents(t, a.hub), drainPresenceEvents(t, b.hub)
	if len(fromA) != 1 || len(fromB) != 1 || fromA[0].Presence.UpdatedAt != fromB[0].Presence.UpdatedAt {
		t.Fatalf("a=%+v b=%+v", fromA, fromB)
	}
	for range 3 {
		a.hub.RefreshPresence("ws-1", "user-1")
		if events := drainPresenceEvents(t, a.hub); len(events) != 0 {
			t.Fatalf("an identical composition minted %+v", events)
		}
	}
}

// Past projectionAttempts the publication is owed rather than published from
// facts that keep moving, and the sweep settles it.
func TestStaleProjection_FactsThatKeepMovingDeferThePublication(t *testing.T) {
	cluster := newPresenceCluster(t)
	a := cluster.node("node-a")
	a.observe(t, "c-obs", "chan-1")
	a.join(t, "c-a", "user-1", "chan-1")
	metrics := newRecordingPresenceMetrics()
	a.hub.presenceMetrics = metrics

	cluster.source.set("user-1", manual(domain.PresenceManualBusy, cluster.clk.Now().Add(time.Hour)))
	cluster.directory.mu.Lock()
	cluster.directory.beforeProject = func() {
		_ = touchFacts(cluster.directory.view("node-x"), context.Background(), "ws-1", "user-1")
	}
	cluster.directory.mu.Unlock()
	a.hub.RefreshPresence("ws-1", "user-1")
	if events := drainPresenceEvents(t, a.hub); len(events) != 0 {
		t.Fatalf("published %+v from facts that never held still", events)
	}
	if metrics.deferredCount("projection_conflict") != 1 {
		t.Fatalf("deferred = %+v", metrics.deferred)
	}

	cluster.directory.mu.Lock()
	cluster.directory.beforeProject = nil
	cluster.directory.mu.Unlock()
	a.hub.sweepPresenceContexts()
	if got := availabilitiesFor(drainPresenceEvents(t, a.hub), "user-1"); len(got) != 1 || got[0] != "busy" {
		t.Fatalf("the sweep settled %v, want busy", got)
	}
}
