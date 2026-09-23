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

	// publishDispatchBatch bounds one delivery pass. Larger than the scan batch
	// because a publish is local and fast — there is no provider on this path.
	publishDispatchBatch = 50
)

// LinkScanQueue is the durable half of the worker: the rows that survive a
// restart. It is an interface so the loop can be tested without a database.
type LinkScanQueue interface {
	ClaimDueLinkScans(ctx context.Context, batchSize int) ([]storage.LinkScanJob, error)
	BeginLinkScanSubmit(ctx context.Context, canonicalURL string, expectedGeneration int) (int, error)
	RecordLinkScanSubmission(ctx context.Context, canonicalURL, scanUUID string, generation int) error
	AdoptScanUUID(ctx context.Context, canonicalURL, scanUUID string, generation int) error
	ReserveProviderSubmit(ctx context.Context, limit int, window time.Duration) (bool, error)
	PruneLinkScanBudget(ctx context.Context, olderThan time.Duration) error
	RecordLinkVerdict(ctx context.Context, write storage.LinkVerdictWrite) error
	RefreshMessageLinkSafety(ctx context.Context, canonicalURL string) ([]storage.MessageLinkSafetyChange, error)
	ResolveDecidedMessages(ctx context.Context) (storage.ResolveSummary, error)
	ReopenExpiredVerdicts(ctx context.Context) (int, error)
	LinkScanBacklog(ctx context.Context) (map[string]int, time.Duration, error)
	// Issue #807: convergence and policy terminals.
	TerminalizeExpiredLinkScans(ctx context.Context) ([]string, error)
	TerminalizePendingLinkScansDisabled(ctx context.Context) ([]string, error)
	RecordLinkTargetTerminal(ctx context.Context, canonicalURL, reason string) error
	ClaimPublishEvents(ctx context.Context, batchSize int) ([]storage.PublishEvent, error)
	MarkPublished(ctx context.Context, messageID string) error
	CancelPublishEvent(ctx context.Context, messageID string) error
	PublishOutboxBacklog(ctx context.Context) (int, time.Duration, error)
}

// LinkScanProvider is the provider half, in the provider-agnostic contract of
// issue #807. *urlsafety.Service satisfies it, which is what keeps the
// strictness rule — only an explicit clearance or condemnation is an answer —
// and the circuit breaker in one place shared with file-service.
//
// Check with an empty ref starts a check; ErrCheckInProgress hands back the ref
// to persist and retry with. A synchronous provider answers on the first call
// and the worker records the verdict in the same pass.
type LinkScanProvider interface {
	Check(ctx context.Context, canonicalURL, providerRef string) (urlsafety.ReputationResult, error)
}

// circuitReporter is the optional half a provider exposes for the breaker gauge.
type circuitReporter interface {
	CircuitState() urlsafety.BreakerState
}

// hostResolver resolves a hostname for the pre-provider internal-network check.
type hostResolver = linkfetch.Resolver

// directProviderRef is the ref persisted for a provider that answered on the
// first call: there is no remote id, and the verdict compare-and-set still
// binds the write to this attempt.
//
// It is the shared constant rather than a local copy because since issue #928
// the provider is a composition that routes on this value: a row carrying it
// has no check outstanding at the asynchronous half, so resuming it means
// asking the synchronous primary again. Two spellings of it would send such a
// row to the scanner with a word that is not a scan id.
const directProviderRef = urlsafety.DirectProviderRef

// LinkScanSearcher is the recovery half, and is deliberately optional.
//
// It is asked for only when a submission's outcome was never written down, and a
// provider client that cannot answer it is a working deployment — the uncertain
// row simply waits out its horizon instead. Keeping it separate from
// LinkScanProvider is what says that: searching is not part of deciding whether
// a URL is safe, and it must never become a second way to obtain a verdict.
type LinkScanSearcher interface {
	// FindRecentScan returns the scan this deployment most plausibly created for
	// canonicalURL at or after `since`. urlsafety.ErrNotCheckable means there is
	// nothing eligible; any other error means the question could not be asked.
	// Neither is ever a reason to submit again.
	FindRecentScan(ctx context.Context, canonicalURL string, since time.Time) (urlsafety.ScanRecord, int, error)
}

// Pipeline step names and outcomes, re-exported from the shared package so the
// worker reads without a package qualifier on every line. The values are the
// shared closed set — nothing here invents a label.
const (
	operationSubmit  = urlsafety.OperationSubmit
	operationPoll    = urlsafety.OperationPoll
	operationResolve = urlsafety.OperationResolve
	operationPublish = urlsafety.OperationPublish
	operationVerify  = urlsafety.OperationVerify

	attemptResultSuccess      = urlsafety.AttemptSuccess
	attemptResultPending      = urlsafety.AttemptPending
	attemptResultRetry        = urlsafety.AttemptRetry
	attemptResultError        = urlsafety.AttemptError
	attemptResultLeaseLost    = urlsafety.AttemptLeaseLost
	attemptResultCancelled    = urlsafety.AttemptCancelled
	attemptResultThrottled    = urlsafety.AttemptThrottled
	attemptResultUncertain    = urlsafety.AttemptUncertain
	attemptResultInconclusive = urlsafety.AttemptInconclusive
	attemptResultDeadline     = urlsafety.AttemptDeadline
	attemptResultPolicy       = urlsafety.AttemptPolicy
)

