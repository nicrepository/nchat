//go:build integration

package storage_test

import (
	"context"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/auth-service/internal/storage"
)

// "Distributed" is a claim about two processes sharing one counter, and only a
// real server can settle it: an in-memory double would share a Go map, which is
// precisely the property under test being assumed rather than demonstrated.
// Shared harness helpers live in invite_migration_postgres_test.go.

const (
	bootstrapLimiterKeyA = "bootstrap-admin-token:203.0.113.10"
	bootstrapLimiterKeyB = "bootstrap-admin-token:198.51.100.7"
)

func bootstrapAttemptDatabase(t *testing.T, ctx context.Context) *storage.PGXBootstrapAttemptStore {
	t.Helper()
	conn := connectTestDatabase(t, ctx)
	applyMigrationsBefore008(t, ctx, conn)
	applyInviteScopeUp(t, ctx, conn)
	applyMigrationFile(t, ctx, conn,
		repositoryRoot(t)+"/migrations/auth/000009_bootstrap_auth_attempts.up.sql")
	return storage.NewPGXBootstrapAttemptStore(testPool(t, ctx))
}

// Two stores over two independent pools are two replicas over one database.
// Alternating between them must spend a single budget — the failure this
// replaces is N replicas granting N times the guesses.
func TestBootstrapAttempts_BudgetIsSharedAcrossReplicas(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	replicaOne := bootstrapAttemptDatabase(t, ctx)
	replicaTwo := storage.NewPGXBootstrapAttemptStore(testPool(t, ctx))

	const limit = 5
	window := time.Hour

	for i := 0; i < limit; i++ {
		replica := replicaOne
		if i%2 == 1 {
			replica = replicaTwo
		}
		allowed, err := replica.RecordAttempt(ctx, bootstrapLimiterKeyA, limit, window)
		if err != nil {
			t.Fatalf("attempt %d: %v", i+1, err)
		}
		if !allowed {
			t.Fatalf("attempt %d of %d must stay within the shared budget", i+1, limit)
		}
	}

	// The attempt past the budget is refused whichever replica serves it.
	allowed, err := replicaTwo.RecordAttempt(ctx, bootstrapLimiterKeyA, limit, window)
	if err != nil {
		t.Fatalf("RecordAttempt: %v", err)
	}
	if allowed {
		t.Fatal("the attempt past the shared budget must be refused")
	}

	// A different address is unaffected: exhausting one must not lock out
	// everyone behind another.
	allowed, err = replicaOne.RecordAttempt(ctx, bootstrapLimiterKeyB, limit, window)
	if err != nil {
		t.Fatalf("RecordAttempt: %v", err)
	}
	if !allowed {
		t.Fatal("a different key must have its own budget")
	}
}

// The window is what makes the budget recover. Advancing it deterministically
// — by writing the previous window's row directly rather than waiting — keeps
// the test free of sleeps.
func TestBootstrapAttempts_BudgetRecoversInTheNextWindow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	store := bootstrapAttemptDatabase(t, ctx)
	conn := connectTestDatabase(t, ctx)

	const limit = 2
	window := time.Hour

	for i := 0; i < limit; i++ {
		if _, err := store.RecordAttempt(ctx, bootstrapLimiterKeyA, limit, window); err != nil {
			t.Fatalf("attempt %d: %v", i+1, err)
		}
	}
	allowed, err := store.RecordAttempt(ctx, bootstrapLimiterKeyA, limit, window)
	if err != nil {
		t.Fatalf("RecordAttempt: %v", err)
	}
	if allowed {
		t.Fatal("the budget must be exhausted before the window is advanced")
	}

	// Move the exhausted counter into the previous window: the next attempt
	// therefore lands in a window with no history, exactly as it would after
	// the clock advanced.
	if _, err := conn.Exec(ctx, `
		UPDATE auth.bootstrap_auth_attempts
		SET window_start = window_start - $2::interval
		WHERE limiter_key = $1`,
		bootstrapLimiterKeyA, window.String(),
	); err != nil {
		t.Fatalf("advance window: %v", err)
	}

	allowed, err = store.RecordAttempt(ctx, bootstrapLimiterKeyA, limit, window)
	if err != nil {
		t.Fatalf("RecordAttempt: %v", err)
	}
	if !allowed {
		t.Fatal("the budget must recover in the next window")
	}
}

