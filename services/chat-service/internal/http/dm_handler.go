package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

type dmProvider interface {
	SearchDMCandidates(ctx context.Context, input service.SearchDMCandidatesInput) ([]domain.DMCandidate, error)
	GetOrCreateDirectConversation(ctx context.Context, input service.CreateDirectConversationInput) (service.CreateDirectConversationOutput, error)
	CreateGroupConversation(ctx context.Context, input service.CreateGroupConversationInput) (domain.DMConversation, error)
	// AddGroupParticipants adds people to an existing group conversation (#398).
	AddGroupParticipants(ctx context.Context, input service.AddGroupParticipantsInput) (storage.AddMembersResult, error)
	// GetGroupDetails is the read-only projection the group panel renders.
	GetGroupDetails(ctx context.Context, input service.GroupDetailsInput) (service.GroupDetails, error)
	// SearchGroupParticipantCandidates lists people not already in the group.
	SearchGroupParticipantCandidates(ctx context.Context, input service.SearchGroupParticipantCandidatesInput) ([]domain.DMCandidate, error)
	// GetDirectProfile is the 1:1 profile projection (issue #443).
	GetDirectProfile(ctx context.Context, input service.DirectProfileInput) (service.DirectProfile, error)
	// GetGroupCallParticipantProfiles resolves presentation identities for a
	// set of call-participant user IDs, scoped to this group conversation
	// (issue #612).
	GetGroupCallParticipantProfiles(ctx context.Context, input service.GroupCallParticipantProfilesInput) ([]domain.CallParticipantProfile, error)
	// RenameGroup sets a group's title (issue #527). Group conversations only.
	RenameGroup(ctx context.Context, input service.RenameGroupInput) (storage.RenameGroupResult, error)
	// LeaveGroup removes the caller's own participation (issue #527).
	LeaveGroup(ctx context.Context, input service.LeaveGroupInput) (storage.LeaveConversationResult, error)
}

type dmRateLimiter interface {
	AllowActionWithLimit(ctx context.Context, userID, action string, maxActions, windowSeconds int) (bool, error)
}

const (
	// dmCandidateSearchRateLimit caps candidate searches at 30 per user per 60s to limit enumeration.
	dmCandidateSearchRateLimit = 30
	// dmCreateRateLimit caps get-or-create calls at 10 per user per 60s to limit write abuse.
	dmCreateRateLimit = 10
	// dmGroupCreateRateLimit is tighter than the direct budget: unlike a 1:1 DM, every
	// group call creates a new conversation and a row per participant, so a burst is
	// pure write amplification with nothing to deduplicate it.
	dmGroupCreateRateLimit   = 5
	dmRateLimitWindowSeconds = 60
)

type DMHandler struct {
	workspaces workspaceResolver
	dms        dmProvider
	limiter    dmRateLimiter
	broadcast  membersBroadcaster
	presence   presenceLookup
}

func NewDMHandler(workspaces workspaceResolver, dms dmProvider, limiter dmRateLimiter) *DMHandler {
	return &DMHandler{workspaces: workspaces, dms: dms, limiter: limiter}
}

// WithPresence returns a handler that annotates group participants with their
// live presence. Wired after the WebSocket hub exists, exactly like the channel
// handler's. Without it participants simply carry no presence.
//
// Presence here is decoration, never a filter: a group lists every active
// participant, so an unwired or empty presence source changes what each row
// says about itself and never who is in the list.
func (h *DMHandler) WithPresence(presence presenceLookup) *DMHandler {
	if h == nil {
		return nil
	}
	next := *h
	next.presence = presence
	return &next
}

// WithMembersBroadcast returns a handler that emits the post-commit members.added
// signal (issue #398). Wired after the hub exists, like the channel handler's.
func (h *DMHandler) WithMembersBroadcast(broadcast membersBroadcaster) *DMHandler {
	if h == nil {
		return nil
	}
	next := *h
	next.broadcast = broadcast
	return &next
}

type dmCandidateJSON struct {
	UserID      string `json:"user_id"`
	DisplayName string `json:"display_name"`
}

type searchDMCandidatesResponse struct {
	Candidates []dmCandidateJSON `json:"candidates"`
}

type createDirectDMRequest struct {
	OtherUserID string `json:"other_user_id"`
}

