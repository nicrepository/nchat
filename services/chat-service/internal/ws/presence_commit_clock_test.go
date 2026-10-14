package ws

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

// Every deadline a commit depends on is judged by the store, on its own clock,
// at the moment it decides (issue #798, HIGH-A): the instant the composer
// captured into ProjectionCommit.Now orders versions and nothing else. And a
// facts-change mark that lapsed is removed, a bounded batch at a time, by the
// next change or commit about the person (MEDIUM-E).

var busyNow = domain.EffectivePresence{Availability: domain.PresenceBusy}

// clockedStore is a store whose clock the test sets.
type clockedStore struct {
	store UserPresenceStore
	set   func(time.Time)
}

func clockedStores(t *testing.T) map[string]clockedStore {
	t.Helper()
	localClock := newFakeClock(time.Unix(1_790_000_000, 0).UTC())
	server := newFakeValkeyServer()
	var serverNow time.Time
	var serverMu sync.Mutex
	server.clock = func() time.Time {
		serverMu.Lock()
		defer serverMu.Unlock()
		return serverNow
	}
	return map[string]clockedStore{
		"local": {store: newLocalUserPresence("self", localClock.Now), set: func(at time.Time) {
			localClock.Advance(at.Sub(localClock.Now()))
		}},
		"valkey-protocol": {store: newTestValkeyDirectory(t, server, "self"), set: func(at time.Time) {
			serverMu.Lock()
			defer serverMu.Unlock()
			serverNow = at
		}},
	}
}

// TESTS A–D: a commit built when the deadline was ahead (Now = T9) is judged
// at the store's T11 — expired; exactly at the deadline it is expired too; a
// moment before it, applied. A composer clock running ahead does not expire a
// commit the store still holds valid either.
func TestCommitClock_TheStoresClockAtTheCommitDecides(t *testing.T) {
	deadline := time.Unix(1_790_000_010, 0).UTC()
	cases := []struct {
		name      string
		composer  time.Time
		authority time.Time
		want      projectionOutcome
	}{
		{"prepared before, committed after", deadline.Add(-time.Second), deadline.Add(time.Second), projectionExpired},
		{"committed exactly at the deadline", deadline.Add(-time.Second), deadline, projectionExpired},
		{"committed just before it", deadline.Add(-time.Second), deadline.Add(-time.Millisecond), projectionApplied},
		{"a composer clock ahead decides nothing", deadline.Add(time.Hour), deadline.Add(-time.Millisecond), projectionApplied},
	}
	for name, clocked := range clockedStores(t) {
		for i, tc := range cases {
			t.Run(name+"/"+tc.name, func(t *testing.T) {
				user := fmt.Sprintf("user-%d", i)
				clocked.set(tc.authority)
				commit := commitOf(busyNow, revisionIn(t, clocked.store, "ws-1", user), tc.composer)
				commit.ValidUntil = deadline
				if _, outcome, err := clocked.store.Project(context.Background(), "ws-1", user, commit); err != nil || outcome != tc.want {
					t.Fatalf("outcome = %v (%v), want %v", outcome, err, tc.want)
				}
			})
		}
	}
}

// HIGH-A/HIGH-C, TESTS C and E: a facts-change mark lives for its lease on
// the store's clock, dated by the store when it records it — no caller clock
// is involved. One millisecond before its end it still holds; at its end it is
// recovered, which refuses the read taken inside it; a read after the recovery
// commits.
func TestCommitClock_AMarkLivesItsLeaseOnTheStoresClock(t *testing.T) {
	begun := time.Unix(1_790_000_000, 0).UTC()
	for name, clocked := range clockedStores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			clocked.set(begun)
			if err := clocked.store.BeginFactsChange(ctx, "ws-1", []string{"user-1"}, FactsChange{Token: "t", Lease: 30 * time.Second}); err != nil {
				t.Fatal(err)
			}
			inside := revisionIn(t, clocked.store, "ws-1", "user-1")
			clocked.set(begun.Add(30*time.Second - time.Millisecond))
			if _, outcome, _ := clocked.store.Project(ctx, "ws-1", "user-1", commitOf(busyNow, inside, begun)); outcome != projectionConflict {
				t.Fatalf("committed while the change was in flight: %v", outcome)
			}
			clocked.set(begun.Add(30 * time.Second))
			if _, outcome, _ := clocked.store.Project(ctx, "ws-1", "user-1", commitOf(busyNow, inside, begun)); outcome != projectionConflict {
				t.Fatalf("a read taken inside the change survived its recovery: %v", outcome)
			}
			after := revisionIn(t, clocked.store, "ws-1", "user-1")
			if after == inside {
				t.Fatal("the recovery did not move the revision")
			}
			if _, outcome, _ := clocked.store.Project(ctx, "ws-1", "user-1", commitOf(busyNow, after, begun)); outcome != projectionApplied {
				t.Fatalf("a read after the recovery = %v", outcome)
			}
		})
	}
}

