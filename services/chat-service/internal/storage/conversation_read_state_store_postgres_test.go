package storage_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// The read cursor family (issue #1082). Every test resets the chat schema and
// seeds the same small world:
//
//	workspace — reader, sender; #geral; a 1:1 DM and a group with both;
//	            a DM between sender and an outsider the reader is not in.
//	otherWS   — one channel with a message, so a foreign id is at hand.
//
// Messages carry explicit, ascending created_at values; m3 and m4 share one
// instant so the id tie-breaker of the canonical (created_at, id) order is
// exercised rather than assumed.
const (
	rsWorkspace = "d1000000-0000-4000-8000-000000000001"
	rsReader    = "d1000000-0000-4000-8000-000000000002"
	rsSender    = "d1000000-0000-4000-8000-000000000003"
	rsChannel   = "d1000000-0000-4000-8000-000000000004"
	rsDM        = "d1000000-0000-4000-8000-000000000005"
	rsGroup     = "d1000000-0000-4000-8000-000000000006"
	rsHiddenDM  = "d1000000-0000-4000-8000-000000000007"
	rsOutsider  = "d1000000-0000-4000-8000-000000000008"
	rsOtherWS   = "d1000000-0000-4000-8000-000000000009"
	rsOtherCh   = "d1000000-0000-4000-8000-00000000000a"

	rsC1 = "c1000000-0000-4000-8000-000000000001"
	rsC2 = "c1000000-0000-4000-8000-000000000002"
	// rsC3 < rsC4 by id, at the same instant.
	rsC3   = "c1000000-0000-4000-8000-000000000003"
	rsC4   = "c1000000-0000-4000-8000-000000000004"
	rsCOwn = "c1000000-0000-4000-8000-000000000005"
	rsC5   = "c1000000-0000-4000-8000-000000000006"

	rsD1       = "c2000000-0000-4000-8000-000000000001"
	rsD2       = "c2000000-0000-4000-8000-000000000002"
	rsG1       = "c3000000-0000-4000-8000-000000000001"
	rsG2       = "c3000000-0000-4000-8000-000000000002"
	rsHidden1  = "c4000000-0000-4000-8000-000000000001"
	rsForeign1 = "c5000000-0000-4000-8000-000000000001"
)

