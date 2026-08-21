package storage_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// Fixture identifiers for the issue #609 real-PostgreSQL proof that a single
// actor can never hold an admitted participation in two different calls at
// once, direct or resource, no matter how the admission requests race.
const (
	singlePGWorkspace    = leavePGWorkspace
	singlePGChannelX     = leavePGChannel
	singlePGChannelY     = "c6090000-0000-4000-8000-0000000000c2"
	singlePGActor        = leavePGUserA
	singlePGCounterpart1 = leavePGUserD
	singlePGCounterpart2 = leavePGUserE
)

func newSingleActiveCallPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := newCallLeavePool(t)
	if _, err := pool.Exec(t.Context(),
		`INSERT INTO chat.channels (id, workspace_id, slug, display_name, type, is_general, status)
		 VALUES ($1, $2, 'segundo', 'segundo', 'public', false, 'active')`,
		singlePGChannelY, singlePGWorkspace,
	); err != nil {
		t.Fatalf("seed second channel: %v", err)
	}
	return pool
}

// countActorLiveLeases counts every lease actor currently, genuinely holds
// across the whole workspace — the ground truth for "how many calls is this
// actor really admitted into right now".
func countActorLiveLeases(t *testing.T, pool *pgxpool.Pool, actorID string) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(t.Context(),
		`SELECT count(*) FROM chat.call_participant_leases WHERE user_id = $1 AND expires_at > clock_timestamp()`,
		actorID,
	).Scan(&count); err != nil {
		t.Fatalf("count actor live leases: %v", err)
	}
	return count
}

func countCallRowsForTarget(t *testing.T, pool *pgxpool.Pool, workspaceID, targetID string) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(t.Context(),
		`SELECT count(*) FROM chat.calls WHERE workspace_id = $1 AND target_id = $2`,
		workspaceID, targetID,
	).Scan(&count); err != nil {
		t.Fatalf("count call rows for target: %v", err)
	}
	return count
}

func countActorLeasesForTarget(t *testing.T, pool *pgxpool.Pool, workspaceID, actorID, targetID string) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(t.Context(),
		`SELECT count(*) FROM chat.call_participant_leases leases
		 JOIN chat.calls calls ON calls.id = leases.call_id
		 WHERE calls.workspace_id = $1 AND leases.user_id = $2 AND calls.target_id = $3`,
		workspaceID, actorID, targetID,
	).Scan(&count); err != nil {
		t.Fatalf("count actor leases for target: %v", err)
	}
	return count
}

func countActiveResourceCallsForTarget(t *testing.T, pool *pgxpool.Pool, workspaceID, targetID string) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(t.Context(),
		`SELECT count(*) FROM chat.calls WHERE workspace_id = $1 AND target_id = $2 AND status = 'active'`,
		workspaceID, targetID,
	).Scan(&count); err != nil {
		t.Fatalf("count active call rows for target: %v", err)
	}
	return count
}

func waitForPostgresLockWaiters609(t *testing.T, pool *pgxpool.Pool, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var waiting int
		if err := pool.QueryRow(context.Background(),
			`SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND wait_event_type = 'Lock'`,
		).Scan(&waiting); err != nil {
			t.Fatalf("poll pg_stat_activity: %v", err)
		}
		if waiting >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d backends to block on locks", want)
}

