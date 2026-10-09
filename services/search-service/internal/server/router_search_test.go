package server

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/nicrepository/nchat/services/search-service/internal/domain"
	"net/http/httptest"
	"testing"
)

func TestSearchRoutesAreRegisteredAndAuthenticated(t *testing.T) {
	h := NewHandlerWithDependencies("search-service", Dependencies{Search: fakeSearchProvider{}, Tokens: fakeTokenValidator{principal: Principal{UserID: "user", SessionID: "11111111-1111-4111-8111-111111111111"}}, Sessions: fakeSessionValidator{}})
	for _, path := range []string{"/api/search/messages?q=x", "/api/search/v2/messages?q=x", "/api/search/users?q=x", "/api/search/channels?q=x", "/api/search/groups?q=x", "/api/search/files?q=x"} {
		unauth := httptest.NewRecorder()
		h.ServeHTTP(unauth, httptest.NewRequest(http.MethodGet, path, nil))
		if unauth.Code != http.StatusUnauthorized {
			t.Fatalf("%s unauth status=%d", path, unauth.Code)
		}
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer token")
		res := httptest.NewRecorder()
		h.ServeHTTP(res, req)
		if res.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", path, res.Code, res.Body.String())
		}
	}
}

func TestSearchRoutesAcceptGatewayStrippedPaths(t *testing.T) {
	h := NewHandlerWithDependencies("search-service", Dependencies{Search: fakeSearchProvider{}, Tokens: fakeTokenValidator{principal: Principal{UserID: "user", SessionID: "11111111-1111-4111-8111-111111111111"}}, Sessions: fakeSessionValidator{}})
	for _, path := range []string{"/messages?q=x", "/v2/messages?q=x", "/users?q=x", "/channels?q=x", "/groups?q=x", "/files?q=x"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer token")
		res := httptest.NewRecorder()
		h.ServeHTTP(res, req)
		if res.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", path, res.Code, res.Body.String())
		}
	}
}

// itemKeys serves path through the full router and returns the JSON keys of
// the first result, sorted.
func itemKeys(t *testing.T, provider fakeSearchProvider, path string) string {
	t.Helper()
	return requestItemKeys(t, provider, httptest.NewRequest(http.MethodGet, path, nil))
}

