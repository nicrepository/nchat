package storage_test

import (
	"context"
	"errors"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
	"github.com/nicrepository/nchat/services/chat-service/internal/ws"
)

// Real-PostgreSQL proofs that a call change announces exactly the people it
// changes, and that the set is final when it is announced (issue #798):
//
//   - HIGH-B: a renewal is classified extension or revival in the statement
//     that renews it; only a revival is announced; and a presence read is
//     ordered against a renewal in flight by the lease row lock.
//   - HIGH-C: admission, renewal, leave and a resource call's end serialize on
//     the call row; the end reads and announces its participants under that
//     lock, so whoever is admitted is either announced by the end or refused.
//
// The last tests run the composer's side against a real Valkey too.

// gatedFacts is a storage.PresenceFacts that records each announcement and can
// hold the announcing transaction inside it — with its locks — until released.
type gatedFacts struct {
	mu      sync.Mutex
	opened  [][]string
	err     error
	gate    chan struct{} // nil: never held
	inside  chan struct{}
	release func()
}

// newGatedFacts holds the announcing transaction until release — called by
// the test, or at its end at the latest, so a failing test never leaves a
// transaction holding the locks its cleanup needs.
func newGatedFacts(t *testing.T) *gatedFacts {
	f := &gatedFacts{gate: make(chan struct{}), inside: make(chan struct{}, 1)}
	f.release = sync.OnceFunc(func() { close(f.gate) })
	t.Cleanup(f.release)
	return f
}

// awaitAnnouncement waits until the operation is held inside its
// announcement, and fails if it finished without making one.
func awaitAnnouncement(t *testing.T, f *gatedFacts, done <-chan error) {
	t.Helper()
	select {
	case <-f.inside:
	case err := <-done:
		t.Fatalf("finished without announcing anything (err %v)", err)
	}
}

func (f *gatedFacts) OpenPresenceFacts(ctx context.Context, _ string, userIDs []string) (context.Context, func(), error) {
	f.mu.Lock()
	f.opened = append(f.opened, slices.Clone(userIDs))
	gate, err := f.gate, f.err
	f.mu.Unlock()
	if err != nil {
		return nil, nil, err
	}
	if gate != nil {
		f.inside <- struct{}{}
		<-gate
	}
	return ctx, func() {}, nil
}

func (f *gatedFacts) announced() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.opened)
}

func announcedUsers(f *gatedFacts) []string {
	var users []string
	for _, set := range f.announced() {
		users = append(users, set...)
	}
	slices.Sort(users)
	return users
}

// presenceCallWorld is one active resource call started by A.
type presenceCallWorld struct {
	pool   *pgxpool.Pool
	call   domain.Call
	leases map[string]string // user → participation
}

