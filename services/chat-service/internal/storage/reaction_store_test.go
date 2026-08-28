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

// toggleReactionColumns mirrors the toggle aggregate's projection, author
// arrays included.
var toggleReactionColumns = []string{
	"channel_id", "dm_id", "added", "emoji", "count", "reacted_by_me", "user_ids", "display_names",
}

const toggleReactionSQL = `(?s)WITH base AS MATERIALIZED.*final_reactions AS MATERIALIZED.*GROUP BY emoji`

func reactionInput() storage.ToggleReactionInput {
	return storage.ToggleReactionInput{WorkspaceID: "ws-1", UserID: "user-1", MessageID: "msg-1", Emoji: "👍"}
}

func reactionAdvisoryKey(input storage.ToggleReactionInput) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(input.MessageID + "\x00" + input.UserID + "\x00" + input.Emoji))
	return int64(h.Sum64()) //nolint:gosec // Matches the production advisory-lock key reinterpretation.
}

func TestPGXReactionStore_ToggleAddsReactionAndReturnsAggregate(t *testing.T) {
	mock := newMock(t)
	mock.ExpectBegin()
	input := reactionInput()
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).WithArgs(reactionAdvisoryKey(input)).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery(toggleReactionSQL).WithArgs("ws-1", "user-1", "msg-1", "👍", reactionAuthorPrefix).
		WillReturnRows(pgxmock.NewRows(toggleReactionColumns).
			AddRow("ch-1", "", true, "👍", 2, true, []string{"user-1", "user-2"}, []string{"Ana", "Bruno"}))
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
	mock.ExpectQuery(toggleReactionSQL).WithArgs("ws-1", "user-1", "msg-1", "👍", reactionAuthorPrefix).
		WillReturnRows(pgxmock.NewRows(toggleReactionColumns).
			AddRow("", "dm-1", false, "", 0, false, []string{}, []string{}))
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
	mock.ExpectQuery(toggleReactionSQL).WithArgs("ws-1", "user-1", "msg-1", "👍", reactionAuthorPrefix).
		WillReturnRows(pgxmock.NewRows(toggleReactionColumns))
	mock.ExpectRollback()

	_, err := storage.NewPGXReactionStore(mock).ToggleReaction(context.Background(), input)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected non-enumerating ErrNotFound, got %v", err)
	}
	checkExpectations(t, mock)
}
