package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/nicrepository/nchat/libs/go/platform/urlsafety"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// Recovering a verdict for a link whose scan finished without one (issue #135).
//
// # What problem this solves
//
// A message may now be published while one of its links is only *inconclusive*:
// the provider confirmed a scan reached a terminal state and produced no usable
// verdict. The real production trigger was Cloudflare answering
//
//	task.status="finished", task.success=false,
//	errors=["Refusing to scan: hostname was recently scanned or too many scans
//	         to hostname in the last days."]
//
// which says nothing whatsoever about the link. Refusing the message on it was a
// product bug, so the message goes out — and the server's own permission to fetch
// that URL stays revoked. This service is what may eventually restore it, or
// replace it with a block.
//
// # The two rules that shape everything here
//
// First: no submission. Not on a timer, not after a horizon, not when a reader
// asks. Cloudflare has no idempotency token, so a second POST is a second billed
// scan, and for the refusal above it is the one action guaranteed not to help.
// There is no code path from this file to LinkScanProvider.Submit — the provider
// interface it depends on does not have one.
//
// Second: the search is not the verdict. urlsafety.Service.Reconcile uses the
// account-scoped search to find a scan *id* for exactly this canonical URL, then
// reads the full report for that id through the same strict path an ordinary poll
// uses. The search answer's own summarised verdict field is never read, and
// urlsafety.ScanRecord has no field that could carry it.

const (
	// LinkReconcileInterval is how often the background pass looks for
	// inconclusive links worth re-reading.
	//
	// Six times slower than the scan worker's, and that is the right relationship:
	// the scan queue holds messages nobody has seen yet, while this holds messages
	// that were delivered. Nobody is blocked on a pass here, so a pass may be
	// infrequent. The claim is one query against a partial index that is empty
	// whenever nothing is inconclusive, which is the normal state.
	LinkReconcileInterval = time.Minute

	// reconcileBatchSize bounds one background pass. Small, because every item
	// costs two provider exchanges rather than one and there is no deadline.
	reconcileBatchSize = 4
)

// LinkReconcileQueue is the durable half of reconciliation.
//
// Note what is absent: there is no method here that submits, resets an attempt
// counter, clears a scan uuid or writes status='pending'. The interface is the
// guarantee — a mistake in this file cannot reopen a scan, because there is
// nothing to call that would.
type LinkReconcileQueue interface {
	// MessageInconclusiveURLs returns the canonical URLs of one message whose
	// scans are inconclusive, for a caller authorised to read that message. The
	// client never supplies a URL; this is where they come from.
	MessageInconclusiveURLs(ctx context.Context, workspaceID, viewerID, messageID string) ([]string, error)
	// MessageLinkSafety reads back the marker a message currently carries, for the
	// same authorised caller.
	MessageLinkSafety(ctx context.Context, workspaceID, viewerID, messageID string) (domain.MessageLinkSafety, time.Time, error)
	// ClaimManualReconcile takes the once-a-minute-per-URL reader-driven slot and
	// returns the URLs the provider may now be asked about, each with the scan id
	// its answer must be read from.
	ClaimManualReconcile(ctx context.Context, canonicalURLs []string) ([]storage.InconclusiveScan, error)
	// ClaimDueInconclusiveScans leases work for the background pass, consuming one
	// of a bounded number of automatic attempts.
	ClaimDueInconclusiveScans(ctx context.Context, batchSize int) ([]storage.InconclusiveScan, error)
	// ReconcileLinkVerdict is the one write that leaves the inconclusive state,
	// bound to the scan the verdict was read from. It takes the evidence rather
	// than a bare verdict because a clearance expires from when the provider
	// produced it, never from when it was adopted (issue #135, CQ-001).
	ReconcileLinkVerdict(ctx context.Context, canonicalURL, scanUUID string, evidence urlsafety.ScanEvidence) error
	// RefreshMessageLinkSafety recomputes the per-message marker for the published
	// messages that name a URL, and reports the ones that changed.
	RefreshMessageLinkSafety(ctx context.Context, canonicalURL string) ([]storage.MessageLinkSafetyChange, error)
}

