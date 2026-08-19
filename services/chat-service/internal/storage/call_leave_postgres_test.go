package storage_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// Fixture identifiers for the issue #569 real-PostgreSQL leave/busy-check
// proof below.
const (
	leavePGWorkspace = "c5000000-0000-4000-8000-000000000001"
	leavePGChannel   = "c5000000-0000-4000-8000-000000000020"
	leavePGUserA     = "c5000000-0000-4000-8000-00000000000a" // starts the resource call (its permanent caller_id)
	leavePGUserB     = "c5000000-0000-4000-8000-00000000000b" // joins after A, stays
	leavePGUserD     = "c5000000-0000-4000-8000-00000000000d" // A's 1:1 counterpart
	leavePGUserE     = "c5000000-0000-4000-8000-00000000000e" // B's 1:1 counterpart
	leavePGUserC     = "c5000000-0000-4000-8000-00000000000c" // joins while A is leaving
)

// newCallLeavePool prepares a schema-reset test database and seeds one
// workspace, one public channel and four active members — the minimum
// authorizeResourceTarget (chat.channel_visible_to_user) and CreateCall's
// workspace-membership check both require.
//
// Skips unless CHAT_TEST_DATABASE_URL points at a *_test database — the same
// gate and safety check every other *_postgres_test.go in this package uses.
func newCallLeavePool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("CHAT_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("CHAT_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	var databaseName string
	if err := pool.QueryRow(ctx, `SELECT current_database()`).Scan(&databaseName); err != nil {
		t.Fatalf("read current database: %v", err)
	}
	if !strings.HasSuffix(databaseName, "_test") {
		t.Fatalf("refusing destructive test against non-test database %q", databaseName)
	}
	if _, err := pool.Exec(ctx, `DROP SCHEMA IF EXISTS chat CASCADE`); err != nil {
		t.Fatalf("reset chat schema: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DROP SCHEMA IF EXISTS chat CASCADE`) })
	if _, err := pool.Exec(ctx, `
		CREATE SCHEMA IF NOT EXISTS auth;
		CREATE TABLE IF NOT EXISTS auth.users (
			id UUID PRIMARY KEY,
			email TEXT NOT NULL DEFAULT '',
			display_name TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'active',
			deleted_at TIMESTAMPTZ
		)`); err != nil {
		t.Fatalf("prepare auth schema: %v", err)
	}
	if _, err := pool.Exec(ctx, readAllChatUpMigrations(t)); err != nil {
		t.Fatalf("apply chat migrations: %v", err)
	}

	// One transaction: a deferred constraint requires every workspace to hold
	// exactly one active public general channel by commit time (see
	// channel_store_postgres_test.go's newChannelAuthzPool), so the workspace
	// and its #geral channel cannot be inserted as separate statements.
	seedTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin seed: %v", err)
	}
	defer func() { _ = seedTx.Rollback(context.Background()) }()
	if _, err := seedTx.Exec(ctx,
		`INSERT INTO chat.workspaces (id, slug, name, status) VALUES ($1, 'call-leave-pg', 'Call Leave PG', 'active')`,
		leavePGWorkspace,
	); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	if _, err := seedTx.Exec(ctx,
		`INSERT INTO chat.channels (id, workspace_id, slug, display_name, type, is_general, status)
		 VALUES ($1, $2, 'geral', 'geral', 'public', true, 'active')`,
		leavePGChannel, leavePGWorkspace,
	); err != nil {
		t.Fatalf("seed channel: %v", err)
	}
	if err := seedTx.Commit(ctx); err != nil {
		t.Fatalf("commit seed: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO chat.workspace_members (workspace_id, user_id, role, status) VALUES
			($1, $2, 'member', 'active'),
			($1, $3, 'member', 'active'),
			($1, $4, 'member', 'active'),
			($1, $5, 'member', 'active'),
			($1, $6, 'member', 'active')`,
		leavePGWorkspace, leavePGUserA, leavePGUserB, leavePGUserD, leavePGUserE, leavePGUserC,
	); err != nil {
		t.Fatalf("seed workspace members: %v", err)
	}
	return pool
}

// waitForPostgresLockWaiter blocks until some backend on this database is
// genuinely waiting on a lock (pg_stat_activity.wait_event_type = 'Lock'),
// or fails the test after a bounded deadline. This is condition-based, not a
// guessed sleep: the pass condition is Postgres's own report that a backend
// is blocked, not an assumption about how long that takes.
func waitForPostgresLockWaiter(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var waiting int
		if err := pool.QueryRow(context.Background(),
			`SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND wait_event_type = 'Lock'`,
		).Scan(&waiting); err != nil {
			t.Fatalf("poll pg_stat_activity: %v", err)
		}
		if waiting > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for a backend to block on a lock")
}

// TestPGXCallStoreLeaveResourceCallReleasesOnlyLeavingParticipantsBusyStatusPostgreSQL
// is the real-PostgreSQL proof behind issue #569's busy-check fix — the one
// thing the pgxmock-based tests in call_store_test.go cannot exercise, since
// they only assert the query's shape and args, never its actual semantics
// against a real engine.
//
// The scenario is exactly the reported bug: A starts a resource call (so
// chat.calls.caller_id = A forever, by construction — that column is never
// rewritten), B joins after. A leaves. The resource call must stay active for
// B, A's lease must be gone, B's must not be, A must be free to place a 1:1
// immediately (proving the historical caller_id no longer makes it "busy"),
// and B — who was never caller_id at all — must still be busy purely because
// its lease is live (proving the added lease branch, not just the removed
// caller_id-only one).
//
// No sleep and no ExpireDueCalls call anywhere: every assertion reads either
// LeaveResourceCall/CreateCall's own return value or the tables directly,
// immediately after the write that should have changed them.
func TestPGXCallStoreLeaveResourceCallReleasesOnlyLeavingParticipantsBusyStatusPostgreSQL(t *testing.T) {
	pool := newCallLeavePool(t)
	store := storage.NewPGXCallStore(pool)
	ctx := t.Context()
	now := time.Now().UTC()
	leaseTTL := now.Add(30 * time.Second)

	// A starts the resource call.
	call, created, err := store.CreateResourceCall(ctx, storage.CreateResourceCallInput{
		WorkspaceID: leavePGWorkspace,
		RequestID:   "c5000000-0000-4000-8000-0000000000f1",
		CallerID:    leavePGUserA,
		TargetType:  domain.CallTargetChannel,
		TargetID:    leavePGChannel,
		Type:        domain.CallTypeAudio,
		ExpiresAt:   leaseTTL,
	})
	if err != nil || !created || call.Status != domain.CallStatusActive || call.CallerID != leavePGUserA {
		t.Fatalf("A starts resource call: call=%+v created=%v err=%v", call, created, err)
	}

	// B joins the same, already-active resource call.
	joined, _, err := store.CreateResourceCall(ctx, storage.CreateResourceCallInput{
		WorkspaceID: leavePGWorkspace,
		RequestID:   "c5000000-0000-4000-8000-0000000000f2",
		CallerID:    leavePGUserB,
		TargetType:  domain.CallTargetChannel,
		TargetID:    leavePGChannel,
		Type:        domain.CallTypeAudio,
		ExpiresAt:   leaseTTL,
	})
	if err != nil || joined.ID != call.ID {
		t.Fatalf("B joins resource call: joined=%+v err=%v (want same call %s)", joined, err, call.ID)
	}

	// Both hold a live lease before anyone leaves.
	if !hasActiveLease(t, pool, call.ID, leavePGUserA) || !hasActiveLease(t, pool, call.ID, leavePGUserB) {
		t.Fatal("both A and B must hold an active lease before A leaves")
	}

	// A leaves.
	result, err := store.LeaveResourceCall(ctx, storage.LeaveResourceCallInput{
		WorkspaceID: leavePGWorkspace,
		CallID:      call.ID,
		ActorID:     leavePGUserA,
	})
	if err != nil {
		t.Fatalf("A leaves: %v", err)
	}

	// The resource call stays active — it must never end just because the
	// participant who happens to be its historical caller_id left.
	if result.Changed || result.Call.Status != domain.CallStatusActive {
		t.Fatalf("resource call must stay active for B: %+v", result)
	}
	if status := currentCallStatus(t, pool, call.ID); status != string(domain.CallStatusActive) {
		t.Fatalf("chat.calls.status = %q, want active", status)
	}

	// A's lease is gone; B's is untouched.
	if hasActiveLease(t, pool, call.ID, leavePGUserA) {
		t.Fatal("A's lease must not survive its own leave")
	}
	if !hasActiveLease(t, pool, call.ID, leavePGUserB) {
		t.Fatal("B's lease must survive A's leave")
	}

	// A — the resource call's permanent, historical caller_id — can place a
	// direct 1:1 immediately: no lease-expiry timeout, no sweep, no ErrConflict.
	if _, _, err := store.CreateCall(ctx, storage.CreateCallInput{
		WorkspaceID: leavePGWorkspace,
		RequestID:   "c5000000-0000-4000-8000-0000000000f3",
		CallerID:    leavePGUserA,
		CalleeID:    leavePGUserD,
		Type:        domain.CallTypeAudio,
		ExpiresAt:   leaseTTL,
	}); err != nil {
		t.Fatalf("A must be free to start a 1:1 immediately after leaving: %v", err)
	}

	// B — who was never caller_id of anything — is still busy purely because
	// its lease on the resource call is still live.
	if _, _, err := store.CreateCall(ctx, storage.CreateCallInput{
		WorkspaceID: leavePGWorkspace,
		RequestID:   "c5000000-0000-4000-8000-0000000000f4",
		CallerID:    leavePGUserB,
		CalleeID:    leavePGUserE,
		Type:        domain.CallTypeAudio,
		ExpiresAt:   leaseTTL,
	}); err == nil {
		t.Fatal("B must still be busy while its resource-call lease is active")
	} else if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("B's busy 1:1 attempt error = %v, want ErrConflict", err)
	}
}

// TestPGXCallStoreLastLeaveNeverLeavesJoinerWithStaleActiveSnapshotPostgreSQL
// is the real-PostgreSQL proof for the race identified in review: A is a
// resource call's last participant and is mid-leave (already holding the
// row's FOR UPDATE lock, already having deleted its own lease and marked
// the call ended, but not yet committed) when C concurrently tries to join
// the same target. Before the FOR UPDATE fix in CreateResourceCall, C's
// unlocked "find the active call" read could return the row a moment before
// A's commit, and the later upsertCallPresence insert — only blocked by the
// row's FK-implied lock, not by that read — would attach C's lease to a
// call already ended by the time it went through.
//
// A's own leave is driven by hand, on a pinned connection, so the test can
// hold the lock open exactly long enough to prove — via
// waitForPostgresLockWaiter, not a guessed sleep — that C's concurrent
// CreateResourceCall genuinely blocks on it before A commits.
func TestPGXCallStoreLastLeaveNeverLeavesJoinerWithStaleActiveSnapshotPostgreSQL(t *testing.T) {
	pool := newCallLeavePool(t)
	store := storage.NewPGXCallStore(pool)
	ctx := context.Background()
	leaseTTL := time.Now().UTC().Add(30 * time.Second)

	call, _, err := store.CreateResourceCall(ctx, storage.CreateResourceCallInput{
		WorkspaceID: leavePGWorkspace,
		RequestID:   "c5000000-0000-4000-8000-0000000000a1",
		CallerID:    leavePGUserA,
		TargetType:  domain.CallTargetChannel,
		TargetID:    leavePGChannel,
		Type:        domain.CallTypeAudio,
		ExpiresAt:   leaseTTL,
	})
	if err != nil {
		t.Fatalf("A starts resource call: %v", err)
	}

	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire pinned connection: %v", err)
	}
	defer conn.Release()
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin A's leave: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Mirrors LeaveResourceCall's own steps (lock the row, delete the
	// actor's lease, and — since A is the last participant — mark the call
	// ended) without going through the store's all-in-one method, which
	// begins and commits internally and so cannot be paused mid-transaction
	// from outside.
	if _, err := tx.Exec(ctx, `SELECT id FROM chat.calls WHERE id = $1 FOR UPDATE`, call.ID); err != nil {
		t.Fatalf("lock call for A's leave: %v", err)
	}
	if ct, err := tx.Exec(ctx,
		`DELETE FROM chat.call_participant_leases WHERE call_id = $1 AND user_id = $2`,
		call.ID, leavePGUserA,
	); err != nil || ct.RowsAffected() != 1 {
		t.Fatalf("delete A's lease: rowsAffected=%v err=%v", ct, err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE chat.calls SET status = 'ended', version = version + 1, ended_at = clock_timestamp() WHERE id = $1`,
		call.ID,
	); err != nil {
		t.Fatalf("end call: %v", err)
	}

	joined := make(chan struct{})
	var joinedCall domain.Call
	var joinErr error
	go func() {
		defer close(joined)
		joinedCall, _, joinErr = store.CreateResourceCall(ctx, storage.CreateResourceCallInput{
			WorkspaceID: leavePGWorkspace,
			RequestID:   "c5000000-0000-4000-8000-0000000000a2",
			CallerID:    leavePGUserC,
			TargetType:  domain.CallTargetChannel,
			TargetID:    leavePGChannel,
			Type:        domain.CallTypeAudio,
			ExpiresAt:   leaseTTL,
		})
	}()

	waitForPostgresLockWaiter(t, pool)
	select {
	case <-joined:
		t.Fatal("C's CreateResourceCall must not proceed while A's leave still holds the row lock")
	default:
	}

	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit A's leave: %v", err)
	}

	select {
	case <-joined:
	case <-time.After(5 * time.Second):
		t.Fatal("C's CreateResourceCall never unblocked after A's leave committed")
	}
	if joinErr != nil {
		t.Fatalf("C's CreateResourceCall: %v", joinErr)
	}

	// C must land on a brand-new, genuinely active call — never the one A
	// just ended: "ou C entra validamente na mesma call antes dela
	// terminar, ou A termina a call e C cria/entra em uma nova call."
	if joinedCall.ID == call.ID {
		t.Fatal("C must not be attached to the call A just ended")
	}
	if joinedCall.Status != domain.CallStatusActive {
		t.Fatalf("C's call status = %q, want active", joinedCall.Status)
	}
	if status := currentCallStatus(t, pool, call.ID); status != string(domain.CallStatusEnded) {
		t.Fatalf("A's original call status = %q, want ended", status)
	}
	if status := currentCallStatus(t, pool, joinedCall.ID); status != string(domain.CallStatusActive) {
		t.Fatalf("C's new call status = %q, want active", status)
	}

	var leasesOnEndedCall int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM chat.call_participant_leases WHERE call_id = $1`, call.ID,
	).Scan(&leasesOnEndedCall); err != nil {
		t.Fatalf("count leases on ended call: %v", err)
	}
	if leasesOnEndedCall != 0 {
		t.Fatalf("the ended call must carry no active lease at all, found %d", leasesOnEndedCall)
	}
	if !hasActiveLease(t, pool, joinedCall.ID, leavePGUserC) {
		t.Fatal("C must hold an active lease on its new call")
	}

	var activeCount int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM chat.calls WHERE workspace_id = $1 AND target_type = 'channel' AND target_id = $2 AND status = 'active'`,
		leavePGWorkspace, leavePGChannel,
	).Scan(&activeCount); err != nil {
		t.Fatalf("count active calls for target: %v", err)
	}
	if activeCount != 1 {
		t.Fatalf("active resource calls for the target = %d, want exactly 1", activeCount)
	}
}

