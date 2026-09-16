package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/nicrepository/nchat/services/notification-service/internal/config"
	"github.com/nicrepository/nchat/services/notification-service/internal/domain"
	"github.com/nicrepository/nchat/services/notification-service/internal/storage"
)

// Web Push delivery, between the worker and the provider (issue #746).
//
// # Where this sits
//
//	outbox ─► worker ─► policy ─► claim ─► WebPushDeliverer ─► VAPIDSender ─► push service
//
// Everything to the left of this type has already decided that the recipient
// should be told. Nothing to the right may decide anything. What is left in the
// middle is four questions, and this file is their answers: is the notification
// still worth delivering, which of the recipient's browsers still needs it,
// what happened to each of them, and what that adds up to for the outbox row.
//
// # Why the provider cannot be reached around the policy
//
// Not by convention — by construction. The only caller of Deliver is
// NotificationWorker.deliverOne, and the only source of the events it delivers
// is NotificationOutboxStore.ClaimDue, whose statement selects rows in
// 'eligible', 'retrying' or 'processing'. A row reaches 'eligible' exactly once,
// through MarkEvaluated, from a policy verdict whose web_push channel was
// allowed; a suppressed row goes to 'suppressed', which is terminal and which
// the claim does not select. So an event the policy denied, or one it suppressed
// for quiet hours, is not merely skipped here — it never becomes claimable, and
// there is no path from an HTTP handler or a WebSocket frame to this type at all.
//
// # Partial success
//
// A notification with several browsers has several independent outcomes, and
// the outbox row has one state. The rule that reconciles them:
//
//	any endpoint still worth retrying  ─► retry the row
//	otherwise, at least one delivered  ─► the row is sent
//	otherwise                          ─► the row failed, permanently
//
// The first clause is what makes the ledger necessary. A row that retries
// because one endpoint answered 5xx must not resend to the endpoint that
// answered 2xx in the same pass, and it does not: the next fan-out excludes
// every endpoint already recorded as delivered. The endpoint owed a retry stays
// eligible on the worker's own backoff, and nothing else is disturbed.
//
// A row that ends 'failed' after some endpoints were delivered is therefore
// possible, and it is honest: the outbox row records what happened to the batch,
// the ledger records what happened to each browser, and neither is asked to
// answer the other's question.
//
// # Idempotency, and where it stops
//
// The logical identity of a delivery is (notification id, subscription id). The
// notification id is stable across every attempt (see Notification.IdempotencyKey)
// and the ledger's primary key is that pair, so: a retry does not create a
// second logical delivery, an endpoint already delivered is never selected
// again, an endpoint the push service retired is never selected again, and a
// replay writes nothing.
//
// Every one of those depends on the ledger row existing. It only stops a resend
// once it is durable, and that is where the guarantee ends: Web Push has no
// idempotency key, so there is nothing to hand a push service that would let it
// collapse two identical POSTs.
//
// When an accept cannot be written down — the write fails past retryWrite, or
// the process dies before it commits — nothing durable records it, so no later
// pass can tell "the provider accepted" from "the request never arrived". The
// row stays retryable and that endpoint may be sent to again, once per attempt
// the worker's budget has left. That is bounded by MaxAttempts and by nothing
// else; it is not one duplicate, and it is not exactly-once. retryWrite narrows
// the window, the outbox's attempt ceiling bounds what escapes it, and writing
// the ledger before the send would close it only by losing notifications
// silently instead. See docs/architecture/notification-web-push.md for the four
// cases and their tests.

// WebPushDeliverer turns one eligible notification into pushes to every browser
// that still needs it.
type WebPushDeliverer struct {
	store   storage.PushDeliveryStore
	sender  PushSender
	metrics *NotificationMetrics
	logger  *slog.Logger
	// ttl is how long a notification is worth delivering, counted from the
	// instant it occurred. One value, read from configuration, used for both
	// the expiry gate and the push service's own TTL header, so the two cannot
	// disagree.
	ttl time.Duration
}

// WebPushDeps is what the deliverer cannot build for itself.
type WebPushDeps struct {
	Store   storage.PushDeliveryStore
	Sender  PushSender
	Metrics *NotificationMetrics
	Logger  *slog.Logger
}

// NewWebPushDeliverer creates a deliverer on an already-bounded configuration.
func NewWebPushDeliverer(cfg config.WebPushConfig, deps WebPushDeps) *WebPushDeliverer {
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &WebPushDeliverer{
		store:   deps.Store,
		sender:  deps.Sender,
		metrics: deps.Metrics,
		logger:  logger,
		ttl:     time.Duration(cfg.Normalized().TTLSeconds) * time.Second,
	}
}

