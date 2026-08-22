package storage_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// Real-PostgreSQL proof for issue #622's call.join, mirroring #609's own
// proof structure (call_single_active_postgres_test.go /
// call_leave_postgres_test.go) but exercising the new, dedicated
// JoinResourceCall path rather than CreateResourceCall's reuse branch.

// TestPGXCallStoreJoinXVsJoinYSerializesPerActorPostgreSQL is scenario A: the
// same actor fires two concurrent call.join requests at two DIFFERENT,
// already-active calls. Exactly one incompatible participation may win.
func TestPGXCallStoreJoinXVsJoinYSerializesPerActorPostgreSQL(t *testing.T) {
	pool := newSingleActiveCallPool(t)
	store := storage.NewPGXCallStore(pool)
	ctx := context.Background()
	leaseTTL := time.Now().UTC().Add(30 * time.Second)

	callX, _, _, err := store.CreateResourceCall(ctx, storage.CreateResourceCallInput{
		WorkspaceID: singlePGWorkspace, RequestID: "c6220000-0000-4000-8000-0000000001a1",
		CallerID: singlePGCounterpart1, TargetType: domain.CallTargetChannel, TargetID: singlePGChannelX,
		Type: domain.CallTypeAudio, ExpiresAt: leaseTTL,
	})
	if err != nil {
		t.Fatalf("seed call X: %v", err)
	}
	callY, _, _, err := store.CreateResourceCall(ctx, storage.CreateResourceCallInput{
		WorkspaceID: singlePGWorkspace, RequestID: "c6220000-0000-4000-8000-0000000001a2",
		CallerID: singlePGCounterpart2, TargetType: domain.CallTargetChannel, TargetID: singlePGChannelY,
		Type: domain.CallTypeAudio, ExpiresAt: leaseTTL,
	})
	if err != nil {
		t.Fatalf("seed call Y: %v", err)
	}

	calls := []domain.Call{callX, callY}
	targets := []string{singlePGChannelX, singlePGChannelY}
	results := make([]error, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range calls {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, _, results[i] = store.JoinResourceCall(ctx, storage.JoinResourceCallInput{
				WorkspaceID: singlePGWorkspace, CallID: calls[i].ID, ActorID: singlePGActor,
				TargetType: domain.CallTargetChannel, TargetID: targets[i], ExpiresAt: leaseTTL,
			})
		}(i)
	}
	close(start)
	wg.Wait()

	successes, busyCount := 0, 0
	for i, err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, domain.ErrCallParticipantBusy):
			busyCount++
		default:
			t.Fatalf("join[%d] unexpected error: %v", i, err)
		}
	}
	if successes != 1 || busyCount != 1 {
		t.Fatalf("want exactly one success and one busy, got successes=%d busy=%d results=%+v", successes, busyCount, results)
	}
	if got := countActorLiveLeases(t, pool, singlePGActor); got != 1 {
		t.Fatalf("actor live leases = %d, want exactly 1", got)
	}
}

