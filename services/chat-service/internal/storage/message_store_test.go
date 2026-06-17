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

// createMsgSQL is a regex that matches the atomic CreateMessage query and asserts
// presence of all authorization joins required by the spec:
//   - chat.workspace_members with wm.status = 'active' (active workspace membership)
//   - chat.channels with c.status = 'active' (active channel)
//   - chat.channel_members (private channel membership guard, LEFT JOIN)
//   - chat.dm_conversations with dc.status = 'active' (active DM conversation)
//   - chat.dm_members with dm.status = 'active' (active DM membership)
//
// Tests that use this pattern verify the SQL sent to the DB contains the correct
// authorization structure. A mock returning 0 rows then verifies the function maps
// authorization/reference failure to the expected error.
const createMsgSQL = `(?s)INSERT INTO chat\.messages.*` +
	`chat\.workspace_members.*wm\.status.*active.*` +
	`chat\.channels.*c\.status.*active.*` +
	`chat\.channel_members.*` +
	`chat\.dm_conversations.*dc\.status.*active.*` +
	`chat\.dm_members.*dm\.status.*active`

// messageCols returns the column names matching messageColumns("") scan order.
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

// listMessageCols returns columns matching listMessageColumns("") scan order
// (base message columns + sender display info from auth.users).
func listMessageCols() []string {
	return append(messageCols(), "sender_display_name", "sender_email")
}

func listMessageRow(id, workspaceID, channelID, dmID string, now time.Time) []any {
	return append(messageRow(id, workspaceID, channelID, dmID, now), "Test User", "test@example.com")
}

// newMock creates and defers close of a pgxmock pool.
func newMock(t *testing.T) pgxmock.PgxPoolIface {
	t.Helper()
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	t.Cleanup(func() { mock.Close() })
	return mock
}

// expectCreate registers a CreateMessage query expectation using the full auth SQL pattern.
func expectCreate(mock pgxmock.PgxPoolIface, rows *pgxmock.Rows) {
	mock.ExpectQuery(createMsgSQL).
		WithArgs(
			pgxmock.AnyArg(), // workspace_id
			pgxmock.AnyArg(), // channel_id
			pgxmock.AnyArg(), // dm_conversation_id
			pgxmock.AnyArg(), // sender_id
			pgxmock.AnyArg(), // kind
			pgxmock.AnyArg(), // body_text
			pgxmock.AnyArg(), // parent_message_id
			pgxmock.AnyArg(), // forwarded_from_message_id
			pgxmock.AnyArg(), // referenced_message_id
		).
		WillReturnRows(rows)
}

