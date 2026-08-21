package storage_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	pgxmock "github.com/pashagolub/pgxmock/v2"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

const (
	callWorkspaceID = "00000000-0000-4000-8000-000000000001"
	callRequestID   = "00000000-0000-4000-8000-000000000002"
	callID          = "00000000-0000-4000-8000-000000000003"
	callCallerID    = "00000000-0000-4000-8000-000000000004"
	callCalleeID    = "00000000-0000-4000-8000-000000000005"
	callOutsiderID  = "00000000-0000-4000-8000-000000000006"
)

func callColumns() []string {
	return []string{
		"id", "workspace_id", "request_id", "caller_id", "callee_id",
		"target_type", "target_id",
		"call_type", "status", "version", "created_at", "updated_at",
		"expires_at", "accepted_at", "ended_at",
	}
}

func callRow(now time.Time, status domain.CallStatus, version int64) *pgxmock.Rows {
	var acceptedAt, endedAt any
	if status == domain.CallStatusActive || status == domain.CallStatusEnded {
		acceptedAt = now
	}
	if status.Terminal() {
		endedAt = now
	}
	return pgxmock.NewRows(callColumns()).AddRow(
		callID, callWorkspaceID, callRequestID, callCallerID, callCalleeID,
		string(domain.CallTargetUser), callCalleeID,
		string(domain.CallTypeVideo), string(status), version, now, now,
		now.Add(30*time.Second), acceptedAt, endedAt,
	)
}

func resourceCallRow(now time.Time, status domain.CallStatus, version int64) *pgxmock.Rows {
	return pgxmock.NewRows(callColumns()).AddRow(
		callID, callWorkspaceID, callRequestID, callCallerID, nil,
		string(domain.CallTargetChannel), callCalleeID,
		string(domain.CallTypeVideo), string(status), version, now, now,
		now.Add(30*time.Second), now, nil,
	)
}

func TestPGXCallStoreCreateSerializesParticipantsAndPersistsRinging(t *testing.T) {
	mock := newCategoryMock(t)
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	expiresAt := now.Add(30 * time.Second)

	mock.ExpectBegin()
	mock.ExpectExec(`pg_advisory_xact_lock`).WithArgs(callCallerID).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectExec(`pg_advisory_xact_lock`).WithArgs(callCalleeID).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery(`FROM chat.calls.*request_id`).WithArgs(callWorkspaceID, callCallerID, callRequestID).
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectQuery(`COUNT\(\*\).*workspace_members`).WithArgs(callWorkspaceID, callCallerID, callCalleeID).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(2))
	mock.ExpectQuery(`EXISTS.*FROM chat.calls`).WithArgs(callWorkspaceID, callCallerID, callCalleeID).
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectQuery(`INSERT INTO chat.calls`).
		WithArgs(callWorkspaceID, callRequestID, callCallerID, callCalleeID, string(domain.CallTypeVideo), expiresAt).
		WillReturnRows(callRow(now, domain.CallStatusRinging, 1))
	mock.ExpectCommit()

	call, created, err := storage.NewPGXCallStore(mock).CreateCall(context.Background(), storage.CreateCallInput{
		WorkspaceID: callWorkspaceID,
		RequestID:   callRequestID,
		CallerID:    callCallerID,
		CalleeID:    callCalleeID,
		Type:        domain.CallTypeVideo,
		ExpiresAt:   expiresAt,
	})
	if err != nil {
		t.Fatalf("CreateCall: %v", err)
	}
	if !created || call.ID != callID || call.Status != domain.CallStatusRinging {
		t.Fatalf("unexpected create result: call=%+v created=%v", call, created)
	}
	requireMetExpectations(t, mock)
}

func TestPGXCallStoreCreateIsIdempotentAndDetectsRequestConflict(t *testing.T) {
	now := time.Now().UTC()
	for _, test := range []struct {
		name     string
		calleeID string
		wantErr  error
	}{
		{name: "same payload", calleeID: callCalleeID},
		{name: "different payload", calleeID: "00000000-0000-4000-8000-000000000099", wantErr: domain.ErrConflict},
	} {
		t.Run(test.name, func(t *testing.T) {
			mock := newCategoryMock(t)
			mock.ExpectBegin()
			mock.ExpectExec(`pg_advisory_xact_lock`).WithArgs(callCallerID).
				WillReturnResult(pgxmock.NewResult("SELECT", 1))
			mock.ExpectExec(`pg_advisory_xact_lock`).WithArgs(test.calleeID).
				WillReturnResult(pgxmock.NewResult("SELECT", 1))
			mock.ExpectQuery(`FROM chat.calls.*request_id`).WithArgs(callWorkspaceID, callCallerID, callRequestID).
				WillReturnRows(callRow(now, domain.CallStatusRinging, 1))
			if test.wantErr == nil {
				mock.ExpectCommit()
			} else {
				mock.ExpectRollback()
			}

			call, created, err := storage.NewPGXCallStore(mock).CreateCall(context.Background(), storage.CreateCallInput{
				WorkspaceID: callWorkspaceID, RequestID: callRequestID,
				CallerID: callCallerID, CalleeID: test.calleeID,
				Type: domain.CallTypeVideo, ExpiresAt: now.Add(30 * time.Second),
			})
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v, want %v", err, test.wantErr)
			}
			if test.wantErr != nil && errors.Is(err, domain.ErrCallParticipantBusy) {
				// A mismatched idempotent replay is a real request conflict,
				// not a busy participant — it must never be classified as
				// busy (issue #575).
				t.Fatalf("error = %v, want plain conflict, not participant-busy", err)
			}
			if test.wantErr == nil && (created || call.ID != callID) {
				t.Fatalf("expected existing call, call=%+v created=%v", call, created)
			}
			requireMetExpectations(t, mock)
		})
	}
}

