package storage_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nicrepository/nchat/libs/go/platform/urlsafety"
	"github.com/nicrepository/nchat/services/file-service/internal/service"
	"github.com/nicrepository/nchat/services/file-service/internal/storage"
)

// files.link_scans, against a real database.
//
// Everything this store does is a claim about SQL that a fake cannot make: a
// lease another worker cannot take, a compare-and-set a late answer loses, a
// budget reserved in one statement so two replicas cannot both fit through the
// gap, a CHECK constraint that refuses a half-written verdict. A fake would
// agree with whatever the Go around the query said, and the query is the thing
// being defended.
//
// Opt-in like the other integration suites here: it needs FILE_TEST_DATABASE_URL
// pointing at a *_test database carrying the real migrations, applied by
// scripts/db/migrate.sh. It does not build its own schema — the RF-21 tables are
// what migrations 000006 and 000007 produce, and testing a hand-written copy of
// them would test the copy.
func newLinkScanTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
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
	return pool
}

func TestLinkScanStorePostgreSQL(t *testing.T) {
	ctx := t.Context()
	pool := newLinkScanTestPool(t)
	store := storage.NewPGXLinkScanStore(pool)

	const (
		firstURL  = "https://files.example/first"
		secondURL = "https://files.example/second"
	)
	// No ceiling at all, so a case that is not about capacity is not accidentally
	// about capacity.
	unlimited := service.LinkScanCapacity{}

	// Every case states its own starting queue, because a claim, a budget and a
	// backlog are all computed from what is already there.
	reset := func(t *testing.T) {
		t.Helper()
		for _, statement := range []string{
			`DELETE FROM files.link_scans`,
			`DELETE FROM files.link_scan_budget_usage`,
		} {
			if _, err := pool.Exec(ctx, statement); err != nil {
				t.Fatalf("reset: %v", err)
			}
		}
	}
	t.Cleanup(func() {
		background := context.Background()
		_, _ = pool.Exec(background, `DELETE FROM files.link_scans`)
		_, _ = pool.Exec(background, `DELETE FROM files.link_scan_budget_usage`)
	})

	claimOne := func(t *testing.T, url string) service.LinkScanJob {
		t.Helper()
		jobs, err := store.ClaimDueScans(ctx, 10)
		if err != nil {
			t.Fatalf("ClaimDueScans: %v", err)
		}
		for _, job := range jobs {
			if job.CanonicalURL == url {
				return job
			}
		}
		t.Fatalf("%s was not claimed; got %+v", url, jobs)
		return service.LinkScanJob{}
	}

	// Submit-then-poll, end to end. Each step reads the row back rather than
	// assuming the previous one landed.
	t.Run("a queued url is claimed, submitted, polled and decided", func(t *testing.T) {
		reset(t)
		admission, err := store.AdmitScan(ctx, firstURL, unlimited)
		if err != nil {
			t.Fatalf("AdmitScan: %v", err)
		}
		if !admission.Allowed() || admission.NewScanCost != 1 {
			t.Fatalf("admission = %+v, want a new url charged once", admission)
		}

		byState, oldest, err := store.Backlog(ctx)
		if err != nil {
			t.Fatalf("Backlog: %v", err)
		}
		if byState["submit_pending"] != 1 || oldest < 0 {
			t.Fatalf("backlog = %v (oldest %v), want one url awaiting submission", byState, oldest)
		}

		job := claimOne(t, firstURL)
		if job.State != "submit_pending" || job.Attempts != 1 {
			t.Fatalf("job = %+v, want a first claim of an unsubmitted url", job)
		}
		if !job.SubmitStartedAt.IsZero() {
			t.Fatalf("SubmitStartedAt = %v, want no attempt outstanding", job.SubmitStartedAt)
		}

		// The intent is durable before the provider is called; that is what makes
		// the gap recoverable instead of resubmittable.
		generation, err := store.BeginSubmit(ctx, job.URLDigest, job.SubmitGeneration)
		if err != nil {
			t.Fatalf("BeginSubmit: %v", err)
		}
		if generation != job.SubmitGeneration+1 {
			t.Fatalf("generation = %d, want %d", generation, job.SubmitGeneration+1)
		}

		if err := store.RecordSubmission(ctx, job.URLDigest, "scan-first", generation); err != nil {
			t.Fatalf("RecordSubmission: %v", err)
		}

		// Nothing is a verdict until one is written.
		if _, found, err := store.LoadVerdict(ctx, firstURL); err != nil || found {
			t.Fatalf("LoadVerdict during polling = found %v (%v)", found, err)
		}

		if err := store.RecordVerdict(ctx, job.URLDigest, "scan-first",
			urlsafety.VerdictSafe, time.Hour); err != nil {
			t.Fatalf("RecordVerdict: %v", err)
		}
		verdict, found, err := store.LoadVerdict(ctx, firstURL)
		if err != nil || !found || verdict != urlsafety.VerdictSafe {
			t.Fatalf("LoadVerdict = %q, %v, %v", verdict, found, err)
		}

		// A decided row is out of the queue, which is what stops a cleared URL
		// from being re-scanned on every pass.
		byState, _, err = store.Backlog(ctx)
		if err != nil {
			t.Fatalf("Backlog: %v", err)
		}
		if len(byState) != 0 {
			t.Fatalf("backlog = %v, want a decided url gone from it", byState)
		}
	})

	// The compare-and-set, from the losing side: a worker whose lease expired
	// while the provider was answering, arriving late about a row that moved on.
	t.Run("a superseded worker cannot overwrite the current attempt", func(t *testing.T) {
		reset(t)
		if _, err := store.AdmitScan(ctx, firstURL, unlimited); err != nil {
			t.Fatalf("AdmitScan: %v", err)
		}
		job := claimOne(t, firstURL)

		stale := job.SubmitGeneration
		generation, err := store.BeginSubmit(ctx, job.URLDigest, stale)
		if err != nil {
			t.Fatalf("BeginSubmit: %v", err)
		}
		if _, err := store.BeginSubmit(ctx, job.URLDigest, stale); !errors.Is(err, service.ErrLinkScanConflict) {
			t.Fatalf("BeginSubmit on a stale generation: %v, want ErrLinkScanConflict", err)
		}
		if err := store.RecordSubmission(ctx, job.URLDigest, "scan-stale", stale); !errors.Is(err, service.ErrLinkScanConflict) {
			t.Fatalf("RecordSubmission on a stale generation: %v, want ErrLinkScanConflict", err)
		}

		if err := store.RecordSubmission(ctx, job.URLDigest, "scan-current", generation); err != nil {
			t.Fatalf("RecordSubmission: %v", err)
		}
		// A verdict is bound to the scan that produced it, so an answer about a
		// scan this row no longer carries decides nothing.
		if err := store.RecordVerdict(ctx, job.URLDigest, "scan-stale",
			urlsafety.VerdictMalicious, time.Hour); !errors.Is(err, service.ErrLinkScanConflict) {
			t.Fatalf("RecordVerdict for a superseded scan: %v, want ErrLinkScanConflict", err)
		}
		// And a second submission finds the row already polling.
		if err := store.RecordSubmission(ctx, job.URLDigest, "scan-second", generation); !errors.Is(err, service.ErrLinkScanConflict) {
			t.Fatalf("a second submission: %v, want ErrLinkScanConflict", err)
		}
	})

	// A current generation is not permission to start over: submitting means the
	// provider call may already have begun. This is a real PostgreSQL CAS check,
	// so a future service dispatch mistake cannot turn into a second submission.
	t.Run("an outstanding submission cannot begin another", func(t *testing.T) {
		reset(t)
		if _, err := store.AdmitScan(ctx, firstURL, unlimited); err != nil {
			t.Fatalf("AdmitScan: %v", err)
		}
		job := claimOne(t, firstURL)
		generation, err := store.BeginSubmit(ctx, job.URLDigest, job.SubmitGeneration)
		if err != nil {
			t.Fatalf("first BeginSubmit: %v", err)
		}

		var beforeStarted time.Time
		if err := pool.QueryRow(ctx,
			`SELECT submit_attempt_started_at FROM files.link_scans WHERE url_digest = $1`, job.URLDigest,
		).Scan(&beforeStarted); err != nil {
			t.Fatalf("read original submission: %v", err)
		}
		if _, err := store.BeginSubmit(ctx, job.URLDigest, generation); !errors.Is(err, service.ErrLinkScanConflict) {
			t.Fatalf("BeginSubmit on submitting = %v, want ErrLinkScanConflict", err)
		}

		var afterGeneration int
		var afterStarted time.Time
		if err := pool.QueryRow(ctx,
			`SELECT submit_generation, submit_attempt_started_at FROM files.link_scans WHERE url_digest = $1`, job.URLDigest,
		).Scan(&afterGeneration, &afterStarted); err != nil {
			t.Fatalf("read submission after conflict: %v", err)
		}
		if afterGeneration != generation || !afterStarted.Equal(beforeStarted) {
			t.Fatalf("submission changed after conflict: generation=%d started=%v, want %d %v", afterGeneration, afterStarted, generation, beforeStarted)
		}
	})

	// The uncertain state is the one a restart must never resubmit from. It is
	// left by adopting an id the provider's own search identified.
	t.Run("an uncertain attempt adopts the id the search recovered", func(t *testing.T) {
		reset(t)
		if _, err := store.AdmitScan(ctx, firstURL, unlimited); err != nil {
			t.Fatalf("AdmitScan: %v", err)
		}
		job := claimOne(t, firstURL)
		generation, err := store.BeginSubmit(ctx, job.URLDigest, job.SubmitGeneration)
		if err != nil {
			t.Fatalf("BeginSubmit: %v", err)
		}
		if err := store.MarkSubmitUncertain(ctx, job.URLDigest, generation); err != nil {
			t.Fatalf("MarkSubmitUncertain: %v", err)
		}

		// Reported as its own state: an uncertain backlog is exactly what an
		// operator must be able to see growing.
		byState, _, err := store.Backlog(ctx)
		if err != nil {
			t.Fatalf("Backlog: %v", err)
		}
		if byState["submit_uncertain"] != 1 {
			t.Fatalf("backlog = %v, want the outstanding attempt reported", byState)
		}

		if err := store.AdoptScanUUID(ctx, job.URLDigest, "scan-recovered", generation+1); !errors.Is(err, service.ErrLinkScanConflict) {
			t.Fatalf("AdoptScanUUID on the wrong generation: %v, want ErrLinkScanConflict", err)
		}
		if err := store.AdoptScanUUID(ctx, job.URLDigest, "scan-recovered", generation); err != nil {
			t.Fatalf("AdoptScanUUID: %v", err)
		}
		// The backoff the claim applied is waited out here rather than slept
		// through; what matters is that the adopted row comes back as polling.
		if _, err := pool.Exec(ctx,
			`UPDATE files.link_scans SET next_attempt_at = NULL`); err != nil {
			t.Fatalf("make due: %v", err)
		}
		recovered := claimOne(t, firstURL)
		if recovered.State != "polling" || recovered.ScanUUID != "scan-recovered" {
			t.Fatalf("job = %+v, want the recovered id adopted", recovered)
		}
	})

	// A claim is a lease. Two workers cannot hold one row, which is what stops
	// one URL from becoming two provider submissions.
	t.Run("a claimed url is not offered again until its lease expires", func(t *testing.T) {
		reset(t)
		if _, err := store.AdmitScan(ctx, firstURL, unlimited); err != nil {
			t.Fatalf("AdmitScan: %v", err)
		}
		claimOne(t, firstURL)

		jobs, err := store.ClaimDueScans(ctx, 10)
		if err != nil {
			t.Fatalf("ClaimDueScans: %v", err)
		}
		if len(jobs) != 0 {
			t.Fatalf("a leased url was handed to a second worker: %+v", jobs)
		}
		// And a batch of nothing is not a query at all.
		if jobs, err := store.ClaimDueScans(ctx, 0); err != nil || jobs != nil {
			t.Fatalf("ClaimDueScans(0) = %v, %v, want no work", jobs, err)
		}
	})

	// The unit that costs money is a canonical URL nobody has a live verdict or
	// an active job for. Everything else is free, because it is free for the
	// provider too.
	t.Run("only genuinely new provider work is charged", func(t *testing.T) {
		reset(t)
		first, err := store.AdmitScan(ctx, firstURL, unlimited)
		if err != nil {
			t.Fatalf("AdmitScan: %v", err)
		}
		if first.NewScanCost != 1 {
			t.Fatalf("cost = %d, want the first sighting charged", first.NewScanCost)
		}

		again, err := store.AdmitScan(ctx, firstURL, unlimited)
		if err != nil {
			t.Fatalf("AdmitScan: %v", err)
		}
		if !again.Allowed() || again.NewScanCost != 0 {
			t.Fatalf("admission = %+v, want an already-queued url to cost nothing", again)
		}

		// Decided and still live: also free, and still not re-opened — re-opening
		// a decided row on request is how a malicious verdict gets cleared.
		job := claimOne(t, firstURL)
		generation, err := store.BeginSubmit(ctx, job.URLDigest, job.SubmitGeneration)
		if err != nil {
			t.Fatalf("BeginSubmit: %v", err)
		}
		if err := store.RecordSubmission(ctx, job.URLDigest, "scan-x", generation); err != nil {
			t.Fatalf("RecordSubmission: %v", err)
		}
		if err := store.RecordVerdict(ctx, job.URLDigest, "scan-x",
			urlsafety.VerdictMalicious, time.Hour); err != nil {
			t.Fatalf("RecordVerdict: %v", err)
		}
		decided, err := store.AdmitScan(ctx, firstURL, unlimited)
		if err != nil {
			t.Fatalf("AdmitScan: %v", err)
		}
		if !decided.Allowed() || decided.NewScanCost != 0 {
			t.Fatalf("admission = %+v, want a live verdict to cost nothing", decided)
		}
		if verdict, found, err := store.LoadVerdict(ctx, firstURL); err != nil ||
			!found || verdict != urlsafety.VerdictMalicious {
			t.Fatalf("the condemnation was disturbed: %q, %v, %v", verdict, found, err)
		}

		// Once the verdict lapses it is neither served nor trusted: the row is
		// re-opened and charged again, which is the only direction this moves.
		if _, err := pool.Exec(ctx,
			`UPDATE files.link_scans SET verdict_expires_at = now() - interval '1 hour'`); err != nil {
			t.Fatalf("expire verdict: %v", err)
		}
		if _, found, err := store.LoadVerdict(ctx, firstURL); err != nil || found {
			t.Fatalf("a lapsed verdict was served: found %v (%v)", found, err)
		}
		reopened, err := store.AdmitScan(ctx, firstURL, unlimited)
		if err != nil {
			t.Fatalf("AdmitScan: %v", err)
		}
		if !reopened.Allowed() || reopened.NewScanCost != 1 {
			t.Fatalf("admission = %+v, want a lapsed verdict re-scanned and charged", reopened)
		}
		var state string
		if err := pool.QueryRow(ctx,
			`SELECT state FROM files.link_scans`).Scan(&state); err != nil {
			t.Fatalf("read state: %v", err)
		}
		if state != "submit_pending" {
			t.Fatalf("state = %q, want the lapsed row back in the queue", state)
		}
	})

	// A full queue and a spent window are refusals, not failures: the caller is
	// told which ceiling it hit so the metric can say so.
	t.Run("capacity ceilings refuse new work with a reason", func(t *testing.T) {
		reset(t)
		if _, err := store.AdmitScan(ctx, firstURL, unlimited); err != nil {
			t.Fatalf("AdmitScan: %v", err)
		}
		backlogged, err := store.AdmitScan(ctx, secondURL,
			service.LinkScanCapacity{MaxPendingJobs: 1})
		if err != nil {
			t.Fatalf("AdmitScan: %v", err)
		}
		if backlogged.Allowed() || backlogged.Result != service.AdmissionBacklog {
			t.Fatalf("admission = %+v, want a full backlog to refuse", backlogged)
		}

		reset(t)
		budget := service.LinkScanCapacity{NewURLBudget: 1, BudgetWindow: time.Hour}
		if admission, err := store.AdmitScan(ctx, firstURL, budget); err != nil || !admission.Allowed() {
			t.Fatalf("the first url of the window was refused: %+v (%v)", admission, err)
		}
		spent, err := store.AdmitScan(ctx, secondURL, budget)
		if err != nil {
			t.Fatalf("AdmitScan: %v", err)
		}
		if spent.Allowed() || spent.Result != service.AdmissionServiceBudget {
			t.Fatalf("admission = %+v, want the spent window to refuse", spent)
		}
		// The refusal wrote nothing: a refused URL must not sit in the queue as
		// work nobody paid for.
		var queued int
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM files.link_scans`).Scan(&queued); err != nil {
			t.Fatalf("count jobs: %v", err)
		}
		if queued != 1 {
			t.Fatalf("%d rows queued, want only the admitted one", queued)
		}
	})

	// The deployment-wide provider allowance, shared by every replica because a
	// per-process limiter is not a limit at all.
	t.Run("the provider allowance is enforced across callers", func(t *testing.T) {
		reset(t)
		const window = time.Hour
		for attempt := 1; attempt <= 2; attempt++ {
			reserved, err := store.ReserveProviderSubmit(ctx, 2, window)
			if err != nil {
				t.Fatalf("ReserveProviderSubmit: %v", err)
			}
			if !reserved {
				t.Fatalf("submission %d of 2 was refused", attempt)
			}
		}
		if reserved, err := store.ReserveProviderSubmit(ctx, 2, window); err != nil || reserved {
			t.Fatalf("a third submission was allowed against an allowance of two (%v)", err)
		}
		// An unset limit is not a limit of zero: it disables the ceiling.
		if reserved, err := store.ReserveProviderSubmit(ctx, 0, window); err != nil || !reserved {
			t.Fatalf("ReserveProviderSubmit(0) = %v, %v, want the ceiling disabled", reserved, err)
		}

		// Retention: a window nothing can count into any more is dropped.
		if _, err := pool.Exec(ctx, `
			UPDATE files.link_scan_budget_usage
			   SET window_start = now() - interval '30 days'`); err != nil {
			t.Fatalf("age window: %v", err)
		}
		if err := store.PruneLinkScanBudget(ctx, 0); err != nil {
			t.Fatalf("PruneLinkScanBudget(0): %v", err)
		}
		if err := store.PruneLinkScanBudget(ctx, 24*time.Hour); err != nil {
			t.Fatalf("PruneLinkScanBudget: %v", err)
		}
		var remaining int
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM files.link_scan_budget_usage`).Scan(&remaining); err != nil {
			t.Fatalf("count windows: %v", err)
		}
		if remaining != 0 {
			t.Fatalf("%d expired window(s) survived the prune", remaining)
		}
	})

	// The strictness that decides a clearance: anything that is not an explicit
	// verdict is refused before it can be written.
	t.Run("only a final verdict may be stored", func(t *testing.T) {
		reset(t)
		if _, err := store.AdmitScan(ctx, firstURL, unlimited); err != nil {
			t.Fatalf("AdmitScan: %v", err)
		}
		job := claimOne(t, firstURL)
		if err := store.RecordVerdict(ctx, job.URLDigest, "scan-any",
			urlsafety.VerdictUnknown, time.Hour); err == nil {
			t.Fatal("a non-final verdict was accepted")
		}
		if _, found, err := store.LoadVerdict(ctx, firstURL); err != nil || found {
			t.Fatalf("a refused verdict left a row behind: found %v (%v)", found, err)
		}
	})

	// The RF-21 incident this migration fixes: Cloudflare answers HTTP 200,
	// task.status=finished, task.success=false, hasVerdicts=false. The provider
	// layer reports that as VerdictInconclusive, and the row must settle into a
	// terminal state that a real claim query, backlog query and re-admission
	// all agree is done — not just what a Go fake believes.
	t.Run("an inconclusive verdict is terminal, excluded from claims and backlog, and never reopened", func(t *testing.T) {
		reset(t)
		if _, err := store.AdmitScan(ctx, firstURL, unlimited); err != nil {
			t.Fatalf("AdmitScan: %v", err)
		}
		job := claimOne(t, firstURL)
		generation, err := store.BeginSubmit(ctx, job.URLDigest, job.SubmitGeneration)
		if err != nil {
			t.Fatalf("BeginSubmit: %v", err)
		}
		if err := store.RecordSubmission(ctx, job.URLDigest, "scan-inconclusive", generation); err != nil {
			t.Fatalf("RecordSubmission: %v", err)
		}

		if err := store.RecordVerdict(ctx, job.URLDigest, "scan-inconclusive",
			urlsafety.VerdictInconclusive, time.Hour); err != nil {
			t.Fatalf("RecordVerdict(inconclusive): %v", err)
		}

		// Never a usable verdict, immediately after settling.
		if _, found, err := store.LoadVerdict(ctx, firstURL); err != nil || found {
			t.Fatalf("LoadVerdict = found %v (%v), want inconclusive to never resolve", found, err)
		}

		// Never claimed again — the real ClaimDueScans query, not a fake's map.
		jobs, err := store.ClaimDueScans(ctx, 10)
		if err != nil {
			t.Fatalf("ClaimDueScans: %v", err)
		}
		for _, j := range jobs {
			if j.CanonicalURL == firstURL {
				t.Fatalf("an inconclusive row was claimed again: %+v", j)
			}
		}

		// Never counted in the backlog gauges. The database guard also makes the
		// terminal row immutable to a stale or legacy UPDATE.
		byState, _, err := store.Backlog(ctx)
		if err != nil {
			t.Fatalf("Backlog: %v", err)
		}
		if _, present := byState["inconclusive"]; present {
			t.Fatalf("backlog counted an inconclusive row: %v", byState)
		}
		tag, err := pool.Exec(ctx,
			`UPDATE files.link_scans SET updated_at = now() - interval '30 days' WHERE url_digest = $1`,
			job.URLDigest)
		if err != nil {
			t.Fatalf("attempt to mutate terminal row: %v", err)
		}
		if tag.RowsAffected() != 0 {
			t.Fatal("the terminal guard allowed an inconclusive row to be mutated")
		}
		byState, _, err = store.Backlog(ctx)
		if err != nil {
			t.Fatalf("Backlog after rejected mutation: %v", err)
		}
		if _, present := byState["inconclusive"]; present {
			t.Fatalf("backlog counted an inconclusive row after rejected mutation: %v", byState)
		}
		jobs, err = store.ClaimDueScans(ctx, 10)
		if err != nil {
			t.Fatalf("ClaimDueScans after rejected mutation: %v", err)
		}
		for _, j := range jobs {
			if j.CanonicalURL == firstURL {
				t.Fatalf("an inconclusive row was claimed after rejected mutation: %+v", j)
			}
		}

		// A later preview request for the same URL must not reopen it — AdmitScan
		// only reopens a 'done' row past its expiry, and inconclusive is never
		// 'done'.
		admission, err := store.AdmitScan(ctx, firstURL, unlimited)
		if err != nil {
			t.Fatalf("AdmitScan (re-request): %v", err)
		}
		if !admission.Allowed() || admission.NewScanCost != 0 {
			t.Fatalf("admission = %+v, want a free admit that reopens nothing", admission)
		}
		var state string
		if err := pool.QueryRow(ctx,
			`SELECT state FROM files.link_scans WHERE url_digest = $1`, job.URLDigest,
		).Scan(&state); err != nil {
			t.Fatalf("read state after re-request: %v", err)
		}
		if state != "inconclusive" {
			t.Fatalf("state = %q, a re-request reopened the row", state)
		}
	})

	t.Run("inconclusive rows consume no pending capacity", func(t *testing.T) {
		reset(t)
		const limit = 3
		for i := 0; i < limit; i++ {
			url := fmt.Sprintf("https://inconclusive-capacity.example/%d", i)
			digest := sha256.Sum256([]byte(url))
			if _, err := pool.Exec(ctx, `
				INSERT INTO files.link_scans
				    (url_digest, canonical_url, state, scan_uuid, attempts, created_at, updated_at)
				VALUES ($1, $2, 'inconclusive', $3, $4,
				        now() - interval '30 days', now() - interval '30 days')`,
				digest[:], url, fmt.Sprintf("scan-%d", i), i+1); err != nil {
				t.Fatalf("seed inconclusive row %d: %v", i, err)
			}
		}

		admission, err := store.AdmitScan(ctx, secondURL,
			service.LinkScanCapacity{MaxPendingJobs: limit})
		if err != nil {
			t.Fatalf("AdmitScan: %v", err)
		}
		if !admission.Allowed() || admission.NewScanCost != 1 {
			t.Fatalf("admission = %+v, want one new job despite terminal rows", admission)
		}

		jobs, err := store.ClaimDueScans(ctx, 10)
		if err != nil {
			t.Fatalf("ClaimDueScans: %v", err)
		}
		for _, job := range jobs {
			if job.State == "inconclusive" {
				t.Fatalf("an inconclusive row became claimable: %+v", job)
			}
		}
	})

	t.Run("the origin develop claim cannot mutate or return an inconclusive row", func(t *testing.T) {
		reset(t)
		const url = "https://legacy-worker.example/inconclusive"
		digest := sha256.Sum256([]byte(url))
		if _, err := pool.Exec(ctx, `
			INSERT INTO files.link_scans
			    (url_digest, canonical_url, state, scan_uuid, attempts, created_at, updated_at)
			VALUES ($1, $2, 'inconclusive', 'scan-preserved', 9,
			        now() - interval '30 days', now() - interval '30 days')`, digest[:], url); err != nil {
			t.Fatalf("seed inconclusive row: %v", err)
		}

		// Exact claim semantics from origin/develop: every state except done is
		// selected, then leased and returned to the legacy worker.
		rows, err := pool.Query(ctx, `
			WITH due AS (
				SELECT ls.url_digest
				FROM files.link_scans ls
				WHERE ls.state <> 'done'
				  AND (ls.next_attempt_at IS NULL OR ls.next_attempt_at <= now())
				  AND (ls.lease_until IS NULL OR ls.lease_until <= now())
				ORDER BY ls.next_attempt_at NULLS FIRST, ls.created_at
				LIMIT 10
				FOR UPDATE SKIP LOCKED
			)
			UPDATE files.link_scans ls
			   SET attempts = LEAST(ls.attempts + 1, 32767),
			       lease_until = now() + interval '60 seconds',
			       next_attempt_at = now() + interval '60 seconds',
			       updated_at = now()
			  FROM due
			 WHERE ls.url_digest = due.url_digest
			RETURNING ls.url_digest`)
		if err != nil {
			t.Fatalf("legacy claim: %v", err)
		}
		defer rows.Close()
		if rows.Next() {
			t.Fatal("origin/develop claimed an inconclusive row")
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("legacy claim rows: %v", err)
		}

		var state, scanUUID string
		var attempts int
		var nextAttempt, leaseUntil *time.Time
		if err := pool.QueryRow(ctx, `
			SELECT state, scan_uuid, attempts, next_attempt_at, lease_until
			FROM files.link_scans WHERE url_digest = $1`, digest[:],
		).Scan(&state, &scanUUID, &attempts, &nextAttempt, &leaseUntil); err != nil {
			t.Fatalf("read row after legacy claim: %v", err)
		}
		if state != "inconclusive" || scanUUID != "scan-preserved" || attempts != 9 ||
			nextAttempt != nil || leaseUntil != nil {
			t.Fatalf("legacy claim mutated state=%q uuid=%q attempts=%d next=%v lease=%v",
				state, scanUUID, attempts, nextAttempt, leaseUntil)
		}
	})
}
