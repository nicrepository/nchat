package httpapi

import (
	"context"
	"errors"
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

// WithPresence returns a handler that can annotate participants with presence.
// Wired after the WebSocket hub exists, exactly like the channel handler's.
// Without it the payload omits presence entirely rather than claiming "offline"
// for everyone — "not tracked" and "offline" are different statements.
func (h *DMHandler) WithPresence(presence presenceLookup) *DMHandler {
	if h == nil {
		return nil
	}
	next := *h
	next.presence = presence
	return &next
}

func NewDMHandler(workspaces workspaceResolver, dms dmProvider, limiter dmRateLimiter) *DMHandler {
	return &DMHandler{workspaces: workspaces, dms: dms, limiter: limiter}
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

// ── Group details (issue #441, extended by #398) ────────────────────────────

// groupParticipantJSON is one participant of the group panel's list.
//
// No role field, unlike the channel panel: chat.dm_members.role is closed by
// CHECK to the single value 'member', so a group has none to show. E-mail,
// workspace role and join date are never serialized.
type groupParticipantJSON struct {
	UserID      string `json:"user_id"`
	DisplayName string `json:"display_name"`
	AvatarURL   string `json:"avatar_url,omitempty"`
	// Omitted when the server does not track presence, so the client can tell
	// "not tracked" from "offline".
	Presence string `json:"presence,omitempty"`
}

// groupDetailsResponse is the panel payload for a group conversation.
//
// Deliberately absent, because a group is not a channel: visibility
// (public/private), slug, category and description. The domain has none of them
// for chat.dm_conversations, so none is invented here.
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
	// Always sent, so a client that predates it reads absent-as-false and hides
	// the add action — the safe direction.
	CanManageMembers bool `json:"can_manage_members"`
}

// presenceOffline is the counterpart of presenceOnline for a list that is not
// filtered by presence.
const presenceOffline = "offline"

// Details handles GET /api/chat/dm/{conversationID}/details.
//
// The conversation ID comes from the path and is never trusted: the service
// resolves the caller's participation against the server-side workspace before
// any participant row is read, and a conversation the caller cannot see — or a
// 1:1 — is answered with the same 404 as a missing one.
func (h *DMHandler) Details(w http.ResponseWriter, r *http.Request) {
	if h.workspaces == nil || h.dms == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "direct messages not available")
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
		// Every denial collapses into one 404 so the route cannot be used to
		// discover which conversation UUIDs exist, nor to tell a group apart
		// from a 1:1 the caller cannot see.
		switch {
		case errors.Is(err, domain.ErrNotFound), errors.Is(err, domain.ErrForbidden):
			httputil.WriteError(w, http.StatusNotFound, httputil.ErrCodeNotFound, "conversation not found")
		default:
			httputil.WriteError(w, http.StatusInternalServerError, httputil.ErrCodeInternal, "internal error")
		}
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