func readStatePostgres(t *testing.T) (*pgxpool.Pool, *storage.PGXConversationReadStateStore) {
	t.Helper()
	dsn := os.Getenv("CHAT_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("CHAT_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)
	var databaseName string
	if err := pool.QueryRow(ctx, `SELECT current_database()`).Scan(&databaseName); err != nil {
		t.Fatalf("read current database: %v", err)
	}
	if !strings.HasSuffix(databaseName, "_test") {
		t.Fatalf("refusing destructive read-state test against non-test database %q", databaseName)
	}
	if _, err := pool.Exec(ctx, `DROP SCHEMA IF EXISTS chat CASCADE`); err != nil {
		t.Fatalf("reset chat schema: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DROP SCHEMA IF EXISTS chat CASCADE`) })
	if _, err := pool.Exec(ctx, `
		CREATE SCHEMA IF NOT EXISTS auth;
		CREATE TABLE IF NOT EXISTS auth.users (
			id UUID PRIMARY KEY, email TEXT NOT NULL DEFAULT '', display_name TEXT NOT NULL DEFAULT '',
			full_name TEXT, avatar_url TEXT, status TEXT NOT NULL DEFAULT 'active',
			deleted_at TIMESTAMPTZ, anonymized_at TIMESTAMPTZ
		)`); err != nil {
		t.Fatalf("prepare auth schema: %v", err)
	}
	if _, err := pool.Exec(ctx, readAllChatUpMigrations(t)); err != nil {
		t.Fatalf("apply chat migrations: %v", err)
	}
	seedReadState(t, pool)
	return pool, storage.NewPGXConversationReadStateStore(pool)
}

func seedReadState(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	at := func(minute string) string { return "2026-07-15T10:" + minute + ":00.123456Z" }
	// One transaction: a workspace and its general channel are only valid
	// together, and that constraint is checked at commit.
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin seed: %v", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	for _, seed := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO auth.users (id, email, display_name) VALUES
			($1, 'reader@example.test', 'Reader'), ($2, 'sender@example.test', 'Sender'), ($3, 'out@example.test', 'Outsider')
			ON CONFLICT (id) DO NOTHING`, []any{rsReader, rsSender, rsOutsider}},
		{`INSERT INTO chat.workspaces (id, slug, name) VALUES ($1, 'read-state', 'Read state'), ($2, 'read-state-other', 'Other')`,
			[]any{rsWorkspace, rsOtherWS}},
		{`INSERT INTO chat.channels (id, workspace_id, slug, display_name, type, is_general) VALUES
			($1, $2, 'geral-read', 'Geral read', 'public', true), ($3, $4, 'geral-other', 'Geral other', 'public', true)`,
			[]any{rsChannel, rsWorkspace, rsOtherCh, rsOtherWS}},
		{`INSERT INTO chat.workspace_members (workspace_id, user_id) VALUES ($1, $2), ($1, $3), ($1, $4), ($5, $3)`,
			[]any{rsWorkspace, rsReader, rsSender, rsOutsider, rsOtherWS}},
		{`INSERT INTO chat.dm_conversations (id, workspace_id, type, title, created_by, direct_pair_key) VALUES
			($1, $4, 'direct', NULL, $5, 'read-state-pair'),
			($2, $4, 'group', 'Read group', $5, NULL),
			($3, $4, 'direct', NULL, $6, 'read-state-hidden')`,
			[]any{rsDM, rsGroup, rsHiddenDM, rsWorkspace, rsReader, rsSender}},
		{`INSERT INTO chat.dm_members (conversation_id, user_id) VALUES ($1, $4), ($1, $5), ($2, $4), ($2, $5), ($3, $5), ($3, $6)`,
			[]any{rsDM, rsGroup, rsHiddenDM, rsReader, rsSender, rsOutsider}},
		{`INSERT INTO chat.messages (id, workspace_id, channel_id, sender_id, body_text, created_at) VALUES
			($1, $7, $8, $9, 'c1', $11), ($2, $7, $8, $9, 'c2', $12),
			($3, $7, $8, $9, 'c3', $13), ($4, $7, $8, $9, 'c4', $13),
			($5, $7, $8, $10, 'own', $14), ($6, $7, $8, $9, 'c5', $15)`,
			[]any{rsC1, rsC2, rsC3, rsC4, rsCOwn, rsC5, rsWorkspace, rsChannel, rsSender, rsReader,
				at("01"), at("02"), at("03"), at("04"), at("05")}},
		{`INSERT INTO chat.messages (id, workspace_id, dm_conversation_id, sender_id, body_text, created_at) VALUES
			($1, $6, $7, $9, 'd1', $10), ($2, $6, $7, $9, 'd2', $11),
			($3, $6, $8, $9, 'g1', $10), ($4, $6, $8, $9, 'g2', $11),
			($5, $6, $12, $9, 'hidden', $10)`,
			[]any{rsD1, rsD2, rsG1, rsG2, rsHidden1, rsWorkspace, rsDM, rsGroup, rsSender, at("01"), at("02"), rsHiddenDM}},
		{`INSERT INTO chat.messages (id, workspace_id, channel_id, sender_id, body_text, created_at) VALUES ($1, $2, $3, $4, 'foreign', $5)`,
			[]any{rsForeign1, rsOtherWS, rsOtherCh, rsSender, at("01")}},
	} {
		if _, err := tx.Exec(t.Context(), seed.sql, seed.args...); err != nil {
			t.Fatalf("seed read-state cases: %v", err)
		}
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatalf("commit seed: %v", err)
	}
}

func markReadThrough(t *testing.T, store *storage.PGXConversationReadStateStore, targetType, targetID, messageID string) error {
	t.Helper()
	var id *string
	if messageID != "" {
		id = &messageID
	}
	state, err := store.MarkRead(t.Context(), rsWorkspace, rsReader, targetType, targetID, id)
	if err != nil {
		return err
	}
	// The write answers with the read state as it now stands: the same count
	// the sidebar listing reports.
	if listed := unreadFor(t, store, targetType, targetID); state.UnreadCount != listed {
		t.Fatalf("MarkRead returned unread %d, listing says %d", state.UnreadCount, listed)
	}
	return nil
}

func unreadFor(t *testing.T, store *storage.PGXConversationReadStateStore, targetType, targetID string) int {
	t.Helper()
	states, err := store.ReadStates(t.Context(), rsWorkspace, rsReader)
	if err != nil {
		t.Fatalf("ReadStates: %v", err)
	}
	return states[targetType+"\x00"+targetID].UnreadCount
}

func storedCursor(t *testing.T, pool *pgxpool.Pool, column, targetID string) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(t.Context(),
		`SELECT cursor_message_id::text FROM chat.conversation_read_state WHERE user_id = $1 AND `+column+` = $2`,
		rsReader, targetID).Scan(&id); err != nil {
		t.Fatalf("read stored cursor: %v", err)
	}
	return id
}

func TestConversationReadStatePostgreSQL_ChannelCursorAdvancesPartiallyAndNeverRegresses(t *testing.T) {
	pool, store := readStatePostgres(t)
	const channel = storage.ConversationReadTargetChannel

	// Five messages from others, one own: the own message never counts.
	if got := unreadFor(t, store, channel, rsChannel); got != 5 {
		t.Fatalf("initial unread = %d, want 5", got)
	}
	steps := []struct {
		through string
		want    int
	}{
		{rsC2, 3},
		// m3 shares m4's instant and sorts before it by id: m4 and m5 remain.
		{rsC3, 2},
		// A regression and a duplicate change nothing.
		{rsC1, 2},
		{rsC3, 2},
		// The own message is a valid position; only c5 is after it.
		{rsCOwn, 1},
		{rsC5, 0},
	}
	for _, step := range steps {
		if err := markReadThrough(t, store, channel, rsChannel, step.through); err != nil {
			t.Fatalf("mark read through %s: %v", step.through, err)
		}
		if got := unreadFor(t, store, channel, rsChannel); got != step.want {
			t.Fatalf("after reading through %s unread = %d, want %d", step.through, got, step.want)
		}
	}
	if got := storedCursor(t, pool, "channel_id", rsChannel); got != rsC5 {
		t.Fatalf("stored cursor = %s, want %s", got, rsC5)
	}
	// The cursor is the message's own instant, never the request time; the
	// legacy boundary sits one microsecond below it, covering only what the
	// cursor covers.
	var cursorIsTheMessage, boundaryBelowIt bool
	if err := pool.QueryRow(t.Context(), `
		SELECT rs.cursor_created_at = m.created_at, rs.last_read_at = m.created_at - interval '1 microsecond'
		FROM chat.conversation_read_state rs
		JOIN chat.messages m ON m.id = rs.cursor_message_id
		WHERE rs.user_id = $1 AND rs.channel_id = $2`, rsReader, rsChannel).Scan(&cursorIsTheMessage, &boundaryBelowIt); err != nil {
		t.Fatalf("read cursor: %v", err)
	}
	if !cursorIsTheMessage || !boundaryBelowIt {
		t.Fatalf("cursor at the message = %v, boundary just below it = %v", cursorIsTheMessage, boundaryBelowIt)
	}
}

func TestConversationReadStatePostgreSQL_OutOfOrderRequestsKeepTheGreatestCursor(t *testing.T) {
	pool, store := readStatePostgres(t)
	const channel = storage.ConversationReadTargetChannel
	// Tab A → c2, tab B → c4, tab A late → c3: the result is c4.
	for _, through := range []string{rsC2, rsC4, rsC3} {
		if err := markReadThrough(t, store, channel, rsChannel, through); err != nil {
			t.Fatalf("mark read through %s: %v", through, err)
		}
	}
	if got := storedCursor(t, pool, "channel_id", rsChannel); got != rsC4 {
		t.Fatalf("stored cursor = %s, want %s", got, rsC4)
	}
	if got := unreadFor(t, store, channel, rsChannel); got != 1 {
		t.Fatalf("unread = %d, want 1", got)
	}
}

func TestConversationReadStatePostgreSQL_ConcurrentWritersConvergeOnTheGreatestCursor(t *testing.T) {
	pool, store := readStatePostgres(t)
	const channel = storage.ConversationReadTargetChannel
	cursors := []string{rsC1, rsC2, rsC3, rsC4, rsCOwn, rsC5}
	var wg sync.WaitGroup
	errs := make(chan error, len(cursors)*4)
	for range 4 {
		for _, through := range cursors {
			wg.Add(1)
			go func() {
				defer wg.Done()
				id := through
				_, err := store.MarkRead(context.Background(), rsWorkspace, rsReader, channel, rsChannel, &id)
				errs <- err
			}()
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent mark read: %v", err)
		}
	}
	// Whatever order the writers ran in, the persisted cursor is the greatest.
	if got := storedCursor(t, pool, "channel_id", rsChannel); got != rsC5 {
		t.Fatalf("stored cursor = %s, want %s", got, rsC5)
	}
	if got := unreadFor(t, store, channel, rsChannel); got != 0 {
		t.Fatalf("unread = %d, want 0", got)
	}
}

func TestConversationReadStatePostgreSQL_DirectAndGroupConversationsShareTheRule(t *testing.T) {
	_, store := readStatePostgres(t)
	const dm = storage.ConversationReadTargetDM
	for _, conv := range []struct{ id, first, last string }{
		{rsDM, rsD1, rsD2},
		{rsGroup, rsG1, rsG2},
	} {
		requireUnread(t, store, dm, conv.id, 2)
		requireReadThrough(t, store, dm, conv.id, conv.first)
		requireUnread(t, store, dm, conv.id, 1)
		requireReadThrough(t, store, dm, conv.id, conv.last)
		requireUnread(t, store, dm, conv.id, 0)
	}
}

func TestConversationReadStatePostgreSQL_RefusesMessagesAndConversationsOutsideTheCaller(t *testing.T) {
	_, store := readStatePostgres(t)
	const (
		channel = storage.ConversationReadTargetChannel
		dm      = storage.ConversationReadTargetDM
		missing = "c9000000-0000-4000-8000-000000000099"
	)
	cases := []struct {
		name, targetType, targetID, messageID string
	}{
		{"message from another conversation", channel, rsChannel, rsD1},
		{"message from another workspace", channel, rsChannel, rsForeign1},
		{"message that does not exist", channel, rsChannel, missing},
		{"conversation the reader is not in", dm, rsHiddenDM, rsHidden1},
		{"conversation the reader is not in, mark all", dm, rsHiddenDM, ""},
		{"conversation in another workspace", channel, rsOtherCh, rsForeign1},
		{"conversation that does not exist", channel, missing, ""},
	}
	for _, tc := range cases {
		if err := markReadThrough(t, store, tc.targetType, tc.targetID, tc.messageID); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("%s: want non-enumerating ErrNotFound, got %v", tc.name, err)
		}
	}
	// None of the refusals moved anything.
	if got := unreadFor(t, store, channel, rsChannel); got != 5 {
		t.Fatalf("channel unread = %d, want 5", got)
	}
	if got := unreadFor(t, store, dm, rsDM); got != 2 {
		t.Fatalf("dm unread = %d, want 2", got)
	}
}

func TestConversationReadStatePostgreSQL_MarkAllReadsTheNewestVisibleMessage(t *testing.T) {
	pool, store := readStatePostgres(t)
	const channel = storage.ConversationReadTargetChannel
	if err := markReadThrough(t, store, channel, rsChannel, rsC2); err != nil {
		t.Fatalf("partial read: %v", err)
	}
	// A message withheld by link scanning is not something the reader can have
	// seen, so mark-all stops before it and it stays unread once released.
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO chat.messages (id, workspace_id, channel_id, sender_id, body_text, created_at, status)
		VALUES ('c1000000-0000-4000-8000-000000000007', $1, $2, $3, 'withheld', '2026-07-15T10:09:00Z', 'pending_link_scan')`,
		rsWorkspace, rsChannel, rsSender); err != nil {
		t.Fatalf("seed withheld message: %v", err)
	}
	for range 2 {
		if err := markReadThrough(t, store, channel, rsChannel, ""); err != nil {
			t.Fatalf("mark all read: %v", err)
		}
	}
	if got := storedCursor(t, pool, "channel_id", rsChannel); got != rsC5 {
		t.Fatalf("stored cursor = %s, want %s", got, rsC5)
	}
	if _, err := pool.Exec(t.Context(), `UPDATE chat.messages SET status = 'active' WHERE id = 'c1000000-0000-4000-8000-000000000007'`); err != nil {
		t.Fatalf("release withheld message: %v", err)
	}
	if got := unreadFor(t, store, channel, rsChannel); got != 1 {
		t.Fatalf("unread after release = %d, want 1", got)
	}
}

// ── Interoperability with the release before #1082 ─────────────────────────
//
// Blue/green runs the previous release against the same database, before the
// cutover and after any rollback. These run that release's own SQL — its
// writer and its reader, verbatim but for now(), which is passed in so the
// request instant is deterministic — against the schema and the store of this
// one, and assert the unread count each side sees at every step.

// oldWriter is the pre-#1082 POST …/read for a channel: "everything up to the
// request instant is read", with an informational message id.
const oldWriter = `
	INSERT INTO chat.conversation_read_state
		(user_id, workspace_id, channel_id, last_read_message_id, last_read_at)
	VALUES ($1, $2, $3, $4, $5)
	ON CONFLICT (user_id, channel_id) WHERE channel_id IS NOT NULL DO UPDATE
	SET last_read_message_id = EXCLUDED.last_read_message_id,
		last_read_at = EXCLUDED.last_read_at,
		updated_at = EXCLUDED.last_read_at
	WHERE chat.conversation_read_state.last_read_at <= EXCLUDED.last_read_at`

// oldReader is the pre-#1082 unread count for a channel.
const oldReader = `
	SELECT COUNT(*) FROM chat.messages m
	LEFT JOIN chat.conversation_read_state rs ON rs.user_id = $1 AND rs.channel_id = m.channel_id
	WHERE m.channel_id = $2 AND m.status = 'active' AND m.sender_id <> $1
	  AND m.created_at > COALESCE(rs.last_read_at, '-infinity'::timestamptz)`

func oldMarkRead(t *testing.T, pool *pgxpool.Pool, requestAt string, informational *string) {
	t.Helper()
	if _, err := pool.Exec(t.Context(), oldWriter, rsReader, rsWorkspace, rsChannel, informational, requestAt); err != nil {
		t.Fatalf("old writer: %v", err)
	}
}

func oldUnread(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(t.Context(), oldReader, rsReader, rsChannel).Scan(&count); err != nil {
		t.Fatalf("old reader: %v", err)
	}
	return count
}

// The instants of the seeded channel messages: c3 and c4 share 10:03.
const (
	rsBetween12 = "2026-07-15T10:01:30Z"
	rsBetween23 = "2026-07-15T10:02:30Z"
	rsAtC3C4    = "2026-07-15T10:03:00.123456Z"
	rsAfterOwn  = "2026-07-15T10:04:30Z"
	rsAfterAll  = "2026-07-15T10:06:00Z"
)

// requireReadThrough marks the channel or DM read through messageID and fails
// the test on any error.
func requireReadThrough(t *testing.T, store *storage.PGXConversationReadStateStore, targetType, targetID, messageID string) {
	t.Helper()
	if err := markReadThrough(t, store, targetType, targetID, messageID); err != nil {
		t.Fatalf("mark %s read through %s: %v", targetID, messageID, err)
	}
}

func requireUnread(t *testing.T, store *storage.PGXConversationReadStateStore, targetType, targetID string, want int) {
	t.Helper()
	if got := unreadFor(t, store, targetType, targetID); got != want {
		t.Fatalf("new reader: unread for %s = %d, want %d", targetID, got, want)
	}
}

func requireOldUnread(t *testing.T, pool *pgxpool.Pool, want int) {
	t.Helper()
	if got := oldUnread(t, pool); got != want {
		t.Fatalf("old reader: unread = %d, want %d", got, want)
	}
}

func TestConversationReadStatePostgreSQL_RowsTheOldReleaseWroteKeepTheirMeaning(t *testing.T) {
	missing := "c9000000-0000-4000-8000-0000000000aa"
	c1, c2 := rsC1, rsC2
	cases := []struct {
		name       string
		requestAt  string
		messageID  *string
		wantUnread int
	}{
		// A. No id at all: everything up to the instant is read.
		{name: "null id", requestAt: rsBetween23, wantUnread: 3},
		// An informational id on the instant c3 and c4 share: both are read,
		// whatever their ids — the id never breaks ties.
		{name: "informational id on a tied instant", requestAt: rsAtC3C4, messageID: &c1, wantUnread: 1},
		{name: "id of a missing message", requestAt: rsBetween23, messageID: &missing, wantUnread: 3},
		{name: "instant after the id's message", requestAt: rsAfterOwn, messageID: &c2, wantUnread: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pool, store := readStatePostgres(t)
			oldMarkRead(t, pool, tc.requestAt, tc.messageID)
			requireUnread(t, store, storage.ConversationReadTargetChannel, rsChannel, tc.wantUnread)
			requireOldUnread(t, pool, tc.wantUnread)
		})
	}
}

