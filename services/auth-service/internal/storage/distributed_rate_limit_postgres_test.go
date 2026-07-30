//go:build integration

package storage_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/auth-service/internal/storage"
)

// "Shared across replicas" is a claim about two processes and one counter, and
// only a real server settles it: an in-memory double would share a Go map,
// which is the property under test being assumed rather than shown.
// Shared harness helpers live in invite_migration_postgres_test.go.

const (
	inviteIPNamespace = "admin-invites-ip"
	inviteIPSubjectA  = "203.0.113.10"
	inviteIPSubjectB  = "198.51.100.7"
	inviteIPBudget    = 30
)

func rateLimitDatabase(t *testing.T, ctx context.Context) *storage.PGXBootstrapAttemptStore {
	t.Helper()
	conn := connectTestDatabase(t, ctx)
	applyMigrationsBefore008(t, ctx, conn)
	applyInviteScopeUp(t, ctx, conn)
	applyMigrationFile(t, ctx, conn,
		repositoryRoot(t)+"/migrations/auth/000009_bootstrap_auth_attempts.up.sql")
	return storage.NewPGXBootstrapAttemptStore(testPool(t, ctx))
}

func inviteIPRequest(subject string, now time.Time) storage.DistributedRateLimitRequest {
	return storage.DistributedRateLimitRequest{
		Namespace: inviteIPNamespace,
		Subject:   subject,
		Limit:     inviteIPBudget,
		Window:    time.Hour,
		Now:       now,
	}
}

// Two stores over independent pools are two replicas over one database.
// Alternating between them must spend a single budget — the in-process bucket
// this replaces gave each replica its own.
func TestInviteIPRateLimit_SharedAcrossReplicasAndRestarts(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	replicaOne := rateLimitDatabase(t, ctx)
	replicaTwo := storage.NewPGXBootstrapAttemptStore(testPool(t, ctx))
	now := time.Now().UTC()

	for i := 0; i < inviteIPBudget; i++ {
		replica := replicaOne
		if i%2 == 1 {
			replica = replicaTwo
		}
		result, err := replica.Allow(ctx, inviteIPRequest(inviteIPSubjectA, now))
		if err != nil {
			t.Fatalf("request %d: %v", i+1, err)
		}
		if !result.Allowed {
			t.Fatalf("request %d of %d must stay within the shared budget", i+1, inviteIPBudget)
		}
	}

	// The request past the budget is refused whichever replica serves it, and
	// reports how long the window still has to run.
	result, err := replicaTwo.Allow(ctx, inviteIPRequest(inviteIPSubjectA, now))
	if err != nil {
		t.Fatalf("Allow: %v", err)
	}
	if result.Allowed {
		t.Fatal("the request past the shared budget must be refused")
	}
	if result.RetryAfter <= 0 || result.RetryAfter > time.Hour {
		t.Fatalf("Retry-After must be the remainder of the window, got %v", result.RetryAfter)
	}

	// A third store stands in for a restarted pod: the count lives in the
	// database, so it does not start over.
	restarted := storage.NewPGXBootstrapAttemptStore(testPool(t, ctx))
	result, err = restarted.Allow(ctx, inviteIPRequest(inviteIPSubjectA, now))
	if err != nil {
		t.Fatalf("Allow: %v", err)
	}
	if result.Allowed {
		t.Fatal("a restart must not renew the budget")
	}

	// A different address is unaffected.
	result, err = replicaOne.Allow(ctx, inviteIPRequest(inviteIPSubjectB, now))
	if err != nil {
		t.Fatalf("Allow: %v", err)
	}
	if !result.Allowed {
		t.Fatal("a different address must have its own budget")
	}

	// The next window renews it, exercised by moving the supplied instant
	// rather than waiting an hour.
	result, err = replicaOne.Allow(ctx, inviteIPRequest(inviteIPSubjectA, now.Add(time.Hour)))
	if err != nil {
		t.Fatalf("Allow: %v", err)
	}
	if !result.Allowed {
		t.Fatal("the budget must renew in the next window")
	}
}

// The invite ceiling and the bootstrap budget share a table. Spending one must
// not consume the other, which is what the namespace is for.
func TestInviteIPRateLimit_NamespaceIsolatesCounters(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	store := rateLimitDatabase(t, ctx)
	now := time.Now().UTC()

	for i := 0; i < inviteIPBudget+1; i++ {
		if _, err := store.Allow(ctx, inviteIPRequest(inviteIPSubjectA, now)); err != nil {
			t.Fatalf("Allow: %v", err)
		}
	}

	// The bootstrap limiter, same address, its own namespace and budget.
	allowed, err := store.RecordAttempt(ctx, "bootstrap-admin-token:"+inviteIPSubjectA, 5, 15*time.Minute)
	if err != nil {
		t.Fatalf("RecordAttempt: %v", err)
	}
	if !allowed {
		t.Fatal("exhausting the invite ceiling must not consume the bootstrap budget")
	}
}

// Concurrency across two replicas must admit exactly the budget and no more:
// the count and the increment are one statement precisely so two replicas
// cannot both observe the last remaining slot.
func TestInviteIPRateLimit_ConcurrentRequestsNeverExceedBudget(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	replicaOne := rateLimitDatabase(t, ctx)
	replicaTwo := storage.NewPGXBootstrapAttemptStore(testPool(t, ctx))
	now := time.Now().UTC()

	const attempts = inviteIPBudget * 2
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		allowed int
		failed  error
	)
	start := make(chan struct{})
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			replica := replicaOne
			if i%2 == 1 {
				replica = replicaTwo
			}
			<-start
			result, err := replica.Allow(ctx, inviteIPRequest(inviteIPSubjectA, now))
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failed = err
				return
			}
			if result.Allowed {
				allowed++
			}
		}(i)
	}
	close(start)
	wg.Wait()

	if failed != nil {
		t.Fatalf("a concurrent request failed: %v", failed)
	}
	if allowed != inviteIPBudget {
		t.Fatalf("exactly %d of %d concurrent requests may be allowed, got %d", inviteIPBudget, attempts, allowed)
	}

	conn := connectTestDatabase(t, ctx)
	assertCount(t, ctx, conn, attempts,
		`SELECT attempts FROM auth.bootstrap_auth_attempts WHERE limiter_key = $1`,
		inviteIPNamespace+":"+inviteIPSubjectA)
	assertNoOpenTransactions(t, ctx, conn)
}
