package storage_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	pgxmock "github.com/pashagolub/pgxmock/v2"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// createMsgSQL is a regex that matches the atomic CreateMessage CTE query and
// asserts presence of all authorization joins required by the spec:
//   - chat.workspace_members with wm.status = 'active' (active workspace membership)
//   - chat.channels with c.status = 'active' (active channel)
//   - chat.channel_members (private channel membership guard, LEFT JOIN)
//   - chat.dm_conversations with dc.status = 'active' (active DM conversation)
//   - chat.dm_members with dm.status = 'active' (active DM membership)
//
// The INSERT is wrapped in a CTE (inserted AS (INSERT ... RETURNING ...)) so the
// outer SELECT can JOIN auth.users and return sender display info in one round-trip.
const createMsgSQL = `(?s)invalid_refs AS.*m\.status = 'active'.*m\.deleted_at IS NULL.*AND NOT.*` +
	`INSERT INTO chat\.messages.*` +
	`chat\.workspace_members.*wm\.status.*active.*` +
	`chat\.channels.*c\.status.*active.*` +
	// Channel read access is chat.channel_visible_to_user's answer and nothing
	// else, so the guest scope RF-74 defines applies to posting too.
	`chat\.channel_visible_to_user\(c\.id, \$4::uuid\).*` +
	`chat\.dm_conversations.*dc\.status.*active.*` +
	`chat\.dm_members.*dm\.status.*active`

// forwardMsgSQL pins the shape of the forwarding statement.
//
// The source CTE is still there and still required — it is the authorization,
// and source.id is still the provenance the new row records. What it must no
// longer do is supply the content: the body and format come from $6 and $7, the
// snapshot the caller already checked, so nothing is re-read between the check
// and the write (RF-21). A regression to source.body_text would reopen that
// window, which is why the parameters are asserted positively and the column
// reference is asserted absent by forwardMsgSQLRereadsSource below.
const forwardMsgSQL = `(?s)WITH source AS.*` +
	`m\.channel_id <>.*m\.kind = 'user'.*m\.status = 'active'.*FOR SHARE OF m.*` +
	`INSERT INTO chat\.messages.*\$6::text.*\$7::text.*source\.id.*` +
	`destination_workspace.*destination_member.*destination_channel.*` +
	`chat\.channel_visible_to_user\(destination_channel\.id, \$3::uuid\).*` +
	`ON CONFLICT.*forward_idempotency_key`

// snapshotForwardSQL pins the read: the same source-side predicates, and
// deliberately no FOR SHARE, no BEGIN — the caller runs a third-party lookup on
// the result and must not be holding a row lock while it does.
const snapshotForwardSQL = `(?s)SELECT m\.id::text, m\.body_text, m\.body_format.*` +
	`FROM chat\.messages m.*` +
	`m\.channel_id <>.*m\.kind = 'user'.*m\.status = 'active'.*m\.deleted_at IS NULL`

// messageCols returns the column names matching messageColumns("") scan order.
func messageCols() []string {
	return []string{
		"id", "workspace_id",
		"channel_id", "dm_conversation_id",
		"sender_id",
		"kind", "body_text", "body_format", "status",
		"parent_message_id", "forwarded_from_message_id", "referenced_message_id",
		"edited_at", "edit_count", "deleted_at",
		"created_at", "updated_at",
	}
}

func messageRow(id, workspaceID, channelID, dmID string, now time.Time) []any {
	return []any{
		id, workspaceID,
		channelID, dmID,
		"user-1",
		"user", "hello", "v1", "active",
		"", "", "",
		(*time.Time)(nil), 0, (*time.Time)(nil),
		now, now,
	}
}

// listMessageCols returns columns matching listMessageColumns("") scan order
// (base message columns + sender display info from auth.users).
func listMessageCols() []string {
	return append(messageCols(), "sender_display_name", "sender_email", "is_favorited")
}

func listMessageWithQuoteCols() []string {
	return append(listMessageCols(),
		"quote_id", "quote_author_id", "quote_body_text", "quote_body_format",
		"quote_status", "quote_deleted_at", "quote_created_at",
	)
}

func forwardMessageCols() []string {
	return append(listMessageWithQuoteCols(), "replayed")
}

func listMessageRow(id, workspaceID, channelID, dmID string, now time.Time) []any {
	return append(messageRow(id, workspaceID, channelID, dmID, now), "Test User", "test@example.com", false)
}

func listMessageWithQuoteRow(id, workspaceID, channelID, dmID string, now time.Time) []any {
	return append(listMessageRow(id, workspaceID, channelID, dmID, now), emptyQuoteRow()...)
}

func emptyQuoteRow() []any {
	return []any{"", "", "", "", "", (*time.Time)(nil), (*time.Time)(nil)}
}

func quoteRow(id, authorID, body, format, status string, deletedAt *time.Time, createdAt time.Time) []any {
	return []any{id, authorID, body, format, status, deletedAt, &createdAt}
}

func listMessageRowWithQuote(id, workspaceID, channelID, dmID string, now time.Time, quote []any) []any {
	row := append(messageRow(id, workspaceID, channelID, dmID, now), "Test User", "test@example.com", false)
	return append(row, quote...)
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
			pgxmock.AnyArg(), // body_format
			pgxmock.AnyArg(), // parent_message_id
			pgxmock.AnyArg(), // forwarded_from_message_id
			pgxmock.AnyArg(), // referenced_message_id
			pgxmock.AnyArg(), // mentioned_user_ids
			pgxmock.AnyArg(), // mentioned_channel_ids
			pgxmock.AnyArg(), // attachment_ids
			pgxmock.AnyArg(), // status (RF-21: active, or withheld for scanning)
			pgxmock.AnyArg(), // link_scan_urls
		).
		WillReturnRows(rows)
}