// TestPGXCallStoreCreateRejectsBusyParticipant covers every path that trips
// the combined busy check in one query (an active/ringing direct call, or a
// live call_participant_leases row from a resource call): caller busy,
// callee busy, and callee busy via resource participation all share the same
// $2/$3 IN-list, so they are indistinguishable from the caller's side and
// exercised as one scenario here. Issue #569 established the lease-vs-stale-
// caller_id distinction; issue #575 requires the result to classify as the
// specific domain.ErrCallParticipantBusy rather than the generic
// domain.ErrConflict, so it reaches the client as a busy-specific message
// instead of "call already changed state".
func TestPGXCallStoreCreateRejectsBusyParticipant(t *testing.T) {
	mock := newCategoryMock(t)
	now := time.Now().UTC()
	mock.ExpectBegin()
	mock.ExpectExec(`pg_advisory_xact_lock`).WithArgs(callCallerID).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectExec(`pg_advisory_xact_lock`).WithArgs(callCalleeID).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery(`FROM chat.calls.*request_id`).WithArgs(callWorkspaceID, callCallerID, callRequestID).
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectQuery(`COUNT\(\*\).*workspace_members`).WithArgs(callWorkspaceID, callCallerID, callCalleeID).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(2))
	mock.ExpectQuery(`call_participant_leases`).WithArgs(callWorkspaceID, callCallerID, callCalleeID).
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectRollback()

	_, _, err := storage.NewPGXCallStore(mock).CreateCall(context.Background(), storage.CreateCallInput{
		WorkspaceID: callWorkspaceID, RequestID: callRequestID,
		CallerID: callCallerID, CalleeID: callCalleeID,
		Type: domain.CallTypeVideo, ExpiresAt: now.Add(30 * time.Second),
	})
	if !errors.Is(err, domain.ErrCallParticipantBusy) {
		t.Fatalf("error = %v, want participant-busy", err)
	}
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("error = %v, want it to still satisfy the wrapped ErrConflict check", err)
	}
	requireMetExpectations(t, mock)
}

func TestPGXCallStoreCreatesAuthorizedResourceAndInitialLease(t *testing.T) {
	mock := newCategoryMock(t)
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	expiresAt := now.Add(30 * time.Second)
	lockKey := callWorkspaceID + ":channel:" + callCalleeID
	mock.ExpectBegin()
	mock.ExpectExec(`pg_advisory_xact_lock`).WithArgs(lockKey).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectExec(`pg_advisory_xact_lock`).WithArgs(callCallerID).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery(`FROM chat.calls.*request_id`).WithArgs(callWorkspaceID, callCallerID, callRequestID).
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectQuery(`chat\.channels.*channel_visible_to_user`).
		WithArgs(callWorkspaceID, callCallerID, callCalleeID).
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectQuery(`FROM chat.calls.*target_type.*status = 'active'`).
		WithArgs(callWorkspaceID, string(domain.CallTargetChannel), callCalleeID).
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectQuery(`INSERT INTO chat.calls.*'active'`).
		WithArgs(callWorkspaceID, callRequestID, callCallerID, string(domain.CallTargetChannel), callCalleeID, string(domain.CallTypeVideo), expiresAt).
		WillReturnRows(resourceCallRow(now, domain.CallStatusActive, 1))
	mock.ExpectQuery(`call_participant_leases`).WithArgs(callWorkspaceID, callCallerID, callID).
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectExec(`INSERT INTO chat.call_participant_leases`).
		WithArgs(callID, callCallerID, expiresAt).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()

	call, created, err := storage.NewPGXCallStore(mock).CreateResourceCall(context.Background(), storage.CreateResourceCallInput{
		WorkspaceID: callWorkspaceID, RequestID: callRequestID, CallerID: callCallerID,
		TargetType: domain.CallTargetChannel, TargetID: callCalleeID,
		Type: domain.CallTypeVideo, ExpiresAt: expiresAt,
	})
	if err != nil || !created || call.TargetType != domain.CallTargetChannel || call.Status != domain.CallStatusActive {
		t.Fatalf("resource call=%+v created=%v err=%v", call, created, err)
	}
	requireMetExpectations(t, mock)
}

func TestPGXCallStoreCreateResourceReplayStaysIdempotentAndDetectsMismatch(t *testing.T) {
	now := time.Now().UTC()
	for _, test := range []struct {
		name     string
		targetID string
		wantErr  error
	}{
		{name: "same payload", targetID: callCalleeID},
		{name: "different target", targetID: callOutsiderID, wantErr: domain.ErrConflict},
	} {
		t.Run(test.name, func(t *testing.T) {
			mock := newCategoryMock(t)
			lockKey := callWorkspaceID + ":channel:" + test.targetID
			mock.ExpectBegin()
			mock.ExpectExec(`pg_advisory_xact_lock`).WithArgs(lockKey).
				WillReturnResult(pgxmock.NewResult("SELECT", 1))
			mock.ExpectExec(`pg_advisory_xact_lock`).WithArgs(callCallerID).
				WillReturnResult(pgxmock.NewResult("SELECT", 1))
			mock.ExpectQuery(`FROM chat.calls.*request_id`).WithArgs(callWorkspaceID, callCallerID, callRequestID).
				WillReturnRows(resourceCallRow(now, domain.CallStatusActive, 1))
			if test.wantErr == nil {
				mock.ExpectCommit()
			} else {
				mock.ExpectRollback()
			}

			call, created, err := storage.NewPGXCallStore(mock).CreateResourceCall(context.Background(), storage.CreateResourceCallInput{
				WorkspaceID: callWorkspaceID, RequestID: callRequestID, CallerID: callCallerID,
				TargetType: domain.CallTargetChannel, TargetID: test.targetID,
				Type: domain.CallTypeVideo, ExpiresAt: now.Add(30 * time.Second),
			})
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v, want %v", err, test.wantErr)
			}
			if errors.Is(err, domain.ErrCallParticipantBusy) {
				t.Fatalf("mismatched replay must remain a plain conflict: %v", err)
			}
			if test.wantErr == nil && (created || call.ID != callID) {
				t.Fatalf("expected idempotent replay, call=%+v created=%v", call, created)
			}
			requireMetExpectations(t, mock)
		})
	}
}