// LinkVerdictReconciler is the provider half. *urlsafety.Service satisfies it.
//
// One method, and deliberately not the Scanner or the LinkScanProvider interface:
// those have Submit, and nothing on this path may reach it.
type LinkVerdictReconciler interface {
	// Reconcile finds this deployment's own scan for canonicalURL and reads its
	// verdict from the full report, together with when the provider produced it.
	// It never submits.
	Reconcile(ctx context.Context, canonicalURL string) (urlsafety.ScanEvidence, error)
}

// LinkSafetyChangePublisher announces that a published message's link-safety state
// changed, to the audience that received the message.
//
// It is not MessageEventPublisher: re-emitting message.created would duplicate the
// message in every client's timeline and re-fire its mention notifications. This
// event mutates one field of a message the client already holds.
type LinkSafetyChangePublisher interface {
	PublishMessageLinkSafetyChanged(
		ctx context.Context, workspaceID, targetType, targetID, messageID, state string, updatedAt time.Time,
	)
}

// LinkReconcileService recovers verdicts for links whose scans finished without
// one, and converges every client that already received the message.
type LinkReconcileService struct {
	queue     LinkReconcileQueue
	provider  LinkVerdictReconciler
	publisher LinkSafetyChangePublisher
	// metrics is nil until SetMetrics is called, and nil is a working value —
	// every *PipelineMetrics method tolerates a nil receiver, so the call sites
	// below are unguarded.
	metrics *urlsafety.PipelineMetrics
	logger  *slog.Logger
}

// NewLinkReconcileService builds the use case. provider may be nil, which is a
// working deployment with reconciliation disabled: an inconclusive link simply
// stays inconclusive, the message stays published, and the server still never
// fetches the URL.
func NewLinkReconcileService(
	queue LinkReconcileQueue, provider LinkVerdictReconciler, logger *slog.Logger,
) *LinkReconcileService {
	if logger == nil {
		logger = slog.Default()
	}
	return &LinkReconcileService{queue: queue, provider: provider, logger: logger}
}

// SetPublisher attaches the broadcaster. Called after the hub exists, exactly as
// the message service's own publisher is wired. Without it a converged state is
// still persisted and still visible on the next read — the same degradation a
// dropped websocket frame produces.
func (s *LinkReconcileService) SetPublisher(publisher LinkSafetyChangePublisher) {
	s.publisher = publisher
}

// SetMetrics attaches the pipeline collectors. Entirely optional.
func (s *LinkReconcileService) SetMetrics(metrics *urlsafety.PipelineMetrics) {
	s.metrics = metrics
}

// Ready reports whether reconciliation can actually reach a provider.
//
// The manual endpoint uses it to answer 503 rather than pretend it looked.
// Promising a check that cannot happen is worse than saying so: a client shown
// "nothing new" would stop asking.
func (s *LinkReconcileService) Ready() bool {
	return s != nil && s.queue != nil && s.provider != nil
}

// ReconcileMessageInput is one reader asking for a second look at one message.
//
// Note what a client cannot send: no canonical URL, no scan uuid, no workspace.
// The workspace is resolved from the session by the HTTP layer, the URLs are read
// from the database, and the scan ids come off the rows. A client that could name
// a URL would have turned this endpoint into a Cloudflare search proxy billed to
// this account.
type ReconcileMessageInput struct {
	WorkspaceID string
	ViewerID    string
	MessageID   string
}

// ReconcileMessageResult is what the reader is told.
//
// Deliberately almost empty. The state is the one thing the client needs, and it
// is the same closed set the message payload already carries. There is no
// provider message, no error text, no URL, no scan id, and no count of how many
// links were examined — a reader watching a warning does not need an inventory of
// the links behind it, and an endpoint that provided one would be a probe.
type ReconcileMessageResult struct {
	// State is the message's authoritative link-safety state after the attempt.
	// It is answered even when nothing changed, so a client never has to guess.
	State domain.MessageLinkSafety
	// UpdatedAt orders this answer against websocket create/edit/correction
	// events that may arrive before or after the HTTP response.
	UpdatedAt time.Time
	// RetryAfter is how long to wait before asking again. Always set, so the
	// button has something to disable itself against, and never a promise that
	// waiting will produce an answer.
	RetryAfter time.Duration
}

