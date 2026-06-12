package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
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

// refreshCookieFor builds an *http.Cookie with the given value to attach to a test request.
func refreshCookieFor(value string) *http.Cookie {
	return &http.Cookie{
		Name:     "nchat_rt",
		Value:    value,
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}
}

// findRefreshCookie returns the nchat_rt Set-Cookie entry from the response, or nil if absent.
func findRefreshCookie(rec *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == "nchat_rt" {
			return c
		}
	}
	return nil
}

// --- AuthRefresh tests ---

func TestAuthRefresh_SuccessReturnsTokenResponse(t *testing.T) {
	submitted := makeTestOpaqueValue("auth-refresh-submitted")
	access := makeTestOpaqueValue("auth-refresh-access")
	nextRefresh := makeTestOpaqueValue("auth-refresh-next")
	auth := &fakeAuthService{pair: domain.TokenPair{
		AccessToken:  access,
		RefreshToken: nextRefresh,
		TokenType:    "Bearer",
		ExpiresIn:    900,
	}}
	handler := httpapi.AuthRefresh(auth, 3600)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, httpapi.RouteAuthRefresh, nil)
	req.AddCookie(refreshCookieFor(submitted))

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if auth.refreshToken != submitted {
		t.Fatalf("expected raw refresh token passed to service, got %q", auth.refreshToken)
	}
	var body struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		TokenType    string `json:"token_type"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.AccessToken != access || body.TokenType != "Bearer" || body.ExpiresIn != 900 {
		t.Fatalf("unexpected response: %+v", body)
	}
	if body.RefreshToken != "" {
		t.Fatalf("refresh_token must not appear in JSON body, got %q", body.RefreshToken)
	}
	// New refresh token must be delivered via Set-Cookie.
	rtCookie := findRefreshCookie(rec)
	if rtCookie == nil {
		t.Fatal("expected nchat_rt Set-Cookie in response")
	}
	if rtCookie.Value != nextRefresh {
		t.Fatalf("expected cookie value %q, got %q", nextRefresh, rtCookie.Value)
	}
	if !rtCookie.HttpOnly {
		t.Fatal("expected nchat_rt cookie to be HttpOnly")
	}
	if !rtCookie.Secure {
		t.Fatal("expected nchat_rt cookie to be Secure")
	}
	if rtCookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("expected SameSite=Strict, got %v", rtCookie.SameSite)
	}
}

func TestAuthRefresh_MissingCookieReturns401(t *testing.T) {
	handler := httpapi.AuthRefresh(&fakeAuthService{}, 3600)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, httpapi.RouteAuthRefresh, nil)

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAuthRefresh_MissingSecretOrStoreReturns503(t *testing.T) {
	handler := httpapi.AuthRefresh(nil, 0)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, httpapi.RouteAuthRefresh, nil)

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
}

func TestAuthRefresh_RevokedOrExpiredRefreshRejected(t *testing.T) {
	handler := httpapi.AuthRefresh(&fakeAuthService{refreshErr: domain.ErrInvalidRefreshToken}, 3600)
	rec := httptest.NewRecorder()
	submitted := makeTestOpaqueValue("auth-refresh-invalid")
	req := httptest.NewRequest(http.MethodPost, httpapi.RouteAuthRefresh, nil)
	req.AddCookie(refreshCookieFor(submitted))

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestAuthRefresh_ServiceInvalidInputReturns400(t *testing.T) {
	handler := httpapi.AuthRefresh(&fakeAuthService{refreshErr: domain.ErrInvalidInput}, 3600)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, httpapi.RouteAuthRefresh, nil)
	req.AddCookie(refreshCookieFor("")) // empty value → service returns ErrInvalidInput

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

// --- AuthLogout tests ---

func TestAuthLogout_SuccessReturns204AndClearsCookie(t *testing.T) {
	auth := &fakeAuthService{}
	handler := httpapi.AuthLogout(auth)
	rec := httptest.NewRecorder()
	submitted := makeTestOpaqueValue("auth-logout-submitted")
	req := httptest.NewRequest(http.MethodPost, httpapi.RouteAuthLogout, nil)
	req.AddCookie(refreshCookieFor(submitted))

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("expected empty body, got %s", rec.Body.String())
	}
	if auth.logoutToken != submitted {
		t.Fatalf("expected raw refresh token passed to service, got %q", auth.logoutToken)
	}
	// Response must include a Set-Cookie that clears nchat_rt.
	rtCookie := findRefreshCookie(rec)
	if rtCookie == nil {
		t.Fatal("expected nchat_rt clear-cookie in logout response")
	}
	if rtCookie.MaxAge != -1 && rtCookie.Value != "" {
		t.Fatalf("expected clear-cookie (MaxAge=-1 or empty value), got MaxAge=%d value=%q", rtCookie.MaxAge, rtCookie.Value)
	}
}

func TestAuthLogout_MissingCookieReturns204AndClearsCookie(t *testing.T) {
	handler := httpapi.AuthLogout(&fakeAuthService{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, httpapi.RouteAuthLogout, nil)

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 when cookie absent, got %d body=%s", rec.Code, rec.Body.String())
	}
	rtCookie := findRefreshCookie(rec)
	if rtCookie == nil {
		t.Fatal("expected nchat_rt clear-cookie in response even when no cookie sent")
	}
}

func TestAuthLogout_MissingSecretOrStoreReturns503(t *testing.T) {
	handler := httpapi.AuthLogout(nil)
	rec := httptest.NewRecorder()
	submitted := makeTestOpaqueValue("auth-logout-unavailable")
	req := httptest.NewRequest(http.MethodPost, httpapi.RouteAuthLogout, nil)
	req.AddCookie(refreshCookieFor(submitted))

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
}

func TestAuthLogout_InvalidRefreshTokenReturns204(t *testing.T) {
	handler := httpapi.AuthLogout(&fakeAuthService{logoutErr: domain.ErrInvalidRefreshToken})
	rec := httptest.NewRecorder()
	submitted := makeTestOpaqueValue("auth-logout-invalid")
	req := httptest.NewRequest(http.MethodPost, httpapi.RouteAuthLogout, nil)
	req.AddCookie(refreshCookieFor(submitted))

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("expected empty body, got %s", rec.Body.String())
	}
	rtCookie := findRefreshCookie(rec)
	if rtCookie == nil {
		t.Fatal("expected nchat_rt clear-cookie for invalid refresh token")
	}
	if rtCookie.MaxAge != -1 {
		t.Fatalf("expected MaxAge=-1 (clear-cookie), got MaxAge=%d", rtCookie.MaxAge)
	}
}

func TestAuthLogout_InternalErrorReturns500AndDoesNotClearCookie(t *testing.T) {
	handler := httpapi.AuthLogout(&fakeAuthService{logoutErr: errors.New("db down")})
	rec := httptest.NewRecorder()
	submitted := makeTestOpaqueValue("auth-logout-internal")
	req := httptest.NewRequest(http.MethodPost, httpapi.RouteAuthLogout, nil)
	req.AddCookie(refreshCookieFor(submitted))

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
	rtCookie := findRefreshCookie(rec)
	if rtCookie != nil && rtCookie.MaxAge == -1 {
		t.Fatalf("expected no nchat_rt clear-cookie on internal error, got MaxAge=%d", rtCookie.MaxAge)
	}
}

// --- AuthLogin tests ---

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
	handler := httpapi.AuthLogin(svc, nil, 3600)
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
	if body.AccessToken != "at" || body.TokenType != "Bearer" || body.ExpiresIn != 900 {
		t.Fatalf("unexpected tokens: %+v", body)
	}
	if body.RefreshToken != "" {
		t.Fatalf("refresh_token must not appear in JSON body, got %q", body.RefreshToken)
	}
	if body.User.ID != "u1" || body.User.Email != "user@example.com" || !body.User.MustChangePassword {
		t.Fatalf("unexpected user: %+v", body.User)
	}
	// Refresh token must be delivered via HttpOnly cookie.
	rtCookie := findRefreshCookie(rec)
	if rtCookie == nil {
		t.Fatal("expected nchat_rt Set-Cookie in login response")
	}
	if rtCookie.Value != "rt" {
		t.Fatalf("expected cookie value %q, got %q", "rt", rtCookie.Value)
	}
	if !rtCookie.HttpOnly {
		t.Fatal("expected nchat_rt cookie to be HttpOnly")
	}
	if !rtCookie.Secure {
		t.Fatal("expected nchat_rt cookie to be Secure")
	}
}

func TestAuthLogin_InvalidCredentialsReturns401(t *testing.T) {
	handler := httpapi.AuthLogin(&fakeLoginService{err: domain.ErrInvalidCredentials}, nil, 0)
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
	handler := httpapi.AuthLogin(&fakeLoginService{}, nil, 0)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, httpapi.RouteAuthLogin, strings.NewReader(`not-json`))

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestAuthLogin_TrailingJSONReturns400(t *testing.T) {
	handler := httpapi.AuthLogin(&fakeLoginService{result: domain.LoginResult{TokenType: "Bearer"}}, nil, 0)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, httpapi.RouteAuthLogin,
		strings.NewReader(`{"email":"e","password":"p"}{"extra":"junk"}`))

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestAuthLogin_OversizedBodyReturns413(t *testing.T) {
	handler := httpapi.AuthLogin(&fakeLoginService{}, nil, 0)
	rec := httptest.NewRecorder()
	body := `{"email":"user@example.com","password":"` + strings.Repeat("a", 5000) + `"}`
	req := httptest.NewRequest(http.MethodPost, httpapi.RouteAuthLogin, strings.NewReader(body))

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d", rec.Code)
	}
}

func TestAuthLogin_ServiceNilReturns503(t *testing.T) {
	handler := httpapi.AuthLogin(nil, nil, 0)
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
	handler := httpapi.AuthLogin(svc, nil, 0)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, httpapi.RouteAuthLogin,
		strings.NewReader(`{"email":"user@example.com","password":"Pass@123"}`))

	handler.ServeHTTP(rec, req)

	body := rec.Body.String()
	for _, sensitive := range []string{"password_hash", "device_fingerprint_hash", `"password":`, `"raw_password"`, `"refresh_token"`} {
		if strings.Contains(body, sensitive) {
			t.Fatalf("response must not include %q: %s", sensitive, body)
		}
	}
}

func TestAuthLogin_TrustedProxy_RecordsXFFAsClientIP(t *testing.T) {
	svc := &fakeLoginService{result: domain.LoginResult{TokenType: "Bearer"}}
	cidrs := httputil.ParseCIDRs("10.0.0.0/8")
	handler := httpapi.AuthLogin(svc, cidrs, 0)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, httpapi.RouteAuthLogin,
		strings.NewReader(`{"email":"user@example.com","password":"Pass@123"}`))
	req.RemoteAddr = "10.0.0.1:1234"
	req.Header.Set("X-Forwarded-For", "203.0.113.50")

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if svc.got.IPAddress != "203.0.113.50" {
		t.Fatalf("expected IPAddress=203.0.113.50 from X-Forwarded-For, got %q", svc.got.IPAddress)
	}
}

func TestAuthLogin_TrustedProxy_MalformedXFF_UsesRemoteAddr(t *testing.T) {
	svc := &fakeLoginService{result: domain.LoginResult{TokenType: "Bearer"}}
	cidrs := httputil.ParseCIDRs("10.0.0.0/8")
	handler := httpapi.AuthLogin(svc, cidrs, 0)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, httpapi.RouteAuthLogin,
		strings.NewReader(`{"email":"user@example.com","password":"Pass@123"}`))
	req.RemoteAddr = "10.0.0.1:1234"
	req.Header.Set("X-Forwarded-For", "not-an-ip")

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if svc.got.IPAddress != "10.0.0.1" {
		t.Fatalf("expected IPAddress=10.0.0.1 fallback to RemoteAddr, got %q", svc.got.IPAddress)
	}
}

func TestAuthLogin_NoTrustedProxy_UsesRemoteAddr(t *testing.T) {
	svc := &fakeLoginService{result: domain.LoginResult{TokenType: "Bearer"}}
	handler := httpapi.AuthLogin(svc, nil, 0)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, httpapi.RouteAuthLogin,
		strings.NewReader(`{"email":"user@example.com","password":"Pass@123"}`))
	req.RemoteAddr = "203.0.113.99:5000"
	req.Header.Set("X-Forwarded-For", "198.51.100.1")

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if svc.got.IPAddress != "203.0.113.99" {
		t.Fatalf("expected IPAddress=203.0.113.99 (no trusted proxies), got %q", svc.got.IPAddress)
	}
}