// The four terminal shapes a fan-out can take, as errors the worker already
// knows how to read.
//
// errPushExpired, errPushNoTarget and errPushRejected all wrap
// ErrPermanentDelivery, so classifyDelivery retires the row without spending
// the remaining attempts on something that cannot change. They are distinct
// values rather than one because they are three different operational stories
// and a log line has to be able to tell them apart.
var (
	// errPushExpired: the notification outlived its own TTL. No provider was
	// called.
	errPushExpired = fmt.Errorf("notification expired before delivery: %w", ErrPermanentDelivery)
	// errPushNoTarget: the recipient has no active browser this notification
	// has not already reached. Not a success — nobody was told by this channel
	// — and not retryable, because a subscription that appears later was not
	// subscribed when the event happened.
	errPushNoTarget = fmt.Errorf("recipient has no deliverable push subscription: %w", ErrPermanentDelivery)
	// errPushRejected: every endpoint refused the message for a reason that
	// will not change — retired subscriptions, or a request this build cannot
	// make correctly.
	errPushRejected = fmt.Errorf("every push endpoint refused the notification: %w", ErrPermanentDelivery)

	// errUnrecordedTransition: the push service answered terminally and the
	// transition that answer implies could not be written down.
	//
	// Emphatically not a provider failure, and it does not wrap
	// ErrPermanentDelivery: the state is recoverable, so classifyDelivery reads
	// it as transient and the worker schedules another pass. Everything already
	// committed survives — the ledger rows written before this point exclude
	// their endpoints from the next fan-out, so the retry resumes rather than
	// starting over.
	errUnrecordedTransition = errors.New("web push transition was not recorded")
)

// The `result` values this layer counts, beyond the per-attempt classes.
const (
	pushFanOutExpired   = "expired"
	pushFanOutNoTarget  = "no_target"
	pushFanOutPartial   = "partial"
	pushFanOutDelivered = "delivered"
	pushFanOutFailed    = "failed"
	// pushFanOutUnrecorded is a fan-out abandoned because a terminal transition
	// could not be persisted. It is counted apart from `failed` because the
	// cause is this deployment's database rather than anybody's push service,
	// and the two want different alerts.
	pushFanOutUnrecorded = "unrecorded"
)

// Deliver sends one notification to every browser that still needs it.
//
// The three gates before the fan-out are ordered by what they cost: expiry is
// arithmetic, the target list is one query, the payload is one encode. An
// expired notification therefore reaches neither the database nor a provider.
func (d *WebPushDeliverer) Deliver(ctx context.Context, notification Notification) error {
	ttl, live := d.remainingTTL(notification)
	if !live {
		d.metrics.CountPushFanOut(pushFanOutExpired, 1)
		d.logger.Info("notification expired before web push delivery",
			"notification_id", notification.ID, "attempt", notification.Attempt)
		return errPushExpired
	}

	targets, err := d.store.ListDeliverable(ctx,
		notification.ID, notification.WorkspaceID, notification.RecipientID)
	if err != nil {
		// Transient by omission, which is the fail-safe direction: a database
		// that cannot answer is a reason to try later, never a reason to
		// declare a recipient unreachable.
		return fmt.Errorf("web push fan-out: %w", err)
	}
	if len(targets) == 0 {
		d.metrics.CountPushFanOut(pushFanOutNoTarget, 1)
		// Logged, because this is the terminal outcome an operator asks about
		// most: in a deployment where few people have enabled push it is the
		// common ending, and "failed" on the outbox row would otherwise carry
		// no explanation of what failed.
		d.logger.Info("notification has no deliverable push subscription",
			"notification_id", notification.ID, "attempt", notification.Attempt)
		return errPushNoTarget
	}

	payload, err := buildPushPayload(notification)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrPermanentDelivery, err)
	}
	return d.fanOut(ctx, notification, targets, payload, ttl)
}

// remainingTTL reports how much of the notification's life is left, and whether
// any of it is.
//
// Measured from occurred_at, not from the attempt: a notification that has been
// retried for an hour is an hour older, and resetting the clock on every retry
// would let a failing endpoint keep a stale notification alive indefinitely.
// The boundary is exclusive — exactly at the TTL there is nothing left to
// deliver — so a retry that crossed the line while waiting is refused by the
// same arithmetic that refuses a first attempt on an old event.
func (d *WebPushDeliverer) remainingTTL(notification Notification) (time.Duration, bool) {
	remaining := d.ttl - time.Since(notification.OccurredAt)
	if remaining <= 0 {
		return 0, false
	}
	return remaining, true
}

