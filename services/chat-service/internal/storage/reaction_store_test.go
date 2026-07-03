package storage_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	pgxmock "github.com/pashagolub/pgxmock/v2"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

const (
	authorizedReactionSQL = `(?s)FROM chat\.messages m.*chat\.workspace_members wm.*wm\.status = 'active'.*chat\.channels c.*chat\.channel_members cm.*chat\.dm_conversations dc.*chat\.dm_members dm.*m\.status = 'active'.*FOR UPDATE OF m`
	aggregateReactionSQL  = `(?s)FROM chat\.message_reactions.*AND EXISTS.*chat\.messages.*workspace_id = \$3.*GROUP BY emoji`
)

func reactionInput() storage.ToggleReactionInput {
	return storage.ToggleReactionInput{WorkspaceID: "ws-1", UserID: "user-1", MessageID: "msg-1", Emoji: "👍"}
}

func TestPGXReactionStore_ToggleAddsReactionAndReturnsAggregate(t *testing.T) {
	mock := newMock(t)
	mock.ExpectBegin()
	mock.ExpectQuery(authorizedReactionSQL).WithArgs("ws-1", "user-1", "msg-1").
		WillReturnRows(pgxmock.NewRows([]string{"channel_id", "dm_id"}).AddRow("ch-1", ""))
	mock.ExpectExec(`DELETE FROM chat\.message_reactions`).WithArgs("msg-1", "user-1", "👍").
		WillReturnResult(pgxmock.NewResult("DELETE", 0))
	mock.ExpectExec(`INSERT INTO chat\.message_reactions`).WithArgs("msg-1", "user-1", "👍").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectQuery(aggregateReactionSQL).WithArgs("msg-1", "user-1", "ws-1").
		WillReturnRows(pgxmock.NewRows([]string{"emoji", "count", "reacted_by_me"}).AddRow("👍", 2, true))
	mock.ExpectCommit()

	got, err := storage.NewPGXReactionStore(mock).ToggleReaction(context.Background(), reactionInput())
	if err != nil {
		t.Fatalf("ToggleReaction: %v", err)
	}
	if !got.Added || got.ChannelID != "ch-1" || len(got.Reactions) != 1 || got.Reactions[0].Count != 2 {
		t.Fatalf("unexpected result: %+v", got)
	}
	checkExpectations(t, mock)
}

func TestPGXReactionStore_ToggleRemovesExistingReaction(t *testing.T) {
	mock := newMock(t)
	mock.ExpectBegin()
	mock.ExpectQuery(authorizedReactionSQL).WithArgs("ws-1", "user-1", "msg-1").
		WillReturnRows(pgxmock.NewRows([]string{"channel_id", "dm_id"}).AddRow("", "dm-1"))
	mock.ExpectExec(`DELETE FROM chat\.message_reactions`).WithArgs("msg-1", "user-1", "👍").
		WillReturnResult(pgxmock.NewResult("DELETE", 1))
	mock.ExpectQuery(aggregateReactionSQL).WithArgs("msg-1", "user-1", "ws-1").
		WillReturnRows(pgxmock.NewRows([]string{"emoji", "count", "reacted_by_me"}))
	mock.ExpectCommit()

	got, err := storage.NewPGXReactionStore(mock).ToggleReaction(context.Background(), reactionInput())
	if err != nil {
		t.Fatalf("ToggleReaction: %v", err)
	}
	if got.Added || got.DMID != "dm-1" || len(got.Reactions) != 0 {
		t.Fatalf("unexpected result: %+v", got)
	}
	checkExpectations(t, mock)
}

func TestPGXReactionStore_InvisibleMissingOrDeletedMessageIsNotFound(t *testing.T) {
	mock := newMock(t)
	mock.ExpectBegin()
	mock.ExpectQuery(authorizedReactionSQL).WithArgs("ws-1", "user-1", "msg-1").
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectRollback()

	_, err := storage.NewPGXReactionStore(mock).ToggleReaction(context.Background(), reactionInput())
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected non-enumerating ErrNotFound, got %v", err)
	}
	checkExpectations(t, mock)
}
