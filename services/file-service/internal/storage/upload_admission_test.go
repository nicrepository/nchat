package storage_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/file-service/internal/domain"
	"github.com/nicrepository/nchat/services/file-service/internal/storage"
)

// fakeLockPool is a shared advisory-lock namespace, which is exactly what a
// PostgreSQL instance is to several file-service replicas. Two admissions built
// over one fakeLockPool therefore behave like two processes against one
// database, which is what makes the cluster-wide property testable without a
// live server.
type fakeLockPool struct {
	mu        sync.Mutex
	held      map[int64]bool
	acquired  int
	released  int
	discarded int
	// acquireErr simulates a pool that cannot lend a connection at all.
	acquireErr error
	// lockErr simulates the lock statement itself failing.
	lockErr error
	// unlockErr simulates pg_advisory_unlock erroring out.
	unlockErr error
	// unlockFalse simulates pg_advisory_unlock returning false: the session did
	// not hold the lock it was asked to release.
	unlockFalse bool
	// unlockFalseKeys narrows unlockFalse to specific keys, so the global and
	// per-user paths can be exercised separately.
	unlockFalseKeys map[int64]bool
	// discardErr simulates the session failing to close cleanly.
	discardErr error
	// unlockAttempts counts every unlock the implementation tried, including the
	// ones after a previous failure.
	unlockAttempts int
	// releasedAfterDiscard records the bug this whole change exists to prevent.
	releasedAfterDiscard bool
	// unlockAfterFinish records a connection being used after it was returned or
	// discarded.
	unlockAfterFinish bool
	// lockErrAfter delays lockErr until this many TryLocks have succeeded, so a
	// partial acquisition can be provoked deterministically.
	lockErrAfter int
	// lockAttempts counts TryLock calls, for lockErrAfter.
	lockAttempts int
	// takenKeys records the keys this pool handed out, in order.
	takenKeys []int64
	// unlockDelay makes an unlock outlast the cleanup deadline.
	unlockDelay time.Duration
	// onTryLock fires after each TryLock, so a test can cancel mid-walk.
	onTryLock func()
}

func newFakeLockPool() *fakeLockPool {
	return &fakeLockPool{held: map[int64]bool{}}
}

func (p *fakeLockPool) Acquire(ctx context.Context) (storage.LockConn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.acquireErr != nil {
		return nil, p.acquireErr
	}
	p.acquired++
	return &fakeLockConn{pool: p}, nil
}

// openConns is how many connections are still out of the pool: neither returned
// nor discarded. It must reach zero on every path.
func (p *fakeLockPool) openConns() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.acquired - p.released - p.discarded
}

func (p *fakeLockPool) heldCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.held)
}

func (p *fakeLockPool) counts() (released, discarded, unlockAttempts int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.released, p.discarded, p.unlockAttempts
}

type fakeLockConn struct {
	pool      *fakeLockPool
	own       []int64
	finished  bool
	discarded bool
}

func (c *fakeLockConn) TryLock(ctx context.Context, key int64) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	c.pool.mu.Lock()
	defer c.pool.mu.Unlock()
	c.pool.lockAttempts++
	if c.pool.lockErr != nil && c.pool.lockAttempts > c.pool.lockErrAfter {
		return false, c.pool.lockErr
	}
	if hook := c.pool.onTryLock; hook != nil {
		hook()
	}
	if c.pool.held[key] {
		return false, nil
	}
	c.pool.held[key] = true
	c.own = append(c.own, key)
	c.pool.takenKeys = append(c.pool.takenKeys, key)
	return true, nil
}

func (c *fakeLockConn) Unlock(ctx context.Context, key int64) (bool, error) {
	c.pool.mu.Lock()
	delay := c.pool.unlockDelay
	c.pool.mu.Unlock()
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
		}
	}
	c.pool.mu.Lock()
	defer c.pool.mu.Unlock()
	c.pool.unlockAttempts++
	if c.finished {
		// Using a connection after it went back to the pool or was discarded is
		// a bug, and the fake records it rather than tolerating it.
		c.pool.unlockAfterFinish = true
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if c.pool.unlockErr != nil {
		return false, c.pool.unlockErr
	}
	if c.pool.unlockFalse ||
		(c.pool.unlockFalseKeys != nil && c.pool.unlockFalseKeys[key]) {
		// pg_advisory_unlock semantics: no error, but the session did not hold
		// the lock. The lock deliberately stays "held" in the fake, which is the
		// state that makes returning this connection to the pool dangerous.
		return false, nil
	}
	delete(c.pool.held, key)
	return true, nil
}

