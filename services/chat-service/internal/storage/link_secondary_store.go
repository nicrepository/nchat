package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/urlsafety"
)

// The background second opinion (issue #928).
//
// # The problem this lane exists for
//
// Google Web Risk answers synchronously, so a cleared link becomes clickable in
// the pass that checked it — which is the whole point of making it the primary.
// Cloudflare URL Scanner cannot answer in that window: it is submit-then-poll,
// and a scan takes far longer than a link may be withheld.
//
// But its answer still matters. A URL no list names yet can still be a page a
// scanner recognises as phishing, and when Cloudflare says so explicitly the
// target must move safe -> malicious, the href must be revoked, the preview must
// be revoked, and every client that already received the message must be told.
// The machinery for all four already exists — it is what a recheck does. What
// did not exist is anything that would *ask* Cloudflare about a URL Google had
// already cleared.
//
// # Why it is a lane and not a queue
//
// The row is already the durable unit of work for a canonical URL, and it can
// hold this: a `safe` row that additionally carries "a verification is
// outstanding, next due at T, currently tracking scan U". The same worker drains
// it in the same pass, with the same lease-by-update discipline as the primary
// claim, so there is no second state machine, no parallel queue and no goroutine
// per message. A row is either in the lane or not, and the columns say which.
//
// # Why it cannot outlive what it verifies
//
// The claim predicate requires the row to still be a fresh safe verdict. When
// the clearance expires the lane ends with it, because there is nothing left to
// contradict — ReopenExpiredVerdicts clears both columns on the way back to
// pending, and the URL is checked again from the top. That is the bound, and it
// is an invariant rather than an attempt counter: there is no state in which a
// verification is outstanding for a clearance that no longer exists.

// secondaryVerifyLease is how long a claimed verification is left alone. The
// same reasoning as linkScanLease: one provider exchange plus room for the
// database round trips around it.
//
// How many are claimed at a time is the worker's decision, not this layer's,
// and it lives beside the loop that spends them.
const secondaryVerifyLease = 60 * time.Second

// LinkSecondaryJob is one outstanding verification, claimed by one attempt.
type LinkSecondaryJob struct {
	CanonicalURL string
	// SecondaryRef is the provider ref this verification is tracking, empty
	// before the secondary has been asked to start one.
	SecondaryRef string
	// Generation identifies the attempt that claimed the lane, and every write
	// this attempt makes carries it back.
	//
	// The lease alone could not do this. It moves a due date, which says when
	// somebody may try next — not who is trying now. A worker whose lease
	// expired while it waited on the provider is still holding a valid-looking
	// canonical URL and scan id, and without an identity it can write them into
	// whatever attempt has since taken the row: overwriting the new scan id,
	// closing the new lane, or making the new attempt's condemnation lose its
	// compare-and-set. That last one is the dangerous shape — a real Cloudflare
	// condemnation silently dropped, leaving a malicious link clickable.
	Generation int
}

// claimDueSecondaryVerificationsQuery leases a batch of outstanding
// verifications, and refuses every row whose clearance is not still current.
//
// The freshness predicate is not an optimisation. A verification exists to
// contradict a clearance, so a row that no longer holds one has nothing to
// contradict: asking the provider about it would spend an exchange on an answer
// nobody could act on, and — worse — a condemnation written against a row that
// had meanwhile been reopened would be a verdict bound to evidence that is gone.
//
// Leasing by pushing secondary_due_at out is the same trick the primary claim
// uses: the claim is the update, so two workers cannot hold one row, and a
// worker that died holds nothing once its lease lapses.
var claimDueSecondaryVerificationsQuery = `
	WITH due AS (
		SELECT ls.canonical_url
		FROM chat.link_scans ls
		WHERE ls.secondary_due_at IS NOT NULL
		  AND ls.secondary_due_at <= now()
		  AND ls.status = 'safe'
		  AND ` + freshVerdictSQL("ls", "$3") + `
		ORDER BY ls.secondary_due_at
		LIMIT $1
		FOR UPDATE SKIP LOCKED
	)
	UPDATE chat.link_scans ls
	   SET secondary_due_at = now() + ($2 * interval '1 second'),
	       secondary_generation = ls.secondary_generation + 1,
	       updated_at = now()
	  FROM due
	 WHERE ls.canonical_url = due.canonical_url
	RETURNING ls.canonical_url, COALESCE(ls.secondary_scan_uuid, ''), ls.secondary_generation`

