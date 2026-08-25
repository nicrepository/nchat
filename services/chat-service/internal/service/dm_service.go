package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

const (
	minGroupDMParticipants = 3
	// maxGroupDMParticipants bounds the participant list of one *creation*
	// request, caller included. Without it a single request could fan out into an
	// unbounded number of membership rows and per-participant eligibility
	// look-ups.
	//
	// It is not a capacity: a group has no total participant limit, and
	// AddGroupParticipants (issue #398) grows one past this number freely. The
	// two are different questions — how large one payload may be, and how large
	// a conversation may become — and only the first has an answer here.
	maxGroupDMParticipants  = 50
	maxDMTitleRunes         = 120
	minDMCandidateQuery     = 2
	maxDMCandidateQuery     = 64
	defaultDMCandidateLimit = 20
	maxDMCandidateLimit     = 50
)

// CreateDirectConversationInput contains caller-provided fields for 1:1 DM creation.
type CreateDirectConversationInput struct {
	WorkspaceID string
	CallerID    string
	OtherUserID string
}

type CreateDirectConversationOutput struct {
	Conversation domain.DMConversation
	Created      bool
}

type SearchDMCandidatesInput struct {
	WorkspaceID string
	CallerID    string
	Query       string
	Limit       int // Zero uses the server default.
}

// CreateGroupConversationInput contains caller-provided fields for ad-hoc group DM creation.
type CreateGroupConversationInput struct {
	WorkspaceID        string
	CallerID           string
	ParticipantUserIDs []string
	Title              string
}

// GetDMConversationInput identifies a visible DM conversation read.
type GetDMConversationInput struct {
	WorkspaceID    string
	CallerID       string
	ConversationID string
}

// DMService handles direct and ad-hoc group DM use cases.
//
// It deliberately holds no WorkspaceStore: every DM decision goes through
// MemberStore.GetEligibleDMMember, whose query already requires the workspace to
// be active, so a separate workspace read would be a second source of truth for
// the same rule.
type DMService struct {
	dms     storage.DMStore
	members storage.MemberStore
}

func NewDMService(dms storage.DMStore, members storage.MemberStore) *DMService {
	return &DMService{dms: dms, members: members}
}

// CreateDirectConversation creates or returns the canonical 1:1 DM for caller and other user.
// Archived direct DMs are reactivated by the storage upsert instead of creating duplicates.
func (s *DMService) CreateDirectConversation(ctx context.Context, input CreateDirectConversationInput) (domain.DMConversation, error) {
	result, err := s.GetOrCreateDirectConversation(ctx, input)
	return result.Conversation, err
}

// GetOrCreateDirectConversation returns the canonical direct DM and whether this call created it.
func (s *DMService) GetOrCreateDirectConversation(ctx context.Context, input CreateDirectConversationInput) (CreateDirectConversationOutput, error) {
	workspaceID := strings.TrimSpace(input.WorkspaceID)
	rawCallerID := strings.TrimSpace(input.CallerID)
	rawOtherUserID := strings.TrimSpace(input.OtherUserID)
	if workspaceID == "" || rawCallerID == "" || rawOtherUserID == "" {
		return CreateDirectConversationOutput{}, fmt.Errorf("%w: workspace_id, caller_id, and other_user_id are required", domain.ErrInvalidInput)
	}
	callerID, err := canonicalizeUserID(rawCallerID)
	if err != nil {
		return CreateDirectConversationOutput{}, err
	}
	otherUserID, err := canonicalizeUserID(rawOtherUserID)
	if err != nil {
		return CreateDirectConversationOutput{}, err
	}
	if callerID == otherUserID {
		return CreateDirectConversationOutput{}, fmt.Errorf("%w: self-DM is not supported", domain.ErrInvalidInput)
	}

	callerMember, err := s.requireEligibleDMMember(ctx, workspaceID, callerID)
	if err != nil {
		return CreateDirectConversationOutput{}, err
	}
	otherMember, err := s.requireEligibleDMMember(ctx, workspaceID, otherUserID)
	if err != nil {
		return CreateDirectConversationOutput{}, err
	}

	result, err := s.dms.CreateDirectConversation(ctx, storage.CreateDirectConversationInput{
		WorkspaceID:        workspaceID,
		CreatedBy:          callerMember.UserID,
		DirectPairKey:      canonicalDirectPairKey(callerMember.UserID, otherMember.UserID),
		ParticipantUserIDs: []string{callerMember.UserID, otherMember.UserID},
	})
	if err != nil {
		return CreateDirectConversationOutput{}, fmt.Errorf("create direct conversation: %w", err)
	}
	return CreateDirectConversationOutput{Conversation: result.Conversation, Created: result.Created}, nil
}

