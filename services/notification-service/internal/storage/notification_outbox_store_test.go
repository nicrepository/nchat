package storage_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	pgxmock "github.com/pashagolub/pgxmock/v2"

	"github.com/nicrepository/nchat/libs/go/platform/notificationevent"
	"github.com/nicrepository/nchat/services/notification-service/internal/storage"
)

// Issue #742: the store's contract at the pgx boundary.
//
// What is provable here is the shape — which statement runs, with which
// arguments, and how each result is turned into an error the worker can act on.
// What is NOT provable here is that the SQL means what it says: SKIP LOCKED,
// the lease and the transition trigger are the database's behaviour, and they
// are proved in notification_outbox_postgres_test.go against a real one.

func newNotificationMock(t *testing.T) pgxmock.PgxPoolIface {
	t.Helper()
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	t.Cleanup(mock.Close)
	return mock
}

func notificationRows() *pgxmock.Rows {
	return pgxmock.NewRows([]string{
		"id", "workspace_id", "recipient_user_id", "kind", "priority",
		"source_type", "message_id", "dedupe_key", "attempts", "occurred_at",
	}).AddRow("n1", "ws-1", "user-1", "mention", "high",
		"message", "msg-1", "message:msg-1:mention", 2, time.Now())
}

func TestListPendingReadsTheEventContract(t *testing.T) {
	mock := newNotificationMock(t)
	mock.ExpectQuery(`FROM chat\.notification_outbox`).
		WithArgs(10).
		WillReturnRows(notificationRows())

	events, err := storage.NewPGXNotificationOutboxStore(mock).ListPending(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListPending: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	event := events[0]
	if event.ID != "n1" || event.WorkspaceID != "ws-1" || event.RecipientID != "user-1" ||
		event.EventType != "mention" || event.SourceID != "msg-1" || event.Attempts != 2 {
		t.Fatalf("unexpected event: %+v", event)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// A non-positive limit is a caller asking for nothing; it must not become an
// unbounded query.
func TestListPendingRefusesANonPositiveLimit(t *testing.T) {
	mock := newNotificationMock(t)

	events, err := storage.NewPGXNotificationOutboxStore(mock).ListPending(context.Background(), 0)
	if err != nil || events != nil {
		t.Fatalf("ListPending(0) = (%v, %v), want (nil, nil)", events, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("a query was issued for an empty request: %v", err)
	}
}

func TestListPendingPropagatesAQueryFailure(t *testing.T) {
	mock := newNotificationMock(t)
	mock.ExpectQuery(`FROM chat\.notification_outbox`).
		WithArgs(5).
		WillReturnError(errors.New("connection reset"))

	if _, err := storage.NewPGXNotificationOutboxStore(mock).ListPending(context.Background(), 5); err == nil {
		t.Fatal("expected an error")
	}
}

func TestListPendingReportsAScanFailure(t *testing.T) {
	mock := newNotificationMock(t)
	mock.ExpectQuery(`FROM chat\.notification_outbox`).
		WithArgs(5).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow("n1"))

	if _, err := storage.NewPGXNotificationOutboxStore(mock).ListPending(context.Background(), 5); err == nil {
		t.Fatal("a row that does not match the projection was accepted")
	}
}

func TestMarkEvaluatedAppliesThePolicyDecision(t *testing.T) {
	tests := map[string]struct {
		state  notificationevent.State
		reason string
	}{
		"eligible":   {notificationevent.StateEligible, ""},
		"suppressed": {notificationevent.StateSuppressed, "quiet_hours"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			mock := newNotificationMock(t)
			// Promotion stamps the availability instant from the database clock,
			// which is why the statement carries no timestamp argument of its own.
			mock.ExpectExec(`next_attempt_at = CASE WHEN \$2::text = 'eligible' THEN now\(\)`).
				WithArgs("n1", string(tc.state), tc.reason).
				WillReturnResult(pgxmock.NewResult("UPDATE", 1))

			store := storage.NewPGXNotificationOutboxStore(mock)
			if err := store.MarkEvaluated(context.Background(), "n1", tc.state, tc.reason); err != nil {
				t.Fatalf("MarkEvaluated: %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet expectations: %v", err)
			}
		})
	}
}

// Only the two transitions out of pending exist. Anything else must be refused
// before it reaches the database, so the caller gets a domain error rather than
// a constraint violation.
func TestMarkEvaluatedRefusesTransitionsTheMachineDoesNotAllow(t *testing.T) {
	tests := map[string]struct {
		state  notificationevent.State
		reason string
	}{
		"straight to sent":          {notificationevent.StateSent, ""},
		"straight to processing":    {notificationevent.StateProcessing, ""},
		"suppressed with no reason": {notificationevent.StateSuppressed, ""},
		"eligible with a reason":    {notificationevent.StateEligible, "quiet_hours"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			mock := newNotificationMock(t)
			store := storage.NewPGXNotificationOutboxStore(mock)

			err := store.MarkEvaluated(context.Background(), "n1", tc.state, tc.reason)
			if !errors.Is(err, storage.ErrInvalidNotificationTransition) {
				t.Fatalf("err = %v, want an invalid transition", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("a refused transition still reached the database: %v", err)
			}
		})
	}
}

// A compare-and-set that matched nothing means the row moved on. It is a
// conflict, not a failure, and the worker branches on the difference.
func TestTransitionsReportAConflictWhenTheRowMovedOn(t *testing.T) {
	tests := map[string]struct {
		args []any
		call func(storage.NotificationOutboxStore) error
	}{
		"delivered": {[]any{"n1", 3}, func(s storage.NotificationOutboxStore) error {
			return s.MarkDelivered(context.Background(), "n1", 3)
		}},
		"retry": {[]any{"n1", 3, 60.0, "delivery_transient"}, func(s storage.NotificationOutboxStore) error {
			return s.ScheduleRetry(context.Background(), "n1", 3, time.Minute, "delivery_transient")
		}},
		"failed": {[]any{"n1", 3, "delivery_permanent"}, func(s storage.NotificationOutboxStore) error {
			return s.MarkFailed(context.Background(), "n1", 3, "delivery_permanent")
		}},
		"evaluated": {[]any{"n1", "eligible", ""}, func(s storage.NotificationOutboxStore) error {
			return s.MarkEvaluated(context.Background(), "n1", notificationevent.StateEligible, "")
		}},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			mock := newNotificationMock(t)
			mock.ExpectExec(`UPDATE chat\.notification_outbox`).
				WithArgs(tc.args...).
				WillReturnResult(pgxmock.NewResult("UPDATE", 0))

			err := tc.call(storage.NewPGXNotificationOutboxStore(mock))
			if !errors.Is(err, storage.ErrNotificationStateConflict) {
				t.Fatalf("err = %v, want a state conflict", err)
			}
		})
	}
}

