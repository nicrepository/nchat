package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/linkfetch"
	"github.com/nicrepository/nchat/libs/go/platform/urlsafety"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// The rich preview worker (issue #807 §15-21).
//
// It is a pipeline apart from safety and downstream of it: only a target with a
// fresh, explicit `safe` verdict is ever claimed, the verdict is re-read at
// claim time by the store, and every failure here — a slow page, a refused
// redirect, an image that would not decode — changes nothing about the link.
// The anchor stays; the card does not appear.
//
// The fetch is the shared hardened fetcher: address policy at every dial,
// bounded bodies, verified TLS, no proxy. What this worker adds is the hop
// policy — a redirect may be followed only onto a target this deployment has
// already cleared — and the derived image.

const (
	// LinkPreviewPollInterval is how often a replica looks for preview work. A
	// card is enrichment, so it may lag a link by a pass.
	LinkPreviewPollInterval = 10 * time.Second
	// linkPreviewBatchSize bounds one pass: four fetches at the fetcher's
	// five-second ceiling, plus images, fit the sixty-second lease with margin.
	linkPreviewBatchSize = 4
	// LinkPreviewFetchTimeout bounds one whole exchange, redirects included.
	LinkPreviewFetchTimeout = 5 * time.Second
)

// ErrRedirectTargetPending is the hop policy's answer for a redirect onto a
// target nobody has cleared yet: not a refusal of the destination, a request to
// wait for its verdict. The preview stays queued and the hop is admitted for a
// scan.
var ErrRedirectTargetPending = errors.New("link preview: redirect target awaiting clearance")

// errRedirectTargetRefused is the hop policy's answer for a redirect onto a
// target that is condemned or terminally unknown: the chain ends here for good.
var errRedirectTargetRefused = errors.New("link preview: redirect target not cleared")

// LinkPreviewQueue is the durable half of the preview worker.
type LinkPreviewQueue interface {
	ClaimDueLinkPreviews(ctx context.Context, batchSize int) ([]storage.LinkPreviewJob, error)
	// Complete and Fail settle the claim the job identifies; a claim that was
	// superseded answers storage.ErrLinkPreviewConflict and changes nothing.
	CompleteLinkPreview(ctx context.Context, claim storage.LinkPreviewJob, result storage.LinkPreviewResult) (storage.LinkPreviewRow, error)
	FailLinkPreview(ctx context.Context, claim storage.LinkPreviewJob, reason string, terminal bool) (storage.LinkPreviewRow, error)
	TerminalizeExpiredLinkPreviews(ctx context.Context) ([]storage.LinkPreviewRow, error)
	DrainLinkPreviewsDisabled(ctx context.Context) ([]storage.LinkPreviewRow, error)
	LinkPreviewBacklog(ctx context.Context) (int, error)
	LoadLinkTargets(ctx context.Context, canonicalURLs []string) (map[string]storage.LinkTargetState, error)
	AdmitLinkScans(ctx context.Context, workspaceID string, canonicalURLs []string, capacity storage.LinkScanCapacity) (storage.LinkScanAdmission, error)
}

// LinkPreviewFetcher is the outbound half. *linkfetch.Fetcher satisfies it.
type LinkPreviewFetcher interface {
	FetchDocument(ctx context.Context, target *url.URL, hop linkfetch.HopPolicy) (*url.URL, []byte, error)
	FetchImage(ctx context.Context, target *url.URL, hop linkfetch.HopPolicy) ([]byte, error)
}

// LinkPreviewService drains the preview queue.
type LinkPreviewService struct {
	queue     LinkPreviewQueue
	fetcher   LinkPreviewFetcher
	announcer *LinkTargetAnnouncer
	metrics   *urlsafety.PipelineMetrics
	logger    *slog.Logger
	enabled   bool
	// scanCapacity is what a redirect hop admitted for a scan is charged
	// against: the workspace whose message named the original URL.
	scanCapacity storage.LinkScanCapacity
}

// NewLinkPreviewService builds the worker. fetcher may be nil, in which case
// the worker only drains: nothing is fetched and every queued row fails as
// disabled.
func NewLinkPreviewService(queue LinkPreviewQueue, fetcher LinkPreviewFetcher, logger *slog.Logger) *LinkPreviewService {
	if logger == nil {
		logger = slog.Default()
	}
	return &LinkPreviewService{queue: queue, fetcher: fetcher, logger: logger, enabled: fetcher != nil}
}

// SetAnnouncer attaches the convergence that tells messages a card is ready.
func (s *LinkPreviewService) SetAnnouncer(announcer *LinkTargetAnnouncer) { s.announcer = announcer }

// SetMetrics attaches the pipeline collectors. Optional; nil is the no-op.
func (s *LinkPreviewService) SetMetrics(metrics *urlsafety.PipelineMetrics) { s.metrics = metrics }

// SetScanCapacity configures what a redirect hop's scan may spend.
func (s *LinkPreviewService) SetScanCapacity(capacity storage.LinkScanCapacity) {
	s.scanCapacity = capacity
}