func newPresenceCallWorld(t *testing.T) *presenceCallWorld {
	t.Helper()
	pool := newCallLeavePool(t)
	call, _, participation, _, err := storage.NewPGXCallStore(pool).CreateResourceCall(t.Context(), storage.CreateResourceCallInput{
		WorkspaceID: leavePGWorkspace, RequestID: "c5000000-0000-4000-8000-0000000007f1", CallerID: leavePGUserA,
		TargetType: domain.CallTargetChannel, TargetID: leavePGChannel, Type: domain.CallTypeAudio,
		ExpiresAt: time.Now().Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("A starts the call: %v", err)
	}
	return &presenceCallWorld{pool: pool, call: call, leases: map[string]string{leavePGUserA: participation}}
}

func (w *presenceCallWorld) store(facts storage.PresenceFacts) *storage.PGXCallStore {
	store := storage.NewPGXCallStore(w.pool)
	if facts != nil {
		store.SetPresenceFacts(facts)
	}
	return store
}

func (w *presenceCallWorld) join(t *testing.T, store *storage.PGXCallStore, userID string) error {
	t.Helper()
	_, participation, err := store.JoinResourceCall(t.Context(), storage.JoinResourceCallInput{
		WorkspaceID: leavePGWorkspace, CallID: w.call.ID, ActorID: userID,
		TargetType: domain.CallTargetChannel, TargetID: leavePGChannel, ExpiresAt: time.Now().Add(time.Minute),
	})
	if err == nil {
		w.leases[userID] = participation
	}
	return err
}

func (w *presenceCallWorld) end(t *testing.T, store *storage.PGXCallStore) (storage.TransitionCallResult, error) {
	t.Helper()
	return store.TransitionCall(t.Context(), storage.TransitionCallInput{
		WorkspaceID: leavePGWorkspace, CallID: w.call.ID, ActorID: leavePGUserA, Action: storage.CallActionEnd,
	})
}

func (w *presenceCallWorld) renew(t *testing.T, store *storage.PGXCallStore, userID string) error {
	t.Helper()
	return store.RenewCallPresence(t.Context(), storage.RenewCallPresenceInput{
		WorkspaceID: leavePGWorkspace, CallID: w.call.ID, ActorID: userID,
		ParticipationID: w.leases[userID], ExpiresAt: time.Now().Add(time.Minute),
	})
}

// lapse ends a lease the way time does: its end is now behind the database clock.
func (w *presenceCallWorld) lapse(t *testing.T, userID string) {
	t.Helper()
	if _, err := w.pool.Exec(t.Context(),
		`UPDATE chat.call_participant_leases SET expires_at = clock_timestamp() WHERE call_id = $1 AND user_id = $2`,
		w.call.ID, userID); err != nil {
		t.Fatal(err)
	}
}

// inBackground runs op and hands back its error once it finishes.
func inBackground(op func() error) <-chan error {
	done := make(chan error, 1)
	go func() { done <- op() }()
	return done
}

// HIGH-B, TESTS B and D: a renewal of a live lease is an extension and is not
// announced; once the lease's end is not after the database clock — exactly
// at it included — the renewal is a revival, announced. The presence read
// agrees: it reports the lapsed lease as no call at all.
func TestCallPresencePG_OnlyARevivalIsAnnounced(t *testing.T) {
	w := newPresenceCallWorld(t)
	facts := &gatedFacts{}
	store := w.store(facts)
	if err := w.renew(t, store, leavePGUserA); err != nil {
		t.Fatal(err)
	}
	if got := facts.announced(); len(got) != 0 {
		t.Fatalf("an extension of a live lease was announced: %v", got)
	}
	w.lapse(t, leavePGUserA)
	contexts, err := storage.NewPGXPresenceStore(w.pool).Contexts(t.Context(), leavePGWorkspace, []string{leavePGUserA})
	if err != nil || contexts[leavePGUserA].Activity != domain.PresenceActivityNone {
		t.Fatalf("a lapsed lease read as %+v (%v)", contexts[leavePGUserA], err)
	}
	if err := w.renew(t, store, leavePGUserA); err != nil {
		t.Fatal(err)
	}
	if got := facts.announced(); len(got) != 1 || !slices.Equal(got[0], []string{leavePGUserA}) {
		t.Fatalf("the revival announced %v", got)
	}
}

// HIGH-B, TEST C: two heartbeats revive the same lapsed lease at once. They
// serialize on the participant; the first is the revival, the second finds a
// live lease and only extends it — one announcement.
func TestCallPresencePG_ConcurrentHeartbeatsReviveOnce(t *testing.T) {
	w := newPresenceCallWorld(t)
	w.lapse(t, leavePGUserA)
	facts := &gatedFacts{}
	store := w.store(facts)
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			if err := w.renew(t, store, leavePGUserA); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if got := facts.announced(); len(got) != 1 {
		t.Fatalf("announced %v, want exactly one revival", got)
	}
}

// HIGH-B, TEST E: a revival that cannot be announced is not made — the lease
// stays lapsed.
func TestCallPresencePG_AnUnannouncedRevivalIsNotMade(t *testing.T) {
	w := newPresenceCallWorld(t)
	w.lapse(t, leavePGUserA)
	unavailable := errors.New("presence unavailable")
	if err := w.renew(t, w.store(&gatedFacts{err: unavailable}), leavePGUserA); !errors.Is(err, unavailable) {
		t.Fatalf("err = %v", err)
	}
	if hasActiveLease(t, w.pool, w.call.ID, leavePGUserA) {
		t.Fatal("the refused revival renewed the lease")
	}
}

// HIGH-B: a presence read that overlaps a renewal still in flight waits for it
// and reads the renewed lease — so it can never conclude "no call" from the row
// the renewal is replacing.
func TestCallPresencePG_AReadWaitsForARenewalInFlight(t *testing.T) {
	w := newPresenceCallWorld(t)
	renewal, err := w.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = renewal.Rollback(context.Background()) }()
	w.lapse(t, leavePGUserA)
	renewedUntil := time.Now().Add(time.Hour).UTC().Truncate(time.Microsecond)
	if _, err := renewal.Exec(t.Context(),
		`UPDATE chat.call_participant_leases SET expires_at = $3 WHERE call_id = $1 AND user_id = $2`,
		w.call.ID, leavePGUserA, renewedUntil); err != nil {
		t.Fatal(err)
	}
	read := make(chan domain.PresenceContext, 1)
	go func() {
		contexts, err := storage.NewPGXPresenceStore(w.pool).Contexts(context.Background(), leavePGWorkspace, []string{leavePGUserA})
		if err != nil {
			t.Error(err)
		}
		read <- contexts[leavePGUserA]
	}()
	waitForPostgresLockWaiter(t, w.pool)
	if err := renewal.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := <-read; got.Activity != domain.PresenceActivityInCall || !got.ActivityUntil.Equal(renewedUntil) {
		t.Fatalf("the read concluded %+v from the row being replaced", got)
	}
}

