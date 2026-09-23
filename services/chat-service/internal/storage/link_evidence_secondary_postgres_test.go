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
	// Upsert rather than insert: a target may be decided more than once in one
	// subtest, which is what a re-check after an expiry looks like.
	if _, err := f.pool.Exec(f.ctx, `
		INSERT INTO chat.link_scans (canonical_url, status, scan_uuid, deadline_at)
		VALUES ($1, 'pending', 'scan-807', now() + interval '1 hour')
		ON CONFLICT (canonical_url) DO UPDATE
		   SET status = 'pending', scan_uuid = 'scan-807', decided_at = NULL,
		       evidence_expires_at = NULL, deadline_at = now() + interval '1 hour',
		       updated_at = now()`, url); err != nil {
		t.Fatalf("seed target: %v", err)
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

// claimOneSecondary takes the lane and returns the attempt that now owns it.
func claimOneSecondary(t *testing.T, f linkTargetFixture) storage.LinkSecondaryJob {
	t.Helper()
	jobs, err := f.store.ClaimDueSecondaryVerifications(f.ctx, 1)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("claim = %+v, %v; want exactly one", jobs, err)
	}
	return jobs[0]
}

// expireSecondaryLease makes the current lease lapse, which is what a worker
// that died or stalled past its lease leaves behind. Done in the database
// rather than by waiting, so nothing here depends on a clock.
func expireSecondaryLease(t *testing.T, f linkTargetFixture, url string) {
	t.Helper()
	if _, err := f.pool.Exec(f.ctx, `
		UPDATE chat.link_scans SET secondary_due_at = now() - interval '1 second'
		 WHERE canonical_url = $1`, url); err != nil {
		t.Fatalf("expire secondary lease: %v", err)
	}
}

