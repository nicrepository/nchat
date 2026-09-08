// Package worker delivers what the outboxes hold.
//
// # The notification worker (issue #742)
//
// chat-service commits a notification event in the same statement as the message
// that caused it (issue #741), so the durable question "who should have been
// told about this?" is already answered before this service sees anything. What
// remains is to answer it *out loud*, from several replicas at once, while
// surviving restarts and a provider that is having a bad day.
//
// # Concurrency, in one paragraph
//
// A claim is an UPDATE. The statement selects due rows FOR UPDATE SKIP LOCKED
// and moves them to 'processing' in the same breath, so two workers can never
// hold one event: the row is either yours or it was skipped. The lease is the
// row's own next_attempt_at, set into the future by that same UPDATE, which is
// why a worker that dies costs one lease rather than one lost notification —
// its rows become due again and the next claim takes them. Nothing external is
// called while any of this runs, and no transaction of this package's spans a
// delivery.
//
// # Idempotency, and the limit of it
//
// Notification.IdempotencyKey is the row's id: assigned once by the producer,
// unchanged across every attempt, and unique per logical event because the
// outbox's unique index says so. A provider that honours idempotency keys will
// therefore collapse retries into one delivery, and an adapter whose provider
// does not must record the key itself before calling out.
//
// That is at-least-once with a strong deduplication handle — not exactly-once,
// and this package does not claim it. One window stays open and is stated here
// rather than hidden: between a provider accepting a delivery and this worker
// writing 'sent', the process can die. The row's lease then expires, the event
// is claimed again, and the adapter is asked to deliver something the provider
// has already taken. Nothing in PostgreSQL can close that window, because the
// two systems cannot commit together. What the design does instead is make it
// small and make it survivable: the write that closes it is a single indexed
// UPDATE issued immediately after the call returns, on a context deliberately
// reserved for it, and the key handed to the adapter is stable so the second
// attempt is *recognisable* as a repeat by anything downstream that keeps
// records. An adapter that cannot recognise it will deliver twice, and that
// fact belongs in the adapter's own documentation, not hidden behind a promise
// this layer cannot keep.
//
// # A panicking adapter
//
// Nothing here recovers from a panic in a delivery adapter, on purpose. It
// takes the process down, which is loud and correct — and it is bounded,
// because attempts are counted when a row is *claimed* rather than when it
// fails. An event that reliably kills the worker burns one attempt per restart,
// reaches the ceiling, and is retired by FailExhausted instead of crash-looping
// the deployment forever.
package worker

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/notificationevent"
	"github.com/nicrepository/nchat/services/notification-service/internal/config"
	"github.com/nicrepository/nchat/services/notification-service/internal/storage"
)

// NotificationWorker drains chat.notification_outbox.
type NotificationWorker struct {
	cfg       config.NotificationWorkerConfig
	store     storage.NotificationOutboxStore
	evaluator Evaluator
	deliverer Deliverer
	retry     RetryPolicy
	metrics   *NotificationMetrics
	logger    *slog.Logger
	// id names this replica in logs, and only in logs. It is never a metric
	// label: pods come and go, so a series keyed by one grows without bound.
	id string
}

// NotificationWorkerDeps is what the worker cannot build for itself.
//
// A struct rather than six positional parameters, so a call site cannot silently
// swap two interfaces that happen to have compatible shapes.
type NotificationWorkerDeps struct {
	Store     storage.NotificationOutboxStore
	Evaluator Evaluator
	Deliverer Deliverer
	Metrics   *NotificationMetrics
	Logger    *slog.Logger
}

// NewNotificationWorker creates a worker on an already-bounded configuration.
func NewNotificationWorker(cfg config.NotificationWorkerConfig, deps NotificationWorkerDeps) *NotificationWorker {
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	evaluator := deps.Evaluator
	if evaluator == nil {
		evaluator = DeliverEverything()
	}
	normalized := cfg.Normalized()
	return &NotificationWorker{
		cfg:       normalized,
		store:     deps.Store,
		evaluator: evaluator,
		deliverer: deps.Deliverer,
		retry: RetryPolicy{
			Base: time.Duration(normalized.RetryBaseSeconds) * time.Second,
			Max:  time.Duration(normalized.RetryMaxSeconds) * time.Second,
		},
		metrics: deps.Metrics,
		logger:  logger,
		id:      workerInstanceID(),
	}
}

