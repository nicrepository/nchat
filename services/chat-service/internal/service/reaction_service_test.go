package service_test

import (
	"context"
	"errors"
	"testing"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

type fakeReactionStore struct {
	input  storage.ToggleReactionInput
	result storage.ToggleReactionResult
	err    error
}

type sequentialReactionStore struct{ active bool }

func (s *sequentialReactionStore) ToggleReaction(_ context.Context, input storage.ToggleReactionInput) (storage.ToggleReactionResult, error) {
	s.active = !s.active
	return storage.ToggleReactionResult{MessageID: input.MessageID, Added: s.active}, nil
}

func (f *fakeReactionStore) ToggleReaction(_ context.Context, input storage.ToggleReactionInput) (storage.ToggleReactionResult, error) {
	f.input = input
	return f.result, f.err
}

func TestReactionService_ToggleValidEmojiUsesAuthenticatedIdentity(t *testing.T) {
	store := &fakeReactionStore{result: storage.ToggleReactionResult{MessageID: "msg-1", Added: true}}
	svc := service.NewReactionService(store)

	got, err := svc.ToggleReaction(context.Background(), service.ToggleReactionInput{
		WorkspaceID: "ws-1", UserID: "user-1", MessageID: "msg-1", Emoji: "👍",
	})
	if err != nil {
		t.Fatalf("ToggleReaction: %v", err)
	}
	if !got.Added || store.input.UserID != "user-1" || store.input.Emoji != "👍" {
		t.Fatalf("unexpected result/input: got=%+v input=%+v", got, store.input)
	}
}

func TestReactionService_RejectsInvalidEmojiBeforeStorage(t *testing.T) {
	store := &fakeReactionStore{err: errors.New("must not be called")}
	svc := service.NewReactionService(store)

	_, err := svc.ToggleReaction(context.Background(), service.ToggleReactionInput{
		WorkspaceID: "ws-1", UserID: "user-1", MessageID: "msg-1", Emoji: "<script>",
	})
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
	if store.input.MessageID != "" {
		t.Fatal("invalid emoji reached storage")
	}
}

func TestReactionService_RejectsMissingIdentityOrMessage(t *testing.T) {
	store := &fakeReactionStore{}
	svc := service.NewReactionService(store)
	for _, input := range []service.ToggleReactionInput{
		{UserID: "u", MessageID: "m", Emoji: "👍"},
		{WorkspaceID: "w", MessageID: "m", Emoji: "👍"},
		{WorkspaceID: "w", UserID: "u", Emoji: "👍"},
	} {
		if _, err := svc.ToggleReaction(context.Background(), input); !errors.Is(err, domain.ErrInvalidInput) {
			t.Fatalf("expected invalid input for %+v, got %v", input, err)
		}
	}
}

func TestReactionService_SequentialDoubleToggleLeavesReactionRemoved(t *testing.T) {
	// True concurrent correctness depends on the store's FOR UPDATE OF m lock and
	// belongs in a PostgreSQL integration test rather than this service unit test.
	store := &sequentialReactionStore{}
	svc := service.NewReactionService(store)
	input := service.ToggleReactionInput{WorkspaceID: "ws-1", UserID: "user-1", MessageID: "msg-1", Emoji: "👍"}
	for range 2 {
		if _, err := svc.ToggleReaction(context.Background(), input); err != nil {
			t.Fatalf("ToggleReaction: %v", err)
		}
	}
	if store.active {
		t.Fatal("two serialized toggles must leave the reaction removed")
	}
}

func TestAllowedReactionEmojis_IsCuratedAndFitsDatabaseConstraint(t *testing.T) {
	emojis := service.AllowedReactionEmojis()
	if len(emojis) < 16 || len(emojis) > 20 {
		t.Fatalf("expected 16-20 emojis, got %d", len(emojis))
	}
	for _, emoji := range emojis {
		if n := len([]rune(emoji)); n < 1 || n > 8 {
			t.Fatalf("emoji %q has %d code points; database permits 1-8", emoji, n)
		}
	}
}
