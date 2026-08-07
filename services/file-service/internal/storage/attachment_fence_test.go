package storage

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/nicrepository/nchat/services/file-service/internal/domain"
	"github.com/nicrepository/nchat/services/file-service/internal/service"
)

// The fence is tested from inside the package because the interesting parts are
// the key derivation and what happens to a connection whose unlock cannot be
// confirmed — neither is reachable from outside, and both decide whether an
// attachment can end up fenced forever.

func fenceLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// The key must come from the id and nothing else, and two different attachments
// must not routinely collide.
func TestAttachmentFenceKeyIsDerivedFromTheAttachmentID(t *testing.T) {
	id := uuid.NewString()

	first, err := attachmentFenceKey(id)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	second, err := attachmentFenceKey(id)
	if err != nil {
		t.Fatalf("derive again: %v", err)
	}
	if first != second {
		t.Fatal("the same attachment must derive the same key")
	}

	other, err := attachmentFenceKey(uuid.NewString())
	if err != nil {
		t.Fatalf("derive other: %v", err)
	}
	if other == first {
		t.Fatal("two attachments derived the same key")
	}
}

// Nothing that is not an attachment id can reach the derivation, so no caller
// can steer the lock space.
func TestAttachmentFenceKeyRefusesAnythingThatIsNotAnID(t *testing.T) {
	for _, invalid := range []string{"", "not-a-uuid", "'; SELECT 1", "../../etc/passwd"} {
		if _, err := attachmentFenceKey(invalid); !errors.Is(err, domain.ErrInvalidInput) {
			t.Fatalf("%q: error = %v, want ErrInvalidInput", invalid, err)
		}
	}
}

// fakeFenceConn records what the fence did to its connection.
type fakeFenceConn struct {
	lockErr     error
	unlockOK    bool
	unlockErr   error
	discardErr  error
	locked      bool
	unlocked    bool
	released    bool
	discarded   bool
	discardCall int
}

func (c *fakeFenceConn) TryLock(context.Context, int64) (bool, error) { return true, nil }

func (c *fakeFenceConn) Lock(_ context.Context, _ int64) error {
	if c.lockErr != nil {
		return c.lockErr
	}
	c.locked = true
	return nil
}

func (c *fakeFenceConn) Unlock(context.Context, int64) (bool, error) {
	c.unlocked = true
	return c.unlockOK, c.unlockErr
}

func (c *fakeFenceConn) Release() { c.released = true }

func (c *fakeFenceConn) Discard(context.Context) error {
	c.discarded = true
	c.discardCall++
	return c.discardErr
}

type fakeFenceConnPool struct {
	conn       *fakeFenceConn
	acquireErr error
}

func (p *fakeFenceConnPool) Acquire(context.Context) (LockConn, error) {
	if p.acquireErr != nil {
		return nil, p.acquireErr
	}
	return p.conn, nil
}

// A confirmed unlock proves the session holds nothing, so the connection can go
// back to the pool.
func TestFenceReleasesAConnectionWhoseUnlockIsConfirmed(t *testing.T) {
	conn := &fakeFenceConn{unlockOK: true}
	fence := NewPGXAttachmentFence(&fakeFenceConnPool{conn: conn}, nil, fenceLogger())

	handle, err := fence.Acquire(context.Background(), uuid.NewString())
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if !conn.locked {
		t.Fatal("the fence did not take the lock")
	}
	handle.Release(context.Background())

	if !conn.unlocked || !conn.released || conn.discarded {
		t.Fatalf("unlocked=%v released=%v discarded=%v", conn.unlocked, conn.released, conn.discarded)
	}
}

// An unlock that reports the lock was not held means this process can no longer
// describe the session. Reusing it would keep an attachment fenced with nothing
// able to notice, so the connection is discarded and PostgreSQL cleans up.
func TestFenceDiscardsAConnectionItCannotProveIsLockFree(t *testing.T) {
	for name, conn := range map[string]*fakeFenceConn{
		"unlock reported false": {unlockOK: false},
		"unlock failed":         {unlockErr: errors.New("connection reset")},
	} {
		t.Run(name, func(t *testing.T) {
			fence := NewPGXAttachmentFence(&fakeFenceConnPool{conn: conn}, nil, fenceLogger())
			handle, err := fence.Acquire(context.Background(), uuid.NewString())
			if err != nil {
				t.Fatalf("acquire: %v", err)
			}
			handle.Release(context.Background())

			if !conn.discarded {
				t.Fatal("a session that cannot be proven lock-free must be discarded")
			}
			if conn.released {
				t.Fatal("a suspect connection must not go back to the pool")
			}
		})
	}
}

