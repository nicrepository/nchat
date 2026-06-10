package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// PermissionService resolves channel read/write access for a user.
type PermissionService struct {
	members  storage.MemberStore
	channels storage.ChannelStore
}

func NewPermissionService(members storage.MemberStore, channels storage.ChannelStore) *PermissionService {
	return &PermissionService{members: members, channels: channels}
}

// ListVisibleChannels returns all active channels in workspaceID that userID may read.
// Non-members receive an empty slice (not an error).
func (s *PermissionService) ListVisibleChannels(ctx context.Context, workspaceID, userID string) ([]domain.Channel, error) {
	wm, err := s.members.GetWorkspaceMember(ctx, workspaceID, userID)
	if errors.Is(err, domain.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get workspace member: %w", err)
	}

	all, err := s.channels.ListChannelsByWorkspace(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list channels: %w", err)
	}

	visible := make([]domain.Channel, 0, len(all))
	for _, ch := range all {
		var cm *domain.ChannelMember
		if ch.Type == domain.ChannelTypePrivate {
			got, err := s.members.GetChannelMember(ctx, ch.ID, userID)
			if err == nil {
				cm = &got
			}
		}
		if domain.CanReadChannel(&wm, cm, ch) {
			visible = append(visible, ch)
		}
	}
	return visible, nil
}

// CanRead reports whether userID may read channelID in workspaceID.
func (s *PermissionService) CanRead(ctx context.Context, workspaceID, channelID, userID string) (bool, error) {
	wm, err := s.members.GetWorkspaceMember(ctx, workspaceID, userID)
	if errors.Is(err, domain.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("get workspace member: %w", err)
	}

	ch, err := s.channels.GetChannelByID(ctx, channelID)
	if errors.Is(err, domain.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("get channel: %w", err)
	}

	var cm *domain.ChannelMember
	if ch.Type == domain.ChannelTypePrivate {
		got, err := s.members.GetChannelMember(ctx, channelID, userID)
		if err == nil {
			cm = &got
		}
	}
	return domain.CanReadChannel(&wm, cm, ch), nil
}

// CanWrite reports whether userID may post to channelID in workspaceID.
// For MVP, write access follows the same rules as read access.
func (s *PermissionService) CanWrite(ctx context.Context, workspaceID, channelID, userID string) (bool, error) {
	return s.CanRead(ctx, workspaceID, channelID, userID)
}
