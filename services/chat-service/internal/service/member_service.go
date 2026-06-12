package service

import (
	"context"
	"errors"

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
