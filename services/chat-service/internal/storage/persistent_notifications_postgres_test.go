package storage_test

import (
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nicrepository/nchat/libs/go/platform/notificationevent"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// Issue #825, the chat-service half, against a real database.
//
// Almost nothing here can be proved with a mock, because what is under test is
// what the database owns: a CHECK that makes an unauthorised combination
// unreachable through any path, a schedule written by the same statement as the
// message, conditional UPDATEs that decide which of two concurrent resolutions
// wins, and an authorization predicate that lives inside the statement it
// guards rather than in a read before it.
//
// Opt-in like its neighbours: needs CHAT_TEST_DATABASE_URL against a _test
// database carrying the real migrations, and it reuses issue #741's fixture.

const persistentPriorityConstraint = "messages_persistent_notifications_priority_check"

// remindingChannelMessage is an urgent channel message that asks to keep
// reminding. notifyChannel's explicit members are the author, notifyPeer and
// notifyThird.
func remindingChannelMessage(idempotencyKey string) storage.CreateMessageInput {
	return storage.CreateMessageInput{
		WorkspaceID:             notifyWorkspace,
		ChannelID:               notifyChannel,
		SenderID:                notifyAuthor,
		BodyText:                "restart the cluster before 14h",
		BodyFormat:              domain.MessageBodyFormatV3,
		Priority:                domain.MessagePriorityUrgent,
		PersistentNotifications: true,
		IdempotencyKey:          idempotencyKey,
	}
}

type reminderRow struct {
	Recipient    string
	State        string
	Count        int
	Scheduled    bool
	DueInSeconds float64
}

// readReminderRows reads the schedule straight from the table, bypassing every
// projection, so a test asserts what was actually written.
func readReminderRows(t *testing.T, pool *pgxpool.Pool, messageID string) []reminderRow {
	t.Helper()
	rows, err := pool.Query(t.Context(), `
		SELECT a.recipient_id::text, a.state, a.reminder_count,
		       a.next_reminder_at IS NOT NULL,
		       COALESCE(EXTRACT(EPOCH FROM (a.next_reminder_at - m.created_at)), 0)
		FROM chat.message_acknowledgements a
		JOIN chat.messages m ON m.id = a.message_id
		WHERE a.message_id = $1::uuid
		ORDER BY a.recipient_id`, messageID)
	if err != nil {
		t.Fatalf("read reminder rows: %v", err)
	}
	defer rows.Close()

	var out []reminderRow
	for rows.Next() {
		var row reminderRow
		if err := rows.Scan(&row.Recipient, &row.State, &row.Count,
			&row.Scheduled, &row.DueInSeconds); err != nil {
			t.Fatalf("scan reminder row: %v", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate reminder rows: %v", err)
	}
	return out
}

// ── the intent, and the rule that bounds it ─────────────────────────────────

// A message that asks for nothing keeps behaving exactly as it always did: no
// per-recipient rows at all, so an ordinary send is untouched by this feature.
func TestPersistentNotificationsAbsentLeavesNoScheduleWhatsoeverPostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	store := storage.NewPGXMessageStore(pool)

	msg := mustCreate(t, store, dmInput("persistent-825-none"))

	if rows := readReminderRows(t, pool, msg.ID); len(rows) != 0 {
		t.Fatalf("an ordinary message wrote %d per-recipient rows", len(rows))
	}
}

// An urgent message that did not ask for reminders writes none either. Urgency
// alone is not the request; #820 keeps the two axes apart.
func TestUrgentWithoutPersistentNotificationsSchedulesNothingPostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	input := dmInput("persistent-825-urgent-quiet")
	input.Priority = domain.MessagePriorityUrgent

	msg := mustCreate(t, storage.NewPGXMessageStore(pool), input)

	if rows := readReminderRows(t, pool, msg.ID); len(rows) != 0 {
		t.Fatalf("an urgent message that asked for nothing wrote %d rows", len(rows))
	}
}

// Asking for reminders writes one row per eligible recipient, each pending, each
// with no reminder sent yet and each due exactly one interval after the message
// was created. The sender is not among them.
func TestPersistentNotificationsScheduleTheFirstReminderPostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)

	msg := mustCreate(t, storage.NewPGXMessageStore(pool),
		remindingChannelMessage("persistent-825-initial"))

	rows := readReminderRows(t, pool, msg.ID)
	if len(rows) != 2 {
		t.Fatalf("scheduled %d recipients, want the two channel members who are not the author", len(rows))
	}
	for _, row := range rows {
		if row.Recipient == notifyAuthor {
			t.Fatal("the author must never be reminded about their own message")
		}
		if row.State != "pending" || row.Count != 0 || !row.Scheduled {
			t.Fatalf("row = %+v, want a pending recipient with a schedule and no reminders yet", row)
		}
		if want := notificationevent.UrgentReminderInterval.Seconds(); row.DueInSeconds != want {
			t.Fatalf("first reminder due %.0fs after the send, want %.0fs", row.DueInSeconds, want)
		}
	}
}

