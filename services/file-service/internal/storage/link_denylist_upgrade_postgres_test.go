package storage_test

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nicrepository/nchat/libs/go/platform/urlsafety"
	"github.com/nicrepository/nchat/services/file-service/internal/storage"
)

// The upgrade to the shared fetch denylist, and the rolling-deployment guarantees
// around it (issue #135, CQ-002 / CQ-003).
//
// # Why these are not ordinary store tests
//
// Everything asserted here is about code this database cannot see: an
// origin/develop pod, still running while the migration has already been applied.
// A guard written in Go protects only the process containing it, so the guarantee
// has to be in the schema — and the only honest way to test a schema guarantee is
// to run the *actual* old SQL against it.
//
// The queries below are copied verbatim from origin/develop
// (services/file-service/internal/storage/link_scan_store.go) rather than
// re-expressed from memory, because the point is compatibility with what really
// shipped.
//
// Opt-in like its neighbours: needs FILE_TEST_DATABASE_URL against a _test
// database carrying the real migrations.

// legacyClaimQuery is origin/develop's ClaimDueScans, verbatim.
const legacyClaimQuery = `
	WITH due AS (
		SELECT ls.url_digest
		FROM files.link_scans ls
		WHERE ls.state IN ('submit_pending', 'submitting', 'submit_uncertain', 'polling')
		  AND (ls.next_attempt_at IS NULL OR ls.next_attempt_at <= now())
		  AND (ls.lease_until IS NULL OR ls.lease_until <= now())
		ORDER BY ls.next_attempt_at NULLS FIRST, ls.created_at
		LIMIT $1
		FOR UPDATE SKIP LOCKED
	)
	UPDATE files.link_scans ls
	   SET attempts = LEAST(ls.attempts + 1, $4),
	       lease_until = now() + ($2 * interval '1 second'),
	       next_attempt_at = now() + ($2 * LEAST(ls.attempts + 1, $3) * interval '1 second'),
	       updated_at = now()
	  FROM due
	 WHERE ls.url_digest = due.url_digest
	RETURNING ls.url_digest, ls.canonical_url, ls.state, COALESCE(ls.scan_uuid, ''), ls.attempts,
	          COALESCE(ls.submit_attempt_started_at, 'epoch'::timestamptz),
	          ls.submit_generation`

// legacyRecordSafeQuery is origin/develop's RecordVerdict for a final verdict,
// verbatim. This is the statement that must stop being able to grant clearance.
const legacyRecordSafeQuery = `
	UPDATE files.link_scans
	   SET state = 'done', verdict = $3,
	       verdict_expires_at = now() + ($4 * interval '1 second'),
	       lease_until = NULL, next_attempt_at = NULL, updated_at = now()
	 WHERE url_digest = $1 AND state = 'polling' AND scan_uuid = $2`

// legacyLoadVerdictQuery is origin/develop's LoadVerdict, verbatim. It knows
// nothing about the denylist, which is exactly why the write side has to hold.
const legacyLoadVerdictQuery = `
	SELECT verdict
	FROM files.link_scans
	WHERE url_digest = $1 AND state = 'done' AND verdict_expires_at > now()`