// TestPGXCallStoreResourceVsResourceConcurrentJoinsSerializePerActorPostgreSQL
// is the real-PostgreSQL proof for issue #609's core scenario: the same actor
// fires two concurrent CreateResourceCall requests at two DIFFERENT, brand
// new targets. Both requests race to INSERT a new chat.calls row for their
// own target, so this also exercises the rollback requirement: whichever one
// loses the busy check must leave no trace at all of the row it inserted.
func TestPGXCallStoreResourceVsResourceConcurrentJoinsSerializePerActorPostgreSQL(t *testing.T) {
	pool := newSingleActiveCallPool(t)
	store := storage.NewPGXCallStore(pool)
	ctx := context.Background()
	leaseTTL := time.Now().UTC().Add(30 * time.Second)

	type outcome struct {
		call domain.Call
		err  error
	}
	results := make([]outcome, 2)
	targets := []string{singlePGChannelX, singlePGChannelY}
	requestIDs := []string{"c6090000-0000-4000-8000-0000000001a1", "c6090000-0000-4000-8000-0000000001a2"}

	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range targets {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			call, _, err := store.CreateResourceCall(ctx, storage.CreateResourceCallInput{
				WorkspaceID: singlePGWorkspace,
				RequestID:   requestIDs[i],
				CallerID:    singlePGActor,
				TargetType:  domain.CallTargetChannel,
				TargetID:    targets[i],
				Type:        domain.CallTypeAudio,
				ExpiresAt:   leaseTTL,
			})
			results[i] = outcome{call: call, err: err}
		}(i)
	}
	close(start)
	wg.Wait()

	successes, busyCount := 0, 0
	var winnerTarget, loserTarget string
	for i, result := range results {
		switch {
		case result.err == nil:
			successes++
			winnerTarget = targets[i]
		case errors.Is(result.err, domain.ErrCallParticipantBusy):
			busyCount++
			loserTarget = targets[i]
		default:
			t.Fatalf("unexpected error for target %s: %v", targets[i], result.err)
		}
	}
	if successes != 1 || busyCount != 1 {
		t.Fatalf("want exactly one success and one busy, got successes=%d busy=%d results=%+v", successes, busyCount, results)
	}

	// Exactly one live lease for the actor across the whole workspace.
	if got := countActorLiveLeases(t, pool, singlePGActor); got != 1 {
		t.Fatalf("actor live leases = %d, want exactly 1", got)
	}

	// The winning target has exactly one active call; the losing target has
	// NO call row at all — the insert it needed before the busy check could
	// run was rolled back in full, not merely marked something else.
	if got := countActiveResourceCallsForTarget(t, pool, singlePGWorkspace, winnerTarget); got != 1 {
		t.Fatalf("winner target active calls = %d, want 1", got)
	}
	if got := countCallRowsForTarget(t, pool, singlePGWorkspace, loserTarget); got != 0 {
		t.Fatalf("loser target call rows = %d, want 0 (no orphan call left by the rolled-back insert)", got)
	}
	if got := countActorLeasesForTarget(t, pool, singlePGWorkspace, singlePGActor, loserTarget); got != 0 {
		t.Fatalf("loser target leases = %d, want 0 (rolled-back admission must leave no lease)", got)
	}
}

