package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/urlsafety"
	"github.com/nicrepository/nchat/services/file-service/internal/service"
)

// Recovering a verdict for a link preview whose scan finished without one
// (issue #135).
//
// # Why file-service needs this at all
//
// The preview gate is unchanged and stays fail-closed: only an explicit,
// unexpired VerdictSafe permits a fetch, so an inconclusive row means no Open
// Graph request, no HEAD, no redirect following and no thumbnail — forever, if
// nothing ever revisits it. That "forever" is the problem. Under 000008 an
// inconclusive row was immutable, so a URL whose scan came back empty could never
// become previewable again even after Cloudflare had a verdict for it.
//
// 000009 opens exactly one door in the trigger — inconclusive -> done, with an
// unchanged scan uuid and a verdict present — and this file is the only caller
// that walks through it.
//
// # What is still impossible
//
// Nothing here submits. There is no method in this file that writes
// 'submit_pending', 'submitting', 'submit_uncertain' or 'polling', clears
// scan_uuid, or resets attempts, and the database refuses all four regardless.
// The provider is only ever asked to *search its own history* and then to read a
// scan this row already owns.

const (
	// maxReconcileClaimBatch bounds one reconciliation pass.
	//
	// Much smaller than the scan batch: each item costs two provider exchanges
	// rather than one, and nothing is waiting on the result — a preview that
	// cannot be rendered today renders tomorrow.
	maxReconcileClaimBatch = 4
)

// ReconcileSchedule is the backoff for reconciling one inconclusive URL.
//
// Four attempts, then never again. The count lives in a column and is compared
// against the length of this list, so the bound is durable and shared across
// replicas — a restart does not buy a fresh set of attempts. Nothing resets it.
//
// The values are wide because what is being waited on is wide: Cloudflare's own
// per-hostname cooldown expiring, or a scan of the same URL appearing in this
// account's history. Neither is helped by polling.
var ReconcileSchedule = []time.Duration{
	5 * time.Minute,
	30 * time.Minute,
	2 * time.Hour,
	6 * time.Hour,
}

// claimDueReconciliationsQuery leases inconclusive rows for one pass and
// schedules the next attempt in the same statement.
//
// The claim is the schedule and the attempt counter, exactly like the scan
// queue's. That is what makes this terminate: a pass consumes an attempt whether
// or not the provider then answers, so an unreachable Cloudflare cannot become an
// endless search loop.
//
// The UPDATE leaves state='inconclusive' untouched, which is the branch 000009's
// trigger permits for bookkeeping — the scan uuid is unchanged and attempts do
// not decrease, so nothing about the row's identity or history can be laundered
// here.
var claimDueReconciliationsQuery = `
	WITH due AS (
		SELECT ls.url_digest, ls.canonical_url
		FROM files.link_scans ls
		WHERE ls.state = 'inconclusive'
		  AND ls.scan_uuid IS NOT NULL
		  AND ls.reconcile_attempts < $2
		  AND (ls.next_reconcile_at IS NULL OR ls.next_reconcile_at <= now())
		  -- Skip what chat-service is already reading, so a contended URL does not
		  -- occupy a slot in this LIMIT. The upsert below decides the actual race.
		  AND ` + urlsafety.ReconcileLeaseAvailablePredicate("ls.url_digest") + `
		ORDER BY ls.next_reconcile_at NULLS FIRST, ls.created_at
		LIMIT $1
		FOR UPDATE SKIP LOCKED
	),` + urlsafety.AcquireReconcileLeaseSQL(
	"due", "due.url_digest", "due.canonical_url",
	"'"+urlsafety.DenylistSourceFiles+"'",
) + `,
	-- Only what this service won. A URL chat-service holds never reaches the
	-- UPDATE, so its reconcile attempt is not spent on a call it will not make.
	won AS (
		SELECT due.url_digest FROM due JOIN leased USING (url_digest)
	)
	UPDATE files.link_scans ls
	   SET reconcile_attempts = ls.reconcile_attempts + 1,
	       next_reconcile_at = now() + (
	           ($3::double precision[])[
	               LEAST(ls.reconcile_attempts + 1, array_length($3::double precision[], 1))
	           ] * interval '1 second'
	       ),
	       updated_at = now()
	  FROM won
	 WHERE ls.url_digest = won.url_digest
	RETURNING ls.url_digest, ls.canonical_url, ls.state, COALESCE(ls.scan_uuid, ''), ls.attempts`