// SetEnabled records the feature flag. Off, the worker drains rather than
// fetches, so switching the flag off leaves no row waiting.
func (s *LinkPreviewService) SetEnabled(enabled bool) { s.enabled = enabled && s.fetcher != nil }

// ProcessDue runs one pass: the sweep, then a bounded batch of fetches. It
// returns how many previews it attempted.
func (s *LinkPreviewService) ProcessDue(ctx context.Context) (int, error) {
	// The gauge is refreshed on every pass, the disabled one included: the
	// drain the sweep just ran changed the backlog, and a gauge left at its
	// last enabled value would report work nobody is doing.
	defer s.observeBacklog(ctx)
	s.sweep(ctx)
	if !s.enabled {
		return 0, nil
	}
	jobs, err := s.queue.ClaimDueLinkPreviews(ctx, linkPreviewBatchSize)
	if err != nil {
		return 0, fmt.Errorf("claim due link previews: %w", err)
	}
	for _, job := range jobs {
		if ctx.Err() != nil {
			break
		}
		s.process(ctx, job)
	}
	return len(jobs), nil
}

// sweep ends every preview past its deadline and, with the feature off, every
// non-terminal one. Both run regardless of the flag for the reason every
// sweep in this pipeline does: a flag must never strand a row.
func (s *LinkPreviewService) sweep(ctx context.Context) {
	expired, err := s.queue.TerminalizeExpiredLinkPreviews(ctx)
	s.announceFailures(ctx, expired, urlsafety.PreviewDeadline, err)
	if s.enabled {
		return
	}
	drained, err := s.queue.DrainLinkPreviewsDisabled(ctx)
	s.announceFailures(ctx, drained, urlsafety.PreviewFailed, err)
}

func (s *LinkPreviewService) announceFailures(ctx context.Context, rows []storage.LinkPreviewRow, result string, err error) {
	if err != nil {
		if ctx.Err() == nil {
			s.logger.WarnContext(ctx, "sweep link previews", slog.String("error", err.Error()))
		}
		return
	}
	for _, row := range rows {
		s.metrics.ObservePreview(result)
		s.announcer.AnnouncePreview(ctx, row)
	}
}

// process fetches one preview and records the outcome.
func (s *LinkPreviewService) process(ctx context.Context, job storage.LinkPreviewJob) {
	target, err := url.Parse(job.CanonicalURL)
	if err != nil {
		s.fail(ctx, job, storage.PreviewFailureBlocked, true, urlsafety.PreviewBlocked)
		return
	}
	final, body, err := s.fetcher.FetchDocument(ctx, target, s.hopPolicy(ctx, job))
	if err != nil {
		s.failFetch(ctx, job, err)
		return
	}
	metadata := linkfetch.Extract(final, body)
	if !metadata.HasMetadata() {
		s.fail(ctx, job, storage.PreviewFailureNoMetadata, true, urlsafety.PreviewUnsupported)
		return
	}
	result := storage.LinkPreviewResult{
		SiteName: metadata.SiteName, Title: metadata.Title, Description: metadata.Description,
	}
	s.attachImage(ctx, job, metadata.ImageURL, &result)
	s.complete(ctx, job, result)
}

// hopPolicy decides whether a redirect may be followed: only onto a target
// this deployment has explicitly cleared. Anything else ends the chain — a
// pending or absent target is admitted for a scan so a later attempt can
// succeed; a condemned or unknown one is refused for good.
func (s *LinkPreviewService) hopPolicy(ctx context.Context, job storage.LinkPreviewJob) linkfetch.HopPolicy {
	return func(next *url.URL) error {
		canonical, err := urlsafety.CanonicalizeURL(next.String())
		if err != nil {
			return errRedirectTargetRefused
		}
		targets, err := s.queue.LoadLinkTargets(ctx, []string{canonical})
		if err != nil {
			return fmt.Errorf("load redirect target: %w", err)
		}
		switch linkSafetyFor(targets[canonical]) {
		case domain.LinkSafetySafe:
			return nil
		case domain.LinkSafetyPending:
			s.admitHop(ctx, job, canonical)
			return ErrRedirectTargetPending
		default:
			return errRedirectTargetRefused
		}
	}
}

// admitHop queues a scan for a redirect target nobody has cleared, charged to
// the workspace whose message named the original URL. Best-effort: a refused
// admission leaves the preview to its deadline.
func (s *LinkPreviewService) admitHop(ctx context.Context, job storage.LinkPreviewJob, canonical string) {
	if _, err := s.queue.AdmitLinkScans(ctx, job.WorkspaceID, []string{canonical}, s.scanCapacity); err != nil && ctx.Err() == nil {
		s.logger.WarnContext(ctx, "admit redirect target scan", slog.String("error", err.Error()))
	}
}

