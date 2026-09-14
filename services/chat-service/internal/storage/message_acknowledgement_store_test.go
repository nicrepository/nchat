package storage_test

import (
	"errors"
	"testing"
	"time"

	pgxmock "github.com/pashagolub/pgxmock/v2"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// Issue #824, the send and delete paths. These assert the statement shape and
// the bind contract — that the flag reaches $24, that the recipient snapshot and
// the reply resolution are CTEs of the creating statement rather than follow-up
// round trips, and that deleting withdraws pending requests in the same
// transaction. Whether those statements produce the right rows is proved
// against a real database.

// acknowledgementColumnIndex is where the flag sits in the shared message
// column contract: last of messageColumns, after issue #821's priority. Derived
// rather than written down, so a projection that grows again moves this with it.
func acknowledgementColumnIndex() int { return len(messageCols()) - 1 }

// expectCreateWithAcknowledgement is expectCreate with $24 pinned instead of
// matched loosely: this is the assertion that the author's request actually
// reaches the statement rather than being dropped between the service and the
// bind list.
func expectCreateWithAcknowledgement(mock pgxmock.PgxPoolIface, required bool, rows *pgxmock.Rows) {
	args := make([]any, 0, 25)
	for range 23 {
		args = append(args, pgxmock.AnyArg())
	}
	// $24 is the request this test is about; $25 is the bound it is judged
	// against, matched loosely because it is a constant and not what is asserted.
	args = append(args, required, pgxmock.AnyArg())
	mock.ExpectQuery(createMsgSQL).WithArgs(args...).WillReturnRows(rows)
}

func acknowledgementRow(id string, now time.Time, required bool) []any {
	row := listMessageWithQuoteRow(id, "ws-1", "ch-1", "", now)
	row[acknowledgementColumnIndex()] = required
	return row
}

// Both values survive the write: bound as sent, and read back into the domain
// unchanged.
func TestPGXMessageStore_CreateMessage_RoundTripsTheAcknowledgementRequest(t *testing.T) {
	for _, required := range []bool{false, true} {
		t.Run(map[bool]string{false: "not requested", true: "requested"}[required], func(t *testing.T) {
			mock := newMock(t)
			now := time.Now()
			expectCreateWithAcknowledgement(mock, required,
				pgxmock.NewRows(listMessageWithQuoteCols()).AddRow(acknowledgementRow("msg-a", now, required)...))

			msg, err := storage.NewPGXMessageStore(mock).CreateMessage(t.Context(), storage.CreateMessageInput{
				WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: "user-1", BodyText: "confirm please",
				AcknowledgementRequired: required,
			})
			if err != nil {
				t.Fatalf("CreateMessage: %v", err)
			}
			if msg.AcknowledgementRequired != required {
				t.Fatalf("read back %v, want %v", msg.AcknowledgementRequired, required)
			}
			checkExpectations(t, mock)
		})
	}
}

// The recipient set is selected once, bounded, and written — all inside the
// creating statement, in that order.
//
// This is the whole of the fan-out bound's atomicity, asserted as an ordering
// over one statement: the candidates are scanned into a CTE, the bound is
// decided over that CTE, the INSERT refuses on it, and the recipient rows read
// the same CTE rather than re-running the scan. A shape that counted in one
// place and materialised in another would satisfy every other test in this file
// and still be able to write one row past the bound.
func TestPGXMessageStore_CreateMessage_BoundsAndWritesRecipientsFromOneSnapshot(t *testing.T) {
	mock := newMock(t)
	now := time.Now()
	mock.ExpectQuery(`(?s)eligible_acknowledgement_recipients AS \(.*` +
		`chat\.channel_members.*chat\.dm_members.*` +
		`invalid_acknowledgement_fanout AS \(.*` +
		`acknowledgement_recipients AS \(.*` +
		`INSERT INTO chat\.messages.*` +
		`NOT EXISTS \(SELECT 1 FROM invalid_acknowledgement_fanout\).*` +
		`INSERT INTO chat\.message_acknowledgements.*` +
		`CROSS JOIN acknowledgement_recipients r.*` +
		`UPDATE chat\.message_acknowledgements.*state = 'responded'.*state = 'pending'`).
		WithArgs(anyCreateArgs()...).
		WillReturnRows(pgxmock.NewRows(listMessageWithQuoteCols()).
			AddRow(acknowledgementRow("msg-a", now, true)...))

	if _, err := storage.NewPGXMessageStore(mock).CreateMessage(t.Context(), storage.CreateMessageInput{
		WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: "user-1", BodyText: "confirm please",
		AcknowledgementRequired: true,
	}); err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}
	checkExpectations(t, mock)
}

