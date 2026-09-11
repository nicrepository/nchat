package storage_test

import (
	"context"
	"testing"
	"time"

	pgxmock "github.com/pashagolub/pgxmock/v2"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// priorityColumnIndex is where priority sits in the shared message column
// contract: last of messageColumns, after the conversation event pair. Derived
// rather than written down, because a row fixture is positional and a literal
// here would start pointing at the wrong column the next time the projection
// grows — which is a test that passes while asserting about event_payload.
func priorityColumnIndex() int { return len(messageCols()) - 1 }

// expectCreateWithPriority is expectCreate with $23 pinned instead of matched
// by AnyArg: this is the assertion that the validated value actually reaches
// the statement, rather than being dropped somewhere between the service and
// the bind list.
func expectCreateWithPriority(mock pgxmock.PgxPoolIface, priority string, rows *pgxmock.Rows) {
	args := make([]any, 0, 23)
	for range 22 {
		args = append(args, pgxmock.AnyArg())
	}
	args = append(args, priority)
	mock.ExpectQuery(createMsgSQL).WithArgs(args...).WillReturnRows(rows)
}

func priorityRow(id string, now time.Time, priority string) []any {
	row := listMessageWithQuoteRow(id, "ws-1", "ch-1", "", now)
	row[priorityColumnIndex()] = priority
	return row
}

// Each of the three survives the whole write: bound to the statement as sent,
// and read back into the domain unchanged.
func TestPGXMessageStore_CreateMessage_RoundTripsEveryPriority(t *testing.T) {
	for _, priority := range []domain.MessagePriority{
		domain.MessagePriorityStandard,
		domain.MessagePriorityImportant,
		domain.MessagePriorityUrgent,
	} {
		t.Run(string(priority), func(t *testing.T) {
			mock := newMock(t)
			now := time.Now()
			expectCreateWithPriority(mock, string(priority),
				pgxmock.NewRows(listMessageWithQuoteCols()).AddRow(priorityRow("msg-p", now, string(priority))...))

			msg, err := storage.NewPGXMessageStore(mock).CreateMessage(context.Background(), storage.CreateMessageInput{
				WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: "user-1",
				BodyText: "hello", Priority: priority,
			})
			if err != nil {
				t.Fatalf("CreateMessage: %v", err)
			}
			if msg.Priority != priority {
				t.Fatalf("Priority = %q, want %q", msg.Priority, priority)
			}
			checkExpectations(t, mock)
		})
	}
}

// A caller that states no priority must not bind an empty string at a NOT NULL
// column with a CHECK on it. The store resolves the default itself, so an
// internal caller that never saw an HTTP request writes a valid row.
func TestPGXMessageStore_CreateMessage_AbsentPriorityBindsStandard(t *testing.T) {
	mock := newMock(t)
	now := time.Now()
	expectCreateWithPriority(mock, string(domain.MessagePriorityStandard),
		pgxmock.NewRows(listMessageWithQuoteCols()).AddRow(priorityRow("msg-d", now, "standard")...))

	msg, err := storage.NewPGXMessageStore(mock).CreateMessage(context.Background(), storage.CreateMessageInput{
		WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: "user-1", BodyText: "hello",
	})
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}
	if msg.Priority != domain.MessagePriorityStandard {
		t.Fatalf("Priority = %q, want standard", msg.Priority)
	}
	checkExpectations(t, mock)
}

// A listing carries each message's own priority. The list path has its own
// scan, separate from the single-row one, and this is what stops the two from
// disagreeing about the column order.
func TestPGXMessageStore_ListChannelMessages_PreservesPriorityPerMessage(t *testing.T) {
	mock := newMock(t)
	now := time.Now()
	mock.ExpectQuery(`SELECT`).
		WithArgs("ws-1", "ch-1", "user-1", 51).
		WillReturnRows(pgxmock.NewRows(listMessageWithQuoteCols()).
			AddRow(priorityRow("msg-1", now, "urgent")...).
			AddRow(priorityRow("msg-2", now, "important")...).
			AddRow(priorityRow("msg-3", now, "standard")...))
	expectReactionBatch(mock, emptyReactionRows())
	expectAttachmentBatch(mock, emptyAttachmentRows())

	result, err := storage.NewPGXMessageStore(mock).ListChannelMessages(context.Background(), storage.ListChannelMessagesInput{
		WorkspaceID: "ws-1", ChannelID: "ch-1", UserID: "user-1",
	})
	if err != nil {
		t.Fatalf("ListChannelMessages: %v", err)
	}
	// Keyed by id, not by position: the listing reverses rows into chronological
	// order, and what this test is about is each message keeping its own
	// priority, not where it lands.
	want := map[string]domain.MessagePriority{
		"msg-1": domain.MessagePriorityUrgent,
		"msg-2": domain.MessagePriorityImportant,
		"msg-3": domain.MessagePriorityStandard,
	}
	if len(result.Messages) != len(want) {
		t.Fatalf("got %d messages, want %d", len(result.Messages), len(want))
	}
	for _, message := range result.Messages {
		if message.Priority != want[message.ID] {
			t.Fatalf("message %s Priority = %q, want %q", message.ID, message.Priority, want[message.ID])
		}
	}
	checkExpectations(t, mock)
}

