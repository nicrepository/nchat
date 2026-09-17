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
	// NotificationLevel is the other half of that preference (issue #136): which
	// events this user wants alerts for here, independently of whether alerts
	// are silenced right now.
	//
	// Two fields and not one derived state, deliberately. They are the two
	// columns the store holds, and keeping them apart all the way to the client
	// is what lets the sidebar's mute shortcut leave the level alone — the
	// invariant the whole issue rests on. The single state a UI shows is the
	// precedence between them, applied where it is displayed.
	//
	// Always one of storage's declared levels; a conversation with no preference
	// row at all reads as NotificationLevelAll.
	NotificationLevel string
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
	// notificationLevelsEnabled is the issue #136 rollout gate. False is the
	// default everywhere, including for a service assembled without the option
	// below: a build that was never told the gate is open has not been told to
	// write granular rows.
	notificationLevelsEnabled bool
}

const (
	ReadTargetChannel = storage.ConversationReadTargetChannel
	ReadTargetDM      = storage.ConversationReadTargetDM
)

// The three notification modes the canonical preference endpoint accepts
// (issue #136).
//
// This is the *public* vocabulary, and it is deliberately not the storage one.
// A client says "silence this conversation" and the server decides what that
// means in columns — which level to keep, which timestamp to write — so nothing
// outside this service has to know that a mute is a nullable muted_at or that
// the pure default is the absence of a row. Two of the three names coincide
// with a stored level and the third does not exist as one at all, which is
// exactly why the translation lives here.
const (
	// NotificationModeAll is every message: the default level, not silenced.
	NotificationModeAll = "all"
	// NotificationModeMentionsReplies is mentions and replies only, not
	// silenced.
	NotificationModeMentionsReplies = "mentions_replies"
	// NotificationModeMuted is silenced, whatever level was chosen before. The
	// level is preserved untouched, so turning notifications back on — from
	// here or from the sidebar's shortcut — restores it.
	NotificationModeMuted = "muted"
)

// ValidNotificationMode reports whether mode is one of the three above.
//
// A closed set, checked before anything is written, so an unknown mode is a
// refusal and never a write that guesses at what the caller meant.
//
// There is deliberately no inverse rendering beside it: the sidebar payload
// publishes the two stored fields — muted and the level — and not the mode
// derived from them. That derivation is one line of precedence (a mute wins)
// and it has to run on the client anyway, because the sidebar's mute shortcut
// updates the row optimistically and the only honest optimistic update is
// "muted changed, the level did not" — the invariant itself. A server-sent mode
// would be stale the instant that happened, and re-deriving it locally on top
// of a field the server also sends is two authorities for one value.
func ValidNotificationMode(mode string) bool {
	switch mode {
	case NotificationModeAll, NotificationModeMentionsReplies, NotificationModeMuted:
		return true
	default:
		return false
	}
}

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

// WithConversationNotificationLevels opens or closes the issue #136 rollout
// gate for *writes*.
//
// It is deliberately not an optional dependency like the stores above: reading
// is never gated. Every read in this service already understands a granular row
// whatever this says, which is what makes enabling the gate and rolling back to
// this same build safe — a row written while it was open keeps its meaning
// after it closes.
func (s *SidebarService) WithConversationNotificationLevels(enabled bool) *SidebarService {
	s.notificationLevelsEnabled = enabled
	return s
}

