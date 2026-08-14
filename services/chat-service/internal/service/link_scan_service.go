package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/urlsafety"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// The RF-21 worker.
//
// Cloudflare URL Scanner is submit-then-poll — its result endpoint answers 404
// while a scan runs, and the provider recommends polling every 10-30 seconds —
// so this is where the waiting happens, outside every user's request. One pass
// claims a batch of due URLs, moves each one step forward, and then promotes or
// blocks the messages whose links have all been decided.
//
// It is deliberately the same shape as file-service's antimalware worker: a
// claim with a lease, a bounded batch, and a context that ends the loop. Nothing
// here holds a goroutine per message, and nothing retries in a tight loop — the
// claim itself is the retry schedule.

const (
	// LinkScanPollInterval is how often a replica looks for work.
	//
	// It is the latency floor of the feature: a message withheld for a scan
	// becomes visible at most one interval after its verdict lands. Ten seconds
	// matches the fastest cadence Cloudflare recommends polling a scan at, and
	// the claim is one indexed query against a partial index that is empty
	// whenever there is no backlog, so idle polling costs almost nothing.
	LinkScanPollInterval = 10 * time.Second

	// linkScanBatchSize is how many URLs one pass moves forward.
	//
	// A pass works serially and each URL costs one provider exchange, so the
	// batch is what bounds a pass against the claim lease: eight exchanges at the
	// client's 10-second ceiling still fits inside the 60-second lease with room
	// to spare, which is what keeps a slow provider from having rows stolen and
	// submitted twice.
	linkScanBatchSize = 8
)

// LinkScanQueue is the durable half of the worker: the rows that survive a
// restart. It is an interface so the loop can be tested without a database.
type LinkScanQueue interface {
	ClaimDueLinkScans(ctx context.Context, batchSize int) ([]storage.LinkScanJob, error)
	RecordLinkScanSubmission(ctx context.Context, canonicalURL, scanUUID string) error
	RecordLinkVerdict(ctx context.Context, canonicalURL string, verdict urlsafety.Verdict) error
	ResolveDecidedMessages(ctx context.Context) ([]storage.ResolvedMessage, error)
}

// LinkScanProvider is the provider half. *urlsafety.Service satisfies it, which
// is what keeps the strictness rule — only Safe and Malicious are answers — in
// one place shared with file-service.
type LinkScanProvider interface {
	Submit(ctx context.Context, canonicalURL string) (string, error)
	Poll(ctx context.Context, canonicalURL, scanID string) (urlsafety.Verdict, error)
}

// LinkScanService drains the RF-21 scan queue and releases the messages waiting
// on it.
type LinkScanService struct {
	queue     LinkScanQueue
	provider  LinkScanProvider
	publisher MessageEventPublisher
	logger    *slog.Logger
}

// NewLinkScanService builds the worker's use case. publisher may be nil, in
// which case a promoted message is simply not broadcast — it is still visible on
// the next read, exactly as it would be after a dropped websocket frame.
func NewLinkScanService(
	queue LinkScanQueue, provider LinkScanProvider,
	publisher MessageEventPublisher, logger *slog.Logger,
) *LinkScanService {
	if logger == nil {
		logger = slog.Default()
	}
	return &LinkScanService{queue: queue, provider: provider, publisher: publisher, logger: logger}
}

// SetPublisher attaches the broadcaster. It is called after the hub exists,
// which is later than the service is built — exactly as the message service's
// own publisher is wired.
func (s *LinkScanService) SetPublisher(publisher MessageEventPublisher) {
	s.publisher = publisher
}

// ProcessDue claims one batch, advances it, and releases what became decidable.
//
// It returns how many URLs it moved, so the caller can log a pass that did work
// without logging the far more common pass that found none.
//
// A failure on one URL never stops the batch. Every failure path leaves the row
// pending with its next attempt already pushed out by the claim, which is what
// makes "the provider is down" cost geometrically fewer attempts instead of a
// retry storm — and what makes a withheld message stay withheld rather than
// being released by an error.
func (s *LinkScanService) ProcessDue(ctx context.Context) (int, error) {
	jobs, err := s.queue.ClaimDueLinkScans(ctx, linkScanBatchSize)
	if err != nil {
		return 0, fmt.Errorf("claim due link scans: %w", err)
	}
	for _, job := range jobs {
		if ctx.Err() != nil {
			break
		}
		s.advance(ctx, job)
	}
	// Always run, even when the batch was empty: a verdict written by another
	// replica leaves messages here that nothing else would release.
	s.releaseDecided(ctx)
	return len(jobs), nil
}

