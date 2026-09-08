package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/nicrepository/nchat/libs/go/platform/notificationevent"
)

// ErrNotificationStateConflict reports that a notification was not in the state
// the caller believed it was in, so nothing was written.
//
// It is a conflict and not a failure. Two workers racing for one row is the
// normal outcome of running more than one replica, and a worker that could not
// tell that apart from a broken database would log an incident every time
// Blue/Green ran two slots at once.
var ErrNotificationStateConflict = errors.New("notification outbox state conflict")

// ErrInvalidNotificationTransition reports a state change the machine in
// libs/go/platform/notificationevent does not allow, or a suppression reason
// that does not agree with the state it was given.
//
// Distinct from a conflict on purpose: a conflict means the row moved, this
// means the caller asked for something no row could ever do.
var ErrInvalidNotificationTransition = errors.New("invalid notification outbox transition")

// NotificationEvent is one row of chat.notification_outbox, in the only shape
// the worker needs.
//
// References only, exactly as the table stores them: no message body, no
// address, no subscription. What a delivery channel needs to render is read
// back later through the authorization every other read path applies, so a
// worker holding this struct has gained nothing a reader did not already have.
type NotificationEvent struct {
	// ID is the notification's identity for its whole life, across every
	// attempt. It is what the worker hands a delivery adapter as an idempotency
	// key, and the unique index on (workspace_id, recipient_user_id, dedupe_key)
	// is what guarantees one logical event has exactly one of them.
	ID          string
	WorkspaceID string
	RecipientID string
	EventType   string
	Priority    string
	SourceType  string
	SourceID    string
	DedupeKey   string
	// Attempts is how many times this event has been claimed, counting the
	// claim that produced this struct. It is therefore also the *identity of
	// that claim*, and every finalisation carries it back as a predicate.
	//
	// That is what stops a stale worker from finalising somebody else's claim.
	// A worker whose lease expired mid-delivery still holds the attempts value
	// its own claim returned; the reclaim that took the row from it incremented
	// the column, so the stale worker's compare-and-set matches nothing and it
	// is told it lost the row. Without this predicate "still processing" was the
	// only condition checked, and the two claims were indistinguishable.
	Attempts   int
	OccurredAt time.Time
}

// NotificationOutboxStore is every persistent operation the notification worker
// performs. An interface because the worker's own tests drive it with a fake,
// while the claim semantics it describes are proved against a real PostgreSQL.
type NotificationOutboxStore interface {
	ListPending(ctx context.Context, limit int) ([]NotificationEvent, error)
	MarkEvaluated(ctx context.Context, id string, state notificationevent.State, reason string) error
	ClaimDue(ctx context.Context, batchSize, maxAttempts int, lease time.Duration) ([]NotificationEvent, error)
	// The three finalisations take the attempts value their claim returned, and
	// apply only while the row still carries it. See NotificationEvent.Attempts.
	MarkDelivered(ctx context.Context, id string, attempt int) error
	ScheduleRetry(ctx context.Context, id string, attempt int, delay time.Duration, category string) error
	MarkFailed(ctx context.Context, id string, attempt int, category string) error
	FailExhausted(ctx context.Context, maxAttempts int) (int, error)
	Backlog(ctx context.Context) (int, error)
}

// PGXNotificationOutboxStore implements NotificationOutboxStore over a pgx pool.
type PGXNotificationOutboxStore struct {
	pool Pool
}

// NewPGXNotificationOutboxStore creates a store backed by the given pool.
func NewPGXNotificationOutboxStore(pool Pool) *PGXNotificationOutboxStore {
	return &PGXNotificationOutboxStore{pool: pool}
}

// notificationColumns is the projection both reads share, so a column added to
// one can never be forgotten in the other.
const notificationColumns = `
	o.id::text, o.workspace_id::text, o.recipient_user_id::text,
	o.kind, o.priority, o.source_type, o.message_id::text,
	COALESCE(o.dedupe_key, ''), o.attempts, o.occurred_at`

