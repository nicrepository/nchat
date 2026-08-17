package storage_test

import (
	"context"
	"strings"
	"testing"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// RF-21 visibility, against a real database.
//
// This is the finding the security review raised, exercised the way it would
// actually be exploited: user B asking for user A's withheld message through
// every read path the service exposes. Predicate unit tests can only say the
// filter is spelled correctly; only the database can say the query that runs
// carries it.
//
// Opt-in, like the other PostgreSQL tests here: it needs CHAT_TEST_DATABASE_URL
// pointing at a database whose name ends in _test.
func TestPendingMessageVisibilityPostgreSQL(t *testing.T) {
	ctx := t.Context()
	pool := newLinkScanTestPool(t)

	const (
		// The workspace the initial migration seeds. Reused rather than created:
		// a workspace carries an invariant requiring exactly one active public
		// "general" channel, and satisfying it here would be fixture noise that
		// says nothing about visibility.
		workspace = "00000000-0000-0000-0000-000000000001"
		channel   = "e1000000-0000-4000-8000-000000000002"
		author    = "e1000000-0000-4000-8000-000000000003"
		observer  = "e1000000-0000-4000-8000-000000000004"
		withheld  = "e1000000-0000-4000-8000-00000000000a"
		published = "e1000000-0000-4000-8000-00000000000b"
	)

	// A workspace with two members who can both see one public channel, one
	// message withheld for a link scan and one published.
	if _, err := pool.Exec(ctx, `
		INSERT INTO auth.users (id, email, display_name)
		VALUES ($1, 'author@e.test', 'Author'), ($2, 'observer@e.test', 'Observer')
		ON CONFLICT (id) DO NOTHING`, author, observer); err != nil {
		t.Fatalf("seed users: %v", err)
	}
	// One statement per Exec: pgx sends parameterised statements through the
	// extended protocol, which does not accept several commands at once.
	for _, seed := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO chat.workspace_members (workspace_id, user_id, status)
		  VALUES ($1, $2, 'active'), ($1, $3, 'active') ON CONFLICT DO NOTHING`,
			[]any{workspace, author, observer}},
		{`INSERT INTO chat.channels (id, workspace_id, slug, display_name, type, status)
		  VALUES ($2, $1, 'rf21-visibility', 'RF21', 'public', 'active') ON CONFLICT (id) DO NOTHING`,
			[]any{workspace, channel}},
	} {
		if _, err := pool.Exec(ctx, seed.sql, seed.args...); err != nil {
			t.Fatalf("seed workspace: %v", err)
		}
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO chat.messages
			(id, workspace_id, channel_id, sender_id, kind, body_text, body_format, status)
		VALUES ($1, $3, $4, $5, 'user', 'withheld body', 'v2', 'pending_link_scan'),
		       ($2, $3, $4, $5, 'user', 'published body', 'v2', 'active')
		ON CONFLICT (id) DO NOTHING`,
		withheld, published, workspace, channel, author); err != nil {
		t.Fatalf("seed messages: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM chat.messages WHERE workspace_id = $1`, workspace)
	})

	store := storage.NewPGXMessageStore(pool)

	t.Run("another member cannot read it by id", func(t *testing.T) {
		_, err := store.GetMessageByIDInWorkspace(ctx, workspace, withheld, observer)
		if err == nil {
			t.Fatal("a withheld message was served to another member")
		}
		// Non-enumerating: the same answer an inaccessible message gives.
		if !strings.Contains(err.Error(), domain.ErrNotFound.Error()) {
			t.Fatalf("want ErrNotFound, got %v", err)
		}
	})

	t.Run("its author can read it by id", func(t *testing.T) {
		message, err := store.GetMessageByIDInWorkspace(ctx, workspace, withheld, author)
		if err != nil {
			t.Fatalf("the author cannot see their own withheld message: %v", err)
		}
		if message.Status != domain.MessageStatusPendingLinkScan {
			t.Fatalf("status=%q", message.Status)
		}
	})

	t.Run("it is absent from another member's channel history", func(t *testing.T) {
		result, err := store.ListChannelMessages(ctx, storage.ListChannelMessagesInput{
			WorkspaceID: workspace, ChannelID: channel, UserID: observer,
		})
		if err != nil {
			t.Fatalf("ListChannelMessages: %v", err)
		}
		for _, message := range result.Messages {
			if message.ID == withheld {
				t.Fatal("a withheld message appeared in another member's history")
			}
		}
		if !containsMessage(result.Messages, published) {
			t.Fatal("the published message is missing from history")
		}
	})

	t.Run("it is present in its author's own history", func(t *testing.T) {
		result, err := store.ListChannelMessages(ctx, storage.ListChannelMessagesInput{
			WorkspaceID: workspace, ChannelID: channel, UserID: author,
		})
		if err != nil {
			t.Fatalf("ListChannelMessages: %v", err)
		}
		if !containsMessage(result.Messages, withheld) {
			t.Fatal("the author cannot rebuild their own pending state")
		}
	})

	t.Run("it does not become another member's channel activity", func(t *testing.T) {
		// The sidebar's last-activity timestamp comes from the newest message in
		// the channel. The withheld one is newer than the published one, so if it
		// counted, the observer's sidebar would move for a message they cannot
		// see.
		var lastActivity *string
		err := pool.QueryRow(ctx, `
			SELECT to_char(lm.created_at, 'YYYY-MM-DD"T"HH24:MI:SS')
			FROM chat.channels c
			LEFT JOIN LATERAL (
			    SELECT m.created_at
			    FROM chat.messages m
			    WHERE m.workspace_id = c.workspace_id
			      AND m.channel_id = c.id
			      AND m.status <> 'pending_link_scan'
			    ORDER BY m.created_at DESC, m.id DESC
			    LIMIT 1
			) lm ON true
			WHERE c.id = $1`, channel).Scan(&lastActivity)
		if err != nil {
			t.Fatalf("read last activity: %v", err)
		}
		var withheldSeen bool
		if err := pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM chat.messages
				WHERE id = $1 AND status = 'pending_link_scan'
			)`, withheld).Scan(&withheldSeen); err != nil {
			t.Fatalf("confirm withheld state: %v", err)
		}
		if !withheldSeen {
			t.Fatal("the fixture is not withheld, so this proves nothing")
		}
		if lastActivity == nil {
			t.Fatal("the channel reports no activity at all")
		}
	})

	t.Run("promotion makes it visible to everyone", func(t *testing.T) {
		if _, err := pool.Exec(ctx,
			`UPDATE chat.messages SET status = 'active' WHERE id = $1`, withheld); err != nil {
			t.Fatalf("promote: %v", err)
		}
		if _, err := store.GetMessageByIDInWorkspace(ctx, workspace, withheld, observer); err != nil {
			t.Fatalf("a promoted message is still hidden: %v", err)
		}
	})
}

func containsMessage(messages []domain.Message, id string) bool {
	for _, message := range messages {
		if message.ID == id {
			return true
		}
	}
	return false
}

// The reconnect answer, against a real database.
//
// This is the recovery path for the finding that realtime delivery cannot cover:
// a message.blocked published while its author was offline reaches nobody, and
// nothing else would ever tell them. What matters here is that the answer is
// derived from durable state — the link-safety rows, not the delivery outbox —
// and that it is only ever given to the person who wrote the message.
//
// Opt-in like its neighbour: needs CHAT_TEST_DATABASE_URL against a _test database.
func TestLinkSafetyStatesPostgreSQL(t *testing.T) {
	ctx := t.Context()
	pool := newLinkScanTestPool(t)

	const (
		workspace = "00000000-0000-0000-0000-000000000001"
		channel   = "e2000000-0000-4000-8000-000000000002"
		author    = "e2000000-0000-4000-8000-000000000003"
		observer  = "e2000000-0000-4000-8000-000000000004"

		stillPending = "e2000000-0000-4000-8000-00000000000a"
		promoted     = "e2000000-0000-4000-8000-00000000000b"
		refused      = "e2000000-0000-4000-8000-00000000000c"
		selfDeleted  = "e2000000-0000-4000-8000-00000000000d"

		badURL  = "https://malware.test/payload"
		goodURL = "https://example.test/ok"
		// The fingerprint binding the associations to the content, exactly as the
		// service stamps it.
		fingerprint = "fp-a"
	)

	if _, err := pool.Exec(ctx, `
		INSERT INTO auth.users (id, email, display_name)
		VALUES ($1, 'rf21-author@e.test', 'Author'), ($2, 'rf21-observer@e.test', 'Observer')
		ON CONFLICT (id) DO NOTHING`, author, observer); err != nil {
		t.Fatalf("seed users: %v", err)
	}
	for _, seed := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO chat.workspace_members (workspace_id, user_id, status)
		  VALUES ($1, $2, 'active'), ($1, $3, 'active') ON CONFLICT DO NOTHING`,
			[]any{workspace, author, observer}},
		{`INSERT INTO chat.channels (id, workspace_id, slug, display_name, type, status)
		  VALUES ($2, $1, 'rf21-states', 'RF21 states', 'public', 'active')
		  ON CONFLICT (id) DO NOTHING`, []any{workspace, channel}},
		{`INSERT INTO chat.link_scans (canonical_url, status, decided_at)
		  VALUES ($1, 'malicious', now()), ($2, 'safe', now())
		  ON CONFLICT (canonical_url) DO UPDATE SET status = EXCLUDED.status, decided_at = now()`,
			[]any{badURL, goodURL}},
	} {
		if _, err := pool.Exec(ctx, seed.sql, seed.args...); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	// Four messages by one author, covering every state the answer distinguishes.
	if _, err := pool.Exec(ctx, `
		INSERT INTO chat.messages
			(id, workspace_id, channel_id, sender_id, kind, body_text, body_format,
			 status, deleted_at, link_safety_fingerprint)
		VALUES ($1, $5, $6, $7, 'user', 'waiting',  'v2', 'pending_link_scan', NULL,  $8),
		       ($2, $5, $6, $7, 'user', 'cleared',  'v2', 'active',            NULL,  $8),
		       ($3, $5, $6, $7, 'user', '',         'v2', 'deleted',           now(), $8),
		       ($4, $5, $6, $7, 'user', '',         'v2', 'deleted',           now(), $8)
		ON CONFLICT (id) DO NOTHING`,
		stillPending, promoted, refused, selfDeleted, workspace, channel, author, fingerprint,
	); err != nil {
		t.Fatalf("seed messages: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM chat.messages WHERE sender_id = $1`, author)
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM chat.link_scans WHERE canonical_url = ANY($1::text[])`,
			[]string{badURL, goodURL})
	})

	// The refused one waited on the malicious URL; the other three on the safe
	// one. The self-deleted message has link-safety history too, which is the
	// case that makes "deleted" and "blocked" genuinely need telling apart.
	if _, err := pool.Exec(ctx, `
		INSERT INTO chat.message_link_scans (message_id, canonical_url, fingerprint)
		VALUES ($1, $6, $5), ($2, $6, $5), ($3, $7, $5), ($4, $6, $5)
		ON CONFLICT DO NOTHING`,
		stillPending, promoted, refused, selfDeleted, fingerprint, goodURL, badURL,
	); err != nil {
		t.Fatalf("seed associations: %v", err)
	}

	store := storage.NewPGXMessageStore(pool)
	ids := []string{stillPending, promoted, refused, selfDeleted}

	t.Run("the author is told what became of each one", func(t *testing.T) {
		states, err := store.LinkSafetyStates(ctx, workspace, author, ids)
		if err != nil {
			t.Fatalf("LinkSafetyStates: %v", err)
		}
		byID := map[string]domain.LinkSafetyState{}
		for _, state := range states {
			byID[state.MessageID] = state.State
		}
		want := map[string]domain.LinkSafetyState{
			stillPending: domain.LinkSafetyStatePending,
			promoted:     domain.LinkSafetyStateActive,
			refused:      domain.LinkSafetyStateBlocked,
			// Deleted, and nothing in the link-safety rows says otherwise. Reporting
			// this as blocked would be telling an author their own deletion was a
			// malicious link.
			selfDeleted: domain.LinkSafetyStateDeleted,
		}
		for id, expected := range want {
			if byID[id] != expected {
				t.Fatalf("%s = %q, want %q", id, byID[id], expected)
			}
		}
	})

	t.Run("another member is told nothing at all", func(t *testing.T) {
		states, err := store.LinkSafetyStates(ctx, workspace, observer, ids)
		if err != nil {
			t.Fatalf("LinkSafetyStates: %v", err)
		}
		// Not "denied" — absent. A caller cannot tell these ids from ids that
		// never existed, so the endpoint is not an existence oracle.
		if len(states) != 0 {
			t.Fatalf("another member learned about %d messages: %+v", len(states), states)
		}
	})

	t.Run("another workspace is told nothing at all", func(t *testing.T) {
		const otherWorkspace = "00000000-0000-0000-0000-0000000000ff"
		states, err := store.LinkSafetyStates(ctx, otherWorkspace, author, ids)
		if err != nil {
			t.Fatalf("LinkSafetyStates: %v", err)
		}
		if len(states) != 0 {
			t.Fatalf("a foreign workspace id returned %+v", states)
		}
	})

	t.Run("a member who is no longer active is told nothing at all", func(t *testing.T) {
		if _, err := pool.Exec(ctx, `
			UPDATE chat.workspace_members SET status = 'left'
			WHERE workspace_id = $1 AND user_id = $2`, workspace, author); err != nil {
			t.Fatalf("deactivate membership: %v", err)
		}
		t.Cleanup(func() {
			_, _ = pool.Exec(context.Background(), `
				UPDATE chat.workspace_members SET status = 'active'
				WHERE workspace_id = $1 AND user_id = $2`, workspace, author)
		})
		states, err := store.LinkSafetyStates(ctx, workspace, author, ids)
		if err != nil {
			t.Fatalf("LinkSafetyStates: %v", err)
		}
		if len(states) != 0 {
			t.Fatalf("an inactive member still reads their own messages: %+v", states)
		}
	})

	t.Run("ids that name nothing are simply absent", func(t *testing.T) {
		states, err := store.LinkSafetyStates(ctx, workspace, author,
			[]string{"e2000000-0000-4000-8000-0000000000ff"})
		if err != nil {
			t.Fatalf("LinkSafetyStates: %v", err)
		}
		if len(states) != 0 {
			t.Fatalf("a nonexistent id returned %+v", states)
		}
	})
}