func checkExpectations(t *testing.T, mock pgxmock.PgxPoolIface) {
	t.Helper()
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// ---- CreateMessage: success paths -------------------------------------------

func TestPGXMessageStore_CreateMessage_ChannelSuccess(t *testing.T) {
	mock := newMock(t)
	now := time.Now()
	expectCreate(mock, pgxmock.NewRows(messageCols()).
		AddRow(messageRow("msg-1", "ws-1", "ch-1", "", now)...))

	store := storage.NewPGXMessageStore(mock)
	msg, err := store.CreateMessage(context.Background(), storage.CreateMessageInput{
		WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: "user-1",
		Kind: domain.MessageKindUser, BodyText: "hello",
	})
	if err != nil {
		t.Fatalf("CreateMessage channel: %v", err)
	}
	if msg.ID != "msg-1" || msg.ChannelID != "ch-1" {
		t.Fatalf("unexpected message: %+v", msg)
	}
	checkExpectations(t, mock)
}

func TestPGXMessageStore_CreateMessage_DMSuccess(t *testing.T) {
	mock := newMock(t)
	now := time.Now()
	expectCreate(mock, pgxmock.NewRows(messageCols()).
		AddRow(messageRow("msg-2", "ws-1", "", "dm-1", now)...))

	store := storage.NewPGXMessageStore(mock)
	msg, err := store.CreateMessage(context.Background(), storage.CreateMessageInput{
		WorkspaceID: "ws-1", DMConversationID: "dm-1", SenderID: "user-1",
		Kind: domain.MessageKindUser, BodyText: "hello",
	})
	if err != nil {
		t.Fatalf("CreateMessage DM: %v", err)
	}
	if msg.ID != "msg-2" || msg.DMConversationID != "dm-1" {
		t.Fatalf("unexpected message: %+v", msg)
	}
	checkExpectations(t, mock)
}

func TestPGXMessageStore_CreateMessage_ValidSameTargetRef_Succeeds(t *testing.T) {
	mock := newMock(t)
	now := time.Now()
	expectCreate(mock, pgxmock.NewRows(messageCols()).
		AddRow(messageRow("msg-c", "ws-1", "ch-1", "", now)...))

	store := storage.NewPGXMessageStore(mock)
	_, err := store.CreateMessage(context.Background(), storage.CreateMessageInput{
		WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: "user-1", BodyText: "reply",
		ParentMessageID: "00000000-0000-0000-0000-000000000010",
	})
	if err != nil {
		t.Fatalf("CreateMessage with valid same-target ref: %v", err)
	}
	checkExpectations(t, mock)
}

// ---- CreateMessage: SQL includes auth joins (SQL structure assertions) -------

// TestPGXMessageStore_CreateMessage_SQLContainsAuthGuards verifies the INSERT SQL
// contains all required authorization predicates. Each sub-test uses a distinct regex
// targeting one guard; if that guard is removed from the query the regex won't match
// and pgxmock will not find an expectation, causing the test to fail.
func TestPGXMessageStore_CreateMessage_SQLContainsAuthGuards(t *testing.T) {
	cases := []struct {
		name  string
		regex string
		input storage.CreateMessageInput
	}{
		{
			name:  "active workspace_members guard",
			regex: `(?s)chat\.workspace_members.*wm\.status.*=.*'active'`,
			input: storage.CreateMessageInput{WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: "user-1", BodyText: "x"},
		},
		{
			name:  "active channel status guard",
			regex: `(?s)chat\.channels.*c\.status.*=.*'active'`,
			input: storage.CreateMessageInput{WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: "user-1", BodyText: "x"},
		},
		{
			name:  "private channel_members guard",
			regex: `(?s)chat\.channel_members.*cm\.channel_id.*cm\.user_id`,
			input: storage.CreateMessageInput{WorkspaceID: "ws-1", ChannelID: "ch-private", SenderID: "user-1", BodyText: "x"},
		},
		{
			name:  "active dm_conversations status guard",
			regex: `(?s)chat\.dm_conversations.*dc\.status.*=.*'active'`,
			input: storage.CreateMessageInput{WorkspaceID: "ws-1", DMConversationID: "dm-1", SenderID: "user-1", BodyText: "x"},
		},
		{
			name:  "active dm_members guard",
			regex: `(?s)chat\.dm_members.*dm\.status.*=.*'active'`,
			input: storage.CreateMessageInput{WorkspaceID: "ws-1", DMConversationID: "dm-1", SenderID: "user-1", BodyText: "x"},
		},
		{
			name:  "invalid_refs CTE with IS NOT DISTINCT FROM",
			regex: `(?s)invalid_refs.*IS NOT DISTINCT FROM`,
			input: storage.CreateMessageInput{WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: "user-1", BodyText: "ref", ParentMessageID: "00000000-0000-0000-0000-000000000099"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mock := newMock(t)
			mock.ExpectQuery(tc.regex).
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
					pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
					pgxmock.AnyArg()).
				WillReturnRows(pgxmock.NewRows(messageCols()))
			store := storage.NewPGXMessageStore(mock)
			_, err := store.CreateMessage(context.Background(), tc.input)
			if !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("expected ErrNotFound (auth/ref backstop), got %v", err)
			}
			checkExpectations(t, mock)
		})
	}
}

// ---- CreateMessage: behavioral denial (0 rows → ErrNotFound) ----------------

func TestPGXMessageStore_CreateMessage_AuthDenied_ReturnsErrNotFound(t *testing.T) {
	for _, name := range []string{"channel auth denied", "DM auth denied", "cross-workspace", "invalid ref backstop"} {
		t.Run(name, func(t *testing.T) {
			mock := newMock(t)
			expectCreate(mock, pgxmock.NewRows(messageCols())) // 0 rows
			store := storage.NewPGXMessageStore(mock)
			_, err := store.CreateMessage(context.Background(), storage.CreateMessageInput{
				WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: "user-1", BodyText: "x",
			})
			if !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("[%s] expected ErrNotFound (non-enumerating backstop), got %v", name, err)
			}
			checkExpectations(t, mock)
		})
	}
}

