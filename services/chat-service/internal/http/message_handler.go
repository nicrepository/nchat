package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// uuidPattern matches a canonical UUID (case-insensitive).
var uuidPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var idempotencyKeyPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

// maxBodyBytes caps request body reads to prevent memory abuse.
const maxBodyBytes = 1 << 16 // 64 KiB

// RF-21 error codes. They are stable identifiers a client keys on, which is the
// point: a frontend must be able to tell a blocked link from a malformed
// request without parsing English. The two are separate because one is
// permanent and the other is worth retrying.
const (
	errCodeMaliciousURL         = "malicious_url"
	errCodeLinkCheckUnavailable = "link_check_unavailable"
	// errCodeLinkCheckPending says the links are being scanned right now and
	// the operation should be retried shortly. It is only ever returned by
	// editing: creating and forwarding accept the message and withhold it
	// instead, which is reported by the body's `status` field rather than by an
	// error.
	errCodeLinkCheckPending = "link_check_pending"
	// errCodeLinkCheckCapacity says this deployment declined to start new scans
	// right now. It is retryable and it is not a verdict — the client must not
	// present it as a blocked link.
	errCodeLinkCheckCapacity = "link_check_capacity"
)

// workspaceResolver resolves the single default workspace.
// Satisfied by storage.WorkspaceStore.
type workspaceResolver interface {
	GetDefaultWorkspace(ctx context.Context) (domain.Workspace, error)
}

// messageProvider is the MessageService interface used by MessageHandler.
type messageProvider interface {
	ListChannelMessages(ctx context.Context, in service.ListChannelMessagesInput) (service.ListChannelMessagesOutput, error)
	CreateChannelMessage(ctx context.Context, in service.CreateChannelMessageInput) (domain.Message, error)
	ForwardChannelMessage(ctx context.Context, in service.ForwardChannelMessageInput) (service.ForwardChannelMessageOutput, error)
	GetChannelMessage(ctx context.Context, in service.GetChannelMessageInput) (domain.Message, error)
	ListDMMessages(ctx context.Context, in service.ListDMMessagesInput) (service.ListDMMessagesOutput, error)
	CreateDMMessage(ctx context.Context, in service.CreateDMMessageInput) (domain.Message, error)
	GetDMMessage(ctx context.Context, in service.GetDMMessageInput) (domain.Message, error)
	ResolveMessageReferenceBatch(ctx context.Context, in service.ResolveMessageReferencesInput) ([]service.MessageReferenceResolution, error)
	MessageSecuritySnapshots(ctx context.Context, in service.MessageSecuritySnapshotsInput) ([]service.MessageSecuritySnapshot, error)
	EditMessage(ctx context.Context, in service.EditMessageInput) (domain.Message, error)
	DeleteMessage(ctx context.Context, in service.DeleteMessageInput) (domain.Message, error)
	GetMessageEditHistory(ctx context.Context, in service.GetMessageEditHistoryInput) ([]domain.MessageEditHistory, error)
	MessageLinkSafetyStates(ctx context.Context, in service.LinkSafetyStatusInput) ([]domain.MessageLinkSafetyState, error)
}

// linkReconcileProvider is the LinkReconcileService interface used by the
// "Verificar novamente" endpoint (issue #135).
//
// One method, taking a workspace, a viewer and a message id — and nothing else.
// There is deliberately no parameter here that could carry a URL or a scan uuid:
// the endpoint reads both from the database, which is what keeps it from becoming
// a Cloudflare search proxy billed to this deployment's account.
type linkReconcileProvider interface {
	ReconcileMessage(ctx context.Context, in service.ReconcileMessageInput) (service.ReconcileMessageResult, error)
	Ready() bool
}

type editRateLimiter interface {
	AllowAction(ctx context.Context, userID, action string) (bool, error)
}

// actionRateLimiter is the shared, Valkey-backed fixed-window limiter with a
// per-operation budget (issue #135, CQ-005).
//
// It is the same limiter the edit and reaction paths already use, asked for a
// different allowance. An in-process limiter was wrong here for a reason that
// scales with the deployment: it gives every replica its own full budget, so N
// pods admit N × the intended rate at the only user-triggered route that reaches
// a paid third party. The counter has to live where all the pods can see it.
//
// The key is derived inside the limiter and hashes the user id, so no PII
// reaches Valkey, a log or a metric.
type actionRateLimiter interface {
	AllowActionWithLimit(ctx context.Context, userID, action string, maxActions, windowSeconds int) (bool, error)
}

type workspaceSettingsAuthorizer interface {
	CanManageWorkspace(ctx context.Context, workspaceID, userID string) (bool, error)
}

type mentionProvider interface {
	SearchMentions(ctx context.Context, in service.SearchMentionsInput) (service.SearchMentionsOutput, error)
}

// MessageHandler handles message list and create endpoints for channels and DMs.
type MessageHandler struct {
	workspaces     workspaceResolver
	messages       messageProvider
	mentions       mentionProvider
	favorites      favoriteProvider
	pins           pinProvider
	pinBroadcaster pinBroadcaster
	settings       storage.WorkspaceSettingsStore
	settingsAuth   workspaceSettingsAuthorizer
	editLimiter    editRateLimiter
	// linkReconcile answers "Verificar novamente" (issue #135). Nil is a working
	// deployment: the route then answers 503 rather than pretending it looked.
	linkReconcile linkReconcileProvider
	// reconcileLimiter is the deployment-wide budget for that route. Nil means the
	// route answers 503 rather than running unlimited: a limiter that cannot be
	// consulted is not permission to spend the provider's quota.
	reconcileLimiter actionRateLimiter
	// antiSpam is the RF-19 guard, held only so a policy update can invalidate
	// its cache. Nil is safe: Invalidate is a no-op and the guard's TTL still
	// picks the new value up.
	antiSpam *AntiSpamGuard
}

// WithEditing enables message editing/history and workspace edit-window settings.
func (h *MessageHandler) WithEditing(settings storage.WorkspaceSettingsStore, auth workspaceSettingsAuthorizer, limiter editRateLimiter) *MessageHandler {
	h.settings, h.settingsAuth, h.editLimiter = settings, auth, limiter
	return h
}

// WithAntiSpam enables the RF-19 admin endpoints to invalidate the policy cache
// of the guard enforcing the limit in this process (issue #419).
func (h *MessageHandler) WithAntiSpam(guard *AntiSpamGuard) *MessageHandler {
	h.antiSpam = guard
	return h
}

// WithLinkReconcile enables the RF-21 "check again" endpoint (issue #135).
//
// Both dependencies are required for the route to serve: the use case, and the
// shared limiter that bounds it across every replica. Returns the handler for
// chaining; when never called, or called with either half missing, the route
// answers 503.
func (h *MessageHandler) WithLinkReconcile(
	reconcile linkReconcileProvider, limiter actionRateLimiter,
) *MessageHandler {
	h.linkReconcile = reconcile
	h.reconcileLimiter = limiter
	return h
}

