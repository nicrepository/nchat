package storage_test

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

func TestPGXCallStoreParticipationFenceP1P2P3PostgreSQL(t *testing.T) {
	pool := newCallLeavePool(t)
	store := storage.NewPGXCallStore(pool)
	ctx := t.Context()
	expiresAt := time.Now().UTC().Add(30 * time.Second)

	call, _, p1, err := store.CreateResourceCall(ctx, storage.CreateResourceCallInput{
		WorkspaceID: leavePGWorkspace, RequestID: "c6220000-0000-4000-8000-000000003001",
		CallerID: leavePGUserA, TargetType: domain.CallTargetChannel, TargetID: leavePGChannel,
		Type: domain.CallTypeAudio, ExpiresAt: expiresAt,
	})
	if err != nil {
		t.Fatalf("admit P1: %v", err)
	}
	_, p2, err := store.JoinResourceCall(ctx, storage.JoinResourceCallInput{
		WorkspaceID: leavePGWorkspace, CallID: call.ID, ActorID: leavePGUserA,
		TargetType: domain.CallTargetChannel, TargetID: leavePGChannel, ExpiresAt: expiresAt,
	})
	if err != nil {
		t.Fatalf("admit P2: %v", err)
	}
	assertDistinctCanonicalParticipations(t, p1, p2)
	if got := currentLeaseParticipation(t, pool, call.ID, leavePGUserA); got != p2 {
		t.Fatalf("current participation = %q, want P2 %q", got, p2)
	}

	before := currentLeaseExpiry(t, pool, call.ID, leavePGUserA)
	if err := store.RenewCallPresence(ctx, storage.RenewCallPresenceInput{
		WorkspaceID: leavePGWorkspace, CallID: call.ID, ActorID: leavePGUserA,
		ParticipationID: p1, ExpiresAt: before.Add(time.Minute),
	}); !errors.Is(err, domain.ErrCallParticipationStale) {
		t.Fatalf("stale P1 presence error = %v", err)
	}
	if got := currentLeaseExpiry(t, pool, call.ID, leavePGUserA); !got.Equal(before) {
		t.Fatalf("stale P1 changed P2 expiry: before=%s after=%s", before, got)
	}

	renewedUntil := before.Add(2 * time.Minute)
	if err := store.RenewCallPresence(ctx, storage.RenewCallPresenceInput{
		WorkspaceID: leavePGWorkspace, CallID: call.ID, ActorID: leavePGUserA,
		ParticipationID: p2, ExpiresAt: renewedUntil,
	}); err != nil {
		t.Fatalf("current P2 presence: %v", err)
	}
	if got := currentLeaseExpiry(t, pool, call.ID, leavePGUserA); !got.Equal(renewedUntil) {
		t.Fatalf("P2 expiry = %s, want %s", got, renewedUntil)
	}

	staleLeave, err := store.LeaveResourceCall(ctx, storage.LeaveResourceCallInput{
		WorkspaceID: leavePGWorkspace, CallID: call.ID, ActorID: leavePGUserA, ParticipationID: p1,
	})
	if err != nil || staleLeave.Released || staleLeave.Changed {
		t.Fatalf("stale P1 leave: result=%+v err=%v", staleLeave, err)
	}
	if got := currentLeaseParticipation(t, pool, call.ID, leavePGUserA); got != p2 {
		t.Fatalf("stale P1 leave replaced P2 with %q", got)
	}
	if currentCallStatus(t, pool, call.ID) != string(domain.CallStatusActive) {
		t.Fatal("stale P1 leave ended the call")
	}

	// Keep the call active while A leaves, so a later stale P1 heartbeat must
	// prove it cannot recreate A's deleted row rather than short-circuiting on
	// an already-ended call.
	_, pB, err := store.JoinResourceCall(ctx, storage.JoinResourceCallInput{
		WorkspaceID: leavePGWorkspace, CallID: call.ID, ActorID: leavePGUserB,
		TargetType: domain.CallTargetChannel, TargetID: leavePGChannel, ExpiresAt: expiresAt,
	})
	if err != nil {
		t.Fatalf("admit B: %v", err)
	}
	currentLeave, err := store.LeaveResourceCall(ctx, storage.LeaveResourceCallInput{
		WorkspaceID: leavePGWorkspace, CallID: call.ID, ActorID: leavePGUserA, ParticipationID: p2,
	})
	if err != nil || !currentLeave.Released || currentLeave.Changed {
		t.Fatalf("current P2 leave with B remaining: result=%+v err=%v", currentLeave, err)
	}
	if err := store.RenewCallPresence(ctx, storage.RenewCallPresenceInput{
		WorkspaceID: leavePGWorkspace, CallID: call.ID, ActorID: leavePGUserA,
		ParticipationID: p1, ExpiresAt: expiresAt.Add(time.Minute),
	}); !errors.Is(err, domain.ErrCallParticipationStale) {
		t.Fatalf("P1 after P2 leave error = %v", err)
	}
	if hasLeaseRow(t, pool, call.ID, leavePGUserA) {
		t.Fatal("stale P1 presence recreated A's deleted lease")
	}

	_, p3, err := store.JoinResourceCall(ctx, storage.JoinResourceCallInput{
		WorkspaceID: leavePGWorkspace, CallID: call.ID, ActorID: leavePGUserA,
		TargetType: domain.CallTargetChannel, TargetID: leavePGChannel, ExpiresAt: expiresAt,
	})
	if err != nil {
		t.Fatalf("admit P3: %v", err)
	}
	assertDistinctCanonicalParticipations(t, p1, p2, p3)
	if result, err := store.LeaveResourceCall(ctx, storage.LeaveResourceCallInput{
		WorkspaceID: leavePGWorkspace, CallID: call.ID, ActorID: leavePGUserA, ParticipationID: p2,
	}); err != nil || result.Released || currentLeaseParticipation(t, pool, call.ID, leavePGUserA) != p3 {
		t.Fatalf("stale P2 touched P3: result=%+v err=%v", result, err)
	}

	// Missing participation_id is a legacy claim and must never match P3.
	if err := store.RenewCallPresence(ctx, storage.RenewCallPresenceInput{
		WorkspaceID: leavePGWorkspace, CallID: call.ID, ActorID: leavePGUserA,
		ExpiresAt: expiresAt.Add(2 * time.Minute),
	}); !errors.Is(err, domain.ErrCallParticipationStale) {
		t.Fatalf("legacy presence against fenced P3 error = %v", err)
	}
	if result, err := store.LeaveResourceCall(ctx, storage.LeaveResourceCallInput{
		WorkspaceID: leavePGWorkspace, CallID: call.ID, ActorID: leavePGUserA,
	}); err != nil || result.Released || currentLeaseParticipation(t, pool, call.ID, leavePGUserA) != p3 {
		t.Fatalf("legacy leave touched fenced P3: result=%+v err=%v", result, err)
	}

	if result, err := store.LeaveResourceCall(ctx, storage.LeaveResourceCallInput{
		WorkspaceID: leavePGWorkspace, CallID: call.ID, ActorID: leavePGUserA, ParticipationID: p3,
	}); err != nil || !result.Released || result.Changed {
		t.Fatalf("current P3 leave: result=%+v err=%v", result, err)
	}
	if result, err := store.LeaveResourceCall(ctx, storage.LeaveResourceCallInput{
		WorkspaceID: leavePGWorkspace, CallID: call.ID, ActorID: leavePGUserB, ParticipationID: pB,
	}); err != nil || !result.Released || !result.Changed || result.Call.Status != domain.CallStatusEnded {
		t.Fatalf("current last B leave: result=%+v err=%v", result, err)
	}
}