func secondaryLaneState(t *testing.T, f linkTargetFixture, url string) (ref string, generation int, open bool) {
	t.Helper()
	var storedRef *string
	if err := f.pool.QueryRow(f.ctx, `
		SELECT secondary_scan_uuid, secondary_generation, secondary_due_at IS NOT NULL
		  FROM chat.link_scans WHERE canonical_url = $1`, url,
	).Scan(&storedRef, &generation, &open); err != nil {
		t.Fatalf("read secondary lane: %v", err)
	}
	if storedRef != nil {
		ref = *storedRef
	}
	return ref, generation, open
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
		if jobs[0].Generation <= 0 {
			t.Fatalf("generation = %d, want the claim to issue one", jobs[0].Generation)
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
		first := claimOneSecondary(t, f)
		if err := f.store.RecordSecondaryRef(
			f.ctx, secondaryURL, first.Generation, "cf-scan-1"); err != nil {
			t.Fatalf("RecordSecondaryRef: %v", err)
		}
		expireSecondaryLease(t, f, secondaryURL)
		jobs, err := f.store.ClaimDueSecondaryVerifications(f.ctx, 10)
		if err != nil || len(jobs) != 1 || jobs[0].SecondaryRef != "cf-scan-1" {
			t.Fatalf("claim = %+v, %v; want the bound ref", jobs, err)
		}
		if jobs[0].Generation <= first.Generation {
			t.Fatalf("generation did not advance: %d then %d",
				first.Generation, jobs[0].Generation)
		}

		// The transition issue #928 requires, at the SQL.
		if err := f.store.RecordSecondaryMalicious(
			f.ctx, secondaryURL, jobs[0].Generation, "cf-scan-1", time.Time{}); err != nil {
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
		job := claimOneSecondary(t, f)
		if err := f.store.RecordSecondaryRef(
			f.ctx, secondaryURL, job.Generation, "cf-scan-1"); err != nil {
			t.Fatalf("RecordSecondaryRef: %v", err)
		}

		err := f.store.RecordSecondaryMalicious(
			f.ctx, secondaryURL, job.Generation, "cf-scan-other", time.Time{})

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
		job := claimOneSecondary(t, f)

		if err := f.store.SettleSecondaryVerification(
			f.ctx, secondaryURL, job.Generation); err != nil {
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
		// A repeated settle finds a lane that is already closed and reports the
		// conflict, rather than silently claiming to have closed one.
		if err := f.store.SettleSecondaryVerification(
			f.ctx, secondaryURL, job.Generation); !errors.Is(err, storage.ErrLinkScanConflict) {
			t.Fatalf("second settle = %v, want ErrLinkScanConflict", err)
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

// The finding the Code Quality review raised: freshness had grown the
// provider-expiry clause in freshVerdictSQL, and the other queries that decide
// the same question had not. Each subtest drives one of those queries directly
// with a verdict whose decided_at is well inside VerdictTTL and whose provider
// ceiling has already passed — the exact window where the definitions used to
// disagree.
//
// None of these waits for a sweep. The invariant is that the queries themselves
// are correct at the instant they run, whatever the sweep has or has not
// reached.
func TestExpiredProviderEvidenceIsNeverActedOnPostgreSQL(t *testing.T) {
	f := newLinkTargetFixture(t)

	// A: EnsureLinkScans must reopen a SAFE whose provider ceiling lapsed,
	// rather than leaving it decided because decided_at still looks recent.
	t.Run("EnsureLinkScans reopens a safe verdict past its provider ceiling", func(t *testing.T) {
		f.reset(t)
		decide(t, f, evidenceURL, urlsafety.VerdictSafe, time.Now().Add(time.Hour), false)
		shiftEvidenceExpiry(t, f, evidenceURL, -time.Second)

		if err := f.store.EnsureLinkScans(f.ctx, []string{evidenceURL}); err != nil {
			t.Fatalf("EnsureLinkScans: %v", err)
		}

		status, _ := freshness(t, f, evidenceURL)
		if status != "pending" {
			t.Fatalf("status = %q, want pending — the clearance was reused past its ceiling", status)
		}
	})

	// B: the same for a condemnation. Expiry is symmetric; it is the evidence
	// that lapsed, not the direction of the verdict.
	t.Run("EnsureLinkScans reopens a malicious verdict past its provider ceiling", func(t *testing.T) {
		f.reset(t)
		decide(t, f, evidenceURL, urlsafety.VerdictMalicious, time.Now().Add(time.Hour), false)
		shiftEvidenceExpiry(t, f, evidenceURL, -time.Second)

		if err := f.store.EnsureLinkScans(f.ctx, []string{evidenceURL}); err != nil {
			t.Fatalf("EnsureLinkScans: %v", err)
		}

		status, _ := freshness(t, f, evidenceURL)
		if status != "pending" {
			t.Fatalf("status = %q, want pending", status)
		}
	})

	// The send path's own verdict load. This is the query a message send
	// consults, and it was reading the same stale clearance.
	t.Run("LoadLinkVerdicts omits a verdict past its provider ceiling", func(t *testing.T) {
		f.reset(t)
		decide(t, f, evidenceURL, urlsafety.VerdictSafe, time.Now().Add(time.Hour), false)
		if verdicts, err := f.store.LoadLinkVerdicts(f.ctx, []string{evidenceURL}); err != nil {
			t.Fatalf("LoadLinkVerdicts: %v", err)
		} else if verdicts[evidenceURL] != urlsafety.VerdictSafe {
			t.Fatalf("before expiry: %v", verdicts)
		}

		shiftEvidenceExpiry(t, f, evidenceURL, -time.Second)

		verdicts, err := f.store.LoadLinkVerdicts(f.ctx, []string{evidenceURL})
		if err != nil {
			t.Fatalf("LoadLinkVerdicts: %v", err)
		}
		if got, present := verdicts[evidenceURL]; present {
			t.Fatalf("expired evidence is still served as %q", got)
		}
	})

	// C: a withheld message must not be promoted on a clearance the provider
	// has already retired, even though nothing has swept the row yet.
	t.Run("a withheld message is not promoted on expired evidence", func(t *testing.T) {
		f.reset(t)
		decide(t, f, evidenceURL, urlsafety.VerdictSafe, time.Now().Add(time.Hour), false)
		messageID := f.message(t, "veja "+evidenceURL, evidenceURL)
		if _, err := f.pool.Exec(f.ctx,
			`UPDATE chat.messages SET status = 'pending_link_scan' WHERE id = $1`,
			messageID); err != nil {
			t.Fatalf("withhold message: %v", err)
		}
		shiftEvidenceExpiry(t, f, evidenceURL, -time.Second)

		summary, err := f.store.ResolveDecidedMessages(f.ctx)
		if err != nil {
			t.Fatalf("ResolveDecidedMessages: %v", err)
		}

		if summary.Published != 0 {
			t.Fatalf("published %d message(s) on evidence the provider had retired",
				summary.Published)
		}
		var status string
		if err := f.pool.QueryRow(f.ctx,
			`SELECT status FROM chat.messages WHERE id = $1`, messageID).Scan(&status); err != nil {
			t.Fatalf("read message: %v", err)
		}
		if status != "pending_link_scan" {
			t.Fatalf("message status = %q, want it still withheld", status)
		}
	})

	// D: the boundary. Fresh requires `> now()`, so an expiry of exactly the
	// database's own now() is expired — never fresh.
	t.Run("an expiry of exactly now is expired", func(t *testing.T) {
		f.reset(t)
		decide(t, f, evidenceURL, urlsafety.VerdictSafe, time.Now().Add(time.Hour), false)
		if _, err := f.pool.Exec(f.ctx, `
			UPDATE chat.link_scans SET evidence_expires_at = now()
			 WHERE canonical_url = $1`, evidenceURL); err != nil {
			t.Fatalf("set boundary: %v", err)
		}

		if _, fresh := freshness(t, f, evidenceURL); fresh {
			t.Fatal("an expiry of exactly now() read as fresh")
		}
		verdicts, err := f.store.LoadLinkVerdicts(f.ctx, []string{evidenceURL})
		if err != nil {
			t.Fatalf("LoadLinkVerdicts: %v", err)
		}
		if _, present := verdicts[evidenceURL]; present {
			t.Fatal("an expiry of exactly now() was served by the send path")
		}
	})

	// E: a NULL ceiling is not an expired one. The provider stated no limit, so
	// the local window governs and nothing here shortens it.
	t.Run("a null ceiling leaves the local window alone", func(t *testing.T) {
		f.reset(t)
		decide(t, f, evidenceURL, urlsafety.VerdictSafe, time.Time{}, false)

		if _, fresh := freshness(t, f, evidenceURL); !fresh {
			t.Fatal("a verdict with no stated ceiling was treated as expired")
		}
		if err := f.store.EnsureLinkScans(f.ctx, []string{evidenceURL}); err != nil {
			t.Fatalf("EnsureLinkScans: %v", err)
		}
		if status, _ := freshness(t, f, evidenceURL); status != "safe" {
			t.Fatalf("status = %q, want the clearance kept", status)
		}
		verdicts, err := f.store.LoadLinkVerdicts(f.ctx, []string{evidenceURL})
		if err != nil || verdicts[evidenceURL] != urlsafety.VerdictSafe {
			t.Fatalf("verdicts = %v, err = %v", verdicts, err)
		}
	})

	// F/G: a restarted replica reading rows nothing has swept. Both halves of
	// the review's concern at once — no in-process memory, no sweep.
	t.Run("a fresh store over unswept rows refuses expired evidence", func(t *testing.T) {
		f.reset(t)
		decide(t, f, evidenceURL, urlsafety.VerdictSafe, time.Now().Add(time.Hour), false)
		shiftEvidenceExpiry(t, f, evidenceURL, -time.Second)

		restarted := storage.NewPGXMessageStore(f.pool)

		targets, err := restarted.LoadLinkTargets(f.ctx, []string{evidenceURL})
		if err != nil {
			t.Fatalf("LoadLinkTargets: %v", err)
		}
		if targets[evidenceURL].Fresh {
			t.Fatal("a restarted replica served evidence the provider had retired")
		}
		verdicts, err := restarted.LoadLinkVerdicts(f.ctx, []string{evidenceURL})
		if err != nil {
			t.Fatalf("LoadLinkVerdicts: %v", err)
		}
		if _, present := verdicts[evidenceURL]; present {
			t.Fatal("a restarted replica served an expired verdict to the send path")
		}
		// And the lane that verifies such a clearance is not claimable either.
		jobs, err := restarted.ClaimDueSecondaryVerifications(f.ctx, 10)
		if err != nil || len(jobs) != 0 {
			t.Fatalf("claim = %+v, %v; want nothing for expired evidence", jobs, err)
		}
	})
}

// The second finding: the lane's lease moved a due date but issued no identity,
// so a worker whose lease had expired could still write into the attempt that
// replaced it.
//
// Every scenario here expires the lease in the database rather than waiting for
// one, so the ordering is exact and nothing is coordinated by a sleep.
func TestSecondaryLaneWritesBelongToTheirAttemptPostgreSQL(t *testing.T) {
	f := newLinkTargetFixture(t)

	// Scenario 1: a stale worker cannot bind its scan id over the current one.
	t.Run("a stale worker cannot record a scan id", func(t *testing.T) {
		f.reset(t)
		decide(t, f, secondaryURL, urlsafety.VerdictSafe, time.Time{}, true)
		workerA := claimOneSecondary(t, f)
		expireSecondaryLease(t, f, secondaryURL)
		workerB := claimOneSecondary(t, f)

		staleErr := f.store.RecordSecondaryRef(
			f.ctx, secondaryURL, workerA.Generation, "uuid-A")
		currentErr := f.store.RecordSecondaryRef(
			f.ctx, secondaryURL, workerB.Generation, "uuid-B")

		if !errors.Is(staleErr, storage.ErrLinkScanConflict) {
			t.Fatalf("stale write = %v, want ErrLinkScanConflict", staleErr)
		}
		if currentErr != nil {
			t.Fatalf("current write = %v, want it to succeed", currentErr)
		}
		ref, _, _ := secondaryLaneState(t, f, secondaryURL)
		if ref != "uuid-B" {
			t.Fatalf("scan id = %q, want the current attempt's", ref)
		}
	})

	// Scenario 4, the other order: the stale write arrives *after* the current
	// attempt has already bound its id, and still may not replace or clear it.
	t.Run("a stale worker cannot overwrite the current scan id", func(t *testing.T) {
		f.reset(t)
		decide(t, f, secondaryURL, urlsafety.VerdictSafe, time.Time{}, true)
		workerA := claimOneSecondary(t, f)
		expireSecondaryLease(t, f, secondaryURL)
		workerB := claimOneSecondary(t, f)
		if err := f.store.RecordSecondaryRef(
			f.ctx, secondaryURL, workerB.Generation, "uuid-B"); err != nil {
			t.Fatalf("current write: %v", err)
		}

		if err := f.store.RecordSecondaryRef(
			f.ctx, secondaryURL, workerA.Generation, "uuid-A"); !errors.Is(
			err, storage.ErrLinkScanConflict) {
			t.Fatalf("stale overwrite = %v, want ErrLinkScanConflict", err)
		}

		ref, _, open := secondaryLaneState(t, f, secondaryURL)
		if ref != "uuid-B" || !open {
			t.Fatalf("lane = %q open=%v, want uuid-B on an open lane", ref, open)
		}
	})

	// Scenario 2: a stale settle must not close somebody else's lane — that
	// would discard a verification in progress and look like success.
	t.Run("a stale worker cannot settle the lane", func(t *testing.T) {
		f.reset(t)
		decide(t, f, secondaryURL, urlsafety.VerdictSafe, time.Time{}, true)
		workerA := claimOneSecondary(t, f)
		expireSecondaryLease(t, f, secondaryURL)
		workerB := claimOneSecondary(t, f)

		err := f.store.SettleSecondaryVerification(f.ctx, secondaryURL, workerA.Generation)

		if !errors.Is(err, storage.ErrLinkScanConflict) {
			t.Fatalf("stale settle = %v, want ErrLinkScanConflict", err)
		}
		_, generation, open := secondaryLaneState(t, f, secondaryURL)
		if !open || generation != workerB.Generation {
			t.Fatalf("lane closed or reassigned: open=%v generation=%d want %d",
				open, generation, workerB.Generation)
		}
	})

	// Scenario 3, the one that matters most: the current attempt's real
	// condemnation must land even with an abandoned worker interfering, because
	// losing it leaves a malicious link clickable.
	t.Run("the current attempt's condemnation lands despite a stale worker", func(t *testing.T) {
		f.reset(t)
		decide(t, f, secondaryURL, urlsafety.VerdictSafe, time.Time{}, true)
		workerA := claimOneSecondary(t, f)
		if err := f.store.RecordSecondaryRef(
			f.ctx, secondaryURL, workerA.Generation, "uuid-A"); err != nil {
			t.Fatalf("worker A ref: %v", err)
		}
		expireSecondaryLease(t, f, secondaryURL)
		workerB := claimOneSecondary(t, f)
		if err := f.store.RecordSecondaryRef(
			f.ctx, secondaryURL, workerB.Generation, "uuid-B"); err != nil {
			t.Fatalf("worker B ref: %v", err)
		}

		// A wakes up and tries everything it could still do.
		if err := f.store.SettleSecondaryVerification(
			f.ctx, secondaryURL, workerA.Generation); !errors.Is(
			err, storage.ErrLinkScanConflict) {
			t.Fatalf("stale settle = %v", err)
		}
		if err := f.store.RecordSecondaryMalicious(
			f.ctx, secondaryURL, workerA.Generation, "uuid-A", time.Time{}); !errors.Is(
			err, storage.ErrLinkScanConflict) {
			t.Fatalf("stale condemnation = %v", err)
		}

		// B's answer is the one that counts, and it goes through.
		if err := f.store.RecordSecondaryMalicious(
			f.ctx, secondaryURL, workerB.Generation, "uuid-B", time.Time{}); err != nil {
			t.Fatalf("current condemnation: %v", err)
		}

		status, fresh := freshness(t, f, secondaryURL)
		if status != "malicious" || !fresh {
			t.Fatalf("status = %q fresh=%v, want a fresh condemnation", status, fresh)
		}
		var denied bool
		if err := f.pool.QueryRow(f.ctx,
			`SELECT EXISTS (SELECT 1 FROM files.link_fetch_denylist WHERE url_digest = $1)`,
			urlsafety.URLDigest(secondaryURL)).Scan(&denied); err != nil {
			t.Fatalf("read denylist: %v", err)
		}
		if !denied {
			t.Fatal("the condemnation did not publish the global fetch denial")
		}
		_, _, open := secondaryLaneState(t, f, secondaryURL)
		if open {
			t.Fatal("the lane survived the condemnation it produced")
		}
	})

	// Scenario 5: two replicas racing the same due lane. The claim is the
	// update, so exactly one of them gets it.
	t.Run("two replicas racing one lane yield exactly one claim", func(t *testing.T) {
		f.reset(t)
		decide(t, f, secondaryURL, urlsafety.VerdictSafe, time.Time{}, true)

		start := make(chan struct{})
		type claimed struct {
			jobs []storage.LinkSecondaryJob
			err  error
		}
		results := make(chan claimed, 2)
		for i := 0; i < 2; i++ {
			go func() {
				<-start
				jobs, err := f.store.ClaimDueSecondaryVerifications(f.ctx, 10)
				results <- claimed{jobs, err}
			}()
		}
		close(start)

		total := 0
		for i := 0; i < 2; i++ {
			got := <-results
			if got.err != nil {
				t.Fatalf("claim: %v", got.err)
			}
			total += len(got.jobs)
		}
		if total != 1 {
			t.Fatalf("claims = %d, want exactly one", total)
		}
	})

	// Scenario 6: the ABA the monotonic counter closes. A lane settled at
	// generation N and reopened later must not be writable by the worker
	// abandoned by the first generation N.
	t.Run("a reopened lane does not resurrect an abandoned attempt", func(t *testing.T) {
		f.reset(t)
		decide(t, f, secondaryURL, urlsafety.VerdictSafe, time.Time{}, true)
		abandoned := claimOneSecondary(t, f)
		if err := f.store.SettleSecondaryVerification(
			f.ctx, secondaryURL, abandoned.Generation); err != nil {
			t.Fatalf("settle: %v", err)
		}

		// The target is checked again and cleared again, opening a new lane.
		decide(t, f, secondaryURL, urlsafety.VerdictSafe, time.Time{}, true)

		err := f.store.RecordSecondaryRef(
			f.ctx, secondaryURL, abandoned.Generation, "uuid-abandoned")

		if !errors.Is(err, storage.ErrLinkScanConflict) {
			t.Fatalf("the abandoned attempt wrote into a reopened lane: %v", err)
		}
		_, generation, _ := secondaryLaneState(t, f, secondaryURL)
		if generation <= abandoned.Generation {
			t.Fatalf("generation = %d, want it past the abandoned %d",
				generation, abandoned.Generation)
		}
	})
}

// The window the second Code Quality review found: a lane is claimed while its
// clearance is fresh, the worker then spends a Cloudflare exchange, and the
// clearance lapses before the answer comes back.
//
// The claim's freshness check cannot cover that — a claim is not a write. The
// condemnation itself has to revalidate, in the same statement, or a target
// gets condemned on the authority of evidence that no longer exists.
func TestSecondaryCondemnationRequiresALiveClearancePostgreSQL(t *testing.T) {
	f := newLinkTargetFixture(t)

	// denylistRows counts the global fetch denial for this URL, which is the
	// side effect that must not appear when the compare-and-set loses.
	denylistRows := func(t *testing.T) int {
		t.Helper()
		var count int
		if err := f.pool.QueryRow(f.ctx,
			`SELECT count(*) FROM files.link_fetch_denylist WHERE url_digest = $1`,
			urlsafety.URLDigest(secondaryURL)).Scan(&count); err != nil {
			t.Fatalf("read denylist: %v", err)
		}
		return count
	}

	// claimWithRef opens a lane, claims it, binds a scan id, and returns the
	// attempt — the state a worker holds when it calls Cloudflare.
	claimWithRef := func(t *testing.T, expiresAt time.Time) storage.LinkSecondaryJob {
		t.Helper()
		decide(t, f, secondaryURL, urlsafety.VerdictSafe, expiresAt, true)
		job := claimOneSecondary(t, f)
		if err := f.store.RecordSecondaryRef(
			f.ctx, secondaryURL, job.Generation, "cf-scan-1"); err != nil {
			t.Fatalf("RecordSecondaryRef: %v", err)
		}
		return job
	}

	// Both ways a clearance can lapse between the claim and the answer. The
	// provider's own ceiling, and the local window — the shared predicate has to
	// cover both, and a caller must not be able to satisfy one and skip the other.
	for name, lapse := range map[string]func(t *testing.T){
		"the provider ceiling passed": func(t *testing.T) {
			shiftEvidenceExpiry(t, f, secondaryURL, -time.Second)
		},
		"the local window passed": func(t *testing.T) {
			if _, err := f.pool.Exec(f.ctx, `
				UPDATE chat.link_scans SET decided_at = now() - ($2 * interval '1 second')
				 WHERE canonical_url = $1`,
				secondaryURL, urlsafety.VerdictTTL.Seconds()+60); err != nil {
				t.Fatalf("age the clearance: %v", err)
			}
		},
	} {
		t.Run("a condemnation is refused after "+name, func(t *testing.T) {
			f.reset(t)
			// A ceiling far in the future, so only the lapse under test ends it.
			job := claimWithRef(t, time.Now().Add(time.Hour))
			before := denylistRows(t)

			lapse(t)

			err := f.store.RecordSecondaryMalicious(
				f.ctx, secondaryURL, job.Generation, "cf-scan-1", time.Time{})

			if !errors.Is(err, storage.ErrLinkScanConflict) {
				t.Fatalf("err = %v, want ErrLinkScanConflict", err)
			}
			// The verdict did not flip. The target is stale, which the ordinary
			// reopen path resolves by asking again — not by taking this answer.
			var status string
			if err := f.pool.QueryRow(f.ctx,
				`SELECT status FROM chat.link_scans WHERE canonical_url = $1`,
				secondaryURL).Scan(&status); err != nil {
				t.Fatalf("read status: %v", err)
			}
			if status != "safe" {
				t.Fatalf("status = %q, want the stale clearance left for the reopen path", status)
			}
			// And none of the condemnation's side effects happened. The CTEs all
			// select from the UPDATE that won, so zero rows updated must mean zero
			// rows anywhere else.
			if after := denylistRows(t); after != before {
				t.Fatalf("denylist rows %d -> %d; a refused condemnation published a denial",
					before, after)
			}
			var laneOpen bool
			if err := f.pool.QueryRow(f.ctx,
				`SELECT secondary_due_at IS NOT NULL FROM chat.link_scans WHERE canonical_url = $1`,
				secondaryURL).Scan(&laneOpen); err != nil {
				t.Fatalf("read lane: %v", err)
			}
			if !laneOpen {
				t.Fatal("a refused condemnation closed the lane it could not conclude")
			}
		})
	}

	// The legitimate path, so the fix is not just "refuse everything": a live
	// clearance still gets condemned, with every side effect intact.
	t.Run("a condemnation on a live clearance still goes through", func(t *testing.T) {
		f.reset(t)
		job := claimWithRef(t, time.Time{})

		if err := f.store.RecordSecondaryMalicious(
			f.ctx, secondaryURL, job.Generation, "cf-scan-1", time.Time{}); err != nil {
			t.Fatalf("RecordSecondaryMalicious: %v", err)
		}

		status, fresh := freshness(t, f, secondaryURL)
		if status != "malicious" || !fresh {
			t.Fatalf("status = %q fresh=%v, want a fresh condemnation", status, fresh)
		}
		if denylistRows(t) != 1 {
			t.Fatal("the condemnation did not publish the global fetch denial")
		}
		_, _, open := secondaryLaneState(t, f, secondaryURL)
		if open {
			t.Fatal("the lane survived the condemnation it produced")
		}
	})
}
