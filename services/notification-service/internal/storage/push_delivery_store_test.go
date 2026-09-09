package storage_test

import (
	"context"
	"errors"
	"regexp"
	"testing"

	pgxmock "github.com/pashagolub/pgxmock/v2"

	"github.com/nicrepository/nchat/services/notification-service/internal/domain"
	"github.com/nicrepository/nchat/services/notification-service/internal/storage"
)

// Issue #746: the delivery store's contract at the pgx boundary.
//
// What is provable here is the shape — which statement runs, with which
// arguments, and how a result becomes something a caller can act on. That the
// SQL *means* what it says — that the primary key deduplicates under
// concurrency, that the fan-out excludes what the ledger holds, that both
// foreign keys cascade — is the database's behaviour and is proved against a
// real one in push_delivery_postgres_test.go.

const testNotificationID = "00000000-0000-0000-0000-0000000000n1"

func deliveryColumns() []string {
	return []string{"id", "generation", "endpoint", "p256dh", "auth"}
}

func deliveryRows(ids ...string) *pgxmock.Rows {
	rows := pgxmock.NewRows(deliveryColumns())
	for i, id := range ids {
		rows.AddRow(id, int64(i+1),
			"https://push.example.com/subscription/"+id, testP256dh, testAuth)
	}
	return rows
}

