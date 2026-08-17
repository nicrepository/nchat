package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/urlsafety"
)

// The RF-21 scan pipeline for link previews.
//
// The preview path can only ever *read* a verdict: Cloudflare URL Scanner is
// submit-then-poll, its result endpoint answers 404 while a scan runs, and the
// provider recommends polling every 10-30 seconds. So a preview that finds no
// verdict records the need for one and answers "being checked"; this worker is
// what turns that record into an answer.
//
// Before it existed the preview submitted a scan directly, threw the provider's
// id away, and never looked again — so a URL could never actually be decided,
// and every retry of the same preview spent another submission.

const (
	// LinkScanPollInterval is how often a replica looks for work. It matches the
	// fastest cadence Cloudflare recommends polling a scan at, and the claim is
	// one indexed query against a partial index that is empty once everything is
	// decided, so idle polling costs almost nothing.
	LinkScanPollInterval = 10 * time.Second

	// linkScanBatchSize bounds one pass. The worker claims one row at a time,
	// immediately before processing it, so this is a budget for the pass rather
	// than a lease-sizing input.
	linkScanBatchSize = 8

	// submitPersistAttempts bounds how hard one worker tries to write down a scan
	// id the provider already gave it. The provider has been billed by then, and
	// the only thing between that and a usable row is a local write — retrying it
	// briefly is far cheaper than the reconciliation, and far cheaper than the
	// duplicate a resubmission would cost.
	submitPersistAttempts = 3
	// submitPersistRetryDelay paces those attempts. A field on the service so a
	// test can drive the retry without waiting.
	submitPersistRetryDelay = 200 * time.Millisecond

	// defaultUncertainTimeout is when an unresolved submission stops being
	// ordinary and starts being something an operator should look at.
	//
	// It gates no action: reconciliation continues on the same schedule and no
	// submission is ever made, because no amount of elapsed time turns "the
	// provider may already have this scan" into "it is safe to send another".
	defaultUncertainTimeout = 15 * time.Minute

	// budgetWindowRetention is how long spent budget windows are kept.
	budgetWindowRetention = time.Hour
)

// ErrLinkScanConflict reports that a compare-and-set lost: another worker
// advanced the row while this one was talking to the provider. The caller must
// not retry the provider call — the work has been done or reassigned — and must
// not treat it as an error worth alarming about.
var ErrLinkScanConflict = errors.New("link scan: superseded by another worker")

// Submission states. The one that did not exist before is submit_uncertain, and
// it is the whole point: a URL with no scan id had either never been submitted
// or been submitted with the answer lost, and those two demand opposite things.
const (
	StateSubmitPending   = "submit_pending"
	StateSubmitting      = "submitting"
	StateSubmitUncertain = "submit_uncertain"
	StatePolling         = "polling"
)

// LinkScanJob is one claimed URL.
type LinkScanJob struct {
	URLDigest    []byte
	CanonicalURL string
	// State is one of the constants above.
	State    string
	ScanUUID string
	Attempts int

	// SubmitStartedAt is when the outstanding attempt was handed to the provider,
	// and zero when there is none. It is what the reconciliation search filters
	// from: a scan older than the attempt is somebody else's history.
	SubmitStartedAt time.Time
	// SubmitGeneration is the compare-and-set token for the current attempt.
	SubmitGeneration int
}

// Admission outcomes. A closed set, because they are metric label values.
const (
	AdmissionAllowed = "allowed"
	// AdmissionServiceBudget means this service has introduced as many new URLs
	// as its window allows.
	AdmissionServiceBudget = "service_budget"
	// AdmissionBacklog means the queue of undecided scans is full.
	AdmissionBacklog = "backlog"
)

