package ws

import (
	"errors"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

// The version of an effective presence (issue #798): every event and every
// snapshot about a person carries the instant their effective presence took
// its current value. It moves with the projection, from whichever source moved
// it — reach, a manual state, its expiry, a call — and only then.

func presenceVersion(t *testing.T, users []PresencePayload, userID string) (string, string) {
	t.Helper()
	for _, user := range users {
		if user.UserID == userID {
			return user.Availability, user.UpdatedAt
		}
	}
	t.Fatalf("%s is not in %+v", userID, users)
	return "", ""
}

func TestPresenceVersion_SnapshotSeesAChangeNoEventCarried(t *testing.T) {
	f := newComposeFixture(t, 0)
	observer, _ := f.connect("c-obs", "observer", "chan-1")
	_, events := f.connect("c-1", "user-1", "chan-1")
	first := events[len(events)-1].Presence.UpdatedAt

	// The manual state changes and no hint arrives: the next snapshot is the
	// first word about it, and it must outrank the event the observer holds.
	f.clk.Advance(time.Second)
	f.source.set("user-1", manual(domain.PresenceManualDoNotDisturb, f.clk.Now().Add(time.Hour)))
	availability, version := presenceVersion(t, snapshotFor(t, f.hub, observer, TargetTypeChannel, "chan-1").Users, "user-1")
	if availability != "dnd" || version <= first {
		t.Fatalf("snapshot = %s @ %s, want dnd after %s", availability, version, first)
	}

	// The event that follows agrees with the snapshot instead of repeating it.
	f.hub.RefreshPresence("ws-1", "user-1")
	if got := availabilitiesFor(drainPresenceEvents(t, f.hub), "user-1"); len(got) != 1 || got[0] != "dnd" {
		t.Fatalf("this replica's rooms were not told: %v", got)
	}
	_, again := presenceVersion(t, snapshotFor(t, f.hub, observer, TargetTypeChannel, "chan-1").Users, "user-1")
	if again != version {
		t.Fatalf("an unchanged state was re-versioned: %s then %s", version, again)
	}
}

func TestPresenceVersion_IdenticalSnapshotsInventNothing(t *testing.T) {
	f := newComposeFixture(t, 0)
	observer, _ := f.connect("c-obs", "observer", "chan-1")
	_, events := f.connect("c-1", "user-1", "chan-1")
	published := events[len(events)-1].Presence.UpdatedAt

	for range 3 {
		f.clk.Advance(time.Minute)
		_, version := presenceVersion(t, snapshotFor(t, f.hub, observer, TargetTypeChannel, "chan-1").Users, "user-1")
		if version != published {
			t.Fatalf("an unchanged snapshot carried %s, want the published %s", version, published)
		}
	}
}

func TestPresenceVersion_EverySourceMovesItForward(t *testing.T) {
	f := newComposeFixture(t, 0)
	f.connect("c-obs", "observer", "chan-1")
	_, events := f.connect("c-1", "user-1", "chan-1")
	last := events[len(events)-1].Presence.UpdatedAt

	steps := []struct {
		name  string
		apply func()
		want  string
	}{
		{"manual busy", func() {
			f.source.set("user-1", manual(domain.PresenceManualBusy, f.clk.Now().Add(time.Minute)))
			f.hub.RefreshPresence("ws-1", "user-1")
		}, "busy"},
		{"manual expiry", func() {
			f.clk.Advance(2 * time.Minute)
			f.source.clear("user-1")
			f.hub.sweepPresenceContexts()
		}, "available"},
		{"call enter", func() {
			f.source.set("user-1", domain.PresenceContext{Activity: domain.PresenceActivityInCall})
			f.hub.RefreshPresence("ws-1", "user-1")
		}, "busy"},
		{"call leave", func() {
			f.source.clear("user-1")
			f.hub.RefreshPresence("ws-1", "user-1")
		}, "available"},
		{"automatic away", func() {
			f.clk.Advance(6 * time.Minute)
			f.tracker.checkAway()
		}, "away"},
		{"back", func() {
			f.tracker.RecordActivity("ws-1", "user-1", "c-1")
			f.hub.RefreshPresence("ws-1", "user-1")
		}, "available"},
		{"appear offline", func() {
			f.source.set("user-1", manual(domain.PresenceManualAppearOffline, f.clk.Now().Add(time.Hour)))
			f.hub.RefreshPresence("ws-1", "user-1")
		}, "offline"},
	}
	for _, step := range steps {
		f.clk.Advance(time.Second)
		step.apply()
		events := drainPresenceEvents(t, f.hub)
		got := availabilitiesFor(events, "user-1")
		if len(got) != 1 || got[0] != step.want {
			t.Fatalf("%s published %v, want %s", step.name, got, step.want)
		}
		version := events[len(events)-1].Presence.UpdatedAt
		if version <= last {
			t.Fatalf("%s carried %s, not after %s", step.name, version, last)
		}
		last = version
	}
}

func TestPresenceVersion_ReplicasShareOneVersion(t *testing.T) {
	cluster := newPresenceCluster(t)
	a, b := cluster.node("node-a"), cluster.node("node-b")
	observerA := a.observe(t, "c-obs-a", "chan-1")
	observerB := b.observe(t, "c-obs-b", "chan-2")
	a.join(t, "c-a", "user-1", "chan-1")
	b.join(t, "c-b", "user-1", "chan-2")

	cluster.clk.Advance(time.Second)
	cluster.source.set("user-1", manual(domain.PresenceManualDoNotDisturb, cluster.clk.Now().Add(time.Hour)))
	a.hub.RefreshPresence("ws-1", "user-1")
	eventsA := drainPresenceEvents(t, a.hub)
	cluster.clk.Advance(time.Second)
	b.hub.RefreshPresence("ws-1", "user-1")
	eventsB := drainPresenceEvents(t, b.hub)
	if len(eventsA) != 1 || len(eventsB) != 1 {
		t.Fatalf("each replica must tell its own rooms once: a=%d b=%d", len(eventsA), len(eventsB))
	}
	if eventsA[0].Presence.UpdatedAt != eventsB[0].Presence.UpdatedAt {
		t.Fatalf("two replicas versioned one change twice: %s vs %s",
			eventsA[0].Presence.UpdatedAt, eventsB[0].Presence.UpdatedAt)
	}
	_, versionA := presenceVersion(t, snapshotFor(t, a.hub, observerA, TargetTypeChannel, "chan-1").Users, "user-1")
	_, versionB := presenceVersion(t, snapshotFor(t, b.hub, observerB, TargetTypeChannel, "chan-2").Users, "user-1")
	if versionA != eventsA[0].Presence.UpdatedAt || versionB != versionA {
		t.Fatalf("snapshots disagree with the event: a=%s b=%s event=%s", versionA, versionB, eventsA[0].Presence.UpdatedAt)
	}
}

// Expiry of a person's own manual state reaches their own sessions as a
// settings hint, once, from the sweep that notices it (issue #798).
func TestPresenceExpiry_TellsTheOwnSessionsOnce(t *testing.T) {
	f := newComposeFixture(t, 0)
	f.source.set("user-1", manual(domain.PresenceManualAppearOffline, f.clk.Now().Add(time.Minute)))
	own, _ := f.connect("c-1", "user-1", "chan-1")
	other, _ := f.connect("c-2", "user-2", "chan-1")
	_ = framesOfType(t, own, string(EventTypePresenceSettingsChanged))

	f.clk.Advance(2 * time.Minute)
	f.source.clear("user-1") // the database stops returning the expired state
	f.hub.sweepPresenceContexts()
	f.hub.sweepPresenceContexts()

	if got := framesOfType(t, own, string(EventTypePresenceSettingsChanged)); got != 1 {
		t.Fatalf("own session got %d hints for one expiry", got)
	}
	if framesOfType(t, other, string(EventTypePresenceSettingsChanged)) != 0 {
		t.Fatal("somebody else was told about another person's settings")
	}
	if got := availabilitiesFor(drainPresenceEvents(t, f.hub), "user-1"); len(got) != 1 || got[0] != "available" {
		t.Fatalf("the expiry published %v, want available", got)
	}
}

func TestPresenceExpiry_AReplacedStateIsNotReportedAsExpired(t *testing.T) {
	f := newComposeFixture(t, 0)
	f.source.set("user-1", manual(domain.PresenceManualBusy, f.clk.Now().Add(time.Minute)))
	own, _ := f.connect("c-1", "user-1", "chan-1")

	// Replaced through the API before it expired: the write's own hint and
	// refresh settle it, and the sweep has nothing left to report.
	replacement := manual(domain.PresenceManualDoNotDisturb, f.clk.Now().Add(time.Hour))
	f.source.set("user-1", replacement)
	f.hub.PublishPresenceSettingsChanged(t.Context(), "ws-1", "user-1")
	drainPresenceEvents(t, f.hub)
	_ = framesOfType(t, own, string(EventTypePresenceSettingsChanged))

	f.clk.Advance(2 * time.Minute)
	f.hub.sweepPresenceContexts()
	if got := framesOfType(t, own, string(EventTypePresenceSettingsChanged)); got != 0 {
		t.Fatalf("a replaced state was reported as expired %d time(s)", got)
	}
	if got := availabilitiesFor(drainPresenceEvents(t, f.hub), "user-1"); len(got) != 0 {
		t.Fatalf("nothing changed, yet %v was published", got)
	}
}

// Somebody hidden from their first connection was never shown, so there is no
// public instant to record when they leave, and a profile says nothing either.
func TestPresenceLastSeen_NeverShownRecordsNothing(t *testing.T) {
	f := newComposeFixture(t, 0)
	f.connect("c-obs", "observer", "chan-1")
	f.source.set("user-1", manual(domain.PresenceManualAppearOffline, f.clk.Now().Add(time.Hour)))
	hidden, _ := f.connect("c-1", "user-1", "chan-1")
	f.hub.dropClient(hidden)
	if leaked := availabilitiesFor(drainPresenceEvents(t, f.hub), "user-1"); len(leaked) != 0 {
		t.Fatalf("a never-shown user published %v", leaked)
	}
	if f.source.seen("user-1") != 0 {
		t.Fatal("a person nobody saw got a last seen")
	}
	if _, offline, err := f.hub.PublicLastSeen(t.Context(), "ws-1", "user-1"); err != nil || offline {
		t.Fatalf("public last seen of a never-shown person = %v %v", offline, err)
	}
}

func TestPresenceLastSeen_NothingPublicAboutSomebodyPresent(t *testing.T) {
	f := newComposeFixture(t, 0)
	f.connect("c-1", "user-1", "chan-1")
	if _, offline, err := f.hub.PublicLastSeen(t.Context(), "ws-1", "user-1"); err != nil || offline {
		t.Fatalf("a present person has a public last seen: %v %v", offline, err)
	}
}

func TestPresenceLastSeen_UnreadableIsNotKnown(t *testing.T) {
	cluster := newPresenceCluster(t)
	a := cluster.node("node-a")
	a.join(t, "c-a", "user-1", "chan-1")
	a.leave(t, "c-a")
	cluster.directory.failUserStore(errors.New("valkey unavailable"))
	if _, offline, err := a.hub.PublicLastSeen(t.Context(), "ws-1", "user-1"); err == nil || offline {
		t.Fatalf("an unreadable projection answered %v %v", offline, err)
	}
}
