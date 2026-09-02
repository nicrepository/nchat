package storage_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nicrepository/nchat/libs/go/platform/notificationevent"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// Issue #741: the notification outbox against a real database.
//
// None of this can be proved with a mock. What is under test is a boundary the
// database owns — one statement that either commits a message together with the
// notifications it produces or commits neither, a unique index that is the
// authority on whether two rows are the same logical event, and a trigger that
// is the reason a terminal state is terminal. A fake would have to reimplement
// all three, and would then be testing itself.
//
// Opt-in like its neighbours: needs CHAT_TEST_DATABASE_URL against a _test
// database carrying the real migrations.

const (
	// The workspace every chat migration seeds.
	notifyWorkspace    = "00000000-0000-0000-0000-000000000001"
	notifySecondWS     = "74100000-0000-4000-8000-000000000001"
	notifyChannel      = "74100000-0000-4000-8000-000000000002"
	notifyConversation = "74100000-0000-4000-8000-000000000003"
	notifySecondConv   = "74100000-0000-4000-8000-000000000004"
	notifyAuthor       = "74100000-0000-4000-8000-000000000005"
	notifyPeer         = "74100000-0000-4000-8000-000000000006"
	notifyThird        = "74100000-0000-4000-8000-000000000007"
	// Seeded as a conversation member but never as a user, so the outbox's
	// recipient foreign key is the thing that fails when it is notified.
	notifyPhantom  = "74100000-0000-4000-8000-000000000008"
	notifyOrphanDM = "74100000-0000-4000-8000-000000000009"
	// Every workspace must carry exactly one active public general channel.
	notifySecondGeneral = "74100000-0000-4000-8000-000000000010"

	notifyScannedURL  = "https://notify-741.example/article"
	notifyFingerprint = "fp-notify-741"
)

// seedNotificationFixture builds the two workspaces, the channel and the
// conversations every test in this file reads, and removes them afterwards.
func seedNotificationFixture(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := newLinkScanTestPool(t)
	ctx := t.Context()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed fixture: %v", err)
		}
	}

	exec(`INSERT INTO auth.users (id, email, display_name) VALUES
		($1, 'notify-741-author@e.test', 'Author'),
		($2, 'notify-741-peer@e.test',   'Peer'),
		($3, 'notify-741-third@e.test',  'Third')
		ON CONFLICT (id) DO NOTHING`, notifyAuthor, notifyPeer, notifyThird)
	// The workspace and its general channel are written together: the invariant
	// that every workspace has one is enforced by a deferred constraint trigger,
	// so a workspace committed on its own is refused.
	exec(`WITH created AS (
			INSERT INTO chat.workspaces (id, slug, name, status)
			VALUES ($1, 'notify-741', 'Notify 741', 'active')
			ON CONFLICT (id) DO NOTHING
			RETURNING id
		)
		INSERT INTO chat.channels (id, workspace_id, slug, display_name, type, is_general, status)
		VALUES ($2, $1, 'geral', 'Geral', 'public', true, 'active')
		ON CONFLICT (id) DO NOTHING`, notifySecondWS, notifySecondGeneral)
	exec(`INSERT INTO chat.workspace_members (workspace_id, user_id, status) VALUES
		($1, $3, 'active'), ($1, $4, 'active'), ($1, $5, 'active'),
		($2, $3, 'active'), ($2, $4, 'active')
		ON CONFLICT DO NOTHING`, notifyWorkspace, notifySecondWS, notifyAuthor, notifyPeer, notifyThird)
	exec(`INSERT INTO chat.channels (id, workspace_id, slug, display_name, type, status)
		VALUES ($2, $1, 'notify-741', 'Notify 741', 'public', 'active')
		ON CONFLICT (id) DO NOTHING`, notifyWorkspace, notifyChannel)
	exec(`INSERT INTO chat.channel_members (channel_id, user_id) VALUES
		($1, $2), ($1, $3), ($1, $4)
		ON CONFLICT DO NOTHING`, notifyChannel, notifyAuthor, notifyPeer, notifyThird)
	exec(`INSERT INTO chat.dm_conversations (id, workspace_id, type, status, created_by, title)
		VALUES ($2, $1, 'group', 'active', $3, 'Notify 741'),
		       ($5, $4, 'group', 'active', $3, 'Notify 741 elsewhere'),
		       ($6, $1, 'group', 'active', $3, 'Notify 741 orphan')
		ON CONFLICT (id) DO NOTHING`,
		notifyWorkspace, notifyConversation, notifyAuthor,
		notifySecondWS, notifySecondConv, notifyOrphanDM)
	exec(`INSERT INTO chat.dm_members (conversation_id, user_id, status) VALUES
		($1, $4, 'active'), ($1, $5, 'active'), ($1, $6, 'active'),
		($2, $4, 'active'), ($2, $5, 'active'),
		($3, $4, 'active'), ($3, $7, 'active')
		ON CONFLICT DO NOTHING`,
		notifyConversation, notifySecondConv, notifyOrphanDM,
		notifyAuthor, notifyPeer, notifyThird, notifyPhantom)

	t.Cleanup(func() { cleanupNotificationFixture(t, pool) })
	return pool
}

