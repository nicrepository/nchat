package urlsafety

import (
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/observability"
	"github.com/prometheus/client_golang/prometheus"
)

// Operational metrics for the asynchronous scan pipeline.
//
// The verdict counter in metrics.go answers "what did the provider say". These
// answer the questions an operator actually has when the pipeline is unwell, and
// which nothing could answer before: how much work is queued, how long the
// oldest piece has been stuck, how often each step is retrying, and how slow the
// provider is. Without them a Cloudflare outage looks identical to a healthy
// system with no traffic — messages simply stop appearing.
//
// They live here rather than in each service because both chat-service and
// file-service run the same pipeline shape and an operator should not have to
// learn two dashboards. The `service` label is what separates them.
//
// # Cardinality
//
// Every label below is a closed set decided in this package: a service name, a
// pipeline state, an operation, an outcome. No URL, hostname, query, user id,
// message id, channel id or scan uuid is ever a label — the caller of a scan
// chooses those values, so a label derived from one would let a single hostile
// client create an unbounded number of series.
const (
	// Pipeline states, matching the durable rows the workers claim.
	//
	// StateSubmitUncertain is reported separately and not folded into
	// submit_pending, because the two need completely different responses: one is
	// work waiting to start, the other is work that may already exist at the
	// provider and can only be resolved by reconciliation or by a person. An
	// operator seeing a growing submit_uncertain backlog is seeing something a
	// restart will not fix — and must not fix, since restarting deliberately does
	// not resubmit.
	StateSubmitPending   = "submit_pending"
	StateSubmitUncertain = "submit_uncertain"
	StatePolling         = "polling"

	// Operations. One per step a claim can take.
	OperationSubmit  = "submit"
	OperationPoll    = "poll"
	OperationResolve = "resolve"
	OperationPublish = "publish"

	// Attempt outcomes.
	AttemptSuccess   = "success"
	AttemptPending   = "pending"
	AttemptRetry     = "retry"
	AttemptError     = "error"
	AttemptLeaseLost = "lease_lost"
	// AttemptCancelled marks work retired because it can never succeed — an
	// announcement whose message was deleted before it could be delivered. It is
	// distinct from success and from error: nothing failed, and nothing happened.
	AttemptCancelled = "cancelled"
	// AttemptThrottled marks a step this deployment chose not to take because the
	// shared provider allowance for the window was spent. Distinct from error on
	// purpose: nothing went wrong, and an operator seeing these needs to look at
	// the configured rate rather than at Cloudflare.
	AttemptThrottled = "throttled"
	// AttemptUncertain marks a submission whose outcome could not be written
	// down. The provider may or may not have accepted it; the row is left for
	// reconciliation rather than submitted again.
	AttemptUncertain = "uncertain"
	// AttemptInconclusive marks a poll whose scan the provider confirms is
	// finished but produced no usable verdict. Distinct from AttemptRetry and
	// AttemptError: nothing failed and nothing will be retried — the row is
	// terminal, and an operator seeing these needs to know the pipeline stopped
	// polling on purpose, not that the provider is unwell.
	AttemptInconclusive = "inconclusive"

	// Reconciliation outcomes, for the uncertain-submission window.
	ReconcileAdopted = "adopted"
	// ReconcileNotFound means the provider's search returned nothing eligible.
	// Not the same as "the provider never accepted it": remote indexing is not
	// synchronous, so this is a reason to ask again, never a reason to submit.
	ReconcileNotFound = "not_found"
	// ReconcileError means the search itself failed. Also never a reason to
	// submit — a throttled or failing search must not become a duplicate scan.
	ReconcileError = "error"
	// ReconcileUnsupported means this deployment's provider client cannot search,
	// so an uncertain attempt can only be resolved by the horizon policy.
	ReconcileUnsupported = "unsupported"
	// ReconcileAmbiguous means several scans of the same URL were eligible and
	// the newest was taken. The provider does not promise one scan per URL, so
	// this is a fact about its history rather than about this deployment.
	ReconcileAmbiguous = "ambiguous"
	// ReconcileStale marks an attempt that has been unresolved for longer than
	// the configured horizon. It changes nothing about what the worker does —
	// reconciliation continues, and no submission is ever made — but it is the
	// signal an operator alerts on: a scan whose submission has been unresolved
	// for an hour needs a human, not another POST.
	ReconcileStale = "stale"

	// Verdict-reconciliation outcomes, for an inconclusive scan being re-read
	// (issue #135). A separate closed set from the submission-reconciliation one
	// above because it answers a different question: not "did my submission
	// happen" but "has the provider since produced a verdict for a scan that
	// finished without one". They share ReconcileUnsupported, which means the same
	// thing in both.
	//
	// Every value is decided here. The provider's own refusal text is never one of
	// them: it is an English sentence chosen by Cloudflare, it can contain the
	// hostname, and a metric label built from it would be both unbounded and a
	// URL leak into a scrape.
	ReconcileRequested = "requested"
	// ReconcileCandidateFound counts a search that produced an exact-URL
	// candidate. It is counted before the report is read, so the ratio between it
	// and the three outcomes below shows how often a candidate turns into an
	// answer.
	ReconcileCandidateFound = "candidate_found"
	// ReconcileNoCandidate means the account-scoped search found no scan for
	// exactly this canonical URL. The ordinary answer, and the expected one when
	// the provider refused the scan because a *different* account scanned the
	// hostname recently.
	ReconcileNoCandidate = "no_candidate"
	// ReconcileVerdictSafe and ReconcileVerdictMalicious are the only two
	// outcomes that change stored state, and they are counted separately because
	// the second one means a published message just lost its links.
	ReconcileVerdictSafe      = "safe"
	ReconcileVerdictMalicious = "malicious"
	// ReconcileStillInconclusive means a candidate existed and its report still
	// carries no usable verdict, or is still running. Nothing changed.
	ReconcileStillInconclusive = "still_inconclusive"
	// ReconcileRateLimited counts a manual reconciliation refused before the
	// provider was asked. Distinct from an error: nothing failed, and an operator
	// seeing these is looking at a client that is clicking, not at Cloudflare.
	ReconcileRateLimited = "rate_limited"
	// ReconcileProviderError means the question could not be asked or the answer
	// could not be trusted. Never a reason to submit.
	ReconcileProviderError = "provider_error"

	// Admission outcomes, mirroring the storage layer's closed set.
	AdmissionAllowed  = "allowed"
	AdmissionRejected = "rejected"

	// revalidationReasonExpired is the only reason a verdict is requeued today.
	// A constant so the label set stays closed.
	revalidationReasonExpired = "expired"
)

