package storage_test

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// Issue #824: per-recipient acknowledgement against a real database.
//
// Almost nothing in this file can be proved with a mock, because what is under
// test is what the database owns. The primary key is the reason two clicks
// cannot become two rows. The conditional UPDATE is the reason an
// acknowledgement racing a reply or a deletion converges instead of
// overwriting. The recipient snapshot is a set derived in SQL, in the same
// statement as the INSERT, from membership a request never names. A pgxmock
// agrees with whatever it is handed, including a recipient list somebody
// computed in Go.
//
// Opt-in like its neighbours: needs CHAT_TEST_DATABASE_URL against a _test
// database carrying the real migrations, and it reuses issue #741's fixture
// rather than seeding a second workspace of its own.

const acknowledgementCheckConstraint = "message_acknowledgements_state_check"

// askingChannelMessage is an urgent channel message that asks its recipients to
// confirm. notifyChannel's explicit members are the author, notifyPeer and
// notifyThird; notifyOutsider can read the channel but never joined it.
func askingChannelMessage(idempotencyKey string) storage.CreateMessageInput {
	return storage.CreateMessageInput{
		WorkspaceID:             notifyWorkspace,
		ChannelID:               notifyChannel,
		SenderID:                notifyAuthor,
		BodyText:                "restart the cluster before 14h",
		BodyFormat:              domain.MessageBodyFormatV3,
		Priority:                domain.MessagePriorityUrgent,
		AcknowledgementRequired: true,
		IdempotencyKey:          idempotencyKey,
	}
}

// askingDMMessage is the same request inside issue #741's group conversation,
// whose active members are the author, notifyPeer and notifyThird.
func askingDMMessage(idempotencyKey string) storage.CreateMessageInput {
	input := dmInput(idempotencyKey)
	input.AcknowledgementRequired = true
	input.Priority = domain.MessagePriorityUrgent
	return input
}

type acknowledgementRowState struct {
	Recipient string
	State     string
	Resolved  bool
}

// readAcknowledgementRows reads one message's rows straight from the table,
// bypassing the store, so a test asserts what was actually written rather than
// what a projection chose to show.
func readAcknowledgementRows(t *testing.T, pool *pgxpool.Pool, messageID string) []acknowledgementRowState {
	t.Helper()
	rows, err := pool.Query(t.Context(), `
		SELECT recipient_id::text, state, resolved_at IS NOT NULL
		FROM chat.message_acknowledgements
		WHERE message_id = $1
		ORDER BY recipient_id`, messageID)
	if err != nil {
		t.Fatalf("read acknowledgement rows: %v", err)
	}
	defer rows.Close()
	var out []acknowledgementRowState
	for rows.Next() {
		var row acknowledgementRowState
		if err := rows.Scan(&row.Recipient, &row.State, &row.Resolved); err != nil {
			t.Fatalf("scan acknowledgement row: %v", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate acknowledgement rows: %v", err)
	}
	return out
}

func recipientIDs(rows []acknowledgementRowState) []string {
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.Recipient)
	}
	sort.Strings(out)
	return out
}

func assertAskedRecipients(t *testing.T, rows []acknowledgementRowState, want ...string) {
	t.Helper()
	sort.Strings(want)
	got := recipientIDs(rows)
	if len(got) != len(want) {
		t.Fatalf("recipients = %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("recipients = %v, want %v", got, want)
		}
	}
}

func assertRecipientState(t *testing.T, rows []acknowledgementRowState, recipient, want string) {
	t.Helper()
	for _, row := range rows {
		if row.Recipient != recipient {
			continue
		}
		if row.State != want {
			t.Fatalf("recipient %s is %q, want %q", recipient, row.State, want)
		}
		if (want == string(domain.AcknowledgementStatePending)) == row.Resolved {
			t.Fatalf("recipient %s is %q with resolved_at present=%v", recipient, row.State, row.Resolved)
		}
		return
	}
	t.Fatalf("recipient %s has no row", recipient)
}

// ── the snapshot ─────────────────────────────────────────────────────────────

// A channel message asks the channel's members, never everyone who could read
// it. notifyOutsider is an active workspace member and a public channel is
// visible to them, but they never joined — and "everybody who could have read
// it" is not a set anybody agreed to be answerable for.
func TestAcknowledgementAsksChannelMembersNotEveryReaderPostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	msg := mustCreate(t, storage.NewPGXMessageStore(pool), askingChannelMessage("824-channel-snapshot"))

	rows := readAcknowledgementRows(t, pool, msg.ID)
	assertAskedRecipients(t, rows, notifyPeer, notifyThird)
	for _, row := range rows {
		if row.State != string(domain.AcknowledgementStatePending) || row.Resolved {
			t.Fatalf("a freshly asked recipient is %+v, want pending with no instant", row)
		}
	}
}

// A conversation asks its active members. Same rule, different container.
func TestAcknowledgementAsksConversationMembersPostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	msg := mustCreate(t, storage.NewPGXMessageStore(pool), askingDMMessage("824-dm-snapshot"))

	assertAskedRecipients(t, readAcknowledgementRows(t, pool, msg.ID), notifyPeer, notifyThird)
}

// Nobody confirms receipt of their own message, and a send that asked for
// nothing writes nothing at all — which is what keeps this feature free for
// almost every message in the product.
func TestAcknowledgementAsksNobodyWhenItWasNotRequestedPostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	store := storage.NewPGXMessageStore(pool)

	plain := mustCreate(t, store, dmInput("824-not-requested"))
	if rows := readAcknowledgementRows(t, pool, plain.ID); len(rows) != 0 {
		t.Fatalf("an ordinary send created %d acknowledgement rows", len(rows))
	}
	asking := mustCreate(t, store, askingChannelMessage("824-excludes-author"))
	for _, row := range readAcknowledgementRows(t, pool, asking.ID) {
		if row.Recipient == notifyAuthor {
			t.Fatal("the author was asked to confirm their own message")
		}
	}
}

// The snapshot is history. Somebody who joins afterwards was never asked, and
// somebody who leaves was still asked — deriving the list from live membership
// on every read would make "4 of 7 confirmed" mean a different thing each time
// it is rendered.
func TestAcknowledgementSnapshotDoesNotFollowMembershipPostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	msg := mustCreate(t, storage.NewPGXMessageStore(pool), askingChannelMessage("824-snapshot-is-history"))

	if _, err := pool.Exec(t.Context(),
		`INSERT INTO chat.channel_members (channel_id, user_id) VALUES ($1, $2)`,
		notifyChannel, notifyOutsider); err != nil {
		t.Fatalf("add a member after the send: %v", err)
	}
	if _, err := pool.Exec(t.Context(),
		`DELETE FROM chat.channel_members WHERE channel_id = $1 AND user_id = $2`,
		notifyChannel, notifyThird); err != nil {
		t.Fatalf("remove a member after the send: %v", err)
	}

	rows := readAcknowledgementRows(t, pool, msg.ID)
	assertAskedRecipients(t, rows, notifyPeer, notifyThird)
}

// ── uniqueness ───────────────────────────────────────────────────────────────