func TestConversationReadStatePostgreSQL_TheOldWriterCannotCorruptThePreciseCursor(t *testing.T) {
	const channel = storage.ConversationReadTargetChannel
	pool, store := readStatePostgres(t)

	// B. New reads through c2: c3, c4 and c5 remain.
	requireReadThrough(t, store, channel, rsChannel, rsC2)
	requireUnread(t, store, channel, rsChannel, 3)

	// F. The old release writes after it, with an informational id far ahead
	// (c5). The id is never a position: nothing past the old instant is read.
	c5 := rsC5
	oldMarkRead(t, pool, rsBetween23, &c5)
	requireUnread(t, store, channel, rsChannel, 3)
	requireOldUnread(t, pool, 3)

	// New keeps reading by position, tie-break included (E): c3 before c4.
	requireReadThrough(t, store, channel, rsChannel, rsC3)
	requireUnread(t, store, channel, rsChannel, 2)

	// F again, on the tied instant itself, with c3's id: the old claim covers
	// the whole instant — c4 too — and its id decides nothing.
	c3 := rsC3
	oldMarkRead(t, pool, rsAtC3C4, &c3)
	requireUnread(t, store, channel, rsChannel, 1)
	requireOldUnread(t, pool, 1)
}

func TestConversationReadStatePostgreSQL_EitherReleaseAdvancesWhatTheOtherWrote(t *testing.T) {
	const channel = storage.ConversationReadTargetChannel
	t.Run("C: the old release reads past the precise cursor", func(t *testing.T) {
		pool, store := readStatePostgres(t)
		requireReadThrough(t, store, channel, rsChannel, rsC2)
		oldMarkRead(t, pool, rsAfterOwn, nil)
		requireUnread(t, store, channel, rsChannel, 1)
		requireOldUnread(t, pool, 1)
	})

	t.Run("D: the new release reads past the legacy boundary", func(t *testing.T) {
		pool, store := readStatePostgres(t)
		oldMarkRead(t, pool, rsBetween23, nil)
		requireUnread(t, store, channel, rsChannel, 3)
		// c3 is past the boundary; c4 — same instant, later id — stays unread.
		requireReadThrough(t, store, channel, rsChannel, rsC3)
		requireUnread(t, store, channel, rsChannel, 2)
		// An older message cannot pull anything back.
		requireReadThrough(t, store, channel, rsChannel, rsC1)
		requireUnread(t, store, channel, rsChannel, 2)
	})
}