// LinkScanCapacity is what this service will spend on new provider work.
//
// The budget is service-wide rather than per tenant, and that is a limitation
// worth stating: a preview request carries no workspace this service can trust —
// it is fetched for a link, not for a tenant — and deriving one from a header
// would be a tenant boundary any client could choose. Per-caller fairness stays
// in the request limiter, which is where it can actually be enforced.
type LinkScanCapacity struct {
	NewURLBudget   int
	BudgetWindow   time.Duration
	MaxPendingJobs int
}

// LinkScanAdmission is what one admission decided.
type LinkScanAdmission struct {
	NewScanCost int
	Result      string
}

// Allowed reports whether the scan may be queued.
func (a LinkScanAdmission) Allowed() bool { return a.Result == AdmissionAllowed }

// ErrLinkScanCapacity reports that new provider work was refused.
//
// Deliberately distinct from a malicious verdict and from a provider failure: a
// spent window says nothing about the link, and nothing was attempted.
var ErrLinkScanCapacity = errors.New("link scan: capacity exceeded")

// LinkScanStore is the durable half: the rows that survive a restart. An
// interface so the worker can be tested without a database.
type LinkScanStore interface {
	LoadVerdict(ctx context.Context, canonicalURL string) (urlsafety.Verdict, bool, error)
	// AdmitScan reserves capacity for a URL that needs new provider work and
	// queues it, atomically. A URL already answered or already queued costs
	// nothing and is always admitted.
	AdmitScan(ctx context.Context, canonicalURL string, capacity LinkScanCapacity) (LinkScanAdmission, error)
	ClaimDueScans(ctx context.Context, batchSize int) ([]LinkScanJob, error)
	// BeginSubmit records the intent to submit before the provider is called.
	BeginSubmit(ctx context.Context, urlDigest []byte, expectedGeneration int) (int, error)
	// MarkSubmitUncertain moves an outstanding attempt to the state that must
	// never be resubmitted from without asking the provider first.
	MarkSubmitUncertain(ctx context.Context, urlDigest []byte, generation int) error
	RecordSubmission(ctx context.Context, urlDigest []byte, scanUUID string, generation int) error
	// AdoptScanUUID binds a scan id recovered from the provider's search.
	AdoptScanUUID(ctx context.Context, urlDigest []byte, scanUUID string, generation int) error
	RecordVerdict(ctx context.Context, urlDigest []byte, scanUUID string, verdict urlsafety.Verdict, ttl time.Duration) error
	Backlog(ctx context.Context) (map[string]int, time.Duration, error)
	// ReserveProviderSubmit takes one submission from the deployment-wide
	// provider allowance, shared across replicas.
	ReserveProviderSubmit(ctx context.Context, limit int, window time.Duration) (bool, error)
	// PruneLinkScanBudget drops budget windows that can no longer be counted into.
	PruneLinkScanBudget(ctx context.Context, olderThan time.Duration) error
}

// LinkScanSearcher is the recovery half, and is deliberately optional: a
// provider client that cannot search is a working deployment, and the uncertain
// row waits out its horizon instead. It must never become a second way to obtain
// a verdict — it recovers a scan id and nothing else.
type LinkScanSearcher interface {
	FindRecentScan(ctx context.Context, canonicalURL string, since time.Time) (urlsafety.ScanRecord, int, error)
}

// LinkScanProvider is the Cloudflare half. *urlsafety.Service satisfies it,
// which keeps the strictness rule — only Safe and Malicious are answers — in one
// place shared with chat-service.
type LinkScanProvider interface {
	Submit(ctx context.Context, canonicalURL string) (string, error)
	Poll(ctx context.Context, canonicalURL, scanID string) (urlsafety.Verdict, error)
}

// LinkScanService drains file-service's scan queue.
type LinkScanService struct {
	store    LinkScanStore
	provider LinkScanProvider
	metrics  *urlsafety.PipelineMetrics
	logger   *slog.Logger

	capacity          LinkScanWorkerCapacity
	persistRetryDelay time.Duration
}

