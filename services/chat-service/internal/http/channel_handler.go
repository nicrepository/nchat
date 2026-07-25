package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
)

// channelProvider is the ChannelService surface used by ChannelHandler.
type channelProvider interface {
	CreateChannel(ctx context.Context, input service.CreateChannelInput) (domain.Channel, error)
}

// channelRateLimiter is the same shape as the DM limiter, declared separately so
// this handler depends on the operation it uses rather than on the DM handler.
type channelRateLimiter interface {
	AllowActionWithLimit(ctx context.Context, userID, action string, maxActions, windowSeconds int) (bool, error)
}

const (
	// channelCreateRateLimit is deliberately tight: every accepted call creates a
	// workspace-wide, permanent object that only an owner/admin can archive again.
	channelCreateRateLimit        = 10
	channelRateLimitWindowSeconds = 60
)

type ChannelHandler struct {
	workspaces workspaceResolver
	channels   channelProvider
	limiter    channelRateLimiter
}

func NewChannelHandler(workspaces workspaceResolver, channels channelProvider, limiter channelRateLimiter) *ChannelHandler {
	return &ChannelHandler{workspaces: workspaces, channels: channels, limiter: limiter}
}

// createChannelRequest is the whole accepted body. The workspace, the creator,
// is_general, status and position are server-derived and deliberately absent —
// the strict decoder answers 400 to a client that sends them.
type createChannelRequest struct {
	Slug        string `json:"slug"`
	DisplayName string `json:"display_name"`
	Type        string `json:"type"`
}

type createChannelResponse struct {
	ID          string `json:"id"`
	Slug        string `json:"slug"`
	DisplayName string `json:"display_name"`
	Type        string `json:"type"`
}

// Create handles POST /api/chat/channels.
//
// It carries transport concerns only — authentication, rate limiting, body shape
// and the server-side workspace lookup. The slug rules, the reserved "geral"
// name, the channel type and, above all, the owner/admin requirement live in
// ChannelService, which is the single authority for them.
func (h *ChannelHandler) Create(w http.ResponseWriter, r *http.Request) {
	if h.workspaces == nil || h.channels == nil || h.limiter == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "channels not available")
		return
	}
	callerID := GetContextUserID(r)
	if callerID == "" {
		writeUnauthorized(w)
		return
	}
	allowed, err := h.limiter.AllowActionWithLimit(r.Context(), callerID, "channel_create", channelCreateRateLimit, channelRateLimitWindowSeconds)
	if err != nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "channels not available")
		return
	}
	if !allowed {
		w.Header().Set("Retry-After", strconv.Itoa(channelRateLimitWindowSeconds))
		httputil.WriteError(w, http.StatusTooManyRequests, "rate_limited", "too many requests")
		return
	}
	if !requireJSONContentType(w, r) {
		return
	}
	var request createChannelRequest
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

	channel, err := h.channels.CreateChannel(r.Context(), service.CreateChannelInput{
		WorkspaceID: workspace.ID,
		CallerID:    callerID,
		Slug:        request.Slug,
		DisplayName: request.DisplayName,
		Type:        domain.ChannelType(request.Type),
	})
	if err != nil {
		writeCreateChannelError(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusCreated, createChannelResponse{
		ID:          channel.ID,
		Slug:        channel.Slug,
		DisplayName: channel.DisplayName,
		Type:        string(channel.Type),
	})
}

// writeCreateChannelError keeps a denial legible. Unlike DM creation — where a
// 404 hides whether a given user exists — there is no identity to protect here,
// so a caller without the role gets a plain 403 and the UI can say why instead
// of showing a generic failure.
func writeCreateChannelError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrInvalidInput):
		httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, "invalid channel")
	case errors.Is(err, domain.ErrForbidden):
		httputil.WriteError(w, http.StatusForbidden, httputil.ErrCodeForbidden, "forbidden")
	case errors.Is(err, domain.ErrDuplicateSlug), errors.Is(err, domain.ErrConflict):
		httputil.WriteError(w, http.StatusConflict, "conflict", "channel already exists")
	case errors.Is(err, domain.ErrNotFound):
		httputil.WriteError(w, http.StatusNotFound, httputil.ErrCodeNotFound, "workspace not found")
	default:
		httputil.WriteError(w, http.StatusInternalServerError, httputil.ErrCodeInternal, "internal error")
	}
}
