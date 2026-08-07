package storage_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/nicrepository/nchat/services/file-service/internal/domain"
	"github.com/nicrepository/nchat/services/file-service/internal/storage"
)

const testCleanupKey = "nchat/previews/77777777-7777-4777-8777-777777777777"

// Enqueue has to be idempotent at the database level, not by the caller
// remembering: the same failed delete is recorded on every retry and every
// restart, and one job must come out.
func TestEnqueueObjectCleanupIsIdempotentByConstraint(t *testing.T) {
	pool := &fakePool{}
	if err := storage.NewPGXObjectCleanupStore(pool).Enqueue(
		context.Background(), testCleanupKey); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(pool.lastSQL, "ON CONFLICT (object_key) DO NOTHING") {
		t.Fatalf("enqueue must be idempotent in SQL:\n%s", pool.lastSQL)
	}
	// A repeat must not push the schedule around, or a fast-failing caller
	// could starve the backoff of a job already waiting.
	if strings.Contains(pool.lastSQL, "DO UPDATE") {
		t.Fatalf("a repeated enqueue must not reschedule:\n%s", pool.lastSQL)
	}
	if pool.lastArgs[0] != testCleanupKey {
		t.Fatalf("unexpected arguments: %v", pool.lastArgs)
	}
}

func TestEnqueueObjectCleanupRefusesAnEmptyKey(t *testing.T) {
	pool := &fakePool{}
	err := storage.NewPGXObjectCleanupStore(pool).Enqueue(context.Background(), "")
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("error = %v, want ErrInvalidInput", err)
	}
	if pool.lastSQL != "" {
		t.Fatal("an empty key must not reach the database")
	}
}

func TestEnqueueObjectCleanupWrapsDatabaseFailures(t *testing.T) {
	dbErr := errors.New("connection refused")
	pool := &fakePool{exec: func(string, ...any) (pgconn.CommandTag, error) {
		return pgconn.CommandTag{}, dbErr
	}}
	if err := storage.NewPGXObjectCleanupStore(pool).Enqueue(
		context.Background(), testCleanupKey); !errors.Is(err, dbErr) {
		t.Fatalf("expected the database error to be wrapped, got %v", err)
	}
}

// The claim is the same queue primitive the preview job uses, so replicas step
// over each other's rows and a lease covers the delete it protects.
func TestClaimDueCleanupsLeasesRows(t *testing.T) {
	pool := &fakePool{query: func(string, ...any) (pgx.Rows, error) {
		return &valueRows{rows: [][]any{{
			"job-1", testCleanupKey, pgtype.Int2{Int16: 1, Valid: true},
		}}}, nil
	}}

	jobs, err := storage.NewPGXObjectCleanupStore(pool).ClaimDueCleanups(
		context.Background(), 5, time.Minute)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(jobs) != 1 || jobs[0].ObjectKey != testCleanupKey || jobs[0].Attempts != 1 {
		t.Fatalf("unexpected jobs: %+v", jobs)
	}
	for _, fragment := range []string{
		"FOR UPDATE SKIP LOCKED",
		"next_attempt_at <= now()",
		"attempts = LEAST(j.attempts + 1, $3)",
		"next_attempt_at = now() + ($2 * interval '1 second')",
	} {
		if !strings.Contains(pool.lastSQL, fragment) {
			t.Fatalf("claim query is missing %q:\n%s", fragment, pool.lastSQL)
		}
	}
}

func TestClaimDueCleanupsRefusesParametersThatWouldBreakTheLease(t *testing.T) {
	store := storage.NewPGXObjectCleanupStore(&fakePool{})
	for name, tt := range map[string]struct {
		batch int
		lease time.Duration
	}{
		"no batch": {batch: 0, lease: time.Minute},
		"no lease": {batch: 1, lease: 0},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := store.ClaimDueCleanups(
				context.Background(), tt.batch, tt.lease,
			); !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("error = %v, want ErrInvalidInput", err)
			}
		})
	}
}