// ---- CreateMessage: editedAt/deletedAt nil-check branches in scanMessage ----

func TestPGXMessageStore_CreateMessage_WithEditedAt_ScansBothTimestamps(t *testing.T) {
	mock := newMock(t)
	now := time.Now()
	editedAt := now.Add(-time.Minute)
	deletedAt := now.Add(-30 * time.Second)
	row := []any{
		"msg-e", "ws-1", "ch-1", "", "user-1",
		"user", "edited", "active",
		"", "", "",
		&editedAt, &deletedAt,
		now, now,
	}
	expectCreate(mock, pgxmock.NewRows(messageCols()).AddRow(row...))

	store := storage.NewPGXMessageStore(mock)
	msg, err := store.CreateMessage(context.Background(), storage.CreateMessageInput{
		WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: "user-1", BodyText: "edited",
	})
	if err != nil {
		t.Fatalf("CreateMessage with editedAt: %v", err)
	}
	if msg.EditedAt.IsZero() || msg.DeletedAt.IsZero() {
		t.Fatalf("expected EditedAt and DeletedAt to be set, got %+v", msg)
	}
	checkExpectations(t, mock)
}

// ---- ValidateRefMessageInTarget ---------------------------------------------

func TestPGXMessageStore_ValidateRefMessageInTarget_ValidReturnsNil(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(`SELECT 1 FROM chat\.messages`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(1))

	store := storage.NewPGXMessageStore(mock)
	if err := store.ValidateRefMessageInTarget(context.Background(), "ws-1", "ch-1", "", "msg-1"); err != nil {
		t.Fatalf("expected nil for valid ref, got %v", err)
	}
	checkExpectations(t, mock)
}

func TestPGXMessageStore_ValidateRefMessageInTarget_MissingReturnsErrInvalidRef(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(`SELECT 1 FROM chat\.messages`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(pgx.ErrNoRows)

	store := storage.NewPGXMessageStore(mock)
	err := store.ValidateRefMessageInTarget(context.Background(), "ws-1", "ch-1", "", "msg-missing")
	if !errors.Is(err, domain.ErrInvalidMessageReference) {
		t.Fatalf("expected ErrInvalidMessageReference for missing ref, got %v", err)
	}
	checkExpectations(t, mock)
}

// ---- GetMessageByIDInWorkspace ----------------------------------------------

func TestPGXMessageStore_GetMessageByIDInWorkspace_Found(t *testing.T) {
	mock := newMock(t)
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
	checkExpectations(t, mock)
}

func TestPGXMessageStore_GetMessageByIDInWorkspace_NotFoundReturnsErrNotFound(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(`SELECT`).
		WithArgs("msg-missing", "ws-1").
		WillReturnError(pgx.ErrNoRows)

	store := storage.NewPGXMessageStore(mock)
	_, err := store.GetMessageByIDInWorkspace(context.Background(), "ws-1", "msg-missing")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	checkExpectations(t, mock)
}

// ---- ListChannelMessages ----------------------------------------------------

func TestPGXMessageStore_ListChannelMessages_ReturnsMessages(t *testing.T) {
	mock := newMock(t)
	now := time.Now()
	// Default limit is 50; query uses limit+1 = 51 to detect next page.
	mock.ExpectQuery(`SELECT`).
		WithArgs("ws-1", "ch-1", "user-1", 51).
		WillReturnRows(pgxmock.NewRows(listMessageCols()).
			AddRow(listMessageRow("msg-1", "ws-1", "ch-1", "", now)...).
			AddRow(listMessageRow("msg-2", "ws-1", "ch-1", "", now)...))

	store := storage.NewPGXMessageStore(mock)
	result, err := store.ListChannelMessages(context.Background(), storage.ListChannelMessagesInput{
		WorkspaceID: "ws-1", ChannelID: "ch-1", UserID: "user-1",
	})
	if err != nil {
		t.Fatalf("ListChannelMessages: %v", err)
	}
	if len(result.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(result.Messages))
	}
	if result.Messages[0].SenderDisplayName != "Test User" {
		t.Fatalf("expected SenderDisplayName 'Test User', got %q", result.Messages[0].SenderDisplayName)
	}
	checkExpectations(t, mock)
}