// The database refuses reminders on a message that is not urgent, whatever
// wrote it. The service refuses it first; this is what makes the combination
// unreachable through an importer, a repair script or a psql session too.
func TestPersistentNotificationsRequireUrgentInTheSchemaPostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)

	_, err := pool.Exec(t.Context(), `
		INSERT INTO chat.messages
			(workspace_id, channel_id, sender_id, kind, body_text, body_format, status,
			 priority, persistent_notifications)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'user', 'not urgent', 'v3', 'active',
		        'important', true)`,
		notifyWorkspace, notifyChannel, notifyAuthor)

	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.ConstraintName != persistentPriorityConstraint {
		t.Fatalf("err = %v, want a violation of %s", err, persistentPriorityConstraint)
	}
}

// ── only PENDING keeps a schedule ───────────────────────────────────────────

// Replying resolves the replier and stops their reminders, in the same
// statement, and reaches nobody else. In a group the other recipients keep
// being asked — #820 is explicit that one person's action must not end
// everybody's.
func TestReplyStopsOnlyTheRepliersRemindersPostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	store := storage.NewPGXMessageStore(pool)
	original := mustCreate(t, store, remindingChannelMessage("persistent-825-reply-original"))

	mustCreate(t, store, storage.CreateMessageInput{
		WorkspaceID:     notifyWorkspace,
		ChannelID:       notifyChannel,
		SenderID:        notifyPeer,
		BodyText:        "on it",
		BodyFormat:      domain.MessageBodyFormatV3,
		ParentMessageID: original.ID,
		IdempotencyKey:  "test",
	})

	for _, row := range readReminderRows(t, pool, original.ID) {
		switch row.Recipient {
		case notifyPeer:
			if row.State != "responded" || row.Scheduled {
				t.Fatalf("the replier = %+v, want responded with no schedule left", row)
			}
		default:
			if row.State != "pending" || !row.Scheduled {
				t.Fatalf("recipient %+v lost their reminders because somebody else replied", row)
			}
		}
	}
}

// Acknowledging does the same for the acknowledger: the schedule is cleared in
// the same write as the state, so the row leaves the due-reminder index at the
// instant it stops being pending rather than at the scheduler's next pass.
func TestAcknowledgementStopsTheAcknowledgersRemindersPostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	input := remindingChannelMessage("persistent-825-ack")
	input.AcknowledgementRequired = true
	msg := mustCreate(t, storage.NewPGXMessageStore(pool), input)

	if _, err := storage.NewPGXAcknowledgementStore(pool).Acknowledge(t.Context(),
		storage.AcknowledgeInput{
			WorkspaceID: notifyWorkspace, MessageID: msg.ID, RecipientID: notifyPeer,
		}); err != nil {
		t.Fatalf("Acknowledge: %v", err)
	}

	for _, row := range readReminderRows(t, pool, msg.ID) {
		if row.Recipient == notifyPeer && (row.State != "acknowledged" || row.Scheduled) {
			t.Fatalf("the acknowledger = %+v, want acknowledged with no schedule left", row)
		}
		if row.Recipient == notifyThird && (row.State != "pending" || !row.Scheduled) {
			t.Fatalf("recipient %+v lost their reminders because somebody else confirmed", row)
		}
	}
}

