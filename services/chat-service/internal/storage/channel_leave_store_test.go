package storage_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	pgxmock "github.com/pashagolub/pgxmock/v2"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// Channel self-leave, at the statement level (issue #527).
//
// The behaviour against a real database is proven in
// conversation_admin_postgres_test.go. What these add is the transactional
// shape: which statements run, in which order, and — the part that matters most
// — that every failure path rolls back, so a departure never leaves an event
// behind and an event never exists without the departure it describes.

func leaveEventRows() *pgxmock.Rows {
	return pgxmock.NewRows([]string{
		"id", "workspace_id", "channel_id", "dm_conversation_id",
		"sender_id", "kind", "event_type", "created_at",
	}).AddRow("event-1", "ws-1", "chan-1", "", "user-1", "system",
		string(domain.ConversationEventMemberLeft), time.Now())
}

// The channel row first, then the general-channel test, then the event, then the
// membership: the lock order every membership mutation obeys.
func expectLeaveChannelUpTo(mock pgxmock.PgxPoolIface, isGeneral bool) {
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id FROM chat\.channels WHERE id = \$1::uuid FOR UPDATE`).
		WithArgs("chan-1").
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow("chan-1"))
	mock.ExpectQuery(`(?s)SELECT c\.is_general.*chat\.workspaces w.*c\.status = 'active'`).
		WithArgs("chan-1", "ws-1").
		WillReturnRows(pgxmock.NewRows([]string{"is_general"}).AddRow(isGeneral))
}

func expectLeaveEventInsert(mock pgxmock.PgxPoolIface) {
	mock.ExpectQuery(`INSERT INTO chat\.messages`).
		WithArgs("ws-1", pgxmock.AnyArg(), pgxmock.AnyArg(), "user-1",
			string(domain.ConversationEventMemberLeft), pgxmock.AnyArg()).
		WillReturnRows(leaveEventRows())
}

func TestPGXChannelStore_LeaveChannelSelf_WritesTheEventAndTheDepartureTogether(t *testing.T) {
	mock := newMock(t)
	expectLeaveChannelUpTo(mock, false)
	mock.ExpectQuery(`(?s)INSERT INTO chat\.messages.*'system'.*RETURNING`).
		WithArgs("ws-1", strPtr("chan-1"), (*string)(nil), "user-1",
			string(domain.ConversationEventMemberLeft), pgxmock.AnyArg()).
		WillReturnRows(leaveEventRows())
	mock.ExpectExec(`(?s)DELETE FROM chat\.channel_members cm.*USING chat\.channels c`).
		WithArgs("chan-1", "user-1", "ws-1").
		WillReturnResult(pgxmock.NewResult("DELETE", 1))
	mock.ExpectCommit()

	result, err := storage.NewPGXChannelStore(mock).LeaveChannelSelf(
		context.Background(), "ws-1", "chan-1", "user-1")
	if err != nil {
		t.Fatalf("LeaveChannelSelf: %v", err)
	}
	if result.Event.ID != "event-1" || result.Event.Kind != domain.MessageKindSystem {
		t.Fatalf("event = %+v, want the persisted system message", result.Event)
	}
	checkExpectations(t, mock)
}

// Membership in #geral is owned by the workspace sync, so leaving it is not a
// thing a person may do — and the refusal must not depend on the UI having
// hidden the action.
func TestPGXChannelStore_LeaveChannelSelf_RefusesTheGeneralChannel(t *testing.T) {
	mock := newMock(t)
	expectLeaveChannelUpTo(mock, true)
	mock.ExpectRollback()

	_, err := storage.NewPGXChannelStore(mock).LeaveChannelSelf(
		context.Background(), "ws-1", "chan-1", "user-1")
	if !errors.Is(err, domain.ErrGeneralChannelImmutable) {
		t.Fatalf("error = %v, want ErrGeneralChannelImmutable", err)
	}
	checkExpectations(t, mock)
}

// A public channel is readable without a chat.channel_members row, so "leaving"
// one that was never joined removes nothing. That is reported rather than
// silently succeeding, because the alternative is a "you left" event in the
// history of a conversation nobody left.
func TestPGXChannelStore_LeaveChannelSelf_WithoutMembershipRollsBack(t *testing.T) {
	mock := newMock(t)
	expectLeaveChannelUpTo(mock, false)
	mock.ExpectQuery(`INSERT INTO chat\.messages`).
		WithArgs("ws-1", pgxmock.AnyArg(), pgxmock.AnyArg(), "user-1",
			string(domain.ConversationEventMemberLeft), pgxmock.AnyArg()).
		WillReturnRows(leaveEventRows())
	mock.ExpectExec(`DELETE FROM chat\.channel_members`).
		WithArgs("chan-1", "user-1", "ws-1").
		WillReturnResult(pgxmock.NewResult("DELETE", 0))
	mock.ExpectRollback()

	_, err := storage.NewPGXChannelStore(mock).LeaveChannelSelf(
		context.Background(), "ws-1", "chan-1", "user-1")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
	checkExpectations(t, mock)
}

func TestPGXChannelStore_LeaveChannelSelf_MissingChannelIsNotFound(t *testing.T) {
	mock := newMock(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`FOR UPDATE`).WithArgs("chan-1").WillReturnError(pgx.ErrNoRows)
	mock.ExpectRollback()

	_, err := storage.NewPGXChannelStore(mock).LeaveChannelSelf(
		context.Background(), "ws-1", "chan-1", "user-1")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
	checkExpectations(t, mock)
}

// A channel of another workspace, or an archived one, has no row to read here —
// indistinguishable from one that never existed.
func TestPGXChannelStore_LeaveChannelSelf_ChannelOutsideTheWorkspaceIsNotFound(t *testing.T) {
	mock := newMock(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`FOR UPDATE`).
		WithArgs("chan-1").
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow("chan-1"))
	mock.ExpectQuery(`SELECT c\.is_general`).
		WithArgs("chan-1", "ws-1").
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectRollback()

	_, err := storage.NewPGXChannelStore(mock).LeaveChannelSelf(
		context.Background(), "ws-1", "chan-1", "user-1")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
	checkExpectations(t, mock)
}

// A missing actor is a wiring bug, never an anonymous departure: it is refused
// before a transaction is even opened.
func TestPGXChannelStore_LeaveChannelSelf_RefusesAnEmptyActorWithoutATransaction(t *testing.T) {
	mock := newMock(t)

	_, err := storage.NewPGXChannelStore(mock).LeaveChannelSelf(
		context.Background(), "ws-1", "chan-1", "")
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("error = %v, want ErrForbidden", err)
	}
	checkExpectations(t, mock)
}

func TestPGXChannelStore_LeaveChannelSelf_PropagatesDatabaseFailures(t *testing.T) {
	t.Run("begin", func(t *testing.T) {
		mock := newMock(t)
		mock.ExpectBegin().WillReturnError(errors.New("db down"))

		if _, err := storage.NewPGXChannelStore(mock).LeaveChannelSelf(
			context.Background(), "ws-1", "chan-1", "user-1"); err == nil {
			t.Fatal("expected the begin failure to propagate")
		}
	})

	t.Run("lock", func(t *testing.T) {
		mock := newMock(t)
		mock.ExpectBegin()
		mock.ExpectQuery(`FOR UPDATE`).WithArgs("chan-1").WillReturnError(errors.New("db down"))
		mock.ExpectRollback()

		if _, err := storage.NewPGXChannelStore(mock).LeaveChannelSelf(
			context.Background(), "ws-1", "chan-1", "user-1"); err == nil {
			t.Fatal("expected the lock failure to propagate")
		}
		checkExpectations(t, mock)
	})

	t.Run("read general flag", func(t *testing.T) {
		mock := newMock(t)
		mock.ExpectBegin()
		mock.ExpectQuery(`FOR UPDATE`).
			WithArgs("chan-1").
			WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow("chan-1"))
		mock.ExpectQuery(`SELECT c\.is_general`).
			WithArgs("chan-1", "ws-1").
			WillReturnError(errors.New("db down"))
		mock.ExpectRollback()

		if _, err := storage.NewPGXChannelStore(mock).LeaveChannelSelf(
			context.Background(), "ws-1", "chan-1", "user-1"); err == nil {
			t.Fatal("expected the read failure to propagate")
		}
		checkExpectations(t, mock)
	})

	t.Run("delete", func(t *testing.T) {
		mock := newMock(t)
		expectLeaveChannelUpTo(mock, false)
		expectLeaveEventInsert(mock)
		mock.ExpectExec(`DELETE FROM chat\.channel_members`).
			WithArgs("chan-1", "user-1", "ws-1").
			WillReturnError(errors.New("db down"))
		mock.ExpectRollback()

		if _, err := storage.NewPGXChannelStore(mock).LeaveChannelSelf(
			context.Background(), "ws-1", "chan-1", "user-1"); err == nil {
			t.Fatal("expected the delete failure to propagate")
		}
		checkExpectations(t, mock)
	})

	// The event and the departure are one commit: a commit that fails must not
	// be reported as a departure that happened.
	t.Run("commit", func(t *testing.T) {
		mock := newMock(t)
		expectLeaveChannelUpTo(mock, false)
		expectLeaveEventInsert(mock)
		mock.ExpectExec(`DELETE FROM chat\.channel_members`).
			WithArgs("chan-1", "user-1", "ws-1").
			WillReturnResult(pgxmock.NewResult("DELETE", 1))
		mock.ExpectCommit().WillReturnError(errors.New("db down"))
		mock.ExpectRollback()

		if _, err := storage.NewPGXChannelStore(mock).LeaveChannelSelf(
			context.Background(), "ws-1", "chan-1", "user-1"); err == nil {
			t.Fatal("expected the commit failure to propagate")
		}
	})
}

func strPtr(s string) *string { return &s }