// PipelineMetrics reports the health of one service's scan pipeline.
//
// A nil *PipelineMetrics is usable and does nothing, which is what lets a caller
// wire observability optionally — the same contract *Metrics already has.
type PipelineMetrics struct {
	service string

	pending    *prometheus.GaugeVec
	oldestAge  *prometheus.GaugeVec
	attempts   *prometheus.CounterVec
	providerMs *prometheus.HistogramVec

	outboxPending *prometheus.GaugeVec
	outboxAge     *prometheus.GaugeVec
	revalidations *prometheus.CounterVec

	admissions      *prometheus.CounterVec
	reconciliations *prometheus.CounterVec
	verdictRecon    *prometheus.CounterVec
}

// NewPipelineMetrics registers the pipeline collectors for one service.
//
// The collectors are shared definitions with a `service` label rather than
// per-service metric names, so a dashboard panel is one query. Registration
// failure returns nil, which disables reporting rather than failing start-up: a
// metric that cannot be registered is not a reason to refuse to run a security
// control.
func NewPipelineMetrics(metrics *observability.Metrics, service string) *PipelineMetrics {
	if metrics == nil || service == "" {
		return nil
	}
	pending := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "nchat_link_scan_pending",
		Help: "URL scans awaiting a verdict, by pipeline state.",
	}, []string{"service", "state"})
	oldestAge := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "nchat_link_scan_oldest_pending_age_seconds",
		Help: "Age of the oldest scan still awaiting a verdict.",
	}, []string{"service"})
	attempts := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nchat_link_scan_attempts_total",
		Help: "Scan pipeline steps by operation and outcome.",
	}, []string{"service", "operation", "result"})
	providerMs := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "nchat_link_scan_provider_duration_seconds",
		Help: "Latency of one Cloudflare URL Scanner exchange.",
		// The provider's own request ceiling is 10s, so the buckets are chosen to
		// resolve the range that matters and to make the timeout itself visible
		// as a pile-up in the last bucket.
		Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
	}, []string{"service", "operation"})

	outboxPending := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "nchat_message_publish_outbox_pending",
		Help: "Promotion events written but not yet broadcast.",
	}, []string{"service"})
	outboxAge := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "nchat_message_publish_outbox_oldest_pending_age_seconds",
		Help: "Age of the oldest promotion event still undelivered.",
	}, []string{"service"})

	revalidations := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nchat_link_scan_revalidations_total",
		Help: "Verdicts requeued for a fresh scan, by reason.",
	}, []string{"service", "reason"})

	// The capacity signal an operator alerts on. `result` says whether the
	// operation was admitted; `reason` says which ceiling refused it, and both
	// are closed sets decided in this package — the tenant that was refused is
	// deliberately absent, because a workspace id as a label is an unbounded
	// series and a tenant identifier in a scrape.
	admissions := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nchat_link_scan_admissions_total",
		Help: "Operations requiring new provider work, by admission outcome.",
	}, []string{"service", "result", "reason"})

	// The uncertainty window, which is otherwise invisible: a submission whose
	// outcome was never written down looks exactly like a slow one.
	reconciliations := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nchat_link_scan_submit_reconciliation_total",
		Help: "Attempts to resolve a submission whose outcome was not recorded.",
	}, []string{"service", "result"})

	// The inconclusive-recovery signal (issue #135). Separate from the submission
	// counter above because the two describe different pipelines: that one is
	// "did my POST happen", this one is "did a scan that finished with nothing
	// ever produce an answer". `source` distinguishes a reader pressing the button
	// from the background pass, which is the difference between a user waiting and
	// a schedule running, and it is a two-value closed set.
	verdictRecon := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nchat_link_safety_reconcile_total",
		Help: "Attempts to obtain a verdict for a scan that finished without one.",
	}, []string{"service", "source", "result"})

	if !registerAll(metrics,
		pending, oldestAge, attempts, providerMs, outboxPending, outboxAge, revalidations,
		admissions, reconciliations, verdictRecon,
	) {
		return nil
	}
	return &PipelineMetrics{
		service: service, pending: pending, oldestAge: oldestAge,
		attempts: attempts, providerMs: providerMs,
		outboxPending: outboxPending, outboxAge: outboxAge,
		revalidations: revalidations,
		admissions:    admissions, reconciliations: reconciliations,
		verdictRecon: verdictRecon,
	}
}

