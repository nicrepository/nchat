package storage_test

import (
	"context"
	"errors"
	"strconv"
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
	return mutedNotificationRows(false)
}

// mutedNotificationRows is the same row with the mute the projection resolved
// (issue #744). A parameter rather than a second literal, so the column can
// never be present in one helper and forgotten in the other.
func mutedNotificationRows(muted bool) *pgxmock.Rows {
	return preferenceNotificationRows(muted, "all")
}

// notificationColumnNames is the projection's column list, in order, declared
// once so a column added to the store cannot be added to one row helper and
// forgotten in another (issue #136 added notification_level to it, #870 the presentation).
func notificationColumnNames() []string {
	return []string{
		"id", "workspace_id", "recipient_user_id", "kind", "priority",
		"source_type", "message_id", "origin", "dedupe_key", "attempts", "occurred_at",
		"muted", "notification_level", "presentation",
	}
}

// preferenceNotificationRows is one row carrying both halves of the recipient's
// conversation preference, which is what the projection resolves and the policy
// engine reads (issues #744 and #136).
func preferenceNotificationRows(muted bool, level string) *pgxmock.Rows {
	return pgxmock.NewRows(notificationColumnNames()).
		AddRow("n1", "ws-1", "user-1", "mention", "high",
			"message", "msg-1", "live", "message:msg-1:mention", 2, time.Now(), muted, level,
			// NULL: the presentation is absent unless a test is about it, which
			// is also what the projection returns for every notification whose
			// message the recipient may not see (issue #870).
			nil)
}

// The mute the policy engine reads has to survive the projection, and it is the
// one field whose loss is invisible: a dropped column would scan as false,
// which is indistinguishable from a recipient who never muted anything, and
// every muted conversation would quietly start alerting again.
func TestListPendingProjectsTheRecipientsMute(t *testing.T) {
	for _, muted := range []bool{true, false} {
		mock := newNotificationMock(t)
		mock.ExpectQuery(`FROM chat\.notification_outbox`).
			WithArgs(10).
			WillReturnRows(mutedNotificationRows(muted))

		events, err := storage.NewPGXNotificationOutboxStore(mock, false).ListPending(context.Background(), 10)
		if err != nil {
			t.Fatalf("ListPending: %v", err)
		}
		if len(events) != 1 || events[0].Muted != muted {
			t.Fatalf("Muted = %v, want %v", events, muted)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet expectations: %v", err)
		}
	}
}

// The level travels with the mute, from the same row and the same statement
// (issue #136).
//
// Its loss is as invisible as the mute's and worse in the other direction: a
// dropped column scans as the empty string, which the engine normalises to "all
// messages", so every recipient who asked to hear only about mentions would
// quietly start being alerted for everything again.
func TestListPendingProjectsTheConversationLevel(t *testing.T) {
	for _, level := range []string{"all", "mentions_replies"} {
		mock := newNotificationMock(t)
		mock.ExpectQuery(`FROM chat\.notification_outbox`).
			WithArgs(10).
			WillReturnRows(preferenceNotificationRows(false, level))

		events, err := storage.NewPGXNotificationOutboxStore(mock, false).ListPending(context.Background(), 10)
		if err != nil {
			t.Fatalf("ListPending: %v", err)
		}
		if len(events) != 1 || events[0].NotificationLevel != level {
			t.Fatalf("NotificationLevel = %+v, want %q", events, level)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet expectations: %v", err)
		}
	}
}