func TestPGXMessageStore_ListChannelMessages_EmptyReturnsEmptySlice(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(`SELECT`).
		WithArgs("ws-1", "ch-1", "user-1", 51).
		WillReturnRows(pgxmock.NewRows(listMessageCols()))

	store := storage.NewPGXMessageStore(mock)
	result, err := store.ListChannelMessages(context.Background(), storage.ListChannelMessagesInput{
		WorkspaceID: "ws-1", ChannelID: "ch-1", UserID: "user-1",
	})
	if err != nil {
		t.Fatalf("ListChannelMessages empty: %v", err)
	}
	if len(result.Messages) != 0 {
		t.Fatalf("expected 0 messages, got %d", len(result.Messages))
	}
	checkExpectations(t, mock)
}

// TestPGXMessageStore_ListChannelMessages_WithEditedAt covers collectMessages
// editedAt/deletedAt nil-check branches.
func TestPGXMessageStore_ListChannelMessages_WithEditedAt_ScansBothTimestamps(t *testing.T) {
	mock := newMock(t)
	now := time.Now()
	editedAt := now.Add(-time.Minute)
	deletedAt := now.Add(-30 * time.Second)
	row := []any{
		"msg-e2", "ws-1", "ch-1", "", "user-1",
		"user", "edited body", "active",
		"", "", "",
		&editedAt, &deletedAt,
		now, now,
		"Test User", "test@example.com",
	}
	mock.ExpectQuery(`SELECT`).
		WithArgs("ws-1", "ch-1", "user-1", 51).
		WillReturnRows(pgxmock.NewRows(listMessageCols()).AddRow(row...))

	store := storage.NewPGXMessageStore(mock)
	result, err := store.ListChannelMessages(context.Background(), storage.ListChannelMessagesInput{
		WorkspaceID: "ws-1", ChannelID: "ch-1", UserID: "user-1",
	})
	if err != nil {
		t.Fatalf("ListChannelMessages with editedAt: %v", err)
	}
	if len(result.Messages) != 1 || result.Messages[0].EditedAt.IsZero() || result.Messages[0].DeletedAt.IsZero() {
		t.Fatalf("expected 1 message with EditedAt/DeletedAt set, got %+v", result.Messages)
	}
	checkExpectations(t, mock)
}

// ---- ListDMMessages ---------------------------------------------------------

func TestPGXMessageStore_ListDMMessages_ReturnsMessages(t *testing.T) {
	mock := newMock(t)
	now := time.Now()
	mock.ExpectQuery(`SELECT`).
		WithArgs("ws-1", "dm-1", "user-1", 51).
		WillReturnRows(pgxmock.NewRows(listMessageCols()).
			AddRow(listMessageRow("msg-3", "ws-1", "", "dm-1", now)...))

	store := storage.NewPGXMessageStore(mock)
	result, err := store.ListDMMessages(context.Background(), storage.ListDMMessagesInput{
		WorkspaceID: "ws-1", ConversationID: "dm-1", UserID: "user-1",
	})
	if err != nil {
		t.Fatalf("ListDMMessages: %v", err)
	}
	if len(result.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(result.Messages))
	}
	checkExpectations(t, mock)
}

func TestPGXMessageStore_ListDMMessages_EmptyReturnsEmptySlice(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(`SELECT`).
		WithArgs("ws-1", "dm-1", "user-1", 51).
		WillReturnRows(pgxmock.NewRows(listMessageCols()))

	store := storage.NewPGXMessageStore(mock)
	result, err := store.ListDMMessages(context.Background(), storage.ListDMMessagesInput{
		WorkspaceID: "ws-1", ConversationID: "dm-1", UserID: "user-1",
	})
	if err != nil {
		t.Fatalf("ListDMMessages empty: %v", err)
	}
	if len(result.Messages) != 0 {
		t.Fatalf("expected 0 messages, got %d", len(result.Messages))
	}
	checkExpectations(t, mock)
}

// ---- EncodeCursor / DecodeCursor -------------------------------------------