type createDirectDMResponse struct {
	ConversationID string `json:"conversation_id"`
	Created        bool   `json:"created"`
}

// createGroupDMRequest is the whole accepted body. The caller, the workspace and
// every membership field are derived server-side and are deliberately absent:
// a client that sends them gets a 400 from the strict decoder.
type createGroupDMRequest struct {
	ParticipantUserIDs []string `json:"participant_user_ids"`
	Title              string   `json:"title"`
}

type createGroupDMResponse struct {
	ConversationID string `json:"conversation_id"`
}

// ── Group details (issue #441) ───────────────────────────────────────────────

// groupParticipantJSON is one participant of the panel's preview.
//
// It carries exactly what the panel renders: a stable ID (so the client can
// mark "you" by identity rather than by name), the resolved display name, an
// optional avatar URL and the live presence. E-mail, workspace role, join date
// and every other profile attribute are deliberately absent — a details panel
// is not a directory export.
//
// There is no role: chat.dm_members.role is closed by CHECK to 'member', so a
// group has no role worth showing.
type groupParticipantJSON struct {
	UserID      string `json:"user_id"`
	DisplayName string `json:"display_name"`
	AvatarURL   string `json:"avatar_url,omitempty"`
	// Presence is omitted when the server does not track it, so a client can
	// tell "not tracked" from "tracked and offline". It never decides whether a
	// participant appears.
	Presence string `json:"presence,omitempty"`
}

// groupDetailsResponse is the panel payload for a group conversation.
//
// Deliberately absent, because a group is not a channel: there is no
// visibility (public/private), no slug, no category and no description. The
// domain has none of them for chat.dm_conversations, so none is invented here.
//
// participant_count is every active participant; participants is the capped
// preview and its length must never be shown as the count.
type groupDetailsResponse struct {
	ID               string                 `json:"id"`
	Type             string                 `json:"type"`
	Name             string                 `json:"name"`
	CreatedAt        string                 `json:"created_at"`
	ParticipantCount int                    `json:"participant_count"`
	Participants     []groupParticipantJSON `json:"participants"`
	// CanManageMembers (issue #398) is always sent, so a client that predates it
	// reads absent-as-false and hides the add action — the safe direction. It is
	// a rendering hint: POST .../members re-derives the decision in its own
	// transaction.
	CanManageMembers bool `json:"can_manage_members"`
}

// GroupDetails handles GET /api/chat/dm/{conversationID}/details.
//
// The conversation ID comes from the path and is never trusted: the service
// resolves the caller's participation against the server-side workspace before
// any participant row is read, and a conversation the caller cannot reach is
// answered with the same 404 as a missing one. A 1:1 conversation is refused
// the same way — details for direct messages are out of scope for this issue.
func (h *DMHandler) GroupDetails(w http.ResponseWriter, r *http.Request) {
	if h.workspaces == nil || h.dms == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "conversations not available")
		return
	}
	conversationID := r.PathValue("conversationID")
	if !validateTargetID(w, conversationID, "conversation_id") {
		return
	}
	callerID := GetContextUserID(r)
	if callerID == "" {
		writeUnauthorized(w)
		return
	}
	workspaceID, ok := h.resolveWorkspaceID(r.Context(), w)
	if !ok {
		return
	}

	details, err := h.dms.GetGroupDetails(r.Context(), service.GroupDetailsInput{
		WorkspaceID:      workspaceID,
		CallerID:         callerID,
		ConversationID:   conversationID,
		ParticipantLimit: domain.MaxDMDetailsParticipants,
	})
	if err != nil {
		writeGroupDetailsError(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, h.groupDetailsBody(workspaceID, details))
}

func (h *DMHandler) groupDetailsBody(workspaceID string, details service.GroupDetails) groupDetailsResponse {
	// One batch presence read for the whole page — never one lookup per
	// participant. The set only annotates rows the query already selected.
	online := map[string]struct{}{}
	if h.presence != nil {
		for _, userID := range h.presence.OnlineUserIDs(workspaceID) {
			online[userID] = struct{}{}
		}
	}
	participants := make([]groupParticipantJSON, 0, len(details.Participants))
	for _, participant := range details.Participants {
		presence := ""
		if h.presence != nil {
			presence = presenceOffline
			if _, isOnline := online[participant.UserID]; isOnline {
				presence = presenceOnline
			}
		}
		participants = append(participants, groupParticipantJSON{
			UserID:      participant.UserID,
			DisplayName: participant.DisplayName,
			AvatarURL:   participant.AvatarURL,
			Presence:    presence,
		})
	}
	return groupDetailsResponse{
		ID:               details.Conversation.ID,
		Type:             string(details.Conversation.Type),
		Name:             details.Conversation.Title,
		CreatedAt:        details.Conversation.CreatedAt.UTC().Format(time.RFC3339),
		ParticipantCount: details.ParticipantCount,
		Participants:     participants,
		CanManageMembers: details.CanManageMembers,
	}
}

