package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// uuidPattern matches a canonical UUID (case-insensitive).
var uuidPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// maxBodyBytes caps request body reads to prevent memory abuse.
const maxBodyBytes = 1 << 16 // 64 KiB

// workspaceResolver resolves the single default workspace.
// Satisfied by storage.WorkspaceStore.
type workspaceResolver interface {
	GetDefaultWorkspace(ctx context.Context) (domain.Workspace, error)
}

// messageProvider is the MessageService interface used by MessageHandler.
type messageProvider interface {
	ListChannelMessages(ctx context.Context, in service.ListChannelMessagesInput) (service.ListChannelMessagesOutput, error)
	CreateChannelMessage(ctx context.Context, in service.CreateChannelMessageInput) (domain.Message, error)
	GetChannelMessage(ctx context.Context, in service.GetChannelMessageInput) (domain.Message, error)
	ListDMMessages(ctx context.Context, in service.ListDMMessagesInput) (service.ListDMMessagesOutput, error)
	CreateDMMessage(ctx context.Context, in service.CreateDMMessageInput) (domain.Message, error)
	GetDMMessage(ctx context.Context, in service.GetDMMessageInput) (domain.Message, error)
}

type mentionProvider interface {
	SearchMentions(ctx context.Context, in service.SearchMentionsInput) (service.SearchMentionsOutput, error)
}

// MessageHandler handles message list and create endpoints for channels and DMs.
type MessageHandler struct {
	workspaces workspaceResolver
	messages   messageProvider
	mentions   mentionProvider
}

// NewMessageHandler returns a MessageHandler. Missing dependencies produce 503
// only on the endpoints that use them.
func NewMessageHandler(workspaces workspaceResolver, messages messageProvider, mentions mentionProvider) *MessageHandler {
	return &MessageHandler{workspaces: workspaces, messages: messages, mentions: mentions}
}

// ── JSON response shapes ──────────────────────────────────────────────────────

// messageJSON is the outbound representation of a single message.
// body_text is suppressed for deleted messages; is_removed is set instead.
type messageJSON struct {
	ID                string         `json:"id"`
	SenderID          string         `json:"sender_id"`
	SenderDisplayName string         `json:"sender_display_name,omitempty"`
	SenderEmail       string         `json:"sender_email,omitempty"`
	Kind              string         `json:"kind"`
	BodyText          string         `json:"body_text,omitempty"`
	BodyFormat        string         `json:"body_format"`
	IsRemoved         bool           `json:"is_removed,omitempty"`
	Status            string         `json:"status"`
	CreatedAt         time.Time      `json:"created_at"`
	UpdatedAt         time.Time      `json:"updated_at"`
	Reactions         []reactionJSON `json:"reactions"`
}

type reactionJSON struct {
	Emoji       string `json:"emoji"`
	Count       int    `json:"count"`
	ReactedByMe bool   `json:"reacted_by_me"`
}

// listMessagesResponseData is the data envelope for list endpoints.
type listMessagesResponseData struct {
	Messages   []messageJSON `json:"messages"`
	NextCursor string        `json:"next_cursor,omitempty"`
}

type mentionJSON struct {
	Type  string `json:"type"`
	ID    string `json:"id"`
	Label string `json:"label"`
}

type searchMentionsResponseData struct {
	Users    []mentionJSON `json:"users"`
	Channels []mentionJSON `json:"channels"`
}

// ── Request shapes ────────────────────────────────────────────────────────────

// createMessageRequest is the inbound body for POST message endpoints.
// Only body_text and body_format are accepted. Any unrecognised field causes a 400.
type createMessageRequest struct {
	BodyText   string `json:"body_text"`
	BodyFormat string `json:"body_format"`
}

// ── Shared helpers ────────────────────────────────────────────────────────────

// checkDeps returns false and writes 503 if either dependency is nil.
func (h *MessageHandler) checkDeps(w http.ResponseWriter) bool {
	if h.workspaces == nil || h.messages == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "messages not available")
		return false
	}
	return true
}