func checkExpectations(t *testing.T, mock pgxmock.PgxPoolIface) {
	t.Helper()
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func expectReactionBatch(mock pgxmock.PgxPoolIface, rows *pgxmock.Rows) {
	mock.ExpectQuery(`(?s)FROM chat\.message_reactions.*message_id = ANY`).
		WithArgs(pgxmock.AnyArg(), "user-1").
		WillReturnRows(rows)
}

func emptyReactionRows() *pgxmock.Rows {
	return pgxmock.NewRows([]string{"message_id", "emoji", "count", "reacted_by_me"})
}

// expectAttachmentBatch registers the RF-32 per-page attachment read. It runs
// once per page, right after the reaction batch, which is what makes both
// anti-N+1.
func expectAttachmentBatch(mock pgxmock.PgxPoolIface, rows *pgxmock.Rows) {
	mock.ExpectQuery(`(?s)FROM chat\.message_attachments ma.*message_id = ANY`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(rows)
}

func emptyAttachmentRows() *pgxmock.Rows {
	return pgxmock.NewRows([]string{
		"message_id", "id", "original_filename", "content_type",
		"size_bytes", "status", "preview_status",
	})
}

func TestPGXMessageStore_CreateMessageMapsAttachmentConstraintErrors(t *testing.T) {
	for name, dbErr := range map[string]*pgconn.PgError{
		"unique":      {Code: "23505"},
		"foreign key": {Code: "23503"},
		"check":       {Code: "23514"},
		"not null":    {Code: "23502"},
	} {
		t.Run(name, func(t *testing.T) {
			mock := newMock(t)
			mock.ExpectQuery(createMsgSQL).
				WithArgs(
					pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
					pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
					pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
					pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				).
				WillReturnError(dbErr)

			_, err := storage.NewPGXMessageStore(mock).CreateMessage(t.Context(), storage.CreateMessageInput{
				WorkspaceID: "ws-1", ChannelID: "channel-1", SenderID: "user-1", BodyText: "hello",
			})
			if name == "check" || name == "not null" {
				if !errors.Is(err, domain.ErrInvalidInput) {
					t.Fatalf("error = %v, want ErrInvalidInput", err)
				}
			} else if !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("error = %v, want ErrNotFound", err)
			}
			checkExpectations(t, mock)
		})
	}
}

// ---- CreateMessage: success paths -------------------------------------------

func TestPGXMessageStore_ForwardChannelMessage_SnapshotsAndPersistsProvenance(t *testing.T) {
	mock := newMock(t)
	now := time.Now()
	row := listMessageWithQuoteRow("forwarded", "ws-1", "destination", "", now)
	row[6] = "copied snapshot"
	row[7] = "v3"
	row[10] = "source"
	mock.ExpectQuery(forwardMsgSQL).
		WithArgs("ws-1", "destination", "actor", "source", "action-1", "copied snapshot", "v3",
			pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(forwardMessageCols()).AddRow(append(row, false)...))

	result, err := storage.NewPGXMessageStore(mock).ForwardChannelMessage(t.Context(), storage.ForwardChannelMessageInput{
		WorkspaceID: "ws-1", DestinationChannelID: "destination", ActorID: "actor",
		SourceMessageID: "source", IdempotencyKey: "action-1",
		BodyText: "copied snapshot", BodyFormat: domain.MessageBodyFormatV3,
	})
	if err != nil {
		t.Fatalf("ForwardChannelMessage: %v", err)
	}
	if result.Replayed || result.Message.BodyText != "copied snapshot" ||
		result.Message.BodyFormat != domain.MessageBodyFormatV3 ||
		result.Message.ForwardedFromMessageID != "source" {
		t.Fatalf("snapshot/provenance not returned: %+v", result)
	}
	checkExpectations(t, mock)
}

func TestPGXMessageStore_ForwardChannelMessage_DeniedIsNonEnumerating(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(forwardMsgSQL).
		WithArgs("ws-1", "destination", "actor", "source", "", "copied snapshot", "v3",
			pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(forwardMessageCols()))
	_, err := storage.NewPGXMessageStore(mock).ForwardChannelMessage(t.Context(), storage.ForwardChannelMessageInput{
		WorkspaceID: "ws-1", DestinationChannelID: "destination", ActorID: "actor", SourceMessageID: "source",
		BodyText: "copied snapshot", BodyFormat: domain.MessageBodyFormatV3,
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	checkExpectations(t, mock)
}

func TestPGXMessageStore_ForwardChannelMessage_IdempotentReplay(t *testing.T) {
	mock := newMock(t)
	now := time.Now()
	row := listMessageWithQuoteRow("forwarded", "ws-1", "destination", "", now)
	row[10] = "source"
	mock.ExpectQuery(forwardMsgSQL).
		WithArgs("ws-1", "destination", "actor", "source", "action-1", "copied snapshot", "v3",
			pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(forwardMessageCols()).AddRow(append(row, true)...))

	result, err := storage.NewPGXMessageStore(mock).ForwardChannelMessage(t.Context(), storage.ForwardChannelMessageInput{
		WorkspaceID: "ws-1", DestinationChannelID: "destination", ActorID: "actor",
		SourceMessageID: "source", IdempotencyKey: "action-1",
		BodyText: "copied snapshot", BodyFormat: domain.MessageBodyFormatV3,
	})

	if err != nil || !result.Replayed || result.Message.ID != "forwarded" {
		t.Fatalf("unexpected replay result: %+v err=%v", result, err)
	}
	checkExpectations(t, mock)
}

func TestPGXMessageStore_ForwardChannelMessage_IdempotencyFingerprintConflict(t *testing.T) {
	mock := newMock(t)
	now := time.Now()
	row := listMessageWithQuoteRow("forwarded", "ws-1", "destination", "", now)
	row[10] = "different-source"
	mock.ExpectQuery(forwardMsgSQL).
		WithArgs("ws-1", "destination", "actor", "source", "action-1", "copied snapshot", "v3",
			pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(forwardMessageCols()).AddRow(append(row, true)...))

	_, err := storage.NewPGXMessageStore(mock).ForwardChannelMessage(t.Context(), storage.ForwardChannelMessageInput{
		WorkspaceID: "ws-1", DestinationChannelID: "destination", ActorID: "actor",
		SourceMessageID: "source", IdempotencyKey: "action-1",
		BodyText: "copied snapshot", BodyFormat: domain.MessageBodyFormatV3,
	})

	if err != domain.ErrConflict {
		t.Fatalf("expected direct ErrConflict, got %v", err)
	}
	checkExpectations(t, mock)
}

// lookupForwardReplaySQL pins the read that resolves an idempotency key without
// writing. Two properties matter: it selects on the ON CONFLICT key of the
// forwarding statement — workspace, channel, sender, key — so it finds exactly
// the row that statement would have returned, and it is a plain SELECT with no
// INSERT and no lock, because it runs before the RF-21 provider call.
const lookupForwardReplaySQL = `(?s)SELECT.*FROM chat\.messages m.*` +
	`m\.workspace_id = \$1::uuid.*m\.channel_id = \$2::uuid.*` +
	`m\.sender_id = \$3::uuid.*m\.forward_idempotency_key = \$4.*` +
	`chat\.channel_visible_to_user\(c\.id, \$3::uuid\)`

func TestPGXMessageStore_LookupForwardReplay_ReturnsTheEarlierMessage(t *testing.T) {
	mock := newMock(t)
	now := time.Now()
	row := listMessageWithQuoteRow("forwarded", "ws-1", "destination", "", now)
	row[10] = "source"
	mock.ExpectQuery(lookupForwardReplaySQL).
		WithArgs("ws-1", "destination", "user-1", "action-1").
		WillReturnRows(pgxmock.NewRows(listMessageWithQuoteCols()).AddRow(row...))
	expectReactionBatch(mock, emptyReactionRows())
	expectAttachmentBatch(mock, emptyAttachmentRows())

	msg, err := storage.NewPGXMessageStore(mock).LookupForwardReplay(t.Context(), storage.ForwardReplayInput{
		WorkspaceID: "ws-1", DestinationChannelID: "destination", ActorID: "user-1",
		SourceMessageID: "source", IdempotencyKey: "action-1",
	})

	if err != nil || msg.ID != "forwarded" || msg.ForwardedFromMessageID != "source" {
		t.Fatalf("unexpected replay: %+v err=%v", msg, err)
	}
	checkExpectations(t, mock)
}

// Same key, different source: the conflict the forwarding statement reported
// after its upsert is now reported before it, by the same rule.
func TestPGXMessageStore_LookupForwardReplay_ConflictsOnADifferentSource(t *testing.T) {
	mock := newMock(t)
	now := time.Now()
	row := listMessageWithQuoteRow("forwarded", "ws-1", "destination", "", now)
	row[10] = "different-source"
	mock.ExpectQuery(lookupForwardReplaySQL).
		WithArgs("ws-1", "destination", "user-1", "action-1").
		WillReturnRows(pgxmock.NewRows(listMessageWithQuoteCols()).AddRow(row...))

	_, err := storage.NewPGXMessageStore(mock).LookupForwardReplay(t.Context(), storage.ForwardReplayInput{
		WorkspaceID: "ws-1", DestinationChannelID: "destination", ActorID: "user-1",
		SourceMessageID: "source", IdempotencyKey: "action-1",
	})

	if err != domain.ErrConflict {
		t.Fatalf("expected direct ErrConflict, got %v", err)
	}
	checkExpectations(t, mock)
}

// No row is "this forward has not happened", which is what lets the caller fall
// through to the check-and-insert path.
func TestPGXMessageStore_LookupForwardReplay_MissIsNotFound(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(lookupForwardReplaySQL).
		WithArgs("ws-1", "destination", "user-1", "action-1").
		WillReturnError(pgx.ErrNoRows)

	_, err := storage.NewPGXMessageStore(mock).LookupForwardReplay(t.Context(), storage.ForwardReplayInput{
		WorkspaceID: "ws-1", DestinationChannelID: "destination", ActorID: "user-1",
		SourceMessageID: "source", IdempotencyKey: "action-1",
	})

	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	checkExpectations(t, mock)
}

// Without a key there is nothing to replay and no reason to query at all.
func TestPGXMessageStore_LookupForwardReplay_NoKeyQueriesNothing(t *testing.T) {
	mock := newMock(t)

	_, err := storage.NewPGXMessageStore(mock).LookupForwardReplay(t.Context(), storage.ForwardReplayInput{
		WorkspaceID: "ws-1", DestinationChannelID: "destination", ActorID: "user-1",
		SourceMessageID: "source",
	})

	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	checkExpectations(t, mock)
}

func TestPGXMessageStore_ForwardChannelMessage_PreservesInfrastructureError(t *testing.T) {
	mock := newMock(t)
	databaseErr := errors.New("database unavailable")
	mock.ExpectQuery(forwardMsgSQL).
		WithArgs("ws-1", "destination", "actor", "source", "", "copied snapshot", "v3",
			pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(databaseErr)

	_, err := storage.NewPGXMessageStore(mock).ForwardChannelMessage(t.Context(), storage.ForwardChannelMessageInput{
		WorkspaceID: "ws-1", DestinationChannelID: "destination", ActorID: "actor", SourceMessageID: "source",
		BodyText: "copied snapshot", BodyFormat: domain.MessageBodyFormatV3,
	})

	if !errors.Is(err, databaseErr) {
		t.Fatalf("expected wrapped database error, got %v", err)
	}
	checkExpectations(t, mock)
}

func TestPGXMessageStore_CreateMessage_ChannelSuccess(t *testing.T) {
	mock := newMock(t)
	now := time.Now()
	expectCreate(mock, pgxmock.NewRows(listMessageWithQuoteCols()).
		AddRow(listMessageWithQuoteRow("msg-1", "ws-1", "ch-1", "", now)...))

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
	expectCreate(mock, pgxmock.NewRows(listMessageWithQuoteCols()).
		AddRow(listMessageWithQuoteRow("msg-2", "ws-1", "", "dm-1", now)...))

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
	expectCreate(mock, pgxmock.NewRows(listMessageWithQuoteCols()).
		AddRow(listMessageWithQuoteRow("msg-c", "ws-1", "ch-1", "", now)...))

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

func TestPGXMessageStore_CreateMessage_ReturnsQuotedParent(t *testing.T) {
	mock := newMock(t)
	now := time.Now()
	expectCreate(mock, pgxmock.NewRows(listMessageWithQuoteCols()).
		AddRow(listMessageRowWithQuote(
			"msg-child", "ws-1", "ch-1", "", now,
			quoteRow("msg-parent", "user-parent", "parent body", "v2", "active", nil, now),
		)...))

	store := storage.NewPGXMessageStore(mock)
	msg, err := store.CreateMessage(context.Background(), storage.CreateMessageInput{
		WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: "user-1", BodyText: "reply",
		ParentMessageID: "00000000-0000-0000-0000-000000000010",
	})
	if err != nil {
		t.Fatalf("CreateMessage with quote: %v", err)
	}
	if msg.Quoted == nil || msg.Quoted.ID != "msg-parent" || msg.Quoted.BodyText != "parent body" {
		t.Fatalf("quoted parent not scanned: %+v", msg.Quoted)
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
					pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
					pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
				WillReturnRows(pgxmock.NewRows(listMessageWithQuoteCols()))
			store := storage.NewPGXMessageStore(mock)
			_, err := store.CreateMessage(context.Background(), tc.input)
			if !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("expected ErrNotFound (auth/ref backstop), got %v", err)
			}
			checkExpectations(t, mock)
		})
	}
}

func TestPGXMessageStore_CreateMessage_ValidatesMentionsAndWritesDirectedOutbox(t *testing.T) {
	mock := newMock(t)
	now := time.Now()
	mock.ExpectQuery(`(?s)invalid_mentions.*chat\.channel_members.*chat\.notification_outbox`).
		WithArgs(
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(),
			[]string{"11111111-1111-1111-1111-111111111111"},
			[]string{"22222222-2222-2222-2222-222222222222"},
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		).
		WillReturnRows(pgxmock.NewRows(listMessageWithQuoteCols()).
			AddRow(listMessageWithQuoteRow("msg-mention", "ws-1", "ch-1", "", now)...))

	store := storage.NewPGXMessageStore(mock)
	msg, err := store.CreateMessage(context.Background(), storage.CreateMessageInput{
		WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: "user-1",
		BodyText: "mention", BodyFormat: domain.MessageBodyFormatV3,
		MentionedUserIDs:    []string{"11111111-1111-1111-1111-111111111111"},
		MentionedChannelIDs: []string{"22222222-2222-2222-2222-222222222222"},
	})
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}
	if msg.ID != "msg-mention" {
		t.Fatalf("unexpected message: %+v", msg)
	}
	checkExpectations(t, mock)
}

func TestPGXMessageStore_CreateMessage_UserOutsideChannelIsRejected(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(`(?s)invalid_mentions.*chat\.channel_members.*NOT EXISTS \(SELECT 1 FROM invalid_mentions\)`).
		WithArgs(
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(),
			[]string{"99999999-9999-9999-9999-999999999999"},
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		).
		WillReturnRows(pgxmock.NewRows(listMessageWithQuoteCols()))

	_, err := storage.NewPGXMessageStore(mock).CreateMessage(
		context.Background(),
		storage.CreateMessageInput{
			WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: "user-1",
			BodyText: "mention", BodyFormat: domain.MessageBodyFormatV3,
			MentionedUserIDs: []string{"99999999-9999-9999-9999-999999999999"},
		},
	)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("user outside channel must reject the message, got %v", err)
	}
	checkExpectations(t, mock)
}