// cleanupNotificationFixture removes everything the fixture created. The second
// workspace is removed whole, so its channels, conversations and memberships go
// with it by cascade — deleting its general channel first would trip the
// invariant that requires one.
func cleanupNotificationFixture(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	for _, statement := range []string{
		`DELETE FROM chat.messages WHERE workspace_id = '` + notifyWorkspace + `'`,
		`DELETE FROM chat.conversation_read_state WHERE workspace_id = '` + notifyWorkspace + `'`,
		`DELETE FROM chat.dm_conversations WHERE id IN ('` + notifyConversation + `', '` + notifyOrphanDM + `')`,
		`DELETE FROM chat.channels WHERE id = '` + notifyChannel + `'`,
		`DELETE FROM chat.link_scans WHERE canonical_url = '` + notifyScannedURL + `'`,
		`DELETE FROM chat.workspace_members WHERE workspace_id = '` + notifyWorkspace + `'
		   AND user_id IN ('` + notifyAuthor + `', '` + notifyPeer + `', '` + notifyThird + `')`,
		`DELETE FROM chat.workspaces WHERE id = '` + notifySecondWS + `'`,
		`DELETE FROM auth.users WHERE id IN ('` + notifyAuthor + `', '` + notifyPeer + `', '` + notifyThird + `')`,
	} {
		if _, err := pool.Exec(ctx, statement); err != nil {
			t.Logf("cleanup: %v", err)
		}
	}
}

// outboxRow is the projection every assertion in this file reads. The message
// body is deliberately absent, because the table does not carry it.
type outboxRow struct {
	ID          string
	Recipient   string
	Kind        string
	Status      string
	SourceType  string
	Priority    string
	Origin      string
	DedupeKey   string
	SameInstant bool
}

func readOutbox(t *testing.T, pool *pgxpool.Pool, messageID string) []outboxRow {
	t.Helper()
	rows, err := pool.Query(t.Context(), `
		SELECT o.id::text, o.recipient_user_id::text, o.kind, o.status, o.source_type,
		       o.priority, o.origin, COALESCE(o.dedupe_key, ''),
		       o.occurred_at = m.created_at
		FROM chat.notification_outbox o
		JOIN chat.messages m ON m.id = o.message_id
		WHERE o.message_id = $1::uuid
		ORDER BY o.kind, o.recipient_user_id`, messageID)
	if err != nil {
		t.Fatalf("read outbox: %v", err)
	}
	defer rows.Close()

	var out []outboxRow
	for rows.Next() {
		var row outboxRow
		if err := rows.Scan(&row.ID, &row.Recipient, &row.Kind, &row.Status, &row.SourceType,
			&row.Priority, &row.Origin, &row.DedupeKey, &row.SameInstant); err != nil {
			t.Fatalf("scan outbox row: %v", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate outbox rows: %v", err)
	}
	return out
}

func dmInput(idempotencyKey string) storage.CreateMessageInput {
	return storage.CreateMessageInput{
		WorkspaceID:      notifyWorkspace,
		DMConversationID: notifyConversation,
		SenderID:         notifyAuthor,
		BodyText:         "outbox fixture body",
		BodyFormat:       domain.MessageBodyFormatV3,
		IdempotencyKey:   idempotencyKey,
	}
}

func mustCreate(t *testing.T, store *storage.PGXMessageStore, input storage.CreateMessageInput) domain.Message {
	t.Helper()
	msg, err := store.CreateMessage(t.Context(), input)
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}
	return msg
}

func assertField(t *testing.T, name, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %q, want %q", name, got, want)
	}
}

func assertRowCount(t *testing.T, rows []outboxRow, want int) {
	t.Helper()
	if len(rows) != want {
		t.Fatalf("got %d outbox rows, want %d: %+v", len(rows), want, rows)
	}
}

func assertCount(t *testing.T, pool *pgxpool.Pool, query, arg string, want int, why string) {
	t.Helper()
	var got int
	if err := pool.QueryRow(t.Context(), query, arg).Scan(&got); err != nil {
		t.Fatalf("count: %v", err)
	}
	if got != want {
		t.Fatalf("%s: got %d rows, want %d", why, got, want)
	}
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func isCheckViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23514"
}

// ── Creation ────────────────────────────────────────────────────────────────

