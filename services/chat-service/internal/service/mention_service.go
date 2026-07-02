package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

const mentionSearchLimit = 10

type SearchMentionsInput struct {
	WorkspaceID string
	ChannelID   string
	CallerID    string
	Query       string
}

type SearchMentionsOutput struct {
	Users    []domain.MentionCandidate
	Channels []domain.MentionCandidate
}

// MentionService composes existing membership and permission rules for autocomplete.
type MentionService struct {
	members     *MemberService
	permissions *PermissionService
}

func NewMentionService(members *MemberService, permissions *PermissionService) *MentionService {
	return &MentionService{members: members, permissions: permissions}
}

func (s *MentionService) SearchMentions(ctx context.Context, input SearchMentionsInput) (SearchMentionsOutput, error) {
	workspaceID := strings.TrimSpace(input.WorkspaceID)
	channelID := strings.TrimSpace(input.ChannelID)
	callerID := strings.TrimSpace(input.CallerID)
	query := strings.TrimSpace(input.Query)
	if workspaceID == "" || channelID == "" || callerID == "" || len([]rune(query)) > 64 {
		return SearchMentionsOutput{}, fmt.Errorf("%w: invalid mention search", domain.ErrInvalidInput)
	}
	allowed, err := s.permissions.CanRead(ctx, workspaceID, channelID, callerID)
	if err != nil {
		return SearchMentionsOutput{}, err
	}
	if !allowed {
		return SearchMentionsOutput{}, domain.ErrNotFound
	}
	users, err := s.members.SearchChannelMembers(ctx, workspaceID, channelID, query, mentionSearchLimit)
	if err != nil {
		return SearchMentionsOutput{}, fmt.Errorf("search channel members: %w", err)
	}
	visible, err := s.permissions.ListVisibleChannels(ctx, workspaceID, callerID)
	if err != nil {
		return SearchMentionsOutput{}, err
	}
	prefix := strings.ToLower(query)
	channels := make([]domain.MentionCandidate, 0, mentionSearchLimit)
	for _, channel := range visible {
		label := channel.DisplayName
		if label == "" {
			label = channel.Slug
		}
		if !strings.HasPrefix(strings.ToLower(label), prefix) && !strings.HasPrefix(strings.ToLower(channel.Slug), prefix) {
			continue
		}
		channels = append(channels, domain.MentionCandidate{Type: domain.MentionTypeChannel, ID: channel.ID, Label: label})
		if len(channels) == mentionSearchLimit {
			break
		}
	}
	return SearchMentionsOutput{Users: users, Channels: channels}, nil
}
