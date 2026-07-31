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
	// maxGroupDMParticipants bounds the participant list, caller included. Without it
	// a single request could fan out into an unbounded number of membership rows and
	// per-participant eligibility look-ups.
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
	}, nil
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
