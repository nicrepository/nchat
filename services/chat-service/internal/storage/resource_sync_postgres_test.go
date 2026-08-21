package storage_test

import (
	"context"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// TestStatementTimestampIsFixedAtStatementStartNotReevaluatedPostgreSQL is the
// semantic proof behind ActiveResourceCall's choice of statement_timestamp()
// over clock_timestamp() (adversarial review of issue #622's null-sync race).
//
// clock_timestamp() is volatile: PostgreSQL documents it as returning the
// actual current time, re-evaluated at the instant each occurrence of the
// expression runs — even multiple times within one statement. If
// ActiveResourceCall used it, a concurrent commit landing *after* this
// statement's READ COMMITTED snapshot was established but *before*
// clock_timestamp() was actually evaluated in the SELECT list could produce
// an observed_at later than that commit's own created_at, even though the
// snapshot correctly reported "not found" — exactly the false-freshness bug
// the null-sync race must not allow.
//
// statement_timestamp() is documented as the start time of the current
// statement, the same instant a READ COMMITTED snapshot for that statement is
// established. This test proves the two functions actually differ operationally,
// not just by documentation: pg_sleep() inside the statement's own FROM clause
// forces real wall-clock time to pass *during* execution, after the snapshot
// (and statement_timestamp()) were already fixed. clock_timestamp(), evaluated
// after the sleep, must reflect that elapsed time; statement_timestamp(),
// evaluated in the very same statement, must not have moved at all.
func TestStatementTimestampIsFixedAtStatementStartNotReevaluatedPostgreSQL(t *testing.T) {
	pool := newCallLeavePool(t)
	ctx := context.Background()

	// clock_after_sleep is a scalar subquery: PostgreSQL must execute
	// pg_sleep(0.2) as part of producing its one row before it can evaluate
	// the clock_timestamp() in that subquery's target list, so
	// clock_after_sleep genuinely reflects a moment ~200ms after this
	// statement began. statement_timestamp() sits in the very same
	// statement, evaluated in the same round trip.
	var statementTS, clockAfterSleep time.Time
	if err := pool.QueryRow(ctx, `
		SELECT statement_timestamp(), (SELECT clock_timestamp() FROM pg_sleep(0.2))
	`).Scan(&statementTS, &clockAfterSleep); err != nil {
		t.Fatalf("query timestamps: %v", err)
	}

	elapsed := clockAfterSleep.Sub(statementTS)
	if elapsed < 150*time.Millisecond {
		t.Fatalf("clock_timestamp() after the in-statement pg_sleep(0.2) (%s) is only %s ahead of "+
			"statement_timestamp() (%s) — the test did not actually force ~200ms of real time to pass "+
			"during statement execution, so it proves nothing", clockAfterSleep, elapsed, statementTS)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("elapsed time between statement_timestamp() and the post-sleep clock_timestamp() is "+
			"implausibly large (%s) — test environment too slow/unstable to trust this run", elapsed)
	}
	// The property ActiveResourceCall's observed_at depends on: real time
	// that elapses *during* a statement's execution (here, simulated by
	// pg_sleep; in ActiveResourceCall, simply query planning/execution work)
	// is reflected by clock_timestamp() but never by statement_timestamp(),
	// which stays pinned to the instant the statement — and its READ
	// COMMITTED snapshot — began. That is exactly why observed_at uses
	// statement_timestamp(): it cannot be later than the commit of any row
	// invisible to this statement's own snapshot, regardless of how long the
	// statement subsequently takes to finish evaluating.
}

// Real-PostgreSQL proof for issue #622's "null sync race": a call.resource.sync
// answer of call:null must never carry an observed_at that could falsely look
// newer, to a future client, than a call that had not yet committed when the
// sync ran.
//
// The scenario: a CreateResourceCall for this exact target is already
// in-flight — its transaction has begun and is blocked holding the very
// advisory lock lockCallKeys would take for this target — when
// ActiveResourceCall (call.resource.sync's storage layer) runs concurrently.
// ActiveResourceCall takes no lock at all, so it proceeds immediately and
// must report "not found" (the row does not exist yet). Only afterwards is
// the blocked create allowed to proceed, insert its row, and commit — its
// created_at is read by clock_timestamp() inside that INSERT, strictly after
// the sync's own snapshot was taken. observed_at must therefore never be
// after created_at. This covers the coarse (whole-transaction) ordering; the
// tighter intra-statement race is closed by construction (statement_timestamp()
// instead of clock_timestamp() — see the semantic proof above), not by timing
// alone, since no black-box test can reliably land inside a microsecond-scale
// window without controlling the query executor itself.
func TestPGXCallStoreActiveResourceCallObservedAtNeverAppearsNewerThanConcurrentCreatePostgreSQL(t *testing.T) {
	pool := newCallLeavePool(t)
	store := storage.NewPGXCallStore(pool)
	ctx := context.Background()
	leaseTTL := time.Now().UTC().Add(30 * time.Second)

	// Hold the exact advisory lock CreateResourceCall's lockCallKeys will
	// acquire for this target, on a pinned connection — so the create below
	// blocks before it can insert anything or read its own clock_timestamp().
	lockConn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire pinned connection: %v", err)
	}
	defer lockConn.Release()
	lockTx, err := lockConn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin lock holder: %v", err)
	}
	defer func() { _ = lockTx.Rollback(ctx) }()
	lockKey := leavePGWorkspace + ":" + string(domain.CallTargetChannel) + ":" + leavePGChannel
	if _, err := lockTx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, lockKey); err != nil {
		t.Fatalf("acquire target advisory lock: %v", err)
	}

	type createOutcome struct {
		call domain.Call
		err  error
	}
	createDone := make(chan createOutcome, 1)
	go func() {
		call, _, createErr := store.CreateResourceCall(ctx, storage.CreateResourceCallInput{
			WorkspaceID: leavePGWorkspace, RequestID: "c6220000-0000-4000-8000-0000000007a1",
			CallerID: leavePGUserA, TargetType: domain.CallTargetChannel, TargetID: leavePGChannel,
			Type: domain.CallTypeAudio, ExpiresAt: leaseTTL,
		})
		createDone <- createOutcome{call: call, err: createErr}
	}()
	waitForPostgresLockWaiter(t, pool)

	// The create is blocked behind our held lock and has inserted nothing:
	// the sync must see "not found" right now.
	result, err := store.ActiveResourceCall(ctx, leavePGWorkspace, leavePGUserB, domain.CallTargetChannel, leavePGChannel)
	if err != nil {
		t.Fatalf("ActiveResourceCall: %v", err)
	}
	if result.Found {
		t.Fatalf("sync saw a call that has not committed yet: %+v", result)
	}
	if !result.Authorized {
		t.Fatal("leavePGUserB is an active workspace member and must be authorized for the public channel")
	}
	observedAt := result.ObservedAt

	if err := lockTx.Commit(ctx); err != nil {
		t.Fatalf("release target lock: %v", err)
	}

	var created createOutcome
	select {
	case created = <-createDone:
	case <-time.After(5 * time.Second):
		t.Fatal("CreateResourceCall never unblocked after the target lock was released")
	}
	if created.err != nil {
		t.Fatalf("CreateResourceCall: %v", created.err)
	}

	if observedAt.After(created.call.CreatedAt) {
		t.Fatalf("observed_at (%s) is after the concurrently-created call's created_at (%s) — "+
			"a client could wrongly treat this null answer as newer than the call it missed",
			observedAt, created.call.CreatedAt)
	}
}

