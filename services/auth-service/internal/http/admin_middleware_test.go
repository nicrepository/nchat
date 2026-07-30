package httpapi_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	httpapi "github.com/nicrepository/nchat/services/auth-service/internal/http"
)

var okHandler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
})

func TestAdminBootstrapGuard_TokenNotConfigured_Returns503(t *testing.T) {
	handler := httpapi.AdminBootstrapGuard("")(okHandler)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/admin/invites", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
	assertErrorCode(t, rec.Body.Bytes(), "service_unavailable")
}

func TestAdminBootstrapGuard_MissingHeader_Returns401(t *testing.T) {
	credential := makeTestOpaqueValue("admin-bootstrap")
	handler := httpapi.AdminBootstrapGuard(credential)(okHandler)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/admin/invites", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
	assertErrorCode(t, rec.Body.Bytes(), "unauthorized")
}

func TestAdminBootstrapGuard_WrongToken_Returns401(t *testing.T) {
	credential := makeTestOpaqueValue("admin-bootstrap")
	handler := httpapi.AdminBootstrapGuard(credential)(okHandler)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/admin/invites", nil)
	req.Header.Set("X-NChat-Admin-Token", makeTestOpaqueValue("admin-bootstrap-wrong"))
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestAdminBootstrapGuard_CorrectToken_Passes(t *testing.T) {
	credential := makeTestOpaqueValue("admin-bootstrap")
	handler := httpapi.AdminBootstrapGuard(credential)(okHandler)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/admin/invites", nil)
	req.Header.Set("X-NChat-Admin-Token", credential)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func assertErrorCode(t *testing.T, body []byte, code string) {
	t.Helper()
	var env struct {
		Error *struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if env.Error == nil {
		t.Fatal("expected error envelope")
	}
	if env.Error.Code != code {
		t.Fatalf("expected code %q, got %q", code, env.Error.Code)
	}
}