// TestPGXCallStoreDirectVsResourceConcurrentAdmissionSerializesPostgreSQL
// covers issue #609's direct<->resource race: the same actor concurrently
// starts a direct 1:1 call and joins a resource call. The two admission paths
// share the actor's own advisory lock (see lockCallKeys in call_store.go), so
// exactly one of the two must win — which one is deliberately not asserted,
// only the resulting invariant.
func TestPGXCallStoreDirectVsResourceConcurrentAdmissionSerializesPostgreSQL(t *testing.T) {
	pool := newSingleActiveCallPool(t)
	store := storage.NewPGXCallStore(pool)
	ctx := context.Background()
	leaseTTL := time.Now().UTC().Add(30 * time.Second)

	var directErr, resourceErr error
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		_, _, directErr = store.CreateCall(ctx, storage.CreateCallInput{
			WorkspaceID: singlePGWorkspace,
			RequestID:   "c6090000-0000-4000-8000-0000000002a1",
			CallerID:    singlePGActor,
			CalleeID:    singlePGCounterpart1,
			Type:        domain.CallTypeAudio,
			ExpiresAt:   leaseTTL,
		})
	}()
	go func() {
		defer wg.Done()
		<-start
		_, _, resourceErr = store.CreateResourceCall(ctx, storage.CreateResourceCallInput{
			WorkspaceID: singlePGWorkspace,
			RequestID:   "c6090000-0000-4000-8000-0000000002a2",
			CallerID:    singlePGActor,
			TargetType:  domain.CallTargetChannel,
			TargetID:    singlePGChannelX,
			Type:        domain.CallTypeAudio,
			ExpiresAt:   leaseTTL,
		})
	}()
	close(start)
	wg.Wait()

	directWon := directErr == nil
	resourceWon := resourceErr == nil
	if directWon == resourceWon {
		t.Fatalf("exactly one admission must win, got directErr=%v resourceErr=%v", directErr, resourceErr)
	}
	if directErr != nil && !errors.Is(directErr, domain.ErrCallParticipantBusy) {
		t.Fatalf("direct loser error = %v, want participant-busy", directErr)
	}
	if resourceErr != nil && !errors.Is(resourceErr, domain.ErrCallParticipantBusy) {
		t.Fatalf("resource loser error = %v, want participant-busy", resourceErr)
	}

	// Ground truth: the actor holds exactly one live resource lease if
	// resource won, zero if direct won — and the loser left no orphan call.
	wantLeases := 0
	if resourceWon {
		wantLeases = 1
	}
	if got := countActorLiveLeases(t, pool, singlePGActor); got != wantLeases {
		t.Fatalf("actor live leases = %d, want %d", got, wantLeases)
	}
	if !resourceWon {
		if got := countCallRowsForTarget(t, pool, singlePGWorkspace, singlePGChannelX); got != 0 {
			t.Fatalf("losing resource target call rows = %d, want 0", got)
		}
	}

	var directRinging int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM chat.calls WHERE workspace_id = $1 AND target_type = 'user' AND caller_id = $2 AND status = 'ringing'`,
		singlePGWorkspace, singlePGActor,
	).Scan(&directRinging); err != nil {
		t.Fatalf("count direct ringing calls: %v", err)
	}
	wantDirect := 0
	if directWon {
		wantDirect = 1
	}
	if directRinging != wantDirect {
		t.Fatalf("actor's ringing direct calls = %d, want %d", directRinging, wantDirect)
	}
}

// TestPGXCallStoreLeaveThenImmediateResourceJoinNeverWaitsForTTLPostgreSQL
// covers issue #609's "leave -> new join" requirement: once LeaveResourceCall
// has actually committed, a brand-new CreateResourceCall for a different
// target must succeed immediately — no lease-expiry wait, no sweep.
func TestPGXCallStoreLeaveThenImmediateResourceJoinNeverWaitsForTTLPostgreSQL(t *testing.T) {
	pool := newSingleActiveCallPool(t)
	store := storage.NewPGXCallStore(pool)
	ctx := t.Context()
	leaseTTL := time.Now().UTC().Add(30 * time.Second)

	callX, _, err := store.CreateResourceCall(ctx, storage.CreateResourceCallInput{
		WorkspaceID: singlePGWorkspace,
		RequestID:   "c6090000-0000-4000-8000-0000000003a1",
		CallerID:    singlePGActor,
		TargetType:  domain.CallTargetChannel,
		TargetID:    singlePGChannelX,
		Type:        domain.CallTypeAudio,
		ExpiresAt:   leaseTTL,
	})
	if err != nil {
		t.Fatalf("actor joins X: %v", err)
	}

	if _, err := store.LeaveResourceCall(ctx, storage.LeaveResourceCallInput{
		WorkspaceID: singlePGWorkspace, CallID: callX.ID, ActorID: singlePGActor,
	}); err != nil {
		t.Fatalf("actor leaves X: %v", err)
	}

	callY, _, err := store.CreateResourceCall(ctx, storage.CreateResourceCallInput{
		WorkspaceID: singlePGWorkspace,
		RequestID:   "c6090000-0000-4000-8000-0000000003a2",
		CallerID:    singlePGActor,
		TargetType:  domain.CallTargetChannel,
		TargetID:    singlePGChannelY,
		Type:        domain.CallTypeAudio,
		ExpiresAt:   leaseTTL,
	})
	if err != nil {
		t.Fatalf("actor joins Y immediately after leaving X: %v", err)
	}
	if callY.Status != domain.CallStatusActive {
		t.Fatalf("Y status = %q, want active", callY.Status)
	}
	if got := countActorLiveLeases(t, pool, singlePGActor); got != 1 {
		t.Fatalf("actor live leases after leave+rejoin elsewhere = %d, want 1", got)
	}
}

// A production LeaveResourceCall holds the actor's participant lock before
// it waits on X's call row. A concurrent admission to Y must therefore wait
// for that leave to commit, then observe the deleted X lease and succeed.
func TestPGXCallStoreConcurrentLeaveXThenJoinYSerializesPerActorPostgreSQL(t *testing.T) {
	pool := newSingleActiveCallPool(t)
	store := storage.NewPGXCallStore(pool)
	ctx := context.Background()
	leaseTTL := time.Now().UTC().Add(30 * time.Second)

	callX, _, err := store.CreateResourceCall(ctx, storage.CreateResourceCallInput{
		WorkspaceID: singlePGWorkspace,
		RequestID:   "c6090000-0000-4000-8000-0000000003b1",
		CallerID:    singlePGActor,
		TargetType:  domain.CallTargetChannel,
		TargetID:    singlePGChannelX,
		Type:        domain.CallTypeAudio,
		ExpiresAt:   leaseTTL,
	})
	if err != nil {
		t.Fatalf("actor joins X: %v", err)
	}

	rowGate, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin X row gate: %v", err)
	}
	defer func() { _ = rowGate.Rollback(ctx) }()
	if _, err := rowGate.Exec(ctx, `SELECT id FROM chat.calls WHERE id = $1 FOR UPDATE`, callX.ID); err != nil {
		t.Fatalf("lock X row: %v", err)
	}

	type leaveOutcome struct {
		result storage.TransitionCallResult
		err    error
	}
	leaveDone := make(chan leaveOutcome, 1)
	go func() {
		result, err := store.LeaveResourceCall(ctx, storage.LeaveResourceCallInput{
			WorkspaceID: singlePGWorkspace, CallID: callX.ID, ActorID: singlePGActor,
		})
		leaveDone <- leaveOutcome{result: result, err: err}
	}()
	waitForPostgresLockWaiters609(t, pool, 1)

	type joinOutcome struct {
		call domain.Call
		err  error
	}
	joinDone := make(chan joinOutcome, 1)
	go func() {
		call, _, err := store.CreateResourceCall(ctx, storage.CreateResourceCallInput{
			WorkspaceID: singlePGWorkspace,
			RequestID:   "c6090000-0000-4000-8000-0000000003b2",
			CallerID:    singlePGActor,
			TargetType:  domain.CallTargetChannel,
			TargetID:    singlePGChannelY,
			Type:        domain.CallTypeAudio,
			ExpiresAt:   leaseTTL,
		})
		joinDone <- joinOutcome{call: call, err: err}
	}()
	waitForPostgresLockWaiters609(t, pool, 2)
	select {
	case result := <-joinDone:
		t.Fatalf("join Y returned before leave X could commit: %+v", result)
	default:
	}

	if err := rowGate.Commit(ctx); err != nil {
		t.Fatalf("release X row gate: %v", err)
	}
	var left leaveOutcome
	select {
	case left = <-leaveDone:
	case <-time.After(5 * time.Second):
		t.Fatal("leave X did not complete after its row lock was released")
	}
	if left.err != nil || !left.result.Changed || left.result.Call.Status != domain.CallStatusEnded {
		t.Fatalf("leave X result = %+v err=%v, want ended", left.result, left.err)
	}

	var joined joinOutcome
	select {
	case joined = <-joinDone:
	case <-time.After(5 * time.Second):
		t.Fatal("join Y did not complete after leave X committed")
	}
	if joined.err != nil || joined.call.Status != domain.CallStatusActive {
		t.Fatalf("join Y result = %+v err=%v, want active", joined.call, joined.err)
	}
	if hasActiveLease(t, pool, callX.ID, singlePGActor) {
		t.Fatal("X lease resurrected after concurrent leave -> join")
	}
	if !hasActiveLease(t, pool, joined.call.ID, singlePGActor) {
		t.Fatal("Y must hold the actor's only live lease")
	}
	if got := countActorLiveLeases(t, pool, singlePGActor); got != 1 {
		t.Fatalf("actor live leases = %d, want 1", got)
	}
	if got := countCallRowsForTarget(t, pool, singlePGWorkspace, singlePGChannelY); got != 1 {
		t.Fatalf("Y call rows = %d, want exactly 1", got)
	}
}

// TestPGXCallStoreExpiredResourceLeaseNeverBlocksAnotherResourceJoinPostgreSQL
// covers issue #609's "expired lease" requirement: a lease row that still
// physically exists but has already expired (not yet swept by
// ExpireDueCalls) must never count as busy.
func TestPGXCallStoreExpiredResourceLeaseNeverBlocksAnotherResourceJoinPostgreSQL(t *testing.T) {
	pool := newSingleActiveCallPool(t)
	store := storage.NewPGXCallStore(pool)
	ctx := t.Context()
	leaseTTL := time.Now().UTC().Add(30 * time.Second)

	callX, _, err := store.CreateResourceCall(ctx, storage.CreateResourceCallInput{
		WorkspaceID: singlePGWorkspace,
		RequestID:   "c6090000-0000-4000-8000-0000000004a1",
		CallerID:    singlePGActor,
		TargetType:  domain.CallTargetChannel,
		TargetID:    singlePGChannelX,
		Type:        domain.CallTypeAudio,
		ExpiresAt:   leaseTTL,
	})
	if err != nil {
		t.Fatalf("actor joins X: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE chat.call_participant_leases SET expires_at = clock_timestamp() - interval '1 minute' WHERE call_id = $1 AND user_id = $2`,
		callX.ID, singlePGActor,
	); err != nil {
		t.Fatalf("expire X's lease: %v", err)
	}

	callY, _, err := store.CreateResourceCall(ctx, storage.CreateResourceCallInput{
		WorkspaceID: singlePGWorkspace,
		RequestID:   "c6090000-0000-4000-8000-0000000004a2",
		CallerID:    singlePGActor,
		TargetType:  domain.CallTargetChannel,
		TargetID:    singlePGChannelY,
		Type:        domain.CallTypeAudio,
		ExpiresAt:   leaseTTL,
	})
	if err != nil {
		t.Fatalf("actor joins Y despite an expired lease on X: %v", err)
	}
	if callY.Status != domain.CallStatusActive {
		t.Fatalf("Y status = %q, want active", callY.Status)
	}
}

