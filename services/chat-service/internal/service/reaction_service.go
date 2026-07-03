package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

var allowedReactionEmojiList = []string{
	"👍", "❤️", "😂", "🎉", "😮", "😢", "👎", "🔥", "🙌", "👏",
	"✅", "👀", "🚀", "💯", "😍", "🤔", "🙏", "💪", "🤝", "😄",
}

var allowedReactionEmojis = func() map[string]struct{} {
	allowed := make(map[string]struct{}, len(allowedReactionEmojiList))
	for _, emoji := range allowedReactionEmojiList {
		allowed[emoji] = struct{}{}
	}
	return allowed
}()

// AllowedReactionEmojis returns a copy so callers cannot mutate validation
// policy shared by the WebSocket handler and the frontend configuration route.
func AllowedReactionEmojis() []string {
	return append([]string(nil), allowedReactionEmojiList...)
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
	if _, ok := allowedReactionEmojis[input.Emoji]; !ok {
		return storage.ToggleReactionResult{}, fmt.Errorf("%w: unsupported emoji", domain.ErrInvalidInput)
	}
	return s.reactions.ToggleReaction(ctx, storage.ToggleReactionInput{
		WorkspaceID: input.WorkspaceID,
		UserID:      input.UserID,
		MessageID:   input.MessageID,
		Emoji:       input.Emoji,
	})
}
