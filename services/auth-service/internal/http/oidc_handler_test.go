package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	platformlog "github.com/nicrepository/nchat/libs/go/platform/log"
	"github.com/nicrepository/nchat/services/auth-service/internal/config"
	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
	httpapi "github.com/nicrepository/nchat/services/auth-service/internal/http"
	"github.com/nicrepository/nchat/services/auth-service/internal/service"
)

type fakeOIDCManager struct {
	loginLocation    string
	loginErr         error
	callbackLocation string
	callbackErr      error
	exchangeResult   domain.OIDCExchangeResult
	exchangeErr      error
	gotCallback      service.OIDCCallbackInput
	gotExchangeCode  string
}

func (f *fakeOIDCManager) Login(context.Context) (string, error) {
	return f.loginLocation, f.loginErr
}

func (f *fakeOIDCManager) Callback(_ context.Context, input service.OIDCCallbackInput) (string, error) {
	f.gotCallback = input
	return f.callbackLocation, f.callbackErr
}

func (f *fakeOIDCManager) Exchange(_ context.Context, code string) (domain.OIDCExchangeResult, error) {
	f.gotExchangeCode = code
	return f.exchangeResult, f.exchangeErr
}

func TestOIDCLogin_DisabledReturns404(t *testing.T) {
	handler := httpapi.OIDCLogin(nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, httpapi.RouteAuthOIDCKeycloakLogin, nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestOIDCLogin_RedirectsToProvider(t *testing.T) {
	providerURL := "https://keycloak.example.com/auth?state=redacted"
	handler := httpapi.OIDCLogin(&fakeOIDCManager{loginLocation: providerURL})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, httpapi.RouteAuthOIDCKeycloakLogin, nil))

	if rec.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != providerURL {
		t.Fatalf("expected redirect %q, got %q", providerURL, got)
	}
}

func TestOIDCCallback_MissingStateReturnsGeneric401(t *testing.T) {
	svc := &fakeOIDCManager{callbackErr: domain.ErrOIDCInvalidCallback}
	handler := httpapi.OIDCCallback(svc, nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, httpapi.RouteAuthOIDCKeycloakCallback+"?code=sensitive-code", nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "sensitive-code") {
		t.Fatalf("response must not echo provider code: %s", rec.Body.String())
	}
}

func TestOIDCCallback_RedirectsToFrontendCallback(t *testing.T) {
	frontendURL := "/oidc-callback?code=opaque"
	svc := &fakeOIDCManager{callbackLocation: frontendURL}
	handler := httpapi.OIDCCallback(svc, nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, httpapi.RouteAuthOIDCKeycloakCallback+"?code=provider-code&state=state", nil)
	req.RemoteAddr = "203.0.113.10:1111"
	req.Header.Set("User-Agent", "test-agent")
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Location") != frontendURL {
		t.Fatalf("unexpected location %q", rec.Header().Get("Location"))
	}
	if svc.gotCallback.Code != "provider-code" || svc.gotCallback.State != "state" || svc.gotCallback.UserAgent != "test-agent" {
		t.Fatalf("unexpected callback input: %+v", svc.gotCallback)
	}
}

func TestOIDCCallback_RejectsUnsafeFrontendRedirectLocation(t *testing.T) {
	for _, location := range []string{
		"https://evil.example.com/oidc-callback?code=opaque",
		"//evil.example.com/oidc-callback?code=opaque",
		"/oidc-callback\r\nSet-Cookie:bad=1",
	} {
		t.Run(location, func(t *testing.T) {
			svc := &fakeOIDCManager{callbackLocation: location}
			handler := httpapi.OIDCCallback(svc, nil)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, httpapi.RouteAuthOIDCKeycloakCallback+"?code=provider-code&state=state", nil)

			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("expected 503, got %d body=%s", rec.Code, rec.Body.String())
			}
			if rec.Header().Get("Location") != "" {
				t.Fatalf("unsafe redirect must not set location, got %q", rec.Header().Get("Location"))
			}
		})
	}
}

