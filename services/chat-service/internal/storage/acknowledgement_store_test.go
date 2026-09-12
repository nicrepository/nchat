package storage_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	pgxmock "github.com/pashagolub/pgxmock/v2"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// Issue #824, the shape of the two statements. What a mock can prove here is
// the contract between Go and SQL: how many statements run, in what order, what
// they are bound with, and what the store does with what comes back. What it
// cannot prove — that a conditional UPDATE actually serialises two concurrent
// acknowledgements, and that a primary key actually refuses a second row — is
// proved against a real database in message_acknowledgement_postgres_test.go,
// because a mock would simply agree with whatever it was handed.

const (
	ackStoreWorkspace = "11111111-1111-4111-8111-111111111111"
	ackStoreMessage   = "22222222-2222-4222-8222-222222222222"
	ackStoreViewer    = "33333333-3333-4333-8333-333333333333"
	ackStorePeer      = "44444444-4444-4444-8444-444444444444"
	ackStoreChannel   = "55555555-5555-4555-8555-555555555555"
)

// summaryCols is the single-row projection both endpoints answer from.
func summaryCols() []string {
	return []string{
		"acknowledgement_required", "is_sender",
		"channel_id", "dm_conversation_id",
		"total", "pending", "acknowledged", "responded", "expired", "cancelled",
		"viewer_state",
	}
}

func summaryRow(required, isSender bool, total, pending, acknowledged int, viewerState string) []any {
	// The route is a channel here; the DM branch is covered against a real
	// database, where the two columns are what the schema actually holds.
	return []any{
		required, isSender, ackStoreChannel, "",
		total, pending, acknowledged, 0, 0, 0, viewerState,
	}
}

// expectSummary registers the projection a write reports from.
func expectSummary(mock pgxmock.PgxPoolIface, row []any) {
	mock.ExpectQuery(`(?s)WITH authorized AS.*FROM chat\.messages m.*chat\.message_acknowledgements`).
		WithArgs(ackStoreWorkspace, ackStoreViewer, ackStoreMessage).
		WillReturnRows(pgxmock.NewRows(summaryCols()).AddRow(row...))
}

// readCols is the read's wider shape: the summary repeated on every row, plus
// the one recipient that row carries.
func readCols() []string {
	return append(summaryCols(), "recipient_id", "recipient_state", "resolved_at")
}

func readRow(summary []any, recipientID, state string, resolvedAt *time.Time) []any {
	return append(append([]any{}, summary...), recipientID, state, resolvedAt)
}

// expectRead registers the single statement a read performs — and registering
// exactly one is the assertion: a second query would find no expectation and
// fail, which is how these tests hold the summary and the detail to one
// snapshot without a database.
func expectRead(mock pgxmock.PgxPoolIface, rows ...[]any) {
	returned := pgxmock.NewRows(readCols())
	for _, row := range rows {
		returned.AddRow(row...)
	}
	mock.ExpectQuery(`(?s)WITH authorized AS.*asked AS.*LEFT JOIN asked detail`).
		WithArgs(ackStoreWorkspace, ackStoreViewer, ackStoreMessage).
		WillReturnRows(returned)
}

// Acknowledging is a transaction: the conditional UPDATE decides, the read
// reports, and both commit together. The order is the property — reading first
// and writing on what it said is the race this shape exists to avoid.
func TestPGXAcknowledgementStore_AcknowledgeWritesBeforeItReads(t *testing.T) {
	mock := newMock(t)
	mock.ExpectBegin()
	mock.ExpectExec(`(?s)UPDATE chat\.message_acknowledgements.*state = 'acknowledged'.*state = 'pending'`).
		WithArgs(ackStoreWorkspace, ackStoreViewer, ackStoreMessage).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	expectSummary(mock, summaryRow(true, false, 3, 1, 2, "acknowledged"))
	mock.ExpectCommit()

	result, err := storage.NewPGXAcknowledgementStore(mock).Acknowledge(t.Context(), storage.AcknowledgeInput{
		WorkspaceID: ackStoreWorkspace, MessageID: ackStoreMessage, RecipientID: ackStoreViewer,
	})
	if err != nil {
		t.Fatalf("Acknowledge: %v", err)
	}
	summary := result.Summary
	if summary.ViewerState != domain.AcknowledgementStateAcknowledged {
		t.Fatalf("viewer state = %q, want acknowledged", summary.ViewerState)
	}
	if summary.Total != 3 || summary.Pending != 1 || summary.Acknowledged != 2 {
		t.Fatalf("counts = %+v, want the summary the database returned", summary)
	}
	// The route the conversation is announced on comes from the message itself,
	// so the caller never looks it up a second time to publish.
	if result.Route.TargetType != "channel" || result.Route.TargetID != ackStoreChannel {
		t.Fatalf("route = %+v, want the message's own channel", result.Route)
	}
	// One row moved, so there is a change worth announcing.
	if !result.Changed {
		t.Fatal("an acknowledgement that moved a row must report a change")
	}
	checkExpectations(t, mock)
}