// valkeyTime is the authority's own clock, as the scripts read it.
func valkeyTime(t *testing.T, d *ValkeyPresenceDirectory) time.Time {
	t.Helper()
	reply, err := d.client.Do(context.Background(), d.client.B().Time().Build()).AsStrSlice()
	if err != nil || len(reply) != 2 {
		t.Fatalf("TIME = %v, %v", reply, err)
	}
	seconds, _ := strconv.ParseInt(reply[0], 10, 64)
	micros, _ := strconv.ParseInt(reply[1], 10, 64)
	return time.Unix(seconds, micros*int64(time.Microsecond)).UTC()
}

// TESTS E–F on the real Lua: the composer's clock is an hour behind and says
// the fact still holds; Valkey's TIME is past the deadline — expired. And a
// composer clock an hour ahead of a deadline Valkey has not reached does not
// expire anything.
func TestCommitClockReal_ValkeysTimeDecidesAtTheCommit(t *testing.T) {
	d := realValkeyDirectory(t, "runtime-clock")
	ctx, workspace := context.Background(), realWorkspace()
	authority := valkeyTime(t, d)

	lapsed := commitOf(busyNow, revisionIn(t, d, workspace, "user-lapsed"), authority.Add(-time.Hour))
	lapsed.ValidUntil = authority.Add(-time.Millisecond)
	if _, outcome, err := d.Project(ctx, workspace, "user-lapsed", lapsed); err != nil || outcome != projectionExpired {
		t.Fatalf("a deadline Valkey passed, from a composer behind it = %v (%v)", outcome, err)
	}

	held := commitOf(busyNow, revisionIn(t, d, workspace, "user-held"), authority.Add(2*time.Hour))
	held.ValidUntil = authority.Add(time.Hour)
	if _, outcome, err := d.Project(ctx, workspace, "user-held", held); err != nil || outcome != projectionApplied {
		t.Fatalf("a deadline Valkey has not reached, from a composer past it = %v (%v)", outcome, err)
	}

	if err := d.BeginFactsChange(ctx, workspace, []string{"user-held"}, FactsChange{Token: "live", Lease: time.Hour}); err != nil {
		t.Fatal(err)
	}
	if _, outcome, _ := d.Project(ctx, workspace, "user-held", commitOf(busyNow, revisionIn(t, d, workspace, "user-held"), authority)); outcome != projectionConflict {
		t.Fatalf("committed while a change was in flight: %v", outcome)
	}
	// A change whose lease is already over by the time anything reads it: the
	// read taken inside it is refused by the recovery, the next one commits.
	if err := d.BeginFactsChange(ctx, workspace, []string{"user-lapsed"}, FactsChange{Token: "dead", Lease: 0}); err != nil {
		t.Fatal(err)
	}
	inside := revisionIn(t, d, workspace, "user-lapsed")
	if _, outcome, _ := d.Project(ctx, workspace, "user-lapsed", commitOf(busyNow, inside, authority)); outcome != projectionConflict {
		t.Fatalf("a read taken inside a change that lapsed was committed: %v", outcome)
	}
	if _, outcome, _ := d.Project(ctx, workspace, "user-lapsed", commitOf(busyNow, revisionIn(t, d, workspace, "user-lapsed"), authority)); outcome != projectionApplied {
		t.Fatalf("a read after the recovery = %v", outcome)
	}
}

// markHash gives raw access to one person's hash on either Valkey.
type markHash struct {
	directory *ValkeyPresenceDirectory
	workspace string
	seed      func(field, value string)
	marks     func() []string
	now       time.Time
}

func markHashes(t *testing.T) map[string]func(t *testing.T) markHash {
	return map[string]func(t *testing.T) markHash{
		"fake": func(t *testing.T) markHash {
			server := newFakeValkeyServer()
			now := time.Unix(1_790_000_000, 0).UTC()
			server.clock = func() time.Time { return now }
			key := userPresenceKey("ws-1", "user-1")
			return markHash{
				directory: newTestValkeyDirectory(t, server, "self"), workspace: "ws-1", now: now,
				seed: func(field, value string) {
					server.mu.Lock()
					defer server.mu.Unlock()
					server.hashOf(key)[field] = value
				},
				marks: func() []string {
					server.mu.Lock()
					defer server.mu.Unlock()
					return marksIn(server.hashes[key])
				},
			}
		},
		"valkey": func(t *testing.T) markHash {
			d := realValkeyDirectory(t, "runtime-marks")
			workspace := realWorkspace()
			key := userPresenceKey(workspace, "user-1")
			return markHash{
				directory: d, workspace: workspace, now: valkeyTime(t, d),
				seed: func(field, value string) {
					if err := d.client.Do(context.Background(), d.client.B().Hset().Key(key).FieldValue().FieldValue(field, value).Build()).Error(); err != nil {
						t.Fatal(err)
					}
				},
				marks: func() []string {
					fields, err := d.client.Do(context.Background(), d.client.B().Hgetall().Key(key).Build()).AsStrMap()
					if err != nil {
						t.Fatal(err)
					}
					return marksIn(fields)
				},
			}
		},
	}
}

