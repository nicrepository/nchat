package storage_test

import (
	"context"
	"errors"
	"testing"
	"time"

	pgxmock "github.com/pashagolub/pgxmock/v2"

	"github.com/nicrepository/nchat/services/auth-service/internal/storage"
)

// The distributed behaviour is proven against a real server in
// bootstrap_attempt_postgres_test.go. These cover the shape of the SQL and the
// branches around it, which is what the tag-gated tests cannot assert cheaply.

const bootstrapAttemptKey = "bootstrap-admin-token:203.0.113.10"

func TestPGXBootstrapAttemptStore_AllowsWhileInsideBudget(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	// One statement, not a read followed by a write: two replicas must not both
	// observe the last remaining attempt.
	mock.ExpectQuery(`INSERT INTO auth\.bootstrap_auth_attempts`).
		WithArgs(bootstrapAttemptKey, pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"attempts"}).AddRow(3))

	store := storage.NewPGXBootstrapAttemptStore(mock)
	allowed, err := store.RecordAttempt(context.Background(), bootstrapAttemptKey, 5, 15*time.Minute)
	if err != nil {
		t.Fatalf("RecordAttempt: %v", err)
	}
	if !allowed {
		t.Fatal("an attempt inside the budget must be allowed")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// The limit is inclusive: the fifth of five is allowed, the sixth is not.
func TestPGXBootstrapAttemptStore_BudgetBoundaryIsInclusive(t *testing.T) {
	for _, tt := range []struct {
		name    string
		count   int
		allowed bool
	}{
		{name: "at the limit", count: 5, allowed: true},
		{name: "past the limit", count: 6, allowed: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			mock, err := pgxmock.NewPool()
			if err != nil {
				t.Fatalf("pgxmock: %v", err)
			}
			defer mock.Close()

			mock.ExpectQuery(`INSERT INTO auth\.bootstrap_auth_attempts`).
				WithArgs(bootstrapAttemptKey, pgxmock.AnyArg()).
				WillReturnRows(pgxmock.NewRows([]string{"attempts"}).AddRow(tt.count))

			store := storage.NewPGXBootstrapAttemptStore(mock)
			allowed, err := store.RecordAttempt(context.Background(), bootstrapAttemptKey, 5, 15*time.Minute)
			if err != nil {
				t.Fatalf("RecordAttempt: %v", err)
			}
			if allowed != tt.allowed {
				t.Fatalf("expected allowed=%v at count %d, got %v", tt.allowed, tt.count, allowed)
			}
		})
	}
}

// An unreachable counter must surface as an error so the middleware can fail
// closed; reporting "allowed" here would be an unbounded credential check.
func TestPGXBootstrapAttemptStore_QueryErrorIsReturnedAndNotAllowed(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`INSERT INTO auth\.bootstrap_auth_attempts`).
		WithArgs(bootstrapAttemptKey, pgxmock.AnyArg()).
		WillReturnError(errors.New("connection refused"))

	store := storage.NewPGXBootstrapAttemptStore(mock)
	allowed, err := store.RecordAttempt(context.Background(), bootstrapAttemptKey, 5, 15*time.Minute)
	if err == nil {
		t.Fatal("expected the failure to propagate")
	}
	if allowed {
		t.Fatal("an unreachable counter must never report the attempt as allowed")
	}
}

// Zero is not "unlimited": a misconfigured budget is an error, and the caller
// refuses on it.
func TestPGXBootstrapAttemptStore_RejectsMisconfiguredBudget(t *testing.T) {
	for _, tt := range []struct {
		name   string
		limit  int
		window time.Duration
	}{
		{name: "zero limit", limit: 0, window: time.Minute},
		{name: "negative limit", limit: -1, window: time.Minute},
		{name: "zero window", limit: 5, window: 0},
		{name: "negative window", limit: 5, window: -time.Minute},
	} {
		t.Run(tt.name, func(t *testing.T) {
			mock, err := pgxmock.NewPool()
			if err != nil {
				t.Fatalf("pgxmock: %v", err)
			}
			defer mock.Close()
			// No query is scripted: a misconfigured budget must not reach the
			// database at all.

			store := storage.NewPGXBootstrapAttemptStore(mock)
			allowed, err := store.RecordAttempt(context.Background(), bootstrapAttemptKey, tt.limit, tt.window)
			if err == nil {
				t.Fatal("expected a misconfigured budget to be an error")
			}
			if allowed {
				t.Fatal("a misconfigured budget must not allow the attempt")
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet expectations: %v", err)
			}
		})
	}
}

func TestPGXBootstrapAttemptStore_SweepExpired(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectExec(`DELETE FROM auth\.bootstrap_auth_attempts`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("DELETE", 3))

	store := storage.NewPGXBootstrapAttemptStore(mock)
	if err := store.SweepExpired(context.Background(), 15*time.Minute); err != nil {
		t.Fatalf("SweepExpired: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXBootstrapAttemptStore_SweepExpiredErrors(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectExec(`DELETE FROM auth\.bootstrap_auth_attempts`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnError(errors.New("connection refused"))

	store := storage.NewPGXBootstrapAttemptStore(mock)
	if err := store.SweepExpired(context.Background(), 15*time.Minute); err == nil {
		t.Fatal("expected the sweep failure to propagate")
	}
}

// Housekeeping with no window configured is a no-op, not a delete of
// everything.
func TestPGXBootstrapAttemptStore_SweepWithoutWindowDoesNothing(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	store := storage.NewPGXBootstrapAttemptStore(mock)
	if err := store.SweepExpired(context.Background(), 0); err != nil {
		t.Fatalf("SweepExpired: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}