func TestConversationReadStatePostgreSQL_RollingBackNeverMarksUnseenMessagesRead(t *testing.T) {
	const channel = storage.ConversationReadTargetChannel
	t.Run("G: the old release reads what the new one wrote", func(t *testing.T) {
		pool, store := readStatePostgres(t)
		// Read through c3: c4 (same instant, later id) and c5 are unseen.
		requireReadThrough(t, store, channel, rsChannel, rsC3)
		requireUnread(t, store, channel, rsChannel, 2)
		// The old release sees c3 again, but never c4 or c5 as read.
		requireOldUnread(t, pool, 3)
	})

	t.Run("M: the down migration leaves only what the old release can read", func(t *testing.T) {
		pool, store := readStatePostgres(t)
		requireReadThrough(t, store, channel, rsChannel, rsC3)
		if _, err := pool.Exec(t.Context(), readChatMigration(t, "000069_conversation_read_cursor.down.sql")); err != nil {
			t.Fatalf("apply down migration: %v", err)
		}
		requireOldUnread(t, pool, 3)
		// And the old release keeps working on it.
		oldMarkRead(t, pool, rsAfterAll, nil)
		requireOldUnread(t, pool, 0)
	})
}

func TestConversationReadStatePostgreSQL_MarkAllInEitherRelease(t *testing.T) {
	const channel = storage.ConversationReadTargetChannel
	t.Run("old mark-all", func(t *testing.T) {
		pool, store := readStatePostgres(t)
		oldMarkRead(t, pool, rsAfterAll, nil)
		requireUnread(t, store, channel, rsChannel, 0)
	})
	t.Run("new mark-all, then an old one", func(t *testing.T) {
		pool, store := readStatePostgres(t)
		requireReadThrough(t, store, channel, rsChannel, "")
		requireUnread(t, store, channel, rsChannel, 0)
		requireOldUnread(t, pool, 1) // c5's own instant is just past the boundary.
		oldMarkRead(t, pool, rsAfterAll, nil)
		requireUnread(t, store, channel, rsChannel, 0)
		requireOldUnread(t, pool, 0)
	})
}

