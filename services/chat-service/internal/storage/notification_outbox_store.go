package storage

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/nicrepository/nchat/libs/go/platform/notificationevent"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

// ErrNotificationStateConflict reports that a notification was not in the state
// the caller expected it to be in, so the transition was not applied.
//
// It is a conflict and not a not-found on purpose. The caller's response to both
// is the same — stop, re-read, decide again — but only one of them means "your
// view of this row is stale", and a worker that cannot tell the difference will
// log a missing notification every time two replicas race for the same row,
// which is a normal outcome and not an incident.
var ErrNotificationStateConflict = errors.New("notification outbox state conflict")

// NotificationTransitionInput is one state change of one notification.
//
// From is required, and that is the design rather than a convenience: it is the
// state the caller believes the row is in, and it becomes the predicate of the
// UPDATE. A transition that named only the target would be a read-then-write
// with the race left in.
type NotificationTransitionInput struct {
	NotificationID string
	From           notificationevent.State
	To             notificationevent.State
	// SuppressedReason is required when To is the suppressed state and refused
	// otherwise. The database enforces the same rule; this refuses the write
	// before it is attempted so the caller gets a domain error rather than a
	// constraint violation.
	SuppressedReason string
}

// validate applies the domain contract to a requested transition.
func (i NotificationTransitionInput) validate() error {
	if i.NotificationID == "" {
		return fmt.Errorf("%w: notification id is required", domain.ErrInvalidInput)
	}
	if !i.From.CanTransitionTo(i.To) {
		return fmt.Errorf("%w: %w %q -> %q",
			domain.ErrInvalidInput, notificationevent.ErrInvalidTransition, i.From, i.To)
	}
	if err := notificationevent.ValidateSuppressedReason(i.To, i.SuppressedReason); err != nil {
		return fmt.Errorf("%w: %w", domain.ErrInvalidInput, err)
	}
	return nil
}

// PGXNotificationOutboxStore is the only supported way to change the state of a
// notification. Nothing else in this service writes
// chat.notification_outbox.status: the two producers insert rows and never
// update them.
//
// Concrete, with no interface in front of it. There is one implementation, no
// consumer that needs to swap it, and its own tests drive it through pgxmock at
// the Pool boundary — which is where the seam already is. An interface here
// would name a dependency nothing depends on.
type PGXNotificationOutboxStore struct {
	pool Pool
}

// NewPGXNotificationOutboxStore creates a PGXNotificationOutboxStore backed by
// the given pool.
func NewPGXNotificationOutboxStore(pool Pool) *PGXNotificationOutboxStore {
	return &PGXNotificationOutboxStore{pool: pool}
}

// transitionNotificationStateQuery is a compare-and-set, and every part of it is
// load-bearing.
//
// The WHERE names the expected current state, so two replicas evaluating the
// same rule against the same row cannot both apply their transition: the second
// one matches nothing and is told so. Under READ COMMITTED the loser's UPDATE
// re-reads the row the winner committed and re-checks the predicate against it,
// which is exactly the serialization needed here — no advisory lock, no SELECT
// FOR UPDATE, no read before the write.
//
// processed_at is set when, and only when, the notification reaches a state it
// will never leave. It is the answer to "when did this stop being work?", and
// writing it on every hop would make it mean "when was this last touched",
// which updated_at already means.
//
// suppressed_reason is assigned unconditionally rather than only on suppression,
// so a transition out of a state that carried one clears it. NULLIF is what
// turns "the caller supplied none" into SQL NULL; validate() has already refused
// the combinations where that would be wrong.
const transitionNotificationStateQuery = `
	UPDATE chat.notification_outbox
	SET status = $2::text,
	    suppressed_reason = NULLIF($3::text, ''),
	    processed_at = CASE
	        WHEN $2::text IN ('suppressed', 'sent', 'failed') THEN now()
	        ELSE processed_at
	    END,
	    updated_at = now()
	WHERE id = $1::uuid
	  AND status = $4::text`

// TransitionState applies one state change, or reports that it could not.
//
// Three refusals, and they are deliberately different errors. A transition the
// machine does not allow is invalid input and never reaches the database. A row
// that had already moved is ErrNotificationStateConflict. Anything else
// PostgreSQL says is propagated as itself — a constraint the trigger raises, a
// connection that dropped — because turning a database error into a state
// conflict would let a broken deployment look like ordinary contention.
func (s *PGXNotificationOutboxStore) TransitionState(
	ctx context.Context, input NotificationTransitionInput,
) error {
	if err := input.validate(); err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, transitionNotificationStateQuery,
		input.NotificationID, string(input.To), input.SuppressedReason, string(input.From))
	if err != nil {
		return transitionError(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotificationStateConflict
	}
	return nil
}

// transitionError keeps the database's own refusals distinguishable.
//
// The trigger raises 23514, the same class as a CHECK, for a transition the
// application layer should already have refused. Reaching it means the Go
// machine and the SQL one disagree, which is a defect rather than contention, so
// it is reported as invalid input and not as a conflict.
func transitionError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23514" {
		return fmt.Errorf("%w: %w", domain.ErrInvalidInput, notificationevent.ErrInvalidTransition)
	}
	return fmt.Errorf("transition notification state: %w", err)
}
