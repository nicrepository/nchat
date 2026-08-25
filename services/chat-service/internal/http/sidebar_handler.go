package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
)

// sidebarProvider is satisfied by *service.SidebarService; extracted for testing.
type sidebarProvider interface {
	GetSidebar(ctx context.Context, userID string) (service.SidebarData, error)
}

type sidebarPinProvider interface {
	PinConversation(ctx context.Context, userID, targetType, targetID string) error
	UnpinConversation(ctx context.Context, userID, targetType, targetID string) error
}

// sidebarMuteProvider is the SidebarService surface the mute routes use,
// declared narrowly and satisfied by the same service the pin routes use.
type sidebarMuteProvider interface {
	MuteConversation(ctx context.Context, userID, targetType, targetID string) error
	UnmuteConversation(ctx context.Context, userID, targetType, targetID string) error
}

type sidebarReadProvider interface {
	MarkConversationRead(ctx context.Context, userID, targetType, targetID string, lastReadMessageID *string) error
}

// sidebarWorkspaceJSON is the JSON shape for workspace info in the sidebar response.
type sidebarWorkspaceJSON struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
	// MaxUploadBytes publishes the RF-32 attachment size policy (issue #458) to
	// every member, not just administrators: a client needs it to tell a user
	// what fits before spending their bandwidth on an upload that file-service
	// will refuse. It is a policy number, not a capability — knowing it grants
	// nothing, and file-service re-reads it from the destination's own row on
	// every upload, so a client that ignores or edits this value changes only
	// which error it receives.
	MaxUploadBytes            int64 `json:"max_upload_bytes"`
	MaxMessageAttachments     int   `json:"max_message_attachments"`
	MaxMessageAttachmentBytes int64 `json:"max_message_attachment_bytes"`
}