// The straightforward counterpart: once a call is genuinely active, sync must
// report it found, with an observed_at no earlier than the call's own
// created_at.
func TestPGXCallStoreActiveResourceCallObservedAtIsConsistentWithFoundCallPostgreSQL(t *testing.T) {
	pool := newCallLeavePool(t)
	store := storage.NewPGXCallStore(pool)
	ctx := context.Background()
	leaseTTL := time.Now().UTC().Add(30 * time.Second)

	call, _, err := store.CreateResourceCall(ctx, storage.CreateResourceCallInput{
		WorkspaceID: leavePGWorkspace, RequestID: "c6220000-0000-4000-8000-0000000007a2",
		CallerID: leavePGUserA, TargetType: domain.CallTargetChannel, TargetID: leavePGChannel,
		Type: domain.CallTypeAudio, ExpiresAt: leaseTTL,
	})
	if err != nil {
		t.Fatalf("seed active call: %v", err)
	}

	result, err := store.ActiveResourceCall(ctx, leavePGWorkspace, leavePGUserB, domain.CallTargetChannel, leavePGChannel)
	if err != nil {
		t.Fatalf("ActiveResourceCall: %v", err)
	}
	if !result.Found || result.Call.ID != call.ID {
		t.Fatalf("expected to find the active call, got %+v", result)
	}
	if result.ObservedAt.Before(call.CreatedAt) {
		t.Fatalf("observed_at (%s) is before the found call's own created_at (%s)", result.ObservedAt, call.CreatedAt)
	}
}