// SearchDMCandidates returns active same-workspace users eligible for a direct DM.
func (s *DMService) SearchDMCandidates(ctx context.Context, input SearchDMCandidatesInput) ([]domain.DMCandidate, error) {
	workspaceID := strings.TrimSpace(input.WorkspaceID)
	query := strings.TrimSpace(input.Query)
	queryRunes := utf8.RuneCountInString(query)
	if workspaceID == "" || queryRunes < minDMCandidateQuery || queryRunes > maxDMCandidateQuery {
		return nil, fmt.Errorf("%w: invalid dm candidate search", domain.ErrInvalidInput)
	}
	limit := input.Limit
	if limit < 0 {
		return nil, fmt.Errorf("%w: invalid dm candidate limit", domain.ErrInvalidInput)
	}
	if limit == 0 {
		limit = defaultDMCandidateLimit
	}
	if limit > maxDMCandidateLimit {
		limit = maxDMCandidateLimit
	}
	callerID, err := canonicalizeUserID(strings.TrimSpace(input.CallerID))
	if err != nil {
		return nil, err
	}
	if _, err := s.requireEligibleDMMember(ctx, workspaceID, callerID); err != nil {
		return nil, err
	}
	candidates, err := s.members.SearchDMCandidates(ctx, workspaceID, callerID, query, limit)
	if err != nil {
		return nil, fmt.Errorf("search dm candidates: %w", err)
	}
	return candidates, nil
}

func (s *DMService) requireEligibleDMMember(ctx context.Context, workspaceID, userID string) (domain.WorkspaceMember, error) {
	member, err := s.members.GetEligibleDMMember(ctx, workspaceID, userID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return domain.WorkspaceMember{}, domain.ErrForbidden
		}
		return domain.WorkspaceMember{}, err
	}
	return member, nil
}

// CreateGroupConversation creates an ad-hoc group DM and automatically includes the caller.
func (s *DMService) CreateGroupConversation(ctx context.Context, input CreateGroupConversationInput) (domain.DMConversation, error) {
	workspaceID := strings.TrimSpace(input.WorkspaceID)
	rawCallerID := strings.TrimSpace(input.CallerID)
	if workspaceID == "" || rawCallerID == "" {
		return domain.DMConversation{}, fmt.Errorf("%w: workspace_id and caller_id are required", domain.ErrInvalidInput)
	}
	callerID, err := canonicalizeUserID(rawCallerID)
	if err != nil {
		return domain.DMConversation{}, err
	}
	title, err := normalizeDMTitle(input.Title)
	if err != nil {
		return domain.DMConversation{}, err
	}
	participantUserIDs, err := normalizeGroupDMParticipants(callerID, input.ParticipantUserIDs)
	if err != nil {
		return domain.DMConversation{}, err
	}

	// Every participant, the caller included, goes through the same eligibility
	// rule as a 1:1 DM: active workspace, active membership, and an active,
	// non-deleted account. A workspace membership alone is not enough — it
	// outlives a suspended or deleted account. The failure is the undifferentiated
	// ErrForbidden, so an ineligible, unknown and foreign-workspace participant
	// are indistinguishable to the caller.
	canonicalParticipants := make([]string, 0, len(participantUserIDs))
	for _, uid := range participantUserIDs {
		m, err := s.requireEligibleDMMember(ctx, workspaceID, uid)
		if err != nil {
			return domain.DMConversation{}, err
		}
		canonicalParticipants = append(canonicalParticipants, m.UserID)
	}

	conversation, err := s.dms.CreateGroupConversation(ctx, storage.CreateGroupConversationInput{
		WorkspaceID:        workspaceID,
		CreatedBy:          callerID,
		Title:              title,
		ParticipantUserIDs: canonicalParticipants,
	})
	if err != nil {
		return domain.DMConversation{}, fmt.Errorf("create group conversation: %w", err)
	}
	return conversation, nil
}

