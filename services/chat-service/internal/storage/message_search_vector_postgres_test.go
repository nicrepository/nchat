package storage_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// RF-15 (issue #123, TASK-94). All assertions run against a real PostgreSQL:
// static string checks on the migration file cannot prove the Portuguese
// dictionary actually stems, that the GIN index is actually usable, or that
// the resync trigger actually fires when a channel's type changes.
func TestChatMigration_MessageSearchVector_PostgreSQLBehavior(t *testing.T) {
	dsn := os.Getenv("CHAT_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("CHAT_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	defer func() { _ = conn.Close(context.Background()) }()

	var databaseName string
	if err := conn.QueryRow(ctx, `SELECT current_database()`).Scan(&databaseName); err != nil {
		t.Fatalf("read current database: %v", err)
	}
	if !strings.HasSuffix(databaseName, "_test") {
		t.Fatalf("refusing destructive search vector test against non-test database %q", databaseName)
	}
	if _, err := conn.Exec(ctx, `DROP SCHEMA IF EXISTS chat CASCADE`); err != nil {
		t.Fatalf("reset chat schema: %v", err)
	}
	t.Cleanup(func() { _, _ = conn.Exec(context.Background(), `DROP SCHEMA IF EXISTS chat CASCADE`) })

	// Some chat migrations (message_edit_history, message_reactions, ...) hold
	// foreign keys into auth.users. This mirrors the fixture other *_postgres_test.go
	// files in this package already use.
	if _, err := conn.Exec(ctx, `
		CREATE SCHEMA IF NOT EXISTS auth;
		CREATE TABLE IF NOT EXISTS auth.users (
			id UUID PRIMARY KEY,
			email TEXT NOT NULL DEFAULT '',
			display_name TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'active',
			deleted_at TIMESTAMPTZ
		)`); err != nil {
		t.Fatalf("prepare auth schema required by chat foreign keys: %v", err)
	}
	if _, err := conn.Exec(ctx, readAllChatUpMigrations(t)); err != nil {
		t.Fatalf("apply chat migrations: %v", err)
	}

	const workspaceID = "c1000000-0000-0000-0000-000000000001"
	// Workspace + general channel must commit together: the deferred
	// constraint trigger from 000002 requires an active public general
	// channel to exist by commit time.
	seedWorkspace := &pgx.Batch{}
	seedWorkspace.Queue(`INSERT INTO chat.workspaces (id, slug, name) VALUES ($1, 'search-ws', 'Search WS')`, workspaceID)
	seedWorkspace.Queue(`INSERT INTO chat.channels (workspace_id, slug, display_name, type, is_general)
		VALUES ($1, 'geral', 'Geral', 'public', true)`, workspaceID)
	if err := conn.SendBatch(ctx, seedWorkspace).Close(); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}

	t.Run("indexing", func(t *testing.T) {
		const (
			publicChannel   = "c1000000-0000-0000-0000-000000000010"
			privateChannel  = "c1000000-0000-0000-0000-000000000011"
			archivedChannel = "c1000000-0000-0000-0000-000000000012"
		)
		if _, err := conn.Exec(ctx, `
			INSERT INTO chat.channels (id, workspace_id, slug, display_name, type)
			VALUES
				($1, $3, 'public-search', 'Public Search', 'public'),
				($2, $3, 'private-search', 'Private Search', 'private')`,
			publicChannel, privateChannel, workspaceID); err != nil {
			t.Fatalf("seed channels: %v", err)
		}
		if _, err := conn.Exec(ctx, `
			INSERT INTO chat.channels (id, workspace_id, slug, display_name, type, status)
			VALUES ($1, $2, 'archived-search', 'Archived Search', 'public', 'archived')`,
			archivedChannel, workspaceID); err != nil {
			t.Fatalf("seed archived channel: %v", err)
		}
		const sender = "c1000000-0000-0000-0000-000000000099"

		var publicMsgID string
		if err := conn.QueryRow(ctx, `
			INSERT INTO chat.messages (workspace_id, channel_id, sender_id, body_text)
			VALUES ($1, $2, $3, 'mensagem publica sobre relatorios')
			RETURNING id`, workspaceID, publicChannel, sender).Scan(&publicMsgID); err != nil {
			t.Fatalf("insert public channel message: %v", err)
		}
		if !messageIsIndexed(t, ctx, conn, publicMsgID) {
			t.Fatal("active public channel message must be indexed")
		}

		var privateMsgID string
		if err := conn.QueryRow(ctx, `
			INSERT INTO chat.messages (workspace_id, channel_id, sender_id, body_text)
			VALUES ($1, $2, $3, 'mensagem privada sobre relatorios')
			RETURNING id`, workspaceID, privateChannel, sender).Scan(&privateMsgID); err != nil {
			t.Fatalf("insert private channel message: %v", err)
		}
		if messageIsIndexed(t, ctx, conn, privateMsgID) {
			t.Fatal("private channel message must not be indexed")
		}

		var archivedMsgID string
		if err := conn.QueryRow(ctx, `
			INSERT INTO chat.messages (workspace_id, channel_id, sender_id, body_text)
			VALUES ($1, $2, $3, 'mensagem em canal arquivado sobre relatorios')
			RETURNING id`, workspaceID, archivedChannel, sender).Scan(&archivedMsgID); err != nil {
			t.Fatalf("insert archived channel message: %v", err)
		}
		if messageIsIndexed(t, ctx, conn, archivedMsgID) {
			t.Fatal("message in an already-archived public channel must not be indexed")
		}

		var directDMID string
		if err := conn.QueryRow(ctx, `
			INSERT INTO chat.dm_conversations (workspace_id, type, status, created_by, direct_pair_key)
			VALUES ($1, 'direct', 'active', $2, 'search-dm-pair')
			RETURNING id`, workspaceID, sender).Scan(&directDMID); err != nil {
			t.Fatalf("seed direct dm: %v", err)
		}
		var directMsgID string
		if err := conn.QueryRow(ctx, `
			INSERT INTO chat.messages (workspace_id, dm_conversation_id, sender_id, body_text)
			VALUES ($1, $2, $3, 'mensagem direta sobre relatorios')
			RETURNING id`, workspaceID, directDMID, sender).Scan(&directMsgID); err != nil {
			t.Fatalf("insert direct dm message: %v", err)
		}
		if messageIsIndexed(t, ctx, conn, directMsgID) {
			t.Fatal("1:1 DM message must not be indexed")
		}

		var groupDMID string
		if err := conn.QueryRow(ctx, `
			INSERT INTO chat.dm_conversations (workspace_id, type, status, created_by)
			VALUES ($1, 'group', 'active', $2)
			RETURNING id`, workspaceID, sender).Scan(&groupDMID); err != nil {
			t.Fatalf("seed group dm: %v", err)
		}
		var groupMsgID string
		if err := conn.QueryRow(ctx, `
			INSERT INTO chat.messages (workspace_id, dm_conversation_id, sender_id, body_text)
			VALUES ($1, $2, $3, 'mensagem de grupo sobre relatorios')
			RETURNING id`, workspaceID, groupDMID, sender).Scan(&groupMsgID); err != nil {
			t.Fatalf("insert group dm message: %v", err)
		}
		if messageIsIndexed(t, ctx, conn, groupMsgID) {
			t.Fatal("group DM message must not be indexed")
		}

		if _, err := conn.Exec(ctx, `UPDATE chat.messages SET body_text = 'mensagem publica sobre orcamentos' WHERE id = $1`, publicMsgID); err != nil {
			t.Fatalf("edit public message body: %v", err)
		}
		if messageMatches(t, ctx, conn, publicMsgID, "relatorios") {
			t.Fatal("search_vector must follow a body edit: old term must no longer match")
		}
		if !messageMatches(t, ctx, conn, publicMsgID, "orcamentos") {
			t.Fatal("search_vector must follow a body edit: new term must match")
		}

		if _, err := conn.Exec(ctx, `UPDATE chat.messages SET status = 'deleted', deleted_at = now() WHERE id = $1`, publicMsgID); err != nil {
			t.Fatalf("soft delete public message: %v", err)
		}
		if messageIsIndexed(t, ctx, conn, publicMsgID) {
			t.Fatal("soft-deleted message must not remain indexed")
		}
	})

	t.Run("channel privacy or archive flip resyncs existing messages", func(t *testing.T) {
		for i, tc := range []struct {
			name     string
			column   string
			offValue string
			onValue  string
		}{
			{name: "public -> private -> public", column: "type", offValue: "private", onValue: "public"},
			{name: "active -> archived -> active", column: "status", offValue: "archived", onValue: "active"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				flipChannel := fmt.Sprintf("c1000000-0000-0000-0000-00000000002%d", i)
				sender := fmt.Sprintf("c1000000-0000-0000-0000-00000000009%d", i)
				if _, err := conn.Exec(ctx, `
					INSERT INTO chat.channels (id, workspace_id, slug, display_name, type)
					VALUES ($1, $2, $3, 'Flip Channel', 'public')`,
					flipChannel, workspaceID, fmt.Sprintf("flip-channel-%d", i)); err != nil {
					t.Fatalf("seed flip channel: %v", err)
				}
				var msgID string
				if err := conn.QueryRow(ctx, `
					INSERT INTO chat.messages (workspace_id, channel_id, sender_id, body_text)
					VALUES ($1, $2, $3, 'conteudo que muda de visibilidade')
					RETURNING id`, workspaceID, flipChannel, sender).Scan(&msgID); err != nil {
					t.Fatalf("insert flip-channel message: %v", err)
				}
				if !messageIsIndexed(t, ctx, conn, msgID) {
					t.Fatal("message must start indexed while channel is public and active")
				}

				flip := func(value string) { //nolint:gosec // tc.column is a fixed test literal, never user input.
					sql := fmt.Sprintf(`UPDATE chat.channels SET %s = $1 WHERE id = $2`, tc.column)
					if _, err := conn.Exec(ctx, sql, value, flipChannel); err != nil {
						t.Fatalf("flip channel %s to %s: %v", tc.column, value, err)
					}
				}

				flip(tc.offValue)
				if messageIsIndexed(t, ctx, conn, msgID) {
					t.Fatalf("%s = %s must clear search_vector for existing messages", tc.column, tc.offValue)
				}

				flip(tc.onValue)
				if !messageIsIndexed(t, ctx, conn, msgID) {
					t.Fatalf("%s = %s must re-populate search_vector for existing messages, once public and active again", tc.column, tc.onValue)
				}
			})
		}
	})

	t.Run("portuguese dictionary stems verbs", func(t *testing.T) {
		var stemmedMatch bool
		if err := conn.QueryRow(ctx, `
			SELECT to_tsvector('portuguese', 'Os funcionarios estavam trabalhando no relatorio')
				@@ to_tsquery('portuguese', 'trabalhar')`).Scan(&stemmedMatch); err != nil {
			t.Fatalf("stem query: %v", err)
		}
		if !stemmedMatch {
			t.Fatal("portuguese config must stem 'trabalhando' to match a 'trabalhar' query")
		}

		// 'simple' does no stemming, so the same query must not match: this is
		// what proves the migration is not silently relying on the database
		// default configuration.
		var unstemmedMatch bool
		if err := conn.QueryRow(ctx, `
			SELECT to_tsvector('simple', 'Os funcionarios estavam trabalhando no relatorio')
				@@ to_tsquery('simple', 'trabalhar')`).Scan(&unstemmedMatch); err != nil {
			t.Fatalf("unstemmed control query: %v", err)
		}
		if unstemmedMatch {
			t.Fatal("control query must not match without stemming - test is not isolating the portuguese config")
		}

		var stopwordRemoved bool
		if err := conn.QueryRow(ctx, `
			SELECT length(to_tsvector('portuguese', 'de para com')::text) = 0`).Scan(&stopwordRemoved); err != nil {
			t.Fatalf("stopword query: %v", err)
		}
		if !stopwordRemoved {
			t.Fatal("portuguese config must strip common Portuguese stopwords")
		}
	})

	t.Run("ranking", func(t *testing.T) {
		const rankChannel = "c1000000-0000-0000-0000-000000000030"
		const sender = "c1000000-0000-0000-0000-000000000097"
		if _, err := conn.Exec(ctx, `
			INSERT INTO chat.channels (id, workspace_id, slug, display_name, type)
			VALUES ($1, $2, 'rank-channel', 'Rank Channel', 'public')`, rankChannel, workspaceID); err != nil {
			t.Fatalf("seed rank channel: %v", err)
		}

		insert := func(body string, ageInterval string) string {
			var id string
			if err := conn.QueryRow(ctx, `
				INSERT INTO chat.messages (workspace_id, channel_id, sender_id, body_text, created_at)
				VALUES ($1, $2, $3, $4, now() - $5::interval)
				RETURNING id`, workspaceID, rankChannel, sender, body, ageInterval).Scan(&id); err != nil {
				t.Fatalf("insert ranking fixture: %v", err)
			}
			return id
		}

		// A material relevance gap (far more than the 10% recency cap can
		// close) is not erased by recency: a message dense with the search
		// term, but old, still outranks one that barely mentions it, even
		// freshly posted.
		relevantOld := insert(strings.Repeat("postgresql ", 8)+"e indices de busca", "100 days")
		barelyRelevantNew := insert("hoje falamos sobre bancos de dados e mencionamos postgresql de passagem em meio a muitos outros assuntos administrativos e operacionais do dia", "0 days")

		relevantOldRank := searchRank(t, ctx, conn, relevantOld, "postgresql")
		barelyRelevantNewRank := searchRank(t, ctx, conn, barelyRelevantNew, "postgresql")
		if relevantOldRank <= barelyRelevantNewRank {
			t.Fatalf("a material relevance gap must survive the bounded recency boost: old dense match (%v) must outrank new sparse match (%v)", relevantOldRank, barelyRelevantNewRank)
		}

		// Among comparable relevance, the more recent message gets a boost.
		tiedOld := insert("mensagem sobre orcamento anual", "60 days")
		tiedNew := insert("mensagem sobre orcamento anual", "1 days")
		tiedOldRank := searchRank(t, ctx, conn, tiedOld, "orcamento")
		tiedNewRank := searchRank(t, ctx, conn, tiedNew, "orcamento")
		if tiedNewRank <= tiedOldRank {
			t.Fatalf("recent message must be boosted over an equally relevant older one: new=%v old=%v", tiedNewRank, tiedOldRank)
		}
		// The boost is capped at +10%: it must not multiply the tied rank by
		// more than 1.1.
		if tiedNewRank > tiedOldRank*1.1+1e-9 {
			t.Fatalf("recency boost exceeded the documented 10%% cap: new=%v old=%v", tiedNewRank, tiedOldRank)
		}

		// No match must not appear: rank against an unrelated query is zero.
		noMatchRank := searchRank(t, ctx, conn, tiedNew, "inexistente")
		if noMatchRank != 0 {
			t.Fatalf("non-matching query must rank 0, got %v", noMatchRank)
		}
	})
}