func TestPGXMessageStore_ResolveMentionLabels(t *testing.T) {
	t.Run("empty IDs skip database", func(t *testing.T) {
		labels, err := storage.NewPGXMessageStore(newMock(t)).ResolveMentionLabels(
			context.Background(), "ws-1", nil, nil,
		)
		if err != nil || len(labels) != 0 {
			t.Fatalf("labels=%v err=%v", labels, err)
		}
	})

	t.Run("returns current scoped labels", func(t *testing.T) {
		mock := newMock(t)
		mock.ExpectQuery(`(?s)SELECT 'user'.*chat\.workspace_members.*UNION ALL.*chat\.channels`).
			WithArgs("ws-1", []string{"user-1"}, []string{"ch-1"}).
			WillReturnRows(pgxmock.NewRows([]string{"kind", "id", "label"}).
				AddRow("user", "user-1", "Ana").
				AddRow("channel", "ch-1", "geral"))
		labels, err := storage.NewPGXMessageStore(mock).ResolveMentionLabels(
			context.Background(), "ws-1", []string{"user-1"}, []string{"ch-1"},
		)
		if err != nil || labels["user:user-1"] != "Ana" || labels["channel:ch-1"] != "geral" {
			t.Fatalf("labels=%v err=%v", labels, err)
		}
		checkExpectations(t, mock)
	})

	t.Run("query error", func(t *testing.T) {
		mock := newMock(t)
		mock.ExpectQuery(`SELECT 'user'`).WillReturnError(errors.New("db unavailable"))
		_, err := storage.NewPGXMessageStore(mock).ResolveMentionLabels(
			context.Background(), "ws-1", []string{"user-1"}, nil,
		)
		if err == nil {
			t.Fatal("expected query error")
		}
	})

	t.Run("scan error", func(t *testing.T) {
		mock := newMock(t)
		mock.ExpectQuery(`SELECT 'user'`).WillReturnRows(
			pgxmock.NewRows([]string{"kind", "id", "label"}).AddRow("user", "user-1", nil),
		)
		_, err := storage.NewPGXMessageStore(mock).ResolveMentionLabels(
			context.Background(), "ws-1", []string{"user-1"}, nil,
		)
		if err == nil {
			t.Fatal("expected scan error")
		}
	})

	t.Run("iteration error", func(t *testing.T) {
		mock := newMock(t)
		mock.ExpectQuery(`SELECT 'user'`).WillReturnRows(
			pgxmock.NewRows([]string{"kind", "id", "label"}).
				AddRow("user", "user-1", "Ana").
				RowError(0, errors.New("stream failed")),
		)
		_, err := storage.NewPGXMessageStore(mock).ResolveMentionLabels(
			context.Background(), "ws-1", []string{"user-1"}, nil,
		)
		if err == nil {
			t.Fatal("expected iteration error")
		}
	})
}