// Release returns the connection to the pool. It drops nothing: a pooled
// session keeps whatever locks it still holds, which is exactly why returning a
// connection with an unconfirmed unlock is the defect being guarded against.
func (c *fakeLockConn) Release() {
	c.pool.mu.Lock()
	defer c.pool.mu.Unlock()
	if c.discarded {
		c.pool.releasedAfterDiscard = true
		return
	}
	if c.finished {
		return
	}
	c.finished = true
	c.pool.released++
}

// Discard ends the session, so PostgreSQL drops every advisory lock still held
// on it — the property the design relies on for crash recovery. The connection
// never goes back to the pool, whether or not the close itself succeeds.
func (c *fakeLockConn) Discard(_ context.Context) error {
	c.pool.mu.Lock()
	defer c.pool.mu.Unlock()
	if c.discarded || c.finished {
		return nil
	}
	c.discarded = true
	c.finished = true
	c.pool.discarded++
	for _, key := range c.own {
		delete(c.pool.held, key)
	}
	return c.pool.discardErr
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func admissionFor(pool storage.LockConnPool, global, perUser int) *storage.PGXUploadAdmission {
	return storage.NewPGXUploadAdmission(pool, storage.UploadAdmissionLimits{
		Global: global, PerUser: perUser,
	}, discardLogger())
}

// assertConnectionFate is the invariant every test below shares: a connection
// goes back to the pool or is discarded, never both and never twice, and a
// discarded one is never released afterwards.
func assertConnectionFate(t *testing.T, pool *fakeLockPool, wantReleased, wantDiscarded int) {
	t.Helper()
	released, discarded, _ := pool.counts()
	if released != wantReleased {
		t.Fatalf("expected %d release(s), got %d", wantReleased, released)
	}
	if discarded != wantDiscarded {
		t.Fatalf("expected %d discard(s), got %d", wantDiscarded, discarded)
	}
	pool.mu.Lock()
	defer pool.mu.Unlock()
	if pool.releasedAfterDiscard {
		t.Fatal("a discarded connection must never be returned to the pool")
	}
	if pool.unlockAfterFinish {
		t.Fatal("a connection must not be used after it was released or discarded")
	}
}

const (
	userA  = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	userB  = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	oneMiB = 1 << 20
)

func TestAcquireGrantsUpToTheGlobalLimit(t *testing.T) {
	pool := newFakeLockPool()
	admission := admissionFor(pool, 2, 2)

	first, err := admission.Acquire(context.Background(), userA, oneMiB)
	if err != nil {
		t.Fatalf("first acquisition must succeed: %v", err)
	}
	second, err := admission.Acquire(context.Background(), userB, oneMiB)
	if err != nil {
		t.Fatalf("second acquisition must succeed: %v", err)
	}

	// Two global slots, both taken: a third caller is refused however few
	// uploads they personally hold.
	if _, err := admission.Acquire(context.Background(), userA, oneMiB); !errors.Is(err, domain.ErrClusterAtCapacity) {
		t.Fatalf("expected ErrClusterAtCapacity, got %v", err)
	}

	first()
	third, err := admission.Acquire(context.Background(), userB, oneMiB)
	if err != nil {
		t.Fatalf("a released slot must be reusable: %v", err)
	}
	second()
	third()
	if pool.heldCount() != 0 {
		t.Fatalf("every lock must be released, %d still held", pool.heldCount())
	}
	if pool.openConns() != 0 {
		t.Fatalf("every connection must go back to the pool, %d open", pool.openConns())
	}
}

func TestAcquireBoundsOneUserBelowTheGlobalLimit(t *testing.T) {
	pool := newFakeLockPool()
	admission := admissionFor(pool, 4, 2)

	first, err := admission.Acquire(context.Background(), userA, oneMiB)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	second, err := admission.Acquire(context.Background(), userA, oneMiB)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The user is full while the cluster is not: this must be the user's own
	// limit, which is a different status from a full cluster.
	if _, err := admission.Acquire(context.Background(), userA, oneMiB); !errors.Is(err, domain.ErrUserAtCapacity) {
		t.Fatalf("expected ErrUserAtCapacity, got %v", err)
	}
	// And it must not have cost the cluster a slot on the way out, or a client
	// hammering its own limit would drain everyone else's capacity.
	other, err := admission.Acquire(context.Background(), userB, oneMiB)
	if err != nil {
		t.Fatalf("another user must still be admitted: %v", err)
	}

	first()
	second()
	other()
	if pool.heldCount() != 0 {
		t.Fatalf("every lock must be released, %d still held", pool.heldCount())
	}
	if pool.openConns() != 0 {
		t.Fatalf("a refused acquisition must not leak a connection, %d open", pool.openConns())
	}
}

// The property that matters is cluster-wide, not per-process: two admissions
// over one lock namespace are two replicas over one database.
func TestSlotsAreSharedAcrossInstances(t *testing.T) {
	pool := newFakeLockPool()
	replicaOne := admissionFor(pool, 2, 2)
	replicaTwo := admissionFor(pool, 2, 2)

	first, err := replicaOne.Acquire(context.Background(), userA, oneMiB)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	second, err := replicaOne.Acquire(context.Background(), userB, oneMiB)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The second replica sees the first replica's slots, which an in-process
	// counter never could.
	if _, err := replicaTwo.Acquire(context.Background(), userA, oneMiB); !errors.Is(err, domain.ErrClusterAtCapacity) {
		t.Fatalf("a second replica must not exceed the cluster limit, got %v", err)
	}

	first()
	release, err := replicaTwo.Acquire(context.Background(), userA, oneMiB)
	if err != nil {
		t.Fatalf("a slot released on one replica must be usable on another: %v", err)
	}
	release()
	second()
}

// A replica that dies drops its connections; PostgreSQL releases the session
// locks with them. Release() on the fake models exactly that.
func TestConnectionLossFreesTheSlot(t *testing.T) {
	pool := newFakeLockPool()
	replicaOne := admissionFor(pool, 1, 1)
	replicaTwo := admissionFor(pool, 1, 1)

	if _, err := replicaOne.Acquire(context.Background(), userA, oneMiB); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := replicaTwo.Acquire(context.Background(), userB, oneMiB); !errors.Is(err, domain.ErrClusterAtCapacity) {
		t.Fatalf("expected the slot to be taken, got %v", err)
	}

	// Simulate the holder's process disappearing without unlocking.
	pool.mu.Lock()
	pool.held = map[int64]bool{}
	pool.mu.Unlock()

	release, err := replicaTwo.Acquire(context.Background(), userB, oneMiB)
	if err != nil {
		t.Fatalf("a slot orphaned by a dead session must become available: %v", err)
	}
	release()
}

func TestReleaseIsIdempotent(t *testing.T) {
	pool := newFakeLockPool()
	admission := admissionFor(pool, 1, 1)

	release, err := admission.Acquire(context.Background(), userA, oneMiB)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	release()
	release()
	release()

	if pool.openConns() != 0 {
		t.Fatalf("a repeated release must not double-return a connection, %d open", pool.openConns())
	}
	// The slot is free exactly once, so the next caller gets it.
	next, err := admission.Acquire(context.Background(), userA, oneMiB)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	next()
}

// "Cannot decide" must never read as "unlimited".
func TestAdmissionFailsClosedWhenTheBackendIsUnavailable(t *testing.T) {
	t.Run("no connection", func(t *testing.T) {
		pool := newFakeLockPool()
		pool.acquireErr = errors.New("pool exhausted")

		_, err := admissionFor(pool, 4, 2).Acquire(context.Background(), userA, oneMiB)
		if !errors.Is(err, domain.ErrAdmissionUnavailable) {
			t.Fatalf("expected ErrAdmissionUnavailable, got %v", err)
		}
	})

	t.Run("lock statement fails", func(t *testing.T) {
		pool := newFakeLockPool()
		pool.lockErr = errors.New("connection reset")

		_, err := admissionFor(pool, 4, 2).Acquire(context.Background(), userA, oneMiB)
		if !errors.Is(err, domain.ErrAdmissionUnavailable) {
			t.Fatalf("expected ErrAdmissionUnavailable, got %v", err)
		}
		if pool.openConns() != 0 {
			t.Fatalf("a failed acquisition must not leak a connection, %d open", pool.openConns())
		}
	})

	t.Run("nil pool", func(t *testing.T) {
		_, err := storage.NewPGXUploadAdmission(nil, storage.UploadAdmissionLimits{Global: 1, PerUser: 1}, discardLogger()).
			Acquire(context.Background(), userA, oneMiB)
		if !errors.Is(err, domain.ErrAdmissionUnavailable) {
			t.Fatalf("expected ErrAdmissionUnavailable, got %v", err)
		}
	})
}

func TestAcquireRefusesAnUnidentifiedCaller(t *testing.T) {
	_, err := admissionFor(newFakeLockPool(), 4, 2).Acquire(context.Background(), "", oneMiB)
	if !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized, got %v", err)
	}
}

