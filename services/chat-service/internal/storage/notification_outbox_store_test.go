package storage_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	pgxmock "github.com/pashagolub/pgxmock/v2"

	"github.com/nicrepository/nchat/libs/go/platform/notificationevent"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

const notifyID = "74100000-0000-4000-8000-0000000000aa"

// transitionSQL pins the two properties that make the write safe: the row is
// addressed by id AND by the state the caller expected it to be in, so nothing
// is read before it is written.
const transitionSQL = `(?s)UPDATE chat\.notification_outbox.*` +
	`SET status = \$2::text.*suppressed_reason = NULLIF\(\$3::text, ''\).*` +
	`WHERE id = \$1::uuid.*AND status = \$4::text`

func newTransitionStore(t *testing.T) (pgxmock.PgxPoolIface, *storage.PGXNotificationOutboxStore) {
	t.Helper()
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	t.Cleanup(mock.Close)
	return mock, storage.NewPGXNotificationOutboxStore(mock)
}

// A transition the machine allows reaches the database, carrying the expected
// current state as a predicate.
func TestTransitionStateAppliesAnAllowedChange(t *testing.T) {
	mock, store := newTransitionStore(t)
	mock.ExpectExec(transitionSQL).
		WithArgs(notifyID, "eligible", "", "pending").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err := store.TransitionState(context.Background(), storage.NotificationTransitionInput{
		NotificationID: notifyID,
		From:           notificationevent.StatePending,
		To:             notificationevent.StateEligible,
	})
	if err != nil {
		t.Fatalf("TransitionState: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// Suppression carries its reason through to the write.
func TestTransitionStateCarriesTheSuppressionReason(t *testing.T) {
	mock, store := newTransitionStore(t)
	mock.ExpectExec(transitionSQL).
		WithArgs(notifyID, "suppressed", "quiet_hours", "pending").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err := store.TransitionState(context.Background(), storage.NotificationTransitionInput{
		NotificationID:   notifyID,
		From:             notificationevent.StatePending,
		To:               notificationevent.StateSuppressed,
		SuppressedReason: "quiet_hours",
	})
	if err != nil {
		t.Fatalf("TransitionState: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// The row had already moved. Zero rows affected is a conflict, not a success and
// not a missing notification.
func TestTransitionStateReportsAConflictWhenTheRowMoved(t *testing.T) {
	mock, store := newTransitionStore(t)
	mock.ExpectExec(transitionSQL).
		WithArgs(notifyID, "processing", "", "eligible").
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))

	err := store.TransitionState(context.Background(), storage.NotificationTransitionInput{
		NotificationID: notifyID,
		From:           notificationevent.StateEligible,
		To:             notificationevent.StateProcessing,
	})
	if !errors.Is(err, storage.ErrNotificationStateConflict) {
		t.Fatalf("TransitionState = %v, want ErrNotificationStateConflict", err)
	}
}

// Transitions the machine refuses never reach the database at all — the mock
// would fail on an unexpected call.
func TestTransitionStateRefusesInvalidTransitionsBeforeWriting(t *testing.T) {
	refused := map[string]storage.NotificationTransitionInput{
		"out of a suppressed terminal": {
			NotificationID: notifyID,
			From:           notificationevent.StateSuppressed,
			To:             notificationevent.StateFailed,
		},
		"out of a delivered terminal": {
			NotificationID: notifyID,
			From:           notificationevent.StateSent,
			To:             notificationevent.StateRetrying,
		},
		"out of a failed terminal": {
			NotificationID: notifyID,
			From:           notificationevent.StateFailed,
			To:             notificationevent.StateSent,
		},
		"skipping evaluation": {
			NotificationID: notifyID,
			From:           notificationevent.StatePending,
			To:             notificationevent.StateSent,
		},
		"an undeclared state": {
			NotificationID: notifyID,
			From:           notificationevent.StatePending,
			To:             "delivered",
		},
		"no notification named": {
			From: notificationevent.StatePending,
			To:   notificationevent.StateEligible,
		},
	}
	for name, input := range refused {
		t.Run(name, func(t *testing.T) {
			mock, store := newTransitionStore(t)
			err := store.TransitionState(context.Background(), input)
			assertInvalidInput(t, err)
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("a refused transition must issue no statement: %v", err)
			}
		})
	}
}

// The reason contract is refused at the same point, for the same reason: a row
// nobody can interpret must never be written.
func TestTransitionStateEnforcesTheSuppressionReasonContract(t *testing.T) {
	refused := map[string]storage.NotificationTransitionInput{
		"suppressed without a reason": {
			NotificationID: notifyID,
			From:           notificationevent.StatePending,
			To:             notificationevent.StateSuppressed,
		},
		"eligible carrying a reason": {
			NotificationID:   notifyID,
			From:             notificationevent.StatePending,
			To:               notificationevent.StateEligible,
			SuppressedReason: "quiet_hours",
		},
		"a reason longer than the column holds": {
			NotificationID:   notifyID,
			From:             notificationevent.StatePending,
			To:               notificationevent.StateSuppressed,
			SuppressedReason: strings.Repeat("x", notificationevent.SuppressedReasonMaxLen+1),
		},
	}
	for name, input := range refused {
		t.Run(name, func(t *testing.T) {
			mock, store := newTransitionStore(t)
			assertInvalidInput(t, store.TransitionState(context.Background(), input))
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("a refused transition must issue no statement: %v", err)
			}
		})
	}
}

// The longest reason the contract permits is accepted, so the bound is a bound
// and not an off-by-one refusal.
func TestTransitionStateAcceptsTheLongestPermittedReason(t *testing.T) {
	mock, store := newTransitionStore(t)
	reason := strings.Repeat("x", notificationevent.SuppressedReasonMaxLen)
	mock.ExpectExec(transitionSQL).
		WithArgs(notifyID, "suppressed", reason, "eligible").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err := store.TransitionState(context.Background(), storage.NotificationTransitionInput{
		NotificationID:   notifyID,
		From:             notificationevent.StateEligible,
		To:               notificationevent.StateSuppressed,
		SuppressedReason: reason,
	})
	if err != nil {
		t.Fatalf("TransitionState: %v", err)
	}
}

// A database refusal is not contention. The trigger raising a check violation
// means the Go machine and the SQL one disagree, which is a defect to surface,
// not a race to retry.
func TestTransitionStateSeparatesDatabaseRefusalFromConflict(t *testing.T) {
	mock, store := newTransitionStore(t)
	mock.ExpectExec(transitionSQL).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(&pgconn.PgError{Code: "23514", ConstraintName: "notification_outbox_transition"})

	err := store.TransitionState(context.Background(), storage.NotificationTransitionInput{
		NotificationID: notifyID,
		From:           notificationevent.StatePending,
		To:             notificationevent.StateEligible,
	})
	assertInvalidInput(t, err)
	if errors.Is(err, storage.ErrNotificationStateConflict) {
		t.Fatal("a refusal by the database must not read as ordinary contention")
	}
}

// Everything else PostgreSQL says is propagated as itself.
func TestTransitionStatePropagatesUnexpectedErrors(t *testing.T) {
	mock, store := newTransitionStore(t)
	boom := errors.New("connection reset")
	mock.ExpectExec(transitionSQL).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(boom)

	err := store.TransitionState(context.Background(), storage.NotificationTransitionInput{
		NotificationID: notifyID,
		From:           notificationevent.StatePending,
		To:             notificationevent.StateEligible,
	})
	if !errors.Is(err, boom) {
		t.Fatalf("TransitionState = %v, want the underlying error", err)
	}
	if errors.Is(err, storage.ErrNotificationStateConflict) || errors.Is(err, domain.ErrInvalidInput) {
		t.Fatal("an unexpected database error must not be reclassified")
	}
}

func assertInvalidInput(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("error = %v, want ErrInvalidInput", err)
	}
}