// AddGroupParticipantsInput asks to add participants to an existing group DM
// (issue #398).
//
// WorkspaceID is resolved server-side and CallerID is the authenticated
// principal. There is no role, status or created_by field: a group's membership
// role is closed by CHECK to 'member', so there is nothing for a caller to set.
type AddGroupParticipantsInput struct {
	WorkspaceID    string
	CallerID       string
	ConversationID string
	UserIDs        []string
}

// AddGroupParticipants adds active workspace members to an existing group DM.
//
// Authorization is participation, and it is settled by the same
// GetVisibleConversationByID predicate every other DM read uses: an active
// workspace, an active workspace membership, an active conversation, and an
// active dm_members row for the caller. A caller who is not in the group cannot
// tell a group they are excluded from apart from one that does not exist —
// both are the same bare ErrNotFound.
//
// Participation is the whole policy on purpose. chat.dm_members.role is closed
// by CHECK to the single value 'member', so a group has no owner, admin or
// moderator to require; there is no privileged participant to be. Routing this
// through domain.CanManageChannelMembers would be worse than useless: a
// workspace admin is not a participant, cannot see the conversation under the
// SQL policy above, and giving them authority over a private peer conversation
// they cannot read would be an escalation rather than a control. Any participant
// may already create a new group with anyone in the workspace, so "a participant
// may add a participant" grants no power that is not already held. The
// divergence from the issue's generic wording is recorded in SECURITY.md.
//
// A 1:1 conversation is refused with the same ErrNotFound, checked against the
// row the database returned rather than anything the client said. Adding a third
// person to a direct DM would silently convert it into a group, and that
// conversion is explicitly out of scope: direct uniqueness is keyed on the
// unordered pair, so the row would keep a direct_pair_key describing a
// conversation that is no longer a pair.
//
// The store re-establishes all of it under a row lock, which is what makes the
// authorization hold against a concurrent revocation. There is no capacity
// check: groups have no fixed participant limit.
func (s *DMService) AddGroupParticipants(ctx context.Context, input AddGroupParticipantsInput) (storage.AddMembersResult, error) {
	workspaceID := strings.TrimSpace(input.WorkspaceID)
	conversation, err := s.dms.GetVisibleConversationByID(
		ctx,
		workspaceID,
		strings.TrimSpace(input.ConversationID),
		strings.TrimSpace(input.CallerID),
	)
	if err != nil {
		return storage.AddMembersResult{}, err
	}
	if conversation.Type != domain.DMConversationTypeGroup {
		return storage.AddMembersResult{}, domain.ErrNotFound
	}

	userIDs, err := normalizeAddMemberIDs(input.UserIDs)
	if err != nil {
		return storage.AddMembersResult{}, err
	}

	// The caller is handed to the store so the transaction re-establishes their
	// participation itself. GetVisibleConversationByID above refuses a stranger
	// before any payload work, but its answer is not the permission the write
	// runs under — participation can be revoked in between.
	//
	// No participant ceiling is passed: a group has no fixed capacity. The only
	// bound is normalizeAddMemberIDs above, which caps one *request* at
	// domain.MaxAddMembersPerRequest; successive requests may keep growing the
	// conversation.
	result, err := s.dms.AddGroupParticipants(ctx, storage.AddGroupParticipantsInput{
		WorkspaceID:    workspaceID,
		ConversationID: conversation.ID,
		CallerID:       strings.TrimSpace(input.CallerID),
		UserIDs:        userIDs,
	})
	if err != nil {
		if errors.Is(err, domain.ErrForbidden) ||
			errors.Is(err, domain.ErrNotFound) ||
			errors.Is(err, domain.ErrInvalidInput) {
			return storage.AddMembersResult{}, err
		}
		return storage.AddMembersResult{}, fmt.Errorf("add group participants: %w", err)
	}
	return result, nil
}

