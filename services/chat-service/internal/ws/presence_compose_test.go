package ws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

// Composed presence (issue #798): what observers see is the cluster's reach
// combined with the user's manual state and call activity, published only when
// it changes, and never in a way that reveals a user who chose to appear
// offline.

// fakePresenceContext is an in-memory PresenceContextSource.
type fakePresenceContext struct {
	mu       sync.Mutex
	contexts map[string]domain.PresenceContext
	err      error
	reads    int
	lastSeen map[string][]time.Time
	// afterRead, when set, runs once a read has taken its answer and before it
	// returns it: a test parks a composition there, holding facts that are
	// about to go stale.
	afterRead func()
	// clock, when set, is the database's: like contextsSQL, a read leaves out
	// a lease or a manual state that has ended by it.
	clock func() time.Time
}

// inForce is what a read at the source's clock returns of one context.
func (f *fakePresenceContext) inForce(c domain.PresenceContext) domain.PresenceContext {
	if f.clock == nil {
		return c
	}
	now := f.clock()
	if !c.ActivityUntil.IsZero() && !c.ActivityUntil.After(now) {
		c.Activity, c.ActivityUntil = domain.PresenceActivityNone, time.Time{}
	}
	if c.Override.State != "" && !c.Override.ExpiresAt.After(now) {
		c.Override = domain.PresenceOverride{}
	}
	return c
}

func newFakePresenceContext() *fakePresenceContext {
	return &fakePresenceContext{
		contexts: make(map[string]domain.PresenceContext),
		lastSeen: make(map[string][]time.Time),
	}
}

func (f *fakePresenceContext) Contexts(
	_ context.Context, _ string, userIDs []string,
) (map[string]domain.PresenceContext, error) {
	f.mu.Lock()
	f.reads++
	if f.err != nil {
		defer f.mu.Unlock()
		return nil, f.err
	}
	out := make(map[string]domain.PresenceContext, len(userIDs))
	for _, id := range userIDs {
		if ctx, ok := f.contexts[id]; ok {
			out[id] = f.inForce(ctx)
		}
	}
	hook := f.afterRead
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	return out, nil
}

// parkNextRead makes the next context read stop after taking its answer, and
// returns a channel that is signalled when it has, and the release.
func (f *fakePresenceContext) parkNextRead() (parked <-chan struct{}, release func()) {
	reached, resume := make(chan struct{}), make(chan struct{})
	var once sync.Once
	f.mu.Lock()
	f.afterRead = func() {
		once.Do(func() {
			f.mu.Lock()
			f.afterRead = nil
			f.mu.Unlock()
			close(reached)
			<-resume
		})
	}
	f.mu.Unlock()
	return reached, func() { close(resume) }
}

func (f *fakePresenceContext) MarkLastSeen(_ context.Context, _, userID string, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastSeen[userID] = append(f.lastSeen[userID], at)
	return nil
}

func (f *fakePresenceContext) set(userID string, ctx domain.PresenceContext) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.contexts[userID] = ctx
}

func (f *fakePresenceContext) contextOf(userID string) domain.PresenceContext {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.contexts[userID]
}

func (f *fakePresenceContext) clear(userID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.contexts, userID)
}

func (f *fakePresenceContext) fail(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

func (f *fakePresenceContext) lastSeenOf(userID string) []time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]time.Time(nil), f.lastSeen[userID]...)
}

func assertPublicLastSeen(t *testing.T, h *Hub, userID, want string) {
	t.Helper()
	at, offline, err := h.PublicLastSeen(t.Context(), "ws-1", userID)
	if err != nil || !offline || formatPresenceTime(at) != want {
		t.Fatalf("public last seen = %v %v %v, want %s", at, offline, err, want)
	}
}

func (f *fakePresenceContext) seen(userID string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.lastSeen[userID])
}

// recordingPresenceMetrics counts what the hub reports.
type recordingPresenceMetrics struct {
	mu          sync.Mutex
	transitions map[string]int
	graces      map[string]int
	deferred    map[string]int
}

func newRecordingPresenceMetrics() *recordingPresenceMetrics {
	return &recordingPresenceMetrics{transitions: map[string]int{}, graces: map[string]int{}, deferred: map[string]int{}}
}

func (m *recordingPresenceMetrics) PresenceTransition(availability string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.transitions[availability]++
}

func (m *recordingPresenceMetrics) DisconnectGrace(outcome string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.graces[outcome]++
}

func (m *recordingPresenceMetrics) PresenceDeferred(reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deferred[reason]++
}

func (m *recordingPresenceMetrics) deferredCount(reason string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.deferred[reason]
}

