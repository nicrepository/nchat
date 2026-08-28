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

// Group rename and self-leave, at the statement level (issue #527).
//
// Their behaviour against a real database — including the concurrency
// properties the security review required — is proven in
// conversation_admin_postgres_test.go and dm_conversation_concurrency_postgres_test.go.
// What these add is what a database cannot show as directly: the exact lock
// protocol each operation runs, and that every refusal rolls back rather than
// committing half of it.
//
// The lock order asserted here is the canonical one, and asserting it is the
// point: conversation row, then the actor's dm_members row, then the actor's
// workspace_members row, then the write.

const (
	adminWS    = "ws-1"
	adminConv  = "dm-1"
	adminActor = "user-1"
)

func groupConversationRows(title string) *pgxmock.Rows {
	return pgxmock.NewRows([]string{
		"id", "workspace_id", "type", "title", "status", "created_by", "created_at", "updated_at",
	}).AddRow(adminConv, adminWS, "group", title, "active", "user-9", time.Now(), time.Now())
}

func groupEventRows(event domain.ConversationEventType) *pgxmock.Rows {
	return pgxmock.NewRows([]string{
		"id", "workspace_id", "channel_id", "dm_conversation_id",
		"sender_id", "kind", "event_type", "created_at",
	}).AddRow("event-1", adminWS, "", adminConv, adminActor, "system", string(event), time.Now())
}