// fanOut attempts every target and reduces the outcomes to one.
//
// Sequential, on purpose. A person has a handful of browsers, the attempts are
// bounded by the worker's own delivery timeout, and the worker already runs
// several notifications concurrently — so a second layer of goroutines here
// would buy nothing measurable and cost cancellation handling, ordering and a
// place for a leak to hide.
//
// A cancelled context stops the loop rather than racing every remaining target
// against a deadline that has already passed. What was not attempted is not
// recorded, so the next claim picks it up: the ledger only ever grows with
// endpoints that genuinely received something.
//
// A transition that could not be made durable stops it too, and returns rather
// than tallying. Continuing would mean making more external calls while the
// internal state is already known to disagree with what the provider has
// accepted — and stopping costs nothing, because every endpoint already
// recorded is committed in the ledger and is excluded from the next fan-out.
func (d *WebPushDeliverer) fanOut(
	ctx context.Context, notification Notification,
	targets []storage.PushTarget, payload []byte, ttl time.Duration,
) error {
	var tally fanOutTally
	for _, target := range targets {
		if ctx.Err() != nil {
			tally.observe(attemptOutcome{result: PushResult{Class: PushTransientFailure}})
			break
		}
		outcome, err := d.deliverTo(ctx, notification, target, payload, ttl)
		if err != nil {
			d.metrics.CountPushFanOut(pushFanOutUnrecorded, 1)
			return err
		}
		tally.observe(outcome)
	}
	d.metrics.CountPushFanOut(tally.fanOutResult(), 1)
	return tally.outcome()
}

// deliverTo sends to one endpoint and records everything that follows from it.
//
// The order is deliberate: send, then persist, then observe. Persisting before
// the send would record a delivery that has not happened; observing before
// persisting would report a success the database refused.
//
// The two return values are two different kinds of fact and never substitute
// for one another. PushResult is what the push service answered. The error is
// a storage failure and nothing else — it is never a provider error dressed up
// as one, and a database that is refusing is never reported as
// PushProviderUnavailable, which would be simply untrue. A non-nil error means
// the provider answered terminally and this process could not write that down;
// the result is still returned so the attempt stays observable.
func (d *WebPushDeliverer) deliverTo(
	ctx context.Context, notification Notification,
	target storage.PushTarget, payload []byte, ttl time.Duration,
) (attemptOutcome, error) {
	result := d.sender.Send(ctx, PushMessage{
		Endpoint: target.Endpoint,
		P256dh:   target.P256dh,
		Auth:     target.Auth,
		Payload:  payload,
		TTL:      ttl,
	})
	state, err := d.persist(ctx, notification, target, result)
	// Observed either way: the attempt happened and its latency is real, so a
	// failed write must not also erase the record that a push went out. The
	// provider's own answer is what is measured and logged — a retirement that
	// was superseded is still a 410, and reporting it as anything else would
	// misstate what the push service said.
	d.metrics.ObservePushAttempt(result.Class, result.Latency)
	d.logAttempt(notification, target, result)
	return attemptOutcome{result: result, state: state}, err
}

// attemptOutcome is one endpoint's turn: what the push service answered, and
// what recording that answer established about the target.
//
// Two facts rather than one, because they can disagree. A 410 says this
// endpoint is finished; the persistence says whether the subscription behind it
// is finished too, or has already rotated to a live endpoint that has had
// nothing.
type attemptOutcome struct {
	result PushResult
	state  persistence
}

// persist writes down what this attempt changed, and reports only the failure
// that would otherwise be invisible: a transition the worker would go on to
// treat as durable when it is not.
//
// Two of the three branches carry a transition that has to survive this
// process, and they are the only two:
//
//   - a delivery, because the ledger row is what stops the endpoint being sent
//     to again for this notification;
//   - a retirement, because the status change is what stops the endpoint being
//     sent to again ever.
//
// Everything else is operational history — a success stamp, a failure count —
// and losing it costs a stale number on a diagnostic column. Reporting that as
// a failure would turn a retryable attempt into an error and buy nothing, so
// those writes stay best effort and are logged rather than returned.
func (d *WebPushDeliverer) persist(
	ctx context.Context, notification Notification,
	target storage.PushTarget, result PushResult,
) (persistence, error) {
	switch result.Class {
	case PushDelivered:
		return persistenceRecorded, d.persistDelivered(ctx, notification, target, result)
	case PushSubscriptionGone:
		return d.persistRetirement(ctx, notification, target, result)
	default:
		d.recordHealth(ctx, notification, target, result)
		return persistenceRecorded, nil
	}
}

