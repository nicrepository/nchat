package storage_test

import (
	"context"
	"errors"
	"fmt"
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

func TestPGXDMStore_CreateDirectConversation_CreatesCanonicalPair(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	now := time.Now()
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)INSERT INTO chat\.dm_conversations.*ON CONFLICT \(workspace_id, direct_pair_key\) WHERE type = 'direct'.*DO NOTHING`).
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
	result, err := store.CreateDirectConversation(context.Background(), storage.CreateDirectConversationInput{
		WorkspaceID:        "ws-1",
		CreatedBy:          "user-1",
		DirectPairKey:      "pair-key",
		ParticipantUserIDs: []string{"user-1", "user-2"},
	})
	if err != nil {
		t.Fatalf("CreateDirectConversation: %v", err)
	}
	if !result.Created || result.Conversation.ID != "dm-1" || result.Conversation.Type != domain.DMConversationTypeDirect {
		t.Fatalf("unexpected result: %+v", result)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXDMStore_CreateDirectConversation_ReturnsExistingCanonicalPair(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	now := time.Now()
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)INSERT INTO chat\.dm_conversations.*DO NOTHING`).
		WithArgs("ws-1", "user-1", "pair-key").
		WillReturnRows(pgxmock.NewRows(dmConversationCols()))
	mock.ExpectQuery(`(?s)UPDATE chat\.dm_conversations.*status = 'active'.*direct_pair_key = \$2`).
		WithArgs("ws-1", "pair-key").
		WillReturnRows(pgxmock.NewRows(dmConversationCols()).
			AddRow("dm-1", "ws-1", "direct", "", "active", "user-2", now, now))
	for _, userID := range []string{"user-1", "user-2"} {
		mock.ExpectExec(`INSERT INTO chat\.dm_members`).
			WithArgs("dm-1", "ws-1", userID).
			WillReturnResult(pgxmock.NewResult("INSERT", 1))
	}
	mock.ExpectCommit()

	result, err := storage.NewPGXDMStore(mock).CreateDirectConversation(context.Background(), storage.CreateDirectConversationInput{
		WorkspaceID:        "ws-1",
		CreatedBy:          "user-1",
		DirectPairKey:      "pair-key",
		ParticipantUserIDs: []string{"user-1", "user-2"},
	})
	if err != nil {
		t.Fatalf("CreateDirectConversation: %v", err)
	}
	if result.Created || result.Conversation.ID != "dm-1" {
		t.Fatalf("unexpected existing result: %+v", result)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXDMStore_CreateDirectConversation_RollsBackFailurePaths(t *testing.T) {
	now := time.Now()
	conversationRows := func() *pgxmock.Rows {
		return pgxmock.NewRows(dmConversationCols()).
			AddRow("dm-1", "ws-1", "direct", "", "active", "user-1", now, now)
	}
	for _, test := range []struct {
		name  string
		setup func(pgxmock.PgxPoolIface)
	}{
		{name: "begin", setup: func(mock pgxmock.PgxPoolIface) {
			mock.ExpectBegin().WillReturnError(errors.New("begin failed"))
		}},
		{name: "insert", setup: func(mock pgxmock.PgxPoolIface) {
			mock.ExpectBegin()
			mock.ExpectQuery(`INSERT INTO chat\.dm_conversations`).WithArgs("ws-1", "user-1", "pair-key").WillReturnError(errors.New("insert failed"))
			mock.ExpectRollback()
		}},
		{name: "recover existing", setup: func(mock pgxmock.PgxPoolIface) {
			mock.ExpectBegin()
			mock.ExpectQuery(`INSERT INTO chat\.dm_conversations`).WithArgs("ws-1", "user-1", "pair-key").WillReturnRows(pgxmock.NewRows(dmConversationCols()))
			mock.ExpectQuery(`UPDATE chat\.dm_conversations`).WithArgs("ws-1", "pair-key").WillReturnError(errors.New("update failed"))
			mock.ExpectRollback()
		}},
		{name: "member", setup: func(mock pgxmock.PgxPoolIface) {
			mock.ExpectBegin()
			mock.ExpectQuery(`INSERT INTO chat\.dm_conversations`).WithArgs("ws-1", "user-1", "pair-key").WillReturnRows(conversationRows())
			mock.ExpectExec(`INSERT INTO chat\.dm_members`).WithArgs("dm-1", "ws-1", "user-1").WillReturnError(errors.New("member failed"))
			mock.ExpectRollback()
		}},
		{name: "commit", setup: func(mock pgxmock.PgxPoolIface) {
			mock.ExpectBegin()
			mock.ExpectQuery(`INSERT INTO chat\.dm_conversations`).WithArgs("ws-1", "user-1", "pair-key").WillReturnRows(conversationRows())
			for _, userID := range []string{"user-1", "user-2"} {
				mock.ExpectExec(`INSERT INTO chat\.dm_members`).WithArgs("dm-1", "ws-1", userID).WillReturnResult(pgxmock.NewResult("INSERT", 1))
			}
			mock.ExpectCommit().WillReturnError(errors.New("commit failed"))
			mock.ExpectRollback()
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			mock, err := pgxmock.NewPool()
			if err != nil {
				t.Fatalf("pgxmock: %v", err)
			}
			defer mock.Close()
			test.setup(mock)
			_, err = storage.NewPGXDMStore(mock).CreateDirectConversation(context.Background(), storage.CreateDirectConversationInput{
				WorkspaceID: "ws-1", CreatedBy: "user-1", DirectPairKey: "pair-key",
				ParticipantUserIDs: []string{"user-1", "user-2"},
			})
			if err == nil {
				t.Fatal("expected error")
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet expectations: %v", err)
			}
		})
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

func TestPGXDMStore_CreateGroupConversation_PropagatesTransactionFailures(t *testing.T) {
	now := time.Now()
	for _, test := range []struct {
		name  string
		setup func(pgxmock.PgxPoolIface)
	}{
		{name: "begin", setup: func(mock pgxmock.PgxPoolIface) {
			mock.ExpectBegin().WillReturnError(errors.New("begin failed"))
		}},
		{name: "insert", setup: func(mock pgxmock.PgxPoolIface) {
			mock.ExpectBegin()
			mock.ExpectQuery(`INSERT INTO chat\.dm_conversations`).WithArgs("ws-1", pgxmock.AnyArg(), "user-1").WillReturnError(errors.New("insert failed"))
			mock.ExpectRollback()
		}},
		{name: "commit", setup: func(mock pgxmock.PgxPoolIface) {
			mock.ExpectBegin()
			mock.ExpectQuery(`INSERT INTO chat\.dm_conversations`).WithArgs("ws-1", pgxmock.AnyArg(), "user-1").
				WillReturnRows(pgxmock.NewRows(dmConversationCols()).AddRow("dm-1", "ws-1", "group", "", "active", "user-1", now, now))
			mock.ExpectExec(`INSERT INTO chat\.dm_members`).WithArgs("dm-1", "ws-1", "user-1").WillReturnResult(pgxmock.NewResult("INSERT", 1))
			mock.ExpectCommit().WillReturnError(errors.New("commit failed"))
			mock.ExpectRollback()
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			mock, err := pgxmock.NewPool()
			if err != nil {
				t.Fatalf("pgxmock: %v", err)
			}
			defer mock.Close()
			test.setup(mock)
			_, err = storage.NewPGXDMStore(mock).CreateGroupConversation(context.Background(), storage.CreateGroupConversationInput{
				WorkspaceID: "ws-1", CreatedBy: "user-1", ParticipantUserIDs: []string{"user-1"},
			})
			if err == nil {
				t.Fatal("expected error")
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet expectations: %v", err)
			}
		})
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

func TestPGXDMStore_ListVisibleConversationsByUser_PropagatesReadFailures(t *testing.T) {
	for _, test := range []struct {
		name string
		rows *pgxmock.Rows
		err  error
	}{
		{name: "query", err: errors.New("query failed")},
		{name: "scan", rows: pgxmock.NewRows([]string{"id"}).AddRow("dm-1")},
		{name: "rows", rows: pgxmock.NewRows(dmConversationCols()).AddRow("dm-1", "ws-1", "direct", "", "active", "user-1", time.Now(), time.Now()).RowError(0, errors.New("rows failed"))},
	} {
		t.Run(test.name, func(t *testing.T) {
			mock, err := pgxmock.NewPool()
			if err != nil {
				t.Fatalf("pgxmock: %v", err)
			}
			defer mock.Close()
			expectation := mock.ExpectQuery(`FROM chat\.dm_conversations dc`).WithArgs("ws-1", "user-1")
			if test.err != nil {
				expectation.WillReturnError(test.err)
			} else {
				expectation.WillReturnRows(test.rows)
			}
			if _, err := storage.NewPGXDMStore(mock).ListVisibleConversationsByUser(context.Background(), "ws-1", "user-1"); err == nil {
				t.Fatal("expected error")
			}
		})
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

func TestPGXDMStore_GetVisibleConversationByID_SuccessAndDatabaseError(t *testing.T) {
	for _, test := range []struct {
		name string
		rows *pgxmock.Rows
		err  error
	}{
		{name: "success", rows: pgxmock.NewRows(dmConversationCols()).AddRow("dm-1", "ws-1", "direct", "", "active", "user-1", time.Now(), time.Now())},
		{name: "database error", err: errors.New("database failed")},
	} {
		t.Run(test.name, func(t *testing.T) {
			mock, err := pgxmock.NewPool()
			if err != nil {
				t.Fatalf("pgxmock: %v", err)
			}
			defer mock.Close()
			expectation := mock.ExpectQuery(`FROM chat\.dm_conversations dc`).WithArgs("ws-1", "dm-1", "user-1")
			if test.err != nil {
				expectation.WillReturnError(test.err)
			} else {
				expectation.WillReturnRows(test.rows)
			}
			conversation, gotErr := storage.NewPGXDMStore(mock).GetVisibleConversationByID(context.Background(), "ws-1", "dm-1", "user-1")
			if test.err == nil && (gotErr != nil || conversation.ID != "dm-1") {
				t.Fatalf("conversation=%+v error=%v", conversation, gotErr)
			}
			if test.err != nil && !errors.Is(gotErr, test.err) {
				t.Fatalf("error=%v", gotErr)
			}
		})
	}
}

// ── ListVisibleConversationsWithParticipantIDs tests ──────────────────────────

func dmWithParticipantCols() []string {
	return []string{"id", "workspace_id", "type", "title", "status", "created_by", "created_at", "updated_at", "participant_ids", "counterpart_display_name"}
}

func TestPGXDMStore_ListVisibleConversationsWithParticipantIDs_ReturnsMemberOnly(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	now := time.Now()
	// SQL must join workspace, workspace_members, and dm_members for current user,
	// and resolve the counterpart display name in the same statement (no N+1).
	mock.ExpectQuery(`(?s)counterpart_display_name.*FROM chat\.dm_conversations dc.*JOIN chat\.workspaces w.*JOIN chat\.workspace_members wm.*wm\.user_id = \$2.*JOIN chat\.dm_members dm.*dm\.user_id = \$2`).
		WithArgs("ws-1", "user-1").
		WillReturnRows(pgxmock.NewRows(dmWithParticipantCols()).
			AddRow("dm-1", "ws-1", "direct", "", "active", "user-1", now, now, []string{"user-1", "user-2"}, "Juliane Lino"))

	store := storage.NewPGXDMStore(mock)
	convs, err := store.ListVisibleConversationsWithParticipantIDs(context.Background(), "ws-1", "user-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(convs) != 1 {
		t.Fatalf("expected 1 conversation, got %d", len(convs))
	}
	if convs[0].ID != "dm-1" {
		t.Fatalf("expected dm-1, got %q", convs[0].ID)
	}
	if len(convs[0].ParticipantIDs) != 2 {
		t.Fatalf("expected 2 participant IDs, got %d", len(convs[0].ParticipantIDs))
	}
	if convs[0].CounterpartDisplayName != "Juliane Lino" {
		t.Fatalf("expected counterpart display name, got %q", convs[0].CounterpartDisplayName)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// TestPGXDMStore_ListVisibleConversationsWithParticipantIDs_SingleQueryForManyDMs
// is the no-N+1 evidence: many conversations resolve their counterpart names
// from exactly one statement. pgxmock fails on any unexpected extra query.
func TestPGXDMStore_ListVisibleConversationsWithParticipantIDs_SingleQueryForManyDMs(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	now := time.Now()
	rows := pgxmock.NewRows(dmWithParticipantCols())
	want := []string{"Ana", "Bruno", "Caio", "Duda", "Elis"}
	for i, name := range want {
		id := fmt.Sprintf("dm-%d", i)
		peer := fmt.Sprintf("user-%d", i+2)
		rows.AddRow(id, "ws-1", "direct", "", "active", "user-1", now, now, []string{"user-1", peer}, name)
	}
	mock.ExpectQuery(`(?s)FROM chat\.dm_conversations dc`).WithArgs("ws-1", "user-1").WillReturnRows(rows)

	convs, err := storage.NewPGXDMStore(mock).ListVisibleConversationsWithParticipantIDs(context.Background(), "ws-1", "user-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(convs) != len(want) {
		t.Fatalf("expected %d conversations, got %d", len(want), len(convs))
	}
	for i, name := range want {
		if convs[i].CounterpartDisplayName != name {
			t.Fatalf("conversation %d: expected %q, got %q", i, name, convs[i].CounterpartDisplayName)
		}
	}
	// Any per-conversation follow-up query would be an unmet/unexpected expectation.
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// TestPGXDMStore_ListVisibleConversationsWithParticipantIDs_GroupHasNoCounterpart
// documents that the counterpart subquery is scoped to direct conversations, so
// group DMs never carry (or leak) a participant name.
func TestPGXDMStore_ListVisibleConversationsWithParticipantIDs_GroupHasNoCounterpart(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	now := time.Now()
	mock.ExpectQuery(`(?s)dc\.type = 'direct'.*FROM chat\.dm_conversations dc`).
		WithArgs("ws-1", "user-1").
		WillReturnRows(pgxmock.NewRows(dmWithParticipantCols()).
			AddRow("dm-grp", "ws-1", "group", "Equipe Infra", "active", "user-1", now, now, []string{"user-1", "user-2", "user-3"}, ""))

	convs, err := storage.NewPGXDMStore(mock).ListVisibleConversationsWithParticipantIDs(context.Background(), "ws-1", "user-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(convs) != 1 {
		t.Fatalf("expected 1 conversation, got %d", len(convs))
	}
	if convs[0].CounterpartDisplayName != "" {
		t.Fatalf("group DM must not carry a counterpart name, got %q", convs[0].CounterpartDisplayName)
	}
	if convs[0].Title != "Equipe Infra" {
		t.Fatalf("group DM must keep its title, got %q", convs[0].Title)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXDMStore_ListVisibleConversationsWithParticipantIDs_EmptyForNonMember(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	// Non-member: the dm_members join filters out all rows — return empty result.
	mock.ExpectQuery(`(?s)FROM chat\.dm_conversations dc.*JOIN chat\.dm_members dm.*dm\.user_id = \$2`).
		WithArgs("ws-1", "user-other").
		WillReturnRows(pgxmock.NewRows(dmWithParticipantCols()))

	store := storage.NewPGXDMStore(mock)
	convs, err := store.ListVisibleConversationsWithParticipantIDs(context.Background(), "ws-1", "user-other")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(convs) != 0 {
		t.Fatalf("expected no conversations for non-member, got %d", len(convs))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXDMStore_ListVisibleConversationsWithParticipantIDs_DBError_Propagates(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`(?s)FROM chat\.dm_conversations dc`).
		WithArgs("ws-1", "user-1").
		WillReturnError(errors.New("connection refused"))

	store := storage.NewPGXDMStore(mock)
	_, err = store.ListVisibleConversationsWithParticipantIDs(context.Background(), "ws-1", "user-1")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXDMStore_ListVisibleConversationsWithParticipantIDs_PropagatesRowFailures(t *testing.T) {
	for _, test := range []struct {
		name string
		rows *pgxmock.Rows
	}{
		{name: "scan", rows: pgxmock.NewRows([]string{"id"}).AddRow("dm-1")},
		{name: "rows", rows: pgxmock.NewRows(dmWithParticipantCols()).
			AddRow("dm-1", "ws-1", "direct", "", "active", "user-1", time.Now(), time.Now(), []string{"user-1", "user-2"}, "Ana").
			AddRow("dm-2", "ws-1", "direct", "", "active", "user-1", time.Now(), time.Now(), []string{"user-1", "user-3"}, "Bruno").
			RowError(1, errors.New("rows failed"))},
	} {
		t.Run(test.name, func(t *testing.T) {
			mock, err := pgxmock.NewPool()
			if err != nil {
				t.Fatalf("pgxmock: %v", err)
			}
			defer mock.Close()
			mock.ExpectQuery(`FROM chat\.dm_conversations dc`).WithArgs("ws-1", "user-1").WillReturnRows(test.rows)
			if _, err := storage.NewPGXDMStore(mock).ListVisibleConversationsWithParticipantIDs(context.Background(), "ws-1", "user-1"); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}
