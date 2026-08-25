package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

const generalChannelSlug = "geral"

// CreateChannelInput is the public CRUD input for channel creation.
// is_general, status, and created_by are intentionally not caller-provided.
type CreateChannelInput struct {
	WorkspaceID string
	CallerID    string
	CategoryID  string
	Slug        string
	DisplayName string
	Type        domain.ChannelType
	Position    int
}

// GetChannelInput supports lookup by either ChannelID or Slug.
type GetChannelInput struct {
	WorkspaceID string
	CallerID    string
	ChannelID   string
	Slug        string
}

// UpdateChannelInput updates mutable fields only.
// CategoryID nil means unchanged; pointer to empty string clears the category.
type UpdateChannelInput struct {
	WorkspaceID string
	CallerID    string
	ChannelID   string
	Slug        string
	DisplayName string
	CategoryID  *string
	Position    *int
	Type        *domain.ChannelType
}

// ChannelService handles channel CRUD authorization and validation.
type ChannelService struct {
	workspaces storage.WorkspaceStore
	channels   storage.ChannelStore
	members    storage.MemberStore
}

func NewChannelService(workspaces storage.WorkspaceStore, channels storage.ChannelStore, members storage.MemberStore) *ChannelService {
	return &ChannelService{workspaces: workspaces, channels: channels, members: members}
}

// CreateChannel creates a public or private channel in an active workspace.
// Private channels add the creator as a channel member in the storage transaction.
//
// Creating a channel takes no management role (BUG #393): a plain member and an
// owner take the same path. The one role it excludes is guest, via
// domain.CanCreateChannel — a guest reaches only the channels it was added to,
// and creating channels would hand back the workspace-wide scope RF-74 removes.
// Update and archive remain management operations. The membership is read
// server-side from the caller bound by the auth middleware, so a role or an
// actor claimed by the client changes nothing here.
//
// The authoritative decision is CreateChannelForActiveMember's, which locks the
// workspace and the membership and inserts from them in one statement — role
// list included. The check below is the same predicate, not a second one: it
// exists so a caller with no business in this workspace is refused before the
// input validation can tell them whether a category ID exists in it.
func (s *ChannelService) CreateChannel(ctx context.Context, input CreateChannelInput) (domain.Channel, error) {
	member, err := s.requireActiveWorkspaceMember(ctx, input.WorkspaceID, input.CallerID)
	if err != nil {
		return domain.Channel{}, err
	}
	if !domain.CanCreateChannel(&member) {
		return domain.Channel{}, domain.ErrForbidden
	}

	slug, displayName, err := normalizeChannelFields(input.Slug, input.DisplayName)
	if err != nil {
		return domain.Channel{}, err
	}
	if slug == generalChannelSlug {
		return domain.Channel{}, fmt.Errorf("%w: geral is reserved", domain.ErrInvalidInput)
	}
	if err := validateChannelType(input.Type); err != nil {
		return domain.Channel{}, err
	}
	categoryID := strings.TrimSpace(input.CategoryID)
	if err := s.requireCategoryInWorkspace(ctx, input.WorkspaceID, categoryID); err != nil {
		return domain.Channel{}, err
	}

	createInput := storage.CreateChannelInput{
		WorkspaceID: input.WorkspaceID,
		CategoryID:  categoryID,
		Slug:        slug,
		DisplayName: displayName,
		Type:        input.Type,
		IsGeneral:   false,
		Position:    input.Position,
		CreatedBy:   input.CallerID,
	}
	// A private channel nobody belongs to is invisible to its own creator, so
	// the membership is part of the same transaction rather than a follow-up
	// write that could fail on its own.
	if input.Type == domain.ChannelTypePrivate {
		createInput.EnsureCreatorMemberRole = domain.ChannelRoleMember
	}
	return s.channels.CreateChannelForActiveMember(ctx, createInput)
}

// ListChannels returns channels visible to callerID in workspaceID.
func (s *ChannelService) ListChannels(ctx context.Context, workspaceID, callerID string) ([]domain.Channel, error) {
	if _, err := s.requireActiveWorkspaceMember(ctx, workspaceID, callerID); err != nil {
		return nil, err
	}
	channels, err := s.channels.ListVisibleChannelsByUser(ctx, workspaceID, callerID)
	if err != nil {
		return nil, fmt.Errorf("list visible channels: %w", err)
	}
	return channels, nil
}