// Deleting the message stops every outstanding reminder, in the same
// transaction as the delete. There is no commit in which a withdrawn message is
// still paging people.
func TestDeletingAMessageStopsItsRemindersPostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	store := storage.NewPGXMessageStore(pool)
	msg := mustCreate(t, store, remindingChannelMessage("persistent-825-delete"))

	if _, _, err := store.DeleteMessage(t.Context(), storage.DeleteMessageInput{
		WorkspaceID: notifyWorkspace, MessageID: msg.ID, RequesterID: notifyAuthor,
	}); err != nil {
		t.Fatalf("DeleteMessage: %v", err)
	}

	for _, row := range readReminderRows(t, pool, msg.ID) {
		if row.State != "cancelled" || row.Scheduled {
			t.Fatalf("row = %+v, want cancelled with no schedule left", row)
		}
	}
}

// ── the #824 endpoints are unchanged by rows they do not own ────────────────

// A message that asked only for reminders answers the acknowledgement read
// exactly as a message that asked for nothing does: all counts zero, no viewer
// state. Without the gate, #824's documented contract — "required false means
// all counts zero" — would quietly stop holding.
func TestRemindersDoNotAppearInTheAcknowledgementSummaryPostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	msg := mustCreate(t, storage.NewPGXMessageStore(pool),
		remindingChannelMessage("persistent-825-summary"))

	summary, err := storage.NewPGXAcknowledgementStore(pool).ReadAcknowledgement(t.Context(),
		storage.ReadAcknowledgementInput{
			WorkspaceID: notifyWorkspace, MessageID: msg.ID, ViewerID: notifyPeer,
		})
	if err != nil {
		t.Fatalf("ReadAcknowledgement: %v", err)
	}
	if summary.Required || summary.Total != 0 || summary.Pending != 0 {
		t.Fatalf("summary = %+v, want the answer a message that asked for nothing gives", summary)
	}
	if summary.ViewerState != "" {
		t.Fatalf("viewer state = %q, want empty: this message asked them nothing", summary.ViewerState)
	}
}

// And the write half: a recipient cannot acknowledge a message that never asked
// them to. Without the same gate on the UPDATE they would resolve their own row
// — silently stopping their reminders — through an endpoint that then answers
// 404.
func TestARemindedRecipientCannotAcknowledgePostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	msg := mustCreate(t, storage.NewPGXMessageStore(pool),
		remindingChannelMessage("persistent-825-no-ack"))

	_, err := storage.NewPGXAcknowledgementStore(pool).Acknowledge(t.Context(),
		storage.AcknowledgeInput{
			WorkspaceID: notifyWorkspace, MessageID: msg.ID, RecipientID: notifyPeer,
		})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	for _, row := range readReminderRows(t, pool, msg.ID) {
		if row.State != "pending" || !row.Scheduled {
			t.Fatalf("a refused acknowledgement changed a row: %+v", row)
		}
	}
}

// ── the sender's cancellation ───────────────────────────────────────────────

func cancelReminders(
	t *testing.T, pool *pgxpool.Pool, messageID, actor string,
) (storage.CancelPersistentNotificationsResult, error) {
	t.Helper()
	return storage.NewPGXAcknowledgementStore(pool).CancelPersistentNotifications(t.Context(),
		storage.CancelPersistentNotificationsInput{
			WorkspaceID: notifyWorkspace, MessageID: messageID, SenderID: actor,
		})
}

// The sender stops every outstanding reminder and is told how many. On a message
// that asked for nothing else, the recipients become CANCELLED — the state
// #820's machine specifies for a withdrawn request.
func TestOnlyTheSenderCanCancelRemindersPostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	msg := mustCreate(t, storage.NewPGXMessageStore(pool),
		remindingChannelMessage("persistent-825-cancel"))

	result, err := cancelReminders(t, pool, msg.ID, notifyAuthor)
	if err != nil {
		t.Fatalf("CancelPersistentNotifications: %v", err)
	}
	if result.Stopped != 2 {
		t.Fatalf("stopped %d, want both recipients", result.Stopped)
	}
	for _, row := range readReminderRows(t, pool, msg.ID) {
		if row.State != "cancelled" || row.Scheduled {
			t.Fatalf("row = %+v, want cancelled with no schedule left", row)
		}
	}
}

