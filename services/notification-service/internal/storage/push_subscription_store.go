package storage

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/nicrepository/nchat/services/notification-service/internal/domain"
)

// endpointUniqueConstraint is the index that makes an endpoint belong to exactly
// one subscription. Named here because a violation of *this* index is a
// meaningful answer to the caller, while any other unique violation is a defect.
const endpointUniqueConstraint = "push_subscriptions_endpoint_unique"

// pgUniqueViolation is SQLSTATE 23505.
const pgUniqueViolation = "23505"

// pushSubscriptionColumns is the projection every read shares.
//
// It is the reconcile projection plus the one token a delivery attempt needs.
// endpoint, p256dh and auth are the capability to push to a browser;
// failure_count, last_success_at, invalidated_at and invalidation_reason are
// operational history. None of them is read here, which is a stronger guarantee
// that they cannot leak than any amount of stripping further up would be. The
// tests that have to see them read them straight from the table.
//
// generation is read because an attempt has to capture it: without it the answer
// to an attempt cannot be attributed to the endpoint that attempt was made
// against. It stops at the storage and service layers — the HTTP DTO does not
// carry it.
const pushSubscriptionColumns = `
	id::text, device_id, generation, status, created_at, last_seen_at`

// pushSubscriptionRotated is true when an incoming registration is a *new
// generation* of the subscription rather than a retry of the one on file.
//
// Stated once and interpolated, because the three columns it decides —
// generation, failure_count and last_success_at — have to agree about it
// exactly. Two of them drifting apart is how a new endpoint inherits the success
// history of the endpoint it replaced.
//
// The three key comparisons are the definition of a generation: an endpoint or
// either key changing means the browser is a different thing to push to. The
// fourth clause covers reactivation — a row that was disabled or retired and is
// being brought back is a new lifetime too, even when the bytes are identical,
// because attempts outstanding against the previous one must not land on it.
//
// IS DISTINCT FROM rather than <>: the columns are NOT NULL today, and a
// comparison that would silently become NULL-tolerant if that ever changed is
// not worth the two saved words. status is NOT NULL by construction and closed
// by a CHECK, so it is compared plainly.
const pushSubscriptionRotated = `(
		existing.endpoint IS DISTINCT FROM EXCLUDED.endpoint
		OR existing.p256dh IS DISTINCT FROM EXCLUDED.p256dh
		OR existing.auth IS DISTINCT FROM EXCLUDED.auth
		OR existing.status <> 'active'
	)`

// upsertPushSubscriptionQuery registers a subscription, or re-registers one.
//
// # Identity and idempotency
//
// The conflict target is the logical identity — (workspace_id, user_id,
// device_id) — so re-presenting the same browser is an update of the row it
// already has, and an identical retry writes the same values and returns the
// same id. There is no read-then-write anywhere in the path, so two concurrent
// registrations of one identity cannot both insert: the unique index serialises
// them and the loser takes the DO UPDATE branch.
//
// # Why the endpoint index is not the conflict target
//
// It must not be resolvable. If an endpoint already belongs to another
// subscription — another device, another user, another workspace — this
// statement raises a unique violation on push_subscriptions_endpoint_unique and
// the registration fails. Making it a second conflict target, or resolving it
// with an UPDATE that rewrote user_id, is precisely how one account would take
// over another's browser: the endpoint is a capability URL, and whoever holds
// the row holds the right to push to it.
//
// # Retry versus rotation
//
// A retry of the current generation changes nothing but last_seen_at: the same
// endpoint and keys are already on file, so the failure count and the last
// success still describe them and are left exactly as they are. That is what
// makes an identical retry genuinely idempotent rather than merely
// non-duplicating.
//
// A rotation is a different endpoint lifetime and starts clean: the generation
// advances, and failure_count and last_success_at are reset because they belong
// to the endpoint that was just replaced. A new browser subscription inheriting
// "last delivered successfully at 09:14" would be recording a success that never
// happened to it.
//
// Either way the row returns to active with any invalidation cleared: a browser
// presenting a subscription is live evidence that the endpoint works, which is
// exactly what a retired row lacks. Without that, a subscription retired during
// a provider outage could never come back without the row being deleted.
const upsertPushSubscriptionQuery = `
	INSERT INTO chat.push_subscriptions AS existing
		(workspace_id, user_id, device_id, endpoint, p256dh, auth)
	VALUES ($1::uuid, $2::uuid, $3::text, $4::text, $5::text, $6::text)
	ON CONFLICT (workspace_id, user_id, device_id) DO UPDATE
	SET endpoint = EXCLUDED.endpoint,
	    p256dh = EXCLUDED.p256dh,
	    auth = EXCLUDED.auth,
	    status = 'active',
	    invalidated_at = NULL,
	    invalidation_reason = NULL,
	    generation = existing.generation
	                 + CASE WHEN ` + pushSubscriptionRotated + ` THEN 1 ELSE 0 END,
	    failure_count = CASE WHEN ` + pushSubscriptionRotated + `
	                         THEN 0 ELSE existing.failure_count END,
	    last_success_at = CASE WHEN ` + pushSubscriptionRotated + `
	                           THEN NULL ELSE existing.last_success_at END,
	    last_seen_at = now(),
	    updated_at = now()
	RETURNING` + pushSubscriptionColumns