// expectGroupLocks registers the three locks every group mutation takes, in the
// order it takes them. `conversationLock` and `participationLock` are the mode
// each operation uses: the row it is about to write is taken exclusively, the
// other shared.
func expectGroupLocks(mock pgxmock.PgxPoolIface, conversationLock, participationLock string, title string) {
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)FROM chat\.dm_conversations dc.*dc\.type = 'group'.*`+conversationLock).
		WithArgs(adminConv, adminWS).
		WillReturnRows(pgxmock.NewRows([]string{"id", "title"}).AddRow(adminConv, title))
	mock.ExpectQuery(`(?s)FROM chat\.dm_members dm.*dm\.status = 'active'.*`+participationLock).
		WithArgs(adminConv, adminActor).
		WillReturnRows(pgxmock.NewRows([]string{"participates"}).AddRow(true))
	mock.ExpectQuery(`(?s)FROM chat\.workspace_members wm.*wm\.status = 'active'.*FOR SHARE OF wm`).
		WithArgs(adminWS, adminActor).
		WillReturnRows(pgxmock.NewRows([]string{"authorized"}).AddRow(true))
}

// ── Rename ──────────────────────────────────────────────────────────────────

// The rename writes the conversation row, so it takes that row FOR UPDATE and
// the membership FOR SHARE. Anything weaker on the row it writes would have to
// be upgraded at the UPDATE, and two renames upgrading at once deadlock.
func TestPGXDMStore_RenameGroupConversation_LocksTheConversationForUpdate(t *testing.T) {
	mock := newMock(t)
	expectGroupLocks(mock, `FOR UPDATE OF dc`, `FOR SHARE OF dm`, "Equipe")
	mock.ExpectQuery(`(?s)UPDATE chat\.dm_conversations.*SET title = \$3.*type = 'group'.*RETURNING`).
		WithArgs(adminConv, adminWS, "Piloto").
		WillReturnRows(groupConversationRows("Piloto"))
	mock.ExpectQuery(`INSERT INTO chat\.messages`).
		WithArgs(adminWS, pgxmock.AnyArg(), pgxmock.AnyArg(), adminActor,
			string(domain.ConversationEventRenamed), pgxmock.AnyArg()).
		WillReturnRows(groupEventRows(domain.ConversationEventRenamed))
	mock.ExpectCommit()

	result, err := storage.NewPGXDMStore(mock).RenameGroupConversation(
		context.Background(), storage.RenameGroupInput{
			WorkspaceID:    adminWS,
			ConversationID: adminConv,
			CallerID:       adminActor,
			Title:          "Piloto",
		})
	if err != nil {
		t.Fatalf("RenameGroupConversation: %v", err)
	}
	if result.Conversation.Title != "Piloto" {
		t.Fatalf("title = %q, want the new one", result.Conversation.Title)
	}
	// The event carries the before and after, and the actor is the caller — never
	// a name taken from the request.
	if result.Event.EventPayload.OldName != "Equipe" || result.Event.EventPayload.NewName != "Piloto" {
		t.Fatalf("payload = %+v, want the old and new names", result.Event.EventPayload)
	}
	if result.Event.SenderID != adminActor {
		t.Fatalf("actor = %q, want the caller", result.Event.SenderID)
	}
	checkExpectations(t, mock)
}

// A 1:1 conversation, an archived one and one from another workspace all reach
// no row at all: the lock statement requires an active group of this workspace.
func TestPGXDMStore_RenameGroupConversation_UnlockableConversationIsNotFound(t *testing.T) {
	mock := newMock(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`FROM chat\.dm_conversations dc`).
		WithArgs(adminConv, adminWS).
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectRollback()

	_, err := storage.NewPGXDMStore(mock).RenameGroupConversation(
		context.Background(), storage.RenameGroupInput{
			WorkspaceID: adminWS, ConversationID: adminConv, CallerID: adminActor, Title: "Piloto",
		})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
	checkExpectations(t, mock)
}

// Participation is the whole authority for a group, and a workspace admin who
// is not in it has none. Both refusals are the same ErrForbidden.
func TestPGXDMStore_RenameGroupConversation_RefusesANonParticipant(t *testing.T) {
	mock := newMock(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`FROM chat\.dm_conversations dc`).
		WithArgs(adminConv, adminWS).
		WillReturnRows(pgxmock.NewRows([]string{"id", "title"}).AddRow(adminConv, "Equipe"))
	mock.ExpectQuery(`FROM chat\.dm_members dm`).
		WithArgs(adminConv, adminActor).
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectRollback()

	_, err := storage.NewPGXDMStore(mock).RenameGroupConversation(
		context.Background(), storage.RenameGroupInput{
			WorkspaceID: adminWS, ConversationID: adminConv, CallerID: adminActor, Title: "Piloto",
		})
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("error = %v, want ErrForbidden", err)
	}
	checkExpectations(t, mock)
}

// A chat.dm_members row outlives the workspace membership that justified it, so
// participation alone is not enough: the workspace membership is re-read and
// held, and a revoked one refuses the write.
func TestPGXDMStore_RenameGroupConversation_RefusesARevokedWorkspaceMembership(t *testing.T) {
	mock := newMock(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`FROM chat\.dm_conversations dc`).
		WithArgs(adminConv, adminWS).
		WillReturnRows(pgxmock.NewRows([]string{"id", "title"}).AddRow(adminConv, "Equipe"))
	mock.ExpectQuery(`FROM chat\.dm_members dm`).
		WithArgs(adminConv, adminActor).
		WillReturnRows(pgxmock.NewRows([]string{"participates"}).AddRow(true))
	mock.ExpectQuery(`FROM chat\.workspace_members wm`).
		WithArgs(adminWS, adminActor).
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectRollback()

	_, err := storage.NewPGXDMStore(mock).RenameGroupConversation(
		context.Background(), storage.RenameGroupInput{
			WorkspaceID: adminWS, ConversationID: adminConv, CallerID: adminActor, Title: "Piloto",
		})
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("error = %v, want ErrForbidden", err)
	}
	checkExpectations(t, mock)
}

func TestPGXDMStore_RenameGroupConversation_RefusesAnEmptyActorWithoutATransaction(t *testing.T) {
	mock := newMock(t)

	_, err := storage.NewPGXDMStore(mock).RenameGroupConversation(
		context.Background(), storage.RenameGroupInput{
			WorkspaceID: adminWS, ConversationID: adminConv, CallerID: "", Title: "Piloto",
		})
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("error = %v, want ErrForbidden", err)
	}
	checkExpectations(t, mock)
}

func TestPGXDMStore_RenameGroupConversation_PropagatesFailures(t *testing.T) {
	t.Run("begin", func(t *testing.T) {
		mock := newMock(t)
		mock.ExpectBegin().WillReturnError(errors.New("db down"))

		if _, err := storage.NewPGXDMStore(mock).RenameGroupConversation(
			context.Background(), storage.RenameGroupInput{
				WorkspaceID: adminWS, ConversationID: adminConv, CallerID: adminActor, Title: "x",
			}); err == nil {
			t.Fatal("expected the begin failure to propagate")
		}
	})

	t.Run("lock", func(t *testing.T) {
		mock := newMock(t)
		mock.ExpectBegin()
		mock.ExpectQuery(`FROM chat\.dm_conversations dc`).
			WithArgs(adminConv, adminWS).
			WillReturnError(errors.New("db down"))
		mock.ExpectRollback()

		if _, err := storage.NewPGXDMStore(mock).RenameGroupConversation(
			context.Background(), storage.RenameGroupInput{
				WorkspaceID: adminWS, ConversationID: adminConv, CallerID: adminActor, Title: "x",
			}); err == nil {
			t.Fatal("expected the lock failure to propagate")
		}
		checkExpectations(t, mock)
	})

	t.Run("participation", func(t *testing.T) {
		mock := newMock(t)
		mock.ExpectBegin()
		mock.ExpectQuery(`FROM chat\.dm_conversations dc`).
			WithArgs(adminConv, adminWS).
			WillReturnRows(pgxmock.NewRows([]string{"id", "title"}).AddRow(adminConv, "Equipe"))
		mock.ExpectQuery(`FROM chat\.dm_members dm`).
			WithArgs(adminConv, adminActor).
			WillReturnError(errors.New("db down"))
		mock.ExpectRollback()

		if _, err := storage.NewPGXDMStore(mock).RenameGroupConversation(
			context.Background(), storage.RenameGroupInput{
				WorkspaceID: adminWS, ConversationID: adminConv, CallerID: adminActor, Title: "x",
			}); err == nil {
			t.Fatal("expected the participation failure to propagate")
		}
		checkExpectations(t, mock)
	})

	t.Run("workspace membership", func(t *testing.T) {
		mock := newMock(t)
		mock.ExpectBegin()
		mock.ExpectQuery(`FROM chat\.dm_conversations dc`).
			WithArgs(adminConv, adminWS).
			WillReturnRows(pgxmock.NewRows([]string{"id", "title"}).AddRow(adminConv, "Equipe"))
		mock.ExpectQuery(`FROM chat\.dm_members dm`).
			WithArgs(adminConv, adminActor).
			WillReturnRows(pgxmock.NewRows([]string{"participates"}).AddRow(true))
		mock.ExpectQuery(`FROM chat\.workspace_members wm`).
			WithArgs(adminWS, adminActor).
			WillReturnError(errors.New("db down"))
		mock.ExpectRollback()

		if _, err := storage.NewPGXDMStore(mock).RenameGroupConversation(
			context.Background(), storage.RenameGroupInput{
				WorkspaceID: adminWS, ConversationID: adminConv, CallerID: adminActor, Title: "x",
			}); err == nil {
			t.Fatal("expected the membership failure to propagate")
		}
		checkExpectations(t, mock)
	})

	// The conversation was locked and the actor authorized, so an UPDATE that
	// matches nothing means it stopped being an active group in between.
	t.Run("update matches nothing", func(t *testing.T) {
		mock := newMock(t)
		expectGroupLocks(mock, `FOR UPDATE OF dc`, `FOR SHARE OF dm`, "Equipe")
		mock.ExpectQuery(`UPDATE chat\.dm_conversations`).
			WithArgs(adminConv, adminWS, "x").
			WillReturnError(pgx.ErrNoRows)
		mock.ExpectRollback()

		if _, err := storage.NewPGXDMStore(mock).RenameGroupConversation(
			context.Background(), storage.RenameGroupInput{
				WorkspaceID: adminWS, ConversationID: adminConv, CallerID: adminActor, Title: "x",
			}); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("error = %v, want ErrNotFound", err)
		}
		checkExpectations(t, mock)
	})

	// The rename and its event are one commit. A failing event write must take
	// the rename with it, or the title would change with nothing in the history.
	t.Run("event insert", func(t *testing.T) {
		mock := newMock(t)
		expectGroupLocks(mock, `FOR UPDATE OF dc`, `FOR SHARE OF dm`, "Equipe")
		mock.ExpectQuery(`UPDATE chat\.dm_conversations`).
			WithArgs(adminConv, adminWS, "x").
			WillReturnRows(groupConversationRows("x"))
		mock.ExpectQuery(`INSERT INTO chat\.messages`).
			WithArgs(adminWS, pgxmock.AnyArg(), pgxmock.AnyArg(), adminActor,
				string(domain.ConversationEventRenamed), pgxmock.AnyArg()).
			WillReturnError(errors.New("db down"))
		mock.ExpectRollback()

		if _, err := storage.NewPGXDMStore(mock).RenameGroupConversation(
			context.Background(), storage.RenameGroupInput{
				WorkspaceID: adminWS, ConversationID: adminConv, CallerID: adminActor, Title: "x",
			}); err == nil {
			t.Fatal("expected the event failure to propagate")
		}
		checkExpectations(t, mock)
	})

	t.Run("commit", func(t *testing.T) {
		mock := newMock(t)
		expectGroupLocks(mock, `FOR UPDATE OF dc`, `FOR SHARE OF dm`, "Equipe")
		mock.ExpectQuery(`UPDATE chat\.dm_conversations`).
			WithArgs(adminConv, adminWS, "x").
			WillReturnRows(groupConversationRows("x"))
		mock.ExpectQuery(`INSERT INTO chat\.messages`).
			WithArgs(adminWS, pgxmock.AnyArg(), pgxmock.AnyArg(), adminActor,
				string(domain.ConversationEventRenamed), pgxmock.AnyArg()).
			WillReturnRows(groupEventRows(domain.ConversationEventRenamed))
		mock.ExpectCommit().WillReturnError(errors.New("db down"))
		mock.ExpectRollback()

		if _, err := storage.NewPGXDMStore(mock).RenameGroupConversation(
			context.Background(), storage.RenameGroupInput{
				WorkspaceID: adminWS, ConversationID: adminConv, CallerID: adminActor, Title: "x",
			}); err == nil {
			t.Fatal("expected the commit failure to propagate")
		}
	})
}

// ── Self-leave ──────────────────────────────────────────────────────────────

// The mirror image of the rename: the membership row is the one being written,
// so it is taken FOR UPDATE and the conversation FOR SHARE. Two self-leaves
// holding a shared lock and then upgrading is the deadlock the security review
// found, and this is the assertion that keeps it fixed.
func TestPGXDMStore_LeaveGroupConversation_LocksTheMembershipForUpdate(t *testing.T) {
	mock := newMock(t)
	expectGroupLocks(mock, `FOR SHARE OF dc`, `FOR UPDATE OF dm`, "Equipe")
	mock.ExpectQuery(`INSERT INTO chat\.messages`).
		WithArgs(adminWS, pgxmock.AnyArg(), pgxmock.AnyArg(), adminActor,
			string(domain.ConversationEventMemberLeft), pgxmock.AnyArg()).
		WillReturnRows(groupEventRows(domain.ConversationEventMemberLeft))
	mock.ExpectExec(`(?s)UPDATE chat\.dm_members.*SET status = 'left', left_at = now\(\).*status = 'active'`).
		WithArgs(adminConv, adminActor).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()

	result, err := storage.NewPGXDMStore(mock).LeaveGroupConversation(
		context.Background(), adminWS, adminConv, adminActor)
	if err != nil {
		t.Fatalf("LeaveGroupConversation: %v", err)
	}
	if result.Event.EventType != string(domain.ConversationEventMemberLeft) {
		t.Fatalf("event = %+v, want a member-left event", result.Event)
	}
	checkExpectations(t, mock)
}

// The membership row is held FOR UPDATE by the check above, so an UPDATE that
// matches nothing can only mean the actor was never active in it. It is refused
// rather than committed as a departure that did not happen.
func TestPGXDMStore_LeaveGroupConversation_NoActiveMembershipRollsBack(t *testing.T) {
	mock := newMock(t)
	expectGroupLocks(mock, `FOR SHARE OF dc`, `FOR UPDATE OF dm`, "Equipe")
	mock.ExpectQuery(`INSERT INTO chat\.messages`).
		WithArgs(adminWS, pgxmock.AnyArg(), pgxmock.AnyArg(), adminActor,
			string(domain.ConversationEventMemberLeft), pgxmock.AnyArg()).
		WillReturnRows(groupEventRows(domain.ConversationEventMemberLeft))
	mock.ExpectExec(`UPDATE chat\.dm_members`).
		WithArgs(adminConv, adminActor).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectRollback()

	_, err := storage.NewPGXDMStore(mock).LeaveGroupConversation(
		context.Background(), adminWS, adminConv, adminActor)
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("error = %v, want ErrForbidden", err)
	}
	checkExpectations(t, mock)
}

func TestPGXDMStore_LeaveGroupConversation_RefusesAnEmptyActorWithoutATransaction(t *testing.T) {
	mock := newMock(t)

	_, err := storage.NewPGXDMStore(mock).LeaveGroupConversation(
		context.Background(), adminWS, adminConv, "")
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("error = %v, want ErrForbidden", err)
	}
	checkExpectations(t, mock)
}

func TestPGXDMStore_LeaveGroupConversation_PropagatesFailures(t *testing.T) {
	t.Run("begin", func(t *testing.T) {
		mock := newMock(t)
		mock.ExpectBegin().WillReturnError(errors.New("db down"))

		if _, err := storage.NewPGXDMStore(mock).LeaveGroupConversation(
			context.Background(), adminWS, adminConv, adminActor); err == nil {
			t.Fatal("expected the begin failure to propagate")
		}
	})

	t.Run("locks", func(t *testing.T) {
		mock := newMock(t)
		mock.ExpectBegin()
		mock.ExpectQuery(`FROM chat\.dm_conversations dc`).
			WithArgs(adminConv, adminWS).
			WillReturnError(pgx.ErrNoRows)
		mock.ExpectRollback()

		if _, err := storage.NewPGXDMStore(mock).LeaveGroupConversation(
			context.Background(), adminWS, adminConv, adminActor); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("error = %v, want ErrNotFound", err)
		}
		checkExpectations(t, mock)
	})

	// The departure and its event are one commit, in both directions.
	t.Run("event insert", func(t *testing.T) {
		mock := newMock(t)
		expectGroupLocks(mock, `FOR SHARE OF dc`, `FOR UPDATE OF dm`, "Equipe")
		mock.ExpectQuery(`INSERT INTO chat\.messages`).
			WithArgs(adminWS, pgxmock.AnyArg(), pgxmock.AnyArg(), adminActor,
				string(domain.ConversationEventMemberLeft), pgxmock.AnyArg()).
			WillReturnError(errors.New("db down"))
		mock.ExpectRollback()

		if _, err := storage.NewPGXDMStore(mock).LeaveGroupConversation(
			context.Background(), adminWS, adminConv, adminActor); err == nil {
			t.Fatal("expected the event failure to propagate")
		}
		checkExpectations(t, mock)
	})

	t.Run("membership update", func(t *testing.T) {
		mock := newMock(t)
		expectGroupLocks(mock, `FOR SHARE OF dc`, `FOR UPDATE OF dm`, "Equipe")
		mock.ExpectQuery(`INSERT INTO chat\.messages`).
			WithArgs(adminWS, pgxmock.AnyArg(), pgxmock.AnyArg(), adminActor,
				string(domain.ConversationEventMemberLeft), pgxmock.AnyArg()).
			WillReturnRows(groupEventRows(domain.ConversationEventMemberLeft))
		mock.ExpectExec(`UPDATE chat\.dm_members`).
			WithArgs(adminConv, adminActor).
			WillReturnError(errors.New("db down"))
		mock.ExpectRollback()

		if _, err := storage.NewPGXDMStore(mock).LeaveGroupConversation(
			context.Background(), adminWS, adminConv, adminActor); err == nil {
			t.Fatal("expected the update failure to propagate")
		}
		checkExpectations(t, mock)
	})

	t.Run("commit", func(t *testing.T) {
		mock := newMock(t)
		expectGroupLocks(mock, `FOR SHARE OF dc`, `FOR UPDATE OF dm`, "Equipe")
		mock.ExpectQuery(`INSERT INTO chat\.messages`).
			WithArgs(adminWS, pgxmock.AnyArg(), pgxmock.AnyArg(), adminActor,
				string(domain.ConversationEventMemberLeft), pgxmock.AnyArg()).
			WillReturnRows(groupEventRows(domain.ConversationEventMemberLeft))
		mock.ExpectExec(`UPDATE chat\.dm_members`).
			WithArgs(adminConv, adminActor).
			WillReturnResult(pgxmock.NewResult("UPDATE", 1))
		mock.ExpectCommit().WillReturnError(errors.New("db down"))
		mock.ExpectRollback()

		if _, err := storage.NewPGXDMStore(mock).LeaveGroupConversation(
			context.Background(), adminWS, adminConv, adminActor); err == nil {
			t.Fatal("expected the commit failure to propagate")
		}
	})
}
