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

func TestPGXMessageStoreForwardChannelMessagePostgreSQL(t *testing.T) {
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
		t.Fatalf("refusing destructive forwarding test against non-test database %q", databaseName)
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
		workspace  = "f1000000-0000-4000-8000-000000000001"
		otherWS    = "f2000000-0000-4000-8000-000000000001"
		actor      = "f1000000-0000-4000-8000-000000000002"
		author     = "f1000000-0000-4000-8000-000000000003"
		origin     = "f1000000-0000-4000-8000-000000000004"
		dest       = "f1000000-0000-4000-8000-000000000005"
		private    = "f1000000-0000-4000-8000-000000000006"
		archived   = "f1000000-0000-4000-8000-000000000007"
		crossDest  = "f2000000-0000-4000-8000-000000000002"
		dmID       = "f1000000-0000-4000-8000-000000000008"
		source     = "f1000000-0000-4000-8000-000000000009"
		systemMsg  = "f1000000-0000-4000-8000-00000000000a"
		deleted    = "f1000000-0000-4000-8000-00000000000b"
		dmMsg      = "f1000000-0000-4000-8000-00000000000c"
		privateMsg = "f1000000-0000-4000-8000-00000000000d"
	)
	if _, err := pool.Exec(ctx, `
		INSERT INTO auth.users (id, email, display_name) VALUES
			($1, 'actor@example.test', 'Actor'),
			($2, 'author@example.test', 'Author')
		ON CONFLICT (id) DO NOTHING;
		INSERT INTO chat.workspaces (id, slug, name) VALUES
			($3, 'rf08-forward-a', 'RF08 A'),
			($4, 'rf08-forward-b', 'RF08 B');
		INSERT INTO chat.workspace_members (workspace_id, user_id, status) VALUES
			($3, $1, 'active'), ($3, $2, 'active'), ($4, $1, 'active');
		INSERT INTO chat.channels (id, workspace_id, slug, display_name, type, status) VALUES
			($5, $3, 'origin', 'Origin', 'public', 'active'),
			($6, $3, 'destination', 'Destination', 'public', 'active'),
			($7, $3, 'private-destination', 'Private destination', 'private', 'active'),
			($8, $3, 'archived-destination', 'Archived destination', 'public', 'archived'),
			($9, $4, 'cross-destination', 'Cross destination', 'public', 'active');
		INSERT INTO chat.dm_conversations (id, workspace_id, type, status, created_by)
			VALUES ($10, $3, 'group', 'active', $2);
		INSERT INTO chat.dm_members (conversation_id, user_id) VALUES ($10, $1), ($10, $2);
		INSERT INTO chat.messages
			(id, workspace_id, channel_id, dm_conversation_id, sender_id, kind, body_text, body_format, status, deleted_at)
		VALUES
			($11, $3, $5, NULL, $2, 'user', 'canonical snapshot', 'v3', 'active', NULL),
			($12, $3, $5, NULL, $2, 'system', 'system', 'v1', 'active', NULL),
			($13, $3, $5, NULL, $2, 'user', '', 'v1', 'deleted', now()),
			($14, $3, NULL, $10, $2, 'user', 'dm', 'v2', 'active', NULL),
			($15, $3, $7, NULL, $2, 'user', 'private', 'v1', 'active', NULL)`,
		actor, author, workspace, otherWS, origin, dest, private, archived, crossDest,
		dmID, source, systemMsg, deleted, dmMsg, privateMsg); err != nil {
		t.Fatalf("seed forwarding cases: %v", err)
	}

	store := storage.NewPGXMessageStore(pool)
	input := storage.ForwardChannelMessageInput{
		WorkspaceID: workspace, DestinationChannelID: dest, ActorID: actor,
		SourceMessageID: source, IdempotencyKey: "concurrent-action",
	}
	results := make([]storage.ForwardChannelMessageResult, 2)
	errs := make([]error, 2)
	var wait sync.WaitGroup
	for i := range results {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			results[index], errs[index] = store.ForwardChannelMessage(context.Background(), input)
		}(i)
	}
	wait.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent forward %d: %v", i, err)
		}
	}
	if results[0].Message.ID != results[1].Message.ID ||
		results[0].Replayed == results[1].Replayed {
		t.Fatalf("expected one insert and one replay of the same message: %+v", results)
	}
	if results[0].Message.BodyText != "canonical snapshot" ||
		results[0].Message.BodyFormat != domain.MessageBodyFormatV3 ||
		results[0].Message.ForwardedFromMessageID != source {
		t.Fatalf("snapshot/provenance mismatch: %+v", results[0].Message)
	}
	var count int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM chat.messages
		WHERE workspace_id = $1 AND sender_id = $2 AND channel_id = $3
		  AND forward_idempotency_key = $4`,
		workspace, actor, dest, input.IdempotencyKey).Scan(&count); err != nil || count != 1 {
		t.Fatalf("idempotent row count=%d err=%v", count, err)
	}

	for name, deniedInput := range map[string]storage.ForwardChannelMessageInput{
		"same channel": {
			WorkspaceID: workspace, DestinationChannelID: origin, ActorID: actor, SourceMessageID: source,
		},
		"private destination": {
			WorkspaceID: workspace, DestinationChannelID: private, ActorID: actor, SourceMessageID: source,
		},
		"archived destination": {
			WorkspaceID: workspace, DestinationChannelID: archived, ActorID: actor, SourceMessageID: source,
		},
		"cross workspace destination": {
			WorkspaceID: workspace, DestinationChannelID: crossDest, ActorID: actor, SourceMessageID: source,
		},
		"system source": {
			WorkspaceID: workspace, DestinationChannelID: dest, ActorID: actor, SourceMessageID: systemMsg,
		},
		"deleted source": {
			WorkspaceID: workspace, DestinationChannelID: dest, ActorID: actor, SourceMessageID: deleted,
		},
		"dm source": {
			WorkspaceID: workspace, DestinationChannelID: dest, ActorID: actor, SourceMessageID: dmMsg,
		},
		"inaccessible source": {
			WorkspaceID: workspace, DestinationChannelID: dest, ActorID: actor, SourceMessageID: privateMsg,
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := store.ForwardChannelMessage(t.Context(), deniedInput)
			if !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("expected non-enumerating ErrNotFound, got %v", err)
			}
		})
	}

	if _, err := pool.Exec(ctx, `
		UPDATE chat.workspace_members SET status = 'suspended'
		WHERE workspace_id = $1 AND user_id = $2`, workspace, actor); err != nil {
		t.Fatalf("suspend actor: %v", err)
	}
	_, err = store.ForwardChannelMessage(ctx, storage.ForwardChannelMessageInput{
		WorkspaceID: workspace, DestinationChannelID: dest, ActorID: actor, SourceMessageID: source,
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("inactive actor must be non-enumerating, got %v", err)
	}
}