// Release runs from deferred code on every path, so calling it twice must not
// double-discard or panic.
func TestFenceReleaseIsIdempotent(t *testing.T) {
	conn := &fakeFenceConn{unlockErr: errors.New("gone")}
	fence := NewPGXAttachmentFence(&fakeFenceConnPool{conn: conn}, nil, fenceLogger())

	handle, err := fence.Acquire(context.Background(), uuid.NewString())
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	handle.Release(context.Background())
	handle.Release(context.Background())

	if conn.discardCall != 1 {
		t.Fatalf("connection discarded %d times, want 1", conn.discardCall)
	}
}

// A lock that was never taken leaves the session provably clean, so the
// connection is returned rather than burned.
func TestFenceReturnsTheConnectionWhenTheLockWasNeverTaken(t *testing.T) {
	conn := &fakeFenceConn{lockErr: errors.New("cancelled")}
	fence := NewPGXAttachmentFence(&fakeFenceConnPool{conn: conn}, nil, fenceLogger())

	if _, err := fence.Acquire(context.Background(), uuid.NewString()); err == nil {
		t.Fatal("expected the lock failure to be reported")
	}
	if !conn.released || conn.discarded {
		t.Fatalf("released=%v discarded=%v", conn.released, conn.discarded)
	}
}

func TestFenceReportsAnUnavailableConnection(t *testing.T) {
	fence := NewPGXAttachmentFence(
		&fakeFenceConnPool{acquireErr: errors.New("pool exhausted")}, nil, fenceLogger())
	if _, err := fence.Acquire(context.Background(), uuid.NewString()); err == nil {
		t.Fatal("expected the acquisition failure to be reported")
	}
}

func TestFenceRefusesToRunWithoutAPool(t *testing.T) {
	var fence *PGXAttachmentFence
	if _, err := fence.Acquire(context.Background(), uuid.NewString()); !errors.Is(
		err, domain.ErrUnavailable,
	) {
		t.Fatalf("error = %v, want ErrUnavailable", err)
	}
	if _, err := fence.WithinTransaction(context.Background(), 1, nil); !errors.Is(
		err, domain.ErrUnavailable,
	) {
		t.Fatalf("transaction error = %v, want ErrUnavailable", err)
	}
}

// An id that is not a UUID never reaches a connection.
func TestFenceRefusesAnInvalidAttachmentBeforeTakingAConnection(t *testing.T) {
	conn := &fakeFenceConn{unlockOK: true}
	fence := NewPGXAttachmentFence(&fakeFenceConnPool{conn: conn}, nil, fenceLogger())

	if _, err := fence.Acquire(context.Background(), "not-a-uuid"); !errors.Is(
		err, domain.ErrInvalidInput,
	) {
		t.Fatalf("error = %v, want ErrInvalidInput", err)
	}
	if conn.locked {
		t.Fatal("an invalid id must not reach a connection")
	}
}

// --- the transaction half of the fence ------------------------------------

// fakeTx implements just enough of pgx.Tx to observe what WithinTransaction
// does: it embeds the interface so unused methods panic rather than silently
// returning zero values.
type fakeTx struct {
	pgx.Tx

	execSQL    string
	execErr    error
	committed  bool
	rolledBack bool
	commitErr  error
}

func (t *fakeTx) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	t.execSQL = sql
	return pgconn.CommandTag{}, t.execErr
}

func (t *fakeTx) Commit(context.Context) error {
	t.committed = true
	return t.commitErr
}

func (t *fakeTx) Rollback(context.Context) error {
	t.rolledBack = true
	return nil
}

type fakeTxPool struct {
	tx       *fakeTx
	beginErr error
}

func (p *fakeTxPool) Begin(context.Context) (pgx.Tx, error) {
	if p.beginErr != nil {
		return nil, p.beginErr
	}
	return p.tx, nil
}