// legacyReaderFindsClearance runs origin/develop's gate and reports whether it
// would authorise a fetch.
func legacyReaderFindsClearance(t *testing.T, pool *pgxpool.Pool, digest []byte) bool {
	t.Helper()
	rows, err := pool.Query(context.Background(), legacyLoadVerdictQuery, digest)
	if err != nil {
		t.Fatalf("legacy reader: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var verdict string
		if err := rows.Scan(&verdict); err != nil {
			t.Fatalf("legacy reader scan: %v", err)
		}
		if verdict == "safe" {
			return true
		}
	}
	return false
}

// TestLinkFetchDenylistBackfillPostgreSQL is the upgrade proof.
//
// It reconstructs the state a real deployment is in the instant before 000010
// runs — a condemnation already known to chat-service, and a live clearance in
// file-service for the same URL — then applies the migration's own SQL and
// asserts the hole is closed for both the new and the old reader.
func TestLinkFetchDenylistBackfillPostgreSQL(t *testing.T) {
	ctx := t.Context()
	pool := newLinkScanTestPool(t)
	store := storage.NewPGXLinkScanStore(pool)

	const (
		chatCondemned  = "https://backfill-chat.example/payload"
		filesCondemned = "https://backfill-files.example/payload"
		unicodeURL     = "https://münchen.example/straße"
	)
	digests := map[string][]byte{
		chatCondemned:  urlsafety.URLDigest(chatCondemned),
		filesCondemned: urlsafety.URLDigest(filesCondemned),
		unicodeURL:     urlsafety.URLDigest(unicodeURL),
	}

	cleanup := func() {
		background := context.Background()
		for _, digest := range digests {
			_, _ = pool.Exec(background,
				`DELETE FROM files.link_fetch_denylist WHERE url_digest = $1`, digest)
			_, _ = pool.Exec(background,
				`DELETE FROM files.link_scans WHERE url_digest = $1`, digest)
		}
		_, _ = pool.Exec(background,
			`DELETE FROM chat.link_scans WHERE canonical_url = ANY($1::text[])`,
			[]string{chatCondemned, filesCondemned, unicodeURL})
	}
	cleanup()
	t.Cleanup(cleanup)

	// The pre-upgrade world: chat knows two URLs are malicious (one of them
	// non-ASCII, so the digest agreement is exercised on a real multi-byte URL),
	// file-service holds a live clearance for both, and file-service separately
	// knows a third is malicious.
	for _, url := range []string{chatCondemned, unicodeURL} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO chat.link_scans (canonical_url, status, scan_uuid, decided_at)
			VALUES ($1, 'malicious', 'chat-scan', now())`, url); err != nil {
			t.Fatalf("seed chat condemnation: %v", err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO files.link_scans
			    (url_digest, canonical_url, state, scan_uuid, verdict, verdict_expires_at)
			VALUES ($1, $2, 'done', 'files-scan', 'safe', now() + interval '10 minutes')`,
			digests[url], url); err != nil {
			t.Fatalf("seed files clearance: %v", err)
		}
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO files.link_scans
		    (url_digest, canonical_url, state, scan_uuid, verdict, verdict_expires_at)
		VALUES ($1, $2, 'done', 'files-scan', 'malicious', now() + interval '10 minutes')`,
		digests[filesCondemned], filesCondemned); err != nil {
		t.Fatalf("seed files condemnation: %v", err)
	}

	// Before the migration's work, the old reader would happily fetch.
	for _, url := range []string{chatCondemned, unicodeURL} {
		if !legacyReaderFindsClearance(t, pool, digests[url]) {
			t.Fatalf("precondition: the legacy reader must start out authorising %s", url)
		}
	}

	// Apply the migration's own backfill statements. Read from the migration file
	// rather than retyped, so this cannot drift from what actually ships.
	applyBackfillFromMigration(t, pool)

	t.Run("every known condemnation is carried into the denylist", func(t *testing.T) {
		for url, source := range map[string]string{
			chatCondemned:  urlsafety.DenylistSourceChat,
			unicodeURL:     urlsafety.DenylistSourceChat,
			filesCondemned: urlsafety.DenylistSourceFiles,
		} {
			var got string
			if err := pool.QueryRow(ctx,
				`SELECT source FROM files.link_fetch_denylist WHERE url_digest = $1`,
				digests[url],
			).Scan(&got); err != nil {
				t.Fatalf("%s was not carried into the denylist: %v", url, err)
			}
			if got != source {
				t.Fatalf("%s source = %q, want %q", url, got, source)
			}
		}
	})

	// The digest is computed in SQL for the chat rows and in Go everywhere else. If
	// the two disagreed the veto would silently never match, and the failure would
	// look like "no such URL" rather than like a bug.
	t.Run("the SQL and Go digests agree, including for non-ASCII", func(t *testing.T) {
		for _, url := range []string{chatCondemned, unicodeURL} {
			var matches bool
			if err := pool.QueryRow(ctx,
				`SELECT EXISTS (SELECT 1 FROM files.link_fetch_denylist WHERE url_digest = $1)`,
				urlsafety.URLDigest(url),
			).Scan(&matches); err != nil {
				t.Fatalf("lookup by Go digest: %v", err)
			}
			if !matches {
				t.Fatalf("the Go digest for %s does not find the row SQL wrote", url)
			}
		}
	})

	t.Run("the conflicting clearance stops authorising, for both readers", func(t *testing.T) {
		for _, url := range []string{chatCondemned, unicodeURL} {
			// The new gate.
			verdict, ok, err := store.LoadVerdict(ctx, url)
			if err != nil {
				t.Fatalf("LoadVerdict: %v", err)
			}
			if !ok || verdict != urlsafety.VerdictMalicious {
				t.Fatalf("%s: new gate = %q/%v, want malicious", url, verdict, ok)
			}
			// And origin/develop's, which has never heard of the denylist.
			if legacyReaderFindsClearance(t, pool, digests[url]) {
				t.Fatalf("%s: the legacy reader still authorises a fetch after the upgrade", url)
			}
		}
	})

	// CQ-003: the old worker is still running. It must not be able to put the
	// clearance back.
	t.Run("the legacy worker cannot recreate a clearance", func(t *testing.T) {
		digest := digests[chatCondemned]
		// Put the row back into the state the old worker would act on.
		if _, err := pool.Exec(ctx, `
			UPDATE files.link_scans
			   SET state = 'polling', verdict = NULL, verdict_expires_at = NULL,
			       lease_until = NULL, next_attempt_at = NULL
			 WHERE url_digest = $1`, digest); err != nil {
			t.Fatalf("reset to polling: %v", err)
		}

		// Its claim still works — nothing here is meant to break the old worker's
		// ordinary operation, only its ability to grant clearance.
		claimed, err := pool.Exec(ctx, legacyClaimQuery, 10, 60.0, 5, 32767)
		if err != nil {
			t.Fatalf("legacy claim: %v", err)
		}
		if claimed.RowsAffected() == 0 {
			t.Fatal("the legacy claim matched nothing; the fixture is wrong")
		}

		// Its SAFE write does not.
		tag, err := pool.Exec(ctx, legacyRecordSafeQuery,
			digest, "files-scan", "safe", urlsafety.VerdictTTL.Seconds())
		if err != nil {
			t.Fatalf("legacy safe write: %v", err)
		}
		if tag.RowsAffected() != 0 {
			t.Fatal("the legacy worker recreated a clearance for a denied URL")
		}
		if legacyReaderFindsClearance(t, pool, digest) {
			t.Fatal("the legacy reader found a clearance after the legacy worker's write")
		}

		// A condemnation from the same old worker is still allowed: the guard
		// refuses permission, not information.
		tag, err = pool.Exec(ctx, legacyRecordSafeQuery,
			digest, "files-scan", "malicious", urlsafety.VerdictTTL.Seconds())
		if err != nil {
			t.Fatalf("legacy malicious write: %v", err)
		}
		if tag.RowsAffected() != 1 {
			t.Fatal("the guard blocked a condemnation, which grants nothing")
		}
	})

	// A brand-new INSERT is the other way a clearance could appear.
	t.Run("no writer can insert a fresh clearance for a denied url", func(t *testing.T) {
		digest := digests[filesCondemned]
		if _, err := pool.Exec(ctx,
			`DELETE FROM files.link_scans WHERE url_digest = $1`, digest); err != nil {
			t.Fatalf("clear row: %v", err)
		}
		tag, err := pool.Exec(ctx, `
			INSERT INTO files.link_scans
			    (url_digest, canonical_url, state, scan_uuid, verdict, verdict_expires_at)
			VALUES ($1, $2, 'done', 'fresh', 'safe', now() + interval '10 minutes')`,
			digest, filesCondemned)
		if err != nil {
			t.Fatalf("insert attempt: %v", err)
		}
		if tag.RowsAffected() != 0 {
			t.Fatal("a fresh clearance was inserted for a denied URL")
		}
		if legacyReaderFindsClearance(t, pool, digest) {
			t.Fatal("the legacy reader found a freshly inserted clearance")
		}
	})

	// Reopening an expired row and closing it as safe is the third shape.
	t.Run("a reopened row cannot be closed as safe", func(t *testing.T) {
		digest := digests[unicodeURL]
		if _, err := pool.Exec(ctx, `
			UPDATE files.link_scans
			   SET state = 'submit_pending', verdict = NULL, verdict_expires_at = NULL
			 WHERE url_digest = $1`, digest); err != nil {
			t.Fatalf("reopen: %v", err)
		}
		tag, err := pool.Exec(ctx, `
			UPDATE files.link_scans
			   SET state = 'done', verdict = 'safe',
			       verdict_expires_at = now() + interval '10 minutes'
			 WHERE url_digest = $1`, digest)
		if err != nil {
			t.Fatalf("close attempt: %v", err)
		}
		if tag.RowsAffected() != 0 {
			t.Fatal("a reopened row was closed as safe for a denied URL")
		}
	})

	// The guard must not become a general obstruction: an ordinary URL still gets
	// its clearance.
	t.Run("an undenied url is unaffected", func(t *testing.T) {
		const ordinary = "https://ordinary.example/a"
		digest := urlsafety.URLDigest(ordinary)
		t.Cleanup(func() {
			_, _ = pool.Exec(context.Background(),
				`DELETE FROM files.link_scans WHERE url_digest = $1`, digest)
		})
		tag, err := pool.Exec(ctx, `
			INSERT INTO files.link_scans
			    (url_digest, canonical_url, state, scan_uuid, verdict, verdict_expires_at)
			VALUES ($1, $2, 'done', 'ok', 'safe', now() + interval '10 minutes')`,
			digest, ordinary)
		if err != nil {
			t.Fatalf("insert: %v", err)
		}
		if tag.RowsAffected() != 1 {
			t.Fatal("the guard blocked a clearance for a URL nobody denied")
		}
		if !legacyReaderFindsClearance(t, pool, digest) {
			t.Fatal("the legacy reader lost a legitimate clearance")
		}
	})
}

// TestLegacySafeWriterLosesToAConcurrentCondemnationPostgreSQL is the race the
// review asked for, run with two real connections.
//
// The dangerous interleaving is the one where the old worker started before the
// condemnation existed: it holds a polling row, chat commits malicious plus the
// denial, and only then does the worker try to write SAFE. Under READ COMMITTED
// its statement takes a fresh snapshot, so the guard sees the committed denial —
// but that is a claim worth checking rather than assuming.
func TestLegacySafeWriterLosesToAConcurrentCondemnationPostgreSQL(t *testing.T) {
	ctx := t.Context()
	pool := newLinkScanTestPool(t)

	const url = "https://race.example/payload"
	digest := urlsafety.URLDigest(url)

	cleanup := func() {
		background := context.Background()
		_, _ = pool.Exec(background, `DELETE FROM files.link_fetch_denylist WHERE url_digest = $1`, digest)
		_, _ = pool.Exec(background, `DELETE FROM files.link_scans WHERE url_digest = $1`, digest)
	}
	cleanup()
	t.Cleanup(cleanup)

	if _, err := pool.Exec(ctx, `
		INSERT INTO files.link_scans (url_digest, canonical_url, state, scan_uuid)
		VALUES ($1, $2, 'polling', 'legacy-scan')`, digest, url); err != nil {
		t.Fatalf("seed polling row: %v", err)
	}

	// Tx A: the legacy worker, already in a transaction, holding the row.
	txA, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin A: %v", err)
	}
	defer func() { _ = txA.Rollback(context.Background()) }()
	var state string
	if err := txA.QueryRow(ctx,
		`SELECT state FROM files.link_scans WHERE url_digest = $1`, digest).Scan(&state); err != nil {
		t.Fatalf("A reads the row: %v", err)
	}
	if state != "polling" {
		t.Fatalf("A sees state %q", state)
	}

	// Tx B: the condemnation, published exactly as the application publishes it.
	txB, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin B: %v", err)
	}
	if _, err := txB.Exec(ctx, `
		WITH `+urlsafety.InvalidateFetchAuthoritySQL(
		"$1", "$2", "'"+urlsafety.DenylistSourceChat+"'")+`
		SELECT 1`, digest, url); err != nil {
		_ = txB.Rollback(ctx)
		t.Fatalf("B publishes the condemnation: %v", err)
	}
	if err := txB.Commit(ctx); err != nil {
		t.Fatalf("commit B: %v", err)
	}

	// Tx A now finishes the scan as SAFE, using origin/develop's own statement.
	tag, err := txA.Exec(ctx, legacyRecordSafeQuery,
		digest, "legacy-scan", "safe", urlsafety.VerdictTTL.Seconds())
	if err != nil {
		t.Fatalf("A writes safe: %v", err)
	}
	if tag.RowsAffected() != 0 {
		t.Fatal("the legacy worker wrote a clearance after the condemnation committed")
	}
	if err := txA.Commit(ctx); err != nil {
		t.Fatalf("commit A: %v", err)
	}

	// And after both transactions, neither reader finds anything.
	if legacyReaderFindsClearance(t, pool, digest) {
		t.Fatal("the legacy reader found a clearance after the race")
	}
	store := storage.NewPGXLinkScanStore(pool)
	verdict, ok, err := store.LoadVerdict(ctx, url)
	if err != nil {
		t.Fatalf("LoadVerdict: %v", err)
	}
	if !ok || verdict != urlsafety.VerdictMalicious {
		t.Fatalf("new gate = %q/%v, want malicious", verdict, ok)
	}
}

// applyBackfillFromMigration runs the data statements of 000010 against an
// already-migrated database.
//
// The migration has of course already run by the time these tests connect — the
// suite requires a fully migrated schema. Re-running its backfill is safe by
// construction (ON CONFLICT DO NOTHING, and an expiry that is a no-op once
// applied), and it is what lets this test set up a realistic pre-upgrade state and
// then watch the real migration SQL close it.
//
// The statements are extracted from the migration file rather than retyped, so a
// change to the shipped SQL is exercised here instead of drifting away from it.
func applyBackfillFromMigration(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	// A fixed, repository-relative path with no caller input in it.
	const path = "../../../../migrations/files/000010_link_fetch_denylist.up.sql"
	raw, err := os.ReadFile(path) //nolint:gosec // fixed migration path, no external input
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(raw)
	start := indexOrFail(t, sql, "INSERT INTO files.link_fetch_denylist (url_digest, canonical_url, source)")
	end := indexOrFail(t, sql, "-- ---------------------------------------------------------------------------\n-- The guard")
	if _, err := pool.Exec(t.Context(), sql[start:end]); err != nil {
		t.Fatalf("apply backfill: %v", err)
	}
	// The expiry runs a second time here because the guard did not exist when the
	// fixture rows were inserted above; in a real upgrade it runs once, inside the
	// migration's transaction.
	if _, err := pool.Exec(t.Context(), `
		UPDATE files.link_scans ls
		   SET verdict_expires_at = now(), updated_at = now()
		 WHERE ls.state = 'done' AND ls.verdict = 'safe' AND ls.verdict_expires_at > now()
		   AND EXISTS (SELECT 1 FROM files.link_fetch_denylist d WHERE d.url_digest = ls.url_digest)`,
	); err != nil {
		t.Fatalf("apply expiry: %v", err)
	}
}

func indexOrFail(t *testing.T, haystack, needle string) int {
	t.Helper()
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	t.Fatalf("migration no longer contains %q; this test must be updated with it", needle)
	return 0
}