func TestPGXCallStoreConcurrentAdmissionSerializesAgainstStaleLeavePostgreSQL(t *testing.T) {
	pool := newCallLeavePool(t)
	store := storage.NewPGXCallStore(pool)
	ctx := t.Context()
	expiresAt := time.Now().UTC().Add(30 * time.Second)
	call, _, p1, err := store.CreateResourceCall(ctx, storage.CreateResourceCallInput{
		WorkspaceID: leavePGWorkspace, RequestID: "c6220000-0000-4000-8000-000000003101",
		CallerID: leavePGUserA, TargetType: domain.CallTargetChannel, TargetID: leavePGChannel,
		Type: domain.CallTypeAudio, ExpiresAt: expiresAt,
	})
	if err != nil {
		t.Fatalf("admit P1: %v", err)
	}
	_, p2, err := store.JoinResourceCall(ctx, storage.JoinResourceCallInput{
		WorkspaceID: leavePGWorkspace, CallID: call.ID, ActorID: leavePGUserA,
		TargetType: domain.CallTargetChannel, TargetID: leavePGChannel, ExpiresAt: expiresAt,
	})
	if err != nil {
		t.Fatalf("admit P2: %v", err)
	}

	var p3 string
	var joinErr, leaveErr error
	var leaveResult storage.TransitionCallResult
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		_, p3, joinErr = store.JoinResourceCall(ctx, storage.JoinResourceCallInput{
			WorkspaceID: leavePGWorkspace, CallID: call.ID, ActorID: leavePGUserA,
			TargetType: domain.CallTargetChannel, TargetID: leavePGChannel, ExpiresAt: expiresAt,
		})
	}()
	go func() {
		defer wg.Done()
		<-start
		leaveResult, leaveErr = store.LeaveResourceCall(ctx, storage.LeaveResourceCallInput{
			WorkspaceID: leavePGWorkspace, CallID: call.ID, ActorID: leavePGUserA, ParticipationID: p1,
		})
	}()
	close(start)
	wg.Wait()

	if joinErr != nil || leaveErr != nil || leaveResult.Released || leaveResult.Changed {
		t.Fatalf("serialized join/stale leave: p3=%q joinErr=%v leave=%+v leaveErr=%v", p3, joinErr, leaveResult, leaveErr)
	}
	assertDistinctCanonicalParticipations(t, p1, p2, p3)
	if got := currentLeaseParticipation(t, pool, call.ID, leavePGUserA); got != p3 {
		t.Fatalf("final participation = %q, want P3 %q", got, p3)
	}
}