// writeGroupDetailsError folds every denial into the same 404. A caller must
// not be able to tell "this conversation exists but is not yours" from "no such
// conversation", nor a group from a 1:1 they cannot see.
// GroupCallParticipants handles POST /api/chat/dm/{conversationID}/call-participants
// (issue #612). Same shape as ChannelHandler.CallParticipants, sharing its
// request/response JSON types since both return the identical
// {user_id, display_name, avatar_url} shape.
func (h *DMHandler) GroupCallParticipants(w http.ResponseWriter, r *http.Request) {
	if h.workspaces == nil || h.dms == nil || h.limiter == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "dms not available")
		return
	}
	conversationID := r.PathValue("conversationID")
	if !validateTargetID(w, conversationID, "conversation_id") {
		return
	}
	callerID := GetContextUserID(r)
	if callerID == "" {
		writeUnauthorized(w)
		return
	}
	allowed, err := h.limiter.AllowActionWithLimit(r.Context(), callerID, callParticipantsAction, callParticipantsRateLimit, dmRateLimitWindowSeconds)
	if err != nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "dms not available")
		return
	}
	if !allowed {
		w.Header().Set("Retry-After", strconv.Itoa(dmRateLimitWindowSeconds))
		httputil.WriteError(w, http.StatusTooManyRequests, "rate_limited", "too many requests")
		return
	}
	if !requireJSONContentType(w, r) {
		return
	}
	var request callParticipantProfilesRequest
	if !decodeStrictJSON(w, r, &request) {
		return
	}
	workspace, err := h.workspaces.GetDefaultWorkspace(r.Context())
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			httputil.WriteError(w, http.StatusNotFound, httputil.ErrCodeNotFound, "workspace not found")
		} else {
			httputil.WriteError(w, http.StatusInternalServerError, httputil.ErrCodeInternal, "internal error")
		}
		return
	}
	profiles, err := h.dms.GetGroupCallParticipantProfiles(r.Context(), service.GroupCallParticipantProfilesInput{
		WorkspaceID:    workspace.ID,
		CallerID:       callerID,
		ConversationID: conversationID,
		UserIDs:        request.UserIDs,
	})
	if err != nil {
		writeCallParticipantProfilesError(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, callParticipantProfilesBody(profiles))
}

func writeGroupDetailsError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrNotFound), errors.Is(err, domain.ErrForbidden):
		httputil.WriteError(w, http.StatusNotFound, httputil.ErrCodeNotFound, "conversation not found")
	default:
		httputil.WriteError(w, http.StatusInternalServerError, httputil.ErrCodeInternal, "internal error")
	}
}

// ── Direct profile (issue #443) ──────────────────────────────────────────────

// directProfileJSON is the other participant of a 1:1 conversation.
//
// It is the group participant's shape plus the corporate e-mail, which the
// prototype's profile card shows and a participant row does not. Nothing else
// from auth.users appears: no phone, no auth_source, no external_subject, no
// status, no last_login_at, no roles, no session or device data. A profile
// summary is not a directory record and must not become one by accretion.
//
// job_title, department and timezone are absent rather than empty. No column in
// the domain stores them today, so an empty string here would assert "this
// person has no job title" instead of "this deployment does not record one" —
// the client distinguishes the two by the key being missing and renders "Não
// informado". When those columns exist they are added here and the panel starts
// showing them with no further change.
type directProfileJSON struct {
	UserID      string `json:"user_id"`
	DisplayName string `json:"display_name"`
	AvatarURL   string `json:"avatar_url,omitempty"`
	Email       string `json:"email,omitempty"`
	// Presence is omitted when the server does not track it, so a client can
	// tell "not tracked" from "tracked and offline".
	Presence string `json:"presence,omitempty"`
}

