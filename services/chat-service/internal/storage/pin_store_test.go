package storage_test

import (
	"context"
	"errors"
	"testing"
	"time"

	pgxmock "github.com/pashagolub/pgxmock/v2"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// addPinSQL asserts the single-statement shape: target-type-aware message guard
// (IDOR), existing-pin check, per-container count, capped insert, idempotent conflict.
const addPinSQL = `(?s)WITH msg AS.*id = \$5.*status = 'active'.*\(\$1 = 'channel' AND m\.channel_id = \$2 AND m\.dm_conversation_id IS NULL\).*\(\$1 = 'dm' AND m\.dm_conversation_id = \$2 AND m\.channel_id IS NULL\).*existing AS.*target_type = \$1 AND target_id = \$2 AND message_id = \$5.*cnt AS.*count\(\*\).*target_type = \$1 AND target_id = \$2.*INSERT INTO chat\.message_pins.*< \$6.*ON CONFLICT \(target_type, target_id, message_id\) DO NOTHING`

func pinAddRows(msgExists, already, inserted bool) *pgxmock.Rows {
	return pgxmock.NewRows([]string{"msg", "existing", "ins"}).AddRow(msgExists, already, inserted)
}

func pinListCols() []string {
	cols := []string{"allowed", "has_pin"}
	cols = append(cols, listMessageCols()...)
	return append(cols, "pinned_at", "pinned_by_user_id", "total_count")
}

func pinListRow(allowed, hasPin bool, msg []any, pinnedAt time.Time, pinnedByUserID string, totalCount int) []any {
	row := []any{allowed, hasPin}
	row = append(row, msg...)
	return append(row, pinnedAt, pinnedByUserID, totalCount)
}

func TestPGXPinStore_AddPin_InsertedSucceeds(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(addPinSQL).
		WithArgs("channel", "ch-1", "ws-1", "user-1", "msg-1", storage.MaxPinsPerContainer).
		WillReturnRows(pinAddRows(true, false, true))

	if err := storage.NewPGXPinStore(mock).AddPin(context.Background(), "ws-1", "channel", "ch-1", "msg-1", "user-1"); err != nil {
		t.Fatalf("AddPin: %v", err)
	}
	checkExpectations(t, mock)
}

func TestPGXPinStore_AddPin_AlreadyPinnedIsIdempotent(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(addPinSQL).
		WithArgs("channel", "ch-1", "ws-1", "user-1", "msg-1", storage.MaxPinsPerContainer).
		WillReturnRows(pinAddRows(true, true, false))

	if err := storage.NewPGXPinStore(mock).AddPin(context.Background(), "ws-1", "channel", "ch-1", "msg-1", "user-1"); err != nil {
		t.Fatalf("expected idempotent success, got %v", err)
	}
	checkExpectations(t, mock)
}

func TestPGXPinStore_AddPin_MessageNotInChannel_ReturnsErrNotFound(t *testing.T) {
	mock := newMock(t)
	// IDOR guard: message_id not bound to target type/id (or missing/deleted) → msg=false.
	mock.ExpectQuery(addPinSQL).
		WithArgs("dm", "dm-1", "ws-1", "user-1", "msg-1", storage.MaxPinsPerContainer).
		WillReturnRows(pinAddRows(false, false, false))

	err := storage.NewPGXPinStore(mock).AddPin(context.Background(), "ws-1", "dm", "dm-1", "msg-1", "user-1")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	checkExpectations(t, mock)
}

func TestPGXPinStore_AddPin_CapReached_ReturnsErrPinLimitReached(t *testing.T) {
	mock := newMock(t)
	// Valid, not-yet-pinned message but nothing inserted → channel at cap.
	mock.ExpectQuery(addPinSQL).
		WithArgs("channel", "ch-1", "ws-1", "user-1", "msg-1", storage.MaxPinsPerContainer).
		WillReturnRows(pinAddRows(true, false, false))

	err := storage.NewPGXPinStore(mock).AddPin(context.Background(), "ws-1", "channel", "ch-1", "msg-1", "user-1")
	if !errors.Is(err, domain.ErrPinLimitReached) {
		t.Fatalf("expected ErrPinLimitReached, got %v", err)
	}
	checkExpectations(t, mock)
}

func TestPGXPinStore_RemovePin_DeletesTargetScopedRow(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(`(?s)WITH readable AS.*FROM chat\.messages m.*chat\.workspace_members.*chat\.dm_members.*m\.workspace_id = \$3.*m\.id = \$5.*\(\$1 = 'dm' AND m\.dm_conversation_id = \$2 AND m\.channel_id IS NULL\).*DELETE FROM chat\.message_pins p.*USING chat\.messages m, readable.*m\.workspace_id = \$3.*p\.target_type = \$1 AND p\.target_id = \$2 AND p\.message_id = \$5.*SELECT EXISTS \(SELECT 1 FROM readable\)`).
		WithArgs("dm", "dm-1", "ws-1", "user-1", "msg-1").
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))

	if err := storage.NewPGXPinStore(mock).RemovePin(context.Background(), "ws-1", "dm", "dm-1", "msg-1", "user-1"); err != nil {
		t.Fatalf("RemovePin: %v", err)
	}
	checkExpectations(t, mock)
}