// Start runs the worker until ctx is cancelled.
//
// Cancelling ctx stops it claiming anything new; it does not abort a pass that
// has already begun. That distinction is the point, and it is the same one the
// SMTP worker makes: an event is claimed, delivered, and only then finalised, so
// a cancellation landing between the delivery and the write would leave the row
// unfinalised, its lease expiring, and the notification delivered a second time.
// A pass therefore runs on its own bounded context, and passTimeout is what
// keeps the shutdown wait finite.
func (w *NotificationWorker) Start(ctx context.Context) {
	if !w.cfg.LeaseCoversProcessing() {
		// Refusing to start is the safe direction. A lease shorter than a pass
		// hands rows to a second worker mid-delivery, which is the duplication
		// the whole claim protocol exists to prevent — and readiness reports the
		// same condition, so the pod does not sit green with nothing behind it.
		w.logger.Error("notification worker lease is shorter than the work it protects",
			"lease_seconds", w.cfg.LeaseSeconds,
			"pass_seconds", w.cfg.ProtectedProcessingSeconds(),
			"worker_id", w.id)
		return
	}

	ticker := time.NewTicker(time.Duration(w.cfg.PollSeconds) * time.Second)
	defer ticker.Stop()

	w.logger.Info("notification worker started", "worker_id", w.id,
		"batch_size", w.cfg.BatchSize, "max_concurrency", w.cfg.MaxConcurrency)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// A pass outlasts the poll interval by design, so by the time one
			// returns the ticker usually has a tick waiting. Both cases are then
			// ready and select chooses at random — which, without this guard,
			// meant a worker that had already been told to stop could take one
			// more batch of claims into a process that is terminating.
			if ctx.Err() != nil {
				return
			}
			w.runPass()
		}
	}
}

// runPass is one bounded unit of work: at most one batch evaluated and at most
// one batch delivered.
//
// One pass per tick, deliberately. Draining the whole backlog in a loop would
// turn a provider outage into a retry storm — every queued event attempted back
// to back against something that is already failing. Throughput is instead
// governed by BatchSize and PollSeconds, which are the two numbers an operator
// can reason about.
//
// It runs on its own context rather than the caller's, so work in flight is
// finalised rather than abandoned when shutdown begins.
func (w *NotificationWorker) runPass() {
	ctx, cancel := context.WithTimeout(context.Background(), w.passTimeout())
	defer cancel()

	w.observeBacklog(ctx)
	w.retireExhausted(ctx)
	w.evaluatePending(ctx)
	w.deliverDue(ctx)
}

// observeBacklog publishes the queue depth. A failure here is reported and
// otherwise ignored: not knowing the backlog is no reason to stop draining it.
func (w *NotificationWorker) observeBacklog(ctx context.Context) {
	depth, err := w.store.Backlog(ctx)
	if err != nil {
		w.logStoreFailure("backlog_query_failed")
		return
	}
	w.metrics.ObserveBacklog(depth)
}

// retireExhausted closes out claims nobody will ever finalise.
//
// This is the bound on a poison event: an abandoned claim whose attempts are
// spent stops being claimable and becomes terminal, instead of being reclaimed
// forever by every replica in turn.
func (w *NotificationWorker) retireExhausted(ctx context.Context) {
	retired, err := w.store.FailExhausted(ctx, w.cfg.MaxAttempts)
	if err != nil {
		w.logStoreFailure("retire_exhausted_failed")
		return
	}
	if retired > 0 {
		w.metrics.Count(resultExhausted, retired)
		w.logger.Warn("notification claims retired after exhausting their attempts",
			"count", retired, "max_attempts", w.cfg.MaxAttempts, "worker_id", w.id)
	}
}

// evaluatePending asks the policy about events no policy has seen.
//
// Unlocked, unlike the delivery claim. Evaluation reaches nothing outside this
// process, so two replicas deciding the same row costs one redundant policy call
// and one compare-and-set that matches nothing — cheaper than holding a
// transaction open across the decision.
func (w *NotificationWorker) evaluatePending(ctx context.Context) {
	events, err := w.store.ListPending(ctx, w.cfg.BatchSize)
	if err != nil {
		w.logStoreFailure("list_pending_failed")
		return
	}
	for _, event := range events {
		w.evaluateOne(ctx, event)
	}
}