// The trigger raises 23514 for a transition the Go machine should already have
// refused. Reaching it means the two definitions disagree, which is a defect —
// so it must not be reported as ordinary contention.
func TestTransitionsSurfaceTheDatabasesOwnRefusal(t *testing.T) {
	mock := newNotificationMock(t)
	mock.ExpectExec(`UPDATE chat\.notification_outbox`).
		WithArgs("n1", 1).
		WillReturnError(&pgconn.PgError{Code: "23514", Message: "transition is not allowed"})

	err := storage.NewPGXNotificationOutboxStore(mock).MarkDelivered(context.Background(), "n1", 1)
	if !errors.Is(err, storage.ErrInvalidNotificationTransition) {
		t.Fatalf("err = %v, want an invalid transition", err)
	}
	if errors.Is(err, storage.ErrNotificationStateConflict) {
		t.Fatal("a schema disagreement was reported as ordinary contention")
	}
}

func TestTransitionsPropagateAnUnexpectedDatabaseError(t *testing.T) {
	mock := newNotificationMock(t)
	mock.ExpectExec(`UPDATE chat\.notification_outbox`).
		WithArgs("n1", 1, "delivery_transient").
		WillReturnError(errors.New("connection reset"))

	err := storage.NewPGXNotificationOutboxStore(mock).MarkFailed(context.Background(), "n1", 1, "delivery_transient")
	if err == nil || errors.Is(err, storage.ErrNotificationStateConflict) ||
		errors.Is(err, storage.ErrInvalidNotificationTransition) {
		t.Fatalf("err = %v, want the database's own failure", err)
	}
}

