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

// A composed sequence must reach storage whole: the picker offers them, and
// truncating one to its first code point would store a different reaction.
func TestReactionService_AcceptsComposedSequencesVerbatim(t *testing.T) {
	for _, emoji := range []string{"👨‍👩‍👧‍👦", "👍🏿", "🏳️‍🌈"} {
		store := &fakeReactionStore{result: storage.ToggleReactionResult{MessageID: "msg-1", Added: true}}
		svc := service.NewReactionService(store)
		if _, err := svc.ToggleReaction(context.Background(), service.ToggleReactionInput{
			WorkspaceID: "ws-1", UserID: "user-1", MessageID: "msg-1", Emoji: emoji,
		}); err != nil {
			t.Fatalf("ToggleReaction(%q): %v", emoji, err)
		}
		if store.input.Emoji != emoji {
			t.Fatalf("expected %q to reach storage unchanged, got %q", emoji, store.input.Emoji)
		}
	}
}

func TestReactionService_RejectsUncataloguedValuesBeforeStorage(t *testing.T) {
	for _, emoji := range []string{"👍 boa", "👍👍", ":+1:", "🏻", "❤"} {
		store := &fakeReactionStore{err: errors.New("must not be called")}
		svc := service.NewReactionService(store)
		_, err := svc.ToggleReaction(context.Background(), service.ToggleReactionInput{
			WorkspaceID: "ws-1", UserID: "user-1", MessageID: "msg-1", Emoji: emoji,
		})
		if !errors.Is(err, domain.ErrInvalidInput) {
			t.Fatalf("expected %q to be refused, got %v", emoji, err)
		}
		if store.input.MessageID != "" {
			t.Fatalf("uncatalogued %q reached storage", emoji)
		}
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

func TestQuickReactionEmojis_IsCuratedAndCatalogued(t *testing.T) {
	emojis := service.QuickReactionEmojis()
	if len(emojis) < 16 || len(emojis) > 20 {
		t.Fatalf("expected 16-20 emojis, got %d", len(emojis))
	}
	for _, emoji := range emojis {
		if !service.IsAllowedReactionEmoji(emoji) {
			t.Fatalf("quick reaction %q is not in the emoji catalog", emoji)
		}
	}
}