// directProfileResponse is the panel payload for a 1:1 conversation.
//
// It carries a profile, not a participant list: a direct conversation's roster
// is the caller plus one person, and shipping it as `participants` would invite
// the client to pick a side. The server has already picked, and `kind` names
// the variant so the client switches on a tag rather than guessing from which
// fields happen to be present.
type directProfileResponse struct {
	Kind           string            `json:"kind"`
	ConversationID string            `json:"conversation_id"`
	Profile        directProfileJSON `json:"profile"`
}

// DirectProfile handles GET /api/chat/dm/{conversationID}/profile.
//
// Only the conversation is named, and it is not trusted: the service settles
// the caller's participation against the server-side workspace, refuses
// anything that is not a `direct` row, and resolves the counterpart from the
// membership rows. There is no user ID anywhere in the request, so this cannot
// be used to read an arbitrary person's profile, and a conversation the caller
// cannot reach — including a group — answers the same 404 as a missing one.
func (h *DMHandler) DirectProfile(w http.ResponseWriter, r *http.Request) {
	if h.workspaces == nil || h.dms == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "conversations not available")
		return
	}
	conversationID := r.PathValue("conversationID")
	if !validateTargetID(w, conversationID, "conversation_id") {
		return
	}
	callerID := GetContextUserID(r)
	if callerID == "" {
		writeUnauthorized(w)
		return
	}
	workspaceID, ok := h.resolveWorkspaceID(r.Context(), w)
	if !ok {
		return
	}

	result, err := h.dms.GetDirectProfile(r.Context(), service.DirectProfileInput{
		WorkspaceID:    workspaceID,
		CallerID:       callerID,
		ConversationID: conversationID,
	})
	if err != nil {
		writeDirectProfileError(w, r, conversationID, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, directProfileResponse{
		Kind:           "direct",
		ConversationID: result.Conversation.ID,
		Profile: directProfileJSON{
			UserID:      result.Profile.UserID,
			DisplayName: result.Profile.DisplayName,
			AvatarURL:   result.Profile.AvatarURL,
			Email:       result.Profile.Email,
			Presence:    h.presenceOf(workspaceID, result.Profile.UserID),
		},
	})
}

// presenceOf reports the live presence of one user, or "" when presence is not
// wired at all. A profile is a single person, so the batch read the group panel
// needs would be one membership scan to answer one question; the set is
// consulted directly instead.
//
// Presence never gates the profile: an unwired or empty source only changes
// what this field says.
func (h *DMHandler) presenceOf(workspaceID, userID string) string {
	if h.presence == nil {
		return ""
	}
	for _, online := range h.presence.OnlineUserIDs(workspaceID) {
		if online == userID {
			return presenceOnline
		}
	}
	return presenceOffline
}

// writeDirectProfileError folds every denial into the same 404 — a caller must
// not be able to tell "this conversation exists but is not yours" from "no such
// conversation", nor a group from a 1:1 they cannot see.
//
// A conversation that is `direct` but does not resolve to exactly one
// counterpart is the one case that is not a denial: it is corrupt data, so it
// answers 500 and is logged with the conversation ID (an identifier the caller
// already holds) and nothing else — no e-mail, no name, no participant IDs.
func writeDirectProfileError(w http.ResponseWriter, r *http.Request, conversationID string, err error) {
	switch {
	case errors.Is(err, domain.ErrInconsistentDirectConversation):
		slog.ErrorContext(r.Context(), "chat direct profile inconsistent conversation",
			slog.String("conversation_id", conversationID))
		httputil.WriteError(w, http.StatusInternalServerError, httputil.ErrCodeInternal, "internal error")
	case errors.Is(err, domain.ErrNotFound), errors.Is(err, domain.ErrForbidden):
		httputil.WriteError(w, http.StatusNotFound, httputil.ErrCodeNotFound, "conversation not found")
	default:
		httputil.WriteError(w, http.StatusInternalServerError, httputil.ErrCodeInternal, "internal error")
	}
}