// ConversationNotificationLevelsEnabled reports whether granular levels may be
// written, so the sidebar payload can publish the capability instead of leaving
// a client to guess at it.
func (s *SidebarService) ConversationNotificationLevelsEnabled() bool {
	return s.notificationLevelsEnabled
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

// SetConversationNotificationPreference applies one of the three public modes
// (issue #136).
//
// It is the only place the public vocabulary is translated into stored columns,
// and the translation is the whole function:
//
//	all               the default level, unsilenced — SetLevel removes the row
//	mentions_replies  that level, unsilenced
//	muted             silenced, level untouched
//
// The actor is the argument the handler took from the session and the workspace
// is resolved here, from the same server-side sidebar context GET uses — so
// nothing a client sends can name a user, a workspace or a role. The target is
// re-authorised inside the store's own statement, including the general-channel
// refusal that applies to the mute and only to the mute.
func (s *SidebarService) SetConversationNotificationPreference(
	ctx context.Context, userID, targetType, targetID, mode string,
) error {
	if !ValidNotificationMode(mode) {
		return fmt.Errorf("%w: unknown notification mode %q", domain.ErrInvalidInput, mode)
	}
	// The rollout gate, and it is here rather than in the handler because this
	// is the only place that writes (issue #136). `all` and `muted` pass while
	// it is closed: both are states the pre-#136 model can represent — the
	// default is the absence of a row and a mute is a row — so neither can
	// produce something a release slot running that build would misread.
	//
	// `mentions_replies` is the one that can, and it is refused *before* the
	// workspace is resolved or anything is written. There is no path to the
	// store for it while the gate is closed, which is what makes the guarantee
	// structural rather than a promise the UI keeps.
	if mode == NotificationModeMentionsReplies && !s.notificationLevelsEnabled {
		return domain.ErrConversationNotificationLevelsDisabled
	}
	workspace, _, err := s.authorizeWorkspaceMember(ctx, userID)
	if err != nil {
		return err
	}
	if s.notifs == nil {
		return fmt.Errorf("notification preferences unavailable")
	}
	switch mode {
	case NotificationModeMuted:
		return s.notifs.Mute(ctx, workspace.ID, userID, targetType, targetID)
	case NotificationModeMentionsReplies:
		return s.notifs.SetLevel(
			ctx, workspace.ID, userID, targetType, targetID, storage.NotificationLevelMentionsReplies,
		)
	default:
		return s.notifs.SetLevel(
			ctx, workspace.ID, userID, targetType, targetID, storage.NotificationLevelAll,
		)
	}
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

// notificationPrefTargets reads this user's expressed notification preferences
// into the same "kind\x00id" keyed lookup GetSidebar uses for pins and unread
// counts, so the projection below stays one map read per row rather than a scan.
//
// One statement for the whole sidebar, which is what keeps the level off the
// list of things that could become a query per conversation (issue #136).
//
// An unconfigured store means nobody expressed anything, which is the honest
// answer for a build without the table rather than a failure to render a
// sidebar. A conversation absent from the map reads as the zero value, and the
// zero value is exactly the default the missing row means: not muted, and a
// level that normalises to "all".
func (s *SidebarService) notificationPrefTargets(
	ctx context.Context, workspaceID, userID string,
) (map[string]storage.ConversationNotificationPref, error) {
	prefs := map[string]storage.ConversationNotificationPref{}
	if s.notifs == nil {
		return prefs, nil
	}
	items, err := s.notifs.ListPreferences(ctx, workspaceID, userID)
	if err != nil {
		return nil, fmt.Errorf("list conversation notification preferences: %w", err)
	}
	for _, item := range items {
		prefs[item.TargetType+"\x00"+item.TargetID] = item
	}
	return prefs, nil
}

// notificationLevelOr keeps a level this build does not recognise, and the
// empty string a missing row yields, from reaching a client as a level.
//
// The normalisation is here rather than at the row, so both target kinds get the
// same answer from the same line.
func notificationLevelOr(level string) string {
	if storage.ValidNotificationLevel(level) {
		return level
	}
	return storage.NotificationLevelAll
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
	// notifPrefs holds both halves of the notification preference — the mute and
	// the level — because they are one row and reading them separately would be
	// a second statement for no gain (issue #136).
	notifPrefs map[string]storage.ConversationNotificationPref
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
	notifPrefs, err := s.notificationPrefTargets(ctx, workspaceID, userID)
	if err != nil {
		return sidebarDecorations{}, err
	}
	return sidebarDecorations{pinnedAt: pinnedAt, unread: unread, notifPrefs: notifPrefs}, nil
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
	notifPref := decorations.notifPrefs[storage.NotificationPrefTargetChannel+"\x00"+access.Channel.ID]
	return SidebarChannel{
		Channel:  access.Channel,
		CanWrite: domain.CanWriteChannel(&member, access.ChannelMember, access.Channel),
		// The same predicate ChannelService.UpdateChannel enforces. The grouped
		// category listing calls the identical function.
		CanRename:         domain.CanRenameChannel(&member, access.Channel),
		Muted:             notifPref.Muted,
		NotificationLevel: notificationLevelOr(notifPref.Level),
		LastMessageAt:     access.LastMessageAt,
		PinnedAt:          pinnedPtr,
		UnreadCount:       decorations.unread[storage.ConversationReadTargetChannel+"\x00"+access.Channel.ID],
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
		notifPref := decorations.notifPrefs[storage.NotificationPrefTargetDM+"\x00"+dms[i].ID]
		dms[i].Muted = notifPref.Muted
		dms[i].NotificationLevel = notificationLevelOr(notifPref.Level)
	}
}