// How the worker behaves in the submission window, and how much a deployment is
// willing to spend at the provider.
//
// The defaults are conservative on purpose: a deployment that never configures
// these gets a system that submits slowly and refuses rather than one that
// spends an account's quota at whatever rate its replicas happen to manage.
const (
	// submitPersistAttempts bounds how hard one worker tries to write down a scan
	// id the provider already gave it.
	//
	// This is the whole failure the finding named: the provider has accepted and
	// been billed, and the only thing standing between that and a usable row is a
	// local write. Retrying it briefly, while the lease is still held, is far
	// cheaper than giving up and reconciling — and far cheaper than the duplicate
	// scan that resubmitting would cost. It stays small because the lease is
	// finite and the row is recoverable either way.
	submitPersistAttempts = 3

	// submitPersistRetryDelay paces those attempts. Overridable in tests, which
	// is why it is a field on the service rather than used directly.
	submitPersistRetryDelay = 200 * time.Millisecond

	// defaultUncertainTimeout is when an unresolved submission stops being
	// ordinary and starts being something an operator should look at.
	//
	// It does not gate any action. Nothing changes at the horizon except the
	// metric: reconciliation continues on the same schedule and no submission is
	// ever made, because there is no amount of elapsed time that turns "the
	// provider may already have this scan" into "it is safe to send another".
	// What crossing it means is that reconciliation has not converged and a
	// person, not a retry, is the next step.
	defaultUncertainTimeout = 15 * time.Minute

	// budgetWindowRetention is how long spent budget windows are kept before the
	// worker prunes them. They are counters, not history: nothing reads a window
	// that can no longer be counted into.
	budgetWindowRetention = time.Hour
)

// LinkScanService drains the RF-21 scan queue and releases the messages waiting
// on it.
type LinkScanService struct {
	queue     LinkScanQueue
	provider  LinkScanProvider
	publisher MessageEventPublisher
	// blockedPublisher carries the terminal refusal to its author. Separate from
	// the message publisher because the audience is different — one user, not a
	// conversation — and conflating them is how blocked content reaches people
	// who were never shown the message.
	blockedPublisher MessageBlockedPublisher
	// metrics is nil until SetMetrics is called, and nil is a working value: it
	// is the no-op reporter. Every *PipelineMetrics method is defined to tolerate
	// a nil receiver, so the workers below call them unconditionally instead of
	// guarding each call site — the tolerance lives in one type rather than being
	// re-derived at every use, which is what keeps a later call from being the one
	// that forgot. TestLinkScanServiceRunsWithoutMetrics holds that contract.
	metrics *urlsafety.PipelineMetrics
	logger  *slog.Logger

	// capacity is what this deployment will spend at the provider. Zero values
	// disable the corresponding ceiling, which is what a deployment that has not
	// configured them gets — see config.Config for the defaults that are actually
	// wired in production.
	capacity LinkScanWorkerCapacity
	// persistRetryDelay paces the bounded retry that tries to write down a scan
	// id the provider already accepted. A field so a test can drive the retry
	// without waiting on a clock.
	persistRetryDelay time.Duration

	// announcer converges a decided target into the messages naming it (issue
	// #807). Nil means only the aggregate drain runs, through publisher.
	announcer *LinkTargetAnnouncer
	// resolve is the DNS lookup for the internal-network check. Nil disables the
	// check; production wires linkfetch.LookupAddrs.
	resolve hostResolver
	// safetyEnabled gates provider exchanges. Off, every pending target is
	// terminalised as unknown/disabled on the next pass rather than waiting.
	safetyEnabled bool
}

// LinkScanWorkerCapacity is the worker's half of the capacity configuration:
// what it may spend at the provider, and how long it tolerates not knowing
// whether it already did.
type LinkScanWorkerCapacity struct {
	// ProviderSubmitLimit and ProviderSubmitWindow bound submissions across every
	// replica, because the number that matters is the one Cloudflare counts. Zero
	// disables the limit.
	ProviderSubmitLimit  int
	ProviderSubmitWindow time.Duration
	// UncertainTimeout is when an unresolved submission starts being reported as
	// stale. It gates no action — see defaultUncertainTimeout.
	UncertainTimeout time.Duration
}

// MessageBlockedPublisher announces to an author that their message was refused.
//
// The payload is deliberately just an id, a fixed reason, and the fact of the
// block: no body, no URL, no scan id, no provider detail. The author needs to
// stop seeing "checking links…"; nothing else about the verdict is theirs to
// receive. reason is one of the closed set the ws package defines
// (MessageBlockedReasonMaliciousLink, MessageBlockedReasonLinkCheckInconclusive)
// and is what lets a refusal for an inconclusive scan read differently from one
// for a malicious link, on the one event both share.
type MessageBlockedPublisher interface {
	PublishMessageBlocked(ctx context.Context, workspaceID, recipientUserID, messageID, reason string)
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
	return &LinkScanService{
		queue: queue, provider: provider, publisher: publisher, logger: logger,
		persistRetryDelay: submitPersistRetryDelay,
		capacity:          LinkScanWorkerCapacity{UncertainTimeout: defaultUncertainTimeout},
		safetyEnabled:     provider != nil,
	}
}