// A recipient is their row. The primary key is what refuses a second one, and
// it refuses it for a writer that never goes through the store at all.
func TestAcknowledgementIsUniquePerRecipientPostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	msg := mustCreate(t, storage.NewPGXMessageStore(pool), askingChannelMessage("824-unique"))

	_, err := pool.Exec(t.Context(),
		`INSERT INTO chat.message_acknowledgements (message_id, recipient_id) VALUES ($1, $2)`,
		msg.ID, notifyPeer)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		t.Fatalf("expected a unique violation, got %v", err)
	}
}

// ── idempotency and concurrency ──────────────────────────────────────────────

func acknowledgeAs(t *testing.T, pool *pgxpool.Pool, messageID, recipient string) (domain.AcknowledgementSummary, error) {
	t.Helper()
	result, err := acknowledgeResultAs(t, pool, messageID, recipient)
	return result.Summary, err
}

func acknowledgeResultAs(
	t *testing.T, pool *pgxpool.Pool, messageID, recipient string,
) (storage.AcknowledgeResult, error) {
	t.Helper()
	return storage.NewPGXAcknowledgementStore(pool).Acknowledge(t.Context(), storage.AcknowledgeInput{
		WorkspaceID: notifyWorkspace, MessageID: messageID, RecipientID: recipient,
	})
}

// Two acknowledgements are one acknowledgement, and the second does not move
// the instant the first recorded.
func TestAcknowledgeIsIdempotentPostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	msg := mustCreate(t, storage.NewPGXMessageStore(pool), askingChannelMessage("824-idempotent"))

	if _, err := acknowledgeAs(t, pool, msg.ID, notifyPeer); err != nil {
		t.Fatalf("first acknowledgement: %v", err)
	}
	var first time.Time
	if err := pool.QueryRow(t.Context(),
		`SELECT resolved_at FROM chat.message_acknowledgements WHERE message_id = $1 AND recipient_id = $2`,
		msg.ID, notifyPeer).Scan(&first); err != nil {
		t.Fatalf("read the first instant: %v", err)
	}

	summary, err := acknowledgeAs(t, pool, msg.ID, notifyPeer)
	if err != nil {
		t.Fatalf("second acknowledgement: %v", err)
	}
	if summary.ViewerState != domain.AcknowledgementStateAcknowledged {
		t.Fatalf("second answer = %q, want acknowledged", summary.ViewerState)
	}
	var second time.Time
	if err := pool.QueryRow(t.Context(),
		`SELECT resolved_at FROM chat.message_acknowledgements WHERE message_id = $1 AND recipient_id = $2`,
		msg.ID, notifyPeer).Scan(&second); err != nil {
		t.Fatalf("read the second instant: %v", err)
	}
	if !first.Equal(second) {
		t.Fatalf("a repeated acknowledgement moved the instant from %v to %v", first, second)
	}
	if rows := readAcknowledgementRows(t, pool, msg.ID); len(rows) != 2 {
		t.Fatalf("acknowledging twice produced %d rows, want 2 recipients", len(rows))
	}
}

// Genuinely concurrent acknowledgements from one person converge on one row in
// one state, with no lock and no idempotency key: the conditional UPDATE is the
// whole mechanism.
func TestAcknowledgeConcurrentCallsConvergePostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	msg := mustCreate(t, storage.NewPGXMessageStore(pool), askingChannelMessage("824-concurrent"))

	const attempts = 8
	var wg sync.WaitGroup
	errs := make([]error, attempts)
	start := make(chan struct{})
	for i := range attempts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, errs[i] = storage.NewPGXAcknowledgementStore(pool).Acknowledge(context.Background(),
				storage.AcknowledgeInput{
					WorkspaceID: notifyWorkspace, MessageID: msg.ID, RecipientID: notifyPeer,
				})
		}()
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent acknowledgement %d failed: %v", i, err)
		}
	}
	rows := readAcknowledgementRows(t, pool, msg.ID)
	assertRecipientState(t, rows, notifyPeer, string(domain.AcknowledgementStateAcknowledged))
	assertRecipientState(t, rows, notifyThird, string(domain.AcknowledgementStatePending))
}

// ── the other transitions ────────────────────────────────────────────────────

// Replying resolves the replier and nobody else. In a group one person's answer
// must not end everybody's request.
func TestReplyResolvesOnlyTheReplierPostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	store := storage.NewPGXMessageStore(pool)
	msg := mustCreate(t, store, askingDMMessage("824-reply"))

	reply := dmInput("824-reply-answer")
	reply.SenderID = notifyPeer
	reply.ParentMessageID = msg.ID
	mustCreate(t, store, reply)

	rows := readAcknowledgementRows(t, pool, msg.ID)
	assertRecipientState(t, rows, notifyPeer, string(domain.AcknowledgementStateResponded))
	assertRecipientState(t, rows, notifyThird, string(domain.AcknowledgementStatePending))
}

// A reply arrives, then the same person presses confirm. The terminal state
// stands: an acknowledgement never rewrites a resolution that already happened,
// and the caller is told what actually holds rather than refused.
func TestAcknowledgeAfterAReplyKeepsTheReplyPostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	store := storage.NewPGXMessageStore(pool)
	msg := mustCreate(t, store, askingDMMessage("824-reply-then-ack"))

	reply := dmInput("824-reply-then-ack-answer")
	reply.SenderID = notifyPeer
	reply.ParentMessageID = msg.ID
	mustCreate(t, store, reply)

	summary, err := acknowledgeAs(t, pool, msg.ID, notifyPeer)
	if err != nil {
		t.Fatalf("Acknowledge after replying: %v", err)
	}
	if summary.ViewerState != domain.AcknowledgementStateResponded {
		t.Fatalf("answer = %q, want the responded state that already held", summary.ViewerState)
	}
	assertRecipientState(t, readAcknowledgementRows(t, pool, msg.ID),
		notifyPeer, string(domain.AcknowledgementStateResponded))
}

// Deleting the message withdraws the pending requests and leaves the answers
// alone. A sender who removes a message does not thereby erase that somebody
// had already confirmed it.
func TestDeleteCancelsPendingAndKeepsAnswersPostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	store := storage.NewPGXMessageStore(pool)
	msg := mustCreate(t, store, askingChannelMessage("824-delete"))

	if _, err := acknowledgeAs(t, pool, msg.ID, notifyPeer); err != nil {
		t.Fatalf("Acknowledge: %v", err)
	}
	if _, _, err := store.DeleteMessage(t.Context(), storage.DeleteMessageInput{
		WorkspaceID: notifyWorkspace, MessageID: msg.ID, RequesterID: notifyAuthor,
	}); err != nil {
		t.Fatalf("DeleteMessage: %v", err)
	}

	rows := readAcknowledgementRows(t, pool, msg.ID)
	assertRecipientState(t, rows, notifyPeer, string(domain.AcknowledgementStateAcknowledged))
	assertRecipientState(t, rows, notifyThird, string(domain.AcknowledgementStateCancelled))
}