// LinkScanWorkerCapacity is the worker's half of the capacity configuration.
type LinkScanWorkerCapacity struct {
	// ProviderSubmitLimit and ProviderSubmitWindow bound submissions across every
	// replica, because the number that matters is the one Cloudflare counts.
	ProviderSubmitLimit  int
	ProviderSubmitWindow time.Duration
	// UncertainTimeout is when an unresolved submission starts being reported as
	// stale. It gates no action — see defaultUncertainTimeout.
	UncertainTimeout time.Duration
}

// NewLinkScanService builds the worker's use case.
func NewLinkScanService(store LinkScanStore, provider LinkScanProvider, logger *slog.Logger) *LinkScanService {
	if logger == nil {
		logger = slog.Default()
	}
	return &LinkScanService{
		store: store, provider: provider, logger: logger,
		persistRetryDelay: submitPersistRetryDelay,
		capacity:          LinkScanWorkerCapacity{UncertainTimeout: defaultUncertainTimeout},
	}
}

// SetPersistRetryDelay overrides the pause between attempts to write down a scan
// id the provider already accepted. Exists for tests, which must exercise the
// retry without waiting on it.
func (s *LinkScanService) SetPersistRetryDelay(delay time.Duration) {
	s.persistRetryDelay = delay
}

// SetCapacity configures what the worker may spend at the provider.
//
// Leaving it unset is a working deployment with no shared submission limit and
// the default staleness threshold; the value that matters comes from the
// environment, because the right rate depends on the Cloudflare plan this
// deployment is billed under and nothing here may assume one.
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
// default to no-op until SetMetrics is called with a non-nil reporter. Every
// *PipelineMetrics method tolerates a nil receiver, so the worker below calls
// them unconditionally rather than guarding each site — the tolerance lives in
// one type instead of being re-derived at every use.
// TestLinkScanServiceRunsWithoutMetrics holds that contract.
func (s *LinkScanService) SetMetrics(metrics *urlsafety.PipelineMetrics) {
	s.metrics = metrics
}

// ProcessDue claims and advances one batch.
//
// One claim per item, taken immediately before it is processed, so the lease has
// to cover a single provider exchange rather than a whole serial batch. A
// failure on one URL never stops the pass: every failure path leaves the row
// where it was with its next attempt already scheduled by the claim, which is
// what makes a provider outage cost geometrically fewer attempts rather than a
// retry storm — and what keeps an undecided URL undecided rather than cleared.
func (s *LinkScanService) ProcessDue(ctx context.Context) (int, error) {
	moved := 0
	for moved < linkScanBatchSize && ctx.Err() == nil {
		jobs, err := s.store.ClaimDueScans(ctx, 1)
		if err != nil {
			return moved, fmt.Errorf("claim due link scans: %w", err)
		}
		if len(jobs) == 0 {
			break
		}
		s.advance(ctx, jobs[0])
		moved++
	}
	s.observeBacklog(ctx)
	if err := s.store.PruneLinkScanBudget(ctx, budgetWindowRetention); err != nil && ctx.Err() == nil {
		s.logger.WarnContext(ctx, "prune link scan budget", slog.String("error", err.Error()))
	}
	return moved, nil
}

// advance moves one URL one step: start its scan, or read the one it has.
//
// Never more than one step per pass. Submitting and then immediately polling
// would spend an exchange on an answer the provider has not had time to produce.
func (s *LinkScanService) advance(ctx context.Context, job LinkScanJob) {
	switch {
	case job.ScanUUID != "":
		s.pollClaim(ctx, job)
	case job.State == StateSubmitting || job.State == StateSubmitUncertain:
		// An attempt was handed to the provider and its outcome was never written
		// down. Both states must not submit: the provider may already have
		// accepted, and asking again is how one logical scan becomes two billed
		// ones. submitting is what remains if parking as submit_uncertain failed.
		s.reconcileUncertainSubmit(ctx, job)
	case job.State == StateSubmitPending:
		s.submitClaim(ctx, job)
	}
}