// SetAnnouncer attaches the per-link convergence (issue #807).
func (s *LinkScanService) SetAnnouncer(announcer *LinkTargetAnnouncer) {
	s.announcer = announcer
}

// SetHostResolver enables the internal-network check before a provider is
// asked: a hostname that resolves into a private range is terminalised as
// internal and never leaves the deployment.
func (s *LinkScanService) SetHostResolver(resolve hostResolver) {
	s.resolve = resolve
}

// SetSafetyEnabled records whether the provider may be consulted. Off, the
// sweep still runs — a flag that stranded rows would be the failure mode issue
// #566 documented — and every pending target converges as unknown/disabled.
func (s *LinkScanService) SetSafetyEnabled(enabled bool) {
	s.safetyEnabled = enabled && s.provider != nil
}

// SetPersistRetryDelay overrides the pause between attempts to write down a
// scan id the provider already accepted.
//
// Exists for tests, which must exercise the retry without waiting on it. There
// is no production caller: the default is the only value a deployment uses.
func (s *LinkScanService) SetPersistRetryDelay(delay time.Duration) {
	s.persistRetryDelay = delay
}

// SetCapacity configures what the worker may spend at the provider.
//
// Called once during startup from config. Leaving it unset is a working
// deployment with no shared submission limit and the default staleness
// threshold — the value that matters for a real one comes from the environment,
// because the right rate depends on the Cloudflare plan this deployment is
// billed under and nothing here may assume one.
//
// Nothing here can enable a second submission for an uncertain attempt. There is
// no such setting, by construction rather than by default.
func (s *LinkScanService) SetCapacity(capacity LinkScanWorkerCapacity) {
	if capacity.UncertainTimeout <= 0 {
		capacity.UncertainTimeout = defaultUncertainTimeout
	}
	s.capacity = capacity
}

// SetMetrics attaches the pipeline collectors.
//
// Entirely optional, and never calling it is a supported deployment: metrics
// default to no-op until SetMetrics is called with a non-nil reporter. Passing
// nil is equally fine and restores that default. Observability is not a
// precondition for running a security control.
func (s *LinkScanService) SetMetrics(metrics *urlsafety.PipelineMetrics) {
	s.metrics = metrics
}

func (s *LinkScanService) observeAttempt(operation, result string) {
	s.metrics.ObserveAttempt(operation, result)
}

func (s *LinkScanService) observeProvider(operation string, started time.Time) {
	s.metrics.ObserveProviderDuration(operation, started)
}

// SetBlockedPublisher attaches the sender-scoped refusal channel.
func (s *LinkScanService) SetBlockedPublisher(publisher MessageBlockedPublisher) {
	s.blockedPublisher = publisher
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
	// Convergence first (issue #807): pending targets past their deadline become
	// unknown, and with the provider switched off every pending target does.
	// This runs whether or not the provider does, so nothing is ever stranded.
	s.terminalizeExpired(ctx)
	// Fan-outs that did not finish in the pass that began them (issue #807):
	// bounded pages, continued from their durable cursor.
	s.announcer.Continue(ctx)
	// Lapsed verdicts next: a URL whose clearance expired must be scanned again
	// before the claim runs, or the withheld message waiting on it would sit
	// decided-but-stale forever — promotable by nothing, re-scanned by nothing.
	s.reopenExpired(ctx)

	moved := 0
	for s.safetyEnabled && moved < linkScanBatchSize && ctx.Err() == nil {
		job, ok, err := s.claimOne(ctx)
		if err != nil {
			return moved, err
		}
		if !ok {
			break
		}
		s.advance(ctx, job)
		moved++
	}
	// The background second opinion (issue #928), last because nothing is
	// waiting on it: every URL in that lane is already clickable, so it yields
	// to every step above that somebody's message is blocked on.
	s.verifySecondary(ctx)
	// Always run, even when nothing was claimed: a verdict written by another
	// replica leaves messages here that nothing else would release.
	s.releaseDecided(ctx)
	s.observeBacklog(ctx)
	// Budget windows are counters, not history: once a window has passed nothing
	// can be counted into it and nothing reads it. Pruned here, bounded and lazy,
	// so the table stays small without a scheduled job to operate.
	if err := s.queue.PruneLinkScanBudget(ctx, budgetWindowRetention); err != nil && ctx.Err() == nil {
		s.logger.WarnContext(ctx, "prune link scan budget", slog.String("error", err.Error()))
	}
	return moved, nil
}

