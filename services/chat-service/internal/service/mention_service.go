package service

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// Twenty candidates gives the bounded popup more than two visible pages while
// keeping an empty-prefix request far from a workspace-directory download.
const mentionSearchLimit = 20

type SearchMentionsInput struct {
	WorkspaceID string
	TargetType  string
	TargetID    string
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
	dms         storage.DMStore
}

func NewMentionService(members *MemberService, permissions *PermissionService, dms storage.DMStore) *MentionService {
	return &MentionService{members: members, permissions: permissions, dms: dms}
}

func (s *MentionService) SearchMentions(ctx context.Context, input SearchMentionsInput) (SearchMentionsOutput, error) {
	workspaceID := strings.TrimSpace(input.WorkspaceID)
	targetType := strings.TrimSpace(input.TargetType)
	targetID := strings.TrimSpace(input.TargetID)
	callerID := strings.TrimSpace(input.CallerID)
	query := strings.TrimSpace(input.Query)
	if workspaceID == "" || targetID == "" || callerID == "" || len([]rune(query)) > 64 {
		return SearchMentionsOutput{}, fmt.Errorf("%w: invalid mention search", domain.ErrInvalidInput)
	}
	if targetType == "dm" {
		if s.dms == nil {
			return SearchMentionsOutput{}, domain.ErrNotFound
		}
		conversation, err := s.dms.GetVisibleConversationByID(ctx, workspaceID, targetID, callerID)
		if err != nil {
			return SearchMentionsOutput{}, err
		}
		if conversation.Type != domain.DMConversationTypeGroup {
			return SearchMentionsOutput{}, domain.ErrNotFound
		}
		users, err := s.members.SearchDMConversationMembers(ctx, workspaceID, targetID, callerID, query, mentionSearchLimit)
		if err != nil {
			return SearchMentionsOutput{}, fmt.Errorf("search dm conversation members: %w", err)
		}
		return SearchMentionsOutput{Users: users, Channels: []domain.MentionCandidate{}}, nil
	}
	if targetType != "channel" {
		return SearchMentionsOutput{}, fmt.Errorf("%w: invalid mention target", domain.ErrInvalidInput)
	}
	allowed, err := s.permissions.CanRead(ctx, workspaceID, targetID, callerID)
	if err != nil {
		return SearchMentionsOutput{}, err
	}
	if !allowed {
		return SearchMentionsOutput{}, domain.ErrNotFound
	}
	users, err := s.members.SearchChannelMembers(ctx, workspaceID, targetID, query, mentionSearchLimit)
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
	}
	sort.Slice(channels, func(i, j int) bool {
		left, right := strings.ToLower(channels[i].Label), strings.ToLower(channels[j].Label)
		return left < right || left == right && channels[i].ID < channels[j].ID
	})
	if len(channels) > mentionSearchLimit {
		channels = channels[:mentionSearchLimit]
	}
	return SearchMentionsOutput{Users: users, Channels: channels}, nil
}
