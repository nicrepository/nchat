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

// channelCategoryProvider is the ChannelCategoryService surface used by the
// handler (RF-17).
type channelCategoryProvider interface {
	ListGroupedChannels(ctx context.Context, workspaceID, callerID string) ([]service.ChannelCategoryGroup, error)
	CreateChannelCategory(ctx context.Context, input service.CreateChannelCategoryInput) (domain.ChannelCategory, error)
	RenameChannelCategory(ctx context.Context, input service.RenameChannelCategoryInput) (domain.ChannelCategory, error)
	ReorderChannelCategories(ctx context.Context, input service.ReorderChannelCategoriesInput) ([]domain.ChannelCategory, error)
	DeleteChannelCategory(ctx context.Context, workspaceID, categoryID, callerID string) error
}

// channelCategoryWriteRateLimit bounds category mutations per user per minute.
//
// Deliberately tight, for the same reason channel creation is: every accepted
// call changes workspace-wide, permanent structure that the whole sidebar renders
// from. All four write operations share one budget, so a caller cannot spend a
// separate allowance on renames and another on reorders.
const (
	channelCategoryWriteRateLimit        = 20
	channelCategoryRateLimitWindowSecond = 60
)

// Group kinds in the listing response. A reader distinguishes the virtual
// grouping from a persisted category by this field, not by inspecting the ID:
// the virtual group simply has no ID, rather than a fabricated UUID that could
// one day collide with a real category.
const (
	channelCategoryKindPersisted     = "category"
	channelCategoryKindUncategorized = "uncategorized"
)

type ChannelCategoryHandler struct {
	workspaces workspaceResolver
	categories channelCategoryProvider
	limiter    channelRateLimiter
}

func NewChannelCategoryHandler(workspaces workspaceResolver, categories channelCategoryProvider, limiter channelRateLimiter) *ChannelCategoryHandler {
	return &ChannelCategoryHandler{workspaces: workspaces, categories: categories, limiter: limiter}
}

// channelCategoryGroupJSON is one section of the grouped listing.
//
// ID and Position are omitted for the virtual group — it is not a row and has
// neither — and Kind states which of the two a section is, so a client never has
// to infer it. Channels reuses the sidebar's channel shape, from the same mapper,
// so the two endpoints cannot describe the same channel differently.
type channelCategoryGroupJSON struct {
	Kind     string               `json:"kind"`
	ID       string               `json:"id,omitempty"`
	Name     string               `json:"name"`
	Position *int                 `json:"position,omitempty"`
	Channels []sidebarChannelJSON `json:"channels"`
}

type listChannelCategoriesResponse struct {
	Groups []channelCategoryGroupJSON `json:"groups"`
}

// channelCategoryJSON is a persisted category on its own, returned by the write
// endpoints. created_at/updated_at are omitted: no client need has been shown for
// them and a narrower body is one less thing to keep compatible.
type channelCategoryJSON struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Position int    `json:"position"`
}

type channelCategoriesResponse struct {
	Categories []channelCategoryJSON `json:"categories"`
}

// The three request bodies are the whole accepted input. There is no
// workspace_id, no position, no role and no actor field anywhere: the workspace
// is resolved server-side, the caller comes from the authenticated context, and
// position is server-derived. The strict decoder answers 400 to a client that
// sends any of them, so a payload cannot claim a privilege.
type createChannelCategoryRequest struct {
	Name string `json:"name"`
}

type renameChannelCategoryRequest struct {
	Name string `json:"name"`
}

type reorderChannelCategoriesRequest struct {
	CategoryIDs []string `json:"category_ids"`
}