// TestPGXCallStoreSameResourceRejoinNeverBusyPostgreSQL covers issue #609's
// "same resource" requirement: an actor who already belongs to active resource
// call X performs a legitimate second join/rejoin of that SAME X (a fresh
// request_id, e.g. a reconnect gesture, not literal idempotent replay) and
// must never be told they are busy with themselves.
func TestPGXCallStoreSameResourceRejoinNeverBusyPostgreSQL(t *testing.T) {
	pool := newSingleActiveCallPool(t)
	store := storage.NewPGXCallStore(pool)
	ctx := t.Context()
	leaseTTL := time.Now().UTC().Add(30 * time.Second)

	first, _, err := store.CreateResourceCall(ctx, storage.CreateResourceCallInput{
		WorkspaceID: singlePGWorkspace,
		RequestID:   "c6090000-0000-4000-8000-0000000005a1",
		CallerID:    singlePGActor,
		TargetType:  domain.CallTargetChannel,
		TargetID:    singlePGChannelX,
		Type:        domain.CallTypeAudio,
		ExpiresAt:   leaseTTL,
	})
	if err != nil {
		t.Fatalf("actor joins X: %v", err)
	}

	rejoin, _, err := store.CreateResourceCall(ctx, storage.CreateResourceCallInput{
		WorkspaceID: singlePGWorkspace,
		RequestID:   "c6090000-0000-4000-8000-0000000005a2",
		CallerID:    singlePGActor,
		TargetType:  domain.CallTargetChannel,
		TargetID:    singlePGChannelX,
		Type:        domain.CallTypeAudio,
		ExpiresAt:   leaseTTL.Add(30 * time.Second),
	})
	if err != nil {
		t.Fatalf("actor rejoins the same X: %v", err)
	}
	if rejoin.ID != first.ID {
		t.Fatalf("rejoin landed on a different call: first=%s rejoin=%s", first.ID, rejoin.ID)
	}
	if got := countActorLiveLeases(t, pool, singlePGActor); got != 1 {
		t.Fatalf("actor live leases after rejoin = %d, want 1", got)
	}
	if _, _, err := store.CreateResourceCall(ctx, storage.CreateResourceCallInput{
		WorkspaceID: singlePGWorkspace,
		RequestID:   "c6090000-0000-4000-8000-0000000005a3",
		CallerID:    singlePGActor,
		TargetType:  domain.CallTargetChannel,
		TargetID:    singlePGChannelY,
		Type:        domain.CallTypeAudio,
		ExpiresAt:   leaseTTL,
	}); !errors.Is(err, domain.ErrCallParticipantBusy) {
		t.Fatalf("join Y after same-call rejoin error = %v, want participant-busy", err)
	}
}