// Editing a body must not touch the priority column. The assertion is on the
// statement itself rather than on the row the mock hands back: a mock returns
// whatever it is told, so only the SQL can prove the UPDATE does not write it.
func TestPGXMessageStore_EditMessage_DoesNotWritePriority(t *testing.T) {
	mock := newMock(t)
	now := time.Now().UTC()
	window := 900
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT m\.sender_id::text.*FOR UPDATE OF m`).
		WithArgs("ws-1", "msg-1", "user-1").
		WillReturnRows(pgxmock.NewRows([]string{"sender_id", "kind", "status", "deleted_at", "created_at", "edit_window_seconds", "now"}).
			AddRow("user-1", "user", "active", nil, now.Add(-time.Minute), &window, now))
	mock.ExpectQuery(`INSERT INTO chat\.message_edit_history`).
		WithArgs("msg-1", "user-1", now).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow("history-1"))
	mock.ExpectExec(`DELETE FROM chat\.message_link_scans`).
		WithArgs("msg-1").
		WillReturnResult(pgxmock.NewResult("DELETE", 1))
	// The UPDATE's SET list, pinned: body, format, the edit bookkeeping and the
	// link-safety columns. `priority` is absent, and stays absent.
	updatedRow := priorityRow("msg-1", now, "urgent")
	updatedRow[6], updatedRow[13] = "new body", 1
	mock.ExpectQuery(`(?s)UPDATE chat\.messages.*SET body_text = \$2.*edit_count = edit_count \+ 1.*link_safety_projection_version = link_safety_projection_version \+ 1\s*WHERE id = \$1`).
		WithArgs("msg-1", "new body", "v1", "user-1", now, "", "").
		WillReturnRows(pgxmock.NewRows(listMessageWithQuoteCols()).AddRow(updatedRow...))
	mock.ExpectCommit()

	message, err := storage.NewPGXMessageStore(mock).EditMessage(context.Background(), storage.EditMessageInput{
		WorkspaceID: "ws-1", MessageID: "msg-1", EditorID: "user-1",
		Body: "new body", BodyFormat: domain.MessageBodyFormatV1,
	})
	if err != nil {
		t.Fatalf("EditMessage: %v", err)
	}
	// The edited message keeps the priority it was created with.
	if message.Priority != domain.MessagePriorityUrgent {
		t.Fatalf("edited message Priority = %q, want urgent", message.Priority)
	}
	checkExpectations(t, mock)
}

// A forward is a new message by a new author, so it takes the column default
// rather than inheriting the source's claim: forwarding must not be a way to
// re-escalate somebody else's message.
func TestPGXMessageStore_ForwardChannelMessage_DoesNotCopySourcePriority(t *testing.T) {
	mock := newMock(t)
	now := time.Now()
	forwardRow := append(priorityRow("msg-f", now, "standard"), false)
	mock.ExpectQuery(forwardMsgSQL).
		WithArgs("ws-1", "ch-2", "user-1", "msg-src", "", "urgent body", "v1", "active", []string(nil), "", "").
		WillReturnRows(pgxmock.NewRows(forwardMessageCols()).AddRow(forwardRow...))

	result, err := storage.NewPGXMessageStore(mock).ForwardChannelMessage(context.Background(), storage.ForwardChannelMessageInput{
		WorkspaceID: "ws-1", DestinationChannelID: "ch-2", ActorID: "user-1",
		SourceMessageID: "msg-src", BodyText: "urgent body", BodyFormat: domain.MessageBodyFormatV1,
	})
	if err != nil {
		t.Fatalf("ForwardChannelMessage: %v", err)
	}
	if result.Message.Priority != domain.MessagePriorityStandard {
		t.Fatalf("forwarded Priority = %q, want standard", result.Message.Priority)
	}
	checkExpectations(t, mock)
}