// The mute is the timestamp and no longer the existence of the row (issue #136).
//
// Asserted against the statement text because that is the whole of the change:
// a row with a NULL muted_at is somebody who asked to keep hearing about
// mentions, and reading its presence as a mute would silence every one of them.
func TestMuteProjectionTestsTheTimestampAndNotTheRow(t *testing.T) {
	mock := newNotificationMock(t)
	mock.ExpectQuery(`p\.muted_at IS NOT NULL`).WithArgs(10).WillReturnRows(notificationRows())
	if _, err := storage.NewPGXNotificationOutboxStore(mock, false).ListPending(context.Background(), 10); err != nil {
		t.Fatalf("ListPending: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// A recipient with no preference row at all must read as the product default,
// which is what the COALESCE in the level projection is for: without it the
// column would come back NULL and fail the scan for every unconfigured
// recipient — which is almost all of them.
func TestLevelProjectionDefaultsToAll(t *testing.T) {
	mock := newNotificationMock(t)
	mock.ExpectQuery(`COALESCE\(\(\s*SELECT p\.notification_level`).
		WithArgs(10).
		WillReturnRows(notificationRows())
	events, err := storage.NewPGXNotificationOutboxStore(mock, false).ListPending(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListPending: %v", err)
	}
	if len(events) != 1 || events[0].NotificationLevel != "all" {
		t.Fatalf("NotificationLevel = %+v, want the default", events)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// The three predicates that keep one person's preference out of another's
// notifications. They are asserted against the statement text because that is
// where they live: a projection that dropped any one of them would still return
// a boolean, and the mock cannot tell a correctly scoped one from a leak.
func TestMuteProjectionIsScopedToRecipientWorkspaceAndConversation(t *testing.T) {
	mock := newNotificationMock(t)
	for _, predicate := range []string{
		// The preference belongs to this recipient, never to another member of
		// the same conversation.
		`p\.user_id = o\.recipient_user_id`,
		// ...and to this tenant. The prefs table's foreign keys do not tie the
		// workspace to the target, so nothing but this predicate does.
		`p\.workspace_id = o\.workspace_id`,
		// ...and the conversation is resolved from a message of the same tenant.
		`m\.workspace_id = o\.workspace_id`,
		// Each target kind matches only a preference written for that kind.
		`p\.channel_id = m\.channel_id`,
		`p\.dm_conversation_id = m\.dm_conversation_id`,
	} {
		mock.ExpectQuery(predicate).WithArgs(10).WillReturnRows(notificationRows())
		if _, err := storage.NewPGXNotificationOutboxStore(mock, false).ListPending(context.Background(), 10); err != nil {
			t.Fatalf("ListPending (%s): %v", predicate, err)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// One statement per batch is the whole N+1 defence, and it is structural: the
// mute is resolved inside the read that was already happening, so a batch of
// many events issues exactly the one query a batch of one does.
func TestListPendingResolvesEveryMuteInOneQuery(t *testing.T) {
	mock := newNotificationMock(t)
	rows := pgxmock.NewRows(notificationColumnNames())
	const batch = 25
	for i := 0; i < batch; i++ {
		rows.AddRow("n"+strconv.Itoa(i), "ws-1", "user-1", "mention", "high",
			"message", "msg-1", "live", "", 1, time.Now(), i%2 == 0, "all", nil)
	}
	// Exactly one ExpectQuery is registered. pgxmock fails any further query,
	// so a per-event lookup could not pass this test.
	mock.ExpectQuery(`FROM chat\.notification_outbox`).WithArgs(batch).WillReturnRows(rows)

	events, err := storage.NewPGXNotificationOutboxStore(mock, false).ListPending(context.Background(), batch)
	if err != nil {
		t.Fatalf("ListPending: %v", err)
	}
	if len(events) != batch {
		t.Fatalf("got %d events, want %d", len(events), batch)
	}
	for i, event := range events {
		if event.Muted != (i%2 == 0) {
			t.Fatalf("event %d muted = %v, want %v", i, event.Muted, i%2 == 0)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestListPendingReadsTheEventContract(t *testing.T) {
	mock := newNotificationMock(t)
	mock.ExpectQuery(`FROM chat\.notification_outbox`).
		WithArgs(10).
		WillReturnRows(notificationRows())

	events, err := storage.NewPGXNotificationOutboxStore(mock, false).ListPending(context.Background(), 10)
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

// The policy reads origin, so a projection that dropped it would silently turn
// every event into one this build does not recognise — and refuse to alert for
// all of them.
func TestListPendingProjectsTheOrigin(t *testing.T) {
	mock := newNotificationMock(t)
	mock.ExpectQuery(`FROM chat\.notification_outbox`).
		WithArgs(10).
		WillReturnRows(notificationRows())

	events, err := storage.NewPGXNotificationOutboxStore(mock, false).ListPending(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListPending: %v", err)
	}
	if len(events) != 1 || events[0].Origin != "live" {
		t.Fatalf("origin = %+v, want the column the row carries", events)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// A non-positive limit is a caller asking for nothing; it must not become an
// unbounded query.
func TestListPendingRefusesANonPositiveLimit(t *testing.T) {
	mock := newNotificationMock(t)

	events, err := storage.NewPGXNotificationOutboxStore(mock, false).ListPending(context.Background(), 0)
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

	if _, err := storage.NewPGXNotificationOutboxStore(mock, false).ListPending(context.Background(), 5); err == nil {
		t.Fatal("expected an error")
	}
}

func TestListPendingReportsAScanFailure(t *testing.T) {
	mock := newNotificationMock(t)
	mock.ExpectQuery(`FROM chat\.notification_outbox`).
		WithArgs(5).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow("n1"))

	if _, err := storage.NewPGXNotificationOutboxStore(mock, false).ListPending(context.Background(), 5); err == nil {
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

			store := storage.NewPGXNotificationOutboxStore(mock, false)
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
			store := storage.NewPGXNotificationOutboxStore(mock, false)

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

			err := tc.call(storage.NewPGXNotificationOutboxStore(mock, false))
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

	err := storage.NewPGXNotificationOutboxStore(mock, false).MarkDelivered(context.Background(), "n1", 1)
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

	err := storage.NewPGXNotificationOutboxStore(mock, false).MarkFailed(context.Background(), "n1", 1, "delivery_transient")
	if err == nil || errors.Is(err, storage.ErrNotificationStateConflict) ||
		errors.Is(err, storage.ErrInvalidNotificationTransition) {
		t.Fatalf("err = %v, want the database's own failure", err)
	}
}

func TestTransitionsRefuseAnEmptyIdentity(t *testing.T) {
	mock := newNotificationMock(t)

	err := storage.NewPGXNotificationOutboxStore(mock, false).MarkDelivered(context.Background(), "", 1)
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
		WithArgs(7, 60.0, 5, false).
		WillReturnRows(notificationRows())

	events, err := storage.NewPGXNotificationOutboxStore(mock, false).
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
	store := storage.NewPGXNotificationOutboxStore(mock, false)

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
		WithArgs(5, 60.0, 5, false).
		WillReturnError(errors.New("deadlock detected"))

	if _, err := storage.NewPGXNotificationOutboxStore(mock, false).
		ClaimDue(context.Background(), 5, 5, time.Minute); err == nil {
		t.Fatal("expected an error")
	}
}

func TestFailExhaustedReportsHowManyWereRetired(t *testing.T) {
	mock := newNotificationMock(t)
	mock.ExpectExec(`UPDATE chat\.notification_outbox`).
		WithArgs(5).
		WillReturnResult(pgxmock.NewResult("UPDATE", 3))

	retired, err := storage.NewPGXNotificationOutboxStore(mock, false).FailExhausted(context.Background(), 5)
	if err != nil {
		t.Fatalf("FailExhausted: %v", err)
	}
	if retired != 3 {
		t.Fatalf("retired = %d, want 3", retired)
	}
}

func TestFailExhaustedRefusesANonPositiveCeiling(t *testing.T) {
	mock := newNotificationMock(t)

	retired, err := storage.NewPGXNotificationOutboxStore(mock, false).FailExhausted(context.Background(), 0)
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

	if _, err := storage.NewPGXNotificationOutboxStore(mock, false).
		FailExhausted(context.Background(), 5); err == nil {
		t.Fatal("expected an error")
	}
}

func TestBacklogCountsTheNonTerminalStates(t *testing.T) {
	mock := newNotificationMock(t)
	mock.ExpectQuery(`SELECT count\(\*\)`).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(42))

	backlog, err := storage.NewPGXNotificationOutboxStore(mock, false).Backlog(context.Background())
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

	if _, err := storage.NewPGXNotificationOutboxStore(mock, false).Backlog(context.Background()); err == nil {
		t.Fatal("expected an error")
	}
}

// The presentation the claim resolved survives the projection (issue #870).
//
// It is scanned from a jsonb value, so the failure this guards against is not a
// dropped column — that would be a scan error — but a decode that silently
// produced the zero value, which is indistinguishable from "the recipient may
// see nothing" and would turn every banner generic without failing anything.
func TestClaimDueDecodesTheApprovedPresentation(t *testing.T) {
	mock := newNotificationMock(t)
	rows := pgxmock.NewRows(notificationColumnNames()).
		AddRow("n1", "ws-1", "user-1", "mention", "high",
			"message", "msg-1", "live", "message:msg-1:mention", 2, time.Now(), false, "all",
			[]byte(`{"sender":"Ana Ribeiro","context":"#geral","body":"subiu o hotfix","attachment":true}`))
	mock.ExpectQuery(`FOR UPDATE SKIP LOCKED`).WithArgs(10, 60.0, 5, true).WillReturnRows(rows)

	events, err := storage.NewPGXNotificationOutboxStore(mock, true).ClaimDue(context.Background(), 10, 5, time.Minute)
	if err != nil {
		t.Fatalf("ClaimDue: %v", err)
	}
	want := storage.MessagePresentation{
		Sender: "Ana Ribeiro", Context: "#geral",
		Body: "subiu o hotfix", Attachment: true,
	}
	if len(events) != 1 || events[0].Presentation != want {
		t.Fatalf("Presentation = %+v, want %+v", events, want)
	}
}

// ListPending deliberately returns no presentation before policy evaluation.
func TestListPendingReadsAnAbsentPresentationAsNothing(t *testing.T) {
	mock := newNotificationMock(t)
	mock.ExpectQuery(`FROM chat\.notification_outbox`).
		WithArgs(10).WillReturnRows(notificationRows())

	events, err := storage.NewPGXNotificationOutboxStore(mock, false).ListPending(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListPending: %v", err)
	}
	if len(events) != 1 || events[0].Presentation != (storage.MessagePresentation{}) {
		t.Fatalf("Presentation = %+v, want nothing at all", events)
	}
}

// The projection scopes the presentation by the outbox row's own tenant and
// recipient, and refuses every state in which the message is not readable. The
// statement is asserted here; TestNotificationPresentationIsScopedPostgreSQL
// proves the behaviour against a real database.
func TestPresentationProjectionIsScopedAndGuarded(t *testing.T) {
	for name, predicate := range map[string]string{
		"the tenant":            `m\.workspace_id = o\.workspace_id`,
		"the message":           `m\.id = o\.message_id`,
		"a deleted message":     `m\.deleted_at IS NULL`,
		"a withheld message":    `m\.status = 'active'`,
		"a condemned message":   `m\.link_safety_state <> 'malicious'`,
		"a system message":      `m\.kind = 'user'`,
		"channel visibility":    `chat\.channel_visible_to_user\(m\.channel_id, o\.recipient_user_id\)`,
		"conversation membersh": `dm\.user_id = o\.recipient_user_id`,
		// The four status predicates that align this projection with
		// chat-service's own ListChannelMessages and ListDMMessages. They are
		// the difference between "the recipient was told about this once" and
		// "the recipient may read this now", and each one is a state a
		// workspace, a membership or a target can enter after the outbox row
		// was written.
		"a disabled workspace":     `w\.status = 'active'`,
		"a revoked membership":     `wm\.status = 'active'`,
		"an archived channel":      `c\.status = 'active'`,
		"an archived conversation": `d\.status = 'active'`,
		// SR-001. The recipient's *global account*, which is a different fact
		// from their membership of this workspace: an operator suspending
		// somebody revokes their sessions and leaves the membership standing.
		// Both patterns name the recipient_user alias explicitly, so neither can
		// be satisfied by the sender's auth.users join — which is what the
		// projection already had, and what made the gap invisible.
		"a globally suspended recipient": `recipient_user\.id = o\.recipient_user_id\s+AND recipient_user\.status = 'active'`,
		"a soft-deleted recipient":       `recipient_user\.deleted_at IS NULL`,
	} {
		t.Run(name, func(t *testing.T) {
			mock := newNotificationMock(t)
			mock.ExpectQuery(predicate).WithArgs(10, 60.0, 5, true).WillReturnRows(notificationRows())

			if _, err := storage.NewPGXNotificationOutboxStore(mock, true).
				ClaimDue(context.Background(), 10, 5, time.Minute); err != nil {
				t.Fatalf("the projection does not carry %s: %v", name, err)
			}
		})
	}
}
