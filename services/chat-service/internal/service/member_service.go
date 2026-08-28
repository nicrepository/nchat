package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

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

// SearchChannelMembers returns active members of one channel by display-name prefix.
// The caller's channel access must be checked by MentionService before this method.
func (s *MemberService) SearchChannelMembers(ctx context.Context, workspaceID, channelID, prefix string, limit int) ([]domain.MentionCandidate, error) {
	return s.members.SearchChannelMembers(ctx, workspaceID, channelID, prefix, limit)
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
//
// A guest is refused (RF-74). This route is the shortest path around guest
// isolation there is: the whole point of scoping a guest to the channels it was
// added to is lost if it can add itself to any public channel. The predicate is
// domain.CanReachPublicChannels — the same one that decides whether the guest
// could have read the channel in the first place — so a role that may not read
// a public channel may not join one either.
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
	if !domain.CanReachPublicChannels(&wm) {
		return domain.ChannelMember{}, domain.ErrForbidden
	}

	m, err := s.members.AddChannelMember(ctx, channelID, userID, domain.ChannelRoleMember)
	if errors.Is(err, domain.ErrAlreadyMember) {
		return s.members.GetChannelMember(ctx, channelID, userID)
	}
	return m, err
}

// AddChannelMembersInput asks to add participants to an existing channel
// (issue #398).
//
// WorkspaceID is resolved server-side and CallerID is the authenticated
// principal; neither is ever taken from a request body. There is no role field:
// added members are always ChannelRoleMember, so a caller cannot mint a
// moderator by asking for one.
type AddChannelMembersInput struct {
	WorkspaceID string
	CallerID    string
	ChannelID   string
	UserIDs     []string
}

// AddChannelMembers adds active workspace members to an existing channel.
//
// Authorization is domain.CanManageChannelMembers — active workspace owner or
// admin — the same authority that already removes a member from a channel and
// that docs/runbooks/task-chat-channel-join-leave.md calls the "manager-add
// flow". It is deliberately checked before the channel is even looked up, so a
// caller with no management rights cannot use the response to learn whether a
// channel UUID exists.
//
// The channel is then loaded workspace-scoped and active-only, which is what
// refuses an archived channel, a channel from another tenant and one that never
// existed with the same ErrNotFound. #geral is refused as well: every active
// workspace member already belongs to it by the membership sync, so "adding"
// someone would either be a no-op or a way to write rows the sync owns.
//
// The store is the authority on the write. Everything below it — the UUID
// parsing, the de-duplication, the batch cap — exists to refuse a malformed or
// oversized request cheaply, never to decide who is eligible.
func (s *MemberService) AddChannelMembers(ctx context.Context, input AddChannelMembersInput) (storage.AddMembersResult, error) {
	// Active membership first, then the capability — deliberately not
	// requireWorkspaceManager, which would apply CanManageWorkspace here and
	// leave CanManageChannelMembers as decoration. The seam only means anything
	// if it is the single predicate this endpoint actually consults: widening it
	// for RF-74 must widen this route, and a second owner/admin gate above it
	// would silently prevent that.
	member, err := requireActiveWorkspaceMember(ctx, s.workspaces, s.members, input.WorkspaceID, input.CallerID)
	if err != nil {
		return storage.AddMembersResult{}, err
	}
	if !domain.CanManageChannelMembers(&member) {
		return storage.AddMembersResult{}, domain.ErrForbidden
	}

	userIDs, err := normalizeAddMemberIDs(input.UserIDs)
	if err != nil {
		return storage.AddMembersResult{}, err
	}

	channel, err := s.channels.GetChannelByIDInWorkspace(ctx, input.WorkspaceID, input.ChannelID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return storage.AddMembersResult{}, domain.ErrNotFound
		}
		return storage.AddMembersResult{}, fmt.Errorf("get channel: %w", err)
	}
	if channel.IsGeneral {
		return storage.AddMembersResult{}, fmt.Errorf("%w: every active member already belongs to geral", domain.ErrInvalidInput)
	}

	// The actor is handed to the store so the transaction re-derives their
	// authority itself. The check above stays because it refuses a caller before
	// the channel lookup can leak whether a channel ID exists — but it is a
	// courtesy, not the control; the store's is the one that cannot be raced.
	result, err := s.members.AddChannelMembers(ctx, input.WorkspaceID, channel.ID, member.UserID, userIDs)
	if err != nil {
		if errors.Is(err, domain.ErrForbidden) || errors.Is(err, domain.ErrInvalidInput) {
			return storage.AddMembersResult{}, err
		}
		return storage.AddMembersResult{}, fmt.Errorf("add channel members: %w", err)
	}
	return result, nil
}

