package httpapi_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	httpapi "github.com/nicrepository/nchat/services/auth-service/internal/http"
	"github.com/nicrepository/nchat/services/auth-service/internal/service"
)

func TestBearerAuth_NoAuthorizationHeader_Returns401(t *testing.T) {
	tokens := makeTestTokenManager(t)
	handler := httpapi.BearerAuth(tokens)(okHandler)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
	assertErrorCode(t, rec.Body.Bytes(), "unauthorized")
}

func TestBearerAuth_EmptyBearerToken_Returns401(t *testing.T) {
	tokens := makeTestTokenManager(t)
	handler := httpapi.BearerAuth(tokens)(okHandler)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Bearer ")
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
	assertErrorCode(t, rec.Body.Bytes(), "unauthorized")
}

func TestBearerAuth_InvalidJWT_Returns401(t *testing.T) {
	tokens := makeTestTokenManager(t)
	handler := httpapi.BearerAuth(tokens)(okHandler)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Bearer invalid.jwt.token")
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
	assertErrorCode(t, rec.Body.Bytes(), "unauthorized")
}

func TestBearerAuth_BasicAuthScheme_Returns401(t *testing.T) {
	tokens := makeTestTokenManager(t)
	handler := httpapi.BearerAuth(tokens)(okHandler)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Basic dXNlcjpwYXNz")
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
	assertErrorCode(t, rec.Body.Bytes(), "unauthorized")
}

func TestBearerAuth_ValidJWT_InjectsUserID(t *testing.T) {
	tokens := makeTestTokenManager(t)
	expectedUserID := "user-123"
	accessToken, _, err := tokens.GenerateAccessToken(expectedUserID, "session-456")
	if err != nil {
		t.Fatalf("generate access token: %v", err)
	}

	svc := &mockLoginAttemptsManager{}
	handler := httpapi.BearerAuth(tokens)(httpapi.GetMyLoginAttempts(svc))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/auth/me/login-attempts", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if svc.lastUserID != expectedUserID {
		t.Fatalf("expected userID %q, got %q", expectedUserID, svc.lastUserID)
	}
}

func TestBearerAuth_NilTokens_Returns503(t *testing.T) {
	handler := httpapi.BearerAuth(nil)(okHandler)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
	assertErrorCode(t, rec.Body.Bytes(), "service_unavailable")
}

func makeTestTokenManager(t *testing.T) *service.TokenManager {
	t.Helper()
	tokens, err := service.NewTokenManager(service.TokenConfig{
		HMACSecret: strings.Repeat("a", 32),
		Issuer:     "test-issuer",
		Audience:   "test-audience",
		AccessTTL:  15 * time.Minute,
		RefreshTTL: 7 * 24 * time.Hour,
	})
	if err != nil {
		t.Fatalf("create token manager: %v", err)
	}
	return tokens
}
