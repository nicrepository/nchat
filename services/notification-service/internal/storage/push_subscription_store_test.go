package storage_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	pgxmock "github.com/pashagolub/pgxmock/v2"

	"github.com/nicrepository/nchat/services/notification-service/internal/domain"
	"github.com/nicrepository/nchat/services/notification-service/internal/storage"
)

// Issue #745: the push subscription store's contract at the pgx boundary.
//
// What is provable here is the shape — which statement runs, with which
// arguments, and how each result becomes an error a caller can act on. That the
// SQL *means* what it says — that the unique indexes decide identity and
// ownership, that concurrent registrations cannot duplicate, that a lifecycle
// CHECK holds — is the database's behaviour and is proved against a real one in
// push_subscription_postgres_test.go.

const (
	testWorkspace = "00000000-0000-0000-0000-0000000000ws"
	testUser      = "00000000-0000-0000-0000-0000000000u1"
	testDevice    = "device-1"
	testEndpoint  = "https://push.example.com/subscription/abc123"
	// Structurally shaped like the encoded keys, without being one: the store
	// never inspects them, it only passes them through.
	testP256dh = "BFAKEp256dhFAKEp256dhFAKEp256dhFAKEp256dhFAKEp256dhFAKEp256dhFAKEp256dhFAKEp256dhFAKEpp"
	testAuth   = "FAKEauthFAKEauthFAKEau"
)

func newPushMock(t *testing.T) pgxmock.PgxPoolIface {
	t.Helper()
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	t.Cleanup(mock.Close)
	return mock
}

func testPrincipal() domain.Principal {
	return domain.Principal{UserID: testUser, WorkspaceID: testWorkspace}
}

func testRegistration() domain.Registration {
	return domain.Registration{
		DeviceID: testDevice, Endpoint: testEndpoint, P256dh: testP256dh, Auth: testAuth,
	}
}

// pushRows is the reconcile projection. Exactly five columns: adding a sixth
// here without adding it to the query is what this shape refuses to let happen
// silently.
func pushColumns() []string {
	return []string{"id", "device_id", "generation", "status", "created_at", "last_seen_at"}
}

func pushRows(status string) *pgxmock.Rows {
	now := time.Now()
	return pgxmock.NewRows(pushColumns()).
		AddRow("sub-1", testDevice, int64(4), status, now, now)
}

