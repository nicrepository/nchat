package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// MemberService handles workspace and channel membership use cases.
type MemberService struct {
	members    storage.MemberStore
	channels   storage.ChannelStore
	workspaces storage.WorkspaceStore
}

func NewMemberService(members storage.MemberStore, channels storage.ChannelStore, workspaces storage.WorkspaceStore) *MemberService {
	return &MemberService{members: members, channels: channels, workspaces: workspaces}
}

// JoinWorkspace adds userID to workspaceID with the given role. If the user is
// already a member, the existing membership record is returned without error.
func (s *MemberService) JoinWorkspace(ctx context.Context, workspaceID, userID string, role domain.WorkspaceRole) (domain.WorkspaceMember, error) {
	m, err := s.members.AddWorkspaceMember(ctx, workspaceID, userID, role)
	if errors.Is(err, domain.ErrAlreadyMember) {
		return s.members.GetWorkspaceMember(ctx, workspaceID, userID)
	}
	return m, err
}

// JoinChannel adds userID to channelID with the given role. If the user is
// already a channel member, the existing record is returned without error.
func (s *MemberService) JoinChannel(ctx context.Context, channelID, userID string, role domain.ChannelRole) (domain.ChannelMember, error) {
	if role != domain.ChannelRoleMember {
		return domain.ChannelMember{}, fmt.Errorf("%w: channel members can only be added with member role", domain.ErrInvalidInput)
	}

	channel, err := s.channels.GetChannelByID(ctx, channelID)
	if err != nil {
		return domain.ChannelMember{}, fmt.Errorf("get channel: %w", err)
	}
	workspace, err := s.workspaces.GetWorkspaceByID(ctx, channel.WorkspaceID)
	if err != nil {
		return domain.ChannelMember{}, fmt.Errorf("get channel workspace: %w", err)
	}
	if workspace.Status != domain.WorkspaceStatusActive {
		return domain.ChannelMember{}, domain.ErrForbidden
	}

	workspaceMember, err := s.members.GetWorkspaceMember(ctx, channel.WorkspaceID, userID)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.ChannelMember{}, domain.ErrForbidden
	}
	if err != nil {
		return domain.ChannelMember{}, fmt.Errorf("get workspace member: %w", err)
	}
	if workspaceMember.Status != domain.MemberStatusActive || workspaceMember.WorkspaceID != channel.WorkspaceID || workspaceMember.UserID != userID {
		return domain.ChannelMember{}, domain.ErrForbidden
	}

	m, err := s.members.AddChannelMember(ctx, channelID, userID, domain.ChannelRoleMember)
	if errors.Is(err, domain.ErrAlreadyMember) {
		return s.members.GetChannelMember(ctx, channelID, userID)
	}
	return m, err
}

// EnsureGeneralMembership adds userID to the #geral channel for workspaceID if
// not already a member. It is idempotent and should be called after JoinWorkspace.
// Returns nil if no #geral channel exists (safe during bootstrap).
func (s *MemberService) EnsureGeneralMembership(ctx context.Context, workspaceID, userID string) error {
	channels, err := s.channels.ListChannelsByWorkspace(ctx, workspaceID)
	if err != nil {
		return fmt.Errorf("list channels for general: %w", err)
	}
	for _, ch := range channels {
		if ch.IsGeneral {
			_, err := s.JoinChannel(ctx, ch.ID, userID, domain.ChannelRoleMember)
			return err
		}
	}
	return nil
}
