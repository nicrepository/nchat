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
	SearchMessages(context.Context, string, string, int, string) (domain.MessagePage, error)
	SearchUsers(context.Context, string, string, int, string) (domain.UserPage, error)
	SearchChannels(context.Context, string, string, int, string) (domain.ChannelPage, error)
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

func (h *SearchHandler) Messages(w http.ResponseWriter, r *http.Request) {
	q, limit, cursor, ok := parseSearchRequest(w, r)
	if !ok {
		return
	}
	p, err := h.provider.SearchMessages(r.Context(), authenticatedUserID(r), q, limit, cursor)
	if err != nil {
		writeSearchError(w, err)
		return
	}
	writePage(w, p.Items, limit, p.NextCursor)
}
func (h *SearchHandler) Users(w http.ResponseWriter, r *http.Request) {
	q, limit, cursor, ok := parseSearchRequest(w, r)
	if !ok {
		return
	}
	p, err := h.provider.SearchUsers(r.Context(), authenticatedUserID(r), q, limit, cursor)
	if err != nil {
		writeSearchError(w, err)
		return
	}
	writePage(w, p.Items, limit, p.NextCursor)
}
func (h *SearchHandler) Channels(w http.ResponseWriter, r *http.Request) {
	q, limit, cursor, ok := parseSearchRequest(w, r)
	if !ok {
		return
	}
	p, err := h.provider.SearchChannels(r.Context(), authenticatedUserID(r), q, limit, cursor)
	if err != nil {
		writeSearchError(w, err)
		return
	}
	writePage(w, p.Items, limit, p.NextCursor)
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
