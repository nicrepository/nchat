package worker

import (
	"context"
	"errors"
	"time"

	"github.com/nicrepository/nchat/services/notification-service/internal/storage"
)

// Issue #742: the two boundaries the notification worker is written against.
//
// The worker decides *when* an event is processed — claiming, leasing, retrying,
// retiring. It decides nothing about *whether* an event should reach a person,
// and nothing about *how* it gets there. Those are the Evaluator and the
// Deliverer, and keeping them out of the loop is what lets the policy engine and
// the delivery channels arrive later without touching any of the concurrency
// this file's neighbours are about.
//
// Neither port is speculative. Without an Evaluator nothing ever leaves the
// pending state a producer writes, so the worker would consume an empty queue
// forever; without a Deliverer there is nothing to claim events *for*. They are
// the minimum, and there is deliberately no registry, no adapter factory and no
// per-channel configuration behind them.

// Notification is one outbox event as a delivery adapter sees it.
//
// References only — the same references the row carries. No message body, no
// e-mail address, no push subscription: an adapter resolves what it needs for
// its own channel, through the authorization that channel already applies. An
// adapter that receives this struct has gained nothing that a reader of the
// outbox had not already been granted.
type Notification struct {
	// ID is stable for the life of the event, across every attempt. Section
	// "Idempotency" of this package's design rests on it: see IdempotencyKey.
	ID          string
	WorkspaceID string
	RecipientID string
	EventType   string
	Priority    string
	SourceType  string
	SourceID    string
	// Origin is where the event came from — live, import, replay, resync — and
	// it is the one field here no delivery adapter needs. It is carried because
	// the policy reads it: an import backfilling a year of messages must not
	// alert anybody, and only the producer can say that a row is one.
	Origin string
	// DedupeKey is the logical identity of the event, in the form
	// libs/go/platform/notificationevent defines. Two rows with the same key in
	// one workspace for one recipient cannot exist: the unique index refuses it.
	DedupeKey string
	// Attempt is which try this is, starting at one. It is what a delivery
	// adapter needs to tell a first send from a repeat of one it may already
	// have accepted.
	//
	// It is also the identity of the claim the worker holds, and the worker
	// passes it back to the outbox with every finalisation: a write applies only
	// while the row still carries this value. An adapter needs none of that —
	// but it is why a slow delivery cannot have its outcome recorded over a
	// claim that has already moved on.
	Attempt    int
	OccurredAt time.Time
	// Muted is the recipient's own mute preference for this conversation, as
	// the outbox projection resolved it from chat.conversation_notification_prefs
	// (issue #744). Like Origin, no delivery adapter reads it: it is carried
	// because the policy does, and the policy is the only thing that may decide
	// what a mute means.
	Muted bool
}

// IdempotencyKey is what an adapter must present to a provider that supports
// one, and what an adapter without provider support must deduplicate on itself.
//
// It is the notification id, and that choice is the whole guarantee. The id is
// assigned once, by the statement that produced the event, and never changes:
// a retry of attempt four carries the same key as attempt one, so a provider
// that honours idempotency keys collapses them into a single delivery. The
// unique index on (workspace_id, recipient_user_id, dedupe_key) is the other
// half — it is why one logical event has one id rather than several.
//
// What this does not buy is exactly-once delivery, and the design does not
// claim it. See the package comment in notification_worker.go for the crash
// window that remains and why it is bounded rather than closed.
func (n Notification) IdempotencyKey() string { return n.ID }

// notificationFrom converts a claimed row into what the ports are given.
func notificationFrom(event storage.NotificationEvent) Notification {
	return Notification{
		ID:          event.ID,
		WorkspaceID: event.WorkspaceID,
		RecipientID: event.RecipientID,
		EventType:   event.EventType,
		Priority:    event.Priority,
		SourceType:  event.SourceType,
		SourceID:    event.SourceID,
		Origin:      event.Origin,
		DedupeKey:   event.DedupeKey,
		Attempt:     event.Attempts,
		OccurredAt:  event.OccurredAt,
		Muted:       event.Muted,
	}
}

// Verdict is what a policy decided about one pending event.
type Verdict struct {
	// Deliver is false when the event must not be sent. That is a terminal,
	// successful outcome — not a failure — and the outbox stores it in a state
	// of its own precisely so the two can never be confused.
	Deliver bool
	// SuppressedReason is operational shorthand recorded against the row so an
	// operator can answer "why did nobody get this?" months later.
	SuppressedReason string
	// PolicyVersion identifies the rule set that produced the verdict, so a
	// decision recorded today can still be explained after the rules change.
	// The reason goes to the outbox column; this goes to the log beside it,
	// because the table has no column for it and inventing one would be a
	// migration this issue does not need.
	PolicyVersion int
}

// defaultSuppressedReason stands in for a policy that suppressed an event
// without saying why.
//
// The database refuses a suppression with no reason, so without this the event
// would be left in pending, re-evaluated on every pass, and suppressed by a
// policy whose decision could never be written down — a silent infinite loop.
const defaultSuppressedReason = "policy_suppressed"

