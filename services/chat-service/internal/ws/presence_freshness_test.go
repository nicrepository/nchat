package ws

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

// A projection is committed only from facts still valid at the commit
// (issue #798, HIGH-2/HIGH-3): no revision moved, no timed fact ended, no
// counted instance died, no facts change in flight — and a facts change that
// cannot be announced is not made. Every case runs against the fake store and,
// when CHAT_TEST_VALKEY_URL is set, against the real Lua on a real Valkey.

// faultStore wraps a store with injectable failures and pauses, per
// operation: "assert", "withdraw", "read", "project", "begin", "end".
type faultStore struct {
	UserPresenceStore
	mu     sync.Mutex
	fail   map[string]error
	before map[string]func()
	// outcomes is every Project outcome, in order.
	outcomes []projectionOutcome
}

func wrapFaults(m *clusterMember) *faultStore {
	store := &faultStore{UserPresenceStore: m.hub.userPresence(), fail: map[string]error{}, before: map[string]func(){}}
	m.hub.userStore = store
	return store
}

func (s *faultStore) set(op string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fail[op] = err
}

func (s *faultStore) pauseBefore(op string, run func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.before[op] = run
}

func (s *faultStore) enter(op string) error {
	s.mu.Lock()
	run, err := s.before[op], s.fail[op]
	delete(s.before, op)
	s.mu.Unlock()
	if run != nil {
		run()
	}
	return err
}

func (s *faultStore) AssertReach(ctx context.Context, w, u string, st PresenceStatus, at time.Time, g uint64) (bool, error) {
	if err := s.enter("assert"); err != nil {
		return false, err
	}
	return s.UserPresenceStore.AssertReach(ctx, w, u, st, at, g)
}

func (s *faultStore) WithdrawReach(ctx context.Context, w, u string, g uint64) (bool, error) {
	if err := s.enter("withdraw"); err != nil {
		return false, err
	}
	return s.UserPresenceStore.WithdrawReach(ctx, w, u, g)
}

func (s *faultStore) ReadUsers(ctx context.Context, w string, ids []string) (map[string]UserPresenceRecord, error) {
	if err := s.enter("read"); err != nil {
		return nil, err
	}
	return s.UserPresenceStore.ReadUsers(ctx, w, ids)
}

func (s *faultStore) Project(ctx context.Context, w, u string, c ProjectionCommit) (time.Time, projectionOutcome, error) {
	if err := s.enter("project"); err != nil {
		return time.Time{}, projectionUnchanged, err
	}
	at, outcome, err := s.UserPresenceStore.Project(ctx, w, u, c)
	s.mu.Lock()
	s.outcomes = append(s.outcomes, outcome)
	s.mu.Unlock()
	return at, outcome, err
}

func (s *faultStore) firstOutcome(t *testing.T) projectionOutcome {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.outcomes) == 0 {
		t.Fatal("nothing was committed")
	}
	return s.outcomes[0]
}

func (s *faultStore) BeginFactsChange(ctx context.Context, w string, ids []string, c FactsChange) error {
	if err := s.enter("begin"); err != nil {
		return err
	}
	return s.UserPresenceStore.BeginFactsChange(ctx, w, ids, c)
}

func (s *faultStore) EndFactsChange(ctx context.Context, w string, ids []string, c FactsChange) error {
	if err := s.enter("end"); err != nil {
		return err
	}
	return s.UserPresenceStore.EndFactsChange(ctx, w, ids, c)
}

// forEachStore runs a case on the fake store and on a real Valkey.
func forEachStore(t *testing.T, run func(t *testing.T, w *raceWorld)) {
	t.Helper()
	forFakeAuthority(t, run)
	t.Run("valkey", func(t *testing.T) {
		if os.Getenv("CHAT_TEST_VALKEY_URL") == "" {
			t.Skip("CHAT_TEST_VALKEY_URL is not set")
		}
		suffix := fmt.Sprintf("%d", time.Now().UnixNano())
		var first *ValkeyPresenceDirectory
		w := newRaceWorld(t, func(id string) PresenceDirectory {
			directory := realValkeyDirectory(t, id)
			if first == nil {
				first = directory
			}
			return directory
		}, "user-"+suffix, "chan-a-"+suffix, "chan-b-"+suffix)
		w.kill = func(id string) {
			_ = first.client.Do(context.Background(), first.client.B().Del().Key(directoryLivePrefix+id).Build()).Error()
		}
		run(t, w)
	})
}