// An acknowledgement racing a deletion converges: whichever committed first
// stands, the loser changes nothing, and the row is never left in two minds.
func TestAcknowledgeRacingDeleteHasOneWinnerPostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	store := storage.NewPGXMessageStore(pool)
	msg := mustCreate(t, store, askingChannelMessage("824-ack-vs-delete"))

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = storage.NewPGXAcknowledgementStore(pool).Acknowledge(context.Background(),
			storage.AcknowledgeInput{
				WorkspaceID: notifyWorkspace, MessageID: msg.ID, RecipientID: notifyPeer,
			})
	}()
	go func() {
		defer wg.Done()
		_, _, _ = store.DeleteMessage(context.Background(), storage.DeleteMessageInput{
			WorkspaceID: notifyWorkspace, MessageID: msg.ID, RequesterID: notifyAuthor,
		})
	}()
	wg.Wait()

	rows := readAcknowledgementRows(t, pool, msg.ID)
	for _, row := range rows {
		if row.Recipient != notifyPeer {
			continue
		}
		if row.State != string(domain.AcknowledgementStateAcknowledged) &&
			row.State != string(domain.AcknowledgementStateCancelled) {
			t.Fatalf("the raced recipient settled on %q, want one of the two terminal outcomes", row.State)
		}
		if !row.Resolved {
			t.Fatalf("the raced recipient is %q with no resolution instant", row.State)
		}
	}
	// Whatever the race decided, the other recipient's request was withdrawn by
	// the delete and nothing reopened it.
	assertRecipientState(t, rows, notifyThird, string(domain.AcknowledgementStateCancelled))
}

// Editing the body changes nothing about who answered. This is the rule #824
// states, and it holds because the edit statement does not name this table at
// all.
func TestEditDoesNotResetAcknowledgementPostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	store := storage.NewPGXMessageStore(pool)
	msg := mustCreate(t, store, askingChannelMessage("824-edit"))

	if _, err := acknowledgeAs(t, pool, msg.ID, notifyPeer); err != nil {
		t.Fatalf("Acknowledge: %v", err)
	}
	if _, err := store.EditMessage(t.Context(), storage.EditMessageInput{
		WorkspaceID: notifyWorkspace, MessageID: msg.ID, EditorID: notifyAuthor,
		Body: "restart the cluster before 15h", BodyFormat: domain.MessageBodyFormatV3,
	}); err != nil {
		t.Fatalf("EditMessage: %v", err)
	}

	rows := readAcknowledgementRows(t, pool, msg.ID)
	assertRecipientState(t, rows, notifyPeer, string(domain.AcknowledgementStateAcknowledged))
	assertRecipientState(t, rows, notifyThird, string(domain.AcknowledgementStatePending))
}

// Reading is not answering. #820 keeps DELIVERED, READ and ACKNOWLEDGED apart,
// and this is the proof at the layer where it would be easiest to conflate them.
func TestReadingAMessageDoesNotAcknowledgeItPostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	store := storage.NewPGXMessageStore(pool)
	msg := mustCreate(t, store, askingChannelMessage("824-read-is-not-ack"))

	if _, err := store.GetMessageByIDInWorkspace(t.Context(), notifyWorkspace, msg.ID, notifyPeer); err != nil {
		t.Fatalf("GetMessageByIDInWorkspace: %v", err)
	}
	if _, err := storage.NewPGXAcknowledgementStore(pool).ReadAcknowledgement(t.Context(),
		storage.ReadAcknowledgementInput{
			WorkspaceID: notifyWorkspace, MessageID: msg.ID, ViewerID: notifyPeer,
		}); err != nil {
		t.Fatalf("ReadAcknowledgement: %v", err)
	}
	assertRecipientState(t, readAcknowledgementRows(t, pool, msg.ID),
		notifyPeer, string(domain.AcknowledgementStatePending))
}

// ── the summary and its authorisation ────────────────────────────────────────

func readAs(t *testing.T, pool *pgxpool.Pool, messageID, viewer string) (domain.AcknowledgementSummary, error) {
	t.Helper()
	return storage.NewPGXAcknowledgementStore(pool).ReadAcknowledgement(t.Context(),
		storage.ReadAcknowledgementInput{
			WorkspaceID: notifyWorkspace, MessageID: messageID, ViewerID: viewer,
		})
}

// The sender sees the aggregate and the per-recipient detail; a recipient sees
// the aggregate and their own state and no list at all.
func TestAcknowledgementSummaryAndDetailPostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	msg := mustCreate(t, storage.NewPGXMessageStore(pool), askingChannelMessage("824-summary"))
	if _, err := acknowledgeAs(t, pool, msg.ID, notifyPeer); err != nil {
		t.Fatalf("Acknowledge: %v", err)
	}
	assertSenderSeesTheDetail(t, pool, msg.ID)
	assertRecipientSeesOnlyTheAggregate(t, pool, msg.ID)
}

// The sender gets the aggregate and the per-recipient list, and no state of
// their own: nobody confirms receipt of their own message.
func assertSenderSeesTheDetail(t *testing.T, pool *pgxpool.Pool, messageID string) {
	t.Helper()
	sender, err := readAs(t, pool, messageID, notifyAuthor)
	if err != nil {
		t.Fatalf("sender read: %v", err)
	}
	if !sender.Required || sender.Total != 2 || sender.Acknowledged != 1 || sender.Pending != 1 {
		t.Fatalf("sender summary = %+v, want 1 of 2 confirmed", sender)
	}
	if sender.Resolved() != 1 {
		t.Fatalf("sender resolved count = %d, want 1", sender.Resolved())
	}
	if sender.ViewerState != "" {
		t.Fatalf("the sender has a state of their own: %q — they were never asked", sender.ViewerState)
	}
	if len(sender.Recipients) != 2 {
		t.Fatalf("sender detail has %d rows, want 2", len(sender.Recipients))
	}
}

// A recipient gets the same aggregate and their own state, and no list at all:
// who personally has not answered is the sender's information.
func assertRecipientSeesOnlyTheAggregate(t *testing.T, pool *pgxpool.Pool, messageID string) {
	t.Helper()
	recipient, err := readAs(t, pool, messageID, notifyThird)
	if err != nil {
		t.Fatalf("recipient read: %v", err)
	}
	if recipient.Total != 2 || recipient.Acknowledged != 1 {
		t.Fatalf("recipient summary = %+v, want the same aggregate", recipient)
	}
	if recipient.ViewerState != domain.AcknowledgementStatePending {
		t.Fatalf("recipient viewer state = %q, want pending", recipient.ViewerState)
	}
	if len(recipient.Recipients) != 0 {
		t.Fatalf("a recipient received %d rows of detail; only the sender may see who answered",
			len(recipient.Recipients))
	}
}

// ── authorisation ────────────────────────────────────────────────────────────

// Somebody who can read the message but was never asked cannot answer it, and
// learns nothing beyond the aggregate. notifyOutsider reads this public channel
// by workspace membership alone and holds no row.
func TestAcknowledgeRefusesSomebodyWhoWasNeverAskedPostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	msg := mustCreate(t, storage.NewPGXMessageStore(pool), askingChannelMessage("824-never-asked"))

	if _, err := acknowledgeAs(t, pool, msg.ID, notifyOutsider); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
	if rows := readAcknowledgementRows(t, pool, msg.ID); len(rows) != 2 {
		t.Fatalf("an unasked caller created a row: %+v", rows)
	}
	// They can still read the message, so the aggregate is not withheld — but it
	// carries no state of their own and no detail.
	summary, err := readAs(t, pool, msg.ID, notifyOutsider)
	if err != nil {
		t.Fatalf("outsider read: %v", err)
	}
	if summary.ViewerState != "" || len(summary.Recipients) != 0 {
		t.Fatalf("outsider summary = %+v, want counts only", summary)
	}
}