// advance moves one URL one step: submit it, or read the scan it already has.
//
// Never more than one step per pass. Submitting and then immediately polling
// would spend an exchange on an answer the provider has not had time to produce,
// and the claim has already scheduled when to come back.
func (s *LinkScanService) advance(ctx context.Context, job storage.LinkScanJob) {
	if job.ScanUUID == "" {
		scanID, err := s.provider.Submit(ctx, job.CanonicalURL)
		if err != nil {
			// Nothing is recorded. The row stays pending and is due again after
			// the backoff the claim already applied.
			s.logFailure(ctx, "submit link scan", job, err)
			return
		}
		if err := s.queue.RecordLinkScanSubmission(ctx, job.CanonicalURL, scanID); err != nil {
			// The scan is running but its id was not stored, so the next claim
			// submits again. That costs one duplicate scan and is the safe
			// direction: the alternative is a row that waits for an id nobody
			// kept.
			s.logFailure(ctx, "record link scan submission", job, err)
		}
		return
	}

	verdict, err := s.provider.Poll(ctx, job.CanonicalURL, job.ScanUUID)
	switch {
	case errors.Is(err, urlsafety.ErrScanPending):
		// Still running. Not an outcome, not an error, and above all not a
		// clearance — the row stays pending and is read again next time.
		return
	case err != nil:
		s.logFailure(ctx, "poll link scan", job, err)
		return
	}
	// The provider layer already refuses anything that is not an explicit
	// clearance or condemnation, and this refuses it again before writing. Belt
	// and braces on purpose: this is the one call that turns a provider answer
	// into a row a message is released by, so a future provider implementation
	// that returned a zero value with a nil error must not be able to write one.
	if !verdict.IsFinal() {
		s.logFailure(ctx, "poll link scan", job, urlsafety.ErrUnavailable)
		return
	}
	if err := s.queue.RecordLinkVerdict(ctx, job.CanonicalURL, verdict); err != nil {
		s.logFailure(ctx, "record link verdict", job, err)
	}
}

// releaseDecided promotes or blocks every withheld message whose links are all
// decided, and broadcasts the promoted ones.
//
// The promotion is exactly-once because the UPDATE that performs it requires the
// row to still be pending: two replicas may both notice the same message, only
// one changes it, and only that one gets a row back to publish from. So a
// message is never broadcast twice, and a blocked one is never broadcast at all.
func (s *LinkScanService) releaseDecided(ctx context.Context) {
	resolved, err := s.queue.ResolveDecidedMessages(ctx)
	if err != nil {
		if ctx.Err() == nil {
			s.logger.ErrorContext(ctx, "resolve decided messages", slog.String("error", err.Error()))
		}
		return
	}
	for _, entry := range resolved {
		if !entry.Published {
			// Blocked. It was never visible to anyone, so there is nothing to
			// retract and nothing to announce.
			s.logger.InfoContext(ctx, "link scan blocked a withheld message",
				slog.String("message_id", entry.Message.ID))
			continue
		}
		if s.publisher == nil {
			continue
		}
		s.publisher.PublishMessageCreated(
			ctx, entry.Message.WorkspaceID, entry.TargetType, entry.TargetID, entry.Message,
		)
	}
}

// logFailure records a failed step without naming the URL.
//
// A canonical URL carries the path and the query, which is exactly where
// internal identifiers and resource names live, so it is not an operational log
// field. The attempt count is what an operator actually needs to tell "one
// blip" from "this one never succeeds".
func (s *LinkScanService) logFailure(ctx context.Context, step string, job storage.LinkScanJob, err error) {
	if ctx.Err() != nil {
		return
	}
	s.logger.WarnContext(ctx, step,
		slog.Int("attempts", job.Attempts),
		slog.String("error", err.Error()),
	)
}

// RunLinkScanWorker polls for work until ctx ends.
//
// The loop is the same one presence reconciliation already uses in this service:
// a ticker, a bounded pass, and a context that ends it. There is no scheduler, no
// broker and no goroutine per message.
func RunLinkScanWorker(ctx context.Context, processor *LinkScanService, interval time.Duration, logger *slog.Logger) {
	if processor == nil {
		return
	}
	if interval <= 0 {
		interval = LinkScanPollInterval
	}
	if logger == nil {
		logger = slog.Default()
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			moved, err := processor.ProcessDue(ctx)
			if err != nil && ctx.Err() == nil {
				logger.ErrorContext(ctx, "link scan pass failed", slog.String("error", err.Error()))
				continue
			}
			if moved > 0 {
				logger.InfoContext(ctx, "link scan pass", slog.Int("urls", moved))
			}
		}
	}
}