// Concurrent attempts must not both observe the last remaining slot: the count
// and the increment are one statement precisely so they cannot.
func TestBootstrapAttempts_ConcurrentAttemptsShareOneCounter(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	replicaOne := bootstrapAttemptDatabase(t, ctx)
	replicaTwo := storage.NewPGXBootstrapAttemptStore(testPool(t, ctx))

	// Budget of one: exactly one of two racing attempts may be allowed.
	const limit = 1
	window := time.Hour

	var allowedOne, allowedTwo bool
	errs := raceTwo(
		func() (err error) {
			allowedOne, err = replicaOne.RecordAttempt(ctx, bootstrapLimiterKeyA, limit, window)
			return err
		},
		func() (err error) {
			allowedTwo, err = replicaTwo.RecordAttempt(ctx, bootstrapLimiterKeyA, limit, window)
			return err
		},
	)
	for _, err := range errs {
		if err != nil {
			t.Fatalf("racing attempt failed: %v", err)
		}
	}

	if allowedOne == allowedTwo {
		t.Fatalf("exactly one racing attempt may be allowed, got %v and %v", allowedOne, allowedTwo)
	}

	var attempts int
	conn := connectTestDatabase(t, ctx)
	if err := conn.QueryRow(ctx,
		`SELECT attempts FROM auth.bootstrap_auth_attempts WHERE limiter_key = $1`,
		bootstrapLimiterKeyA,
	).Scan(&attempts); err != nil {
		t.Fatalf("read counter: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("both attempts must be charged against one counter, got %d", attempts)
	}
}

// The sweep is housekeeping, not correctness: an expired window is never read.
// It exists so the table does not keep one row per (IP, window) forever.
func TestBootstrapAttempts_SweepDiscardsExpiredWindows(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	store := bootstrapAttemptDatabase(t, ctx)
	conn := connectTestDatabase(t, ctx)
	window := time.Hour

	if _, err := store.RecordAttempt(ctx, bootstrapLimiterKeyA, 5, window); err != nil {
		t.Fatalf("RecordAttempt: %v", err)
	}
	if _, err := conn.Exec(ctx, `
		INSERT INTO auth.bootstrap_auth_attempts (limiter_key, window_start, attempts)
		VALUES ($1, now() - interval '30 days', 99)`, bootstrapLimiterKeyB); err != nil {
		t.Fatalf("seed stale window: %v", err)
	}

	if err := store.SweepExpired(ctx, window); err != nil {
		t.Fatalf("SweepExpired: %v", err)
	}

	assertCount(t, ctx, conn, 0,
		`SELECT count(*) FROM auth.bootstrap_auth_attempts WHERE limiter_key = $1`, bootstrapLimiterKeyB)
	assertCount(t, ctx, conn, 1,
		`SELECT count(*) FROM auth.bootstrap_auth_attempts WHERE limiter_key = $1`, bootstrapLimiterKeyA)
}

// The migration is reversible like every other one in this service.
func TestBootstrapAttemptsMigration_Reverses(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	conn := connectTestDatabase(t, ctx)
	applyMigrationsBefore008(t, ctx, conn)
	applyInviteScopeUp(t, ctx, conn)
	root := repositoryRoot(t)
	applyMigrationFile(t, ctx, conn, root+"/migrations/auth/000009_bootstrap_auth_attempts.up.sql")

	if !objectExists(t, ctx, conn, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = 'auth' AND table_name = 'bootstrap_auth_attempts')`) {
		t.Fatal("expected the counter table after the up")
	}
	if !indexExists(t, ctx, conn, "idx_bootstrap_auth_attempts_window") {
		t.Fatal("expected the sweep index after the up")
	}

	applyMigrationFile(t, ctx, conn, root+"/migrations/auth/000009_bootstrap_auth_attempts.down.sql")

	if objectExists(t, ctx, conn, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = 'auth' AND table_name = 'bootstrap_auth_attempts')`) {
		t.Fatal("the counter table must be gone after the down")
	}
}