// submitClaim records the intent, submits, and binds the scan id to the row.
//
// The order is the correction. It used to be submit-then-record, so a crash in
// between left a row indistinguishable from one that had never been submitted —
// and recovery submitted again, buying a second scan for every restart that
// landed in that window. Now the intent is durable *before* the provider is
// called.
//
// A provider error takes the same path, deliberately: the client cannot
// distinguish "Cloudflare refused" from "Cloudflare accepted and the response
// was lost", because both surface as a failed exchange. Leaving the attempt
// uncertain means the next pass asks what happened instead of assuming nothing
// did.
func (s *LinkScanService) submitClaim(ctx context.Context, job LinkScanJob) {
	if !s.acquireProviderSubmitCapacity(ctx, job) {
		return
	}
	generation, err := s.store.BeginSubmit(ctx, job.URLDigest, job.SubmitGeneration)
	switch {
	case errors.Is(err, ErrLinkScanConflict):
		s.metrics.ObserveAttempt(urlsafety.OperationSubmit, urlsafety.AttemptLeaseLost)
		return
	case err != nil:
		// The intent could not be recorded, so nothing may be submitted: a
		// submission the database does not know about is exactly the
		// unrecoverable state this ordering exists to prevent.
		s.metrics.ObserveAttempt(urlsafety.OperationSubmit, urlsafety.AttemptError)
		s.logFailure(ctx, "begin link scan submit", job, err)
		return
	}

	started := time.Now()
	scanID, err := s.provider.Submit(ctx, job.CanonicalURL)
	s.metrics.ObserveProviderDuration(urlsafety.OperationSubmit, started)
	if err != nil {
		s.metrics.ObserveAttempt(urlsafety.OperationSubmit, urlsafety.AttemptError)
		s.logFailure(ctx, "submit link scan", job, err)
		s.markUncertain(ctx, job, generation)
		return
	}
	s.persistScanID(ctx, job, generation, scanID)
}

// persistScanID writes down an id the provider has already given us, trying
// harder than once.
//
// This write is the cheapest thing in the path and the most expensive one to
// lose: the scan exists, the account has been billed, and a row without the id
// has to be recovered by asking the provider or — eventually — by paying for it
// again. Giving up is not a resubmission: the row becomes uncertain and the next
// pass reconciles it.
func (s *LinkScanService) persistScanID(
	ctx context.Context, job LinkScanJob, generation int, scanID string,
) {
	for attempt := 0; attempt < submitPersistAttempts; attempt++ {
		err := s.store.RecordSubmission(ctx, job.URLDigest, scanID, generation)
		switch {
		case err == nil:
			s.metrics.ObserveAttempt(urlsafety.OperationSubmit, urlsafety.AttemptSuccess)
			return
		case errors.Is(err, ErrLinkScanConflict):
			// The row moved on: decided, or a newer attempt owns it. This scan is
			// orphaned at the provider, and submitting again would only add a third.
			s.metrics.ObserveAttempt(urlsafety.OperationSubmit, urlsafety.AttemptLeaseLost)
			return
		}
		s.logFailure(ctx, "record link scan submission", job, err)
		if ctx.Err() != nil || attempt == submitPersistAttempts-1 {
			break
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(s.persistRetryDelay):
		}
	}
	s.metrics.ObserveAttempt(urlsafety.OperationSubmit, urlsafety.AttemptUncertain)
	s.markUncertain(ctx, job, generation)
}

// markUncertain parks an attempt whose outcome is unknown.
//
// Best-effort: if even this write fails the row stays in 'submitting' with its
// lease lapsing, which the claim treats the same way — either state means an
// attempt is outstanding, and neither is a reason to submit.
func (s *LinkScanService) markUncertain(ctx context.Context, job LinkScanJob, generation int) {
	if err := s.store.MarkSubmitUncertain(ctx, job.URLDigest, generation); err != nil {
		s.logFailure(ctx, "mark submit uncertain", job, err)
	}
}