// Reconciliation sources. A closed two-value set: who asked.
const (
	// ReconcileSourceManual is a reader who pressed "Verificar novamente".
	ReconcileSourceManual = "manual"
	// ReconcileSourceBackground is the worker's own bounded schedule.
	ReconcileSourceBackground = "background"
)

// ObserveVerdictReconciliation counts one attempt to obtain a verdict for an
// inconclusive scan.
//
// Both labels come from closed sets decided in this package. There is no
// parameter here that could carry a URL, a hostname, a scan uuid, a message id
// or a user id, which is what keeps the series count bounded by
// services × 2 × outcomes rather than by traffic.
func (m *PipelineMetrics) ObserveVerdictReconciliation(source, result string) {
	if m == nil {
		return
	}
	m.verdictRecon.WithLabelValues(m.service, source, result).Inc()
}

// ObserveAdmission counts one capacity decision.
//
// The reason is only meaningful for a refusal; an allowed operation reports "ok"
// so the label is always present and a dashboard never has to handle a missing
// dimension. Both values come from the caller's own closed set — nothing derived
// from a URL, a tenant or a user may be passed here, and there is no parameter
// that could carry one.
func (m *PipelineMetrics) ObserveAdmission(result, reason string) {
	if m == nil {
		return
	}
	m.admissions.WithLabelValues(m.service, result, reason).Inc()
}