func requestItemKeys(t *testing.T, provider fakeSearchProvider, req *http.Request) string {
	t.Helper()
	path := req.URL.Path
	h := NewHandlerWithDependencies("search-service", Dependencies{Search: provider, Tokens: fakeTokenValidator{principal: Principal{UserID: "user", SessionID: "11111111-1111-4111-8111-111111111111"}}, Sessions: fakeSessionValidator{}})
	req.Header.Set("Authorization", "Bearer token")
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("%s status=%d body=%s", path, res.Code, res.Body.String())
	}
	var body struct {
		Data struct {
			Data []map[string]any `json:"data"`
		} `json:"data"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil || len(body.Data.Data) != 1 {
		t.Fatalf("%s body: %v", path, err)
	}
	keys := make([]string, 0)
	for k := range body.Data.Data[0] {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}

// DEPRECATED/LEGACY, RETAINED FOR ROLLOUT COMPATIBILITY (#900).
//
// A web build from before #900 calls GET /api/search/messages and routes every
// row to /chat/channel/:channel_id. Until no such build can be served — blue
// and green slots, a rollback, a cached bundle — this route must answer with
// exactly the pre-#900 shape. Deleting or reshaping it fails this test on
// purpose; see docs/api/search.md before touching it.
func TestLegacyMessagesRouteIsRetainedForRolloutCompatibility(t *testing.T) {
	created := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	provider := fakeSearchProvider{
		legacy:   domain.LegacyMessagePage{Items: []domain.LegacyMessageResult{{ID: "m1", ChannelID: "c1", ChannelName: "geral", CreatedAt: created}}},
		messages: domain.MessagePage{Items: []domain.MessageResult{{ID: "m1", ConversationKind: "dm", ConversationID: "d1", CreatedAt: created}}},
	}
	const legacyShape = "body_text,channel_id,channel_name,created_at,id,score,sender_display_name,sender_id"
	for _, path := range []string{"/api/search/messages?q=x", "/messages?q=x"} {
		if got := itemKeys(t, provider, path); got != legacyShape {
			t.Fatalf("%s legacy shape changed: %s", path, got)
		}
	}
	const v2Shape = "body_text,conversation_id,conversation_kind,conversation_name,conversation_type,created_at,id,score,sender_display_name,sender_id"
	for _, path := range []string{"/api/search/v2/messages?q=x", "/v2/messages?q=x"} {
		if got := itemKeys(t, provider, path); got != v2Shape {
			t.Fatalf("%s v2 shape changed: %s", path, got)
		}
	}
}

// POST /api/search/links is the only body search; GET there is refused, so a
// client can never be steered into putting a URL in the request line.
func TestLinksRouteIsPostOnlyAndAuthenticated(t *testing.T) {
	provider := fakeSearchProvider{links: domain.LinkPage{Items: []domain.LinkResult{{MessageID: "m1"}}}}
	h := NewHandlerWithDependencies("search-service", Dependencies{Search: provider, Tokens: fakeTokenValidator{principal: Principal{UserID: "user", SessionID: "11111111-1111-4111-8111-111111111111"}}, Sessions: fakeSessionValidator{}})
	const linkShape = "conversation_id,conversation_kind,conversation_name,conversation_type,created_at,hostname,message_id,sender_display_name,sender_id,target_key,url"
	for _, path := range []string{"/api/search/links", "/links"} {
		unauth := httptest.NewRecorder()
		h.ServeHTTP(unauth, httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"q":"x"}`)))
		get := httptest.NewRequest(http.MethodGet, path+"?q=x", nil)
		get.Header.Set("Authorization", "Bearer token")
		getRes := httptest.NewRecorder()
		h.ServeHTTP(getRes, get)
		if unauth.Code != http.StatusUnauthorized || getRes.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s unauth=%d GET=%d", path, unauth.Code, getRes.Code)
		}
		if got := requestItemKeys(t, provider, httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"q":"x"}`))); got != linkShape {
			t.Fatalf("%s link shape: %s", path, got)
		}
	}
}

// #1081: every modern category answers the same search as a POST body, so the
// web never has to put a query — possibly a whole URL with a token — in a URL.
// GET stays for older web builds; the legacy route stays GET only.
func TestModernCategoriesAcceptTheBodyTransportAndKeepGET(t *testing.T) {
	h := NewHandlerWithDependencies("search-service", Dependencies{Search: fakeSearchProvider{}, Tokens: fakeTokenValidator{principal: Principal{UserID: "user", SessionID: "11111111-1111-4111-8111-111111111111"}}, Sessions: fakeSessionValidator{}})
	serve := func(method, target, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, target, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer token")
		res := httptest.NewRecorder()
		h.ServeHTTP(res, req)
		return res
	}
	for _, path := range []string{"/api/search/v2/messages", "/api/search/users", "/api/search/channels", "/api/search/groups", "/api/search/files", "/users"} {
		post := serve(http.MethodPost, path, `{"q":"https://example.test/reset/token?signature=SECRET123","limit":5}`)
		get := serve(http.MethodGet, path+"?q=backup", "")
		// A POST reads only its body: a query string is not a fallback for it.
		queryOnly := serve(http.MethodPost, path+"?q=backup", "")
		if post.Code != http.StatusOK || get.Code != http.StatusOK || queryOnly.Code != http.StatusBadRequest {
			t.Fatalf("%s POST=%d GET=%d POST-without-body=%d", path, post.Code, get.Code, queryOnly.Code)
		}
	}
	legacy := serve(http.MethodPost, "/api/search/messages", `{"q":"x"}`)
	other := serve(http.MethodDelete, "/api/search/users", "")
	if legacy.Code != http.StatusMethodNotAllowed || other.Code != http.StatusMethodNotAllowed || other.Header().Get("Allow") != "GET, POST" {
		t.Fatalf("legacy POST=%d users DELETE=%d Allow=%q", legacy.Code, other.Code, other.Header().Get("Allow"))
	}
}