// Unauthorized must be indistinguishable from "no active call" at this layer
// too: Found is false, but Authorized is what actually differs internally —
// the protocol layer is what collapses them further (see CallService.ResourceSync).
func TestPGXCallStoreActiveResourceCallUnauthorizedNeverRevealsExistencePostgreSQL(t *testing.T) {
	pool := newCallLeavePool(t)
	store := storage.NewPGXCallStore(pool)
	ctx := context.Background()
	leaseTTL := time.Now().UTC().Add(30 * time.Second)

	if _, _, err := store.CreateResourceCall(ctx, storage.CreateResourceCallInput{
		WorkspaceID: leavePGWorkspace, RequestID: "c6220000-0000-4000-8000-0000000007a3",
		CallerID: leavePGUserA, TargetType: domain.CallTargetChannel, TargetID: leavePGChannel,
		Type: domain.CallTypeAudio, ExpiresAt: leaseTTL,
	}); err != nil {
		t.Fatalf("seed active call: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE chat.workspace_members SET status = 'suspended' WHERE workspace_id = $1 AND user_id = $2`,
		leavePGWorkspace, leavePGUserC,
	); err != nil {
		t.Fatalf("revoke membership: %v", err)
	}

	result, err := store.ActiveResourceCall(ctx, leavePGWorkspace, leavePGUserC, domain.CallTargetChannel, leavePGChannel)
	if err != nil {
		t.Fatalf("ActiveResourceCall: %v", err)
	}
	if result.Authorized {
		t.Fatal("a suspended member must not be authorized for the channel")
	}
	if result.Found || result.Call.ID != "" {
		t.Fatalf("an unauthorized caller must never receive call data: %+v", result)
	}
}

// TestPGXCallStoreActiveResourceCallNeverSeesTornStateAcrossConcurrentEndPostgreSQL
// is the "active -> concurrent end" scenario from adversarial review: a
// concurrent transaction is mid-way through ending the call (holds the row's
// UPDATE, has not committed) while ActiveResourceCall runs. The single-
// statement design (issue #622 fix) must see one whole, consistent, committed
// state — the pre-end "active" snapshot in full, with the call payload intact
// — never a mix of the old status with new data or vice versa, and never an
// error. Only after the concurrent end actually commits does a fresh read see
// "not found".
func TestPGXCallStoreActiveResourceCallNeverSeesTornStateAcrossConcurrentEndPostgreSQL(t *testing.T) {
	pool := newCallLeavePool(t)
	store := storage.NewPGXCallStore(pool)
	ctx := context.Background()
	leaseTTL := time.Now().UTC().Add(30 * time.Second)

	call, _, err := store.CreateResourceCall(ctx, storage.CreateResourceCallInput{
		WorkspaceID: leavePGWorkspace, RequestID: "c6220000-0000-4000-8000-0000000007b1",
		CallerID: leavePGUserA, TargetType: domain.CallTargetChannel, TargetID: leavePGChannel,
		Type: domain.CallTypeAudio, ExpiresAt: leaseTTL,
	})
	if err != nil {
		t.Fatalf("seed active call: %v", err)
	}

	endConn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire pinned connection: %v", err)
	}
	defer endConn.Release()
	endTx, err := endConn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin end-in-progress: %v", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = endTx.Rollback(ctx)
		}
	}()
	if _, err := endTx.Exec(ctx,
		`UPDATE chat.calls SET status = 'ended', version = version + 1, ended_at = clock_timestamp() WHERE id = $1`,
		call.ID,
	); err != nil {
		t.Fatalf("start end-in-progress (uncommitted): %v", err)
	}

	// The end is mid-transaction, not committed: a plain SELECT never blocks
	// on another transaction's row lock (MVCC, not 2PL), so this must see the
	// last COMMITTED state whole — still active, with the full call payload —
	// never a partial mix of old status and new data.
	result, err := store.ActiveResourceCall(ctx, leavePGWorkspace, leavePGUserB, domain.CallTargetChannel, leavePGChannel)
	if err != nil {
		t.Fatalf("ActiveResourceCall during uncommitted end: %v", err)
	}
	if !result.Found || result.Call.Status != domain.CallStatusActive || result.Call.Version != call.Version {
		t.Fatalf("torn read: expected the pre-end committed state whole, got %+v", result)
	}

	if err := endTx.Commit(ctx); err != nil {
		t.Fatalf("commit end: %v", err)
	}
	committed = true

	after, err := store.ActiveResourceCall(ctx, leavePGWorkspace, leavePGUserB, domain.CallTargetChannel, leavePGChannel)
	if err != nil {
		t.Fatalf("ActiveResourceCall after end committed: %v", err)
	}
	if after.Found {
		t.Fatalf("expected not found once the end actually committed, got %+v", after)
	}
}

