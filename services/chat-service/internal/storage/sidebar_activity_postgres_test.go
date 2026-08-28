package storage_test

import (
	"context"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// countingPool records how many statements a store issues, so "no N+1" is an
// assertion about behaviour rather than a claim about the SQL text. A store
// that resolved the last message per conversation would show one Query per row
// however the query itself were written.
type countingPool struct {
	storage.Pool
	queries atomic.Int64
}

func (p *countingPool) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	p.queries.Add(1)
	return p.Pool.Query(ctx, sql, args...)
}

func (p *countingPool) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	p.queries.Add(1)
	return p.Pool.QueryRow(ctx, sql, args...)
}

func (p *countingPool) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	p.queries.Add(1)
	return p.Pool.Exec(ctx, sql, args...)
}

// TestPGXSidebarActivityPostgreSQL is the authoritative evidence for issue #414:
// each visible channel and conversation carries the created_at of its newest
// message, resolved inside the authorized listing query, and a caller never
// learns the activity of something they may not read.
func TestPGXSidebarActivityPostgreSQL(t *testing.T) {
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
		t.Fatalf("refusing destructive activity test against non-test database %q", databaseName)
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
			full_name TEXT,
			avatar_url TEXT,
			status TEXT NOT NULL DEFAULT 'active',
			deleted_at TIMESTAMPTZ,
			anonymized_at TIMESTAMPTZ
		)`); err != nil {
		t.Fatalf("prepare auth schema: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		ALTER TABLE auth.users
			ADD COLUMN IF NOT EXISTS full_name TEXT,
			ADD COLUMN IF NOT EXISTS avatar_url TEXT,
			ADD COLUMN IF NOT EXISTS anonymized_at TIMESTAMPTZ,
			ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ,
			ADD COLUMN IF NOT EXISTS email TEXT NOT NULL DEFAULT '',
			ADD COLUMN IF NOT EXISTS display_name TEXT NOT NULL DEFAULT ''`); err != nil {
		t.Fatalf("align auth.users columns: %v", err)
	}
	if _, err := pool.Exec(ctx, readAllChatUpMigrations(t)); err != nil {
		t.Fatalf("apply chat migrations: %v", err)
	}

	const (
		workspace = "a1000000-0000-4000-8000-000000000001"
		otherWS   = "a2000000-0000-4000-8000-000000000001"
		userA     = "a1000000-0000-4000-8000-00000000000a"
		userB     = "a1000000-0000-4000-8000-00000000000b"
		outsider  = "a1000000-0000-4000-8000-00000000000d"

		generalA = "a1000000-0000-4000-8000-000000000020"
		generalB = "a2000000-0000-4000-8000-000000000021"
		busyCh   = "a1000000-0000-4000-8000-000000000022"
		emptyCh  = "a1000000-0000-4000-8000-000000000023"
		secretCh = "a1000000-0000-4000-8000-000000000024"
		foreign  = "a2000000-0000-4000-8000-000000000025"

		dmAB       = "a1000000-0000-4000-8000-000000000030"
		dmGroup    = "a1000000-0000-4000-8000-000000000031"
		dmEmpty    = "a1000000-0000-4000-8000-000000000032"
		dmOutsider = "a1000000-0000-4000-8000-000000000033"
	)

	at := func(day int, hour int) time.Time {
		return time.Date(2026, 7, day, hour, 0, 0, 0, time.UTC)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin seed: %v", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	for _, seed := range []struct {
		sql  string
		args []any
	}{
		{sql: `INSERT INTO auth.users (id, email, display_name) VALUES
			($1, 'a@example.test', 'Ana'),
			($2, 'b@example.test', 'Bruno'),
			($3, 'd@example.test', 'Dora')
			ON CONFLICT (id) DO UPDATE SET display_name = EXCLUDED.display_name`,
			args: []any{userA, userB, outsider}},
		{sql: `INSERT INTO chat.workspaces (id, slug, name) VALUES
			($1, 'activity-a', 'Activity A'),
			($2, 'activity-b', 'Activity B')`,
			args: []any{workspace, otherWS}},
		{sql: `INSERT INTO chat.channels (id, workspace_id, slug, display_name, type, is_general, status) VALUES
			($1, $7, 'geral',   'geral',   'public',  true,  'active'),
			($2, $8, 'geral',   'geral',   'public',  true,  'active'),
			($3, $7, 'movimentado', 'movimentado', 'public',  false, 'active'),
			($4, $7, 'vazio',   'vazio',   'public',  false, 'active'),
			($5, $7, 'secreto', 'secreto', 'private', false, 'active'),
			($6, $8, 'outro',   'outro',   'public',  false, 'active')`,
			args: []any{generalA, generalB, busyCh, emptyCh, secretCh, foreign, workspace, otherWS}},
		{sql: `INSERT INTO chat.workspace_members (workspace_id, user_id, status) VALUES
			($1, $3, 'active'), ($1, $4, 'active'), ($1, $5, 'active'),
			($2, $3, 'active')`,
			args: []any{workspace, otherWS, userA, userB, outsider}},
		// Only B is in the private channel; A never is.
		{sql: `INSERT INTO chat.channel_members (channel_id, user_id, role) VALUES ($1, $2, 'member')`,
			args: []any{secretCh, userB}},
		{sql: `INSERT INTO chat.dm_conversations (id, workspace_id, type, title, status, created_by, direct_pair_key) VALUES
			($1, $5, 'direct', NULL,      'active', $6, 'pair-ab'),
			($2, $5, 'group',  'Equipe',  'active', $6, NULL),
			($3, $5, 'direct', NULL,      'active', $6, 'pair-empty'),
			($4, $5, 'direct', NULL,      'active', $7, 'pair-outsider')`,
			args: []any{dmAB, dmGroup, dmEmpty, dmOutsider, workspace, userA, outsider}},
		{sql: `INSERT INTO chat.dm_members (conversation_id, user_id) VALUES
			($1, $5), ($1, $6),
			($2, $5), ($2, $6),
			($3, $5),
			($4, $7)`,
			args: []any{dmAB, dmGroup, dmEmpty, dmOutsider, userA, userB, outsider}},
		// Deliberately inserted out of chronological order, so "the newest" is
		// not the last row written.
		{sql: `INSERT INTO chat.messages (workspace_id, channel_id, sender_id, body_text, created_at) VALUES
			($1, $2, $3, 'primeira', $4),
			($1, $2, $3, 'ultima',   $5),
			($1, $2, $3, 'meio',     $6)`,
			args: []any{workspace, busyCh, userA, at(10, 8), at(20, 18), at(15, 9)}},
		// The private channel is the busiest thing in the workspace: if activity
		// ever leaked, it would leak here.
		{sql: `INSERT INTO chat.messages (workspace_id, channel_id, sender_id, body_text, created_at) VALUES
			($1, $2, $3, 'secreta', $4)`,
			args: []any{workspace, secretCh, userB, at(31, 23)}},
		{sql: `INSERT INTO chat.messages (workspace_id, dm_conversation_id, sender_id, body_text, created_at) VALUES
			($1, $2, $3, 'oi',   $5),
			($1, $2, $3, 'tudo', $6),
			($1, $4, $3, 'time', $7)`,
			args: []any{workspace, dmAB, userA, dmGroup, at(11, 8), at(21, 19), at(12, 7)}},
		// Soft-deleted: the row and its created_at survive, and the message list
		// still renders it as a placeholder, so it is still activity.
		{sql: `INSERT INTO chat.messages (workspace_id, dm_conversation_id, sender_id, body_text, status, deleted_at, created_at)
			VALUES ($1, $2, $3, '', 'deleted', $4, $4)`,
			args: []any{workspace, dmGroup, userA, at(25, 10)}},
	} {
		if _, err := tx.Exec(ctx, seed.sql, seed.args...); err != nil {
			t.Fatalf("seed activity cases: %v", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit seed: %v", err)
	}

	counting := &countingPool{Pool: pool}
	channels := storage.NewPGXChannelStore(counting)
	dms := storage.NewPGXDMStore(counting)

	// ── Channels ──────────────────────────────────────────────────────────────

	counting.queries.Store(0)
	accesses, err := channels.ListVisibleChannelAccessByUser(ctx, workspace, userA)
	if err != nil {
		t.Fatalf("list channels for A: %v", err)
	}
	if got := counting.queries.Load(); got != 1 {
		t.Fatalf("listing %d channels must take one statement, took %d", len(accesses), got)
	}

	channelActivity := map[string]*time.Time{}
	for _, access := range accesses {
		channelActivity[access.Channel.ID] = access.LastMessageAt
	}
	if _, leaked := channelActivity[secretCh]; leaked {
		t.Fatal("a private channel A does not belong to leaked into the listing")
	}
	if _, leaked := channelActivity[foreign]; leaked {
		t.Fatal("a channel from another workspace leaked into the listing")
	}
	if got := channelActivity[busyCh]; got == nil || !got.Equal(at(20, 18)) {
		t.Fatalf("expected the newest of three messages, got %v", got)
	}
	if got, ok := channelActivity[emptyCh]; !ok || got != nil {
		t.Fatalf("a channel with no messages must report no activity, got %v", got)
	}
	if got, ok := channelActivity[generalA]; !ok || got != nil {
		t.Fatalf("#geral has no messages here and must report none, got %v", got)
	}

	// B is a member of the private channel and sees its activity; A, who is not,
	// does not even see the channel. The activity is not a separate permission.
	accessesB, err := channels.ListVisibleChannelAccessByUser(ctx, workspace, userB)
	if err != nil {
		t.Fatalf("list channels for B: %v", err)
	}
	var sawSecret bool
	for _, access := range accessesB {
		if access.Channel.ID != secretCh {
			continue
		}
		sawSecret = true
		if access.LastMessageAt == nil || !access.LastMessageAt.Equal(at(31, 23)) {
			t.Fatalf("member of the private channel must see its activity, got %v", access.LastMessageAt)
		}
	}
	if !sawSecret {
		t.Fatal("B is a member of the private channel and must see it")
	}

	// A non-member of the workspace receives nothing at all, activity included.
	if outsiderChannels, err := channels.ListVisibleChannelAccessByUser(ctx, otherWS, userB); err != nil {
		t.Fatalf("list channels in the other workspace: %v", err)
	} else if len(outsiderChannels) != 0 {
		t.Fatalf("non-member must receive no channels, got %d", len(outsiderChannels))
	}

	// ── Conversations ─────────────────────────────────────────────────────────

	counting.queries.Store(0)
	convs, err := dms.ListVisibleConversationsWithParticipantIDs(ctx, workspace, userA)
	if err != nil {
		t.Fatalf("list conversations for A: %v", err)
	}
	if got := counting.queries.Load(); got != 1 {
		t.Fatalf("listing %d conversations must take one statement, took %d", len(convs), got)
	}

	dmActivity := map[string]*time.Time{}
	for _, conv := range convs {
		dmActivity[conv.ID] = conv.LastMessageAt
	}
	if _, leaked := dmActivity[dmOutsider]; leaked {
		t.Fatal("a conversation A does not participate in leaked into the listing")
	}
	if got := dmActivity[dmAB]; got == nil || !got.Equal(at(21, 19)) {
		t.Fatalf("expected the newest 1:1 message, got %v", got)
	}
	// The group's newest message is the soft-deleted one. Deletion is soft here
	// and the placeholder still occupies the end of the conversation, so it
	// still counts as activity — and still carries no content anywhere.
	if got := dmActivity[dmGroup]; got == nil || !got.Equal(at(25, 10)) {
		t.Fatalf("a soft-deleted newest message must still count as activity, got %v", got)
	}
	if got, ok := dmActivity[dmEmpty]; !ok || got != nil {
		t.Fatalf("a conversation with no messages must report no activity, got %v", got)
	}

	// A conversation whose participants belong to another workspace is invisible
	// here whatever its activity.
	if foreignConvs, err := dms.ListVisibleConversationsWithParticipantIDs(ctx, otherWS, userA); err != nil {
		t.Fatalf("list conversations in the other workspace: %v", err)
	} else if len(foreignConvs) != 0 {
		t.Fatalf("no conversation exists for A in the other workspace, got %d", len(foreignConvs))
	}

	// ── Determinism ───────────────────────────────────────────────────────────

	// Two messages at the same instant in different conversations must not make
	// either listing unstable: the value read is the instant itself.
	if _, err := pool.Exec(ctx, `
		INSERT INTO chat.messages (workspace_id, channel_id, sender_id, body_text, created_at)
		VALUES ($1, $2, $3, 'empate', $4)`, workspace, emptyCh, userA, at(20, 18)); err != nil {
		t.Fatalf("seed tie: %v", err)
	}
	for range 3 {
		repeated, err := channels.ListVisibleChannelAccessByUser(ctx, workspace, userA)
		if err != nil {
			t.Fatalf("repeat channel listing: %v", err)
		}
		for _, access := range repeated {
			if access.Channel.ID != busyCh && access.Channel.ID != emptyCh {
				continue
			}
			if access.LastMessageAt == nil || !access.LastMessageAt.Equal(at(20, 18)) {
				t.Fatalf("tied activity must read identically every time, got %v", access.LastMessageAt)
			}
		}
	}

	// A newly persisted message becomes the activity instant, and an older one
	// inserted afterwards does not take its place.
	if _, err := pool.Exec(ctx, `
		INSERT INTO chat.messages (workspace_id, dm_conversation_id, sender_id, body_text, created_at)
		VALUES ($1, $2, $3, 'nova', $4), ($1, $2, $3, 'antiga', $5)`,
		workspace, dmAB, userA, at(28, 12), at(1, 1)); err != nil {
		t.Fatalf("seed later message: %v", err)
	}
	convs, err = dms.ListVisibleConversationsWithParticipantIDs(ctx, workspace, userA)
	if err != nil {
		t.Fatalf("list conversations after new message: %v", err)
	}
	for _, conv := range convs {
		if conv.ID != dmAB {
			continue
		}
		if conv.LastMessageAt == nil || !conv.LastMessageAt.Equal(at(28, 12)) {
			t.Fatalf("expected the newest message to win, got %v", conv.LastMessageAt)
		}
	}
}
