package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

type ownershipProvider interface {
	PrivateEnabled(context.Context, storage.OwnershipScope) (bool, error)
	Details(context.Context, storage.OwnershipScope) (storage.OwnershipDetails, error)
	Mutate(context.Context, storage.OwnershipMutation) (storage.OwnershipMutationResult, error)
}

// WithOwnership wires the compatibility-aware store without expanding the
// existing fake provider interfaces used by legacy endpoint tests.
func (h *DMHandler) WithOwnership(provider ownershipProvider) *DMHandler {
	if h == nil {
		return nil
	}
	next := *h
	next.ownership = provider
	return &next
}
func (h *ChannelHandler) WithOwnership(provider ownershipProvider) *ChannelHandler {
	if h == nil {
		return nil
	}
	next := *h
	next.ownership = provider
	return &next
}

func (h *DMHandler) Ownership(w http.ResponseWriter, r *http.Request) {
	id, actor, workspace, ok := h.beginGroupAdmin(w, r, false)
	if !ok {
		return
	}
	handleOwnership(w, r, h.ownership, storage.OwnershipScope{WorkspaceID: workspace, Kind: "dm", ConversationID: id, ActorID: actor}, h.broadcast)
}
func (h *ChannelHandler) Ownership(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("channelID")
	if !validateTargetID(w, id, "channel_id") {
		return
	}
	actor, ok := h.admitChannelWriter(w, r, "conversation_ownership", 30)
	if !ok {
		return
	}
	workspace, ok := h.resolveDefaultWorkspaceID(w, r)
	if !ok {
		return
	}
	handleOwnership(w, r, h.ownership, storage.OwnershipScope{WorkspaceID: workspace, Kind: "channel", ConversationID: id, ActorID: actor}, h.channelUpdates)
}

func handleOwnership(w http.ResponseWriter, r *http.Request, provider ownershipProvider, scope storage.OwnershipScope, broadcast channelUpdateBroadcaster) {
	if provider == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "unavailable", "ownership unavailable")
		return
	}
	if r.Method == http.MethodGet {
		details, err := provider.Details(r.Context(), scope)
		if err != nil {
			writeOwnershipError(w, err)
			return
		}
		httputil.WriteJSON(w, http.StatusOK, details)
		return
	}
	input, ok := decodeOwnershipMutation(w, r, scope)
	if !ok {
		return
	}
	result, err := provider.Mutate(r.Context(), input)
	if err != nil {
		writeOwnershipError(w, err)
		return
	}
	publishOwnershipResult(r.Context(), broadcast, scope, result)
	httputil.WriteJSON(w, http.StatusOK, result)
}

func decodeOwnershipMutation(w http.ResponseWriter, r *http.Request, scope storage.OwnershipScope) (storage.OwnershipMutation, bool) {
	if r.Method == http.MethodPost && r.PathValue("operation") != "transfer" && r.PathValue("operation") != "transfer-and-leave" {
		httputil.WriteError(w, http.StatusNotFound, httputil.ErrCodeNotFound, "operation unavailable")
		return storage.OwnershipMutation{}, false
	}
	input := storage.OwnershipMutation{Scope: scope, Operation: r.PathValue("operation"), IdempotencyKey: r.Header.Get("Idempotency-Key")}
	if r.Method == http.MethodPatch {
		input.Operation = "role"
		input.TargetUserID = r.PathValue("userID")
		var body struct {
			Role domain.ConversationRole `json:"role"`
		}
		if !decodeStrictJSON(w, r, &body) {
			return input, false
		}
		input.Role = body.Role
	} else {
		var body struct {
			NewOwnerUserID string                  `json:"new_owner_user_id"`
			ActorNewRole   domain.ConversationRole `json:"actor_new_role"`
		}
		if !decodeStrictJSON(w, r, &body) {
			return input, false
		}
		input.TargetUserID, input.Role = body.NewOwnerUserID, body.ActorNewRole
	}
	if !validateTargetID(w, input.TargetUserID, "user_id") {
		return input, false
	}
	return validateOwnershipHTTPMutation(w, input)
}

func validateOwnershipHTTPMutation(w http.ResponseWriter, input storage.OwnershipMutation) (storage.OwnershipMutation, bool) {
	if !input.Role.Valid() || input.Operation != "role" && input.Role == domain.ConversationOwner {
		httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, "invalid role")
		return input, false
	}
	if input.Operation != "role" && (len(input.IdempotencyKey) == 0 || len(input.IdempotencyKey) > 128) {
		httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, "Idempotency-Key required (1-128 characters)")
		return input, false
	}
	return input, true
}

