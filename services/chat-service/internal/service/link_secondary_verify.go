package service

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/urlsafety"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// The background second opinion, worker half (issue #928).
//
// Google Web Risk clears a link in the pass that checks it, so the href is
// released without waiting for anybody. Cloudflare URL Scanner is submit-then-
// poll and cannot answer in that window — but its answer still matters, because
// a URL no list names yet can still be a page a scanner recognises. This is the
// lane that asks it, afterwards, off the critical path.
//
// What it may do is exactly one thing: turn an explicit Cloudflare condemnation
// into the safe -> malicious transition the pipeline already knows how to make.
// It cannot clear anything, it cannot downgrade a clearance for any other
// reason, and none of its failures reach the verdict.

// secondaryVerifyBatch bounds one pass of this lane. Small, and smaller than
// the primary batch, because nothing is waiting on it.
const secondaryVerifyBatch = 4

// LinkSecondaryQueue is the durable half of the lane. Separate from
// LinkScanQueue so the dependency says what this worker actually touches: four
// statements about one column pair, and no access to the primary claim at all.
type LinkSecondaryQueue interface {
	ClaimDueSecondaryVerifications(ctx context.Context, batchSize int) ([]storage.LinkSecondaryJob, error)
	// Every write after the claim carries the generation the claim issued, so a
	// worker whose lease expired matches no row rather than writing into the
	// attempt that replaced it.
	RecordSecondaryRef(ctx context.Context, canonicalURL string, generation int, secondaryRef string) error
	SettleSecondaryVerification(ctx context.Context, canonicalURL string, generation int) error
	RecordSecondaryMalicious(ctx context.Context, canonicalURL string, generation int,
		secondaryRef string, evidenceExpiresAt time.Time) error
}

// LinkSecondaryVerifier is the provider half: ask the *secondary* source only.
//
// Deliberately not Check. Check is the composition, and running it here would
// ask Google again about a URL Google has already cleared — a second answer
// from the same source, which is not a second opinion.
type LinkSecondaryVerifier interface {
	CheckSecondary(
		ctx context.Context, canonicalURL, providerRef string) (urlsafety.ReputationResult, error)
}

// verifySecondary drains a bounded batch of outstanding second opinions.
//
// Bounded, serial and last in the pass, because nothing is waiting on it: every
// URL in this lane is already clickable. It yields to the work that is on
// somebody's critical path.
func (s *LinkScanService) verifySecondary(ctx context.Context) {
	queue, ok := s.queue.(LinkSecondaryQueue)
	verifier, canVerify := s.provider.(LinkSecondaryVerifier)
	if !ok || !canVerify || !s.safetyEnabled {
		return
	}
	jobs, err := queue.ClaimDueSecondaryVerifications(ctx, secondaryVerifyBatch)
	if err != nil {
		if ctx.Err() == nil {
			s.logger.WarnContext(ctx, "claim secondary verifications",
				slog.String("error", err.Error()))
		}
		return
	}
	for _, job := range jobs {
		if ctx.Err() != nil {
			return
		}
		s.advanceSecondary(ctx, queue, verifier, job)
	}
}