func TestPGXMessageStore_ResolveAuthorizedMentionLabels(t *testing.T) {
	t.Run("empty IDs skip database", func(t *testing.T) {
		labels, err := storage.NewPGXMessageStore(newMock(t)).ResolveAuthorizedMentionLabels(
			context.Background(), "ws-1", "ch-1", "requester-1", nil, nil,
		)
		if err != nil || len(labels) != 0 {
			t.Fatalf("labels=%v err=%v", labels, err)
		}
	})

	t.Run("returns only channel members and visible channels", func(t *testing.T) {
		mock := newMock(t)
		mock.ExpectQuery(`(?s)chat\.channel_members.*chat\.workspace_members.*auth\.users.*UNION ALL.*chat\.workspaces.*channel_visible_to_user`).
			WithArgs("ws-1", "ch-1", "requester-1", []string{"user-1"}, []string{"ch-2"}).
			WillReturnRows(pgxmock.NewRows([]string{"kind", "id", "label"}).
				AddRow("user", "user-1", "Ana").
				AddRow("channel", "ch-2", "produto"))

		labels, err := storage.NewPGXMessageStore(mock).ResolveAuthorizedMentionLabels(
			context.Background(), "ws-1", "ch-1", "requester-1",
			[]string{"user-1"}, []string{"ch-2"},
		)
		if err != nil || labels["user:user-1"] != "Ana" || labels["channel:ch-2"] != "produto" {
			t.Fatalf("labels=%v err=%v", labels, err)
		}
		checkExpectations(t, mock)
	})

	t.Run("query error", func(t *testing.T) {
		mock := newMock(t)
		mock.ExpectQuery(`SELECT 'user'`).WillReturnError(errors.New("db unavailable"))
		_, err := storage.NewPGXMessageStore(mock).ResolveAuthorizedMentionLabels(
			context.Background(), "ws-1", "ch-1", "requester-1", []string{"user-1"}, nil,
		)
		if err == nil {
			t.Fatal("expected query error")
		}
	})

	t.Run("scan error", func(t *testing.T) {
		mock := newMock(t)
		mock.ExpectQuery(`SELECT 'user'`).WillReturnRows(
			pgxmock.NewRows([]string{"kind", "id", "label"}).AddRow("user", "user-1", nil),
		)
		_, err := storage.NewPGXMessageStore(mock).ResolveAuthorizedMentionLabels(
			context.Background(), "ws-1", "ch-1", "requester-1", []string{"user-1"}, nil,
		)
		if err == nil {
			t.Fatal("expected scan error")
		}
	})

	t.Run("iteration error", func(t *testing.T) {
		mock := newMock(t)
		mock.ExpectQuery(`SELECT 'user'`).WillReturnRows(
			pgxmock.NewRows([]string{"kind", "id", "label"}).
				AddRow("user", "user-1", "Ana").
				RowError(0, errors.New("stream failed")),
		)
		_, err := storage.NewPGXMessageStore(mock).ResolveAuthorizedMentionLabels(
			context.Background(), "ws-1", "ch-1", "requester-1", []string{"user-1"}, nil,
		)
		if err == nil {
			t.Fatal("expected iteration error")
		}
	})
}

// ---- CreateMessage: behavioral denial (0 rows → ErrNotFound) ----------------