func (h *MessageHandler) checkMentionDeps(w http.ResponseWriter) bool {
	if h.workspaces == nil || h.mentions == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "mentions not available")
		return false
	}
	return true
}

// resolveWorkspaceID resolves the default workspace and returns its ID.
func (h *MessageHandler) resolveWorkspaceID(ctx context.Context, w http.ResponseWriter) (string, bool) {
	ws, err := h.workspaces.GetDefaultWorkspace(ctx)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			httputil.WriteError(w, http.StatusNotFound, httputil.ErrCodeNotFound, "workspace not found")
		} else {
			httputil.WriteError(w, http.StatusInternalServerError, httputil.ErrCodeInternal, "internal error")
		}
		return "", false
	}
	return ws.ID, true
}

// validateTargetID validates that the given path parameter is a well-formed UUID.
// Writes 400 and returns false on failure.
func validateTargetID(w http.ResponseWriter, id, paramName string) bool {
	if !uuidPattern.MatchString(id) {
		httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, paramName+" must be a valid UUID")
		return false
	}
	return true
}

// parseLimitParam parses the optional ?limit= query parameter.
// Returns 0 (default) when absent or invalid.
func parseLimitParam(r *http.Request) int {
	s := r.URL.Query().Get("limit")
	if s == "" {
		return 0
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// mapToMessageJSON converts a domain.Message to its JSON representation.
// For deleted messages, body_text is withheld and is_removed is set.
func mapToMessageJSON(m domain.Message) messageJSON {
	j := messageJSON{
		ID:                m.ID,
		SenderID:          m.SenderID,
		SenderDisplayName: m.SenderDisplayName,
		SenderEmail:       m.SenderEmail,
		Kind:              string(m.Kind),
		BodyFormat:        string(m.BodyFormat),
		Status:            string(m.Status),
		CreatedAt:         m.CreatedAt,
		UpdatedAt:         m.UpdatedAt,
		Reactions:         make([]reactionJSON, len(m.Reactions)),
	}
	for i, reaction := range m.Reactions {
		j.Reactions[i] = reactionJSON{Emoji: reaction.Emoji, Count: reaction.Count, ReactedByMe: reaction.ReactedByMe}
	}
	if m.Status == domain.MessageStatusDeleted {
		j.IsRemoved = true
	} else {
		j.BodyText = m.BodyText
	}
	return j
}

// decodeCreateRequest reads and decodes the request body into a createMessageRequest.
// Rejects unknown fields. Returns false and writes 400 on any parse error.
func decodeCreateRequest(w http.ResponseWriter, r *http.Request) (createMessageRequest, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	var req createMessageRequest
	if err := dec.Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, "invalid request body")
		return createMessageRequest{}, false
	}
	return req, true
}

// ── Channel endpoints ─────────────────────────────────────────────────────────

