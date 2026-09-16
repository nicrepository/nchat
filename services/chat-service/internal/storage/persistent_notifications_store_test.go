package storage_test

import (
	"errors"
	"testing"
	"time"

	pgxmock "github.com/pashagolub/pgxmock/v2"

	"github.com/nicrepository/nchat/libs/go/platform/notificationevent"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// Issue #825, the statement shape and the bind contract. What these assert is
// that the reminder schedule is written by the statement that writes the
// message, that the flag reaches $26, and that the cancellation is one
// statement whose authorization is a predicate rather than a preceding read.
// Whether those statements then produce the right rows is proved against a real
// database.

// persistentColumnIndex is where the flag sits in the shared message column
// contract: last of messageColumns, after issue #824's. Derived rather than
// written down, so a projection that grows again moves this with it.
func persistentColumnIndex() int { return len(messageCols()) - 1 }

// expectCreateWithPersistentNotifications is expectCreate with $26 pinned
// instead of matched loosely: this is the assertion that the author's request
// actually reaches the statement rather than being dropped between the service
// and the bind list.
func expectCreateWithPersistentNotifications(
	mock pgxmock.PgxPoolIface, persistent bool, rows *pgxmock.Rows,
) {
	args := make([]any, 0, 27)
	for range 25 {
		args = append(args, pgxmock.AnyArg())
	}
	// $26 is the request this test is about; $27 is the interval it schedules
	// against, asserted separately below.
	args = append(args, persistent, pgxmock.AnyArg())
	mock.ExpectQuery(createMsgSQL).WithArgs(args...).WillReturnRows(rows)
}

func persistentRow(id string, now time.Time, persistent bool) []any {
	row := listMessageWithQuoteRow(id, "ws-1", "ch-1", "", now)
	row[persistentColumnIndex()] = persistent
	return row
}

// Both values survive the write: bound as sent, and read back into the domain
// unchanged.
func TestPGXMessageStore_CreateMessage_RoundTripsPersistentNotifications(t *testing.T) {
	for _, persistent := range []bool{false, true} {
		t.Run(map[bool]string{false: "quiet", true: "reminding"}[persistent], func(t *testing.T) {
			mock := newMock(t)
			now := time.Now()
			expectCreateWithPersistentNotifications(mock, persistent,
				pgxmock.NewRows(listMessageWithQuoteCols()).
					AddRow(persistentRow("msg-p825", now, persistent)...))

			msg, err := storage.NewPGXMessageStore(mock).CreateMessage(t.Context(),
				storage.CreateMessageInput{
					WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: "user-1",
					BodyText: "restart the cluster",
					Priority: domain.MessagePriorityUrgent, PersistentNotifications: persistent,
				})
			if err != nil {
				t.Fatalf("CreateMessage: %v", err)
			}
			if msg.PersistentNotifications != persistent {
				t.Fatalf("read back %v, want %v", msg.PersistentNotifications, persistent)
			}
			checkExpectations(t, mock)
		})
	}
}

// The interval is bound from the Go constant, never written as a literal in the
// statement. A number spelled twice is a number that can disagree with itself,
// and the half that drifts is whichever nobody is looking at.
func TestPGXMessageStore_CreateMessage_BindsTheReminderIntervalFromTheContract(t *testing.T) {
	mock := newMock(t)
	now := time.Now()
	args := make([]any, 0, 27)
	for range 26 {
		args = append(args, pgxmock.AnyArg())
	}
	args = append(args, notificationevent.UrgentReminderInterval.Seconds())
	mock.ExpectQuery(createMsgSQL).WithArgs(args...).
		WillReturnRows(pgxmock.NewRows(listMessageWithQuoteCols()).
			AddRow(persistentRow("msg-interval", now, true)...))

	if _, err := storage.NewPGXMessageStore(mock).CreateMessage(t.Context(),
		storage.CreateMessageInput{
			WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: "user-1", BodyText: "x",
			Priority: domain.MessagePriorityUrgent, PersistentNotifications: true,
		}); err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}
	checkExpectations(t, mock)
}

// The recipient snapshot is taken when either flag is set. A message that asks
// only for reminders needs the same per-recipient rows, so the guard inside the
// scan has to admit it — and an ordinary message must still walk no member list.
func TestPGXMessageStore_CreateMessage_ReminderRecipientsUseTheSharedGuard(t *testing.T) {
	mock := newMock(t)
	now := time.Now()
	mock.ExpectQuery(`(?s)eligible_acknowledgement_recipients AS \(.*` +
		`\(\$24::boolean OR \$26::boolean\).*` +
		`INSERT INTO chat\.message_acknowledgements\s*\n?\s*\(message_id, recipient_id, next_reminder_at\).*` +
		`CASE WHEN \$26::boolean AND inserted\.status = 'active'.*` +
		`inserted\.created_at \+ \(\$27 \* interval '1 second'\)`).
		WithArgs(anyCreateArgs()...).
		WillReturnRows(pgxmock.NewRows(listMessageWithQuoteCols()).
			AddRow(persistentRow("msg-guard", now, true)...))

	if _, err := storage.NewPGXMessageStore(mock).CreateMessage(t.Context(),
		storage.CreateMessageInput{
			WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: "user-1", BodyText: "x",
			Priority: domain.MessagePriorityUrgent, PersistentNotifications: true,
		}); err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}
	checkExpectations(t, mock)
}

