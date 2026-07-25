package storage_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// TestPGXDMStoreParticipantEligibilityPostgreSQL is the authoritative evidence
// that DM membership is written only for accounts that are still eligible at
// write time, against real PostgreSQL.
//
// The service layer checks eligibility before calling the store, but that check
// and the INSERT are separate steps; an account suspended or deleted in between
// would slip through a service-only guard. These cases call the store directly,
// which is exactly the state the race produces: a caller convinced the
// participants are fine, and a database that disagrees. The guard is a single
// INSERT ... SELECT, so the eligibility test and the write cannot be
// interleaved — an ineligible participant simply yields no row, the count check
// fails, and the whole transaction is rolled back.
func TestPGXDMStoreParticipantEligibilityPostgreSQL(t *testing.T) {
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
		t.Fatalf("refusing destructive eligibility test against non-test database %q", databaseName)
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
	// Sibling fixtures in this package create auth.users with different column
	// sets and only the chat schema is dropped between them, so CREATE TABLE
	// IF NOT EXISTS can be a no-op. Add what this test needs.
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
		workspace = "e1000000-0000-4000-8000-000000000001"
		otherWS   = "e2000000-0000-4000-8000-000000000001"
		general   = "e1000000-0000-4000-8000-000000000020"
		generalB  = "e2000000-0000-4000-8000-000000000021"
		caller    = "e1000000-0000-4000-8000-00000000000a"
		active1   = "e1000000-0000-4000-8000-00000000000b"
		active2   = "e1000000-0000-4000-8000-00000000000c"
		suspended = "e1000000-0000-4000-8000-00000000000d"
		deleted   = "e1000000-0000-4000-8000-00000000000e"
		foreign   = "e1000000-0000-4000-8000-00000000000f"
	)

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin seed: %v", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	for _, seed := range []struct {
		sql  string
		args []any
	}{
		// Every user below is an active workspace member. Only the account state
		// differs, which is the whole point: membership alone must not be enough.
		{sql: `INSERT INTO auth.users (id, email, display_name, status, deleted_at) VALUES
			($1, 'caller@example.test',    'Caller',    'active',    NULL),
			($2, 'active1@example.test',   'Active One','active',    NULL),
			($3, 'active2@example.test',   'Active Two','active',    NULL),
			($4, 'suspended@example.test', 'Suspended', 'suspended', NULL),
			($5, 'deleted@example.test',   'Deleted',   'active',    now()),
			($6, 'foreign@example.test',   'Foreign',   'active',    NULL)
			ON CONFLICT (id) DO UPDATE SET
				status     = EXCLUDED.status,
				deleted_at = EXCLUDED.deleted_at`,
			args: []any{caller, active1, active2, suspended, deleted, foreign}},
		{sql: `INSERT INTO chat.workspaces (id, slug, name) VALUES
			($1, 'eligibility-a', 'Eligibility A'),
			($2, 'eligibility-b', 'Eligibility B')`,
			args: []any{workspace, otherWS}},
		{sql: `INSERT INTO chat.channels (id, workspace_id, slug, display_name, type, is_general, status) VALUES
			($1, $3, 'geral', 'geral', 'public', true, 'active'),
			($2, $4, 'geral', 'geral', 'public', true, 'active')`,
			args: []any{general, generalB, workspace, otherWS}},
		{sql: `INSERT INTO chat.workspace_members (workspace_id, user_id, status) VALUES
			($1, $3, 'active'), ($1, $4, 'active'), ($1, $5, 'active'),
			($1, $6, 'active'), ($1, $7, 'active'),
			($2, $8, 'active')`,
			args: []any{workspace, otherWS, caller, active1, active2, suspended, deleted, foreign}},
	} {
		if _, err := tx.Exec(ctx, seed.sql, seed.args...); err != nil {
			t.Fatalf("seed eligibility cases: %v", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit seed: %v", err)
	}

	store := storage.NewPGXDMStore(pool)
	countRows := func(t *testing.T, query string, args ...any) int {
		t.Helper()
		var total int
		if err := pool.QueryRow(ctx, query, args...).Scan(&total); err != nil {
			t.Fatalf("count rows: %v", err)
		}
		return total
	}
	conversations := func(t *testing.T) int {
		return countRows(t, `SELECT count(*) FROM chat.dm_conversations WHERE workspace_id = $1`, workspace)
	}
	members := func(t *testing.T) int {
		return countRows(t, `
			SELECT count(*)
			FROM chat.dm_members dm
			JOIN chat.dm_conversations dc ON dc.id = dm.conversation_id
			WHERE dc.workspace_id = $1`, workspace)
	}

	t.Run("group with only eligible participants is created with every member", func(t *testing.T) {
		conversation, err := store.CreateGroupConversation(ctx, storage.CreateGroupConversationInput{
			WorkspaceID:        workspace,
			CreatedBy:          caller,
			Title:              "Elegíveis",
			ParticipantUserIDs: []string{caller, active1, active2},
		})
		if err != nil {
			t.Fatalf("CreateGroupConversation: %v", err)
		}
		got := countRows(t, `SELECT count(*) FROM chat.dm_members WHERE conversation_id = $1 AND status = 'active'`, conversation.ID)
		if got != 3 {
			t.Fatalf("member rows = %d, want 3", got)
		}
	})

	// One subtest per rejected participant class. Each asserts the generic error
	// and that the transaction left nothing behind — no orphan conversation and
	// no partial membership.
	for _, test := range []struct {
		name         string
		participants []string
	}{
		{name: "suspended account", participants: []string{caller, active1, suspended}},
		{name: "deleted account", participants: []string{caller, active1, deleted}},
		{name: "other workspace", participants: []string{caller, active1, foreign}},
		{name: "unknown user", participants: []string{caller, active1, "e1000000-0000-4000-8000-0000000000ff"}},
		{name: "only the ineligible one is invalid", participants: []string{caller, active1, active2, suspended}},
	} {
		t.Run("group is refused for a "+test.name, func(t *testing.T) {
			conversationsBefore, membersBefore := conversations(t), members(t)

			_, err := store.CreateGroupConversation(ctx, storage.CreateGroupConversationInput{
				WorkspaceID:        workspace,
				CreatedBy:          caller,
				Title:              "Inelegível",
				ParticipantUserIDs: test.participants,
			})
			if !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("expected ErrForbidden, got %v", err)
			}
			// The error carries no participant identity, account state or workspace.
			for _, leaked := range append([]string{"suspended", "deleted", workspace}, test.participants...) {
				if strings.Contains(err.Error(), leaked) {
					t.Fatalf("error leaks %q: %v", leaked, err)
				}
			}
			if got := conversations(t); got != conversationsBefore {
				t.Fatalf("conversations = %d, want %d — rollback left an orphan", got, conversationsBefore)
			}
			if got := members(t); got != membersBefore {
				t.Fatalf("member rows = %d, want %d — rollback left a partial group", got, membersBefore)
			}
		})
	}

	t.Run("direct DM keeps working and still refuses an ineligible counterpart", func(t *testing.T) {
		result, err := store.CreateDirectConversation(ctx, storage.CreateDirectConversationInput{
			WorkspaceID:        workspace,
			CreatedBy:          caller,
			DirectPairKey:      "pair-caller-active1",
			ParticipantUserIDs: []string{caller, active1},
		})
		if err != nil || !result.Created {
			t.Fatalf("CreateDirectConversation: err=%v created=%v", err, result.Created)
		}
		if got := countRows(t, `SELECT count(*) FROM chat.dm_members WHERE conversation_id = $1`, result.Conversation.ID); got != 2 {
			t.Fatalf("direct member rows = %d, want 2", got)
		}

		conversationsBefore, membersBefore := conversations(t), members(t)
		if _, err := store.CreateDirectConversation(ctx, storage.CreateDirectConversationInput{
			WorkspaceID:        workspace,
			CreatedBy:          caller,
			DirectPairKey:      "pair-caller-deleted",
			ParticipantUserIDs: []string{caller, deleted},
		}); !errors.Is(err, domain.ErrForbidden) {
			t.Fatalf("expected ErrForbidden for deleted counterpart, got %v", err)
		}
		if got := conversations(t); got != conversationsBefore {
			t.Fatalf("conversations = %d, want %d", got, conversationsBefore)
		}
		if got := members(t); got != membersBefore {
			t.Fatalf("member rows = %d, want %d", got, membersBefore)
		}
	})

	// The count check must treat a reactivated membership as written, otherwise
	// re-opening an archived 1:1 would look like an ineligible participant.
	t.Run("archived direct DM is reactivated instead of duplicated", func(t *testing.T) {
		first, err := store.CreateDirectConversation(ctx, storage.CreateDirectConversationInput{
			WorkspaceID:        workspace,
			CreatedBy:          caller,
			DirectPairKey:      "pair-caller-active2",
			ParticipantUserIDs: []string{caller, active2},
		})
		if err != nil {
			t.Fatalf("create direct: %v", err)
		}
		if _, err := pool.Exec(ctx, `
			UPDATE chat.dm_conversations SET status = 'archived' WHERE id = $1`, first.Conversation.ID); err != nil {
			t.Fatalf("archive conversation: %v", err)
		}
		if _, err := pool.Exec(ctx, `
			UPDATE chat.dm_members SET status = 'left', left_at = now() WHERE conversation_id = $1`,
			first.Conversation.ID); err != nil {
			t.Fatalf("archive memberships: %v", err)
		}

		second, err := store.CreateDirectConversation(ctx, storage.CreateDirectConversationInput{
			WorkspaceID:        workspace,
			CreatedBy:          caller,
			DirectPairKey:      "pair-caller-active2",
			ParticipantUserIDs: []string{caller, active2},
		})
		if err != nil {
			t.Fatalf("recreate direct: %v", err)
		}
		if second.Conversation.ID != first.Conversation.ID || second.Created {
			t.Fatalf("expected the same conversation reactivated, got %+v", second)
		}
		if got := countRows(t, `
			SELECT count(*) FROM chat.dm_members
			WHERE conversation_id = $1 AND status = 'active' AND left_at IS NULL`, first.Conversation.ID); got != 2 {
			t.Fatalf("reactivated member rows = %d, want 2", got)
		}
	})

	// An account that loses eligibility after a conversation exists keeps its
	// membership: this guard is about joining, not about retroactively erasing
	// people from conversations they are already in.
	t.Run("losing eligibility later does not remove an existing membership", func(t *testing.T) {
		conversation, err := store.CreateGroupConversation(ctx, storage.CreateGroupConversationInput{
			WorkspaceID:        workspace,
			CreatedBy:          caller,
			ParticipantUserIDs: []string{caller, active1, active2},
		})
		if err != nil {
			t.Fatalf("CreateGroupConversation: %v", err)
		}
		if _, err := pool.Exec(ctx, `UPDATE auth.users SET status = 'suspended' WHERE id = $1`, active2); err != nil {
			t.Fatalf("suspend account: %v", err)
		}
		t.Cleanup(func() {
			_, _ = pool.Exec(context.Background(), `UPDATE auth.users SET status = 'active' WHERE id = $1`, active2)
		})
		if got := countRows(t, `SELECT count(*) FROM chat.dm_members WHERE conversation_id = $1 AND status = 'active'`, conversation.ID); got != 3 {
			t.Fatalf("member rows = %d, want 3", got)
		}
	})
}
