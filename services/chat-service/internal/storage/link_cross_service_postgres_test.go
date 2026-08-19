package storage_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nicrepository/nchat/libs/go/platform/urlsafety"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// The cross-service fetch invariant, and the convergence and concurrency
// properties around it (issue #135, CQ-002 / CQ-003).
//
// These need a real database because every claim they make is a claim about one
// statement's atomicity, one trigger's decision, or two connections racing. A
// fake would agree with whatever the Go around it said.
//
// Opt-in like its neighbours: needs CHAT_TEST_DATABASE_URL against a _test
// database carrying the real migrations.

const crossServiceURL = "https://cross-service.example/payload"

// TestCrossServiceMaliciousInvalidationPostgreSQL is the CQ-002 proof.
//
// chat.link_scans and files.link_scans are independent authorities with
// independent lifetimes, which made this sequence reachable:
//
//	T0  files.link_scans holds SAFE for X with TTL remaining
//	T1  chat proves X MALICIOUS
//	T2  a preview reads files.link_scans, still sees SAFE, and fetches X
//
// The fix makes a condemnation global in the same statement that records it. This
// asserts both halves from the chat side: the durable denylist row, and the
// expiry of the file-service clearance that an *old* pod would otherwise still
// honour.
func TestCrossServiceMaliciousInvalidationPostgreSQL(t *testing.T) {
	ctx := t.Context()
	pool := newLinkScanTestPool(t)
	store := storage.NewPGXMessageStore(pool)
	digest := urlsafety.URLDigest(crossServiceURL)

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
		if _, err := pool.Exec(ctx,
			`DELETE FROM chat.link_scans WHERE canonical_url = $1`, crossServiceURL); err != nil {
			t.Fatalf("reset chat: %v", err)
		}
	}
	// Cleanup runs after the test's own context is cancelled, so it needs its own.
	t.Cleanup(func() {
		background := context.Background()
		_, _ = pool.Exec(background, `DELETE FROM files.link_fetch_denylist WHERE url_digest = $1`, digest)
		_, _ = pool.Exec(background, `DELETE FROM files.link_scans WHERE url_digest = $1`, digest)
		_, _ = pool.Exec(background, `DELETE FROM chat.link_scans WHERE canonical_url = $1`, crossServiceURL)
	})

	// seedFilesClearance puts file-service in the dangerous state: a live SAFE
	// verdict with plenty of TTL left.
	seedFilesClearance := func(t *testing.T) {
		t.Helper()
		if _, err := pool.Exec(ctx, `
			INSERT INTO files.link_scans
			    (url_digest, canonical_url, state, scan_uuid, verdict, verdict_expires_at)
			VALUES ($1, $2, 'done', 'files-scan', 'safe', now() + interval '10 minutes')`,
			digest, crossServiceURL); err != nil {
			t.Fatalf("seed files clearance: %v", err)
		}
	}

	seedChatInconclusive := func(t *testing.T) {
		t.Helper()
		if _, err := pool.Exec(ctx, `
			INSERT INTO chat.link_scans (canonical_url, status, scan_uuid, decided_at)
			VALUES ($1, 'inconclusive', 'chat-scan', now())
			ON CONFLICT (canonical_url) DO UPDATE
			   SET status = 'inconclusive', scan_uuid = 'chat-scan', decided_at = now()`,
			crossServiceURL); err != nil {
			t.Fatalf("seed chat inconclusive: %v", err)
		}
	}

	// filesWouldAuthoriseFetch answers the question file-service's preview gate
	// asks, in exactly the two forms that matter: the old query (a pod that
	// predates the denylist) and the new one.
	filesWouldAuthoriseFetch := func(t *testing.T) (legacy bool, current bool) {
		t.Helper()
		if err := pool.QueryRow(ctx, `
			SELECT
				EXISTS (
					SELECT 1 FROM files.link_scans
					 WHERE url_digest = $1 AND state = 'done' AND verdict_expires_at > now()
					   AND verdict = 'safe'
				),
				EXISTS (
					SELECT 1 FROM files.link_scans
					 WHERE url_digest = $1 AND state = 'done' AND verdict_expires_at > now()
					   AND verdict = 'safe'
				) AND NOT EXISTS (
					SELECT 1 FROM files.link_fetch_denylist WHERE url_digest = $1
				)`,
			digest,
		).Scan(&legacy, &current); err != nil {
			t.Fatalf("read fetch authority: %v", err)
		}
		return legacy, current
	}

	t.Run("a chat condemnation immediately revokes file-service's clearance", func(t *testing.T) {
		reset(t)
		seedFilesClearance(t)
		seedChatInconclusive(t)

		// Before: file-service would fetch. This is the state the finding describes.
		if legacy, current := filesWouldAuthoriseFetch(t); !legacy || !current {
			t.Fatalf("precondition: file-service must start out authorising the fetch (legacy=%v current=%v)",
				legacy, current)
		}

		if err := store.ReconcileLinkVerdict(ctx, crossServiceURL, "chat-scan",
			urlsafety.ScanEvidence{Verdict: urlsafety.VerdictMalicious, ObservedAt: time.Now()},
		); err != nil {
			t.Fatalf("ReconcileLinkVerdict: %v", err)
		}

		// After: neither the old query nor the new one authorises anything. No
		// waiting, no TTL expiry, no second pass — the same statement did it.
		legacy, current := filesWouldAuthoriseFetch(t)
		if legacy {
			t.Fatal("an old file-service pod would still have fetched a URL chat proved malicious")
		}
		if current {
			t.Fatal("file-service would still have fetched a URL chat proved malicious")
		}

		// The durable authority carries the fact, with its provenance.
		var source, storedURL string
		if err := pool.QueryRow(ctx,
			`SELECT source, canonical_url FROM files.link_fetch_denylist WHERE url_digest = $1`,
			digest,
		).Scan(&source, &storedURL); err != nil {
			t.Fatalf("read denylist row: %v", err)
		}
		if source != urlsafety.DenylistSourceChat || storedURL != crossServiceURL {
			t.Fatalf("denylist row = %q/%q", source, storedURL)
		}
	})

	// A later clearance must not lift the denial. The table is insert-only, so
	// there is no ordering in which SAFE wins.
	t.Run("a later clearance does not lift the denial", func(t *testing.T) {
		reset(t)
		seedChatInconclusive(t)
		if err := store.ReconcileLinkVerdict(ctx, crossServiceURL, "chat-scan",
			urlsafety.ScanEvidence{Verdict: urlsafety.VerdictMalicious, ObservedAt: time.Now()},
		); err != nil {
			t.Fatalf("ReconcileLinkVerdict: %v", err)
		}

		// file-service scans the URL itself later and gets SAFE.
		if _, err := pool.Exec(ctx, `
			INSERT INTO files.link_scans
			    (url_digest, canonical_url, state, scan_uuid, verdict, verdict_expires_at)
			VALUES ($1, $2, 'done', 'files-rescan', 'safe', now() + interval '10 minutes')
			ON CONFLICT (url_digest) DO UPDATE
			   SET state = 'done', verdict = 'safe',
			       verdict_expires_at = now() + interval '10 minutes'`,
			digest, crossServiceURL); err != nil {
			t.Fatalf("seed later clearance: %v", err)
		}

		if _, current := filesWouldAuthoriseFetch(t); current {
			t.Fatal("a later SAFE scan overrode an existing condemnation")
		}
		var rows int
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM files.link_fetch_denylist WHERE url_digest = $1`, digest,
		).Scan(&rows); err != nil {
			t.Fatalf("count denylist rows: %v", err)
		}
		if rows != 1 {
			t.Fatalf("denylist rows = %d, want the denial retained exactly once", rows)
		}
	})

	// Publishing the condemnation twice is a no-op rather than an error, so a
	// retried worker pass cannot fail on its own previous success.
	t.Run("recording a condemnation twice is idempotent", func(t *testing.T) {
		reset(t)
		seedChatInconclusive(t)
		evidence := urlsafety.ScanEvidence{
			Verdict: urlsafety.VerdictMalicious, ObservedAt: time.Now(),
		}
		if err := store.ReconcileLinkVerdict(ctx, crossServiceURL, "chat-scan", evidence); err != nil {
			t.Fatalf("first: %v", err)
		}
		// The row is no longer inconclusive, so the second attempt loses the
		// compare-and-set rather than duplicating the denial.
		if err := store.ReconcileLinkVerdict(ctx, crossServiceURL, "chat-scan", evidence); err == nil {
			t.Fatal("a decided row was reconciled again")
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

	// Both services must hash a URL identically or the veto silently never
	// matches. One shared function is the guarantee; this is the regression net.
	t.Run("both services key the denylist by the same digest", func(t *testing.T) {
		reset(t)
		seedChatInconclusive(t)
		if err := store.ReconcileLinkVerdict(ctx, crossServiceURL, "chat-scan",
			urlsafety.ScanEvidence{Verdict: urlsafety.VerdictMalicious, ObservedAt: time.Now()},
		); err != nil {
			t.Fatalf("ReconcileLinkVerdict: %v", err)
		}
		// The digest file-service would compute for the same URL finds the row chat
		// wrote.
		var found bool
		if err := pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM files.link_fetch_denylist WHERE url_digest = $1)`,
			urlsafety.URLDigest(crossServiceURL),
		).Scan(&found); err != nil {
			t.Fatalf("lookup by shared digest: %v", err)
		}
		if !found {
			t.Fatal("the digest the two services compute does not agree")
		}
	})
}

