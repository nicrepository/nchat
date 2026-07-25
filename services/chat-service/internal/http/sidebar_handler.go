package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

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

// sidebarDMCounterpartJSON is the identity of the other participant of a 1:1
// DM, as seen by the requesting user. It carries the minimum the UI needs to
// render an avatar: a stable ID (for a deterministic fallback colour), the
// resolved visual name, and an optional avatar URL. E-mail, status, auth source
// and external subject are deliberately absent.
type sidebarDMCounterpartJSON struct {
	UserID      string `json:"user_id"`
	DisplayName string `json:"display_name"`
	AvatarURL   string `json:"avatar_url,omitempty"`
}

// sidebarDMJSON is the JSON shape for a DM conversation in the sidebar response.
// participant_ids and title are intentionally omitted: participant_ids would
// leak member identity metadata, and title is an internal field not consumed
// by the sidebar UI (the computed display name is in Name).
// Counterpart is present only for direct conversations whose other participant
// could be resolved; group conversations never carry one, and Name stays the
// group title for them.
type sidebarDMJSON struct {
	ID          string                    `json:"id"`
	Type        string                    `json:"type"` // "direct" | "group"
	Name        string                    `json:"name"` // computed display name
	Counterpart *sidebarDMCounterpartJSON `json:"counterpart,omitempty"`
}

// sidebarResponseBody is the top-level JSON data object for the sidebar endpoint.
type sidebarResponseBody struct {
	CurrentUserID string               `json:"current_user_id"`
	Workspace     sidebarWorkspaceJSON `json:"workspace"`
	Channels      []sidebarChannelJSON `json:"channels"`
	DMConvs       []sidebarDMJSON      `json:"dm_conversations"`
	// CanCreateChannel is advisory UI state, not an authorization decision:
	// POST /api/chat/channels checks the caller's role again for every request.
	CanCreateChannel bool `json:"can_create_channel"`
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
		Channels:         mapChannels(data.Channels),
		DMConvs:          mapDMs(data.DMs),
		CanCreateChannel: data.CanCreateChannel,
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
func mapDMs(dms []domain.DMConversationWithParticipantIDs) []sidebarDMJSON {
	out := make([]sidebarDMJSON, 0, len(dms))
	for _, dm := range dms {
		name := computeDMName(dm.Type, dm.Title, dm.CounterpartDisplayName)
		out = append(out, sidebarDMJSON{
			ID:          dm.ID,
			Type:        string(dm.Type),
			Name:        name,
			Counterpart: mapDMCounterpart(dm, name),
		})
	}
	return out
}

// mapDMCounterpart exposes the other participant of a direct conversation.
// It returns nil for group conversations and for direct ones whose counterpart
// could not be resolved, so the client never has to distinguish a missing
// counterpart from a fabricated one. DisplayName reuses the already computed
// conversation name, keeping the structured field and `name` in agreement even
// when the server had to fall back to the generic label.
func mapDMCounterpart(dm domain.DMConversationWithParticipantIDs, name string) *sidebarDMCounterpartJSON {
	if dm.Type != domain.DMConversationTypeDirect || dm.CounterpartUserID == "" {
		return nil
	}
	return &sidebarDMCounterpartJSON{
		UserID:      dm.CounterpartUserID,
		DisplayName: name,
		AvatarURL:   dm.CounterpartAvatarURL,
	}
}

// computeDMName derives a sidebar display name for a DM conversation.
// Group DMs use their title (or "Grupo DM" if untitled).
// Direct DMs show the other participant, already resolved by the storage layer
// for the requesting user, so the same conversation reads as B for A and as A
// for B. The generic placeholder is a last resort for conversations whose
// counterpart cannot be resolved at all.
func computeDMName(dmType domain.DMConversationType, title, counterpartDisplayName string) string {
	if dmType == domain.DMConversationTypeGroup {
		if title != "" {
			return title
		}
		return "Grupo DM"
	}
	if name := strings.TrimSpace(counterpartDisplayName); name != "" {
		return name
	}
	// Direct conversations are created with a NULL title; a non-empty one only
	// occurs in legacy or hand-written rows, where it still beats the placeholder.
	if title != "" {
		return title
	}
	return "Mensagem Direta"
}
