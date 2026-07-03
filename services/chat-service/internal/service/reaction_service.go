package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

var allowedReactionEmojis = map[string]struct{}{
	"👍": {}, "❤️": {}, "😂": {}, "🎉": {}, "😮": {}, "😢": {}, "👎": {}, "🔥": {},
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