func TestConversationReadStatePostgreSQL_OldAndNewWritersConcurrentlyAndOutOfOrder(t *testing.T) {
	const channel = storage.ConversationReadTargetChannel
	t.Run("K: concurrently", func(t *testing.T) {
		pool, store := readStatePostgres(t)
		writeFromBothReleasesConcurrently(t, pool, store)
		// Whatever the order: the greatest cursor (c4) and the greatest instant.
		requireUnread(t, store, channel, rsChannel, 1)
		if got := storedCursor(t, pool, "channel_id", rsChannel); got != rsC4 {
			t.Fatalf("cursor = %s, want %s", got, rsC4)
		}
	})

	t.Run("L: out of order", func(t *testing.T) {
		pool, store := readStatePostgres(t)
		requireReadThrough(t, store, channel, rsChannel, rsC4)
		oldMarkRead(t, pool, rsBetween12, nil)                 // a late, older old write
		requireReadThrough(t, store, channel, rsChannel, rsC2) // a late, older new write
		requireUnread(t, store, channel, rsChannel, 1)
		if got := storedCursor(t, pool, "channel_id", rsChannel); got != rsC4 {
			t.Fatalf("cursor = %s, want %s", got, rsC4)
		}
	})
}

// writeFromBothReleasesConcurrently races, twice over, a new-release write to
// each of c1..c4 against old-release writes at instants between them.
func writeFromBothReleasesConcurrently(t *testing.T, pool *pgxpool.Pool, store *storage.PGXConversationReadStateStore) {
	t.Helper()
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for range 2 {
		for _, through := range []string{rsC1, rsC2, rsC3, rsC4} {
			wg.Go(func() {
				_, err := store.MarkRead(context.Background(), rsWorkspace, rsReader, storage.ConversationReadTargetChannel, rsChannel, &through)
				errs <- err
			})
		}
		for _, requestAt := range []string{rsBetween12, rsBetween23} {
			wg.Go(func() {
				_, err := pool.Exec(context.Background(), oldWriter, rsReader, rsWorkspace, rsChannel, nil, requestAt)
				errs <- err
			})
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent writer: %v", err)
		}
	}
}

