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
	// CanRename is the server's own answer to "may this caller rename this
	// channel" (issue #527), derived from the membership GetSidebar already
	// loaded. It exists so the row's action menu can omit an item the server
	// would refuse, and it is never the control: PATCH /api/chat/channels/{id}
	// re-derives the same decision from the session on every call.
	//
	// False for #geral, matching the write path: the general channel is
	// immutable, so no role makes it renameable.
	CanRename bool
	// Muted is this user's own notification preference for the channel (#527).
	// Individual by construction — it comes from a row keyed by user_id — so one
	// member silencing a channel changes nothing for anyone else. Always false
	// for the general channel, which is not silenceable.
	Muted bool
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
	notifs     storage.NotificationPrefStore
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

// WithNotificationPrefs adds the optional per-user mute store (#527), the same
// optional-dependency shape WithPins and WithReadState use: a build without it
// reports nothing as muted rather than failing to serve a sidebar.
func (s *SidebarService) WithNotificationPrefs(notifs storage.NotificationPrefStore) *SidebarService {
	s.notifs = notifs
	return s
}

// MuteConversation and UnmuteConversation resolve the workspace from the same
// server-side sidebar context GET does. The client supplies only a target, and
// the store re-checks visibility — and, for a channel, the general-channel
// invariant — inside its own statement.
func (s *SidebarService) MuteConversation(ctx context.Context, userID, targetType, targetID string) error {
	workspace, _, err := s.authorizeWorkspaceMember(ctx, userID)
	if err != nil {
		return err
	}
	if s.notifs == nil {
		return fmt.Errorf("notification preferences unavailable")
	}
	return s.notifs.Mute(ctx, workspace.ID, userID, targetType, targetID)
}