// sidebarChannelJSON is the JSON shape for a channel in the sidebar response.
//
// CreatedAt and LastMessageAt are the two ordering keys the sidebar sorts each
// section by (issue #414), and they are two fields rather than one pre-resolved
// COALESCE on purpose: a conversation that has never been written to must sort
// *after* every conversation that has activity, however recently it was
// created, and a single collapsed value cannot express that distinction. Both
// come from the database — created_at from the channel row, last_message_at
// from the newest message's own created_at — so the order a client computes is
// the order the persisted state dictates, identical on every reload.
//
// LastMessageAt is a pointer so an empty channel serialises as an explicit
// null: absent-because-empty is a fact the client needs, not a value to guess
// at. It is the only thing said about that message — no body, author, kind or
// id — because ordering is all the sidebar does with it, and a preview is
// deliberately out of scope.
type sidebarChannelJSON struct {
	ID          string `json:"id"`
	Slug        string `json:"slug"`
	DisplayName string `json:"display_name"`
	Type        string `json:"type"`
	IsGeneral   bool   `json:"is_general"`
	CanWrite    bool   `json:"can_write"`
	// CanRename is the server's answer to "may this caller rename this channel"
	// (issue #527). It is presentation only — the row's action menu omits an
	// item the server would refuse — and PATCH /api/chat/channels/{channelID}
	// re-derives the same decision on every call. Never omitempty: a client
	// that predates the field reads a missing value as "no", which hides an
	// action rather than offering one that 403s.
	CanRename bool `json:"can_rename"`
	// Muted is this viewer's own notification preference (issue #527), never a
	// property of the channel. Always false for the general channel, which is
	// not silenceable. Never omitempty: absent must read as "not muted".
	Muted bool `json:"muted"`
	// RFC 3339 UTC, with the sub-second fraction kept — see formatSidebarTime
	// for why these two keep a precision the detail endpoints do not need.
	CreatedAt     string  `json:"created_at"`
	LastMessageAt *string `json:"last_message_at"`
	PinnedAt      *string `json:"pinned_at"`
	UnreadCount   *int    `json:"unread_count,omitempty"`
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
// created_at and last_message_at carry the same meaning, the same nullability
// and the same deliberate minimalism as their sidebarChannelJSON counterparts,
// so one ordering rule serves all three sections.
type sidebarDMJSON struct {
	ID            string                    `json:"id"`
	Type          string                    `json:"type"` // "direct" | "group"
	Name          string                    `json:"name"` // computed display name
	Counterpart   *sidebarDMCounterpartJSON `json:"counterpart,omitempty"`
	CreatedAt     string                    `json:"created_at"`
	LastMessageAt *string                   `json:"last_message_at"`
	PinnedAt      *string                   `json:"pinned_at"`
	UnreadCount   int                       `json:"unread_count"`
	// Muted is this viewer's own notification preference (issue #527).
	Muted bool `json:"muted"`
}

// sidebarResponseBody is the top-level JSON data object for the sidebar endpoint.
type sidebarResponseBody struct {
	CurrentUserID string               `json:"current_user_id"`
	Workspace     sidebarWorkspaceJSON `json:"workspace"`
	Channels      []sidebarChannelJSON `json:"channels"`
	DMConvs       []sidebarDMJSON      `json:"dm_conversations"`
	// Deprecated: retained for compatibility with older clients. Active
	// workspace members can create channels (BUG #393), so a 200 here already
	// implies true. Never omitempty — a client that predates the change reads a
	// missing field as "no" and would hide an action the server allows. The
	// current UI ignores it; POST /api/chat/channels remains authoritative.
	CanCreateChannel bool `json:"can_create_channel"`
}

// SidebarHandler handles GET /api/chat/sidebar.
type SidebarHandler struct {
	svc                       sidebarProvider
	maxMessageAttachments     int
	maxMessageAttachmentBytes int64
}

// NewSidebarHandler returns a SidebarHandler backed by svc. When svc is nil,
// all requests return 503 (service not yet wired).
func NewSidebarHandler(svc sidebarProvider) *SidebarHandler {
	return &SidebarHandler{
		svc:                       svc,
		maxMessageAttachments:     domain.MaxMessageAttachments,
		maxMessageAttachmentBytes: domain.DefaultMaxMessageAttachmentBytes,
	}
}

func (h *SidebarHandler) WithMessageAttachmentLimits(count int, bytes int64) *SidebarHandler {
	if count > 0 && count <= domain.MaxMessageAttachments {
		h.maxMessageAttachments = count
	}
	if bytes > 0 {
		h.maxMessageAttachmentBytes = bytes
	}
	return h
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
			ID:                        data.Workspace.ID,
			Name:                      data.Workspace.Name,
			Slug:                      data.Workspace.Slug,
			MaxUploadBytes:            domain.EffectiveMaxUploadBytes(data.Workspace.MaxUploadBytes),
			MaxMessageAttachments:     h.maxMessageAttachments,
			MaxMessageAttachmentBytes: h.maxMessageAttachmentBytes,
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
		unreadCount := sidebarChannel.UnreadCount
		out = append(out, sidebarChannelJSON{
			ID:            ch.ID,
			Slug:          ch.Slug,
			DisplayName:   ch.DisplayName,
			Type:          string(ch.Type),
			IsGeneral:     ch.IsGeneral,
			CanWrite:      sidebarChannel.CanWrite,
			CanRename:     sidebarChannel.CanRename,
			CreatedAt:     formatSidebarTime(ch.CreatedAt),
			LastMessageAt: formatSidebarTimePtr(sidebarChannel.LastMessageAt),
			PinnedAt:      formatSidebarTimePtr(sidebarChannel.PinnedAt),
			UnreadCount:   &unreadCount,
			Muted:         sidebarChannel.Muted,
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
			ID:            dm.ID,
			Type:          string(dm.Type),
			Name:          name,
			Counterpart:   mapDMCounterpart(dm, name),
			CreatedAt:     formatSidebarTime(dm.CreatedAt),
			LastMessageAt: formatSidebarTimePtr(dm.LastMessageAt),
			PinnedAt:      formatSidebarTimePtr(dm.PinnedAt),
			UnreadCount:   dm.UnreadCount,
			Muted:         dm.Muted,
		})
	}
	return out
}

func (h *SidebarHandler) PinChannel(w http.ResponseWriter, r *http.Request) {
	h.pinConversation(w, r, service.PinTargetChannel, r.PathValue("channelID"), "channel_id", true)
}

func (h *SidebarHandler) UnpinChannel(w http.ResponseWriter, r *http.Request) {
	h.pinConversation(w, r, service.PinTargetChannel, r.PathValue("channelID"), "channel_id", false)
}

func (h *SidebarHandler) PinDM(w http.ResponseWriter, r *http.Request) {
	h.pinConversation(w, r, service.PinTargetDM, r.PathValue("conversationID"), "conversation_id", true)
}

func (h *SidebarHandler) UnpinDM(w http.ResponseWriter, r *http.Request) {
	h.pinConversation(w, r, service.PinTargetDM, r.PathValue("conversationID"), "conversation_id", false)
}

func (h *SidebarHandler) MuteChannel(w http.ResponseWriter, r *http.Request) {
	h.muteConversation(w, r, service.ReadTargetChannel, r.PathValue("channelID"), "channel_id", true)
}

