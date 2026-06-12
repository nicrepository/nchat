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

func dmConversationCols() []string {
	return []string{"id", "workspace_id", "type", "title", "status", "created_by", "created_at", "updated_at"}
}

func TestPGXDMStore_CreateDirectConversation_UpsertsAndReactivatesCanonicalPair(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	now := time.Now()
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)INSERT INTO chat\.dm_conversations.*ON CONFLICT \(workspace_id, direct_pair_key\) WHERE type = 'direct'.*DO UPDATE SET status = 'active'`).
		WithArgs("ws-1", "user-1", "pair-key").
		WillReturnRows(pgxmock.NewRows(dmConversationCols()).
			AddRow("dm-1", "ws-1", "direct", "", "active", "user-1", now, now))
	mock.ExpectExec(`INSERT INTO chat\.dm_members`).
		WithArgs("dm-1", "ws-1", "user-1").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec(`INSERT INTO chat\.dm_members`).
		WithArgs("dm-1", "ws-1", "user-2").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()

	store := storage.NewPGXDMStore(mock)
	conversation, err := store.CreateDirectConversation(context.Background(), storage.CreateDirectConversationInput{
		WorkspaceID:        "ws-1",
		CreatedBy:          "user-1",
		DirectPairKey:      "pair-key",
		ParticipantUserIDs: []string{"user-1", "user-2"},
	})
	if err != nil {
		t.Fatalf("CreateDirectConversation: %v", err)
	}
	if conversation.ID != "dm-1" || conversation.Type != domain.DMConversationTypeDirect {
		t.Fatalf("unexpected conversation: %+v", conversation)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXDMStore_CreateGroupConversation_CommitsConversationAndMembers(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	now := time.Now()
	mock.ExpectBegin()
	mock.ExpectQuery(`INSERT INTO chat\.dm_conversations`).
		WithArgs("ws-1", pgxmock.AnyArg(), "user-1").
		WillReturnRows(pgxmock.NewRows(dmConversationCols()).
			AddRow("dm-group", "ws-1", "group", "Project", "active", "user-1", now, now))
	for _, userID := range []string{"user-1", "user-2", "user-3"} {
		mock.ExpectExec(`INSERT INTO chat\.dm_members`).
			WithArgs("dm-group", "ws-1", userID).
			WillReturnResult(pgxmock.NewResult("INSERT", 1))
	}
	mock.ExpectCommit()

	store := storage.NewPGXDMStore(mock)
	conversation, err := store.CreateGroupConversation(context.Background(), storage.CreateGroupConversationInput{
		WorkspaceID:        "ws-1",
		CreatedBy:          "user-1",
		Title:              "Project",
		ParticipantUserIDs: []string{"user-1", "user-2", "user-3"},
	})
	if err != nil {
		t.Fatalf("CreateGroupConversation: %v", err)
	}
	if conversation.ID != "dm-group" || conversation.Title != "Project" {
		t.Fatalf("unexpected conversation: %+v", conversation)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXDMStore_CreateGroupConversation_RollsBackWhenMemberInsertFails(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	now := time.Now()
	mock.ExpectBegin()
	mock.ExpectQuery(`INSERT INTO chat\.dm_conversations`).
		WithArgs("ws-1", pgxmock.AnyArg(), "user-1").
		WillReturnRows(pgxmock.NewRows(dmConversationCols()).
			AddRow("dm-group", "ws-1", "group", "", "active", "user-1", now, now))
	mock.ExpectExec(`INSERT INTO chat\.dm_members`).
		WithArgs("dm-group", "ws-1", "user-1").
		WillReturnError(errors.New("member insert failed"))
	mock.ExpectRollback()

	store := storage.NewPGXDMStore(mock)
	_, err = store.CreateGroupConversation(context.Background(), storage.CreateGroupConversationInput{
		WorkspaceID:        "ws-1",
		CreatedBy:          "user-1",
		ParticipantUserIDs: []string{"user-1", "user-2", "user-3"},
	})
	if err == nil {
		t.Fatal("expected member insert error")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXDMStore_CreateGroupConversation_RollsBackWhenParticipantIsNotActiveWorkspaceMember(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	now := time.Now()
	mock.ExpectBegin()
	mock.ExpectQuery(`INSERT INTO chat\.dm_conversations`).
		WithArgs("ws-1", pgxmock.AnyArg(), "user-1").
		WillReturnRows(pgxmock.NewRows(dmConversationCols()).
			AddRow("dm-group", "ws-1", "group", "", "active", "user-1", now, now))
	mock.ExpectExec(`INSERT INTO chat\.dm_members`).
		WithArgs("dm-group", "ws-1", "user-1").
		WillReturnResult(pgxmock.NewResult("INSERT", 0))
	mock.ExpectRollback()

	store := storage.NewPGXDMStore(mock)
	_, err = store.CreateGroupConversation(context.Background(), storage.CreateGroupConversationInput{
		WorkspaceID:        "ws-1",
		CreatedBy:          "user-1",
		ParticipantUserIDs: []string{"user-1", "user-2", "user-3"},
	})
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("expected ErrForbidden, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXDMStore_ListVisibleConversationsByUser_UsesSQLVisibility(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	now := time.Now()
	mock.ExpectQuery(`(?s)FROM chat\.dm_conversations dc.*JOIN chat\.workspaces w.*w\.status = 'active'.*JOIN chat\.workspace_members wm.*wm\.status = 'active'.*JOIN chat\.dm_members dm.*dm\.status = 'active'.*dc\.status = 'active'`).
		WithArgs("ws-1", "user-1").
		WillReturnRows(pgxmock.NewRows(dmConversationCols()).
			AddRow("dm-1", "ws-1", "direct", "", "active", "user-1", now, now))

	store := storage.NewPGXDMStore(mock)
	conversations, err := store.ListVisibleConversationsByUser(context.Background(), "ws-1", "user-1")
	if err != nil {
		t.Fatalf("ListVisibleConversationsByUser: %v", err)
	}
	if len(conversations) != 1 || conversations[0].ID != "dm-1" {
		t.Fatalf("unexpected conversations: %+v", conversations)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXDMStore_GetVisibleConversationByID_NotFound(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`(?s)FROM chat\.dm_conversations dc.*JOIN chat\.workspace_members wm.*JOIN chat\.dm_members dm.*dc\.id = \$2.*dc\.status = 'active'`).
		WithArgs("ws-1", "dm-hidden", "user-1").
		WillReturnError(pgx.ErrNoRows)

	store := storage.NewPGXDMStore(mock)
	_, err = store.GetVisibleConversationByID(context.Background(), "ws-1", "dm-hidden", "user-1")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}