// persistence is what recording an attempt established about the target, and it
// has exactly two values because the fan-out only needs to know one thing: is
// this endpoint finished with, or is there still a live one behind the same
// subscription?
type persistence int

const (
	// persistenceRecorded: the transition landed on the endpoint the attempt
	// was about, and that endpoint's story is over for this notification.
	persistenceRecorded persistence = iota
	// persistenceSuperseded: the subscription rotated, so the answer belongs to
	// an endpoint that no longer exists and the one that replaced it has had
	// nothing. The notification is not finished.
	persistenceSuperseded
)

// retirementSuperseded is the closed reason recorded when a retirement did not
// apply because the subscription had already rotated.
const retirementSuperseded = "subscription_generation_changed"

// persistDelivered records that this notification reached this endpoint.
//
// The ledger row first and durably, because it is the whole of the
// deduplication: without it the next retry of this notification selects this
// endpoint again and a person is told twice. The success stamp follows and is
// best effort — last_success_at is for an operator, not for a decision.
func (d *WebPushDeliverer) persistDelivered(
	ctx context.Context, notification Notification,
	target storage.PushTarget, result PushResult,
) error {
	err := d.durably(ctx, notification, target, "ledger_write_failed",
		func(ctx context.Context) error {
			return d.store.MarkDelivered(ctx,
				notification.ID, target.SubscriptionID, target.Generation)
		})
	if err != nil {
		return err
	}
	d.recordHealth(ctx, notification, target, result)
	return nil
}

// persistRetirement records that the push service has finished with this
// endpoint.
//
// One write, and it is the durable one: for a 404 or a 410 the lifecycle
// transition *is* the terminal fact. There is deliberately no second write to
// make atomic with it — a retired subscription needs no success stamp and no
// failure count, so nothing here needs a transaction, and the rule that no
// transaction spans a call to a push service holds without an exception.
func (d *WebPushDeliverer) persistRetirement(
	ctx context.Context, notification Notification,
	target storage.PushTarget, result PushResult,
) (persistence, error) {
	var application domain.DeliveryApplication
	err := d.durably(ctx, notification, target, "subscription_write_failed",
		func(ctx context.Context) error {
			var err error
			application, err = d.store.RecordDelivery(ctx,
				target.SubscriptionID, target.Generation,
				domain.ClassifyDeliveryStatus(result.StatusCode))
			return err
		})
	if err != nil {
		return persistenceRecorded, err
	}
	if application.Deliverable() {
		// The 404 or 410 was about an endpoint the browser has already
		// replaced, and the replacement is live and has had nothing. Retiring
		// nothing is the correct write; treating it as a dead subscription
		// would end the notification on the word of an endpoint that no longer
		// exists.
		d.logger.Info("push subscription rotated before its retirement could apply",
			"notification_id", notification.ID,
			"subscription_id", target.SubscriptionID,
			"reason", retirementSuperseded)
		return persistenceSuperseded, nil
	}
	return persistenceRecorded, nil
}

// durably makes one write land, or says plainly that it did not.
//
// What is retried is the write, never the push. That distinction is the whole
// point: the provider has already accepted or already refused, so repeating the
// request would tell somebody twice to fix a database blip. Retrying the
// statement costs one round trip and no external effect at all.
func (d *WebPushDeliverer) durably(
	ctx context.Context, notification Notification,
	target storage.PushTarget, errorType string, write func(context.Context) error,
) error {
	err := retryWrite(ctx, write)
	if err == nil {
		return nil
	}
	d.logger.Error("web push transition could not be recorded",
		"notification_id", notification.ID,
		"subscription_id", target.SubscriptionID,
		"error_type", errorType)
	// The driver's message is deliberately not wrapped into what the worker
	// logs: it carries the statement. The category is what an operator alerts
	// on, and errUnrecordedTransition is what the worker classifies.
	return fmt.Errorf("%s: %w", errorType, errUnrecordedTransition)
}

// recordHealth stamps the subscription's operational history.
//
// Best effort by construction. It is also where a compare-and-set that matched
// nothing is reported, which is not a failure: the subscription rotated, was
// disabled, or was already retired while the attempt was in flight, and the
// answer correctly did not land on the endpoint that replaced the one it
// describes.
func (d *WebPushDeliverer) recordHealth(
	ctx context.Context, notification Notification,
	target storage.PushTarget, result PushResult,
) {
	application, err := d.store.RecordDelivery(ctx, target.SubscriptionID, target.Generation,
		domain.ClassifyDeliveryStatus(result.StatusCode))
	if err != nil {
		d.logger.Error("push subscription history could not be recorded",
			"notification_id", notification.ID,
			"subscription_id", target.SubscriptionID,
			"error_type", "subscription_history_write_failed")
		return
	}
	if application != domain.ApplicationRecorded {
		d.logger.Info("push subscription changed while the attempt was in flight",
			"notification_id", notification.ID,
			"subscription_id", target.SubscriptionID)
	}
}