// List handles GET /api/chat/channel-categories.
//
// Any active workspace member may read. The response carries only channels the
// caller can already list, so this endpoint grants no access that
// GET /api/chat/sidebar does not; the authorization and the channel filtering
// both live below, in the service and in SQL.
func (h *ChannelCategoryHandler) List(w http.ResponseWriter, r *http.Request) {
	callerID, workspaceID, ok := h.begin(w, r)
	if !ok {
		return
	}
	groups, err := h.categories.ListGroupedChannels(r.Context(), workspaceID, callerID)
	if err != nil {
		writeChannelCategoryError(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, listChannelCategoriesResponse{Groups: mapChannelCategoryGroups(groups)})
}

// Create handles POST /api/chat/channel-categories.
func (h *ChannelCategoryHandler) Create(w http.ResponseWriter, r *http.Request) {
	callerID, workspaceID, ok := h.beginWrite(w, r)
	if !ok {
		return
	}
	var request createChannelCategoryRequest
	if !decodeStrictJSON(w, r, &request) {
		return
	}

	category, err := h.categories.CreateChannelCategory(r.Context(), service.CreateChannelCategoryInput{
		WorkspaceID: workspaceID,
		CallerID:    callerID,
		Name:        request.Name,
	})
	if err != nil {
		writeChannelCategoryError(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusCreated, mapChannelCategory(category))
}

// Rename handles PATCH /api/chat/channel-categories/{categoryID}.
func (h *ChannelCategoryHandler) Rename(w http.ResponseWriter, r *http.Request) {
	callerID, workspaceID, ok := h.beginWrite(w, r)
	if !ok {
		return
	}
	categoryID, ok := channelCategoryPathID(w, r)
	if !ok {
		return
	}
	var request renameChannelCategoryRequest
	if !decodeStrictJSON(w, r, &request) {
		return
	}

	category, err := h.categories.RenameChannelCategory(r.Context(), service.RenameChannelCategoryInput{
		WorkspaceID: workspaceID,
		CallerID:    callerID,
		CategoryID:  categoryID,
		Name:        request.Name,
	})
	if err != nil {
		writeChannelCategoryError(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, mapChannelCategory(category))
}

// Reorder handles PUT /api/chat/channel-categories/order.
//
// The body must list every category of the workspace exactly once. The length is
// bounded here before anything is sent to the database, and every ID is checked
// for UUID shape, so a malformed or oversized payload never reaches a query.
func (h *ChannelCategoryHandler) Reorder(w http.ResponseWriter, r *http.Request) {
	callerID, workspaceID, ok := h.beginWrite(w, r)
	if !ok {
		return
	}
	var request reorderChannelCategoriesRequest
	if !decodeStrictJSON(w, r, &request) {
		return
	}
	if len(request.CategoryIDs) == 0 || len(request.CategoryIDs) > domain.MaxCategoriesPerWorkspace {
		httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, "invalid category order")
		return
	}
	for _, id := range request.CategoryIDs {
		if !isValidUUID(id) {
			httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, "invalid category order")
			return
		}
	}

	categories, err := h.categories.ReorderChannelCategories(r.Context(), service.ReorderChannelCategoriesInput{
		WorkspaceID: workspaceID,
		CallerID:    callerID,
		OrderedIDs:  request.CategoryIDs,
	})
	if err != nil {
		writeChannelCategoryError(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, channelCategoriesResponse{Categories: mapChannelCategories(categories)})
}

// Delete handles DELETE /api/chat/channel-categories/{categoryID}.
//
// Deleting a category never deletes a channel: the channels that were in it
// become uncategorized and show up in the virtual "Geral" group on the next
// listing. 204 carries no body because there is nothing left to describe.
func (h *ChannelCategoryHandler) Delete(w http.ResponseWriter, r *http.Request) {
	callerID, workspaceID, ok := h.beginWrite(w, r)
	if !ok {
		return
	}
	categoryID, ok := channelCategoryPathID(w, r)
	if !ok {
		return
	}

	if err := h.categories.DeleteChannelCategory(r.Context(), workspaceID, categoryID, callerID); err != nil {
		writeChannelCategoryError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// begin performs the checks every category request shares: wiring, an
// authenticated caller, and the server-side workspace lookup. The workspace is
// never read from the request — no path segment, no query parameter and no body
// field carries one — so a client cannot aim an operation at another workspace.
func (h *ChannelCategoryHandler) begin(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	if h.workspaces == nil || h.categories == nil || h.limiter == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "channel categories not available")
		return "", "", false
	}
	callerID := GetContextUserID(r)
	if callerID == "" {
		writeUnauthorized(w)
		return "", "", false
	}
	workspace, err := h.workspaces.GetDefaultWorkspace(r.Context())
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			httputil.WriteError(w, http.StatusNotFound, httputil.ErrCodeNotFound, "workspace not found")
		} else {
			httputil.WriteError(w, http.StatusInternalServerError, httputil.ErrCodeInternal, "internal error")
		}
		return "", "", false
	}
	return callerID, workspace.ID, true
}

