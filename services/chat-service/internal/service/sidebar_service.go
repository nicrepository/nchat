package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// SidebarData is the aggregate returned by SidebarService.GetSidebar.
type SidebarData struct {
	Workspace domain.Workspace
	Channels  []SidebarChannel
	DMs       []domain.DMConversationWithParticipantIDs
	// CanCreateChannel is a deprecated compatibility field, retained only to
	// keep feeding the sidebar's can_create_channel JSON key for clients that
	// predate BUG #393. It is always true when this struct is returned: active
	// workspace members can create channels, and reaching this point already
	// proves an active membership in an active workspace, so the value carries
	// no information and is never derived from the caller's role.
	// POST /api/chat/channels decides for itself on every request.
	//
	// The formal Deprecated: marker lives on the JSON field it feeds, in
	// sidebarResponseBody; putting it here too would only flag the single
	// handler assignment that has to exist for the contract to be kept.
	CanCreateChannel bool
}

// SidebarChannel carries server-derived destination eligibility.
type SidebarChannel struct {
	Channel  domain.Channel
	CanWrite bool
}

type sidebarChannelStore interface {
	ListVisibleChannelAccessByUser(ctx context.Context, workspaceID, userID string) ([]storage.VisibleChannelAccess, error)
}

// SidebarService aggregates workspace, channel, and DM data for the sidebar
// in a single authorized read. No N+1 queries are performed.
type SidebarService struct {
	workspaces storage.WorkspaceStore
	channels   sidebarChannelStore
	members    storage.MemberStore
	dms        storage.DMStore
}

func NewSidebarService(
	workspaces storage.WorkspaceStore,
	channels sidebarChannelStore,
	members storage.MemberStore,
	dms storage.DMStore,
) *SidebarService {
	return &SidebarService{
		workspaces: workspaces,
		channels:   channels,
		members:    members,
		dms:        dms,
	}
}

// GetSidebar returns the channels and DM conversations visible to userID in
// the default workspace. Returns ErrForbidden if the user is not an active
// workspace member, ErrNotFound if the workspace does not exist.
func (s *SidebarService) GetSidebar(ctx context.Context, userID string) (SidebarData, error) {
	if userID == "" {
		return SidebarData{}, fmt.Errorf("%w: user_id is required", domain.ErrInvalidInput)
	}

	workspace, err := s.workspaces.GetDefaultWorkspace(ctx)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return SidebarData{}, domain.ErrNotFound
		}
		return SidebarData{}, fmt.Errorf("get default workspace: %w", err)
	}
	if workspace.Status != domain.WorkspaceStatusActive {
		return SidebarData{}, domain.ErrForbidden
	}

	member, err := s.members.GetWorkspaceMember(ctx, workspace.ID, userID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return SidebarData{}, domain.ErrForbidden
		}
		return SidebarData{}, fmt.Errorf("get workspace member: %w", err)
	}
	if member.Status != domain.MemberStatusActive {
		return SidebarData{}, domain.ErrForbidden
	}

	channels, err := s.channels.ListVisibleChannelAccessByUser(ctx, workspace.ID, userID)
	if err != nil {
		return SidebarData{}, fmt.Errorf("list channels: %w", err)
	}
	sidebarChannels := make([]SidebarChannel, 0, len(channels))
	for _, access := range channels {
		sidebarChannels = append(sidebarChannels, SidebarChannel{
			Channel:  access.Channel,
			CanWrite: domain.CanWriteChannel(&member, access.ChannelMember, access.Channel),
		})
	}

	dms, err := s.dms.ListVisibleConversationsWithParticipantIDs(ctx, workspace.ID, userID)
	if err != nil {
		return SidebarData{}, fmt.Errorf("list dms: %w", err)
	}

	return SidebarData{
		Workspace: workspace,
		Channels:  sidebarChannels,
		DMs:       dms,
		// Constant by construction, not a role lookup: every path that reaches
		// here has already proved the membership channel creation requires.
		CanCreateChannel: true,
	}, nil
}