// A reply and an acknowledgement both clear the schedule in the same write as
// the state. Leaving next_reminder_at behind would keep the row in the
// due-reminder index until the scheduler noticed, which is a window in which a
// person who has already answered can still be paged.
func TestPGXMessageStore_CreateMessage_AReplyClearsTheRepliersSchedule(t *testing.T) {
	mock := newMock(t)
	now := time.Now()
	mock.ExpectQuery(`(?s)answered_acknowledgement AS \(.*` +
		`state = 'responded', resolved_at = now\(\), next_reminder_at = NULL`).
		WithArgs(anyCreateArgs()...).
		WillReturnRows(pgxmock.NewRows(listMessageWithQuoteCols()).
			AddRow(persistentRow("msg-reply", now, false)...))

	if _, err := storage.NewPGXMessageStore(mock).CreateMessage(t.Context(),
		storage.CreateMessageInput{
			WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: "user-1", BodyText: "on it",
			ParentMessageID: "99999999-9999-4999-8999-999999999999",
		}); err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}
	checkExpectations(t, mock)
}

// ── the cancellation (issue #825) ────────────────────────────────────────────

const cancelRemindersSQL = `(?s)WITH authorized AS \(.*` +
	`m\.sender_id = \$3::uuid.*m\.persistent_notifications.*` +
	`stopped AS \(.*UPDATE chat\.message_acknowledgements.*` +
	`next_reminder_at = NULL.*a\.state = 'pending'.*a\.next_reminder_at IS NOT NULL`

func cancelInput() storage.CancelPersistentNotificationsInput {
	return storage.CancelPersistentNotificationsInput{
		WorkspaceID: "11111111-1111-4111-8111-111111111111",
		MessageID:   "22222222-2222-4222-8222-222222222222",
		SenderID:    "33333333-3333-4333-8333-333333333333",
	}
}

// One statement, and the sender is a predicate inside it. Reading first and
// writing on what the read said would be the TOCTOU this shape exists to avoid.
func TestPGXAcknowledgementStore_CancelPersistentNotifications_IsOneAuthorizedStatement(t *testing.T) {
	mock := newMock(t)
	input := cancelInput()
	mock.ExpectQuery(cancelRemindersSQL).
		WithArgs(input.WorkspaceID, input.MessageID, input.SenderID).
		WillReturnRows(pgxmock.NewRows([]string{"authorized", "stopped"}).AddRow(1, 4))

	result, err := storage.NewPGXAcknowledgementStore(mock).
		CancelPersistentNotifications(t.Context(), input)
	if err != nil {
		t.Fatalf("CancelPersistentNotifications: %v", err)
	}
	if result.Stopped != 4 {
		t.Fatalf("stopped = %d, want 4", result.Stopped)
	}
	checkExpectations(t, mock)
}

// A caller the statement did not authorise is told the same thing a caller
// naming a message that does not exist is told.
func TestPGXAcknowledgementStore_CancelPersistentNotifications_UnauthorizedIsNotFound(t *testing.T) {
	mock := newMock(t)
	input := cancelInput()
	mock.ExpectQuery(cancelRemindersSQL).
		WithArgs(input.WorkspaceID, input.MessageID, input.SenderID).
		WillReturnRows(pgxmock.NewRows([]string{"authorized", "stopped"}).AddRow(0, 0))

	_, err := storage.NewPGXAcknowledgementStore(mock).
		CancelPersistentNotifications(t.Context(), input)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	checkExpectations(t, mock)
}

// An authorised call that stopped nothing is a success, not a refusal: it is a
// repeat, or a message whose recipients have all answered.
func TestPGXAcknowledgementStore_CancelPersistentNotifications_AuthorizedNoOpSucceeds(t *testing.T) {
	mock := newMock(t)
	input := cancelInput()
	mock.ExpectQuery(cancelRemindersSQL).
		WithArgs(input.WorkspaceID, input.MessageID, input.SenderID).
		WillReturnRows(pgxmock.NewRows([]string{"authorized", "stopped"}).AddRow(1, 0))

	result, err := storage.NewPGXAcknowledgementStore(mock).
		CancelPersistentNotifications(t.Context(), input)
	if err != nil {
		t.Fatalf("a repeat must not be an error, got %v", err)
	}
	if result.Stopped != 0 {
		t.Fatalf("stopped = %d, want 0", result.Stopped)
	}
	checkExpectations(t, mock)
}

// Deleting a message stops its reminders in the same statement that cancels its
// requests, so there is no commit in which a removed message is still paging
// people.
func TestPGXMessageStore_DeleteMessage_StopsTheReminderSchedule(t *testing.T) {
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
	mock.ExpectExec(`(?s)UPDATE chat\.message_acknowledgements.*`+
		`state = 'cancelled', resolved_at = \$2, next_reminder_at = NULL.*`+
		`state = 'pending'`).
		WithArgs("msg-1", now).
		WillReturnResult(pgxmock.NewResult("UPDATE", 3))
	mock.ExpectQuery(`(?s)SELECT .*FROM chat\.messages m`).
		WithArgs("msg-1", "ws-1", "user-1").
		WillReturnRows(pgxmock.NewRows(listMessageWithQuoteCols()).
			AddRow(persistentRow("msg-1", now, true)...))
	mock.ExpectCommit()

	if _, _, err := storage.NewPGXMessageStore(mock).DeleteMessage(t.Context(), storage.DeleteMessageInput{
		WorkspaceID: "ws-1", MessageID: "msg-1", RequesterID: "user-1",
	}); err != nil {
		t.Fatalf("DeleteMessage: %v", err)
	}
	checkExpectations(t, mock)
}