// ReconcileMessage takes a second look at one message's unverified links.
//
// The order of operations is the authorization:
//
//  1. the HTTP layer has already established an authenticated session and
//     resolved the workspace from it;
//  2. MessageInconclusiveURLs applies the ordinary message-read authorization —
//     active workspace, active membership, channel visibility or DM
//     participation — and returns ErrNotFound for anything this caller may not
//     read, so the endpoint cannot be used to discover message ids;
//  3. the URLs come out of that same query, bound to the fingerprint the message
//     currently carries. Nothing the client sent reaches the provider;
//  4. ClaimManualReconcile applies the deployment-wide per-URL cooldown, so ten
//     readers clicking on one warning cost one search;
//  5. only then is the provider asked, and only through Reconcile, which cannot
//     submit.
//
// Nothing here is a promise that a new scan will run, and the result deliberately
// cannot express one.
func (s *LinkReconcileService) ReconcileMessage(
	ctx context.Context, input ReconcileMessageInput,
) (ReconcileMessageResult, error) {
	workspaceID := strings.TrimSpace(input.WorkspaceID)
	viewerID := strings.TrimSpace(input.ViewerID)
	messageID, err := normalizeMessageID(input.MessageID)
	if err != nil {
		return ReconcileMessageResult{}, err
	}
	if workspaceID == "" || viewerID == "" {
		return ReconcileMessageResult{}, fmt.Errorf(
			"%w: workspace_id and viewer_id are required", domain.ErrInvalidInput)
	}
	if !s.Ready() {
		return ReconcileMessageResult{}, domain.ErrURLCheckUnavailable
	}

	s.observe(urlsafety.ReconcileSourceManual, urlsafety.ReconcileRequested)

	// Authorization and input derivation in one query. ErrNotFound covers "you may
	// not read this", "there is no such message" and "this message has no
	// unverified link" — three answers a caller must not be able to tell apart.
	urls, err := s.queue.MessageInconclusiveURLs(ctx, workspaceID, viewerID, messageID)
	if err != nil {
		return ReconcileMessageResult{}, err
	}

	claimed, err := s.queue.ClaimManualReconcile(ctx, urls)
	if err != nil {
		if ctx.Err() != nil {
			return ReconcileMessageResult{}, ctx.Err()
		}
		return ReconcileMessageResult{}, domain.ErrURLCheckUnavailable
	}
	if len(claimed) == 0 {
		// Every URL is inside its cooldown, or was decided a moment ago by another
		// path. Not an error and not a refusal to serve the caller: the current
		// state is authoritative and is what they came for.
		s.observe(urlsafety.ReconcileSourceManual, urlsafety.ReconcileRateLimited)
		return s.currentState(ctx, workspaceID, viewerID, messageID)
	}
	for _, scan := range claimed {
		if ctx.Err() != nil {
			break
		}
		s.reconcileOne(ctx, scan, urlsafety.ReconcileSourceManual)
	}
	return s.currentState(ctx, workspaceID, viewerID, messageID)
}

// currentState reads back the authoritative marker, under the same authorization.
//
// Read again rather than inferred from what the loop above decided, and that is
// deliberate: a message may carry several links, another replica may have
// reconciled one of them concurrently, and the aggregate is computed in SQL. The
// answer the reader gets is the one the next page load will also produce.
func (s *LinkReconcileService) currentState(
	ctx context.Context, workspaceID, viewerID, messageID string,
) (ReconcileMessageResult, error) {
	state, updatedAt, err := s.queue.MessageLinkSafety(ctx, workspaceID, viewerID, messageID)
	if err != nil {
		return ReconcileMessageResult{}, err
	}
	return ReconcileMessageResult{
		State: state, UpdatedAt: updatedAt, RetryAfter: storage.ManualReconcileCooldown,
	}, nil
}

// ProcessDueReconciliations runs one bounded background pass.
//
// It returns how many URLs it examined so the caller can log a pass that did work
// without logging the far more common pass that found none.
//
// The claim consumes one of a small, fixed number of automatic attempts per URL
// and schedules the next one, whether or not the provider then answers. That is
// what makes this terminate: an unreachable provider still spends the budget, so
// there is no configuration of failures that turns this into an endless search
// loop. Nothing anywhere resets that counter.
func (s *LinkReconcileService) ProcessDueReconciliations(ctx context.Context) (int, error) {
	if !s.Ready() {
		return 0, nil
	}
	scans, err := s.queue.ClaimDueInconclusiveScans(ctx, reconcileBatchSize)
	if err != nil {
		return 0, fmt.Errorf("claim due reconciliations: %w", err)
	}
	examined := 0
	for _, scan := range scans {
		if ctx.Err() != nil {
			return examined, nil
		}
		s.observe(urlsafety.ReconcileSourceBackground, urlsafety.ReconcileRequested)
		s.reconcileOne(ctx, scan, urlsafety.ReconcileSourceBackground)
		examined++
	}
	return examined, nil
}