func TestPGXPinStore_RemovePin_NotReadableReturnsErrNotFound(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(`(?s)WITH readable AS.*DELETE FROM chat\.message_pins p.*USING chat\.messages m, readable.*m\.workspace_id = \$3.*p\.target_type = \$1 AND p\.target_id = \$2 AND p\.message_id = \$5`).
		WithArgs("channel", "ch-1", "ws-1", "user-1", "msg-1").
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(false))

	err := storage.NewPGXPinStore(mock).RemovePin(context.Background(), "ws-1", "channel", "ch-1", "msg-1", "user-1")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	checkExpectations(t, mock)
}

func TestPGXPinStore_ListPins_ReturnsTotalAndPins(t *testing.T) {
	mock := newMock(t)
	now := time.Now()
	pinnedAt := now.Add(time.Minute)

	mock.ExpectQuery(`(?s)WITH target_access AS.*chat\.workspace_members.*chat\.dm_members.*authorized_pins AS.*FROM chat\.message_pins p.*JOIN chat\.messages m.*chat\.workspace_members.*chat\.dm_members.*m\.workspace_id = \$3.*p\.target_type = \$1 AND p\.target_id = \$2.*\(\$1 = 'dm' AND m\.dm_conversation_id = \$2 AND m\.channel_id IS NULL\).*count\(\*\) OVER\(\).*LIMIT \$5.*UNION ALL`).
		WithArgs("dm", "dm-1", "ws-1", "user-1", storage.MaxPinsPerContainer).
		WillReturnRows(pgxmock.NewRows(pinListCols()).
			AddRow(pinListRow(true, true, listMessageRow("msg-1", "ws-1", "", "dm-1", now), pinnedAt, "user-2", 1)...))

	got, err := storage.NewPGXPinStore(mock).ListPins(context.Background(), "ws-1", "dm", "dm-1", "user-1")
	if err != nil {
		t.Fatalf("ListPins: %v", err)
	}
	if got.TotalCount != 1 || len(got.Pins) != 1 {
		t.Fatalf("expected one total and one pin, got total=%d len=%d", got.TotalCount, len(got.Pins))
	}
	if got.Pins[0].Message.ID != "msg-1" || got.Pins[0].Message.DMConversationID != "dm-1" {
		t.Fatalf("unexpected message: %+v", got.Pins[0].Message)
	}
	if !got.Pins[0].PinnedAt.Equal(pinnedAt) || got.Pins[0].PinnedByUserID != "user-2" {
		t.Fatalf("unexpected pin metadata: %+v", got.Pins[0])
	}
	checkExpectations(t, mock)
}

func TestPGXPinStore_ListPins_NotReadableReturnsErrNotFound(t *testing.T) {
	mock := newMock(t)
	now := time.Now()
	mock.ExpectQuery(`(?s)WITH target_access AS.*authorized_pins AS.*UNION ALL`).
		WithArgs("dm", "dm-1", "ws-1", "user-1", storage.MaxPinsPerContainer).
		WillReturnRows(pgxmock.NewRows(pinListCols()).
			AddRow(pinListRow(false, false, listMessageRow("", "", "", "", now), now, "", 0)...))

	_, err := storage.NewPGXPinStore(mock).ListPins(context.Background(), "ws-1", "dm", "dm-1", "user-1")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	checkExpectations(t, mock)
}

func TestPGXPinStore_ListPins_ReadableEmptyReturnsEmpty(t *testing.T) {
	mock := newMock(t)
	now := time.Now()
	mock.ExpectQuery(`(?s)WITH target_access AS.*authorized_pins AS.*UNION ALL`).
		WithArgs("channel", "ch-1", "ws-1", "user-1", storage.MaxPinsPerContainer).
		WillReturnRows(pgxmock.NewRows(pinListCols()).
			AddRow(pinListRow(true, false, listMessageRow("", "", "", "", now), now, "", 0)...))

	got, err := storage.NewPGXPinStore(mock).ListPins(context.Background(), "ws-1", "channel", "ch-1", "user-1")
	if err != nil {
		t.Fatalf("ListPins: %v", err)
	}
	if got.TotalCount != 0 || len(got.Pins) != 0 {
		t.Fatalf("expected empty readable pins, got total=%d len=%d", got.TotalCount, len(got.Pins))
	}
	checkExpectations(t, mock)
}