// evaluateOne records one policy decision.
func (w *NotificationWorker) evaluateOne(ctx context.Context, event storage.NotificationEvent) {
	verdict, err := w.evaluator.Evaluate(ctx, notificationFrom(event))
	if err != nil {
		w.metrics.Count(resultError, 1)
		w.logger.Error("notification policy evaluation failed",
			"notification_id", event.ID, "error_type", "evaluate_failed", "worker_id", w.id)
		return
	}

	state, result := notificationevent.StateEligible, resultEligible
	if !verdict.Deliver {
		state, result = notificationevent.StateSuppressed, resultSuppressed
	}
	if err := w.store.MarkEvaluated(ctx, event.ID, state, verdict.Reason()); err != nil {
		w.recordTransitionFailure("evaluate", event.ID, err)
		return
	}
	w.metrics.Count(result, 1)
}

// deliverDue claims a batch and delivers it.
func (w *NotificationWorker) deliverDue(ctx context.Context) {
	events, err := w.store.ClaimDue(ctx, w.cfg.BatchSize, w.cfg.MaxAttempts, w.lease())
	if err != nil {
		w.logStoreFailure("claim_due_failed")
		return
	}
	if len(events) == 0 {
		return
	}
	w.metrics.Count(resultClaimed, len(events))
	w.deliverBatch(ctx, events)
}

// deliverBatch runs the claimed events with at most MaxConcurrency in flight.
//
// A buffered channel is the whole limiter. A backlog of ten thousand cannot
// become ten thousand goroutines, because a batch is bounded before it gets
// here and this bounds it again; and the WaitGroup is what makes the lease
// arithmetic honest, since the pass does not return while a delivery it started
// is still running.
func (w *NotificationWorker) deliverBatch(ctx context.Context, events []storage.NotificationEvent) {
	slots := make(chan struct{}, w.cfg.MaxConcurrency)
	var inFlight sync.WaitGroup

	for _, event := range events {
		slots <- struct{}{}
		inFlight.Add(1)
		go func() {
			defer inFlight.Done()
			defer func() { <-slots }()
			w.deliverOne(ctx, event)
		}()
	}
	inFlight.Wait()
}

// deliveryOutcome is what happened to one attempt, in the vocabulary the metric
// and the log both use.
type deliveryOutcome struct {
	result   string
	category string
}

// deliverOne delivers a single claimed event and records what happened.
func (w *NotificationWorker) deliverOne(ctx context.Context, event storage.NotificationEvent) {
	notification := notificationFrom(event)

	started := time.Now()
	deliveryErr := w.deliver(ctx, notification)
	elapsed := time.Since(started)

	outcome, err := w.recordOutcome(ctx, notification, deliveryErr)
	if err != nil {
		// The outcome was not written, so it is not counted as one. Recording
		// "delivered" here would publish a delivery the table never accepted —
		// which is exactly what happens when this worker's lease expired and
		// another claim superseded it. recordTransitionFailure counts that.
		w.recordTransitionFailure("deliver", notification.ID, err)
		return
	}
	w.metrics.ObserveDelivery(outcome.result, elapsed)
	w.logOutcome(notification, outcome)
}

// deliver calls the adapter under its own deadline.
//
// Its own, and not the pass's: a provider that never answers would otherwise
// consume the whole pass budget and leave nothing to finalise with, so the row
// would sit unfinalised until its lease expired and be delivered again.
func (w *NotificationWorker) deliver(ctx context.Context, notification Notification) error {
	deliverCtx, cancel := context.WithTimeout(ctx, w.deliveryTimeout())
	defer cancel()
	return w.deliverer.Deliver(deliverCtx, notification)
}

