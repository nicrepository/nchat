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
)

func callColumns() []string {
	return []string{
		"id", "workspace_id", "request_id", "caller_id", "callee_id",
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
		string(domain.CallTypeVideo), string(status), version, now, now,
		now.Add(30*time.Second), acceptedAt, endedAt,
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
			if test.wantErr == nil && (created || call.ID != callID) {
				t.Fatalf("expected existing call, call=%+v created=%v", call, created)
			}
			requireMetExpectations(t, mock)
		})
	}
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

func TestPGXCallStoreExpireDueUsesSkipLockedAndReturnsOnlyWinners(t *testing.T) {
	mock := newCategoryMock(t)
	now := time.Now().UTC()
	mock.ExpectQuery(`FOR UPDATE SKIP LOCKED.*UPDATE chat.calls`).
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
