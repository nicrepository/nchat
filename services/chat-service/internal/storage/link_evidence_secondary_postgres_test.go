package storage_test

import (
	"errors"
	"testing"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/urlsafety"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// Issue #928 at the SQL: the provider-stated evidence ceiling, and the
// background second-opinion lane.
//
// Both are properties only a database can hold — one is about a verdict
// outliving the process that wrote it, the other is a compare-and-set across
// three statements and two schemas — so neither is provable with a fake.

const (
	evidenceURL  = "https://targets.example/evidence"
	secondaryURL = "https://targets.example/secondary"
)

// decide writes a terminal verdict for a pending target, optionally with a
// provider-stated ceiling and optionally opening the verification lane.
func decide(
	t *testing.T, f linkTargetFixture, url string,
	verdict urlsafety.Verdict, expiresAt time.Time, verifySecondary bool,
) {
	t.Helper()
	f.target(t, url, "pending", -time.Hour)
	if _, err := f.pool.Exec(f.ctx,
		`UPDATE chat.link_scans SET scan_uuid = 'scan-807' WHERE canonical_url = $1`, url); err != nil {
		t.Fatalf("bind scan: %v", err)
	}
	write := storage.LinkVerdictWrite{
		CanonicalURL:      url,
		ScanUUID:          "scan-807",
		Verdict:           verdict,
		EvidenceExpiresAt: expiresAt,
		VerifySecondary:   verifySecondary,
	}
	if err := f.store.RecordLinkVerdict(f.ctx, write); err != nil {
		t.Fatalf("RecordLinkVerdict: %v", err)
	}
}

// freshness reports what every reader sees, through the one definition they all
// share: LoadLinkTargets' Fresh flag.
func freshness(t *testing.T, f linkTargetFixture, url string) (status string, fresh bool) {
	t.Helper()
	targets, err := f.store.LoadLinkTargets(f.ctx, []string{url})
	if err != nil {
		t.Fatalf("LoadLinkTargets: %v", err)
	}
	target, ok := targets[url]
	if !ok {
		t.Fatalf("no row for %s", url)
	}
	return target.Status, target.Fresh
}

// shiftEvidenceExpiry moves the stored ceiling, which is how a test ages
// evidence without waiting.
func shiftEvidenceExpiry(t *testing.T, f linkTargetFixture, url string, offset time.Duration) {
	t.Helper()
	if _, err := f.pool.Exec(f.ctx, `
		UPDATE chat.link_scans SET evidence_expires_at = now() + ($2 * interval '1 second')
		 WHERE canonical_url = $1`, url, offset.Seconds()); err != nil {
		t.Fatalf("shift evidence expiry: %v", err)
	}
}

func TestLinkEvidenceExpiryPostgreSQL(t *testing.T) {
	f := newLinkTargetFixture(t)

	t.Run("a ceiling inside the local window ends the verdict early", func(t *testing.T) {
		f.reset(t)
		// Decided now, so decided_at alone would keep it fresh for the full
		// VerdictTTL. The provider's ceiling is what ends it.
		decide(t, f, evidenceURL, urlsafety.VerdictMalicious, time.Now().Add(time.Hour), false)
		if status, fresh := freshness(t, f, evidenceURL); status != "malicious" || !fresh {
			t.Fatalf("before expiry: %s fresh=%v", status, fresh)
		}

		shiftEvidenceExpiry(t, f, evidenceURL, -time.Second)

		status, fresh := freshness(t, f, evidenceURL)
		if fresh {
			t.Fatal("a verdict past the provider's own ceiling is still being served as fresh")
		}
		// Expiry is not a verdict: the row still says malicious, it simply
		// stopped being usable. Nothing anywhere turned it into a clearance.
		if status != "malicious" {
			t.Fatalf("status = %q, want the condemnation to remain recorded", status)
		}
	})

	t.Run("a ceiling beyond the local window changes nothing", func(t *testing.T) {
		f.reset(t)
		decide(t, f, evidenceURL, urlsafety.VerdictSafe, time.Now().Add(30*24*time.Hour), false)

		if _, fresh := freshness(t, f, evidenceURL); !fresh {
			t.Fatal("a generous ceiling must not shorten the verdict")
		}
		// And it does not lengthen it either: VerdictTTL still ends it.
		if _, err := f.pool.Exec(f.ctx, `
			UPDATE chat.link_scans SET decided_at = now() - ($2 * interval '1 second')
			 WHERE canonical_url = $1`, evidenceURL, urlsafety.VerdictTTL.Seconds()+1); err != nil {
			t.Fatalf("age the verdict: %v", err)
		}
		if _, fresh := freshness(t, f, evidenceURL); fresh {
			t.Fatal("a provider ceiling extended a verdict past VerdictTTL")
		}
	})

	t.Run("no ceiling leaves the local window in charge", func(t *testing.T) {
		f.reset(t)
		decide(t, f, evidenceURL, urlsafety.VerdictSafe, time.Time{}, false)

		var stored *time.Time
		if err := f.pool.QueryRow(f.ctx,
			`SELECT evidence_expires_at FROM chat.link_scans WHERE canonical_url = $1`,
			evidenceURL).Scan(&stored); err != nil {
			t.Fatalf("read ceiling: %v", err)
		}
		if stored != nil {
			t.Fatalf("a zero ceiling was stored as %s rather than NULL", stored)
		}
		if _, fresh := freshness(t, f, evidenceURL); !fresh {
			t.Fatal("a verdict with no stated ceiling expired immediately")
		}
	})

	// The restart case. The in-process cache is gone; the row is all that is
	// left, and it has to remember the ceiling — otherwise a verdict whose
	// evidence lapsed while the service was down comes back usable.
	t.Run("a restart does not resurrect expired evidence", func(t *testing.T) {
		f.reset(t)
		decide(t, f, evidenceURL, urlsafety.VerdictMalicious, time.Now().Add(2*time.Minute), false)
		shiftEvidenceExpiry(t, f, evidenceURL, -time.Minute)

		// A brand-new store over the same database is exactly what a restarted
		// replica has: no memory, only rows.
		restarted := storage.NewPGXMessageStore(f.pool)
		targets, err := restarted.LoadLinkTargets(f.ctx, []string{evidenceURL})
		if err != nil {
			t.Fatalf("LoadLinkTargets: %v", err)
		}
		if targets[evidenceURL].Fresh {
			t.Fatal("a restarted replica is serving evidence the provider had already expired")
		}
	})

	// And the other half: a message waiting on it is not stranded. The reopen
	// sweep puts the target back to pending — never to safe — so the pipeline
	// asks again.
	t.Run("expiry reopens the target rather than resolving it", func(t *testing.T) {
		f.reset(t)
		decide(t, f, evidenceURL, urlsafety.VerdictMalicious, time.Now().Add(time.Hour), false)
		f.message(t, "veja "+evidenceURL, evidenceURL)
		if _, err := f.pool.Exec(f.ctx,
			`UPDATE chat.messages SET status = 'pending_link_scan' WHERE sender_id = $1`,
			f.member); err != nil {
			t.Fatalf("withhold message: %v", err)
		}
		shiftEvidenceExpiry(t, f, evidenceURL, -time.Second)

		reopened, err := f.store.ReopenExpiredVerdicts(f.ctx)
		if err != nil || reopened != 1 {
			t.Fatalf("ReopenExpiredVerdicts = %d, %v; want 1", reopened, err)
		}

		var status string
		var ceiling *time.Time
		if err := f.pool.QueryRow(f.ctx,
			`SELECT status, evidence_expires_at FROM chat.link_scans WHERE canonical_url = $1`,
			evidenceURL).Scan(&status, &ceiling); err != nil {
			t.Fatalf("read reopened row: %v", err)
		}
		if status != "pending" {
			t.Fatalf("status = %q, want pending — expiry is not a verdict", status)
		}
		if ceiling != nil {
			t.Fatal("the previous answer's ceiling survived into the next attempt")
		}
	})
}

func TestLinkSecondaryVerificationPostgreSQL(t *testing.T) {
	f := newLinkTargetFixture(t)

	t.Run("a clearance opens the lane and a condemnation does not", func(t *testing.T) {
		f.reset(t)
		decide(t, f, secondaryURL, urlsafety.VerdictSafe, time.Time{}, true)

		jobs, err := f.store.ClaimDueSecondaryVerifications(f.ctx, 10)
		if err != nil || len(jobs) != 1 || jobs[0].CanonicalURL != secondaryURL {
			t.Fatalf("claim = %+v, %v; want the cleared target", jobs, err)
		}
		if jobs[0].SecondaryRef != "" {
			t.Fatalf("a fresh lane must carry no ref, got %q", jobs[0].SecondaryRef)
		}
	})

	t.Run("the claim leases, so a second worker takes nothing", func(t *testing.T) {
		f.reset(t)
		decide(t, f, secondaryURL, urlsafety.VerdictSafe, time.Time{}, true)

		first, err := f.store.ClaimDueSecondaryVerifications(f.ctx, 10)
		if err != nil || len(first) != 1 {
			t.Fatalf("first claim = %+v, %v", first, err)
		}
		second, err := f.store.ClaimDueSecondaryVerifications(f.ctx, 10)
		if err != nil || len(second) != 0 {
			t.Fatalf("second claim = %+v, %v; want nothing while the lease holds", second, err)
		}
	})

	t.Run("a lane never outlives the clearance it verifies", func(t *testing.T) {
		f.reset(t)
		decide(t, f, secondaryURL, urlsafety.VerdictSafe, time.Time{}, true)
		// Age the clearance past its window. There is nothing left to contradict,
		// so there is nothing left to ask.
		if _, err := f.pool.Exec(f.ctx, `
			UPDATE chat.link_scans
			   SET decided_at = now() - ($2 * interval '1 second'), secondary_due_at = now()
			 WHERE canonical_url = $1`,
			secondaryURL, urlsafety.VerdictTTL.Seconds()+60); err != nil {
			t.Fatalf("age the clearance: %v", err)
		}

		jobs, err := f.store.ClaimDueSecondaryVerifications(f.ctx, 10)
		if err != nil || len(jobs) != 0 {
			t.Fatalf("claim = %+v, %v; want nothing for a lapsed clearance", jobs, err)
		}
	})

	t.Run("the scan id is bound, then the condemnation flips the target", func(t *testing.T) {
		f.reset(t)
		decide(t, f, secondaryURL, urlsafety.VerdictSafe, time.Time{}, true)
		if err := f.store.RecordSecondaryRef(f.ctx, secondaryURL, "cf-scan-1"); err != nil {
			t.Fatalf("RecordSecondaryRef: %v", err)
		}
		jobs, err := f.store.ClaimDueSecondaryVerifications(f.ctx, 10)
		if err != nil || len(jobs) != 1 || jobs[0].SecondaryRef != "cf-scan-1" {
			t.Fatalf("claim = %+v, %v; want the bound ref", jobs, err)
		}

		// The transition issue #928 requires, at the SQL.
		if err := f.store.RecordSecondaryMalicious(
			f.ctx, secondaryURL, "cf-scan-1", time.Time{}); err != nil {
			t.Fatalf("RecordSecondaryMalicious: %v", err)
		}

		status, fresh := freshness(t, f, secondaryURL)
		if status != "malicious" || !fresh {
			t.Fatalf("status = %q fresh=%v, want a fresh condemnation", status, fresh)
		}
		// The global fetch denial lands in the same statement, which is what
		// stops file-service from previewing a URL chat has just condemned.
		var denied bool
		if err := f.pool.QueryRow(f.ctx,
			`SELECT EXISTS (SELECT 1 FROM files.link_fetch_denylist WHERE url_digest = $1)`,
			urlsafety.URLDigest(secondaryURL)).Scan(&denied); err != nil {
			t.Fatalf("read denylist: %v", err)
		}
		if !denied {
			t.Fatal("the condemnation did not publish the global fetch denial")
		}
		// And the lane is gone: there is nothing left to verify.
		var open bool
		if err := f.pool.QueryRow(f.ctx,
			`SELECT secondary_due_at IS NOT NULL FROM chat.link_scans WHERE canonical_url = $1`,
			secondaryURL).Scan(&open); err != nil {
			t.Fatalf("read lane: %v", err)
		}
		if open {
			t.Fatal("the lane survived the condemnation it produced")
		}
	})

	t.Run("a condemnation bound to the wrong scan changes nothing", func(t *testing.T) {
		f.reset(t)
		decide(t, f, secondaryURL, urlsafety.VerdictSafe, time.Time{}, true)
		if err := f.store.RecordSecondaryRef(f.ctx, secondaryURL, "cf-scan-1"); err != nil {
			t.Fatalf("RecordSecondaryRef: %v", err)
		}

		err := f.store.RecordSecondaryMalicious(f.ctx, secondaryURL, "cf-scan-other", time.Time{})

		if !errors.Is(err, storage.ErrLinkScanConflict) {
			t.Fatalf("err = %v, want ErrLinkScanConflict", err)
		}
		if status, _ := freshness(t, f, secondaryURL); status != "safe" {
			t.Fatalf("status = %q, want the clearance untouched", status)
		}
	})

	t.Run("settling closes the lane and leaves the clearance alone", func(t *testing.T) {
		f.reset(t)
		decide(t, f, secondaryURL, urlsafety.VerdictSafe, time.Time{}, true)

		if err := f.store.SettleSecondaryVerification(f.ctx, secondaryURL); err != nil {
			t.Fatalf("SettleSecondaryVerification: %v", err)
		}

		status, fresh := freshness(t, f, secondaryURL)
		if status != "safe" || !fresh {
			t.Fatalf("status = %q fresh=%v, want the clearance intact", status, fresh)
		}
		jobs, err := f.store.ClaimDueSecondaryVerifications(f.ctx, 10)
		if err != nil || len(jobs) != 0 {
			t.Fatalf("claim = %+v, %v; want a closed lane", jobs, err)
		}
		// Idempotent: settling again is the outcome it asks for, not an error.
		if err := f.store.SettleSecondaryVerification(f.ctx, secondaryURL); err != nil {
			t.Fatalf("second settle: %v", err)
		}
	})

	t.Run("reopening a target clears any outstanding verification", func(t *testing.T) {
		f.reset(t)
		decide(t, f, secondaryURL, urlsafety.VerdictSafe, time.Time{}, true)
		f.message(t, "veja "+secondaryURL, secondaryURL)
		if _, err := f.pool.Exec(f.ctx,
			`UPDATE chat.messages SET status = 'pending_link_scan' WHERE sender_id = $1`,
			f.member); err != nil {
			t.Fatalf("withhold message: %v", err)
		}
		if _, err := f.pool.Exec(f.ctx, `
			UPDATE chat.link_scans SET decided_at = now() - ($2 * interval '1 second')
			 WHERE canonical_url = $1`, secondaryURL, urlsafety.VerdictTTL.Seconds()+60); err != nil {
			t.Fatalf("age the clearance: %v", err)
		}

		if _, err := f.store.ReopenExpiredVerdicts(f.ctx); err != nil {
			t.Fatalf("ReopenExpiredVerdicts: %v", err)
		}

		var open bool
		if err := f.pool.QueryRow(f.ctx,
			`SELECT secondary_due_at IS NOT NULL OR secondary_scan_uuid IS NOT NULL
			   FROM chat.link_scans WHERE canonical_url = $1`, secondaryURL).Scan(&open); err != nil {
			t.Fatalf("read lane: %v", err)
		}
		if open {
			t.Fatal("a reopened target kept a verification for a clearance that no longer exists")
		}
	})
}
