package storage

import (
	"context"
	"fmt"

	"github.com/nicrepository/nchat/services/notification-service/internal/domain"
)

// What the Web Push delivery layer reads and writes (issue #746).
//
// Two responsibilities, and they are one file because they are two halves of a
// single question: which of a recipient's browsers still needs this
// notification, and which of them has already had it. The answer to the first
// is defined by the answer to the second.
//
// The lifecycle of a subscription — success stamps, failure counts,
// invalidation — is emphatically not here. That is push_subscription_store.go's
// RecordDelivery, written by issue #745 for exactly this caller, and there is
// no second path to it.

// PushTarget is one endpoint a notification still has to reach.
//
// It carries the capability to push to somebody's browser: endpoint, p256dh and
// auth together are enough to deliver a message to a real person's device. That
// makes it the one type in this service that must never be logged, never be
// serialised into a span, and never be returned from an HTTP handler. Nothing
// does — it is read here and consumed by the sender, and neither end has a
// String method that could put it in a format verb by accident.
type PushTarget struct {
	// SubscriptionID and Generation are the compare-and-set pair every delivery
	// answer is applied against. Generation is the endpoint lifetime this
	// target describes, so an answer that arrives after the browser rotated its
	// subscription is discarded rather than written to the endpoint that
	// replaced the one it is about.
	SubscriptionID string
	Generation     int64

	Endpoint string
	P256dh   string
	Auth     string
}

// PushDeliveryStore is the delivery layer's whole view of persistence.
//
// An interface here rather than a concrete type because the fan-out logic is
// what needs testing at unit level and a pgx pool is not the way to test it.
// Three methods, each exactly one statement: nothing in this contract can be
// used to hold a transaction open across a call to a push service.
type PushDeliveryStore interface {
	// ListDeliverable returns the recipient's active subscriptions that this
	// notification has not already reached, oldest first.
	ListDeliverable(ctx context.Context, notificationID, workspaceID, recipientID string) ([]PushTarget, error)
	// MarkDelivered records that this notification reached this subscription.
	// Idempotent: recording it twice is a no-op.
	MarkDelivered(ctx context.Context, notificationID, subscriptionID string, generation int64) error
	// RecordDelivery applies one attempt's outcome to the subscription
	// lifecycle and says what that did. It is issue #745's method, restated
	// here so the delivery layer depends on a contract rather than on a struct.
	//
	// The answer is a classification rather than a boolean because a
	// compare-and-set that matched nothing is not one fact: a 410 whose endpoint
	// the browser has already replaced leaves a live endpoint still owed the
	// notification, and must not retire it (issue #746).
	RecordDelivery(ctx context.Context, subscriptionID string, generation int64, result domain.DeliveryResult) (domain.DeliveryApplication, error)
}

// listDeliverableQuery is the fan-out.
//
// # Authorisation
//
// workspace_id and recipient come from the outbox row, which chat-service
// derived in SQL from the message and the membership that produced it. Nothing
// in the request path chose them, so scoping the read by both is the whole
// authorisation: there is no addressable form of this query that reaches
// somebody else's browsers.
//
// # Why NOT EXISTS and not a LEFT JOIN
//
// The two plan the same way here, but this reads as what it is — a filter, not
// a widening — and it cannot accidentally return two rows for one subscription
// if the ledger ever gains a second row per pair. The subquery is anchored on
// notification_push_deliveries' primary key.
//
// # Why status is compared and not the failure count
//
// A subscription with a hundred transient failures is still deliverable: only
// the push service's own 404 or 410 retires one, and that is already recorded
// as status. Reading failure_count here would be a second, quieter retirement
// policy disagreeing with the first.
const listDeliverableQuery = `
	SELECT s.id::text, s.generation, s.endpoint, s.p256dh, s.auth
	FROM chat.push_subscriptions s
	WHERE s.workspace_id = $2::uuid
	  AND s.user_id = $3::uuid
	  AND s.status = 'active'
	  AND NOT EXISTS (
	      SELECT 1
	      FROM chat.notification_push_deliveries d
	      WHERE d.notification_id = $1::uuid
	        AND d.subscription_id = s.id
	  )
	ORDER BY s.created_at, s.id`

// markPushDeliveredQuery records one endpoint that has been reached.
//
// ON CONFLICT DO NOTHING is the deduplication and the concurrency control at
// once. A retry that reaches an endpoint already recorded writes nothing, so a
// replay cannot grow the table; two workers racing on one claim cannot produce
// two logical deliveries; and the first delivered_at is kept, because when the
// person was told is a fact and the second attempt did not change it.
const markPushDeliveredQuery = `
	INSERT INTO chat.notification_push_deliveries
		(notification_id, subscription_id, generation)
	VALUES ($1::uuid, $2::uuid, $3::bigint)
	ON CONFLICT (notification_id, subscription_id) DO NOTHING`

// PGXPushDeliveryStore serves the delivery layer over a pgx pool.
type PGXPushDeliveryStore struct {
	pool Pool
	// subscriptions owns the lifecycle statements. Composed rather than
	// reimplemented: the compare-and-set on (id, generation, status) that keeps
	// a late answer off a rotated endpoint is written once, in issue #745's
	// store, and this type must not gain a second copy of it.
	subscriptions *PGXPushSubscriptionStore
}

// NewPGXPushDeliveryStore creates a store backed by the given pool.
func NewPGXPushDeliveryStore(pool Pool) *PGXPushDeliveryStore {
	return &PGXPushDeliveryStore{pool: pool, subscriptions: NewPGXPushSubscriptionStore(pool)}
}

// ListDeliverable returns the endpoints this notification still owes.
func (s *PGXPushDeliveryStore) ListDeliverable(
	ctx context.Context, notificationID, workspaceID, recipientID string,
) ([]PushTarget, error) {
	rows, err := s.pool.Query(ctx, listDeliverableQuery, notificationID, workspaceID, recipientID)
	if err != nil {
		return nil, fmt.Errorf("list deliverable push subscriptions: %w", err)
	}
	defer rows.Close()

	targets := make([]PushTarget, 0)
	for rows.Next() {
		var target PushTarget
		if err := rows.Scan(&target.SubscriptionID, &target.Generation,
			&target.Endpoint, &target.P256dh, &target.Auth); err != nil {
			return nil, fmt.Errorf("list deliverable push subscriptions: %w", err)
		}
		targets = append(targets, target)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list deliverable push subscriptions: %w", err)
	}
	return targets, nil
}

// MarkDelivered records that one endpoint received this notification.
//
// It reports no "was it new?" boolean, because no caller could act on one: a
// row that was already there means the same endpoint was already told, which is
// precisely the outcome this call exists to produce.
func (s *PGXPushDeliveryStore) MarkDelivered(
	ctx context.Context, notificationID, subscriptionID string, generation int64,
) error {
	if _, err := s.pool.Exec(ctx, markPushDeliveredQuery,
		notificationID, subscriptionID, generation); err != nil {
		return fmt.Errorf("record push delivery ledger: %w", err)
	}
	return nil
}

// RecordDelivery applies one attempt's outcome to the subscription lifecycle.
func (s *PGXPushDeliveryStore) RecordDelivery(
	ctx context.Context, subscriptionID string, generation int64, result domain.DeliveryResult,
) (domain.DeliveryApplication, error) {
	return s.subscriptions.RecordDelivery(ctx, subscriptionID, generation, result)
}