// ChannelDetailsInput asks for the channel-details panel payload (issue #435).
// The workspace is resolved server-side by the handler and the caller is the
// authenticated principal; neither is ever taken from the request body.
type ChannelDetailsInput struct {
	WorkspaceID string
	CallerID    string
	ChannelID   string
	// OnlineUserIDs is the presence snapshot for the workspace, resolved by the
	// handler from the presence source in one batch. It is a *filter*, never a
	// source of identity: only IDs that are also active members of this channel
	// survive the store's join, so a stale or foreign entry selects nothing.
	// Empty means nobody is online and the preview comes back empty — absence of
	// presence is never read as "online".
	OnlineUserIDs []string
	// MemberLimit caps the online-member preview. Values outside
	// (0, domain.MaxChannelDetailsMembers] are clamped by the store.
	MemberLimit int
}

// ChannelDetails is the panel payload: the channel itself, a capped preview of
// the members who are online, and the two totals behind it.
//
// MemberCount is every active member of the channel and is deliberately not
// derived from OnlineMembers — the panel shows it as the channel's size, which
// does not change when someone disconnects.
type ChannelDetails struct {
	Channel       domain.Channel
	OnlineMembers []domain.ChannelMemberProfile
	OnlineCount   int
	MemberCount   int
	// CanManageMembers is the server's own answer to "may this caller add
	// participants" (issue #398), derived from the membership this method already
	// had to load. It exists so the panel can disable an action the server would
	// refuse, and it is never the control: POST .../members re-derives the
	// decision from the session on every call. A client that ignores it gets a
	// 403, not a membership row.
	//
	// It is false for #geral, matching the write path: membership there is owned
	// by the workspace sync, not by this flow.
	CanManageMembers bool
}

// GetChannelDetails returns the channel-details payload for a channel the
// caller may read.
//
// Visibility is settled first, by the same GetVisibleChannelByID predicate the
// rest of the channel surface uses: a private channel the caller does not
// belong to, an archived channel, a channel in another workspace and a channel
// that does not exist all come back as a bare ErrNotFound, so the endpoint
// cannot be used to probe which channel UUIDs exist. Only after that does the
// member query run, so an unauthorised caller never reaches a roster — the
// presence snapshot the handler collected is likewise never returned to a
// caller who fails this gate.
func (s *ChannelService) GetChannelDetails(ctx context.Context, input ChannelDetailsInput) (ChannelDetails, error) {
	member, err := s.requireActiveWorkspaceMember(ctx, input.WorkspaceID, input.CallerID)
	if err != nil {
		return ChannelDetails{}, err
	}
	channel, err := s.channels.GetVisibleChannelByID(ctx, input.WorkspaceID, input.ChannelID, input.CallerID)
	if err != nil {
		return ChannelDetails{}, err
	}
	page, err := s.members.ListOnlineChannelMemberProfiles(
		ctx, input.WorkspaceID, channel.ID, input.OnlineUserIDs, input.MemberLimit,
	)
	if err != nil {
		return ChannelDetails{}, fmt.Errorf("list online channel member profiles: %w", err)
	}
	return ChannelDetails{
		Channel:       channel,
		OnlineMembers: page.Online,
		OnlineCount:   page.OnlineCount,
		MemberCount:   page.TotalCount,
		// The same predicate the write path checks, evaluated on the membership
		// already loaded above — not a second, parallel rule that could drift.
		CanManageMembers: !channel.IsGeneral && domain.CanManageChannelMembers(&member),
	}, nil
}

// ChannelCallParticipantProfilesInput asks for presentation identities of a
// specific set of call participants (issue #612). UserIDs is the caller's
// own LiveKit room roster — never trusted as "these are real members", only
// as "resolve these if they are".
type ChannelCallParticipantProfilesInput struct {
	WorkspaceID string
	CallerID    string
	ChannelID   string
	UserIDs     []string
}