// WithFavorites enables the RF-06 favorite endpoints. Returns the handler for
// chaining; when never called, favorite routes answer 503.
func (h *MessageHandler) WithFavorites(favorites favoriteProvider) *MessageHandler {
	h.favorites = favorites
	return h
}

// WithPins enables the RF-05 pin endpoints. broadcaster fans pin changes out
// to target subscribers over WebSocket. Returns the handler for
// chaining; when never called, pin routes answer 503.
func (h *MessageHandler) WithPins(pins pinProvider, broadcaster pinBroadcaster) *MessageHandler {
	h.pins = pins
	h.pinBroadcaster = broadcaster
	return h
}

// NewMessageHandler returns a MessageHandler. Missing dependencies produce 503
// only on the endpoints that use them.
func NewMessageHandler(workspaces workspaceResolver, messages messageProvider, mentions mentionProvider) *MessageHandler {
	return &MessageHandler{workspaces: workspaces, messages: messages, mentions: mentions}
}

// Ready reports whether the handler is wired to the DB-backed message service
// and workspace resolver. Used by the readiness probe; when either is nil the
// message endpoints return 503.
func (h *MessageHandler) Ready() bool {
	return h != nil && h.messages != nil && h.workspaces != nil
}

// ── JSON response shapes ──────────────────────────────────────────────────────

// messageJSON is the outbound representation of a single message.
// body_text is suppressed for deleted messages; is_removed is set instead.
type messageJSON struct {
	ID                string `json:"id"`
	SenderID          string `json:"sender_id"`
	SenderDisplayName string `json:"sender_display_name,omitempty"`
	SenderEmail       string `json:"sender_email,omitempty"`
	// SenderAvatarURL is the sender's auth.users.avatar_url, straight from the
	// same JOIN as SenderDisplayName/SenderEmail (issue #495). Omitted when the
	// sender has none set. Same-origin/scheme safety is a render-time client
	// concern, exactly like every other avatar_url this API already returns
	// (sidebar counterpart, channel members, group participants, direct
	// profile) — this handler forwards the stored value unmodified.
	SenderAvatarURL string `json:"sender_avatar_url,omitempty"`
	Kind            string `json:"kind"`
	BodyText        string `json:"body_text,omitempty"`
	BodyFormat      string `json:"body_format"`
	IsRemoved       bool   `json:"is_removed,omitempty"`
	Status          string `json:"status"`
	// LinkSafetyState is the link-safety axis and is independent of Status
	// (issue #135): a published message whose links could not all be verified is
	// `active` and carries "inconclusive" here. It is what the client draws the
	// notice from, and it authorises nothing — file-service re-derives its own
	// verdict from its own store on every preview request. Omitted for a message
	// with no link-safety opinion, which is almost all of them.
	LinkSafetyState string         `json:"link_safety_state,omitempty"`
	DeletedAt       *time.Time     `json:"deleted_at,omitempty"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	EditedAt        *time.Time     `json:"edited_at,omitempty"`
	EditCount       int            `json:"edit_count"`
	IsEdited        bool           `json:"is_edited"`
	Reactions       []reactionJSON `json:"reactions"`
	IsFavorited     bool           `json:"is_favorited,omitempty"`
	IsForwarded     bool           `json:"is_forwarded"`
	Quoted          *quoteJSON     `json:"quoted,omitempty"`
	Reference       *referenceJSON `json:"reference,omitempty"`
	// Attachments is omitted entirely for a message that carries none, so every
	// existing text-only response is byte-for-byte what it was.
	Attachments []messageAttachmentJSON `json:"attachments,omitempty"`
}

// messageAttachmentJSON is the only shape of an attachment a message viewer
// ever sees (RF-32).
//
// It is metadata for drawing a row and nothing else. storage_object_key,
// wrapped_dek, kek_key_id, envelope/wrap versions, scanner detail and every
// internal path are absent by construction — they are not on domain.Message
// either, so there is no field here to forget to strip. status and
// preview_status are lifecycle values, not grants: file-service re-evaluates
// both on every content and preview request.
type messageAttachmentJSON struct {
	ID            string `json:"id"`
	Filename      string `json:"filename"`
	ContentType   string `json:"content_type"`
	Size          int64  `json:"size"`
	Status        string `json:"status"`
	PreviewStatus string `json:"preview_status"`
}

type quoteJSON struct {
	ID         string     `json:"id"`
	AuthorID   string     `json:"author_id"`
	Body       string     `json:"body,omitempty"`
	BodyFormat string     `json:"body_format"`
	IsRemoved  bool       `json:"is_removed,omitempty"`
	DeletedAt  *time.Time `json:"deleted_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	// Empty unless the quoted message's own links were condemned, in which case
	// Body above is already empty and this is what tells the client to say so
	// rather than draw a blank quote (issue #135, CQ-002).
	LinkSafetyState string `json:"link_safety_state,omitempty"`
}

type referenceJSON struct {
	Available         bool       `json:"available"`
	MessageID         string     `json:"message_id,omitempty"`
	TargetType        string     `json:"target_type,omitempty"`
	TargetID          string     `json:"target_id,omitempty"`
	TargetLabel       string     `json:"target_label,omitempty"`
	AuthorDisplayName string     `json:"author_display_name,omitempty"`
	Body              string     `json:"body,omitempty"`
	BodyFormat        string     `json:"body_format,omitempty"`
	CreatedAt         *time.Time `json:"created_at,omitempty"`
	UpdatedAt         *time.Time `json:"updated_at,omitempty"`
	LinkSafetyState   string     `json:"link_safety_state,omitempty"`
}

type reactionJSON struct {
	Emoji       string             `json:"emoji"`
	Count       int                `json:"count"`
	ReactedByMe bool               `json:"reacted_by_me"`
	Users       []reactionUserJSON `json:"users"`
}

// reactionUserJSON is the identity a reaction tooltip needs and nothing more
// (issue #496): who, and what to call them. The reader is already authorized to
// read this message, and so already sees names in it.
type reactionUserJSON struct {
	UserID      string `json:"user_id"`
	DisplayName string `json:"display_name"`
}

func reactionUsersJSON(users []domain.ReactionUser) []reactionUserJSON {
	out := make([]reactionUserJSON, len(users))
	for i, user := range users {
		out[i] = reactionUserJSON{UserID: user.UserID, DisplayName: user.DisplayName}
	}
	return out
}

// listMessagesResponseData is the data envelope for list endpoints.
type listMessagesResponseData struct {
	Messages   []messageJSON `json:"messages"`
	NextCursor string        `json:"next_cursor,omitempty"`
}

// linkSafetyStatusRequest asks what became of messages the caller is still
// showing as being checked (RF-21).
type linkSafetyStatusRequest struct {
	MessageIDs []string `json:"message_ids"`
}