// ClaimDueReconciliations leases up to batchSize inconclusive URLs for a
// reconciliation pass.
//
// The scan id comes off the row and never from a caller, which is what binds the
// eventual verdict write back to this exact scan.
func (s *PGXLinkScanStore) ClaimDueReconciliations(
	ctx context.Context, batchSize int,
) ([]service.LinkScanJob, error) {
	if batchSize <= 0 {
		return nil, nil
	}
	if batchSize > maxReconcileClaimBatch {
		batchSize = maxReconcileClaimBatch
	}
	seconds := make([]float64, len(ReconcileSchedule))
	for i, delay := range ReconcileSchedule {
		seconds[i] = delay.Seconds()
	}
	rows, err := s.pool.Query(ctx, claimDueReconciliationsQuery,
		batchSize, len(ReconcileSchedule), seconds)
	if err != nil {
		return nil, fmt.Errorf("claim due reconciliations: %w", err)
	}
	defer rows.Close()

	var jobs []service.LinkScanJob
	for rows.Next() {
		var job service.LinkScanJob
		if err := rows.Scan(&job.URLDigest, &job.CanonicalURL,
			&job.State, &job.ScanUUID, &job.Attempts); err != nil {
			return nil, fmt.Errorf("scan due reconciliation: %w", err)
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("claim due reconciliations: %w", err)
	}
	return jobs, nil
}

// ReconcileVerdict records a verdict obtained for a scan that had already
// finished without one, moving the row out of 'inconclusive'.
//
// This is the only write that leaves that state, and it satisfies every condition
// 000009's trigger requires — new state 'done', unchanged scan uuid, a verdict
// present, attempts not decreasing — so the transition succeeds here and is
// silently dropped anywhere else, including in a legacy worker whose claim
// predicate still reads `state <> 'done'`.
//
// The predicate is the application-level half of the same rule:
//
//   - state = 'inconclusive' makes it a one-way door. It cannot promote a polling
//     row (that is RecordVerdict's job) and cannot overwrite a verdict already
//     recorded;
//   - scan_uuid = the id the verdict was read from, so a stale or concurrent
//     reconciliation cannot write an answer that belongs to a different scan.
//
// # Freshness comes from the evidence, not from adoption
//
// A **clearance** expires at `evidence.ObservedAt + VerdictTTL` — the provider's
// own scan time plus the lifetime, never the moment it was adopted. A report from
// 23 hours ago under a 15-minute TTL is therefore worth nothing, and the `$4 >
// now()` predicate refuses to write it at all rather than storing an
// already-expired row. urlsafety.Reconcile has already refused such evidence
// before it gets here; the predicate is the same rule restated where the row is
// written, so a future caller cannot talk the store into it.
//
// A **condemnation** is retained from adoption instead, and that asymmetry is
// deliberate rather than an oversight: a restriction grants nothing, so keeping
// it beyond the age of its evidence only keeps a known-bad URL refused for
// longer. Back-dating it would age the row straight out of every reader's
// freshness window and discard the finding. In any case that timestamp is not the
// security boundary for a condemnation — files.link_fetch_denylist is, and it is
// permanent.
//
// Non-final verdicts are refused, inconclusive included: this call exists to
// leave that state, and being able to write it here would make "reconcile" a way
// to reset the attempt bookkeeping.
func (s *PGXLinkScanStore) ReconcileVerdict(
	ctx context.Context, urlDigest []byte, scanUUID string, evidence urlsafety.ScanEvidence,
) error {
	if !evidence.Verdict.IsFinal() {
		return fmt.Errorf("link scan: reconciliation may only record a final verdict")
	}
	if evidence.ObservedAt.IsZero() {
		// A verdict with no evidence time has no honest lifetime, and inventing one
		// is the finding this rule exists for. Refused rather than dated locally.
		return fmt.Errorf("link scan: reconciliation may only record dated evidence")
	}

	if evidence.Verdict == urlsafety.VerdictMalicious {
		// A condemnation is published to the shared authority in the same statement
		// (issue #135, CQ-002). Its own expiry is dated from *adoption* rather than
		// from the evidence, and that asymmetry is deliberate: a restriction is not
		// a permission, so retaining it beyond the evidence's age grants nothing and
		// only keeps a known-bad URL refused for longer. The denylist row is
		// permanent regardless, so this timestamp is bookkeeping and not the
		// security boundary.
		return s.execReconcile(ctx, `
			WITH updated AS (
				UPDATE files.link_scans
				   SET state = 'done', verdict = $3,
				       verdict_expires_at = now() + ($4 * interval '1 second'),
				       lease_until = NULL, next_attempt_at = NULL, next_reconcile_at = NULL,
				       updated_at = now()
				 WHERE url_digest = $1
				   AND state = 'inconclusive'
				   AND scan_uuid IS NOT NULL
				   AND scan_uuid = $2
				RETURNING url_digest, canonical_url
			),
			`+urlsafety.InvalidateFetchAuthoritySQL(
			"$1",
			"updated.canonical_url",
			"'"+urlsafety.DenylistSourceFiles+"'",
			"updated",
		)+`
			SELECT url_digest FROM updated`,
			urlDigest, scanUUID, string(evidence.Verdict), urlsafety.VerdictTTL.Seconds())
	}

	// A clearance expires from the provider's own evidence time, never from
	// adoption: a scan that ran 23 hours ago under a 15-minute TTL is worth
	// nothing. `$4` is evidence.ExpiresAt(), and the `$4 > now()` predicate means an
	// already-expired clearance writes no row at all.
	return s.execReconcile(ctx, `
		UPDATE files.link_scans
		   SET state = 'done', verdict = $3,
		       verdict_expires_at = $4,
		       lease_until = NULL, next_attempt_at = NULL, next_reconcile_at = NULL,
		       updated_at = now()
		 WHERE url_digest = $1
		   AND state = 'inconclusive'
		   AND scan_uuid IS NOT NULL
		   AND scan_uuid = $2
		   AND $4 > now()`,
		urlDigest, scanUUID, string(evidence.Verdict), evidence.ExpiresAt())
}

// execReconcile runs one reconciliation write and maps "changed nothing" to the
// conflict the callers expect.
//
// Zero rows means the row moved on, the trigger dropped the UPDATE, or — for a
// clearance — the evidence had already expired by the time the statement ran. All
// three mean the same thing to the caller: this answer is not the one that
// counts, and none of them is ever a reason to submit.
func (s *PGXLinkScanStore) execReconcile(ctx context.Context, sql string, args ...any) error {
	tag, err := s.pool.Exec(ctx, sql, args...)
	if err != nil {
		return fmt.Errorf("reconcile link verdict: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return service.ErrLinkScanConflict
	}
	return nil
}