// advanceSecondary moves one verification one step: start it, or read the scan
// it already has.
//
// Every outcome and what it does to the clearance:
//
//	in progress          -> remember the ref; the clearance is untouched
//	MALICIOUS            -> safe -> malicious, href and preview revoked, announced
//	SAFE                 -> lane closed; the clearance stands, unchanged
//	UNKNOWN              -> lane closed; the clearance stands. This is Cloudflare's
//	                        "finished, no verdicts" and its hostname refusal, and
//	                        neither is evidence of anything. A non-answer has
//	                        never been allowed to decide anything here and is not
//	                        about to start by decidingP against a verdict that exists
//	anything else        -> left alone; the lease schedules the retry, and the
//	                        lane ends on its own when the clearance expires
func (s *LinkScanService) advanceSecondary(
	ctx context.Context, queue LinkSecondaryQueue,
	verifier LinkSecondaryVerifier, job storage.LinkSecondaryJob,
) {
	started := time.Now()
	result, err := verifier.CheckSecondary(ctx, job.CanonicalURL, job.SecondaryRef)
	s.observeProvider(operationVerify, started)
	switch {
	case errors.Is(err, urlsafety.ErrCheckInProgress):
		s.bindSecondaryRef(ctx, queue, job, result.ProviderRef)
	case err != nil:
		// Including a circuit-open refusal and every provider failure. A second
		// opinion that could not be obtained is not an opinion, and the link it
		// was about keeps the clearance it already had.
		s.observeAttempt(operationVerify, attemptResultRetry)
	case result.Verdict == urlsafety.ReputationMalicious:
		s.condemnFromSecondary(ctx, queue, job, result)
	default:
		s.closeSecondary(ctx, queue, job, attemptResultSuccess)
	}
}

// bindSecondaryRef persists the scan the secondary started, so the next pass
// reads it instead of starting another.
func (s *LinkScanService) bindSecondaryRef(
	ctx context.Context, queue LinkSecondaryQueue, job storage.LinkSecondaryJob, ref string,
) {
	if ref == "" {
		// A check in progress with nothing to resume it by is a scan nobody can
		// read. Closed rather than retried into a submission loop.
		s.closeSecondary(ctx, queue, job, attemptResultError)
		return
	}
	if err := queue.RecordSecondaryRef(ctx, job.CanonicalURL, job.Generation, ref); err != nil {
		// The clearance lapsed, the lane was settled, or this worker's lease
		// expired and another attempt owns the row. Nothing to do either way:
		// the scan this worker started is not the one the lane is tracking, and
		// forcing it in is precisely what the generation exists to prevent.
		s.observeAttempt(operationVerify, attemptResultLeaseLost)
		return
	}
	s.observeAttempt(operationVerify, attemptResultPending)
}

// condemnFromSecondary is the transition this whole lane exists for.
//
// The write is one statement: status safe -> malicious, the global fetch denial
// published, and any file-service clearance expired. Announcing afterwards is
// what removes the href and the preview from every client that already has the
// message, through the same path a recheck uses.
func (s *LinkScanService) condemnFromSecondary(
	ctx context.Context, queue LinkSecondaryQueue,
	job storage.LinkSecondaryJob, result urlsafety.ReputationResult,
) {
	err := queue.RecordSecondaryMalicious(
		ctx, job.CanonicalURL, job.Generation, job.SecondaryRef, result.ExpiresAt)
	if err != nil {
		// The row was reopened or condemned by somebody else first. Either way
		// this answer is not the one that counts, and the lane is already gone.
		s.observeAttempt(operationVerify, attemptResultLeaseLost)
		return
	}
	s.observeAttempt(operationVerify, attemptResultSuccess)
	s.converge(ctx, job.CanonicalURL)
}

// closeSecondary ends a lane without touching the verdict.
func (s *LinkScanService) closeSecondary(
	ctx context.Context, queue LinkSecondaryQueue, job storage.LinkSecondaryJob, outcome string,
) {
	switch err := queue.SettleSecondaryVerification(ctx, job.CanonicalURL, job.Generation); {
	case err == nil:
		s.observeAttempt(operationVerify, outcome)
	case errors.Is(err, storage.ErrLinkScanConflict):
		// The lane moved on: this worker's lease expired and another attempt
		// owns it, or the target was reopened. Closing it would discard a
		// verification in progress, so the statement matched nothing and this
		// is counted as the lost lease it is rather than as a settlement.
		s.observeAttempt(operationVerify, attemptResultLeaseLost)
	case ctx.Err() == nil:
		s.logger.WarnContext(ctx, "settle secondary verification",
			slog.String("error", err.Error()))
	}
}
