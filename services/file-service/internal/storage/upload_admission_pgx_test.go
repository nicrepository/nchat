package storage_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nicrepository/nchat/services/file-service/internal/storage"
)

// LockConnPoolFrom is the seam between "we have a database" and "admission
// control can work at all". It must never invent a usable lock pool from
// something that cannot lend a session, because the caller turns a false into a
// start-up failure and a true into a live concurrency control.
func TestLockConnPoolFromAcceptsOnlyAPgxPool(t *testing.T) {
	if _, ok := storage.LockConnPoolFrom(&fakeAdmissionPool{}); ok {
		t.Fatal("a pool that cannot lend a session must not become a lock pool")
	}
	// A pgx pool qualifies. It is never dialled here: the type is the whole
	// question this function answers.
	if _, ok := storage.LockConnPoolFrom((*pgxpool.Pool)(nil)); !ok {
		t.Fatal("a pgx pool must expose lock connections")
	}
}

// Once a connection has been handed back or destroyed, every further call must
// be inert. pgxpool panics on a double release or a double hijack, so these
// guards are what make the idempotent release safe rather than merely tidy.
func TestAFinishedLockConnIsInert(t *testing.T) {
	conn := storage.NewFinishedLockConnForTest()
	ctx := context.Background()

	// No panic, no error: discarding twice is a no-op.
	if err := conn.Discard(ctx); err != nil {
		t.Fatalf("discarding a finished connection must be a no-op, got %v", err)
	}
	// No panic: releasing after discard must not reach the pool.
	conn.Release()
	conn.Release()

	// Using it is a reported error rather than a nil-pointer dereference.
	released, err := conn.Unlock(ctx, 1)
	if err == nil {
		t.Fatal("unlocking a finished connection must report an error")
	}
	if released {
		t.Fatal("a finished connection cannot have released anything")
	}
}

// A pool that cannot produce a connection must surface the failure rather than
// hand back a nil connection the caller would then use. This is the path that
// becomes ErrAdmissionUnavailable, and admission failing closed depends on it.
//
// pgxpool connects lazily, so an unreachable address exercises it without a
// database: the connection is simply refused.
func TestAcquireReportsAPoolThatCannotLendAConnection(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), "postgres://u:p@127.0.0.1:1/db?sslmode=disable")
	if err != nil {
		t.Fatalf("build pool: %v", err)
	}
	t.Cleanup(pool.Close)

	locks, ok := storage.LockConnPoolFrom(pool)
	if !ok {
		t.Fatal("a pgx pool must expose lock connections")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := locks.Acquire(ctx)
	if err == nil {
		t.Fatal("acquiring from an unreachable database must fail")
	}
	if conn != nil {
		t.Fatal("a failed acquisition must not return a connection")
	}
}

// fakeAdmissionPool satisfies storage.Pool without being a pgx pool.
type fakeAdmissionPool struct{ storage.Pool }
