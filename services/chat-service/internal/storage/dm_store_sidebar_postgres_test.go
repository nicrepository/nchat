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
			full_name TEXT,
			avatar_url TEXT,
			status TEXT NOT NULL DEFAULT 'active',
			deleted_at TIMESTAMPTZ,
			anonymized_at TIMESTAMPTZ
		)`); err != nil {
		t.Fatalf("prepare auth schema: %v", err)
	}
	// auth.users survives between tests in this package (only the chat schema is
	// dropped), and sibling fixtures create it with fewer columns, so CREATE
	// TABLE IF NOT EXISTS can silently be a no-op. Add what this test needs.
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
		workspace = "c1000000-0000-4000-8000-000000000001"
		otherWS   = "c2000000-0000-4000-8000-000000000001"
		userA     = "c1000000-0000-4000-8000-00000000000a"
		userB     = "c1000000-0000-4000-8000-00000000000b"
		userC     = "c1000000-0000-4000-8000-00000000000c"
		outsider  = "c1000000-0000-4000-8000-00000000000d"
		dmAB      = "c1000000-0000-4000-8000-000000000010"
		dmAC      = "c1000000-0000-4000-8000-000000000014"
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
		// Name resolution cases, one per user: A has a full_name and an avatar,
		// B has neither (NULL full_name), C has a whitespace-only full_name.
		{sql: `INSERT INTO auth.users (id, email, display_name, full_name, avatar_url) VALUES
			($1, 'a@example.test', 'Ana Souza',  'Ana Carolina Souza', '/media/avatars/ana.png'),
			($2, 'b@example.test', 'Bruno Lima', NULL,                 NULL),
			($3, 'c@example.test', 'Caio Alves', '   ',                NULL),
			($4, 'd@example.test', 'Dora Reis',  NULL,                 NULL)
			ON CONFLICT (id) DO UPDATE SET
				display_name = EXCLUDED.display_name,
				full_name    = EXCLUDED.full_name,
				avatar_url   = EXCLUDED.avatar_url`,
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
			($1, $6, 'direct', NULL,           'active', $7, 'pair-ab'),
			($2, $6, 'group',  'Equipe Infra', 'active', $7, NULL),
			($3, $6, 'direct', NULL,           'active', $7, 'pair-orphan'),
			($4, $8, 'direct', NULL,           'active', $7, 'pair-foreign'),
			($5, $6, 'direct', NULL,           'active', $7, 'pair-ac')`,
			args: []any{dmAB, dmGroup, dmOrphan, dmForeign, dmAC, workspace, userA, otherWS}},
		{sql: `INSERT INTO chat.dm_members (conversation_id, user_id) VALUES
			($1, $6), ($1, $7),
			($2, $6), ($2, $7), ($2, $8),
			($3, $6),
			($4, $6), ($4, $7),
			($5, $6), ($5, $8)`,
			args: []any{dmAB, dmGroup, dmOrphan, dmForeign, dmAC, userA, userB, userC}},
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
	// B has no full_name: display_name is the fallback, and no avatar is stored.
	seenByA := byID(t, convsA, dmAB)
	if seenByA.CounterpartDisplayName != "Bruno Lima" {
		t.Fatalf("user A must see B by display_name fallback, got %q", seenByA.CounterpartDisplayName)
	}
	if seenByA.CounterpartUserID != userB {
		t.Fatalf("user A must see B's user id, got %q", seenByA.CounterpartUserID)
	}
	if seenByA.CounterpartAvatarURL != "" {
		t.Fatalf("user B has no avatar, got %q", seenByA.CounterpartAvatarURL)
	}

	// A has a full_name: it wins over display_name, and the avatar comes along.
	seenByB := byID(t, convsB, dmAB)
	if seenByB.CounterpartDisplayName != "Ana Carolina Souza" {
		t.Fatalf("user B must see A by full_name, got %q", seenByB.CounterpartDisplayName)
	}
	if seenByB.CounterpartUserID != userA {
		t.Fatalf("user B must see A's user id, got %q", seenByB.CounterpartUserID)
	}
	if seenByB.CounterpartAvatarURL != "/media/avatars/ana.png" {
		t.Fatalf("user B must see A's avatar, got %q", seenByB.CounterpartAvatarURL)
	}

	// C's full_name is whitespace-only: it must be treated as absent.
	if got := byID(t, convsA, dmAC).CounterpartDisplayName; got != "Caio Alves" {
		t.Fatalf("whitespace full_name must fall back to display_name, got %q", got)
	}

	// Group DMs keep their title and never carry a participant name.
	group := byID(t, convsA, dmGroup)
	if group.CounterpartDisplayName != "" || group.CounterpartUserID != "" || group.CounterpartAvatarURL != "" {
		t.Fatalf("group DM must not resolve a counterpart, got %+v", group)
	}
	if group.Title != "Equipe Infra" {
		t.Fatalf("group DM title changed: %q", group.Title)
	}

	// A 1:1 conversation whose other member is gone falls back to an empty name.
	orphan := byID(t, convsA, dmOrphan)
	if orphan.CounterpartDisplayName != "" || orphan.CounterpartUserID != "" || orphan.CounterpartAvatarURL != "" {
		t.Fatalf("orphan DM must not resolve a counterpart, got %+v", orphan)
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

	// Anonymization policy: auth-service owns it at the source. Once it has
	// scrubbed the row, the sidebar reflects the scrubbed identity — it neither
	// resurrects the old name nor hides the conversation behind a placeholder.
	if _, err := pool.Exec(ctx, `
		UPDATE auth.users
		SET display_name = 'Usuário removido', full_name = NULL, avatar_url = NULL,
		    anonymized_at = now(), deleted_at = now()
		WHERE id = $1`, userC); err != nil {
		t.Fatalf("anonymize user C: %v", err)
	}
	convsA, err = store.ListVisibleConversationsWithParticipantIDs(ctx, workspace, userA)
	if err != nil {
		t.Fatalf("list after anonymization: %v", err)
	}
	anonymized := byID(t, convsA, dmAC)
	if anonymized.CounterpartDisplayName != "Usuário removido" {
		t.Fatalf("anonymized counterpart must read from the scrubbed row, got %q", anonymized.CounterpartDisplayName)
	}
	if anonymized.CounterpartAvatarURL != "" {
		t.Fatalf("anonymized counterpart must not keep an avatar, got %q", anonymized.CounterpartAvatarURL)
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
	left := byID(t, convsA, dmAB)
	if left.CounterpartDisplayName != "" || left.CounterpartUserID != "" || left.CounterpartAvatarURL != "" {
		t.Fatalf("left member must not resolve as counterpart, got %+v", left)
	}
}
