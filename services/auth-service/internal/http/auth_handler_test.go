package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
	httpapi "github.com/nicrepository/nchat/services/auth-service/internal/http"
)

type fakeAuthService struct {
	pair       domain.TokenPair
	refreshErr error
	logoutErr  error

	refreshToken string
	logoutToken  string
}

func (f *fakeAuthService) Refresh(_ context.Context, refreshToken string) (domain.TokenPair, error) {
	f.refreshToken = refreshToken
	return f.pair, f.refreshErr
}

func (f *fakeAuthService) Logout(_ context.Context, refreshToken string) error {
	f.logoutToken = refreshToken
	return f.logoutErr
}

func TestAuthRefresh_SuccessReturnsTokenResponse(t *testing.T) {
	auth := &fakeAuthService{pair: domain.TokenPair{
		AccessToken:  "access-token",
		RefreshToken: "new-refresh-token",
		TokenType:    "Bearer",
		ExpiresIn:    900,
	}}
	handler := httpapi.AuthRefresh(auth)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, httpapi.RouteAuthRefresh, strings.NewReader(`{"refresh_token":"old-refresh-token"}`))

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if auth.refreshToken != "old-refresh-token" {
		t.Fatalf("expected raw refresh token passed to service, got %q", auth.refreshToken)
	}
	var body struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		TokenType    string `json:"token_type"`
		ExpiresIn    int    `json:"expires_in"`
		TokenHash    string `json:"refresh_token_hash"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.AccessToken != "access-token" || body.RefreshToken != "new-refresh-token" || body.TokenType != "Bearer" || body.ExpiresIn != 900 {
		t.Fatalf("unexpected response: %+v", body)
	}
	if body.TokenHash != "" || strings.Contains(rec.Body.String(), "hash") {
		t.Fatalf("response must not include token hash: %s", rec.Body.String())
	}
}

func TestAuthRefresh_MissingSecretOrStoreReturns503(t *testing.T) {
	handler := httpapi.AuthRefresh(nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, httpapi.RouteAuthRefresh, strings.NewReader(`{"refresh_token":"token"}`))

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
}

func TestAuthRefresh_RevokedOrExpiredRefreshRejected(t *testing.T) {
	handler := httpapi.AuthRefresh(&fakeAuthService{refreshErr: domain.ErrInvalidRefreshToken})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, httpapi.RouteAuthRefresh, strings.NewReader(`{"refresh_token":"bad-token"}`))

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestAuthRefresh_InvalidJSONReturns400(t *testing.T) {
	handler := httpapi.AuthRefresh(&fakeAuthService{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, httpapi.RouteAuthRefresh, strings.NewReader(`not-json`))

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestAuthLogout_SuccessReturns204(t *testing.T) {
	auth := &fakeAuthService{}
	handler := httpapi.AuthLogout(auth)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, httpapi.RouteAuthLogout, strings.NewReader(`{"refresh_token":"refresh-token"}`))

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("expected empty body, got %s", rec.Body.String())
	}
	if auth.logoutToken != "refresh-token" {
		t.Fatalf("expected raw refresh token passed to service, got %q", auth.logoutToken)
	}
}

func TestAuthLogout_MissingSecretOrStoreReturns503(t *testing.T) {
	handler := httpapi.AuthLogout(nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, httpapi.RouteAuthLogout, strings.NewReader(`{"refresh_token":"token"}`))

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
}

func TestAuthLogout_InvalidRefreshTokenReturns401(t *testing.T) {
	handler := httpapi.AuthLogout(&fakeAuthService{logoutErr: domain.ErrInvalidRefreshToken})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, httpapi.RouteAuthLogout, strings.NewReader(`{"refresh_token":"bad-token"}`))

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestAuthLogout_InternalErrorReturns500(t *testing.T) {
	handler := httpapi.AuthLogout(&fakeAuthService{logoutErr: errors.New("db down")})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, httpapi.RouteAuthLogout, strings.NewReader(`{"refresh_token":"token"}`))

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
}

func TestAuthRefresh_ServiceInvalidInputReturns400(t *testing.T) {
	handler := httpapi.AuthRefresh(&fakeAuthService{refreshErr: domain.ErrInvalidInput})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, httpapi.RouteAuthRefresh, strings.NewReader(`{"refresh_token":""}`))

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestAuthLogout_InvalidJSONReturns400(t *testing.T) {
	handler := httpapi.AuthLogout(&fakeAuthService{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, httpapi.RouteAuthLogout, strings.NewReader(`not-json`))

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}