// terminalizeExpired ends every pending target that has waited long enough,
// and announces each one so the links it holds up become interstitials.
func (s *LinkScanService) terminalizeExpired(ctx context.Context) {
	urls, err := s.queue.TerminalizeExpiredLinkScans(ctx)
	if err == nil && !s.safetyEnabled {
		var disabled []string
		disabled, err = s.queue.TerminalizePendingLinkScansDisabled(ctx)
		urls = append(urls, disabled...)
	}
	if err != nil {
		if ctx.Err() == nil {
			s.logger.WarnContext(ctx, "terminalize pending link scans", slog.String("error", err.Error()))
		}
		return
	}
	for _, url := range urls {
		s.observeAttempt(operationResolve, attemptResultDeadline)
		s.announcer.Announce(ctx, url)
	}
}

// reopenExpired requeues verdicts that lapsed while a message waited on them.
//
// Best-effort and counted: a failure here means some messages wait one more
// interval, not that anything unsafe is published.
func (s *LinkScanService) reopenExpired(ctx context.Context) {
	reopened, err := s.queue.ReopenExpiredVerdicts(ctx)
	switch {
	case err != nil:
		if ctx.Err() == nil {
			s.logger.WarnContext(ctx, "reopen expired verdicts", slog.String("error", err.Error()))
		}
	case reopened > 0:
		// Visible on its own so an operator can tell "the provider is slow" from
		// "verdicts keep ageing out before their messages resolve".
		s.metrics.ObserveRevalidations(reopened)
		s.logger.InfoContext(ctx, "link scan reopened expired verdicts", slog.Int("urls", reopened))
	}
}

// observeBacklog publishes the queue gauges once per pass.
//
// Best-effort: a failure to read the backlog is a missing sample, not a reason
// to fail a pass that already did its work.
func (s *LinkScanService) observeBacklog(ctx context.Context) {
	// The nil check here is a cost decision, not a safety one — reporting to a
	// nil reporter is harmless, but the two backlog queries below are not free
	// and there is nowhere to put their results.
	if s.metrics == nil || ctx.Err() != nil {
		return
	}
	if reporter, ok := s.provider.(circuitReporter); ok {
		s.metrics.ObserveCircuitState(string(reporter.CircuitState()))
	}
	byState, oldest, err := s.queue.LinkScanBacklog(ctx)
	if err != nil {
		return
	}
	s.metrics.ObserveBacklog(byState, oldest)

	pending, oldestEvent, err := s.queue.PublishOutboxBacklog(ctx)
	if err != nil {
		return
	}
	s.metrics.ObserveOutbox(pending, oldestEvent)
}

// claimOne leases exactly one due URL.
//
// One at a time, immediately before it is processed, and that is the fix for the
// lease finding rather than a bigger number: a batch leased up front is a batch
// whose last row sits unclaimed-but-leased for as long as the earlier ones take,
// so a slow provider made rows reclaimable while they were still being worked.
// With one row per claim the lease has to cover one provider exchange and
// nothing else, which is a relationship the constants can assert.
//
// The extra query per item is one indexed statement against a partial index that
// is empty whenever there is no backlog.
func (s *LinkScanService) claimOne(ctx context.Context) (storage.LinkScanJob, bool, error) {
	jobs, err := s.queue.ClaimDueLinkScans(ctx, 1)
	if err != nil {
		return storage.LinkScanJob{}, false, fmt.Errorf("claim due link scans: %w", err)
	}
	if len(jobs) == 0 {
		return storage.LinkScanJob{}, false, nil
	}
	return jobs[0], true, nil
}

// advance moves one URL one step: submit it, or read the scan it already has.
//
// Never more than one step per pass. Submitting and then immediately polling
// would spend an exchange on an answer the provider has not had time to produce,
// and the claim has already scheduled when to come back.
func (s *LinkScanService) advance(ctx context.Context, job storage.LinkScanJob) {
	switch job.SubmitState() {
	case storage.SubmitPolling:
		s.pollClaim(ctx, job)
	case storage.SubmitUncertain:
		// A submission was handed to the provider and its outcome was never
		// written down. This is the one state that must not submit: the provider
		// may already have accepted, and asking again is how one logical scan
		// becomes two billed ones.
		s.reconcileUncertainSubmit(ctx, job)
	default:
		s.submitClaim(ctx, job)
	}
}