// Losing access closes the door on a row that is still theirs. Being a
// recipient when the message was sent does not survive leaving the channel.
func TestAcknowledgeRefusesAfterLosingAccessPostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	store := storage.NewPGXMessageStore(pool)
	// A private channel, so read access really is membership: a public one stays
	// visible to every workspace member and would prove nothing.
	if _, err := pool.Exec(t.Context(),
		`UPDATE chat.channels SET type = 'private' WHERE id = $1`, notifyChannel); err != nil {
		t.Fatalf("make the channel private: %v", err)
	}
	msg := mustCreate(t, store, askingChannelMessage("824-lost-access"))
	if _, err := pool.Exec(t.Context(),
		`DELETE FROM chat.channel_members WHERE channel_id = $1 AND user_id = $2`,
		notifyChannel, notifyPeer); err != nil {
		t.Fatalf("remove the recipient: %v", err)
	}

	if _, err := acknowledgeAs(t, pool, msg.ID, notifyPeer); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
	if _, err := readAs(t, pool, msg.ID, notifyPeer); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("read error = %v, want ErrNotFound", err)
	}
	assertRecipientState(t, readAcknowledgementRows(t, pool, msg.ID),
		notifyPeer, string(domain.AcknowledgementStatePending))
}

// A message belongs to exactly one workspace, and naming another one finds
// nothing — the workspace is part of every predicate, not a hint.
func TestAcknowledgementIsIsolatedByWorkspacePostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	store := storage.NewPGXMessageStore(pool)
	// The same two people are members of both workspaces, so the only thing that
	// can refuse this is the workspace predicate itself.
	elsewhere := storage.CreateMessageInput{
		WorkspaceID:             notifySecondWS,
		DMConversationID:        notifySecondConv,
		SenderID:                notifyAuthor,
		BodyText:                "another tenant's urgent notice",
		BodyFormat:              domain.MessageBodyFormatV3,
		AcknowledgementRequired: true,
		IdempotencyKey:          "824-cross-tenant",
	}
	msg := mustCreate(t, store, elsewhere)

	acknowledgements := storage.NewPGXAcknowledgementStore(pool)
	_, err := acknowledgements.Acknowledge(t.Context(), storage.AcknowledgeInput{
		WorkspaceID: notifyWorkspace, MessageID: msg.ID, RecipientID: notifyPeer,
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("cross-tenant acknowledge error = %v, want ErrNotFound", err)
	}
	_, err = acknowledgements.ReadAcknowledgement(t.Context(), storage.ReadAcknowledgementInput{
		WorkspaceID: notifyWorkspace, MessageID: msg.ID, ViewerID: notifyAuthor,
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("cross-tenant read error = %v, want ErrNotFound", err)
	}
	assertRecipientState(t, readAcknowledgementRows(t, pool, msg.ID),
		notifyPeer, string(domain.AcknowledgementStatePending))
}

// ── what the schema refuses ──────────────────────────────────────────────────

// The state vocabulary is closed, and it is closed against a writer that never
// goes through the store.
func TestAcknowledgementStateConstraintRefusesUndeclaredValuesPostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	msg := mustCreate(t, storage.NewPGXMessageStore(pool), askingChannelMessage("824-state-check"))

	for _, state := range []string{"read", "delivered", "ACKNOWLEDGED", "confirmed", ""} {
		_, err := pool.Exec(t.Context(),
			`UPDATE chat.message_acknowledgements SET state = $3, resolved_at = now()
			 WHERE message_id = $1 AND recipient_id = $2`, msg.ID, notifyPeer, state)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23514" ||
			pgErr.ConstraintName != acknowledgementCheckConstraint {
			t.Fatalf("state %q: expected %s to refuse it, got %v", state, acknowledgementCheckConstraint, err)
		}
	}
}

// Resolution and its instant are one fact. Neither half can be written without
// the other, in either direction.
func TestAcknowledgementRefusesHalfAResolutionPostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	msg := mustCreate(t, storage.NewPGXMessageStore(pool), askingChannelMessage("824-coherence"))

	for name, statement := range map[string]string{
		"resolved without an instant": `UPDATE chat.message_acknowledgements SET state = 'acknowledged'
			 WHERE message_id = $1 AND recipient_id = $2`,
		"pending with an instant": `UPDATE chat.message_acknowledgements SET resolved_at = now()
			 WHERE message_id = $1 AND recipient_id = $2`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := pool.Exec(t.Context(), statement, msg.ID, notifyPeer)
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
				t.Fatalf("expected a check violation, got %v", err)
			}
		})
	}
}

// The rows belong to the message. Removing it for real removes them, so this
// table has no retention policy of its own to get wrong.
func TestAcknowledgementCascadesWithItsMessagePostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	msg := mustCreate(t, storage.NewPGXMessageStore(pool), askingChannelMessage("824-cascade"))
	if len(readAcknowledgementRows(t, pool, msg.ID)) == 0 {
		t.Fatal("the fixture wrote no rows to cascade")
	}

	if _, err := pool.Exec(t.Context(), `DELETE FROM chat.messages WHERE id = $1`, msg.ID); err != nil {
		t.Fatalf("hard delete the message: %v", err)
	}
	if rows := readAcknowledgementRows(t, pool, msg.ID); len(rows) != 0 {
		t.Fatalf("%d rows outlived their message", len(rows))
	}
}

// ── the bound ────────────────────────────────────────────────────────────────

// The count reads the same set the creating statement materialises, excludes
// the sender, and stops at the ceiling it was given.
func TestCountAcknowledgementRecipientsMatchesTheSnapshotPostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	store := storage.NewPGXMessageStore(pool)

	channel, err := store.CountAcknowledgementRecipientsUpTo(
		t.Context(), notifyWorkspace, notifyChannel, "", notifyAuthor, domain.MaxAcknowledgementRecipients+1)
	if err != nil {
		t.Fatalf("count channel recipients: %v", err)
	}
	if channel != 2 {
		t.Fatalf("channel count = %d, want the two members who are not the author", channel)
	}

	conversation, err := store.CountAcknowledgementRecipientsUpTo(
		t.Context(), notifyWorkspace, "", notifyConversation, notifyAuthor, domain.MaxAcknowledgementRecipients+1)
	if err != nil {
		t.Fatalf("count conversation recipients: %v", err)
	}
	if conversation != 2 {
		t.Fatalf("conversation count = %d, want the two members who are not the author", conversation)
	}

	// The ceiling is a real stop, not a post-filter: asking for one says one,
	// however many there are.
	capped, err := store.CountAcknowledgementRecipientsUpTo(
		t.Context(), notifyWorkspace, notifyChannel, "", notifyAuthor, 1)
	if err != nil {
		t.Fatalf("count with a ceiling of one: %v", err)
	}
	if capped != 1 {
		t.Fatalf("capped count = %d, want 1", capped)
	}
}