// TestRecordLinkVerdictMaliciousInvalidationPostgreSQL covers the ordinary
// submit/poll worker path. Reconciliation already publishes a condemnation to
// files.link_fetch_denylist; a first poll must make exactly the same global
// fetch decision in the same compare-and-set that records the chat verdict.
func TestRecordLinkVerdictMaliciousInvalidationPostgreSQL(t *testing.T) {
	ctx := t.Context()
	pool := newLinkScanTestPool(t)
	store := storage.NewPGXMessageStore(pool)
	const url = "https://ordinary-poll.example/payload"
	digest := urlsafety.URLDigest(url)

	reset := func(t *testing.T, status, scanUUID string) {
		t.Helper()
		for _, statement := range []string{
			`DELETE FROM files.link_fetch_denylist WHERE url_digest = $1`,
			`DELETE FROM files.link_scans WHERE url_digest = $1`,
		} {
			if _, err := pool.Exec(ctx, statement, digest); err != nil {
				t.Fatalf("reset: %v", err)
			}
		}
		if _, err := pool.Exec(ctx,
			`DELETE FROM chat.link_scans WHERE canonical_url = $1`, url); err != nil {
			t.Fatalf("reset chat: %v", err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO files.link_scans
			    (url_digest, canonical_url, state, scan_uuid, verdict, verdict_expires_at)
			VALUES ($1, $2, 'done', 'files-safe', 'safe', now() + interval '10 minutes')`,
			digest, url); err != nil {
			t.Fatalf("seed files SAFE: %v", err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO chat.link_scans (canonical_url, status, scan_uuid, decided_at)
			VALUES ($1, $2, $3, CASE WHEN $2 = 'pending' THEN NULL ELSE now() END)`,
			url, status, scanUUID); err != nil {
			t.Fatalf("seed chat scan: %v", err)
		}
	}
	t.Cleanup(func() {
		background := context.Background()
		_, _ = pool.Exec(background, `DELETE FROM files.link_fetch_denylist WHERE url_digest = $1`, digest)
		_, _ = pool.Exec(background, `DELETE FROM files.link_scans WHERE url_digest = $1`, digest)
		_, _ = pool.Exec(background, `DELETE FROM chat.link_scans WHERE canonical_url = $1`, url)
	})

	readAuthority := func(t *testing.T) (chatStatus string, denied, legacySafe, currentSafe bool) {
		t.Helper()
		if err := pool.QueryRow(ctx, `
			SELECT status FROM chat.link_scans WHERE canonical_url = $1`, url,
		).Scan(&chatStatus); err != nil {
			t.Fatalf("read chat status: %v", err)
		}
		if err := pool.QueryRow(ctx, `
			SELECT
			  EXISTS (SELECT 1 FROM files.link_fetch_denylist WHERE url_digest = $1),
			  EXISTS (
			    SELECT 1 FROM files.link_scans
			     WHERE url_digest = $1 AND state = 'done' AND verdict = 'safe'
			       AND verdict_expires_at > now()
			  ),
			  EXISTS (
			    SELECT 1 FROM files.link_scans
			     WHERE url_digest = $1 AND state = 'done' AND verdict = 'safe'
			       AND verdict_expires_at > now()
			  ) AND NOT EXISTS (
			    SELECT 1 FROM files.link_fetch_denylist WHERE url_digest = $1
			  )`, digest,
		).Scan(&denied, &legacySafe, &currentSafe); err != nil {
			t.Fatalf("read files authority: %v", err)
		}
		return chatStatus, denied, legacySafe, currentSafe
	}

	t.Run("malicious poll atomically revokes old and new readers", func(t *testing.T) {
		reset(t, "pending", "chat-poll")
		if err := store.RecordLinkVerdict(ctx, url, "chat-poll", urlsafety.VerdictMalicious); err != nil {
			t.Fatalf("RecordLinkVerdict: %v", err)
		}
		status, denied, legacySafe, currentSafe := readAuthority(t)
		if status != "malicious" || !denied || legacySafe || currentSafe {
			t.Fatalf("status=%q denied=%v legacy_safe=%v current_safe=%v", status, denied, legacySafe, currentSafe)
		}
	})

	t.Run("failed scan uuid CAS creates no denial", func(t *testing.T) {
		reset(t, "pending", "chat-current")
		if err := store.RecordLinkVerdict(ctx, url, "chat-stale", urlsafety.VerdictMalicious); err == nil {
			t.Fatal("stale scan uuid unexpectedly recorded a verdict")
		}
		status, denied, legacySafe, currentSafe := readAuthority(t)
		if status != "pending" || denied || !legacySafe || !currentSafe {
			t.Fatalf("status=%q denied=%v legacy_safe=%v current_safe=%v", status, denied, legacySafe, currentSafe)
		}
	})

	t.Run("failed status CAS creates no denial", func(t *testing.T) {
		reset(t, "safe", "chat-poll")
		if err := store.RecordLinkVerdict(ctx, url, "chat-poll", urlsafety.VerdictMalicious); err == nil {
			t.Fatal("a decided row unexpectedly accepted another verdict")
		}
		status, denied, legacySafe, currentSafe := readAuthority(t)
		if status != "safe" || denied || !legacySafe || !currentSafe {
			t.Fatalf("status=%q denied=%v legacy_safe=%v current_safe=%v", status, denied, legacySafe, currentSafe)
		}
	})

	for _, verdict := range []urlsafety.Verdict{urlsafety.VerdictSafe, urlsafety.VerdictInconclusive} {
		t.Run(string(verdict)+" does not touch the denylist", func(t *testing.T) {
			reset(t, "pending", "chat-poll")
			if err := store.RecordLinkVerdict(ctx, url, "chat-poll", verdict); err != nil {
				t.Fatalf("RecordLinkVerdict: %v", err)
			}
			status, denied, legacySafe, currentSafe := readAuthority(t)
			if status != string(verdict) || denied || !legacySafe || !currentSafe {
				t.Fatalf("status=%q denied=%v legacy_safe=%v current_safe=%v", status, denied, legacySafe, currentSafe)
			}
		})
	}
}

func TestLookupInconclusiveScansPostgreSQL(t *testing.T) {
	ctx := t.Context()
	pool := newLinkScanTestPool(t)
	store := storage.NewPGXMessageStore(pool)
	urls := []string{
		"https://lookup-inconclusive.example/eligible",
		"https://lookup-inconclusive.example/safe",
		"https://lookup-inconclusive.example/unsubmitted",
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM chat.link_scans WHERE canonical_url = ANY($1::text[])`, urls)
	})
	if _, err := pool.Exec(ctx, `
		INSERT INTO chat.link_scans (canonical_url, status, scan_uuid, decided_at)
		VALUES ($1, 'inconclusive', 'scan-eligible', now()),
		       ($2, 'safe', 'scan-safe', now()),
		       ($3, 'inconclusive', NULL, now())`, urls[0], urls[1], urls[2]); err != nil {
		t.Fatalf("seed scans: %v", err)
	}

	scans, err := store.LookupInconclusiveScans(ctx, urls)
	if err != nil {
		t.Fatalf("LookupInconclusiveScans: %v", err)
	}
	if len(scans) != 1 || scans[0].CanonicalURL != urls[0] || scans[0].ScanUUID != "scan-eligible" {
		t.Fatalf("scans = %+v, want only the submitted inconclusive scan", scans)
	}
	empty, err := store.LookupInconclusiveScans(ctx, nil)
	if err != nil || empty != nil {
		t.Fatalf("empty lookup = %+v, %v; want nil, nil", empty, err)
	}
}

// TestLinkReconcileConcurrencyPostgreSQL runs two connections at the same URL.
//
// The properties under test are all properties of predicates and of SKIP LOCKED,
// which is why this needs a real database: the manual cooldown must hand out one
// slot, the one-way door must admit one writer, and neither may produce a
// submission.
func TestLinkReconcileConcurrencyPostgreSQL(t *testing.T) {
	ctx := t.Context()
	pool := newLinkScanTestPool(t)
	store := storage.NewPGXMessageStore(pool)

	const url = "https://concurrent.example/a"
	digest := urlsafety.URLDigest(url)

	reset := func(t *testing.T) {
		t.Helper()
		if _, err := pool.Exec(ctx,
			`DELETE FROM chat.link_scans WHERE canonical_url = $1`, url); err != nil {
			t.Fatalf("reset: %v", err)
		}
		if _, err := pool.Exec(ctx,
			`DELETE FROM files.link_fetch_denylist WHERE url_digest = $1`, digest); err != nil {
			t.Fatalf("reset denylist: %v", err)
		}
		// CQ-003's cross-service lease is keyed by digest in files and is not
		// cascaded from either verdict store, so it survives the row it is about.
		if _, err := pool.Exec(ctx,
			`DELETE FROM files.link_reconcile_leases WHERE url_digest = $1`, digest); err != nil {
			t.Fatalf("reset reconcile lease: %v", err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO chat.link_scans (canonical_url, status, scan_uuid, decided_at)
			VALUES ($1, 'inconclusive', 'scan-conc', now())`, url); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	t.Cleanup(func() {
		background := context.Background()
		_, _ = pool.Exec(background, `DELETE FROM chat.link_scans WHERE canonical_url = $1`, url)
		_, _ = pool.Exec(background, `DELETE FROM files.link_fetch_denylist WHERE url_digest = $1`, digest)
		_, _ = pool.Exec(background, `DELETE FROM files.link_reconcile_leases WHERE url_digest = $1`, digest)
	})

	// Two readers press "Verificar novamente" at the same instant. Exactly one may
	// reach the provider: the cooldown is what stops a channel full of people from
	// costing a search each.
	t.Run("concurrent manual claims hand out one slot", func(t *testing.T) {
		reset(t)

		var wg sync.WaitGroup
		results := make([][]storage.InconclusiveScan, 2)
		errs := make([]error, 2)
		start := make(chan struct{})
		for i := range results {
			wg.Add(1)
			go func(index int) {
				defer wg.Done()
				<-start
				results[index], errs[index] = store.ClaimManualReconcile(ctx, []string{url})
			}(i)
		}
		close(start)
		wg.Wait()

		claimed := 0
		for i := range results {
			if errs[i] != nil {
				t.Fatalf("claim %d: %v", i, errs[i])
			}
			claimed += len(results[i])
		}
		if claimed != 1 {
			t.Fatalf("claimed %d slots concurrently, want exactly one", claimed)
		}
	})

	// Two workers hold an answer for the same scan. The one-way door admits one.
	t.Run("concurrent verdict writes admit exactly one", func(t *testing.T) {
		reset(t)
		evidence := urlsafety.ScanEvidence{
			Verdict: urlsafety.VerdictSafe, ObservedAt: time.Now(),
		}

		var wg sync.WaitGroup
		errs := make([]error, 2)
		start := make(chan struct{})
		for i := range errs {
			wg.Add(1)
			go func(index int) {
				defer wg.Done()
				<-start
				errs[index] = store.ReconcileLinkVerdict(ctx, url, "scan-conc", evidence)
			}(i)
		}
		close(start)
		wg.Wait()

		wins := 0
		for _, err := range errs {
			if err == nil {
				wins++
			}
		}
		if wins != 1 {
			t.Fatalf("%d writers succeeded, want exactly one", wins)
		}
		var status string
		if err := pool.QueryRow(ctx,
			`SELECT status FROM chat.link_scans WHERE canonical_url = $1`, url).Scan(&status); err != nil {
			t.Fatalf("read status: %v", err)
		}
		if status != "safe" {
			t.Fatalf("status = %q, want a single consistent outcome", status)
		}
	})

	// The dangerous race: two workers, one holding SAFE and one holding MALICIOUS,
	// writing at the same instant. Whatever the interleaving, a fetch must not end
	// up authorised — so if the condemnation lands at all, the denial is durable.
	t.Run("a concurrent condemnation is never lost to a clearance", func(t *testing.T) {
		for attempt := 0; attempt < 8; attempt++ {
			reset(t)
			now := time.Now()

			var wg sync.WaitGroup
			var maliciousErr, safeErr error
			start := make(chan struct{})
			wg.Add(2)
			go func() {
				defer wg.Done()
				<-start
				maliciousErr = store.ReconcileLinkVerdict(ctx, url, "scan-conc",
					urlsafety.ScanEvidence{Verdict: urlsafety.VerdictMalicious, ObservedAt: now})
			}()
			go func() {
				defer wg.Done()
				<-start
				safeErr = store.ReconcileLinkVerdict(ctx, url, "scan-conc",
					urlsafety.ScanEvidence{Verdict: urlsafety.VerdictSafe, ObservedAt: now})
			}()
			close(start)
			wg.Wait()

			// Exactly one wins the compare-and-set.
			if (maliciousErr == nil) == (safeErr == nil) {
				t.Fatalf("attempt %d: malicious=%v safe=%v, want exactly one winner",
					attempt, maliciousErr, safeErr)
			}
			// And if it was the condemnation, the global denial is durable — the
			// clearance cannot have raced past it.
			if maliciousErr == nil {
				var denied bool
				if err := pool.QueryRow(ctx,
					`SELECT EXISTS (SELECT 1 FROM files.link_fetch_denylist WHERE url_digest = $1)`,
					digest,
				).Scan(&denied); err != nil {
					t.Fatalf("read denylist: %v", err)
				}
				if !denied {
					t.Fatalf("attempt %d: a condemnation was recorded without its global denial", attempt)
				}
			}
		}
	})

	// Nothing on any of these paths may resubmit: the scan uuid and the attempt
	// history survive every concurrent write.
	t.Run("no concurrent path reopens the scan", func(t *testing.T) {
		reset(t)
		evidence := urlsafety.ScanEvidence{
			Verdict: urlsafety.VerdictSafe, ObservedAt: time.Now(),
		}
		var wg sync.WaitGroup
		for i := 0; i < 4; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, _ = store.ClaimManualReconcile(ctx, []string{url})
				_ = store.ReconcileLinkVerdict(ctx, url, "scan-conc", evidence)
			}()
		}
		wg.Wait()

		var status, scanUUID string
		if err := pool.QueryRow(ctx,
			`SELECT status, COALESCE(scan_uuid, '') FROM chat.link_scans WHERE canonical_url = $1`,
			url).Scan(&status, &scanUUID); err != nil {
			t.Fatalf("read row: %v", err)
		}
		if status == "pending" {
			t.Fatal("a concurrent path reopened the scan for submission")
		}
		if scanUUID != "scan-conc" {
			t.Fatalf("scan uuid = %q, want it preserved", scanUUID)
		}
	})
}

