package ws

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

// The application clock decides no validity (issue #798, sixth preparation):
//
//   - HIGH-A: a facts-change mark is dated by the store that judges it, so a
//     writer whose clock is behind or ahead cannot shorten or stretch it;
//   - HIGH-B: a manual state the database returned is used, and its end
//     travels to the commit, whatever the composer's clock says;
//   - HIGH-C: a mark that lapsed without End is recovered with a revision
//     move, so a read taken inside the change cannot be committed after it.

// skewedWorld is a fake-authority world whose application clock (the hubs')
// runs appAhead in front of the authority's — the store's, which here is also
// the database's.
func skewedWorld(t *testing.T, appAhead time.Duration) (*raceWorld, *fakeClock) {
	t.Helper()
	authority := newFakeClock(time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC))
	shared := newFakeDirectory()
	shared.clock = authority.Now
	w := newRaceWorldAt(t, authority.Now().Add(appAhead), func(id string) PresenceDirectory { return shared.view(id) }, "user-1", "chan-1", "chan-2")
	w.source.clock = authority.Now
	w.kill = shared.killInstance
	return w, authority
}

func projectNow(t *testing.T, store UserPresenceStore, revision uint64) projectionOutcome {
	t.Helper()
	_, outcome, err := store.Project(context.Background(), "ws-1", "user-1", commitOf(busyNow, revision, time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	return outcome
}

// HIGH-A, TESTS A, B and E: the hub opens a change with its clock 31 s behind
// the authority, or 2 min ahead. Either way the mark holds for 30 s of the
// authority's clock — not a moment less, not a moment more.
func TestAuthorityClock_AMarkLivesItsLeaseWhateverTheAppClock(t *testing.T) {
	for _, appAhead := range []time.Duration{-31 * time.Second, 2 * time.Minute} {
		t.Run(fmt.Sprintf("app %+v", appAhead), func(t *testing.T) {
			w, authority := skewedWorld(t, appAhead)
			store := w.b.hub.userPresence()
			faults := wrapFaults(w.b)
			faults.set("end", errors.New("valkey unavailable"))
			var inside uint64
			if err := w.b.hub.ChangePresenceFacts(t.Context(), "ws-1", []string{"user-1"}, func(context.Context) error {
				inside = revisionIn(t, store, "ws-1", "user-1")
				if got := projectNow(t, store, inside); got != projectionConflict {
					t.Fatalf("a commit while the change is open = %v", got)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			authority.Advance(presenceFactsChangeLease - time.Millisecond)
			if got := projectNow(t, store, inside); got != projectionConflict {
				t.Fatalf("the mark lapsed before its lease on the authority's clock: %v", got)
			}
			authority.Advance(time.Millisecond)
			if got := projectNow(t, store, inside); got != projectionConflict {
				t.Fatalf("the read taken inside the change survived the end of its lease: %v", got)
			}
			if got := projectNow(t, store, revisionIn(t, store, "ws-1", "user-1")); got != projectionApplied {
				t.Fatalf("the mark outlived its lease on the authority's clock: %v", got)
			}
		})
	}
}

// HIGH-C, the causal test. Busy is published; a change opens (revision
// moves); A reads inside it — Busy — and stops; the database commits DND; End
// fails. The authority's clock passes the lease with nothing else happening:
// no sweep, no other composer, no change, no commit. A offers its old
// composition: the recovery moves the revision before the comparison, A's
// commit conflicts, and A reads again — DND.
func TestEndFailure_AReadTakenInsideTheChangeConflictsAfterItsLease(t *testing.T) {
	w, authority := skewedWorld(t, 0)
	w.a.observe(t, "c-obs", w.chanA)
	w.source.set(w.user, manual(domain.PresenceManualBusy, authority.Now().Add(time.Hour)))
	w.a.join(t, "c-a", w.user, w.chanA)
	committer := wrapFaults(w.a)
	writer := wrapFaults(w.b)
	writer.set("end", errors.New("valkey unavailable"))

	var release func() []Event
	if err := w.b.hub.ChangePresenceFacts(t.Context(), "ws-1", []string{w.user}, func(context.Context) error {
		release = composeParked(t, w, w.a, func() { w.a.hub.RefreshPresence("ws-1", w.user) })
		w.source.set(w.user, manual(domain.PresenceManualDoNotDisturb, authority.Now().Add(time.Hour)))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	authority.Advance(presenceFactsChangeLease)
	events := release()
	if got := committer.firstOutcome(t); got != projectionConflict {
		t.Fatalf("the composition read inside the change = %v, want a conflict", got)
	}
	if got := availabilitiesFor(events, w.user); len(got) != 1 || got[0] != "dnd" {
		t.Fatalf("published %v, want dnd", got)
	}
	if p := storedProjection(t, w.a, w.user); p == nil || p.Effective.Availability != domain.PresenceDoNotDisturb {
		t.Fatalf("committed = %+v", p)
	}
}

// HIGH-C, crash windows: the writer dies after Begin — before its database
// change (A), or after it (B). Compositions are held until the lease; then
// the recovery lets the next one read the database as it was left.
func TestEndFailure_ACrashOnEitherSideOfTheWriteIsRecovered(t *testing.T) {
	for _, tc := range []struct {
		name    string
		written bool
		want    domain.PresenceAvailability
	}{
		{"before the database change", false, domain.PresenceBusy},
		{"after the database change", true, domain.PresenceDoNotDisturb},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, authority := skewedWorld(t, 0)
			w.a.observe(t, "c-obs", w.chanA)
			w.source.set(w.user, manual(domain.PresenceManualBusy, authority.Now().Add(time.Hour)))
			w.a.join(t, "c-a", w.user, w.chanA)
			if err := w.b.hub.userPresence().BeginFactsChange(t.Context(), "ws-1", []string{w.user},
				FactsChange{Token: "crashed", Lease: presenceFactsChangeLease}); err != nil {
				t.Fatal(err)
			}
			if tc.written {
				w.source.set(w.user, manual(domain.PresenceManualDoNotDisturb, authority.Now().Add(time.Hour)))
			}
			w.a.hub.RefreshPresence("ws-1", w.user)
			if got := availabilitiesFor(drainPresenceEvents(t, w.a.hub), w.user); len(got) != 0 {
				t.Fatalf("published %v while the change was open", got)
			}
			authority.Advance(presenceFactsChangeLease)
			w.a.hub.sweepPresenceContexts()
			drainPresenceEvents(t, w.a.hub)
			if p := storedProjection(t, w.a, w.user); p == nil || p.Effective.Availability != tc.want {
				t.Fatalf("after the recovery = %+v, want %s", p, tc.want)
			}
		})
	}
}

// HIGH-B, every manual state: the database returned it as in force until
// T+30 s; the hub's clock is already at T+60 s; the authority is at T. The
// state is published as the database said. Then a composition is read at
// T+29 s and its commit reaches the authority at T+31 s: expired, read again,
// automatic.
func TestManualState_TheAppClockDoesNotEndIt(t *testing.T) {
	for _, tc := range []struct {
		state domain.PresenceManualState
		want  domain.PresenceAvailability
	}{
		{domain.PresenceManualAvailable, domain.PresenceAvailable},
		{domain.PresenceManualBusy, domain.PresenceBusy},
		{domain.PresenceManualDoNotDisturb, domain.PresenceDoNotDisturb},
		{domain.PresenceManualBeRightBack, domain.PresenceBeRightBack},
		{domain.PresenceManualAway, domain.PresenceAway},
		{domain.PresenceManualAppearOffline, domain.PresenceOffline},
	} {
		t.Run(string(tc.state), func(t *testing.T) {
			w, authority := skewedWorld(t, time.Minute)
			w.a.observe(t, "c-obs", w.chanA)
			w.source.set(w.user, manual(tc.state, authority.Now().Add(30*time.Second)))
			w.a.join(t, "c-a", w.user, w.chanA)
			if got := publicAvailability(t, w.a, w.user); got != tc.want {
				t.Fatalf("with the database saying %s, published %s", tc.state, got)
			}

			authority.Advance(29 * time.Second)
			faults := wrapFaults(w.a)
			faults.pauseBefore("project", func() { authority.Advance(2 * time.Second) })
			w.a.hub.RefreshPresence("ws-1", w.user)
			drainPresenceEvents(t, w.a.hub)
			if got := faults.firstOutcome(t); got != projectionExpired {
				t.Fatalf("a commit after the end = %v, want expired", got)
			}
			if got := publicAvailability(t, w.a, w.user); got != domain.PresenceAvailable {
				t.Fatalf("after the end, published %s, want the automatic available", got)
			}
		})
	}
}

// publicAvailability is the stored projection, offline when there is none.
func publicAvailability(t *testing.T, m *clusterMember, userID string) domain.PresenceAvailability {
	t.Helper()
	if p := storedProjection(t, m, userID); p != nil {
		return p.Effective.Availability
	}
	return domain.PresenceOffline
}

// HIGH-B, TEST 14: a manual state without an end gives no deadline at all —
// and the fields that do end bound the commit by their own end, however close.
func TestFactsValidUntil_EveryFactUsedBoundsTheCommit(t *testing.T) {
	end := time.Date(2026, 10, 5, 12, 0, 30, 0, time.UTC)
	lease := end.Add(-10 * time.Second)
	cases := []struct {
		name string
		ctx  domain.PresenceContext
		want time.Time
	}{
		{"nothing timed", domain.PresenceContext{}, time.Time{}},
		{"manual without an end", domain.PresenceContext{Override: domain.PresenceOverride{State: domain.PresenceManualBusy}}, time.Time{}},
		{"manual", domain.PresenceContext{Override: domain.PresenceOverride{State: domain.PresenceManualBusy, ExpiresAt: end}}, end},
		{"call lease", domain.PresenceContext{Activity: domain.PresenceActivityInCall, ActivityUntil: lease}, lease},
		{"untimed call", domain.PresenceContext{Activity: domain.PresenceActivityInCall}, time.Time{}},
		{"the earlier of both", domain.PresenceContext{
			Override: domain.PresenceOverride{State: domain.PresenceManualDoNotDisturb, ExpiresAt: end},
			Activity: domain.PresenceActivityInCall, ActivityUntil: lease,
		}, lease},
	}
	for _, tc := range cases {
		if got := factsValidUntil(tc.ctx); !got.Equal(tc.want) {
			t.Fatalf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

// valkeyWorldAt is a world on a real Valkey whose application clock starts at
// origin; the fake database's clock is the real one. It also returns the
// directory one of its replicas uses.
func valkeyWorldAt(t *testing.T, origin time.Time) (*raceWorld, *ValkeyPresenceDirectory) {
	t.Helper()
	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)
	var directory *ValkeyPresenceDirectory
	w := newRaceWorldAt(t, origin, func(id string) PresenceDirectory {
		directory = realValkeyDirectory(t, id)
		return directory
	}, "user-"+suffix, "chan-a-"+suffix, "chan-b-"+suffix)
	w.source.clock = time.Now
	return w, directory
}

// HIGH-A, TEST D on a real Valkey: the hub's clock 31 s behind or 2 min ahead;
// the mark it opens ends 30 s after Valkey's own TIME.
func TestAuthorityClockReal_AMarkIsDatedByValkey(t *testing.T) {
	for _, appAhead := range []time.Duration{-31 * time.Second, 2 * time.Minute} {
		t.Run(fmt.Sprintf("app %+v", appAhead), func(t *testing.T) {
			w, directory := valkeyWorldAt(t, time.Now().Add(appAhead))
			before := valkeyTime(t, directory)
			var until time.Time
			if err := w.b.hub.ChangePresenceFacts(t.Context(), "ws-1", []string{w.user}, func(context.Context) error {
				until = markUntil(t, directory, w.user)
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			after := valkeyTime(t, directory)
			if until.Before(before.Add(presenceFactsChangeLease-time.Second)) || until.After(after.Add(presenceFactsChangeLease+time.Second)) {
				t.Fatalf("mark until %v, want about %v + 30 s on Valkey's clock", until, before)
			}
		})
	}
}

// markUntil reads the one open mark's end straight from the hash.
func markUntil(t *testing.T, directory *ValkeyPresenceDirectory, userID string) time.Time {
	t.Helper()
	fields, err := directory.client.Do(context.Background(),
		directory.client.B().Hgetall().Key(userPresenceKey("ws-1", userID)).Build()).AsStrMap()
	if err != nil {
		t.Fatal(err)
	}
	for field, value := range fields {
		if strings.HasPrefix(field, userChangeFieldPrefix) {
			millis, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				t.Fatal(err)
			}
			return time.UnixMilli(millis).UTC()
		}
	}
	t.Fatal("no open mark")
	return time.Time{}
}

// HIGH-B on a real Valkey: the hub's clock is a minute ahead of a DND or an
// appear-offline the database still holds for 30 s, and Valkey is before the
// end. The state is what is published.
func TestManualStateReal_TheAppClockDoesNotEndIt(t *testing.T) {
	for _, tc := range []struct {
		state domain.PresenceManualState
		want  domain.PresenceAvailability
	}{
		{domain.PresenceManualDoNotDisturb, domain.PresenceDoNotDisturb},
		{domain.PresenceManualAppearOffline, domain.PresenceOffline},
	} {
		t.Run(string(tc.state), func(t *testing.T) {
			w, _ := valkeyWorldAt(t, time.Now().Add(time.Minute))
			w.source.set(w.user, manual(tc.state, time.Now().Add(30*time.Second)))
			w.a.join(t, "c-a", w.user, w.chanA)
			if got := publicAvailability(t, w.a, w.user); got != tc.want {
				t.Fatalf("published %s, want %s", got, tc.want)
			}
		})
	}
}

// HIGH-C on a real Valkey: a change whose lease is already over by the time
// anything reads it — the End never came. The read taken inside it is refused
// by the recovery; the read after it commits.
func TestEndFailureReal_AReadTakenInsideTheChangeConflicts(t *testing.T) {
	d := realValkeyDirectory(t, "runtime-end-failure")
	ctx, workspace := context.Background(), realWorkspace()
	if err := d.BeginFactsChange(ctx, workspace, []string{"user-1"}, FactsChange{Token: "unended", Lease: 0}); err != nil {
		t.Fatal(err)
	}
	inside := revisionIn(t, d, workspace, "user-1")
	if _, outcome, _ := d.Project(ctx, workspace, "user-1", commitOf(busyNow, inside, time.Now())); outcome != projectionConflict {
		t.Fatalf("the read taken inside the change = %v, want a conflict", outcome)
	}
	if after := revisionIn(t, d, workspace, "user-1"); after != inside+1 {
		t.Fatalf("revision %d after the recovery, want %d", after, inside+1)
	}
	if _, outcome, _ := d.Project(ctx, workspace, "user-1", commitOf(busyNow, inside+1, time.Now())); outcome != projectionApplied {
		t.Fatalf("the read after the recovery = %v", outcome)
	}
}