func (s *SidebarService) UnmuteConversation(ctx context.Context, userID, targetType, targetID string) error {
	if _, _, err := s.authorizeWorkspaceMember(ctx, userID); err != nil {
		return err
	}
	if s.notifs == nil {
		return fmt.Errorf("notification preferences unavailable")
	}
	return s.notifs.Unmute(ctx, userID, targetType, targetID)
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

// mutedTargets reads this user's silenced conversations into the same
// "kind\x00id" keyed lookup GetSidebar uses for pins and unread counts, so the
// projection below stays one map read per row rather than a scan.
//
// An unconfigured store means nothing is muted, which is the honest answer for a
// build without the table rather than a failure to render a sidebar.
func (s *SidebarService) mutedTargets(ctx context.Context, workspaceID, userID string) (map[string]bool, error) {
	muted := map[string]bool{}
	if s.notifs == nil {
		return muted, nil
	}
	items, err := s.notifs.ListMuted(ctx, workspaceID, userID)
	if err != nil {
		return nil, fmt.Errorf("list muted conversations: %w", err)
	}
	for _, item := range items {
		muted[item.TargetType+"\x00"+item.TargetID] = true
	}
	return muted, nil
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
	decorations, err := s.loadSidebarDecorations(ctx, workspace.ID, userID)
	if err != nil {
		return SidebarData{}, err
	}
	sidebarChannels := make([]SidebarChannel, 0, len(channels))
	for _, access := range channels {
		sidebarChannels = append(sidebarChannels, projectSidebarChannel(access, member, decorations))
	}

	dms, err := s.dms.ListVisibleConversationsWithParticipantIDs(ctx, workspace.ID, userID)
	if err != nil {
		return SidebarData{}, fmt.Errorf("list dms: %w", err)
	}
	decorateSidebarDMs(dms, decorations)

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

// sidebarDecorations are the per-conversation flags that do not come from the
// conversation itself: whether the caller pinned it, how many messages they have
// not read, whether they muted it. Three stores, three maps, loaded once for the
// whole sidebar — the alternative is a query per row.
//
// Each map is keyed the way its own store keys it, target type and id joined by
// a NUL, which is why the lookups below name the target-type constant of the
// store they came from rather than sharing one.
type sidebarDecorations struct {
	pinnedAt map[string]time.Time
	unread   map[string]int
	muted    map[string]bool
}

func (s *SidebarService) loadSidebarDecorations(ctx context.Context, workspaceID, userID string) (sidebarDecorations, error) {
	pinnedAt, err := s.pinnedTargets(ctx, workspaceID, userID)
	if err != nil {
		return sidebarDecorations{}, err
	}
	unread, err := s.unreadTargets(ctx, workspaceID, userID)
	if err != nil {
		return sidebarDecorations{}, err
	}
	muted, err := s.mutedTargets(ctx, workspaceID, userID)
	if err != nil {
		return sidebarDecorations{}, err
	}
	return sidebarDecorations{pinnedAt: pinnedAt, unread: unread, muted: muted}, nil
}

// A nil store is a sidebar assembled without that feature wired in, not an
// error: the flags it would have contributed are simply absent, and every
// lookup against an empty map yields the zero value the row already means.
func (s *SidebarService) pinnedTargets(ctx context.Context, workspaceID, userID string) (map[string]time.Time, error) {
	pinnedAt := map[string]time.Time{}
	if s.pins == nil {
		return pinnedAt, nil
	}
	pins, err := s.pins.ListVisible(ctx, workspaceID, userID)
	if err != nil {
		return nil, fmt.Errorf("list sidebar pins: %w", err)
	}
	for _, pin := range pins {
		pinnedAt[pin.TargetType+"\x00"+pin.TargetID] = pin.PinnedAt
	}
	return pinnedAt, nil
}

func (s *SidebarService) unreadTargets(ctx context.Context, workspaceID, userID string) (map[string]int, error) {
	if s.readState == nil {
		return map[string]int{}, nil
	}
	counts, err := s.readState.UnreadCounts(ctx, workspaceID, userID)
	if err != nil {
		return nil, fmt.Errorf("list unread counts: %w", err)
	}
	return counts, nil
}

// projectSidebarChannel decides what one row says. The capabilities are the same
// predicates the write paths enforce, evaluated on the membership already loaded
// for the whole sidebar — not a second, parallel rule, and not a per-row query.
func projectSidebarChannel(access storage.VisibleChannelAccess, member domain.WorkspaceMember, decorations sidebarDecorations) SidebarChannel {
	var pinnedPtr *time.Time
	if pinned := decorations.pinnedAt[storage.SidebarPinTargetChannel+"\x00"+access.Channel.ID]; !pinned.IsZero() {
		pinnedCopy := pinned
		pinnedPtr = &pinnedCopy
	}
	return SidebarChannel{
		Channel:  access.Channel,
		CanWrite: domain.CanWriteChannel(&member, access.ChannelMember, access.Channel),
		// The same predicate ChannelService.UpdateChannel enforces. The grouped
		// category listing calls the identical function.
		CanRename:     domain.CanRenameChannel(&member, access.Channel),
		Muted:         decorations.muted[storage.NotificationPrefTargetChannel+"\x00"+access.Channel.ID],
		LastMessageAt: access.LastMessageAt,
		PinnedAt:      pinnedPtr,
		UnreadCount:   decorations.unread[storage.ConversationReadTargetChannel+"\x00"+access.Channel.ID],
	}
}

// A DM row carries its own fields, so it is decorated in place rather than
// projected into a second type.
func decorateSidebarDMs(dms []domain.DMConversationWithParticipantIDs, decorations sidebarDecorations) {
	for i := range dms {
		if pinned := decorations.pinnedAt[storage.SidebarPinTargetDM+"\x00"+dms[i].ID]; !pinned.IsZero() {
			pinnedCopy := pinned
			dms[i].PinnedAt = &pinnedCopy
		}
		dms[i].UnreadCount = decorations.unread[storage.ConversationReadTargetDM+"\x00"+dms[i].ID]
		dms[i].Muted = decorations.muted[storage.NotificationPrefTargetDM+"\x00"+dms[i].ID]
	}
}