// reconcileOne asks the provider about one URL and, if it learned something,
// records it and converges the messages that carry it.
//
// Every outcome that is not an explicit clearance or condemnation leaves the row
// exactly as it was. That is fail-closed for the thing that matters: the server's
// permission to fetch the URL is only ever restored by a VerdictSafe read from a
// full provider report.
func (s *LinkReconcileService) reconcileOne(
	ctx context.Context, scan storage.InconclusiveScan, source string,
) {
	started := time.Now()
	evidence, err := s.provider.Reconcile(ctx, scan.CanonicalURL)
	s.metrics.ObserveProviderDuration(urlsafety.OperationPoll, started)
	if ctx.Err() != nil {
		return
	}
	if evidence.CandidateFound {
		// Counted before the outcome, and only for an exact-URL match whose report
		// was actually read — the difference between "the search found nothing" and
		// "it found something that did not clear anything".
		s.observe(source, urlsafety.ReconcileCandidateFound)
	}
	s.observe(source, urlsafety.ReconcileOutcome(evidence.Verdict, err))
	if err != nil {
		if !errors.Is(err, urlsafety.ErrNotCheckable) {
			// ErrNotCheckable is the ordinary "no candidate" answer and is not worth a
			// log line — it is what a hostname another account scanned recently looks
			// like, every single pass. Everything else is a fact about the provider.
			s.logFailure(ctx, "reconcile link verdict", scan, err)
		}
		return
	}

	// The write is bound to the scan the verdict was read from. A conflict means
	// another replica, or the reader-driven path, decided this row first — the
	// answer is the same one, so there is nothing to repair and nothing to report
	// beyond not announcing it twice.
	switch err := s.queue.ReconcileLinkVerdict(
		ctx, scan.CanonicalURL, scan.ScanUUID, evidence); {
	case err == nil:
	case errors.Is(err, storage.ErrLinkScanConflict):
		return
	default:
		s.logFailure(ctx, "record reconciled verdict", scan, err)
		return
	}
	s.converge(ctx, scan)
}

// ErrConvergenceStalled reports a convergence loop that stopped making progress.
//
// It exists so the drain below can be unbounded in the number of *rows* it
// converges while still being guaranteed to terminate. A batch identical to the
// one before it means the store reported rows as changed that it did not
// actually change, which is a bug rather than a workload — and looping on it
// forever would turn that bug into a hung worker.
var ErrConvergenceStalled = errors.New("link reconcile: convergence made no progress")

// converge updates the published messages that name a URL and tells their
// subscribers.
//
// # What this is not responsible for
//
// It is not the security boundary, and nothing waits on it. The moment
// ReconcileLinkVerdict commits, the verdict is durable and — for a condemnation —
// files.link_fetch_denylist already forbids every server-side fetch of the URL,
// deployment-wide. A URL proven malicious is refused for new messages and for
// previews immediately, whether or not a single message row has been updated yet.
//
// What this loop does is converge *clients*: the per-message marker they render
// from.
//
// # Why it drains instead of stopping at a cap
//
// It used to run a fixed number of batches, which meant a URL carried by more
// messages than batch × passes left the remainder permanently on the old marker —
// the scan is no longer inconclusive, so nothing ever reclaims it. For a
// condemnation that is a message still showing a live link to a URL this
// deployment knows is malicious, forever. A cap that abandons rows silently is
// not an acceptable bound.
//
// So the loop runs until the store reports nothing left to change. That
// terminates on its own: RefreshMessageLinkSafety only returns rows whose marker
// it just moved, so every batch strictly consumes work and a row cannot be
// returned twice. The two guards below are for the cases where that invariant is
// violated or the process is going away — a repeated batch, and context
// cancellation — not for bounding ordinary work.
func (s *LinkReconcileService) converge(ctx context.Context, scan storage.InconclusiveScan) {
	if err := drainMessageLinkSafety(ctx, s.queue, scan.CanonicalURL, s.publisher); err != nil {
		s.logFailure(ctx, "converge message link safety", scan, err)
	}
}