// HIGH-C, TEST B (join first) and TEST D: C's admission holds the call row;
// the end waits for it and then announces C with everybody else.
func TestCallPresencePG_AJoinThatWinsIsAnnouncedByTheEnd(t *testing.T) {
	w := newPresenceCallWorld(t)
	if err := w.join(t, w.store(nil), leavePGUserB); err != nil {
		t.Fatal(err)
	}
	joining := newGatedFacts(t)
	joined := inBackground(func() error { return w.join(t, w.store(joining), leavePGUserC) })
	awaitAnnouncement(t, joining, joined)
	ending := &gatedFacts{}
	ended := inBackground(func() error { _, err := w.end(t, w.store(ending)); return err })
	waitForPostgresLockWaiter(t, w.pool)
	joining.release()
	if err := <-joined; err != nil {
		t.Fatal(err)
	}
	if err := <-ended; err != nil {
		t.Fatal(err)
	}
	if got := announcedUsers(ending); !slices.Equal(got, sorted(leavePGUserA, leavePGUserB, leavePGUserC)) {
		t.Fatalf("the end announced %v, want A, B and the late joiner C", got)
	}
}

// HIGH-C, TESTS A and B (end first): the end holds the call row while it
// announces; C's join waits, and then finds the call over — refused, no lease.
func TestCallPresencePG_AJoinAfterTheEndIsRefused(t *testing.T) {
	w := newPresenceCallWorld(t)
	if err := w.join(t, w.store(nil), leavePGUserB); err != nil {
		t.Fatal(err)
	}
	ending := newGatedFacts(t)
	ended := inBackground(func() error { _, err := w.end(t, w.store(ending)); return err })
	awaitAnnouncement(t, ending, ended)
	joining := &gatedFacts{}
	joined := inBackground(func() error { return w.join(t, w.store(joining), leavePGUserC) })
	waitForPostgresLockWaiter(t, w.pool)
	ending.release()
	if err := <-ended; err != nil {
		t.Fatal(err)
	}
	if err := <-joined; !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("a join after the end = %v, want refused", err)
	}
	if got := announcedUsers(ending); !slices.Equal(got, sorted(leavePGUserA, leavePGUserB)) {
		t.Fatalf("the end announced %v", got)
	}
	if hasLeaseRow(t, w.pool, w.call.ID, leavePGUserC) || len(joining.announced()) != 0 {
		t.Fatal("the refused join left a lease or an announcement")
	}
}