// ── the fan-out bound, decided inside the writing statement ──────────────────
//
// Code review, #824: the bound was a COUNT in one statement and a materialising
// INSERT in another, so a member joining between the two turned a send judged at
// the bound into one row past it. The bound now lives in the creating statement,
// over the same CTE the rows are written from, which is a property only a real
// database can demonstrate.

const (
	boundChannel   = "82400000-0000-4000-8000-000000000001"
	boundUserStem  = "82400000-0000-4000-8000-1"
	boundUserEmail = "ack-824-bulk-"
)

// boundMemberID is the deterministic id of the nth bulk member, wide enough to
// stay a valid UUID for the whole range these tests use.
func boundMemberID(n int) string {
	return fmt.Sprintf("%s%011d", boundUserStem, n)
}

// seedBoundChannel builds a channel whose membership is `members` people plus
// the author, so `members` is exactly the eligible recipient count.
//
// Seeded set-based rather than row by row: the point of these tests is a bound
// in the low hundreds, and 200 round trips per test would dominate the suite.
func seedBoundChannel(t *testing.T, pool *pgxpool.Pool, members int) {
	t.Helper()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(t.Context(), sql, args...); err != nil {
			t.Fatalf("seed bound channel: %v", err)
		}
	}
	exec(`INSERT INTO chat.channels (id, workspace_id, slug, display_name, type, status)
		VALUES ($1, $2, 'ack-824-bound', 'Ack 824 bound', 'public', 'active')
		ON CONFLICT (id) DO NOTHING`, boundChannel, notifyWorkspace)
	exec(`INSERT INTO chat.channel_members (channel_id, user_id) VALUES ($1, $2)
		ON CONFLICT DO NOTHING`, boundChannel, notifyAuthor)
	t.Cleanup(func() { cleanupBoundChannel(t, pool) })
	addBoundMembers(t, pool, 1, members)
}

// addBoundMembers creates members [from, to] and joins them to the channel. It
// is separate from the seed so a test can grow the conversation after a send.
func addBoundMembers(t *testing.T, pool *pgxpool.Pool, from, to int) {
	t.Helper()
	if to < from {
		return
	}
	ids := make([]string, 0, to-from+1)
	emails := make([]string, 0, to-from+1)
	for n := from; n <= to; n++ {
		ids = append(ids, boundMemberID(n))
		emails = append(emails, fmt.Sprintf("%s%d@e.test", boundUserEmail, n))
	}
	// Each statement is bound with exactly the arguments it names: the three
	// write different tables and pgx refuses a mismatched parameter count.
	steps := []struct {
		statement string
		args      []any
	}{
		{`INSERT INTO auth.users (id, email, display_name)
		  SELECT id::uuid, email, 'Bulk' FROM unnest($1::text[], $2::text[]) AS u(id, email)
		  ON CONFLICT (id) DO NOTHING`, []any{ids, emails}},
		{`INSERT INTO chat.workspace_members (workspace_id, user_id, status)
		  SELECT $2::uuid, id::uuid, 'active' FROM unnest($1::text[]) AS u(id)
		  ON CONFLICT DO NOTHING`, []any{ids, notifyWorkspace}},
		{`INSERT INTO chat.channel_members (channel_id, user_id)
		  SELECT $2::uuid, id::uuid FROM unnest($1::text[]) AS u(id)
		  ON CONFLICT DO NOTHING`, []any{ids, boundChannel}},
	}
	for _, step := range steps {
		if _, err := pool.Exec(t.Context(), step.statement, step.args...); err != nil {
			t.Fatalf("add bound members: %v", err)
		}
	}
}

func cleanupBoundChannel(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	for _, statement := range []string{
		`DELETE FROM chat.messages WHERE channel_id = '` + boundChannel + `'`,
		`DELETE FROM chat.channels WHERE id = '` + boundChannel + `'`,
		`DELETE FROM chat.workspace_members
		   WHERE user_id::text LIKE '` + boundUserStem + `%'`,
		`DELETE FROM auth.users WHERE email LIKE '` + boundUserEmail + `%'`,
	} {
		if _, err := pool.Exec(ctx, statement); err != nil {
			t.Errorf("cleanup bound channel: %v", err)
		}
	}
}

// boundChannelMessage is an acknowledgement-requiring send into that channel.
func boundChannelMessage(idempotencyKey string) storage.CreateMessageInput {
	input := askingChannelMessage(idempotencyKey)
	input.ChannelID = boundChannel
	return input
}

func countAcknowledgementRows(t *testing.T, pool *pgxpool.Pool, messageID string) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(t.Context(),
		`SELECT count(*) FROM chat.message_acknowledgements WHERE message_id = $1`,
		messageID).Scan(&count); err != nil {
		t.Fatalf("count acknowledgement rows: %v", err)
	}
	return count
}

// At the bound the send succeeds, and the rows written are exactly the eligible
// recipients the statement counted — not one more, not one fewer.
func TestAcknowledgementBoundAdmitsExactlyTheLimitPostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	seedBoundChannel(t, pool, domain.MaxAcknowledgementRecipients)

	msg := mustCreate(t, storage.NewPGXMessageStore(pool), boundChannelMessage("824-bound-at-limit"))
	if got := countAcknowledgementRows(t, pool, msg.ID); got != domain.MaxAcknowledgementRecipients {
		t.Fatalf("wrote %d recipient rows, want exactly the %d eligible",
			got, domain.MaxAcknowledgementRecipients)
	}
}

// One past the bound and the statement writes nothing at all: no message, so no
// recipient rows, no outbox row and no link-scan edge either, because every one
// of those CTEs reads the message that was never inserted.
func TestAcknowledgementBoundRefusesTheWholeMessagePostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	seedBoundChannel(t, pool, domain.MaxAcknowledgementRecipients+1)
	store := storage.NewPGXMessageStore(pool)

	if _, err := store.CreateMessage(t.Context(), boundChannelMessage("824-bound-over")); err == nil {
		t.Fatal("an over-bound send was written")
	}
	assertNothingWasWritten(t, pool)

	// The same send with the request switched off is not affected by the bound:
	// it asks nobody, so there is nothing to bound.
	plain := boundChannelMessage("824-bound-over-plain")
	plain.AcknowledgementRequired = false
	msg := mustCreate(t, store, plain)
	if got := countAcknowledgementRows(t, pool, msg.ID); got != 0 {
		t.Fatalf("a send that asked nobody wrote %d recipient rows", got)
	}
}

// assertNothingWasWritten proves a refused send left no trace in the channel it
// was aimed at.
func assertNothingWasWritten(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	var messages, acknowledgements, outbox int
	err := pool.QueryRow(t.Context(), `
		SELECT
			(SELECT count(*) FROM chat.messages WHERE channel_id = $1),
			(SELECT count(*) FROM chat.message_acknowledgements a
			   JOIN chat.messages m ON m.id = a.message_id WHERE m.channel_id = $1),
			(SELECT count(*) FROM chat.notification_outbox o
			   JOIN chat.messages m ON m.id = o.message_id WHERE m.channel_id = $1)`,
		boundChannel).Scan(&messages, &acknowledgements, &outbox)
	if err != nil {
		t.Fatalf("read the refused send's footprint: %v", err)
	}
	if messages != 0 || acknowledgements != 0 || outbox != 0 {
		t.Fatalf("a refused send left %d messages, %d recipient rows and %d outbox rows",
			messages, acknowledgements, outbox)
	}
}

