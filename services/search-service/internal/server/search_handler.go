package server

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/services/search-service/internal/domain"
)

const (
	defaultLimit = 20
	maxLimit     = 100
)

type SearchProvider interface {
	SearchLegacyMessages(context.Context, string, string, int, string) (domain.LegacyMessagePage, error)
	SearchMessages(context.Context, string, string, int, string) (domain.MessagePage, error)
	SearchUsers(context.Context, string, string, int, string) (domain.UserPage, error)
	SearchChannels(context.Context, string, string, int, string) (domain.ChannelPage, error)
	SearchGroups(context.Context, string, string, int, string) (domain.GroupPage, error)
	SearchFiles(context.Context, string, string, int, string) (domain.FilePage, error)
}

type SearchHandler struct{ provider SearchProvider }

func NewSearchHandler(provider SearchProvider) *SearchHandler {
	return &SearchHandler{provider: provider}
}

type pagination struct {
	Limit      int     `json:"limit"`
	NextCursor *string `json:"next_cursor"`
	HasMore    bool    `json:"has_more"`
}
type pageResponse struct {
	Data       any        `json:"data"`
	Pagination pagination `json:"pagination"`
}

type searchFunc[T any] func(context.Context, string, string, int, string) (domain.Page[T], error)

// servePage is every search endpoint: the caller is the authenticated
// principal and nothing else — no user or workspace is read from the request.
func servePage[T any](w http.ResponseWriter, r *http.Request, search searchFunc[T]) {
	q, limit, cursor, ok := parseSearchRequest(w, r)
	if !ok {
		return
	}
	p, err := search(r.Context(), authenticatedUserID(r), q, limit, cursor)
	if err != nil {
		writeSearchError(w, err)
		return
	}
	writePage(w, p.Items, limit, p.NextCursor)
}

// LegacyMessages is GET /api/search/messages as it was before #900: channel
// messages in the channel-only shape. Deprecated, retained for rollout
// compatibility — a web build from before #900 calls it and routes every row
// to a channel. Not to be removed while such a build can still be served
// (docs/api/search.md).
func (h *SearchHandler) LegacyMessages(w http.ResponseWriter, r *http.Request) {
	servePage(w, r, h.provider.SearchLegacyMessages)
}

// Messages is GET /api/search/v2/messages: every conversation kind.
func (h *SearchHandler) Messages(w http.ResponseWriter, r *http.Request) {
	servePage(w, r, h.provider.SearchMessages)
}
func (h *SearchHandler) Users(w http.ResponseWriter, r *http.Request) {
	servePage(w, r, h.provider.SearchUsers)
}
func (h *SearchHandler) Channels(w http.ResponseWriter, r *http.Request) {
	servePage(w, r, h.provider.SearchChannels)
}
func (h *SearchHandler) Groups(w http.ResponseWriter, r *http.Request) {
	servePage(w, r, h.provider.SearchGroups)
}
func (h *SearchHandler) Files(w http.ResponseWriter, r *http.Request) {
	servePage(w, r, h.provider.SearchFiles)
}

func parseSearchRequest(w http.ResponseWriter, r *http.Request) (string, int, string, bool) {
	if authenticatedUserID(r) == "" {
		httputil.WriteError(w, http.StatusUnauthorized, httputil.ErrCodeUnauthorized, "unauthorized")
		return "", 0, "", false
	}
	values := r.URL.Query()
	if len(values["q"]) != 1 || len(values["limit"]) > 1 || len(values["cursor"]) > 1 {
		httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, "invalid search")
		return "", 0, "", false
	}
	q := strings.TrimSpace(values.Get("q"))
	if q == "" || len([]byte(q)) > 512 {
		httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, "invalid search")
		return "", 0, "", false
	}
	limit := defaultLimit
	if raw := values.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > maxLimit {
			httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, "invalid limit")
			return "", 0, "", false
		}
		limit = n
	}
	return q, limit, values.Get("cursor"), true
}

func writePage(w http.ResponseWriter, data any, limit int, next string) {
	var ptr *string
	if next != "" {
		ptr = &next
	}
	httputil.WriteJSON(w, http.StatusOK, pageResponse{Data: data, Pagination: pagination{Limit: limit, NextCursor: ptr, HasMore: next != ""}})
}
func writeSearchError(w http.ResponseWriter, err error) {
	if errors.Is(err, domain.ErrInvalidInput) || errors.Is(err, domain.ErrInvalidCursor) {
		httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, "invalid search")
		return
	}
	if errors.Is(err, domain.ErrUnauthorized) {
		httputil.WriteError(w, http.StatusUnauthorized, httputil.ErrCodeUnauthorized, "unauthorized")
		return
	}
	httputil.WriteError(w, http.StatusInternalServerError, httputil.ErrCodeInternal, "internal error")
}