// listPendingQuery reads events no policy has looked at yet.
//
// No lock and no lease. Evaluation reaches nothing outside this process, so the
// worst a race costs is two workers deciding the same row and one of them
// finding its compare-and-set already applied — cheap, and cheaper than holding
// a transaction open across a policy call. The claim that precedes a *delivery*
// is a different matter and is locked accordingly.
const listPendingQuery = `
	SELECT` + notificationColumns + `
	FROM chat.notification_outbox o
	WHERE o.status = 'pending'
	ORDER BY o.occurred_at, o.id
	LIMIT $1`

// ListPending returns at most limit events awaiting a policy decision, oldest
// occurrence first.
func (s *PGXNotificationOutboxStore) ListPending(ctx context.Context, limit int) ([]NotificationEvent, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, listPendingQuery, limit)
	if err != nil {
		return nil, fmt.Errorf("list pending notifications: %w", err)
	}
	return scanNotificationEvents(rows, "list pending notifications")
}

// markEvaluatedQuery records a policy's decision about a pending event.
//
// The WHERE names the state the caller believes the row is in, which makes the
// statement a compare-and-set rather than a read followed by a write with the
// race left in. processed_at is written only for suppression, because that is
// the only outcome here a row never leaves.
//
// # Why this writes next_attempt_at
//
// Becoming eligible is the moment the event becomes *available to the worker*,
// and that instant has to be recorded, because it is not the instant the event
// happened. A message from three weeks ago that sat pending until today became
// available today; occurred_at still says three weeks ago.
//
// Leaving the column NULL and letting the claim fall back to occurred_at merged
// the two facts, and the merge had a consequence: every historical event
// promoted to eligible entered the queue with a decades-old priority and
// overtook retries that had genuinely been waiting. Under a steady trickle of
// old pending rows being evaluated, a due retry could be overtaken for ever.
//
// now() is the database's clock — the same one the lease deadline, the retry
// schedule and the claim's due predicate are all written and compared against.
// Taking it from the worker's clock instead would make availability the only
// value in the protocol subject to skew between replicas.
//
// Suppression deliberately writes nothing here: a suppressed event is terminal
// and must never become claimable.
const markEvaluatedQuery = `
	UPDATE chat.notification_outbox
	SET status = $2::text,
	    suppressed_reason = NULLIF($3::text, ''),
	    next_attempt_at = CASE WHEN $2::text = 'eligible' THEN now() ELSE next_attempt_at END,
	    processed_at = CASE WHEN $2::text = 'suppressed' THEN now() ELSE processed_at END,
	    updated_at = now()
	WHERE id = $1::uuid
	  AND status = 'pending'`

// MarkEvaluated moves a pending event to the state a policy chose for it.
//
// Only the two transitions the machine allows out of pending are accepted, and
// the reason contract — exactly the suppressed state carries one — is checked
// here so a caller gets a domain error instead of a constraint violation.
func (s *PGXNotificationOutboxStore) MarkEvaluated(
	ctx context.Context, id string, state notificationevent.State, reason string,
) error {
	if !notificationevent.StatePending.CanTransitionTo(state) {
		return fmt.Errorf("%w: pending -> %q", ErrInvalidNotificationTransition, state)
	}
	if err := notificationevent.ValidateSuppressedReason(state, reason); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidNotificationTransition, err)
	}
	return s.applyTransition(ctx, markEvaluatedQuery, id, string(state), reason)
}