// TestPGXCallStoreActiveResourceBlocksFreshDirectStartPostgreSQL is the
// direct-busy regression issue #609 must preserve: an actor with a live
// resource-call lease cannot start a brand-new direct 1:1.
func TestPGXCallStoreActiveResourceBlocksFreshDirectStartPostgreSQL(t *testing.T) {
	pool := newSingleActiveCallPool(t)
	store := storage.NewPGXCallStore(pool)
	ctx := t.Context()
	leaseTTL := time.Now().UTC().Add(30 * time.Second)

	if _, _, err := store.CreateResourceCall(ctx, storage.CreateResourceCallInput{
		WorkspaceID: singlePGWorkspace,
		RequestID:   "c6090000-0000-4000-8000-0000000006a1",
		CallerID:    singlePGActor,
		TargetType:  domain.CallTargetChannel,
		TargetID:    singlePGChannelX,
		Type:        domain.CallTypeAudio,
		ExpiresAt:   leaseTTL,
	}); err != nil {
		t.Fatalf("actor joins X: %v", err)
	}

	_, _, err := store.CreateCall(ctx, storage.CreateCallInput{
		WorkspaceID: singlePGWorkspace,
		RequestID:   "c6090000-0000-4000-8000-0000000006a2",
		CallerID:    singlePGActor,
		CalleeID:    singlePGCounterpart1,
		Type:        domain.CallTypeAudio,
		ExpiresAt:   leaseTTL,
	})
	if !errors.Is(err, domain.ErrCallParticipantBusy) {
		t.Fatalf("error = %v, want participant-busy", err)
	}
}