// attachImage downloads and derives the og:image when there is one and it
// passes every gate. Any failure leaves the card without an image; it never
// fails the preview.
func (s *LinkPreviewService) attachImage(ctx context.Context, job storage.LinkPreviewJob, imageURL string, result *storage.LinkPreviewResult) {
	if imageURL == "" {
		return
	}
	canonical, err := urlsafety.CanonicalizeURL(imageURL)
	if err != nil || urlsafety.ClassifyURL(canonical) != urlsafety.URLClassPublic {
		s.metrics.ObservePreview(urlsafety.PreviewImageRejected)
		return
	}
	target, _ := url.Parse(canonical)
	data, err := s.fetcher.FetchImage(ctx, target, s.hopPolicy(ctx, job))
	if err != nil {
		s.metrics.ObservePreview(urlsafety.PreviewImageRejected)
		return
	}
	thumbnail, err := linkfetch.DeriveThumbnail(data)
	if err != nil {
		s.metrics.ObservePreview(urlsafety.PreviewImageRejected)
		return
	}
	result.ImageData, result.ImageContentType = thumbnail.Data, thumbnail.ContentType
	result.ImageWidth, result.ImageHeight = thumbnail.Width, thumbnail.Height
}

// failFetch maps a fetch error onto a stored reason and a retry decision.
func (s *LinkPreviewService) failFetch(ctx context.Context, job storage.LinkPreviewJob, err error) {
	switch {
	case errors.Is(err, ErrRedirectTargetPending):
		// Not a failure: the preview waits for the hop's verdict, and the claim
		// already scheduled the retry. Reported as queued so the row is claimable.
		s.fail(ctx, job, storage.PreviewFailureRedirectRefused, false, urlsafety.PreviewQueued)
	case errors.Is(err, linkfetch.ErrRedirectRefused):
		s.fail(ctx, job, storage.PreviewFailureRedirectRefused, true, urlsafety.PreviewRedirectRefused)
	case errors.Is(err, linkfetch.ErrURLNotAllowed), errors.Is(err, linkfetch.ErrInvalidURL):
		s.fail(ctx, job, storage.PreviewFailureBlocked, true, urlsafety.PreviewBlocked)
	case errors.Is(err, linkfetch.ErrUnsupportedContentType):
		s.fail(ctx, job, storage.PreviewFailureUnsupported, true, urlsafety.PreviewUnsupported)
	case errors.Is(err, linkfetch.ErrTimeout):
		s.fail(ctx, job, storage.PreviewFailureTimeout, false, urlsafety.PreviewTimeout)
	default:
		s.fail(ctx, job, storage.PreviewFailureUpstream, false, urlsafety.PreviewFailed)
	}
}

// fail records one failed attempt and, if the row became terminal, announces
// it so a placeholder card can disappear.
func (s *LinkPreviewService) fail(ctx context.Context, job storage.LinkPreviewJob, reason string, terminal bool, result string) {
	if ctx.Err() != nil {
		return
	}
	row, err := s.queue.FailLinkPreview(ctx, job, reason, terminal)
	if err != nil {
		s.logOutcome(ctx, "fail link preview", job, err)
		return
	}
	s.metrics.ObservePreview(result)
	if row.State != string(domain.LinkPreviewQueued) {
		s.announcer.AnnouncePreview(ctx, row)
	}
}

// complete stores the preview and announces the card.
func (s *LinkPreviewService) complete(ctx context.Context, job storage.LinkPreviewJob, result storage.LinkPreviewResult) {
	row, err := s.queue.CompleteLinkPreview(ctx, job, result)
	if err != nil {
		s.logOutcome(ctx, "complete link preview", job, err)
		return
	}
	s.metrics.ObservePreview(urlsafety.PreviewReady)
	s.announcer.AnnouncePreview(ctx, row)
}

func (s *LinkPreviewService) observeBacklog(ctx context.Context) {
	if s.metrics == nil || ctx.Err() != nil {
		return
	}
	if pending, err := s.queue.LinkPreviewBacklog(ctx); err == nil {
		s.metrics.ObservePreviewBacklog(pending)
	}
}

// logOutcome records a failed step without the URL: a canonical URL carries the
// path and query, which is not an operational log field. A lost compare-and-set
// is not logged at all — another worker owns the row.
func (s *LinkPreviewService) logOutcome(ctx context.Context, step string, job storage.LinkPreviewJob, err error) {
	if ctx.Err() != nil || errors.Is(err, storage.ErrLinkPreviewConflict) {
		return
	}
	s.logger.WarnContext(ctx, step, slog.Int("attempts", job.Attempts), slog.String("error", err.Error()))
}

// RunLinkPreviewWorker polls for preview work until ctx ends.
func RunLinkPreviewWorker(ctx context.Context, processor *LinkPreviewService, interval time.Duration, logger *slog.Logger) {
	if processor == nil {
		return
	}
	if interval <= 0 {
		interval = LinkPreviewPollInterval
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
			logPreviewPass(ctx, logger, moved, err)
		}
	}
}

func logPreviewPass(ctx context.Context, logger *slog.Logger, moved int, err error) {
	switch {
	case err != nil && ctx.Err() == nil:
		logger.ErrorContext(ctx, "link preview pass failed", slog.String("error", err.Error()))
	case err == nil && moved > 0:
		logger.InfoContext(ctx, "link preview pass", slog.Int("previews", moved))
	}
}
