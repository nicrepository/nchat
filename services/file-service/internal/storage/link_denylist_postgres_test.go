package storage_test

import (
	"context"
	"testing"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/urlsafety"
	"github.com/nicrepository/nchat/services/file-service/internal/storage"
)

// The shared fetch denylist, from file-service's side (issue #135, CQ-002).
//
// chat-service's own tests prove that recording a condemnation writes the row and
// expires this service's clearance in one statement. These prove the other half:
// that the gate which authorises an outbound fetch actually consults it, that a
// later clearance cannot override it, and that a condemnation observed here is
// published for chat-service in the same way.
//
// Opt-in like its neighbours: needs FILE_TEST_DATABASE_URL against a _test
// database carrying the real migrations.
func TestLinkFetchDenylistPostgreSQL(t *testing.T) {
	ctx := t.Context()
	pool := newLinkScanTestPool(t)
	store := storage.NewPGXLinkScanStore(pool)

	const url = "https://denylist.example/payload"
	digest := urlsafety.URLDigest(url)

	reset := func(t *testing.T) {
		t.Helper()
		for _, statement := range []string{
			`DELETE FROM files.link_fetch_denylist WHERE url_digest = $1`,
			`DELETE FROM files.link_scans WHERE url_digest = $1`,
		} {
			if _, err := pool.Exec(ctx, statement, digest); err != nil {
				t.Fatalf("reset: %v", err)
			}
		}
	}
	t.Cleanup(func() {
		background := context.Background()
		_, _ = pool.Exec(background, `DELETE FROM files.link_fetch_denylist WHERE url_digest = $1`, digest)
		_, _ = pool.Exec(background, `DELETE FROM files.link_scans WHERE url_digest = $1`, digest)
	})

	seedClearance := func(t *testing.T) {
		t.Helper()
		if _, err := pool.Exec(ctx, `
			INSERT INTO files.link_scans
			    (url_digest, canonical_url, state, scan_uuid, verdict, verdict_expires_at)
			VALUES ($1, $2, 'done', 'files-scan', 'safe', now() + interval '10 minutes')
			ON CONFLICT (url_digest) DO UPDATE
			   SET state = 'done', verdict = 'safe',
			       verdict_expires_at = now() + interval '10 minutes'`,
			digest, url); err != nil {
			t.Fatalf("seed clearance: %v", err)
		}
	}

	deny := func(t *testing.T, source string) {
		t.Helper()
		if _, err := pool.Exec(ctx, `
			INSERT INTO files.link_fetch_denylist (url_digest, canonical_url, source)
			VALUES ($1, $2, $3) ON CONFLICT (url_digest) DO NOTHING`,
			digest, url, source); err != nil {
			t.Fatalf("seed denial: %v", err)
		}
	}

	// The invariant, at the exact point it matters: the read that decides whether a
	// fetch may happen.
	t.Run("a denial overrides a live local clearance", func(t *testing.T) {
		reset(t)
		seedClearance(t)

		// Precondition: without the denial this URL is fetchable.
		verdict, ok, err := store.LoadVerdict(ctx, url)
		if err != nil {
			t.Fatalf("LoadVerdict: %v", err)
		}
		if !ok || verdict != urlsafety.VerdictSafe {
			t.Fatalf("precondition: verdict=%q ok=%v, want a live clearance", verdict, ok)
		}

		deny(t, urlsafety.DenylistSourceChat)

		verdict, ok, err = store.LoadVerdict(ctx, url)
		if err != nil {
			t.Fatalf("LoadVerdict after denial: %v", err)
		}
		// Reported as malicious rather than as absent, so the caller refuses
		// permanently instead of queueing another scan for a URL already known bad.
		if !ok || verdict != urlsafety.VerdictMalicious {
			t.Fatalf("verdict=%q ok=%v, want malicious", verdict, ok)
		}
	})

	// A denial with no local row at all is still a refusal — the URL may never have
	// been scanned by this service.
	t.Run("a denial refuses a url this service never scanned", func(t *testing.T) {
		reset(t)
		deny(t, urlsafety.DenylistSourceChat)

		verdict, ok, err := store.LoadVerdict(ctx, url)
		if err != nil {
			t.Fatalf("LoadVerdict: %v", err)
		}
		if !ok || verdict != urlsafety.VerdictMalicious {
			t.Fatalf("verdict=%q ok=%v, want malicious", verdict, ok)
		}
	})

	// The monotonic property: a scan that comes back SAFE afterwards cannot
	// re-open the URL.
	t.Run("a later clearance does not lift the denial", func(t *testing.T) {
		reset(t)
		deny(t, urlsafety.DenylistSourceChat)
		seedClearance(t)

		verdict, ok, err := store.LoadVerdict(ctx, url)
		if err != nil {
			t.Fatalf("LoadVerdict: %v", err)
		}
		if !ok || verdict != urlsafety.VerdictMalicious {
			t.Fatalf("verdict=%q ok=%v, a later clearance overrode a denial", verdict, ok)
		}
	})

	// A condemnation this service observes is published for chat-service too, in
	// the same statement that records it.
	t.Run("recording a condemnation here publishes it globally", func(t *testing.T) {
		reset(t)
		if _, err := pool.Exec(ctx, `
			INSERT INTO files.link_scans (url_digest, canonical_url, state, scan_uuid)
			VALUES ($1, $2, 'polling', 'files-poll')`, digest, url); err != nil {
			t.Fatalf("seed polling row: %v", err)
		}

		if err := store.RecordVerdict(ctx, digest, "files-poll",
			urlsafety.VerdictMalicious, urlsafety.VerdictTTL); err != nil {
			t.Fatalf("RecordVerdict: %v", err)
		}

		var source string
		if err := pool.QueryRow(ctx,
			`SELECT source FROM files.link_fetch_denylist WHERE url_digest = $1`, digest,
		).Scan(&source); err != nil {
			t.Fatalf("read denylist row: %v", err)
		}
		if source != urlsafety.DenylistSourceFiles {
			t.Fatalf("source = %q", source)
		}
	})

	// A clearance recorded here must not write a denial. Only condemnation is
	// global.
	t.Run("recording a clearance publishes nothing", func(t *testing.T) {
		reset(t)
		if _, err := pool.Exec(ctx, `
			INSERT INTO files.link_scans (url_digest, canonical_url, state, scan_uuid)
			VALUES ($1, $2, 'polling', 'files-poll')`, digest, url); err != nil {
			t.Fatalf("seed polling row: %v", err)
		}

		if err := store.RecordVerdict(ctx, digest, "files-poll",
			urlsafety.VerdictSafe, urlsafety.VerdictTTL); err != nil {
			t.Fatalf("RecordVerdict: %v", err)
		}

		var rows int
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM files.link_fetch_denylist WHERE url_digest = $1`, digest,
		).Scan(&rows); err != nil {
			t.Fatalf("count denylist rows: %v", err)
		}
		if rows != 0 {
			t.Fatalf("denylist rows = %d, want none for a clearance", rows)
		}
	})

	// Reconciliation condemning a URL publishes it too, through the other write
	// path.
	t.Run("reconciling to malicious publishes it globally", func(t *testing.T) {
		reset(t)
		if _, err := pool.Exec(ctx, `
			INSERT INTO files.link_scans (url_digest, canonical_url, state, scan_uuid)
			VALUES ($1, $2, 'inconclusive', 'files-terminal')`, digest, url); err != nil {
			t.Fatalf("seed inconclusive row: %v", err)
		}

		if err := store.ReconcileVerdict(ctx, digest, "files-terminal",
			urlsafety.ScanEvidence{Verdict: urlsafety.VerdictMalicious, ObservedAt: time.Now()},
		); err != nil {
			t.Fatalf("ReconcileVerdict: %v", err)
		}

		var rows int
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM files.link_fetch_denylist WHERE url_digest = $1`, digest,
		).Scan(&rows); err != nil {
			t.Fatalf("count denylist rows: %v", err)
		}
		if rows != 1 {
			t.Fatalf("denylist rows = %d, want one", rows)
		}
	})

	// Admission must not spend provider budget on a URL that is already denied.
	t.Run("a denied url is never queued for a new scan", func(t *testing.T) {
		reset(t)
		deny(t, urlsafety.DenylistSourceChat)

		verdict, ok, err := store.LoadVerdict(ctx, url)
		if err != nil {
			t.Fatalf("LoadVerdict: %v", err)
		}
		// The gate answers with a verdict, so the preview path never reaches
		// AdmitScan at all — this is what stops a known-bad URL from consuming scan
		// quota on every request.
		if !ok || verdict != urlsafety.VerdictMalicious {
			t.Fatalf("verdict=%q ok=%v", verdict, ok)
		}
	})
}