// linkSafetyStatusJSON is one answer, and is deliberately almost empty.
//
// A state, and a reason only when there is one. No body, no URL, no canonical
// URL, no query, no scan identifier and nothing the provider said: the author is
// told that their message was refused, not what the scanner knows.
type linkSafetyStatusJSON struct {
	MessageID string `json:"message_id"`
	State     string `json:"state"`
	Reason    string `json:"reason,omitempty"`
}

type linkSafetyStatusResponseData struct {
	Statuses []linkSafetyStatusJSON `json:"statuses"`
}

type resolveMessageReferencesRequest struct {
	MessageIDs []string `json:"message_ids"`
}

type messageReferenceResolutionJSON struct {
	MessageID string        `json:"message_id"`
	Reference referenceJSON `json:"reference"`
}

type messageReferenceResolutionsData struct {
	References []messageReferenceResolutionJSON `json:"references"`
}

type messageSecuritySnapshotsRequest struct {
	MessageIDs []string `json:"message_ids"`
}

type quotedMessageSecuritySnapshotJSON struct {
	MessageID       string `json:"message_id"`
	Status          string `json:"status"`
	LinkSafetyState string `json:"link_safety_state"`
	UpdatedAt       string `json:"updated_at"`
}

type messageSecuritySnapshotJSON struct {
	MessageID       string                             `json:"message_id"`
	Available       bool                               `json:"available"`
	Status          string                             `json:"status,omitempty"`
	LinkSafetyState string                             `json:"link_safety_state,omitempty"`
	UpdatedAt       string                             `json:"updated_at,omitempty"`
	Quoted          *quotedMessageSecuritySnapshotJSON `json:"quoted,omitempty"`
}

type messageSecuritySnapshotsData struct {
	Snapshots []messageSecuritySnapshotJSON `json:"snapshots"`
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
// Only body_text, body_format, parent_message_id, referenced_message_id, and
// attachment_ids are accepted. Any unrecognised field causes a 400.
type createMessageRequest struct {
	BodyText            string `json:"body_text"`
	BodyFormat          string `json:"body_format"`
	ParentMessageID     string `json:"parent_message_id"`
	ReferencedMessageID string `json:"referenced_message_id"`
	// AttachmentIDs binds already-uploaded files to this message (RF-32).
	//
	// A list, even though the product rule is one attachment per message, so
	// raising that rule is a constant and not a wire change. The list is a set of
	// *candidate* references and nothing more: the ids are canonicalised and
	// bounded by the service, then re-validated against the database in the same
	// statement that inserts the message. Anything a client could claim about an
	// attachment — its workspace, its destination, its status, its uploader — is
	// re-read server-side and never taken from here.
	//
	// A non-array value fails to decode and answers 400, exactly like every other
	// malformed field.
	AttachmentIDs []string `json:"attachment_ids"`
}

type forwardMessageRequest struct {
	SourceMessageID string `json:"source_message_id"`
}

type editMessageRequest struct {
	Body       string `json:"body"`
	BodyFormat string `json:"body_format"`
}

type editHistoryJSON struct {
	ID           string    `json:"id"`
	MessageID    string    `json:"message_id"`
	Body         string    `json:"body"`
	BodyFormat   string    `json:"body_format"`
	EditorUserID string    `json:"editor_user_id"`
	VersionedAt  time.Time `json:"versioned_at"`
}

type updateEditWindowRequest struct {
	EditWindowSeconds json.RawMessage `json:"edit_window_seconds"`
}

// updateAntiSpamRequest is the RF-19 admin payload. The single field is decoded
// as RawMessage so "absent" is distinguishable from "null" and so a non-integer
// (string, decimal, object) is a validation failure here rather than a silent
// zero from encoding/json. Unknown fields are rejected by decodeStrictJSON, so
// nothing else in the body can reach the workspace row.
type updateAntiSpamRequest struct {
	MessageRateLimitPerMinute json.RawMessage `json:"message_rate_limit_per_minute"`
}

// updateUploadLimitRequest is the RF-32 admin payload, decoded exactly like the
// anti-spam one above and for the same reasons.
type updateUploadLimitRequest struct {
	MaxUploadBytes json.RawMessage `json:"max_upload_bytes"`
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

// resolveWorkspaceID returns the workspace this request operates on.
//
// When a middleware has already resolved the canonical workspace server-side —
// which AntiSpamGuard.Middleware does on every send route — that value is
// reused. Reusing it is not an optimisation only: it is what guarantees the
// workspace a message is rate-limited against and the workspace it is written
// to are the same one, with no second lookup that could answer differently.
func (h *MessageHandler) resolveWorkspaceID(ctx context.Context, w http.ResponseWriter) (string, bool) {
	if id := contextWorkspaceID(ctx); id != "" {
		return id, true
	}
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
	parsed, err := uuid.Parse(id)
	if !uuidPattern.MatchString(id) || err != nil || parsed == uuid.Nil {
		httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, paramName+" must be a valid UUID")
		return false
	}
	return true
}

func parseIdempotencyKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	values := r.Header.Values("Idempotency-Key")
	if len(values) == 0 {
		return "", true
	}
	if len(values) != 1 {
		httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, "invalid Idempotency-Key")
		return "", false
	}
	key := strings.TrimSpace(values[0])
	if !idempotencyKeyPattern.MatchString(key) {
		httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, "invalid Idempotency-Key")
		return "", false
	}
	return key, true
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
	var editedAt *time.Time
	if !m.EditedAt.IsZero() {
		editedAt = &m.EditedAt
	}
	j := messageJSON{
		ID:                m.ID,
		SenderID:          m.SenderID,
		SenderDisplayName: m.SenderDisplayName,
		SenderEmail:       m.SenderEmail,
		SenderAvatarURL:   m.SenderAvatarURL,
		Kind:              string(m.Kind),
		BodyFormat:        string(m.BodyFormat),
		Status:            string(m.Status),
		LinkSafetyState:   string(m.LinkSafety),
		CreatedAt:         m.CreatedAt,
		UpdatedAt:         m.UpdatedAt,
		EditedAt:          editedAt,
		EditCount:         m.EditCount,
		IsEdited:          m.EditCount > 0,
		Reactions:         make([]reactionJSON, len(m.Reactions)),
		IsFavorited:       m.IsFavorited,
		IsForwarded:       m.ForwardedFromMessageID != "",
	}
	for i, reaction := range m.Reactions {
		j.Reactions[i] = reactionJSON{
			Emoji: reaction.Emoji, Count: reaction.Count, ReactedByMe: reaction.ReactedByMe,
			Users: reactionUsersJSON(reaction.Users),
		}
	}
	if m.Status == domain.MessageStatusDeleted || !m.DeletedAt.IsZero() {
		j.IsRemoved = true
		j.Status = string(domain.MessageStatusDeleted)
		if !m.DeletedAt.IsZero() {
			deletedAt := m.DeletedAt
			j.DeletedAt = &deletedAt
		}
	} else {
		j.BodyText = m.BodyText
		j.Quoted = mapQuoteJSON(m.Quoted)
		j.Reference = mapReferenceJSON(m)
		// Withheld for a removed message, like the body: the placeholder is the
		// whole of what a deleted message says.
		j.Attachments = mapAttachmentsJSON(m.Attachments)
	}
	return j
}