// forFakeAuthority runs a case whose clock crosses a deadline. Deadlines are
// judged by the store's own clock at the commit, so only a store whose clock
// the test drives can be made to see one crossed; the real Lua is held to the
// same contract against Valkey's own TIME by the TestCommitClockReal_* cases.
func forFakeAuthority(t *testing.T, run func(t *testing.T, w *raceWorld)) {
	t.Helper()
	t.Run("fake", func(t *testing.T) {
		shared := newFakeDirectory()
		w := newRaceWorld(t, func(id string) PresenceDirectory { return shared.view(id) }, "user-1", "chan-1", "chan-2")
		shared.clock = w.clk.Now
		w.kill = shared.killInstance
		run(t, w)
	})
}

// composeParked starts a refresh of the person on m that stops right after it
// read its facts, and returns what it publishes once released.
func composeParked(t *testing.T, w *raceWorld, m *clusterMember, start func()) (release func() []Event) {
	t.Helper()
	parked, resume := w.source.parkNextRead()
	start()
	published := make(chan []Event)
	go func() { published <- drainPresenceEvents(t, m.hub) }()
	<-parked
	return func() []Event {
		resume()
		return <-published
	}
}

func storedProjection(t *testing.T, m *clusterMember, userID string) *PresenceProjection {
	t.Helper()
	records, err := m.hub.userPresence().ReadUsers(context.Background(), "ws-1", []string{userID})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return records[userID].Projection
}

// TEST C: Busy until T10 is read at T9; nothing sweeps, hints or projects; the
// commit happens at T11. Busy is not committed, not even as "unchanged".
func TestFreshness_ManualStateEndingBeforeTheCommitIsNotCommitted(t *testing.T) {
	forFakeAuthority(t, func(t *testing.T, w *raceWorld) {
		w.a.observe(t, "c-obs", w.chanA)
		w.source.set(w.user, manual(domain.PresenceManualBusy, w.clk.Now().Add(10*time.Second)))
		w.a.join(t, "c-a", w.user, w.chanA)
		if got := availabilitiesFor(w.a.lastEvents, w.user); len(got) == 0 || got[len(got)-1] != "busy" {
			t.Fatalf("setup published %v", got)
		}
		w.clk.Advance(9 * time.Second)
		release := composeParked(t, w, w.a, func() { w.a.hub.RefreshPresence("ws-1", w.user) })
		w.clk.Advance(2 * time.Second)
		events := release()
		if got := availabilitiesFor(events, w.user); len(got) != 1 || got[0] != "available" {
			t.Fatalf("a commit after Busy ended published %v, want available", got)
		}
		if p := storedProjection(t, w.a, w.user); p == nil || p.Effective.Availability != domain.PresenceAvailable {
			t.Fatalf("committed = %+v", p)
		}
	})
}

// TEST D: in a call whose lease ends at T10, read at T9, committed at T11.
func TestFreshness_CallLeaseEndingBeforeTheCommitIsNotCommitted(t *testing.T) {
	forFakeAuthority(t, func(t *testing.T, w *raceWorld) {
		w.a.observe(t, "c-obs", w.chanA)
		w.source.set(w.user, domain.PresenceContext{
			Activity: domain.PresenceActivityInCall, ActivityUntil: w.clk.Now().Add(10 * time.Second),
		})
		w.a.join(t, "c-a", w.user, w.chanA)
		w.clk.Advance(9 * time.Second)
		release := composeParked(t, w, w.a, func() { w.a.hub.RefreshPresence("ws-1", w.user) })
		w.clk.Advance(2 * time.Second)
		events := release()
		for _, evt := range events {
			if evt.Presence != nil && evt.Presence.UserID == w.user && evt.Presence.Activity != "" {
				t.Fatalf("a lapsed call was published: %+v", evt.Presence)
			}
		}
		if got := availabilitiesFor(events, w.user); len(got) != 1 || got[0] != "available" {
			t.Fatalf("published %v, want available", got)
		}
	})
}

// HIGH-A: the composition is resolved and its commit built at T9 — Busy,
// valid until T10 — and the commit reaches the store only at T11. The instant
// the composer captured does not decide: the store's clock at the commit does.
func TestFreshness_ManualStateEndingWhileTheCommitTravelsIsNotCommitted(t *testing.T) {
	forFakeAuthority(t, func(t *testing.T, w *raceWorld) {
		w.a.observe(t, "c-obs", w.chanA)
		w.a.join(t, "c-a", w.user, w.chanA)
		w.source.set(w.user, manual(domain.PresenceManualBusy, w.clk.Now().Add(10*time.Second)))
		w.clk.Advance(9 * time.Second)
		faults := wrapFaults(w.a)
		faults.pauseBefore("project", func() { w.clk.Advance(2 * time.Second) })
		w.a.hub.RefreshPresence("ws-1", w.user)
		if got := availabilitiesFor(drainPresenceEvents(t, w.a.hub), w.user); len(got) != 0 {
			t.Fatalf("a Busy that ended while its commit travelled published %v", got)
		}
		if p := storedProjection(t, w.a, w.user); p == nil || p.Effective.Availability != domain.PresenceAvailable {
			t.Fatalf("committed = %+v, want the available it already was", p)
		}
	})
}

