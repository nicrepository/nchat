package service

import (
	"context"

	"github.com/nicrepository/nchat/services/notification-service/internal/domain"
)

// PushSubscriptionStore is every persistent operation the use cases below
// perform. An interface so the service can be driven with a fake, while the
// concurrency and uniqueness semantics it depends on are proved against a real
// PostgreSQL in the storage package.
type PushSubscriptionStore interface {
	Upsert(ctx context.Context, principal domain.Principal, registration domain.Registration) (domain.PushSubscription, error)
	List(ctx context.Context, principal domain.Principal) ([]domain.PushSubscription, error)
	Disable(ctx context.Context, principal domain.Principal, subscriptionID string) error
	RecordDelivery(ctx context.Context, subscriptionID string, generation int64,
		result domain.DeliveryResult) (domain.DeliveryApplication, error)
}

// PushSubscriptions is the Web Push subscription lifecycle (issue #745).
type PushSubscriptions struct {
	store PushSubscriptionStore
}

// NewPushSubscriptions creates the service over a store.
func NewPushSubscriptions(store PushSubscriptionStore) *PushSubscriptions {
	return &PushSubscriptions{store: store}
}

// Register persists a browser's subscription for the authenticated principal.
//
// Validation happens before the store and never after: a registration that the
// domain refuses must not reach a statement that could otherwise raise a
// constraint violation the caller would have to interpret.
//
// The principal is the caller's, resolved from their session. Nothing the client
// sent contributes a user or a workspace, so a body that names either changes
// nothing about which rows this touches.
func (s *PushSubscriptions) Register(
	ctx context.Context, principal domain.Principal, registration domain.Registration,
) (domain.PushSubscription, error) {
	if err := registration.Validate(); err != nil {
		return domain.PushSubscription{}, err
	}
	return s.store.Upsert(ctx, principal, registration)
}

// List returns the principal's own subscriptions, for a client reconciling what
// the browser holds against what the server knows.
func (s *PushSubscriptions) List(
	ctx context.Context, principal domain.Principal,
) ([]domain.PushSubscription, error) {
	return s.store.List(ctx, principal)
}

// Disable turns off one of the principal's own subscriptions.
//
// It does not delete. The row is what a later diagnosis reads and what a
// controlled cleanup would act on, and removing it the moment somebody switches
// push off would throw both away for no gain.
func (s *PushSubscriptions) Disable(
	ctx context.Context, principal domain.Principal, subscriptionID string,
) error {
	return s.store.Disable(ctx, principal, subscriptionID)
}

// RecordDeliveryStatus applies the push service's HTTP status to the generation
// of one subscription, and says what that did.
//
// The generation is the one the attempt was made against, captured with the id
// when the send started. An answer that arrives after the browser re-registered
// describes an endpoint the row no longer has, so it applies to nothing — and
// the returned domain.DeliveryApplication is what tells those cases apart, so a
// caller holding a 410 can see that a live endpoint replaced the dead one
// rather than concluding the subscription is finished (issue #746).
//
// The classification is the domain's, so the rule that only 404 and 410 retire a
// subscription is stated once. Delivery itself, the retry schedule and the
// backoff all belong to the worker; none of them is here.
func (s *PushSubscriptions) RecordDeliveryStatus(
	ctx context.Context, subscriptionID string, generation int64, statusCode int,
) (domain.DeliveryApplication, error) {
	return s.store.RecordDelivery(ctx, subscriptionID, generation,
		domain.ClassifyDeliveryStatus(statusCode))
}