func mapAttachmentsJSON(attachments []domain.MessageAttachment) []messageAttachmentJSON {
	if len(attachments) == 0 {
		return nil
	}
	out := make([]messageAttachmentJSON, len(attachments))
	for i, attachment := range attachments {
		out[i] = messageAttachmentJSON{
			ID: attachment.ID, Filename: attachment.Filename,
			ContentType: attachment.ContentType, Size: attachment.SizeBytes,
			Status: attachment.Status, PreviewStatus: attachment.PreviewStatus,
		}
	}
	return out
}

func mapReferenceJSON(m domain.Message) *referenceJSON {
	if m.Reference == nil && m.ReferencedMessageID == "" {
		return nil
	}
	if m.Reference == nil || !m.Reference.Available {
		return &referenceJSON{Available: false}
	}
	ref := mapDomainReferenceJSON(*m.Reference)
	return &ref
}

func mapDomainReferenceJSON(ref domain.MessageReference) referenceJSON {
	if !ref.Available {
		return referenceJSON{Available: false}
	}
	createdAt, updatedAt := ref.CreatedAt, ref.UpdatedAt
	return referenceJSON{
		Available: true, MessageID: ref.MessageID,
		TargetType: ref.TargetType, TargetID: ref.TargetID,
		TargetLabel: ref.TargetLabel, AuthorDisplayName: ref.AuthorDisplayName,
		Body: ref.BodyText, BodyFormat: string(ref.BodyFormat), CreatedAt: &createdAt, UpdatedAt: &updatedAt,
		LinkSafetyState: string(ref.LinkSafety),
	}
}

func mapQuoteJSON(q *domain.QuotedMessage) *quoteJSON {
	if q == nil {
		return nil
	}
	var deletedAt *time.Time
	if !q.DeletedAt.IsZero() {
		t := q.DeletedAt
		deletedAt = &t
	}
	j := &quoteJSON{
		ID:         q.ID,
		AuthorID:   q.AuthorID,
		BodyFormat: string(q.BodyFormat),
		IsRemoved:  q.Status == domain.MessageStatusDeleted || deletedAt != nil,
		DeletedAt:  deletedAt,
		CreatedAt:  q.CreatedAt,
		UpdatedAt:  q.UpdatedAt,

		LinkSafetyState: string(q.LinkSafety),
	}
	if !j.IsRemoved {
		j.Body = q.BodyText
	}
	return j
}

type allowedReactionEmojisResponseData struct {
	// Emojis is the quick-reaction row, unchanged in name and meaning from
	// before issue #496 so a client built against the old contract keeps working.
	Emojis []string `json:"emojis"`
	// Version names the Unicode emoji catalog this server validates against.
	// The full catalog is not shipped here: it is a fixed, versioned Unicode
	// projection that the client already carries, so repeating thousands of
	// sequences on every chat open would be payload for nothing.
	Version string `json:"version"`
}

// ListAllowedReactionEmojis returns the quick-reaction row and the catalog
// version. Authentication and active-session checks are applied by NewRouter.
//
// The answer changes only when the deployment adopts a new Unicode version, so
// it carries a validator derived from exactly that: a client that already holds
// this version is told so instead of being sent the body again.
func (h *MessageHandler) ListAllowedReactionEmojis(w http.ResponseWriter, r *http.Request) {
	version := service.EmojiCatalogVersion()
	etag := `"emoji-catalog-` + version + `"`
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "private, max-age=300")
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, allowedReactionEmojisResponseData{
		Emojis:  service.QuickReactionEmojis(),
		Version: version,
	})
}

// decodeCreateRequest reads and decodes the request body into a createMessageRequest.
// Rejects unknown fields. Returns false and writes 400 on any parse error.
func decodeCreateRequest(w http.ResponseWriter, r *http.Request) (createMessageRequest, bool) {
	var req createMessageRequest
	return req, decodeStrictJSON(w, r, &req)
}

func decodeStrictJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, "invalid request body")
		return false
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, "invalid request body")
		return false
	}
	return true
}

// EditMessage handles PATCH /api/chat/messages/{messageID}.
func (h *MessageHandler) EditMessage(w http.ResponseWriter, r *http.Request) {
	if !h.checkDeps(w) {
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
	if h.editLimiter == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "message editing not available")
		return
	}
	allowed, err := h.editLimiter.AllowAction(r.Context(), userID, "edit_message")
	if err != nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "message editing not available")
		return
	}
	if !allowed {
		w.Header().Set("Retry-After", "60")
		httputil.WriteError(w, http.StatusTooManyRequests, "rate_limited", "too many requests")
		return
	}
	var req editMessageRequest
	if !decodeStrictJSON(w, r, &req) {
		return
	}
	workspaceID, ok := h.resolveWorkspaceID(r.Context(), w)
	if !ok {
		return
	}
	message, err := h.messages.EditMessage(r.Context(), service.EditMessageInput{
		WorkspaceID: workspaceID, MessageID: messageID, EditorID: userID,
		Body: req.Body, BodyFormat: domain.MessageBodyFormat(req.BodyFormat),
	})
	if err != nil {
		mapServiceError(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, mapToMessageJSON(message))
}

