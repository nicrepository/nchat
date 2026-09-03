package storage_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// TestPGXMessageStore_AllMentionGroupRecipientsPostgreSQL is the authoritative
// evidence for issue #776's central claim: @all in a group DM notifies the
// group's active, authorized members and nobody else, derived from
// chat.dm_members at the exact instant CreateMessage's INSERT runs — never
// from a list the service computed ahead of time.
//
// Opt-in like its neighbours: needs CHAT_TEST_DATABASE_URL against a _test
// database carrying the real migrations.
func TestPGXMessageStore_AllMentionGroupRecipientsPostgreSQL(t *testing.T) {
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
		t.Fatalf("refusing destructive test against non-test database %q", databaseName)
	}
	if _, err := pool.Exec(ctx, `DROP SCHEMA IF EXISTS chat CASCADE`); err != nil {
		t.Fatalf("reset chat schema: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DROP SCHEMA IF EXISTS chat CASCADE`) })
	if _, err := pool.Exec(ctx, `
		CREATE SCHEMA IF NOT EXISTS auth;
		CREATE TABLE IF NOT EXISTS auth.users (
			id UUID PRIMARY KEY,
			email TEXT NOT NULL DEFAULT '',
			display_name TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'active',
			deleted_at TIMESTAMPTZ
		)`); err != nil {
		t.Fatalf("prepare auth schema: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		ALTER TABLE auth.users
			ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'active',
			ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ,
			ADD COLUMN IF NOT EXISTS email TEXT NOT NULL DEFAULT '',
			ADD COLUMN IF NOT EXISTS display_name TEXT NOT NULL DEFAULT ''`); err != nil {
		t.Fatalf("align auth.users columns: %v", err)
	}
	if _, err := pool.Exec(ctx, readAllChatUpMigrations(t)); err != nil {
		t.Fatalf("apply chat migrations: %v", err)
	}

	const (
		workspace = "f1000000-0000-4000-8000-000000000001"
		otherWS   = "f2000000-0000-4000-8000-000000000001"

		sender        = "f1000000-0000-4000-8000-00000000000a"
		active1       = "f1000000-0000-4000-8000-00000000000b"
		active2       = "f1000000-0000-4000-8000-00000000000c"
		removed       = "f1000000-0000-4000-8000-00000000000d" // dm_members.status = 'left'
		suspendedWS   = "f1000000-0000-4000-8000-00000000000e" // dm_members active, workspace_members suspended
		otherGroupper = "f1000000-0000-4000-8000-00000000000f" // active elsewhere in the workspace, not in this group
		foreignMember = "f1000000-0000-4000-8000-000000000010" // workspace_members row only in otherWS
		deletedUser   = "f1000000-0000-4000-8000-000000000011" // dm_members + workspace_members active, auth.users.deleted_at set

		group      = "f1000000-0000-4000-8000-0000000000a0"
		otherGroup = "f1000000-0000-4000-8000-0000000000a1"
		direct     = "f1000000-0000-4000-8000-0000000000a2"

		generalChannel      = "f1000000-0000-4000-8000-0000000000b0"
		otherGeneralChannel = "f1000000-0000-4000-8000-0000000000b1"
	)

	seed := []struct {
		sql  string
		args []any
	}{
		// ON CONFLICT DO UPDATE: this schema (unlike chat, which the test drops
		// and recreates) is never reset between runs against a persistent test
		// database, so a repeat run must reconcile the same fixture rather than
		// collide on their fixed UUIDs.
		{sql: `INSERT INTO auth.users (id, email, display_name, status, deleted_at) VALUES
			($1, 'sender@example.test', 'Sender', 'active', NULL),
			($2, 'active1@example.test', 'Active One', 'active', NULL),
			($3, 'active2@example.test', 'Active Two', 'active', NULL),
			($4, 'removed@example.test', 'Removed', 'active', NULL),
			($5, 'suspended@example.test', 'Suspended', 'active', NULL),
			($6, 'other@example.test', 'Other Grouper', 'active', NULL),
			($7, 'foreign@example.test', 'Foreign Member', 'active', NULL),
			($8, 'deleted@example.test', 'Deleted Account', 'active', now())
			ON CONFLICT (id) DO UPDATE SET status = EXCLUDED.status, deleted_at = EXCLUDED.deleted_at`,
			args: []any{sender, active1, active2, removed, suspendedWS, otherGroupper, foreignMember, deletedUser}},
		{sql: `INSERT INTO chat.workspaces (id, slug, name) VALUES
			($1, 'all-mention-ws', 'All Mention WS'),
			($2, 'all-mention-other-ws', 'All Mention Other WS')`,
			args: []any{workspace, otherWS}},
		// Every workspace needs exactly one active public general channel
		// (migration 000002's deferred trigger) — unrelated to this feature, but
		// required for either seeded workspace to commit at all.
		{sql: `INSERT INTO chat.channels (id, workspace_id, slug, display_name, type, is_general, status) VALUES
			($1, $3, 'geral', 'geral', 'public', true, 'active'),
			($2, $4, 'geral', 'geral', 'public', true, 'active')`,
			args: []any{generalChannel, otherGeneralChannel, workspace, otherWS}},
		{sql: `INSERT INTO chat.workspace_members (workspace_id, user_id, status) VALUES
			($1, $2, 'active'), ($1, $3, 'active'), ($1, $4, 'active'),
			($1, $5, 'active'), ($1, $6, 'suspended'), ($1, $7, 'active'), ($1, $8, 'active')`,
			args: []any{workspace, sender, active1, active2, removed, suspendedWS, otherGroupper, deletedUser}},
		// foreignMember belongs only to otherWS, never to workspace.
		{sql: `INSERT INTO chat.workspace_members (workspace_id, user_id, status) VALUES ($1, $2, 'active')`,
			args: []any{otherWS, foreignMember}},
		{sql: `INSERT INTO chat.dm_conversations (id, workspace_id, type, title, status, created_by) VALUES
			($1, $3, 'group', 'All Mention Group', 'active', $4),
			($2, $3, 'group', 'Other Group', 'active', $4)`,
			args: []any{group, otherGroup, workspace, sender}},
		{sql: `INSERT INTO chat.dm_conversations (id, workspace_id, type, direct_pair_key, status, created_by) VALUES
			($1, $2, 'direct', 'pair-sender-active1', 'active', $3)`,
			args: []any{direct, workspace, sender}},
		// The group's roster: sender, two active members, and one whose
		// *workspace* membership is suspended even though their dm_members row
		// still says active — the recipient CTE must fail that one on the
		// workspace_members join, not just the dm_members one. "removed" starts
		// active too and is taken out below via the same UPDATE path a real
		// leave/kick would use, since dm_members_left_at_check requires left_at
		// alongside status = 'left' and a bare INSERT cannot set both correctly
		// for a row that starts active.
		{sql: `INSERT INTO chat.dm_members (conversation_id, user_id, status) VALUES
			($1, $2, 'active'), ($1, $3, 'active'), ($1, $4, 'active'),
			($1, $5, 'active'), ($1, $6, 'active'), ($1, $7, 'active')`,
			args: []any{group, sender, active1, active2, removed, suspendedWS, deletedUser}},
		{sql: `UPDATE chat.dm_members SET status = 'left', left_at = now()
			WHERE conversation_id = $1 AND user_id = $2`,
			args: []any{group, removed}},
		// foreignMember is forced into the group's dm_members directly, in SQL,
		// bypassing every application-level eligibility check the real
		// AddGroupParticipants path would apply — the only way to prove the SQL
		// predicate itself (not the service layer in front of it) refuses a
		// cross-workspace row, should one ever exist through a bug or a manual
		// data fix elsewhere.
		{sql: `INSERT INTO chat.dm_members (conversation_id, user_id, status) VALUES ($1, $2, 'active')`,
			args: []any{group, foreignMember}},
		// otherGroupper belongs to a *different* group in the same workspace —
		// present to prove @all never leaks across conversations.
		{sql: `INSERT INTO chat.dm_members (conversation_id, user_id, status) VALUES ($1, $2, 'active'), ($1, $3, 'active')`,
			args: []any{otherGroup, sender, otherGroupper}},
		{sql: `INSERT INTO chat.dm_members (conversation_id, user_id, status) VALUES ($1, $2, 'active'), ($1, $3, 'active')`,
			args: []any{direct, sender, active1}},
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin seed: %v", err)
	}
	for _, s := range seed {
		if _, err := tx.Exec(ctx, s.sql, s.args...); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatalf("seed: %v (%s)", err, s.sql)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit seed: %v", err)
	}

	store := storage.NewPGXMessageStore(pool)

	mentionRecipients := func(t *testing.T, messageID string) []string {
		t.Helper()
		rows, err := pool.Query(ctx,
			`SELECT recipient_user_id::text FROM chat.notification_outbox
			 WHERE message_id = $1 AND kind = 'mention' ORDER BY recipient_user_id`, messageID)
		if err != nil {
			t.Fatalf("query notification_outbox: %v", err)
		}
		defer rows.Close()
		var got []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				t.Fatalf("scan recipient: %v", err)
			}
			got = append(got, id)
		}
		return got
	}

	t.Run("@all notifies active authorized members only, sender included", func(t *testing.T) {
		msg, err := store.CreateMessage(ctx, storage.CreateMessageInput{
			WorkspaceID: workspace, DMConversationID: group, SenderID: sender,
			Kind:                   domain.MessageKindUser,
			BodyText:               `@[all](mention:all:00000000-0000-0000-0000-000000000000)`,
			BodyFormat:             domain.MessageBodyFormatV3,
			MentionAllGroupMembers: true,
		})
		if err != nil {
			t.Fatalf("CreateMessage: %v", err)
		}
		got := mentionRecipients(t, msg.ID)
		want := []string{sender, active1, active2}
		if !sameSet(got, want) {
			t.Fatalf("recipients = %v, want %v (no removed member, no suspended workspace member, no other group's member, no cross-workspace member, no deleted account)", got, want)
		}
	})

	t.Run("@all plus an individual mention of the same person is not double-notified", func(t *testing.T) {
		msg, err := store.CreateMessage(ctx, storage.CreateMessageInput{
			WorkspaceID: workspace, DMConversationID: group, SenderID: sender,
			Kind: domain.MessageKindUser,
			BodyText: `@[all](mention:all:00000000-0000-0000-0000-000000000000) ` +
				`@[Active One](mention:user:` + active1 + `)`,
			BodyFormat:             domain.MessageBodyFormatV3,
			MentionedUserIDs:       []string{active1},
			MentionAllGroupMembers: true,
		})
		if err != nil {
			t.Fatalf("CreateMessage: %v", err)
		}
		got := mentionRecipients(t, msg.ID)
		want := []string{sender, active1, active2}
		if !sameSet(got, want) {
			t.Fatalf("recipients = %v, want %v (deduplicated)", got, want)
		}
	})

	t.Run("retried idempotency key does not duplicate outbox rows", func(t *testing.T) {
		input := storage.CreateMessageInput{
			WorkspaceID: workspace, DMConversationID: group, SenderID: sender,
			Kind:                   domain.MessageKindUser,
			BodyText:               `@[all](mention:all:00000000-0000-0000-0000-000000000000) retry`,
			BodyFormat:             domain.MessageBodyFormatV3,
			MentionAllGroupMembers: true,
			IdempotencyKey:         "retry-key-1",
			RequestFingerprint:     "fp-retry-1",
		}
		first, err := store.CreateMessage(ctx, input)
		if err != nil {
			t.Fatalf("first CreateMessage: %v", err)
		}
		if _, err := store.CreateMessage(ctx, input); err != storage.ErrCreateReplay {
			t.Fatalf("expected ErrCreateReplay on retry, got %v", err)
		}
		got := mentionRecipients(t, first.ID)
		want := []string{sender, active1, active2}
		if !sameSet(got, want) {
			t.Fatalf("recipients after retry = %v, want %v (no duplicates)", got, want)
		}
	})

	t.Run("a member removed before send never receives the notification", func(t *testing.T) {
		// Re-add "removed" as active, then remove them again in a separate,
		// already-committed statement before CreateMessage is even called. This
		// is the TOCTOU case #776 actually promises: a membership change that
		// *committed* before the send reaches the database is always reflected,
		// because CreateMessage reads chat.dm_members itself rather than trusting
		// a list computed earlier. It is not a test of a write racing
		// CreateMessage's own statement — that scenario is ordinary Postgres
		// read-committed visibility, not a guarantee this design makes (see the
		// CreateMessageInput.MentionAllGroupMembers doc).
		if _, err := pool.Exec(ctx, `UPDATE chat.dm_members SET status = 'active', left_at = NULL
			WHERE conversation_id = $1 AND user_id = $2`, group, removed); err != nil {
			t.Fatalf("re-add removed member: %v", err)
		}
		if _, err := pool.Exec(ctx, `UPDATE chat.dm_members SET status = 'left', left_at = now()
			WHERE conversation_id = $1 AND user_id = $2`, group, removed); err != nil {
			t.Fatalf("remove member again: %v", err)
		}
		msg, err := store.CreateMessage(ctx, storage.CreateMessageInput{
			WorkspaceID: workspace, DMConversationID: group, SenderID: sender,
			Kind:                   domain.MessageKindUser,
			BodyText:               `@[all](mention:all:00000000-0000-0000-0000-000000000000) toctou`,
			BodyFormat:             domain.MessageBodyFormatV3,
			MentionAllGroupMembers: true,
		})
		if err != nil {
			t.Fatalf("CreateMessage: %v", err)
		}
		got := mentionRecipients(t, msg.ID)
		for _, id := range got {
			if id == removed {
				t.Fatalf("removed member received a notification: %v", got)
			}
		}
	})

	t.Run("a direct DM never expands, even if the flag is set (defense in depth)", func(t *testing.T) {
		msg, err := store.CreateMessage(ctx, storage.CreateMessageInput{
			WorkspaceID: workspace, DMConversationID: direct, SenderID: sender,
			Kind:                   domain.MessageKindUser,
			BodyText:               `@[all](mention:all:00000000-0000-0000-0000-000000000000) direct`,
			BodyFormat:             domain.MessageBodyFormatV3,
			MentionAllGroupMembers: true,
		})
		if err != nil {
			t.Fatalf("CreateMessage: %v", err)
		}
		if got := mentionRecipients(t, msg.ID); len(got) != 0 {
			t.Fatalf("expected no expansion for a direct DM regardless of the flag, got %v", got)
		}
	})

	t.Run("a channel send never expands (the flag is never set there, but confirm the CTE itself is inert)", func(t *testing.T) {
		msg, err := store.CreateMessage(ctx, storage.CreateMessageInput{
			WorkspaceID: workspace, DMConversationID: "", ChannelID: "", SenderID: sender,
			Kind:                   domain.MessageKindUser,
			BodyText:               `no target, no dm`,
			BodyFormat:             domain.MessageBodyFormatV1,
			MentionAllGroupMembers: true,
		})
		// No channel/DM target at all is refused by the auth branch (0 rows),
		// which the store reports as ErrNotFound — the point here is only that
		// this does not panic or leak a row, not that it succeeds.
		if err == nil {
			t.Fatalf("expected auth rejection for a targetless send, got message %+v", msg)
		}
	})
}

// TestPGXMessageStore_AllMentionGroupFanoutBoundPostgreSQL is the
// authoritative evidence for SR-002: @all in a group DM never notifies more
// than domain.MaxGroupAllMentionRecipients eligible members, enforced inside
// CreateMessage's own statement (invalid_all_mention_fanout), and an @all
// over the bound produces no message and no partial outbox at all — not a
// truncated first-50 subset.
//
// Opt-in like its neighbour: needs CHAT_TEST_DATABASE_URL against a _test
// database carrying the real migrations.
func TestPGXMessageStore_AllMentionGroupFanoutBoundPostgreSQL(t *testing.T) {
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
		t.Fatalf("refusing destructive test against non-test database %q", databaseName)
	}
	if _, err := pool.Exec(ctx, `DROP SCHEMA IF EXISTS chat CASCADE`); err != nil {
		t.Fatalf("reset chat schema: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DROP SCHEMA IF EXISTS chat CASCADE`) })
	if _, err := pool.Exec(ctx, `
		CREATE SCHEMA IF NOT EXISTS auth;
		CREATE TABLE IF NOT EXISTS auth.users (
			id UUID PRIMARY KEY,
			email TEXT NOT NULL DEFAULT '',
			display_name TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'active',
			deleted_at TIMESTAMPTZ
		)`); err != nil {
		t.Fatalf("prepare auth schema: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		ALTER TABLE auth.users
			ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'active',
			ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ,
			ADD COLUMN IF NOT EXISTS email TEXT NOT NULL DEFAULT '',
			ADD COLUMN IF NOT EXISTS display_name TEXT NOT NULL DEFAULT ''`); err != nil {
		t.Fatalf("align auth.users columns: %v", err)
	}
	if _, err := pool.Exec(ctx, readAllChatUpMigrations(t)); err != nil {
		t.Fatalf("apply chat migrations: %v", err)
	}

	store := storage.NewPGXMessageStore(pool)

	// newFixture builds an isolated workspace (its own general channel, its
	// own sender) with one empty group DM the sender belongs to. Fresh random
	// ids per call, so each subtest's roster can never leak into another's.
	newFixture := func(t *testing.T) (workspaceID, senderID, groupID string) {
		t.Helper()
		workspaceID, senderID, groupID = uuid.NewString(), uuid.NewString(), uuid.NewString()
		generalChannel := uuid.NewString()
		seeds := []struct {
			sql  string
			args []any
		}{
			{sql: `INSERT INTO auth.users (id, email, display_name, status) VALUES ($1::uuid, $1::text || '@example.test', 'Sender', 'active')`,
				args: []any{senderID}},
			{sql: `INSERT INTO chat.workspaces (id, slug, name) VALUES ($1::uuid, $1::text, 'Fanout Bound WS')`,
				args: []any{workspaceID}},
			{sql: `INSERT INTO chat.channels (id, workspace_id, slug, display_name, type, is_general, status)
				VALUES ($1, $2, 'geral', 'geral', 'public', true, 'active')`,
				args: []any{generalChannel, workspaceID}},
			{sql: `INSERT INTO chat.workspace_members (workspace_id, user_id, status) VALUES ($1, $2, 'active')`,
				args: []any{workspaceID, senderID}},
			{sql: `INSERT INTO chat.dm_conversations (id, workspace_id, type, title, status, created_by)
				VALUES ($1, $2, 'group', 'Fanout Bound', 'active', $3)`,
				args: []any{groupID, workspaceID, senderID}},
			{sql: `INSERT INTO chat.dm_members (conversation_id, user_id, status) VALUES ($1, $2, 'active')`,
				args: []any{groupID, senderID}},
		}
		// One transaction, not one per statement: migration 000002's general-
		// channel trigger is deferred to commit, but each pool.Exec is its own
		// implicit transaction, so a workspace committed before its general
		// channel exists would trip it immediately.
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin seed: %v", err)
		}
		defer func() { _ = tx.Rollback(context.Background()) }()
		for _, s := range seeds {
			if _, err := tx.Exec(ctx, s.sql, s.args...); err != nil {
				t.Fatalf("seed fixture: %v (%s)", err, s.sql)
			}
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit seed: %v", err)
		}
		return workspaceID, senderID, groupID
	}

	// addEligibleMembers bulk-inserts n additional users who are fully
	// eligible @all recipients — active dm_member, active workspace_member,
	// active non-deleted account — with three set-based statements regardless
	// of n, never one row at a time.
	addEligibleMembers := func(t *testing.T, workspaceID, groupID string, n int) {
		t.Helper()
		if n == 0 {
			return
		}
		ids := make([]string, n)
		for i := range ids {
			ids[i] = uuid.NewString()
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO auth.users (id, email, display_name, status)
			SELECT id, id::text || '@example.test', 'Eligible', 'active' FROM unnest($1::uuid[]) AS id`, ids); err != nil {
			t.Fatalf("seed eligible users: %v", err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO chat.workspace_members (workspace_id, user_id, status)
			SELECT $1, id, 'active' FROM unnest($2::uuid[]) AS id`, workspaceID, ids); err != nil {
			t.Fatalf("seed eligible workspace members: %v", err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO chat.dm_members (conversation_id, user_id, status)
			SELECT $1, id, 'active' FROM unnest($2::uuid[]) AS id`, groupID, ids); err != nil {
			t.Fatalf("seed eligible dm members: %v", err)
		}
	}

	countOutbox := func(t *testing.T, messageID string) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM chat.notification_outbox WHERE message_id = $1 AND kind = 'mention'`,
			messageID).Scan(&n); err != nil {
			t.Fatalf("count outbox: %v", err)
		}
		return n
	}

	countMessages := func(t *testing.T, workspaceID string) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM chat.messages WHERE workspace_id = $1`, workspaceID).Scan(&n); err != nil {
			t.Fatalf("count messages: %v", err)
		}
		return n
	}

	allMentionBody := `@[all](mention:all:00000000-0000-0000-0000-000000000000)`

	t.Run("A: exactly the bound (50 eligible, sender included) is accepted", func(t *testing.T) {
		workspaceID, senderID, groupID := newFixture(t)
		addEligibleMembers(t, workspaceID, groupID, domain.MaxGroupAllMentionRecipients-1) // + sender = 50
		msg, err := store.CreateMessage(ctx, storage.CreateMessageInput{
			WorkspaceID: workspaceID, DMConversationID: groupID, SenderID: senderID,
			Kind:                   domain.MessageKindUser,
			BodyText:               allMentionBody,
			BodyFormat:             domain.MessageBodyFormatV3,
			MentionAllGroupMembers: true,
		})
		if err != nil {
			t.Fatalf("CreateMessage at exactly the bound: %v", err)
		}
		if got := countOutbox(t, msg.ID); got != domain.MaxGroupAllMentionRecipients {
			t.Fatalf("outbox rows = %d, want %d", got, domain.MaxGroupAllMentionRecipients)
		}
	})

	t.Run("B: one over the bound (51 eligible) is rejected with zero fan-out", func(t *testing.T) {
		workspaceID, senderID, groupID := newFixture(t)
		addEligibleMembers(t, workspaceID, groupID, domain.MaxGroupAllMentionRecipients) // + sender = 51
		before := countMessages(t, workspaceID)
		_, err := store.CreateMessage(ctx, storage.CreateMessageInput{
			WorkspaceID: workspaceID, DMConversationID: groupID, SenderID: senderID,
			Kind:                   domain.MessageKindUser,
			BodyText:               allMentionBody,
			BodyFormat:             domain.MessageBodyFormatV3,
			MentionAllGroupMembers: true,
		})
		// This exercises the atomic backstop directly (no service-layer
		// pre-flight in front of a raw store.CreateMessage call), so the
		// refusal surfaces as the same non-enumerating ErrNotFound every other
		// atomic backstop in this statement produces — not a distinguishing
		// SR-002 error, which is the service layer's pre-flight check's job.
		if !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("expected the atomic backstop's ErrNotFound over the bound, got %v", err)
		}
		if got := countMessages(t, workspaceID); got != before {
			t.Fatalf("messages = %d, want %d — a refused @all must not persist a message, partial or otherwise",
				got, before)
		}
	})

	t.Run("C: 51 raw members but only 50 eligible (one deleted account) is accepted", func(t *testing.T) {
		workspaceID, senderID, groupID := newFixture(t)
		addEligibleMembers(t, workspaceID, groupID, domain.MaxGroupAllMentionRecipients-1) // + sender = 50 eligible
		// A 51st member, present in the raw roster but not in the count: an
		// active dm_member whose account is deleted.
		deleted := uuid.NewString()
		for _, s := range []struct {
			sql  string
			args []any
		}{
			{sql: `INSERT INTO auth.users (id, email, display_name, status, deleted_at) VALUES ($1::uuid, $1::text || '@example.test', 'Deleted', 'active', now())`, args: []any{deleted}},
			{sql: `INSERT INTO chat.workspace_members (workspace_id, user_id, status) VALUES ($1, $2, 'active')`, args: []any{workspaceID, deleted}},
			{sql: `INSERT INTO chat.dm_members (conversation_id, user_id, status) VALUES ($1, $2, 'active')`, args: []any{groupID, deleted}},
		} {
			if _, err := pool.Exec(ctx, s.sql, s.args...); err != nil {
				t.Fatalf("seed the 51st, ineligible member: %v", err)
			}
		}
		var rawMembers int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM chat.dm_members WHERE conversation_id = $1 AND status = 'active'`, groupID).Scan(&rawMembers); err != nil {
			t.Fatalf("count raw members: %v", err)
		}
		if rawMembers != domain.MaxGroupAllMentionRecipients+1 {
			t.Fatalf("raw active dm_members = %d, want %d (fixture bug)", rawMembers, domain.MaxGroupAllMentionRecipients+1)
		}

		msg, err := store.CreateMessage(ctx, storage.CreateMessageInput{
			WorkspaceID: workspaceID, DMConversationID: groupID, SenderID: senderID,
			Kind:                   domain.MessageKindUser,
			BodyText:               allMentionBody,
			BodyFormat:             domain.MessageBodyFormatV3,
			MentionAllGroupMembers: true,
		})
		if err != nil {
			t.Fatalf("CreateMessage with 51 raw / 50 eligible members: %v", err)
		}
		if got := countOutbox(t, msg.ID); got != domain.MaxGroupAllMentionRecipients {
			t.Fatalf("outbox rows = %d, want %d (raw member count must never be what's compared to the bound)",
				got, domain.MaxGroupAllMentionRecipients)
		}
	})

	// SEC-776-01: a group far past the bound must be refused on exactly the
	// same terms as one a single member over it, and must leave exactly as
	// little behind. The size here (three times the bound) is chosen to be
	// obviously beyond it while keeping the fixture fast; the logic under test
	// stops at the 51st row either way.
	t.Run("D: far over the bound is refused identically, with nothing persisted", func(t *testing.T) {
		workspaceID, senderID, groupID := newFixture(t)
		addEligibleMembers(t, workspaceID, groupID, domain.MaxGroupAllMentionRecipients*3)
		_, err := store.CreateMessage(ctx, storage.CreateMessageInput{
			WorkspaceID: workspaceID, DMConversationID: groupID, SenderID: senderID,
			Kind:                   domain.MessageKindUser,
			BodyText:               allMentionBody,
			BodyFormat:             domain.MessageBodyFormatV3,
			MentionAllGroupMembers: true,
		})
		if !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("expected the atomic backstop's ErrNotFound far over the bound, got %v", err)
		}
		if got := countMessages(t, workspaceID); got != 0 {
			t.Fatalf("messages = %d, want 0", got)
		}
		var outboxRows, pendingRows int
		if err := pool.QueryRow(ctx, `
			SELECT
				(SELECT count(*) FROM chat.notification_outbox n
				  JOIN chat.messages m ON m.id = n.message_id WHERE m.workspace_id = $1),
				(SELECT count(*) FROM chat.message_pending_mentions p
				  JOIN chat.messages m ON m.id = p.message_id WHERE m.workspace_id = $1)`,
			workspaceID).Scan(&outboxRows, &pendingRows); err != nil {
			t.Fatalf("count side effects: %v", err)
		}
		if outboxRows != 0 || pendingRows != 0 {
			t.Fatalf("outbox=%d pending=%d, want 0/0 — a refused @all leaves no partial fan-out",
				outboxRows, pendingRows)
		}
	})

	// The pre-flight's own early stop, against the same oversized group: it
	// answers with the ceiling it was given, never the roster's real size, so
	// the work it costs is bounded by that ceiling rather than by the group.
	t.Run("E: the pre-flight count saturates at the ceiling it is given", func(t *testing.T) {
		workspaceID, _, groupID := newFixture(t)
		addEligibleMembers(t, workspaceID, groupID, domain.MaxGroupAllMentionRecipients*3)

		ceiling := domain.MaxGroupAllMentionRecipients + 1
		got, err := store.CountEligibleAllMentionRecipientsUpTo(ctx, workspaceID, groupID, ceiling)
		if err != nil {
			t.Fatalf("CountEligibleAllMentionRecipientsUpTo: %v", err)
		}
		if got != ceiling {
			t.Fatalf("count = %d, want the ceiling %d — it must stop counting there, not report the real size",
				got, ceiling)
		}

		// And under the ceiling it is still exact, which is what makes the
		// comparison against the bound meaningful rather than merely safe.
		smallWorkspace, _, smallGroup := newFixture(t)
		addEligibleMembers(t, smallWorkspace, smallGroup, 2) // + sender = 3
		exact, err := store.CountEligibleAllMentionRecipientsUpTo(ctx, smallWorkspace, smallGroup, ceiling)
		if err != nil || exact != 3 {
			t.Fatalf("count = %d err=%v, want exactly 3 below the ceiling", exact, err)
		}
	})

	// Structural evidence for SEC-776-01, on the real planner: the LIMIT is
	// applied to the membership scan *below* the aggregate, not to its result.
	// The assertion is deliberately structural — that a Limit node exists and
	// is a child of the Aggregate — and never touches estimated cost or row
	// counts, which would make it a planner-version tripwire.
	t.Run("F: EXPLAIN shows the LIMIT beneath the aggregate", func(t *testing.T) {
		workspaceID, _, groupID := newFixture(t)
		addEligibleMembers(t, workspaceID, groupID, domain.MaxGroupAllMentionRecipients*3)

		rows, err := pool.Query(ctx, `
			EXPLAIN
			SELECT count(*)
			FROM (
				SELECT 1
				FROM chat.dm_conversations dc
				JOIN chat.dm_members dm
				  ON dm.conversation_id = dc.id
				 AND dm.status = 'active'
				JOIN chat.workspace_members wm
				  ON wm.workspace_id = dc.workspace_id
				 AND wm.user_id = dm.user_id
				 AND wm.status = 'active'
				JOIN auth.users u
				  ON u.id = dm.user_id
				 AND u.status = 'active'
				 AND u.deleted_at IS NULL
				WHERE dc.id = $2::uuid
				  AND dc.workspace_id = $1::uuid
				  AND dc.type = 'group'
				  AND dc.status = 'active'
				LIMIT $3::int
			) eligible_limited`,
			workspaceID, groupID, domain.MaxGroupAllMentionRecipients+1)
		if err != nil {
			t.Fatalf("EXPLAIN: %v", err)
		}
		defer rows.Close()
		var plan strings.Builder
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				t.Fatalf("scan plan line: %v", err)
			}
			plan.WriteString(line)
			plan.WriteString("\n")
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("read plan: %v", err)
		}
		text := plan.String()
		aggregate := strings.Index(text, "Aggregate")
		limit := strings.Index(text, "Limit")
		if aggregate < 0 || limit < 0 || limit < aggregate {
			t.Fatalf("expected a Limit node beneath the Aggregate, plan was:\n%s", text)
		}
		t.Logf("plan:\n%s", text)
	})
}

func sameSet(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	seen := make(map[string]bool, len(want))
	for _, id := range want {
		seen[id] = true
	}
	for _, id := range got {
		if !seen[id] {
			return false
		}
	}
	return true
}
