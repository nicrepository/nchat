package storage_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nicrepository/nchat/libs/go/platform/urlsafety"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// Editing a message and the link-safety facts about it, and withholding a
// condemned body from every projection (issue #135, CQ-001 and CQ-002).
//
// These need a real database. Both findings are about what a *statement* leaves
// behind — one transaction's atomicity, and four read queries that must all agree
// — and a mock would agree with whatever the Go around it said.
//
// Opt-in like its neighbours: needs CHAT_TEST_DATABASE_URL against a _test
// database carrying the real migrations.

const (
	editWorkspace = "00000000-0000-0000-0000-000000000001"
	editChannel   = "e6000000-0000-4000-8000-000000000002"
	editAuthor    = "e6000000-0000-4000-8000-000000000003"

	editMessage  = "e6000000-0000-4000-8000-00000000000a"
	editMessage2 = "e6000000-0000-4000-8000-00000000000d"
	quoteMessage = "e6000000-0000-4000-8000-00000000000b"
	refMessage   = "e6000000-0000-4000-8000-00000000000c"

	urlOld = "https://edit.example/old"
	urlNew = "https://edit.example/new"
)

type editFixture struct {
	pool  *pgxpool.Pool
	store *storage.PGXMessageStore
	ctx   context.Context
}

// TestEditMessageRechecksLockedLinkRowsPostgreSQL reproduces the edit versus
// reconciliation TOCTOU with two real transactions. The reconciliation owns the
// scan row but has not committed yet; EditMessage must lock that row, wait, then
// re-read the committed malicious state instead of publishing the stale
// inconclusive classification it received from the service.
func TestEditMessageRechecksLockedLinkRowsPostgreSQL(t *testing.T) {
	f := newEditFixture(t)
	f.reset(t)
	f.verdict(t, urlOld, "safe")
	f.verdict(t, urlNew, "inconclusive")
	f.seedMessage(t, editMessage, "veja "+urlOld, "safe", "fp-old", urlOld)

	reconcileTx, err := f.pool.Begin(f.ctx)
	if err != nil {
		t.Fatalf("begin reconcile: %v", err)
	}
	defer func() { _ = reconcileTx.Rollback(context.Background()) }()
	if _, err := reconcileTx.Exec(f.ctx, `
		UPDATE chat.link_scans SET status = 'malicious', decided_at = now()
		WHERE canonical_url = $1 AND status = 'inconclusive'`, urlNew); err != nil {
		t.Fatalf("hold malicious reconciliation: %v", err)
	}

	editDone := make(chan error, 1)
	go func() {
		_, editErr := f.store.EditMessage(f.ctx, storage.EditMessageInput{
			WorkspaceID:           editWorkspace,
			MessageID:             editMessage,
			EditorID:              editAuthor,
			Body:                  "veja " + urlNew,
			BodyFormat:            "v2",
			LinkSafetyState:       domain.MessageLinkSafetyInconclusive,
			LinkSafetyFingerprint: "fp-new",
			LinkScanURLs:          []string{urlNew},
		})
		editDone <- editErr
	}()

	// Wait until EditMessage owns the message row. With the broken unlocked
	// recheck it has already read the old inconclusive version by this point; with
	// the fixed recheck it is blocked acquiring the scan row.
	deadline := time.Now().Add(3 * time.Second)
	for {
		select {
		case err := <-editDone:
			if err == nil {
				t.Fatal("EditMessage committed from an unlocked stale inconclusive read")
			}
			t.Fatalf("EditMessage returned before the concurrent verdict committed: %v", err)
		default:
		}
		var one int
		err := f.pool.QueryRow(f.ctx, `
			SELECT 1 FROM chat.messages WHERE id = $1::uuid FOR UPDATE NOWAIT`, editMessage,
		).Scan(&one)
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "55P03" {
			break
		}
		if err != nil {
			t.Fatalf("probe edit message lock: %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatal("EditMessage did not acquire the message lock")
		}
		time.Sleep(10 * time.Millisecond)
	}

	if err := reconcileTx.Commit(f.ctx); err != nil {
		t.Fatalf("commit reconciliation: %v", err)
	}
	select {
	case err := <-editDone:
		if !errors.Is(err, domain.ErrMaliciousURL) {
			t.Fatalf("EditMessage error = %v, want malicious URL refusal", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("EditMessage deadlocked after reconciliation committed")
	}

	var body, state string
	if err := f.pool.QueryRow(f.ctx, `
		SELECT body_text, link_safety_state FROM chat.messages WHERE id = $1::uuid`, editMessage,
	).Scan(&body, &state); err != nil {
		t.Fatalf("read final message: %v", err)
	}
	if body != "veja "+urlOld || state != "safe" {
		t.Fatalf("unsafe edit committed: body=%q state=%q", body, state)
	}
}

// TestEditWinsThenReconcileConvergesPostgreSQL uses a database advisory-lock
// barrier in a trigger to pause the real EditMessage after it owns the message
// and both scan rows. Reconciliation then queues behind the scan lock; once the
// edit commits, it must see the new associations and converge the message.
func TestEditWinsThenReconcileConvergesPostgreSQL(t *testing.T) {
	f := newEditFixture(t)
	f.reset(t)
	f.verdict(t, urlOld, "inconclusive")
	f.verdict(t, urlNew, "inconclusive")
	f.seedMessage(t, editMessage, "sem links", "", "")

	const advisoryKey = 13500201
	gate, err := f.pool.Acquire(f.ctx)
	if err != nil {
		t.Fatalf("acquire barrier connection: %v", err)
	}
	t.Cleanup(func() {
		background := context.Background()
		_, _ = gate.Exec(background, `SELECT pg_advisory_unlock($1)`, advisoryKey)
		gate.Release()
		_, _ = f.pool.Exec(background, `DROP TRIGGER IF EXISTS cq135_pause_message_edit ON chat.messages`)
		_, _ = f.pool.Exec(background, `DROP FUNCTION IF EXISTS chat.cq135_pause_message_edit()`)
		_, _ = f.pool.Exec(background,
			`DELETE FROM files.link_fetch_denylist WHERE url_digest = $1`, urlsafety.URLDigest(urlNew))
	})
	if _, err := gate.Exec(f.ctx, `SELECT pg_advisory_lock($1)`, advisoryKey); err != nil {
		t.Fatalf("lock edit barrier: %v", err)
	}
	if _, err := f.pool.Exec(f.ctx, `
		DROP TRIGGER IF EXISTS cq135_pause_message_edit ON chat.messages;
		CREATE OR REPLACE FUNCTION chat.cq135_pause_message_edit() RETURNS trigger
		LANGUAGE plpgsql AS $$
		BEGIN
		  IF NEW.id = 'e6000000-0000-4000-8000-00000000000a'::uuid
		     AND NEW.edit_count > OLD.edit_count THEN
		    PERFORM pg_advisory_xact_lock(13500201);
		  END IF;
		  RETURN NEW;
		END $$;
		CREATE TRIGGER cq135_pause_message_edit
		BEFORE UPDATE ON chat.messages
		FOR EACH ROW EXECUTE FUNCTION chat.cq135_pause_message_edit()`); err != nil {
		t.Fatalf("install edit barrier: %v", err)
	}

	editDone := make(chan error, 1)
	go func() {
		_, editErr := f.store.EditMessage(f.ctx, storage.EditMessageInput{
			WorkspaceID: editWorkspace, MessageID: editMessage, EditorID: editAuthor,
			Body: urlOld + " e " + urlNew, BodyFormat: "v2",
			LinkSafetyState:       domain.MessageLinkSafetyInconclusive,
			LinkSafetyFingerprint: "fp-two", LinkScanURLs: []string{urlNew, urlOld},
		})
		editDone <- editErr
	}()
	waitForCQ135AdvisoryWaiter(t, f.pool, advisoryKey)

	reconcileDone := make(chan error, 1)
	go func() {
		err := f.store.ReconcileLinkVerdict(f.ctx, urlNew, "scan-edit",
			urlsafety.ScanEvidence{Verdict: urlsafety.VerdictMalicious, ObservedAt: time.Now()})
		if err == nil {
			for {
				changes, refreshErr := f.store.RefreshMessageLinkSafety(f.ctx, urlNew)
				if refreshErr != nil {
					err = refreshErr
					break
				}
				if len(changes) == 0 {
					break
				}
			}
		}
		reconcileDone <- err
	}()
	waitForCQ135BlockedLinkScanUpdate(t, f.pool)

	if _, err := gate.Exec(f.ctx, `SELECT pg_advisory_unlock($1)`, advisoryKey); err != nil {
		t.Fatalf("release edit barrier: %v", err)
	}
	for name, done := range map[string]<-chan error{"edit": editDone, "reconcile": reconcileDone} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s deadlocked", name)
		}
	}

	var body, state string
	if err := f.pool.QueryRow(f.ctx, `
		SELECT body_text, link_safety_state FROM chat.messages WHERE id = $1::uuid`, editMessage,
	).Scan(&body, &state); err != nil {
		t.Fatalf("read converged edit: %v", err)
	}
	if body != urlOld+" e "+urlNew || state != "malicious" {
		t.Fatalf("body=%q state=%q, want committed edit converged to malicious", body, state)
	}
	if got := f.associations(t, editMessage); len(got) != 2 || got[0] != urlNew || got[1] != urlOld {
		t.Fatalf("associations = %q, want both edited URLs", got)
	}
}

// TestEditRemovingURLRejectsStaleRefreshPostgreSQL covers the other side of
// the edit/reconcile race: RefreshMessageLinkSafety snapshots the old
// association, then waits for an edit which removes that URL. Once unblocked it
// must not apply the stale malicious fold to the new link-free body.
func TestEditRemovingURLRejectsStaleRefreshPostgreSQL(t *testing.T) {
	f := newEditFixture(t)
	f.reset(t)
	f.verdict(t, urlOld, "inconclusive")
	f.seedMessage(t, editMessage, "veja "+urlOld, "inconclusive", "fp-old", urlOld)

	const advisoryKey = 13500202
	gate, err := f.pool.Acquire(f.ctx)
	if err != nil {
		t.Fatalf("acquire barrier connection: %v", err)
	}
	t.Cleanup(func() {
		background := context.Background()
		_, _ = gate.Exec(background, `SELECT pg_advisory_unlock($1)`, advisoryKey)
		gate.Release()
		_, _ = f.pool.Exec(background, `DROP TRIGGER IF EXISTS cq135_pause_url_removal ON chat.messages`)
		_, _ = f.pool.Exec(background, `DROP FUNCTION IF EXISTS chat.cq135_pause_url_removal()`)
		_, _ = f.pool.Exec(background,
			`DELETE FROM files.link_fetch_denylist WHERE url_digest = $1`, urlsafety.URLDigest(urlOld))
	})
	if _, err := gate.Exec(f.ctx, `SELECT pg_advisory_lock($1)`, advisoryKey); err != nil {
		t.Fatalf("lock edit barrier: %v", err)
	}
	if _, err := f.pool.Exec(f.ctx, `
		DROP TRIGGER IF EXISTS cq135_pause_url_removal ON chat.messages;
		CREATE OR REPLACE FUNCTION chat.cq135_pause_url_removal() RETURNS trigger
		LANGUAGE plpgsql AS $$
		BEGIN
		  IF NEW.id = 'e6000000-0000-4000-8000-00000000000a'::uuid
		     AND NEW.edit_count > OLD.edit_count THEN
		    PERFORM pg_advisory_xact_lock(13500202);
		  END IF;
		  RETURN NEW;
		END $$;
		CREATE TRIGGER cq135_pause_url_removal
		BEFORE UPDATE ON chat.messages
		FOR EACH ROW EXECUTE FUNCTION chat.cq135_pause_url_removal()`); err != nil {
		t.Fatalf("install edit barrier: %v", err)
	}

	editDone := make(chan error, 1)
	go func() {
		_, editErr := f.store.EditMessage(f.ctx, storage.EditMessageInput{
			WorkspaceID: editWorkspace, MessageID: editMessage, EditorID: editAuthor,
			Body: "sem link nenhum", BodyFormat: "v2",
			LinkSafetyState: domain.MessageLinkSafetyNone,
		})
		editDone <- editErr
	}()
	waitForCQ135AdvisoryWaiter(t, f.pool, advisoryKey)

	if err := f.store.ReconcileLinkVerdict(f.ctx, urlOld, "scan-edit",
		urlsafety.ScanEvidence{Verdict: urlsafety.VerdictMalicious, ObservedAt: time.Now()}); err != nil {
		t.Fatalf("reconcile old URL: %v", err)
	}
	type refreshResult struct {
		changes []storage.MessageLinkSafetyChange
		err     error
	}
	refreshDone := make(chan refreshResult, 1)
	go func() {
		changes, refreshErr := f.store.RefreshMessageLinkSafety(f.ctx, urlOld)
		refreshDone <- refreshResult{changes: changes, err: refreshErr}
	}()
	waitForCQ135DatabaseCondition(t, f.pool, `
		SELECT EXISTS (
		  SELECT 1 FROM pg_stat_activity
		   WHERE datname = current_database()
		     AND pid <> pg_backend_pid()
		     AND wait_event_type = 'Lock'
		     AND query LIKE '%WITH candidate AS%'
		)`)

	if _, err := gate.Exec(f.ctx, `SELECT pg_advisory_unlock($1)`, advisoryKey); err != nil {
		t.Fatalf("release edit barrier: %v", err)
	}
	select {
	case err := <-editDone:
		if err != nil {
			t.Fatalf("edit: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("edit deadlocked")
	}
	select {
	case result := <-refreshDone:
		if result.err != nil {
			t.Fatalf("refresh: %v", result.err)
		}
		if len(result.changes) != 0 {
			t.Fatalf("stale refresh published changes: %+v", result.changes)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("refresh deadlocked")
	}

	var body, state, fingerprint string
	if err := f.pool.QueryRow(f.ctx, `
		SELECT body_text, link_safety_state, COALESCE(link_safety_fingerprint, '')
		FROM chat.messages WHERE id = $1::uuid`, editMessage,
	).Scan(&body, &state, &fingerprint); err != nil {
		t.Fatalf("read edited message: %v", err)
	}
	if body != "sem link nenhum" || state != "" || fingerprint != "" {
		t.Fatalf("body=%q state=%q fingerprint=%q, want link-free edit", body, state, fingerprint)
	}
	if got := f.associations(t, editMessage); len(got) != 0 {
		t.Fatalf("associations = %q, want none", got)
	}
}

// TestTwoURLConcurrentEditsUseStableLockOrderPostgreSQL starts two real edits
// with the same scan rows supplied in opposite orders. Both must finish before
// the timeout; ORDER BY canonical_url makes their row-lock order identical.
func TestTwoURLConcurrentEditsUseStableLockOrderPostgreSQL(t *testing.T) {
	f := newEditFixture(t)
	f.reset(t)
	f.verdict(t, urlOld, "safe")
	f.verdict(t, urlNew, "inconclusive")
	f.seedMessage(t, editMessage, "primeira", "", "")
	f.seedMessage(t, editMessage2, "segunda", "", "")

	start := make(chan struct{})
	done := make(chan error, 2)
	for _, input := range []storage.EditMessageInput{
		{WorkspaceID: editWorkspace, MessageID: editMessage, EditorID: editAuthor,
			Body: urlOld + " " + urlNew, BodyFormat: "v2",
			LinkSafetyState:       domain.MessageLinkSafetyInconclusive,
			LinkSafetyFingerprint: "fp-one", LinkScanURLs: []string{urlOld, urlNew}},
		{WorkspaceID: editWorkspace, MessageID: editMessage2, EditorID: editAuthor,
			Body: urlNew + " " + urlOld, BodyFormat: "v2",
			LinkSafetyState:       domain.MessageLinkSafetyInconclusive,
			LinkSafetyFingerprint: "fp-two", LinkScanURLs: []string{urlNew, urlOld}},
	} {
		input := input
		go func() {
			<-start
			_, err := f.store.EditMessage(f.ctx, input)
			done <- err
		}()
	}
	close(start)
	for range 2 {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("concurrent edit: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("opposite two-URL edits deadlocked")
		}
	}
}

func TestMessageSecuritySnapshotsAreOneAuthorizedProjectionPostgreSQL(t *testing.T) {
	f := newEditFixture(t)
	f.reset(t)
	f.seedMessage(t, editMessage, "", "malicious", "")
	if _, err := f.pool.Exec(f.ctx, `
		INSERT INTO chat.messages
			(id, workspace_id, channel_id, sender_id, kind, body_text, body_format,
			 status, parent_message_id, link_safety_state)
		VALUES ($1, $2, $3, $4, 'user', 'quote', 'v2', 'active', $5, 'inconclusive')`,
		quoteMessage, editWorkspace, editChannel, editAuthor, editMessage); err != nil {
		t.Fatalf("seed quoted message: %v", err)
	}
	missing := "e6000000-0000-4000-8000-000000000099"

	snapshots, err := f.store.ListMessageSecuritySnapshots(
		f.ctx, editWorkspace, editAuthor, editChannel, "", []string{quoteMessage, missing},
	)
	if err != nil {
		t.Fatalf("ListMessageSecuritySnapshots: %v", err)
	}
	if len(snapshots) != 2 || !snapshots[0].Available || snapshots[1].Available {
		t.Fatalf("snapshots = %+v", snapshots)
	}
	if snapshots[0].Status != domain.MessageStatusActive ||
		snapshots[0].LinkSafetyState != domain.MessageLinkSafetyInconclusive ||
		snapshots[0].UpdatedAt.IsZero() ||
		snapshots[0].Quoted == nil ||
		snapshots[0].Quoted.MessageID != editMessage ||
		snapshots[0].Quoted.LinkSafetyState != domain.MessageLinkSafetyMalicious ||
		snapshots[0].Quoted.UpdatedAt.IsZero() {
		t.Fatalf("authorized security projection = %+v", snapshots[0])
	}
	if snapshots[1].MessageID != missing || snapshots[1].Status != "" ||
		snapshots[1].LinkSafetyState != "" || snapshots[1].Quoted != nil {
		t.Fatalf("unavailable sentinel leaked metadata: %+v", snapshots[1])
	}
}

func waitForCQ135AdvisoryWaiter(t *testing.T, pool *pgxpool.Pool, key int) {
	t.Helper()
	waitForCQ135DatabaseCondition(t, pool, `
		SELECT EXISTS (
		  SELECT 1 FROM pg_locks
		   WHERE locktype = 'advisory' AND objid = $1::oid AND NOT granted
		)`, key)
}

func waitForCQ135BlockedLinkScanUpdate(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	waitForCQ135DatabaseCondition(t, pool, `
		SELECT EXISTS (
		  SELECT 1 FROM pg_stat_activity
		   WHERE datname = current_database()
		     AND pid <> pg_backend_pid()
		     AND wait_event_type = 'Lock'
		     AND query LIKE '%UPDATE chat.link_scans%'
		)`)
}

func waitForCQ135DatabaseCondition(t *testing.T, pool *pgxpool.Pool, query string, args ...any) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		var ready bool
		if err := pool.QueryRow(t.Context(), query, args...).Scan(&ready); err != nil {
			t.Fatalf("wait for database barrier: %v", err)
		}
		if ready {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("database barrier was not reached")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func newEditFixture(t *testing.T) editFixture {
	t.Helper()
	ctx := t.Context()
	pool := newLinkScanTestPool(t)

	for _, seed := range []struct {
		what string
		sql  string
		args []any
	}{
		{"user", `INSERT INTO auth.users (id, email, display_name)
		  VALUES ($1, 'rf21-edit@e.test', 'Editor') ON CONFLICT (id) DO NOTHING`,
			[]any{editAuthor}},
		{"membership", `INSERT INTO chat.workspace_members (workspace_id, user_id, status)
		  VALUES ($1, $2, 'active') ON CONFLICT DO NOTHING`,
			[]any{editWorkspace, editAuthor}},
		{"channel", `INSERT INTO chat.channels (id, workspace_id, slug, display_name, type, status)
		  VALUES ($2, $1, 'rf21-edit', 'RF21 edit', 'private', 'active')
		  ON CONFLICT (id) DO UPDATE SET status = 'active'`,
			[]any{editWorkspace, editChannel}},
		{"channel member", `INSERT INTO chat.channel_members (channel_id, user_id)
		  VALUES ($1, $2) ON CONFLICT DO NOTHING`,
			[]any{editChannel, editAuthor}},
	} {
		if _, err := pool.Exec(ctx, seed.sql, seed.args...); err != nil {
			t.Fatalf("seed %s: %v", seed.what, err)
		}
	}
	t.Cleanup(func() {
		background := context.Background()
		_, _ = pool.Exec(background, `DELETE FROM chat.messages WHERE sender_id = $1`, editAuthor)
		_, _ = pool.Exec(background,
			`DELETE FROM chat.link_scans WHERE canonical_url = ANY($1::text[])`,
			[]string{urlOld, urlNew})
	})
	return editFixture{pool: pool, store: storage.NewPGXMessageStore(pool), ctx: ctx}
}

func (f editFixture) reset(t *testing.T) {
	t.Helper()
	for _, statement := range []string{
		`DELETE FROM chat.messages WHERE sender_id = $1`,
	} {
		if _, err := f.pool.Exec(f.ctx, statement, editAuthor); err != nil {
			t.Fatalf("reset messages: %v", err)
		}
	}
	if _, err := f.pool.Exec(f.ctx,
		`DELETE FROM chat.link_scans WHERE canonical_url = ANY($1::text[])`,
		[]string{urlOld, urlNew}); err != nil {
		t.Fatalf("reset scans: %v", err)
	}
}

// verdict writes one decided link_scans row. Status "" leaves the URL unscanned,
// which is how the "still pending" cases are set up.
func (f editFixture) verdict(t *testing.T, url, status string) {
	t.Helper()
	if status == "" {
		return
	}
	if _, err := f.pool.Exec(f.ctx, `
		INSERT INTO chat.link_scans (canonical_url, status, scan_uuid, decided_at)
		VALUES ($1, $2, 'scan-edit', now())
		ON CONFLICT (canonical_url) DO UPDATE
		   SET status = EXCLUDED.status, decided_at = now()`, url, status); err != nil {
		t.Fatalf("seed verdict %s=%s: %v", url, status, err)
	}
}

func (f editFixture) seedMessage(
	t *testing.T, id, body, linkSafety, fingerprint string, urls ...string,
) {
	t.Helper()
	if _, err := f.pool.Exec(f.ctx, `
		INSERT INTO chat.messages
			(id, workspace_id, channel_id, sender_id, kind, body_text, body_format,
			 status, link_safety_state, link_safety_fingerprint,
			 link_safety_projection_version)
		VALUES ($1, $2, $3, $4, 'user', $5, 'v2', 'active', $6, $7, 1)`,
		id, editWorkspace, editChannel, editAuthor, body, linkSafety, fingerprint); err != nil {
		t.Fatalf("seed message %s: %v", id, err)
	}
	for _, url := range urls {
		if _, err := f.pool.Exec(f.ctx, `
			INSERT INTO chat.message_link_scans (message_id, canonical_url, fingerprint)
			VALUES ($1, $2, $3)`, id, url, fingerprint); err != nil {
			t.Fatalf("seed association %s/%s: %v", id, url, err)
		}
	}
}

// associations is what the message is currently claimed to be about, straight
// from the table the reconciliation pass reads.
func (f editFixture) associations(t *testing.T, id string) []string {
	t.Helper()
	rows, err := f.pool.Query(f.ctx,
		`SELECT mls.canonical_url
		 FROM chat.message_link_scans mls
		 JOIN chat.messages m ON m.id = mls.message_id
		 WHERE mls.message_id = $1::uuid
		   AND mls.fingerprint = COALESCE(m.link_safety_fingerprint, '')
		 ORDER BY mls.canonical_url`, id)
	if err != nil {
		t.Fatalf("read associations: %v", err)
	}
	defer rows.Close()
	var urls []string
	for rows.Next() {
		var url string
		if err := rows.Scan(&url); err != nil {
			t.Fatalf("scan association: %v", err)
		}
		urls = append(urls, url)
	}
	return urls
}

func (f editFixture) storedState(t *testing.T, id string) (state, fingerprint string) {
	t.Helper()
	if err := f.pool.QueryRow(f.ctx, `
		SELECT link_safety_state, COALESCE(link_safety_fingerprint, '')
		  FROM chat.messages WHERE id = $1::uuid`, id).Scan(&state, &fingerprint); err != nil {
		t.Fatalf("read stored state: %v", err)
	}
	return state, fingerprint
}

func (f editFixture) edit(
	t *testing.T, body string, state domain.MessageLinkSafety, urls []string,
) (domain.Message, error) {
	t.Helper()
	return f.store.EditMessage(f.ctx, storage.EditMessageInput{
		WorkspaceID:           editWorkspace,
		MessageID:             editMessage,
		EditorID:              editAuthor,
		Body:                  body,
		BodyFormat:            "v2",
		LinkSafetyState:       state,
		LinkSafetyFingerprint: "fp-new",
		LinkScanURLs:          urls,
	})
}

// TestEditMessageIsAtomicWithLinkSafetyPostgreSQL is the CQ-001 proof.
//
// # What was wrong
//
// EditMessage updated the body, the format and the edit bookkeeping, and left
// link_safety_state, link_safety_fingerprint and chat.message_link_scans exactly
// as they were. So a message could carry a *new* body while every link-safety
// fact about it still described the *old* one: the URL the reader now sees was
// never associated with the message, and the URL that decides the message's
// state is no longer in it.
//
// # The invariant
//
// After a committed EditMessage, every link-safety fact about the message
// describes the new body and only the new body.
func TestEditMessageIsAtomicWithLinkSafetyPostgreSQL(t *testing.T) {
	f := newEditFixture(t)

	// 1. A -> no URL. The old association must go, and with it the old state.
	// Nothing schedules this cleanup later; if the transaction does not do it, an
	// unrelated URL keeps deciding this message forever.
	t.Run("editing the link out clears every link fact", func(t *testing.T) {
		f.reset(t)
		f.verdict(t, urlOld, "inconclusive")
		f.seedMessage(t, editMessage, "veja "+urlOld, "inconclusive", "fp-old", urlOld)

		if _, err := f.edit(t, "sem link nenhum", domain.MessageLinkSafetyNone, nil); err != nil {
			t.Fatalf("EditMessage: %v", err)
		}
		if got := f.associations(t, editMessage); len(got) != 0 {
			t.Fatalf("associations = %q, want none — the message no longer contains a url", got)
		}
		state, fingerprint := f.storedState(t, editMessage)
		if state != "" || fingerprint != "" {
			t.Fatalf("state=%q fingerprint=%q, want both cleared", state, fingerprint)
		}
	})

	// 2. A -> B safe. The association swaps wholesale; the old URL must not remain,
	// or reconciling *it* would still move this message.
	t.Run("editing to a safe link swaps the association", func(t *testing.T) {
		f.reset(t)
		f.verdict(t, urlOld, "inconclusive")
		f.verdict(t, urlNew, "safe")
		f.seedMessage(t, editMessage, "veja "+urlOld, "inconclusive", "fp-old", urlOld)

		if _, err := f.edit(t, "veja "+urlNew, domain.MessageLinkSafetySafe, []string{urlNew}); err != nil {
			t.Fatalf("EditMessage: %v", err)
		}
		got := f.associations(t, editMessage)
		if len(got) != 1 || got[0] != urlNew {
			t.Fatalf("associations = %q, want only the new url", got)
		}
		if state, _ := f.storedState(t, editMessage); state != "safe" {
			t.Fatalf("state = %q, want safe", state)
		}
	})

	// 3. A -> B inconclusive. Published, and the notice follows the new URL.
	t.Run("editing to an inconclusive link publishes with the new state", func(t *testing.T) {
		f.reset(t)
		f.verdict(t, urlOld, "safe")
		f.verdict(t, urlNew, "inconclusive")
		f.seedMessage(t, editMessage, "veja "+urlOld, "safe", "fp-old", urlOld)

		updated, err := f.edit(t, "veja "+urlNew, domain.MessageLinkSafetyInconclusive, []string{urlNew})
		if err != nil {
			t.Fatalf("EditMessage: %v", err)
		}
		if updated.LinkSafety != domain.MessageLinkSafetyInconclusive {
			t.Fatalf("returned state = %q, want inconclusive", updated.LinkSafety)
		}
		if state, _ := f.storedState(t, editMessage); state != "inconclusive" {
			t.Fatalf("stored state = %q, want inconclusive", state)
		}
	})

	// 4. A -> B still pending. There is no verdict yet, so there is nothing to
	// commit to. The edit is refused rather than published under a guessed state,
	// and — the part worth proving — the *old* facts are untouched by the refusal.
	t.Run("editing to an unscanned link is refused and changes nothing", func(t *testing.T) {
		f.reset(t)
		f.verdict(t, urlOld, "safe")
		f.seedMessage(t, editMessage, "veja "+urlOld, "safe", "fp-old", urlOld)

		_, err := f.edit(t, "veja "+urlNew, domain.MessageLinkSafetySafe, []string{urlNew})
		if err == nil {
			t.Fatal("EditMessage accepted a body whose link has no verdict")
		}
		got := f.associations(t, editMessage)
		if len(got) != 1 || got[0] != urlOld {
			t.Fatalf("associations = %q, want the original untouched after a refusal", got)
		}
		if state, fingerprint := f.storedState(t, editMessage); state != "safe" || fingerprint != "fp-old" {
			t.Fatalf("state=%q fingerprint=%q, want the original untouched", state, fingerprint)
		}
		var body string
		if err := f.pool.QueryRow(f.ctx,
			`SELECT body_text FROM chat.messages WHERE id = $1::uuid`, editMessage).Scan(&body); err != nil {
			t.Fatalf("read body: %v", err)
		}
		if body != "veja "+urlOld {
			t.Fatalf("body = %q, want the original — a refused edit must not half-commit", body)
		}
	})

	// 5. A -> B malicious. An edit is not a way to publish a condemned link, and
	// the refusal is the store's, not the caller's: the caller here is passing
	// "safe" on purpose.
	t.Run("editing to a malicious link is refused by the store", func(t *testing.T) {
		f.reset(t)
		f.verdict(t, urlOld, "safe")
		f.verdict(t, urlNew, "malicious")
		f.seedMessage(t, editMessage, "veja "+urlOld, "safe", "fp-old", urlOld)

		_, err := f.edit(t, "veja "+urlNew, domain.MessageLinkSafetySafe, []string{urlNew})
		if err == nil {
			t.Fatal("EditMessage published a body containing a condemned url")
		}
		if got := f.associations(t, editMessage); len(got) != 1 || got[0] != urlOld {
			t.Fatalf("associations = %q, want the original untouched", got)
		}
	})

	// 6. Several URLs at once. Every one of them is recorded, so whichever is
	// reconciled next finds this message.
	t.Run("every url in the new body is associated", func(t *testing.T) {
		f.reset(t)
		f.verdict(t, urlOld, "safe")
		f.verdict(t, urlNew, "inconclusive")
		f.seedMessage(t, editMessage, "nada", "", "", nil...)

		if _, err := f.edit(t, urlOld+" e "+urlNew,
			domain.MessageLinkSafetyInconclusive, []string{urlOld, urlNew}); err != nil {
			t.Fatalf("EditMessage: %v", err)
		}
		got := f.associations(t, editMessage)
		if len(got) != 2 || got[0] != urlNew || got[1] != urlOld {
			t.Fatalf("associations = %q, want both urls", got)
		}
	})

	// 7. The race the atomicity is for: reconciliation condemns the *new* URL
	// between the caller's classification and the commit. The store re-reads the
	// verdicts inside the transaction, so the stale classification loses.
	t.Run("a verdict that lands before the commit wins", func(t *testing.T) {
		f.reset(t)
		f.verdict(t, urlOld, "safe")
		f.verdict(t, urlNew, "inconclusive")
		f.seedMessage(t, editMessage, "veja "+urlOld, "safe", "fp-old", urlOld)

		// The caller classified urlNew as inconclusive; reconciliation condemns it
		// before EditMessage runs, which is the window a check outside the
		// transaction would miss.
		f.verdict(t, urlNew, "malicious")

		if _, err := f.edit(t, "veja "+urlNew,
			domain.MessageLinkSafetyInconclusive, []string{urlNew}); err == nil {
			t.Fatal("a classification made before the condemnation was allowed to commit")
		}
		if got := f.associations(t, editMessage); len(got) != 1 || got[0] != urlOld {
			t.Fatalf("associations = %q, want the original untouched", got)
		}
	})

	// 8. Reload matches what the edit returned. The realtime payload and a fresh
	// read come from the same columns, so a client that reconnects converges.
	t.Run("a reload agrees with the edit result", func(t *testing.T) {
		f.reset(t)
		f.verdict(t, urlNew, "inconclusive")
		f.seedMessage(t, editMessage, "nada", "", "", nil...)

		updated, err := f.edit(t, "veja "+urlNew,
			domain.MessageLinkSafetyInconclusive, []string{urlNew})
		if err != nil {
			t.Fatalf("EditMessage: %v", err)
		}
		reloaded, err := f.store.GetMessageByIDInWorkspace(f.ctx, editWorkspace, editMessage, editAuthor)
		if err != nil {
			t.Fatalf("GetMessageByIDInWorkspace: %v", err)
		}
		if reloaded.LinkSafety != updated.LinkSafety {
			t.Fatalf("reload = %q but the edit returned %q — realtime and reload disagree",
				reloaded.LinkSafety, updated.LinkSafety)
		}
		if reloaded.BodyText != updated.BodyText {
			t.Fatalf("reload body = %q, edit returned %q", reloaded.BodyText, updated.BodyText)
		}
	})
}

// TestMaliciousBodyIsWithheldFromEveryProjectionPostgreSQL is the CQ-002 proof.
//
// # What was wrong
//
// A message whose links were condemned kept its body in the database and kept
// sending it. The client refused to render it, which is a decision taken over
// data it had already received — so the URL was still in the response, still in
// the network tab, still in any cached payload, and still reachable through the
// three projections that read body_text from the same row without going near the
// client's check: a quote, a cross-target reference, and the edit history.
//
// # What is asserted
//
// The body does not appear in any read the server serves — not in the main
// message, not in a quote of it, not in a reference to it, not in any stored
// version of it, and not in a channel listing.
func TestMaliciousBodyIsWithheldFromEveryProjectionPostgreSQL(t *testing.T) {
	f := newEditFixture(t)
	const maliciousBody = "olha isso https://edit.example/old agora"

	t.Run("a new link-free history version remains readable", func(t *testing.T) {
		f.reset(t)
		f.seedMessage(t, editMessage, "texto original sem link", "", "")
		if _, err := f.edit(t, "texto editado sem link", domain.MessageLinkSafetyNone, nil); err != nil {
			t.Fatalf("EditMessage: %v", err)
		}
		history, err := f.store.ListMessageEditHistory(f.ctx, storage.ListMessageEditHistoryInput{
			WorkspaceID: editWorkspace, MessageID: editMessage, UserID: editAuthor,
		})
		if err != nil || len(history) != 1 || history[0].Body != "texto original sem link" {
			t.Fatalf("link-free history was hidden: %+v / %v", history, err)
		}
	})

	// seedCondemned builds a *published* message whose links have since been
	// condemned — the reconciliation outcome, not the admission one. A message
	// condemned at admission is never published and has no projections at all.
	seedCondemned := func(t *testing.T) {
		t.Helper()
		f.reset(t)
		f.verdict(t, urlOld, "malicious")
		f.seedMessage(t, editMessage, maliciousBody, "malicious", "fp-old", urlOld)
	}

	seedQuoting := func(t *testing.T) {
		t.Helper()
		if _, err := f.pool.Exec(f.ctx, `
			INSERT INTO chat.messages
				(id, workspace_id, channel_id, sender_id, kind, body_text, body_format,
				 status, parent_message_id)
			VALUES ($1, $2, $3, $4, 'user', 'concordo', 'v2', 'active', $5)`,
			quoteMessage, editWorkspace, editChannel, editAuthor, editMessage); err != nil {
			t.Fatalf("seed quoting message: %v", err)
		}
	}

	t.Run("the main body is withheld by the server, not by the client", func(t *testing.T) {
		seedCondemned(t)
		got, err := f.store.GetMessageByIDInWorkspace(f.ctx, editWorkspace, editMessage, editAuthor)
		if err != nil {
			t.Fatalf("GetMessageByIDInWorkspace: %v", err)
		}
		if got.BodyText != "" {
			t.Fatalf("body = %q, want empty — the client refusing to render it is not a block",
				got.BodyText)
		}
		if got.LinkSafety != domain.MessageLinkSafetyMalicious {
			t.Fatalf("link safety = %q, want malicious so the reader is told why", got.LinkSafety)
		}
	})

	t.Run("a quote of it carries no body", func(t *testing.T) {
		seedCondemned(t)
		seedQuoting(t)

		got, err := f.store.GetMessageByIDInWorkspace(f.ctx, editWorkspace, quoteMessage, editAuthor)
		if err != nil {
			t.Fatalf("GetMessageByIDInWorkspace: %v", err)
		}
		if got.Quoted == nil {
			t.Fatal("the quote vanished; withholding the body must not remove the quote itself")
		}
		if got.Quoted.BodyText != "" {
			t.Fatalf("quoted body = %q, want empty", got.Quoted.BodyText)
		}
		if got.Quoted.LinkSafety != domain.MessageLinkSafetyMalicious {
			t.Fatalf("quoted link safety = %q, want malicious so the quote can say why",
				got.Quoted.LinkSafety)
		}
	})

	t.Run("a cross-target reference to it carries no body", func(t *testing.T) {
		seedCondemned(t)
		refs, err := f.store.ResolveMessageReferences(
			f.ctx, editWorkspace, editAuthor, []string{editMessage})
		if err != nil {
			t.Fatalf("ResolveMessageReferences: %v", err)
		}
		ref, ok := refs[editMessage]
		if !ok || !ref.Available {
			t.Fatal("the reference became unavailable; the message still exists and is readable")
		}
		if ref.BodyText != "" {
			t.Fatalf("reference body = %q, want empty", ref.BodyText)
		}
		if ref.LinkSafety != domain.MessageLinkSafetyMalicious {
			t.Fatalf("reference link safety = %q, want malicious", ref.LinkSafety)
		}
	})

	// The history is where the URL is *most* likely to still be written down: an
	// edit that reworded the sentence around a link kept the link. Every stored
	// version is withheld, not only whichever one was condemned.
	t.Run("the edit history returns no body from any version", func(t *testing.T) {
		seedCondemned(t)
		for _, body := range []string{
			"primeira versão https://edit.example/old",
			"segunda versão https://edit.example/old ainda",
		} {
			if _, err := f.pool.Exec(f.ctx, `
				INSERT INTO chat.message_edit_history (message_id, body, body_format, editor_user_id)
				VALUES ($1::uuid, $2, 'v2', $3::uuid)`,
				editMessage, body, editAuthor); err != nil {
				t.Fatalf("seed history: %v", err)
			}
		}

		history, err := f.store.ListMessageEditHistory(f.ctx, storage.ListMessageEditHistoryInput{
			WorkspaceID: editWorkspace, MessageID: editMessage, UserID: editAuthor,
		})
		if err != nil {
			t.Fatalf("ListMessageEditHistory: %v", err)
		}
		if len(history) != 2 {
			t.Fatalf("history has %d versions, want 2 — the versions are preserved, "+
				"only their text is withheld", len(history))
		}
		for i, version := range history {
			if version.Body != "" {
				t.Fatalf("history[%d].Body = %q, want empty", i, version.Body)
			}
		}
	})

	// The state is read live from the source row, so a message condemned *after*
	// it was quoted stops showing through that quote on the next read — no
	// backfill, nothing to schedule, and a reload agrees with realtime because
	// both come from this query.
	t.Run("a later condemnation withdraws an existing quote", func(t *testing.T) {
		f.reset(t)
		f.verdict(t, urlOld, "inconclusive")
		f.seedMessage(t, editMessage, maliciousBody, "inconclusive", "fp-old", urlOld)
		seedQuoting(t)

		before, err := f.store.GetMessageByIDInWorkspace(f.ctx, editWorkspace, quoteMessage, editAuthor)
		if err != nil {
			t.Fatalf("GetMessageByIDInWorkspace: %v", err)
		}
		if before.Quoted == nil || before.Quoted.BodyText != maliciousBody {
			t.Fatalf("quoted body = %+v while merely inconclusive, want the real body — "+
				"inconclusive publishes", before.Quoted)
		}

		if _, err := f.pool.Exec(f.ctx,
			`UPDATE chat.messages SET link_safety_state = 'malicious' WHERE id = $1::uuid`,
			editMessage); err != nil {
			t.Fatalf("condemn: %v", err)
		}

		after, err := f.store.GetMessageByIDInWorkspace(f.ctx, editWorkspace, quoteMessage, editAuthor)
		if err != nil {
			t.Fatalf("GetMessageByIDInWorkspace: %v", err)
		}
		if after.Quoted == nil || after.Quoted.BodyText != "" {
			t.Fatalf("quoted body = %+v after condemnation, want it withheld", after.Quoted)
		}
	})

	// Editing the condemned link out is the way back, and it works because the
	// edit transaction reclassifies the message rather than leaving the old state.
	t.Run("editing the condemned link out restores the projections", func(t *testing.T) {
		seedCondemned(t)
		if _, err := f.edit(t, "sem link", domain.MessageLinkSafetyNone, nil); err != nil {
			t.Fatalf("EditMessage: %v", err)
		}
		got, err := f.store.GetMessageByIDInWorkspace(f.ctx, editWorkspace, editMessage, editAuthor)
		if err != nil {
			t.Fatalf("GetMessageByIDInWorkspace: %v", err)
		}
		if got.BodyText != "sem link" {
			t.Fatalf("body = %q, want the new one — the message is no longer about a condemned url",
				got.BodyText)
		}
		history, err := f.store.ListMessageEditHistory(f.ctx, storage.ListMessageEditHistoryInput{
			WorkspaceID: editWorkspace, MessageID: editMessage, UserID: editAuthor,
		})
		if err != nil || len(history) != 1 || history[0].Body != "" {
			t.Fatalf("condemned prior version was exposed after edit: %+v / %v", history, err)
		}
	})

	t.Run("a verdict after URL removal still redacts the historical version", func(t *testing.T) {
		f.reset(t)
		f.verdict(t, urlOld, "inconclusive")
		f.seedMessage(t, editMessage, maliciousBody, "inconclusive", "fp-old", urlOld)
		if _, err := f.edit(t, "sem link", domain.MessageLinkSafetyNone, nil); err != nil {
			t.Fatalf("EditMessage: %v", err)
		}
		f.verdict(t, urlOld, "malicious")

		history, err := f.store.ListMessageEditHistory(f.ctx, storage.ListMessageEditHistoryInput{
			WorkspaceID: editWorkspace, MessageID: editMessage, UserID: editAuthor,
		})
		if err != nil || len(history) != 1 || history[0].Body != "" {
			t.Fatalf("later verdict did not redact historical version: %+v / %v", history, err)
		}
	})

	t.Run("the first new edit does not trust a body drifted by a legacy writer", func(t *testing.T) {
		f.reset(t)
		f.verdict(t, urlOld, "safe")
		f.seedMessage(t, editMessage, "antes "+urlOld, "safe", "fp-old", urlOld)

		// Literal origin/develop edit: body bookkeeping changes, while the link
		// projection is not mentioned. The migration trigger must invalidate that
		// projection during a rolling deploy.
		if _, err := f.pool.Exec(f.ctx, `
			UPDATE chat.messages
			SET body_text = $2, edited_at = now(), edit_count = edit_count + 1,
			    updated_at = now()
			WHERE id = $1::uuid`, editMessage, "legado "+urlNew); err != nil {
			t.Fatalf("legacy edit: %v", err)
		}
		var state, fingerprint string
		var projectionVersion int64
		if err := f.pool.QueryRow(f.ctx, `
			SELECT link_safety_state, COALESCE(link_safety_fingerprint, ''),
			       link_safety_projection_version
			FROM chat.messages WHERE id = $1::uuid`, editMessage,
		).Scan(&state, &fingerprint, &projectionVersion); err != nil {
			t.Fatalf("read invalidated legacy projection: %v", err)
		}
		if state != "" || fingerprint != "" || projectionVersion != 0 {
			t.Fatalf("legacy projection remained trusted: state=%q fingerprint=%q version=%d",
				state, fingerprint, projectionVersion)
		}

		if _, err := f.edit(t, "sem link", domain.MessageLinkSafetyNone, nil); err != nil {
			t.Fatalf("first new EditMessage: %v", err)
		}
		f.verdict(t, urlNew, "malicious")
		if _, err := f.pool.Exec(f.ctx, `
			INSERT INTO files.link_fetch_denylist (url_digest, canonical_url, source)
			VALUES ($1, $2, 'chat') ON CONFLICT DO NOTHING`,
			urlsafety.URLDigest(urlNew), urlNew); err != nil {
			t.Fatalf("deny legacy body URL: %v", err)
		}

		history, err := f.store.ListMessageEditHistory(f.ctx, storage.ListMessageEditHistoryInput{
			WorkspaceID: editWorkspace, MessageID: editMessage, UserID: editAuthor,
		})
		if err != nil || len(history) != 1 || history[0].Body != "" {
			t.Fatalf("legacy-drifted body was exposed from history: %+v / %v", history, err)
		}
	})

	// The channel listing is a fourth query over the same column, and the one a
	// reader actually hits first.
	t.Run("the channel listing does not leak the body", func(t *testing.T) {
		seedCondemned(t)
		result, err := f.store.ListChannelMessages(f.ctx, storage.ListChannelMessagesInput{
			WorkspaceID: editWorkspace, ChannelID: editChannel, UserID: editAuthor, Limit: 50,
		})
		if err != nil {
			t.Fatalf("ListChannelMessages: %v", err)
		}
		found := false
		for _, message := range result.Messages {
			if message.ID != editMessage {
				continue
			}
			found = true
			if message.BodyText != "" {
				t.Fatalf("listed body = %q, want empty", message.BodyText)
			}
		}
		if !found {
			t.Fatal("the message vanished from the listing; it is withheld, not deleted")
		}
	})
}
