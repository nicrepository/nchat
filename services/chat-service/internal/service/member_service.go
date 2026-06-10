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
	members  storage.MemberStore
	channels storage.ChannelStore
}

func NewMemberService(members storage.MemberStore, channels storage.ChannelStore) *MemberService {
	return &MemberService{members: members, channels: channels}
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
	m, err := s.members.AddChannelMember(ctx, channelID, userID, role)
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