// The invariant, under a membership that is growing while sends are happening.
//
// This is the shape the old code could not hold: the count and the write were
// two statements, so a join landing between them produced a message with one
// row more than the bound allows. Now both read one CTE, so whichever snapshot a
// send sees, the rows it writes are that snapshot — and never more than the
// bound.
func TestAcknowledgementBoundHoldsWhileMembershipGrowsPostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	// Start one short of the bound, so a handful of concurrent joins carries the
	// channel across it while sends are in flight.
	seedBoundChannel(t, pool, domain.MaxAcknowledgementRecipients-4)
	store := storage.NewPGXMessageStore(pool)

	const sends = 8
	var wg sync.WaitGroup
	created := make([]string, sends)
	start := make(chan struct{})
	for i := range sends {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			msg, err := store.CreateMessage(context.Background(),
				boundChannelMessage(fmt.Sprintf("824-bound-race-%d", i)))
			if err == nil {
				created[i] = msg.ID
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		// The joins that cross the bound, committing while the sends run.
		addBoundMembers(t, pool, domain.MaxAcknowledgementRecipients-3,
			domain.MaxAcknowledgementRecipients+3)
	}()
	close(start)
	wg.Wait()

	assertEveryCreatedMessageRespectsTheBound(t, pool, created)
}

// assertEveryCreatedMessageRespectsTheBound is the invariant the race exists to
// check: no message that was written asks more people than the bound allows.
func assertEveryCreatedMessageRespectsTheBound(t *testing.T, pool *pgxpool.Pool, created []string) {
	t.Helper()
	written := 0
	for _, id := range created {
		if id == "" {
			continue
		}
		written++
		rows := countAcknowledgementRows(t, pool, id)
		if rows > domain.MaxAcknowledgementRecipients {
			t.Fatalf("message %s asks %d recipients, past the bound of %d",
				id, rows, domain.MaxAcknowledgementRecipients)
		}
		if rows == 0 {
			t.Fatalf("message %s was written asking nobody, but it asked for confirmation", id)
		}
	}
	if written == 0 {
		t.Fatal("no send succeeded; the race proved nothing")
	}
}

// A send that already succeeded stays retrievable by its key after the channel
// outgrows the bound. The idempotency lookup reads a message, not a membership,
// so the bound has nothing to say about it — which is what keeps the previous
// round's fix true at the storage layer as well.
func TestAcknowledgementReplayIsUnaffectedByTheBoundPostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	seedBoundChannel(t, pool, domain.MaxAcknowledgementRecipients-1)
	store := storage.NewPGXMessageStore(pool)

	const key = "824-bound-replay"
	original := mustCreate(t, store, boundChannelMessage(key))

	addBoundMembers(t, pool, domain.MaxAcknowledgementRecipients,
		domain.MaxAcknowledgementRecipients+5)

	replayed, err := store.LookupCreateReplay(t.Context(), storage.CreateReplayInput{
		WorkspaceID: notifyWorkspace, ChannelID: boundChannel, SenderID: notifyAuthor,
		IdempotencyKey: key, RequestFingerprint: original.CreateFingerprint,
	})
	if err != nil {
		t.Fatalf("replay lookup after the channel outgrew the bound: %v", err)
	}
	if replayed.ID != original.ID {
		t.Fatalf("replay returned %q, want the original %q", replayed.ID, original.ID)
	}
	if got := countAcknowledgementRows(t, pool, original.ID); got != domain.MaxAcknowledgementRecipients-1 {
		t.Fatalf("the replay changed the recipient set: %d rows", got)
	}
}

// ── acknowledge racing a reply ───────────────────────────────────────────────
//
// Code review, #824: ack against ack and ack against delete were proved under
// real concurrency; ack against reply was only proved in sequence. The two
// resolve the same row through different statements — a conditional UPDATE in
// the acknowledge path, a CTE of the creating statement in the reply path — so
// "they converge" is a claim about two writers meeting on one row, and only a
// database can answer it.
//
// #820 states no precedence between the two: an acknowledgement and a reply are
// both terminal resolutions of the same request, and the reminder policy it
// defines stops on either. So the rule under test is first-terminal-wins — the
// transition that commits first stands — and the assertions name the outcomes
// that are legal rather than picking a winner the parent never chose.

// acknowledgeReplyOutcome is what one race settled on.
type acknowledgeReplyOutcome struct {
	State      string
	Resolved   bool
	RowsForMe  int
	OtherState string
}

// raceAcknowledgeAgainstReply resolves one fresh request both ways at once and
// reports what the row settled on.
//
// The two goroutines are released by a shared barrier rather than by a sleep:
// the window this is probing is the gap between reading a row's state and
// writing it, which is microseconds wide, so the only useful synchronisation is
// to have both writers already parked at the same starting line.
func raceAcknowledgeAgainstReply(t *testing.T, pool *pgxpool.Pool, round int) acknowledgeReplyOutcome {
	t.Helper()
	store := storage.NewPGXMessageStore(pool)
	msg := mustCreate(t, store, askingDMMessage(fmt.Sprintf("824-ack-vs-reply-%d", round)))

	reply := dmInput(fmt.Sprintf("824-ack-vs-reply-answer-%d", round))
	reply.SenderID = notifyPeer
	reply.ParentMessageID = msg.ID

	var wg sync.WaitGroup
	start := make(chan struct{})
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		_, _ = storage.NewPGXAcknowledgementStore(pool).Acknowledge(context.Background(),
			storage.AcknowledgeInput{
				WorkspaceID: notifyWorkspace, MessageID: msg.ID, RecipientID: notifyPeer,
			})
	}()
	go func() {
		defer wg.Done()
		<-start
		_, _ = store.CreateMessage(context.Background(), reply)
	}()
	close(start)
	wg.Wait()

	return readRaceOutcome(t, pool, msg.ID)
}

func readRaceOutcome(t *testing.T, pool *pgxpool.Pool, messageID string) acknowledgeReplyOutcome {
	t.Helper()
	outcome := acknowledgeReplyOutcome{}
	for _, row := range readAcknowledgementRows(t, pool, messageID) {
		if row.Recipient == notifyPeer {
			outcome.State, outcome.Resolved = row.State, row.Resolved
			outcome.RowsForMe++
			continue
		}
		outcome.OtherState = row.State
	}
	return outcome
}

// The race, repeated enough times to land on both sides of it without making
// the suite slow. Every round must converge; none may be left pending, doubled
// or incoherent.
func TestAcknowledgeRacingReplyConvergesPostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)

	const rounds = 12
	seen := map[string]int{}
	for round := range rounds {
		outcome := raceAcknowledgeAgainstReply(t, pool, round)
		assertRaceOutcomeIsCoherent(t, round, outcome)
		seen[outcome.State]++
	}
	// Both outcomes are legal; the test does not require seeing both, because
	// which one wins is a scheduling fact and demanding it would make this
	// flaky. What it does require is that nothing outside the legal set ever
	// appears, which the per-round assertion above enforces.
	t.Logf("acknowledge/reply race settled as %v over %d rounds", seen, rounds)
}