// HIGH-A, the same for a call lease: in a call until T10, the commit built at
// T9 arrives at T11.
func TestFreshness_CallLeaseEndingWhileTheCommitTravelsIsNotCommitted(t *testing.T) {
	forFakeAuthority(t, func(t *testing.T, w *raceWorld) {
		w.a.observe(t, "c-obs", w.chanA)
		w.a.join(t, "c-a", w.user, w.chanA)
		w.source.set(w.user, domain.PresenceContext{
			Activity: domain.PresenceActivityInCall, ActivityUntil: w.clk.Now().Add(10 * time.Second),
		})
		w.clk.Advance(9 * time.Second)
		faults := wrapFaults(w.a)
		faults.pauseBefore("project", func() { w.clk.Advance(2 * time.Second) })
		w.a.hub.RefreshPresence("ws-1", w.user)
		if got := availabilitiesFor(drainPresenceEvents(t, w.a.hub), w.user); len(got) != 0 {
			t.Fatalf("a call that ended while its commit travelled published %v", got)
		}
		if p := storedProjection(t, w.a, w.user); p == nil || p.Effective != (domain.EffectivePresence{Availability: domain.PresenceAvailable}) {
			t.Fatalf("committed = %+v", p)
		}
	})
}

// TEST E: a composition counted another instance's reach; that instance died
// before the commit. What it composed is not committed.
func TestFreshness_ReachOfAnInstanceThatDiedIsNotCommitted(t *testing.T) {
	forEachStore(t, func(t *testing.T, w *raceWorld) {
		w.a.observe(t, "c-obs", w.chanA)
		w.a.join(t, "c-a", w.user, w.chanA)
		w.clk.Advance(4 * time.Minute)
		w.b.join(t, "c-b", w.user, w.chanB)
		w.clk.Advance(2 * time.Minute)
		release := composeParked(t, w, w.a, func() { w.a.tracker.checkAway() })
		w.kill(w.b.id)
		events := release()
		if got := availabilitiesFor(events, w.user); len(got) != 1 || got[0] != "away" {
			t.Fatalf("with the active replica dead before the commit, published %v, want away", got)
		}
	})
}

// announceCallChange is what the call store does around a participation
// change: it announces it for the people its locks made final, changes the
// database before committing, and closes the announcement after.
func announceCallChange(t *testing.T, m *clusterMember, users []string, change func()) {
	t.Helper()
	_, closeFacts, err := m.hub.OpenPresenceFacts(t.Context(), "ws-1", users)
	if err != nil {
		t.Fatal(err)
	}
	change()
	closeFacts()
}

// HIGH-C, the presence half: C joined, C's composition read "in a call" and
// stopped; the call ends, and C is among the people the end announces (the
// call store reads them under the call's row lock — see the PostgreSQL proof
// in storage). C's composition cannot commit the call that ended.
func TestFreshness_CallEndInvalidatesALateJoinersInFlightComposition(t *testing.T) {
	forEachStore(t, func(t *testing.T, w *raceWorld) {
		w.a.observe(t, "c-obs", w.chanA)
		w.source.set(w.user, domain.PresenceContext{Activity: domain.PresenceActivityInCall})
		w.a.join(t, "c-a", w.user, w.chanA)
		release := composeParked(t, w, w.a, func() { w.a.hub.RefreshPresence("ws-1", w.user) })
		announceCallChange(t, w.b, []string{"caller", "early-joiner", w.user}, func() { w.source.clear(w.user) })
		events := release()
		if got := availabilitiesFor(events, w.user); len(got) != 1 || got[0] != "available" {
			t.Fatalf("published %v, want available", got)
		}
		if p := storedProjection(t, w.a, w.user); p == nil || p.Effective.Activity != domain.PresenceActivityNone {
			t.Fatalf("committed = %+v: the ended call survived", p)
		}
	})
}