// CASE 1 and CASE 7: a message and the notifications it produces commit
// together, in one statement, however many recipients there are.
func TestNotificationOutboxCommitsWithMessagePostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	counter := &countingPool{Pool: pool}
	msg := mustCreate(t, storage.NewPGXMessageStore(counter), dmInput("notify-741-commit"))

	// Two recipients, one statement. A fan-out written as a loop would have
	// issued three.
	if got := counter.queries.Load(); got != 1 {
		t.Fatalf("creating a message for two recipients issued %d statements, want 1", got)
	}

	rows := readOutbox(t, pool, msg.ID)
	assertRowCount(t, rows, 2)
	assertRecipients(t, rows, notifyPeer, notifyThird)
}

func assertRecipients(t *testing.T, rows []outboxRow, want ...string) {
	t.Helper()
	got := make(map[string]bool, len(rows))
	for _, row := range rows {
		got[row.Recipient] = true
	}
	for _, recipient := range want {
		if !got[recipient] {
			t.Errorf("recipient %s was not notified", recipient)
		}
	}
	if got[notifyAuthor] {
		t.Error("the sender must not be notified of their own message")
	}
	if len(got) != len(want) {
		t.Errorf("notified %d recipients, want %d", len(got), len(want))
	}
}

// Every column the worker and the policy engine will read, on a row produced by
// the real creating statement.
func TestNotificationOutboxRowContractPostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	msg := mustCreate(t, storage.NewPGXMessageStore(pool), dmInput("notify-741-contract"))

	rows := readOutbox(t, pool, msg.ID)
	assertRowCount(t, rows, 2)
	for _, row := range rows {
		assertMessageEventContract(t, row, msg.ID, notificationevent.EventTypeDirectMessage,
			notificationevent.PriorityNormal)
	}
}

// assertMessageEventContract checks one row against the domain contract. The
// dedupe key is rebuilt by the Go authority rather than compared to a literal:
// the fan-out is set-based, so Go never sees an individual row, and a drift
// between the SQL that writes the key and the builder that defines it would
// silently disable deduplication.
func assertMessageEventContract(
	t *testing.T,
	row outboxRow,
	messageID string,
	eventType notificationevent.EventType,
	priority notificationevent.Priority,
) {
	t.Helper()
	assertField(t, "kind", row.Kind, string(eventType))
	assertField(t, "status", row.Status, string(notificationevent.StatePending))
	assertField(t, "source_type", row.SourceType, string(notificationevent.SourceTypeMessage))
	assertField(t, "priority", row.Priority, string(priority))
	assertField(t, "origin", row.Origin, string(notificationevent.OriginLive))
	if !row.SameInstant {
		t.Error("occurred_at must be the message's own created_at")
	}
	want, err := notificationevent.Identity{
		WorkspaceID: notifyWorkspace,
		RecipientID: row.Recipient,
		EventType:   eventType,
		SourceType:  notificationevent.SourceTypeMessage,
		SourceID:    messageID,
	}.DedupeKey()
	if err != nil {
		t.Fatalf("build contract dedupe key: %v", err)
	}
	assertField(t, "dedupe_key", row.DedupeKey, want)
}

// The outbox stores references, never content. A body reaching it would be the
// privacy failure #741 forbids, and it would survive every later authorization
// change.
func TestNotificationOutboxStoresNoMessageBodyPostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	msg := mustCreate(t, storage.NewPGXMessageStore(pool), dmInput("notify-741-privacy"))

	var serialized string
	if err := pool.QueryRow(t.Context(),
		`SELECT to_jsonb(o)::text FROM chat.notification_outbox o WHERE o.message_id = $1::uuid LIMIT 1`,
		msg.ID).Scan(&serialized); err != nil {
		t.Fatalf("serialize outbox row: %v", err)
	}
	if strings.Contains(serialized, "outbox fixture body") {
		t.Fatalf("the outbox must not carry the message body: %s", serialized)
	}
}

// CASE 3: retrying the same logical send produces one logical notification.
func TestNotificationOutboxRetryCreatesNoDuplicatePostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	store := storage.NewPGXMessageStore(pool)
	msg := mustCreate(t, store, dmInput("notify-741-retry"))

	if _, err := store.CreateMessage(t.Context(), dmInput("notify-741-retry")); !errors.Is(err, storage.ErrCreateReplay) {
		t.Fatalf("retry error = %v, want ErrCreateReplay", err)
	}
	assertRowCount(t, readOutbox(t, pool, msg.ID), 2)
}

// The index, not the absence of a second call, is the authority. Writing the
// same identity directly must be refused.
func TestNotificationOutboxDedupeIndexRefusesDuplicatesPostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	msg := mustCreate(t, storage.NewPGXMessageStore(pool), dmInput("notify-741-dedupe"))

	_, err := pool.Exec(t.Context(), `
		INSERT INTO chat.notification_outbox
			(workspace_id, message_id, recipient_user_id, kind, status,
			 source_type, occurred_at, priority, origin, dedupe_key)
		SELECT workspace_id, message_id, recipient_user_id, kind, status,
		       source_type, occurred_at, priority, origin, dedupe_key
		FROM chat.notification_outbox
		WHERE message_id = $1::uuid
		LIMIT 1`, msg.ID)
	if !isUniqueViolation(err) {
		t.Fatalf("re-inserting an identical dedupe identity returned %v, want a unique violation", err)
	}
}