// GetCallParticipantProfiles resolves display name and avatar for the
// requested user IDs, scoped to one channel's active membership (issue
// #612). Visibility is settled first, exactly like GetChannelDetails: an
// invisible or foreign channel is ErrNotFound before any identity is read,
// so this cannot be used to probe channel existence or membership. UserIDs
// that are not active members of the channel are silently omitted from the
// result rather than erroring — an unresolvable participant is a client-side
// "degrade to initials" case, not a request failure.
func (s *ChannelService) GetCallParticipantProfiles(ctx context.Context, input ChannelCallParticipantProfilesInput) ([]domain.CallParticipantProfile, error) {
	if _, err := s.requireActiveWorkspaceMember(ctx, input.WorkspaceID, input.CallerID); err != nil {
		return nil, err
	}
	channel, err := s.channels.GetVisibleChannelByID(ctx, input.WorkspaceID, input.ChannelID, input.CallerID)
	if err != nil {
		return nil, err
	}
	userIDs, err := normalizeCallParticipantIDs(input.UserIDs)
	if err != nil {
		return nil, err
	}
	profiles, err := s.members.ListChannelMemberProfilesByIDs(ctx, input.WorkspaceID, channel.ID, userIDs)
	if err != nil {
		return nil, fmt.Errorf("list channel member profiles by ids: %w", err)
	}
	return profiles, nil
}

// GetChannel returns one visibility-bound active channel by ID or slug.
func (s *ChannelService) GetChannel(ctx context.Context, input GetChannelInput) (domain.Channel, error) {
	if _, err := s.requireActiveWorkspaceMember(ctx, input.WorkspaceID, input.CallerID); err != nil {
		return domain.Channel{}, err
	}
	if input.ChannelID != "" {
		ch, err := s.channels.GetVisibleChannelByID(ctx, input.WorkspaceID, input.ChannelID, input.CallerID)
		if err != nil {
			return domain.Channel{}, err
		}
		return ch, nil
	}

	slug := strings.ToLower(strings.TrimSpace(input.Slug))
	if !slugRE.MatchString(slug) {
		return domain.Channel{}, fmt.Errorf("%w: slug must be lowercase alphanumeric with optional internal hyphens, no leading/trailing hyphens, max 63 chars", domain.ErrInvalidInput)
	}
	ch, err := s.channels.GetVisibleChannelBySlug(ctx, input.WorkspaceID, slug, input.CallerID)
	if err != nil {
		return domain.Channel{}, err
	}
	return ch, nil
}

// UpdateChannel updates mutable fields for a non-general channel.
//
// Two authorization checks, and they are not redundant. This one runs before
// anything is read or written so a caller with no business in this workspace
// gets a plain ErrForbidden instead of learning, from a validation error,
// whether a channel or a category exists in it. The one that *decides* is
// PGXChannelStore.UpdateChannel's, taken inside the write transaction with the
// membership row locked — a role revoked after this line still stops the write.
func (s *ChannelService) UpdateChannel(ctx context.Context, input UpdateChannelInput) (storage.UpdateChannelResult, error) {
	if _, err := s.requireManagePermission(ctx, input.WorkspaceID, input.CallerID); err != nil {
		return storage.UpdateChannelResult{}, err
	}

	current, err := s.channels.GetChannelByIDInWorkspace(ctx, input.WorkspaceID, input.ChannelID)
	if err != nil {
		return storage.UpdateChannelResult{}, err
	}
	if current.IsGeneral {
		return storage.UpdateChannelResult{}, fmt.Errorf("%w: geral is immutable", domain.ErrInvalidInput)
	}

	next := storage.UpdateChannelInput{
		// The actor, not a decision about the actor. requireManagePermission
		// above is a fail-fast for a legible error; the authorization that
		// governs the write is re-derived from the database inside the store's
		// transaction, holding the membership row, so a role revoked between the
		// two cannot be outrun. See PGXChannelStore.UpdateChannel.
		CallerID:    input.CallerID,
		WorkspaceID: input.WorkspaceID,
		ChannelID:   input.ChannelID,
		CategoryID:  current.CategoryID,
		Slug:        current.Slug,
		DisplayName: current.DisplayName,
		Type:        current.Type,
		Position:    current.Position,
	}
	if err := s.applyChannelUpdatePatch(ctx, input, &next); err != nil {
		return storage.UpdateChannelResult{}, err
	}
	if current.Type == domain.ChannelTypePublic && next.Type == domain.ChannelTypePrivate {
		next.EnsureMemberUserID = input.CallerID
	}

	updated, err := s.channels.UpdateChannel(ctx, next)
	if err != nil {
		return storage.UpdateChannelResult{}, err
	}
	return updated, nil
}