// HIGH-B, the presence half: the lease had lapsed, so a composition read "not
// in a call" and stopped; a heartbeat revives the lease — a revival, announced
// like an admission. The composition recomposes and publishes the call.
func TestFreshness_LeaseRevivalInvalidatesAnInFlightComposition(t *testing.T) {
	forEachStore(t, func(t *testing.T, w *raceWorld) {
		w.a.observe(t, "c-obs", w.chanA)
		w.a.join(t, "c-a", w.user, w.chanA)
		release := composeParked(t, w, w.a, func() { w.a.hub.RefreshPresence("ws-1", w.user) })
		announceCallChange(t, w.b, []string{w.user}, func() {
			w.source.set(w.user, domain.PresenceContext{
				Activity: domain.PresenceActivityInCall, ActivityUntil: w.clk.Now().Add(time.Hour),
			})
		})
		events := release()
		var last *PresencePayload
		for _, evt := range events {
			if evt.Presence != nil && evt.Presence.UserID == w.user {
				last = evt.Presence
			}
		}
		if last == nil || last.Availability != "busy" || last.Activity != "in_call" {
			t.Fatalf("published %+v, want busy in a call", last)
		}
	})
}

// TEST F, A: the invalidation cannot be opened, so the change is not made; the
// composition that read the old fact commits the old fact — which is still the
// truth. Then the store recovers, the change is made, and it converges.
func TestFreshness_FailedInvalidationRefusesTheChange(t *testing.T) {
	forEachStore(t, func(t *testing.T, w *raceWorld) {
		w.a.observe(t, "c-obs", w.chanA)
		w.source.set(w.user, manual(domain.PresenceManualBusy, w.clk.Now().Add(time.Hour)))
		w.a.join(t, "c-a", w.user, w.chanA)
		faults := wrapFaults(w.b)
		faults.set("begin", errors.New("valkey unavailable"))

		release := composeParked(t, w, w.a, func() { w.a.hub.RefreshPresence("ws-1", w.user) })
		dnd := manual(domain.PresenceManualDoNotDisturb, w.clk.Now().Add(time.Hour))
		err := w.b.hub.ChangePresenceFacts(t.Context(), "ws-1", []string{w.user}, func(context.Context) error {
			w.source.set(w.user, dnd)
			return nil
		})
		if !errors.Is(err, domain.ErrPresenceFactsUnavailable) {
			t.Fatalf("an unannounced change = %v", err)
		}
		release()
		if state := w.source.contextOf(w.user).Override.State; state != domain.PresenceManualBusy {
			t.Fatalf("the refused change reached the database: %s", state)
		}
		if p := storedProjection(t, w.a, w.user); p == nil || p.Effective.Availability != domain.PresenceBusy {
			t.Fatalf("projection = %+v, want the database's busy", p)
		}

		faults.set("begin", nil)
		if err := w.b.hub.ChangePresenceFacts(t.Context(), "ws-1", []string{w.user}, func(context.Context) error {
			w.source.set(w.user, dnd)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		w.a.hub.RefreshPresence("ws-1", w.user)
		if got := availabilitiesFor(drainPresenceEvents(t, w.a.hub), w.user); len(got) != 1 || got[0] != "dnd" {
			t.Fatalf("after recovery published %v, want dnd", got)
		}
	})
}

// TEST F, B: the change is made but its end cannot be recorded. The mark stays
// until its lease: the composition that read the old fact never commits it,
// nothing is published meanwhile, and after the lease the sweep converges.
func TestFreshness_UnendedChangeHoldsCompositionsUntilItsLease(t *testing.T) {
	forFakeAuthority(t, func(t *testing.T, w *raceWorld) {
		w.a.observe(t, "c-obs", w.chanA)
		w.source.set(w.user, manual(domain.PresenceManualBusy, w.clk.Now().Add(time.Hour)))
		w.a.join(t, "c-a", w.user, w.chanA)
		metrics := newRecordingPresenceMetrics()
		w.a.hub.presenceMetrics = metrics
		faults := wrapFaults(w.b)
		faults.set("end", errors.New("valkey unavailable"))

		release := composeParked(t, w, w.a, func() { w.a.hub.RefreshPresence("ws-1", w.user) })
		dnd := manual(domain.PresenceManualDoNotDisturb, w.clk.Now().Add(time.Hour))
		if err := w.b.hub.ChangePresenceFacts(t.Context(), "ws-1", []string{w.user}, func(context.Context) error {
			w.source.set(w.user, dnd)
			return nil
		}); err != nil {
			t.Fatalf("the change itself was made: %v", err)
		}
		if events := release(); len(availabilitiesFor(events, w.user)) != 0 {
			t.Fatalf("published %v while the change was unsettled", availabilitiesFor(events, w.user))
		}
		if metrics.deferredCount("projection_conflict") != 1 {
			t.Fatalf("deferred = %+v", metrics.deferred)
		}
		if p := storedProjection(t, w.a, w.user); p == nil || p.Effective.Availability != domain.PresenceBusy {
			t.Fatalf("projection = %+v, want nothing new committed", p)
		}

		w.clk.Advance(presenceFactsChangeLease + time.Second)
		w.a.hub.sweepPresenceContexts()
		if got := availabilitiesFor(drainPresenceEvents(t, w.a.hub), w.user); len(got) != 1 || got[0] != "dnd" {
			t.Fatalf("after the lease published %v, want dnd", got)
		}
	})
}

// TEST H: many compositions read one revision, one fact change lands, then
// they all commit at once. None of them is applied; a fresh one is, once.
func TestFreshness_ConcurrentCompositionsAgainstOneFactChange(t *testing.T) {
	stores := map[string]func(t *testing.T) (UserPresenceStore, string){
		"fake": func(t *testing.T) (UserPresenceStore, string) {
			return newTestValkeyDirectory(t, newFakeValkeyServer(), "runtime-a"), "ws-1"
		},
		"valkey": func(t *testing.T) (UserPresenceStore, string) {
			return realValkeyDirectory(t, "runtime-a"), realWorkspace()
		},
	}
	for name, open := range stores {
		t.Run(name, func(t *testing.T) {
			store, workspace := open(t)
			ctx := context.Background()
			stale := revisionIn(t, store, workspace, "user-1")
			if err := touchFacts(store, ctx, workspace, "user-1"); err != nil {
				t.Fatal(err)
			}
			now := time.Unix(1_790_000_000, 0).UTC()
			outcomes := make(chan projectionOutcome, 16)
			var wg sync.WaitGroup
			for i := range 16 {
				wg.Go(func() {
					availability := []domain.PresenceAvailability{domain.PresenceBusy, domain.PresenceAway}[i%2]
					_, outcome, err := store.Project(ctx, workspace, "user-1",
						commitOf(domain.EffectivePresence{Availability: availability}, stale, now))
					if err != nil {
						t.Error(err)
					}
					outcomes <- outcome
				})
			}
			wg.Wait()
			close(outcomes)
			for outcome := range outcomes {
				if outcome != projectionConflict {
					t.Fatalf("a composition from before the change got %v", outcome)
				}
			}
			fresh := revisionIn(t, store, workspace, "user-1")
			if _, outcome, _ := store.Project(ctx, workspace, "user-1",
				commitOf(domain.EffectivePresence{Availability: domain.PresenceDoNotDisturb}, fresh, now)); outcome != projectionApplied {
				t.Fatalf("the fresh composition = %v", outcome)
			}
		})
	}
}

func revisionIn(t *testing.T, store UserPresenceStore, workspace, userID string) uint64 {
	t.Helper()
	records, err := store.ReadUsers(context.Background(), workspace, []string{userID})
	if err != nil {
		t.Fatal(err)
	}
	return records[userID].Revision
}

// Every per-user operation failing on its own: nothing is published, nothing
// recorded, the reason is observable, and recovery converges.
func TestFreshness_EachFailingOperationDefersAndRecovers(t *testing.T) {
	for _, op := range []string{"assert", "read", "project"} {
		t.Run(op, func(t *testing.T) {
			cluster := newPresenceCluster(t)
			a := cluster.node("node-a")
			a.observe(t, "c-obs", "chan-1")
			a.join(t, "c-a", "user-1", "chan-1")
			metrics := newRecordingPresenceMetrics()
			a.hub.presenceMetrics = metrics
			cluster.directory.failUserOp(op, errors.New("valkey unavailable"))
			cluster.source.set("user-1", manual(domain.PresenceManualBusy, cluster.clk.Now().Add(time.Hour)))
			a.hub.RefreshPresence("ws-1", "user-1")
			if got := availabilitiesFor(drainPresenceEvents(t, a.hub), "user-1"); len(got) != 0 {
				t.Fatalf("published %v through a failing %s", got, op)
			}
			if len(metrics.deferred) == 0 {
				t.Fatal("the deferral was not observable")
			}
			cluster.directory.failUserOp(op, nil)
			a.hub.sweepPresenceContexts()
			if got := availabilitiesFor(drainPresenceEvents(t, a.hub), "user-1"); len(got) != 1 || got[0] != "busy" {
				t.Fatalf("recovery published %v", got)
			}
		})
	}
}