func TestConversationReadStatePostgreSQL_DirectAndGroupConversationsInterleaveTheSameWay(t *testing.T) {
	const dm = storage.ConversationReadTargetDM
	for _, conv := range []struct{ name, id, first, last string }{
		{"direct", rsDM, rsD1, rsD2},
		{"group", rsGroup, rsG1, rsG2},
	} {
		t.Run(conv.name, func(t *testing.T) {
			pool, store := readStatePostgres(t)
			// An old-release row, informational id ahead of its instant.
			if _, err := pool.Exec(t.Context(), `
				INSERT INTO chat.conversation_read_state (user_id, workspace_id, dm_conversation_id, last_read_message_id, last_read_at)
				VALUES ($1, $2, $3, $4, $5)`, rsReader, rsWorkspace, conv.id, conv.last, rsBetween12); err != nil {
				t.Fatalf("seed old row: %v", err)
			}
			requireUnread(t, store, dm, conv.id, 1)
			requireReadThrough(t, store, dm, conv.id, conv.first)
			requireUnread(t, store, dm, conv.id, 1)
			requireReadThrough(t, store, dm, conv.id, conv.last)
			requireUnread(t, store, dm, conv.id, 0)
		})
	}
}

// #1082 seventh review: a message the client still holds, deleted on the
// server with no event delivered, is not in the count the client is given —
// so the client cannot take a read of it off that count. The count falls by
// what the write actually reads: 5 → (delete) 4 → (read through c3) 2.
func TestConversationReadStatePostgreSQL_ADeletedMessageLeavesTheCountItWasNeverIn(t *testing.T) {
	pool, store := readStatePostgres(t)
	const channel = storage.ConversationReadTargetChannel
	requireUnread(t, store, channel, rsChannel, 5)

	if _, _, err := storage.NewPGXMessageStore(pool).DeleteMessage(t.Context(), storage.DeleteMessageInput{
		WorkspaceID: rsWorkspace, MessageID: rsC1, RequesterID: rsSender,
	}); err != nil {
		t.Fatalf("delete c1: %v", err)
	}
	// The GET a client holding c1..c5 is answered with.
	requireUnread(t, store, channel, rsChannel, 4)

	// The client read c1, c2 and c3; its write names c3.
	state, err := store.MarkRead(t.Context(), rsWorkspace, rsReader, channel, rsChannel, ptr(rsC3))
	if err != nil {
		t.Fatalf("MarkRead: %v", err)
	}
	if state.UnreadCount != 2 {
		t.Fatalf("unread after the write = %d, want 2 (c4, c5)", state.UnreadCount)
	}
	if got := storedCursor(t, pool, "channel_id", rsChannel); got != rsC3 {
		t.Fatalf("cursor = %s, want %s", got, rsC3)
	}
}

