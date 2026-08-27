package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// quickReactionEmojiList is the small, product-curated row offered above a
// message before anyone opens the full picker (issue #496). It is a shortcut,
// not a policy: what a reaction may be is decided by the embedded Unicode
// catalog, and this list is a subset of it.
var quickReactionEmojiList = []string{
	"👍", "❤️", "😂", "🎉", "😮", "😢", "👎", "🔥", "🙌", "👏",
	"✅", "👀", "🚀", "💯", "😍", "🤔", "🙏", "💪", "🤝", "😄",
}

// QuickReactionEmojis returns a copy so callers cannot mutate the row shared by
// the frontend configuration route.
func QuickReactionEmojis() []string {
	return append([]string(nil), quickReactionEmojiList...)
}

type ToggleReactionInput struct {
	WorkspaceID string
	UserID      string
	MessageID   string
	Emoji       string
}

type ReactionService struct{ reactions storage.ReactionStore }

func NewReactionService(reactions storage.ReactionStore) *ReactionService {
	return &ReactionService{reactions: reactions}
}

func (s *ReactionService) ToggleReaction(ctx context.Context, input ToggleReactionInput) (storage.ToggleReactionResult, error) {
	input.WorkspaceID = strings.TrimSpace(input.WorkspaceID)
	input.UserID = strings.TrimSpace(input.UserID)
	input.MessageID = strings.TrimSpace(input.MessageID)
	if input.WorkspaceID == "" || input.UserID == "" || input.MessageID == "" {
		return storage.ToggleReactionResult{}, fmt.Errorf("%w: workspace, user and message are required", domain.ErrInvalidInput)
	}
	if !IsAllowedReactionEmoji(input.Emoji) {
		return storage.ToggleReactionResult{}, fmt.Errorf("%w: unsupported emoji", domain.ErrInvalidInput)
	}
	return s.reactions.ToggleReaction(ctx, storage.ToggleReactionInput{
		WorkspaceID: input.WorkspaceID,
		UserID:      input.UserID,
		MessageID:   input.MessageID,
		Emoji:       input.Emoji,
	})
}
