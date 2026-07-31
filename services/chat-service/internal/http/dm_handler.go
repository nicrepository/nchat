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
)

type dmProvider interface {
	SearchDMCandidates(ctx context.Context, input service.SearchDMCandidatesInput) ([]domain.DMCandidate, error)
	GetOrCreateDirectConversation(ctx context.Context, input service.CreateDirectConversationInput) (service.CreateDirectConversationOutput, error)
	CreateGroupConversation(ctx context.Context, input service.CreateGroupConversationInput) (domain.DMConversation, error)
	GetGroupDetails(ctx context.Context, input service.GroupDetailsInput) (service.GroupDetails, error)
	GetDirectProfile(ctx context.Context, input service.DirectProfileInput) (service.DirectProfile, error)
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
	}
}

// writeGroupDetailsError folds every denial into the same 404. A caller must
// not be able to tell "this conversation exists but is not yours" from "no such
// conversation", nor a group from a 1:1 they cannot see.
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
	if !h.allowAction(w, r, callerID, "dm_search", dmCandidateSearchRateLimit) {
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
	response := searchDMCandidatesResponse{Candidates: make([]dmCandidateJSON, 0, len(candidates))}
	for _, candidate := range candidates {
		response.Candidates = append(response.Candidates, dmCandidateJSON{
			UserID: candidate.UserID, DisplayName: candidate.DisplayName,
		})
	}
	httputil.WriteJSON(w, http.StatusOK, response)
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
