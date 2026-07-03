package storage_test

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// TestPGXReactionStore_ConcurrentToggleSerializesViaMessageLock exercises the
// one behavior pgxmock cannot: real concurrent transactions racing on the
// same (message_id, user_id, emoji) tuple. It asserts that the FOR UPDATE OF m
// lock in authorizeMessageAccess actually serializes ToggleReaction calls,
// leaving no duplicate rows and a deterministic added/removed split.
func TestPGXReactionStore_ConcurrentToggleSerializesViaMessageLock(t *testing.T) {
	dsn := os.Getenv("CHAT_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("CHAT_TEST_DATABASE_URL is not set")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	defer pool.Close()

	var databaseName string
	if err := pool.QueryRow(ctx, `SELECT current_database()`).Scan(&databaseName); err != nil {
		t.Fatalf("read current database: %v", err)
	}
	if len(databaseName) < 5 || databaseName[len(databaseName)-5:] != "_test" {
		t.Fatalf("refusing to run against non-test database %q", databaseName)
	}

	if _, err := pool.Exec(ctx, `DROP SCHEMA IF EXISTS chat CASCADE`); err != nil {
		t.Fatalf("reset chat schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DROP SCHEMA IF EXISTS chat CASCADE`)
	})
	if _, err := pool.Exec(ctx, readAllChatUpMigrations(t)); err != nil {
		t.Fatalf("apply chat migrations: %v", err)
	}

	// message_reactions.user_id has a real FK to auth.users; ensure the schema
	// exists (owned by auth-service migrations against the same database) and
	// seed the one test user this test needs.
	if _, err := pool.Exec(ctx, `CREATE SCHEMA IF NOT EXISTS auth`); err != nil {
		t.Fatalf("ensure auth schema: %v", err)
	}
	if _, err := pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS auth.users (id UUID PRIMARY KEY)`); err != nil {
		t.Fatalf("ensure auth.users: %v", err)
	}

	const (
		workspaceID = "00000000-0000-0000-0000-000000000001" // seeded by 000001 migration
		userID      = "00000000-0000-0000-0000-0000000000aa"
		emoji       = "👍"
	)
	if _, err := pool.Exec(ctx, `INSERT INTO auth.users (id) VALUES ($1) ON CONFLICT (id) DO NOTHING`, userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO chat.workspace_members (workspace_id, user_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
		workspaceID, userID); err != nil {
		t.Fatalf("seed workspace member: %v", err)
	}
	var channelID string
	if err := pool.QueryRow(ctx,
		`SELECT id FROM chat.channels WHERE workspace_id = $1 AND is_general = true`, workspaceID).
		Scan(&channelID); err != nil {
		t.Fatalf("read seeded general channel: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO chat.channel_members (channel_id, user_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
		channelID, userID); err != nil {
		t.Fatalf("seed channel member: %v", err)
	}
	var messageID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO chat.messages (workspace_id, channel_id, sender_id, body_text) VALUES ($1, $2, $3, 'race target') RETURNING id`,
		workspaceID, channelID, userID).Scan(&messageID); err != nil {
		t.Fatalf("seed message: %v", err)
	}

	store := storage.NewPGXReactionStore(pool)
	input := storage.ToggleReactionInput{WorkspaceID: workspaceID, UserID: userID, MessageID: messageID, Emoji: emoji}

	const concurrency = 10 // even, so the toggle nets back to "removed"
	var wg sync.WaitGroup
	results := make(chan bool, concurrency)
	errs := make(chan error, concurrency)
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			callCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			result, err := store.ToggleReaction(callCtx, input)
			if err != nil {
				errs <- err
				return
			}
			results <- result.Added
		}()
	}
	wg.Wait()
	close(results)
	close(errs)

	for err := range errs {
		t.Fatalf("concurrent ToggleReaction: %v", err)
	}

	addedCount, removedCount := 0, 0
	for added := range results {
		if added {
			addedCount++
		} else {
			removedCount++
		}
	}
	// A toggle starting from "no reaction" alternates add/remove regardless of
	// goroutine interleaving order: N applications always yield ceil(N/2) adds
	// and floor(N/2) removes.
	if addedCount != concurrency/2 || removedCount != concurrency/2 {
		t.Fatalf("expected %d adds and %d removes from %d serialized toggles, got %d adds and %d removes",
			concurrency/2, concurrency/2, concurrency, addedCount, removedCount)
	}

	var rowCount int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM chat.message_reactions WHERE message_id = $1 AND user_id = $2 AND emoji = $3`,
		messageID, userID, emoji).Scan(&rowCount); err != nil {
		t.Fatalf("count reaction rows: %v", err)
	}
	if rowCount != 0 {
		t.Fatalf("expected no leftover reaction row after an even number of toggles, found %d", rowCount)
	}
}