// TestPGXCallStoreCreateResourceRejectsBusyActorAndRollsBackTheNewCall covers
// issue #609: an actor already busy elsewhere (direct call, or a lease on a
// DIFFERENT active resource call) must be refused even for the very first
// join that has to insert a brand-new chat.calls row for this target — and
// that insert must not survive. Since pgxmock.ExpectRollback (rather than
// ExpectCommit) is the only expectation after the busy check, this proves the
// call store never reaches upsertCallPresence/commit for the newly-inserted
// row; PostgreSQL-integration coverage below additionally proves the row is
// actually gone afterwards, which pgxmock cannot observe.
func TestPGXCallStoreCreateResourceRejectsBusyActorAndRollsBackTheNewCall(t *testing.T) {
	mock := newCategoryMock(t)
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	expiresAt := now.Add(30 * time.Second)
	lockKey := callWorkspaceID + ":channel:" + callCalleeID
	mock.ExpectBegin()
	mock.ExpectExec(`pg_advisory_xact_lock`).WithArgs(lockKey).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectExec(`pg_advisory_xact_lock`).WithArgs(callCallerID).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery(`FROM chat.calls.*request_id`).WithArgs(callWorkspaceID, callCallerID, callRequestID).
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectQuery(`chat\.channels.*channel_visible_to_user`).
		WithArgs(callWorkspaceID, callCallerID, callCalleeID).
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectQuery(`FROM chat.calls.*target_type.*status = 'active'`).
		WithArgs(callWorkspaceID, string(domain.CallTargetChannel), callCalleeID).
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectQuery(`INSERT INTO chat.calls.*'active'`).
		WithArgs(callWorkspaceID, callRequestID, callCallerID, string(domain.CallTargetChannel), callCalleeID, string(domain.CallTypeVideo), expiresAt).
		WillReturnRows(resourceCallRow(now, domain.CallStatusActive, 1))
	mock.ExpectQuery(`call_participant_leases`).WithArgs(callWorkspaceID, callCallerID, callID).
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectRollback()

	_, _, err := storage.NewPGXCallStore(mock).CreateResourceCall(context.Background(), storage.CreateResourceCallInput{
		WorkspaceID: callWorkspaceID, RequestID: callRequestID, CallerID: callCallerID,
		TargetType: domain.CallTargetChannel, TargetID: callCalleeID,
		Type: domain.CallTypeVideo, ExpiresAt: expiresAt,
	})
	if !errors.Is(err, domain.ErrCallParticipantBusy) {
		t.Fatalf("error = %v, want participant-busy", err)
	}
	requireMetExpectations(t, mock)
}

// TestPGXCallStoreCreateResourceExcludesTheSameCallFromItsOwnBusyCheck covers
// the late-join/rejoin path (issue #609): the busy query's third argument
// must be the EXISTING active call's id, not a freshly-inserted one, so a
// participant who already holds a live lease on the very call they are
// joining is never told they are busy with themselves.
func TestPGXCallStoreCreateResourceExcludesTheSameCallFromItsOwnBusyCheck(t *testing.T) {
	mock := newCategoryMock(t)
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	expiresAt := now.Add(30 * time.Second)
	lockKey := callWorkspaceID + ":channel:" + callCalleeID
	mock.ExpectBegin()
	mock.ExpectExec(`pg_advisory_xact_lock`).WithArgs(lockKey).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectExec(`pg_advisory_xact_lock`).WithArgs(callCallerID).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery(`FROM chat.calls.*request_id`).WithArgs(callWorkspaceID, callCallerID, callRequestID).
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectQuery(`chat\.channels.*channel_visible_to_user`).
		WithArgs(callWorkspaceID, callCallerID, callCalleeID).
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))
	// An already-active call for this target is found — no INSERT this time.
	mock.ExpectQuery(`FROM chat.calls.*target_type.*status = 'active'`).
		WithArgs(callWorkspaceID, string(domain.CallTargetChannel), callCalleeID).
		WillReturnRows(resourceCallRow(now, domain.CallStatusActive, 3))
	mock.ExpectQuery(`call_participant_leases`).WithArgs(callWorkspaceID, callCallerID, callID).
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectExec(`INSERT INTO chat.call_participant_leases`).
		WithArgs(callID, callCallerID, expiresAt).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()

	call, _, err := storage.NewPGXCallStore(mock).CreateResourceCall(context.Background(), storage.CreateResourceCallInput{
		WorkspaceID: callWorkspaceID, RequestID: callRequestID, CallerID: callCallerID,
		TargetType: domain.CallTargetChannel, TargetID: callCalleeID,
		Type: domain.CallTypeVideo, ExpiresAt: expiresAt,
	})
	if err != nil || call.ID != callID {
		t.Fatalf("rejoin call=%+v err=%v", call, err)
	}
	requireMetExpectations(t, mock)
}