// GroupDetailsInput asks for the group-details panel payload (issue #441).
// The workspace is resolved server-side by the handler and the caller is the
// authenticated principal; neither is ever taken from the request.
type GroupDetailsInput struct {
	WorkspaceID    string
	CallerID       string
	ConversationID string
	// ParticipantLimit caps the participant preview. Values outside
	// (0, domain.MaxDMDetailsParticipants] are clamped by the store.
	ParticipantLimit int
}

// GroupDetails is the panel payload: the conversation itself, a capped preview
// of its participants and the authoritative participant total.
//
// ParticipantCount is every active participant and is deliberately not derived
// from Participants, which is only as long as the preview cap allows.
type GroupDetails struct {
	Conversation     domain.DMConversation
	Participants     []domain.DMParticipantProfile
	ParticipantCount int
	// CanManageMembers is the server's answer to "may this caller add
	// participants" (issue #398).
	//
	// For a group it is true whenever the payload exists at all, and that is not
	// a shortcut: the policy *is* active participation, and
	// GetVisibleConversationByID succeeding already means the caller is an
	// active participant of an active conversation. The field is still sent so
	// the panel reads one shape for channels and groups, and so a future policy
	// change has one place to become false. It remains a rendering hint —
	// POST .../members re-derives it inside its own transaction.
	CanManageMembers bool
}

// GetGroupDetails returns the group-details payload for a group conversation
// the caller participates in.
//
// Access is settled first, by the same GetVisibleConversationByID predicate the
// rest of the DM surface uses: a conversation that does not exist, is archived,
// belongs to another workspace, or that the caller is not an active participant
// of all come back as a bare ErrNotFound, so the endpoint cannot be used to
// probe which conversation UUIDs exist. Only after that are participants read,
// so an unauthorised caller never reaches a roster.
//
// A 1:1 conversation is refused with the same ErrNotFound. Details for direct
// messages are out of scope for this issue, and answering for them here would
// ship an unreviewed surface — the type is checked against the row the database
// returned, never against anything the client said.
func (s *DMService) GetGroupDetails(ctx context.Context, input GroupDetailsInput) (GroupDetails, error) {
	workspaceID := strings.TrimSpace(input.WorkspaceID)
	conversation, err := s.dms.GetVisibleConversationByID(
		ctx,
		workspaceID,
		strings.TrimSpace(input.ConversationID),
		strings.TrimSpace(input.CallerID),
	)
	if err != nil {
		return GroupDetails{}, err
	}
	if conversation.Type != domain.DMConversationTypeGroup {
		return GroupDetails{}, domain.ErrNotFound
	}
	page, err := s.dms.ListParticipantProfiles(ctx, workspaceID, conversation.ID, input.ParticipantLimit)
	if err != nil {
		return GroupDetails{}, fmt.Errorf("list dm participant profiles: %w", err)
	}
	return GroupDetails{
		Conversation:     conversation,
		Participants:     page.Participants,
		ParticipantCount: page.TotalCount,
		CanManageMembers: true,
	}, nil
}

// GroupCallParticipantProfilesInput asks for presentation identities of a
// specific set of group-call participants (issue #612).
type GroupCallParticipantProfilesInput struct {
	WorkspaceID    string
	CallerID       string
	ConversationID string
	UserIDs        []string
}

// GetGroupCallParticipantProfiles resolves display name and avatar for the
// requested user IDs, scoped to one group conversation's active
// participants (issue #612). Same access gate as GetGroupDetails — a 1:1
// conversation and one the caller does not participate in both come back as
// ErrNotFound — and unresolvable IDs are silently omitted rather than
// erroring, for the same reason GetCallParticipantProfiles omits them.
func (s *DMService) GetGroupCallParticipantProfiles(ctx context.Context, input GroupCallParticipantProfilesInput) ([]domain.CallParticipantProfile, error) {
	workspaceID := strings.TrimSpace(input.WorkspaceID)
	conversation, err := s.dms.GetVisibleConversationByID(
		ctx, workspaceID, strings.TrimSpace(input.ConversationID), strings.TrimSpace(input.CallerID),
	)
	if err != nil {
		return nil, err
	}
	if conversation.Type != domain.DMConversationTypeGroup {
		return nil, domain.ErrNotFound
	}
	userIDs, err := normalizeCallParticipantIDs(input.UserIDs)
	if err != nil {
		return nil, err
	}
	profiles, err := s.dms.ListParticipantProfilesByIDs(ctx, workspaceID, conversation.ID, userIDs)
	if err != nil {
		return nil, fmt.Errorf("list dm participant profiles by ids: %w", err)
	}
	return profiles, nil
}

