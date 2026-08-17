package storage_test

import (
	"context"
	"errors"
	"hash/fnv"
	"testing"

	pgxmock "github.com/pashagolub/pgxmock/v2"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

const toggleReactionSQL = `(?s)WITH base AS MATERIALIZED.*final_reactions AS MATERIALIZED.*GROUP BY emoji`

func reactionInput() storage.ToggleReactionInput {
	return storage.ToggleReactionInput{WorkspaceID: "ws-1", UserID: "user-1", MessageID: "msg-1", Emoji: "👍"}
}

func reactionAdvisoryKey(input storage.ToggleReactionInput) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(input.MessageID + "\x00" + input.UserID))
	return int64(h.Sum64()) //nolint:gosec // Matches the production advisory-lock key reinterpretation.
}

func TestPGXReactionStore_ToggleAddsReactionAndReturnsAggregate(t *testing.T) {
	mock := newMock(t)
	mock.ExpectBegin()
	input := reactionInput()
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).WithArgs(reactionAdvisoryKey(input)).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery(toggleReactionSQL).WithArgs("ws-1", "user-1", "msg-1", "👍").
		WillReturnRows(pgxmock.NewRows([]string{"channel_id", "dm_id", "added", "limit_reached", "emoji", "count", "reacted_by_me"}).
			AddRow("ch-1", "", true, false, "👍", 2, true))
	mock.ExpectCommit()

	got, err := storage.NewPGXReactionStore(mock).ToggleReaction(context.Background(), input)
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
	input := reactionInput()
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).WithArgs(reactionAdvisoryKey(input)).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery(toggleReactionSQL).WithArgs("ws-1", "user-1", "msg-1", "👍").
		WillReturnRows(pgxmock.NewRows([]string{"channel_id", "dm_id", "added", "limit_reached", "emoji", "count", "reacted_by_me"}).
			AddRow("", "dm-1", false, false, "", 0, false))
	mock.ExpectCommit()

	got, err := storage.NewPGXReactionStore(mock).ToggleReaction(context.Background(), input)
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
	input := reactionInput()
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).WithArgs(reactionAdvisoryKey(input)).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery(toggleReactionSQL).WithArgs("ws-1", "user-1", "msg-1", "👍").
		WillReturnRows(pgxmock.NewRows([]string{"channel_id", "dm_id", "added", "limit_reached", "emoji", "count", "reacted_by_me"}))
	mock.ExpectRollback()

	_, err := storage.NewPGXReactionStore(mock).ToggleReaction(context.Background(), input)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected non-enumerating ErrNotFound, got %v", err)
	}
	checkExpectations(t, mock)
}

func TestPGXReactionStore_ToggleRejectsReactionBeyondLimit(t *testing.T) {
	mock := newMock(t)
	mock.ExpectBegin()
	input := reactionInput()
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).WithArgs(reactionAdvisoryKey(input)).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery(toggleReactionSQL).WithArgs("ws-1", "user-1", "msg-1", "👍").
		WillReturnRows(pgxmock.NewRows([]string{"channel_id", "dm_id", "added", "limit_reached", "emoji", "count", "reacted_by_me"}).
			AddRow("ch-1", "", false, true, "", 0, false))
	mock.ExpectRollback()

	_, err := storage.NewPGXReactionStore(mock).ToggleReaction(context.Background(), input)
	if !errors.Is(err, domain.ErrReactionLimitReached) {
		t.Fatalf("expected reaction limit error, got %v", err)
	}
	checkExpectations(t, mock)
}