// submitClaim records the intent, submits, and binds the scan id to the row.
//
// The order is the correction. It used to be submit-then-record, so a crash in
// between left a row indistinguishable from one that had never been submitted —
// and recovery submitted again, buying a second scan for every restart that
// landed in that window. Now the intent is durable *before* the provider is
// called, so the same crash leaves a row that says "an attempt is outstanding",
// and the recovery path is reconciliation rather than resubmission.
//
// A provider error is treated the same way, and that is deliberate: the client
// cannot distinguish "Cloudflare refused" from "Cloudflare accepted and the
// response was lost", because both surface as a failed exchange. Leaving the
// attempt outstanding means the next pass asks the provider what happened
// instead of assuming nothing did.
func (s *LinkScanService) submitClaim(ctx context.Context, job storage.LinkScanJob) {
	if reason := s.refusedByPolicy(ctx, job.CanonicalURL); reason != "" {
		s.recordPolicyTerminal(ctx, job, reason)
		return
	}
	if !s.acquireProviderSubmitCapacity(ctx, job) {
		return
	}
	generation, err := s.queue.BeginLinkScanSubmit(ctx, job.CanonicalURL, job.SubmitGeneration)
	switch {
	case errors.Is(err, storage.ErrLinkScanConflict):
		// Decided, submitted, or re-attempted by another worker while this one
		// held the claim. Nothing to submit, and nothing was spent.
		s.observeAttempt(operationSubmit, attemptResultLeaseLost)
		return
	case err != nil:
		// The intent could not be recorded, so nothing may be submitted: a
		// submission the database does not know about is exactly the unrecoverable
		// state this ordering exists to prevent.
		s.observeAttempt(operationSubmit, attemptResultError)
		s.logFailure(ctx, "begin link scan submit", job, err)
		return
	}

	started := time.Now()
	result, err := s.provider.Check(ctx, job.CanonicalURL, "")
	s.observeProvider(operationSubmit, started)
	switch {
	case errors.Is(err, urlsafety.ErrCheckInProgress):
		s.persistScanID(ctx, job, generation, result.ProviderRef)
	case err != nil:
		// The attempt stays outstanding. It may have been accepted — a timeout
		// after acceptance looks identical from here — so the next pass
		// reconciles rather than submits.
		s.observeAttempt(operationSubmit, attemptResultError)
		s.logFailure(ctx, "submit link scan", job, err)
	default:
		// A synchronous provider answered outright. The ref is bound first so the
		// verdict write goes through the same compare-and-set every poll uses.
		job.ScanUUID = directProviderRef
		if result.ProviderRef != "" {
			job.ScanUUID = result.ProviderRef
		}
		if s.persistScanID(ctx, job, generation, job.ScanUUID) {
			s.recordVerdict(ctx, job, result)
		}
	}
}

// refusedByPolicy names the reason a URL must not be handed to a public
// provider at all — a sensitive URL, an internal host by name, or a host that
// resolves into a private range — or "" when it may. Nothing here is a verdict
// about the link; it is a statement about what this deployment is willing to
// send outside (issue #807 §22-23).
func (s *LinkScanService) refusedByPolicy(ctx context.Context, canonicalURL string) string {
	switch urlsafety.ClassifyURL(canonicalURL) {
	case urlsafety.URLClassSensitive:
		return storage.TerminalReasonSensitive
	case urlsafety.URLClassInternal:
		return storage.TerminalReasonInternal
	}
	if s.resolve == nil {
		return ""
	}
	parsed, err := url.Parse(canonicalURL)
	if err != nil {
		return ""
	}
	_, err = linkfetch.ResolveAndValidate(ctx, s.resolve, parsed.Hostname())
	if errors.Is(err, linkfetch.ErrURLNotAllowed) {
		return storage.TerminalReasonInternal
	}
	// A name that does not resolve is not an internal one. The provider may
	// still know it; the preview fetcher will refuse it on its own.
	return ""
}

// recordPolicyTerminal ends a target the policy refused to ask about, and
// announces it so its links become interstitials now rather than at the
// deadline.
func (s *LinkScanService) recordPolicyTerminal(ctx context.Context, job storage.LinkScanJob, reason string) {
	switch err := s.queue.RecordLinkTargetTerminal(ctx, job.CanonicalURL, reason); {
	case err == nil:
		s.observeAttempt(operationSubmit, attemptResultPolicy)
		s.announcer.Announce(ctx, job.CanonicalURL)
	case errors.Is(err, storage.ErrLinkScanConflict):
		s.observeAttempt(operationSubmit, attemptResultLeaseLost)
	default:
		s.observeAttempt(operationSubmit, attemptResultError)
		s.logFailure(ctx, "record policy terminal", job, err)
	}
}

// persistScanID writes down an id the provider has already given us, trying
// harder than once.
//
// This write is the cheapest thing in the whole path and the most expensive one
// to lose: the scan exists, the account has been billed, and a row without the
// id has to be recovered by asking the provider or — eventually — by paying for
// it again. So a transient database failure is retried briefly while the lease
// is still held, rather than abandoned on the first error.
//
// Giving up is not a resubmission. The row keeps its outstanding attempt and the
// next pass reconciles it.
func (s *LinkScanService) persistScanID(
	ctx context.Context, job storage.LinkScanJob, generation int, scanID string,
) bool {
	for attempt := 0; attempt < submitPersistAttempts; attempt++ {
		err := s.queue.RecordLinkScanSubmission(ctx, job.CanonicalURL, scanID, generation)
		switch {
		case err == nil:
			s.observeAttempt(operationSubmit, attemptResultSuccess)
			return true
		case errors.Is(err, storage.ErrLinkScanConflict):
			// The row moved on: decided, or a newer attempt owns it. This scan is
			// orphaned at the provider, and submitting again would only add a third.
			s.observeAttempt(operationSubmit, attemptResultLeaseLost)
			return false
		}
		s.logFailure(ctx, "record link scan submission", job, err)
		if ctx.Err() != nil || attempt == submitPersistAttempts-1 {
			break
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(s.persistRetryDelay):
		}
	}
	// The scan is running and its id was not stored. Reported as its own outcome
	// rather than as an error, because an operator needs to see the uncertainty
	// window filling up — it is the thing that eventually costs a duplicate.
	s.observeAttempt(operationSubmit, attemptResultUncertain)
	return false
}