func TestClaimDueCleanupsWrapsDatabaseFailures(t *testing.T) {
	dbErr := errors.New("connection reset")
	pool := &fakePool{query: func(string, ...any) (pgx.Rows, error) { return nil, dbErr }}
	if _, err := storage.NewPGXObjectCleanupStore(pool).ClaimDueCleanups(
		context.Background(), 1, time.Minute); !errors.Is(err, dbErr) {
		t.Fatalf("expected the database error to be wrapped, got %v", err)
	}
}

// Completing is fenced by the attempt count, so a worker whose lease expired
// cannot forget a job the current attempt is still working on.
func TestCompleteObjectCleanupIsFencedByTheClaim(t *testing.T) {
	pool := &fakePool{exec: func(string, ...any) (pgconn.CommandTag, error) {
		return pgconn.NewCommandTag("DELETE 1"), nil
	}}
	completed, err := storage.NewPGXObjectCleanupStore(pool).Complete(
		context.Background(), "job-1", 2)
	if err != nil || !completed {
		t.Fatalf("completed = %v, err = %v", completed, err)
	}
	if !strings.Contains(pool.lastSQL, "AND attempts = $2") {
		t.Fatalf("completing must require the claim:\n%s", pool.lastSQL)
	}
	// A finished job leaves the table: the queue is the backlog.
	if !strings.Contains(pool.lastSQL, "DELETE FROM files.object_cleanup_jobs") {
		t.Fatalf("a completed job must be removed:\n%s", pool.lastSQL)
	}
}

func TestCompleteObjectCleanupReportsALostClaim(t *testing.T) {
	pool := &fakePool{exec: func(string, ...any) (pgconn.CommandTag, error) {
		return pgconn.NewCommandTag("DELETE 0"), nil
	}}
	completed, err := storage.NewPGXObjectCleanupStore(pool).Complete(
		context.Background(), "job-1", 2)
	if err != nil {
		t.Fatalf("a lost claim is not an error: %v", err)
	}
	if completed {
		t.Fatal("a stale claim must not report completion")
	}
}

func TestCompleteObjectCleanupRefusesAJobWithoutItsClaim(t *testing.T) {
	pool := &fakePool{}
	for name, claim := range map[string]int{"zero": 0, "negative": -1} {
		t.Run(name, func(t *testing.T) {
			if _, err := storage.NewPGXObjectCleanupStore(pool).Complete(
				context.Background(), "job-1", claim,
			); !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("error = %v, want ErrInvalidInput", err)
			}
		})
	}
}

// The guard that stops this worker from becoming the thing that breaks a
// working preview: an object a published row points at is referenced.
func TestIsObjectReferencedAsksAboutPublishedPreviews(t *testing.T) {
	pool := &fakePool{queryRow: func(string, ...any) pgx.Row {
		return valueRow{values: []any{true}}
	}}
	referenced, err := storage.NewPGXObjectCleanupStore(pool).IsObjectReferenced(
		context.Background(), testCleanupKey)
	if err != nil || !referenced {
		t.Fatalf("referenced = %v, err = %v", referenced, err)
	}
	if !strings.Contains(pool.lastSQL, "preview_status = 'ready'") {
		t.Fatalf("only a published preview references an object:\n%s", pool.lastSQL)
	}
}

func TestObjectCleanupStoreRefusesToRunWithoutAPool(t *testing.T) {
	store := storage.NewPGXObjectCleanupStore(nil)
	if err := store.Enqueue(context.Background(), testCleanupKey); !errors.Is(
		err, domain.ErrUnavailable,
	) {
		t.Fatalf("enqueue error = %v, want ErrUnavailable", err)
	}
	if _, err := store.ClaimDueCleanups(context.Background(), 1, time.Minute); !errors.Is(
		err, domain.ErrUnavailable,
	) {
		t.Fatalf("claim error = %v, want ErrUnavailable", err)
	}
	if _, err := store.Complete(context.Background(), "job-1", 1); !errors.Is(
		err, domain.ErrUnavailable,
	) {
		t.Fatalf("complete error = %v, want ErrUnavailable", err)
	}
	if _, err := store.IsObjectReferenced(context.Background(), testCleanupKey); !errors.Is(
		err, domain.ErrUnavailable,
	) {
		t.Fatalf("reference error = %v, want ErrUnavailable", err)
	}
}