// HIGH-C, TEST C: B leaves while the end is in flight. Whichever is first, B
// is announced exactly by the change that moved B.
func TestCallPresencePG_ALeaveRacingTheEndIsAnnouncedOnce(t *testing.T) {
	w := newPresenceCallWorld(t)
	if err := w.join(t, w.store(nil), leavePGUserB); err != nil {
		t.Fatal(err)
	}
	leaving := newGatedFacts(t)
	left := inBackground(func() error {
		_, err := w.store(leaving).LeaveResourceCall(t.Context(), storage.LeaveResourceCallInput{
			WorkspaceID: leavePGWorkspace, CallID: w.call.ID, ActorID: leavePGUserB, ParticipationID: w.leases[leavePGUserB],
		})
		return err
	})
	awaitAnnouncement(t, leaving, left)
	ending := &gatedFacts{}
	ended := inBackground(func() error { _, err := w.end(t, w.store(ending)); return err })
	waitForPostgresLockWaiter(t, w.pool)
	leaving.release()
	if err := <-left; err != nil {
		t.Fatal(err)
	}
	if err := <-ended; err != nil {
		t.Fatal(err)
	}
	if got := announcedUsers(leaving); !slices.Equal(got, []string{leavePGUserB}) {
		t.Fatalf("the leave announced %v", got)
	}
	if got := announcedUsers(ending); !slices.Equal(got, []string{leavePGUserA}) {
		t.Fatalf("the end announced %v, want only who still held a lease", got)
	}
}

// HIGH-C, TEST E: B's lapsed lease is being revived while A ends the call. The
// end waits for the revival and announces B too.
func TestCallPresencePG_AHeartbeatRacingTheEndIsCoveredByIt(t *testing.T) {
	w := newPresenceCallWorld(t)
	if err := w.join(t, w.store(nil), leavePGUserB); err != nil {
		t.Fatal(err)
	}
	w.lapse(t, leavePGUserB)
	reviving := newGatedFacts(t)
	revived := inBackground(func() error { return w.renew(t, w.store(reviving), leavePGUserB) })
	awaitAnnouncement(t, reviving, revived)
	ending := &gatedFacts{}
	ended := inBackground(func() error { _, err := w.end(t, w.store(ending)); return err })
	waitForPostgresLockWaiter(t, w.pool)
	reviving.release()
	if err := <-revived; err != nil {
		t.Fatal(err)
	}
	if err := <-ended; err != nil {
		t.Fatal(err)
	}
	if got := announcedUsers(ending); !slices.Equal(got, sorted(leavePGUserA, leavePGUserB)) {
		t.Fatalf("the end announced %v", got)
	}
	if err := w.renew(t, w.store(nil), leavePGUserB); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("a heartbeat after the end = %v, want refused", err)
	}
}

// HIGH-C, TEST F: two ends at once — one ends the call and announces it, the
// other finds it ended and announces nothing.
func TestCallPresencePG_TwoEndsAnnounceOnce(t *testing.T) {
	w := newPresenceCallWorld(t)
	if err := w.join(t, w.store(nil), leavePGUserB); err != nil {
		t.Fatal(err)
	}
	facts := &gatedFacts{}
	store := w.store(facts)
	results := make(chan storage.TransitionCallResult, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			result, err := w.end(t, store)
			if err != nil {
				t.Error(err)
			}
			results <- result
		})
	}
	wg.Wait()
	close(results)
	changed := 0
	for result := range results {
		if result.Changed {
			changed++
		}
	}
	if changed != 1 || len(facts.announced()) != 1 {
		t.Fatalf("changed %d times, announced %v", changed, facts.announced())
	}
}

func sorted(values ...string) []string {
	slices.Sort(values)
	return values
}

// valkeyFacts announces through a real Valkey, the way the hub does: Begin
// before the transaction commits, End after.
type valkeyFacts struct{ directory *ws.ValkeyPresenceDirectory }

func (f valkeyFacts) OpenPresenceFacts(ctx context.Context, workspaceID string, userIDs []string) (context.Context, func(), error) {
	change := ws.FactsChange{Token: time.Now().Format(time.RFC3339Nano), Lease: 30 * time.Second}
	if err := f.directory.BeginFactsChange(ctx, workspaceID, userIDs, change); err != nil {
		return nil, nil, err
	}
	return ctx, func() { _ = f.directory.EndFactsChange(context.Background(), workspaceID, userIDs, change) }, nil
}

