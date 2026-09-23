package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// Twenty candidates gives the bounded popup more than two visible pages while
// keeping an empty-prefix request far from a workspace-directory download.
const (
	mentionSearchLimit    = 20
	mentionOutsideReserve = 5
)

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
		users, err = s.appendGroupCandidates(ctx, users, workspaceID, targetID, callerID, query)
		if err != nil {
			return SearchMentionsOutput{}, err
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
	users, err = s.appendChannelCandidates(ctx, users, workspaceID, targetID, callerID, query)
	if err != nil {
		return SearchMentionsOutput{}, err
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

func (s *MentionService) appendChannelCandidates(
	ctx context.Context, current []domain.MentionCandidate, workspaceID, channelID, callerID, query string,
) ([]domain.MentionCandidate, error) {
	candidates, err := s.members.searchChannelMemberCandidates(ctx, SearchChannelMemberCandidatesInput{
		WorkspaceID: workspaceID, ChannelID: channelID, CallerID: callerID,
		Query: query, Limit: mentionOutsideReserve,
	}, 0)
	if errors.Is(err, domain.ErrForbidden) {
		return current, nil
	}
	if err != nil {
		return nil, fmt.Errorf("search mention add candidates: %w", err)
	}
	return appendAutoAddCandidates(current, candidates, mentionSearchLimit), nil
}

func (s *MentionService) appendGroupCandidates(
	ctx context.Context, current []domain.MentionCandidate, workspaceID, conversationID, callerID, query string,
) ([]domain.MentionCandidate, error) {
	candidates, err := s.dms.SearchGroupParticipantCandidates(
		ctx, workspaceID, conversationID, callerID, query, mentionOutsideReserve,
	)
	if err != nil {
		return nil, fmt.Errorf("search mention add candidates: %w", err)
	}
	return appendAutoAddCandidates(current, candidates, mentionSearchLimit), nil
}

func appendAutoAddCandidates(
	current []domain.MentionCandidate, candidates []domain.DMCandidate, limit int,
) []domain.MentionCandidate {
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	if keep := limit - len(candidates); len(current) > keep {
		current = current[:keep]
	}
	for _, candidate := range candidates {
		if len(current) >= limit {
			break
		}
		current = append(current, domain.MentionCandidate{
			Type: domain.MentionTypeUser, ID: candidate.UserID,
			Label: candidate.DisplayName, WillBeAdded: true,
		})
	}
	return current
}
