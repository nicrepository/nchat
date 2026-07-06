package storage_test

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

const (
	reactionWorkspaceID = "00000000-0000-0000-0000-000000000001"
	reactionUserID      = "00000000-0000-0000-0000-0000000000aa"
	reactionOtherUserID = "00000000-0000-0000-0000-0000000000bb"
	reactionEmoji       = "👍"
)

type reactionPostgresFixture struct {
	pool      *pgxpool.Pool
	store     *storage.PGXReactionStore
	channelID string
	messageID string
}

func newReactionPostgresFixture(t *testing.T) *reactionPostgresFixture {
	t.Helper()
	dsn := os.Getenv("CHAT_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("CHAT_TEST_DATABASE_URL is not set")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

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
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DROP SCHEMA IF EXISTS chat CASCADE`) })
	if _, err := pool.Exec(ctx, `CREATE SCHEMA IF NOT EXISTS auth`); err != nil {
		t.Fatalf("ensure auth schema: %v", err)
	}
	if _, err := pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS auth.users (id UUID PRIMARY KEY)`); err != nil {
		t.Fatalf("ensure auth.users: %v", err)
	}
	if _, err := pool.Exec(ctx, readAllChatUpMigrations(t)); err != nil {
		t.Fatalf("apply chat migrations: %v", err)
	}

	var channelID string
	if err := pool.QueryRow(ctx,
		`SELECT id FROM chat.channels WHERE workspace_id = $1 AND is_general = true`, reactionWorkspaceID).
		Scan(&channelID); err != nil {
		t.Fatalf("read seeded general channel: %v", err)
	}
	fixture := &reactionPostgresFixture{
		pool: pool, store: storage.NewPGXReactionStore(pool), channelID: channelID,
	}
	fixture.addUser(t, reactionUserID)
	if err := pool.QueryRow(ctx,
		`INSERT INTO chat.messages (workspace_id, channel_id, sender_id, body_text) VALUES ($1, $2, $3, 'race target') RETURNING id`,
		reactionWorkspaceID, channelID, reactionUserID).Scan(&fixture.messageID); err != nil {
		t.Fatalf("seed message: %v", err)
	}
	return fixture
}

func (f *reactionPostgresFixture) addUser(t *testing.T, userID string) {
	t.Helper()
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `INSERT INTO auth.users (id) VALUES ($1) ON CONFLICT (id) DO NOTHING`, userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO chat.workspace_members (workspace_id, user_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
		reactionWorkspaceID, userID); err != nil {
		t.Fatalf("seed workspace member: %v", err)
	}
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO chat.channel_members (channel_id, user_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
		f.channelID, userID); err != nil {
		t.Fatalf("seed channel member: %v", err)
	}
}

func (f *reactionPostgresFixture) input(userID string) storage.ToggleReactionInput {
	return storage.ToggleReactionInput{
		WorkspaceID: reactionWorkspaceID, UserID: userID, MessageID: f.messageID, Emoji: reactionEmoji,
	}
}