func TestAcquireHonoursACancelledContext(t *testing.T) {
	pool := newFakeLockPool()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := admissionFor(pool, 4, 2).Acquire(ctx, userA, oneMiB); err == nil {
		t.Fatal("a cancelled context must not be admitted")
	}
	if pool.openConns() != 0 {
		t.Fatalf("a cancelled acquisition must not leak a connection, %d open", pool.openConns())
	}
}

// Zero slots means "admit nothing", never "admit everything": a limit that can
// be switched off by misconfiguration is not a limit.
func TestZeroSlotsAdmitNothing(t *testing.T) {
	pool := newFakeLockPool()

	_, err := admissionFor(pool, 0, 0).Acquire(context.Background(), userA, oneMiB)
	if !errors.Is(err, domain.ErrClusterAtCapacity) {
		t.Fatalf("expected ErrClusterAtCapacity, got %v", err)
	}
}

// ── Connection lifecycle ─────────────────────────────────────────────────────
//
// Advisory locks are bound to the session that took them, so returning a
// connection to the pool is only safe once every lock on it is *confirmed*
// released. A session handed back still holding one keeps that slot out of
// circulation for as long as the pool keeps the connection — with nothing to
// notice it and nothing to fix it. Every test below asserts which of the two
// fates the connection met.

