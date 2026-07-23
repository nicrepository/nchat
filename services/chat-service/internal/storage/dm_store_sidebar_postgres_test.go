package storage_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// TestPGXDMStoreSidebarCounterpartPostgreSQL exercises the sidebar DM listing
// against real PostgreSQL. It is the authoritative evidence that the counterpart
// display name is viewer-scoped (A sees B, B sees A from the same row) and that
// membership and workspace isolation still gate the listing.
func TestPGXDMStoreSidebarCounterpartPostgreSQL(t *testing.T) {
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
		t.Fatalf("refusing destructive sidebar test against non-test database %q", databaseName)
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
	if _, err := pool.Exec(ctx, readAllChatUpMigrations(t)); err != nil {
		t.Fatalf("apply chat migrations: %v", err)
	}

	const (
		workspace = "c1000000-0000-4000-8000-000000000001"
		otherWS   = "c2000000-0000-4000-8000-000000000001"
		userA     = "c1000000-0000-4000-8000-00000000000a"
		userB     = "c1000000-0000-4000-8000-00000000000b"
		userC     = "c1000000-0000-4000-8000-00000000000c"
		outsider  = "c1000000-0000-4000-8000-00000000000d"
		dmAB      = "c1000000-0000-4000-8000-000000000010"
		dmGroup   = "c1000000-0000-4000-8000-000000000011"
		dmOrphan  = "c1000000-0000-4000-8000-000000000012"
		dmForeign = "c2000000-0000-4000-8000-000000000013"
		generalA  = "c1000000-0000-4000-8000-000000000020"
		generalB  = "c2000000-0000-4000-8000-000000000021"
	)

	// Seeded one statement at a time inside a single transaction: pgx rejects
	// multi-statement prepared queries, and the "exactly one #geral per active
	// workspace" constraint is only satisfiable at commit time.
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
			($1, 'a@example.test', 'Ana Souza'),
			($2, 'b@example.test', 'Bruno Lima'),
			($3, 'c@example.test', 'Caio Alves'),
			($4, 'd@example.test', 'Dora Reis')
			ON CONFLICT (id) DO UPDATE SET display_name = EXCLUDED.display_name`,
			args: []any{userA, userB, userC, outsider}},
		{sql: `INSERT INTO chat.workspaces (id, slug, name) VALUES
			($1, 'sidebar-a', 'Sidebar A'),
			($2, 'sidebar-b', 'Sidebar B')`,
			args: []any{workspace, otherWS}},
		{sql: `INSERT INTO chat.channels (id, workspace_id, slug, display_name, type, is_general, status) VALUES
			($1, $3, 'geral', 'geral', 'public', true, 'active'),
			($2, $4, 'geral', 'geral', 'public', true, 'active')`,
			args: []any{generalA, generalB, workspace, otherWS}},
		{sql: `INSERT INTO chat.workspace_members (workspace_id, user_id, status) VALUES
			($1, $3, 'active'), ($1, $4, 'active'), ($1, $5, 'active'), ($1, $6, 'active'),
			($2, $3, 'active'), ($2, $4, 'active')`,
			args: []any{workspace, otherWS, userA, userB, userC, outsider}},
		{sql: `INSERT INTO chat.dm_conversations (id, workspace_id, type, title, status, created_by, direct_pair_key) VALUES
			($1, $5, 'direct', NULL,           'active', $6, 'pair-ab'),
			($2, $5, 'group',  'Equipe Infra', 'active', $6, NULL),
			($3, $5, 'direct', NULL,           'active', $6, 'pair-orphan'),
			($4, $7, 'direct', NULL,           'active', $6, 'pair-foreign')`,
			args: []any{dmAB, dmGroup, dmOrphan, dmForeign, workspace, userA, otherWS}},
		{sql: `INSERT INTO chat.dm_members (conversation_id, user_id) VALUES
			($1, $5), ($1, $6),
			($2, $5), ($2, $6), ($2, $7),
			($3, $5),
			($4, $5), ($4, $6)`,
			args: []any{dmAB, dmGroup, dmOrphan, dmForeign, userA, userB, userC}},
	} {
		if _, err := tx.Exec(ctx, seed.sql, seed.args...); err != nil {
			t.Fatalf("seed sidebar cases: %v", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit seed: %v", err)
	}

	store := storage.NewPGXDMStore(pool)
	byID := func(t *testing.T, convs []domain.DMConversationWithParticipantIDs, id string) domain.DMConversationWithParticipantIDs {
		t.Helper()
		for _, c := range convs {
			if c.ID == id {
				return c
			}
		}
		t.Fatalf("conversation %s not found in %+v", id, convs)
		return domain.DMConversationWithParticipantIDs{}
	}

	// The same 1:1 row resolves to a different name for each participant.
	convsA, err := store.ListVisibleConversationsWithParticipantIDs(ctx, workspace, userA)
	if err != nil {
		t.Fatalf("list for user A: %v", err)
	}
	convsB, err := store.ListVisibleConversationsWithParticipantIDs(ctx, workspace, userB)
	if err != nil {
		t.Fatalf("list for user B: %v", err)
	}
	if got := byID(t, convsA, dmAB).CounterpartDisplayName; got != "Bruno Lima" {
		t.Fatalf("user A must see B, got %q", got)
	}
	if got := byID(t, convsB, dmAB).CounterpartDisplayName; got != "Ana Souza" {
		t.Fatalf("user B must see A, got %q", got)
	}

	// Group DMs keep their title and never carry a participant name.
	group := byID(t, convsA, dmGroup)
	if group.CounterpartDisplayName != "" {
		t.Fatalf("group DM must not resolve a counterpart, got %q", group.CounterpartDisplayName)
	}
	if group.Title != "Equipe Infra" {
		t.Fatalf("group DM title changed: %q", group.Title)
	}

	// A 1:1 conversation whose other member is gone falls back to an empty name.
	if got := byID(t, convsA, dmOrphan).CounterpartDisplayName; got != "" {
		t.Fatalf("orphan DM must not resolve a counterpart, got %q", got)
	}

	// Membership and workspace isolation remain enforced by the query itself.
	for _, c := range convsA {
		if c.ID == dmForeign {
			t.Fatal("conversation from another workspace leaked into the listing")
		}
	}
	convsOutsider, err := store.ListVisibleConversationsWithParticipantIDs(ctx, workspace, outsider)
	if err != nil {
		t.Fatalf("list for outsider: %v", err)
	}
	if len(convsOutsider) != 0 {
		t.Fatalf("non-member must receive no conversations, got %d", len(convsOutsider))
	}

	// A member who left stops resolving as a counterpart.
	if _, err := pool.Exec(ctx, `
		UPDATE chat.dm_members SET status = 'left', left_at = now()
		WHERE conversation_id = $1 AND user_id = $2`, dmAB, userB); err != nil {
		t.Fatalf("mark member as left: %v", err)
	}
	convsA, err = store.ListVisibleConversationsWithParticipantIDs(ctx, workspace, userA)
	if err != nil {
		t.Fatalf("list after member left: %v", err)
	}
	if got := byID(t, convsA, dmAB).CounterpartDisplayName; got != "" {
		t.Fatalf("left member must not resolve as counterpart, got %q", got)
	}
}