// RF-15 post-Code Quality Review fix: the message trigger's SELECT ... FOR
// SHARE (see 000027) must serialize the "is this channel public and active"
// decision against a concurrent channel type/status change, so that after
// both transactions commit — regardless of which one the database happened
// to run first — a message can never end up indexed against a channel that
// is not public and active.
//
// The scenario under test is the one identified in review: a channel UPDATE
// (flipping it private or archived) and a message INSERT into that same
// channel are issued concurrently. Ordering is made deterministic by holding
// the channel row's write lock open in one transaction until PostgreSQL
// itself reports (via pg_locks) that the other session is blocked waiting on
// it — not by sleeping and hoping the interleaving landed a particular way.
func TestChatMigration_MessageSearchVector_ConcurrentChannelChangeVsMessageInsert_PostgreSQLBehavior(t *testing.T) {
	dsn := os.Getenv("CHAT_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("CHAT_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()

	setup, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	defer func() { _ = setup.Close(context.Background()) }()

	var databaseName string
	if err := setup.QueryRow(ctx, `SELECT current_database()`).Scan(&databaseName); err != nil {
		t.Fatalf("read current database: %v", err)
	}
	if !strings.HasSuffix(databaseName, "_test") {
		t.Fatalf("refusing destructive concurrency test against non-test database %q", databaseName)
	}
	if _, err := setup.Exec(ctx, `DROP SCHEMA IF EXISTS chat CASCADE`); err != nil {
		t.Fatalf("reset chat schema: %v", err)
	}
	t.Cleanup(func() { _, _ = setup.Exec(context.Background(), `DROP SCHEMA IF EXISTS chat CASCADE`) })
	if _, err := setup.Exec(ctx, `
		CREATE SCHEMA IF NOT EXISTS auth;
		CREATE TABLE IF NOT EXISTS auth.users (
			id UUID PRIMARY KEY,
			email TEXT NOT NULL DEFAULT '',
			display_name TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'active',
			deleted_at TIMESTAMPTZ
		)`); err != nil {
		t.Fatalf("prepare auth schema required by chat foreign keys: %v", err)
	}
	if _, err := setup.Exec(ctx, readAllChatUpMigrations(t)); err != nil {
		t.Fatalf("apply chat migrations: %v", err)
	}

	const workspaceID = "c9000000-0000-0000-0000-000000000001"
	seedWorkspace := &pgx.Batch{}
	seedWorkspace.Queue(`INSERT INTO chat.workspaces (id, slug, name) VALUES ($1, 'race-ws', 'Race WS')`, workspaceID)
	seedWorkspace.Queue(`INSERT INTO chat.channels (workspace_id, slug, display_name, type, is_general)
		VALUES ($1, 'geral', 'Geral', 'public', true)`, workspaceID)
	if err := setup.SendBatch(ctx, seedWorkspace).Close(); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}

	// Both type -> private and status -> archived take the same row-level
	// lock on chat.channels (a plain UPDATE, regardless of which column it
	// sets), so the same FOR SHARE mechanism closes the race for both. This
	// exercises both explicitly rather than only asserting it once and
	// trusting the argument.
	for i, tc := range []struct {
		name     string
		column   string
		offValue string
	}{
		{name: "public channel concurrently flips to private", column: "type", offValue: "private"},
		{name: "active channel concurrently flips to archived", column: "status", offValue: "archived"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var channelID string
			if err := setup.QueryRow(ctx, `
				INSERT INTO chat.channels (workspace_id, slug, display_name, type)
				VALUES ($1, $2, 'Race Channel', 'public') RETURNING id`,
				workspaceID, fmt.Sprintf("race-channel-%d", i)).Scan(&channelID); err != nil {
				t.Fatalf("seed race channel: %v", err)
			}

			connA, err := pgx.Connect(ctx, dsn)
			if err != nil {
				t.Fatalf("connect connA: %v", err)
			}
			defer func() { _ = connA.Close(context.Background()) }()
			connB, err := pgx.Connect(ctx, dsn)
			if err != nil {
				t.Fatalf("connect connB: %v", err)
			}
			defer func() { _ = connB.Close(context.Background()) }()

			txA, err := connA.Begin(ctx)
			if err != nil {
				t.Fatalf("begin txA: %v", err)
			}
			// txA takes the channel row's write lock and holds it open
			// (no commit yet), which is what forces connB's message insert
			// below to block instead of racing ahead with a stale view of
			// the channel.
			updateSQL := fmt.Sprintf(`UPDATE chat.channels SET %s = $1 WHERE id = $2`, tc.column) //nolint:gosec // tc.column is a fixed test literal, never user input.
			if _, err := txA.Exec(ctx, updateSQL, tc.offValue, channelID); err != nil {
				t.Fatalf("flip channel in txA: %v", err)
			}

			type insertOutcome struct {
				id  string
				err error
			}
			outcomeCh := make(chan insertOutcome, 1)
			go func() {
				var id string
				err := connB.QueryRow(ctx, `
					INSERT INTO chat.messages (workspace_id, channel_id, sender_id, body_text)
					VALUES ($1, $2, $1, 'mensagem concorrente com mudanca de canal')
					RETURNING id`, workspaceID, channelID).Scan(&id)
				outcomeCh <- insertOutcome{id: id, err: err}
			}()

			// Deterministic ordering: proceed only once PostgreSQL itself
			// reports connB is blocked on the channel row's lock (polling
			// real engine state), never a fixed sleep guess.
			waitForRowLockWaiter(t, setup, connB.PgConn().PID())

			if err := txA.Commit(ctx); err != nil {
				t.Fatalf("commit txA: %v", err)
			}

			var outcome insertOutcome
			select {
			case outcome = <-outcomeCh:
			case <-time.After(5 * time.Second):
				t.Fatal("connB insert did not unblock after txA committed")
			}
			if outcome.err != nil {
				t.Fatalf("connB insert: %v", outcome.err)
			}

			var searchVectorIsNull bool
			if err := setup.QueryRow(ctx, `SELECT search_vector IS NULL FROM chat.messages WHERE id = $1`, outcome.id).Scan(&searchVectorIsNull); err != nil {
				t.Fatalf("read committed search_vector: %v", err)
			}
			if !searchVectorIsNull {
				t.Fatalf("message inserted concurrently with the channel's %s becoming %q must not be indexed after both commits", tc.column, tc.offValue)
			}
		})
	}
}