// recordOutcome writes the attempt's result to the outbox and names it.
//
// The three endings are the three the state machine allows out of 'processing',
// and the choice between the last two is the whole retry policy: a failure the
// adapter called permanent, or an attempt count that has reached its ceiling,
// is terminal; anything else is due again at a time computed once and persisted,
// so a restart resumes the schedule instead of retrying everything at once.
//
// Every one of the three carries notification.Attempt, which is the identity of
// the claim this worker holds. A worker whose lease expired mid-delivery finds
// no row matching it and is told so; it does not get to record an outcome
// against the claim that superseded it.
func (w *NotificationWorker) recordOutcome(
	ctx context.Context, notification Notification, deliveryErr error,
) (deliveryOutcome, error) {
	if deliveryErr == nil {
		return deliveryOutcome{result: resultDelivered},
			w.store.MarkDelivered(ctx, notification.ID, notification.Attempt)
	}

	category, permanent := classifyDelivery(deliveryErr)
	if permanent || notification.Attempt >= w.cfg.MaxAttempts {
		return deliveryOutcome{result: resultFailed, category: category},
			w.store.MarkFailed(ctx, notification.ID, notification.Attempt, category)
	}
	return deliveryOutcome{result: resultRetry, category: category},
		w.store.ScheduleRetry(ctx, notification.ID, notification.Attempt,
			w.retry.Delay(notification.Attempt), category)
}

// logOutcome states what happened, in identifiers and categories only.
//
// No message body, no recipient, no provider response: the outbox holds none of
// those and this line must not be the place they appear. The notification id is
// here because an operator investigating a single event has nothing else to go
// on, and it grants no access by itself.
func (w *NotificationWorker) logOutcome(notification Notification, outcome deliveryOutcome) {
	w.logger.Info("notification "+outcome.result,
		"notification_id", notification.ID,
		"event_type", notification.EventType,
		"attempt", notification.Attempt,
		"result", outcome.result,
		"error_category", outcome.category,
		"worker_id", w.id)
}

// recordTransitionFailure separates a lost race from a real fault.
//
// A compare-and-set that matched nothing means this worker no longer owns the
// claim: its lease expired and another replica reclaimed the row, incrementing
// the attempts this worker was holding, or the row was already finalised. That
// is a normal consequence of running more than one worker, so it is counted and
// logged rather than raised — but it is emphatically not a success, and nothing
// downstream treats it as one. The event stays where its new owner left it, and
// this worker does not retry: retrying would mean calling the provider again for
// a claim somebody else holds.
func (w *NotificationWorker) recordTransitionFailure(stage, notificationID string, err error) {
	if errors.Is(err, storage.ErrNotificationStateConflict) {
		w.metrics.Count(resultLeaseLost, 1)
		w.logger.Info("notification claim was lost to another worker",
			"stage", stage, "notification_id", notificationID, "worker_id", w.id)
		return
	}
	w.metrics.Count(resultError, 1)
	w.logger.Error("notification state could not be recorded",
		"stage", stage, "notification_id", notificationID,
		"error_type", "transition_failed", "worker_id", w.id)
}

// logStoreFailure reports a database failure by category only.
//
// The driver's error text is deliberately not logged: it carries the statement,
// and the same reticence the rest of this service applies to provider errors
// applies here. The metric and the category are what an operator alerts on.
func (w *NotificationWorker) logStoreFailure(errorType string) {
	w.metrics.Count(resultError, 1)
	w.logger.Error("notification worker database call failed",
		"error_type", errorType, "worker_id", w.id)
}

func (w *NotificationWorker) lease() time.Duration {
	return time.Duration(w.cfg.LeaseSeconds) * time.Second
}

func (w *NotificationWorker) deliveryTimeout() time.Duration {
	return time.Duration(w.cfg.DeliveryTimeoutSeconds) * time.Second
}

// passTimeout bounds one pass. It is the configuration's ProcessingBudget and
// nothing else: App waits exactly this long for a pass to drain during shutdown,
// and the lease is validated against it, so all three must read the same
// function.
func (w *NotificationWorker) passTimeout() time.Duration {
	return w.cfg.ProcessingBudget()
}

// workerInstanceID names this replica for its log lines.
//
// The hostname, which in Kubernetes is the pod name and is exactly what an
// operator would grep for next. It appears in no metric label and no persisted
// row.
func workerInstanceID() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		return "unknown"
	}
	return host
}