func (h *DMHandler) SearchCandidates(w http.ResponseWriter, r *http.Request) {
	if !h.checkDeps(w) {
		return
	}
	callerID := GetContextUserID(r)
	if callerID == "" {
		writeUnauthorized(w)
		return
	}
	if !h.allowAction(w, r, callerID, dmCandidateSearchAction, dmCandidateSearchRateLimit) {
		return
	}
	limit, ok := parseDMCandidateLimit(w, r)
	if !ok {
		return
	}
	workspaceID, ok := h.resolveWorkspaceID(r.Context(), w)
	if !ok {
		return
	}
	candidates, err := h.dms.SearchDMCandidates(r.Context(), service.SearchDMCandidatesInput{
		WorkspaceID: workspaceID,
		CallerID:    callerID,
		Query:       r.URL.Query().Get("query"),
		Limit:       limit,
	})
	if err != nil {
		writeDMCandidateError(w, err)
		return
	}
	writeCandidates(w, candidates)
}

func (h *DMHandler) GetOrCreateDirect(w http.ResponseWriter, r *http.Request) {
	if !h.checkDeps(w) {
		return
	}
	callerID := GetContextUserID(r)
	if callerID == "" {
		writeUnauthorized(w)
		return
	}
	if !h.allowAction(w, r, callerID, "dm_create", dmCreateRateLimit) {
		return
	}
	if !requireJSONContentType(w, r) {
		return
	}
	var request createDirectDMRequest
	if !decodeStrictJSON(w, r, &request) {
		return
	}
	request.OtherUserID = strings.TrimSpace(request.OtherUserID)
	if !validateTargetID(w, request.OtherUserID, "other_user_id") {
		return
	}
	workspaceID, ok := h.resolveWorkspaceID(r.Context(), w)
	if !ok {
		return
	}
	result, err := h.dms.GetOrCreateDirectConversation(r.Context(), service.CreateDirectConversationInput{
		WorkspaceID: workspaceID,
		CallerID:    callerID,
		OtherUserID: request.OtherUserID,
	})
	if err != nil {
		writeDMConversationError(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, createDirectDMResponse{
		ConversationID: result.Conversation.ID,
		Created:        result.Created,
	})
}

// CreateGroup handles POST /api/chat/dms/group.
//
// It only carries the transport concerns — authentication, rate limiting, body
// shape and the server-side workspace lookup. Participant eligibility, caller
// inclusion, de-duplication, the participant count bounds and the title limit
// all stay in DMService, which is the single authority for them; this handler
// never inspects the list beyond decoding it.
func (h *DMHandler) CreateGroup(w http.ResponseWriter, r *http.Request) {
	if !h.checkDeps(w) {
		return
	}
	callerID := GetContextUserID(r)
	if callerID == "" {
		writeUnauthorized(w)
		return
	}
	if !h.allowAction(w, r, callerID, "dm_group_create", dmGroupCreateRateLimit) {
		return
	}
	if !requireJSONContentType(w, r) {
		return
	}
	var request createGroupDMRequest
	if !decodeStrictJSON(w, r, &request) {
		return
	}
	workspaceID, ok := h.resolveWorkspaceID(r.Context(), w)
	if !ok {
		return
	}
	conversation, err := h.dms.CreateGroupConversation(r.Context(), service.CreateGroupConversationInput{
		WorkspaceID:        workspaceID,
		CallerID:           callerID,
		ParticipantUserIDs: request.ParticipantUserIDs,
		Title:              request.Title,
	})
	if err != nil {
		writeDMConversationError(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusCreated, createGroupDMResponse{ConversationID: conversation.ID})
}

// AddParticipants handles POST /api/chat/dm/{conversationID}/members.
//
// The mirror of the channel route, and deliberately a separate one: a group is a
// chat.dm_conversations row, so pointing a conversation ID at /channels/… would
// name the wrong aggregate. It shares the add-members rate-limit budget with the
// channel route, so a caller cannot get a second allowance by switching
// conversation type.
//
// Everything that decides the outcome is in DMService: whether the caller
// participates, whether the conversation is a group rather than a 1:1, who is
// eligible, the batch cap and the participant ceiling. A 1:1 conversation is
// answered 404 here even when the caller is in it — adding a third person would
// convert it into a group, which this issue does not do.
func (h *DMHandler) AddParticipants(w http.ResponseWriter, r *http.Request) {
	if !h.checkDeps(w) {
		return
	}
	conversationID := r.PathValue("conversationID")
	if !validateTargetID(w, conversationID, "conversation_id") {
		return
	}
	callerID := GetContextUserID(r)
	if callerID == "" {
		writeUnauthorized(w)
		return
	}
	if !h.allowAction(w, r, callerID, addMembersAction, addMembersRateLimit) {
		return
	}
	if !requireJSONContentType(w, r) {
		return
	}
	var request addMembersRequest
	if !decodeStrictJSON(w, r, &request) {
		return
	}
	workspaceID, ok := h.resolveWorkspaceID(r.Context(), w)
	if !ok {
		return
	}
	result, err := h.dms.AddGroupParticipants(r.Context(), service.AddGroupParticipantsInput{
		WorkspaceID:    workspaceID,
		CallerID:       callerID,
		ConversationID: conversationID,
		UserIDs:        request.UserIDs,
	})
	if err != nil {
		writeAddMembersError(w, err)
		return
	}
	// Only after the service returned successfully, which means the transaction
	// committed. A rolled-back add broadcasts nothing.
	//
	// Gated on the ID list rather than on Added: both come from the same
	// RETURNING and cannot disagree, but the list is the one the fan-out below
	// actually addresses, so reading the gate from it leaves no way for a future
	// change to publish to nobody.
	if h.broadcast != nil && len(result.AddedUserIDs) > 0 {
		h.broadcast.PublishMembersAdded(
			r.Context(), workspaceID, "dm", conversationID, callerID, result.Added, result.TotalCount,
		)
		h.broadcast.PublishConversationAvailable(
			r.Context(), workspaceID, "dm", conversationID, result.AddedUserIDs,
		)
	}
	httputil.WriteJSON(w, http.StatusOK, addMembersResponse{
		Added:          result.Added,
		AlreadyMembers: result.AlreadyMembers,
		MemberCount:    result.TotalCount,
	})
}

// dmCandidateSearchAction is the shared limiter namespace for every candidate
// search — workspace-wide and both contextual ones. One budget, so a caller
// cannot enumerate faster by alternating routes.
const dmCandidateSearchAction = "dm_search"

// writeCandidates serialises a candidate list. The two fields are what the
// picker renders; e-mail, role and membership state are never included.
func writeCandidates(w http.ResponseWriter, candidates []domain.DMCandidate) {
	response := searchDMCandidatesResponse{Candidates: make([]dmCandidateJSON, 0, len(candidates))}
	for _, candidate := range candidates {
		response.Candidates = append(response.Candidates, dmCandidateJSON{
			UserID: candidate.UserID, DisplayName: candidate.DisplayName,
		})
	}
	httputil.WriteJSON(w, http.StatusOK, response)
}

// parseCandidateLimit reads the optional limit, rejecting anything that is not a
// positive integer. The service clamps it to the server maximum; the client
// never chooses the ceiling.
func parseCandidateLimit(w http.ResponseWriter, r *http.Request) (int, bool) {
	return parseDMCandidateLimit(w, r)
}

// ParticipantCandidates handles GET /api/chat/dm/{conversationID}/member-candidates.
//
// The group counterpart of the channel route. Participation authorises it, so a
// caller who is not in the group cannot learn who is — and a 1:1 is answered
// 404, since it has no add-participants flow at all.
func (h *DMHandler) ParticipantCandidates(w http.ResponseWriter, r *http.Request) {
	if !h.checkDeps(w) {
		return
	}
	conversationID := r.PathValue("conversationID")
	if !validateTargetID(w, conversationID, "conversation_id") {
		return
	}
	callerID := GetContextUserID(r)
	if callerID == "" {
		writeUnauthorized(w)
		return
	}
	if !h.allowAction(w, r, callerID, dmCandidateSearchAction, dmCandidateSearchRateLimit) {
		return
	}
	limit, ok := parseCandidateLimit(w, r)
	if !ok {
		return
	}
	workspaceID, ok := h.resolveWorkspaceID(r.Context(), w)
	if !ok {
		return
	}
	candidates, err := h.dms.SearchGroupParticipantCandidates(
		r.Context(), service.SearchGroupParticipantCandidatesInput{
			WorkspaceID:    workspaceID,
			CallerID:       callerID,
			ConversationID: conversationID,
			Query:          r.URL.Query().Get("query"),
			Limit:          limit,
		})
	if err != nil {
		writeCandidateSearchError(w, err)
		return
	}
	writeCandidates(w, candidates)
}

func requireJSONContentType(w http.ResponseWriter, r *http.Request) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		httputil.WriteError(w, http.StatusUnsupportedMediaType, httputil.ErrCodeBadRequest, "content type must be application/json")
		return false
	}
	return true
}

