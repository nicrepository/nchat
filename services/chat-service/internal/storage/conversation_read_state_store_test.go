package storage_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	pgxmock "github.com/pashagolub/pgxmock/v2"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

func TestPGXConversationReadStateStore_MarkReadAuthorizesAndUpsertsChannel(t *testing.T) {
	mock := newMock(t)
	messageID := "22222222-2222-4222-8222-222222222222"
	mock.ExpectQuery(`(?s)WITH authorized AS.*FROM chat\.channels c.*channel_visible_to_user.*INSERT INTO chat\.conversation_read_state.*last_read_message_id.*ON CONFLICT \(user_id, channel_id\).*DO UPDATE.*last_read_at = EXCLUDED\.last_read_at.*SELECT EXISTS`).
		WithArgs("ws-1", "user-1", "11111111-1111-4111-8111-111111111111", &messageID).
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))

	err := storage.NewPGXConversationReadStateStore(mock).MarkRead(
		context.Background(), "ws-1", "user-1", storage.ConversationReadTargetChannel,
		"11111111-1111-4111-8111-111111111111", &messageID,
	)
	if err != nil {
		t.Fatalf("MarkRead: %v", err)
	}
	checkExpectations(t, mock)
}

func TestPGXConversationReadStateStore_MarkReadDMIsIdempotentAndNonEnumerating(t *testing.T) {
	mock := newMock(t)
	for _, allowed := range []bool{true, true, false} {
		mock.ExpectQuery(`(?s)FROM chat\.dm_conversations dc.*chat\.dm_members dm.*INSERT INTO chat\.conversation_read_state.*ON CONFLICT \(user_id, dm_conversation_id\).*DO UPDATE.*SELECT EXISTS`).
			WithArgs("ws-1", "user-1", "33333333-3333-4333-8333-333333333333", (*string)(nil)).
			WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(allowed))
	}
	store := storage.NewPGXConversationReadStateStore(mock)
	for range 2 {
		if err := store.MarkRead(context.Background(), "ws-1", "user-1", storage.ConversationReadTargetDM, "33333333-3333-4333-8333-333333333333", nil); err != nil {
			t.Fatalf("idempotent MarkRead: %v", err)
		}
	}
	if err := store.MarkRead(context.Background(), "ws-1", "user-1", storage.ConversationReadTargetDM, "33333333-3333-4333-8333-333333333333", nil); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	checkExpectations(t, mock)
}

func TestPGXConversationReadStateStore_UnreadCountsUsesAuthorizedTargetsAndExcludesOwnMessages(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(`(?s)SELECT 'channel'.*FROM chat\.messages m.*m\.status = 'active'.*m\.sender_id <> \$2.*COALESCE\(rs\.last_read_at, '-infinity'.*chat\.channel_visible_to_user.*UNION ALL.*SELECT 'dm'.*chat\.dm_members`).
		WithArgs("ws-1", "user-1").
		WillReturnRows(pgxmock.NewRows([]string{"target_type", "target_id", "unread_count"}).
			AddRow("channel", "channel-1", int64(2)).
			AddRow("dm", "dm-1", int64(1)))

	counts, err := storage.NewPGXConversationReadStateStore(mock).UnreadCounts(context.Background(), "ws-1", "user-1")
	if err != nil {
		t.Fatalf("UnreadCounts: %v", err)
	}
	if counts[storage.ConversationReadTargetChannel+"\x00channel-1"] != 2 || counts[storage.ConversationReadTargetDM+"\x00dm-1"] != 1 {
		t.Fatalf("unexpected counts: %#v", counts)
	}
	checkExpectations(t, mock)
}

func TestPGXConversationReadStateStore_RejectsInvalidTargetAndWrapsDatabaseErrors(t *testing.T) {
	store := storage.NewPGXConversationReadStateStore(newMock(t))
	if err := store.MarkRead(context.Background(), "ws-1", "user-1", "group", "group-1", nil); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}

	mock := newMock(t)
	mock.ExpectQuery(`(?s)SELECT 'channel'.*UNION ALL.*SELECT 'dm'`).WithArgs("ws-1", "user-1").WillReturnError(errors.New("database unavailable"))
	_, err := storage.NewPGXConversationReadStateStore(mock).UnreadCounts(context.Background(), "ws-1", "user-1")
	if err == nil || !strings.Contains(err.Error(), "list conversation unread counts") {
		t.Fatalf("expected wrapped database error, got %v", err)
	}
	checkExpectations(t, mock)
}
