package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
)

// channelProvider is the ChannelService surface used by ChannelHandler.
type channelProvider interface {
	CreateChannel(ctx context.Context, input service.CreateChannelInput) (domain.Channel, error)
	GetChannelDetails(ctx context.Context, input service.ChannelDetailsInput) (service.ChannelDetails, error)
}

// presenceLookup answers "who in this workspace is online right now" for the
// details panel. It is declared here, as the narrowest thing this handler needs,
// so the HTTP layer never imports the WebSocket package: app.go adapts the hub's
// tracker to it.
//
// It is deliberately a batch question rather than a per-user one. The panel
// needs the online set *before* the database applies its preview limit — asking
// per member would either mean a query per member or, worse, filtering a page
// that had already been cut, which is exactly the defect this shape prevents.
//
// Presence is per-instance state held by the WebSocket hub in this process. It
// is therefore accurate for a single-instance deployment (every overlay in
// infra/k8s runs chat-service with replicas: 1) and under-reports — never
// over-reports — if that ever changes. A nil lookup yields no online members
// rather than an invented status: "presence unknown" is never read as "online".
type presenceLookup interface {
	OnlineUserIDs(workspaceID string) []string
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
	presence   presenceLookup
}

func NewChannelHandler(workspaces workspaceResolver, channels channelProvider, limiter channelRateLimiter) *ChannelHandler {
	return &ChannelHandler{workspaces: workspaces, channels: channels, limiter: limiter}
}