func TestReleaseReturnsACleanConnectionToThePool(t *testing.T) {
	pool := newFakeLockPool()
	admission := admissionFor(pool, 2, 2)

	release, err := admission.Acquire(context.Background(), userA, oneMiB)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	release()

	assertConnectionFate(t, pool, 1, 0)
	if _, _, attempts := pool.counts(); attempts != 2 {
		t.Fatalf("both locks must be unlocked, got %d attempts", attempts)
	}
	if pool.heldCount() != 0 {
		t.Fatalf("no lock may remain, %d held", pool.heldCount())
	}

	// A second call does nothing at all: no unlock, no second release.
	release()
	assertConnectionFate(t, pool, 1, 0)
	if _, _, attempts := pool.counts(); attempts != 2 {
		t.Fatalf("a repeated release must not unlock again, got %d attempts", attempts)
	}
}

// An unlock that errors leaves the session's lock state unknown, so the
// connection must be destroyed rather than reused.
func TestUnlockErrorDiscardsTheConnection(t *testing.T) {
	pool := newFakeLockPool()
	admission := admissionFor(pool, 2, 2)

	release, err := admission.Acquire(context.Background(), userA, oneMiB)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	pool.mu.Lock()
	pool.unlockErr = errors.New("connection reset by peer")
	pool.mu.Unlock()

	release()

	assertConnectionFate(t, pool, 0, 1)
	// Every known lock is still attempted: the connection may accept later
	// commands, and the attempt is real cleanup rather than noise.
	if _, _, attempts := pool.counts(); attempts != 2 {
		t.Fatalf("all locks must still be attempted, got %d attempts", attempts)
	}

	release()
	assertConnectionFate(t, pool, 0, 1)
}

