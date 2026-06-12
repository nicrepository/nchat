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

// ActivateWorkspaceMember delegates reactivation to the member store. The store
// implementation enforces #geral sync as part of that persistence operation.
func (s *MemberService) ActivateWorkspaceMember(ctx context.Context, workspaceID, userID string) (domain.WorkspaceMember, error) {
	return s.members.ActivateWorkspaceMember(ctx, workspaceID, userID)
}

// EnsureGeneralMembership adds userID to the #geral channel for workspaceID if
// not already a member. It is idempotent and only applies to active workspace
// members.
func (s *MemberService) EnsureGeneralMembership(ctx context.Context, workspaceID, userID string) error {
	return s.members.EnsureGeneralMembership(ctx, workspaceID, userID)
}

// SyncGeneralMemberships repairs missing #geral channel_members rows for active
// workspace members.
func (s *MemberService) SyncGeneralMemberships(ctx context.Context, workspaceID string) (int64, error) {
	return s.members.SyncGeneralMemberships(ctx, workspaceID)
}

// SelfJoinChannel adds userID to a public active channel in workspaceID.
// Private channel self-join returns ErrNotFound (non-enumerating: callers cannot
// distinguish private channels from missing/archived/cross-workspace channels).
// #geral explicit join is idempotent (returns existing membership).
// The caller must be an active workspace member; the workspace must be active.
// The channel role is always ChannelRoleMember; the caller cannot set it.
func (s *MemberService) SelfJoinChannel(ctx context.Context, workspaceID, channelID, userID string) (domain.ChannelMember, error) {
	channel, err := s.channels.GetChannelByIDInWorkspace(ctx, workspaceID, channelID)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.ChannelMember{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.ChannelMember{}, fmt.Errorf("get channel: %w", err)
	}
	// Non-enumerating: private channels must be indistinguishable from missing/
	// archived/cross-workspace channels — all return the same bare ErrNotFound.
	if channel.Type == domain.ChannelTypePrivate {
		return domain.ChannelMember{}, domain.ErrNotFound
	}

	workspace, err := s.workspaces.GetWorkspaceByID(ctx, workspaceID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return domain.ChannelMember{}, domain.ErrForbidden
		}
		return domain.ChannelMember{}, fmt.Errorf("get workspace: %w", err)
	}
	if workspace.Status != domain.WorkspaceStatusActive {
		return domain.ChannelMember{}, domain.ErrForbidden
	}

	wm, err := s.members.GetWorkspaceMember(ctx, workspaceID, userID)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.ChannelMember{}, domain.ErrForbidden
	}
	if err != nil {
		return domain.ChannelMember{}, fmt.Errorf("get workspace member: %w", err)
	}
	if wm.Status != domain.MemberStatusActive {
		return domain.ChannelMember{}, domain.ErrForbidden
	}

	m, err := s.members.AddChannelMember(ctx, channelID, userID, domain.ChannelRoleMember)
	if errors.Is(err, domain.ErrAlreadyMember) {
		return s.members.GetChannelMember(ctx, channelID, userID)
	}
	return m, err
}

// LeaveChannel removes userID from channelID in workspaceID.
// Returns ErrCannotLeaveGeneralChannel when channelID is the #geral channel.
// Returns nil (idempotent) when userID is not a channel member.
// Returns ErrNotFound when the channel is archived or belongs to a different workspace.
func (s *MemberService) LeaveChannel(ctx context.Context, workspaceID, channelID, userID string) error {
	channel, err := s.channels.GetChannelByIDInWorkspace(ctx, workspaceID, channelID)
	if err != nil {
		return fmt.Errorf("get channel: %w", err)
	}
	if channel.IsGeneral {
		return domain.ErrCannotLeaveGeneralChannel
	}
	if err := s.members.RemoveChannelMember(ctx, workspaceID, channelID, userID); err != nil {
		return fmt.Errorf("remove channel member: %w", err)
	}
	return nil
}

// RemoveMemberFromChannel removes targetUserID from channelID in workspaceID.
// callerID must be an active owner or admin in the workspace.
// Returns ErrForbidden when removing from #geral or when caller lacks manager permission.
func (s *MemberService) RemoveMemberFromChannel(ctx context.Context, workspaceID, channelID, callerID, targetUserID string) error {
	channel, err := s.channels.GetChannelByIDInWorkspace(ctx, workspaceID, channelID)
	if err != nil {
		return fmt.Errorf("get channel: %w", err)
	}
	if channel.IsGeneral {
		return domain.ErrForbidden
	}

	workspace, err := s.workspaces.GetWorkspaceByID(ctx, workspaceID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return domain.ErrForbidden
		}
		return fmt.Errorf("get workspace: %w", err)
	}
	if workspace.Status != domain.WorkspaceStatusActive {
		return domain.ErrForbidden
	}

	caller, err := s.members.GetWorkspaceMember(ctx, workspaceID, callerID)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.ErrForbidden
	}
	if err != nil {
		return fmt.Errorf("get caller workspace member: %w", err)
	}
	if caller.Status != domain.MemberStatusActive {
		return domain.ErrForbidden
	}
	if caller.Role != domain.WorkspaceRoleOwner && caller.Role != domain.WorkspaceRoleAdmin {
		return domain.ErrForbidden
	}

	if err := s.members.RemoveChannelMember(ctx, workspaceID, channelID, targetUserID); err != nil {
		return fmt.Errorf("remove channel member: %w", err)
	}
	return nil
}