// CASE 2: a rolled back transaction leaves neither half.
func TestNotificationOutboxRollsBackWithMessagePostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	ctx := t.Context()

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	// pgx.Tx satisfies storage.Pool, so the store can be driven inside a
	// transaction the test controls.
	msg, err := storage.NewPGXMessageStore(tx).CreateMessage(ctx, dmInput("notify-741-rollback"))
	if err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("create message: %v", err)
	}
	var insideTx int
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM chat.notification_outbox WHERE message_id = $1::uuid`, msg.ID).Scan(&insideTx); err != nil {
		t.Fatalf("count inside transaction: %v", err)
	}
	if insideTx != 2 {
		t.Fatalf("inside the transaction there are %d outbox rows, want 2", insideTx)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("rollback: %v", err)
	}

	assertCount(t, pool, `SELECT count(*) FROM chat.messages WHERE id = $1::uuid`, msg.ID, 0,
		"a rolled back message must not survive")
	assertCount(t, pool, `SELECT count(*) FROM chat.notification_outbox WHERE message_id = $1::uuid`, msg.ID, 0,
		"a rolled back message must leave no orphan notification")
}

// The other direction: the conversation holds a member who is not a user, so the
// outbox's recipient foreign key fails. The message must not survive that on its
// own.
func TestNotificationOutboxFailureTakesTheMessageWithItPostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	failing := dmInput("notify-741-orphan")
	failing.DMConversationID = notifyOrphanDM

	if _, err := storage.NewPGXMessageStore(pool).CreateMessage(t.Context(), failing); err == nil {
		t.Fatal("a message whose notification cannot be written must not be created")
	}
	assertCount(t, pool,
		`SELECT count(*) FROM chat.messages WHERE dm_conversation_id = $1::uuid`, notifyOrphanDM, 0,
		"a failed outbox write must take the message with it")
}

// CASE 4: two equivalent executions racing produce one logical notification per
// recipient, decided by the database rather than by ordering luck.
func TestNotificationOutboxConcurrentCreateStaysSinglePostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	store := storage.NewPGXMessageStore(pool)
	ctx := t.Context()

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		created  []string
		replayed int
		failures []error
	)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			msg, err := store.CreateMessage(ctx, dmInput("notify-741-concurrent"))
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				created = append(created, msg.ID)
			case errors.Is(err, storage.ErrCreateReplay):
				replayed++
			default:
				failures = append(failures, err)
			}
		}()
	}
	wg.Wait()

	if len(failures) > 0 {
		t.Fatalf("concurrent sends failed: %v", failures)
	}
	if len(created) != 1 || replayed != 1 {
		t.Fatalf("got %d creations and %d replays, want exactly one of each", len(created), replayed)
	}
	assertRowCount(t, readOutbox(t, pool, created[0]), 2)
}

// CASE 5: the event outlives the process that wrote it. The pool is closed
// entirely and a new one opened, which is as close to a restart as a test can
// get without a supervisor.
func TestNotificationOutboxSurvivesRestartPostgreSQL(t *testing.T) {
	seedNotificationFixture(t)
	// A second pool stands in for the process that wrote the event: it is closed
	// outright, while the fixture's own pool stays open so the cleanup can run.
	writer := newLinkScanTestPool(t)
	msg := mustCreate(t, storage.NewPGXMessageStore(writer), dmInput("notify-741-restart"))
	writer.Close()

	reopened := newLinkScanTestPool(t)
	var kind, status string
	if err := reopened.QueryRow(t.Context(), `
		SELECT kind, status FROM chat.notification_outbox
		WHERE message_id = $1::uuid AND recipient_user_id = $2::uuid`,
		msg.ID, notifyPeer).Scan(&kind, &status); err != nil {
		t.Fatalf("read notification after restart: %v", err)
	}
	assertField(t, "kind", kind, string(notificationevent.EventTypeDirectMessage))
	assertField(t, "status", status, string(notificationevent.StatePending))
}

// CASE 6: a workspace is a boundary, and the dedupe identity is qualified by it.
func TestNotificationOutboxIsolatedByWorkspacePostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	store := storage.NewPGXMessageStore(pool)
	ctx := t.Context()

	here := mustCreate(t, store, dmInput("notify-741-here"))
	elsewhere := dmInput("notify-741-elsewhere")
	elsewhere.WorkspaceID = notifySecondWS
	elsewhere.DMConversationID = notifySecondConv
	other := mustCreate(t, store, elsewhere)

	assertCount(t, pool, `
		SELECT count(*) FROM chat.notification_outbox
		WHERE message_id = $1::uuid AND workspace_id <> '`+notifySecondWS+`'::uuid`,
		other.ID, 0, "a notification must never be recorded under another workspace")

	// The tenant is part of the uniqueness, not merely part of the row: this
	// carries the first workspace's dedupe key, verbatim and for the same
	// recipient, into the second one. The index must accept it — two workspaces
	// producing the same key are two events, and one must never suppress the
	// other. That the index refuses a genuine duplicate is proved by
	// TestNotificationOutboxDedupeIndexRefusesDuplicatesPostgreSQL.
	if _, err := pool.Exec(ctx, `
		INSERT INTO chat.notification_outbox
			(workspace_id, message_id, recipient_user_id, kind, status,
			 source_type, occurred_at, priority, origin, dedupe_key)
		SELECT $1::uuid, $2::uuid, recipient_user_id, 'mention', status,
		       source_type, occurred_at, priority, origin, dedupe_key
		FROM chat.notification_outbox
		WHERE message_id = $3::uuid AND recipient_user_id = $4::uuid`,
		notifySecondWS, other.ID, here.ID, notifyPeer); err != nil {
		t.Fatalf("an identical dedupe key in another workspace must be accepted: %v", err)
	}
}

// ── Classification ──────────────────────────────────────────────────────────

// newChannelParent writes the message later tests answer, authored by somebody
// other than the sender under test.
func newChannelParent(t *testing.T, store *storage.PGXMessageStore) domain.Message {
	t.Helper()
	return mustCreate(t, store, storage.CreateMessageInput{
		WorkspaceID: notifyWorkspace,
		ChannelID:   notifyChannel,
		SenderID:    notifyPeer,
		BodyText:    "the message being answered",
		BodyFormat:  domain.MessageBodyFormatV3,
	})
}

func channelAnswer(parentID, body string, mentioned ...string) storage.CreateMessageInput {
	return storage.CreateMessageInput{
		WorkspaceID:      notifyWorkspace,
		ChannelID:        notifyChannel,
		SenderID:         notifyAuthor,
		BodyText:         body,
		BodyFormat:       domain.MessageBodyFormatV3,
		ParentMessageID:  parentID,
		MentionedUserIDs: mentioned,
	}
}

// A recipient reached by several rules is notified once, by the strongest of
// them. Three rows for one message is what a worker would turn into three
// pushes, so the collapse happens where the rows are written.
func TestNotificationOutboxClassifiesEachRecipientOncePostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	store := storage.NewPGXMessageStore(pool)

	parent := newChannelParent(t, store)
	// Peer wrote the parent and is named in the answer: two rules, one person.
	answer := mustCreate(t, store, channelAnswer(parent.ID, "answering and naming", notifyPeer))

	rows := readOutbox(t, pool, answer.ID)
	assertRowCount(t, rows, 1)
	assertField(t, "recipient", rows[0].Recipient, notifyPeer)
	assertField(t, "kind", rows[0].Kind, string(notificationevent.EventTypeMention))
	assertField(t, "priority", rows[0].Priority, string(notificationevent.PriorityHigh))
}

// Answering somebody who is not named still notifies them, as a reply.
func TestNotificationOutboxNotifiesTheAnsweredAuthorPostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	store := storage.NewPGXMessageStore(pool)

	parent := newChannelParent(t, store)
	answer := mustCreate(t, store, channelAnswer(parent.ID, "answering quietly"))

	rows := readOutbox(t, pool, answer.ID)
	assertRowCount(t, rows, 1)
	assertMessageEventContract(t, rows[0], answer.ID,
		notificationevent.EventTypeReply, notificationevent.PriorityHigh)
}

// A channel message that names nobody and answers nothing notifies nobody:
// channel fan-out is not produced by this issue.
func TestNotificationOutboxDoesNotFanOutChannelMessagesPostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	plain := mustCreate(t, storage.NewPGXMessageStore(pool), storage.CreateMessageInput{
		WorkspaceID: notifyWorkspace,
		ChannelID:   notifyChannel,
		SenderID:    notifyAuthor,
		BodyText:    "just talking",
		BodyFormat:  domain.MessageBodyFormatV3,
	})
	assertRowCount(t, readOutbox(t, pool, plain.ID), 0)
}

// A notification names a target and says something happened there. Sending one
// to somebody who has left tells them about activity they are no longer entitled
// to observe, so leaving ends the reply rule as well as the DM one.
func TestNotificationOutboxSkipsRecipientsWhoLostAccessPostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	store := storage.NewPGXMessageStore(pool)
	ctx := t.Context()

	parent := mustCreate(t, store, storage.CreateMessageInput{
		WorkspaceID:      notifyWorkspace,
		DMConversationID: notifyConversation,
		SenderID:         notifyThird,
		BodyText:         "written while still a member",
		BodyFormat:       domain.MessageBodyFormatV3,
	})
	if _, err := pool.Exec(ctx, `
		UPDATE chat.dm_members SET status = 'left', left_at = now()
		WHERE conversation_id = $1::uuid AND user_id = $2::uuid`,
		notifyConversation, notifyThird); err != nil {
		t.Fatalf("mark member as left: %v", err)
	}

	answer := mustCreate(t, store, storage.CreateMessageInput{
		WorkspaceID:      notifyWorkspace,
		DMConversationID: notifyConversation,
		SenderID:         notifyAuthor,
		BodyText:         "answering somebody who left",
		BodyFormat:       domain.MessageBodyFormatV3,
		ParentMessageID:  parent.ID,
	})
	for _, row := range readOutbox(t, pool, answer.ID) {
		if row.Recipient == notifyThird {
			t.Fatalf("a departed member was notified as %q", row.Kind)
		}
	}
}

// ── The state machine ───────────────────────────────────────────────────────

// newNotification creates a message and returns the id of one notification it
// produced, so a test can drive that row through the machine.
func newNotification(t *testing.T, pool *pgxpool.Pool, key string) string {
	t.Helper()
	msg := mustCreate(t, storage.NewPGXMessageStore(pool), dmInput(key))
	rows := readOutbox(t, pool, msg.ID)
	assertRowCount(t, rows, 2)
	return rows[0].ID
}

func readStatus(t *testing.T, pool *pgxpool.Pool, id string) (string, string) {
	t.Helper()
	var status string
	var reason *string
	if err := pool.QueryRow(t.Context(),
		`SELECT status, suppressed_reason FROM chat.notification_outbox WHERE id = $1::uuid`,
		id).Scan(&status, &reason); err != nil {
		t.Fatalf("read status: %v", err)
	}
	if reason == nil {
		return status, ""
	}
	return status, *reason
}

func mustTransition(t *testing.T, store *storage.PGXNotificationOutboxStore, input storage.NotificationTransitionInput) {
	t.Helper()
	if err := store.TransitionState(t.Context(), input); err != nil {
		t.Fatalf("TransitionState %q -> %q: %v", input.From, input.To, err)
	}
}

// The delivery path, end to end, through the only supported writer.
func TestNotificationOutboxWalksToDeliveredPostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	store := storage.NewPGXNotificationOutboxStore(pool)
	id := newNotification(t, pool, "notify-741-delivered")

	for _, step := range [][2]notificationevent.State{
		{notificationevent.StatePending, notificationevent.StateEligible},
		{notificationevent.StateEligible, notificationevent.StateProcessing},
		{notificationevent.StateProcessing, notificationevent.StateRetrying},
		{notificationevent.StateRetrying, notificationevent.StateProcessing},
		{notificationevent.StateProcessing, notificationevent.StateSent},
	} {
		mustTransition(t, store, storage.NotificationTransitionInput{
			NotificationID: id, From: step[0], To: step[1],
		})
	}

	status, reason := readStatus(t, pool, id)
	assertField(t, "status", status, string(notificationevent.StateSent))
	assertField(t, "suppressed_reason", reason, "")
	assertProcessedAt(t, pool, id, true)
}

func assertProcessedAt(t *testing.T, pool *pgxpool.Pool, id string, want bool) {
	t.Helper()
	var stamped bool
	if err := pool.QueryRow(t.Context(),
		`SELECT processed_at IS NOT NULL FROM chat.notification_outbox WHERE id = $1::uuid`,
		id).Scan(&stamped); err != nil {
		t.Fatalf("read processed_at: %v", err)
	}
	if stamped != want {
		t.Errorf("processed_at set = %t, want %t", stamped, want)
	}
}

// A suppression is a terminal outcome that is not a failure, and it carries its
// reason. An eligible notification does not.
func TestNotificationOutboxSuppressionIsTerminalPostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	store := storage.NewPGXNotificationOutboxStore(pool)
	id := newNotification(t, pool, "notify-741-suppressed")

	mustTransition(t, store, storage.NotificationTransitionInput{
		NotificationID:   id,
		From:             notificationevent.StatePending,
		To:               notificationevent.StateSuppressed,
		SuppressedReason: "quiet_hours",
	})
	status, reason := readStatus(t, pool, id)
	assertField(t, "status", status, string(notificationevent.StateSuppressed))
	assertField(t, "suppressed_reason", reason, "quiet_hours")
	assertProcessedAt(t, pool, id, true)

	// Nothing turns a suppression into a delivery or a failure.
	assertTransitionRefused(t, store, id, notificationevent.StateSuppressed, notificationevent.StateSent)
	assertTransitionRefused(t, store, id, notificationevent.StateSuppressed, notificationevent.StateFailed)
	assertTransitionRefused(t, store, id, notificationevent.StateSuppressed, notificationevent.StateProcessing)
}

func assertTransitionRefused(
	t *testing.T,
	store *storage.PGXNotificationOutboxStore,
	id string,
	from, to notificationevent.State,
) {
	t.Helper()
	err := store.TransitionState(t.Context(), storage.NotificationTransitionInput{
		NotificationID: id, From: from, To: to,
	})
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Errorf("TransitionState %q -> %q = %v, want ErrInvalidInput", from, to, err)
	}
}

// The database is the last authority, not the Go machine. A statement that
// bypasses the storage layer entirely is still refused, which is what makes a
// terminal state terminal rather than a naming convention.
func TestNotificationOutboxTriggerRefusesDirectTerminalEscapePostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	store := storage.NewPGXNotificationOutboxStore(pool)
	id := newNotification(t, pool, "notify-741-trigger")

	mustTransition(t, store, storage.NotificationTransitionInput{
		NotificationID:   id,
		From:             notificationevent.StatePending,
		To:               notificationevent.StateSuppressed,
		SuppressedReason: "conversation_muted",
	})

	for _, target := range []notificationevent.State{
		notificationevent.StateSent,
		notificationevent.StateFailed,
		notificationevent.StateProcessing,
		notificationevent.StatePending,
	} {
		assertDirectStatusRefused(t, pool, id, target)
	}

	// And the row is still exactly what the suppression left behind.
	status, reason := readStatus(t, pool, id)
	assertField(t, "status", status, string(notificationevent.StateSuppressed))
	assertField(t, "suppressed_reason", reason, "conversation_muted")
}

func assertDirectStatusRefused(t *testing.T, pool *pgxpool.Pool, id string, target notificationevent.State) {
	t.Helper()
	_, err := pool.Exec(t.Context(),
		`UPDATE chat.notification_outbox SET status = $2::text, suppressed_reason = NULL
		 WHERE id = $1::uuid`, id, string(target))
	if !isCheckViolation(err) {
		t.Errorf("a direct UPDATE to %q returned %v, want a refusal from the database", target, err)
	}
}

// The database refuses forward jumps too, not only escapes from a terminal.
func TestNotificationOutboxTriggerRefusesSkippedStepsPostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	id := newNotification(t, pool, "notify-741-skip")

	for _, target := range []notificationevent.State{
		notificationevent.StateSent,
		notificationevent.StateFailed,
		notificationevent.StateProcessing,
		notificationevent.StateRetrying,
	} {
		assertDirectStatusRefused(t, pool, id, target)
	}
	status, _ := readStatus(t, pool, id)
	assertField(t, "status", status, string(notificationevent.StatePending))
}

// Two workers evaluating the same notification: the compare-and-set decides, and
// the loser is told it lost rather than silently overwriting the winner.
func TestNotificationOutboxConcurrentTransitionHasOneWinnerPostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	store := storage.NewPGXNotificationOutboxStore(pool)
	id := newNotification(t, pool, "notify-741-race")
	ctx := t.Context()

	attempts := []storage.NotificationTransitionInput{
		{NotificationID: id, From: notificationevent.StatePending, To: notificationevent.StateEligible},
		{
			NotificationID:   id,
			From:             notificationevent.StatePending,
			To:               notificationevent.StateSuppressed,
			SuppressedReason: "already_read",
		},
	}
	results := make([]error, len(attempts))
	var wg sync.WaitGroup
	for i, attempt := range attempts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = store.TransitionState(ctx, attempt)
		}()
	}
	wg.Wait()

	assertExactlyOneWinner(t, results)
	status, _ := readStatus(t, pool, id)
	if status != string(notificationevent.StateEligible) && status != string(notificationevent.StateSuppressed) {
		t.Fatalf("status = %q, want whichever transition won", status)
	}
}

func assertExactlyOneWinner(t *testing.T, results []error) {
	t.Helper()
	var applied, conflicts int
	for _, err := range results {
		switch {
		case err == nil:
			applied++
		case errors.Is(err, storage.ErrNotificationStateConflict):
			conflicts++
		default:
			t.Fatalf("unexpected transition error: %v", err)
		}
	}
	if applied != 1 || conflicts != 1 {
		t.Fatalf("%d applied and %d conflicted, want exactly one of each", applied, conflicts)
	}
}

// ── The suppression reason contract ─────────────────────────────────────────

// The bound is a bound in the database too, not only in the domain package.
func TestNotificationOutboxSuppressedReasonIsBoundedPostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	id := newNotification(t, pool, "notify-741-reason-bound")

	longest := strings.Repeat("x", notificationevent.SuppressedReasonMaxLen)
	if err := suppressDirectly(t, pool, id, longest); err != nil {
		t.Fatalf("the longest permitted reason must be accepted: %v", err)
	}
	_, stored := readStatus(t, pool, id)
	if len(stored) != notificationevent.SuppressedReasonMaxLen {
		t.Fatalf("stored reason is %d characters, want %d", len(stored), notificationevent.SuppressedReasonMaxLen)
	}

	overflowed := newNotification(t, pool, "notify-741-reason-overflow")
	if err := suppressDirectly(t, pool, overflowed, longest+"x"); !isCheckViolation(err) {
		t.Fatalf("one character over the bound returned %v, want a check violation", err)
	}
}

// suppressDirectly writes the suppression with plain SQL, so the constraint is
// what answers rather than the storage layer's own validation.
func suppressDirectly(t *testing.T, pool *pgxpool.Pool, id, reason string) error {
	t.Helper()
	_, err := pool.Exec(t.Context(),
		`UPDATE chat.notification_outbox SET status = 'suppressed', suppressed_reason = $2
		 WHERE id = $1::uuid`, id, reason)
	return err
}

// A suppression with no reason, and a reason on a state that is not a
// suppression, are both rows nobody could interpret.
func TestNotificationOutboxSuppressedReasonPairingPostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	id := newNotification(t, pool, "notify-741-reason-pairing")

	if err := suppressDirectly(t, pool, id, ""); !isCheckViolation(err) {
		t.Errorf("a suppression with no reason returned %v, want a check violation", err)
	}
	_, err := pool.Exec(t.Context(),
		`UPDATE chat.notification_outbox SET status = 'eligible', suppressed_reason = 'quiet_hours'
		 WHERE id = $1::uuid`, id)
	if !isCheckViolation(err) {
		t.Errorf("a reason on an eligible notification returned %v, want a check violation", err)
	}
}

// The vocabularies the database refuses outright.
func TestNotificationOutboxRefusesUndeclaredValuesPostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	id := newNotification(t, pool, "notify-741-vocabulary")

	for name, assignment := range map[string]string{
		"an undeclared state":      `status = 'delivered'`,
		"an undeclared event type": `kind = 'gossip'`,
		"an undeclared origin":     `origin = 'backfill'`,
		"an undeclared priority":   `priority = 'urgent'`,
		"an undeclared source":     `source_type = 'thread'`,
	} {
		_, err := pool.Exec(t.Context(),
			`UPDATE chat.notification_outbox SET `+assignment+` WHERE id = $1::uuid`, id)
		if !isCheckViolation(err) {
			t.Errorf("%s returned %v, want a check violation", name, err)
		}
	}
}

// The historical marker is storable, which is the whole requirement: what a
// policy does with it is not decided here.
func TestNotificationOutboxStoresHistoricalOriginsPostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	id := newNotification(t, pool, "notify-741-origin")

	for _, origin := range []notificationevent.Origin{
		notificationevent.OriginImport,
		notificationevent.OriginReplay,
		notificationevent.OriginResync,
	} {
		if _, err := pool.Exec(t.Context(),
			`UPDATE chat.notification_outbox SET origin = $2 WHERE id = $1::uuid`,
			id, string(origin)); err != nil {
			t.Errorf("origin %q must be storable: %v", origin, err)
		}
		if !origin.IsHistorical() {
			t.Errorf("origin %q must read as historical", origin)
		}
	}
}

// ── Regression ──────────────────────────────────────────────────────────────

// Unread is a separate mechanism and stays one. Sending does not write read
// state, and reading does not touch the outbox — neither depends on a worker,
// and neither is the other's source of truth.
func TestNotificationOutboxLeavesUnreadIndependentPostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	ctx := t.Context()
	msg := mustCreate(t, storage.NewPGXMessageStore(pool), dmInput("notify-741-unread"))

	assertCount(t, pool,
		`SELECT count(*) FROM chat.conversation_read_state WHERE dm_conversation_id = $1::uuid`,
		notifyConversation, 0, "producing a notification must not write read state")

	readState := storage.NewPGXConversationReadStateStore(pool)
	if err := readState.MarkRead(ctx, notifyWorkspace, notifyPeer, "dm", notifyConversation, &msg.ID); err != nil {
		t.Fatalf("mark read: %v", err)
	}
	unread, err := readState.UnreadCounts(ctx, notifyWorkspace, notifyPeer)
	if err != nil {
		t.Fatalf("unread counts: %v", err)
	}
	if unread["dm:"+notifyConversation] != 0 {
		t.Fatalf("unread = %v, want the conversation read", unread)
	}

	// The notification is still pending. Nothing about reading a message
	// delivers, suppresses or removes the event that announced it.
	rows := readOutbox(t, pool, msg.ID)
	assertRowCount(t, rows, 2)
	for _, row := range rows {
		assertField(t, "status", row.Status, string(notificationevent.StatePending))
	}
}