// DeleteMessage handles DELETE /api/chat/messages/{messageID}.
func (h *MessageHandler) DeleteMessage(w http.ResponseWriter, r *http.Request) {
	if !h.checkDeps(w) {
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
	workspaceID, ok := h.resolveWorkspaceID(r.Context(), w)
	if !ok {
		return
	}
	message, err := h.messages.DeleteMessage(r.Context(), service.DeleteMessageInput{
		WorkspaceID: workspaceID, MessageID: messageID, RequesterID: userID,
	})
	if err != nil {
		mapServiceError(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, mapToMessageJSON(message))
}

// GetMessageEditHistory handles GET /api/chat/messages/{messageID}/history.
func (h *MessageHandler) GetMessageEditHistory(w http.ResponseWriter, r *http.Request) {
	if !h.checkDeps(w) {
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
	offset := 0
	if raw := r.URL.Query().Get("offset"); raw != "" {
		var err error
		offset, err = strconv.Atoi(raw)
		if err != nil || offset < 0 || offset > 10_000 {
			httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, "invalid offset")
			return
		}
	}
	workspaceID, ok := h.resolveWorkspaceID(r.Context(), w)
	if !ok {
		return
	}
	history, err := h.messages.GetMessageEditHistory(r.Context(), service.GetMessageEditHistoryInput{
		WorkspaceID: workspaceID, MessageID: messageID, CallerID: userID,
		Limit: parseLimitParam(r), Offset: offset,
	})
	if err != nil {
		mapServiceError(w, err)
		return
	}
	out := make([]editHistoryJSON, len(history))
	for i, version := range history {
		out[i] = editHistoryJSON{
			ID: version.ID, MessageID: version.MessageID, Body: version.Body,
			BodyFormat: string(version.BodyFormat), EditorUserID: version.EditorUserID,
			VersionedAt: version.VersionedAt,
		}
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"history": out, "offset": offset})
}

// UpdateWorkspaceEditWindow handles PATCH /api/v1/workspaces/{workspaceID}/settings.
func (h *MessageHandler) UpdateWorkspaceEditWindow(w http.ResponseWriter, r *http.Request) {
	if h.settings == nil || h.settingsAuth == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "workspace settings not available")
		return
	}
	workspaceID := r.PathValue("workspaceID")
	if !validateTargetID(w, workspaceID, "workspace_id") {
		return
	}
	userID := GetContextUserID(r)
	if userID == "" {
		httputil.WriteError(w, http.StatusUnauthorized, httputil.ErrCodeUnauthorized, "unauthorized")
		return
	}
	var req updateEditWindowRequest
	if !decodeStrictJSON(w, r, &req) {
		return
	}
	if req.EditWindowSeconds == nil {
		httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, "invalid request body")
		return
	}
	var seconds *int
	if string(req.EditWindowSeconds) != "null" {
		var value int
		if err := json.Unmarshal(req.EditWindowSeconds, &value); err != nil || value < 30 || value > 86400 {
			httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, "invalid edit_window_seconds")
			return
		}
		seconds = &value
	}
	allowed, err := h.settingsAuth.CanManageWorkspace(r.Context(), workspaceID, userID)
	if err != nil {
		mapServiceError(w, err)
		return
	}
	if !allowed {
		httputil.WriteError(w, http.StatusForbidden, httputil.ErrCodeForbidden, "forbidden")
		return
	}
	workspace, err := h.settings.UpdateEditWindow(r.Context(), workspaceID, userID, seconds)
	if err != nil {
		mapServiceError(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"workspace_id": workspace.ID, "edit_window_seconds": workspace.EditWindowSeconds,
	})
}

// ── RF-19 anti-spam policy (issue #419) ──────────────────────────────────────

// antiSpamJSON is the response body for both the GET and the PATCH. The bounds
// are returned alongside the value so the admin UI renders and validates
// against the server's numbers instead of restating them as its own truth.
type antiSpamJSON struct {
	WorkspaceID               string `json:"workspace_id"`
	MessageRateLimitPerMinute int    `json:"message_rate_limit_per_minute"`
	Min                       int    `json:"min"`
	Max                       int    `json:"max"`
}

func antiSpamResponse(workspace domain.Workspace) antiSpamJSON {
	return antiSpamJSON{
		WorkspaceID:               workspace.ID,
		MessageRateLimitPerMinute: domain.EffectiveMessageRateLimitPerMinute(workspace.MessageRateLimitPerMinute),
		Min:                       domain.MinMessageRateLimitPerMinute,
		Max:                       domain.MaxMessageRateLimitPerMinute,
	}
}

// authorizeWorkspaceAdmin resolves the caller and confirms they administer the
// workspace named in the path. Shared by the anti-spam GET and PATCH so both
// verbs are gated identically — a readable-but-not-writable split here would
// leak a workspace's policy to any authenticated member of any workspace.
//
// A caller who is not an admin, and a caller who administers some *other*
// workspace, both get the same 403: the response never distinguishes "you lack
// the role" from "that workspace is not yours", so the endpoint cannot be used
// to probe which workspace IDs exist.
func (h *MessageHandler) authorizeWorkspaceAdmin(w http.ResponseWriter, r *http.Request) (workspaceID, userID string, ok bool) {
	if h.settings == nil || h.settingsAuth == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "workspace settings not available")
		return "", "", false
	}
	workspaceID = r.PathValue("workspaceID")
	if !validateTargetID(w, workspaceID, "workspace_id") {
		return "", "", false
	}
	userID = GetContextUserID(r)
	if userID == "" {
		httputil.WriteError(w, http.StatusUnauthorized, httputil.ErrCodeUnauthorized, "unauthorized")
		return "", "", false
	}
	allowed, err := h.settingsAuth.CanManageWorkspace(r.Context(), workspaceID, userID)
	if err != nil {
		mapServiceError(w, err)
		return "", "", false
	}
	if !allowed {
		httputil.WriteError(w, http.StatusForbidden, httputil.ErrCodeForbidden, "forbidden")
		return "", "", false
	}
	return workspaceID, userID, true
}

// GetWorkspaceAntiSpam handles GET /api/chat/workspaces/{workspaceID}/anti-spam.
func (h *MessageHandler) GetWorkspaceAntiSpam(w http.ResponseWriter, r *http.Request) {
	workspaceID, _, ok := h.authorizeWorkspaceAdmin(w, r)
	if !ok {
		return
	}
	workspace, err := h.settings.GetWorkspaceByID(r.Context(), workspaceID)
	if err != nil {
		mapServiceError(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, antiSpamResponse(workspace))
}

// UpdateWorkspaceAntiSpam handles PATCH /api/chat/workspaces/{workspaceID}/anti-spam.
//
// Validation runs before authorization is spent on a write, but after the
// membership check, so an unprivileged caller learns nothing about which values
// the field accepts. The store applies the same RBAC predicate atomically with
// the UPDATE, so this handler's check is defence in depth rather than the only
// gate.
func (h *MessageHandler) UpdateWorkspaceAntiSpam(w http.ResponseWriter, r *http.Request) {
	workspaceID, userID, ok := h.authorizeWorkspaceAdmin(w, r)
	if !ok {
		return
	}
	var req updateAntiSpamRequest
	if !decodeStrictJSON(w, r, &req) {
		return
	}
	if req.MessageRateLimitPerMinute == nil {
		httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, "invalid request body")
		return
	}
	var perMinute int
	// json.Unmarshal into int rejects strings, decimals, booleans, null and
	// values that overflow int64; the bounds check rejects 0, negatives and
	// anything outside the policy range. The database CHECK repeats both.
	if err := json.Unmarshal(req.MessageRateLimitPerMinute, &perMinute); err != nil ||
		!domain.ValidMessageRateLimitPerMinute(perMinute) {
		httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, "invalid message_rate_limit_per_minute")
		return
	}
	workspace, err := h.settings.UpdateMessageRateLimit(r.Context(), workspaceID, userID, perMinute)
	if err != nil {
		mapServiceError(w, err)
		return
	}
	// Drop this instance's cached policy so the next message is counted against
	// the new limit without waiting out the TTL.
	h.antiSpam.Invalidate(workspace.ID)
	httputil.WriteJSON(w, http.StatusOK, antiSpamResponse(workspace))
}

// ── RF-32 attachment size policy (issue #458) ────────────────────────────────