// SearchChannelMemberCandidatesInput asks who could still be added to a channel
// (issue #398). The workspace is resolved server-side and the caller is the
// authenticated principal.
type SearchChannelMemberCandidatesInput struct {
	WorkspaceID string
	CallerID    string
	ChannelID   string
	Query       string
	Limit       int // Zero uses the server default.
}

// SearchChannelMemberCandidates returns workspace members eligible to be added
// to a channel, with current members already excluded by the store.
//
// The authorization is the same gate the write uses — domain.CanManageChannelMembers
// — and it is checked before the channel is looked up, so a caller with no
// management rights cannot use the response to learn whether a channel ID
// exists. That is deliberate: this endpoint reveals which people are *not* in a
// channel, which is a fact about a private channel's composition.
//
// The exclusion of current members happens in SQL, not here. The panel's member
// preview is presence-filtered and capped, so it was never a complete
// membership list; using it to decide eligibility is exactly the defect this
// method replaces.
func (s *MemberService) SearchChannelMemberCandidates(
	ctx context.Context, input SearchChannelMemberCandidatesInput,
) ([]domain.DMCandidate, error) {
	member, err := requireActiveWorkspaceMember(ctx, s.workspaces, s.members, input.WorkspaceID, input.CallerID)
	if err != nil {
		return nil, err
	}
	if !domain.CanManageChannelMembers(&member) {
		return nil, domain.ErrForbidden
	}

	query, limit, err := normalizeCandidateSearch(input.Query, input.Limit)
	if err != nil {
		return nil, err
	}

	channel, err := s.channels.GetChannelByIDInWorkspace(ctx, input.WorkspaceID, input.ChannelID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return nil, domain.ErrNotFound
		}
		return nil, fmt.Errorf("get channel: %w", err)
	}

	candidates, err := s.members.SearchChannelMemberCandidates(
		ctx, input.WorkspaceID, channel.ID, member.UserID, query, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("search channel member candidates: %w", err)
	}
	return candidates, nil
}

// normalizeCandidateSearch applies the same query bounds and limit clamping the
// DM candidate search already uses, so the three searches cannot drift.
func normalizeCandidateSearch(rawQuery string, rawLimit int) (string, int, error) {
	query := strings.TrimSpace(rawQuery)
	queryRunes := utf8.RuneCountInString(query)
	if queryRunes < minDMCandidateQuery || queryRunes > maxDMCandidateQuery {
		return "", 0, fmt.Errorf("%w: invalid candidate search", domain.ErrInvalidInput)
	}
	if rawLimit < 0 {
		return "", 0, fmt.Errorf("%w: invalid candidate limit", domain.ErrInvalidInput)
	}
	limit := rawLimit
	if limit == 0 {
		limit = defaultDMCandidateLimit
	}
	if limit > maxDMCandidateLimit {
		limit = maxDMCandidateLimit
	}
	return query, limit, nil
}

// normalizeAddMemberIDs canonicalises, de-duplicates and bounds a requested user
// list (issue #398).
//
// The size check runs on the raw list, before any parsing, so an oversized
// payload costs one comparison rather than a UUID parse per entry. De-duplication
// happens after canonicalisation, so the same user written in two different
// letter cases collapses to one entry instead of reaching the store as two and
// being refused there as a count mismatch.
//
// Order is not preserved and does not matter: the store's set-based statement
// treats the list as a set, and sorting makes the value a function of its
// contents alone, which is what makes a retry byte-identical.
func normalizeAddMemberIDs(raw []string) ([]string, error) {
	if len(raw) > domain.MaxAddMembersPerRequest {
		return nil, domain.ErrTooManyMembersRequested
	}
	unique := make(map[string]struct{}, len(raw))
	for _, rawID := range raw {
		trimmed := strings.TrimSpace(rawID)
		if trimmed == "" {
			return nil, fmt.Errorf("%w: user_ids cannot contain empty user IDs", domain.ErrInvalidInput)
		}
		userID, err := canonicalizeUserID(trimmed)
		if err != nil {
			return nil, err
		}
		unique[userID] = struct{}{}
	}
	if len(unique) == 0 {
		return nil, domain.ErrNoMembersRequested
	}
	userIDs := make([]string, 0, len(unique))
	for userID := range unique {
		userIDs = append(userIDs, userID)
	}
	sort.Strings(userIDs)
	return userIDs, nil
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
//
// Authorization is domain.CanManageChannelMembers — the same predicate the add
// path uses, rather than a second inline role list that could drift from it.
// Adding and removing the same row are the same authority, so RF-74 widening
// the add to the workspace moderator widens the removal with it.
// Returns ErrForbidden when removing from #geral or when caller lacks permission.
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
	if !domain.CanManageChannelMembers(&caller) {
		return domain.ErrForbidden
	}

	if err := s.members.RemoveChannelMember(ctx, workspaceID, channelID, targetUserID); err != nil {
		return fmt.Errorf("remove channel member: %w", err)
	}
	return nil
}
