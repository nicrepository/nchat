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
		DedupeKey:   event.DedupeKey,
		Attempt:     event.Attempts,
		OccurredAt:  event.OccurredAt,
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
// This is the seam the policy engine plugs into. Nothing about quiet hours,
// mute preferences, read state or channel selection belongs in the worker loop,
// and none of it is here.
type Evaluator interface {
	Evaluate(ctx context.Context, notification Notification) (Verdict, error)
}

// EvaluatorFunc adapts a function to Evaluator.
type EvaluatorFunc func(ctx context.Context, notification Notification) (Verdict, error)

// Evaluate calls f.
func (f EvaluatorFunc) Evaluate(ctx context.Context, notification Notification) (Verdict, error) {
	return f(ctx, notification)
}

// DeliverEverything is the policy in force until a policy engine exists: every
// event a producer wrote is eligible.
//
// It suppresses nothing, and that is the honest default. A worker with no
// policy must not invent one — inventing "probably do not send this" here would
// be a product decision made in a retry loop.
func DeliverEverything() Evaluator {
	return EvaluatorFunc(func(context.Context, Notification) (Verdict, error) {
		return Verdict{Deliver: true}, nil
	})
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