// ClaimDueSecondaryVerifications leases up to batchSize outstanding second
// opinions.
func (s *PGXMessageStore) ClaimDueSecondaryVerifications(
	ctx context.Context, batchSize int,
) ([]LinkSecondaryJob, error) {
	if batchSize <= 0 {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, claimDueSecondaryVerificationsQuery,
		batchSize, secondaryVerifyLease.Seconds(), urlsafety.VerdictTTL.Seconds())
	if err != nil {
		return nil, fmt.Errorf("claim due secondary verifications: %w", err)
	}
	defer rows.Close()

	var jobs []LinkSecondaryJob
	for rows.Next() {
		var job LinkSecondaryJob
		if err := rows.Scan(&job.CanonicalURL, &job.SecondaryRef, &job.Generation); err != nil {
			return nil, fmt.Errorf("scan secondary verification: %w", err)
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("claim due secondary verifications: %w", err)
	}
	return jobs, nil
}

// RecordSecondaryRef binds the provider's scan id to the attempt that started
// it, so the next pass reads that scan rather than starting another.
//
// Every precondition is checked in the one statement: the lane is still open,
// this attempt still owns it, and the clearance it verifies is still a fresh
// safe verdict. A worker whose lease expired matches nothing — it cannot
// overwrite the scan id the current attempt has, or plant one on a lane it no
// longer holds.
func (s *PGXMessageStore) RecordSecondaryRef(
	ctx context.Context, canonicalURL string, generation int, secondaryRef string,
) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE chat.link_scans ls
		   SET secondary_scan_uuid = $2, updated_at = now()
		 WHERE ls.canonical_url = $1
		   AND ls.secondary_due_at IS NOT NULL
		   AND ls.secondary_generation = $4
		   AND ls.status = 'safe'
		   AND `+freshVerdictSQL("ls", "$3"),
		canonicalURL, secondaryRef, urlsafety.VerdictTTL.Seconds(), generation,
	)
	if err != nil {
		return fmt.Errorf("record secondary ref: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrLinkScanConflict
	}
	return nil
}

// SettleSecondaryVerification closes the lane without changing the verdict.
//
// This is what a secondary SAFE, a terminal non-answer and a hostname refusal
// all do, and they all do the same thing on purpose: none of them contradicts
// the clearance, so none of them may disturb it. Clearing the columns is the
// only effect — the row keeps its status, its decided_at and its href.
//
// Bound to the attempt, like every other write after the claim. A worker whose
// lease expired must not close a lane that now belongs to somebody else — doing
// so would discard a verification in progress and, worse, look like success.
// Matching nothing is reported as ErrLinkScanConflict so the caller counts a
// lost lease rather than a settlement that never happened.
func (s *PGXMessageStore) SettleSecondaryVerification(
	ctx context.Context, canonicalURL string, generation int,
) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE chat.link_scans ls
		   SET secondary_due_at = NULL, secondary_scan_uuid = NULL, updated_at = now()
		 WHERE ls.canonical_url = $1
		   AND ls.secondary_due_at IS NOT NULL
		   AND ls.secondary_generation = $2`,
		canonicalURL, generation,
	)
	if err != nil {
		return fmt.Errorf("settle secondary verification: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrLinkScanConflict
	}
	return nil
}

// RecordSecondaryMalicious is the one transition this lane exists to produce:
// a target that was safe becomes malicious because the secondary provider said
// so explicitly.
//
// It goes through the same compare-and-set every chat condemnation goes
// through — recordMaliciousLinkVerdictQuery — so the global fetch denial and
// the expiry of any file-service clearance happen inside the same statement.
// That is what makes "no component can still fetch this URL" true at the moment
// the condemnation lands rather than shortly afterwards.
//
// Two differences from the primary path, both in the predicate: the expected
// status is `safe` rather than `pending`, and the id compared is the secondary
// ref rather than the scan id the row was decided by — the primary's ref is
// still there and still describes the answer that cleared it.
// The compare-and-set is on three things at once: the attempt (generation), the
// scan the answer came from (secondary_scan_uuid), and the lane still being
// open. All three are needed. Without the generation an abandoned worker's
// condemnation could land on somebody else's attempt; without the scan id an
// answer could be written against a scan the lane has since replaced; without
// the open lane a reopened target could be condemned on evidence about a
// clearance that no longer exists.
func (s *PGXMessageStore) RecordSecondaryMalicious(
	ctx context.Context, canonicalURL string, generation int,
	secondaryRef string, evidenceExpiresAt time.Time,
) error {
	return s.recordSecondaryMaliciousVerdict(
		ctx, canonicalURL, generation, secondaryRef, evidenceExpiresAt)
}