func (h *DMHandler) checkDeps(w http.ResponseWriter) bool {
	if h.workspaces == nil || h.dms == nil || h.limiter == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "direct messages not available")
		return false
	}
	return true
}

func (h *DMHandler) allowAction(w http.ResponseWriter, r *http.Request, userID, action string, limit int) bool {
	allowed, err := h.limiter.AllowActionWithLimit(r.Context(), userID, action, limit, dmRateLimitWindowSeconds)
	if err != nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "direct messages not available")
		return false
	}
	if !allowed {
		w.Header().Set("Retry-After", strconv.Itoa(dmRateLimitWindowSeconds))
		httputil.WriteError(w, http.StatusTooManyRequests, "rate_limited", "too many requests")
		return false
	}
	return true
}

func (h *DMHandler) resolveWorkspaceID(ctx context.Context, w http.ResponseWriter) (string, bool) {
	workspace, err := h.workspaces.GetDefaultWorkspace(ctx)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			httputil.WriteError(w, http.StatusNotFound, httputil.ErrCodeNotFound, "workspace not found")
		} else {
			httputil.WriteError(w, http.StatusInternalServerError, httputil.ErrCodeInternal, "internal error")
		}
		return "", false
	}
	return workspace.ID, true
}

func parseDMCandidateLimit(w http.ResponseWriter, r *http.Request) (int, bool) {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return 0, true
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit <= 0 {
		httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, "limit must be a positive integer")
		return 0, false
	}
	return limit, true
}