// acquireProviderSubmitCapacity takes one submission from the deployment-wide
// allowance, and reports whether the worker may proceed.
//
// The HTTP limiter caps how often a *client* may ask for something. It says
// nothing about how fast this deployment talks to Cloudflare, because one
// request can queue ten URLs, retries and revalidations queue more that nobody
// asked for, and every replica does it independently. This is the limit on the
// thing the provider actually counts.
//
// Running out is throttling, not failure: the row keeps its place and comes back
// after the backoff the claim already applied. Nothing is recorded as a provider
// error, because the provider was never asked.
func (s *LinkScanService) acquireProviderSubmitCapacity(
	ctx context.Context, job storage.LinkScanJob,
) bool {
	allowed, err := s.queue.ReserveProviderSubmit(ctx,
		s.capacity.ProviderSubmitLimit, s.capacity.ProviderSubmitWindow)
	switch {
	case err != nil:
		// Fail closed. Not being able to tell whether there is allowance left is
		// not permission to spend it.
		s.observeAttempt(operationSubmit, attemptResultError)
		s.logFailure(ctx, "reserve provider submit", job, err)
		return false
	case !allowed:
		s.observeAttempt(operationSubmit, attemptResultThrottled)
		return false
	}
	return true
}

// reconcileUncertainSubmit resolves a submission whose outcome was never
// recorded, without submitting.
//
// The rule this enforces is the finding, stated as behaviour: an absent scan id
// is not evidence that nothing was submitted. Before anything is sent again, the
// provider is asked whether the scan it may have accepted exists — and a search
// that answers "nothing" or fails to answer at all leaves the row exactly where
// it is. Remote indexing is not synchronous and a throttled search is not an
// absence; treating either as one is how the duplicate gets bought.
func (s *LinkScanService) reconcileUncertainSubmit(ctx context.Context, job storage.LinkScanJob) {
	searcher, canSearch := s.provider.(LinkScanSearcher)
	if !canSearch {
		s.metrics.ObserveReconciliation(urlsafety.ReconcileUnsupported)
		s.reportStaleAttempt(job)
		return
	}
	started := time.Now()
	record, matches, err := searcher.FindRecentScan(ctx, job.CanonicalURL, job.SubmitStartedAt)
	s.observeProvider(operationSubmit, started)
	switch {
	case errors.Is(err, urlsafety.ErrSearchUnsupported):
		// This deployment's provider client cannot search, so the attempt can only
		// be settled by the horizon policy. Counted separately because the remedy
		// is different: it is a wiring fact, not a provider one.
		s.metrics.ObserveReconciliation(urlsafety.ReconcileUnsupported)
		s.reportStaleAttempt(job)
		return
	case errors.Is(err, urlsafety.ErrNotCheckable):
		// Nothing eligible yet. Ask again next pass.
		s.metrics.ObserveReconciliation(urlsafety.ReconcileNotFound)
		s.reportStaleAttempt(job)
		return
	case err != nil:
		s.metrics.ObserveReconciliation(urlsafety.ReconcileError)
		s.logFailure(ctx, "search link scan", job, err)
		s.reportStaleAttempt(job)
		return
	}
	if matches > 1 {
		// Several scans of this URL are eligible, which usually means an earlier
		// bounded resubmit really did create a duplicate. Counted so that is
		// visible; the newest is adopted either way.
		s.metrics.ObserveReconciliation(urlsafety.ReconcileAmbiguous)
	}
	if err := s.queue.AdoptScanUUID(ctx, job.CanonicalURL, record.UUID, job.SubmitGeneration); err != nil {
		// The row moved on while the search ran. Somebody else resolved it.
		s.metrics.ObserveReconciliation(urlsafety.ReconcileError)
		s.observeAttempt(operationSubmit, attemptResultLeaseLost)
		return
	}
	s.metrics.ObserveReconciliation(urlsafety.ReconcileAdopted)
	s.observeAttempt(operationSubmit, attemptResultSuccess)
}

// reportStaleAttempt marks an attempt reconciliation has not settled for a long
// time.
//
// It does nothing else, and that is the whole design. There is no branch below
// this one that submits: once a POST may have reached the provider, no amount of
// elapsed time makes another one safe, so the horizon is a reporting threshold
// rather than a policy switch.
//
// The trade-off is stated rather than hidden. A URL whose scan the provider
// accepted, and whose id neither the local write nor the search could recover,
// stays undecided — and its messages stay withheld. That is a real availability
// cost, chosen over the alternative: buying a second scan for a submission that
// probably already exists, on a provider with no idempotency token to make the
// second one free. The metric is how an operator finds out, and the runbook says
// what to do about it. Restarting the worker is explicitly not it: a restart
// resubmits nothing.
func (s *LinkScanService) reportStaleAttempt(job storage.LinkScanJob) {
	if time.Since(job.SubmitStartedAt) >= s.capacity.UncertainTimeout {
		s.metrics.ObserveReconciliation(urlsafety.ReconcileStale)
	}
}

