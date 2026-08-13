package storage_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	pgxmock "github.com/pashagolub/pgxmock/v2"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

func TestPGXSidebarPinStore_PinChannelPersistsOnlyAuthorizedConversation(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(`(?s)WITH authorized AS.*FROM chat\.channels c.*workspace_id = \$1.*channel_visible_to_user.*INSERT INTO chat\.sidebar_conversation_pins \(user_id, workspace_id, channel_id\).*ON CONFLICT \(user_id, channel_id\).*SELECT EXISTS`).
		WithArgs("ws-1", "user-1", "channel-1").
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))

	if err := storage.NewPGXSidebarPinStore(mock).Pin(context.Background(), "ws-1", "user-1", storage.SidebarPinTargetChannel, "channel-1"); err != nil {
		t.Fatalf("Pin: %v", err)
	}
	checkExpectations(t, mock)
}

func TestPGXSidebarPinStore_PinDMIsIdempotent(t *testing.T) {
	mock := newMock(t)
	for range 2 {
		mock.ExpectQuery(`(?s)FROM chat\.dm_conversations dc.*chat\.dm_members dm.*INSERT INTO chat\.sidebar_conversation_pins \(user_id, workspace_id, dm_conversation_id\).*ON CONFLICT \(user_id, dm_conversation_id\).*SELECT EXISTS`).
			WithArgs("ws-1", "user-1", "dm-1").
			WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))
	}

	store := storage.NewPGXSidebarPinStore(mock)
	for range 2 {
		if err := store.Pin(context.Background(), "ws-1", "user-1", storage.SidebarPinTargetDM, "dm-1"); err != nil {
			t.Fatalf("idempotent Pin: %v", err)
		}
	}
	checkExpectations(t, mock)
}

func TestPGXSidebarPinStore_PinInaccessibleConversationDoesNotDiscloseIt(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(`(?s)WITH authorized AS.*channel_visible_to_user.*SELECT EXISTS`).
		WithArgs("ws-1", "user-1", "private-channel").
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(false))

	err := storage.NewPGXSidebarPinStore(mock).Pin(context.Background(), "ws-1", "user-1", storage.SidebarPinTargetChannel, "private-channel")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	checkExpectations(t, mock)
}

func TestPGXSidebarPinStore_PinRejectsInvalidTargetAndWrapsDatabaseFailure(t *testing.T) {
	store := storage.NewPGXSidebarPinStore(newMock(t))
	if err := store.Pin(context.Background(), "ws-1", "user-1", "group", "group-1"); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}

	mock := newMock(t)
	mock.ExpectQuery(`(?s)WITH authorized AS.*channel_visible_to_user.*SELECT EXISTS`).
		WithArgs("ws-1", "user-1", "channel-1").
		WillReturnError(errors.New("database unavailable"))
	err := storage.NewPGXSidebarPinStore(mock).Pin(context.Background(), "ws-1", "user-1", storage.SidebarPinTargetChannel, "channel-1")
	if err == nil || !strings.Contains(err.Error(), "pin sidebar conversation") {
		t.Fatalf("expected wrapped database error, got %v", err)
	}
	checkExpectations(t, mock)
}

func TestPGXSidebarPinStore_UnpinIsScopedToCurrentUserAndIdempotent(t *testing.T) {
	mock := newMock(t)
	mock.ExpectExec(`DELETE FROM chat\.sidebar_conversation_pins WHERE user_id = \$1 AND dm_conversation_id = \$2`).
		WithArgs("user-1", "dm-1").
		WillReturnResult(pgxmock.NewResult("DELETE", 0))

	if err := storage.NewPGXSidebarPinStore(mock).Unpin(context.Background(), "user-1", storage.SidebarPinTargetDM, "dm-1"); err != nil {
		t.Fatalf("Unpin: %v", err)
	}
	checkExpectations(t, mock)
}

func TestPGXSidebarPinStore_UnpinRejectsInvalidTargetAndWrapsDatabaseFailure(t *testing.T) {
	store := storage.NewPGXSidebarPinStore(newMock(t))
	if err := store.Unpin(context.Background(), "user-1", "group", "group-1"); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}

	mock := newMock(t)
	mock.ExpectExec(`DELETE FROM chat\.sidebar_conversation_pins WHERE user_id = \$1 AND channel_id = \$2`).
		WithArgs("user-1", "channel-1").
		WillReturnError(errors.New("database unavailable"))
	err := storage.NewPGXSidebarPinStore(mock).Unpin(context.Background(), "user-1", storage.SidebarPinTargetChannel, "channel-1")
	if err == nil || !strings.Contains(err.Error(), "unpin sidebar conversation") {
		t.Fatalf("expected wrapped database error, got %v", err)
	}
	checkExpectations(t, mock)
}

func TestPGXSidebarPinStore_ListVisibleFiltersByWorkspaceAndAccess(t *testing.T) {
	mock := newMock(t)
	pinnedAt := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	mock.ExpectQuery(`(?s)FROM chat\.sidebar_conversation_pins p.*p\.workspace_id = \$1.*channel_visible_to_user.*UNION ALL.*chat\.dm_members.*ORDER BY 3 ASC`).
		WithArgs("ws-1", "user-1").
		WillReturnRows(pgxmock.NewRows([]string{"target_type", "target_id", "pinned_at"}).
			AddRow("channel", "channel-1", pinnedAt).
			AddRow("dm", "group-1", pinnedAt.Add(time.Second)))

	pins, err := storage.NewPGXSidebarPinStore(mock).ListVisible(context.Background(), "ws-1", "user-1")
	if err != nil {
		t.Fatalf("ListVisible: %v", err)
	}
	if len(pins) != 2 || pins[0].TargetID != "channel-1" || pins[1].TargetType != storage.SidebarPinTargetDM {
		t.Fatalf("unexpected pins: %+v", pins)
	}
	checkExpectations(t, mock)
}

func TestPGXSidebarPinStore_ListVisibleWrapsQueryAndIterationFailures(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(`(?s)FROM chat\.sidebar_conversation_pins p.*ORDER BY 3 ASC`).
		WithArgs("ws-1", "user-1").
		WillReturnError(errors.New("database unavailable"))
	_, err := storage.NewPGXSidebarPinStore(mock).ListVisible(context.Background(), "ws-1", "user-1")
	if err == nil || !strings.Contains(err.Error(), "list visible sidebar pins") {
		t.Fatalf("expected wrapped query error, got %v", err)
	}
	checkExpectations(t, mock)

	mock = newMock(t)
	mock.ExpectQuery(`(?s)FROM chat\.sidebar_conversation_pins p.*ORDER BY 3 ASC`).
		WithArgs("ws-1", "user-1").
		WillReturnRows(pgxmock.NewRows([]string{"target_type", "target_id", "pinned_at"}).
			AddRow("channel", "channel-1", time.Now()).
			RowError(0, errors.New("row unavailable")))
	_, err = storage.NewPGXSidebarPinStore(mock).ListVisible(context.Background(), "ws-1", "user-1")
	if err == nil || !strings.Contains(err.Error(), "scan visible sidebar pin") {
		t.Fatalf("expected wrapped scan error, got %v", err)
	}
	checkExpectations(t, mock)
}