// Reason returns the reason to persist: empty when the event is to be
// delivered, and never empty when it is not.
func (v Verdict) Reason() string {
	if v.Deliver {
		return ""
	}
	if v.SuppressedReason == "" {
		return defaultSuppressedReason
	}
	return v.SuppressedReason
}

// Evaluator decides whether an event should be delivered at all.
//
// This is the seam the policy engine plugs into, and it is plugged in:
// NewPolicyEvaluator is what the worker is built with, here and in the app
// wiring. Nothing about quiet hours, mute preferences, read state or channel
// selection belongs in the worker loop, and none of it is here.
type Evaluator interface {
	Evaluate(ctx context.Context, notification Notification) (Verdict, error)
}

// EvaluatorFunc adapts a function to Evaluator.
type EvaluatorFunc func(ctx context.Context, notification Notification) (Verdict, error)

// Evaluate calls f.
func (f EvaluatorFunc) Evaluate(ctx context.Context, notification Notification) (Verdict, error) {
	return f(ctx, notification)
}

// Deliverer is one delivery channel.
//
// An implementation must treat Notification.IdempotencyKey as the identity of
// the delivery, not of the attempt: given the same key twice it must produce at
// most one logical notification for the recipient, either by passing the key to
// a provider that supports one or by recording the key itself before it calls
// out. That is the contract; the worker guarantees the key is stable and that
// no two workers hold the same claim, and it cannot guarantee anything past the
// call.
type Deliverer interface {
	Deliver(ctx context.Context, notification Notification) error
}

// ErrPermanentDelivery marks a failure that no retry can fix — a recipient who
// no longer exists, a subscription the provider has rejected outright, a payload
// the channel refuses.
//
// A Deliverer signals it by wrapping: fmt.Errorf("...: %w", ErrPermanentDelivery).
// Everything else is treated as transient, which is the fail-safe direction:
// retrying something unretryable costs a bounded number of attempts, while
// retiring something transient loses a notification for good.
var ErrPermanentDelivery = errors.New("permanent delivery failure")

// The closed set of failure categories persisted in
// chat.notification_outbox.last_error and used as a metric label.
//
// A category, never the provider's own message. The column is bounded at 64
// characters for the same reason this set is closed: a provider error body
// carries recipient addresses, subscription endpoints and token fragments, and
// this is the one column in a table designed to hold no content where such a
// string could otherwise be parked.
const (
	// CategoryTransient is a failure worth another attempt.
	CategoryTransient = "delivery_transient"
	// CategoryTimeout is a delivery that did not answer inside its deadline.
	// Distinct from transient because it is the one an operator correlates with
	// provider latency rather than with provider errors.
	CategoryTimeout = "delivery_timeout"
	// CategoryPermanent is a failure that will never succeed.
	CategoryPermanent = "delivery_permanent"
)

// classifyDelivery turns an adapter's error into the two facts the worker acts
// on: what to record, and whether to try again.
//
// The provider's own error text is never part of the result and is never
// returned to a caller that logs it.
func classifyDelivery(err error) (category string, permanent bool) {
	switch {
	case err == nil:
		return "", false
	case errors.Is(err, ErrPermanentDelivery):
		return CategoryPermanent, true
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return CategoryTimeout, false
	default:
		return CategoryTransient, false
	}
}

// RetryAfterError is a transient failure that also carries the provider's own
// opinion about when to try again (issue #746).
//
// It exists because a rate limiter is the one case where the adapter knows
// something the worker's backoff cannot work out: a 429 with a Retry-After is
// the provider stating a fact about its own capacity. Every other transient
// failure is scheduled by RetryPolicy alone, and this type changes nothing
// about how such a failure is classified — it wraps an ordinary error, so
// classifyDelivery still reads it as transient.
//
// The worker treats After as a floor, never as the schedule. See
// NotificationWorker.retryDelay for why both bounds are kept.
type RetryAfterError struct {
	// After is what the provider asked for, already normalised by the adapter:
	// zero when it asked for nothing, or asked for something unusable.
	After time.Duration
	// Err is the failure itself. Unwrapping reaches it, so errors.Is against
	// ErrPermanentDelivery or a context error behaves exactly as it would
	// without this wrapper.
	Err error
}

func (e *RetryAfterError) Error() string {
	return e.Err.Error()
}

func (e *RetryAfterError) Unwrap() error { return e.Err }

// requestedRetryDelay reports the delay an adapter asked for, if it asked.
//
// A free function rather than a method on the worker so the rule has one
// definition and can be exercised on its own: any error in the chain that
// carries a positive After wins, and everything else asks for nothing.
func requestedRetryDelay(err error) time.Duration {
	var retryAfter *RetryAfterError
	if errors.As(err, &retryAfter) && retryAfter.After > 0 {
		return retryAfter.After
	}
	return 0
}