// TestPGXCallStoreTwoSimultaneousLastLeavesProduceExactlyOneEndedTransitionPostgreSQL
// drives the "A and B are the two last participants, both call.leave at
// nearly the same instant" scenario from review with genuine concurrency
// (both goroutines released by the same closed channel, not sequenced) —
// the property asserted below must hold regardless of which one the Go/OS
// scheduler actually runs first, which is what makes this deterministic
// without needing to force a specific interleaving.
func TestPGXCallStoreTwoSimultaneousLastLeavesProduceExactlyOneEndedTransitionPostgreSQL(t *testing.T) {
	pool := newCallLeavePool(t)
	store := storage.NewPGXCallStore(pool)
	ctx := context.Background()
	leaseTTL := time.Now().UTC().Add(30 * time.Second)

	call, _, err := store.CreateResourceCall(ctx, storage.CreateResourceCallInput{
		WorkspaceID: leavePGWorkspace,
		RequestID:   "c5000000-0000-4000-8000-0000000000b1",
		CallerID:    leavePGUserA,
		TargetType:  domain.CallTargetChannel,
		TargetID:    leavePGChannel,
		Type:        domain.CallTypeAudio,
		ExpiresAt:   leaseTTL,
	})
	if err != nil {
		t.Fatalf("A starts resource call: %v", err)
	}
	if _, _, err := store.CreateResourceCall(ctx, storage.CreateResourceCallInput{
		WorkspaceID: leavePGWorkspace,
		RequestID:   "c5000000-0000-4000-8000-0000000000b2",
		CallerID:    leavePGUserB,
		TargetType:  domain.CallTargetChannel,
		TargetID:    leavePGChannel,
		Type:        domain.CallTypeAudio,
		ExpiresAt:   leaseTTL,
	}); err != nil {
		t.Fatalf("B joins resource call: %v", err)
	}

	actors := []string{leavePGUserA, leavePGUserB}
	results := make([]storage.TransitionCallResult, len(actors))
	errs := make([]error, len(actors))
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i, actor := range actors {
		wg.Add(1)
		go func(i int, actor string) {
			defer wg.Done()
			<-start
			results[i], errs[i] = store.LeaveResourceCall(ctx, storage.LeaveResourceCallInput{
				WorkspaceID: leavePGWorkspace,
				CallID:      call.ID,
				ActorID:     actor,
			})
		}(i, actor)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("leave[%d] (%s): %v", i, actors[i], err)
		}
	}

	changedCount := 0
	for _, result := range results {
		if result.Changed {
			changedCount++
		}
	}
	if changedCount != 1 {
		t.Fatalf("exactly one of the two simultaneous leaves must observe the final ended transition, got %d: %+v", changedCount, results)
	}
	// The other leave is not stale: whichever transaction committed first
	// correctly saw itself as not the last participant (the row lock
	// serializes the pair, so this is its true linearized status at that
	// instant), and only the DB's final state after both complete has to be
	// 'ended' — asserted below.

	if status := currentCallStatus(t, pool, call.ID); status != string(domain.CallStatusEnded) {
		t.Fatalf("final call status = %q, want ended", status)
	}
	var remainingLeases int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM chat.call_participant_leases WHERE call_id = $1`, call.ID,
	).Scan(&remainingLeases); err != nil {
		t.Fatalf("count remaining leases: %v", err)
	}
	if remainingLeases != 0 {
		t.Fatalf("no lease may survive both last participants leaving, found %d", remainingLeases)
	}
}

func hasActiveLease(t *testing.T, pool *pgxpool.Pool, callID, userID string) bool {
	t.Helper()
	var exists bool
	if err := pool.QueryRow(t.Context(),
		`SELECT EXISTS (SELECT 1 FROM chat.call_participant_leases WHERE call_id = $1 AND user_id = $2 AND expires_at > clock_timestamp())`,
		callID, userID,
	).Scan(&exists); err != nil {
		t.Fatalf("check lease for %s: %v", userID, err)
	}
	return exists
}

func currentCallStatus(t *testing.T, pool *pgxpool.Pool, callID string) string {
	t.Helper()
	var status string
	if err := pool.QueryRow(t.Context(), `SELECT status FROM chat.calls WHERE id = $1`, callID).Scan(&status); err != nil {
		t.Fatalf("read call status: %v", err)
	}
	return status
}