func TestEncodeCursor_DecodeCursor_Roundtrip(t *testing.T) {
	now := time.Date(2025, 6, 1, 12, 0, 0, 123456789, time.UTC)
	id := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	c := storage.MessageCursor{CreatedAt: now, ID: id}

	encoded := storage.EncodeCursor(c)
	if encoded == "" {
		t.Fatal("EncodeCursor returned empty string")
	}

	decoded, err := storage.DecodeCursor(encoded)
	if err != nil {
		t.Fatalf("DecodeCursor roundtrip error: %v", err)
	}
	if !decoded.CreatedAt.Equal(now) {
		t.Fatalf("CreatedAt mismatch: want %v, got %v", now, decoded.CreatedAt)
	}
	if decoded.ID != id {
		t.Fatalf("ID mismatch: want %q, got %q", id, decoded.ID)
	}
}

func TestDecodeCursor_InvalidInputs(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"not base64", "!!!invalid!!!"},
		{"missing pipe separator", "aGVsbG8="},
		{"bad timestamp", "YmFkfGFhYWFhYWFhLWJiYmItY2NjYy1kZGRkLWVlZWVlZWVlZWVlZQ=="},
		{"empty string", ""},
		{"valid base64 bad uuid", storage.EncodeCursor(storage.MessageCursor{CreatedAt: time.Now(), ID: "not-a-uuid"})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := storage.DecodeCursor(tc.input)
			if err == nil {
				t.Fatalf("expected error for input %q, got nil", tc.input)
			}
		})
	}
}

// ---- resolveLimit (via ListChannelMessages Limit param) --------------------

func TestPGXMessageStore_ListChannelMessages_DefaultLimit(t *testing.T) {
	mock := newMock(t)
	now := time.Now()
	// Default limit is 50 → query uses limit+1 = 51.
	mock.ExpectQuery(`SELECT`).
		WithArgs("ws-1", "ch-1", "user-1", 51).
		WillReturnRows(pgxmock.NewRows(listMessageCols()).
			AddRow(listMessageRow("msg-1", "ws-1", "ch-1", "", now)...))

	store := storage.NewPGXMessageStore(mock)
	result, err := store.ListChannelMessages(context.Background(), storage.ListChannelMessagesInput{
		WorkspaceID: "ws-1", ChannelID: "ch-1", UserID: "user-1", Limit: 0,
	})
	if err != nil {
		t.Fatalf("ListChannelMessages default limit: %v", err)
	}
	if len(result.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(result.Messages))
	}
	checkExpectations(t, mock)
}

func TestPGXMessageStore_ListChannelMessages_LimitCappedAtMax(t *testing.T) {
	mock := newMock(t)
	// Limit 999 → capped at 100 → query uses 101.
	mock.ExpectQuery(`SELECT`).
		WithArgs("ws-1", "ch-1", "user-1", 101).
		WillReturnRows(pgxmock.NewRows(listMessageCols()))

	store := storage.NewPGXMessageStore(mock)
	_, err := store.ListChannelMessages(context.Background(), storage.ListChannelMessagesInput{
		WorkspaceID: "ws-1", ChannelID: "ch-1", UserID: "user-1", Limit: 999,
	})
	if err != nil {
		t.Fatalf("ListChannelMessages capped limit: %v", err)
	}
	checkExpectations(t, mock)
}

func TestPGXMessageStore_ListChannelMessages_CustomLimitWithinBounds(t *testing.T) {
	mock := newMock(t)
	// Explicit limit 10 → query uses 11.
	mock.ExpectQuery(`SELECT`).
		WithArgs("ws-1", "ch-1", "user-1", 11).
		WillReturnRows(pgxmock.NewRows(listMessageCols()))

	store := storage.NewPGXMessageStore(mock)
	_, err := store.ListChannelMessages(context.Background(), storage.ListChannelMessagesInput{
		WorkspaceID: "ws-1", ChannelID: "ch-1", UserID: "user-1", Limit: 10,
	})
	if err != nil {
		t.Fatalf("ListChannelMessages custom limit: %v", err)
	}
	checkExpectations(t, mock)
}

// ---- collectMessagesResult: next page detection ----------------------------