// TestPGXCallStoreDirectStartVsJoinConcurrentAdmissionSerializesPostgreSQL is
// scenario B: the same actor concurrently starts a direct 1:1 and joins an
// already-active resource call. Exactly one admission may win, sharing the
// actor's own advisory lock exactly as CreateCall/CreateResourceCall already
// do (issue #609).
func TestPGXCallStoreDirectStartVsJoinConcurrentAdmissionSerializesPostgreSQL(t *testing.T) {
	pool := newSingleActiveCallPool(t)
	store := storage.NewPGXCallStore(pool)
	ctx := context.Background()
	leaseTTL := time.Now().UTC().Add(30 * time.Second)

	callX, _, _, err := store.CreateResourceCall(ctx, storage.CreateResourceCallInput{
		WorkspaceID: singlePGWorkspace, RequestID: "c6220000-0000-4000-8000-0000000002a1",
		CallerID: singlePGCounterpart1, TargetType: domain.CallTargetChannel, TargetID: singlePGChannelX,
		Type: domain.CallTypeAudio, ExpiresAt: leaseTTL,
	})
	if err != nil {
		t.Fatalf("seed call X: %v", err)
	}

	var directErr, joinErr error
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		_, _, directErr = store.CreateCall(ctx, storage.CreateCallInput{
			WorkspaceID: singlePGWorkspace, RequestID: "c6220000-0000-4000-8000-0000000002a2",
			CallerID: singlePGActor, CalleeID: singlePGCounterpart2,
			Type: domain.CallTypeAudio, ExpiresAt: leaseTTL,
		})
	}()
	go func() {
		defer wg.Done()
		<-start
		_, _, joinErr = store.JoinResourceCall(ctx, storage.JoinResourceCallInput{
			WorkspaceID: singlePGWorkspace, CallID: callX.ID, ActorID: singlePGActor,
			TargetType: domain.CallTargetChannel, TargetID: singlePGChannelX, ExpiresAt: leaseTTL,
		})
	}()
	close(start)
	wg.Wait()

	directWon := directErr == nil
	joinWon := joinErr == nil
	if directWon == joinWon {
		t.Fatalf("exactly one admission must win, got directErr=%v joinErr=%v", directErr, joinErr)
	}
	if directErr != nil && !errors.Is(directErr, domain.ErrCallParticipantBusy) {
		t.Fatalf("direct loser error = %v, want participant-busy", directErr)
	}
	if joinErr != nil && !errors.Is(joinErr, domain.ErrCallParticipantBusy) {
		t.Fatalf("join loser error = %v, want participant-busy", joinErr)
	}
	wantLeases := 0
	if joinWon {
		wantLeases = 1
	}
	if got := countActorLiveLeases(t, pool, singlePGActor); got != wantLeases {
		t.Fatalf("actor live leases = %d, want %d", got, wantLeases)
	}
}

// TestPGXCallStoreLastLeaveVsConcurrentJoinNeverLeavesLeaseOnEndedCallPostgreSQL
// is scenario C: A is the last participant leaving X while B concurrently
// tries to join X. Whichever wins the row lock first determines the
// serialized outcome, and the one invariant that must hold regardless of
// which one wins is asserted here: a lease is never attached to an ended
// call, and A's own lease never survives its leave either way.
func TestPGXCallStoreLastLeaveVsConcurrentJoinNeverLeavesLeaseOnEndedCallPostgreSQL(t *testing.T) {
	pool := newCallLeavePool(t)
	store := storage.NewPGXCallStore(pool)
	ctx := context.Background()
	leaseTTL := time.Now().UTC().Add(30 * time.Second)

	call, _, participationA, err := store.CreateResourceCall(ctx, storage.CreateResourceCallInput{
		WorkspaceID: leavePGWorkspace, RequestID: "c6220000-0000-4000-8000-0000000003a1",
		CallerID: leavePGUserA, TargetType: domain.CallTargetChannel, TargetID: leavePGChannel,
		Type: domain.CallTypeAudio, ExpiresAt: leaseTTL,
	})
	if err != nil {
		t.Fatalf("A starts resource call: %v", err)
	}

	var leaveErr error
	var leaveResult storage.TransitionCallResult
	var joinErr error
	var joinedCall domain.Call
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		leaveResult, leaveErr = store.LeaveResourceCall(ctx, storage.LeaveResourceCallInput{
			WorkspaceID: leavePGWorkspace, CallID: call.ID, ActorID: leavePGUserA,
			ParticipationID: participationA,
		})
	}()
	go func() {
		defer wg.Done()
		<-start
		joinedCall, _, joinErr = store.JoinResourceCall(ctx, storage.JoinResourceCallInput{
			WorkspaceID: leavePGWorkspace, CallID: call.ID, ActorID: leavePGUserB,
			TargetType: domain.CallTargetChannel, TargetID: leavePGChannel, ExpiresAt: leaseTTL,
		})
	}()
	close(start)
	wg.Wait()

	if leaveErr != nil {
		t.Fatalf("A's leave: %v", leaveErr)
	}
	switch {
	case joinErr == nil:
		// Join won: X must stay active, with B's lease live.
		if joinedCall.Status != domain.CallStatusActive {
			t.Fatalf("join won but call status = %q, want active", joinedCall.Status)
		}
		if !hasActiveLease(t, pool, call.ID, leavePGUserB) {
			t.Fatal("join won but B has no live lease")
		}
		if currentCallStatus(t, pool, call.ID) != string(domain.CallStatusActive) {
			t.Fatal("join won but chat.calls.status is not active")
		}
		if leaveResult.Changed || leaveResult.Call.Status != domain.CallStatusActive {
			t.Fatalf("A's leave must have found itself not-last (B was already seated): %+v", leaveResult)
		}
	case errors.Is(joinErr, domain.ErrConflict):
		// Leave won: X ended before B's join reached it, so B must get no
		// lease at all — never one attached to an ended call.
		if hasActiveLease(t, pool, call.ID, leavePGUserB) {
			t.Fatal("leave won but B still gained a lease on the ended call")
		}
		if currentCallStatus(t, pool, call.ID) != string(domain.CallStatusEnded) {
			t.Fatal("leave won but chat.calls.status is not ended")
		}
		if !leaveResult.Changed || leaveResult.Call.Status != domain.CallStatusEnded {
			t.Fatalf("A's leave must have observed itself as last: %+v", leaveResult)
		}
	default:
		t.Fatalf("unexpected join error: %v", joinErr)
	}
	if hasActiveLease(t, pool, call.ID, leavePGUserA) {
		t.Fatal("A's lease must not survive its own leave, regardless of which side won")
	}
}