// WithPresence returns a handler that can resolve which members are online.
// Wired after the WebSocket hub exists, exactly like MessageHandler's pin
// broadcaster. Without it the details payload carries no online members.
func (h *ChannelHandler) WithPresence(presence presenceLookup) *ChannelHandler {
	if h == nil {
		return nil
	}
	next := *h
	next.presence = presence
	return &next
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
// name, the channel type, the display-name bound and the authorization itself
// live below: any caller with a valid token, a live session, an active
// membership and an active workspace may create a channel, whatever their role
// (BUG #393), and the storage layer settles that atomically with the write.
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

// ── Channel details (issue #435) ─────────────────────────────────────────────

// channelDetailsMemberJSON is one member of the panel's online preview.
//
// It carries exactly what the panel renders and nothing else: a stable ID (so
// the client can mark "you" by identity rather than by name), the resolved
// display name, an optional avatar URL and the channel role. E-mail, workspace
// role, join date and every other profile attribute are deliberately absent —
// a details panel is not a directory export.
//
// Presence is a constant here rather than a per-member field: every entry in
// online_members is online by construction, and the field is still sent so a
// client can assert that rather than infer it from the list's name.
type channelDetailsMemberJSON struct {
	UserID      string `json:"user_id"`
	DisplayName string `json:"display_name"`
	AvatarURL   string `json:"avatar_url,omitempty"`
	Role        string `json:"role"`
	Presence    string `json:"presence"`
}

// channelDetailsResponse is the panel payload.
//
// The three member fields answer three different questions and none is derived
// from another:
//   - member_count is every active member of the channel, online or not;
//   - online_member_count is how many of those are online right now, which can
//     exceed len(online_members) when more are online than the preview shows;
//   - online_members is the capped preview itself, ordered by normalised display
//     name with the user ID as a deterministic tie-break.
//
// The list is named online_members, not members, because it is not a general
// roster and must not be read as one: it is filtered by presence *before* the
// limit is applied, so an offline member never occupies one of its slots and an
// online member never loses a slot to one.
//
// There is no description field: chat.channels has no description column, so
// there is nothing truthful to send. When the domain grows one, it is added
// here and the panel's existing empty state stops being the only outcome.
type channelDetailsResponse struct {
	ID                string                     `json:"id"`
	Slug              string                     `json:"slug"`
	DisplayName       string                     `json:"display_name"`
	Type              string                     `json:"type"`
	CreatedAt         string                     `json:"created_at"`
	MemberCount       int                        `json:"member_count"`
	OnlineMemberCount int                        `json:"online_member_count"`
	OnlineMembers     []channelDetailsMemberJSON `json:"online_members"`
}

// Presence values serialised by the details endpoints. presenceOnline is the
// only status an entry in online_members can carry; presenceOffline exists for
// the group panel, whose participant list is not presence-filtered and so has
// to say which of its rows are connected.
const (
	presenceOnline  = "online"
	presenceOffline = "offline"
)

// Details handles GET /api/chat/channels/{channelID}/details.
//
// The channel ID comes from the path and is never trusted: the service resolves
// the caller's visibility against the server-side workspace before any member
// row is read, and an invisible channel is answered with the same 404 as a
// missing one.
//
// The presence snapshot is collected once, in a single batch call, and handed
// to the service as a filter. It is read from in-process state and never
// reaches a response body unless the service's authorization gate passes first.
func (h *ChannelHandler) Details(w http.ResponseWriter, r *http.Request) {
	if h.workspaces == nil || h.channels == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "channels not available")
		return
	}
	channelID := r.PathValue("channelID")
	if !validateTargetID(w, channelID, "channel_id") {
		return
	}
	callerID := GetContextUserID(r)
	if callerID == "" {
		writeUnauthorized(w)
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

	details, err := h.channels.GetChannelDetails(r.Context(), service.ChannelDetailsInput{
		WorkspaceID:   workspace.ID,
		CallerID:      callerID,
		ChannelID:     channelID,
		OnlineUserIDs: h.onlineUserIDs(workspace.ID),
		MemberLimit:   domain.MaxChannelDetailsMembers,
	})
	if err != nil {
		writeChannelDetailsError(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, channelDetailsBody(details))
}

// onlineUserIDs returns the workspace's online users in one call, or nothing
// when presence is not wired — an unknown presence source yields an empty
// preview rather than members the server cannot vouch for.
func (h *ChannelHandler) onlineUserIDs(workspaceID string) []string {
	if h.presence == nil {
		return nil
	}
	return h.presence.OnlineUserIDs(workspaceID)
}

func channelDetailsBody(details service.ChannelDetails) channelDetailsResponse {
	members := make([]channelDetailsMemberJSON, 0, len(details.OnlineMembers))
	for _, member := range details.OnlineMembers {
		members = append(members, channelDetailsMemberJSON{
			UserID:      member.UserID,
			DisplayName: member.DisplayName,
			AvatarURL:   member.AvatarURL,
			Role:        string(member.Role),
			Presence:    presenceOnline,
		})
	}
	return channelDetailsResponse{
		ID:                details.Channel.ID,
		Slug:              details.Channel.Slug,
		DisplayName:       details.Channel.DisplayName,
		Type:              string(details.Channel.Type),
		CreatedAt:         details.Channel.CreatedAt.UTC().Format(time.RFC3339),
		MemberCount:       details.MemberCount,
		OnlineMemberCount: details.OnlineCount,
		OnlineMembers:     members,
	}
}

// writeChannelDetailsError maps the service errors. Unlike channel creation, a
// denial here must not distinguish "private channel you are not in" from
// "no such channel": both are ErrNotFound by construction in the service, and
// ErrForbidden (no active workspace membership) is folded into the same 404 so
// the route cannot confirm that a channel UUID exists in this workspace.
func writeChannelDetailsError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrNotFound), errors.Is(err, domain.ErrForbidden):
		httputil.WriteError(w, http.StatusNotFound, httputil.ErrCodeNotFound, "channel not found")
	default:
		httputil.WriteError(w, http.StatusInternalServerError, httputil.ErrCodeInternal, "internal error")
	}
}

// writeCreateChannelError keeps a denial legible. Unlike DM creation — where a
// 404 hides whether a given user exists — there is no identity to protect here,
// so a caller whose membership or workspace is no longer active gets a plain 403
// and the UI can say why instead of showing a generic failure. Which of the two
// it was stays unsaid; that distinction is the workspace's business.
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