// normalizeCallParticipantIDs canonicalises, de-duplicates and bounds a
// requested call-participant ID list (issue #612), the same shape
// normalizeAddMemberIDs applies to add-members but against the call cap
// instead of the add-members cap — the two batches answer different
// questions (who to add vs. whose identity to resolve) and must not share
// one error message.
func normalizeCallParticipantIDs(raw []string) ([]string, error) {
	if len(raw) > domain.MaxCallParticipantProfileIDs {
		return nil, domain.ErrTooManyCallParticipantsRequested
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
		return nil, domain.ErrNoCallParticipantsRequested
	}
	userIDs := make([]string, 0, len(unique))
	for userID := range unique {
		userIDs = append(userIDs, userID)
	}
	sort.Strings(userIDs)
	return userIDs, nil
}

// SearchGroupParticipantCandidatesInput asks who could still be added to a group
// (issue #398).
type SearchGroupParticipantCandidatesInput struct {
	WorkspaceID    string
	CallerID       string
	ConversationID string
	Query          string
	Limit          int // Zero uses the server default.
}

// SearchGroupParticipantCandidates returns workspace members eligible to be
// added to a group, with current participants already excluded by the store.
//
// Access is settled first by GetVisibleConversationByID — the same predicate the
// rest of the DM surface uses — so a caller who does not participate cannot use
// this to learn who is in a group they cannot see, and a 1:1 is refused with the
// same bare ErrNotFound as a conversation that does not exist. The policy is
// unchanged: participation is what authorises, exactly as for the write.
//
// Current participants are excluded in SQL. The panel's participant list is
// capped at domain.MaxDMDetailsParticipants, so in a larger group it was never
// a complete roster; deciding eligibility from it is the defect this replaces.
func (s *DMService) SearchGroupParticipantCandidates(
	ctx context.Context, input SearchGroupParticipantCandidatesInput,
) ([]domain.DMCandidate, error) {
	workspaceID := strings.TrimSpace(input.WorkspaceID)
	callerID := strings.TrimSpace(input.CallerID)
	conversation, err := s.dms.GetVisibleConversationByID(
		ctx, workspaceID, strings.TrimSpace(input.ConversationID), callerID,
	)
	if err != nil {
		return nil, err
	}
	if conversation.Type != domain.DMConversationTypeGroup {
		return nil, domain.ErrNotFound
	}

	query, limit, err := normalizeCandidateSearch(input.Query, input.Limit)
	if err != nil {
		return nil, err
	}

	candidates, err := s.dms.SearchGroupParticipantCandidates(
		ctx, workspaceID, conversation.ID, callerID, query, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("search group participant candidates: %w", err)
	}
	return candidates, nil
}

// ListConversations returns active DM conversations visible to callerID in workspaceID.
// Visibility is enforced in SQL by the DM store.
func (s *DMService) ListConversations(ctx context.Context, workspaceID, callerID string) ([]domain.DMConversation, error) {
	conversations, err := s.dms.ListVisibleConversationsByUser(ctx, strings.TrimSpace(workspaceID), strings.TrimSpace(callerID))
	if err != nil {
		return nil, fmt.Errorf("list visible dm conversations: %w", err)
	}
	return conversations, nil
}

// GetConversation returns one visible active DM conversation.
// Missing, cross-workspace, archived, and non-participant reads return ErrNotFound.
func (s *DMService) GetConversation(ctx context.Context, input GetDMConversationInput) (domain.DMConversation, error) {
	conversation, err := s.dms.GetVisibleConversationByID(
		ctx,
		strings.TrimSpace(input.WorkspaceID),
		strings.TrimSpace(input.ConversationID),
		strings.TrimSpace(input.CallerID),
	)
	if err != nil {
		return domain.DMConversation{}, err
	}
	return conversation, nil
}

