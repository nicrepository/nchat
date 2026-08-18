package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSearchRoutesAreRegisteredAndAuthenticated(t *testing.T) {
	h := NewHandlerWithDependencies("search-service", Dependencies{Search: fakeSearchProvider{}, Tokens: fakeTokenValidator{principal: Principal{UserID: "user", SessionID: "11111111-1111-4111-8111-111111111111"}}, Sessions: fakeSessionValidator{}})
	for _, path := range []string{"/api/search/messages?q=x", "/api/search/users?q=x", "/api/search/channels?q=x"} {
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
	for _, path := range []string{"/messages?q=x", "/users?q=x", "/channels?q=x"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer token")
		res := httptest.NewRecorder()
		h.ServeHTTP(res, req)
		if res.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", path, res.Code, res.Body.String())
		}
	}
}
