package storage_test

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nicrepository/nchat/libs/go/platform/notificationevent"
	"github.com/nicrepository/nchat/libs/go/platform/urlsafety"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// Issue #741, the second producer: a message withheld for a link scan produces
// its notifications when it is promoted, not when it is written.
//
// RF-21's rule is that a withheld message causes no side effect aimed at anyone
// else, so its recipients are parked at creation and released by the promotion —
// in the same statement that makes the message publishable. The regression this
// covers is the one that matters after #741: the parked row now carries a
// classification, and a promoted message must be announced as what it actually
// was. Releasing every recipient as a mention, or publishing the message with no
// notification at all, are both silent.
//
// The whole Link Safety path is driven for real: EnsureLinkScans, the claim, the
// submit, the verdict, then ResolveDecidedMessages. Nothing is inserted into the
// outbox by hand.

// parkedRow is one recipient held back while its message is withheld.
type parkedRow struct {
	UserID   string
	Kind     string
	Priority string
}

func readParked(t *testing.T, pool *pgxpool.Pool, messageID string) []parkedRow {
	t.Helper()
	rows, err := pool.Query(t.Context(), `
		SELECT user_id::text, kind, priority
		FROM chat.message_pending_mentions
		WHERE message_id = $1::uuid
		ORDER BY kind`, messageID)
	if err != nil {
		t.Fatalf("read parked notifications: %v", err)
	}
	defer rows.Close()

	var out []parkedRow
	for rows.Next() {
		var row parkedRow
		if err := rows.Scan(&row.UserID, &row.Kind, &row.Priority); err != nil {
			t.Fatalf("scan parked notification: %v", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate parked notifications: %v", err)
	}
	return out
}

// seedWithheldReply creates a DM reply that is withheld pending a link scan. The
// parent is written by notifyPeer, so the withheld message reaches two people by
// two different rules — a reply for the author it answers, a direct message for
// everybody else still in the conversation. One classification would be
// indistinguishable from a bug; two are not.
func seedWithheldReply(t *testing.T, pool *pgxpool.Pool, store *storage.PGXMessageStore) domain.Message {
	t.Helper()
	ctx := t.Context()
	if err := store.EnsureLinkScans(ctx, []string{notifyScannedURL}); err != nil {
		t.Fatalf("EnsureLinkScans: %v", err)
	}
	parent := mustCreate(t, store, storage.CreateMessageInput{
		WorkspaceID:      notifyWorkspace,
		DMConversationID: notifyConversation,
		SenderID:         notifyPeer,
		BodyText:         "the message being answered",
		BodyFormat:       domain.MessageBodyFormatV3,
	})
	withheld := mustCreate(t, store, storage.CreateMessageInput{
		WorkspaceID:           notifyWorkspace,
		DMConversationID:      notifyConversation,
		SenderID:              notifyAuthor,
		BodyText:              "answering with " + notifyScannedURL,
		BodyFormat:            domain.MessageBodyFormatV3,
		ParentMessageID:       parent.ID,
		Status:                domain.MessageStatusPendingLinkScan,
		LinkScanURLs:          []string{notifyScannedURL},
		LinkSafetyFingerprint: notifyFingerprint,
		IdempotencyKey:        "notify-741-withheld",
	})
	if withheld.Status != domain.MessageStatusPendingLinkScan {
		t.Fatalf("status = %q, want the message withheld", withheld.Status)
	}
	assertRowCount(t, readOutbox(t, pool, withheld.ID), 0)
	return withheld
}

// clearTheScan drives the real worker path to a safe verdict.
func clearTheScan(t *testing.T, store *storage.PGXMessageStore) {
	t.Helper()
	ctx := t.Context()
	job := claimScannedURL(t, store)
	generation, err := store.BeginLinkScanSubmit(ctx, notifyScannedURL, job.SubmitGeneration)
	if err != nil {
		t.Fatalf("BeginLinkScanSubmit: %v", err)
	}
	if err := store.RecordLinkScanSubmission(ctx, notifyScannedURL, "scan-notify-741", generation); err != nil {
		t.Fatalf("RecordLinkScanSubmission: %v", err)
	}
	if err := store.RecordLinkVerdict(ctx, notifyScannedURL, "scan-notify-741", urlsafety.VerdictSafe); err != nil {
		t.Fatalf("RecordLinkVerdict: %v", err)
	}
}

func claimScannedURL(t *testing.T, store *storage.PGXMessageStore) storage.LinkScanJob {
	t.Helper()
	jobs, err := store.ClaimDueLinkScans(t.Context(), 50)
	if err != nil {
		t.Fatalf("ClaimDueLinkScans: %v", err)
	}
	for _, job := range jobs {
		if job.CanonicalURL == notifyScannedURL {
			return job
		}
	}
	t.Fatalf("the withheld message's URL was not claimable among %d jobs", len(jobs))
	return storage.LinkScanJob{}
}

func readMessageStatus(t *testing.T, pool *pgxpool.Pool, messageID string) string {
	t.Helper()
	var status string
	if err := pool.QueryRow(t.Context(),
		`SELECT status FROM chat.messages WHERE id = $1::uuid`, messageID).Scan(&status); err != nil {
		t.Fatalf("read message status: %v", err)
	}
	return status
}

// A withheld message parks its recipients with the classification they were
// given, and tells nobody in the meantime.
func TestNotificationOutboxParksClassifiedRecipientsPostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	withheld := seedWithheldReply(t, pool, storage.NewPGXMessageStore(pool))

	parked := readParked(t, pool, withheld.ID)
	if len(parked) != 2 {
		t.Fatalf("parked %d recipients, want two: %+v", len(parked), parked)
	}
	byUser := map[string]parkedRow{}
	for _, row := range parked {
		byUser[row.UserID] = row
	}
	assertField(t, "the answered author's parked kind", byUser[notifyPeer].Kind,
		string(notificationevent.EventTypeReply))
	assertField(t, "the answered author's parked priority", byUser[notifyPeer].Priority,
		string(notificationevent.PriorityHigh))
	assertField(t, "the other member's parked kind", byUser[notifyThird].Kind,
		string(notificationevent.EventTypeDirectMessage))
	assertField(t, "the other member's parked priority", byUser[notifyThird].Priority,
		string(notificationevent.PriorityNormal))
}

// The promotion: the message becomes observable and its notifications are born
// with it, carrying the classification they were parked with.
func TestNotificationOutboxPromotedFromLinkScanPostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	store := storage.NewPGXMessageStore(pool)
	withheld := seedWithheldReply(t, pool, store)
	clearTheScan(t, store)

	summary, err := store.ResolveDecidedMessages(t.Context())
	if err != nil {
		t.Fatalf("ResolveDecidedMessages: %v", err)
	}
	if summary.Published != 1 || summary.Blocked != 0 {
		t.Fatalf("summary = %+v, want the one withheld message published", summary)
	}
	assertField(t, "message status", readMessageStatus(t, pool, withheld.ID), "active")

	rows := readOutbox(t, pool, withheld.ID)
	assertRowCount(t, rows, 2)
	assertPromotedContract(t, rows, withheld.ID)
}