// TestLinkReconcileConvergenceScalePostgreSQL is the CQ-003 proof.
//
// The convergence loop used to run a fixed number of fixed-size batches, so a URL
// carried by more messages than that product left the remainder permanently on
// the old marker — and for a condemnation, that is a message still rendering a
// live link to a URL this deployment knows is malicious, forever.
//
// This drives more messages than the old ceiling allowed and asserts every one of
// them converges.
func TestLinkReconcileConvergenceScalePostgreSQL(t *testing.T) {
	ctx := t.Context()
	pool := newLinkScanTestPool(t)
	store := storage.NewPGXMessageStore(pool)

	const (
		workspace = "00000000-0000-0000-0000-000000000001"
		channel   = "e5000000-0000-4000-8000-000000000002"
		author    = "e5000000-0000-4000-8000-000000000003"
		url       = "https://scale.example/a"
		// Past the 4 000 the old batch × pass ceiling allowed, so a regression to a
		// capped drain fails here rather than in production.
		messageCount = 4201
		fingerprint  = "fp-scale"
	)

	if _, err := pool.Exec(ctx, `
		INSERT INTO auth.users (id, email, display_name)
		VALUES ($1, 'rf21-scale@e.test', 'Scale')
		ON CONFLICT (id) DO NOTHING`, author); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	for _, seed := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO chat.workspace_members (workspace_id, user_id, status)
		  VALUES ($1, $2, 'active') ON CONFLICT DO NOTHING`, []any{workspace, author}},
		{`INSERT INTO chat.channels (id, workspace_id, slug, display_name, type, status)
		  VALUES ($2, $1, 'rf21-scale', 'RF21 scale', 'public', 'active')
		  ON CONFLICT (id) DO UPDATE SET status = 'active'`, []any{workspace, channel}},
	} {
		if _, err := pool.Exec(ctx, seed.sql, seed.args...); err != nil {
			t.Fatalf("seed workspace: %v", err)
		}
	}
	t.Cleanup(func() {
		background := context.Background()
		_, _ = pool.Exec(background, `DELETE FROM chat.messages WHERE sender_id = $1`, author)
		_, _ = pool.Exec(background, `DELETE FROM chat.link_scans WHERE canonical_url = $1`, url)
		_, _ = pool.Exec(background,
			`DELETE FROM files.link_fetch_denylist WHERE url_digest = $1`, urlsafety.URLDigest(url))
	})

	seed := func(t *testing.T, state string) {
		t.Helper()
		if _, err := pool.Exec(ctx, `DELETE FROM chat.messages WHERE sender_id = $1`, author); err != nil {
			t.Fatalf("clear messages: %v", err)
		}
		if _, err := pool.Exec(ctx, `DELETE FROM chat.link_scans WHERE canonical_url = $1`, url); err != nil {
			t.Fatalf("clear scan: %v", err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO chat.link_scans (canonical_url, status, scan_uuid, decided_at)
			VALUES ($1, 'inconclusive', 'scan-scale', now())`, url); err != nil {
			t.Fatalf("seed scan: %v", err)
		}
		// Bulk-insert the messages and their associations in two statements; one row
		// at a time would make this test about round trips.
		if _, err := pool.Exec(ctx, `
			INSERT INTO chat.messages
				(id, workspace_id, channel_id, sender_id, kind, body_text, body_format,
				 status, link_safety_state, link_safety_fingerprint)
			SELECT gen_random_uuid(), $1, $2, $3, 'user', 'ver ' || $6, 'v2', 'active', $4, $5
			FROM generate_series(1, $7)`,
			workspace, channel, author, state, fingerprint, url, messageCount); err != nil {
			t.Fatalf("seed messages: %v", err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO chat.message_link_scans (message_id, canonical_url, fingerprint)
			SELECT id, $1, $2 FROM chat.messages WHERE sender_id = $3`,
			url, fingerprint, author); err != nil {
			t.Fatalf("seed associations: %v", err)
		}
	}

	// drain is what LinkReconcileService.converge does: batch until nothing is
	// left. The store's contract — only rows it actually changed are returned — is
	// what makes that terminate.
	drain := func(t *testing.T) int {
		t.Helper()
		total := 0
		for batches := 0; ; batches++ {
			if batches > messageCount {
				t.Fatal("the drain did not terminate")
			}
			changes, err := store.RefreshMessageLinkSafety(ctx, url)
			if err != nil {
				t.Fatalf("RefreshMessageLinkSafety: %v", err)
			}
			if len(changes) == 0 {
				return total
			}
			total += len(changes)
		}
	}

	countWithState := func(t *testing.T, state string) int {
		t.Helper()
		var count int
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM chat.messages WHERE sender_id = $1 AND link_safety_state = $2`,
			author, state).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", state, err)
		}
		return count
	}

	t.Run("every message converges when a link is condemned", func(t *testing.T) {
		seed(t, "inconclusive")
		if err := store.ReconcileLinkVerdict(ctx, url, "scan-scale",
			urlsafety.ScanEvidence{Verdict: urlsafety.VerdictMalicious, ObservedAt: time.Now()},
		); err != nil {
			t.Fatalf("ReconcileLinkVerdict: %v", err)
		}

		converged := drain(t)

		if converged != messageCount {
			t.Fatalf("converged %d of %d messages", converged, messageCount)
		}
		if left := countWithState(t, "inconclusive"); left != 0 {
			t.Fatalf("%d messages were abandoned on the old marker", left)
		}
		if got := countWithState(t, string(domain.MessageLinkSafetyMalicious)); got != messageCount {
			t.Fatalf("%d of %d messages carry the condemnation", got, messageCount)
		}
	})

	t.Run("every message converges when a link is cleared", func(t *testing.T) {
		seed(t, "inconclusive")
		if err := store.ReconcileLinkVerdict(ctx, url, "scan-scale",
			urlsafety.ScanEvidence{Verdict: urlsafety.VerdictSafe, ObservedAt: time.Now()},
		); err != nil {
			t.Fatalf("ReconcileLinkVerdict: %v", err)
		}

		converged := drain(t)

		if converged != messageCount {
			t.Fatalf("converged %d of %d messages", converged, messageCount)
		}
		if left := countWithState(t, "inconclusive"); left != 0 {
			t.Fatalf("%d messages were abandoned on the old marker", left)
		}
	})

	// The drain's termination argument, asserted directly: a second pass over a
	// converged URL reports nothing, so the loop cannot spin.
	t.Run("a converged url reports no further work", func(t *testing.T) {
		changes, err := store.RefreshMessageLinkSafety(ctx, url)
		if err != nil {
			t.Fatalf("RefreshMessageLinkSafety: %v", err)
		}
		if len(changes) != 0 {
			t.Fatalf("changes = %d, want none once converged", len(changes))
		}
	})
}

