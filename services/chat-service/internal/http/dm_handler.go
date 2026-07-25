package httpapi

import (
	"context"
	"errors"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
)

type dmProvider interface {
	SearchDMCandidates(ctx context.Context, input service.SearchDMCandidatesInput) ([]domain.DMCandidate, error)
	GetOrCreateDirectConversation(ctx context.Context, input service.CreateDirectConversationInput) (service.CreateDirectConversationOutput, error)
	CreateGroupConversation(ctx context.Context, input service.CreateGroupConversationInput) (domain.DMConversation, error)
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
}

func NewDMHandler(workspaces workspaceResolver, dms dmProvider, limiter dmRateLimiter) *DMHandler {
	return &DMHandler{workspaces: workspaces, dms: dms, limiter: limiter}
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