func realPresenceDirectory(t *testing.T) *ws.ValkeyPresenceDirectory {
	t.Helper()
	url := os.Getenv("CHAT_TEST_VALKEY_URL")
	if url == "" {
		t.Skip("CHAT_TEST_VALKEY_URL is not set")
	}
	directory, err := ws.NewValkeyPresenceDirectory(url, "runtime-pg-"+time.Now().Format("150405.000000000"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(directory.Close)
	return directory
}

// staleComposition is a composer's read of one person: the revision, then the
// database.
type staleComposition struct {
	revision uint64
	context  domain.PresenceContext
}

func readComposition(t *testing.T, directory *ws.ValkeyPresenceDirectory, pool *pgxpool.Pool, userID string) staleComposition {
	t.Helper()
	records, err := directory.ReadUsers(t.Context(), leavePGWorkspace, []string{userID})
	if err != nil {
		t.Fatal(err)
	}
	contexts, err := storage.NewPGXPresenceStore(pool).Contexts(t.Context(), leavePGWorkspace, []string{userID})
	if err != nil {
		t.Fatal(err)
	}
	return staleComposition{revision: records[userID].Revision, context: contexts[userID]}
}

// commitComposition offers what was read to the real projection script and
// reports whether it became the person's presence.
func commitComposition(t *testing.T, directory *ws.ValkeyPresenceDirectory, userID string, read staleComposition) bool {
	t.Helper()
	effective := domain.ResolvePresence(domain.PresenceReachActive, read.context)
	at, _, err := directory.Project(t.Context(), leavePGWorkspace, userID, ws.ProjectionCommit{
		Effective: effective, Expected: read.revision, Now: time.Now(), ValidUntil: read.context.ActivityUntil,
	})
	if err != nil {
		t.Fatal(err)
	}
	return !at.IsZero()
}

// HIGH-B, TEST F, PostgreSQL and Valkey: a composer read A's lapsed lease — no
// call — and stopped; A's heartbeat revives the lease. The stale "available"
// is refused; a fresh composition commits A in the call.
func TestCallPresencePGValkey_ARevivalRefusesTheStaleComposition(t *testing.T) {
	directory := realPresenceDirectory(t)
	w := newPresenceCallWorld(t)
	w.lapse(t, leavePGUserA)
	stale := readComposition(t, directory, w.pool, leavePGUserA)
	if stale.context.Activity != domain.PresenceActivityNone {
		t.Fatalf("setup read %+v", stale.context)
	}
	if err := w.renew(t, w.store(valkeyFacts{directory}), leavePGUserA); err != nil {
		t.Fatal(err)
	}
	if commitComposition(t, directory, leavePGUserA, stale) {
		t.Fatal("the composition from before the revival was committed")
	}
	fresh := readComposition(t, directory, w.pool, leavePGUserA)
	if fresh.context.Activity != domain.PresenceActivityInCall || !commitComposition(t, directory, leavePGUserA, fresh) {
		t.Fatalf("the fresh composition read %+v and was not committed", fresh.context)
	}
}

// HIGH-C, TEST G, PostgreSQL and Valkey: C joined; a composer read C "in a
// call" and stopped; A ends the call. The end announced C, so the stale "in a
// call" is refused.
func TestCallPresencePGValkey_TheEndRefusesALateJoinersStaleComposition(t *testing.T) {
	directory := realPresenceDirectory(t)
	w := newPresenceCallWorld(t)
	facts := valkeyFacts{directory}
	if err := w.join(t, w.store(facts), leavePGUserC); err != nil {
		t.Fatal(err)
	}
	stale := readComposition(t, directory, w.pool, leavePGUserC)
	if stale.context.Activity != domain.PresenceActivityInCall {
		t.Fatalf("setup read %+v", stale.context)
	}
	if _, err := w.end(t, w.store(facts)); err != nil {
		t.Fatal(err)
	}
	if commitComposition(t, directory, leavePGUserC, stale) {
		t.Fatal("the late joiner's composition from before the end was committed")
	}
	if fresh := readComposition(t, directory, w.pool, leavePGUserC); fresh.context.Activity != domain.PresenceActivityNone {
		t.Fatalf("after the end C reads %+v", fresh.context)
	}
}