// TestPGXCallStoreRingingDirectBlocksResourceJoinPostgreSQL is the
// direct-busy regression issue #609 must preserve for the opposite
// direction: a merely-ringing (not yet accepted) direct call already blocks
// a resource join, not only an active one.
func TestPGXCallStoreRingingDirectBlocksResourceJoinPostgreSQL(t *testing.T) {
	pool := newSingleActiveCallPool(t)
	store := storage.NewPGXCallStore(pool)
	ctx := t.Context()
	leaseTTL := time.Now().UTC().Add(30 * time.Second)

	if _, _, err := store.CreateCall(ctx, storage.CreateCallInput{
		WorkspaceID: singlePGWorkspace,
		RequestID:   "c6090000-0000-4000-8000-0000000007a1",
		CallerID:    singlePGActor,
		CalleeID:    singlePGCounterpart1,
		Type:        domain.CallTypeAudio,
		ExpiresAt:   leaseTTL,
	}); err != nil {
		t.Fatalf("actor starts ringing direct call: %v", err)
	}

	_, _, err := store.CreateResourceCall(ctx, storage.CreateResourceCallInput{
		WorkspaceID: singlePGWorkspace,
		RequestID:   "c6090000-0000-4000-8000-0000000007a2",
		CallerID:    singlePGActor,
		TargetType:  domain.CallTargetChannel,
		TargetID:    singlePGChannelX,
		Type:        domain.CallTypeAudio,
		ExpiresAt:   leaseTTL,
	})
	if !errors.Is(err, domain.ErrCallParticipantBusy) {
		t.Fatalf("error = %v, want participant-busy", err)
	}
	// The rejected join must not have left an orphan call for X.
	if got := countCallRowsForTarget(t, pool, singlePGWorkspace, singlePGChannelX); got != 0 {
		t.Fatalf("channel X call rows = %d, want 0", got)
	}
}

func TestPGXCallStoreActiveDirectBlocksResourceJoinPostgreSQL(t *testing.T) {
	pool := newSingleActiveCallPool(t)
	store := storage.NewPGXCallStore(pool)
	ctx := t.Context()
	expiresAt := time.Now().UTC().Add(30 * time.Second)

	direct, _, err := store.CreateCall(ctx, storage.CreateCallInput{
		WorkspaceID: singlePGWorkspace, RequestID: "c6090000-0000-4000-8000-0000000007b1",
		CallerID: singlePGActor, CalleeID: singlePGCounterpart1,
		Type: domain.CallTypeAudio, ExpiresAt: expiresAt,
	})
	if err != nil {
		t.Fatalf("start direct call: %v", err)
	}
	if _, err := store.TransitionCall(ctx, storage.TransitionCallInput{
		WorkspaceID: singlePGWorkspace, CallID: direct.ID, ActorID: singlePGCounterpart1,
		Action: storage.CallActionAccept,
	}); err != nil {
		t.Fatalf("accept direct call: %v", err)
	}

	_, _, err = store.CreateResourceCall(ctx, storage.CreateResourceCallInput{
		WorkspaceID: singlePGWorkspace, RequestID: "c6090000-0000-4000-8000-0000000007b2",
		CallerID: singlePGActor, TargetType: domain.CallTargetChannel, TargetID: singlePGChannelX,
		Type: domain.CallTypeAudio, ExpiresAt: expiresAt,
	})
	if !errors.Is(err, domain.ErrCallParticipantBusy) {
		t.Fatalf("error = %v, want participant-busy", err)
	}
}