// A reply resolves only a published reply. A withheld one would be invisible to
// the person who asked, so recording them as answered would show a sender a
// response against a message they cannot see and may never see.
func TestPGXMessageStore_CreateMessage_OnlyAPublishedReplyResolvesARequest(t *testing.T) {
	mock := newMock(t)
	now := time.Now()
	mock.ExpectQuery(`(?s)answered_acknowledgement AS \(.*WHERE inserted\.status = 'active'`).
		WithArgs(anyCreateArgs()...).
		WillReturnRows(pgxmock.NewRows(listMessageWithQuoteCols()).
			AddRow(acknowledgementRow("msg-reply", now, false)...))

	if _, err := storage.NewPGXMessageStore(mock).CreateMessage(t.Context(), storage.CreateMessageInput{
		WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: "user-1", BodyText: "on it",
		ParentMessageID: "msg-a",
	}); err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}
	checkExpectations(t, mock)
}

// Forwarding never copies the request. Inheriting it would ask a second set of
// people to confirm a message on behalf of an author who asked nobody, and
// would create their rows from a forward the original sender cannot see.
func TestPGXMessageStore_ForwardChannelMessage_DoesNotCopyTheAcknowledgementRequest(t *testing.T) {
	mock := newMock(t)
	now := time.Now()
	forwardRow := append(acknowledgementRow("msg-f", now, false), false)
	mock.ExpectQuery(`(?s)INSERT INTO chat\.messages.*RETURNING.*acknowledgement_required`).
		WithArgs("ws-1", "ch-2", "user-1", "msg-src", "", "urgent body", "v1", "active", []string(nil), "", "").
		WillReturnRows(pgxmock.NewRows(forwardMessageCols()).AddRow(forwardRow...))

	result, err := storage.NewPGXMessageStore(mock).ForwardChannelMessage(t.Context(),
		storage.ForwardChannelMessageInput{
			WorkspaceID: "ws-1", DestinationChannelID: "ch-2", ActorID: "user-1",
			SourceMessageID: "msg-src", BodyText: "urgent body", BodyFormat: domain.MessageBodyFormatV1,
		})
	if err != nil {
		t.Fatalf("ForwardChannelMessage: %v", err)
	}
	if result.Message.AcknowledgementRequired {
		t.Fatal("a forward must not inherit its source's confirmation request")
	}
	checkExpectations(t, mock)
}