// applyChannelUpdatePatch overlays the fields the request actually set onto the
// channel's current values.
//
// Every field is optional and each one is validated by its own rule, which is
// why they are separate helpers rather than one long sequence of ifs: a rename
// and a re-categorisation fail for unrelated reasons, and reading them apart is
// how the reasons stay distinguishable.
func (s *ChannelService) applyChannelUpdatePatch(ctx context.Context, input UpdateChannelInput, next *storage.UpdateChannelInput) error {
	if err := applyChannelSlugPatch(input, next); err != nil {
		return err
	}
	if err := applyChannelDisplayNamePatch(input, next); err != nil {
		return err
	}
	if err := s.applyChannelCategoryPatch(ctx, input, next); err != nil {
		return err
	}
	// Position carries no rule of its own: any int is an ordering hint.
	if input.Position != nil {
		next.Position = *input.Position
	}
	return applyChannelTypePatch(input, next)
}

// applyChannelSlugPatch refuses a malformed slug and the reserved one. "geral"
// is reserved because the general channel is identified structurally, and a
// second channel wearing its slug is a name collision waiting to be mistaken for
// it.
func applyChannelSlugPatch(input UpdateChannelInput, next *storage.UpdateChannelInput) error {
	if input.Slug == "" {
		return nil
	}
	slug := strings.ToLower(strings.TrimSpace(input.Slug))
	if !slugRE.MatchString(slug) {
		return fmt.Errorf("%w: slug must be lowercase alphanumeric with optional internal hyphens, no leading/trailing hyphens, max 63 chars", domain.ErrInvalidInput)
	}
	if slug == generalChannelSlug {
		return fmt.Errorf("%w: geral is reserved", domain.ErrInvalidInput)
	}
	next.Slug = slug
	return nil
}

// applyChannelDisplayNamePatch normalises through the same helper creation uses:
// a rename must not be the way past a cap the create path enforces.
func applyChannelDisplayNamePatch(input UpdateChannelInput, next *storage.UpdateChannelInput) error {
	if input.DisplayName == "" {
		return nil
	}
	displayName, err := domain.NormalizeChannelDisplayName(input.DisplayName)
	if err != nil {
		return err
	}
	next.DisplayName = displayName
	return nil
}

// applyChannelCategoryPatch requires the category to belong to this workspace,
// so a channel cannot be filed under a category from another one.
func (s *ChannelService) applyChannelCategoryPatch(ctx context.Context, input UpdateChannelInput, next *storage.UpdateChannelInput) error {
	if input.CategoryID == nil {
		return nil
	}
	categoryID := strings.TrimSpace(*input.CategoryID)
	if err := s.requireCategoryInWorkspace(ctx, input.WorkspaceID, categoryID); err != nil {
		return err
	}
	next.CategoryID = categoryID
	return nil
}

func applyChannelTypePatch(input UpdateChannelInput, next *storage.UpdateChannelInput) error {
	if input.Type == nil {
		return nil
	}
	if err := validateChannelType(*input.Type); err != nil {
		return err
	}
	next.Type = *input.Type
	return nil
}

// ArchiveChannel marks a non-general channel archived. It never hard-deletes.
func (s *ChannelService) ArchiveChannel(ctx context.Context, workspaceID, channelID, callerID string) (domain.Channel, error) {
	if _, err := s.requireManagePermission(ctx, workspaceID, callerID); err != nil {
		return domain.Channel{}, err
	}
	current, err := s.channels.GetChannelByIDInWorkspace(ctx, workspaceID, channelID)
	if err != nil {
		return domain.Channel{}, err
	}
	if current.IsGeneral {
		return domain.Channel{}, fmt.Errorf("%w: geral is immutable", domain.ErrInvalidInput)
	}
	archived, err := s.channels.ArchiveChannel(ctx, workspaceID, channelID)
	if err != nil {
		return domain.Channel{}, err
	}
	return archived, nil
}

func (s *ChannelService) requireManagePermission(ctx context.Context, workspaceID, userID string) (domain.WorkspaceMember, error) {
	return requireWorkspaceManager(ctx, s.workspaces, s.members, workspaceID, userID)
}

func (s *ChannelService) requireActiveWorkspaceMember(ctx context.Context, workspaceID, userID string) (domain.WorkspaceMember, error) {
	return requireActiveWorkspaceMember(ctx, s.workspaces, s.members, workspaceID, userID)
}