// pollClaim reads a scan already submitted and records a final verdict.
func (s *LinkScanService) pollClaim(ctx context.Context, job storage.LinkScanJob) {
	started := time.Now()
	result, err := s.provider.Check(ctx, job.CanonicalURL, job.ScanUUID)
	s.observeProvider(operationPoll, started)
	switch {
	case errors.Is(err, urlsafety.ErrCheckInProgress):
		// Still running. Not an outcome, not an error, and above all not a
		// clearance — the row stays pending and is read again next time.
		s.observeAttempt(operationPoll, attemptResultPending)
		return
	case err != nil:
		s.observeAttempt(operationPoll, attemptResultRetry)
		s.logFailure(ctx, "poll link scan", job, err)
		return
	}
	verdict := result.Verdict.LegacyVerdict()
	if !verdict.IsFinal() && result.Verdict != urlsafety.ReputationUnknown {
		// The provider layer already refuses anything that is not one of the three
		// known answers, and this refuses it again before writing. Belt and braces
		// on purpose: this is the one call that turns a provider answer into a row
		// a link is released by, so a provider returning a zero value with a nil
		// error must not be able to write one.
		s.observeAttempt(operationPoll, attemptResultError)
		s.logFailure(ctx, "poll link scan", job, urlsafety.ErrUnavailable)
		return
	}
	// ReputationUnknown is the provider confirming a finished check with no
	// usable verdict — the production incident this branch exists for. It is
	// terminal and fail-closed: recorded once as inconclusive, never polled
	// again, and there is no path from here into resubmission.
	s.recordVerdict(ctx, job, result)
}

// recordVerdict writes a terminal poll outcome — safe, malicious, or
// inconclusive — and reports the attempt. Shared by pollClaim's ordinary
// success path and its ErrScanInconclusive branch, because both are "this scan
// id is decided, write it down and stop polling"; only the outcome label
// differs.
func (s *LinkScanService) recordVerdict(
	ctx context.Context, job storage.LinkScanJob, answer urlsafety.ReputationResult,
) {
	verdict := answer.Verdict.LegacyVerdict()
	outcome := attemptResultSuccess
	if verdict == urlsafety.VerdictInconclusive {
		outcome = attemptResultInconclusive
	}
	write := storage.LinkVerdictWrite{
		CanonicalURL:      job.CanonicalURL,
		ScanUUID:          job.ScanUUID,
		Verdict:           verdict,
		EvidenceExpiresAt: answer.ExpiresAt,
		VerifySecondary:   s.wantsSecondOpinion(answer),
	}
	result := outcome
	switch err := s.queue.RecordLinkVerdict(ctx, write); {
	case err == nil:
		s.observeAttempt(operationPoll, result)
		s.converge(ctx, job.CanonicalURL)
	case errors.Is(err, storage.ErrLinkScanConflict):
		// This worker's lease had already been lost and the row now carries a
		// different scan. Its answer describes a scan nobody is waiting on.
		s.observeAttempt(operationPoll, attemptResultLeaseLost)
	default:
		s.observeAttempt(operationPoll, attemptResultError)
		s.logFailure(ctx, "record link verdict", job, err)
	}
}

// wantsSecondOpinion reports whether a clearance should be verified in the
// background by the secondary provider (issue #928).
//
// Two conditions, and both are necessary:
//
//   - the answer is a clearance. There is nothing to double-check about a
//     condemnation — it is already the strictest outcome — and nothing to
//     double-check about a non-answer, which decided nothing;
//   - it did not come from the secondary itself. When the primary was
//     unavailable and Cloudflare produced the clearance, asking Cloudflare again
//     is not a second opinion, it is the same one.
//
// A deployment whose provider cannot verify simply never opens the lane: the
// worker checks for the capability before claiming, so the column stays NULL
// and nothing is scheduled that nothing would drain.
func (s *LinkScanService) wantsSecondOpinion(answer urlsafety.ReputationResult) bool {
	if answer.Verdict != urlsafety.ReputationSafe {
		return false
	}
	if answer.Provider == urlsafety.ProviderCloudflareURLScanner {
		return false
	}
	_, canVerify := s.provider.(LinkSecondaryVerifier)
	return canVerify
}

// converge propagates a decided target into its messages: the per-link
// announcer when wired (issue #807), else the aggregate drain alone.
func (s *LinkScanService) converge(ctx context.Context, canonicalURL string) {
	if s.announcer != nil {
		s.announcer.Announce(ctx, canonicalURL)
		return
	}
	publisher, _ := s.publisher.(LinkSafetyChangePublisher)
	if err := drainMessageLinkSafety(ctx, s.queue, canonicalURL, publisher); err != nil && ctx.Err() == nil {
		s.logger.WarnContext(ctx, "converge ordinary link verdict", slog.String("error", err.Error()))
	}
}