func TestPGXCallStoreRenewsPresenceOnlyAfterResourceAuthorization(t *testing.T) {
	now := time.Now().UTC()
	for _, test := range []struct {
		name       string
		authorized bool
		wantErr    error
	}{
		{name: "member", authorized: true},
		{name: "outsider", wantErr: domain.ErrNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			mock := newCategoryMock(t)
			mock.ExpectBegin()
			mock.ExpectExec(`pg_advisory_xact_lock`).WithArgs(callCallerID).
				WillReturnResult(pgxmock.NewResult("SELECT", 1))
			mock.ExpectQuery(`FROM chat.calls.*FOR SHARE`).WithArgs(callWorkspaceID, callID).
				WillReturnRows(resourceCallRow(now, domain.CallStatusActive, 1))
			mock.ExpectQuery(`chat\.channels.*channel_visible_to_user`).
				WithArgs(callWorkspaceID, callCallerID, callCalleeID).
				WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(test.authorized))
			if test.authorized {
				mock.ExpectQuery(`call_participant_leases`).WithArgs(callWorkspaceID, callCallerID, callID).
					WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(false))
				mock.ExpectExec(`INSERT INTO chat.call_participant_leases`).
					WithArgs(callID, callCallerID, now.Add(30*time.Second)).
					WillReturnResult(pgxmock.NewResult("INSERT", 1))
				mock.ExpectCommit()
			} else {
				mock.ExpectRollback()
			}
			err := storage.NewPGXCallStore(mock).RenewCallPresence(context.Background(), storage.RenewCallPresenceInput{
				WorkspaceID: callWorkspaceID, CallID: callID, ActorID: callCallerID,
				ExpiresAt: now.Add(30 * time.Second),
			})
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("error=%v want=%v", err, test.wantErr)
			}
			requireMetExpectations(t, mock)
		})
	}
}

func TestPGXCallStoreRenewPresenceCannotAdmitActorBusyInAnotherCall(t *testing.T) {
	mock := newCategoryMock(t)
	now := time.Now().UTC()
	mock.ExpectBegin()
	mock.ExpectExec(`pg_advisory_xact_lock`).WithArgs(callCallerID).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery(`FROM chat.calls.*FOR SHARE`).WithArgs(callWorkspaceID, callID).
		WillReturnRows(resourceCallRow(now, domain.CallStatusActive, 1))
	mock.ExpectQuery(`chat\.channels.*channel_visible_to_user`).
		WithArgs(callWorkspaceID, callCallerID, callCalleeID).
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectQuery(`call_participant_leases`).WithArgs(callWorkspaceID, callCallerID, callID).
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectRollback()

	err := storage.NewPGXCallStore(mock).RenewCallPresence(context.Background(), storage.RenewCallPresenceInput{
		WorkspaceID: callWorkspaceID, CallID: callID, ActorID: callCallerID,
		ExpiresAt: now.Add(30 * time.Second),
	})
	if !errors.Is(err, domain.ErrCallParticipantBusy) {
		t.Fatalf("error = %v, want participant-busy", err)
	}
	requireMetExpectations(t, mock)
}

// ── LeaveResourceCall (issue #569): a participant leaving must release only
// their own lease, never the whole call — and must end the call
// deterministically, right away, only when they were its last one. ────────

func TestPGXCallStoreLeaveResourceCallReleasesOnlyTheActorsLease(t *testing.T) {
	mock := newCategoryMock(t)
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)

	mock.ExpectBegin()
	mock.ExpectExec(`pg_advisory_xact_lock`).WithArgs(callCallerID).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery(`FOR UPDATE`).WithArgs(callWorkspaceID, callID).
		WillReturnRows(resourceCallRow(now, domain.CallStatusActive, 3))
	mock.ExpectQuery(`DELETE FROM chat.call_participant_leases`).
		WithArgs(callID, callCallerID).
		WillReturnRows(pgxmock.NewRows([]string{"true"}).AddRow(true))
	mock.ExpectQuery(`EXISTS.*call_participant_leases.*expires_at > clock_timestamp\(\)`).
		WithArgs(callID).
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectCommit()

	result, err := storage.NewPGXCallStore(mock).LeaveResourceCall(context.Background(), storage.LeaveResourceCallInput{
		WorkspaceID: callWorkspaceID, CallID: callID, ActorID: callCallerID,
	})
	if err != nil {
		t.Fatalf("LeaveResourceCall: %v", err)
	}
	// Other participants remain: the call keeps going, unpublished.
	if result.Changed || result.Call.Status != domain.CallStatusActive {
		t.Fatalf("unexpected leave result: %+v", result)
	}
	requireMetExpectations(t, mock)
}