type messageLinkSafetyRefresher interface {
	RefreshMessageLinkSafety(ctx context.Context, canonicalURL string) ([]storage.MessageLinkSafetyChange, error)
}

// drainMessageLinkSafety is shared by ordinary polls and reconciliation so a
// terminal verdict has one convergence policy regardless of how it arrived.
func drainMessageLinkSafety(
	ctx context.Context, queue messageLinkSafetyRefresher, canonicalURL string,
	publisher LinkSafetyChangePublisher,
) error {
	var previous map[string]struct{}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		changes, err := queue.RefreshMessageLinkSafety(ctx, canonicalURL)
		if err != nil {
			return err
		}
		if len(changes) == 0 {
			return nil
		}
		current := changedIDs(changes)
		if sameChangeSet(previous, current) {
			// Non-progress: the store handed back exactly the batch it just handed
			// back. Reported and abandoned rather than retried, because retrying is
			// what would spin.
			return ErrConvergenceStalled
		}
		previous = current

		if publisher == nil {
			continue
		}
		for _, change := range changes {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			publisher.PublishMessageLinkSafetyChanged(ctx,
				change.WorkspaceID, change.TargetType, change.TargetID,
				change.MessageID, string(change.State), change.UpdatedAt)
		}
	}
}

// changedIDs is the identity of one batch, for the non-progress check.
func changedIDs(changes []storage.MessageLinkSafetyChange) map[string]struct{} {
	ids := make(map[string]struct{}, len(changes))
	for _, change := range changes {
		ids[change.MessageID] = struct{}{}
	}
	return ids
}

// sameChangeSet reports whether two consecutive batches converged the same rows.
//
// nil (there was no previous batch) is never equal to anything, so the first
// iteration always proceeds.
func sameChangeSet(previous, current map[string]struct{}) bool {
	if previous == nil || len(previous) != len(current) {
		return false
	}
	for id := range current {
		if _, seen := previous[id]; !seen {
			return false
		}
	}
	return true
}

func (s *LinkReconcileService) observe(source, result string) {
	s.metrics.ObserveVerdictReconciliation(source, result)
}

// logFailure records a failed step without naming the URL or the scan.
//
// A canonical URL carries the path and the query, which is exactly where internal
// identifiers and resource names live, and a scan uuid is an account-internal
// provider identifier. Neither is an operational log field, and neither is a
// metric label — see ObserveVerdictReconciliation, which has no parameter that
// could carry one.
func (s *LinkReconcileService) logFailure(
	ctx context.Context, step string, _ storage.InconclusiveScan, err error,
) {
	if ctx.Err() != nil {
		return
	}
	s.logger.WarnContext(ctx, step, slog.String("error", err.Error()))
}

// RunLinkReconcileWorker runs the background pass until ctx ends.
//
// Its own loop rather than a step inside the scan worker's, because the two have
// different cadences and different urgency: the scan worker releases messages
// nobody has seen, and a pass here corrects messages that were already delivered.
// Sharing a ticker would either make this run six times more often than it needs
// to or slow the one that people are waiting on.
func RunLinkReconcileWorker(
	ctx context.Context, processor *LinkReconcileService, interval time.Duration, logger *slog.Logger,
) {
	if processor == nil || !processor.Ready() {
		return
	}
	if interval <= 0 {
		interval = LinkReconcileInterval
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
			examined, err := processor.ProcessDueReconciliations(ctx)
			switch {
			case err != nil && ctx.Err() == nil:
				logger.ErrorContext(ctx, "link reconcile pass failed", slog.String("error", err.Error()))
			case err == nil && examined > 0:
				logger.InfoContext(ctx, "link reconcile pass", slog.Int("urls", examined))
			}
		}
	}
}

// normalizeMessageID validates one message id from a client before it reaches a
// query.
//
// Shared shape with normalizeMessageIDBatch, for one id: a UUID or nothing. It is
// what keeps a path parameter from reaching the database as text.
func normalizeMessageID(rawID string) (string, error) {
	parsed, err := uuid.Parse(strings.TrimSpace(rawID))
	if err != nil {
		return "", fmt.Errorf("%w: invalid message id", domain.ErrInvalidInput)
	}
	return parsed.String(), nil
}