// releaseDecided promotes or blocks every withheld message whose links are all
// decided, then delivers the events those promotions wrote.
//
// Since issue #807 no new message is withheld; this drains the rows the
// previous release left in pending_link_scan, whose targets now all converge.
// Remove once no deployment has such rows.
//
// The two halves are deliberately separate. The promotion and its event are one
// transaction, so a crash can never leave a message active with nobody told
// about it; delivery is a second, retryable step that reads the outbox. That is
// what replaced "commit, then hope the websocket publish works".
func (s *LinkScanService) releaseDecided(ctx context.Context) {
	summary, err := s.queue.ResolveDecidedMessages(ctx)
	switch {
	case err != nil:
		s.observeAttempt(operationResolve, attemptResultError)
		if ctx.Err() == nil {
			s.logger.ErrorContext(ctx, "resolve decided messages", slog.String("error", err.Error()))
		}
	case summary.Total() > 0:
		s.observeAttempt(operationResolve, attemptResultSuccess)
		s.logger.InfoContext(ctx, "link scan resolved withheld messages",
			slog.Int("published", summary.Published), slog.Int("blocked", summary.Blocked))
	}
	s.dispatchPublishEvents(ctx)
}

// dispatchPublishEvents broadcasts the promotions the outbox is holding.
//
// An event stays pending until the publish returns and the row is retired, so a
// dispatcher that dies mid-flight leaves the event to the next pass. Delivery is
// at-least-once and the client deduplicates by message id; that is the honest
// contract, and it is strictly better than the previous one, in which a failed
// publish meant the event was simply lost.
func (s *LinkScanService) dispatchPublishEvents(ctx context.Context) {
	if s.publisher == nil && s.blockedPublisher == nil {
		return
	}
	events, err := s.queue.ClaimPublishEvents(ctx, publishDispatchBatch)
	if err != nil {
		s.observeAttempt(operationPublish, attemptResultError)
		if ctx.Err() == nil {
			s.logger.ErrorContext(ctx, "claim publish events", slog.String("error", err.Error()))
		}
		return
	}
	for _, event := range events {
		if ctx.Err() != nil {
			return
		}
		s.deliver(ctx, event)
	}
}

// deliver announces one resolved message, or retires an event that can never be
// announced.
//
// Three outcomes, and they are genuinely different: a promotion goes to the
// conversation, a refusal goes only to its author, and an event whose message was
// deleted in between goes nowhere and must stop being retried.
func (s *LinkScanService) deliver(ctx context.Context, event storage.PublishEvent) {
	if event.Cancelled {
		// Nothing to announce and nothing that a later attempt could fix.
		if err := s.queue.CancelPublishEvent(ctx, event.MessageID); err != nil {
			s.observeAttempt(operationPublish, attemptResultError)
			return
		}
		s.observeAttempt(operationPublish, attemptResultCancelled)
		return
	}
	if event.Blocked() {
		if s.blockedPublisher == nil {
			// Nothing wired to carry it. Left pending so a deployment that gains
			// the publisher later still delivers it.
			s.observeAttempt(operationPublish, attemptResultRetry)
			return
		}
		s.blockedPublisher.PublishMessageBlocked(ctx, event.WorkspaceID, event.TargetID, event.MessageID, event.Reason)
	} else {
		s.publisher.PublishMessageCreated(
			ctx, event.WorkspaceID, event.TargetType, event.TargetID, event.Message,
		)
	}
	if err := s.queue.MarkPublished(ctx, event.MessageID); err != nil {
		// Announced but not retired: it will be delivered again. Safe, because the
		// consumer upserts by message id.
		s.observeAttempt(operationPublish, attemptResultRetry)
		return
	}
	s.observeAttempt(operationPublish, attemptResultSuccess)
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
// The loop owns scheduling and shutdown and nothing else: what one pass actually
// does lives in ProcessDue, and reporting what it did lives in logPass. That
// split is deliberate — a scheduler that also knew about Cloudflare parsing,
// persistence and message resolution is the shape this had before, and it is the
// shape that makes a worker hard to reason about.
//
// It is the same loop presence reconciliation already uses in this service: a
// ticker, a bounded pass, and a context that ends it. No scheduler, no broker,
// no goroutine per message.
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
			logPass(ctx, logger, moved, err)
		}
	}
}

// logPass reports one pass.
//
// A cancelled context is not a failure worth logging: it is the shutdown that
// was asked for, and a stack of errors on every deploy is noise an operator
// learns to ignore. A pass that moved nothing is not logged either — that is the
// overwhelmingly common case.
func logPass(ctx context.Context, logger *slog.Logger, moved int, err error) {
	switch {
	case err != nil && ctx.Err() == nil:
		logger.ErrorContext(ctx, "link scan pass failed", slog.String("error", err.Error()))
	case err == nil && moved > 0:
		logger.InfoContext(ctx, "link scan pass", slog.Int("urls", moved))
	}
}
