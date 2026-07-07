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
	visible, err := s.channels.ListVisibleChannelsByUser(ctx, workspaceID, userID)
	if err != nil {
		return nil, fmt.Errorf("list visible channels: %w", err)
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
	if wm.Status != domain.MemberStatusActive {
		return false, nil
	}

	ch, err := s.channels.GetChannelByIDInWorkspace(ctx, workspaceID, channelID)
	if errors.Is(err, domain.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("get channel: %w", err)
	}

	var cm *domain.ChannelMember
	if ch.Type == domain.ChannelTypePrivate {
		got, err := s.members.GetChannelMember(ctx, channelID, userID)
		if errors.Is(err, domain.ErrNotFound) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("get channel member: %w", err)
		}
		cm = &got
	}
	return domain.CanReadChannel(&wm, cm, ch), nil
}

// CanWrite reports whether userID may post to channelID in workspaceID.
// For MVP, write access follows the same rules as read access.
func (s *PermissionService) CanWrite(ctx context.Context, workspaceID, channelID, userID string) (bool, error) {
	return s.CanRead(ctx, workspaceID, channelID, userID)
}

// CanModerateChannel resolves both read and pin/moderate permission for a
// channel action in a single membership lookup (RF-05). readable distinguishes
// "channel not visible" (→ 404, non-enumerating) from "visible but role too
// low" (→ 403). A user who cannot read the channel is never a moderator.
func (s *PermissionService) CanModerateChannel(ctx context.Context, workspaceID, channelID, userID string) (readable, moderate bool, err error) {
	wm, err := s.members.GetWorkspaceMember(ctx, workspaceID, userID)
	if errors.Is(err, domain.ErrNotFound) {
		return false, false, nil
	}
	if err != nil {
		return false, false, fmt.Errorf("get workspace member: %w", err)
	}

	ch, err := s.channels.GetChannelByIDInWorkspace(ctx, workspaceID, channelID)
	if errors.Is(err, domain.ErrNotFound) {
		return false, false, nil
	}
	if err != nil {
		return false, false, fmt.Errorf("get channel: %w", err)
	}

	var cm *domain.ChannelMember
	if got, err := s.members.GetChannelMember(ctx, channelID, userID); err == nil {
		cm = &got
	} else if !errors.Is(err, domain.ErrNotFound) {
		return false, false, fmt.Errorf("get channel member: %w", err)
	}

	return domain.CanReadChannel(&wm, cm, ch), domain.CanPinInChannel(&wm, cm, ch), nil
}