// ObserveReconciliation counts one attempt to resolve an uncertain submission.
func (m *PipelineMetrics) ObserveReconciliation(result string) {
	if m == nil {
		return
	}
	m.reconciliations.WithLabelValues(m.service, result).Inc()
}

// registerAll registers every collector, reporting failure instead of crashing.
//
// observability.Metrics.Register wraps MustRegister, which panics on a duplicate.
// A duplicate here is a wiring mistake — a service builds this once — but an
// observability mistake must not take the process down: a chat server that
// refuses to start because a counter was registered twice is a worse outcome
// than one running without that counter. The recover turns it into the same nil
// result a disabled registry already produces, and the caller's nil-tolerant
// methods do the rest.
func registerAll(metrics *observability.Metrics, collectors ...prometheus.Collector) (ok bool) {
	defer func() {
		if recovered := recover(); recovered != nil {
			ok = false
		}
	}()
	for _, collector := range collectors {
		if !metrics.Register(collector) {
			return false
		}
	}
	return true
}

// ObserveBacklog publishes the current queue depth and the age of its oldest
// entry.
//
// Called once per worker pass rather than on every write: a gauge describes a
// level, and one sample per poll interval is enough to see a backlog forming
// without the workers doing extra queries to maintain it.
func (m *PipelineMetrics) ObserveBacklog(byState map[string]int, oldest time.Duration) {
	if m == nil {
		return
	}
	// Every known state is reported, including the ones at zero. A state that
	// simply stops being exported reads as "no data" on a dashboard, which is
	// indistinguishable from the scraper being broken.
	for _, state := range []string{StateSubmitPending, StateSubmitUncertain, StatePolling} {
		m.pending.WithLabelValues(m.service, state).Set(float64(byState[state]))
	}
	m.oldestAge.WithLabelValues(m.service).Set(oldest.Seconds())
}

// ObserveOutbox publishes the promotion-event backlog.
//
// Separate from the scan backlog because the two fail for different reasons and
// an operator needs to tell them apart: scans back up when Cloudflare is slow,
// events back up when the websocket hub is refusing traffic.
func (m *PipelineMetrics) ObserveOutbox(pending int, oldest time.Duration) {
	if m == nil {
		return
	}
	m.outboxPending.WithLabelValues(m.service).Set(float64(pending))
	m.outboxAge.WithLabelValues(m.service).Set(oldest.Seconds())
}

// ObserveRevalidations counts verdicts requeued because they aged out.
//
// Separate from the retry counter because the cause is different and so is the
// remedy: retries mean the provider is failing, revalidations mean verdicts are
// expiring faster than the messages waiting on them resolve.
func (m *PipelineMetrics) ObserveRevalidations(count int) {
	if m == nil || count <= 0 {
		return
	}
	m.revalidations.WithLabelValues(m.service, revalidationReasonExpired).Add(float64(count))
}

// ObserveAttempt counts one pipeline step.
func (m *PipelineMetrics) ObserveAttempt(operation, result string) {
	if m == nil {
		return
	}
	m.attempts.WithLabelValues(m.service, operation, result).Inc()
}

// ObserveProviderDuration records how long one provider exchange took.
func (m *PipelineMetrics) ObserveProviderDuration(operation string, started time.Time) {
	if m == nil {
		return
	}
	m.providerMs.WithLabelValues(m.service, operation).Observe(time.Since(started).Seconds())
}