func marksIn(fields map[string]string) []string {
	var marks []string
	for field := range fields {
		if strings.HasPrefix(field, userChangeFieldPrefix) {
			marks = append(marks, field)
		}
	}
	return marks
}

// TESTS A, D, E: 1000 lapsed marks and one live one. A change removes at most
// factsMarkReapLimit lapsed marks and never the live one; changes and commits
// keep removing them until none is left; a recovery moves the revision once,
// however many marks it removes.
func TestFactsMarks_LapsedMarksArePhysicallyRemovedABoundedBatchAtATime(t *testing.T) {
	for name, open := range markHashes(t) {
		t.Run(name, func(t *testing.T) {
			h := open(t)
			ctx := context.Background()
			for i := range 1000 {
				h.seed(fmt.Sprintf("w:old-%d", i), strconv.FormatInt(h.now.Add(-time.Minute).UnixMilli(), 10))
			}
			h.seed("w:live", strconv.FormatInt(h.now.Add(time.Hour).UnixMilli(), 10))
			_ = h.directory.RefreshUsers(ctx, h.workspace, []string{"user-1"}) // a hash kept alive meanwhile

			before := revisionIn(t, h.directory, h.workspace, "user-1")
			change := FactsChange{Token: "next", Lease: time.Hour}
			if err := h.directory.BeginFactsChange(ctx, h.workspace, []string{"user-1"}, change); err != nil {
				t.Fatal(err)
			}
			if got := len(h.marks()); got != 1000-factsMarkReapLimit+2 {
				t.Fatalf("%d marks after one change, want exactly %d lapsed removed", got, factsMarkReapLimit)
			}
			if revisionIn(t, h.directory, h.workspace, "user-1") != before+2 {
				t.Fatal("want one move for the recovery of the lapsed marks, however many, and one for the change")
			}
			if err := h.directory.EndFactsChange(ctx, h.workspace, []string{"user-1"}, change); err != nil {
				t.Fatal(err)
			}
			for range 1000/factsMarkReapLimit + 1 {
				_, _, _ = h.directory.Project(ctx, h.workspace, "user-1", commitOf(busyNow, 0, h.now))
			}
			if marks := h.marks(); len(marks) != 1 || marks[0] != "w:live" {
				t.Fatalf("marks left = %v, want only the live one", marks)
			}
		})
	}
}

// TEST F: changes nobody ends — their End failed, again and again — do not
// make the hash grow: each one removes what lapsed before it.
func TestFactsMarks_UnendedChangesDoNotAccumulate(t *testing.T) {
	for name, open := range markHashes(t) {
		t.Run(name, func(t *testing.T) {
			h := open(t)
			ctx := context.Background()
			for i := range 500 {
				lapsedAtBirth := FactsChange{Token: fmt.Sprintf("unended-%d", i), Lease: 0}
				if err := h.directory.BeginFactsChange(ctx, h.workspace, []string{"user-1"}, lapsedAtBirth); err != nil {
					t.Fatal(err)
				}
			}
			if got := len(h.marks()); got > 1 {
				t.Fatalf("%d marks held after 500 unended changes", got)
			}
		})
	}
}

// TESTS B, C: changes begun and ended while lapsed marks are reaped. A live
// mark is never removed by a reap, and an End after a reap is harmless.
func TestFactsMarks_ConcurrentChangesSurviveTheReap(t *testing.T) {
	for name, open := range markHashes(t) {
		t.Run(name, func(t *testing.T) {
			h := open(t)
			ctx := context.Background()
			for i := range 200 {
				h.seed(fmt.Sprintf("w:old-%d", i), strconv.FormatInt(h.now.Add(-time.Minute).UnixMilli(), 10))
			}
			var wg sync.WaitGroup
			for i := range 16 {
				wg.Go(func() {
					live := FactsChange{Token: fmt.Sprintf("open-%d", i), Lease: time.Hour}
					if err := h.directory.BeginFactsChange(ctx, h.workspace, []string{"user-1"}, live); err != nil {
						t.Error(err)
					}
					if i%2 == 0 {
						if err := h.directory.EndFactsChange(ctx, h.workspace, []string{"user-1"}, live); err != nil {
							t.Error(err)
						}
					}
				})
				wg.Go(func() {
					_, _, _ = h.directory.Project(ctx, h.workspace, "user-1", commitOf(busyNow, 0, h.now))
				})
			}
			wg.Wait()
			for range 200 / factsMarkReapLimit {
				_, _, _ = h.directory.Project(ctx, h.workspace, "user-1", commitOf(busyNow, 0, h.now))
			}
			marks := h.marks()
			if len(marks) != 8 {
				t.Fatalf("marks = %v, want exactly the 8 changes still open", marks)
			}
			for _, mark := range marks {
				index, _ := strconv.Atoi(strings.TrimPrefix(mark, "w:open-"))
				if !strings.HasPrefix(mark, "w:open-") || index%2 == 0 {
					t.Fatalf("unexpected mark %s", mark)
				}
			}
		})
	}
}
