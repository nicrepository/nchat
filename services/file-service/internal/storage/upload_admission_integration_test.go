package storage_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nicrepository/nchat/services/file-service/internal/domain"
	"github.com/nicrepository/nchat/services/file-service/internal/storage"
)

// Opt-in integration coverage for cluster-wide upload admission.
//
// The unit tests prove the algorithm against a shared in-memory lock namespace.
// This one proves the assumption underneath it: that PostgreSQL session
// advisory locks really are shared between independent connections and really
// are released when a session ends. Nothing else in the design would survive
// either of those being false.
//
// Run with:
//
//	make dev-env-up
//	FILE_TEST_DATABASE_URL='postgresql://nchat:<password>@localhost:5432/nchat_test?sslmode=disable' \
//	  go test ./services/file-service/internal/storage/ -run AdmissionIntegration -v
//
// The suite skips without that variable, so the default run never depends on an
// external service. No table is created and no migration is required: advisory
// locks live entirely in the lock manager.
const admissionTestDatabaseURLEnv = "FILE_TEST_DATABASE_URL"

func admissionIntegrationPool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	dsn := os.Getenv(admissionTestDatabaseURLEnv)
	if dsn == "" {
		t.Skipf("set %s to run upload admission integration tests", admissionTestDatabaseURLEnv)
	}
	// Same guard the other integration suites use: never point these at a
	// database that is not obviously a test one.
	if !strings.Contains(dsn, "_test") {
		t.Fatalf("%s must point at a *_test database", admissionTestDatabaseURLEnv)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Fatalf("ping: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// Two admissions over two independent pools are two replicas over one database.
func TestUploadAdmissionIntegrationSharesSlotsAcrossPools(t *testing.T) {
	poolOne := admissionIntegrationPool(t)
	poolTwo := admissionIntegrationPool(t)

	lockPoolOne, ok := storage.LockConnPoolFrom(poolOne)
	if !ok {
		t.Fatal("a pgx pool must expose lock connections")
	}
	lockPoolTwo, ok := storage.LockConnPoolFrom(poolTwo)
	if !ok {
		t.Fatal("a pgx pool must expose lock connections")
	}

	limits := storage.UploadAdmissionLimits{Global: 1, PerUser: 1}
	replicaOne := storage.NewPGXUploadAdmission(lockPoolOne, limits, discardLogger())
	replicaTwo := storage.NewPGXUploadAdmission(lockPoolTwo, limits, discardLogger())

	ctx := context.Background()
	release, err := replicaOne.Acquire(ctx, userA, oneMiB)
	if err != nil {
		t.Fatalf("first replica must be admitted: %v", err)
	}

	// The lock lives in PostgreSQL, so the other replica sees it.
	if _, err := replicaTwo.Acquire(ctx, userB, oneMiB); !errors.Is(err, domain.ErrClusterAtCapacity) {
		t.Fatalf("the second replica must see the taken slot, got %v", err)
	}

	release()

	after, err := replicaTwo.Acquire(ctx, userB, oneMiB)
	if err != nil {
		t.Fatalf("the released slot must be usable from the other replica: %v", err)
	}
	after()
}

// Closing the pool ends its sessions; PostgreSQL must drop their locks with
// them. This is what makes a crashed replica give its slots back with no lease,
// no sweeper and no cleanup job.
func TestUploadAdmissionIntegrationFreesSlotsWhenASessionEnds(t *testing.T) {
	survivor := admissionIntegrationPool(t)

	dying, err := pgxpool.New(context.Background(), os.Getenv(admissionTestDatabaseURLEnv))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	dyingLocks, ok := storage.LockConnPoolFrom(dying)
	if !ok {
		t.Fatal("a pgx pool must expose lock connections")
	}
	survivorLocks, ok := storage.LockConnPoolFrom(survivor)
	if !ok {
		t.Fatal("a pgx pool must expose lock connections")
	}

	limits := storage.UploadAdmissionLimits{Global: 1, PerUser: 1}
	ctx := context.Background()

	// Acquire and deliberately never release: this models a process that dies
	// mid-upload.
	if _, err := storage.NewPGXUploadAdmission(dyingLocks, limits, discardLogger()).Acquire(ctx, userA, oneMiB); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := storage.NewPGXUploadAdmission(survivorLocks, limits, discardLogger()).Acquire(ctx, userB, oneMiB); !errors.Is(err, domain.ErrClusterAtCapacity) {
		t.Fatalf("the slot must be held, got %v", err)
	}

	dying.Close()

	// PostgreSQL releases the locks with the sessions, so the slot comes back
	// without anything having to notice the crash.
	release, err := storage.NewPGXUploadAdmission(survivorLocks, limits, discardLogger()).Acquire(ctx, userB, oneMiB)
	if err != nil {
		t.Fatalf("a slot orphaned by a dead session must become available: %v", err)
	}
	release()
}

// Discard must end the session, which is what makes PostgreSQL drop the
// advisory locks held on it. This is the assumption the whole "unlock failed →
// throw the connection away" rule rests on: if ending the session did not free
// the lock, discarding would strand a slot exactly as returning it would.
//
// It also asserts the property Hijack gives us: the connection is out of the
// pool, so nothing can hand it back afterwards.
func TestUploadAdmissionIntegrationDiscardFreesLocksAndRemovesTheConnection(t *testing.T) {
	pool := admissionIntegrationPool(t)
	locks, ok := storage.LockConnPoolFrom(pool)
	if !ok {
		t.Fatal("a pgx pool must expose lock connections")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	const key int64 = 0x6e63686174 // "nchat", a key no production path uses.

	held, err := locks.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	taken, err := held.TryLock(ctx, key)
	if err != nil || !taken {
		t.Fatalf("TryLock() = %v, %v; want true, nil", taken, err)
	}

	// A second connection must not be able to take it while the first holds it.
	other, err := locks.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if taken, err := other.TryLock(ctx, key); err != nil || taken {
		t.Fatalf("the lock must be held elsewhere: TryLock() = %v, %v", taken, err)
	}

	// Ending the session is the only thing that happens here — no unlock.
	if err := held.Discard(ctx); err != nil {
		t.Fatalf("discard: %v", err)
	}

	// PostgreSQL released the lock with the session.
	regained, err := other.TryLock(ctx, key)
	if err != nil {
		t.Fatalf("TryLock after discard: %v", err)
	}
	if !regained {
		t.Fatal("ending the session must free the advisory locks it held")
	}
	if _, err := other.Unlock(ctx, key); err != nil {
		t.Fatalf("unlock: %v", err)
	}
	other.Release()

	// A discarded connection is out of the pool: a second Discard is a no-op and
	// a Release must not put it back (pgxpool panics on a double release, so this
	// also asserts the nil guard).
	if err := held.Discard(ctx); err != nil {
		t.Fatalf("a repeated discard must be a no-op, got %v", err)
	}
	held.Release()
}
