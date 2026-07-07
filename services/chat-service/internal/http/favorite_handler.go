package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
)

// favoriteProvider is the FavoriteService interface used by MessageHandler.
type favoriteProvider interface {
	FavoriteMessage(ctx context.Context, in service.FavoriteMessageInput) error
	UnfavoriteMessage(ctx context.Context, in service.FavoriteMessageInput) error
	ListFavorites(ctx context.Context, in service.ListFavoritesInput) (service.ListFavoritesOutput, error)
}

// ── JSON response shapes ──────────────────────────────────────────────────────

// favoriteJSON is one entry of the caller's own favorites list. Channel/DM IDs
// let the client navigate to the source conversation; they are already known
// to the caller (read access is enforced in the listing query).
type favoriteJSON struct {
	Message          messageJSON `json:"message"`
	ChannelID        string      `json:"channel_id,omitempty"`
	DMConversationID string      `json:"dm_conversation_id,omitempty"`
	FavoritedAt      time.Time   `json:"favorited_at"`
}

type listFavoritesResponseData struct {
	Favorites  []favoriteJSON `json:"favorites"`
	NextCursor string         `json:"next_cursor,omitempty"`
}

// ── Shared helpers ────────────────────────────────────────────────────────────

func (h *MessageHandler) checkFavoriteDeps(w http.ResponseWriter) bool {
	if h.workspaces == nil || h.favorites == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "favorites not available")
		return false
	}
	return true
}

// favoriteRequestContext validates the message path param and resolves the
// caller and workspace. Returns ok=false after writing the error response.
func (h *MessageHandler) favoriteRequestContext(w http.ResponseWriter, r *http.Request) (in service.FavoriteMessageInput, ok bool) {
	messageID := r.PathValue("messageID")
	if !validateTargetID(w, messageID, "message_id") {
		return in, false
	}
	userID := GetContextUserID(r)
	if userID == "" {
		httputil.WriteError(w, http.StatusUnauthorized, httputil.ErrCodeUnauthorized, "unauthorized")
		return in, false
	}
	wsID, ok := h.resolveWorkspaceID(r.Context(), w)
	if !ok {
		return in, false
	}
	// UserID always from auth context — never from the payload (no body accepted).
	return service.FavoriteMessageInput{WorkspaceID: wsID, UserID: userID, MessageID: messageID}, true
}

// ── Endpoints ─────────────────────────────────────────────────────────────────

// FavoriteMessage handles POST /api/chat/messages/{messageID}/favorite.
// Idempotent; responds 204. Missing, deleted, and unauthorized messages all
// respond 404 (non-enumerating).
func (h *MessageHandler) FavoriteMessage(w http.ResponseWriter, r *http.Request) {
	if !h.checkFavoriteDeps(w) {
		return
	}
	in, ok := h.favoriteRequestContext(w, r)
	if !ok {
		return
	}
	if err := h.favorites.FavoriteMessage(r.Context(), in); err != nil {
		mapServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// UnfavoriteMessage handles DELETE /api/chat/messages/{messageID}/favorite.
// Idempotent; responds 204 whether or not the favorite existed.
func (h *MessageHandler) UnfavoriteMessage(w http.ResponseWriter, r *http.Request) {
	if !h.checkFavoriteDeps(w) {
		return
	}
	in, ok := h.favoriteRequestContext(w, r)
	if !ok {
		return
	}
	if err := h.favorites.UnfavoriteMessage(r.Context(), in); err != nil {
		mapServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ListFavorites handles GET /api/chat/favorites?before=&limit=.
// Always scoped to the authenticated caller — there is no way to address
// another user's favorites. Entries whose channel/DM the caller can no longer
// read are filtered by the query.
func (h *MessageHandler) ListFavorites(w http.ResponseWriter, r *http.Request) {
	if !h.checkFavoriteDeps(w) {
		return
	}
	userID := GetContextUserID(r)
	if userID == "" {
		httputil.WriteError(w, http.StatusUnauthorized, httputil.ErrCodeUnauthorized, "unauthorized")
		return
	}
	wsID, ok := h.resolveWorkspaceID(r.Context(), w)
	if !ok {
		return
	}

	// Cursor validation is the FavoriteService's responsibility; malformed
	// cursors surface as domain.ErrInvalidCursor → 400 via mapServiceError.
	out, err := h.favorites.ListFavorites(r.Context(), service.ListFavoritesInput{
		WorkspaceID:  wsID,
		UserID:       userID,
		BeforeCursor: r.URL.Query().Get("before"),
		Limit:        parseLimitParam(r),
	})
	if err != nil {
		mapServiceError(w, err)
		return
	}

	resp := listFavoritesResponseData{
		Favorites:  make([]favoriteJSON, 0, len(out.Favorites)),
		NextCursor: out.NextCursor,
	}
	for _, fav := range out.Favorites {
		resp.Favorites = append(resp.Favorites, favoriteJSON{
			Message:          mapToMessageJSON(fav.Message),
			ChannelID:        fav.Message.ChannelID,
			DMConversationID: fav.Message.DMConversationID,
			FavoritedAt:      fav.FavoritedAt,
		})
	}
	httputil.WriteJSON(w, http.StatusOK, resp)
}