func TestConversationReadStatePostgreSQL_NeverReadReportsNoPoint(t *testing.T) {
	_, store := readStatePostgres(t)
	never := channelReadState(t, store)
	if never.ReadThrough != nil {
		t.Fatalf("never-read point = %+v", never.ReadThrough)
	}
}

func TestConversationReadStatePostgreSQL_AMessageCursorReportsItsMessage(t *testing.T) {
	_, store := readStatePostgres(t)
	state, err := store.MarkRead(t.Context(), rsWorkspace, rsReader, storage.ConversationReadTargetChannel, rsChannel, ptr(rsC3))
	if err != nil {
		t.Fatalf("MarkRead: %v", err)
	}
	if state.ReadThrough == nil || state.ReadThrough.MessageID == nil || *state.ReadThrough.MessageID != rsC3 {
		t.Fatalf("cursor point = %+v", state.ReadThrough)
	}
}

func TestConversationReadStatePostgreSQL_AnOldClaimPastTheCursorReportsItsInstant(t *testing.T) {
	pool, store := readStatePostgres(t)
	requireReadThrough(t, store, storage.ConversationReadTargetChannel, rsChannel, rsC3)
	c1 := rsC1
	oldMarkRead(t, pool, rsAfterOwn, &c1)
	// The bare instant, never the old claim's informational id.
	point := channelReadState(t, store).ReadThrough
	if point == nil || point.MessageID != nil || point.CreatedAt.UTC().Format("15:04:05") != "10:04:30" {
		t.Fatalf("legacy-led point = %+v", point)
	}
}

// channelReadState is the reader's read state for rsChannel, as the sidebar lists it.
func channelReadState(t *testing.T, store *storage.PGXConversationReadStateStore) domain.ConversationReadState {
	t.Helper()
	states, err := store.ReadStates(t.Context(), rsWorkspace, rsReader)
	if err != nil {
		t.Fatalf("ReadStates: %v", err)
	}
	return states[storage.ConversationReadTargetChannel+"\x00"+rsChannel]
}