func TestPGXMessageStore_CreateMessage_AuthDenied_ReturnsErrNotFound(t *testing.T) {
	for _, name := range []string{"channel auth denied", "DM auth denied", "cross-workspace", "invalid ref backstop"} {
		t.Run(name, func(t *testing.T) {
			mock := newMock(t)
			expectCreate(mock, pgxmock.NewRows(listMessageWithQuoteCols())) // 0 rows
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
		"user", "edited", "v1", "active",
		"", "", "",
		&editedAt, 1, &deletedAt,
		now, now,
		"Test User", "test@example.com", false,
	}
	row = append(row, emptyQuoteRow()...)
	expectCreate(mock, pgxmock.NewRows(listMessageWithQuoteCols()).AddRow(row...))

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
	mock.ExpectQuery(`(?s)FROM chat\.messages m.*JOIN chat\.workspace_members wm.*wm\.user_id = \$5.*m\.status = 'active'.*m\.channel_id IS NOT DISTINCT FROM \$3`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(1))

	store := storage.NewPGXMessageStore(mock)
	if err := store.ValidateRefMessageInTarget(context.Background(), "ws-1", "ch-1", "", "msg-1", "user-1"); err != nil {
		t.Fatalf("expected nil for valid ref, got %v", err)
	}
	checkExpectations(t, mock)
}

func TestPGXMessageStore_ValidateRefMessageInTarget_MissingReturnsErrInvalidRef(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(`FROM chat\.messages m`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(pgx.ErrNoRows)

	store := storage.NewPGXMessageStore(mock)
	err := store.ValidateRefMessageInTarget(context.Background(), "ws-1", "ch-1", "", "msg-missing", "user-1")
	if !errors.Is(err, domain.ErrInvalidMessageReference) {
		t.Fatalf("expected ErrInvalidMessageReference for missing ref, got %v", err)
	}
	checkExpectations(t, mock)
}

func TestPGXMessageStore_ValidateRefMessageInTarget_DeletedReturnsErrInvalidRef(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(`(?s)FROM chat\.messages m.*m\.status = 'active'`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(pgx.ErrNoRows)

	store := storage.NewPGXMessageStore(mock)
	err := store.ValidateRefMessageInTarget(context.Background(), "ws-1", "ch-1", "", "msg-deleted", "user-1")
	if !errors.Is(err, domain.ErrInvalidMessageReference) {
		t.Fatalf("expected ErrInvalidMessageReference for deleted ref, got %v", err)
	}
	checkExpectations(t, mock)
}

func TestPGXMessageStore_ResolveMessageReferences_FiltersWithCanonicalReadAccess(t *testing.T) {
	mock := newMock(t)
	now := time.Now().UTC()
	mock.ExpectQuery(`(?s)FROM unnest\(\$3::text\[\]\).*JOIN chat\.messages m.*JOIN chat\.workspace_members wm.*LEFT JOIN chat\.dm_members dm.*m\.status = 'active'.*m\.deleted_at IS NULL.*chat\.channel_visible_to_user\(c\.id, \$2::uuid\)`).
		WithArgs("ws-1", "user-1", []string{"msg-visible", "msg-hidden"}).
		WillReturnRows(pgxmock.NewRows([]string{
			"message_id", "target_type", "target_id", "target_label", "author", "body", "body_format", "created_at",
		}).AddRow("msg-visible", "channel", "ch-private", "privado", "Ana", "conteúdo", "v3", now))

	refs, err := storage.NewPGXMessageStore(mock).ResolveMessageReferences(
		t.Context(), "ws-1", "user-1", []string{"msg-visible", "msg-hidden"},
	)
	if err != nil {
		t.Fatalf("ResolveMessageReferences: %v", err)
	}
	if len(refs) != 1 || !refs["msg-visible"].Available || refs["msg-visible"].BodyText != "conteúdo" {
		t.Fatalf("unexpected resolved references: %+v", refs)
	}
	if _, leaked := refs["msg-hidden"]; leaked {
		t.Fatalf("inaccessible reference leaked into result: %+v", refs["msg-hidden"])
	}
	checkExpectations(t, mock)
}

func TestPGXMessageStore_ResolveMessageReferences_EmptySkipsDatabase(t *testing.T) {
	refs, err := storage.NewPGXMessageStore(newMock(t)).ResolveMessageReferences(t.Context(), "ws-1", "user-1", nil)
	if err != nil || len(refs) != 0 {
		t.Fatalf("empty references = %+v, %v", refs, err)
	}
}

// ---- GetMessageByIDInWorkspace ----------------------------------------------

func TestPGXMessageStore_GetMessageByIDInWorkspace_Found(t *testing.T) {
	mock := newMock(t)
	now := time.Now()
	// GetMessageByIDInWorkspace now uses listMessageColumns + auth.users JOIN,
	// matching the list endpoint contract (includes sender_display_name, sender_email).
	mock.ExpectQuery(`SELECT`).
		WithArgs("msg-1", "ws-1", "user-1").
		WillReturnRows(pgxmock.NewRows(listMessageWithQuoteCols()).
			AddRow(listMessageWithQuoteRow("msg-1", "ws-1", "ch-1", "", now)...))
	expectReactionBatch(mock, emptyReactionRows().AddRow("msg-1", "👍", 1, true))
	expectAttachmentBatch(mock, emptyAttachmentRows())

	store := storage.NewPGXMessageStore(mock)
	msg, err := store.GetMessageByIDInWorkspace(context.Background(), "ws-1", "msg-1", "user-1")
	if err != nil {
		t.Fatalf("GetMessageByIDInWorkspace: %v", err)
	}
	if msg.ID != "msg-1" {
		t.Fatalf("expected msg-1, got %q", msg.ID)
	}
	if msg.SenderDisplayName != "Test User" {
		t.Fatalf("expected sender_display_name 'Test User', got %q", msg.SenderDisplayName)
	}
	if msg.SenderEmail != "test@example.com" {
		t.Fatalf("expected sender_email 'test@example.com', got %q", msg.SenderEmail)
	}
	if len(msg.Reactions) != 1 || !msg.Reactions[0].ReactedByMe {
		t.Fatalf("expected personalized reactions, got %+v", msg.Reactions)
	}
	checkExpectations(t, mock)
}

func TestPGXMessageStore_EditMessage_SnapshotsAndUpdatesInOneTransaction(t *testing.T) {
	mock := newMock(t)
	now := time.Now().UTC()
	window := 900
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT m\.sender_id::text.*FOR UPDATE OF m`).
		WithArgs("ws-1", "msg-1", "user-1").
		WillReturnRows(pgxmock.NewRows([]string{"sender_id", "status", "deleted_at", "created_at", "edit_window_seconds", "now"}).
			AddRow("user-1", "active", nil, now.Add(-time.Minute), &window, now))
	mock.ExpectExec(`INSERT INTO chat\.message_edit_history`).
		WithArgs("msg-1", "user-1", now).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	updatedRow := listMessageWithQuoteRow("msg-1", "ws-1", "ch-1", "", now)
	updatedRow[6], updatedRow[7], updatedRow[13] = "new body", "v2", 1
	updatedRow[12], updatedRow[16] = &now, now
	mock.ExpectQuery(`(?s)UPDATE chat\.messages.*edit_count = edit_count \+ 1`).
		WithArgs("msg-1", "new body", "v2", "user-1", now).
		WillReturnRows(pgxmock.NewRows(listMessageWithQuoteCols()).AddRow(updatedRow...))
	mock.ExpectCommit()

	message, err := storage.NewPGXMessageStore(mock).EditMessage(context.Background(), storage.EditMessageInput{
		WorkspaceID: "ws-1", MessageID: "msg-1", EditorID: "user-1",
		Body: "new body", BodyFormat: domain.MessageBodyFormatV2,
	})
	if err != nil {
		t.Fatalf("EditMessage: %v", err)
	}
	if message.BodyText != "new body" || message.EditCount != 1 || message.EditedAt.IsZero() {
		t.Fatalf("unexpected updated message: %+v", message)
	}
	checkExpectations(t, mock)
}

func TestPGXMessageStore_EditMessage_ExpiredRollsBackBeforeSnapshot(t *testing.T) {
	mock := newMock(t)
	now := time.Now().UTC()
	window := 900
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT m\.sender_id::text.*FOR UPDATE OF m`).
		WithArgs("ws-1", "msg-1", "user-1").
		WillReturnRows(pgxmock.NewRows([]string{"sender_id", "status", "deleted_at", "created_at", "edit_window_seconds", "now"}).
			AddRow("user-1", "active", nil, now.Add(-901*time.Second), &window, now))
	mock.ExpectRollback()

	_, err := storage.NewPGXMessageStore(mock).EditMessage(context.Background(), storage.EditMessageInput{
		WorkspaceID: "ws-1", MessageID: "msg-1", EditorID: "user-1",
		Body: "new body", BodyFormat: domain.MessageBodyFormatV1,
	})
	if !errors.Is(err, domain.ErrEditWindowExpired) {
		t.Fatalf("expected ErrEditWindowExpired, got %v", err)
	}
	checkExpectations(t, mock)
}

func TestPGXMessageStore_EditMessage_AtWindowLimitSucceeds(t *testing.T) {
	mock := newMock(t)
	now := time.Now().UTC()
	window := 900
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT m\.sender_id::text.*FOR UPDATE OF m`).
		WithArgs("ws-1", "msg-1", "user-1").
		WillReturnRows(pgxmock.NewRows([]string{"sender_id", "status", "deleted_at", "created_at", "edit_window_seconds", "now"}).
			AddRow("user-1", "active", nil, now.Add(-900*time.Second), &window, now))
	mock.ExpectExec(`INSERT INTO chat\.message_edit_history`).
		WithArgs("msg-1", "user-1", now).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	updatedRow := listMessageWithQuoteRow("msg-1", "ws-1", "ch-1", "", now)
	updatedRow[6], updatedRow[7], updatedRow[13] = "at limit", "v1", 1
	updatedRow[12], updatedRow[16] = &now, now
	mock.ExpectQuery(`(?s)UPDATE chat\.messages.*edit_count = edit_count \+ 1`).
		WithArgs("msg-1", "at limit", "v1", "user-1", now).
		WillReturnRows(pgxmock.NewRows(listMessageWithQuoteCols()).AddRow(updatedRow...))
	mock.ExpectCommit()

	message, err := storage.NewPGXMessageStore(mock).EditMessage(context.Background(), storage.EditMessageInput{
		WorkspaceID: "ws-1", MessageID: "msg-1", EditorID: "user-1",
		Body: "at limit", BodyFormat: domain.MessageBodyFormatV1,
	})
	if err != nil || message.BodyText != "at limit" {
		t.Fatalf("EditMessage at exact window limit = %+v, %v", message, err)
	}
	checkExpectations(t, mock)
}

func TestPGXMessageStore_EditMessage_MissingSnapshotRowRollsBack(t *testing.T) {
	mock := newMock(t)
	now := time.Now().UTC()
	window := 900
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT m\.sender_id::text.*FOR UPDATE OF m`).
		WithArgs("ws-1", "msg-1", "user-1").
		WillReturnRows(pgxmock.NewRows([]string{"sender_id", "status", "deleted_at", "created_at", "edit_window_seconds", "now"}).
			AddRow("user-1", "active", nil, now.Add(-time.Minute), &window, now))
	mock.ExpectExec(`INSERT INTO chat\.message_edit_history`).
		WithArgs("msg-1", "user-1", now).
		WillReturnResult(pgxmock.NewResult("INSERT", 0))
	mock.ExpectRollback()

	_, err := storage.NewPGXMessageStore(mock).EditMessage(context.Background(), storage.EditMessageInput{
		WorkspaceID: "ws-1", MessageID: "msg-1", EditorID: "user-1",
		Body: "new body", BodyFormat: domain.MessageBodyFormatV1,
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	checkExpectations(t, mock)
}

func TestPGXMessageStore_DeleteMessage_SoftDeletesAndPreservesRow(t *testing.T) {
	mock := newMock(t)
	now := time.Now().UTC()
	createdAt := now.Add(-time.Hour)
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT m\.sender_id::text, m\.kind, m\.status, m\.deleted_at, clock_timestamp\(\).*FOR UPDATE OF m`).
		WithArgs("ws-1", "msg-1", "user-1").
		WillReturnRows(pgxmock.NewRows([]string{"sender_id", "kind", "status", "deleted_at", "now"}).
			AddRow("user-1", "user", "active", nil, now))
	mock.ExpectExec(`(?s)UPDATE chat\.messages.*status = 'deleted'.*sender_id = \$3`).
		WithArgs("msg-1", "ws-1", "user-1", now).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	row := listMessageWithQuoteRow("msg-1", "ws-1", "ch-1", "", createdAt)
	row[8], row[14], row[16] = "deleted", &now, now
	mock.ExpectQuery(`(?s)SELECT .*FROM chat\.messages m.*WHERE m\.id = \$1 AND m\.workspace_id = \$2`).
		WithArgs("msg-1", "ws-1", "user-1").
		WillReturnRows(pgxmock.NewRows(listMessageWithQuoteCols()).AddRow(row...))
	mock.ExpectCommit()

	message, changed, err := storage.NewPGXMessageStore(mock).DeleteMessage(t.Context(), storage.DeleteMessageInput{
		WorkspaceID: "ws-1", MessageID: "msg-1", RequesterID: "user-1",
	})
	if err != nil || !changed {
		t.Fatalf("DeleteMessage = %+v, changed=%v, err=%v", message, changed, err)
	}
	if message.Status != domain.MessageStatusDeleted || message.DeletedAt.IsZero() || message.BodyText != "hello" || !message.CreatedAt.Equal(createdAt) {
		t.Fatalf("soft-deleted row not preserved: %+v", message)
	}
	checkExpectations(t, mock)
}

func TestPGXMessageStore_DeleteMessage_RejectsOtherAuthorWithoutUpdate(t *testing.T) {
	mock := newMock(t)
	now := time.Now().UTC()
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT m\.sender_id::text.*FOR UPDATE OF m`).
		WithArgs("ws-1", "msg-1", "user-2").
		WillReturnRows(pgxmock.NewRows([]string{"sender_id", "kind", "status", "deleted_at", "now"}).
			AddRow("user-1", "user", "active", nil, now))
	mock.ExpectRollback()

	_, changed, err := storage.NewPGXMessageStore(mock).DeleteMessage(t.Context(), storage.DeleteMessageInput{
		WorkspaceID: "ws-1", MessageID: "msg-1", RequesterID: "user-2",
	})
	if !errors.Is(err, domain.ErrForbidden) || changed {
		t.Fatalf("expected forbidden unchanged delete, changed=%v err=%v", changed, err)
	}
	checkExpectations(t, mock)
}

func TestPGXMessageStore_DeleteMessage_HidesMissingOrInaccessibleScope(t *testing.T) {
	mock := newMock(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT m\.sender_id::text.*m\.workspace_id = \$1.*FOR UPDATE OF m`).
		WithArgs("other-workspace", "msg-1", "user-1").
		WillReturnRows(pgxmock.NewRows([]string{"sender_id", "kind", "status", "deleted_at", "now"}))
	mock.ExpectRollback()

	_, changed, err := storage.NewPGXMessageStore(mock).DeleteMessage(t.Context(), storage.DeleteMessageInput{
		WorkspaceID: "other-workspace", MessageID: "msg-1", RequesterID: "user-1",
	})
	if !errors.Is(err, domain.ErrNotFound) || changed {
		t.Fatalf("expected non-enumerable not found, changed=%v err=%v", changed, err)
	}
	checkExpectations(t, mock)
}

func TestPGXMessageStore_DeleteMessage_IsIdempotent(t *testing.T) {
	mock := newMock(t)
	deletedAt := time.Now().UTC().Add(-time.Minute)
	now := deletedAt.Add(time.Minute)
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT m\.sender_id::text.*FOR UPDATE OF m`).
		WithArgs("ws-1", "msg-1", "user-1").
		WillReturnRows(pgxmock.NewRows([]string{"sender_id", "kind", "status", "deleted_at", "now"}).
			AddRow("user-1", "user", "deleted", &deletedAt, now))
	row := listMessageWithQuoteRow("msg-1", "ws-1", "ch-1", "", deletedAt.Add(-time.Hour))
	row[8], row[14] = "deleted", &deletedAt
	mock.ExpectQuery(`(?s)SELECT .*FROM chat\.messages m.*WHERE m\.id = \$1 AND m\.workspace_id = \$2`).
		WithArgs("msg-1", "ws-1", "user-1").
		WillReturnRows(pgxmock.NewRows(listMessageWithQuoteCols()).AddRow(row...))
	mock.ExpectCommit()

	message, changed, err := storage.NewPGXMessageStore(mock).DeleteMessage(t.Context(), storage.DeleteMessageInput{
		WorkspaceID: "ws-1", MessageID: "msg-1", RequesterID: "user-1",
	})
	if err != nil || changed || !message.DeletedAt.Equal(deletedAt) {
		t.Fatalf("idempotent DeleteMessage = %+v, changed=%v, err=%v", message, changed, err)
	}
	checkExpectations(t, mock)
}

func TestPGXMessageStore_ListMessageEditHistory_FiltersDeletedMessages(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(`(?s)WITH authorized AS.*m\.status = 'active' AND m\.deleted_at IS NULL.*message_edit_history`).
		WithArgs("ws-1", "msg-1", "user-1", 50, 0).
		WillReturnRows(pgxmock.NewRows([]string{"id", "message_id", "body", "body_format", "editor_user_id", "versioned_at"}))

	_, err := storage.NewPGXMessageStore(mock).ListMessageEditHistory(t.Context(), storage.ListMessageEditHistoryInput{
		WorkspaceID: "ws-1", MessageID: "msg-1", UserID: "user-1",
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected deleted history to be hidden, got %v", err)
	}
	checkExpectations(t, mock)
}

func TestPGXMessageStore_ListMessageEditHistory_ReturnsMultipleVersionsNewestFirst(t *testing.T) {
	mock := newMock(t)
	now := time.Now().UTC()
	older := now.Add(-time.Minute)
	mock.ExpectQuery(`(?s)WITH authorized AS.*message_edit_history.*ORDER BY versioned_at DESC`).
		WithArgs("ws-1", "msg-1", "user-1", 2, 1).
		WillReturnRows(pgxmock.NewRows([]string{"id", "message_id", "body", "body_format", "editor_user_id", "versioned_at"}).
			AddRow("hist-2", "msg-1", "second", "v2", "user-1", &now).
			AddRow("hist-1", "msg-1", "first", "v1", "user-1", &older))

	history, err := storage.NewPGXMessageStore(mock).ListMessageEditHistory(context.Background(), storage.ListMessageEditHistoryInput{
		WorkspaceID: "ws-1", MessageID: "msg-1", UserID: "user-1", Limit: 2, Offset: 1,
	})
	if err != nil {
		t.Fatalf("ListMessageEditHistory: %v", err)
	}
	if len(history) != 2 || history[0].Body != "second" || history[1].Body != "first" {
		t.Fatalf("unexpected history order/content: %+v", history)
	}
	checkExpectations(t, mock)
}

func TestPGXMessageStore_ListMessageEditHistory_EmptyAndNonEnumeratingNotFound(t *testing.T) {
	t.Run("no edits", func(t *testing.T) {
		mock := newMock(t)
		mock.ExpectQuery(`(?s)WITH authorized AS.*message_edit_history`).
			WithArgs("ws-1", "msg-1", "user-1", 50, 0).
			WillReturnRows(pgxmock.NewRows([]string{"id", "message_id", "body", "body_format", "editor_user_id", "versioned_at"}).
				AddRow("", "", "", "", "", nil))

		history, err := storage.NewPGXMessageStore(mock).ListMessageEditHistory(context.Background(), storage.ListMessageEditHistoryInput{
			WorkspaceID: "ws-1", MessageID: "msg-1", UserID: "user-1",
		})
		if err != nil || len(history) != 0 {
			t.Fatalf("empty history = %+v, %v", history, err)
		}
		checkExpectations(t, mock)
	})

	t.Run("unauthorized or missing", func(t *testing.T) {
		mock := newMock(t)
		mock.ExpectQuery(`(?s)WITH authorized AS.*message_edit_history`).
			WithArgs("ws-1", "msg-1", "non-member", 50, 0).
			WillReturnRows(pgxmock.NewRows([]string{"id", "message_id", "body", "body_format", "editor_user_id", "versioned_at"}))

		_, err := storage.NewPGXMessageStore(mock).ListMessageEditHistory(context.Background(), storage.ListMessageEditHistoryInput{
			WorkspaceID: "ws-1", MessageID: "msg-1", UserID: "non-member",
		})
		if !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("expected ErrNotFound, got %v", err)
		}
		checkExpectations(t, mock)
	})
}

func TestPGXMessageStore_GetMessageByIDInWorkspace_NotFoundReturnsErrNotFound(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(`SELECT`).
		WithArgs("msg-missing", "ws-1", "user-1").
		WillReturnError(pgx.ErrNoRows)

	store := storage.NewPGXMessageStore(mock)
	_, err := store.GetMessageByIDInWorkspace(context.Background(), "ws-1", "msg-missing", "user-1")
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
		WillReturnRows(pgxmock.NewRows(listMessageWithQuoteCols()).
			AddRow(listMessageWithQuoteRow("msg-1", "ws-1", "ch-1", "", now)...).
			AddRow(listMessageWithQuoteRow("msg-2", "ws-1", "ch-1", "", now)...))
	expectReactionBatch(mock, emptyReactionRows().AddRow("msg-1", "👍", 2, true))
	expectAttachmentBatch(mock, emptyAttachmentRows())

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
	if len(result.Messages[1].Reactions) != 1 || result.Messages[1].Reactions[0].Emoji != "👍" {
		t.Fatalf("expected batched reaction aggregate, got %+v", result.Messages[1].Reactions)
	}
	checkExpectations(t, mock)
}

func TestPGXMessageStore_ListChannelMessages_JoinsQuotedParent(t *testing.T) {
	mock := newMock(t)
	now := time.Now()
	mock.ExpectQuery(`(?s)LEFT JOIN chat\.messages q.*q\.id = m\.parent_message_id.*q\.workspace_id = m\.workspace_id.*q\.channel_id IS NOT DISTINCT FROM m\.channel_id`).
		WithArgs("ws-1", "ch-1", "user-1", 51).
		WillReturnRows(pgxmock.NewRows(listMessageWithQuoteCols()).
			AddRow(listMessageRowWithQuote(
				"msg-child", "ws-1", "ch-1", "", now,
				quoteRow("msg-parent", "user-parent", "parent body", "v1", "active", nil, now),
			)...))
	expectReactionBatch(mock, emptyReactionRows())
	expectAttachmentBatch(mock, emptyAttachmentRows())

	store := storage.NewPGXMessageStore(mock)
	result, err := store.ListChannelMessages(context.Background(), storage.ListChannelMessagesInput{
		WorkspaceID: "ws-1", ChannelID: "ch-1", UserID: "user-1",
	})
	if err != nil {
		t.Fatalf("ListChannelMessages: %v", err)
	}
	if len(result.Messages) != 1 || result.Messages[0].Quoted == nil || result.Messages[0].Quoted.ID != "msg-parent" {
		t.Fatalf("quoted parent not loaded: %+v", result.Messages)
	}
	checkExpectations(t, mock)
}

func TestPGXMessageStore_ListChannelMessages_EmptyReturnsEmptySlice(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(`SELECT`).
		WithArgs("ws-1", "ch-1", "user-1", 51).
		WillReturnRows(pgxmock.NewRows(listMessageWithQuoteCols()))

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
		"user", "edited body", "v1", "active",
		"", "", "",
		&editedAt, 1, &deletedAt,
		now, now,
		"Test User", "test@example.com", false,
	}
	row = append(row, emptyQuoteRow()...)
	mock.ExpectQuery(`SELECT`).
		WithArgs("ws-1", "ch-1", "user-1", 51).
		WillReturnRows(pgxmock.NewRows(listMessageWithQuoteCols()).AddRow(row...))
	expectReactionBatch(mock, emptyReactionRows())
	expectAttachmentBatch(mock, emptyAttachmentRows())

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
		WillReturnRows(pgxmock.NewRows(listMessageWithQuoteCols()).
			AddRow(listMessageWithQuoteRow("msg-3", "ws-1", "", "dm-1", now)...))
	expectReactionBatch(mock, emptyReactionRows())
	expectAttachmentBatch(mock, emptyAttachmentRows())

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
		WillReturnRows(pgxmock.NewRows(listMessageWithQuoteCols()))

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
		WillReturnRows(pgxmock.NewRows(listMessageWithQuoteCols()).
			AddRow(listMessageWithQuoteRow("msg-1", "ws-1", "ch-1", "", now)...))
	expectReactionBatch(mock, emptyReactionRows())
	expectAttachmentBatch(mock, emptyAttachmentRows())

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
		WillReturnRows(pgxmock.NewRows(listMessageWithQuoteCols()))

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
		WillReturnRows(pgxmock.NewRows(listMessageWithQuoteCols()))

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
	rows := pgxmock.NewRows(listMessageWithQuoteCols())
	for i := range 51 {
		id := "msg-" + string(rune('a'+i%26))
		rows.AddRow(listMessageWithQuoteRow(id, "ws-1", "ch-1", "", now)...)
	}
	mock.ExpectQuery(`SELECT`).
		WithArgs("ws-1", "ch-1", "user-1", 51).
		WillReturnRows(rows)
	expectReactionBatch(mock, emptyReactionRows())
	expectAttachmentBatch(mock, emptyAttachmentRows())

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
	rows := pgxmock.NewRows(listMessageWithQuoteCols())
	for i := range 51 {
		id := "dm-msg-" + string(rune('a'+i%26))
		rows.AddRow(listMessageWithQuoteRow(id, "ws-1", "", "dm-1", now)...)
	}
	mock.ExpectQuery(`SELECT`).
		WithArgs("ws-1", "dm-1", "user-1", 51).
		WillReturnRows(rows)
	expectReactionBatch(mock, emptyReactionRows())
	expectAttachmentBatch(mock, emptyAttachmentRows())

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
		WillReturnRows(pgxmock.NewRows(listMessageWithQuoteCols()).
			AddRow(listMessageWithQuoteRow("msg-older", "ws-1", "ch-1", "", now.Add(-time.Minute))...))
	expectReactionBatch(mock, emptyReactionRows())
	expectAttachmentBatch(mock, emptyAttachmentRows())

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
		WillReturnRows(pgxmock.NewRows(listMessageWithQuoteCols()).
			AddRow(listMessageWithQuoteRow("dm-older", "ws-1", "", "dm-1", now.Add(-time.Minute))...))
	expectReactionBatch(mock, emptyReactionRows())
	expectAttachmentBatch(mock, emptyAttachmentRows())

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

// ---- Stable ordering: SQL tie-break on id when created_at ties --------------

// TestPGXMessageStore_ListChannelMessages_KeysetSQLContainsIDTieBreaker verifies the
// keyset pagination SQL uses a row comparison (m.created_at, m.id) < ($4, $5::uuid)
// so that results are stable even when multiple messages share the same created_at
// timestamp. The row comparison uses native UUID ordering (no text cast) and aligns
// with the ORDER BY m.created_at DESC, m.id DESC clause.
func TestPGXMessageStore_ListChannelMessages_KeysetSQLContainsIDTieBreaker(t *testing.T) {
	mock := newMock(t)
	now := time.Now()
	cursor := &storage.MessageCursor{CreatedAt: now, ID: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"}

	// Match the row-comparison keyset clause in the query.
	mock.ExpectQuery(`(?s)\(m\.created_at,\s*m\.id\)\s*<\s*\(\$4,\s*\$5::uuid\)`).
		WithArgs("ws-1", "ch-1", "user-1", pgxmock.AnyArg(), cursor.ID, 51).
		WillReturnRows(pgxmock.NewRows(listMessageWithQuoteCols()))

	store := storage.NewPGXMessageStore(mock)
	_, err := store.ListChannelMessages(context.Background(), storage.ListChannelMessagesInput{
		WorkspaceID: "ws-1", ChannelID: "ch-1", UserID: "user-1",
		BeforeCursor: cursor,
	})
	if err != nil {
		t.Fatalf("ListChannelMessages keyset tie-break: %v", err)
	}
	checkExpectations(t, mock)
}

// TestPGXMessageStore_ListDMMessages_KeysetSQLContainsIDTieBreaker mirrors the channel
// test for DM conversations, ensuring the same row-comparison predicate is present.
func TestPGXMessageStore_ListDMMessages_KeysetSQLContainsIDTieBreaker(t *testing.T) {
	mock := newMock(t)
	now := time.Now()
	cursor := &storage.MessageCursor{CreatedAt: now, ID: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"}

	// Match the row-comparison keyset clause in the DM query.
	mock.ExpectQuery(`(?s)\(m\.created_at,\s*m\.id\)\s*<\s*\(\$4,\s*\$5::uuid\)`).
		WithArgs("ws-1", "conv-1", "user-1", pgxmock.AnyArg(), cursor.ID, 51).
		WillReturnRows(pgxmock.NewRows(listMessageWithQuoteCols()))

	store := storage.NewPGXMessageStore(mock)
	_, err := store.ListDMMessages(context.Background(), storage.ListDMMessagesInput{
		WorkspaceID: "ws-1", ConversationID: "conv-1", UserID: "user-1",
		BeforeCursor: cursor,
	})
	if err != nil {
		t.Fatalf("ListDMMessages keyset tie-break: %v", err)
	}
	checkExpectations(t, mock)
}

// ── RF-21: the forwardable snapshot ──────────────────────────────────────────

func TestPGXMessageStore_SnapshotForwardableMessage_ReadsSourceContent(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(snapshotForwardSQL).
		WithArgs("ws-1", "destination", "actor", "source").
		WillReturnRows(pgxmock.NewRows([]string{"id", "body_text", "body_format"}).
			AddRow("source", "veja https://example.com", "v3"))

	snapshot, err := storage.NewPGXMessageStore(mock).SnapshotForwardableMessage(
		t.Context(), storage.ForwardSnapshotInput{
			WorkspaceID: "ws-1", DestinationChannelID: "destination",
			ActorID: "actor", SourceMessageID: "source",
		})
	if err != nil {
		t.Fatalf("SnapshotForwardableMessage: %v", err)
	}
	if snapshot.SourceMessageID != "source" || snapshot.BodyText != "veja https://example.com" ||
		snapshot.BodyFormat != domain.MessageBodyFormatV3 {
		t.Fatalf("snapshot: %+v", snapshot)
	}
	// No ExpectBegin and no ExpectCommit were registered, and the expectations
	// are met: the read opened no transaction. That is the property the safety
	// check depends on — the provider call that follows must not happen with a
	// row locked or a connection reserved.
	checkExpectations(t, mock)
}

func TestPGXMessageStore_SnapshotForwardableMessage_DeniedIsNonEnumerating(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(snapshotForwardSQL).
		WithArgs("ws-1", "destination", "actor", "source").
		WillReturnRows(pgxmock.NewRows([]string{"id", "body_text", "body_format"}))

	_, err := storage.NewPGXMessageStore(mock).SnapshotForwardableMessage(
		t.Context(), storage.ForwardSnapshotInput{
			WorkspaceID: "ws-1", DestinationChannelID: "destination",
			ActorID: "actor", SourceMessageID: "source",
		})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	checkExpectations(t, mock)
}

// The forwarding statement must write the caller's snapshot, never re-read the
// source. Passing a body that differs from anything the source could hold and
// asserting it reached the parameters is what pins that.
func TestPGXMessageStore_ForwardChannelMessage_WritesTheSuppliedSnapshot(t *testing.T) {
	mock := newMock(t)
	now := time.Now()
	row := listMessageWithQuoteRow("forwarded", "ws-1", "destination", "", now)
	row[6] = "checked body"
	row[7] = "v2"
	row[10] = "source"
	mock.ExpectQuery(forwardMsgSQL).
		WithArgs("ws-1", "destination", "actor", "source", "action-1", "checked body", "v2",
			pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(forwardMessageCols()).AddRow(append(row, false)...))

	result, err := storage.NewPGXMessageStore(mock).ForwardChannelMessage(
		t.Context(), storage.ForwardChannelMessageInput{
			WorkspaceID: "ws-1", DestinationChannelID: "destination", ActorID: "actor",
			SourceMessageID: "source", IdempotencyKey: "action-1",
			BodyText: "checked body", BodyFormat: domain.MessageBodyFormatV2,
		})
	if err != nil {
		t.Fatalf("ForwardChannelMessage: %v", err)
	}
	if result.Message.BodyText != "checked body" {
		t.Fatalf("persisted body: %q", result.Message.BodyText)
	}
	checkExpectations(t, mock)
}