func writeUnauthorized(w http.ResponseWriter) {
	httputil.WriteError(w, http.StatusUnauthorized, httputil.ErrCodeUnauthorized, "unauthorized")
}

func writeDMCandidateError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrInvalidInput):
		httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, "invalid search")
	case errors.Is(err, domain.ErrForbidden):
		httputil.WriteError(w, http.StatusForbidden, httputil.ErrCodeForbidden, "forbidden")
	default:
		httputil.WriteError(w, http.StatusInternalServerError, httputil.ErrCodeInternal, "internal error")
	}
}

func writeDMConversationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrInvalidInput):
		httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, "invalid request")
	case errors.Is(err, domain.ErrForbidden), errors.Is(err, domain.ErrNotFound):
		httputil.WriteError(w, http.StatusNotFound, httputil.ErrCodeNotFound, "user not available")
	default:
		httputil.WriteError(w, http.StatusInternalServerError, httputil.ErrCodeInternal, "internal error")
	}
}

// ── Group rename and self-leave (issue #527) ─────────────────────────────────

const (
	// One budget for both group mutations, following the category precedent: a
	// caller must not get a separate allowance for renaming and another for
	// leaving, and both change what the whole group sees.
	groupAdminRateLimit = 20
	groupAdminAction    = "group_admin"
)

// renameGroupRequest is the whole accepted body. One field, and the omissions
// are the contract: no workspace, no actor, no participant list, no type. The
// strict decoder answers 400 to a client that sends any of them.
type renameGroupRequest struct {
	Title string `json:"title"`
}

// renameGroupResponse echoes the unchanged conversation id alongside the
// persisted name, so a client can assert what the contract guarantees — a
// rename keeps the same group.
type renameGroupResponse struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// RenameGroup handles PATCH /api/chat/dm/{conversationID} (issue #527).
//
// Group conversations only. A 1:1 conversation reaches nothing: the statement
// behind this requires type = 'group', so a direct conversation's ID is a
// not-found — renaming a DM is not a refused operation but an absent one, since
// a direct conversation has no title to change.
//
// Authorization is participation, re-derived inside the write transaction. The
// realtime signals fire only after that transaction commits.
func (h *DMHandler) RenameGroup(w http.ResponseWriter, r *http.Request) {
	conversationID, callerID, workspaceID, ok := h.beginGroupAdmin(w, r, true)
	if !ok {
		return
	}
	var request renameGroupRequest
	if !decodeStrictJSON(w, r, &request) {
		return
	}
	result, err := h.dms.RenameGroup(r.Context(), service.RenameGroupInput{
		WorkspaceID:    workspaceID,
		CallerID:       callerID,
		ConversationID: conversationID,
		Title:          request.Title,
	})
	if err != nil {
		writeGroupAdminError(w, err)
		return
	}
	h.publishGroupChange(r.Context(), workspaceID, conversationID, result.Event.ID, true)
	httputil.WriteJSON(w, http.StatusOK, renameGroupResponse{
		ID:    result.Conversation.ID,
		Title: result.Conversation.Title,
	})
}