// assertPromotedContract checks each released row against the same domain
// contract the creating statement obeys — the two producers must be
// indistinguishable to the worker that reads them.
func assertPromotedContract(t *testing.T, rows []outboxRow, messageID string) {
	t.Helper()
	for _, row := range rows {
		eventType := notificationevent.EventTypeDirectMessage
		priority := notificationevent.PriorityNormal
		if row.Recipient == notifyPeer {
			eventType = notificationevent.EventTypeReply
			priority = notificationevent.PriorityHigh
		}
		assertMessageEventContract(t, row, messageID, eventType, priority)
	}
	assertRecipients(t, rows, notifyPeer, notifyThird)
}

// occurred_at is the message's own created_at, not the promotion's. A scan that
// took a minute must not make the event look like it happened a minute later —
// assertMessageEventContract checks the equality, this checks that the promotion
// really did happen later, so the equality is a decision and not a coincidence.
func TestNotificationOutboxPromotionKeepsTheOriginalInstantPostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	store := storage.NewPGXMessageStore(pool)
	withheld := seedWithheldReply(t, pool, store)
	clearTheScan(t, store)
	if _, err := store.ResolveDecidedMessages(t.Context()); err != nil {
		t.Fatalf("ResolveDecidedMessages: %v", err)
	}

	var beforePromotion bool
	if err := pool.QueryRow(t.Context(), `
		SELECT bool_and(o.occurred_at = m.created_at AND o.created_at >= m.created_at)
		FROM chat.notification_outbox o
		JOIN chat.messages m ON m.id = o.message_id
		WHERE o.message_id = $1::uuid`, withheld.ID).Scan(&beforePromotion); err != nil {
		t.Fatalf("compare timestamps: %v", err)
	}
	if !beforePromotion {
		t.Fatal("a released notification must occur when its message did, not when it was released")
	}
}

// Re-running the promotion produces no second notification. The message status
// is put back so the statement genuinely runs again rather than finding nothing
// to do — the dedupe index, not the absence of a second pass, is what holds.
func TestNotificationOutboxPromotionRetryCreatesNoDuplicatePostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	store := storage.NewPGXMessageStore(pool)
	withheld := seedWithheldReply(t, pool, store)
	clearTheScan(t, store)
	ctx := t.Context()

	if _, err := store.ResolveDecidedMessages(ctx); err != nil {
		t.Fatalf("first promotion: %v", err)
	}
	assertRowCount(t, readOutbox(t, pool, withheld.ID), 2)

	if _, err := pool.Exec(ctx,
		`UPDATE chat.messages SET status = 'pending_link_scan' WHERE id = $1::uuid`,
		withheld.ID); err != nil {
		t.Fatalf("re-withhold the message: %v", err)
	}
	summary, err := store.ResolveDecidedMessages(ctx)
	if err != nil {
		t.Fatalf("second promotion: %v", err)
	}
	if summary.Published != 1 {
		t.Fatalf("summary = %+v, want the message promoted again", summary)
	}
	assertRowCount(t, readOutbox(t, pool, withheld.ID), 2)
}

// The promotion and the notifications it releases are one atomic fact. Rolling
// the statement back must leave the message withheld and the outbox empty — a
// commit holding a published message with no notification is exactly the state
// #741 exists to make unreachable.
func TestNotificationOutboxPromotionIsAtomicPostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	store := storage.NewPGXMessageStore(pool)
	withheld := seedWithheldReply(t, pool, store)
	clearTheScan(t, store)
	ctx := t.Context()

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	// pgx.Tx satisfies storage.Pool, so the promotion runs inside a transaction
	// the test controls.
	if _, err := storage.NewPGXMessageStore(tx).ResolveDecidedMessages(ctx); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("ResolveDecidedMessages: %v", err)
	}
	var insideTx int
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM chat.notification_outbox WHERE message_id = $1::uuid`,
		withheld.ID).Scan(&insideTx); err != nil {
		t.Fatalf("count inside transaction: %v", err)
	}
	if insideTx != 2 {
		t.Fatalf("inside the transaction there are %d notifications, want 2", insideTx)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("rollback: %v", err)
	}

	assertField(t, "message status after rollback",
		readMessageStatus(t, pool, withheld.ID), string(domain.MessageStatusPendingLinkScan))
	assertRowCount(t, readOutbox(t, pool, withheld.ID), 0)
}