// claimDueQuery takes a batch of due events and leases them in one statement.
//
// Every clause is load-bearing:
//
//   - FOR UPDATE SKIP LOCKED is what lets several replicas drain the queue
//     without coordinating. Each takes rows nobody else holds, so no two workers
//     can ever be delivering the same notification because of this query;
//   - the UPDATE *is* the claim. There is no window between choosing a row and
//     owning it, because they are the same write;
//   - next_attempt_at is when the row next becomes available, in every claimable
//     state: the instant a policy made it eligible, the instant a backoff ends,
//     the instant a lease lapses. A worker that died leaves its rows in
//     'processing' with a deadline in the past, which is exactly what the
//     predicate calls due, so a crash costs one lease and not a lost event;
//   - 'processing' is in the predicate for that reason alone. A row still inside
//     its lease is not due and cannot be taken;
//   - attempts is counted here rather than on failure. A notification that kills
//     the worker before anything can be recorded still burns an attempt each
//     time it is reclaimed, so a poison event reaches the ceiling instead of
//     cycling forever;
//   - the ORDER BY matches idx_notification_outbox_claimable exactly, so the
//     LIMIT stops the scan after a batch instead of sorting the backlog.
//
// # Why the order is next_attempt_at, and why occurred_at only breaks ties
//
// next_attempt_at is the availability instant — when the row became, or becomes,
// something this worker may take. Every claimable state writes it: MarkEvaluated
// on promotion to eligible, ScheduleRetry when a backoff is computed, this
// statement when a lease is granted. So the queue is FIFO by *availability*,
// which is the only ordering that guarantees liveness.
//
// occurred_at is a different fact — when the thing happened in the product — and
// it is deliberately demoted to a tie-break. Two earlier versions of this query
// let it decide priority and both starved retries:
//
//	next_attempt_at NULLS FIRST     put every fresh eligible row, which had no
//	                                next_attempt_at, ahead of every retry;
//	COALESCE(next_attempt_at,       fell back to occurred_at for the same rows,
//	         occurred_at)           so a three-week-old message evaluated today
//	                                entered the queue with a three-week-old
//	                                priority and overtook a retry due a minute
//	                                ago. A trickle of historical pending rows
//	                                being evaluated could hold a due retry back
//	                                indefinitely.
//
// With availability recorded rather than inferred, a retry that became due at T
// is ahead of everything that became available after T, and nothing can become
// available before T once T has passed. The queue ahead of it is therefore
// finite and drains, which is what makes progress guaranteed rather than likely.
//
// # Why NULL is not due
//
// The predicate is a plain comparison, so a NULL next_attempt_at is not due —
// which is exactly right for pending, the one state that carries NULL by design
// and is not claimable anyway. It also picks the safe direction if a future
// writer ever forgets to stamp availability: the row waits and shows up in the
// backlog gauge, instead of being claimed with an unknown priority and
// overtaking everything else. 000044 backfills the same invariant onto any row
// that predates it.
//
// Nothing external is called while this runs, and it holds no transaction of the
// caller's: the row is leased, the statement commits, and only then does a
// delivery begin.
const claimDueQuery = `
	WITH due AS (
		SELECT o.id
		FROM chat.notification_outbox o
		WHERE o.status IN ('eligible', 'retrying', 'processing')
		  AND o.attempts < $3
		  AND o.next_attempt_at <= now()
		ORDER BY o.next_attempt_at, o.occurred_at, o.id
		LIMIT $1
		FOR UPDATE SKIP LOCKED
	)
	UPDATE chat.notification_outbox o
	SET status = 'processing',
	    attempts = LEAST(o.attempts + 1, 32767),
	    next_attempt_at = now() + ($2 * interval '1 second'),
	    updated_at = now()
	FROM due
	WHERE o.id = due.id
	RETURNING` + notificationColumns

// ClaimDue leases up to batchSize events for delivery.
//
// An event whose attempts already reached maxAttempts is not claimed: it is work
// that can no longer succeed, and FailExhausted is what retires it.
func (s *PGXNotificationOutboxStore) ClaimDue(
	ctx context.Context, batchSize, maxAttempts int, lease time.Duration,
) ([]NotificationEvent, error) {
	if batchSize <= 0 || maxAttempts <= 0 {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, claimDueQuery, batchSize, lease.Seconds(), maxAttempts)
	if err != nil {
		return nil, fmt.Errorf("claim due notifications: %w", err)
	}
	return scanNotificationEvents(rows, "claim due notifications")
}