// LeaveGroup handles DELETE /api/chat/dm/{conversationID}/membership (#527).
//
// The caller's own participation and nothing else: there is no target user in
// the path, in a body (there is none) or in the statement below, which updates
// the row matching the session's actor.
func (h *DMHandler) LeaveGroup(w http.ResponseWriter, r *http.Request) {
	conversationID, callerID, workspaceID, ok := h.beginGroupAdmin(w, r, false)
	if !ok {
		return
	}
	result, err := h.dms.LeaveGroup(r.Context(), service.LeaveGroupInput{
		WorkspaceID:    workspaceID,
		CallerID:       callerID,
		ConversationID: conversationID,
	})
	if err != nil {
		writeGroupAdminError(w, err)
		return
	}
	h.publishGroupChange(r.Context(), workspaceID, conversationID, result.Event.ID, false)
	w.WriteHeader(http.StatusNoContent)
}

// beginGroupAdmin performs what both group mutations share, in the order that
// matters: wiring, a well-formed target, an authenticated actor, the shared
// budget, the content type when there is a body, and the workspace last —
// resolved from the session and never from the request.
func (h *DMHandler) beginGroupAdmin(w http.ResponseWriter, r *http.Request, hasBody bool) (string, string, string, bool) {
	if !h.checkDeps(w) {
		return "", "", "", false
	}
	conversationID := r.PathValue("conversationID")
	if !validateTargetID(w, conversationID, "conversation_id") {
		return "", "", "", false
	}
	callerID := GetContextUserID(r)
	if callerID == "" {
		writeUnauthorized(w)
		return "", "", "", false
	}
	if !h.allowAction(w, r, callerID, groupAdminAction, groupAdminRateLimit) {
		return "", "", "", false
	}
	if hasBody && !requireJSONContentType(w, r) {
		return "", "", "", false
	}
	workspaceID, ok := h.resolveWorkspaceID(r.Context(), w)
	if !ok {
		return "", "", "", false
	}
	return conversationID, callerID, workspaceID, true
}

// publishGroupChange announces a committed group mutation.
//
// A rename also invalidates every subscriber's sidebar copy of the name, so it
// publishes the conversation-updated signal as well as the system message; a
// departure only produces the message, because the person who left is no longer
// a subscriber and everyone else's list is unchanged.
func (h *DMHandler) publishGroupChange(ctx context.Context, workspaceID, conversationID, messageID string, renamed bool) {
	if h.broadcast == nil {
		return
	}
	if renamed {
		h.broadcast.PublishConversationUpdated(ctx, workspaceID, "dm", conversationID)
	}
	h.broadcast.PublishConversationEvent(ctx, workspaceID, "dm", conversationID, messageID)
}

// writeGroupAdminError keeps the refusals legible without describing state.
//
// A conversation that is not an active group of this workspace — including every
// 1:1 conversation — is 404, so the route cannot be used to learn which IDs
// exist or which are groups. A caller who does not participate is 403, which
// discloses nothing they could not already infer from having the ID.
func writeGroupAdminError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrInvalidInput):
		httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, "invalid request")
	case errors.Is(err, domain.ErrForbidden):
		httputil.WriteError(w, http.StatusForbidden, httputil.ErrCodeForbidden, "forbidden")
	case errors.Is(err, domain.ErrNotFound):
		httputil.WriteError(w, http.StatusNotFound, httputil.ErrCodeNotFound, "not found")
	case errors.Is(err, domain.ErrConflict):
		httputil.WriteError(w, http.StatusConflict, httputil.ErrCodeConflict, "conflict")
	default:
		httputil.WriteError(w, http.StatusInternalServerError, httputil.ErrCodeInternal, "internal error")
	}
}