func TestTransitionsRefuseAnEmptyIdentity(t *testing.T) {
	mock := newNotificationMock(t)

	err := storage.NewPGXNotificationOutboxStore(mock).MarkDelivered(context.Background(), "", 1)
	if !errors.Is(err, storage.ErrInvalidNotificationTransition) {
		t.Fatalf("err = %v, want an invalid transition", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("an empty id still reached the database: %v", err)
	}
}

func TestClaimDuePassesTheBatchLeaseAndCeiling(t *testing.T) {
	mock := newNotificationMock(t)
	// The ORDER BY is asserted here as well as against a real database: it is
	// the difference between a fair queue and one where retries starve. It must
	// lead on the persisted availability instant, with occurred_at only as the
	// tie-break.
	mock.ExpectQuery(`ORDER BY o\.next_attempt_at, o\.occurred_at, o\.id`).
		WithArgs(7, 60.0, 5).
		WillReturnRows(notificationRows())

	events, err := storage.NewPGXNotificationOutboxStore(mock).
		ClaimDue(context.Background(), 7, 5, 60*time.Second)
	if err != nil {
		t.Fatalf("ClaimDue: %v", err)
	}
	if len(events) != 1 || events[0].ID != "n1" {
		t.Fatalf("claimed %+v, want the seeded event", events)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestClaimDueRefusesADegenerateRequest(t *testing.T) {
	mock := newNotificationMock(t)
	store := storage.NewPGXNotificationOutboxStore(mock)

	for _, args := range []struct{ batch, attempts int }{{0, 5}, {5, 0}, {-1, -1}} {
		events, err := store.ClaimDue(context.Background(), args.batch, args.attempts, time.Minute)
		if err != nil || events != nil {
			t.Fatalf("ClaimDue(%d, %d) = (%v, %v), want (nil, nil)", args.batch, args.attempts, events, err)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("a query was issued for an empty request: %v", err)
	}
}

func TestClaimDuePropagatesAQueryFailure(t *testing.T) {
	mock := newNotificationMock(t)
	mock.ExpectQuery(`FOR UPDATE SKIP LOCKED`).
		WithArgs(5, 60.0, 5).
		WillReturnError(errors.New("deadlock detected"))

	if _, err := storage.NewPGXNotificationOutboxStore(mock).
		ClaimDue(context.Background(), 5, 5, time.Minute); err == nil {
		t.Fatal("expected an error")
	}
}

func TestFailExhaustedReportsHowManyWereRetired(t *testing.T) {
	mock := newNotificationMock(t)
	mock.ExpectExec(`UPDATE chat\.notification_outbox`).
		WithArgs(5).
		WillReturnResult(pgxmock.NewResult("UPDATE", 3))

	retired, err := storage.NewPGXNotificationOutboxStore(mock).FailExhausted(context.Background(), 5)
	if err != nil {
		t.Fatalf("FailExhausted: %v", err)
	}
	if retired != 3 {
		t.Fatalf("retired = %d, want 3", retired)
	}
}

func TestFailExhaustedRefusesANonPositiveCeiling(t *testing.T) {
	mock := newNotificationMock(t)

	retired, err := storage.NewPGXNotificationOutboxStore(mock).FailExhausted(context.Background(), 0)
	if retired != 0 || err != nil {
		t.Fatalf("FailExhausted(0) = (%d, %v), want (0, nil)", retired, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("a statement ran for a ceiling of zero: %v", err)
	}
}

func TestFailExhaustedPropagatesAFailure(t *testing.T) {
	mock := newNotificationMock(t)
	mock.ExpectExec(`UPDATE chat\.notification_outbox`).
		WithArgs(5).
		WillReturnError(errors.New("connection reset"))

	if _, err := storage.NewPGXNotificationOutboxStore(mock).
		FailExhausted(context.Background(), 5); err == nil {
		t.Fatal("expected an error")
	}
}

func TestBacklogCountsTheNonTerminalStates(t *testing.T) {
	mock := newNotificationMock(t)
	mock.ExpectQuery(`SELECT count\(\*\)`).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(42))

	backlog, err := storage.NewPGXNotificationOutboxStore(mock).Backlog(context.Background())
	if err != nil {
		t.Fatalf("Backlog: %v", err)
	}
	if backlog != 42 {
		t.Fatalf("backlog = %d, want 42", backlog)
	}
}

func TestBacklogPropagatesAFailure(t *testing.T) {
	mock := newNotificationMock(t)
	mock.ExpectQuery(`SELECT count\(\*\)`).
		WillReturnError(errors.New("connection reset"))

	if _, err := storage.NewPGXNotificationOutboxStore(mock).Backlog(context.Background()); err == nil {
		t.Fatal("expected an error")
	}
}