func TestPGXCallStoreDirectCallerAndCalleeStayBusyPostgreSQL(t *testing.T) {
	pool := newSingleActiveCallPool(t)
	store := storage.NewPGXCallStore(pool)
	ctx := t.Context()
	expiresAt := time.Now().UTC().Add(30 * time.Second)

	if _, _, err := store.CreateCall(ctx, storage.CreateCallInput{
		WorkspaceID: singlePGWorkspace, RequestID: "c6090000-0000-4000-8000-0000000008a1",
		CallerID: singlePGActor, CalleeID: singlePGCounterpart1,
		Type: domain.CallTypeAudio, ExpiresAt: expiresAt,
	}); err != nil {
		t.Fatalf("seed direct call: %v", err)
	}

	for _, input := range []storage.CreateCallInput{
		{
			WorkspaceID: singlePGWorkspace, RequestID: "c6090000-0000-4000-8000-0000000008a2",
			CallerID: singlePGActor, CalleeID: singlePGCounterpart2,
			Type: domain.CallTypeAudio, ExpiresAt: expiresAt,
		},
		{
			WorkspaceID: singlePGWorkspace, RequestID: "c6090000-0000-4000-8000-0000000008a3",
			CallerID: singlePGCounterpart2, CalleeID: singlePGCounterpart1,
			Type: domain.CallTypeAudio, ExpiresAt: expiresAt,
		},
	} {
		if _, _, err := store.CreateCall(ctx, input); !errors.Is(err, domain.ErrCallParticipantBusy) {
			t.Fatalf("direct admission error = %v, want participant-busy", err)
		}
	}
}