// A retry updates nothing and is still a success. The answer comes from the row
// that is actually stored, never from the number of rows the UPDATE touched —
// which is what makes "I clicked twice" and "my first response was lost" the
// same outcome.
func TestPGXAcknowledgementStore_AcknowledgeIsIdempotentWhenNothingChanged(t *testing.T) {
	mock := newMock(t)
	mock.ExpectBegin()
	mock.ExpectExec(`(?s)UPDATE chat\.message_acknowledgements`).
		WithArgs(ackStoreWorkspace, ackStoreViewer, ackStoreMessage).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	expectSummary(mock, summaryRow(true, false, 3, 1, 2, "acknowledged"))
	mock.ExpectCommit()

	result, err := storage.NewPGXAcknowledgementStore(mock).Acknowledge(t.Context(), storage.AcknowledgeInput{
		WorkspaceID: ackStoreWorkspace, MessageID: ackStoreMessage, RecipientID: ackStoreViewer,
	})
	if err != nil {
		t.Fatalf("a repeated acknowledgement must succeed, got %v", err)
	}
	if result.Summary.ViewerState != domain.AcknowledgementStateAcknowledged {
		t.Fatalf("viewer state = %q, want the stored state", result.Summary.ViewerState)
	}
	// Nothing moved, so nothing is announced: a retry storm must not become an
	// event storm.
	if result.Changed {
		t.Fatal("a retry that moved no row must not report a change")
	}
	checkExpectations(t, mock)
}

// A caller whose request was already resolved some other way is told that
// state, not refused. The UPDATE matched nothing because the row is terminal,
// and reporting "responded" is more useful to a client than a conflict it would
// have to translate back into a refetch.
func TestPGXAcknowledgementStore_AcknowledgeReportsAnAlreadyTerminalState(t *testing.T) {
	for _, state := range []domain.AcknowledgementState{
		domain.AcknowledgementStateResponded,
		domain.AcknowledgementStateCancelled,
		domain.AcknowledgementStateExpired,
	} {
		t.Run(string(state), func(t *testing.T) {
			mock := newMock(t)
			mock.ExpectBegin()
			mock.ExpectExec(`(?s)UPDATE chat\.message_acknowledgements`).
				WithArgs(ackStoreWorkspace, ackStoreViewer, ackStoreMessage).
				WillReturnResult(pgxmock.NewResult("UPDATE", 0))
			expectSummary(mock, summaryRow(true, false, 1, 0, 0, string(state)))
			mock.ExpectCommit()

			result, err := storage.NewPGXAcknowledgementStore(mock).Acknowledge(t.Context(), storage.AcknowledgeInput{
				WorkspaceID: ackStoreWorkspace, MessageID: ackStoreMessage, RecipientID: ackStoreViewer,
			})
			if err != nil {
				t.Fatalf("Acknowledge: %v", err)
			}
			if result.Summary.ViewerState != state {
				t.Fatalf("viewer state = %q, want %q reported back unchanged", result.Summary.ViewerState, state)
			}
			if result.Changed {
				t.Fatal("a row that was already terminal moved nothing")
			}
			checkExpectations(t, mock)
		})
	}
}