func TestUpsertRegistersWithTheServerDerivedPrincipal(t *testing.T) {
	mock := newPushMock(t)
	mock.ExpectQuery(`INSERT INTO chat\.push_subscriptions`).
		WithArgs(testWorkspace, testUser, testDevice, testEndpoint, testP256dh, testAuth).
		WillReturnRows(pushRows("active"))

	subscription, err := storage.NewPGXPushSubscriptionStore(mock).
		Upsert(context.Background(), testPrincipal(), testRegistration())
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if subscription.ID != "sub-1" || subscription.Status != domain.StatusActive {
		t.Fatalf("Upsert returned %+v", subscription)
	}
	// The generation comes back with the subscription because a delivery attempt
	// has to capture it: without it there is no way to attribute an answer to
	// the endpoint the attempt was made against.
	if subscription.Generation != 4 {
		t.Fatalf("Generation = %d, want 4", subscription.Generation)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// The endpoint index is an ownership guard, not a conflict to resolve. A
// registration that collides with somebody else's endpoint has to come back as
// a refusal the handler can turn into a 409 — never as an update that rewrites
// user_id, which is the takeover the design exists to prevent.
func TestUpsertReportsAnEndpointAlreadyOwned(t *testing.T) {
	mock := newPushMock(t)
	mock.ExpectQuery(`INSERT INTO chat\.push_subscriptions`).
		WithArgs(anyArgs(6)...).
		WillReturnError(&pgconn.PgError{
			Code:           "23505",
			ConstraintName: "push_subscriptions_endpoint_unique",
		})

	_, err := storage.NewPGXPushSubscriptionStore(mock).
		Upsert(context.Background(), testPrincipal(), testRegistration())
	if !errors.Is(err, domain.ErrEndpointConflict) {
		t.Fatalf("Upsert = %v, want ErrEndpointConflict", err)
	}
}

// Any other unique violation means the statement and the schema disagree, which
// is a defect. Reporting it as the endpoint conflict would tell a client their
// endpoint was taken when it was not.
func TestUpsertDoesNotMistakeOtherViolationsForAnEndpointConflict(t *testing.T) {
	mock := newPushMock(t)
	mock.ExpectQuery(`INSERT INTO chat\.push_subscriptions`).
		WithArgs(anyArgs(6)...).
		WillReturnError(&pgconn.PgError{Code: "23505", ConstraintName: "some_other_unique"})

	_, err := storage.NewPGXPushSubscriptionStore(mock).
		Upsert(context.Background(), testPrincipal(), testRegistration())
	if err == nil || errors.Is(err, domain.ErrEndpointConflict) {
		t.Fatalf("Upsert = %v, want a plain failure", err)
	}
}

// The upsert projects the whole generation rule, so the statement has to carry
// it. A build whose SET clauses lost the rotation branch would still compile,
// still return a row, and silently let a replaced endpoint inherit the previous
// one's success history.
func TestUpsertStatementDecidesTheGeneration(t *testing.T) {
	fragments := []string{
		`existing\.endpoint IS DISTINCT FROM EXCLUDED\.endpoint`,
		`existing\.p256dh IS DISTINCT FROM EXCLUDED\.p256dh`,
		`existing\.auth IS DISTINCT FROM EXCLUDED\.auth`,
		`existing\.status <> 'active'`,
		`generation = existing\.generation`,
		`failure_count = CASE WHEN`,
		`last_success_at = CASE WHEN`,
	}
	for _, fragment := range fragments {
		mock := newPushMock(t)
		mock.ExpectQuery(fragment).
			WithArgs(anyArgs(6)...).
			WillReturnRows(pushRows("active"))
		if _, err := storage.NewPGXPushSubscriptionStore(mock).
			Upsert(context.Background(), testPrincipal(), testRegistration()); err != nil {
			t.Fatalf("upsert statement is missing %q: %v", fragment, err)
		}
	}
}

func TestListIsScopedToTheWorkspaceAndUser(t *testing.T) {
	mock := newPushMock(t)
	mock.ExpectQuery(`FROM chat\.push_subscriptions`).
		WithArgs(testWorkspace, testUser).
		WillReturnRows(pushRows("invalid"))

	subscriptions, err := storage.NewPGXPushSubscriptionStore(mock).
		List(context.Background(), testPrincipal())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	// Retired rows are returned on purpose: a client that cannot see its device
	// was invalidated has no way to know it must register again.
	if len(subscriptions) != 1 || subscriptions[0].Status != domain.StatusInvalid {
		t.Fatalf("List = %+v", subscriptions)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestListReturnsAnEmptySliceRatherThanNil(t *testing.T) {
	mock := newPushMock(t)
	mock.ExpectQuery(`FROM chat\.push_subscriptions`).
		WithArgs(anyArgs(2)...).
		WillReturnRows(pgxmock.NewRows(pushColumns()))

	subscriptions, err := storage.NewPGXPushSubscriptionStore(mock).
		List(context.Background(), testPrincipal())
	if err != nil || subscriptions == nil || len(subscriptions) != 0 {
		t.Fatalf("List = %v, %v", subscriptions, err)
	}
}

func TestListReportsAQueryFailure(t *testing.T) {
	mock := newPushMock(t)
	mock.ExpectQuery(`FROM chat\.push_subscriptions`).
		WithArgs(anyArgs(2)...).
		WillReturnError(errors.New("boom"))

	if _, err := storage.NewPGXPushSubscriptionStore(mock).
		List(context.Background(), testPrincipal()); err == nil {
		t.Fatal("List = nil, want an error")
	}
}

func TestListReportsAScanFailure(t *testing.T) {
	mock := newPushMock(t)
	mock.ExpectQuery(`FROM chat\.push_subscriptions`).
		WithArgs(anyArgs(2)...).
		WillReturnRows(pgxmock.NewRows(pushColumns()).
			AddRow("sub-1", testDevice, int64(1), "active", "not-a-time", time.Now()))

	if _, err := storage.NewPGXPushSubscriptionStore(mock).
		List(context.Background(), testPrincipal()); err == nil {
		t.Fatal("List = nil, want a scan error")
	}
}

// Ownership is part of the write, not a check that precedes it: there is no
// window in which the row could change hands between the two.
func TestDisableCarriesTheOwnershipPredicate(t *testing.T) {
	mock := newPushMock(t)
	mock.ExpectExec(`UPDATE chat\.push_subscriptions`).
		WithArgs("sub-1", testWorkspace, testUser).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	if err := storage.NewPGXPushSubscriptionStore(mock).
		Disable(context.Background(), testPrincipal(), "sub-1"); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// Somebody else's identifier affects no rows, and that is reported as "not
// found" rather than as a distinct answer. Any other outcome would let one user
// discover which subscription identifiers exist.
func TestDisableReportsNotFoundWhenNothingIsOwned(t *testing.T) {
	mock := newPushMock(t)
	mock.ExpectExec(`UPDATE chat\.push_subscriptions`).
		WithArgs(anyArgs(3)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))

	err := storage.NewPGXPushSubscriptionStore(mock).
		Disable(context.Background(), testPrincipal(), "sub-1")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Disable = %v, want ErrNotFound", err)
	}
}

func TestDisableReportsAnExecFailure(t *testing.T) {
	mock := newPushMock(t)
	mock.ExpectExec(`UPDATE chat\.push_subscriptions`).
		WithArgs(anyArgs(3)...).
		WillReturnError(errors.New("boom"))

	err := storage.NewPGXPushSubscriptionStore(mock).
		Disable(context.Background(), testPrincipal(), "sub-1")
	if err == nil || errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Disable = %v, want a plain failure", err)
	}
}

// One statement per outcome, and each one has to be the right one. A success
// that ran the failure statement, or a transient failure that ran the
// invalidation, would be the difference between a working subscription and a
// person who stops being notified. Every one of them carries the generation the
// attempt was made against.
func TestRecordDeliveryRunsTheStatementTheOutcomeAuthorises(t *testing.T) {
	cases := map[string]struct {
		result domain.DeliveryResult
		expect string
		args   []any
	}{
		"success": {
			result: domain.DeliveryResult{Outcome: domain.OutcomeSucceeded},
			expect: "last_success_at = now()",
			args:   []any{"sub-1", int64(4)},
		},
		"transient": {
			result: domain.DeliveryResult{Outcome: domain.OutcomeTransient},
			expect: "failure_count = LEAST",
			args:   []any{"sub-1", int64(4)},
		},
		"invalidated": {
			result: domain.DeliveryResult{
				Outcome: domain.OutcomeInvalidated, Reason: domain.ReasonGone,
			},
			expect: "status = 'invalid'",
			args:   []any{"sub-1", int64(4), string(domain.ReasonGone)},
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			mock := newPushMock(t)
			mock.ExpectExec(regexpQuote(testCase.expect)).
				WithArgs(testCase.args...).
				WillReturnResult(pgxmock.NewResult("UPDATE", 1))

			applied, err := storage.NewPGXPushSubscriptionStore(mock).
				RecordDelivery(context.Background(), "sub-1", 4, testCase.result)
			if err != nil || !applied {
				t.Fatalf("RecordDelivery = %v, %v", applied, err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet expectations: %v", err)
			}
		})
	}
}

// Every delivery statement is a compare-and-set on the generation, and losing
// that predicate from any one of them is what would let a late answer reach the
// endpoint that replaced the one it describes.
func TestEveryDeliveryStatementComparesTheGeneration(t *testing.T) {
	results := map[string]domain.DeliveryResult{
		"success":     {Outcome: domain.OutcomeSucceeded},
		"transient":   {Outcome: domain.OutcomeTransient},
		"invalidated": {Outcome: domain.OutcomeInvalidated, Reason: domain.ReasonGone},
	}
	for name, result := range results {
		t.Run(name, func(t *testing.T) {
			mock := newPushMock(t)
			mock.ExpectExec(`generation = \$2::bigint AND status = 'active'`).
				WithArgs(anyArgs(len(deliveryArgs(result)))...).
				WillReturnResult(pgxmock.NewResult("UPDATE", 1))

			if _, err := storage.NewPGXPushSubscriptionStore(mock).
				RecordDelivery(context.Background(), "sub-1", 4, result); err != nil {
				t.Fatalf("statement does not compare the generation: %v", err)
			}
		})
	}
}

// deliveryArgs is how many arguments one outcome's statement takes: the
// invalidation carries a reason, the other two do not.
func deliveryArgs(result domain.DeliveryResult) []any {
	if result.Outcome == domain.OutcomeInvalidated {
		return []any{nil, nil, nil}
	}
	return []any{nil, nil}
}

// An outcome value this build does not know must fall to the transient
// statement. That is the direction that keeps a real person subscribed: only
// the two provider verdicts that mean "gone" may reach the retiring statement.
func TestRecordDeliveryTreatsAnUnknownOutcomeAsTransient(t *testing.T) {
	mock := newPushMock(t)
	mock.ExpectExec(regexpQuote("failure_count = LEAST")).
		WithArgs(anyArgs(2)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	if _, err := storage.NewPGXPushSubscriptionStore(mock).RecordDelivery(
		context.Background(), "sub-1", 4, domain.DeliveryResult{Outcome: domain.Outcome(99)},
	); err != nil {
		t.Fatalf("RecordDelivery: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// A result that matched nothing is an ordinary outcome and not an error: the
// subscription rotated, was disabled, or is already retired. Reporting it as an
// error would put a log line on every late answer in a system where a send and
// its answer are never simultaneous.
func TestRecordDeliveryReportsAStaleResultAsNotApplied(t *testing.T) {
	mock := newPushMock(t)
	mock.ExpectExec(`UPDATE chat\.push_subscriptions`).
		WithArgs(anyArgs(2)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))

	applied, err := storage.NewPGXPushSubscriptionStore(mock).RecordDelivery(
		context.Background(), "sub-1", 4, domain.DeliveryResult{Outcome: domain.OutcomeSucceeded})
	if err != nil {
		t.Fatalf("RecordDelivery: %v", err)
	}
	if applied {
		t.Fatal("a statement that matched no row reported applied")
	}
}

// A database that could not answer is the one thing that is an error here.
func TestRecordDeliveryReportsAnExecFailure(t *testing.T) {
	mock := newPushMock(t)
	mock.ExpectExec(`UPDATE chat\.push_subscriptions`).
		WithArgs(anyArgs(2)...).
		WillReturnError(errors.New("boom"))

	applied, err := storage.NewPGXPushSubscriptionStore(mock).RecordDelivery(
		context.Background(), "sub-1", 4, domain.DeliveryResult{Outcome: domain.OutcomeSucceeded})
	if err == nil || applied {
		t.Fatalf("RecordDelivery = %v, %v, want a failure", applied, err)
	}
}

// anyArgs matches n arguments whose values are asserted elsewhere; these tests
// are about which statement runs and what its result becomes.
func anyArgs(count int) []any {
	args := make([]any, count)
	for i := range args {
		args[i] = pgxmock.AnyArg()
	}
	return args
}

// regexpQuote escapes a literal fragment for pgxmock's regexp matching.
func regexpQuote(fragment string) string {
	replacer := strings.NewReplacer("(", `\(`, ")", `\)`, ".", `\.`, "+", `\+`, "*", `\*`)
	return replacer.Replace(fragment)
}