// beginWrite adds what only the mutations need: the shared rate-limit budget and
// a strict JSON content type. The limiter runs before the body is read, so an
// oversized or malformed payload cannot be used to bypass the budget.
func (h *ChannelCategoryHandler) beginWrite(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	if h.workspaces == nil || h.categories == nil || h.limiter == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "channel categories not available")
		return "", "", false
	}
	callerID := GetContextUserID(r)
	if callerID == "" {
		writeUnauthorized(w)
		return "", "", false
	}
	allowed, err := h.limiter.AllowActionWithLimit(
		r.Context(), callerID, "channel_category_write",
		channelCategoryWriteRateLimit, channelCategoryRateLimitWindowSecond,
	)
	if err != nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "channel categories not available")
		return "", "", false
	}
	if !allowed {
		w.Header().Set("Retry-After", strconv.Itoa(channelCategoryRateLimitWindowSecond))
		httputil.WriteError(w, http.StatusTooManyRequests, "rate_limited", "too many requests")
		return "", "", false
	}
	// DELETE carries no body, so requiring a JSON content type on it would reject
	// a correct request.
	if r.Method != http.MethodDelete && !requireJSONContentType(w, r) {
		return "", "", false
	}
	workspace, err := h.workspaces.GetDefaultWorkspace(r.Context())
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			httputil.WriteError(w, http.StatusNotFound, httputil.ErrCodeNotFound, "workspace not found")
		} else {
			httputil.WriteError(w, http.StatusInternalServerError, httputil.ErrCodeInternal, "internal error")
		}
		return "", "", false
	}
	return callerID, workspace.ID, true
}

func channelCategoryPathID(w http.ResponseWriter, r *http.Request) (string, bool) {
	categoryID := r.PathValue("categoryID")
	if !isValidUUID(categoryID) {
		httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, "invalid category_id")
		return "", false
	}
	return categoryID, true
}

func mapChannelCategoryGroups(groups []service.ChannelCategoryGroup) []channelCategoryGroupJSON {
	out := make([]channelCategoryGroupJSON, 0, len(groups))
	for _, group := range groups {
		item := channelCategoryGroupJSON{
			Kind:     channelCategoryKindUncategorized,
			Name:     domain.UncategorizedGroupName,
			Channels: mapCategoryChannels(group.Channels),
		}
		if group.Category != nil {
			position := group.Category.Position
			item.Kind = channelCategoryKindPersisted
			item.ID = group.Category.ID
			item.Name = group.Category.Name
			item.Position = &position
		}
		out = append(out, item)
	}
	return out
}

func mapCategoryChannels(channels []service.SidebarChannel) []sidebarChannelJSON {
	out := mapChannels(channels)
	for i := range out {
		out[i].UnreadCount = nil
	}
	return out
}

func mapChannelCategory(category domain.ChannelCategory) channelCategoryJSON {
	return channelCategoryJSON{ID: category.ID, Name: category.Name, Position: category.Position}
}

func mapChannelCategories(categories []domain.ChannelCategory) []channelCategoryJSON {
	out := make([]channelCategoryJSON, 0, len(categories))
	for _, category := range categories {
		out = append(out, mapChannelCategory(category))
	}
	return out
}

// writeChannelCategoryError keeps denials legible without describing state.
//
// A category that belongs to another workspace and one that does not exist both
// answer 404, and the authorization check runs first, so neither a non-manager nor
// a manager of a different workspace can use the status code to learn whether an
// ID exists. No branch carries a SQL message, a constraint name or the rejected
// value.
func writeChannelCategoryError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrInvalidChannelCategoryOrder):
		httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, "invalid category order")
	case errors.Is(err, domain.ErrDuplicateChannelCategoryName):
		httputil.WriteError(w, http.StatusConflict, httputil.ErrCodeConflict, "category name already in use")
	case errors.Is(err, domain.ErrChannelCategoryLimitReached):
		httputil.WriteError(w, http.StatusConflict, httputil.ErrCodeConflict, "category limit reached")
	case errors.Is(err, domain.ErrConflict):
		httputil.WriteError(w, http.StatusConflict, httputil.ErrCodeConflict, "conflict")
	case errors.Is(err, domain.ErrInvalidInput):
		httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, "invalid category")
	case errors.Is(err, domain.ErrForbidden):
		httputil.WriteError(w, http.StatusForbidden, httputil.ErrCodeForbidden, "forbidden")
	case errors.Is(err, domain.ErrNotFound):
		httputil.WriteError(w, http.StatusNotFound, httputil.ErrCodeNotFound, "category not found")
	default:
		httputil.WriteError(w, http.StatusInternalServerError, httputil.ErrCodeInternal, "internal error")
	}
}