// Deleting the message withdraws the question, in the same transaction and
// against pending rows only — an acknowledgement that committed first stands.
func TestPGXMessageStore_DeleteMessage_CancelsOnlyPendingRequests(t *testing.T) {
	mock := newMock(t)
	now := time.Now().UTC()
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT m\.sender_id::text.*FOR UPDATE OF m`).
		WithArgs("ws-1", "msg-1", "user-1").
		WillReturnRows(pgxmock.NewRows([]string{"sender_id", "kind", "status", "deleted_at", "now"}).
			AddRow("user-1", "user", "active", nil, now))
	mock.ExpectExec(`(?s)UPDATE chat\.messages.*status = 'deleted'`).
		WithArgs("msg-1", "ws-1", "user-1", now).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec(`(?s)UPDATE chat\.message_acknowledgements.*state = 'cancelled'.*state = 'pending'`).
		WithArgs("msg-1", now).
		WillReturnResult(pgxmock.NewResult("UPDATE", 3))
	mock.ExpectQuery(`(?s)SELECT .*FROM chat\.messages m`).
		WithArgs("msg-1", "ws-1", "user-1").
		WillReturnRows(pgxmock.NewRows(listMessageWithQuoteCols()).
			AddRow(acknowledgementRow("msg-1", now, true)...))
	mock.ExpectCommit()

	if _, _, err := storage.NewPGXMessageStore(mock).DeleteMessage(t.Context(), storage.DeleteMessageInput{
		WorkspaceID: "ws-1", MessageID: "msg-1", RequesterID: "user-1",
	}); err != nil {
		t.Fatalf("DeleteMessage: %v", err)
	}
	checkExpectations(t, mock)
}

// A failure withdrawing the requests takes the delete with it. A commit in which
// the message is gone but people are still pending on it is the inconsistency
// the shared transaction exists to prevent.
func TestPGXMessageStore_DeleteMessage_FailedCancellationAbortsTheDelete(t *testing.T) {
	mock := newMock(t)
	now := time.Now().UTC()
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT m\.sender_id::text.*FOR UPDATE OF m`).
		WithArgs("ws-1", "msg-1", "user-1").
		WillReturnRows(pgxmock.NewRows([]string{"sender_id", "kind", "status", "deleted_at", "now"}).
			AddRow("user-1", "user", "active", nil, now))
	mock.ExpectExec(`(?s)UPDATE chat\.messages.*status = 'deleted'`).
		WithArgs("msg-1", "ws-1", "user-1", now).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec(`(?s)UPDATE chat\.message_acknowledgements`).
		WithArgs("msg-1", now).
		WillReturnError(errors.New("connection reset"))
	mock.ExpectRollback()

	if _, _, err := storage.NewPGXMessageStore(mock).DeleteMessage(t.Context(), storage.DeleteMessageInput{
		WorkspaceID: "ws-1", MessageID: "msg-1", RequesterID: "user-1",
	}); err == nil {
		t.Fatal("a failed withdrawal must abort the delete rather than commit half of it")
	}
	checkExpectations(t, mock)
}

// The bound query reads one row past the limit and counts the channel's members
// or the conversation's, never the sender.
func TestPGXMessageStore_CountAcknowledgementRecipientsUpTo_BindsTheTargetAndCeiling(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(`(?s)chat\.channel_members.*chat\.dm_members.*LIMIT \$5::int`).
		WithArgs("ws-1", (*string)(nil), ptrTo("conv-1"), "user-1", 51).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(7))

	got, err := storage.NewPGXMessageStore(mock).
		CountAcknowledgementRecipientsUpTo(t.Context(), "ws-1", "", "conv-1", "user-1", 51)
	if err != nil {
		t.Fatalf("CountAcknowledgementRecipientsUpTo: %v", err)
	}
	if got != 7 {
		t.Fatalf("count = %d, want 7", got)
	}
	checkExpectations(t, mock)
}

// A non-positive ceiling is a caller bug, and is refused before any query: an
// unbounded count is exactly what the ceiling exists to prevent.
func TestPGXMessageStore_CountAcknowledgementRecipientsUpTo_RefusesANonPositiveCeiling(t *testing.T) {
	for _, limit := range []int{0, -1} {
		mock := newMock(t)
		_, err := storage.NewPGXMessageStore(mock).
			CountAcknowledgementRecipientsUpTo(t.Context(), "ws-1", "ch-1", "", "user-1", limit)
		if !errors.Is(err, domain.ErrInvalidInput) {
			t.Fatalf("limit %d: error = %v, want ErrInvalidInput", limit, err)
		}
		checkExpectations(t, mock)
	}
}

func ptrTo(s string) *string { return &s }

// anyCreateArgs matches the creating statement's whole bind list loosely, for
// the tests that are about the SQL's shape rather than about what it is bound
// with.
func anyCreateArgs() []any {
	args := make([]any, 0, 25)
	for range 25 {
		args = append(args, pgxmock.AnyArg())
	}
	return args
}