func TestOIDCExchange_ReturnsLoginShape(t *testing.T) {
	svc := &fakeOIDCManager{exchangeResult: domain.OIDCExchangeResult{
		AccessToken:  "access",
		RefreshToken: "refresh",
		TokenType:    "Bearer",
		ExpiresIn:    900,
		User:         domain.LoginUser{ID: "u1", Email: "user@example.com", DisplayName: "User"},
	}}
	handler := httpapi.OIDCExchange(svc, 3600)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, httpapi.RouteAuthOIDCKeycloakExchange, strings.NewReader(`{"code":"opaque-code"}`)))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if svc.gotExchangeCode != "opaque-code" {
		t.Fatalf("expected raw exchange code passed to service, got %q", svc.gotExchangeCode)
	}
	var body struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		User         struct {
			DisplayName string `json:"display_name"`
		} `json:"user"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.AccessToken != "access" || body.User.DisplayName != "User" {
		t.Fatalf("unexpected body: %+v", body)
	}
	if body.RefreshToken != "" {
		t.Fatalf("refresh_token must not appear in JSON body, got %q", body.RefreshToken)
	}
	// Refresh token must be in Set-Cookie.
	var rtCookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == "nchat_rt" {
			rtCookie = c
		}
	}
	if rtCookie == nil {
		t.Fatal("expected nchat_rt Set-Cookie in OIDC exchange response")
	}
	if rtCookie.Value != "refresh" {
		t.Fatalf("expected cookie value %q, got %q", "refresh", rtCookie.Value)
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
	if rtCookie.Path != "/api/auth" {
		t.Fatalf("expected Path=/api/auth, got %q", rtCookie.Path)
	}
}

func TestOIDCExchange_DisabledReturns404(t *testing.T) {
	handler := httpapi.OIDCExchange(nil, 0)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, httpapi.RouteAuthOIDCKeycloakExchange, strings.NewReader(`{"code":"opaque"}`)))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestOIDCExchange_InvalidRequestBodyReturns400AndSkipsService(t *testing.T) {
	svc := &fakeOIDCManager{}
	handler := httpapi.OIDCExchange(svc, 0)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, httpapi.RouteAuthOIDCKeycloakExchange, strings.NewReader(`{"code":"opaque"} {}`)))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rec.Code, rec.Body.String())
	}
	if svc.gotExchangeCode != "" {
		t.Fatalf("service must not receive malformed exchange request, got code %q", svc.gotExchangeCode)
	}
}

func TestOIDCExchange_TooLargeRequestBodyReturns413AndSkipsService(t *testing.T) {
	svc := &fakeOIDCManager{}
	handler := httpapi.OIDCExchange(svc, 0)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, httpapi.RouteAuthOIDCKeycloakExchange, strings.NewReader(`{"code":"`+strings.Repeat("a", 65_000)+`"}`)))

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d body=%s", rec.Code, rec.Body.String())
	}
	if svc.gotExchangeCode != "" {
		t.Fatalf("service must not receive oversized exchange request, got code %q", svc.gotExchangeCode)
	}
}

func TestOIDCExchange_InvalidCodeIsGenericAndDoesNotEchoCode(t *testing.T) {
	svc := &fakeOIDCManager{exchangeErr: domain.ErrInvalidToken}
	handler := httpapi.OIDCExchange(svc, 0)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, httpapi.RouteAuthOIDCKeycloakExchange, strings.NewReader(`{"code":"sensitive-exchange-code"}`)))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "sensitive-exchange-code") {
		t.Fatalf("response must not echo exchange code: %s", rec.Body.String())
	}
}

func TestOIDCRoutesRegisteredOnRouter(t *testing.T) {
	cfg := testRouterConfigForOIDC()
	router := httpapi.NewRouter(cfg, platformlog.New("auth-service", "test"), nil, nil, nil, nil, nil, nil, nil, nil, &fakeOIDCManager{loginErr: domain.ErrOIDCMisconfigured})
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, httpapi.RouteAuthOIDCKeycloakLogin, nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 from registered OIDC route, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestOIDCRouterDisabledRoutesReturn404BeforeRateLimit(t *testing.T) {
	cfg := testRouterConfigForOIDC()
	cfg.OIDCEnabled = false
	cfg.AuthTokenEndpointRateLimitPerMinute = 1
	cfg.AuthTokenEndpointRateLimitBurst = 1
	router := httpapi.NewRouter(cfg, platformlog.New("auth-service", "test"), nil, nil, nil, nil, nil, nil, nil, nil)

	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, httpapi.RouteAuthOIDCKeycloakLogin, nil)
		req.RemoteAddr = "203.0.113.20:1234"
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Fatalf("request %d expected 404 before rate limit, got %d body=%s", i+1, rec.Code, rec.Body.String())
		}
	}
}

func TestOIDCErrorMappingConflict(t *testing.T) {
	handler := httpapi.OIDCExchange(&fakeOIDCManager{exchangeErr: domain.ErrOIDCAccountConflict}, 0)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, httpapi.RouteAuthOIDCKeycloakExchange, strings.NewReader(`{"code":"opaque"}`)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", rec.Code)
	}
}

func TestOIDCExchange_InternalErrorsAreGeneric(t *testing.T) {
	handler := httpapi.OIDCExchange(&fakeOIDCManager{exchangeErr: errors.New("database leaked detail")}, 0)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, httpapi.RouteAuthOIDCKeycloakExchange, strings.NewReader(`{"code":"opaque"}`)))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "database leaked detail") {
		t.Fatalf("response must not render raw backend error: %s", rec.Body.String())
	}
}

func testRouterConfigForOIDC() config.Config {
	cfg := config.Config{ServiceName: "auth-service", Env: "test", Port: 8081, ReadHeaderTimeoutSeconds: 5}
	cfg.AuthTokenEndpointRateLimitPerMinute = 60
	cfg.AuthTokenEndpointRateLimitBurst = 10
	return cfg
}