// assertRaceOutcomeIsCoherent is the whole contract of the race in one place.
func assertRaceOutcomeIsCoherent(t *testing.T, round int, outcome acknowledgeReplyOutcome) {
	t.Helper()
	// One writer, one row. The primary key is what guarantees it, and a race is
	// exactly where a second row would appear if it did not.
	if outcome.RowsForMe != 1 {
		t.Fatalf("round %d: the raced recipient holds %d rows, want exactly 1", round, outcome.RowsForMe)
	}
	// Never pending: both writers were resolving, so one of them committed.
	switch outcome.State {
	case string(domain.AcknowledgementStateAcknowledged), string(domain.AcknowledgementStateResponded):
	default:
		t.Fatalf("round %d: settled on %q; only acknowledged or responded may win", round, outcome.State)
	}
	// The instant and the state are one fact, and the schema refuses to hold
	// half of it — so a terminal row without a resolution instant would mean the
	// CHECK had been bypassed, not merely that a timestamp was forgotten.
	//
	// There is one instant, not one per transition: chat.message_acknowledgements
	// records resolved_at and the state that resolved it, so the loser leaves
	// nothing behind to be inconsistent with. That is what makes "acknowledged
	// carrying a responded timestamp" unrepresentable rather than merely untested.
	if !outcome.Resolved {
		t.Fatalf("round %d: settled on %q with no resolution instant", round, outcome.State)
	}
	// One person's answer resolves one person's request.
	if outcome.OtherState != string(domain.AcknowledgementStatePending) {
		t.Fatalf("round %d: an uninvolved recipient moved to %q", round, outcome.OtherState)
	}
}

// The loser does not get a second chance afterwards. Whichever transition won
// the race, a later acknowledgement finds no pending row and reports the state
// that actually holds.
func TestAcknowledgeAfterLosingTheRaceChangesNothingPostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	outcome := raceAcknowledgeAgainstReply(t, pool, 100)

	var messageID string
	if err := pool.QueryRow(t.Context(),
		`SELECT message_id::text FROM chat.message_acknowledgements
		 WHERE recipient_id = $1 AND state <> 'pending'
		 ORDER BY resolved_at DESC LIMIT 1`, notifyPeer).Scan(&messageID); err != nil {
		t.Fatalf("find the raced message: %v", err)
	}

	summary, err := acknowledgeAs(t, pool, messageID, notifyPeer)
	if err != nil {
		t.Fatalf("Acknowledge after the race: %v", err)
	}
	if string(summary.ViewerState) != outcome.State {
		t.Fatalf("a later acknowledgement changed %q into %q", outcome.State, summary.ViewerState)
	}
}

// ── the page batch ───────────────────────────────────────────────────────────
//
// Code review, #824: the page asked per message, so a conversation with twenty
// urgent notices cost twenty round trips and twenty aggregations on every open
// and every reconnect. The batch answers for the whole page in one statement.
// What only a real database can show is that the authorization is still per
// message inside that one statement, and that the aggregation is genuinely
// set-based rather than a loop wearing a batch's clothes.

func readBatchAs(
	t *testing.T, pool *pgxpool.Pool, viewer string, messageIDs ...string,
) map[string]domain.AcknowledgementSummary {
	t.Helper()
	summaries, err := storage.NewPGXAcknowledgementStore(pool).ReadAcknowledgementBatch(
		t.Context(), storage.ReadAcknowledgementBatchInput{
			WorkspaceID: notifyWorkspace, MessageIDs: messageIDs, ViewerID: viewer,
		})
	if err != nil {
		t.Fatalf("ReadAcknowledgementBatch: %v", err)
	}
	return summaries
}

// The page's answers, each keyed by its own message, with the counts and the
// viewer's own state that the single read would have given one at a time.
func TestAcknowledgementBatchAnswersAWholePagePostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	store := storage.NewPGXMessageStore(pool)
	first := mustCreate(t, store, askingChannelMessage("824-batch-1"))
	second := mustCreate(t, store, askingChannelMessage("824-batch-2"))
	plain := mustCreate(t, store, dmInput("824-batch-plain"))
	if _, err := acknowledgeAs(t, pool, first.ID, notifyPeer); err != nil {
		t.Fatalf("Acknowledge: %v", err)
	}

	// The recipient's view: their own state on both asking messages.
	recipient := readBatchAs(t, pool, notifyPeer, first.ID, second.ID, plain.ID)
	if len(recipient) != 3 {
		t.Fatalf("got %d entries, want all three readable messages", len(recipient))
	}
	if recipient[first.ID].ViewerState != domain.AcknowledgementStateAcknowledged {
		t.Fatalf("first message viewer state = %q", recipient[first.ID].ViewerState)
	}
	if recipient[second.ID].ViewerState != domain.AcknowledgementStatePending {
		t.Fatalf("second message viewer state = %q", recipient[second.ID].ViewerState)
	}
	if recipient[first.ID].Acknowledged != 1 || recipient[first.ID].Pending != 1 {
		t.Fatalf("first message counts = %+v", recipient[first.ID])
	}
	// A message that asked nobody is still answered — required false, all zero —
	// so a client can tell it apart from one whose recipients have not replied.
	if recipient[plain.ID].Required || recipient[plain.ID].Total != 0 {
		t.Fatalf("an ordinary message summarised as %+v", recipient[plain.ID])
	}

	// The sender's view of the same page: counts, and no state of their own.
	sender := readBatchAs(t, pool, notifyAuthor, first.ID, second.ID)
	if sender[first.ID].ViewerState != "" || sender[second.ID].ViewerState != "" {
		t.Fatal("the sender was given a state of their own; they were never asked")
	}
	if sender[first.ID].Total != 2 || sender[first.ID].Acknowledged != 1 {
		t.Fatalf("sender counts = %+v", sender[first.ID])
	}
	// The page view carries no per-recipient list, whoever is asking.
	for _, summary := range sender {
		if len(summary.Recipients) != 0 {
			t.Fatalf("the page batch carried recipient detail: %+v", summary)
		}
	}
}

// Authorization stays per message inside the one statement. Mixing ids the
// caller may not read into the request reveals nothing: they are simply absent,
// and so is an id that names nothing at all.
func TestAcknowledgementBatchOmitsWhatTheCallerMayNotReadPostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	store := storage.NewPGXMessageStore(pool)
	readable := mustCreate(t, store, askingChannelMessage("824-batch-readable"))

	// Another workspace entirely, which the fixture's second tenant provides.
	// The access predicate is one test, so this covers the conversation boundary
	// as well as the tenant one: the viewer belongs to neither.
	elsewhere := mustCreate(t, store, storage.CreateMessageInput{
		WorkspaceID: notifySecondWS, DMConversationID: notifySecondConv, SenderID: notifyAuthor,
		BodyText: "another tenant", BodyFormat: domain.MessageBodyFormatV3,
		AcknowledgementRequired: true, IdempotencyKey: "824-batch-cross-tenant",
	})
	const absent = "82400000-0000-4000-8000-00000000dead"

	summaries := readBatchAs(t, pool, notifyThird, readable.ID, elsewhere.ID, absent)
	if _, present := summaries[elsewhere.ID]; present {
		t.Fatal("a message from another workspace appeared in the answer")
	}
	if _, present := summaries[absent]; present {
		t.Fatal("an id naming nothing produced an entry")
	}
	if len(summaries) != 1 || summaries[readable.ID].Total != 2 {
		t.Fatalf("answer = %+v, want only the readable message", summaries)
	}
}