// pg_advisory_unlock returning false is not benign: it means this session never
// held the lock the code believes it holds.
func TestUnlockReturningFalseDiscardsTheConnection(t *testing.T) {
	// Both the per-user unlock and the global one are distinct code paths.
	for _, tt := range []struct {
		name string
		of   func(userKey, globalKey int64) map[int64]bool
	}{
		{name: "user lock", of: func(userKey, _ int64) map[int64]bool { return map[int64]bool{userKey: true} }},
		{name: "global lock", of: func(_, globalKey int64) map[int64]bool { return map[int64]bool{globalKey: true} }},
		{name: "both locks", of: func(userKey, globalKey int64) map[int64]bool {
			return map[int64]bool{userKey: true, globalKey: true}
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			pool := newFakeLockPool()
			admission := admissionFor(pool, 1, 1)

			release, err := admission.Acquire(context.Background(), userA, oneMiB)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			// The two keys this acquisition took, in acquisition order.
			pool.mu.Lock()
			keys := append([]int64(nil), pool.takenKeys...)
			pool.mu.Unlock()
			if len(keys) != 2 {
				t.Fatalf("expected two locks, got %d", len(keys))
			}
			pool.mu.Lock()
			pool.unlockFalseKeys = tt.of(keys[1], keys[0])
			pool.mu.Unlock()

			release()

			assertConnectionFate(t, pool, 0, 1)
		})
	}
}

func TestBothUnlocksFailingDiscardsExactlyOnce(t *testing.T) {
	pool := newFakeLockPool()
	pool.unlockFalse = true
	admission := admissionFor(pool, 2, 2)

	release, err := admission.Acquire(context.Background(), userA, oneMiB)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	release()
	release()

	assertConnectionFate(t, pool, 0, 1)
	if _, _, attempts := pool.counts(); attempts != 2 {
		t.Fatalf("both unlocks must be attempted once each, got %d", attempts)
	}
}

// ── Rollback of a partial acquisition ────────────────────────────────────────

func TestRollbackAfterUserCapacityReturnsTheConnection(t *testing.T) {
	pool := newFakeLockPool()
	admission := admissionFor(pool, 4, 1)

	first, err := admission.Acquire(context.Background(), userA, oneMiB)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The global slot is taken and then rolled back cleanly, so this connection
	// is fine to reuse.
	if _, err := admission.Acquire(context.Background(), userA, oneMiB); !errors.Is(err, domain.ErrUserAtCapacity) {
		t.Fatalf("expected ErrUserAtCapacity, got %v", err)
	}
	first()

	assertConnectionFate(t, pool, 2, 0)
	if pool.heldCount() != 0 {
		t.Fatalf("the rolled-back global slot must be free, %d held", pool.heldCount())
	}
}

// The dangerous variant: the global slot was taken, the user had no room, and
// giving the global slot back failed. Returning that connection would strand a
// cluster-wide slot.
func TestRollbackWithAFailingUnlockDiscardsAndKeepsBothErrors(t *testing.T) {
	pool := newFakeLockPool()
	pool.unlockErr = errors.New("connection reset by peer")
	admission := admissionFor(pool, 4, 0)

	_, err := admission.Acquire(context.Background(), userA, oneMiB)

	// The acquisition failure is still what the caller maps to a status...
	if !errors.Is(err, domain.ErrUserAtCapacity) {
		t.Fatalf("the acquisition failure must survive, got %v", err)
	}
	// ...and the rollback failure remains observable behind it.
	if !strings.Contains(err.Error(), "connection reset by peer") {
		t.Fatalf("the rollback failure must stay visible, got %v", err)
	}
	assertConnectionFate(t, pool, 0, 1)
}

func TestRollbackAfterAUserAcquisitionErrorFollowsTheSameRule(t *testing.T) {
	t.Run("clean rollback releases", func(t *testing.T) {
		pool := newFakeLockPool()
		admission := admissionFor(pool, 4, 2)
		// The global lock succeeds, then the next TryLock fails.
		pool.mu.Lock()
		pool.lockErrAfter = 1
		pool.lockErr = errors.New("connection reset by peer")
		pool.mu.Unlock()

		_, err := admission.Acquire(context.Background(), userA, oneMiB)
		if !errors.Is(err, domain.ErrAdmissionUnavailable) {
			t.Fatalf("expected ErrAdmissionUnavailable, got %v", err)
		}
		assertConnectionFate(t, pool, 1, 0)
	})

	t.Run("failed rollback discards", func(t *testing.T) {
		pool := newFakeLockPool()
		admission := admissionFor(pool, 4, 2)
		pool.mu.Lock()
		pool.lockErrAfter = 1
		pool.lockErr = errors.New("connection reset by peer")
		pool.unlockFalse = true
		pool.mu.Unlock()

		if _, err := admission.Acquire(context.Background(), userA, oneMiB); err == nil {
			t.Fatal("expected an acquisition failure")
		}
		assertConnectionFate(t, pool, 0, 1)
	})
}