// markDeliveredQuery records that a channel accepted the notification.
//
// last_error is cleared: a delivered notification that still names the failure
// of an earlier attempt would read, months later, as one that failed.
//
// The attempts predicate is what makes this the finalisation of *this* claim.
// Without it a worker whose lease expired mid-delivery would mark the row sent
// under the claim another worker now holds — cancelling a delivery that is still
// in flight and recording an outcome for an attempt that never reported one.
const markDeliveredQuery = `
	UPDATE chat.notification_outbox
	SET status = 'sent',
	    processed_at = now(),
	    next_attempt_at = NULL,
	    last_error = NULL,
	    updated_at = now()
	WHERE id = $1::uuid
	  AND status = 'processing'
	  AND attempts = $2`

// MarkDelivered ends a claim in the terminal state that means somebody was told.
//
// attempt is the value the claim returned. A mismatch means this worker no
// longer owns the row and is reported as ErrNotificationStateConflict.
func (s *PGXNotificationOutboxStore) MarkDelivered(ctx context.Context, id string, attempt int) error {
	return s.applyTransition(ctx, markDeliveredQuery, id, attempt)
}

// scheduleRetryQuery hands the event back to the queue with its next attempt
// already decided.
//
// The delay is computed by the worker's retry policy and persisted here, so a
// restart resumes the same schedule rather than retrying everything at once.
const scheduleRetryQuery = `
	UPDATE chat.notification_outbox
	SET status = 'retrying',
	    next_attempt_at = now() + ($3 * interval '1 second'),
	    last_error = $4::text,
	    updated_at = now()
	WHERE id = $1::uuid
	  AND status = 'processing'
	  AND attempts = $2`

// ScheduleRetry releases a claim for another attempt at a stated time.
//
// Guarded by the claim's attempts for the same reason as MarkDelivered, and it
// matters most here: a stale worker rescheduling would push the *live* claim's
// lease out into the future and overwrite its error category, so the delivery
// actually in flight would be finalised against a row that had already been
// handed back to the queue.
func (s *PGXNotificationOutboxStore) ScheduleRetry(
	ctx context.Context, id string, attempt int, delay time.Duration, category string,
) error {
	return s.applyTransition(ctx, scheduleRetryQuery, id, attempt, delay.Seconds(), category)
}

// markFailedQuery ends a claim in the terminal state that means we tried and
// could not.
//
// Emphatically not the same row state as suppression: suppressed_reason is left
// untouched and stays NULL, so "nobody was told, on purpose" and "we tried and
// could not" remain two different, queryable facts.
const markFailedQuery = `
	UPDATE chat.notification_outbox
	SET status = 'failed',
	    processed_at = now(),
	    next_attempt_at = NULL,
	    last_error = $3::text,
	    updated_at = now()
	WHERE id = $1::uuid
	  AND status = 'processing'
	  AND attempts = $2`

// MarkFailed retires a claim that will not be attempted again.
//
// Guarded by the claim's attempts: a stale worker must not be able to retire an
// event permanently on the strength of an attempt that has already been
// superseded.
func (s *PGXNotificationOutboxStore) MarkFailed(
	ctx context.Context, id string, attempt int, category string,
) error {
	return s.applyTransition(ctx, markFailedQuery, id, attempt, category)
}

// failExhaustedQuery retires abandoned claims that can no longer succeed.
//
// It exists for one case, and the case is real: a notification whose delivery
// kills the worker is never finalised by anybody, so it is reclaimed, counted,
// abandoned, reclaimed again. Without this it would do that forever. The
// predicate is narrow on purpose — only a claim whose lease has already expired
// and whose attempts are spent — so it can never overtake a delivery that is
// still in flight, and processing -> failed is a transition the trigger allows.
const failExhaustedQuery = `
	UPDATE chat.notification_outbox
	SET status = 'failed',
	    processed_at = now(),
	    next_attempt_at = NULL,
	    last_error = 'attempts_exhausted',
	    updated_at = now()
	WHERE status = 'processing'
	  AND attempts >= $1
	  AND next_attempt_at IS NOT NULL
	  AND next_attempt_at <= now()`