func assertDistinctCanonicalParticipations(t *testing.T, values ...string) {
	t.Helper()
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		parsed, err := uuid.Parse(value)
		if err != nil || parsed.String() != value {
			t.Fatalf("participation_id %q is not a canonical UUID", value)
		}
		if _, duplicate := seen[value]; duplicate {
			t.Fatalf("participation_id reused (ABA): %q", value)
		}
		seen[value] = struct{}{}
	}
}

func currentLeaseParticipation(t *testing.T, pool *pgxpool.Pool, callID, userID string) string {
	t.Helper()
	var participationID string
	if err := pool.QueryRow(t.Context(),
		`SELECT participation_id::text FROM chat.call_participant_leases WHERE call_id = $1 AND user_id = $2`,
		callID, userID,
	).Scan(&participationID); err != nil {
		t.Fatalf("read participation id: %v", err)
	}
	return participationID
}

func currentLeaseExpiry(t *testing.T, pool *pgxpool.Pool, callID, userID string) time.Time {
	t.Helper()
	var expiresAt time.Time
	if err := pool.QueryRow(t.Context(),
		`SELECT expires_at FROM chat.call_participant_leases WHERE call_id = $1 AND user_id = $2`,
		callID, userID,
	).Scan(&expiresAt); err != nil {
		t.Fatalf("read lease expiry: %v", err)
	}
	return expiresAt.UTC()
}

func hasLeaseRow(t *testing.T, pool *pgxpool.Pool, callID, userID string) bool {
	t.Helper()
	var exists bool
	if err := pool.QueryRow(t.Context(),
		`SELECT EXISTS (SELECT 1 FROM chat.call_participant_leases WHERE call_id = $1 AND user_id = $2)`,
		callID, userID,
	).Scan(&exists); err != nil {
		t.Fatalf("check lease row: %v", err)
	}
	return exists
}