// DirectProfileInput asks for the 1:1 profile panel payload (issue #443).
//
// There is deliberately no user ID field. Which profile is shown is a
// consequence of who the caller is and which conversation they opened — never
// something the request states — so the endpoint cannot be turned into a
// user-profile lookup by ID.
type DirectProfileInput struct {
	WorkspaceID    string
	CallerID       string
	ConversationID string
}

// DirectProfile is the panel payload for a 1:1 conversation: the conversation
// it describes and the profile of the one other active participant.
type DirectProfile struct {
	Conversation domain.DMConversation
	Profile      domain.DMDirectProfile
}

// GetDirectProfile returns the other participant's profile for a 1:1
// conversation the caller participates in.
//
// Two checks, and the second is the one that counts.
// GetVisibleConversationByID runs first so a group, a foreign workspace or an
// unknown ID is refused before any profile query is issued, and so every
// refusal is the same bare ErrNotFound the rest of the DM surface returns. But
// its answer is never treated as the permission the profile is read under:
// GetDirectCounterpartProfile re-establishes the caller's active membership in
// the same statement that projects the counterpart, so a membership revoked in
// between yields ErrNotFound instead of a name and an e-mail. Nothing from the
// first read reaches the response.
//
// The conversation type is checked against the row the database returned and
// never against anything the client said, in both queries.
func (s *DMService) GetDirectProfile(ctx context.Context, input DirectProfileInput) (DirectProfile, error) {
	workspaceID := strings.TrimSpace(input.WorkspaceID)
	callerID := strings.TrimSpace(input.CallerID)
	conversation, err := s.dms.GetVisibleConversationByID(
		ctx,
		workspaceID,
		strings.TrimSpace(input.ConversationID),
		callerID,
	)
	if err != nil {
		return DirectProfile{}, err
	}
	if conversation.Type != domain.DMConversationTypeDirect {
		return DirectProfile{}, domain.ErrNotFound
	}
	profile, err := s.dms.GetDirectCounterpartProfile(ctx, workspaceID, conversation.ID, callerID)
	if err != nil {
		// ErrNotFound and ErrInconsistentDirectConversation are both passed
		// through unwrapped: the first is a denial the handler folds into the
		// common 404, the second is corrupt data it must report as a server
		// error instead of disguising as one.
		if errors.Is(err, domain.ErrNotFound) || errors.Is(err, domain.ErrInconsistentDirectConversation) {
			return DirectProfile{}, err
		}
		return DirectProfile{}, fmt.Errorf("get direct counterpart profile: %w", err)
	}
	return DirectProfile{Conversation: conversation, Profile: profile}, nil
}

func normalizeGroupDMParticipants(callerID string, invited []string) ([]string, error) {
	// Checked on the raw list, before any per-ID work, so an oversized payload is
	// rejected without parsing every entry first.
	if len(invited)+1 > maxGroupDMParticipants {
		return nil, fmt.Errorf("%w: group DMs allow at most %d participants", domain.ErrInvalidInput, maxGroupDMParticipants)
	}
	participants := map[string]struct{}{callerID: {}}
	for _, rawID := range invited {
		rawID = strings.TrimSpace(rawID)
		if rawID == "" {
			return nil, fmt.Errorf("%w: participant_user_ids cannot contain empty user IDs", domain.ErrInvalidInput)
		}
		userID, err := canonicalizeUserID(rawID)
		if err != nil {
			return nil, err
		}
		participants[userID] = struct{}{}
	}
	if len(participants) < minGroupDMParticipants {
		return nil, fmt.Errorf("%w: group DMs require at least three unique participants", domain.ErrInvalidInput)
	}

	participantUserIDs := make([]string, 0, len(participants))
	for userID := range participants {
		participantUserIDs = append(participantUserIDs, userID)
	}
	sort.Strings(participantUserIDs)
	return participantUserIDs, nil
}

func normalizeDMTitle(title string) (string, error) {
	title = strings.TrimSpace(title)
	if utf8.RuneCountInString(title) > maxDMTitleRunes {
		return "", fmt.Errorf("%w: title must be 120 characters or fewer", domain.ErrInvalidInput)
	}
	return title, nil
}

func canonicalDirectPairKey(userA, userB string) string {
	first, second := userA, userB
	if second < first {
		first, second = second, first
	}
	return fmt.Sprintf("%d:%s%d:%s", len(first), first, len(second), second)
}