// requireWorkspaceManager and requireActiveWorkspaceMember are package-level so
// that every service in this package decides membership and management rights
// from the same code. They were methods on ChannelService until RF-17 needed the
// same two predicates for channel categories; copying an authorization predicate
// is how the two copies eventually stop agreeing.
//
// Both return domain.ErrForbidden for every denial, and deliberately never
// distinguish a missing workspace from a disabled one or from a caller who is not
// a member: a caller with no business in a workspace must not learn from the
// error whether it exists.
func requireWorkspaceManager(ctx context.Context, workspaces storage.WorkspaceStore, members storage.MemberStore, workspaceID, userID string) (domain.WorkspaceMember, error) {
	member, err := requireActiveWorkspaceMember(ctx, workspaces, members, workspaceID, userID)
	if err != nil {
		return domain.WorkspaceMember{}, err
	}
	if !domain.CanManageWorkspace(&member) {
		return domain.WorkspaceMember{}, domain.ErrForbidden
	}
	return member, nil
}

func requireActiveWorkspaceMember(ctx context.Context, workspaces storage.WorkspaceStore, members storage.MemberStore, workspaceID, userID string) (domain.WorkspaceMember, error) {
	workspace, err := workspaces.GetWorkspaceByID(ctx, workspaceID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return domain.WorkspaceMember{}, domain.ErrForbidden
		}
		return domain.WorkspaceMember{}, fmt.Errorf("get workspace: %w", err)
	}
	if workspace.Status != domain.WorkspaceStatusActive {
		return domain.WorkspaceMember{}, domain.ErrForbidden
	}

	member, err := members.GetWorkspaceMember(ctx, workspaceID, userID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return domain.WorkspaceMember{}, domain.ErrForbidden
		}
		return domain.WorkspaceMember{}, fmt.Errorf("get workspace member: %w", err)
	}
	if member.Status != domain.MemberStatusActive || member.WorkspaceID != workspaceID {
		return domain.WorkspaceMember{}, domain.ErrForbidden
	}
	return member, nil
}

func (s *ChannelService) requireCategoryInWorkspace(ctx context.Context, workspaceID, categoryID string) error {
	categoryID = strings.TrimSpace(categoryID)
	if categoryID == "" {
		return nil
	}
	if _, err := s.channels.GetCategoryByIDInWorkspace(ctx, workspaceID, categoryID); err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return fmt.Errorf("%w: category_id must belong to workspace", domain.ErrInvalidInput)
		}
		return fmt.Errorf("get category: %w", err)
	}
	return nil
}

func normalizeChannelFields(slug, displayName string) (string, string, error) {
	slug = strings.ToLower(strings.TrimSpace(slug))
	if !slugRE.MatchString(slug) {
		return "", "", fmt.Errorf("%w: slug must be lowercase alphanumeric with optional internal hyphens, no leading/trailing hyphens, max 63 chars", domain.ErrInvalidInput)
	}
	displayName, err := domain.NormalizeChannelDisplayName(displayName)
	if err != nil {
		return "", "", err
	}
	return slug, displayName, nil
}

func validateChannelType(t domain.ChannelType) error {
	if t != domain.ChannelTypePublic && t != domain.ChannelTypePrivate {
		return fmt.Errorf("%w: type must be public or private", domain.ErrInvalidInput)
	}
	return nil
}

// ── Self-leave (issue #527) ──────────────────────────────────────────────────

// LeaveChannel removes the caller's own membership from a channel.
//
// Deliberately not MemberService.RemoveMemberFromChannel with the caller as its
// own target. That method is the administrative removal: it takes a workspace
// moderation role and names a target user, and reaching it with "myself" would
// mean a plain member could not leave at all — while an endpoint that accepted a
// user ID from someone with no authority over anyone would be a privilege
// escalation wearing the shape of a membership change. Leaving takes no role,
// because the only row it can affect is the actor's own.
//
// Two structural refusals, and both are re-derived below in SQL rather than
// trusted from here: the general channel cannot be left, and a channel outside
// this workspace does not exist as far as this caller is concerned. The check
// here is the fail-fast that produces a legible error; LeaveChannelSelf's is the
// one that decides, serialized with the write.
func (s *ChannelService) LeaveChannel(ctx context.Context, workspaceID, channelID, callerID string) (storage.LeaveConversationResult, error) {
	if _, err := s.requireActiveWorkspaceMember(ctx, workspaceID, callerID); err != nil {
		return storage.LeaveConversationResult{}, err
	}
	current, err := s.channels.GetVisibleChannelByID(ctx, workspaceID, channelID, callerID)
	if err != nil {
		return storage.LeaveConversationResult{}, err
	}
	if !domain.CanLeaveChannel(current) {
		return storage.LeaveConversationResult{}, domain.ErrGeneralChannelImmutable
	}
	return s.channels.LeaveChannelSelf(ctx, workspaceID, channelID, callerID)
}
