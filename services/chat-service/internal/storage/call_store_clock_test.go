package storage_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
	"github.com/pashagolub/pgxmock/v2"
)

func TestPGXCallStoreUsesDatabaseClockForLateAccept(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	mock := newCategoryMock(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`FOR UPDATE`).WithArgs(callWorkspaceID, callID).
		WillReturnRows(callRow(now, domain.CallStatusRinging, 1))
	mock.ExpectQuery(`clock_timestamp`).WillReturnRows(
		pgxmock.NewRows([]string{"now"}).AddRow(now.Add(31 * time.Second)),
	)
	mock.ExpectQuery(`UPDATE chat.calls.*timed_out`).
		WithArgs(callID, string(domain.CallStatusTimedOut)).
		WillReturnRows(callRow(now, domain.CallStatusTimedOut, 2))
	mock.ExpectCommit()

	result, err := storage.NewPGXCallStore(mock).TransitionCall(context.Background(), storage.TransitionCallInput{
		WorkspaceID: callWorkspaceID, CallID: callID, ActorID: callCalleeID,
		Action: storage.CallActionAccept,
	})
	if !errors.Is(err, domain.ErrConflict) || !result.TimedOut ||
		result.Call.Status != domain.CallStatusTimedOut {
		t.Fatalf("late accept result=%+v err=%v", result, err)
	}
	requireMetExpectations(t, mock)
}
