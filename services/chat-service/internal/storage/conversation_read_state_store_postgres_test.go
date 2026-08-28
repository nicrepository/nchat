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

func TestPGXConversationReadStateStorePostgreSQL(t *testing.T) {
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

	const (
		workspace = "d1000000-0000-4000-8000-000000000001"
		reader    = "d1000000-0000-4000-8000-000000000002"
		sender    = "d1000000-0000-4000-8000-000000000003"
		channel   = "d1000000-0000-4000-8000-000000000004"
		dm        = "d1000000-0000-4000-8000-000000000005"
	)
	for _, seed := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO auth.users (id, email, display_name) VALUES
			($1, 'reader@example.test', 'Reader'), ($2, 'sender@example.test', 'Sender')
			ON CONFLICT (id) DO NOTHING`, []any{reader, sender}},
		{`INSERT INTO chat.workspaces (id, slug, name) VALUES ($1, 'read-state', 'Read state')`, []any{workspace}},
		{`INSERT INTO chat.channels (id, workspace_id, slug, display_name, type, is_general)
			VALUES ($1, $2, 'geral-read', 'Geral read', 'public', true)`, []any{channel, workspace}},
		{`INSERT INTO chat.workspace_members (workspace_id, user_id) VALUES ($1, $2), ($1, $3)`, []any{workspace, reader, sender}},
		{`INSERT INTO chat.dm_conversations (id, workspace_id, type, title, created_by, direct_pair_key)
			VALUES ($1, $2, 'direct', NULL, $3, 'read-state-pair')`, []any{dm, workspace, reader}},
		{`INSERT INTO chat.dm_members (conversation_id, user_id) VALUES ($1, $2), ($1, $3)`, []any{dm, reader, sender}},
		{`INSERT INTO chat.messages (workspace_id, channel_id, sender_id, body_text, created_at) VALUES
			($1, $2, $3, 'other channel', now() - interval '1 hour'),
			($1, $2, $4, 'own channel', now() - interval '30 minutes')`, []any{workspace, channel, sender, reader}},
		{`INSERT INTO chat.messages (workspace_id, dm_conversation_id, sender_id, body_text, created_at)
			VALUES ($1, $2, $3, 'other dm', now() - interval '1 hour')`, []any{workspace, dm, sender}},
	} {
		if _, err := pool.Exec(ctx, seed.sql, seed.args...); err != nil {
			t.Fatalf("seed read-state cases: %v", err)
		}
	}

	store := storage.NewPGXConversationReadStateStore(pool)
	counts, err := store.UnreadCounts(ctx, workspace, reader)
	if err != nil {
		t.Fatalf("initial UnreadCounts: %v", err)
	}
	if counts[storage.ConversationReadTargetChannel+"\x00"+channel] != 1 || counts[storage.ConversationReadTargetDM+"\x00"+dm] != 1 {
		t.Fatalf("own message was counted or target missing: %#v", counts)
	}
	if err := store.MarkRead(ctx, workspace, reader, storage.ConversationReadTargetChannel, channel, nil); err != nil {
		t.Fatalf("mark channel read: %v", err)
	}
	if err := store.MarkRead(ctx, workspace, reader, storage.ConversationReadTargetDM, dm, nil); err != nil {
		t.Fatalf("mark DM read: %v", err)
	}
	counts, err = store.UnreadCounts(ctx, workspace, reader)
	if err != nil {
		t.Fatalf("post-read UnreadCounts: %v", err)
	}
	if counts[storage.ConversationReadTargetChannel+"\x00"+channel] != 0 || counts[storage.ConversationReadTargetDM+"\x00"+dm] != 0 {
		t.Fatalf("counts did not clear after mark-read: %#v", counts)
	}
	if err := store.MarkRead(ctx, workspace, reader, storage.ConversationReadTargetChannel, "d1000000-0000-4000-8000-000000000099", nil); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing target must be non-enumerating ErrNotFound, got %v", err)
	}
}