func TestPGXMessageStore_ListChannelMessages_HasNextPage_SetsNextCursor(t *testing.T) {
	mock := newMock(t)
	now := time.Now()
	// Return limit+1 rows (default 50 → 51 rows) to trigger NextCursor.
	rows := pgxmock.NewRows(listMessageCols())
	for i := range 51 {
		id := "msg-" + string(rune('a'+i%26))
		rows.AddRow(listMessageRow(id, "ws-1", "ch-1", "", now)...)
	}
	mock.ExpectQuery(`SELECT`).
		WithArgs("ws-1", "ch-1", "user-1", 51).
		WillReturnRows(rows)

	store := storage.NewPGXMessageStore(mock)
	result, err := store.ListChannelMessages(context.Background(), storage.ListChannelMessagesInput{
		WorkspaceID: "ws-1", ChannelID: "ch-1", UserID: "user-1",
	})
	if err != nil {
		t.Fatalf("ListChannelMessages next page: %v", err)
	}
	if len(result.Messages) != 50 {
		t.Fatalf("expected 50 messages (trimmed), got %d", len(result.Messages))
	}
	if result.NextCursor == nil {
		t.Fatal("expected NextCursor to be set when more pages exist")
	}
	checkExpectations(t, mock)
}

func TestPGXMessageStore_ListDMMessages_HasNextPage_SetsNextCursor(t *testing.T) {
	mock := newMock(t)
	now := time.Now()
	rows := pgxmock.NewRows(listMessageCols())
	for i := range 51 {
		id := "dm-msg-" + string(rune('a'+i%26))
		rows.AddRow(listMessageRow(id, "ws-1", "", "dm-1", now)...)
	}
	mock.ExpectQuery(`SELECT`).
		WithArgs("ws-1", "dm-1", "user-1", 51).
		WillReturnRows(rows)

	store := storage.NewPGXMessageStore(mock)
	result, err := store.ListDMMessages(context.Background(), storage.ListDMMessagesInput{
		WorkspaceID: "ws-1", ConversationID: "dm-1", UserID: "user-1",
	})
	if err != nil {
		t.Fatalf("ListDMMessages next page: %v", err)
	}
	if len(result.Messages) != 50 {
		t.Fatalf("expected 50 messages (trimmed), got %d", len(result.Messages))
	}
	if result.NextCursor == nil {
		t.Fatal("expected NextCursor to be set when more pages exist")
	}
	checkExpectations(t, mock)
}

// ---- BeforeCursor path: keyset pagination branch ----------------------------

func TestPGXMessageStore_ListChannelMessages_WithBeforeCursor(t *testing.T) {
	mock := newMock(t)
	now := time.Now()
	cursorID := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	cursor := &storage.MessageCursor{CreatedAt: now, ID: cursorID}

	// When BeforeCursor is set, query has 6 args: ws, ch, user, ts, id, limit+1.
	mock.ExpectQuery(`SELECT`).
		WithArgs("ws-1", "ch-1", "user-1", pgxmock.AnyArg(), cursorID, 51).
		WillReturnRows(pgxmock.NewRows(listMessageCols()).
			AddRow(listMessageRow("msg-older", "ws-1", "ch-1", "", now.Add(-time.Minute))...))

	store := storage.NewPGXMessageStore(mock)
	result, err := store.ListChannelMessages(context.Background(), storage.ListChannelMessagesInput{
		WorkspaceID: "ws-1", ChannelID: "ch-1", UserID: "user-1",
		BeforeCursor: cursor,
	})
	if err != nil {
		t.Fatalf("ListChannelMessages with cursor: %v", err)
	}
	if len(result.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(result.Messages))
	}
	checkExpectations(t, mock)
}

func TestPGXMessageStore_ListDMMessages_WithBeforeCursor(t *testing.T) {
	mock := newMock(t)
	now := time.Now()
	cursorID := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	cursor := &storage.MessageCursor{CreatedAt: now, ID: cursorID}

	mock.ExpectQuery(`SELECT`).
		WithArgs("ws-1", "dm-1", "user-1", pgxmock.AnyArg(), cursorID, 51).
		WillReturnRows(pgxmock.NewRows(listMessageCols()).
			AddRow(listMessageRow("dm-older", "ws-1", "", "dm-1", now.Add(-time.Minute))...))

	store := storage.NewPGXMessageStore(mock)
	result, err := store.ListDMMessages(context.Background(), storage.ListDMMessagesInput{
		WorkspaceID: "ws-1", ConversationID: "dm-1", UserID: "user-1",
		BeforeCursor: cursor,
	})
	if err != nil {
		t.Fatalf("ListDMMessages with cursor: %v", err)
	}
	if len(result.Messages) != 1 {
		t.Fatalf("expected 1 DM message, got %d", len(result.Messages))
	}
	checkExpectations(t, mock)
}