// Somebody who joined after the question was put holds no row, so the batch
// gives them the counts and no state — which is what the client needs to tell a
// late joiner from a recipient.
func TestAcknowledgementBatchGivesALateJoinerNoStatePostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	msg := mustCreate(t, storage.NewPGXMessageStore(pool), askingChannelMessage("824-batch-late"))

	// notifyOutsider reads this public channel by workspace membership alone and
	// was never an explicit member, so the send never asked them.
	summaries := readBatchAs(t, pool, notifyOutsider, msg.ID)
	if len(summaries) != 1 {
		t.Fatalf("got %d entries, want the readable message", len(summaries))
	}
	if summaries[msg.ID].ViewerState != "" {
		t.Fatalf("a late joiner was given state %q", summaries[msg.ID].ViewerState)
	}
	if summaries[msg.ID].Total != 2 {
		t.Fatalf("late joiner counts = %+v", summaries[msg.ID])
	}
}

// ── one response, one snapshot ───────────────────────────────────────────────
//
// Code review, #824: the read asked for the counts and then for the recipients.
// Two statements are two snapshots under READ COMMITTED, so a transition
// committing between them produced a single response whose summary still said
// pending while its detail already said acknowledged. The fix is one statement;
// what only a real database can show is that a concurrent transition can no
// longer be observed half-applied.

// recountFromRecipients rebuilds the aggregate from the detail of the same
// response. The two must agree. Which snapshot they agree on is a scheduling
// fact this contract deliberately does not pin down: before the transition and
// after it are both correct answers, and only a mixture is wrong.
func recountFromRecipients(
	recipients []domain.AcknowledgementRecipient,
) map[domain.AcknowledgementState]int {
	counted := map[domain.AcknowledgementState]int{}
	for _, recipient := range recipients {
		counted[recipient.State]++
	}
	return counted
}

func assertAcknowledgementSnapshotConsistent(
	t *testing.T, key string, summary domain.AcknowledgementSummary,
) {
	t.Helper()
	// A viewer without the detail has nothing to be inconsistent with; the
	// invariant below is only recomputable where the list is present.
	if len(summary.Recipients) == 0 {
		return
	}
	if len(summary.Recipients) != summary.Total {
		t.Fatalf("%s: summary counts %d recipients, detail carries %d",
			key, summary.Total, len(summary.Recipients))
	}
	counted := recountFromRecipients(summary.Recipients)
	for state, want := range map[domain.AcknowledgementState]int{
		domain.AcknowledgementStatePending:      summary.Pending,
		domain.AcknowledgementStateAcknowledged: summary.Acknowledged,
		domain.AcknowledgementStateResponded:    summary.Responded,
		domain.AcknowledgementStateExpired:      summary.Expired,
		domain.AcknowledgementStateCancelled:    summary.Cancelled,
	} {
		if counted[state] != want {
			t.Fatalf("%s: summary says %d %s, detail carries %d — two snapshots in one response",
				key, want, state, counted[state])
		}
	}
}

// raceReadAgainstTransition reads the message repeatedly as its sender while one
// recipient resolves their row, and returns every response the sender saw.
//
// The reader loops rather than reading once, because the window being probed is
// the gap between two statements of a single read: a single read released at the
// same instant as the transition lands inside that gap only by luck, while
// thirty consecutive reads straddle it. Both sides start from the same barrier
// rather than from a sleep.
//
// Nothing is asserted here: t.Fatalf belongs to the test goroutine, so the
// responses are collected and judged after both sides have finished.
func raceReadAgainstTransition(
	t *testing.T, pool *pgxpool.Pool, key string, transition func(messageID string),
) (string, []domain.AcknowledgementSummary) {
	t.Helper()
	msg := mustCreate(t, storage.NewPGXMessageStore(pool), askingDMMessage(key))

	const reads = 30
	seen := make([]domain.AcknowledgementSummary, 0, reads)
	var readErr error
	var wg sync.WaitGroup
	start := make(chan struct{})
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		for range reads {
			summary, err := readAs(t, pool, msg.ID, notifyAuthor)
			if err != nil {
				readErr = err
				return
			}
			seen = append(seen, summary)
		}
	}()
	go func() {
		defer wg.Done()
		<-start
		transition(msg.ID)
	}()
	close(start)
	wg.Wait()

	if readErr != nil {
		t.Fatalf("%s: sender read: %v", key, readErr)
	}
	return msg.ID, seen
}

// A sender reading while a recipient answers never receives a response that
// mixes the two snapshots — with either of the transitions that resolve a row.
func TestReadAcknowledgementNeverMixesSnapshotsPostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	store := storage.NewPGXMessageStore(pool)

	transitions := []struct {
		name string
		run  func(key, messageID string)
	}{
		{"acknowledge", func(_, messageID string) {
			_, _ = storage.NewPGXAcknowledgementStore(pool).Acknowledge(context.Background(),
				storage.AcknowledgeInput{
					WorkspaceID: notifyWorkspace, MessageID: messageID, RecipientID: notifyPeer,
				})
		}},
		{"reply", func(key, messageID string) {
			answer := dmInput(key + "-answer")
			answer.SenderID = notifyPeer
			answer.ParentMessageID = messageID
			_, _ = store.CreateMessage(context.Background(), answer)
		}},
	}

	const rounds = 3
	for _, transition := range transitions {
		t.Run(transition.name, func(t *testing.T) {
			for round := range rounds {
				key := fmt.Sprintf("824-snapshot-%s-%d", transition.name, round)
				messageID, seen := raceReadAgainstTransition(t, pool, key,
					func(messageID string) { transition.run(key, messageID) })
				for _, summary := range seen {
					assertAcknowledgementSnapshotConsistent(t, key, summary)
				}
				assertTheTransitionActuallyLanded(t, pool, key, messageID)
			}
		})
	}
}

// The race is only worth running if the transition resolved something. Read
// after both sides have finished, so this is a settled state rather than a
// third snapshot: one of the two asked recipients answered, the other did not.
func assertTheTransitionActuallyLanded(t *testing.T, pool *pgxpool.Pool, key, messageID string) {
	t.Helper()
	settled, err := readAs(t, pool, messageID, notifyAuthor)
	if err != nil {
		t.Fatalf("%s: settled read: %v", key, err)
	}
	if settled.Total != 2 || settled.Pending != 1 {
		t.Fatalf("%s: settled as %d of %d pending; the transition resolved nothing to race against",
			key, settled.Pending, settled.Total)
	}
	assertAcknowledgementSnapshotConsistent(t, key+"-settled", settled)
}