// TestCrossServiceReconcileLeasePostgreSQL is the CQ-003 proof.
//
// # What was wrong
//
// chat-service and file-service hold separate inconclusive rows for the same
// address and reconcile on separate schedules, so nothing stopped both from
// running Search + GET against the same URL, on the same account, inside the same
// minute. Two provider attempts, one question, and the second answer necessarily
// identical to the first — reconciliation reads a scan that has already finished.
//
// # What is asserted here
//
// Both services race for the same canonical URL through their *real* claim
// statements, on separate connections, released together. Exactly one may come
// back holding work, and — the half that is easy to get wrong — the loser must
// not have spent a reconcile attempt on a provider call it never made.
func TestCrossServiceReconcileLeasePostgreSQL(t *testing.T) {
	ctx := t.Context()
	pool := newLinkScanTestPool(t)
	store := storage.NewPGXMessageStore(pool)

	const url = "https://lease.example/contended"
	digest := urlsafety.URLDigest(url)
	messageID := "44444444-4444-4444-4444-444444444444"

	reset := func(t *testing.T) {
		t.Helper()
		for _, statement := range []string{
			`DELETE FROM files.link_reconcile_leases WHERE url_digest = $1`,
			`DELETE FROM files.link_scans WHERE url_digest = $1`,
		} {
			if _, err := pool.Exec(ctx, statement, digest); err != nil {
				t.Fatalf("reset files: %v", err)
			}
		}
		if _, err := pool.Exec(ctx,
			`DELETE FROM chat.messages WHERE id = $1::uuid`, messageID); err != nil {
			t.Fatalf("reset message: %v", err)
		}
		if _, err := pool.Exec(ctx,
			`DELETE FROM chat.link_scans WHERE canonical_url = $1`, url); err != nil {
			t.Fatalf("reset chat: %v", err)
		}
	}
	t.Cleanup(func() {
		background := context.Background()
		_, _ = pool.Exec(background, `DELETE FROM chat.messages WHERE id = $1::uuid`, messageID)
		_, _ = pool.Exec(background, `DELETE FROM chat.link_scans WHERE canonical_url = $1`, url)
		_, _ = pool.Exec(background, `DELETE FROM files.link_scans WHERE url_digest = $1`, digest)
		_, _ = pool.Exec(background, `DELETE FROM files.link_reconcile_leases WHERE url_digest = $1`, digest)
	})

	// file-service's claim, run verbatim as that service runs it. It is reproduced
	// here rather than imported because the two services are separate modules;
	// what matters is that it is the same statement shape against the same table,
	// racing the chat claim on a different connection.
	claimAsFileService := func(t *testing.T) int {
		t.Helper()
		var claimed int
		err := pool.QueryRow(ctx, `
			WITH due AS (
				SELECT ls.url_digest, ls.canonical_url
				FROM files.link_scans ls
				WHERE ls.state = 'inconclusive'
				  AND ls.scan_uuid IS NOT NULL
				  AND `+urlsafety.ReconcileLeaseAvailablePredicate("ls.url_digest")+`
				FOR UPDATE SKIP LOCKED
			),`+urlsafety.AcquireReconcileLeaseSQL(
			"due", "due.url_digest", "due.canonical_url",
			"'"+urlsafety.DenylistSourceFiles+"'",
		)+`,
			won AS (
				SELECT due.url_digest FROM due JOIN leased USING (url_digest)
			),
			spent AS (
				UPDATE files.link_scans ls
				   SET reconcile_attempts = ls.reconcile_attempts + 1, updated_at = now()
				  FROM won
				 WHERE ls.url_digest = won.url_digest
				RETURNING 1
			)
			SELECT count(*)::int FROM spent`).Scan(&claimed)
		if err != nil {
			t.Fatalf("file-service claim: %v", err)
		}
		return claimed
	}

	seed := func(t *testing.T) {
		t.Helper()
		reset(t)
		if _, err := pool.Exec(ctx, `
			INSERT INTO chat.link_scans (canonical_url, status, scan_uuid, decided_at)
			VALUES ($1, 'inconclusive', 'scan-lease', now())`, url); err != nil {
			t.Fatalf("seed chat scan: %v", err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO files.link_scans
			    (url_digest, canonical_url, state, scan_uuid, attempts, created_at, updated_at)
			VALUES ($1, $2, 'inconclusive', 'scan-lease', 7, now(), now())`,
			digest, url); err != nil {
			t.Fatalf("seed files scan: %v", err)
		}
	}

	attemptsSpent := func(t *testing.T) (chatAttempts, fileAttempts int) {
		t.Helper()
		if err := pool.QueryRow(ctx,
			`SELECT reconcile_attempts FROM chat.link_scans WHERE canonical_url = $1`,
			url).Scan(&chatAttempts); err != nil {
			t.Fatalf("read chat attempts: %v", err)
		}
		if err := pool.QueryRow(ctx,
			`SELECT reconcile_attempts FROM files.link_scans WHERE url_digest = $1`,
			digest).Scan(&fileAttempts); err != nil {
			t.Fatalf("read files attempts: %v", err)
		}
		return chatAttempts, fileAttempts
	}

	// The headline: two services, one URL, one provider attempt.
	t.Run("two services racing one url yield exactly one claim", func(t *testing.T) {
		seed(t)
		// The background claim only looks at URLs a published message is showing a
		// notice for, so the chat side needs one.
		seedLinkSafetyMessage(t, pool, messageID, "inconclusive", url)

		var wg sync.WaitGroup
		var chatClaims, fileClaims int
		var chatErr error
		start := make(chan struct{})

		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			scans, err := store.ClaimDueInconclusiveScans(ctx, 10)
			chatErr = err
			chatClaims = len(scans)
		}()
		go func() {
			defer wg.Done()
			<-start
			fileClaims = claimAsFileService(t)
		}()
		close(start)
		wg.Wait()

		if chatErr != nil {
			t.Fatalf("chat claim: %v", chatErr)
		}
		if chatClaims+fileClaims != 1 {
			t.Fatalf("chat claimed %d and files claimed %d; exactly one service may "+
				"spend a provider attempt on one url in one lease window",
				chatClaims, fileClaims)
		}

		// The blocked service must not have paid for the call it did not make.
		chatAttempts, fileAttempts := attemptsSpent(t)
		if chatAttempts+fileAttempts != 1 {
			t.Fatalf("reconcile attempts: chat=%d files=%d, want exactly one spent — "+
				"a worker the lease turned away must not consume its budget",
				chatAttempts, fileAttempts)
		}
		if chatClaims == 1 && chatAttempts != 1 {
			t.Fatalf("chat won the lease but spent %d attempts", chatAttempts)
		}
		if fileClaims == 1 && fileAttempts != 1 {
			t.Fatalf("files won the lease but spent %d attempts", fileAttempts)
		}
	})

	// The lease is a cooldown, not a permanent lock: once it lapses the address is
	// available again, to either service.
	t.Run("an expired lease is available again", func(t *testing.T) {
		seed(t)
		seedLinkSafetyMessage(t, pool, messageID, "inconclusive", url)

		if got := len(mustClaimChat(t, store, ctx)); got != 1 {
			t.Fatalf("first chat claim returned %d, want the url", got)
		}
		if got := claimAsFileService(t); got != 0 {
			t.Fatalf("file-service claimed %d while chat holds the lease", got)
		}

		if _, err := pool.Exec(ctx, `
			UPDATE files.link_reconcile_leases
			   SET leased_until = now() - interval '1 second'
			 WHERE url_digest = $1`, digest); err != nil {
			t.Fatalf("expire lease: %v", err)
		}
		if got := claimAsFileService(t); got != 1 {
			t.Fatalf("file-service claimed %d after the lease lapsed, want the url", got)
		}
		// And the holder swapped, rather than a second row appearing.
		var rows int
		var holder string
		if err := pool.QueryRow(ctx, `
			SELECT count(*)::int, max(leased_by) FROM files.link_reconcile_leases
			 WHERE url_digest = $1`, digest).Scan(&rows, &holder); err != nil {
			t.Fatalf("read lease: %v", err)
		}
		if rows != 1 || holder != urlsafety.DenylistSourceFiles {
			t.Fatalf("lease rows=%d holder=%q, want one row held by files", rows, holder)
		}
	})

	// The lease grants a provider attempt and nothing else. It is not a fetch
	// clearance, and it must never be readable as one.
	t.Run("a lease is not a fetch clearance", func(t *testing.T) {
		seed(t)
		seedLinkSafetyMessage(t, pool, messageID, "inconclusive", url)
		if got := len(mustClaimChat(t, store, ctx)); got != 1 {
			t.Fatalf("chat claim returned %d, want the url", got)
		}

		var state string
		var verdict *string
		if err := pool.QueryRow(ctx,
			`SELECT state, verdict FROM files.link_scans WHERE url_digest = $1`,
			digest).Scan(&state, &verdict); err != nil {
			t.Fatalf("read files row: %v", err)
		}
		if state != "inconclusive" || verdict != nil {
			t.Fatalf("taking a lease changed the verdict row to state=%q verdict=%v",
				state, verdict)
		}
	})
}

// TestCrossServiceTwoURLLeaseOrderPostgreSQL makes two callers present the same
// lease rows in opposite orders. A tiny row trigger widens the interval after
// each first acquisition: without the shared digest ORDER BY both transactions
// own one row and PostgreSQL detects a deadlock; with it they serialize.
func TestCrossServiceTwoURLLeaseOrderPostgreSQL(t *testing.T) {
	pool := newLinkScanTestPool(t)
	ctx := t.Context()
	urls := []string{"https://lease-order.example/a", "https://lease-order.example/b"}
	digests := [][]byte{urlsafety.URLDigest(urls[0]), urlsafety.URLDigest(urls[1])}

	t.Cleanup(func() {
		background := context.Background()
		_, _ = pool.Exec(background, `DROP TRIGGER IF EXISTS cq135_slow_lease_row ON files.link_reconcile_leases`)
		_, _ = pool.Exec(background, `DROP FUNCTION IF EXISTS files.cq135_slow_lease_row()`)
		_, _ = pool.Exec(background,
			`DELETE FROM files.link_reconcile_leases WHERE url_digest = ANY($1::bytea[])`, digests)
	})
	if _, err := pool.Exec(ctx,
		`DELETE FROM files.link_reconcile_leases WHERE url_digest = ANY($1::bytea[])`, digests); err != nil {
		t.Fatalf("reset leases: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		CREATE OR REPLACE FUNCTION files.cq135_slow_lease_row() RETURNS trigger
		LANGUAGE plpgsql AS $$
		BEGIN
		  PERFORM pg_sleep(0.05);
		  RETURN NEW;
		END $$;
		CREATE TRIGGER cq135_slow_lease_row
		AFTER INSERT OR UPDATE ON files.link_reconcile_leases
		FOR EACH ROW EXECUTE FUNCTION files.cq135_slow_lease_row()`); err != nil {
		t.Fatalf("install lease race trigger: %v", err)
	}

	claim := func(holder string, orderedDigests [][]byte, orderedURLs []string) error {
		query := `
			WITH due AS (
				SELECT url_digest, canonical_url
				FROM unnest($1::bytea[], $2::text[]) WITH ORDINALITY
				     AS rows(url_digest, canonical_url, ordinality)
				ORDER BY ordinality
			),` + urlsafety.AcquireReconcileLeaseSQL("due", "due.url_digest", "due.canonical_url", "$3") + `
			SELECT count(*) FROM leased`
		var count int
		return pool.QueryRow(ctx, query, orderedDigests, orderedURLs, holder).Scan(&count)
	}

	start := make(chan struct{})
	done := make(chan error, 2)
	go func() {
		<-start
		done <- claim(urlsafety.DenylistSourceChat, digests, urls)
	}()
	go func() {
		<-start
		done <- claim(urlsafety.DenylistSourceFiles,
			[][]byte{digests[1], digests[0]}, []string{urls[1], urls[0]})
	}()
	close(start)
	for range 2 {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("opposite-order lease claim: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("opposite-order lease claims deadlocked")
		}
	}
}

func mustClaimChat(
	t *testing.T, store *storage.PGXMessageStore, ctx context.Context,
) []storage.InconclusiveScan {
	t.Helper()
	scans, err := store.ClaimDueInconclusiveScans(ctx, 10)
	if err != nil {
		t.Fatalf("ClaimDueInconclusiveScans: %v", err)
	}
	return scans
}

// Fixtures for the lease test's chat side. The background claim only looks at
// URLs a *published* message is showing an inconclusive notice for, so proving
// anything about it needs a real message, a real channel and a real membership.
const (
	leaseWorkspace   = "00000000-0000-0000-0000-000000000001"
	leaseChannel     = "e5000000-0000-4000-8000-000000000002"
	leaseAuthor      = "e5000000-0000-4000-8000-000000000003"
	leaseFingerprint = "fp-lease"
)

func seedLinkSafetyMessage(
	t *testing.T, pool *pgxpool.Pool, messageID, linkSafety, canonicalURL string,
) {
	t.Helper()
	ctx := t.Context()
	for _, seed := range []struct {
		what string
		sql  string
		args []any
	}{
		{"user", `INSERT INTO auth.users (id, email, display_name)
		  VALUES ($1, 'rf21-lease@e.test', 'Lease') ON CONFLICT (id) DO NOTHING`,
			[]any{leaseAuthor}},
		{"membership", `INSERT INTO chat.workspace_members (workspace_id, user_id, status)
		  VALUES ($1, $2, 'active') ON CONFLICT DO NOTHING`,
			[]any{leaseWorkspace, leaseAuthor}},
		{"channel", `INSERT INTO chat.channels (id, workspace_id, slug, display_name, type, status)
		  VALUES ($2, $1, 'rf21-lease', 'RF21 lease', 'private', 'active')
		  ON CONFLICT (id) DO UPDATE SET status = 'active'`,
			[]any{leaseWorkspace, leaseChannel}},
		{"channel member", `INSERT INTO chat.channel_members (channel_id, user_id)
		  VALUES ($1, $2) ON CONFLICT DO NOTHING`,
			[]any{leaseChannel, leaseAuthor}},
		{"message", `INSERT INTO chat.messages
			(id, workspace_id, channel_id, sender_id, kind, body_text, body_format,
			 status, link_safety_state, link_safety_fingerprint)
		  VALUES ($1, $2, $3, $4, 'user', 'body', 'v2', 'active', $5, $6)
		  ON CONFLICT (id) DO UPDATE SET link_safety_state = EXCLUDED.link_safety_state`,
			[]any{messageID, leaseWorkspace, leaseChannel, leaseAuthor, linkSafety, leaseFingerprint}},
		{"association", `INSERT INTO chat.message_link_scans (message_id, canonical_url, fingerprint)
		  VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
			[]any{messageID, canonicalURL, leaseFingerprint}},
	} {
		if _, err := pool.Exec(ctx, seed.sql, seed.args...); err != nil {
			t.Fatalf("seed %s: %v", seed.what, err)
		}
	}
}