// listPushSubscriptionsQuery reads one caller's own subscriptions.
//
// Scoped by workspace and user together, which is the whole authorisation: the
// identifiers come from the resolved principal, so there is no addressable form
// of this query that reaches another person's rows. Retired rows are included on
// purpose — a client reconciling has to be able to see that its device was
// invalidated, or it would never know to register again.
const listPushSubscriptionsQuery = `
	SELECT` + pushSubscriptionColumns + `
	FROM chat.push_subscriptions
	WHERE workspace_id = $1::uuid AND user_id = $2::uuid
	ORDER BY created_at, id`

// disablePushSubscriptionQuery turns off one of the caller's subscriptions.
//
// The ownership predicate is part of the write rather than a check preceding it,
// so there is no window in which a row could change hands between the two, and
// "not mine" and "not there" are indistinguishable from the outside: both affect
// no rows.
//
// The two COALESCEs keep an earlier provider verdict intact. A subscription the
// push service already reported gone stays 'gone' with the instant it happened,
// and still becomes disabled — so the owner's decision is recorded without
// erasing the diagnosis. Applying to a row that is already disabled writes the
// same values again, which is what makes the endpoint idempotent.
const disablePushSubscriptionQuery = `
	UPDATE chat.push_subscriptions
	SET status = 'disabled',
	    invalidated_at = COALESCE(invalidated_at, now()),
	    invalidation_reason = COALESCE(invalidation_reason, 'user_disabled'),
	    updated_at = now()
	WHERE id = $1::uuid
	  AND workspace_id = $2::uuid
	  AND user_id = $3::uuid`

// The three delivery outcomes. Every one of them is a compare-and-set on
// (id, generation, status), and all three predicates are load-bearing:
//
//   - generation is the endpoint lifetime the attempt was actually made
//     against. An attempt and its answer are not simultaneous: while a send is
//     in flight the browser can re-register and replace the endpoint, and the
//     answer then describes an endpoint the row no longer has. Without this
//     predicate a late 410 would retire a subscription that works, and a late
//     success or failure would write history belonging to an endpoint that is
//     gone. It is the same compare-and-set token chat.link_scans.submit_
//     generation is for a submission;
//   - status keeps a late success from reviving a subscription the owner
//     disabled, and a second 410 from overwriting the reason and instant the
//     first recorded.
//
// failure_count is bounded at int4's ceiling for the same reason the outbox
// bounds attempts: a counter that can overflow is a counter that can wrap to a
// value the CHECK refuses.
const (
	recordPushSuccessQuery = `
	UPDATE chat.push_subscriptions
	SET last_success_at = now(),
	    failure_count = 0,
	    updated_at = now()
	WHERE id = $1::uuid AND generation = $2::bigint AND status = 'active'`

	recordPushTransientFailureQuery = `
	UPDATE chat.push_subscriptions
	SET failure_count = LEAST(failure_count + 1, 2147483647),
	    updated_at = now()
	WHERE id = $1::uuid AND generation = $2::bigint AND status = 'active'`

	invalidatePushSubscriptionQuery = `
	UPDATE chat.push_subscriptions
	SET status = 'invalid',
	    invalidated_at = now(),
	    invalidation_reason = $3::text,
	    updated_at = now()
	WHERE id = $1::uuid AND generation = $2::bigint AND status = 'active'`
)

// PGXPushSubscriptionStore persists Web Push subscriptions over a pgx pool.
type PGXPushSubscriptionStore struct {
	pool Pool
}

// NewPGXPushSubscriptionStore creates a store backed by the given pool.
func NewPGXPushSubscriptionStore(pool Pool) *PGXPushSubscriptionStore {
	return &PGXPushSubscriptionStore{pool: pool}
}

