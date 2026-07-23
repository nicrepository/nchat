package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
)

// sidebarProvider is satisfied by *service.SidebarService; extracted for testing.
type sidebarProvider interface {
	GetSidebar(ctx context.Context, userID string) (service.SidebarData, error)
}

// sidebarWorkspaceJSON is the JSON shape for workspace info in the sidebar response.
type sidebarWorkspaceJSON struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}

// sidebarChannelJSON is the JSON shape for a channel in the sidebar response.
type sidebarChannelJSON struct {
	ID          string `json:"id"`
	Slug        string `json:"slug"`
	DisplayName string `json:"display_name"`
	Type        string `json:"type"`
	IsGeneral   bool   `json:"is_general"`
	CanWrite    bool   `json:"can_write"`
}

// sidebarDMJSON is the JSON shape for a DM conversation in the sidebar response.
// participant_ids and title are intentionally omitted: participant_ids would
// leak member identity metadata, and title is an internal field not consumed
// by the sidebar UI (the computed display name is in Name).
type sidebarDMJSON struct {
	ID   string `json:"id"`
	Type string `json:"type"` // "direct" | "group"
	Name string `json:"name"` // computed display name
}

// sidebarResponseBody is the top-level JSON data object for the sidebar endpoint.
type sidebarResponseBody struct {
	CurrentUserID string               `json:"current_user_id"`
	Workspace     sidebarWorkspaceJSON `json:"workspace"`
	Channels      []sidebarChannelJSON `json:"channels"`
	DMConvs       []sidebarDMJSON      `json:"dm_conversations"`
}

// SidebarHandler handles GET /api/chat/sidebar.
type SidebarHandler struct {
	svc sidebarProvider
}

// NewSidebarHandler returns a SidebarHandler backed by svc. When svc is nil,
// all requests return 503 (service not yet wired).
func NewSidebarHandler(svc sidebarProvider) *SidebarHandler {
	return &SidebarHandler{svc: svc}
}

// Ready reports whether the handler is wired to a real sidebar service.
// Used by the readiness probe; a nil service means every request returns 503.
func (h *SidebarHandler) Ready() bool {
	return h != nil && h.svc != nil
}

func (h *SidebarHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.svc == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "sidebar not available")
		return
	}

	userID := GetContextUserID(r)
	if userID == "" {
		httputil.WriteError(w, http.StatusUnauthorized, httputil.ErrCodeUnauthorized, "unauthorized")
		return
	}

	data, err := h.svc.GetSidebar(r.Context(), userID)
	if err != nil {
		switch {
		case errors.Is(err, domain.ErrForbidden):
			httputil.WriteError(w, http.StatusForbidden, httputil.ErrCodeForbidden, "forbidden")
		case errors.Is(err, domain.ErrNotFound):
			httputil.WriteError(w, http.StatusNotFound, httputil.ErrCodeNotFound, "workspace not found")
		default:
			httputil.WriteError(w, http.StatusInternalServerError, "internal_error", "internal error")
		}
		return
	}

	body := sidebarResponseBody{
		CurrentUserID: userID,
		Workspace: sidebarWorkspaceJSON{
			ID:   data.Workspace.ID,
			Name: data.Workspace.Name,
			Slug: data.Workspace.Slug,
		},
		Channels: mapChannels(data.Channels),
		DMConvs:  mapDMs(data.DMs, userID),
	}
	// Ensure arrays are never null in JSON output.
	if body.Channels == nil {
		body.Channels = []sidebarChannelJSON{}
	}
	if body.DMConvs == nil {
		body.DMConvs = []sidebarDMJSON{}
	}

	httputil.WriteJSON(w, http.StatusOK, body)
}

func mapChannels(channels []service.SidebarChannel) []sidebarChannelJSON {
	out := make([]sidebarChannelJSON, 0, len(channels))
	for _, sidebarChannel := range channels {
		ch := sidebarChannel.Channel
		out = append(out, sidebarChannelJSON{
			ID:          ch.ID,
			Slug:        ch.Slug,
			DisplayName: ch.DisplayName,
			Type:        string(ch.Type),
			IsGeneral:   ch.IsGeneral,
			CanWrite:    sidebarChannel.CanWrite,
		})
	}
	return out
}

// mapDMs converts domain DMs to JSON, computing a display name for each.
func mapDMs(dms []domain.DMConversationWithParticipantIDs, currentUserID string) []sidebarDMJSON {
	out := make([]sidebarDMJSON, 0, len(dms))
	for _, dm := range dms {
		name := computeDMName(dm.Type, dm.Title, dm.ParticipantIDs, currentUserID)
		out = append(out, sidebarDMJSON{
			ID:   dm.ID,
			Type: string(dm.Type),
			Name: name,
		})
	}
	return out
}

// computeDMName derives a sidebar display name for a DM conversation.
// Group DMs use their title (or "Grupo DM" if untitled).
// Direct DMs use a neutral placeholder pending a profile-safe display source.
// Participant IDs are used internally for group fallback counting but are not
// returned in the JSON response.
func computeDMName(dmType domain.DMConversationType, title string, participantIDs []string, currentUserID string) string {
	if dmType == domain.DMConversationTypeGroup {
		if title != "" {
			return title
		}
		return "Grupo DM"
	}
	// Direct DM: neutral fallback to avoid leaking participant user IDs.
	// Real display name will come from a profile service in a future iteration.
	_ = currentUserID
	_ = participantIDs
	return "Mensagem Direta"
}