// FailExhausted retires every abandoned claim that has spent its attempts, and
// reports how many. It is the bound on a poison event.
//
// It is the one path that finalises a 'processing' row without naming a claim
// generation, and it does not need to: its predicate already requires a lease
// that has lapsed, and a claim taken by any live worker sets next_attempt_at
// into the future. So a row this statement can touch is by definition one no
// worker holds. It is also unreachable for a row a worker could still claim,
// because the claim refuses attempts >= maxAttempts.
func (s *PGXNotificationOutboxStore) FailExhausted(ctx context.Context, maxAttempts int) (int, error) {
	if maxAttempts <= 0 {
		return 0, nil
	}
	tag, err := s.pool.Exec(ctx, failExhaustedQuery, maxAttempts)
	if err != nil {
		return 0, fmt.Errorf("fail exhausted notifications: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// backlogQuery counts the work that is neither terminal nor in flight. Its
// predicate is idx_notification_outbox_open's, so the count is an index scan
// over the backlog rather than over the history.
const backlogQuery = `
	SELECT count(*)
	FROM chat.notification_outbox
	WHERE status IN ('pending', 'eligible', 'retrying')`

// Backlog reports how many notifications are waiting to be delivered.
func (s *PGXNotificationOutboxStore) Backlog(ctx context.Context) (int, error) {
	var backlog int
	if err := s.pool.QueryRow(ctx, backlogQuery).Scan(&backlog); err != nil {
		return 0, fmt.Errorf("count notification backlog: %w", err)
	}
	return backlog, nil
}

// applyTransition executes a compare-and-set and turns its result into the
// error the caller can act on.
//
// Every statement in this file is the same shape — a SET clause over an id and
// an expected status — so the reading of the result lives here once. A claim
// this worker no longer holds, because its lease expired and another worker took
// the row, matches nothing and is reported as a conflict: a thing to count, not
// a thing to fail on.
func (s *PGXNotificationOutboxStore) applyTransition(
	ctx context.Context, query, id string, args ...any,
) error {
	if id == "" {
		return fmt.Errorf("%w: notification id is required", ErrInvalidNotificationTransition)
	}
	tag, err := s.pool.Exec(ctx, query, append([]any{id}, args...)...)
	if err != nil {
		return transitionError(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotificationStateConflict
	}
	return nil
}

// transitionError keeps the database's own refusal distinguishable from a lost
// race.
//
// chat.enforce_notification_outbox_transition raises 23514 for a transition the
// Go machine should already have refused. Reaching it means the two definitions
// disagree, which is a defect and not contention, so it must not be reported as
// an ordinary conflict.
func transitionError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23514" {
		return fmt.Errorf("%w: refused by the database", ErrInvalidNotificationTransition)
	}
	return fmt.Errorf("transition notification: %w", err)
}

// scanNotificationEvents reads a result set of notification rows. Shared by both
// reads so their projections cannot drift apart.
func scanNotificationEvents(rows pgx.Rows, operation string) ([]NotificationEvent, error) {
	defer rows.Close()

	var events []NotificationEvent
	for rows.Next() {
		var event NotificationEvent
		if err := rows.Scan(&event.ID, &event.WorkspaceID, &event.RecipientID,
			&event.EventType, &event.Priority, &event.SourceType, &event.SourceID,
			&event.DedupeKey, &event.Attempts, &event.OccurredAt); err != nil {
			return nil, fmt.Errorf("%s: %w", operation, err)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", operation, err)
	}
	return events, nil
}