func waitForRowLockWaiter(t *testing.T, probe *pgx.Conn, pid uint32) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for ctx.Err() == nil {
		var waiting bool
		if err := probe.QueryRow(ctx, `SELECT EXISTS (
			SELECT 1 FROM pg_locks WHERE pid = $1 AND NOT granted
		)`, pid).Scan(&waiting); err != nil {
			t.Fatalf("inspect row locks for pid %d: %v", pid, err)
		}
		if waiting {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("connection with pid %d did not block on the channel row lock", pid)
}

func messageIsIndexed(t *testing.T, ctx context.Context, conn *pgx.Conn, messageID string) bool {
	t.Helper()
	var indexed bool
	if err := conn.QueryRow(ctx, `SELECT search_vector IS NOT NULL FROM chat.messages WHERE id = $1`, messageID).Scan(&indexed); err != nil {
		t.Fatalf("read search_vector for %s: %v", messageID, err)
	}
	return indexed
}

func messageMatches(t *testing.T, ctx context.Context, conn *pgx.Conn, messageID string, term string) bool {
	t.Helper()
	var matches bool
	if err := conn.QueryRow(ctx, `
		SELECT search_vector @@ plainto_tsquery('portuguese', $2)
		FROM chat.messages WHERE id = $1`, messageID, term).Scan(&matches); err != nil {
		t.Fatalf("match search_vector for %s: %v", messageID, err)
	}
	return matches
}

func searchRank(t *testing.T, ctx context.Context, conn *pgx.Conn, messageID string, term string) float64 {
	t.Helper()
	var rank float64
	if err := conn.QueryRow(ctx, `
		SELECT chat.message_search_rank(search_vector, plainto_tsquery('portuguese', $2), created_at)
		FROM chat.messages WHERE id = $1`, messageID, term).Scan(&rank); err != nil {
		t.Fatalf("rank message %s: %v", messageID, err)
	}
	return rank
}
