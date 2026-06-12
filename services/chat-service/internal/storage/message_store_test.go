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

// messageCols returns the column names in the order that scanMessage/collectMessages
// scans them (matching messageColumns("") and messageColumns("m") positional order).
func messageCols() []string {
	return []string{
		"id", "workspace_id",
		"channel_id", "dm_conversation_id",
		"sender_id",
		"kind", "body_text", "status",
		"parent_message_id", "forwarded_from_message_id", "referenced_message_id",
		"edited_at", "deleted_at",
		"created_at", "updated_at",
	}
}

// messageRow returns a standard message row for the given IDs.
func messageRow(id, workspaceID, channelID, dmID string, now time.Time) []any {
	return []any{
		id, workspaceID,
		channelID, dmID,
		"user-1",
		"user", "hello", "active",
		"", "", "",
		(*time.Time)(nil), (*time.Time)(nil),
		now, now,
	}
}

func TestPGXMessageStore_CreateMessage_ChannelSuccess(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	now := time.Now()
	mock.ExpectQuery(`(?s)INSERT INTO chat\.messages`).
		WithArgs(
			"ws-1",
			pgxmock.AnyArg(), // channel_id *string
			pgxmock.AnyArg(), // dm_conversation_id *string (nil)
			"user-1",
			"user",
			"hello",
			pgxmock.AnyArg(), // parent_message_id nil
			pgxmock.AnyArg(), // forwarded_from_message_id nil
			pgxmock.AnyArg(), // referenced_message_id nil
		).
		WillReturnRows(pgxmock.NewRows(messageCols()).
			AddRow(messageRow("msg-1", "ws-1", "ch-1", "", now)...))

	store := storage.NewPGXMessageStore(mock)
	msg, err := store.CreateMessage(context.Background(), storage.CreateMessageInput{
		WorkspaceID: "ws-1",
		ChannelID:   "ch-1",
		SenderID:    "user-1",
		Kind:        domain.MessageKindUser,
		BodyText:    "hello",
	})
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}
	if msg.ID != "msg-1" || msg.ChannelID != "ch-1" {
		t.Fatalf("unexpected message: %+v", msg)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXMessageStore_CreateMessage_DMSuccess(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	now := time.Now()
	mock.ExpectQuery(`(?s)INSERT INTO chat\.messages`).
		WithArgs(
			"ws-1",
			pgxmock.AnyArg(), // channel_id nil
			pgxmock.AnyArg(), // dm_conversation_id *string
			"user-1",
			"user",
			"hello",
			pgxmock.AnyArg(),
			pgxmock.AnyArg(),
			pgxmock.AnyArg(),
		).
		WillReturnRows(pgxmock.NewRows(messageCols()).
			AddRow(messageRow("msg-2", "ws-1", "", "dm-1", now)...))

	store := storage.NewPGXMessageStore(mock)
	msg, err := store.CreateMessage(context.Background(), storage.CreateMessageInput{
		WorkspaceID:      "ws-1",
		DMConversationID: "dm-1",
		SenderID:         "user-1",
		Kind:             domain.MessageKindUser,
		BodyText:         "hello",
	})
	if err != nil {
		t.Fatalf("CreateMessage DM: %v", err)
	}
	if msg.ID != "msg-2" || msg.DMConversationID != "dm-1" {
		t.Fatalf("unexpected message: %+v", msg)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXMessageStore_CreateMessage_InvalidParentRef_ReturnsErrInvalidMessageReference(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	// CTE condition fails → INSERT selects 0 rows → ErrNoRows
	mock.ExpectQuery(`(?s)INSERT INTO chat\.messages`).
		WithArgs(
			"ws-1",
			pgxmock.AnyArg(),
			pgxmock.AnyArg(),
			"user-1",
			"user",
			"reply",
			pgxmock.AnyArg(), // non-nil parent ref that fails CTE check
			pgxmock.AnyArg(),
			pgxmock.AnyArg(),
		).
		WillReturnRows(pgxmock.NewRows(messageCols())) // 0 rows

	store := storage.NewPGXMessageStore(mock)
	_, err = store.CreateMessage(context.Background(), storage.CreateMessageInput{
		WorkspaceID:     "ws-1",
		ChannelID:       "ch-1",
		SenderID:        "user-1",
		Kind:            domain.MessageKindUser,
		BodyText:        "reply",
		ParentMessageID: "00000000-0000-0000-0000-000000000001",
	})
	if !errors.Is(err, domain.ErrInvalidMessageReference) {
		t.Fatalf("expected ErrInvalidMessageReference for invalid parent ref, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXMessageStore_CreateMessage_CrossWorkspaceRef_ReturnsErrInvalidMessageReference(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`(?s)INSERT INTO chat\.messages`).
		WithArgs(
			"ws-1",
			pgxmock.AnyArg(),
			pgxmock.AnyArg(),
			"user-1",
			"user",
			"reply",
			pgxmock.AnyArg(), // ref from ws-2
			pgxmock.AnyArg(),
			pgxmock.AnyArg(),
		).
		WillReturnRows(pgxmock.NewRows(messageCols())) // 0 rows: workspace mismatch

	store := storage.NewPGXMessageStore(mock)
	_, err = store.CreateMessage(context.Background(), storage.CreateMessageInput{
		WorkspaceID:     "ws-1",
		ChannelID:       "ch-1",
		SenderID:        "user-1",
		BodyText:        "reply",
		ParentMessageID: "00000000-0000-0000-0000-000000000099",
	})
	if !errors.Is(err, domain.ErrInvalidMessageReference) {
		t.Fatalf("expected ErrInvalidMessageReference for cross-workspace ref, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXMessageStore_CreateMessage_CrossChannelRef_ReturnsErrInvalidMessageReference(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`(?s)INSERT INTO chat\.messages`).
		WithArgs(
			"ws-1",
			pgxmock.AnyArg(),
			pgxmock.AnyArg(),
			"user-1",
			"user",
			"reply",
			pgxmock.AnyArg(), // ref in ch-other, not ch-1
			pgxmock.AnyArg(),
			pgxmock.AnyArg(),
		).
		WillReturnRows(pgxmock.NewRows(messageCols())) // 0 rows: channel mismatch

	store := storage.NewPGXMessageStore(mock)
	_, err = store.CreateMessage(context.Background(), storage.CreateMessageInput{
		WorkspaceID:     "ws-1",
		ChannelID:       "ch-1",
		SenderID:        "user-1",
		BodyText:        "reply",
		ParentMessageID: "00000000-0000-0000-0000-000000000002",
	})
	if !errors.Is(err, domain.ErrInvalidMessageReference) {
		t.Fatalf("expected ErrInvalidMessageReference for cross-channel ref, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXMessageStore_CreateMessage_ChannelToDMRef_ReturnsErrInvalidMessageReference(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`(?s)INSERT INTO chat\.messages`).
		WithArgs(
			"ws-1",
			pgxmock.AnyArg(),
			pgxmock.AnyArg(),
			"user-1",
			"user",
			"reply",
			pgxmock.AnyArg(), // ref is in a DM, not in ch-1
			pgxmock.AnyArg(),
			pgxmock.AnyArg(),
		).
		WillReturnRows(pgxmock.NewRows(messageCols())) // 0 rows: target mismatch

	store := storage.NewPGXMessageStore(mock)
	_, err = store.CreateMessage(context.Background(), storage.CreateMessageInput{
		WorkspaceID:     "ws-1",
		ChannelID:       "ch-1",
		SenderID:        "user-1",
		BodyText:        "reply",
		ParentMessageID: "00000000-0000-0000-0000-000000000003",
	})
	if !errors.Is(err, domain.ErrInvalidMessageReference) {
		t.Fatalf("expected ErrInvalidMessageReference for channel-to-DM ref, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXMessageStore_CreateMessage_DMToChannelRef_ReturnsErrInvalidMessageReference(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`(?s)INSERT INTO chat\.messages`).
		WithArgs(
			"ws-1",
			pgxmock.AnyArg(),
			pgxmock.AnyArg(),
			"user-1",
			"user",
			"reply",
			pgxmock.AnyArg(), // ref is in a channel, not in dm-1
			pgxmock.AnyArg(),
			pgxmock.AnyArg(),
		).
		WillReturnRows(pgxmock.NewRows(messageCols())) // 0 rows: target mismatch

	store := storage.NewPGXMessageStore(mock)
	_, err = store.CreateMessage(context.Background(), storage.CreateMessageInput{
		WorkspaceID:      "ws-1",
		DMConversationID: "dm-1",
		SenderID:         "user-1",
		BodyText:         "reply",
		ParentMessageID:  "00000000-0000-0000-0000-000000000004",
	})
	if !errors.Is(err, domain.ErrInvalidMessageReference) {
		t.Fatalf("expected ErrInvalidMessageReference for DM-to-channel ref, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXMessageStore_CreateMessage_ValidSameTargetRef_Succeeds(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	now := time.Now()
	mock.ExpectQuery(`(?s)INSERT INTO chat\.messages`).
		WithArgs(
			"ws-1",
			pgxmock.AnyArg(),
			pgxmock.AnyArg(),
			"user-1",
			"user",
			"reply",
			pgxmock.AnyArg(), // valid parent same target
			pgxmock.AnyArg(),
			pgxmock.AnyArg(),
		).
		WillReturnRows(pgxmock.NewRows(messageCols()).
			AddRow(messageRow("msg-child", "ws-1", "ch-1", "", now)...))

	store := storage.NewPGXMessageStore(mock)
	msg, err := store.CreateMessage(context.Background(), storage.CreateMessageInput{
		WorkspaceID:     "ws-1",
		ChannelID:       "ch-1",
		SenderID:        "user-1",
		BodyText:        "reply",
		ParentMessageID: "00000000-0000-0000-0000-000000000010",
	})
	if err != nil {
		t.Fatalf("CreateMessage with valid same-target ref: %v", err)
	}
	if msg.ID != "msg-child" {
		t.Fatalf("unexpected id: %q", msg.ID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXMessageStore_ValidateRefMessageInTarget_ValidReturnsNil(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`SELECT 1 FROM chat\.messages`).
		WithArgs(
			"msg-1",
			"ws-1",
			pgxmock.AnyArg(), // channel_id
			pgxmock.AnyArg(), // dm_conversation_id nil
		).
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(1))

	store := storage.NewPGXMessageStore(mock)
	err = store.ValidateRefMessageInTarget(context.Background(), "ws-1", "ch-1", "", "msg-1")
	if err != nil {
		t.Fatalf("expected nil for valid ref, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXMessageStore_ValidateRefMessageInTarget_MissingReturnsErrInvalidMessageReference(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`SELECT 1 FROM chat\.messages`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(pgx.ErrNoRows)

	store := storage.NewPGXMessageStore(mock)
	err = store.ValidateRefMessageInTarget(context.Background(), "ws-1", "ch-1", "", "msg-missing")
	if !errors.Is(err, domain.ErrInvalidMessageReference) {
		t.Fatalf("expected ErrInvalidMessageReference for missing ref, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXMessageStore_GetMessageByIDInWorkspace_Found(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	now := time.Now()
	mock.ExpectQuery(`SELECT`).
		WithArgs("msg-1", "ws-1").
		WillReturnRows(pgxmock.NewRows(messageCols()).
			AddRow(messageRow("msg-1", "ws-1", "ch-1", "", now)...))

	store := storage.NewPGXMessageStore(mock)
	msg, err := store.GetMessageByIDInWorkspace(context.Background(), "ws-1", "msg-1")
	if err != nil {
		t.Fatalf("GetMessageByIDInWorkspace: %v", err)
	}
	if msg.ID != "msg-1" {
		t.Fatalf("expected msg-1, got %q", msg.ID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXMessageStore_GetMessageByIDInWorkspace_NotFoundReturnsErrNotFound(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`SELECT`).
		WithArgs("msg-missing", "ws-1").
		WillReturnError(pgx.ErrNoRows)

	store := storage.NewPGXMessageStore(mock)
	_, err = store.GetMessageByIDInWorkspace(context.Background(), "ws-1", "msg-missing")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXMessageStore_ListChannelMessages_ReturnsMessages(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	now := time.Now()
	mock.ExpectQuery(`SELECT`).
		WithArgs("ws-1", "ch-1", "user-1").
		WillReturnRows(pgxmock.NewRows(messageCols()).
			AddRow(messageRow("msg-1", "ws-1", "ch-1", "", now)...).
			AddRow(messageRow("msg-2", "ws-1", "ch-1", "", now)...))

	store := storage.NewPGXMessageStore(mock)
	messages, err := store.ListChannelMessages(context.Background(), storage.ListChannelMessagesInput{
		WorkspaceID: "ws-1",
		ChannelID:   "ch-1",
		UserID:      "user-1",
	})
	if err != nil {
		t.Fatalf("ListChannelMessages: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(messages))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXMessageStore_ListChannelMessages_EmptyReturnsEmptySlice(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`SELECT`).
		WithArgs("ws-1", "ch-1", "user-1").
		WillReturnRows(pgxmock.NewRows(messageCols()))

	store := storage.NewPGXMessageStore(mock)
	messages, err := store.ListChannelMessages(context.Background(), storage.ListChannelMessagesInput{
		WorkspaceID: "ws-1",
		ChannelID:   "ch-1",
		UserID:      "user-1",
	})
	if err != nil {
		t.Fatalf("ListChannelMessages empty: %v", err)
	}
	if len(messages) != 0 {
		t.Fatalf("expected 0 messages, got %d", len(messages))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXMessageStore_ListDMMessages_ReturnsMessages(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	now := time.Now()
	mock.ExpectQuery(`SELECT`).
		WithArgs("ws-1", "dm-1", "user-1").
		WillReturnRows(pgxmock.NewRows(messageCols()).
			AddRow(messageRow("msg-3", "ws-1", "", "dm-1", now)...))

	store := storage.NewPGXMessageStore(mock)
	messages, err := store.ListDMMessages(context.Background(), storage.ListDMMessagesInput{
		WorkspaceID:    "ws-1",
		ConversationID: "dm-1",
		UserID:         "user-1",
	})
	if err != nil {
		t.Fatalf("ListDMMessages: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(messages))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXMessageStore_ListDMMessages_EmptyReturnsEmptySlice(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`SELECT`).
		WithArgs("ws-1", "dm-1", "user-1").
		WillReturnRows(pgxmock.NewRows(messageCols()))

	store := storage.NewPGXMessageStore(mock)
	messages, err := store.ListDMMessages(context.Background(), storage.ListDMMessagesInput{
		WorkspaceID:    "ws-1",
		ConversationID: "dm-1",
		UserID:         "user-1",
	})
	if err != nil {
		t.Fatalf("ListDMMessages empty: %v", err)
	}
	if len(messages) != 0 {
		t.Fatalf("expected 0 messages, got %d", len(messages))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// messageRowEdited returns a row with non-nil edited_at and deleted_at to exercise
// the nil-pointer branches in scanMessage and collectMessages.
func messageRowEdited(id, workspaceID, channelID string, now time.Time) []any {
	editedAt := now.Add(-time.Minute)
	deletedAt := now.Add(-30 * time.Second)
	return []any{
		id, workspaceID,
		channelID, "",
		"user-1",
		"user", "edited body", "active",
		"", "", "",
		&editedAt, &deletedAt,
		now, now,
	}
}

func TestPGXMessageStore_CreateMessage_WithEditedAt_ScansBothTimestamps(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	now := time.Now()
	mock.ExpectQuery(`(?s)INSERT INTO chat\.messages`).
		WithArgs(
			"ws-1",
			pgxmock.AnyArg(),
			pgxmock.AnyArg(),
			"user-1",
			"user",
			"edited body",
			pgxmock.AnyArg(),
			pgxmock.AnyArg(),
			pgxmock.AnyArg(),
		).
		WillReturnRows(pgxmock.NewRows(messageCols()).
			AddRow(messageRowEdited("msg-e", "ws-1", "ch-1", now)...))

	store := storage.NewPGXMessageStore(mock)
	msg, err := store.CreateMessage(context.Background(), storage.CreateMessageInput{
		WorkspaceID: "ws-1",
		ChannelID:   "ch-1",
		SenderID:    "user-1",
		Kind:        domain.MessageKindUser,
		BodyText:    "edited body",
	})
	if err != nil {
		t.Fatalf("CreateMessage with editedAt: %v", err)
	}
	if msg.EditedAt.IsZero() {
		t.Fatalf("expected EditedAt to be set, got zero")
	}
	if msg.DeletedAt.IsZero() {
		t.Fatalf("expected DeletedAt to be set, got zero")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXMessageStore_ListChannelMessages_WithEditedAt_ScansBothTimestamps(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	now := time.Now()
	mock.ExpectQuery(`SELECT`).
		WithArgs("ws-1", "ch-1", "user-1").
		WillReturnRows(pgxmock.NewRows(messageCols()).
			AddRow(messageRowEdited("msg-e2", "ws-1", "ch-1", now)...))

	store := storage.NewPGXMessageStore(mock)
	messages, err := store.ListChannelMessages(context.Background(), storage.ListChannelMessagesInput{
		WorkspaceID: "ws-1",
		ChannelID:   "ch-1",
		UserID:      "user-1",
	})
	if err != nil {
		t.Fatalf("ListChannelMessages with editedAt: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(messages))
	}
	if messages[0].EditedAt.IsZero() {
		t.Fatalf("expected EditedAt to be set")
	}
	if messages[0].DeletedAt.IsZero() {
		t.Fatalf("expected DeletedAt to be set")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}