// A TryLock failure on the very first slot leaves no lock to release, but it
// does leave a connection whose health is unknown.
func TestAFailedGlobalAcquisitionDiscardsTheConnection(t *testing.T) {
	pool := newFakeLockPool()
	pool.lockErr = errors.New("connection reset by peer")

	_, err := admissionFor(pool, 4, 2).Acquire(context.Background(), userA, oneMiB)
	if !errors.Is(err, domain.ErrAdmissionUnavailable) {
		t.Fatalf("expected ErrAdmissionUnavailable, got %v", err)
	}
	assertConnectionFate(t, pool, 0, 1)
}

// ── Cleanup context ──────────────────────────────────────────────────────────

// By the time a deferred release runs, the request's context is usually already
// cancelled. Cleanup must not inherit it, or every upload would leak its slot.
func TestCleanupUsesAContextIndependentOfTheRequest(t *testing.T) {
	pool := newFakeLockPool()
	admission := admissionFor(pool, 2, 2)

	ctx, cancel := context.WithCancel(context.Background())
	release, err := admission.Acquire(ctx, userA, oneMiB)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The client hangs up before the upload finishes.
	cancel()
	release()

	assertConnectionFate(t, pool, 1, 0)
	if pool.heldCount() != 0 {
		t.Fatalf("a cancelled request must still free its locks, %d held", pool.heldCount())
	}
}

// And cleanup cannot wait forever: an unlock that never answers must end in a
// discarded connection, not a blocked one.
func TestCleanupThatTimesOutDiscardsTheConnection(t *testing.T) {
	// The deadline, not an injected error, is what must fail this cleanup: the
	// unlock simply takes longer than cleanup is willing to wait.
	defer storage.SetSlotReleaseTimeoutForTest(10 * time.Millisecond)()

	pool := newFakeLockPool()
	pool.unlockDelay = 200 * time.Millisecond
	admission := admissionFor(pool, 2, 2)

	release, err := admission.Acquire(context.Background(), userA, oneMiB)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	release()

	assertConnectionFate(t, pool, 0, 1)
}

// Even a discard that fails must not put the connection back: the pool is the
// one place it may never go.
func TestAFailingDiscardStillNeverReleases(t *testing.T) {
	pool := newFakeLockPool()
	pool.unlockFalse = true
	pool.discardErr = errors.New("close: broken pipe")
	admission := admissionFor(pool, 2, 2)

	release, err := admission.Acquire(context.Background(), userA, oneMiB)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	release()
	release()

	assertConnectionFate(t, pool, 0, 1)
}

// The constructor promises a usable logger, because the release path logs from
// a deferred call where a nil dereference would take the process down long after
// the upload it belonged to.
func TestNilLoggerFallsBackToADefault(t *testing.T) {
	pool := newFakeLockPool()
	pool.unlockFalse = true
	admission := storage.NewPGXUploadAdmission(pool,
		storage.UploadAdmissionLimits{Global: 1, PerUser: 1}, nil)

	release, err := admission.Acquire(context.Background(), userA, oneMiB)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Exercises the logging path in releaseConn and discard.
	release()

	assertConnectionFate(t, pool, 0, 1)
}

// A request cancelled while the slots are being walked must stop there. Walking
// on would keep issuing locks for a caller that has already gone.
func TestAcquireStopsWalkingSlotsWhenTheContextIsCancelled(t *testing.T) {
	pool := newFakeLockPool()
	// Every slot appears taken, so the loop keeps going until something stops it.
	pool.mu.Lock()
	for slot := range 4 {
		pool.held[storage.GlobalSlotKeyForTest(slot)] = true
	}
	pool.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	pool.onTryLock = cancel

	_, err := admissionFor(pool, 4, 2).Acquire(ctx, userA, oneMiB)
	if err == nil {
		t.Fatal("a cancelled acquisition must fail")
	}
	if _, _, _ = pool.counts(); pool.lockAttempts > 2 {
		t.Fatalf("the walk must stop at cancellation, got %d attempts", pool.lockAttempts)
	}
	assertConnectionFate(t, pool, 0, 1)
}