func TestPGXCallStoreLeaveResourceCallEndsDeterministicallyWhenLastParticipantLeaves(t *testing.T) {
	mock := newCategoryMock(t)
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)

	mock.ExpectBegin()
	mock.ExpectExec(`pg_advisory_xact_lock`).WithArgs(callCallerID).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery(`FOR UPDATE`).WithArgs(callWorkspaceID, callID).
		WillReturnRows(resourceCallRow(now, domain.CallStatusActive, 3))
	mock.ExpectQuery(`DELETE FROM chat.call_participant_leases`).
		WithArgs(callID, callCallerID).
		WillReturnRows(pgxmock.NewRows([]string{"true"}).AddRow(true))
	mock.ExpectQuery(`EXISTS.*call_participant_leases.*expires_at > clock_timestamp\(\)`).
		WithArgs(callID).
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectQuery(`UPDATE chat.calls.*status = \$2`).
		WithArgs(callID, string(domain.CallStatusEnded)).
		WillReturnRows(resourceCallRow(now, domain.CallStatusEnded, 4))
	mock.ExpectCommit()

	result, err := storage.NewPGXCallStore(mock).LeaveResourceCall(context.Background(), storage.LeaveResourceCallInput{
		WorkspaceID: callWorkspaceID, CallID: callID, ActorID: callCallerID,
	})
	if err != nil {
		t.Fatalf("LeaveResourceCall: %v", err)
	}
	// No lease survives the actor's own delete, and no lease is left behind by
	// ending the call here: nothing for ExpireDueCalls to ever have to sweep.
	if !result.Changed || result.Call.Status != domain.CallStatusEnded {
		t.Fatalf("unexpected leave result: %+v", result)
	}
	requireMetExpectations(t, mock)
}

func TestPGXCallStoreLeaveResourceCallIsIdempotentWhenActorAlreadyLeft(t *testing.T) {
	mock := newCategoryMock(t)
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)

	mock.ExpectBegin()
	mock.ExpectExec(`pg_advisory_xact_lock`).WithArgs(callCallerID).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery(`FOR UPDATE`).WithArgs(callWorkspaceID, callID).
		WillReturnRows(resourceCallRow(now, domain.CallStatusActive, 3))
	mock.ExpectQuery(`DELETE FROM chat.call_participant_leases`).
		WithArgs(callID, callCallerID).
		WillReturnError(pgx.ErrNoRows)
	// A retry/duplicate leave from someone who is still a legitimate member
	// of the call's own target is authorized exactly like RenewCallPresence
	// requires, and stays a safe no-op — see
	// TestPGXCallStoreLeaveResourceCallRejectsUnauthorizedActorWithoutMetadata
	// for the counterpart where this authorization fails.
	mock.ExpectQuery(`chat\.channels.*channel_visible_to_user`).
		WithArgs(callWorkspaceID, callCallerID, callCalleeID).
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectCommit()

	result, err := storage.NewPGXCallStore(mock).LeaveResourceCall(context.Background(), storage.LeaveResourceCallInput{
		WorkspaceID: callWorkspaceID, CallID: callID, ActorID: callCallerID,
	})
	if err != nil {
		t.Fatalf("LeaveResourceCall: %v", err)
	}
	if result.Changed || result.Call.Status != domain.CallStatusActive {
		t.Fatalf("a retried/duplicate leave must be a safe no-op: %+v", result)
	}
	requireMetExpectations(t, mock)
}

// Issue #569 follow-up: LeaveResourceCall must never hand back a call's
// metadata (target, status, timestamps) to a caller who never held a lease
// on it and is not currently authorized for its target — an authenticated
// user who merely knows/guesses a call_id must see the exact same
// domain.ErrNotFound (and receive nothing else) as one for a call_id that
// does not exist at all.
func TestPGXCallStoreLeaveResourceCallRejectsUnauthorizedActorWithoutMetadata(t *testing.T) {
	mock := newCategoryMock(t)
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)

	mock.ExpectBegin()
	mock.ExpectExec(`pg_advisory_xact_lock`).WithArgs(callOutsiderID).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery(`FOR UPDATE`).WithArgs(callWorkspaceID, callID).
		WillReturnRows(resourceCallRow(now, domain.CallStatusActive, 3))
	mock.ExpectQuery(`DELETE FROM chat.call_participant_leases`).
		WithArgs(callID, callOutsiderID).
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectQuery(`chat\.channels.*channel_visible_to_user`).
		WithArgs(callWorkspaceID, callOutsiderID, callCalleeID).
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectRollback()

	result, err := storage.NewPGXCallStore(mock).LeaveResourceCall(context.Background(), storage.LeaveResourceCallInput{
		WorkspaceID: callWorkspaceID, CallID: callID, ActorID: callOutsiderID,
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("error = %v, want not found", err)
	}
	if result.Call.ID != "" || result.Call.TargetID != "" || result.Call.Status != "" {
		t.Fatalf("unauthorized leave must not carry any call metadata: %+v", result)
	}
	requireMetExpectations(t, mock)
}

func TestPGXCallStoreLeaveResourceCallRejectsDirectCalls(t *testing.T) {
	mock := newCategoryMock(t)
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)

	mock.ExpectBegin()
	mock.ExpectExec(`pg_advisory_xact_lock`).WithArgs(callCallerID).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery(`FOR UPDATE`).WithArgs(callWorkspaceID, callID).
		WillReturnRows(callRow(now, domain.CallStatusActive, 2))
	mock.ExpectRollback()

	// call.leave has no meaning for RF-23's direct-call lifecycle — that
	// stays owned by call.decline/call.cancel/call.end, untouched here.
	_, err := storage.NewPGXCallStore(mock).LeaveResourceCall(context.Background(), storage.LeaveResourceCallInput{
		WorkspaceID: callWorkspaceID, CallID: callID, ActorID: callCallerID,
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("error = %v, want not found", err)
	}
	requireMetExpectations(t, mock)
}

