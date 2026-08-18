package server

import (
	"context"
	"github.com/nicrepository/nchat/services/search-service/internal/domain"
	"net/http"
	"net/http/httptest"
	"testing"
)

type fakeTokenValidator struct {
	principal Principal
	err       error
}

func (f fakeTokenValidator) ValidateAccessToken(string) (Principal, error) { return f.principal, f.err }

type fakeSessionValidator struct{ err error }

func (f fakeSessionValidator) ValidateActiveSession(context.Context, string, string) error {
	return f.err
}

func TestAuthenticationRequiresBearerAndActiveSession(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if authenticatedUserID(r) != "user" {
			t.Fatal("principal missing")
		}
		w.WriteHeader(http.StatusNoContent)
	})
	h := BearerAuth(fakeTokenValidator{principal: Principal{UserID: "user", SessionID: "11111111-1111-4111-8111-111111111111"}})(RequireActiveSession(fakeSessionValidator{})(next))
	missing := httptest.NewRecorder()
	h.ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/", nil))
	if missing.Code != http.StatusUnauthorized {
		t.Fatalf("missing status=%d", missing.Code)
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer token")
	ok := httptest.NewRecorder()
	h.ServeHTTP(ok, req)
	if ok.Code != http.StatusNoContent {
		t.Fatalf("valid status=%d body=%s", ok.Code, ok.Body.String())
	}
}

func TestAuthenticationRejectsRevokedSession(t *testing.T) {
	h := BearerAuth(fakeTokenValidator{principal: Principal{UserID: "user", SessionID: "11111111-1111-4111-8111-111111111111"}})(RequireActiveSession(fakeSessionValidator{err: domain.ErrUnauthorized})(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("must not reach handler") })))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer token")
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d", res.Code)
	}
}