// uploadLimitJSON is the response body for both verbs. Like the anti-spam
// payload it returns the bounds alongside the value so the admin UI renders and
// validates against the server's numbers instead of restating them.
//
// The unit is bytes. Formatting into MB happens in the UI, at the presentation
// edge, so no rounding ever reaches a comparison.
type uploadLimitJSON struct {
	WorkspaceID    string `json:"workspace_id"`
	MaxUploadBytes int64  `json:"max_upload_bytes"`
	Min            int64  `json:"min"`
	Max            int64  `json:"max"`
}

func uploadLimitResponse(workspace domain.Workspace) uploadLimitJSON {
	return uploadLimitJSON{
		WorkspaceID:    workspace.ID,
		MaxUploadBytes: domain.EffectiveMaxUploadBytes(workspace.MaxUploadBytes),
		Min:            domain.MinMaxUploadBytes,
		Max:            domain.MaxMaxUploadBytes,
	}
}

// GetWorkspaceUploadLimit handles GET /api/chat/workspaces/{workspaceID}/upload-limit.
//
// Administrative read. Ordinary members do not need it: the effective limit
// they must respect is published on GET /api/chat/sidebar, which they already
// load, so this endpoint stays behind the same gate as the write.
func (h *MessageHandler) GetWorkspaceUploadLimit(w http.ResponseWriter, r *http.Request) {
	workspaceID, _, ok := h.authorizeWorkspaceAdmin(w, r)
	if !ok {
		return
	}
	workspace, err := h.settings.GetWorkspaceByID(r.Context(), workspaceID)
	if err != nil {
		mapServiceError(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, uploadLimitResponse(workspace))
}

// UpdateWorkspaceUploadLimit handles PATCH /api/chat/workspaces/{workspaceID}/upload-limit.
//
// Validation runs after the membership check, so an unprivileged caller learns
// nothing about which values the field accepts. The store applies the same RBAC
// predicate atomically with the UPDATE, so this handler's check is defence in
// depth rather than the only gate.
//
// No cache is invalidated here because none exists: file-service reads the
// policy from the destination's own row in the query that authorises the upload,
// once per request. An upload already in flight finishes under the policy it
// started with; the next one sees the new value.
func (h *MessageHandler) UpdateWorkspaceUploadLimit(w http.ResponseWriter, r *http.Request) {
	workspaceID, userID, ok := h.authorizeWorkspaceAdmin(w, r)
	if !ok {
		return
	}
	var req updateUploadLimitRequest
	if !decodeStrictJSON(w, r, &req) {
		return
	}
	if req.MaxUploadBytes == nil {
		httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, "invalid request body")
		return
	}
	var maxBytes int64
	// json.Unmarshal into int64 rejects strings, decimals, booleans, null and
	// values that overflow int64; domain.ValidMaxUploadBytes rejects 0,
	// negatives, anything outside the policy range and anything that is not a
	// whole number of MiB. The database CHECK repeats both halves. An invalid
	// value is refused, never clamped or rounded into range.
	if err := json.Unmarshal(req.MaxUploadBytes, &maxBytes); err != nil ||
		!domain.ValidMaxUploadBytes(maxBytes) {
		httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest,
			"max_upload_bytes must be a whole number of MiB within the allowed range")
		return
	}
	workspace, err := h.settings.UpdateMaxUploadBytes(r.Context(), workspaceID, userID, maxBytes)
	if err != nil {
		mapServiceError(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, uploadLimitResponse(workspace))
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

	// Optional, and the same header forwarding already uses: a retried send
	// returns the original message instead of creating a second one. It matters
	// more since RF-21, because a message withheld for a link scan is delivered
	// to nobody, so a client with a dropped response has every reason to retry.
	idempotencyKey, ok := parseIdempotencyKey(w, r)
	if !ok {
		return
	}

	msg, err := h.messages.CreateChannelMessage(r.Context(), service.CreateChannelMessageInput{
		WorkspaceID:         wsID,
		ChannelID:           channelID,
		SenderID:            userID, // always from auth context — never from body
		BodyText:            req.BodyText,
		BodyFormat:          domain.MessageBodyFormat(req.BodyFormat),
		IdempotencyKey:      idempotencyKey,
		ParentMessageID:     req.ParentMessageID,
		ReferencedMessageID: req.ReferencedMessageID,
		AttachmentIDs:       req.AttachmentIDs,
	})
	if err != nil {
		mapServiceError(w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusCreated, mapToMessageJSON(msg))
}

// ForwardChannelMessage handles POST /api/chat/channels/{channelID}/messages/forward.
// The client supplies only the source ID; the server snapshots content and provenance.
func (h *MessageHandler) ForwardChannelMessage(w http.ResponseWriter, r *http.Request) {
	if !h.checkDeps(w) {
		return
	}
	destinationChannelID := r.PathValue("channelID")
	if !validateTargetID(w, destinationChannelID, "channel_id") {
		return
	}
	userID := GetContextUserID(r)
	if userID == "" {
		httputil.WriteError(w, http.StatusUnauthorized, httputil.ErrCodeUnauthorized, "unauthorized")
		return
	}
	var req forwardMessageRequest
	if !decodeStrictJSON(w, r, &req) {
		return
	}
	if !validateTargetID(w, req.SourceMessageID, "source_message_id") {
		return
	}
	idempotencyKey, ok := parseIdempotencyKey(w, r)
	if !ok {
		return
	}
	workspaceID, ok := h.resolveWorkspaceID(r.Context(), w)
	if !ok {
		return
	}
	result, err := h.messages.ForwardChannelMessage(r.Context(), service.ForwardChannelMessageInput{
		WorkspaceID: workspaceID, DestinationChannelID: destinationChannelID,
		ActorID: userID, SourceMessageID: req.SourceMessageID,
		IdempotencyKey: idempotencyKey,
	})
	if err != nil {
		mapServiceError(w, err)
		return
	}
	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
	}
	httputil.WriteJSON(w, status, mapToMessageJSON(result.Message))
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

	// Optional, and the same header forwarding already uses: a retried send
	// returns the original message instead of creating a second one. It matters
	// more since RF-21, because a message withheld for a link scan is delivered
	// to nobody, so a client with a dropped response has every reason to retry.
	idempotencyKey, ok := parseIdempotencyKey(w, r)
	if !ok {
		return
	}

	msg, err := h.messages.CreateDMMessage(r.Context(), service.CreateDMMessageInput{
		WorkspaceID:         wsID,
		ConversationID:      convID,
		SenderID:            userID,
		BodyText:            req.BodyText,
		BodyFormat:          domain.MessageBodyFormat(req.BodyFormat),
		IdempotencyKey:      idempotencyKey,
		ParentMessageID:     req.ParentMessageID,
		ReferencedMessageID: req.ReferencedMessageID,
		AttachmentIDs:       req.AttachmentIDs,
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

// ResolveChannelMessageReferences re-authorizes RF-09 references for a bounded
// set of destination messages in one server-side batch.
func (h *MessageHandler) ResolveChannelMessageReferences(w http.ResponseWriter, r *http.Request) {
	h.resolveMessageReferences(w, r, r.PathValue("channelID"), "")
}

// ResolveDMMessageReferences is the DM equivalent of
// ResolveChannelMessageReferences.
func (h *MessageHandler) ResolveDMMessageReferences(w http.ResponseWriter, r *http.Request) {
	h.resolveMessageReferences(w, r, "", r.PathValue("conversationID"))
}

func (h *MessageHandler) resolveMessageReferences(w http.ResponseWriter, r *http.Request, channelID, conversationID string) {
	if !h.checkDeps(w) {
		return
	}
	targetID, targetParam := channelID, "channel_id"
	if targetID == "" {
		targetID, targetParam = conversationID, "conversation_id"
	}
	if !validateTargetID(w, targetID, targetParam) {
		return
	}
	userID := GetContextUserID(r)
	if userID == "" {
		httputil.WriteError(w, http.StatusUnauthorized, httputil.ErrCodeUnauthorized, "unauthorized")
		return
	}
	var req resolveMessageReferencesRequest
	if !decodeStrictJSON(w, r, &req) {
		return
	}
	workspaceID, ok := h.resolveWorkspaceID(r.Context(), w)
	if !ok {
		return
	}
	resolved, err := h.messages.ResolveMessageReferenceBatch(r.Context(), service.ResolveMessageReferencesInput{
		WorkspaceID: workspaceID, ChannelID: channelID, DMConversationID: conversationID,
		CallerID: userID, MessageIDs: req.MessageIDs,
	})
	if err != nil {
		mapServiceError(w, err)
		return
	}
	response := messageReferenceResolutionsData{References: make([]messageReferenceResolutionJSON, 0, len(resolved))}
	for _, resolution := range resolved {
		response.References = append(response.References, messageReferenceResolutionJSON{
			MessageID: resolution.MessageID,
			Reference: mapDomainReferenceJSON(resolution.Reference),
		})
	}
	httputil.WriteJSON(w, http.StatusOK, response)
}

func (h *MessageHandler) GetChannelMessageSecuritySnapshots(w http.ResponseWriter, r *http.Request) {
	h.getMessageSecuritySnapshots(w, r, r.PathValue("channelID"), "")
}

func (h *MessageHandler) GetDMMessageSecuritySnapshots(w http.ResponseWriter, r *http.Request) {
	h.getMessageSecuritySnapshots(w, r, "", r.PathValue("conversationID"))
}

func (h *MessageHandler) getMessageSecuritySnapshots(w http.ResponseWriter, r *http.Request, channelID, conversationID string) {
	if !h.checkDeps(w) {
		return
	}
	targetID, targetParam := channelID, "channel_id"
	if targetID == "" {
		targetID, targetParam = conversationID, "conversation_id"
	}
	if !validateTargetID(w, targetID, targetParam) {
		return
	}
	userID := GetContextUserID(r)
	if userID == "" {
		httputil.WriteError(w, http.StatusUnauthorized, httputil.ErrCodeUnauthorized, "unauthorized")
		return
	}
	var req messageSecuritySnapshotsRequest
	if !decodeStrictJSON(w, r, &req) {
		return
	}
	workspaceID, ok := h.resolveWorkspaceID(r.Context(), w)
	if !ok {
		return
	}
	snapshots, err := h.messages.MessageSecuritySnapshots(r.Context(), service.MessageSecuritySnapshotsInput{
		WorkspaceID: workspaceID, ChannelID: channelID, DMConversationID: conversationID,
		CallerID: userID, MessageIDs: req.MessageIDs,
	})
	if err != nil {
		mapServiceError(w, err)
		return
	}
	response := messageSecuritySnapshotsData{Snapshots: make([]messageSecuritySnapshotJSON, 0, len(snapshots))}
	for _, snapshot := range snapshots {
		item := messageSecuritySnapshotJSON{
			MessageID: snapshot.MessageID, Available: snapshot.Available,
			Status:          string(snapshot.Status),
			LinkSafetyState: string(snapshot.LinkSafetyState),
		}
		if snapshot.Available {
			item.UpdatedAt = snapshot.UpdatedAt.UTC().Format(time.RFC3339Nano)
		}
		if snapshot.Quoted != nil {
			item.Quoted = &quotedMessageSecuritySnapshotJSON{
				MessageID: snapshot.Quoted.MessageID, Status: string(snapshot.Quoted.Status),
				LinkSafetyState: string(snapshot.Quoted.LinkSafetyState),
				UpdatedAt:       snapshot.Quoted.UpdatedAt.UTC().Format(time.RFC3339Nano),
			}
		}
		response.Snapshots = append(response.Snapshots, item)
	}
	httputil.WriteJSON(w, http.StatusOK, response)
}

// GetMessageLinkSafetyStatus answers what became of the caller's own withheld
// messages (RF-21).
//
// The recovery path for a verdict whose realtime announcement was missed. A
// message.blocked is published once, to its author, and an author who was
// offline at that moment gets nothing — their client would otherwise show
// "checking links…" for a message that no longer exists. On reconnect it asks
// here instead.
//
// Ids the service will not answer about are omitted from the response rather
// than reported as forbidden, so this cannot be used to learn whether a message
// id exists. Nothing is logged about which ids were asked for.
func (h *MessageHandler) GetMessageLinkSafetyStatus(w http.ResponseWriter, r *http.Request) {
	if !h.checkDeps(w) {
		return
	}
	userID := GetContextUserID(r)
	if userID == "" {
		httputil.WriteError(w, http.StatusUnauthorized, httputil.ErrCodeUnauthorized, "unauthorized")
		return
	}
	var req linkSafetyStatusRequest
	if !decodeStrictJSON(w, r, &req) {
		return
	}
	workspaceID, ok := h.resolveWorkspaceID(r.Context(), w)
	if !ok {
		return
	}
	states, err := h.messages.MessageLinkSafetyStates(r.Context(), service.LinkSafetyStatusInput{
		WorkspaceID: workspaceID, SenderID: userID, MessageIDs: req.MessageIDs,
	})
	if err != nil {
		mapServiceError(w, err)
		return
	}
	response := linkSafetyStatusResponseData{Statuses: make([]linkSafetyStatusJSON, 0, len(states))}
	for _, state := range states {
		status := linkSafetyStatusJSON{MessageID: state.MessageID, State: string(state.State), Reason: state.Reason}
		response.Statuses = append(response.Statuses, status)
	}
	httputil.WriteJSON(w, http.StatusOK, response)
}

// linkSafetyReconcileResponse is what "Verificar novamente" answers with.
//
// Three fields, and the omissions are the contract. There is no URL, no scan uuid,
// no provider status, no error text and no count of links examined: a reader
// asking whether a warning can go away needs the answer, and every one of those
// would be either an account-internal identifier or a probe. In particular
// nothing here reports whether a *new scan* was started, because none ever is.
type linkSafetyReconcileResponse struct {
	// LinkSafetyState is the message's authoritative state after the attempt —
	// the same closed set the message payload carries. Answered even when nothing
	// changed, so a client never has to infer.
	LinkSafetyState string `json:"link_safety_state"`
	// UpdatedAt orders this readback against websocket create/edit/correction
	// events without trusting the client's clock.
	UpdatedAt time.Time `json:"updated_at"`
	// RetryAfterSeconds is how long to wait before asking again. It is a hint for
	// disabling the button, never a promise that waiting produces an answer.
	RetryAfterSeconds int `json:"retry_after_seconds"`
}

// ReconcileMessageLinkSafety takes a second look at one message's unverified
// links (issue #135).
//
// # What it does not do
//
// It does not submit a scan. Cloudflare has no idempotency token, so a second POST
// is a second billed scan, and the production case this endpoint exists for is a
// provider refusal that says the hostname was scanned too recently — the one
// situation where another POST cannot help. What it does is search this account's
// own scan history for exactly this URL and, if it finds one, read that scan's
// full report. There is no "force scan" variant of this route and no parameter
// that could become one.
//
// # Why the body is empty
//
// The client sends a message id in the path and nothing else. It does not send a
// URL and it does not send a scan uuid: both are read server-side, from the
// associations recorded when the message was created. A client that could name a
// URL would have turned this into a Cloudflare proxy anyone with an account could
// use, paid for by this deployment.
//
// # Authorization and rate limiting
//
// Authentication and session validity are the router's; the workspace is resolved
// from the session. The service then applies the ordinary message-read
// authorization — active membership plus channel visibility or DM participation —
// and answers 404 for anything this caller may not read, so the endpoint cannot be
// used to discover message ids. Two rate limits stack: a per-user budget in the
// router, and a deployment-wide once-a-minute-per-URL cooldown in the database, so
// a channel full of people clicking one warning costs one provider search.
func (h *MessageHandler) ReconcileMessageLinkSafety(w http.ResponseWriter, r *http.Request) {
	if h.linkReconcile == nil || !h.linkReconcile.Ready() || h.reconcileLimiter == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, errCodeLinkCheckUnavailable,
			"link verification is not available")
		return
	}
	userID := GetContextUserID(r)
	if userID == "" {
		httputil.WriteError(w, http.StatusUnauthorized, httputil.ErrCodeUnauthorized, "unauthorized")
		return
	}
	// The deployment-wide per-user budget, applied before anything else costs
	// work. It is deliberately the shared Valkey counter rather than an in-process
	// one: this is the only user-triggered route that reaches a paid third party,
	// and a per-replica budget would multiply by the pod count.
	//
	// A limiter that cannot answer is a refusal, not a pass. Not knowing whether
	// there is allowance left is not permission to spend it.
	allowed, err := h.reconcileLimiter.AllowActionWithLimit(
		r.Context(), userID, linkReconcileAction,
		linkReconcileRateLimit, int(linkReconcileRateWindow.Seconds()))
	if err != nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, errCodeLinkCheckUnavailable,
			"link verification is not available")
		return
	}
	if !allowed {
		w.Header().Set("Retry-After", strconv.Itoa(int(linkReconcileRateWindow.Seconds())))
		httputil.WriteError(w, http.StatusTooManyRequests, "rate_limited", "too many requests")
		return
	}
	messageID := r.PathValue("messageID")
	if !validateTargetID(w, messageID, "messageID") {
		return
	}
	workspaceID, ok := h.resolveWorkspaceID(r.Context(), w)
	if !ok {
		return
	}
	result, err := h.linkReconcile.ReconcileMessage(r.Context(), service.ReconcileMessageInput{
		WorkspaceID: workspaceID, ViewerID: userID, MessageID: messageID,
	})
	if err != nil {
		mapServiceError(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, linkSafetyReconcileResponse{
		LinkSafetyState:   string(result.State),
		UpdatedAt:         result.UpdatedAt,
		RetryAfterSeconds: int(result.RetryAfter.Seconds()),
	})
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
	case errors.Is(err, domain.ErrInvalidMessageReference):
		// Non-enumerating: missing, deleted, and cross-target parents look identical.
		httputil.WriteError(w, http.StatusNotFound, httputil.ErrCodeNotFound, "not found")
	case errors.Is(err, domain.ErrForbidden):
		httputil.WriteError(w, http.StatusForbidden, httputil.ErrCodeForbidden, "forbidden")
	case errors.Is(err, domain.ErrEditForbidden):
		httputil.WriteError(w, http.StatusForbidden, httputil.ErrCodeForbidden, "message edit forbidden")
	case errors.Is(err, domain.ErrEditWindowExpired):
		httputil.WriteError(w, http.StatusConflict, httputil.ErrCodeConflict, "message edit window expired")
	case errors.Is(err, domain.ErrConflict):
		httputil.WriteError(w, http.StatusConflict, httputil.ErrCodeConflict, "conflict")
	case errors.Is(err, domain.ErrPinLimitReached):
		httputil.WriteError(w, http.StatusConflict, httputil.ErrCodeConflict, "pin limit reached")
	case errors.Is(err, domain.ErrMaliciousURL):
		// The code is the contract; the text is a fallback. Which link was
		// condemned and which category the provider reported are both withheld:
		// repeating them would turn the send endpoint into a free oracle for
		// checking whether a domain is already burned.
		httputil.WriteError(w, http.StatusForbidden, errCodeMaliciousURL,
			"this message contains a link blocked for security reasons")
	case errors.Is(err, domain.ErrLinkScanCapacity):
		// 429 and not 403, and its own code. The message was refused because this
		// deployment is not currently willing to buy more scans — a spent window
		// or a full queue — and that says nothing whatsoever about its links.
		// Reporting it as malicious_url would show a security warning for an
		// operational condition, and would teach an operator to read a spike in
		// refusals as an attack on their users.
		//
		// Which ceiling refused it is deliberately not in the response: it is an
		// internal capacity detail, and a client that could tell "my workspace is
		// spent" from "the deployment is full" could use the endpoint to probe
		// another tenant's activity.
		httputil.WriteError(w, http.StatusTooManyRequests, errCodeLinkCheckCapacity,
			"the links could not be checked right now, try again shortly")
	case errors.Is(err, domain.ErrURLCheckUnavailable):
		httputil.WriteError(w, http.StatusServiceUnavailable, errCodeLinkCheckUnavailable,
			"the link could not be checked for safety, try again")
	case errors.Is(err, domain.ErrURLCheckPending):
		// 409 and not 503: nothing is broken, the scan this request queued is
		// simply not finished. The already-published version of the message is
		// untouched, which is the point — an edit is never shown to anyone in a
		// state nobody has checked.
		httputil.WriteError(w, http.StatusConflict, errCodeLinkCheckPending,
			"the links in this edit are being checked for safety, try again shortly")
	default:
		httputil.WriteError(w, http.StatusInternalServerError, httputil.ErrCodeInternal, "internal error")
	}
}