// Upsert registers the caller's subscription, or re-registers an existing one.
func (s *PGXPushSubscriptionStore) Upsert(
	ctx context.Context, principal domain.Principal, registration domain.Registration,
) (domain.PushSubscription, error) {
	row := s.pool.QueryRow(ctx, upsertPushSubscriptionQuery,
		principal.WorkspaceID, principal.UserID, registration.DeviceID,
		registration.Endpoint, registration.P256dh, registration.Auth)

	// INSERT ... ON CONFLICT DO UPDATE ... RETURNING always produces exactly one
	// row, so anything other than a scanned subscription is a database failure.
	subscription, err := scanPushSubscription(row)
	if err != nil {
		return domain.PushSubscription{}, upsertError(err)
	}
	return subscription, nil
}

// upsertError keeps the endpoint-ownership refusal distinguishable from every
// other database failure. Any other unique violation would mean this statement
// and the schema disagree, which is a defect rather than something a client did.
func upsertError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) &&
		pgErr.Code == pgUniqueViolation && pgErr.ConstraintName == endpointUniqueConstraint {
		return domain.ErrEndpointConflict
	}
	return fmt.Errorf("upsert push subscription: %w", err)
}

// List returns every subscription the principal owns, oldest first.
func (s *PGXPushSubscriptionStore) List(
	ctx context.Context, principal domain.Principal,
) ([]domain.PushSubscription, error) {
	rows, err := s.pool.Query(ctx, listPushSubscriptionsQuery,
		principal.WorkspaceID, principal.UserID)
	if err != nil {
		return nil, fmt.Errorf("list push subscriptions: %w", err)
	}
	defer rows.Close()

	subscriptions := make([]domain.PushSubscription, 0)
	for rows.Next() {
		subscription, err := scanPushSubscription(rows)
		if err != nil {
			return nil, fmt.Errorf("list push subscriptions: %w", err)
		}
		subscriptions = append(subscriptions, subscription)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list push subscriptions: %w", err)
	}
	return subscriptions, nil
}

// Disable turns off one subscription the principal owns.
func (s *PGXPushSubscriptionStore) Disable(
	ctx context.Context, principal domain.Principal, subscriptionID string,
) error {
	tag, err := s.pool.Exec(ctx, disablePushSubscriptionQuery,
		subscriptionID, principal.WorkspaceID, principal.UserID)
	if err != nil {
		return fmt.Errorf("disable push subscription: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// RecordDelivery applies the outcome of one delivery attempt to the generation
// that attempt was made against, and reports whether it applied.
//
// generation is the caller's half of the contract: an attempt captures it with
// the id when it starts and hands both back here. A result that no longer
// matches the row is a no-op and applied is false — the subscription rotated,
// was disabled, or is already retired. That is an ordinary outcome in a system
// where a send and its answer are not simultaneous, not a failure: it is
// reported as a boolean rather than an error so a caller has nothing to log
// about it. Only a database that could not answer is an error.
//
// It is a store API and not an HTTP route on purpose: the only caller is the
// delivery worker that does not exist yet, and publishing an endpoint that lets
// a client declare somebody's subscription dead would be handing out an
// unsubscribe primitive.
func (s *PGXPushSubscriptionStore) RecordDelivery(
	ctx context.Context, subscriptionID string, generation int64, result domain.DeliveryResult,
) (bool, error) {
	query, args := deliveryStatement(subscriptionID, generation, result)
	tag, err := s.pool.Exec(ctx, query, args...)
	if err != nil {
		return false, fmt.Errorf("record push delivery: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// deliveryStatement picks the statement one outcome authorises.
//
// An outcome this build does not know is treated as transient, which is the
// direction that keeps a real person subscribed: the two statements that retire
// a subscription are reachable only from the two provider verdicts that mean it
// is gone.
func deliveryStatement(
	subscriptionID string, generation int64, result domain.DeliveryResult,
) (string, []any) {
	switch result.Outcome {
	case domain.OutcomeSucceeded:
		return recordPushSuccessQuery, []any{subscriptionID, generation}
	case domain.OutcomeInvalidated:
		return invalidatePushSubscriptionQuery,
			[]any{subscriptionID, generation, string(result.Reason)}
	default:
		return recordPushTransientFailureQuery, []any{subscriptionID, generation}
	}
}

// scanPushSubscription reads one row of the shared projection.
func scanPushSubscription(row pgx.Row) (domain.PushSubscription, error) {
	var subscription domain.PushSubscription
	var status string
	if err := row.Scan(&subscription.ID, &subscription.DeviceID,
		&subscription.Generation, &status,
		&subscription.CreatedAt, &subscription.LastSeenAt); err != nil {
		return domain.PushSubscription{}, err
	}
	subscription.Status = domain.Status(status)
	return subscription, nil
}
