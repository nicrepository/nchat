package storage_test

import (
	"context"
	"testing"
	"time"
)

// The lock behaviour of migration 000027/000028 (issue #135, CQ-005).
//
// `ALTER TABLE ... ADD CONSTRAINT ... CHECK` in one statement takes ACCESS
// EXCLUSIVE on chat.messages and holds it for a full sequential scan, so every
// read and write of every message queues behind it for as long as the scan takes.
// Split into `NOT VALID` (a catalogue write, no scan) plus `VALIDATE CONSTRAINT`
// (a scan under SHARE UPDATE EXCLUSIVE), ordinary traffic runs throughout.
//
// This asserts the property that actually matters — that a concurrent writer is
// not blocked during the validating scan — rather than benchmarking it. The
// statement_timeout is what turns "would have blocked" into a failure instead of
// a hang.
//
// Opt-in like its neighbours: needs CHAT_TEST_DATABASE_URL against a _test
// database carrying the real migrations.
func TestLinkSafetyCheckValidationDoesNotBlockWritersPostgreSQL(t *testing.T) {
	ctx := t.Context()
	pool := newLinkScanTestPool(t)

	const (
		workspace = "00000000-0000-0000-0000-000000000001"
		channel   = "e6000000-0000-4000-8000-000000000002"
		author    = "e6000000-0000-4000-8000-000000000003"
	)

	if _, err := pool.Exec(ctx, `
		INSERT INTO auth.users (id, email, display_name)
		VALUES ($1, 'rf21-lock@e.test', 'Lock') ON CONFLICT (id) DO NOTHING`, author); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	for _, seed := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO chat.workspace_members (workspace_id, user_id, status)
		  VALUES ($1, $2, 'active') ON CONFLICT DO NOTHING`, []any{workspace, author}},
		{`INSERT INTO chat.channels (id, workspace_id, slug, display_name, type, status)
		  VALUES ($2, $1, 'rf21-lock', 'RF21 lock', 'public', 'active')
		  ON CONFLICT (id) DO UPDATE SET status = 'active'`, []any{workspace, channel}},
	} {
		if _, err := pool.Exec(ctx, seed.sql, seed.args...); err != nil {
			t.Fatalf("seed workspace: %v", err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM chat.messages WHERE sender_id = $1`, author)
	})

	// A representative population, so the validating scan has something to read.
	if _, err := pool.Exec(ctx, `
		INSERT INTO chat.messages
			(id, workspace_id, channel_id, sender_id, kind, body_text, body_format, status)
		SELECT gen_random_uuid(), $1, $2, $3, 'user', 'lock fixture', 'v2', 'active'
		FROM generate_series(1, 2000)`, workspace, channel, author); err != nil {
		t.Fatalf("seed messages: %v", err)
	}

	// The constraint is already validated by the migrations. Return it to the state
	// 000027 leaves it in, exactly as 000028's down migration does, so 000028's own
	// statement can be exercised against a live workload.
	if _, err := pool.Exec(ctx, `
		ALTER TABLE chat.messages DROP CONSTRAINT messages_link_safety_state_check;
		ALTER TABLE chat.messages
		    ADD CONSTRAINT messages_link_safety_state_check
		    CHECK (link_safety_state IN ('', 'safe', 'inconclusive', 'malicious'))
		    NOT VALID;`); err != nil {
		t.Fatalf("return the constraint to NOT VALID: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`ALTER TABLE chat.messages VALIDATE CONSTRAINT messages_link_safety_state_check`)
	})

	// A writer holding an open transaction against chat.messages for the duration
	// of the validation. If VALIDATE took ACCESS EXCLUSIVE it would queue behind
	// this row lock and then time out.
	writer, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin writer: %v", err)
	}
	defer func() { _ = writer.Rollback(context.Background()) }()
	if _, err := writer.Exec(ctx, `
		INSERT INTO chat.messages
			(id, workspace_id, channel_id, sender_id, kind, body_text, body_format, status)
		VALUES (gen_random_uuid(), $1, $2, $3, 'user', 'concurrent write', 'v2', 'active')`,
		workspace, channel, author); err != nil {
		t.Fatalf("concurrent insert: %v", err)
	}

	// VALIDATE, under a timeout. SHARE UPDATE EXCLUSIVE does not conflict with the
	// writer's ROW EXCLUSIVE, so this completes; ACCESS EXCLUSIVE would not.
	validation, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin validation: %v", err)
	}
	defer func() { _ = validation.Rollback(context.Background()) }()
	if _, err := validation.Exec(ctx, `SET LOCAL lock_timeout = '5s'`); err != nil {
		t.Fatalf("set lock_timeout: %v", err)
	}
	if _, err := validation.Exec(ctx, `SET LOCAL statement_timeout = '30s'`); err != nil {
		t.Fatalf("set statement_timeout: %v", err)
	}

	started := time.Now()
	if _, err := validation.Exec(ctx,
		`ALTER TABLE chat.messages VALIDATE CONSTRAINT messages_link_safety_state_check`); err != nil {
		t.Fatalf("VALIDATE blocked behind a concurrent writer: %v", err)
	}
	elapsed := time.Since(started)

	// The lock actually taken, read from the catalogue rather than assumed.
	var mode string
	if err := validation.QueryRow(ctx, `
		SELECT mode FROM pg_locks
		 WHERE pid = pg_backend_pid()
		   AND relation = 'chat.messages'::regclass
		   AND locktype = 'relation'
		 ORDER BY mode
		 LIMIT 1`).Scan(&mode); err != nil {
		t.Fatalf("read lock mode: %v", err)
	}
	if mode == "AccessExclusiveLock" {
		t.Fatalf("VALIDATE took %s; the split exists precisely to avoid that", mode)
	}
	t.Logf("VALIDATE CONSTRAINT completed in %s holding %s while a writer held an open transaction",
		elapsed, mode)

	if err := validation.Commit(ctx); err != nil {
		t.Fatalf("commit validation: %v", err)
	}

	// And the writer was never disturbed.
	if err := writer.Commit(ctx); err != nil {
		t.Fatalf("the concurrent writer could not commit: %v", err)
	}
}