// SearchMentions handles GET /api/chat/channels/{channelID}/mentions?q=prefix.
func (h *MessageHandler) SearchMentions(w http.ResponseWriter, r *http.Request) {
	if !h.checkMentionDeps(w) {
		return
	}

	channelID := r.PathValue("channelID")
	if !validateTargetID(w, channelID, "channel_id") {
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

	out, err := h.mentions.SearchMentions(r.Context(), service.SearchMentionsInput{
		WorkspaceID: wsID,
		ChannelID:   channelID,
		CallerID:    userID,
		Query:       r.URL.Query().Get("q"),
	})
	if err != nil {
		mapServiceError(w, err)
		return
	}

	resp := searchMentionsResponseData{
		Users:    mapMentions(out.Users),
		Channels: mapMentions(out.Channels),
	}
	httputil.WriteJSON(w, http.StatusOK, resp)
}

// ListChannelMessages handles GET /api/chat/channels/{channelID}/messages.
// Auth: BearerAuth + RequireActiveSession (applied in router).
// Query params: before= (cursor), limit= (1-100, default 50).
// Response: {"data": {"messages": [...], "next_cursor": "..."}}
func (h *MessageHandler) ListChannelMessages(w http.ResponseWriter, r *http.Request) {
	if !h.checkDeps(w) {
		return
	}

	channelID := r.PathValue("channelID")
	if !validateTargetID(w, channelID, "channel_id") {
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

	beforeCursor := r.URL.Query().Get("before")
	if beforeCursor != "" {
		if _, err := storage.DecodeCursor(beforeCursor); err != nil {
			httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, "invalid cursor")
			return
		}
	}

	out, err := h.messages.ListChannelMessages(r.Context(), service.ListChannelMessagesInput{
		WorkspaceID:  wsID,
		ChannelID:    channelID,
		CallerID:     userID,
		BeforeCursor: beforeCursor,
		Limit:        parseLimitParam(r),
	})
	if err != nil {
		mapServiceError(w, err)
		return
	}

	resp := listMessagesResponseData{
		Messages:   mapMessages(out.Messages),
		NextCursor: out.NextCursor,
	}
	if resp.Messages == nil {
		resp.Messages = []messageJSON{}
	}
	httputil.WriteJSON(w, http.StatusOK, resp)
}

// CreateChannelMessage handles POST /api/chat/channels/{channelID}/messages.
// Auth: BearerAuth + RequireActiveSession (applied in router).
// Body: {"body_text": "..."}  — author_id and all other fields are rejected.
func (h *MessageHandler) CreateChannelMessage(w http.ResponseWriter, r *http.Request) {
	if !h.checkDeps(w) {
		return
	}

	channelID := r.PathValue("channelID")
	if !validateTargetID(w, channelID, "channel_id") {
		return
	}

	userID := GetContextUserID(r)
	if userID == "" {
		httputil.WriteError(w, http.StatusUnauthorized, httputil.ErrCodeUnauthorized, "unauthorized")
		return
	}

	req, ok := decodeCreateRequest(w, r)
	if !ok {
		return
	}

	wsID, ok := h.resolveWorkspaceID(r.Context(), w)
	if !ok {
		return
	}

	msg, err := h.messages.CreateChannelMessage(r.Context(), service.CreateChannelMessageInput{
		WorkspaceID: wsID,
		ChannelID:   channelID,
		SenderID:    userID, // always from auth context — never from body
		BodyText:    req.BodyText,
		BodyFormat:  domain.MessageBodyFormat(req.BodyFormat),
	})
	if err != nil {
		mapServiceError(w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusCreated, mapToMessageJSON(msg))
}

// ── DM endpoints ──────────────────────────────────────────────────────────────

// ListDMMessages handles GET /api/chat/dm/{conversationID}/messages.
func (h *MessageHandler) ListDMMessages(w http.ResponseWriter, r *http.Request) {
	if !h.checkDeps(w) {
		return
	}

	convID := r.PathValue("conversationID")
	if !validateTargetID(w, convID, "conversation_id") {
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

	beforeCursor := r.URL.Query().Get("before")
	if beforeCursor != "" {
		if _, err := storage.DecodeCursor(beforeCursor); err != nil {
			httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, "invalid cursor")
			return
		}
	}

	out, err := h.messages.ListDMMessages(r.Context(), service.ListDMMessagesInput{
		WorkspaceID:    wsID,
		ConversationID: convID,
		CallerID:       userID,
		BeforeCursor:   beforeCursor,
		Limit:          parseLimitParam(r),
	})
	if err != nil {
		mapServiceError(w, err)
		return
	}

	resp := listMessagesResponseData{
		Messages:   mapMessages(out.Messages),
		NextCursor: out.NextCursor,
	}
	if resp.Messages == nil {
		resp.Messages = []messageJSON{}
	}
	httputil.WriteJSON(w, http.StatusOK, resp)
}

// CreateDMMessage handles POST /api/chat/dm/{conversationID}/messages.
func (h *MessageHandler) CreateDMMessage(w http.ResponseWriter, r *http.Request) {
	if !h.checkDeps(w) {
		return
	}

	convID := r.PathValue("conversationID")
	if !validateTargetID(w, convID, "conversation_id") {
		return
	}

	userID := GetContextUserID(r)
	if userID == "" {
		httputil.WriteError(w, http.StatusUnauthorized, httputil.ErrCodeUnauthorized, "unauthorized")
		return
	}

	req, ok := decodeCreateRequest(w, r)
	if !ok {
		return
	}

	wsID, ok := h.resolveWorkspaceID(r.Context(), w)
	if !ok {
		return
	}

	msg, err := h.messages.CreateDMMessage(r.Context(), service.CreateDMMessageInput{
		WorkspaceID:    wsID,
		ConversationID: convID,
		SenderID:       userID,
		BodyText:       req.BodyText,
		BodyFormat:     domain.MessageBodyFormat(req.BodyFormat),
	})
	if err != nil {
		mapServiceError(w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusCreated, mapToMessageJSON(msg))
}

// ── Shared helpers ────────────────────────────────────────────────────────────

// GetChannelMessage handles GET /api/chat/channels/{channelID}/messages/{messageID}.
// Returns a single message. The caller must have read access to the channel.
func (h *MessageHandler) GetChannelMessage(w http.ResponseWriter, r *http.Request) {
	if !h.checkDeps(w) {
		return
	}

	channelID := r.PathValue("channelID")
	if !validateTargetID(w, channelID, "channel_id") {
		return
	}

	messageID := r.PathValue("messageID")
	if !validateTargetID(w, messageID, "message_id") {
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

	msg, err := h.messages.GetChannelMessage(r.Context(), service.GetChannelMessageInput{
		WorkspaceID: wsID,
		ChannelID:   channelID,
		CallerID:    userID,
		MessageID:   messageID,
	})
	if err != nil {
		mapServiceError(w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusOK, mapToMessageJSON(msg))
}

// GetDMMessage handles GET /api/chat/dm/{conversationID}/messages/{messageID}.
// Returns a single message. The caller must be a participant in the DM conversation.
func (h *MessageHandler) GetDMMessage(w http.ResponseWriter, r *http.Request) {
	if !h.checkDeps(w) {
		return
	}

	convID := r.PathValue("conversationID")
	if !validateTargetID(w, convID, "conversation_id") {
		return
	}

	messageID := r.PathValue("messageID")
	if !validateTargetID(w, messageID, "message_id") {
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

	msg, err := h.messages.GetDMMessage(r.Context(), service.GetDMMessageInput{
		WorkspaceID:    wsID,
		ConversationID: convID,
		CallerID:       userID,
		MessageID:      messageID,
	})
	if err != nil {
		mapServiceError(w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusOK, mapToMessageJSON(msg))
}

func mapMessages(msgs []domain.Message) []messageJSON {
	out := make([]messageJSON, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, mapToMessageJSON(m))
	}
	return out
}

func mapMentions(candidates []domain.MentionCandidate) []mentionJSON {
	out := make([]mentionJSON, 0, len(candidates))
	for _, candidate := range candidates {
		out = append(out, mentionJSON{
			Type:  string(candidate.Type),
			ID:    candidate.ID,
			Label: candidate.Label,
		})
	}
	return out
}

// mapServiceError maps service-layer errors to HTTP status codes.
// Keeps error messages generic to avoid leaking internal details.
func mapServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrInvalidInput):
		httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, "invalid request")
	case errors.Is(err, domain.ErrInvalidCursor):
		httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, "invalid cursor")
	case errors.Is(err, domain.ErrNotFound):
		// Non-enumerating: use the same status for unauthorized targets.
		httputil.WriteError(w, http.StatusNotFound, httputil.ErrCodeNotFound, "not found")
	case errors.Is(err, domain.ErrForbidden):
		httputil.WriteError(w, http.StatusForbidden, httputil.ErrCodeForbidden, "forbidden")
	default:
		httputil.WriteError(w, http.StatusInternalServerError, httputil.ErrCodeInternal, "internal error")
	}
}
