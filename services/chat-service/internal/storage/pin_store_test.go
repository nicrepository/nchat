package storage_test

import (
	"context"
	"errors"
	"testing"

	pgxmock "github.com/pashagolub/pgxmock/v2"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// addPinSQL asserts the single-statement shape: message-in-channel guard (IDOR),
// existing-pin check, per-channel count, capped insert, idempotent conflict.
const addPinSQL = `(?s)WITH msg AS.*channel_id = \$1 AND status = 'active'.*existing AS.*cnt AS.*count\(\*\).*INSERT INTO chat\.message_pins.*< \$4.*ON CONFLICT \(channel_id, message_id\) DO NOTHING`

func pinAddRows(msgExists, already, inserted bool) *pgxmock.Rows {
	return pgxmock.NewRows([]string{"msg", "existing", "ins"}).AddRow(msgExists, already, inserted)
}

func TestPGXPinStore_AddPin_InsertedSucceeds(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(addPinSQL).
		WithArgs("ch-1", "msg-1", "user-1", storage.MaxPinsPerChannel).
		WillReturnRows(pinAddRows(true, false, true))

	if err := storage.NewPGXPinStore(mock).AddPin(context.Background(), "ch-1", "msg-1", "user-1"); err != nil {
		t.Fatalf("AddPin: %v", err)
	}
	checkExpectations(t, mock)
}

func TestPGXPinStore_AddPin_AlreadyPinnedIsIdempotent(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(addPinSQL).
		WithArgs("ch-1", "msg-1", "user-1", storage.MaxPinsPerChannel).
		WillReturnRows(pinAddRows(true, true, false))

	if err := storage.NewPGXPinStore(mock).AddPin(context.Background(), "ch-1", "msg-1", "user-1"); err != nil {
		t.Fatalf("expected idempotent success, got %v", err)
	}
	checkExpectations(t, mock)
}

func TestPGXPinStore_AddPin_MessageNotInChannel_ReturnsErrNotFound(t *testing.T) {
	mock := newMock(t)
	// IDOR guard: message_id not bound to channel_id (or missing/deleted) → msg=false.
	mock.ExpectQuery(addPinSQL).
		WithArgs("ch-1", "msg-1", "user-1", storage.MaxPinsPerChannel).
		WillReturnRows(pinAddRows(false, false, false))

	err := storage.NewPGXPinStore(mock).AddPin(context.Background(), "ch-1", "msg-1", "user-1")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	checkExpectations(t, mock)
}

func TestPGXPinStore_AddPin_CapReached_ReturnsErrPinLimitReached(t *testing.T) {
	mock := newMock(t)
	// Valid, not-yet-pinned message but nothing inserted → channel at cap.
	mock.ExpectQuery(addPinSQL).
		WithArgs("ch-1", "msg-1", "user-1", storage.MaxPinsPerChannel).
		WillReturnRows(pinAddRows(true, false, false))

	err := storage.NewPGXPinStore(mock).AddPin(context.Background(), "ch-1", "msg-1", "user-1")
	if !errors.Is(err, domain.ErrPinLimitReached) {
		t.Fatalf("expected ErrPinLimitReached, got %v", err)
	}
	checkExpectations(t, mock)
}

func TestPGXPinStore_RemovePin_DeletesChannelScopedRow(t *testing.T) {
	mock := newMock(t)
	mock.ExpectExec(`(?s)DELETE FROM chat\.message_pins.*channel_id = \$1 AND message_id = \$2`).
		WithArgs("ch-1", "msg-1").
		WillReturnResult(pgxmock.NewResult("DELETE", 1))

	if err := storage.NewPGXPinStore(mock).RemovePin(context.Background(), "ch-1", "msg-1"); err != nil {
		t.Fatalf("RemovePin: %v", err)
	}
	checkExpectations(t, mock)
}
