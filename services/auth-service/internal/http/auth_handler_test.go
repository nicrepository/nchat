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

func TestAuthRefresh_OversizedBodyReturns413(t *testing.T) {
	auth := &fakeAuthService{pair: domain.TokenPair{AccessToken: "access", RefreshToken: "refresh", TokenType: "Bearer", ExpiresIn: 900}}
	handler := httpapi.AuthRefresh(auth)
	rec := httptest.NewRecorder()
	body := `{"refresh_token":"` + strings.Repeat("a", 5000) + `"}`
	req := httptest.NewRequest(http.MethodPost, httpapi.RouteAuthRefresh, strings.NewReader(body))

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d body=%s", rec.Code, rec.Body.String())
	}
	if auth.refreshToken != "" {
		t.Fatalf("oversized body must not reach service, got token %q", auth.refreshToken)
	}
	if strings.Contains(rec.Body.String(), strings.Repeat("a", 32)) {
		t.Fatalf("response must not echo token material: %s", rec.Body.String())
	}
}

func TestAuthRefresh_OversizedTrailingBodyReturns413(t *testing.T) {
	auth := &fakeAuthService{pair: domain.TokenPair{AccessToken: "access", RefreshToken: "refresh", TokenType: "Bearer", ExpiresIn: 900}}
	handler := httpapi.AuthRefresh(auth)
	rec := httptest.NewRecorder()
	body := `{"refresh_token":"token"}` + strings.Repeat(" ", 5000)
	req := httptest.NewRequest(http.MethodPost, httpapi.RouteAuthRefresh, strings.NewReader(body))

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d body=%s", rec.Code, rec.Body.String())
	}
	if auth.refreshToken != "" {
		t.Fatalf("oversized trailing body must not reach service, got token %q", auth.refreshToken)
	}
	if strings.Contains(rec.Body.String(), strings.Repeat(" ", 32)) {
		t.Fatalf("response must not echo token material: %s", rec.Body.String())
	}
}

func TestAuthLogout_OversizedBodyReturns413(t *testing.T) {
	auth := &fakeAuthService{}
	handler := httpapi.AuthLogout(auth)
	rec := httptest.NewRecorder()
	body := `{"refresh_token":"` + strings.Repeat("b", 5000) + `"}`
	req := httptest.NewRequest(http.MethodPost, httpapi.RouteAuthLogout, strings.NewReader(body))

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d body=%s", rec.Code, rec.Body.String())
	}
	if auth.logoutToken != "" {
		t.Fatalf("oversized body must not reach service, got token %q", auth.logoutToken)
	}
	if strings.Contains(rec.Body.String(), strings.Repeat("b", 32)) {
		t.Fatalf("response must not echo token material: %s", rec.Body.String())
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

func TestAuthLogout_InvalidRefreshTokenReturns204(t *testing.T) {
	handler := httpapi.AuthLogout(&fakeAuthService{logoutErr: domain.ErrInvalidRefreshToken})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, httpapi.RouteAuthLogout, strings.NewReader(`{"refresh_token":"bad-token"}`))

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("expected empty body, got %s", rec.Body.String())
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

// --- Login handler tests ---

type fakeLoginService struct {
	result domain.LoginResult
	err    error
	got    domain.LoginInput
}

func (f *fakeLoginService) Login(_ context.Context, input domain.LoginInput) (domain.LoginResult, error) {
	f.got = input
	return f.result, f.err
}

func TestAuthLogin_SuccessReturnsTokenAndSafeUser(t *testing.T) {
	svc := &fakeLoginService{result: domain.LoginResult{
		AccessToken:  "at",
		RefreshToken: "rt",
		TokenType:    "Bearer",
		ExpiresIn:    900,
		User:         domain.LoginUser{ID: "u1", Email: "user@example.com", DisplayName: "User", MustChangePassword: true},
	}}
	handler := httpapi.AuthLogin(svc)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, httpapi.RouteAuthLogin,
		strings.NewReader(`{"email":"user@example.com","password":"Pass@123"}`))

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		TokenType    string `json:"token_type"`
		ExpiresIn    int    `json:"expires_in"`
		User         struct {
			ID                 string `json:"id"`
			Email              string `json:"email"`
			DisplayName        string `json:"display_name"`
			MustChangePassword bool   `json:"must_change_password"`
		} `json:"user"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.AccessToken != "at" || body.RefreshToken != "rt" || body.TokenType != "Bearer" || body.ExpiresIn != 900 {
		t.Fatalf("unexpected tokens: %+v", body)
	}
	if body.User.ID != "u1" || body.User.Email != "user@example.com" || !body.User.MustChangePassword {
		t.Fatalf("unexpected user: %+v", body.User)
	}
}

func TestAuthLogin_InvalidCredentialsReturns401(t *testing.T) {
	handler := httpapi.AuthLogin(&fakeLoginService{err: domain.ErrInvalidCredentials})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, httpapi.RouteAuthLogin,
		strings.NewReader(`{"email":"user@example.com","password":"wrong"}`))

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "invalid_credentials") {
		t.Fatalf("expected invalid_credentials code in body: %s", rec.Body.String())
	}
}

func TestAuthLogin_InvalidJSONReturns400(t *testing.T) {
	handler := httpapi.AuthLogin(&fakeLoginService{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, httpapi.RouteAuthLogin, strings.NewReader(`not-json`))

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestAuthLogin_TrailingJSONReturns400(t *testing.T) {
	handler := httpapi.AuthLogin(&fakeLoginService{result: domain.LoginResult{TokenType: "Bearer"}})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, httpapi.RouteAuthLogin,
		strings.NewReader(`{"email":"e","password":"p"}{"extra":"junk"}`))

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestAuthLogin_OversizedBodyReturns413(t *testing.T) {
	handler := httpapi.AuthLogin(&fakeLoginService{})
	rec := httptest.NewRecorder()
	body := `{"email":"user@example.com","password":"` + strings.Repeat("a", 5000) + `"}`
	req := httptest.NewRequest(http.MethodPost, httpapi.RouteAuthLogin, strings.NewReader(body))

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d", rec.Code)
	}
}

func TestAuthLogin_ServiceNilReturns503(t *testing.T) {
	handler := httpapi.AuthLogin(nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, httpapi.RouteAuthLogin,
		strings.NewReader(`{"email":"user@example.com","password":"Pass@123"}`))

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
}

func TestAuthLogin_ResponseDoesNotLeakSensitiveFields(t *testing.T) {
	svc := &fakeLoginService{result: domain.LoginResult{
		AccessToken:  "at",
		RefreshToken: "rt",
		TokenType:    "Bearer",
		ExpiresIn:    900,
		User:         domain.LoginUser{ID: "u1", Email: "user@example.com"},
	}}
	handler := httpapi.AuthLogin(svc)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, httpapi.RouteAuthLogin,
		strings.NewReader(`{"email":"user@example.com","password":"Pass@123"}`))

	handler.ServeHTTP(rec, req)

	body := rec.Body.String()
	// Verify no raw password material or hash fields are leaked.
	// Note: "must_change_password" is an intentional boolean field — we check for sensitive
	// key names that would expose credential data, not the bool flag.
	for _, sensitive := range []string{"password_hash", "device_fingerprint_hash", `"password":`, `"raw_password"`} {
		if strings.Contains(body, sensitive) {
			t.Fatalf("response must not include %q: %s", sensitive, body)
		}
	}
}