// A recipient of the message cannot cancel it, and learns nothing from trying:
// the same answer a message that does not exist gives.
func TestARecipientCannotCancelSomebodyElsesRemindersPostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	msg := mustCreate(t, storage.NewPGXMessageStore(pool),
		remindingChannelMessage("persistent-825-cancel-other"))

	if _, err := cancelReminders(t, pool, msg.ID, notifyPeer); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	for _, row := range readReminderRows(t, pool, msg.ID) {
		if row.State != "pending" || !row.Scheduled {
			t.Fatalf("a refused cancellation changed a row: %+v", row)
		}
	}
}

// A message id from another tenant is unreachable even for a caller who is its
// sender there. The workspace comes from the resolved session and is a predicate
// of the same statement, so there is no id a client can present to cross the
// boundary.
func TestCancellingRemindersIsScopedToTheWorkspacePostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	input := remindingChannelMessage("persistent-825-cancel-tenant")
	msg := mustCreate(t, storage.NewPGXMessageStore(pool), input)

	_, err := storage.NewPGXAcknowledgementStore(pool).CancelPersistentNotifications(t.Context(),
		storage.CancelPersistentNotificationsInput{
			// The other workspace the fixture seeds, with the author's own id.
			WorkspaceID: notifySecondWS, MessageID: msg.ID, SenderID: notifyAuthor,
		})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	for _, row := range readReminderRows(t, pool, msg.ID) {
		if !row.Scheduled {
			t.Fatalf("a cross-tenant cancellation reached a row: %+v", row)
		}
	}
}

// Cancelling twice is safe. The second call finds nothing left to stop, reports
// zero, and reopens nothing.
func TestCancellingRemindersIsIdempotentPostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	msg := mustCreate(t, storage.NewPGXMessageStore(pool),
		remindingChannelMessage("persistent-825-cancel-twice"))

	if _, err := cancelReminders(t, pool, msg.ID, notifyAuthor); err != nil {
		t.Fatalf("first cancellation: %v", err)
	}
	second, err := cancelReminders(t, pool, msg.ID, notifyAuthor)
	if err != nil {
		t.Fatalf("second cancellation: %v", err)
	}
	if second.Stopped != 0 {
		t.Fatalf("second cancellation stopped %d, want 0", second.Stopped)
	}
}

// Cancelling reminders never erases an answer already given. A recipient who
// confirmed before the sender changed their mind stays confirmed, and their
// resolution instant is not rewritten.
func TestCancellingRemindersPreservesAnswersAlreadyGivenPostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	input := remindingChannelMessage("persistent-825-cancel-preserve")
	input.AcknowledgementRequired = true
	msg := mustCreate(t, storage.NewPGXMessageStore(pool), input)

	if _, err := storage.NewPGXAcknowledgementStore(pool).Acknowledge(t.Context(),
		storage.AcknowledgeInput{
			WorkspaceID: notifyWorkspace, MessageID: msg.ID, RecipientID: notifyPeer,
		}); err != nil {
		t.Fatalf("Acknowledge: %v", err)
	}
	if _, err := cancelReminders(t, pool, msg.ID, notifyAuthor); err != nil {
		t.Fatalf("CancelPersistentNotifications: %v", err)
	}

	for _, row := range readReminderRows(t, pool, msg.ID) {
		if row.Recipient == notifyPeer && row.State != "acknowledged" {
			t.Fatalf("the acknowledger became %q; a cancellation must not erase an answer", row.State)
		}
		// The message also asked for confirmation, so the question is still open
		// for whoever has not answered: only the reminding stops.
		if row.Recipient == notifyThird {
			if row.State != "pending" {
				t.Fatalf("state = %q, want the confirmation request still open", row.State)
			}
			if row.Scheduled {
				t.Fatal("the reminder schedule must be gone even though the request stands")
			}
		}
	}
}

