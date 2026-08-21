package service

import (
	"context"
	"errors"
	"fmt"
	"time"

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
//
// LastMessageAt is the channel's activity instant, nil when it has never been
// written to (issue #414). It is carried through untouched from the authorized
// listing query — this layer neither derives it nor substitutes created_at for
// a missing one, because "has activity" and "was created" are two different
// facts and the ordering rule needs to tell them apart.
type SidebarChannel struct {
	Channel       domain.Channel
	CanWrite      bool
	LastMessageAt *time.Time
	PinnedAt      *time.Time
	UnreadCount   int
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
	pins       storage.SidebarPinStore
	readState  storage.ConversationReadStateStore
}

const (
	ReadTargetChannel = storage.ConversationReadTargetChannel
	ReadTargetDM      = storage.ConversationReadTargetDM
)

// WithPins adds the optional per-user preference store without changing the
// existing constructor used by sidebar readers and tests.
func (s *SidebarService) WithPins(pins storage.SidebarPinStore) *SidebarService {
	s.pins = pins
	return s
}

func (s *SidebarService) WithReadState(readState storage.ConversationReadStateStore) *SidebarService {
	s.readState = readState
	return s
}

func (s *SidebarService) MarkConversationRead(ctx context.Context, userID, targetType, targetID string, lastReadMessageID *string) error {
	workspace, _, err := s.authorizeWorkspaceMember(ctx, userID)
	if err != nil {
		return err
	}
	if s.readState == nil {
		return fmt.Errorf("conversation read state unavailable")
	}
	return s.readState.MarkRead(ctx, workspace.ID, userID, targetType, targetID, lastReadMessageID)
}

// PinConversation and UnpinConversation always resolve the workspace from the
// same server-side sidebar context as GET. The client supplies only a target.
func (s *SidebarService) PinConversation(ctx context.Context, userID, targetType, targetID string) error {
	workspace, _, err := s.authorizeWorkspaceMember(ctx, userID)
	if err != nil {
		return err
	}
	if s.pins == nil {
		return fmt.Errorf("sidebar pins unavailable")
	}
	return s.pins.Pin(ctx, workspace.ID, userID, targetType, targetID)
}

func (s *SidebarService) UnpinConversation(ctx context.Context, userID, targetType, targetID string) error {
	if _, _, err := s.authorizeWorkspaceMember(ctx, userID); err != nil {
		return err
	}
	if s.pins == nil {
		return fmt.Errorf("sidebar pins unavailable")
	}
	return s.pins.Unpin(ctx, userID, targetType, targetID)
}

func (s *SidebarService) authorizeWorkspaceMember(ctx context.Context, userID string) (domain.Workspace, domain.WorkspaceMember, error) {
	if userID == "" {
		return domain.Workspace{}, domain.WorkspaceMember{}, fmt.Errorf("%w: user_id is required", domain.ErrInvalidInput)
	}
	workspace, err := s.workspaces.GetDefaultWorkspace(ctx)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return domain.Workspace{}, domain.WorkspaceMember{}, domain.ErrNotFound
		}
		return domain.Workspace{}, domain.WorkspaceMember{}, fmt.Errorf("get default workspace: %w", err)
	}
	if workspace.Status != domain.WorkspaceStatusActive {
		return domain.Workspace{}, domain.WorkspaceMember{}, domain.ErrForbidden
	}
	member, err := s.members.GetWorkspaceMember(ctx, workspace.ID, userID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return domain.Workspace{}, domain.WorkspaceMember{}, domain.ErrForbidden
		}
		return domain.Workspace{}, domain.WorkspaceMember{}, fmt.Errorf("get workspace member: %w", err)
	}
	if member.Status != domain.MemberStatusActive {
		return domain.Workspace{}, domain.WorkspaceMember{}, domain.ErrForbidden
	}
	return workspace, member, nil
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

	workspace, member, err := s.authorizeWorkspaceMember(ctx, userID)
	if err != nil {
		return SidebarData{}, err
	}
	channels, err := s.channels.ListVisibleChannelAccessByUser(ctx, workspace.ID, userID)
	if err != nil {
		return SidebarData{}, fmt.Errorf("list channels: %w", err)
	}
	pinnedAt := map[string]time.Time{}
	unreadCounts := map[string]int{}
	if s.pins != nil {
		pins, err := s.pins.ListVisible(ctx, workspace.ID, userID)
		if err != nil {
			return SidebarData{}, fmt.Errorf("list sidebar pins: %w", err)
		}
		for _, pin := range pins {
			pinnedAt[pin.TargetType+"\x00"+pin.TargetID] = pin.PinnedAt
		}
	}
	if s.readState != nil {
		unreadCounts, err = s.readState.UnreadCounts(ctx, workspace.ID, userID)
		if err != nil {
			return SidebarData{}, fmt.Errorf("list unread counts: %w", err)
		}
	}
	sidebarChannels := make([]SidebarChannel, 0, len(channels))
	for _, access := range channels {
		pinned := pinnedAt[storage.SidebarPinTargetChannel+"\x00"+access.Channel.ID]
		var pinnedPtr *time.Time
		if !pinned.IsZero() {
			pinnedCopy := pinned
			pinnedPtr = &pinnedCopy
		}
		sidebarChannels = append(sidebarChannels, SidebarChannel{
			Channel:       access.Channel,
			CanWrite:      domain.CanWriteChannel(&member, access.ChannelMember, access.Channel),
			LastMessageAt: access.LastMessageAt,
			PinnedAt:      pinnedPtr,
			UnreadCount:   unreadCounts[storage.ConversationReadTargetChannel+"\x00"+access.Channel.ID],
		})
	}

	dms, err := s.dms.ListVisibleConversationsWithParticipantIDs(ctx, workspace.ID, userID)
	if err != nil {
		return SidebarData{}, fmt.Errorf("list dms: %w", err)
	}
	for i := range dms {
		if pinned := pinnedAt[storage.SidebarPinTargetDM+"\x00"+dms[i].ID]; !pinned.IsZero() {
			pinnedCopy := pinned
			dms[i].PinnedAt = &pinnedCopy
		}
		dms[i].UnreadCount = unreadCounts[storage.ConversationReadTargetDM+"\x00"+dms[i].ID]
	}

	return SidebarData{
		Workspace: workspace,
		Channels:  sidebarChannels,
		DMs:       dms,
		// The same predicate CreateChannel enforces, not a constant: active
		// membership stopped being sufficient when RF-74 excluded the guest.
		// This flag is an affordance, never the control — ChannelService still
		// decides — but it must not offer a guest a button that returns 403.
		CanCreateChannel: domain.CanCreateChannel(&member),
	}, nil
}
