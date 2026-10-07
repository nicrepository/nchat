package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/services/search-service/internal/domain"
)

const (
	defaultLimit  = 20
	maxLimit      = 100
	maxQueryBytes = 512
	// maxSearchBodyBytes bounds a body search: a 512-byte query fully
	// JSON-escaped, a maximal cursor and the JSON around them.
	maxSearchBodyBytes = 8 << 10
)

type SearchProvider interface {
	SearchLegacyMessages(context.Context, string, string, int, string) (domain.LegacyMessagePage, error)
	SearchMessages(context.Context, string, string, int, string) (domain.MessagePage, error)
	SearchUsers(context.Context, string, string, int, string) (domain.UserPage, error)
	SearchChannels(context.Context, string, string, int, string) (domain.ChannelPage, error)
	SearchGroups(context.Context, string, string, int, string) (domain.GroupPage, error)
	SearchFiles(context.Context, string, string, int, string) (domain.FilePage, error)
	SearchLinks(context.Context, string, string, int, string) (domain.LinkPage, error)
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

// searchRequest is the validated q/limit/cursor of one search.
type searchRequest struct {
	q      string
	limit  int
	cursor string
}

var (
	errInvalidSearch = errors.New("invalid search")
	errInvalidLimit  = errors.New("invalid limit")
)

// servePage is every search endpoint: the caller is the authenticated
// principal and nothing else — no user or workspace is read from the request.
// A POST carries q/limit/cursor in its body, a GET in its query string; which
// methods a route answers is decided where it is registered (server.go).
func servePage[T any](w http.ResponseWriter, r *http.Request, search searchFunc[T]) {
	if authenticatedUserID(r) == "" {
		httputil.WriteError(w, http.StatusUnauthorized, httputil.ErrCodeUnauthorized, "unauthorized")
		return
	}
	parse := queryRequest
	if r.Method == http.MethodPost {
		parse = bodyRequest
	}
	req, err := parse(r)
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, err.Error())
		return
	}
	p, err := search(r.Context(), authenticatedUserID(r), req.q, req.limit, req.cursor)
	if err != nil {
		writeSearchError(w, err)
		return
	}
	writePage(w, p.Items, req.limit, p.NextCursor)
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

// Links is POST /api/search/links: its query may be a whole URL, and a URL can
// carry a token or a signed parameter that must not reach an access log, a
// trace or a proxy through the request line (docs/api/search.md). It is
// registered for POST only.
func (h *SearchHandler) Links(w http.ResponseWriter, r *http.Request) {
	servePage(w, r, h.provider.SearchLinks)
}

func queryRequest(r *http.Request) (searchRequest, error) {
	values := r.URL.Query()
	if len(values["q"]) != 1 || len(values["limit"]) > 1 || len(values["cursor"]) > 1 {
		return searchRequest{}, errInvalidSearch
	}
	limit := defaultLimit
	if raw := values.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			return searchRequest{}, errInvalidLimit
		}
		limit = n
	}
	return validRequest(values.Get("q"), limit, values.Get("cursor"))
}

// bodyRequest reads exactly {"q", "limit"?, "cursor"?}: unknown fields,
// trailing data and bodies over maxSearchBodyBytes are refused.
func bodyRequest(r *http.Request) (searchRequest, error) {
	var body struct {
		Q      string `json:"q"`
		Limit  *int   `json:"limit"`
		Cursor string `json:"cursor"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxSearchBodyBytes))
	dec.DisallowUnknownFields()
	if dec.Decode(&body) != nil || dec.Decode(&struct{}{}) != io.EOF {
		return searchRequest{}, errInvalidSearch
	}
	limit := defaultLimit
	if body.Limit != nil {
		limit = *body.Limit
	}
	return validRequest(body.Q, limit, body.Cursor)
}

func validRequest(q string, limit int, cursor string) (searchRequest, error) {
	q = strings.TrimSpace(q)
	if q == "" || len(q) > maxQueryBytes {
		return searchRequest{}, errInvalidSearch
	}
	if limit < 1 || limit > maxLimit {
		return searchRequest{}, errInvalidLimit
	}
	return searchRequest{q: q, limit: limit, cursor: cursor}, nil
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
