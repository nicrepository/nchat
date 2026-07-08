package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
)

// pinProvider is the PinService interface used by MessageHandler (RF-05).
type pinProvider interface {
	Pin(ctx context.Context, in service.PinActionInput) error
	Unpin(ctx context.Context, in service.PinActionInput) error
	ListPins(ctx context.Context, in service.ListPinsInput) (service.ListPinsOutput, error)
}

// pinBroadcaster fans a pin change out to a target's WebSocket subscribers.
// The hub re-checks per-subscriber authorization before delivery.
type pinBroadcaster interface {
	PublishPinUpdated(ctx context.Context, workspaceID, targetType, targetID, messageID, actorUserID string, pinned bool)
}

// ── JSON response shapes ──────────────────────────────────────────────────────

type pinJSON struct {
	Message        messageJSON `json:"message"`
	PinnedByUserID string      `json:"pinned_by_user_id"`
	PinnedAt       time.Time   `json:"pinned_at"`
}

type listPinsResponseData struct {
	Pins       []pinJSON `json:"pins"`
	TotalCount int       `json:"total_count"`
}

// ── Shared helpers ────────────────────────────────────────────────────────────

func (h *MessageHandler) checkPinDeps(w http.ResponseWriter) bool {
	if h.workspaces == nil || h.pins == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "pins not available")
		return false
	}
	return true
}

// pinRequestContext validates the target + message path params and resolves
// the caller and workspace. Returns ok=false after writing the error response.
func (h *MessageHandler) pinRequestContext(w http.ResponseWriter, r *http.Request, targetType, targetID, targetParam string) (in service.PinActionInput, ok bool) {
	if !validateTargetID(w, targetID, targetParam) {
		return in, false
	}
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
	// ActorUserID always from auth context — never from the payload (no body).
	return service.PinActionInput{
		WorkspaceID: wsID, TargetType: targetType, TargetID: targetID, MessageID: messageID, ActorUserID: userID,
	}, true
}

// ── Endpoints ─────────────────────────────────────────────────────────────────

// PinMessage handles POST /api/chat/channels/{channelID}/messages/{messageID}/pin.
// Idempotent; responds 204. A message that is not visible / not in the channel
// gets 404; a full channel gets 409.
func (h *MessageHandler) PinMessage(w http.ResponseWriter, r *http.Request) {
	if !h.checkPinDeps(w) {
		return
	}
	in, ok := h.pinRequestContext(w, r, service.PinTargetChannel, r.PathValue("channelID"), "channel_id")
	if !ok {
		return
	}
	if err := h.pins.Pin(r.Context(), in); err != nil {
		mapServiceError(w, err)
		return
	}
	h.broadcastPin(r.Context(), in, true)
	w.WriteHeader(http.StatusNoContent)
}

// PinDMMessage handles POST /api/chat/dm/{conversationID}/messages/{messageID}/pin.
func (h *MessageHandler) PinDMMessage(w http.ResponseWriter, r *http.Request) {
	if !h.checkPinDeps(w) {
		return
	}
	in, ok := h.pinRequestContext(w, r, service.PinTargetDM, r.PathValue("conversationID"), "conversation_id")
	if !ok {
		return
	}
	if err := h.pins.Pin(r.Context(), in); err != nil {
		mapServiceError(w, err)
		return
	}
	h.broadcastPin(r.Context(), in, true)
	w.WriteHeader(http.StatusNoContent)
}

// UnpinMessage handles DELETE /api/chat/channels/{channelID}/messages/{messageID}/pin.
// Idempotent; responds 204 whether or not the pin existed.
func (h *MessageHandler) UnpinMessage(w http.ResponseWriter, r *http.Request) {
	if !h.checkPinDeps(w) {
		return
	}
	in, ok := h.pinRequestContext(w, r, service.PinTargetChannel, r.PathValue("channelID"), "channel_id")
	if !ok {
		return
	}
	if err := h.pins.Unpin(r.Context(), in); err != nil {
		mapServiceError(w, err)
		return
	}
	h.broadcastPin(r.Context(), in, false)
	w.WriteHeader(http.StatusNoContent)
}

// UnpinDMMessage handles DELETE /api/chat/dm/{conversationID}/messages/{messageID}/pin.
func (h *MessageHandler) UnpinDMMessage(w http.ResponseWriter, r *http.Request) {
	if !h.checkPinDeps(w) {
		return
	}
	in, ok := h.pinRequestContext(w, r, service.PinTargetDM, r.PathValue("conversationID"), "conversation_id")
	if !ok {
		return
	}
	if err := h.pins.Unpin(r.Context(), in); err != nil {
		mapServiceError(w, err)
		return
	}
	h.broadcastPin(r.Context(), in, false)
	w.WriteHeader(http.StatusNoContent)
}

// broadcastPin publishes the pin change to target subscribers. Best-effort:
// the write already succeeded, so a nil broadcaster or bus failure is ignored.
func (h *MessageHandler) broadcastPin(ctx context.Context, in service.PinActionInput, pinned bool) {
	if h.pinBroadcaster == nil {
		return
	}
	h.pinBroadcaster.PublishPinUpdated(ctx, in.WorkspaceID, in.TargetType, in.TargetID, in.MessageID, in.ActorUserID, pinned)
}

// ListPins handles GET /api/chat/channels/{channelID}/pins.
// Returns the channel's pinned messages, newest first. Requires the same read
// access as reading the channel's messages; unauthorized channels return 404.
func (h *MessageHandler) ListPins(w http.ResponseWriter, r *http.Request) {
	if !h.checkPinDeps(w) {
		return
	}
	h.listPins(w, r, service.PinTargetChannel, r.PathValue("channelID"), "channel_id")
}

// ListDMPins handles GET /api/chat/dm/{conversationID}/pins.
// Requires the same read access as reading the DM messages.
func (h *MessageHandler) ListDMPins(w http.ResponseWriter, r *http.Request) {
	h.listPins(w, r, service.PinTargetDM, r.PathValue("conversationID"), "conversation_id")
}

func (h *MessageHandler) listPins(w http.ResponseWriter, r *http.Request, targetType, targetID, targetParam string) {
	if !h.checkPinDeps(w) {
		return
	}
	if !validateTargetID(w, targetID, targetParam) {
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

	out, err := h.pins.ListPins(r.Context(), service.ListPinsInput{
		WorkspaceID: wsID, TargetType: targetType, TargetID: targetID, ViewerID: userID,
	})
	if err != nil {
		mapServiceError(w, err)
		return
	}

	resp := listPinsResponseData{Pins: make([]pinJSON, 0, len(out.Pins)), TotalCount: out.TotalCount}
	for _, pin := range out.Pins {
		resp.Pins = append(resp.Pins, pinJSON{
			Message:        mapToMessageJSON(pin.Message),
			PinnedByUserID: pin.PinnedByUserID,
			PinnedAt:       pin.PinnedAt,
		})
	}
	httputil.WriteJSON(w, http.StatusOK, resp)
}