// canonicalizeUserID parses s as a UUID and returns its lowercase canonical form.
// Returns ErrInvalidInput for any input that is not a valid UUID.
func canonicalizeUserID(s string) (string, error) {
	id, err := uuid.Parse(s)
	if err != nil || id == uuid.Nil {
		return "", fmt.Errorf("%w: user_id is not a valid UUID", domain.ErrInvalidInput)
	}
	return id.String(), nil
}

// ── Group rename and self-leave (issue #527) ─────────────────────────────────

// RenameGroupInput and LeaveGroupInput carry an actor and a target, and nothing
// else. There is no role, no capability and no participant list: a group's
// authority is participation, re-derived inside the store's transaction, so
// there is nothing here a client could assert about itself.
type RenameGroupInput struct {
	WorkspaceID    string
	CallerID       string
	ConversationID string
	Title          string
}

type LeaveGroupInput struct {
	WorkspaceID    string
	CallerID       string
	ConversationID string
}

// RenameGroup sets a group conversation's title.
//
// Groups only, and that is enforced twice: this method is reached from a route
// that exists for group conversations, and the store's statement requires
// type = 'group', so a 1:1 conversation ID matches nothing and comes back as
// ErrNotFound. A direct conversation has no title to change — its name is the
// counterpart's, resolved per viewer — so renaming one is not a restricted
// operation but an impossible one.
//
// Authorization is participation and is settled in the store, serialized with
// the write. Nothing is checked here that the store does not check again.
func (s *DMService) RenameGroup(ctx context.Context, input RenameGroupInput) (storage.RenameGroupResult, error) {
	if strings.TrimSpace(input.WorkspaceID) == "" || strings.TrimSpace(input.ConversationID) == "" {
		return storage.RenameGroupResult{}, fmt.Errorf("%w: workspace_id and conversation_id are required", domain.ErrInvalidInput)
	}
	callerID, err := canonicalizeUserID(input.CallerID)
	if err != nil {
		return storage.RenameGroupResult{}, err
	}
	title, err := normalizeGroupRenameTitle(input.Title)
	if err != nil {
		return storage.RenameGroupResult{}, err
	}
	return s.dms.RenameGroupConversation(ctx, storage.RenameGroupInput{
		WorkspaceID:    input.WorkspaceID,
		ConversationID: input.ConversationID,
		CallerID:       callerID,
		Title:          title,
	})
}

// LeaveGroup removes the caller's own participation in a group.
//
// The actor is the only person this can affect: there is no target user in the
// input and none in the store's statement, which updates the row matching the
// caller. A 1:1 conversation is again unreachable — the store requires
// type = 'group' — so "leaving a DM" is not a refused operation but an absent
// one.
func (s *DMService) LeaveGroup(ctx context.Context, input LeaveGroupInput) (storage.LeaveConversationResult, error) {
	if strings.TrimSpace(input.WorkspaceID) == "" || strings.TrimSpace(input.ConversationID) == "" {
		return storage.LeaveConversationResult{}, fmt.Errorf("%w: workspace_id and conversation_id are required", domain.ErrInvalidInput)
	}
	callerID, err := canonicalizeUserID(input.CallerID)
	if err != nil {
		return storage.LeaveConversationResult{}, err
	}
	return s.dms.LeaveGroupConversation(ctx, input.WorkspaceID, input.ConversationID, callerID)
}

// normalizeGroupRenameTitle trims and bounds a new group title.
//
// It shares maxDMTitleRunes with creation, deliberately, so a rename cannot be
// the way past a cap creation enforces. It differs from normalizeDMTitle in one
// respect and only one: creation accepts an empty title — a group with none is
// named after its participants — whereas a *rename* to nothing is not a name,
// it is a request with no content, and silently keeping the old title would
// leave the dialog showing a change that never happened.
func normalizeGroupRenameTitle(title string) (string, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return "", fmt.Errorf("%w: title is required", domain.ErrInvalidInput)
	}
	if utf8.RuneCountInString(title) > maxDMTitleRunes {
		return "", fmt.Errorf("%w: title must be 120 characters or fewer", domain.ErrInvalidInput)
	}
	return title, nil
}