// A caller this message never asked is answered exactly like a caller who may
// not read it. The empty viewer state is the only signal, and nothing is
// committed.
func TestPGXAcknowledgementStore_AcknowledgeRefusesANonRecipient(t *testing.T) {
	mock := newMock(t)
	mock.ExpectBegin()
	mock.ExpectExec(`(?s)UPDATE chat\.message_acknowledgements`).
		WithArgs(ackStoreWorkspace, ackStoreViewer, ackStoreMessage).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	expectSummary(mock, summaryRow(true, true, 4, 4, 0, ""))
	mock.ExpectRollback()

	_, err := storage.NewPGXAcknowledgementStore(mock).Acknowledge(t.Context(), storage.AcknowledgeInput{
		WorkspaceID: ackStoreWorkspace, MessageID: ackStoreMessage, RecipientID: ackStoreViewer,
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
	checkExpectations(t, mock)
}

// An unreadable or absent message produces no row at all, and the same
// ErrNotFound — so the two cannot be told apart by a caller probing ids.
func TestPGXAcknowledgementStore_ReadRefusesAnUnreadableMessage(t *testing.T) {
	mock := newMock(t)
	expectRead(mock)

	_, err := storage.NewPGXAcknowledgementStore(mock).ReadAcknowledgement(t.Context(),
		storage.ReadAcknowledgementInput{
			WorkspaceID: ackStoreWorkspace, MessageID: ackStoreMessage, ViewerID: ackStoreViewer,
		})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
	checkExpectations(t, mock)
}

// A recipient reading the summary gets counts and their own state and no
// detail. This is the assertion that keeps a group's members from auditing each
// other: the list is not filtered out afterwards, the statement's join
// condition is what decides the rows exist, so there is no later branch that
// could forget to drop them.
func TestPGXAcknowledgementStore_ReadWithholdsTheDetailFromANonSender(t *testing.T) {
	mock := newMock(t)
	expectRead(mock, readRow(summaryRow(true, false, 7, 3, 4, "pending"), "", "", nil))

	summary, err := storage.NewPGXAcknowledgementStore(mock).ReadAcknowledgement(t.Context(),
		storage.ReadAcknowledgementInput{
			WorkspaceID: ackStoreWorkspace, MessageID: ackStoreMessage, ViewerID: ackStoreViewer,
		})
	if err != nil {
		t.Fatalf("ReadAcknowledgement: %v", err)
	}
	if len(summary.Recipients) != 0 {
		t.Fatalf("a recipient received %d rows of detail; only the sender may see who answered", len(summary.Recipients))
	}
	if summary.Total != 7 || summary.Acknowledged != 4 {
		t.Fatalf("counts = %+v, want the aggregate every reader gets", summary)
	}
	checkExpectations(t, mock)
}

// The sender gets the per-recipient list, ordered and with a resolution instant
// only where one exists.
func TestPGXAcknowledgementStore_ReadGivesTheSenderTheDetail(t *testing.T) {
	mock := newMock(t)
	resolved := time.Date(2026, 9, 11, 10, 0, 0, 0, time.UTC)
	summaryOf := summaryRow(true, true, 2, 1, 1, "")
	expectRead(mock,
		readRow(summaryOf, ackStorePeer, "acknowledged", &resolved),
		readRow(summaryOf, ackStoreViewer, "pending", nil))

	summary, err := storage.NewPGXAcknowledgementStore(mock).ReadAcknowledgement(t.Context(),
		storage.ReadAcknowledgementInput{
			WorkspaceID: ackStoreWorkspace, MessageID: ackStoreMessage, ViewerID: ackStoreViewer,
		})
	if err != nil {
		t.Fatalf("ReadAcknowledgement: %v", err)
	}
	if len(summary.Recipients) != 2 {
		t.Fatalf("sender received %d recipients, want 2", len(summary.Recipients))
	}
	if summary.Recipients[0].State != domain.AcknowledgementStateAcknowledged ||
		!summary.Recipients[0].ResolvedAt.Equal(resolved) {
		t.Fatalf("resolved recipient = %+v, want its state and instant", summary.Recipients[0])
	}
	if !summary.Recipients[1].ResolvedAt.IsZero() {
		t.Fatalf("a pending recipient must carry no resolution instant, got %v", summary.Recipients[1].ResolvedAt)
	}
	checkExpectations(t, mock)
}

// A message that asked nobody still answers, with zeros and no detail. The
// statement returns the message's row whether or not anything hangs off it, so
// "asked nobody" is a summary rather than a missing answer.
func TestPGXAcknowledgementStore_ReadSkipsTheDetailWhenNobodyWasAsked(t *testing.T) {
	mock := newMock(t)
	expectRead(mock, readRow(summaryRow(false, true, 0, 0, 0, ""), "", "", nil))

	summary, err := storage.NewPGXAcknowledgementStore(mock).ReadAcknowledgement(t.Context(),
		storage.ReadAcknowledgementInput{
			WorkspaceID: ackStoreWorkspace, MessageID: ackStoreMessage, ViewerID: ackStoreViewer,
		})
	if err != nil {
		t.Fatalf("ReadAcknowledgement: %v", err)
	}
	if summary.Required || summary.Total != 0 || len(summary.Recipients) != 0 {
		t.Fatalf("a message that asked nobody summarised as %+v", summary)
	}
	checkExpectations(t, mock)
}

// A failed read fails rather than returning a summary with a silently empty
// recipient list, which the sender would read as "nobody was asked".
func TestPGXAcknowledgementStore_ReadFailsWhenTheDetailCannotBeListed(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(`(?s)WITH authorized AS.*asked AS`).
		WithArgs(ackStoreWorkspace, ackStoreViewer, ackStoreMessage).
		WillReturnError(errors.New("connection reset"))

	_, err := storage.NewPGXAcknowledgementStore(mock).ReadAcknowledgement(t.Context(),
		storage.ReadAcknowledgementInput{
			WorkspaceID: ackStoreWorkspace, MessageID: ackStoreMessage, ViewerID: ackStoreViewer,
		})
	if err == nil {
		t.Fatal("a failed detail read must not be reported as an empty recipient list")
	}
	checkExpectations(t, mock)
}

// The acknowledging statement carries its own authorization. Without the EXISTS
// over the shared message-access predicate, a former member's stale row would
// still be theirs to resolve.
func TestPGXAcknowledgementStore_AcknowledgeStatementRechecksReadAccess(t *testing.T) {
	mock := newMock(t)
	mock.ExpectBegin()
	mock.ExpectExec(`(?s)UPDATE chat\.message_acknowledgements.*EXISTS \(.*chat\.dm_members.*chat\.channel_visible_to_user`).
		WithArgs(ackStoreWorkspace, ackStoreViewer, ackStoreMessage).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	expectSummary(mock, summaryRow(true, false, 1, 0, 1, "acknowledged"))
	mock.ExpectCommit()

	if _, err := storage.NewPGXAcknowledgementStore(mock).Acknowledge(t.Context(), storage.AcknowledgeInput{
		WorkspaceID: ackStoreWorkspace, MessageID: ackStoreMessage, RecipientID: ackStoreViewer,
	}); err != nil {
		t.Fatalf("Acknowledge: %v", err)
	}
	checkExpectations(t, mock)
}

// A failed UPDATE aborts the transaction rather than falling through to a read
// that would report the state as if nothing had gone wrong.
func TestPGXAcknowledgementStore_AcknowledgeRollsBackAFailedUpdate(t *testing.T) {
	mock := newMock(t)
	mock.ExpectBegin()
	mock.ExpectExec(`(?s)UPDATE chat\.message_acknowledgements`).
		WithArgs(ackStoreWorkspace, ackStoreViewer, ackStoreMessage).
		WillReturnError(errors.New("deadlock detected"))
	mock.ExpectRollback()

	if _, err := storage.NewPGXAcknowledgementStore(mock).Acknowledge(t.Context(), storage.AcknowledgeInput{
		WorkspaceID: ackStoreWorkspace, MessageID: ackStoreMessage, RecipientID: ackStoreViewer,
	}); err == nil {
		t.Fatal("a failed acknowledgement must be reported, not swallowed")
	}
	checkExpectations(t, mock)
}

// ── the page batch is one statement ──────────────────────────────────────────
//
// Code review, #824: the page asked per message. The correction is only real if
// the batch is a single set-based statement — replacing N HTTP calls with N SQL
// round trips would have moved the problem rather than fixed it. A mock is the
// exact instrument for that: it counts the statements.

func batchCols() []string {
	return []string{
		"message_id", "acknowledgement_required",
		"total", "pending", "acknowledged", "responded", "expired", "cancelled",
		"viewer_state",
	}
}

func TestPGXAcknowledgementStore_BatchIssuesOneQueryForTheWholePage(t *testing.T) {
	mock := newMock(t)
	ids := make([]string, 0, 40)
	rows := pgxmock.NewRows(batchCols())
	for i := range 40 {
		id := fmt.Sprintf("82400000-0000-4000-8000-%012d", i)
		ids = append(ids, id)
		rows = rows.AddRow(id, true, 3, 2, 1, 0, 0, 0, "pending")
	}
	// Exactly one expectation. pgxmock fails the call if a second statement is
	// issued, so "one query for forty messages" is asserted rather than assumed.
	mock.ExpectQuery(`(?s)WITH authorized AS.*ANY\(\$3::uuid\[\]\).*GROUP BY ma\.message_id`).
		WithArgs(ackStoreWorkspace, ackStoreViewer, ids).
		WillReturnRows(rows)

	summaries, err := storage.NewPGXAcknowledgementStore(mock).ReadAcknowledgementBatch(
		t.Context(), storage.ReadAcknowledgementBatchInput{
			WorkspaceID: ackStoreWorkspace, MessageIDs: ids, ViewerID: ackStoreViewer,
		})
	if err != nil {
		t.Fatalf("ReadAcknowledgementBatch: %v", err)
	}
	if len(summaries) != 40 {
		t.Fatalf("got %d summaries, want 40", len(summaries))
	}
	if summaries[ids[7]].ViewerState != domain.AcknowledgementStatePending {
		t.Fatalf("summary keyed wrongly: %+v", summaries[ids[7]])
	}
	checkExpectations(t, mock)
}

// An empty request spends no statement at all. The service refuses it first, so
// this is the store being safe on its own terms rather than a reachable path.
func TestPGXAcknowledgementStore_BatchWithNoIDsQueriesNothing(t *testing.T) {
	mock := newMock(t)
	summaries, err := storage.NewPGXAcknowledgementStore(mock).ReadAcknowledgementBatch(
		t.Context(), storage.ReadAcknowledgementBatchInput{
			WorkspaceID: ackStoreWorkspace, ViewerID: ackStoreViewer,
		})
	if err != nil || len(summaries) != 0 {
		t.Fatalf("summaries = %+v, err = %v", summaries, err)
	}
	checkExpectations(t, mock)
}

// A message the statement did not return is absent from the map rather than
// present and empty — which is what lets a caller tell "not readable" from
// "asked nobody".
func TestPGXAcknowledgementStore_BatchOmitsWhatTheStatementDidNotReturn(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(`(?s)WITH authorized AS`).
		WithArgs(ackStoreWorkspace, ackStoreViewer, []string{ackStoreMessage, ackStorePeer}).
		WillReturnRows(pgxmock.NewRows(batchCols()).
			AddRow(ackStoreMessage, true, 2, 1, 1, 0, 0, 0, ""))

	summaries, err := storage.NewPGXAcknowledgementStore(mock).ReadAcknowledgementBatch(
		t.Context(), storage.ReadAcknowledgementBatchInput{
			WorkspaceID: ackStoreWorkspace,
			MessageIDs:  []string{ackStoreMessage, ackStorePeer},
			ViewerID:    ackStoreViewer,
		})
	if err != nil {
		t.Fatalf("ReadAcknowledgementBatch: %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("got %d entries, want only what the statement returned", len(summaries))
	}
	if _, present := summaries[ackStorePeer]; present {
		t.Fatal("an id the statement withheld produced an entry")
	}
	// No recipient list travels in the page view.
	if len(summaries[ackStoreMessage].Recipients) != 0 {
		t.Fatal("the page batch carried recipient detail")
	}
}