func (h *SidebarHandler) UnmuteChannel(w http.ResponseWriter, r *http.Request) {
	h.muteConversation(w, r, service.ReadTargetChannel, r.PathValue("channelID"), "channel_id", false)
}

func (h *SidebarHandler) MuteDM(w http.ResponseWriter, r *http.Request) {
	h.muteConversation(w, r, service.ReadTargetDM, r.PathValue("conversationID"), "conversation_id", true)
}

func (h *SidebarHandler) UnmuteDM(w http.ResponseWriter, r *http.Request) {
	h.muteConversation(w, r, service.ReadTargetDM, r.PathValue("conversationID"), "conversation_id", false)
}

// muteConversation is the same shape pinConversation has, and for the same
// reasons: the target is a path segment validated as a UUID, the actor is the
// authenticated principal, the workspace is resolved server-side, and there is
// no body at all — so nothing a client sends can name a user, a workspace or a
// role. The general-channel refusal lives below, in SQL.
func (h *SidebarHandler) muteConversation(w http.ResponseWriter, r *http.Request, targetType, targetID, targetParam string, muted bool) {
	mutes, ok := h.svc.(sidebarMuteProvider)
	if h.svc == nil || !ok {
		httputil.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "sidebar not available")
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
	var err error
	if muted {
		err = mutes.MuteConversation(r.Context(), userID, targetType, targetID)
	} else {
		err = mutes.UnmuteConversation(r.Context(), userID, targetType, targetID)
	}
	if err != nil {
		mapServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type markConversationReadRequest struct {
	LastReadMessageID *string `json:"last_read_message_id"`
}

func (h *SidebarHandler) MarkChannelRead(w http.ResponseWriter, r *http.Request) {
	h.markConversationRead(w, r, service.ReadTargetChannel, r.PathValue("channelID"), "channel_id")
}

func (h *SidebarHandler) MarkDMRead(w http.ResponseWriter, r *http.Request) {
	h.markConversationRead(w, r, service.ReadTargetDM, r.PathValue("conversationID"), "conversation_id")
}

func (h *SidebarHandler) markConversationRead(w http.ResponseWriter, r *http.Request, targetType, targetID, targetParam string) {
	reads, ok := h.svc.(sidebarReadProvider)
	if h.svc == nil || !ok {
		httputil.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "sidebar not available")
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
	var body markConversationReadRequest
	if r.ContentLength > 0 && !decodeStrictJSON(w, r, &body) {
		return
	}
	if body.LastReadMessageID != nil && !validateTargetID(w, *body.LastReadMessageID, "last_read_message_id") {
		return
	}
	if err := reads.MarkConversationRead(r.Context(), userID, targetType, targetID, body.LastReadMessageID); err != nil {
		mapServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *SidebarHandler) pinConversation(w http.ResponseWriter, r *http.Request, targetType, targetID, targetParam string, pinned bool) {
	pins, ok := h.svc.(sidebarPinProvider)
	if h.svc == nil || !ok {
		httputil.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "sidebar not available")
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
	var err error
	if pinned {
		err = pins.PinConversation(r.Context(), userID, targetType, targetID)
	} else {
		err = pins.UnpinConversation(r.Context(), userID, targetType, targetID)
	}
	if err != nil {
		mapServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// formatSidebarTime renders an instant as RFC 3339 in UTC, so a client parses
// one shape whatever the server's zone is, and keeps whatever sub-second
// precision the value carries.
//
// The fraction is not cosmetic here, which is why this is RFC3339Nano and not
// RFC3339 like the detail endpoints. chat.messages.created_at is a TIMESTAMPTZ
// and holds microseconds; the query picks the newest message by
// (created_at, id), so two conversations written in the same second are
// genuinely ordered. Truncating to whole seconds would publish them as the same
// instant and hand the decision to the name/id tie-breakers — a different order
// from the one the database actually has, and one that could disagree with what
// the WebSocket event (which never truncated) already showed.
//
// RFC3339Nano drops trailing zeros, so ".900000000" is emitted as ".9" and a
// whole second carries no fraction at all. Those are the same instant written
// three ways, and the client compares instants rather than strings.
func formatSidebarTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

// formatSidebarTimePtr keeps "there is no such instant" distinguishable from
// "the instant is the zero time": nil stays nil and serialises as JSON null,
// rather than becoming year 1 — a value that would sort as very old activity
// instead of as no activity at all.
func formatSidebarTimePtr(value *time.Time) *string {
	if value == nil {
		return nil
	}
	formatted := formatSidebarTime(*value)
	return &formatted
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