func (m *recordingPresenceMetrics) grace(outcome string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.graces[outcome]
}

type composeFixture struct {
	t       *testing.T
	clk     *fakeClock
	tracker *PresenceTracker
	hub     *Hub
	source  *fakePresenceContext
	metrics *recordingPresenceMetrics
}

func newComposeFixture(t *testing.T, grace time.Duration) *composeFixture {
	t.Helper()
	clk := newFakeClock(time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	tracker := newTestPresenceTrackerWithGrace(5*time.Minute, grace, clk)
	hub := newPresenceTestHub(allowAllAuthorizer{}, tracker)
	source := newFakePresenceContext()
	source.clock = clk.Now // the database's clock: it stops returning a state that ended
	metrics := newRecordingPresenceMetrics()
	hub.presenceContext = source
	hub.presenceMetrics = metrics
	return &composeFixture{t: t, clk: clk, tracker: tracker, hub: hub, source: source, metrics: metrics}
}

// connect does what the hub's register path does, then subscribes and
// announces, and returns what that published.
func (f *composeFixture) connect(id, userID string, targets ...string) (*Client, []Event) {
	f.t.Helper()
	c := newClient(id, userID, "ws-1", &fakeSender{})
	registerInHub(f.t, f.hub, c)
	f.hub.connectPresence(c)
	for _, target := range targets {
		subscribeInHubState(f.t, f.hub, c, TargetTypeChannel, target)
		f.hub.handleSubscribed(c, TargetTypeChannel, target, 0)
	}
	_ = takeSnapshots(f.t, c)
	return c, drainPresenceEvents(f.t, f.hub)
}

func availabilitiesFor(events []Event, userID string) []string {
	out := make([]string, 0, len(events))
	for _, evt := range events {
		if evt.Presence != nil && evt.Presence.UserID == userID {
			out = append(out, evt.Presence.Availability)
		}
	}
	return out
}

func manual(state domain.PresenceManualState, expires time.Time) domain.PresenceContext {
	return domain.PresenceContext{Override: domain.PresenceOverride{State: state, ExpiresAt: expires}}
}

func TestPresenceCompose_ConnectPublishesAvailableWithItsLegacyState(t *testing.T) {
	f := newComposeFixture(t, 0)
	f.connect("c-1", "user-1", "chan-1")
	f.connect("c-2", "user-2", "chan-1")
	f.hub.RefreshPresence("ws-1", "user-1")

	// Nothing changed for user-1, so the refresh publishes nothing.
	if events := drainPresenceEvents(t, f.hub); len(events) != 0 {
		t.Fatalf("an unchanged refresh published %+v", events)
	}
	f.source.set("user-1", manual(domain.PresenceManualDoNotDisturb, f.clk.Now().Add(time.Hour)))
	f.hub.RefreshPresence("ws-1", "user-1")
	events := drainPresenceEvents(t, f.hub)
	if len(events) != 1 || events[0].Presence.Availability != "dnd" || events[0].Presence.State != "online" {
		t.Fatalf("expected one dnd (legacy online), got %+v", events)
	}
}

func TestPresenceCompose_IdleUnderDoNotDisturbPublishesNothing(t *testing.T) {
	f := newComposeFixture(t, 0)
	f.source.set("user-1", manual(domain.PresenceManualDoNotDisturb, f.clk.Now().Add(time.Hour)))
	_, events := f.connect("c-1", "user-1", "chan-1")
	if got := availabilitiesFor(events, "user-1"); len(got) != 1 || got[0] != "dnd" {
		t.Fatalf("announce = %v, want dnd", got)
	}

	f.clk.Advance(6 * time.Minute)
	f.tracker.checkAway()

	if events := drainPresenceEvents(t, f.hub); len(events) != 0 {
		t.Fatalf("going idle under Do Not Disturb published %+v", events)
	}
}

func TestPresenceCompose_CallPreventsAwayAndItsEndRecalculates(t *testing.T) {
	f := newComposeFixture(t, 0)
	f.source.set("user-1", domain.PresenceContext{Activity: domain.PresenceActivityInCall})
	_, events := f.connect("c-1", "user-1", "chan-1")
	if len(events) != 1 || events[0].Presence.Availability != "busy" || events[0].Presence.Activity != "in_call" {
		t.Fatalf("announce = %+v, want busy in a call", events)
	}

	f.clk.Advance(6 * time.Minute)
	f.tracker.checkAway()
	if events := drainPresenceEvents(t, f.hub); len(events) != 0 {
		t.Fatalf("a user in a call was published away: %+v", events)
	}

	// The call ends: recomputed from current facts — idle, so away.
	f.source.clear("user-1")
	f.hub.RefreshPresence("ws-1", "user-1")
	events = drainPresenceEvents(t, f.hub)
	if got := availabilitiesFor(events, "user-1"); len(got) != 1 || got[0] != "away" {
		t.Fatalf("after the call = %v, want away", got)
	}
	if events[0].Presence.Activity != "" {
		t.Fatalf("the ended call is still announced: %+v", events[0].Presence)
	}
}

func TestPresenceCompose_AppearOfflineIsIndistinguishableFromOffline(t *testing.T) {
	f := newComposeFixture(t, 0)
	observer, _ := f.connect("c-obs", "observer", "chan-1")
	user, _ := f.connect("c-1", "user-1", "chan-1")

	f.source.set("user-1", manual(domain.PresenceManualAppearOffline, f.clk.Now().Add(time.Hour)))
	f.hub.RefreshPresence("ws-1", "user-1")
	events := drainPresenceEvents(t, f.hub)
	if len(events) != 1 {
		t.Fatalf("expected one offline, got %+v", events)
	}
	hidden := *events[0].Presence
	if hidden.State != "offline" || hidden.Availability != "offline" || hidden.Activity != "" {
		t.Fatalf("appear offline published %+v", hidden)
	}
	// Appearing offline is not evidence that no session is left (issue #798):
	// last seen is written only for a departure the cluster confirms.
	if f.source.seen("user-1") != 0 {
		t.Fatal("appearing offline wrote a last seen while the person is connected")
	}

	// What a profile says about when they were last seen is what the event said.
	assertPublicLastSeen(t, f.hub, "user-1", hidden.UpdatedAt)

	// Activity, idleness and the real disconnect all stay invisible.
	f.clk.Advance(6 * time.Minute)
	f.tracker.checkAway()
	f.hub.dropClient(user)
	if leaked := availabilitiesFor(drainPresenceEvents(t, f.hub), "user-1"); len(leaked) != 0 {
		t.Fatalf("a hidden user leaked %v", leaked)
	}
	// The real departure is the evidence, and what it records is the instant
	// observers last saw them — the moment they hid — not when they left.
	if got := f.source.lastSeenOf("user-1"); len(got) != 1 || formatPresenceTime(got[0]) != hidden.UpdatedAt {
		t.Fatalf("the hidden user's departure recorded %v, want once at %s", got, hidden.UpdatedAt)
	}

	// And a snapshot does not name them.
	snapshot := snapshotFor(t, f.hub, observer, TargetTypeChannel, "chan-1")
	for _, entry := range snapshot.Users {
		if entry.UserID == "user-1" {
			t.Fatalf("snapshot named a hidden user: %+v", snapshot.Users)
		}
	}
}

func TestPresenceCompose_HiddenUserReconnectPublishesNothing(t *testing.T) {
	f := newComposeFixture(t, 0)
	f.source.set("user-1", manual(domain.PresenceManualAppearOffline, f.clk.Now().Add(time.Hour)))
	if _, events := f.connect("c-1", "user-1", "chan-1"); len(events) != 0 {
		t.Fatalf("a hidden user's connect published %+v", events)
	}
}

func TestPresenceCompose_SnapshotIsComposedAndHidesTheHidden(t *testing.T) {
	f := newComposeFixture(t, 0)
	observer, _ := f.connect("c-obs", "observer", "chan-1")
	f.connect("c-1", "user-dnd", "chan-1")
	f.connect("c-2", "user-hidden", "chan-1")
	f.source.set("user-dnd", manual(domain.PresenceManualDoNotDisturb, f.clk.Now().Add(time.Hour)))
	f.source.set("user-hidden", manual(domain.PresenceManualAppearOffline, f.clk.Now().Add(time.Hour)))

	snapshot := snapshotFor(t, f.hub, observer, TargetTypeChannel, "chan-1")
	byUser := map[string]PresencePayload{}
	for _, entry := range snapshot.Users {
		byUser[entry.UserID] = entry
	}
	if _, named := byUser["user-hidden"]; named {
		t.Fatalf("hidden user in snapshot: %+v", snapshot.Users)
	}
	if got := byUser["user-dnd"]; got.Availability != "dnd" || got.State != "online" {
		t.Fatalf("dnd user = %+v", got)
	}
	if !snapshot.Complete {
		t.Fatal("a single-node roster with every context read is complete")
	}
}

func TestPresenceCompose_UnreadableContextWithholdsTheRoster(t *testing.T) {
	f := newComposeFixture(t, 0)
	observer, _ := f.connect("c-obs", "observer", "chan-1")
	f.connect("c-1", "user-1", "chan-1")
	f.source.fail(errors.New("database unavailable"))

	snapshot := snapshotFor(t, f.hub, observer, TargetTypeChannel, "chan-1")
	if len(snapshot.Users) != 0 || snapshot.Complete {
		t.Fatalf("snapshot = %+v, want empty and incomplete", snapshot)
	}
}

func TestPresenceCompose_UnreadableContextPublishesNothingAndTheSweepRetries(t *testing.T) {
	f := newComposeFixture(t, 0)
	f.connect("c-1", "user-1", "chan-1")
	f.source.set("user-1", manual(domain.PresenceManualBusy, f.clk.Now().Add(time.Hour)))
	f.source.fail(errors.New("database unavailable"))

	f.hub.RefreshPresence("ws-1", "user-1")
	if events := drainPresenceEvents(t, f.hub); len(events) != 0 {
		t.Fatalf("published without a context: %+v", events)
	}

	f.source.fail(nil)
	f.hub.sweepPresenceContexts()
	events := drainPresenceEvents(t, f.hub)
	if got := availabilitiesFor(events, "user-1"); len(got) != 1 || got[0] != "busy" {
		t.Fatalf("after the retry = %v, want busy", got)
	}
}

func TestPresenceCompose_ExpiredOverrideConvergesThroughTheSweep(t *testing.T) {
	f := newComposeFixture(t, 0)
	f.source.set("user-1", manual(domain.PresenceManualBeRightBack, f.clk.Now().Add(time.Hour)))
	f.connect("c-1", "user-1", "chan-1")

	// The database stops returning it once it has expired.
	f.source.clear("user-1")
	f.hub.sweepPresenceContexts()
	events := drainPresenceEvents(t, f.hub)
	if got := availabilitiesFor(events, "user-1"); len(got) != 1 || got[0] != "available" {
		t.Fatalf("after expiry = %v, want available", got)
	}

	// A second sweep with nothing new is silent.
	f.hub.sweepPresenceContexts()
	if events := drainPresenceEvents(t, f.hub); len(events) != 0 {
		t.Fatalf("an idle sweep published %+v", events)
	}
}

func TestPresenceCompose_ExpiryIsAppliedAtThePublicationInstant(t *testing.T) {
	f := newComposeFixture(t, 0)
	f.source.set("user-1", manual(domain.PresenceManualBusy, f.clk.Now().Add(time.Minute)))
	f.connect("c-1", "user-1", "chan-1")

	// The stored row still says busy, but its expiry has passed on the
	// database's clock by the time the next publication reads it.
	f.clk.Advance(2 * time.Minute)
	f.hub.RefreshPresence("ws-1", "user-1")
	events := drainPresenceEvents(t, f.hub)
	if got := availabilitiesFor(events, "user-1"); len(got) != 1 || got[0] != "available" {
		t.Fatalf("= %v, want the expired busy to be gone", got)
	}
}

func TestPresenceProjection_VersionMovesOnlyWithTheProjection(t *testing.T) {
	store := newLocalUserPresence("self", time.Now)
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	if err := assertReach(store, ctx, "ws-1", "user-1", PresenceOnline, at); err != nil {
		t.Fatal(err)
	}
	assertProjectionContract(t, store, "ws-1")
	records, _ := store.ReadUsers(ctx, "ws-1", []string{"user-1", "nobody"})
	if records["user-1"].Projection == nil || records["user-1"].Projection.Effective.Availability != domain.PresenceDoNotDisturb {
		t.Fatalf("read = %+v", records)
	}
	if nobody := records["nobody"]; nobody.Projection != nil || len(nobody.Reach) != 0 {
		t.Fatalf("a user with no record was answered for: %+v", nobody)
	}
	// Somebody gone with no reach left is forgotten once published offline.
	_ = withdrawReach(store, ctx, "ws-1", "user-1")
	offline := domain.EffectivePresence{Availability: domain.PresenceOffline}
	if _, outcome, _ := store.Project(ctx, "ws-1", "user-1", commitOf(offline, revisionOf(t, store, "user-1"), at.Add(time.Minute))); outcome != projectionApplied {
		t.Fatalf("the departure = %v", outcome)
	}
	if store.size() != 0 {
		t.Fatalf("a departed user is still held: %d records", store.size())
	}
	if err := store.RefreshUsers(ctx, "ws-1", nil); err != nil {
		t.Fatal(err)
	}
}

func revisionOf(t *testing.T, store UserPresenceStore, userID string) uint64 {
	t.Helper()
	records, err := store.ReadUsers(context.Background(), "ws-1", []string{userID})
	if err != nil {
		t.Fatal(err)
	}
	return records[userID].Revision
}

// MEDIUM-E: forgetting a departed person must not forget ordering. Offline at
// 100, forgotten, the clock steps back to 90, the person returns: their
// Available must outrank the Offline every observer holds.
func TestLocalUserPresence_OrderingSurvivesForgettingAndAClockStepBack(t *testing.T) {
	store := newLocalUserPresence("self", time.Now)
	ctx := context.Background()
	t100 := time.Unix(100, 0).UTC()
	available := domain.EffectivePresence{Availability: domain.PresenceAvailable}
	offline := domain.EffectivePresence{Availability: domain.PresenceOffline}

	last := time.Time{}
	for cycle := range 3 {
		clock := t100.Add(-time.Duration(cycle) * 10 * time.Second) // the clock keeps stepping back
		_ = assertReach(store, ctx, "ws-1", "user-1", PresenceOnline, clock)
		online, outcome, _ := store.Project(ctx, "ws-1", "user-1", commitOf(available, revisionOf(t, store, "user-1"), clock))
		if outcome != projectionApplied || !online.After(last) {
			t.Fatalf("cycle %d: back online at %v (%v), not after %v", cycle, online, outcome, last)
		}
		_ = withdrawReach(store, ctx, "ws-1", "user-1")
		gone, outcome, _ := store.Project(ctx, "ws-1", "user-1", commitOf(offline, revisionOf(t, store, "user-1"), clock))
		if outcome != projectionApplied || !gone.After(online) {
			t.Fatalf("cycle %d: offline at %v (%v), not after %v", cycle, gone, outcome, online)
		}
		if store.size() != 0 {
			t.Fatalf("cycle %d: a departed person is still held", cycle)
		}
		last = gone
		// An identical offline again is no new version.
		if again, outcome, _ := store.Project(ctx, "ws-1", "user-1", commitOf(offline, revisionOf(t, store, "user-1"), clock)); outcome != projectionUnchanged || !again.IsZero() {
			t.Fatalf("cycle %d: a repeated offline = %v %v", cycle, again, outcome)
		}
	}
}

// What survives the forgetting is two numbers for the whole process, never a
// record per person: a thousand people coming and going leave nothing behind.
func TestLocalUserPresence_RetentionIsBoundedByWhoIsHeld(t *testing.T) {
	store := newLocalUserPresence("self", time.Now)
	ctx := context.Background()
	at := time.Unix(1_790_000_000, 0).UTC()
	for i := range 1000 {
		user := fmt.Sprintf("user-%d", i)
		_ = assertReach(store, ctx, "ws-1", user, PresenceOnline, at)
		_, _, _ = store.Project(ctx, "ws-1", user, commitOf(domain.EffectivePresence{Availability: domain.PresenceAvailable}, revisionOf(t, store, user), at))
		_ = withdrawReach(store, ctx, "ws-1", user)
		_, _, _ = store.Project(ctx, "ws-1", user, commitOf(domain.EffectivePresence{Availability: domain.PresenceOffline}, revisionOf(t, store, user), at))
	}
	if store.size() != 0 {
		t.Fatalf("%d records held after everybody left", store.size())
	}
}

// A composition that began before a person was forgotten and came back cannot
// commit as if nothing had happened in between.
func TestLocalUserPresence_ForgettingIsAFactChange(t *testing.T) {
	store := newLocalUserPresence("self", time.Now)
	ctx := context.Background()
	at := time.Unix(1_790_000_000, 0).UTC()
	stale := revisionOf(t, store, "user-1")
	_ = assertReach(store, ctx, "ws-1", "user-1", PresenceOnline, at)
	_ = withdrawReach(store, ctx, "ws-1", "user-1")
	_, _, _ = store.Project(ctx, "ws-1", "user-1", commitOf(domain.EffectivePresence{Availability: domain.PresenceOffline}, revisionOf(t, store, "user-1"), at))
	if _, outcome, _ := store.Project(ctx, "ws-1", "user-1", commitOf(domain.EffectivePresence{Availability: domain.PresenceAvailable}, stale, at)); outcome != projectionConflict {
		t.Fatalf("a composition from before the forgetting = %v, want a conflict", outcome)
	}
}

// ── grace at the hub ─────────────────────────────────────────────────────────

func TestPresenceCompose_ReconnectInsideTheGraceShowsNoFlicker(t *testing.T) {
	f := newComposeFixture(t, testGrace)
	observer, _ := f.connect("c-obs", "observer", "chan-1")
	first, _ := f.connect("c-1", "user-1", "chan-1")

	f.hub.dropClient(first)
	if events := drainPresenceEvents(t, f.hub); len(events) != 0 {
		t.Fatalf("a dropped socket published %+v", events)
	}
	// Still in the room for anybody who asks meanwhile.
	snapshot := snapshotFor(t, f.hub, observer, TargetTypeChannel, "chan-1")
	if !rosterIncludes(snapshot.Users, "user-1") {
		t.Fatalf("a lingering user vanished from the roster: %+v", snapshot.Users)
	}
	if _, covered := f.hub.coveredTargetsForUser("ws-1", "user-1")[targetKey{
		workspaceID: "ws-1", targetType: TargetTypeChannel, targetID: "chan-1",
	}.String()]; !covered {
		t.Fatal("a lingering user's room is no longer covered")
	}

	f.clk.Advance(5 * time.Second)
	if _, events := f.connect("c-2", "user-1", "chan-1"); len(availabilitiesFor(events, "user-1")) > 1 {
		t.Fatalf("reconnect published %+v", events)
	}
	f.clk.Advance(testGrace)
	f.tracker.expireGraces()
	if events := drainPresenceEvents(t, f.hub); len(events) != 0 {
		t.Fatalf("an expired grace of a reconnected user published %+v", events)
	}
	if f.metrics.grace("recovered") != 1 || f.metrics.grace("expired") != 0 {
		t.Fatalf("grace metrics = %+v", f.metrics.graces)
	}
	f.hub.sweepPresenceContexts()
	if f.hub.lingeringKeysFor(presenceKey{workspaceID: "ws-1", userID: "user-1"}) != nil {
		t.Fatal("a recovered grace left its cover behind after the resume window")
	}
}

func TestPresenceCompose_ExpiredGraceAnnouncesOfflineInTheDepartedRooms(t *testing.T) {
	f := newComposeFixture(t, testGrace)
	f.connect("c-obs", "observer", "chan-1")
	leaving, _ := f.connect("c-1", "user-1", "chan-1")

	f.hub.dropClient(leaving)
	_ = drainPresenceEvents(t, f.hub)
	f.clk.Advance(testGrace)
	f.tracker.expireGraces()

	events := drainPresenceEvents(t, f.hub)
	if got := presenceStatesFor(events, "user-1"); len(got) != 1 || got[0] != "offline" {
		t.Fatalf("after the grace = %v, want one offline", got)
	}
	if events[0].TargetID != "chan-1" {
		t.Fatalf("offline addressed to %q", events[0].TargetID)
	}
	if f.source.seen("user-1") != 1 {
		t.Fatal("the real offline did not record last seen")
	}
	if f.metrics.grace("expired") != 1 {
		t.Fatalf("grace metrics = %+v", f.metrics.graces)
	}
}

func rosterIncludes(users []PresencePayload, userID string) bool {
	for _, user := range users {
		if user.UserID == userID {
			return true
		}
	}
	return false
}

// ── across instances ─────────────────────────────────────────────────────────

func TestPresenceCompose_ClusterIdleTabDoesNotHideAnActiveDevice(t *testing.T) {
	clk := newFakeClock(time.Now())
	shared := newFakeDirectory()
	trackerA := newTestPresenceTracker(5*time.Minute, clk)
	nodeA := newClusterNode("node-a", shared.view("node-a"), trackerA)
	trackerB := newTestPresenceTracker(5*time.Minute, clk)
	nodeB := newClusterNode("node-b", shared.view("node-b"), trackerB)

	desktop := newClient("c-desk", "user-1", "ws-1", &fakeSender{})
	registerInHub(t, nodeA, desktop)
	trackerA.Connect("ws-1", "user-1", desktop.id)
	subscribeInHubState(t, nodeA, desktop, TargetTypeChannel, "chan-1")
	nodeA.handleSubscribed(desktop, TargetTypeChannel, "chan-1", 0)
	drainPresenceEvents(t, nodeA)

	clk.Advance(4 * time.Minute)
	laptop := newClient("c-lap", "user-1", "ws-1", &fakeSender{})
	registerInHub(t, nodeB, laptop)
	trackerB.Connect("ws-1", "user-1", laptop.id)
	subscribeInHubState(t, nodeB, laptop, TargetTypeChannel, "chan-1")
	nodeB.handleSubscribed(laptop, TargetTypeChannel, "chan-1", 0)
	drainPresenceEvents(t, nodeB)

	// The desktop goes idle; the laptop is in use on another replica.
	clk.Advance(2 * time.Minute)
	trackerA.checkAway()
	if got := availabilitiesFor(drainPresenceEvents(t, nodeA), "user-1"); len(got) != 0 {
		t.Fatalf("an idle tab published %v while another device is active", got)
	}

	// The desktop disconnects entirely: still nobody is told offline.
	nodeA.dropClient(desktop)
	events := drainPresenceEvents(t, nodeA)
	for _, state := range presenceStatesFor(events, "user-1") {
		if state == "offline" {
			t.Fatalf("one device leaving published offline while another is connected: %+v", events)
		}
	}
}

// ── realtime hint and calls ──────────────────────────────────────────────────

func TestPresenceCompose_SettingsHintReachesOnlyTheUsersOwnSessions(t *testing.T) {
	f := newComposeFixture(t, 0)
	bus := &fakeBus{}
	f.hub.bus = bus
	own, _ := f.connect("c-1", "user-1", "chan-1")
	other, _ := f.connect("c-2", "user-2", "chan-1")
	foreign := newClient("c-3", "user-1", "ws-2", &fakeSender{})
	registerInHub(t, f.hub, foreign)

	f.hub.PublishPresenceSettingsChanged(context.Background(), "ws-1", "user-1")

	if got := framesOfType(t, own, string(EventTypePresenceSettingsChanged)); got != 1 {
		t.Fatalf("own session got %d hints", got)
	}
	if framesOfType(t, other, string(EventTypePresenceSettingsChanged)) != 0 ||
		framesOfType(t, foreign, string(EventTypePresenceSettingsChanged)) != 0 {
		t.Fatal("the hint reached somebody else's session")
	}
	published, ok := bus.lastPublished()
	if !ok || published.Type != EventTypePresenceSettingsChanged || published.RecipientUserID != "user-1" {
		t.Fatalf("bus = %+v", published)
	}
	if len(f.hub.takePresenceChanges()) != 1 {
		t.Fatal("the hint did not ask for a republish")
	}
	before := bus.publishCount()
	f.hub.PublishPresenceSettingsChanged(context.Background(), "", "user-1")
	if bus.publishCount() != before {
		t.Fatal("a hint without a workspace was published")
	}
}

func TestPresenceCompose_RemoteHintIsDeliveredAndRepublishes(t *testing.T) {
	f := newComposeFixture(t, 0)
	userID := uuid.NewString()
	workspaceID := uuid.NewString()
	c := newClient("c-1", userID, workspaceID, &fakeSender{})
	registerInHub(t, f.hub, c)
	f.tracker.Connect(workspaceID, userID, c.id)

	f.hub.handleRemoteBusEvent(Event{
		SchemaVersion: CurrentEventSchemaVersion, Type: EventTypePresenceSettingsChanged,
		WorkspaceID: workspaceID, TargetType: TargetTypeUser, TargetID: userID, RecipientUserID: userID,
		EventID: uuid.NewString(), SourceInstanceID: "other-node", CreatedAt: time.Now(),
	})
	if got := framesOfType(t, c, string(EventTypePresenceSettingsChanged)); got != 1 {
		t.Fatalf("remote hint frames = %d", got)
	}
	if len(f.hub.takePresenceChanges()) != 1 {
		t.Fatal("the remote hint did not ask for a republish")
	}
}

func TestPresenceCompose_CallLifecycleRefreshesParticipants(t *testing.T) {
	f := newComposeFixture(t, 0)
	f.connect("c-1", "caller", "chan-1")
	f.connect("c-2", "callee", "chan-1")

	f.hub.refreshCallParticipants(domain.Call{WorkspaceID: "ws-1", CallerID: "caller", CalleeID: "callee"})
	if got := len(f.hub.takePresenceChanges()); got != 2 {
		t.Fatalf("refreshed %d participants, want 2", got)
	}
	f.hub.refreshCallParticipants(domain.Call{WorkspaceID: "ws-1", CallerID: "caller"})
	if got := len(f.hub.takePresenceChanges()); got != 1 {
		t.Fatalf("resource call refreshed %d, want the caller", got)
	}
	// Somebody this instance does not serve is left to the instance that does.
	f.hub.RefreshPresence("ws-1", "nobody")
	f.hub.RefreshPresence("", "caller")
	if got := len(f.hub.takePresenceChanges()); got != 0 {
		t.Fatalf("refreshed %d users this instance does not hold", got)
	}
}

func framesOfType(t *testing.T, c *Client, frameType string) int {
	t.Helper()
	count := 0
	for {
		select {
		case data := <-c.outbox:
			var frame struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal(data, &frame); err != nil {
				t.Fatalf("decode frame: %v", err)
			}
			if frame.Type == frameType {
				count++
			}
		default:
			return count
		}
	}
}

// ── remote validation ────────────────────────────────────────────────────────

func TestPresenceCompose_RemoteCompositionIsValidated(t *testing.T) {
	cases := []struct {
		name    string
		payload PresencePayload
		ok      bool
	}{
		{"legacy only", PresencePayload{State: "online"}, true},
		{"available", PresencePayload{State: "online", Availability: "available"}, true},
		{"dnd in a call", PresencePayload{State: "online", Availability: "dnd", Activity: "in_call"}, true},
		{"brb", PresencePayload{State: "away", Availability: "brb"}, true},
		{"offline", PresencePayload{State: "offline", Availability: "offline"}, true},
		{"unknown availability", PresencePayload{State: "online", Availability: "invisible"}, false},
		{"unknown activity", PresencePayload{State: "online", Availability: "busy", Activity: "gaming"}, false},
		{"state disagrees", PresencePayload{State: "online", Availability: "offline"}, false},
		{"activity on available", PresencePayload{State: "online", Availability: "available", Activity: "in_call"}, false},
		{"activity on offline", PresencePayload{State: "offline", Availability: "offline", Activity: "in_call"}, false},
		{"activity without availability", PresencePayload{State: "online", Activity: "in_call"}, false},
	}
	for _, tc := range cases {
		if got := validPresenceComposition(tc.payload); got != tc.ok {
			t.Fatalf("%s: valid = %v, want %v", tc.name, got, tc.ok)
		}
	}
}

func TestPresenceCompose_RemoteSettingsHintMustAddressItsOwnSubject(t *testing.T) {
	userID := uuid.NewString()
	base := Event{
		SchemaVersion: CurrentEventSchemaVersion, Type: EventTypePresenceSettingsChanged,
		WorkspaceID: uuid.NewString(), TargetType: TargetTypeUser, TargetID: userID, RecipientUserID: userID,
		EventID: uuid.NewString(), SourceInstanceID: "node-b", CreatedAt: time.Now(),
		Presence: &PresencePayload{UserID: userID, State: "online"},
	}
	canonical, ok := canonicalizeRemoteEvent(base)
	if !ok || canonical.Presence != nil {
		t.Fatalf("canonical = %+v (ok %v), want accepted with no payload", canonical, ok)
	}
	mismatch := base
	mismatch.RecipientUserID = uuid.NewString()
	if _, ok := canonicalizeRemoteEvent(mismatch); ok {
		t.Fatal("a hint naming somebody else as recipient was accepted")
	}
	channel := base
	channel.TargetType = TargetTypeChannel
	if _, ok := canonicalizeRemoteEvent(channel); ok {
		t.Fatal("a hint routed to a channel was accepted")
	}
}

func TestPresenceCompose_LegacyProjection(t *testing.T) {
	cases := map[domain.PresenceAvailability]PresenceStatus{
		domain.PresenceAvailable: PresenceOnline, domain.PresenceBusy: PresenceOnline,
		domain.PresenceDoNotDisturb: PresenceOnline, domain.PresenceBeRightBack: PresenceAway,
		domain.PresenceAway: PresenceAway, domain.PresenceOffline: PresenceOffline,
		domain.PresenceAvailability("future"): PresenceOffline,
	}
	for availability, want := range cases {
		if got := legacyStateFor(availability); got != want {
			t.Fatalf("legacyStateFor(%q) = %q, want %q", availability, got, want)
		}
	}
	if reachFor(PresenceStatus("bogus")) != domain.PresenceReachNone {
		t.Fatal("an unknown status must not reach anybody")
	}
}

func TestPresenceCompose_WithoutASourceEveryoneIsAutomatic(t *testing.T) {
	h := newPresenceTestHub(allowAllAuthorizer{}, nil)
	contexts, err := h.loadPresenceContexts(context.Background(), "ws-1", []string{"user-1"})
	if err != nil || len(contexts) != 0 {
		t.Fatalf("contexts = %+v, %v", contexts, err)
	}
	if h.presenceClock().IsZero() {
		t.Fatal("presence clock without a tracker")
	}
	h.metrics().PresenceTransition("available")
	h.metrics().DisconnectGrace("expired")
	h.sweepPresenceContexts()
	h.markLastSeen(context.Background(), presenceKey{workspaceID: "ws-1", userID: "user-1"}, time.Now())
	if WithPresenceContext(newFakePresenceContext())(h); h.presenceContext == nil {
		t.Fatal("WithPresenceContext did not attach")
	}
	if WithPresenceMetrics(newRecordingPresenceMetrics())(h); h.presenceMetrics == nil {
		t.Fatal("WithPresenceMetrics did not attach")
	}
}