// TestPGXCallStoreConcurrentDuplicateJoinsProduceExactlyOneLeasePostgreSQL is
// scenario D: two tabs of the same user send duplicate call.join for the
// SAME call at nearly the same instant. Both must succeed (retry is
// idempotent) and the database must still hold exactly one lease.
func TestPGXCallStoreConcurrentDuplicateJoinsProduceExactlyOneLeasePostgreSQL(t *testing.T) {
	pool := newCallLeavePool(t)
	store := storage.NewPGXCallStore(pool)
	ctx := context.Background()
	leaseTTL := time.Now().UTC().Add(30 * time.Second)

	call, _, _, err := store.CreateResourceCall(ctx, storage.CreateResourceCallInput{
		WorkspaceID: leavePGWorkspace, RequestID: "c6220000-0000-4000-8000-0000000004a1",
		CallerID: leavePGUserA, TargetType: domain.CallTargetChannel, TargetID: leavePGChannel,
		Type: domain.CallTypeAudio, ExpiresAt: leaseTTL,
	})
	if err != nil {
		t.Fatalf("A starts resource call: %v", err)
	}

	results := make([]error, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, _, results[i] = store.JoinResourceCall(ctx, storage.JoinResourceCallInput{
				WorkspaceID: leavePGWorkspace, CallID: call.ID, ActorID: leavePGUserB,
				TargetType: domain.CallTargetChannel, TargetID: leavePGChannel, ExpiresAt: leaseTTL,
			})
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range results {
		if err != nil {
			t.Fatalf("duplicate join[%d]: %v", i, err)
		}
	}
	if got := countActorLiveLeases(t, pool, leavePGUserB); got != 1 {
		t.Fatalf("B's live leases = %d, want exactly 1", got)
	}
}

// TestPGXCallStoreJoinFailsAfterMembershipRevokedPostgreSQL is scenario E:
// membership is revoked between discovery and join. The join must fail and
// must not create a lease.
func TestPGXCallStoreJoinFailsAfterMembershipRevokedPostgreSQL(t *testing.T) {
	pool := newCallLeavePool(t)
	store := storage.NewPGXCallStore(pool)
	ctx := context.Background()
	leaseTTL := time.Now().UTC().Add(30 * time.Second)

	call, _, _, err := store.CreateResourceCall(ctx, storage.CreateResourceCallInput{
		WorkspaceID: leavePGWorkspace, RequestID: "c6220000-0000-4000-8000-0000000005a1",
		CallerID: leavePGUserA, TargetType: domain.CallTargetChannel, TargetID: leavePGChannel,
		Type: domain.CallTypeAudio, ExpiresAt: leaseTTL,
	})
	if err != nil {
		t.Fatalf("A starts resource call: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE chat.workspace_members SET status = 'suspended' WHERE workspace_id = $1 AND user_id = $2`,
		leavePGWorkspace, leavePGUserC,
	); err != nil {
		t.Fatalf("revoke membership: %v", err)
	}

	_, _, err = store.JoinResourceCall(ctx, storage.JoinResourceCallInput{
		WorkspaceID: leavePGWorkspace, CallID: call.ID, ActorID: leavePGUserC,
		TargetType: domain.CallTargetChannel, TargetID: leavePGChannel, ExpiresAt: leaseTTL,
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("error = %v, want not found (non-revealing)", err)
	}
	if hasActiveLease(t, pool, call.ID, leavePGUserC) {
		t.Fatal("a revoked member must not gain a lease")
	}
}

// TestPGXCallStoreJoinRejectsTargetMismatchWithoutMutationPostgreSQL is
// scenario F: a client claims call_id X actually belongs to target Y. The
// join must fail with no distinguishing detail and no mutation at all.
func TestPGXCallStoreJoinRejectsTargetMismatchWithoutMutationPostgreSQL(t *testing.T) {
	pool := newSingleActiveCallPool(t)
	store := storage.NewPGXCallStore(pool)
	ctx := context.Background()
	leaseTTL := time.Now().UTC().Add(30 * time.Second)

	callX, _, _, err := store.CreateResourceCall(ctx, storage.CreateResourceCallInput{
		WorkspaceID: singlePGWorkspace, RequestID: "c6220000-0000-4000-8000-0000000006a1",
		CallerID: singlePGCounterpart1, TargetType: domain.CallTargetChannel, TargetID: singlePGChannelX,
		Type: domain.CallTypeAudio, ExpiresAt: leaseTTL,
	})
	if err != nil {
		t.Fatalf("seed call X: %v", err)
	}

	_, _, err = store.JoinResourceCall(ctx, storage.JoinResourceCallInput{
		WorkspaceID: singlePGWorkspace, CallID: callX.ID, ActorID: singlePGActor,
		TargetType: domain.CallTargetChannel, TargetID: singlePGChannelY, ExpiresAt: leaseTTL,
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("error = %v, want not found", err)
	}
	if hasActiveLease(t, pool, callX.ID, singlePGActor) {
		t.Fatal("a mismatched join must not create a lease")
	}
	if got := countActiveResourceCallsForTarget(t, pool, singlePGWorkspace, singlePGChannelX); got != 1 {
		t.Fatalf("call X row must be untouched, active calls for X = %d", got)
	}
}

// TestPGXCallStoreJoinOldCallNeverSilentlyAdmitsIntoNewCallAfterLastLeavePostgreSQL
// is the adversarial-review scenario: X (call_id=old) is active with A. B has
// discovered "old" (e.g. via an earlier call.resource.sync). A performs the
// last leave, ending old, and — nearly simultaneously — C starts a brand-new
// call at the very same target, producing a different call_id ("new"). B
// then sends call.join(old), naming the call_id it actually knows.
//
// JoinResourceCall looks a call up strictly by (workspace_id, id) — never by
// target — so it is architecturally impossible for call.join(old) to land B
// in "new" instead: the row it locks and validates is old's own row, full
// stop. This test makes that invariant explicit and regression-proof: the
// join must fail (old is no longer active), no lease may appear on old, and
// no lease may appear on new either — B never gets seated anywhere by a
// call_id it did not name. The client is expected to resync and explicitly
// join "new" itself; call.join never redirects across resources for it.
func TestPGXCallStoreJoinOldCallNeverSilentlyAdmitsIntoNewCallAfterLastLeavePostgreSQL(t *testing.T) {
	pool := newCallLeavePool(t)
	store := storage.NewPGXCallStore(pool)
	ctx := context.Background()
	leaseTTL := time.Now().UTC().Add(30 * time.Second)

	old, _, oldParticipation, err := store.CreateResourceCall(ctx, storage.CreateResourceCallInput{
		WorkspaceID: leavePGWorkspace, RequestID: "c6220000-0000-4000-8000-0000000008a1",
		CallerID: leavePGUserA, TargetType: domain.CallTargetChannel, TargetID: leavePGChannel,
		Type: domain.CallTypeAudio, ExpiresAt: leaseTTL,
	})
	if err != nil {
		t.Fatalf("A starts old: %v", err)
	}

	// A's last leave ends old.
	leaveResult, err := store.LeaveResourceCall(ctx, storage.LeaveResourceCallInput{
		WorkspaceID: leavePGWorkspace, CallID: old.ID, ActorID: leavePGUserA,
		ParticipationID: oldParticipation,
	})
	if err != nil || !leaveResult.Changed || leaveResult.Call.Status != domain.CallStatusEnded {
		t.Fatalf("A's last leave must end old: result=%+v err=%v", leaveResult, err)
	}

	// C starts a brand-new call at the same target — a different call_id.
	newCall, _, _, err := store.CreateResourceCall(ctx, storage.CreateResourceCallInput{
		WorkspaceID: leavePGWorkspace, RequestID: "c6220000-0000-4000-8000-0000000008a2",
		CallerID: leavePGUserC, TargetType: domain.CallTargetChannel, TargetID: leavePGChannel,
		Type: domain.CallTypeAudio, ExpiresAt: leaseTTL,
	})
	if err != nil {
		t.Fatalf("C starts new: %v", err)
	}
	if newCall.ID == old.ID {
		t.Fatal("new must be a genuinely different call row from old")
	}

	// B names the call_id it actually knows: old. It must not be silently
	// admitted into new.
	_, _, err = store.JoinResourceCall(ctx, storage.JoinResourceCallInput{
		WorkspaceID: leavePGWorkspace, CallID: old.ID, ActorID: leavePGUserB,
		TargetType: domain.CallTargetChannel, TargetID: leavePGChannel, ExpiresAt: leaseTTL,
	})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("call.join(old) error = %v, want conflict (old has ended)", err)
	}
	if hasActiveLease(t, pool, old.ID, leavePGUserB) {
		t.Fatal("B must not gain a lease on the ended call old")
	}
	if hasActiveLease(t, pool, newCall.ID, leavePGUserB) {
		t.Fatal("B must not be silently seated in new — it never named new's call_id")
	}
	if got := countActorLiveLeases(t, pool, leavePGUserB); got != 0 {
		t.Fatalf("B must hold zero leases after a failed join(old), got %d", got)
	}

	// B must be free to explicitly join new once it resyncs and learns
	// new's own call_id.
	joined, _, err := store.JoinResourceCall(ctx, storage.JoinResourceCallInput{
		WorkspaceID: leavePGWorkspace, CallID: newCall.ID, ActorID: leavePGUserB,
		TargetType: domain.CallTargetChannel, TargetID: leavePGChannel, ExpiresAt: leaseTTL,
	})
	if err != nil || joined.ID != newCall.ID || joined.Status != domain.CallStatusActive {
		t.Fatalf("B's explicit join(new) after resync: call=%+v err=%v", joined, err)
	}
	if !hasActiveLease(t, pool, newCall.ID, leavePGUserB) {
		t.Fatal("B must hold a live lease on new after explicitly joining it")
	}
}