func TestPGXCallStoreExpiredResourceLeaseDoesNotBlockDirectPostgreSQL(t *testing.T) {
	pool := newSingleActiveCallPool(t)
	store := storage.NewPGXCallStore(pool)
	ctx := t.Context()
	expiresAt := time.Now().UTC().Add(30 * time.Second)

	callX, _, err := store.CreateResourceCall(ctx, storage.CreateResourceCallInput{
		WorkspaceID: singlePGWorkspace, RequestID: "c6090000-0000-4000-8000-0000000009a1",
		CallerID: singlePGActor, TargetType: domain.CallTargetChannel, TargetID: singlePGChannelX,
		Type: domain.CallTypeAudio, ExpiresAt: expiresAt,
	})
	if err != nil {
		t.Fatalf("join X: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE chat.call_participant_leases SET expires_at = clock_timestamp() - interval '1 minute' WHERE call_id = $1 AND user_id = $2`,
		callX.ID, singlePGActor,
	); err != nil {
		t.Fatalf("expire X lease: %v", err)
	}

	if _, _, err := store.CreateCall(ctx, storage.CreateCallInput{
		WorkspaceID: singlePGWorkspace, RequestID: "c6090000-0000-4000-8000-0000000009a2",
		CallerID: singlePGActor, CalleeID: singlePGCounterpart1,
		Type: domain.CallTypeAudio, ExpiresAt: expiresAt,
	}); err != nil {
		t.Fatalf("expired resource lease blocked direct call: %v", err)
	}
}

func TestPGXCallStoreEndedDirectCallDoesNotBlockResourceAdmissionPostgreSQL(t *testing.T) {
	pool := newSingleActiveCallPool(t)
	store := storage.NewPGXCallStore(pool)
	ctx := t.Context()
	expiresAt := time.Now().UTC().Add(30 * time.Second)

	direct, _, err := store.CreateCall(ctx, storage.CreateCallInput{
		WorkspaceID: singlePGWorkspace, RequestID: "c6090000-0000-4000-8000-0000000009b1",
		CallerID: singlePGActor, CalleeID: singlePGCounterpart1,
		Type: domain.CallTypeAudio, ExpiresAt: expiresAt,
	})
	if err != nil {
		t.Fatalf("start direct: %v", err)
	}
	if _, err := store.TransitionCall(ctx, storage.TransitionCallInput{
		WorkspaceID: singlePGWorkspace, CallID: direct.ID,
		ActorID: singlePGCounterpart1, Action: storage.CallActionAccept,
	}); err != nil {
		t.Fatalf("accept direct: %v", err)
	}
	if _, err := store.TransitionCall(ctx, storage.TransitionCallInput{
		WorkspaceID: singlePGWorkspace, CallID: direct.ID,
		ActorID: singlePGActor, Action: storage.CallActionEnd,
	}); err != nil {
		t.Fatalf("end direct: %v", err)
	}
	if _, _, err := store.CreateResourceCall(ctx, storage.CreateResourceCallInput{
		WorkspaceID: singlePGWorkspace, RequestID: "c6090000-0000-4000-8000-0000000009b2",
		CallerID: singlePGActor, TargetType: domain.CallTargetChannel, TargetID: singlePGChannelX,
		Type: domain.CallTypeAudio, ExpiresAt: expiresAt,
	}); err != nil {
		t.Fatalf("ended direct call blocked resource admission: %v", err)
	}
}

func TestPGXCallStorePresenceCannotResurrectOldParticipationPostgreSQL(t *testing.T) {
	pool := newSingleActiveCallPool(t)
	store := storage.NewPGXCallStore(pool)
	ctx := t.Context()
	expiresAt := time.Now().UTC().Add(30 * time.Second)

	callX, _, err := store.CreateResourceCall(ctx, storage.CreateResourceCallInput{
		WorkspaceID: singlePGWorkspace, RequestID: "c6090000-0000-4000-8000-0000000010a1",
		CallerID: singlePGActor, TargetType: domain.CallTargetChannel, TargetID: singlePGChannelX,
		Type: domain.CallTypeAudio, ExpiresAt: expiresAt,
	})
	if err != nil {
		t.Fatalf("join X: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE chat.call_participant_leases SET expires_at = clock_timestamp() - interval '1 minute' WHERE call_id = $1 AND user_id = $2`,
		callX.ID, singlePGActor,
	); err != nil {
		t.Fatalf("expire X lease: %v", err)
	}
	callY, _, err := store.CreateResourceCall(ctx, storage.CreateResourceCallInput{
		WorkspaceID: singlePGWorkspace, RequestID: "c6090000-0000-4000-8000-0000000010a2",
		CallerID: singlePGActor, TargetType: domain.CallTargetChannel, TargetID: singlePGChannelY,
		Type: domain.CallTypeAudio, ExpiresAt: expiresAt,
	})
	if err != nil {
		t.Fatalf("join Y: %v", err)
	}

	err = store.RenewCallPresence(ctx, storage.RenewCallPresenceInput{
		WorkspaceID: singlePGWorkspace, CallID: callX.ID, ActorID: singlePGActor,
		ExpiresAt: expiresAt.Add(30 * time.Second),
	})
	if !errors.Is(err, domain.ErrCallParticipantBusy) {
		t.Fatalf("stale X presence error = %v, want participant-busy", err)
	}
	if got := countActorLiveLeases(t, pool, singlePGActor); got != 1 {
		t.Fatalf("actor live leases after stale presence = %d, want 1", got)
	}
	if hasActiveLease(t, pool, callX.ID, singlePGActor) {
		t.Fatal("stale presence resurrected X's expired lease")
	}
	if !hasActiveLease(t, pool, callY.ID, singlePGActor) {
		t.Fatal("stale X presence disturbed Y's valid lease")
	}
}

func TestPGXCallStoreSameCallExpiredPresenceCanRenewPostgreSQL(t *testing.T) {
	pool := newSingleActiveCallPool(t)
	store := storage.NewPGXCallStore(pool)
	ctx := t.Context()
	expiresAt := time.Now().UTC().Add(30 * time.Second)

	callX, _, err := store.CreateResourceCall(ctx, storage.CreateResourceCallInput{
		WorkspaceID: singlePGWorkspace, RequestID: "c6090000-0000-4000-8000-0000000010b1",
		CallerID: singlePGActor, TargetType: domain.CallTargetChannel, TargetID: singlePGChannelX,
		Type: domain.CallTypeAudio, ExpiresAt: expiresAt,
	})
	if err != nil {
		t.Fatalf("join X: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE chat.call_participant_leases SET expires_at = clock_timestamp() - interval '1 minute' WHERE call_id = $1 AND user_id = $2`,
		callX.ID, singlePGActor,
	); err != nil {
		t.Fatalf("expire X lease: %v", err)
	}
	if err := store.RenewCallPresence(ctx, storage.RenewCallPresenceInput{
		WorkspaceID: singlePGWorkspace, CallID: callX.ID, ActorID: singlePGActor,
		ExpiresAt: expiresAt.Add(30 * time.Second),
	}); err != nil {
		t.Fatalf("renew same-call presence: %v", err)
	}
	if !hasActiveLease(t, pool, callX.ID, singlePGActor) {
		t.Fatal("same-call presence did not renew the expired X lease")
	}
}
