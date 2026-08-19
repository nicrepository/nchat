package linkpreview

import (
	"context"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nicrepository/nchat/libs/go/platform/urlsafety"
	"github.com/nicrepository/nchat/services/file-service/internal/storage"
)

// The end-to-end CQ-002 proof: a URL any component proved malicious produces
// **zero** outbound HTTP requests from the preview path.
//
// The storage tests prove the gate returns the right verdict. This proves the
// thing that actually matters — that no packet leaves the process — by wiring the
// real PGX store into the real preview service and counting requests at a server
// that would answer perfectly good HTML if anything ever asked.
//
// Opt-in like its neighbours: needs FILE_TEST_DATABASE_URL against a _test
// database carrying the real migrations.
func TestPreviewRefusesADeniedURLPostgreSQL(t *testing.T) {
	dsn := os.Getenv("FILE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("FILE_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	var databaseName string
	if err := pool.QueryRow(t.Context(), `SELECT current_database()`).Scan(&databaseName); err != nil {
		t.Fatalf("read current database: %v", err)
	}
	if !strings.HasSuffix(databaseName, "_test") {
		t.Fatalf("refusing destructive test against non-test database %q", databaseName)
	}

	ctx := t.Context()
	store := storage.NewPGXLinkScanStore(pool)

	var requests atomic.Int64
	server := countingServer(t, &requests)
	// A public-looking hostname, which serviceAgainst's dialer routes to the test
	// server — the same arrangement every other test in this package uses. It has
	// to be a name rather than the server's own IP literal, because an IP literal
	// has no consultable reputation and would be refused before the denylist was
	// ever consulted, proving nothing.
	//
	// The server really does answer, so "zero requests" is a meaningful assertion
	// rather than an accident of an unreachable host.
	const target = "http://example.com/page"
	canonical, err := urlsafety.CanonicalizeURL(target)
	if err != nil {
		t.Fatalf("canonicalize: %v", err)
	}
	digest := urlsafety.URLDigest(canonical)

	t.Cleanup(func() {
		background := context.Background()
		_, _ = pool.Exec(background, `DELETE FROM files.link_fetch_denylist WHERE url_digest = $1`, digest)
		_, _ = pool.Exec(background, `DELETE FROM files.link_scans WHERE url_digest = $1`, digest)
	})

	preview := serviceAgainst(t, server, newClock(), nil).WithURLSafety(store)

	// Step 1 of the finding's scenario: file-service holds a live SAFE clearance,
	// so the preview works and the fetch really does happen.
	if _, err := pool.Exec(ctx, `
		INSERT INTO files.link_scans
		    (url_digest, canonical_url, state, scan_uuid, verdict, verdict_expires_at)
		VALUES ($1, $2, 'done', 'files-scan', 'safe', now() + interval '10 minutes')
		ON CONFLICT (url_digest) DO UPDATE
		   SET state = 'done', verdict = 'safe',
		       verdict_expires_at = now() + interval '10 minutes'`,
		digest, canonical); err != nil {
		t.Fatalf("seed clearance: %v", err)
	}
	if _, err := preview.Preview(ctx, target); err != nil {
		t.Fatalf("precondition: a cleared URL must preview: %v", err)
	}
	if requests.Load() != 1 {
		t.Fatalf("precondition: expected one fetch, got %d", requests.Load())
	}

	// Step 2: chat-service proves the URL malicious. This is exactly what
	// chat-service's ReconcileLinkVerdict writes, in one statement.
	if _, err := pool.Exec(ctx, `
		WITH `+urlsafety.InvalidateFetchAuthoritySQL(
		"$1", "$2", "'"+urlsafety.DenylistSourceChat+"'")+`
		SELECT 1`,
		digest, canonical); err != nil {
		t.Fatalf("publish condemnation: %v", err)
	}

	// Step 3: the very next preview request. Not after a TTL, not after a worker
	// pass — immediately.
	before := requests.Load()
	_, err = preview.Preview(ctx, target)

	if err == nil {
		t.Fatal("a URL known to be malicious was previewed")
	}
	// Step 4: zero outbound HTTP requests.
	if after := requests.Load(); after != before {
		t.Fatalf("%d outbound request(s) were made for a denied URL, want zero", after-before)
	}
	// And it is refused as malicious, not as a transient problem — a client must
	// not be told to retry a URL that will never be allowed.
	if !isMaliciousRefusal(err) {
		t.Fatalf("err = %v, want a permanent security refusal", err)
	}

	// The preview cache must not be a way around it either: repeated requests keep
	// refusing and keep fetching nothing.
	for attempt := 0; attempt < 3; attempt++ {
		if _, err := preview.Preview(ctx, target); err == nil {
			t.Fatal("a repeated request for a denied URL was served")
		}
	}
	if after := requests.Load(); after != before {
		t.Fatalf("%d outbound request(s) after repeats, want zero", after-before)
	}
}

// isMaliciousRefusal keeps the assertion readable without importing errors just
// for one call.
func isMaliciousRefusal(err error) bool {
	return err == ErrMaliciousURL
}