// The fan-out is scoped by workspace and recipient together, and both come from
// the outbox row rather than from anything a client sent. That scoping is the
// whole authorisation: there is no addressable form of this query that reaches
// another person's browsers.
func TestListDeliverableIsScopedToTheEventsOwnRecipient(t *testing.T) {
	mock := newPushMock(t)
	mock.ExpectQuery(regexp.QuoteMeta("FROM chat.push_subscriptions s")).
		WithArgs(testNotificationID, testWorkspace, testUser).
		WillReturnRows(deliveryRows("sub-a", "sub-b"))

	targets, err := storage.NewPGXPushDeliveryStore(mock).
		ListDeliverable(context.Background(), testNotificationID, testWorkspace, testUser)
	if err != nil {
		t.Fatalf("ListDeliverable: %v", err)
	}
	if len(targets) != 2 {
		t.Fatalf("got %d targets, want 2", len(targets))
	}
	if targets[0].SubscriptionID != "sub-a" || targets[0].Generation != 1 {
		t.Fatalf("first target = %+v, want sub-a at generation 1", targets[0])
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// Only active subscriptions, and only those the notification has not reached.
// Both conditions are in the statement rather than filtered afterwards, so a
// retired endpoint is never read into memory in the first place.
func TestTheFanOutFiltersInTheStatement(t *testing.T) {
	mock := newPushMock(t)
	// Both conditions, in the statement, in order. A store that read every
	// subscription and filtered in Go would not match this.
	mock.ExpectQuery(`(?s)s\.status = 'active'.*NOT EXISTS.*notification_push_deliveries`).
		WithArgs(testNotificationID, testWorkspace, testUser).
		WillReturnRows(deliveryRows())

	if _, err := storage.NewPGXPushDeliveryStore(mock).
		ListDeliverable(context.Background(), testNotificationID, testWorkspace, testUser); err != nil {
		t.Fatalf("ListDeliverable: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// An empty answer is an empty slice, never nil. A caller that ranged over nil
// would behave identically, but one that checked `== nil` to mean "the query
// failed" would not.
func TestListDeliverableReturnsAnEmptySliceWhenThereAreNoBrowsers(t *testing.T) {
	mock := newPushMock(t)
	mock.ExpectQuery(regexp.QuoteMeta("FROM chat.push_subscriptions s")).
		WithArgs(testNotificationID, testWorkspace, testUser).
		WillReturnRows(deliveryRows())

	targets, err := storage.NewPGXPushDeliveryStore(mock).
		ListDeliverable(context.Background(), testNotificationID, testWorkspace, testUser)
	if err != nil {
		t.Fatalf("ListDeliverable: %v", err)
	}
	if targets == nil {
		t.Fatal("an empty result was returned as nil")
	}
	if len(targets) != 0 {
		t.Fatalf("got %d targets, want none", len(targets))
	}
}

func TestListDeliverableReportsAQueryFailure(t *testing.T) {
	mock := newPushMock(t)
	mock.ExpectQuery(regexp.QuoteMeta("FROM chat.push_subscriptions s")).
		WithArgs(testNotificationID, testWorkspace, testUser).
		WillReturnError(errors.New("connection reset"))

	if _, err := storage.NewPGXPushDeliveryStore(mock).
		ListDeliverable(context.Background(), testNotificationID, testWorkspace, testUser); err == nil {
		t.Fatal("a failed query was reported as success")
	}
}

func TestListDeliverableReportsAMalformedRow(t *testing.T) {
	mock := newPushMock(t)
	mock.ExpectQuery(regexp.QuoteMeta("FROM chat.push_subscriptions s")).
		WithArgs(testNotificationID, testWorkspace, testUser).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow("sub-a"))

	if _, err := storage.NewPGXPushDeliveryStore(mock).
		ListDeliverable(context.Background(), testNotificationID, testWorkspace, testUser); err == nil {
		t.Fatal("a row that does not match the projection was accepted")
	}
}

// The ledger write is an upsert that does nothing on conflict. That is the
// deduplication and the concurrency control at once: a replay writes nothing,
// and two workers racing on one claim cannot produce two rows.
func TestMarkDeliveredIsAnIdempotentInsert(t *testing.T) {
	mock := newPushMock(t)
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO chat.notification_push_deliveries")).
		WithArgs(testNotificationID, "sub-a", int64(4)).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	if err := storage.NewPGXPushDeliveryStore(mock).
		MarkDelivered(context.Background(), testNotificationID, "sub-a", 4); err != nil {
		t.Fatalf("MarkDelivered: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A conflict affects no rows, and that is a success: the endpoint was already
// recorded, which is precisely the outcome the call exists to produce.
func TestMarkDeliveredTreatsAConflictAsSuccess(t *testing.T) {
	mock := newPushMock(t)
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO chat.notification_push_deliveries")).
		WithArgs(testNotificationID, "sub-a", int64(1)).
		WillReturnResult(pgxmock.NewResult("INSERT", 0))

	if err := storage.NewPGXPushDeliveryStore(mock).
		MarkDelivered(context.Background(), testNotificationID, "sub-a", 1); err != nil {
		t.Fatalf("a conflicting insert was reported as an error: %v", err)
	}
}

func TestMarkDeliveredReportsADatabaseFailure(t *testing.T) {
	mock := newPushMock(t)
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO chat.notification_push_deliveries")).
		WithArgs(testNotificationID, "sub-a", int64(1)).
		WillReturnError(errors.New("disk full"))

	if err := storage.NewPGXPushDeliveryStore(mock).
		MarkDelivered(context.Background(), testNotificationID, "sub-a", 1); err == nil {
		t.Fatal("a failed insert was reported as success")
	}
}

// The lifecycle write is issue #745's statement, reached through this store
// rather than reimplemented in it. Each outcome runs the statement it
// authorises, and the compare-and-set on (id, generation) is intact.
func TestRecordDeliveryDelegatesToTheSubscriptionLifecycle(t *testing.T) {
	cases := map[string]struct {
		result    domain.DeliveryResult
		statement string
		args      []any
	}{
		"success": {
			result:    domain.DeliveryResult{Outcome: domain.OutcomeSucceeded},
			statement: "SET last_success_at = now()",
			args:      []any{"sub-a", int64(4)},
		},
		"transient": {
			result:    domain.DeliveryResult{Outcome: domain.OutcomeTransient},
			statement: "SET failure_count = LEAST(failure_count + 1, 2147483647)",
			args:      []any{"sub-a", int64(4)},
		},
		"invalidated": {
			result: domain.DeliveryResult{
				Outcome: domain.OutcomeInvalidated, Reason: domain.ReasonGone,
			},
			statement: "SET status = 'invalid'",
			args:      []any{"sub-a", int64(4), "gone"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			mock := newPushMock(t)
			mock.ExpectQuery(regexp.QuoteMeta(tc.statement)).
				WithArgs(tc.args...).
				WillReturnRows(pgxmock.NewRows([]string{"applied", "status"}).
					AddRow(true, string(domain.StatusActive)))

			application, err := storage.NewPGXPushDeliveryStore(mock).
				RecordDelivery(context.Background(), "sub-a", 4, tc.result)
			if err != nil {
				t.Fatalf("RecordDelivery: %v", err)
			}
			if application != domain.ApplicationRecorded {
				t.Fatal("a matching row reported that nothing applied")
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// A result that matched no row is classified, not merely refused, and never an
// error. The delivery layer routes on that classification: a rotated
// subscription still has a live endpoint, and the other cases do not.
func TestRecordDeliveryClassifiesALostCompareAndSet(t *testing.T) {
	mock := newPushMock(t)
	mock.ExpectQuery(regexp.QuoteMeta("SET last_success_at = now()")).
		WithArgs("sub-a", int64(4)).
		WillReturnRows(pgxmock.NewRows([]string{"applied", "status"}).
			AddRow(false, string(domain.StatusActive)))

	application, err := storage.NewPGXPushDeliveryStore(mock).
		RecordDelivery(context.Background(), "sub-a", 4,
			domain.DeliveryResult{Outcome: domain.OutcomeSucceeded})
	if err != nil {
		t.Fatalf("RecordDelivery: %v", err)
	}
	if application == domain.ApplicationRecorded {
		t.Fatal("a statement that matched nothing reported that it applied")
	}
	if !application.Deliverable() {
		t.Fatal("a rotated subscription was not reported as still deliverable")
	}
}

// The store satisfies the contract the delivery layer is written against.
var _ storage.PushDeliveryStore = (*storage.PGXPushDeliveryStore)(nil)