// The CQ-001 half that lives in this store: a clearance expires from the
// provider's evidence time, never from adoption.
func TestLinkScanReconcileEvidenceAgePostgreSQL(t *testing.T) {
	ctx := t.Context()
	pool := newLinkScanTestPool(t)
	store := storage.NewPGXLinkScanStore(pool)

	const url = "https://aged.example/a"
	digest := urlsafety.URLDigest(url)

	seed := func(t *testing.T) {
		t.Helper()
		if _, err := pool.Exec(ctx,
			`DELETE FROM files.link_scans WHERE url_digest = $1`, digest); err != nil {
			t.Fatalf("reset: %v", err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO files.link_scans (url_digest, canonical_url, state, scan_uuid)
			VALUES ($1, $2, 'inconclusive', 'scan-aged')`, digest, url); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM files.link_scans WHERE url_digest = $1`, digest)
	})

	t.Run("a clearance keeps only the lifetime its evidence has left", func(t *testing.T) {
		seed(t)
		age := urlsafety.VerdictTTL / 3
		evidence := urlsafety.ScanEvidence{
			Verdict: urlsafety.VerdictSafe, ObservedAt: time.Now().Add(-age),
		}

		if err := store.ReconcileVerdict(ctx, digest, "scan-aged", evidence); err != nil {
			t.Fatalf("ReconcileVerdict: %v", err)
		}

		var seconds float64
		if err := pool.QueryRow(ctx,
			`SELECT EXTRACT(EPOCH FROM (verdict_expires_at - now()))
			   FROM files.link_scans WHERE url_digest = $1`, digest).Scan(&seconds); err != nil {
			t.Fatalf("read expiry: %v", err)
		}
		remaining := time.Duration(seconds * float64(time.Second))
		if remaining >= urlsafety.VerdictTTL {
			t.Fatalf("remaining %v >= a full TTL %v; stale evidence was rejuvenated",
				remaining, urlsafety.VerdictTTL)
		}
		if remaining <= 0 {
			t.Fatalf("remaining %v, want a positive remainder", remaining)
		}
	})

	t.Run("expired evidence writes no clearance at all", func(t *testing.T) {
		seed(t)
		evidence := urlsafety.ScanEvidence{
			Verdict:    urlsafety.VerdictSafe,
			ObservedAt: time.Now().Add(-urlsafety.VerdictTTL - time.Minute),
		}

		err := store.ReconcileVerdict(ctx, digest, "scan-aged", evidence)

		if err == nil {
			t.Fatal("a clearance older than its lifetime was written")
		}
		var state string
		if err := pool.QueryRow(ctx,
			`SELECT state FROM files.link_scans WHERE url_digest = $1`, digest).Scan(&state); err != nil {
			t.Fatalf("read state: %v", err)
		}
		if state != "inconclusive" {
			t.Fatalf("state = %q, want the row left inconclusive", state)
		}
	})

	t.Run("undated evidence is refused", func(t *testing.T) {
		seed(t)

		err := store.ReconcileVerdict(ctx, digest, "scan-aged",
			urlsafety.ScanEvidence{Verdict: urlsafety.VerdictSafe})

		if err == nil {
			t.Fatal("a verdict with no evidence time was accepted")
		}
	})

	// A condemnation is retained from adoption, deliberately: it grants nothing,
	// so keeping it longer than its evidence is the conservative direction.
	t.Run("a condemnation is retained for a full lifetime", func(t *testing.T) {
		seed(t)
		evidence := urlsafety.ScanEvidence{
			Verdict:    urlsafety.VerdictMalicious,
			ObservedAt: time.Now().Add(-23 * time.Hour),
		}

		if err := store.ReconcileVerdict(ctx, digest, "scan-aged", evidence); err != nil {
			t.Fatalf("ReconcileVerdict: %v", err)
		}

		verdict, ok, err := store.LoadVerdict(ctx, url)
		if err != nil {
			t.Fatalf("LoadVerdict: %v", err)
		}
		if !ok || verdict != urlsafety.VerdictMalicious {
			t.Fatalf("verdict=%q ok=%v, an old condemnation was discarded", verdict, ok)
		}
		// And it is permanent through the denylist regardless of that expiry.
		var denied bool
		if err := pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM files.link_fetch_denylist WHERE url_digest = $1)`,
			digest).Scan(&denied); err != nil {
			t.Fatalf("read denylist: %v", err)
		}
		if !denied {
			t.Fatal("a condemnation was not published globally")
		}
		_, _ = pool.Exec(ctx, `DELETE FROM files.link_fetch_denylist WHERE url_digest = $1`, digest)
	})

}