func TestPGXCallStoreLeaveResourceCallReturnsNotFoundForMissingCall(t *testing.T) {
	mock := newCategoryMock(t)
	mock.ExpectBegin()
	mock.ExpectExec(`pg_advisory_xact_lock`).WithArgs(callCallerID).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery(`FOR UPDATE`).WithArgs(callWorkspaceID, callID).WillReturnError(pgx.ErrNoRows)
	mock.ExpectRollback()

	_, err := storage.NewPGXCallStore(mock).LeaveResourceCall(context.Background(), storage.LeaveResourceCallInput{
		WorkspaceID: callWorkspaceID, CallID: callID, ActorID: callCallerID,
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("error = %v, want not found", err)
	}
	requireMetExpectations(t, mock)
}

func TestPGXCallStoreTransitionAcceptAndDuplicate(t *testing.T) {
	now := time.Now().UTC()
	for _, test := range []struct {
		name        string
		current     domain.CallStatus
		expectWrite bool
	}{
		{name: "first accept", current: domain.CallStatusRinging, expectWrite: true},
		{name: "duplicate accept", current: domain.CallStatusActive},
	} {
		t.Run(test.name, func(t *testing.T) {
			mock := newCategoryMock(t)
			mock.ExpectBegin()
			mock.ExpectQuery(`FOR UPDATE`).WithArgs(callWorkspaceID, callID).
				WillReturnRows(callRow(now, test.current, 1))
			if test.expectWrite {
				mock.ExpectQuery(`clock_timestamp`).WillReturnRows(
					pgxmock.NewRows([]string{"now"}).AddRow(now),
				)
				mock.ExpectQuery(`UPDATE chat.calls.*status = \$2`).
					WithArgs(callID, string(domain.CallStatusActive)).
					WillReturnRows(callRow(now, domain.CallStatusActive, 2))
			}
			mock.ExpectCommit()

			result, err := storage.NewPGXCallStore(mock).TransitionCall(context.Background(), storage.TransitionCallInput{
				WorkspaceID: callWorkspaceID,
				CallID:      callID,
				ActorID:     callCalleeID,
				Action:      storage.CallActionAccept,
			})
			if err != nil {
				t.Fatalf("TransitionCall: %v", err)
			}
			if result.Call.Status != domain.CallStatusActive || result.Changed != test.expectWrite {
				t.Fatalf("unexpected transition: %+v", result)
			}
			requireMetExpectations(t, mock)
		})
	}
}

// ── JoinResourceCall (issue #622): admits an actor into an already-existing,
// active resource call — never creates one. ─────────────────────────────────

func TestPGXCallStoreJoinResourceCallAdmitsAuthorizedActor(t *testing.T) {
	mock := newCategoryMock(t)
	now := time.Now().UTC()
	expiresAt := now.Add(30 * time.Second)
	mock.ExpectBegin()
	mock.ExpectExec(`pg_advisory_xact_lock`).WithArgs(callCallerID).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery(`FROM chat.calls.*FOR UPDATE`).WithArgs(callWorkspaceID, callID).
		WillReturnRows(resourceCallRow(now, domain.CallStatusActive, 1))
	mock.ExpectQuery(`chat\.channels.*channel_visible_to_user`).
		WithArgs(callWorkspaceID, callCallerID, callCalleeID).
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectQuery(`call_participant_leases`).WithArgs(callWorkspaceID, callCallerID, callID).
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectExec(`INSERT INTO chat.call_participant_leases`).
		WithArgs(callID, callCallerID, expiresAt).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()

	call, err := storage.NewPGXCallStore(mock).JoinResourceCall(context.Background(), storage.JoinResourceCallInput{
		WorkspaceID: callWorkspaceID, CallID: callID, ActorID: callCallerID,
		TargetType: domain.CallTargetChannel, TargetID: callCalleeID, ExpiresAt: expiresAt,
	})
	if err != nil || call.ID != callID {
		t.Fatalf("JoinResourceCall: call=%+v err=%v", call, err)
	}
	requireMetExpectations(t, mock)
}

func TestPGXCallStoreJoinResourceCallRejectsTargetMismatchWithoutMutation(t *testing.T) {
	mock := newCategoryMock(t)
	now := time.Now().UTC()
	mock.ExpectBegin()
	mock.ExpectExec(`pg_advisory_xact_lock`).WithArgs(callCallerID).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery(`FROM chat.calls.*FOR UPDATE`).WithArgs(callWorkspaceID, callID).
		WillReturnRows(resourceCallRow(now, domain.CallStatusActive, 1))
	mock.ExpectRollback()

	_, err := storage.NewPGXCallStore(mock).JoinResourceCall(context.Background(), storage.JoinResourceCallInput{
		WorkspaceID: callWorkspaceID, CallID: callID, ActorID: callCallerID,
		// resourceCallRow's persisted target is callCalleeID; claiming a
		// different one must fail with no further query at all.
		TargetType: domain.CallTargetChannel, TargetID: callOutsiderID, ExpiresAt: now.Add(30 * time.Second),
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("error = %v, want not found", err)
	}
	requireMetExpectations(t, mock)
}

func TestPGXCallStoreJoinResourceCallRejectsEndedCall(t *testing.T) {
	mock := newCategoryMock(t)
	now := time.Now().UTC()
	mock.ExpectBegin()
	mock.ExpectExec(`pg_advisory_xact_lock`).WithArgs(callCallerID).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery(`FROM chat.calls.*FOR UPDATE`).WithArgs(callWorkspaceID, callID).
		WillReturnRows(resourceCallRow(now, domain.CallStatusEnded, 3))
	mock.ExpectRollback()

	_, err := storage.NewPGXCallStore(mock).JoinResourceCall(context.Background(), storage.JoinResourceCallInput{
		WorkspaceID: callWorkspaceID, CallID: callID, ActorID: callCallerID,
		TargetType: domain.CallTargetChannel, TargetID: callCalleeID, ExpiresAt: now.Add(30 * time.Second),
	})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("error = %v, want conflict", err)
	}
	requireMetExpectations(t, mock)
}

func TestPGXCallStoreJoinResourceCallRejectsUnauthorizedActor(t *testing.T) {
	mock := newCategoryMock(t)
	now := time.Now().UTC()
	mock.ExpectBegin()
	mock.ExpectExec(`pg_advisory_xact_lock`).WithArgs(callOutsiderID).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery(`FROM chat.calls.*FOR UPDATE`).WithArgs(callWorkspaceID, callID).
		WillReturnRows(resourceCallRow(now, domain.CallStatusActive, 1))
	mock.ExpectQuery(`chat\.channels.*channel_visible_to_user`).
		WithArgs(callWorkspaceID, callOutsiderID, callCalleeID).
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectRollback()

	_, err := storage.NewPGXCallStore(mock).JoinResourceCall(context.Background(), storage.JoinResourceCallInput{
		WorkspaceID: callWorkspaceID, CallID: callID, ActorID: callOutsiderID,
		TargetType: domain.CallTargetChannel, TargetID: callCalleeID, ExpiresAt: now.Add(30 * time.Second),
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("error = %v, want not found (non-revealing)", err)
	}
	requireMetExpectations(t, mock)
}

func TestPGXCallStoreJoinResourceCallRejectsBusyActor(t *testing.T) {
	mock := newCategoryMock(t)
	now := time.Now().UTC()
	mock.ExpectBegin()
	mock.ExpectExec(`pg_advisory_xact_lock`).WithArgs(callCallerID).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery(`FROM chat.calls.*FOR UPDATE`).WithArgs(callWorkspaceID, callID).
		WillReturnRows(resourceCallRow(now, domain.CallStatusActive, 1))
	mock.ExpectQuery(`chat\.channels.*channel_visible_to_user`).
		WithArgs(callWorkspaceID, callCallerID, callCalleeID).
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectQuery(`call_participant_leases`).WithArgs(callWorkspaceID, callCallerID, callID).
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectRollback()

	_, err := storage.NewPGXCallStore(mock).JoinResourceCall(context.Background(), storage.JoinResourceCallInput{
		WorkspaceID: callWorkspaceID, CallID: callID, ActorID: callCallerID,
		TargetType: domain.CallTargetChannel, TargetID: callCalleeID, ExpiresAt: now.Add(30 * time.Second),
	})
	if !errors.Is(err, domain.ErrCallParticipantBusy) {
		t.Fatalf("error = %v, want participant-busy", err)
	}
	requireMetExpectations(t, mock)
}

func TestPGXCallStoreJoinResourceCallRejectsDirectCalls(t *testing.T) {
	mock := newCategoryMock(t)
	now := time.Now().UTC()
	mock.ExpectBegin()
	mock.ExpectExec(`pg_advisory_xact_lock`).WithArgs(callCallerID).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery(`FROM chat.calls.*FOR UPDATE`).WithArgs(callWorkspaceID, callID).
		WillReturnRows(callRow(now, domain.CallStatusActive, 1))
	mock.ExpectRollback()

	_, err := storage.NewPGXCallStore(mock).JoinResourceCall(context.Background(), storage.JoinResourceCallInput{
		WorkspaceID: callWorkspaceID, CallID: callID, ActorID: callCallerID,
		TargetType: domain.CallTargetChannel, TargetID: callCalleeID, ExpiresAt: now.Add(30 * time.Second),
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("error = %v, want not found", err)
	}
	requireMetExpectations(t, mock)
}

func TestPGXCallStoreJoinResourceCallReturnsNotFoundForMissingCall(t *testing.T) {
	mock := newCategoryMock(t)
	mock.ExpectBegin()
	mock.ExpectExec(`pg_advisory_xact_lock`).WithArgs(callCallerID).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery(`FROM chat.calls.*FOR UPDATE`).WithArgs(callWorkspaceID, callID).
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectRollback()

	_, err := storage.NewPGXCallStore(mock).JoinResourceCall(context.Background(), storage.JoinResourceCallInput{
		WorkspaceID: callWorkspaceID, CallID: callID, ActorID: callCallerID,
		TargetType: domain.CallTargetChannel, TargetID: callCalleeID, ExpiresAt: time.Now().Add(30 * time.Second),
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("error = %v, want not found", err)
	}
	requireMetExpectations(t, mock)
}

// ── ActiveResourceCall (issue #622): read-only call.resource.sync lookup —
// creates no lease, issues no token, mutates nothing. ───────────────────────

// activeResourceCallColumns matches the single-statement shape ActiveResourceCall
// scans: authorized.ok, statement_timestamp(), then every chat.calls column
// (all nullable — absent entirely when LEFT JOIN active_call finds no row).
func activeResourceCallColumns() []string {
	return []string{
		"ok", "observed_at",
		"id", "workspace_id", "request_id", "caller_id", "callee_id",
		"target_type", "target_id", "call_type", "status", "version",
		"created_at", "updated_at", "expires_at", "accepted_at", "ended_at",
	}
}

func activeResourceCallFoundRow(authorized bool, observedAt, callTimestamps time.Time, status domain.CallStatus, version int64) *pgxmock.Rows {
	var acceptedAt, endedAt any
	if status == domain.CallStatusActive || status == domain.CallStatusEnded {
		acceptedAt = callTimestamps
	}
	if status.Terminal() {
		endedAt = callTimestamps
	}
	return pgxmock.NewRows(activeResourceCallColumns()).AddRow(
		authorized, observedAt,
		callID, callWorkspaceID, callRequestID, callCallerID, nil,
		string(domain.CallTargetChannel), callCalleeID, string(domain.CallTypeVideo), string(status), version,
		callTimestamps, callTimestamps, callTimestamps.Add(30*time.Second), acceptedAt, endedAt,
	)
}

func activeResourceCallNotFoundRow(authorized bool, observedAt time.Time) *pgxmock.Rows {
	return pgxmock.NewRows(activeResourceCallColumns()).AddRow(
		authorized, observedAt,
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
	)
}

func TestPGXCallStoreActiveResourceCallReturnsAuthorizedAndFound(t *testing.T) {
	mock := newCategoryMock(t)
	now := time.Now().UTC()
	mock.ExpectQuery(`(?s)authorized\.ok.*statement_timestamp\(\).*active_call\.\*.*LEFT JOIN active_call`).
		WithArgs(callWorkspaceID, callCallerID, callCalleeID).
		WillReturnRows(activeResourceCallFoundRow(true, now, now, domain.CallStatusActive, 1))

	result, err := storage.NewPGXCallStore(mock).ActiveResourceCall(
		context.Background(), callWorkspaceID, callCallerID, domain.CallTargetChannel, callCalleeID,
	)
	if err != nil {
		t.Fatalf("ActiveResourceCall: %v", err)
	}
	if !result.Authorized || !result.Found || result.Call.ID != callID || !result.ObservedAt.Equal(now) {
		t.Fatalf("unexpected result: %+v", result)
	}
	if result.Call.CalleeID != "" {
		t.Fatalf("resource call must carry no callee_id: %+v", result.Call)
	}
	requireMetExpectations(t, mock)
}

func TestPGXCallStoreActiveResourceCallReturnsNotFoundWhenAuthorizedButIdle(t *testing.T) {
	mock := newCategoryMock(t)
	now := time.Now().UTC()
	mock.ExpectQuery(`(?s)authorized\.ok.*statement_timestamp\(\).*active_call\.\*.*LEFT JOIN active_call`).
		WithArgs(callWorkspaceID, callCallerID, callCalleeID).
		WillReturnRows(activeResourceCallNotFoundRow(true, now))

	result, err := storage.NewPGXCallStore(mock).ActiveResourceCall(
		context.Background(), callWorkspaceID, callCallerID, domain.CallTargetChannel, callCalleeID,
	)
	if err != nil {
		t.Fatalf("ActiveResourceCall: %v", err)
	}
	if !result.Authorized || result.Found || result.Call.ID != "" || !result.ObservedAt.Equal(now) {
		t.Fatalf("unexpected result: %+v", result)
	}
	requireMetExpectations(t, mock)
}

// Authorized=false and Found=false at the storage layer are kept apart from
// "authorized but idle" so the protocol layer can collapse both into the same
// found=false wire response without the storage layer conflating them.
func TestPGXCallStoreActiveResourceCallDistinguishesUnauthorizedInternally(t *testing.T) {
	mock := newCategoryMock(t)
	now := time.Now().UTC()
	mock.ExpectQuery(`(?s)authorized\.ok.*statement_timestamp\(\).*active_call\.\*.*LEFT JOIN active_call`).
		WithArgs(callWorkspaceID, callOutsiderID, callCalleeID).
		WillReturnRows(activeResourceCallFoundRow(false, now, now, domain.CallStatusActive, 1))

	result, err := storage.NewPGXCallStore(mock).ActiveResourceCall(
		context.Background(), callWorkspaceID, callOutsiderID, domain.CallTargetChannel, callCalleeID,
	)
	if err != nil {
		t.Fatalf("ActiveResourceCall: %v", err)
	}
	if result.Authorized || result.Found || !result.ObservedAt.Equal(now) {
		t.Fatalf("unexpected result: %+v", result)
	}
	requireMetExpectations(t, mock)
}

func TestPGXCallStoreActiveResourceCallSupportsDMTarget(t *testing.T) {
	mock := newCategoryMock(t)
	now := time.Now().UTC()
	mock.ExpectQuery(`(?s)dm_members.*authorized\.ok.*statement_timestamp\(\).*active_call\.\*.*LEFT JOIN active_call`).
		WithArgs(callWorkspaceID, callCallerID, callCalleeID).
		WillReturnRows(activeResourceCallNotFoundRow(true, now))

	result, err := storage.NewPGXCallStore(mock).ActiveResourceCall(
		context.Background(), callWorkspaceID, callCallerID, domain.CallTargetDM, callCalleeID,
	)
	if err != nil {
		t.Fatalf("ActiveResourceCall: %v", err)
	}
	if !result.Authorized || result.Found {
		t.Fatalf("unexpected result: %+v", result)
	}
	requireMetExpectations(t, mock)
}

func TestPGXCallStoreExpireDueUsesSkipLockedAndReturnsOnlyWinners(t *testing.T) {
	mock := newCategoryMock(t)
	now := time.Now().UTC()
	mock.ExpectQuery(`call_participant_leases.*expires_at > clock_timestamp\(\).*FOR UPDATE SKIP LOCKED.*UPDATE chat.calls`).
		WithArgs(100).
		WillReturnRows(callRow(now, domain.CallStatusTimedOut, 2))

	calls, err := storage.NewPGXCallStore(mock).ExpireDueCalls(context.Background(), 100)
	if err != nil {
		t.Fatalf("ExpireDueCalls: %v", err)
	}
	if len(calls) != 1 || calls[0].Status != domain.CallStatusTimedOut {
		t.Fatalf("unexpected expired calls: %+v", calls)
	}
	requireMetExpectations(t, mock)
}