// The invalidation side takes a *transaction-scoped* lock and runs its
// statement on the same connection: one connection, one transaction, so an
// invalidation never waits for a second connection while holding a lock.
func TestWithinTransactionTakesTheLockAndCommits(t *testing.T) {
	tx := &fakeTx{}
	fence := NewPGXAttachmentFence(nil, &fakeTxPool{tx: tx}, fenceLogger())

	ran := false
	state, err := fence.WithinTransaction(context.Background(), 42,
		func(q TransactionalQuerier) (service.AttachmentLifecycleState, error) {
			ran = true
			if q != TransactionalQuerier(tx) {
				t.Fatal("the statement must run inside the fenced transaction")
			}
			return service.AttachmentLifecycleState{Status: domain.StatusRejected}, nil
		})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ran {
		t.Fatal("the statement never ran")
	}
	if state.Status != domain.StatusRejected {
		t.Fatalf("state = %+v", state)
	}
	if tx.execSQL != `SELECT pg_advisory_xact_lock($1)` {
		t.Fatalf("the fence took %q, want a transaction-scoped advisory lock", tx.execSQL)
	}
	if !tx.committed {
		t.Fatal("a successful transition must commit")
	}
}

// A failing statement must not commit: the lock is released by the rollback and
// the transition is not recorded.
func TestWithinTransactionRollsBackAFailedStatement(t *testing.T) {
	tx := &fakeTx{}
	fence := NewPGXAttachmentFence(nil, &fakeTxPool{tx: tx}, fenceLogger())

	statementErr := errors.New("no row matched")
	if _, err := fence.WithinTransaction(context.Background(), 42,
		func(TransactionalQuerier) (service.AttachmentLifecycleState, error) {
			return service.AttachmentLifecycleState{}, statementErr
		}); !errors.Is(err, statementErr) {
		t.Fatalf("error = %v, want the statement's own", err)
	}
	if tx.committed {
		t.Fatal("a failed transition must not commit")
	}
	if !tx.rolledBack {
		t.Fatal("a failed transition must roll back, which is what frees the lock")
	}
}

// A lock that cannot be taken stops the transition before the statement runs.
func TestWithinTransactionStopsWhenTheLockCannotBeTaken(t *testing.T) {
	tx := &fakeTx{execErr: errors.New("cancelled")}
	fence := NewPGXAttachmentFence(nil, &fakeTxPool{tx: tx}, fenceLogger())

	ran := false
	if _, err := fence.WithinTransaction(context.Background(), 42,
		func(TransactionalQuerier) (service.AttachmentLifecycleState, error) {
			ran = true
			return service.AttachmentLifecycleState{}, nil
		}); err == nil {
		t.Fatal("expected the lock failure to be reported")
	}
	if ran {
		t.Fatal("the statement ran without the fence")
	}
}

func TestWithinTransactionReportsAFailedBeginAndCommit(t *testing.T) {
	beginFailure := NewPGXAttachmentFence(
		nil, &fakeTxPool{beginErr: errors.New("pool exhausted")}, fenceLogger())
	if _, err := beginFailure.WithinTransaction(context.Background(), 1,
		func(TransactionalQuerier) (service.AttachmentLifecycleState, error) {
			return service.AttachmentLifecycleState{}, nil
		}); err == nil {
		t.Fatal("expected the begin failure to be reported")
	}

	commitFailure := NewPGXAttachmentFence(
		nil, &fakeTxPool{tx: &fakeTx{commitErr: errors.New("connection reset")}}, fenceLogger())
	if _, err := commitFailure.WithinTransaction(context.Background(), 1,
		func(TransactionalQuerier) (service.AttachmentLifecycleState, error) {
			return service.AttachmentLifecycleState{}, nil
		}); err == nil {
		t.Fatal("expected the commit failure to be reported")
	}
}

// The pool has to be able to open a transaction, or the fence cannot lock.
func TestTransactionPoolFromRejectsAPoolThatCannotBegin(t *testing.T) {
	if _, ok := TransactionPoolFrom(nil); ok {
		t.Fatal("a nil pool must not pass as a transaction source")
	}
}