// TestPGXCallStoreActiveResourceCallNeverSeesTornStateAcrossConcurrentMembershipRevokePostgreSQL
// is the "membership revoke concurrent" scenario from adversarial review: a
// concurrent transaction is mid-way through revoking the actor's workspace
// membership (uncommitted) while ActiveResourceCall runs. It must see the
// whole pre-revoke committed state — authorized, with the call payload —
// never a mix, and never an error.
func TestPGXCallStoreActiveResourceCallNeverSeesTornStateAcrossConcurrentMembershipRevokePostgreSQL(t *testing.T) {
	pool := newCallLeavePool(t)
	store := storage.NewPGXCallStore(pool)
	ctx := context.Background()
	leaseTTL := time.Now().UTC().Add(30 * time.Second)

	call, _, err := store.CreateResourceCall(ctx, storage.CreateResourceCallInput{
		WorkspaceID: leavePGWorkspace, RequestID: "c6220000-0000-4000-8000-0000000007b2",
		CallerID: leavePGUserA, TargetType: domain.CallTargetChannel, TargetID: leavePGChannel,
		Type: domain.CallTypeAudio, ExpiresAt: leaseTTL,
	})
	if err != nil {
		t.Fatalf("seed active call: %v", err)
	}

	revokeConn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire pinned connection: %v", err)
	}
	defer revokeConn.Release()
	revokeTx, err := revokeConn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin revoke-in-progress: %v", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = revokeTx.Rollback(ctx)
		}
	}()
	if _, err := revokeTx.Exec(ctx,
		`UPDATE chat.workspace_members SET status = 'suspended' WHERE workspace_id = $1 AND user_id = $2`,
		leavePGWorkspace, leavePGUserB,
	); err != nil {
		t.Fatalf("start revoke-in-progress (uncommitted): %v", err)
	}

	result, err := store.ActiveResourceCall(ctx, leavePGWorkspace, leavePGUserB, domain.CallTargetChannel, leavePGChannel)
	if err != nil {
		t.Fatalf("ActiveResourceCall during uncommitted revoke: %v", err)
	}
	if !result.Authorized || !result.Found || result.Call.ID != call.ID {
		t.Fatalf("torn read: expected the pre-revoke committed state whole, got %+v", result)
	}

	if err := revokeTx.Commit(ctx); err != nil {
		t.Fatalf("commit revoke: %v", err)
	}
	committed = true

	after, err := store.ActiveResourceCall(ctx, leavePGWorkspace, leavePGUserB, domain.CallTargetChannel, leavePGChannel)
	if err != nil {
		t.Fatalf("ActiveResourceCall after revoke committed: %v", err)
	}
	if after.Authorized || after.Found {
		t.Fatalf("expected unauthorized/not-found once the revoke actually committed, got %+v", after)
	}
}