// acquireProviderSubmitCapacity takes one submission from the deployment-wide
// allowance.
//
// The request limiter caps how often a client may ask for a preview. It says
// nothing about how fast this service talks to Cloudflare: retries and
// revalidations submit without anyone asking, and every replica does it
// independently. This is the limit on the thing the provider actually counts.
func (s *LinkScanService) acquireProviderSubmitCapacity(ctx context.Context, job LinkScanJob) bool {
	allowed, err := s.store.ReserveProviderSubmit(ctx,
		s.capacity.ProviderSubmitLimit, s.capacity.ProviderSubmitWindow)
	switch {
	case err != nil:
		// Fail closed. Not being able to tell whether there is allowance left is
		// not permission to spend it.
		s.metrics.ObserveAttempt(urlsafety.OperationSubmit, urlsafety.AttemptError)
		s.logFailure(ctx, "reserve provider submit", job, err)
		return false
	case !allowed:
		s.metrics.ObserveAttempt(urlsafety.OperationSubmit, urlsafety.AttemptThrottled)
		return false
	}
	return true
}

// reconcileUncertainSubmit resolves a submission whose outcome was never
// recorded, without submitting.
//
// An absent scan id is not evidence that nothing was submitted. Before anything
// is sent again the provider is asked whether the scan it may have accepted
// exists — and a search that answers "nothing" or fails to answer at all leaves
// the row where it is. Remote indexing is not synchronous and a throttled search
// is not an absence; treating either as one is how the duplicate gets bought.
func (s *LinkScanService) reconcileUncertainSubmit(ctx context.Context, job LinkScanJob) {
	searcher, canSearch := s.provider.(LinkScanSearcher)
	if !canSearch {
		s.metrics.ObserveReconciliation(urlsafety.ReconcileUnsupported)
		s.reportStaleAttempt(job)
		return
	}
	started := time.Now()
	record, matches, err := searcher.FindRecentScan(ctx, job.CanonicalURL, job.SubmitStartedAt)
	s.metrics.ObserveProviderDuration(urlsafety.OperationSubmit, started)
	switch {
	case errors.Is(err, urlsafety.ErrSearchUnsupported):
		s.metrics.ObserveReconciliation(urlsafety.ReconcileUnsupported)
		s.reportStaleAttempt(job)
		return
	case errors.Is(err, urlsafety.ErrNotCheckable):
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
	if err := s.store.AdoptScanUUID(ctx, job.URLDigest, record.UUID, job.SubmitGeneration); err != nil {
		s.metrics.ObserveReconciliation(urlsafety.ReconcileError)
		s.metrics.ObserveAttempt(urlsafety.OperationSubmit, urlsafety.AttemptLeaseLost)
		return
	}
	s.metrics.ObserveReconciliation(urlsafety.ReconcileAdopted)
	s.metrics.ObserveAttempt(urlsafety.OperationSubmit, urlsafety.AttemptSuccess)
}

// reportStaleAttempt marks an attempt reconciliation has not settled for a long
// time.
//
// It does nothing else. There is no branch below this one that submits: once a
// POST may have reached the provider, no amount of elapsed time makes another
// one safe, so the horizon is a reporting threshold rather than a policy switch.
//
// The trade-off is deliberate and stated: a URL whose scan the provider accepted
// and whose id neither the local write nor the search could recover stays
// undecided, and its preview keeps answering "being checked". That availability
// cost is chosen over buying a second scan for a submission that probably
// already exists.
func (s *LinkScanService) reportStaleAttempt(job LinkScanJob) {
	if time.Since(job.SubmitStartedAt) >= s.capacity.UncertainTimeout {
		s.metrics.ObserveReconciliation(urlsafety.ReconcileStale)
	}
}

func (s *LinkScanService) pollClaim(ctx context.Context, job LinkScanJob) {
	started := time.Now()
	verdict, err := s.provider.Poll(ctx, job.CanonicalURL, job.ScanUUID)
	s.metrics.ObserveProviderDuration(urlsafety.OperationPoll, started)
	switch {
	case errors.Is(err, urlsafety.ErrScanPending):
		// Still running. Not an outcome, not an error, and above all not a
		// clearance.
		s.metrics.ObserveAttempt(urlsafety.OperationPoll, urlsafety.AttemptPending)
		return
	case errors.Is(err, urlsafety.ErrScanInconclusive):
		// The provider confirms this exact scan finished and produced no usable
		// verdict. Terminal and fail-closed: recorded once, below, and never
		// polled again — no path from here into resubmission.
		s.recordVerdict(ctx, job, urlsafety.VerdictInconclusive)
		return
	case err != nil:
		s.metrics.ObserveAttempt(urlsafety.OperationPoll, urlsafety.AttemptRetry)
		s.logFailure(ctx, "poll link scan", job, err)
		return
	case !verdict.IsFinal():
		// Belt and braces over the provider layer's own rule: this is the call
		// that turns an answer into a row a preview is served by.
		s.metrics.ObserveAttempt(urlsafety.OperationPoll, urlsafety.AttemptError)
		s.logFailure(ctx, "poll link scan", job, urlsafety.ErrUnavailable)
		return
	}
	s.recordVerdict(ctx, job, verdict)
}

// recordVerdict writes a terminal poll outcome — safe, malicious, or
// inconclusive — and reports the attempt. Shared by pollClaim's ordinary
// success path and its ErrScanInconclusive branch: both are "this scan id is
// decided, write it down and stop polling", differing only in the outcome
// label and in the TTL, which RecordVerdict itself ignores for inconclusive.
func (s *LinkScanService) recordVerdict(ctx context.Context, job LinkScanJob, verdict urlsafety.Verdict) {
	result := urlsafety.AttemptSuccess
	if verdict == urlsafety.VerdictInconclusive {
		result = urlsafety.AttemptInconclusive
	}
	switch err := s.store.RecordVerdict(ctx, job.URLDigest, job.ScanUUID, verdict, urlsafety.VerdictTTL); {
	case err == nil:
		s.metrics.ObserveAttempt(urlsafety.OperationPoll, result)
	case errors.Is(err, ErrLinkScanConflict):
		// This worker's lease was already lost and the row now carries a
		// different scan. Its answer describes a scan nobody is waiting on.
		s.metrics.ObserveAttempt(urlsafety.OperationPoll, urlsafety.AttemptLeaseLost)
	default:
		s.metrics.ObserveAttempt(urlsafety.OperationPoll, urlsafety.AttemptError)
		s.logFailure(ctx, "record link verdict", job, err)
	}
}

// observeBacklog publishes the queue gauges once per pass. Best-effort: a
// missing sample is not a reason to fail a pass that did its work.
func (s *LinkScanService) observeBacklog(ctx context.Context) {
	// A cost decision, not a safety one: reporting to a nil reporter is harmless,
	// but the backlog query below is not free and there is nowhere to put it.
	if s.metrics == nil || ctx.Err() != nil {
		return
	}
	byState, oldest, err := s.store.Backlog(ctx)
	if err != nil {
		return
	}
	s.metrics.ObserveBacklog(byState, oldest)
}

// logFailure records a failed step without naming the URL.
//
// A canonical URL carries the path and the query, which is where internal
// identifiers and resource names live, so it is not an operational log field.
// The attempt count is what actually distinguishes "one blip" from "this one
// never succeeds".
func (s *LinkScanService) logFailure(ctx context.Context, step string, job LinkScanJob, err error) {
	if ctx.Err() != nil {
		return
	}
	s.logger.WarnContext(ctx, step,
		slog.Int("attempts", job.Attempts),
		slog.String("error", err.Error()),
	)
}
