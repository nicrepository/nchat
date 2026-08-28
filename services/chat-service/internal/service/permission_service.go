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

	// Ask the domain with the workspace membership alone first. For a role that
	// reaches public channels that is already the whole answer, so the common
	// path keeps its single round trip.
	//
	// When it is not — a guest, whose reach is only the channels it was added
	// to (RF-74), or anybody at all facing a private channel — an explicit
	// channel membership is the one thing that can still change the answer, so
	// load it and ask again. Deliberately not "if the channel is private":
	// that condition was the bug, because it left a guest that *is* a member of
	// a public channel being judged as if it were not one.
	//
	// The decision stays in domain.CanReadChannel either way. Nothing here
	// tests a role or a channel type; this only supplies the input the domain
	// needs to answer correctly.
	if domain.CanReadChannel(&wm, nil, ch) {
		return true, nil
	}
	cm, err := s.members.GetChannelMember(ctx, channelID, userID)
	if errors.Is(err, domain.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("get channel member: %w", err)
	}
	return domain.CanReadChannel(&wm, &cm, ch), nil
}

// CanWrite reports whether userID may post to channelID in workspaceID.
// For MVP, write access follows the same rules as read access.
func (s *PermissionService) CanWrite(ctx context.Context, workspaceID, channelID, userID string) (bool, error) {
	return s.CanRead(ctx, workspaceID, channelID, userID)
}

// CanManageWorkspace reports whether the caller is an active workspace owner/admin.
func (s *PermissionService) CanManageWorkspace(ctx context.Context, workspaceID, userID string) (bool, error) {
	member, err := s.members.GetWorkspaceMember(ctx, workspaceID, userID)
	if errors.Is(err, domain.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("get workspace manager: %w", err)
	}
	return domain.CanManageWorkspace(&member), nil
}