func TestPGXReactionStoreConcurrentToggleSerializesViaAdvisoryTupleLock(t *testing.T) {
	fixture := newReactionPostgresFixture(t)
	input := fixture.input(reactionUserID)

	const concurrency = 10
	var wg sync.WaitGroup
	results := make(chan bool, concurrency)
	errs := make(chan error, concurrency)
	for range concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			callCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			result, err := fixture.store.ToggleReaction(callCtx, input)
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
	if addedCount != concurrency/2 || removedCount != concurrency/2 {
		t.Fatalf("expected %d adds and removes, got %d adds and %d removes",
			concurrency/2, addedCount, removedCount)
	}

	var rowCount int
	if err := fixture.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM chat.message_reactions WHERE message_id = $1 AND user_id = $2 AND emoji = $3`,
		fixture.messageID, reactionUserID, reactionEmoji).Scan(&rowCount); err != nil {
		t.Fatalf("count reaction rows: %v", err)
	}
	if rowCount != 0 {
		t.Fatalf("expected no reaction after even toggles, found %d", rowCount)
	}
}

func TestPGXReactionStoreDifferentUsersDoNotSerializeOnSameMessage(t *testing.T) {
	fixture := newReactionPostgresFixture(t)
	fixture.addUser(t, reactionOtherUserID)
	assertReactionTuplesDoNotBlock(t, fixture, fixture.input(reactionUserID), fixture.input(reactionOtherUserID))
}

func TestPGXReactionStoreSameUserDifferentEmojiDoNotSerializeOnSameMessage(t *testing.T) {
	fixture := newReactionPostgresFixture(t)
	otherEmoji := fixture.input(reactionUserID)
	otherEmoji.Emoji = "🔥"
	assertReactionTuplesDoNotBlock(t, fixture, fixture.input(reactionUserID), otherEmoji)
}

func assertReactionTuplesDoNotBlock(
	t *testing.T,
	fixture *reactionPostgresFixture,
	slowInput, fastInput storage.ToggleReactionInput,
) {
	t.Helper()
	gate := holdReactionAdvisoryLock(t, fixture.pool, reactionAdvisoryKey(slowInput))

	type toggleOutcome struct {
		result storage.ToggleReactionResult
		err    error
	}
	slowResult := make(chan toggleOutcome, 1)
	go func() {
		result, err := fixture.store.ToggleReaction(context.Background(), slowInput)
		slowResult <- toggleOutcome{result: result, err: err}
	}()
	waitForAdvisoryWaiter(t, fixture.pool)

	fastCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := fixture.store.ToggleReaction(fastCtx, fastInput); err != nil {
		t.Fatalf("distinct reaction tuple was blocked by the first tuple: %v", err)
	}
	if err := gate.Rollback(context.Background()); err != nil {
		t.Fatalf("release advisory gate: %v", err)
	}
	outcome := <-slowResult
	if outcome.err != nil {
		t.Fatalf("blocked tuple toggle: %v", outcome.err)
	}
	total := 0
	for _, reaction := range outcome.result.Reactions {
		total += reaction.Count
	}
	if total != 2 {
		t.Fatalf("aggregate after both toggles contains %d reactions, want 2", total)
	}
	var count int
	if err := fixture.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM chat.message_reactions WHERE message_id = $1`, fixture.messageID).Scan(&count); err != nil {
		t.Fatalf("count concurrent reaction rows: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected both distinct reaction tuples, found %d rows", count)
	}
}

func TestPGXReactionStoreReauthorizesAfterAdvisoryLock(t *testing.T) {
	for _, tt := range []struct {
		name   string
		mutate func(context.Context, *reactionPostgresFixture) error
	}{
		{name: "soft-deleted message", mutate: func(ctx context.Context, f *reactionPostgresFixture) error {
			_, err := f.pool.Exec(ctx, `UPDATE chat.messages SET status = 'deleted', deleted_at = now() WHERE id = $1`, f.messageID)
			return err
		}},
		{name: "revoked workspace membership", mutate: func(ctx context.Context, f *reactionPostgresFixture) error {
			_, err := f.pool.Exec(ctx, `UPDATE chat.workspace_members SET status = 'suspended' WHERE workspace_id = $1 AND user_id = $2`, reactionWorkspaceID, reactionUserID)
			return err
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newReactionPostgresFixture(t)
			input := fixture.input(reactionUserID)
			gate := holdReactionAdvisoryLock(t, fixture.pool, reactionAdvisoryKey(input))
			result := make(chan error, 1)
			go func() {
				_, err := fixture.store.ToggleReaction(context.Background(), input)
				result <- err
			}()
			waitForAdvisoryWaiter(t, fixture.pool)
			if err := tt.mutate(context.Background(), fixture); err != nil {
				t.Fatalf("mutate authorization state: %v", err)
			}
			if err := gate.Rollback(context.Background()); err != nil {
				t.Fatalf("release advisory gate: %v", err)
			}
			if err := <-result; !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("expected reauthorization failure, got %v", err)
			}
			var count int
			if err := fixture.pool.QueryRow(context.Background(),
				`SELECT count(*) FROM chat.message_reactions WHERE message_id = $1 AND user_id = $2`,
				fixture.messageID, reactionUserID).Scan(&count); err != nil {
				t.Fatalf("count reactions: %v", err)
			}
			if count != 0 {
				t.Fatalf("unauthorized toggle wrote %d reaction rows", count)
			}
		})
	}
}

func holdReactionAdvisoryLock(t *testing.T, pool *pgxpool.Pool, key int64) pgx.Tx {
	t.Helper()
	tx, err := pool.Begin(context.Background())
	if err != nil {
		t.Fatalf("begin advisory gate: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	if _, err := tx.Exec(context.Background(), `SELECT pg_advisory_xact_lock($1)`, key); err != nil {
		t.Fatalf("acquire advisory gate: %v", err)
	}
	return tx
}

func waitForAdvisoryWaiter(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for ctx.Err() == nil {
		var waiting bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS (
			SELECT 1 FROM pg_locks
			WHERE locktype = 'advisory' AND database = (SELECT oid FROM pg_database WHERE datname = current_database())
			  AND NOT granted
		)`).Scan(&waiting); err != nil {
			t.Fatalf("inspect advisory locks: %v", err)
		}
		if waiting {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("toggle did not wait on its tuple advisory lock")
}