// A message that never asked for reminders cannot be cancelled at all, and the
// refusal is the same one an unknown id gets.
func TestCancellingRemindersOnAQuietMessageIsNotFoundPostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	msg := mustCreate(t, storage.NewPGXMessageStore(pool), dmInput("persistent-825-cancel-quiet"))

	if _, err := cancelReminders(t, pool, msg.ID, notifyAuthor); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// ── the withheld message ────────────────────────────────────────────────────

// seedWithheldReminder creates an urgent, reminding message that is withheld
// pending a real link scan, using the same path RF-21 uses in production.
func seedWithheldReminder(
	t *testing.T, store *storage.PGXMessageStore, idempotencyKey string,
) domain.Message {
	t.Helper()
	if err := store.EnsureLinkScans(t.Context(), []string{notifyScannedURL}); err != nil {
		t.Fatalf("EnsureLinkScans: %v", err)
	}
	input := remindingChannelMessage(idempotencyKey)
	input.BodyText = "restart the cluster, see " + notifyScannedURL
	input.Status = domain.MessageStatusPendingLinkScan
	input.LinkScanURLs = []string{notifyScannedURL}
	input.LinkSafetyFingerprint = notifyFingerprint
	msg := mustCreate(t, store, input)
	if msg.Status != domain.MessageStatusPendingLinkScan {
		t.Fatalf("status = %q, want the message withheld", msg.Status)
	}
	return msg
}

// A message withheld for a link scan schedules nothing: a reminder is the
// loudest side effect there is, and RF-21's rule is that a message nobody may
// see yet produces none of them.
func TestAWithheldMessageSchedulesNoRemindersPostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	msg := seedWithheldReminder(t, storage.NewPGXMessageStore(pool), "persistent-825-withheld")

	rows := readReminderRows(t, pool, msg.ID)
	if len(rows) != 2 {
		t.Fatalf("wrote %d recipient rows, want the snapshot of who was asked", len(rows))
	}
	for _, row := range rows {
		if row.Scheduled {
			t.Fatalf("a withheld message scheduled a reminder: %+v", row)
		}
	}
}

// Publishing it starts the clock, counted from the promotion rather than from
// the send: a scan that took ten minutes must not make the first reminder due
// the instant the message appears.
func TestPublishingAWithheldMessageStartsItsRemindersPostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	store := storage.NewPGXMessageStore(pool)
	msg := seedWithheldReminder(t, store, "persistent-825-promote")

	clearTheScan(t, store)
	if _, err := store.ResolveDecidedMessages(t.Context()); err != nil {
		t.Fatalf("ResolveDecidedMessages: %v", err)
	}

	for _, row := range readReminderRows(t, pool, msg.ID) {
		if !row.Scheduled {
			t.Fatalf("publishing did not start the reminders: %+v", row)
		}
		// Counted from now(), which is after created_at, so the gap is strictly
		// larger than one interval.
		if row.DueInSeconds <= notificationevent.UrgentReminderInterval.Seconds() {
			t.Fatalf("first reminder due %.0fs after the send; the clock must start at the promotion",
				row.DueInSeconds)
		}
	}
}

// A second promotion pass does not push the schedule further out. Without the
// compare-and-set a message promoted twice would have its first reminder
// delayed by another interval each time.
func TestPromotingTwiceDoesNotRestartTheReminderClockPostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	store := storage.NewPGXMessageStore(pool)
	msg := seedWithheldReminder(t, store, "persistent-825-promote-twice")

	clearTheScan(t, store)
	if _, err := store.ResolveDecidedMessages(t.Context()); err != nil {
		t.Fatalf("first resolve: %v", err)
	}
	first := readReminderRows(t, pool, msg.ID)
	time.Sleep(5 * time.Millisecond)
	if _, err := store.ResolveDecidedMessages(t.Context()); err != nil {
		t.Fatalf("second resolve: %v", err)
	}

	second := readReminderRows(t, pool, msg.ID)
	for i := range first {
		if first[i].DueInSeconds != second[i].DueInSeconds {
			t.Fatalf("a second promotion moved the schedule from %.3f to %.3f",
				first[i].DueInSeconds, second[i].DueInSeconds)
		}
	}
}