// The bound on recovering a write. Three attempts a fiftieth of a second apart:
// enough to ride out a failover or a saturated pool, and small enough that a
// whole fan-out of them stays far inside the worker's delivery budget. It is
// deliberately not a general-purpose retry facility — there is one caller, the
// numbers are constants, and there is nothing to configure.
const (
	terminalWriteAttempts = 3
	terminalWriteBackoff  = 50 * time.Millisecond
)

// retryWrite runs write until it succeeds, the attempts run out, or the context
// ends. It never sleeps past a cancellation and never loops without a bound.
func retryWrite(ctx context.Context, write func(context.Context) error) error {
	err := write(ctx)
	for attempt := 1; err != nil && attempt < terminalWriteAttempts; attempt++ {
		if !pause(ctx, terminalWriteBackoff) {
			return err
		}
		err = write(ctx)
	}
	return err
}

// pause waits, and reports false if the context ended first.
func pause(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// logAttempt records one attempt in identifiers and categories only.
//
// The subscription id is here because it is what an operator correlates a
// failing browser by, and it is an internal identifier that grants nothing. The
// endpoint, p256dh, auth, the Authorization header and the payload are not
// here, and cannot be: PushResult carries none of them, so there is nothing in
// scope to log even by mistake.
func (d *WebPushDeliverer) logAttempt(
	notification Notification, target storage.PushTarget, result PushResult,
) {
	d.logger.Info("web push attempt",
		"notification_id", notification.ID,
		"subscription_id", target.SubscriptionID,
		"attempt", notification.Attempt,
		"result", string(result.Class),
		"status_code", result.StatusCode,
		"latency_ms", result.Latency.Milliseconds(),
		"invalidation_reason", string(invalidationReason(result)))
}

// invalidationReason names why an endpoint was retired, or nothing when it was
// not. It reads the same authority the lifecycle write uses, so the log and the
// table cannot disagree about which attempt retired a subscription.
func invalidationReason(result PushResult) domain.InvalidationReason {
	return domain.ClassifyDeliveryStatus(result.StatusCode).Reason
}

// fanOutTally accumulates what a fan-out amounted to.
//
// Three counts and one duration, which is everything the reduction needs. It
// holds no target, no endpoint and no result detail: what an aggregate is
// allowed to remember is how many of each kind there were.
type fanOutTally struct {
	delivered  int
	retryable  int
	permanent  int
	retryAfter time.Duration
}

// observe folds one attempt into the tally.
//
// Supersession is read first and outranks the provider's own class. A 410 is
// permanent for the endpoint that answered it, and that endpoint is already
// gone — but the subscription now holds a live one that has had nothing, so the
// notification is still owed and the event has to come back. Letting the 410
// fall through to permanent here is precisely how a rotated browser lost a
// notification.
func (t *fanOutTally) observe(outcome attemptOutcome) {
	result := outcome.result
	switch {
	case outcome.state == persistenceSuperseded:
		t.retryable++
	case result.Class == PushDelivered:
		t.delivered++
	case result.Class.Retryable():
		t.retryable++
		t.retryAfter = max(t.retryAfter, result.RetryAfter)
	default:
		t.permanent++
	}
}

// outcome reduces the fan-out to the one answer the outbox row can hold.
//
// Retryable first, and that ordering is the design: an endpoint still owed a
// delivery outranks endpoints already served, because the row is what schedules
// the next attempt and the ledger is what stops the served ones being served
// twice.
func (t fanOutTally) outcome() error {
	switch {
	case t.retryable > 0:
		return &RetryAfterError{After: t.retryAfter, Err: errPushTransient}
	case t.delivered > 0:
		return nil
	default:
		return errPushRejected
	}
}

// fanOutResult names the shape of the fan-out for the metric.
func (t fanOutTally) fanOutResult() string {
	switch {
	case t.retryable > 0 && t.delivered > 0:
		return pushFanOutPartial
	case t.retryable > 0:
		return pushFanOutFailed
	case t.delivered > 0:
		return pushFanOutDelivered
	default:
		return pushFanOutFailed
	}
}

// errPushTransient is the plain transient failure the worker retries. It wraps
// nothing: ErrPermanentDelivery is the only marker classifyDelivery looks for,
// and its absence is what "try again" means.
var errPushTransient = errors.New("web push delivery failed on at least one endpoint")