func writeOwnershipError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrInvalidInput):
		httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, "invalid conversation metadata")
	case errors.Is(err, domain.ErrNotFound):
		httputil.WriteError(w, http.StatusNotFound, httputil.ErrCodeNotFound, "conversation unavailable")
	case errors.Is(err, domain.ErrForbidden):
		httputil.WriteError(w, http.StatusForbidden, httputil.ErrCodeForbidden, "ownership action forbidden")
	case errors.Is(err, domain.ErrOwnershipConflict):
		httputil.WriteError(w, http.StatusConflict, httputil.ErrCodeConflict, "ownership changed or no eligible successor")
	default:
		httputil.WriteError(w, http.StatusInternalServerError, httputil.ErrCodeInternal, "ownership operation failed")
	}
}

func (h *DMHandler) ownershipAware(operation string, legacy http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h.ownership == nil {
			legacy(w, r)
			return
		}
		workspace, ok := h.resolveWorkspaceID(r.Context(), w)
		if !ok {
			return
		}
		scope := storage.OwnershipScope{WorkspaceID: workspace, Kind: "dm", ConversationID: r.PathValue("conversationID"), ActorID: GetContextUserID(r)}
		if !validateTargetID(w, scope.ConversationID, "conversation_id") {
			return
		}
		enabled, err := h.ownership.PrivateEnabled(r.Context(), scope)
		if err != nil {
			writeOwnershipError(w, err)
			return
		}
		if !enabled {
			legacy(w, r)
			return
		}
		if !h.allowAction(w, r, scope.ActorID, groupAdminAction, groupAdminRateLimit) {
			return
		}
		handlePrivateLegacyMutation(w, r, h.ownership, scope, operation, h.broadcast)
	}
}
func (h *ChannelHandler) ownershipAware(operation string, legacy http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h.ownership == nil {
			legacy(w, r)
			return
		}
		workspace, ok := h.resolveDefaultWorkspaceID(w, r)
		if !ok {
			return
		}
		scope := storage.OwnershipScope{WorkspaceID: workspace, Kind: "channel", ConversationID: r.PathValue("channelID"), ActorID: GetContextUserID(r)}
		if !validateTargetID(w, scope.ConversationID, "channel_id") {
			return
		}
		enabled, err := h.ownership.PrivateEnabled(r.Context(), scope)
		if err != nil {
			writeOwnershipError(w, err)
			return
		}
		if !enabled {
			legacy(w, r)
			return
		}
		if _, ok := h.admitChannelWriter(w, r, "conversation_ownership", 30); !ok {
			return
		}
		handlePrivateLegacyMutation(w, r, h.ownership, scope, operation, h.channelUpdates)
	}
}

func handlePrivateLegacyMutation(w http.ResponseWriter, r *http.Request, provider ownershipProvider, scope storage.OwnershipScope, operation string, broadcast channelUpdateBroadcaster) {
	input := storage.OwnershipMutation{Scope: scope, Operation: operation, TargetUserID: r.PathValue("userID")}
	if operation == "remove" && !validateTargetID(w, input.TargetUserID, "user_id") {
		return
	}
	if operation == "rename" {
		var body struct {
			Title       string `json:"title"`
			DisplayName string `json:"display_name"`
		}
		if !decodeStrictJSON(w, r, &body) {
			return
		}
		input.Name = body.DisplayName
		if scope.Kind == "dm" {
			input.Name = body.Title
		}
	}
	result, err := provider.Mutate(r.Context(), input)
	if err != nil {
		writeOwnershipError(w, err)
		return
	}
	publishOwnershipResult(r.Context(), broadcast, scope, result)
	if operation == "rename" {
		if scope.Kind == "dm" {
			httputil.WriteJSON(w, http.StatusOK, map[string]string{"id": scope.ConversationID, "title": strings.TrimSpace(input.Name)})
		} else {
			httputil.WriteJSON(w, http.StatusOK, map[string]string{"id": scope.ConversationID, "display_name": strings.TrimSpace(input.Name)})
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func publishOwnershipResult(ctx context.Context, broadcast channelUpdateBroadcaster, scope storage.OwnershipScope, result storage.OwnershipMutationResult) {
	if result.Replayed || broadcast == nil {
		return
	}
	if result.EventID != "" {
		broadcast.PublishConversationEvent(ctx, scope.WorkspaceID, scope.Kind, scope.ConversationID, result.EventID)
	}
}

func ownershipProjection(ctx context.Context, provider ownershipProvider, scope storage.OwnershipScope) (*storage.OwnershipDetails, error) {
	if provider == nil {
		return nil, nil
	}
	enabled, err := provider.PrivateEnabled(ctx, scope)
	if err != nil || !enabled {
		return nil, err
	}
	details, err := provider.Details(ctx, scope)
	if err != nil {
		return nil, err
	}
	return &details, nil
}
