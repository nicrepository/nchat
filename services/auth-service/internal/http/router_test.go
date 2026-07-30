package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/health"
	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	platformlog "github.com/nicrepository/nchat/libs/go/platform/log"
	"github.com/nicrepository/nchat/services/auth-service/internal/config"
	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
	"github.com/nicrepository/nchat/services/auth-service/internal/service"
)

func TestHealthzContract(t *testing.T) {
	router := NewRouter(testConfig(), platformlog.New("auth-service", "test"), nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, RouteHealthz, nil))

	assertJSONResponse(t, response, http.StatusOK)
	body := decodeHealthEnvelope(t, response)
	if body.Data.Service != "auth-service" {
		t.Fatalf("expected service auth-service, got %q", body.Data.Service)
	}
	if body.Data.Probe != health.ProbeLiveness {
		t.Fatalf("expected liveness probe, got %q", body.Data.Probe)
	}
	if body.Data.Status != health.StatusOK {
		t.Fatalf("expected ok status, got %q", body.Data.Status)
	}
	if body.Data.Version != "0.0.0" {
		t.Fatalf("expected version 0.0.0, got %q", body.Data.Version)
	}
	if body.Data.Commit != "dev" {
		t.Fatalf("expected commit dev, got %q", body.Data.Commit)
	}
	assertRFC3339(t, body.Data.CheckedAt)
	if len(body.Data.Checks) != 0 {
		t.Fatalf("expected no liveness checks, got %d", len(body.Data.Checks))
	}
}

// readyUsersStub is a non-nil service.UserAdmin for readiness tests; its
// methods are never invoked by the probe.
type readyUsersStub struct{ service.UserAdmin }

// newFullyWiredRouter builds a router with every mandatory dependency
// present, mirroring a successful bootstrap.
func newFullyWiredRouter(t *testing.T) http.Handler {
	t.Helper()
	return NewRouter(jwtTestConfig(), platformlog.New("auth-service", "test"),
		readyUsersStub{}, routerAuthStub{}, routerLoginStub{}, nil, nil, nil, routerSessionStub{}, nil, nil, nil)
}

// TestReadyzContract: a partially initialized instance (no DB, no login,
// no session manager) must never report Ready — Kubernetes keeps it out of
// the Endpoints and the previous pod continues serving.
func TestReadyzContract(t *testing.T) {
	router := NewRouter(testConfig(), platformlog.New("auth-service", "test"), nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, RouteReadyz, nil))

	assertJSONResponse(t, response, http.StatusServiceUnavailable)
	body := decodeHealthEnvelope(t, response)
	if body.Data.Service != "auth-service" {
		t.Fatalf("expected service auth-service, got %q", body.Data.Service)
	}
	if body.Data.Probe != health.ProbeReadiness {
		t.Fatalf("expected readiness probe, got %q", body.Data.Probe)
	}
	if body.Data.Status != health.StatusUnready {
		t.Fatalf("expected unready status, got %q", body.Data.Status)
	}
	if body.Data.Version != "0.0.0" {
		t.Fatalf("expected version 0.0.0, got %q", body.Data.Version)
	}
	if body.Data.Commit != "dev" {
		t.Fatalf("expected commit dev, got %q", body.Data.Commit)
	}
	assertRFC3339(t, body.Data.CheckedAt)
	assertReadinessCheck(t, body.Data.Checks, "service-bootstrap")
	assertReadinessCheck(t, body.Data.Checks, "config-loaded")
	assertReadinessCheckFails(t, body.Data.Checks, "database")
	assertReadinessCheckFails(t, body.Data.Checks, "login-manager")
	assertReadinessCheckFails(t, body.Data.Checks, "session-manager")
}

func TestReadyzReadyWhenAllMandatoryDependenciesWired(t *testing.T) {
	router := newFullyWiredRouter(t)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, RouteReadyz, nil))

	assertJSONResponse(t, response, http.StatusOK)
	body := decodeHealthEnvelope(t, response)
	if body.Data.Status != health.StatusReady {
		t.Fatalf("expected ready status, got %q", body.Data.Status)
	}
	for _, name := range []string{"database", "jwt-token-manager", "login-manager", "session-manager"} {
		assertReadinessCheck(t, body.Data.Checks, name)
	}
}

// TestReadyzUnreadyWhenJWTConfigInvalid: dependencies wired but the JWT
// secret is missing → the token manager cannot be built and readiness must
// stay failing.
func TestReadyzUnreadyWhenJWTConfigInvalid(t *testing.T) {
	router := NewRouter(testConfig(), platformlog.New("auth-service", "test"),
		readyUsersStub{}, routerAuthStub{}, routerLoginStub{}, nil, nil, nil, routerSessionStub{}, nil, nil, nil)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, RouteReadyz, nil))

	assertJSONResponse(t, response, http.StatusServiceUnavailable)
	body := decodeHealthEnvelope(t, response)
	if body.Data.Status != health.StatusUnready {
		t.Fatalf("expected unready status, got %q", body.Data.Status)
	}
	assertReadinessCheckFails(t, body.Data.Checks, "jwt-token-manager")
	assertReadinessCheck(t, body.Data.Checks, "database")
}

// TestReadyzSecretAbsentFailsJWTAndDependentChecks: database up (stub) but
// AUTH_JWT_HMAC_SECRET empty — the token manager is never built, so
// jwt-token-manager must fail (not silently pass via a zero-value error) and
// the JWT-dependent managers, unwired as in app.New, fail with it. database
// must stay pass: it is an independent signal.
func TestReadyzSecretAbsentFailsJWTAndDependentChecks(t *testing.T) {
	cfg := testConfig() // AuthJWTHMACSecret empty
	router := NewRouter(cfg, platformlog.New("auth-service", "test"),
		readyUsersStub{}, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, RouteReadyz, nil))

	assertJSONResponse(t, response, http.StatusServiceUnavailable)
	body := decodeHealthEnvelope(t, response)
	assertReadinessCheck(t, body.Data.Checks, "database")
	assertReadinessCheckFails(t, body.Data.Checks, "jwt-token-manager")
	assertReadinessCheckFails(t, body.Data.Checks, "login-manager")
	assertReadinessCheckFails(t, body.Data.Checks, "session-manager")
}

// TestReadyzSecretTooShortFailsJWTCheck: a secret shorter than the
// TokenManager minimum must fail the jwt-token-manager check while database
// stays pass. The value is a deliberately invalid test literal.
func TestReadyzSecretTooShortFailsJWTCheck(t *testing.T) {
	cfg := jwtTestConfig()
	cfg.AuthJWTHMACSecret = "too-short" //nolint:gosec
	router := NewRouter(cfg, platformlog.New("auth-service", "test"),
		readyUsersStub{}, routerAuthStub{}, routerLoginStub{}, nil, nil, nil, routerSessionStub{}, nil, nil, nil)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, RouteReadyz, nil))

	assertJSONResponse(t, response, http.StatusServiceUnavailable)
	body := decodeHealthEnvelope(t, response)
	assertReadinessCheck(t, body.Data.Checks, "database")
	assertReadinessCheckFails(t, body.Data.Checks, "jwt-token-manager")
}

// TestLoginNotPublishedByPartiallyInitializedInstance: the same instance
// that reports unready must also refuse login instead of exposing a
// half-wired endpoint.
func TestLoginNotPublishedByPartiallyInitializedInstance(t *testing.T) {
	router := NewRouter(testConfig(), platformlog.New("auth-service", "test"), nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)

	ready := httptest.NewRecorder()
	router.ServeHTTP(ready, httptest.NewRequest(http.MethodGet, RouteReadyz, nil))
	if ready.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected readyz 503, got %d", ready.Code)
	}

	login := httptest.NewRecorder()
	router.ServeHTTP(login, httptest.NewRequest(http.MethodPost, RouteAuthLogin, strings.NewReader(`{"email":"a@b.c","password":"x"}`)))
	if login.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected login 503 on partially initialized instance, got %d", login.Code)
	}
}

func TestVersionRouteStillWorks(t *testing.T) {
	router := NewRouter(testConfig(), platformlog.New("auth-service", "test"), nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, RouteVersion, nil))

	assertJSONResponse(t, response, http.StatusOK)
	var body struct {
		Data map[string]string `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode version response: %v", err)
	}
	if body.Data["service"] != "auth-service" || body.Data["version"] != "0.0.0" || body.Data["commit"] != "dev" {
		t.Fatalf("unexpected version response: %+v", body.Data)
	}
}

func TestMethodAndNotFoundBehavior(t *testing.T) {
	router := NewRouter(testConfig(), platformlog.New("auth-service", "test"), nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)

	tests := []struct {
		name   string
		method string
		path   string
		want   int
	}{
		{name: "post healthz", method: http.MethodPost, path: RouteHealthz, want: http.StatusMethodNotAllowed},
		{name: "post readyz", method: http.MethodPost, path: RouteReadyz, want: http.StatusMethodNotAllowed},
		{name: "missing", method: http.MethodGet, path: "/missing", want: http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(tt.method, tt.path, nil))
			assertJSONResponse(t, response, tt.want)
		})
	}
}

type healthEnvelope struct {
	Data health.Response `json:"data"`
}

func decodeHealthEnvelope(t *testing.T, response *httptest.ResponseRecorder) healthEnvelope {
	t.Helper()

	var generic httputil.Envelope
	if err := json.Unmarshal(response.Body.Bytes(), &generic); err != nil {
		t.Fatalf("decode generic envelope: %v", err)
	}
	if generic.Error != nil {
		t.Fatalf("expected data envelope, got error %+v", generic.Error)
	}

	var body healthEnvelope
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode health envelope: %v", err)
	}
	return body
}

func assertJSONResponse(t *testing.T, response *httptest.ResponseRecorder, wantStatus int) {
	t.Helper()

	if response.Code != wantStatus {
		t.Fatalf("expected status %d, got %d", wantStatus, response.Code)
	}
	if response.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("expected application/json content type, got %q", response.Header().Get("Content-Type"))
	}
	if response.Header().Get("X-Request-ID") == "" {
		t.Fatal("expected X-Request-ID")
	}
	if response.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("expected X-Content-Type-Options nosniff, got %q", response.Header().Get("X-Content-Type-Options"))
	}
}

func assertReadinessCheck(t *testing.T, checks []health.CheckResult, name string) {
	t.Helper()

	for _, check := range checks {
		if check.Name == name {
			if check.Status != health.CheckPass {
				t.Fatalf("expected %s to pass, got %q", name, check.Status)
			}
			if !check.Critical {
				t.Fatalf("expected %s to be critical", name)
			}
			if check.DurationMS < 0 {
				t.Fatalf("expected %s duration to be non-negative, got %d", name, check.DurationMS)
			}
			return
		}
	}
	t.Fatalf("expected readiness check %s in %+v", name, checks)
}

func assertReadinessCheckFails(t *testing.T, checks []health.CheckResult, name string) {
	t.Helper()

	for _, check := range checks {
		if check.Name == name {
			if check.Status != health.CheckFail {
				t.Fatalf("expected %s to fail, got %q", name, check.Status)
			}
			if !check.Critical {
				t.Fatalf("expected %s to be critical", name)
			}
			return
		}
	}
	t.Fatalf("expected readiness check %s in %+v", name, checks)
}

func assertRFC3339(t *testing.T, value string) {
	t.Helper()

	if _, err := time.Parse(time.RFC3339, value); err != nil {
		t.Fatalf("expected RFC3339 timestamp, got %q: %v", value, err)
	}
}

func testConfig() config.Config {
	return config.Config{
		ServiceName: "auth-service", Env: "test", Port: 8081, ReadHeaderTimeoutSeconds: 5,
		// The invite budget and its Retry-After come from config.
		// Leaving them zero would silently disable both, so the router tests
		// would assert against a limiter that is not actually running.
		AuthInviteRateLimitPerActor:      10,
		AuthInviteRateLimitWindowMinutes: 10,
		AuthInviteRateLimitPerIPPerHour:  30,
	}
}

func TestMetricsRouteReturns200(t *testing.T) {
	router := NewRouter(testConfig(), platformlog.New("auth-service", "test"), nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, RouteMetrics, nil))

	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", response.Code)
	}
	body := response.Body.String()
	if body == "" {
		t.Fatal("expected non-empty metrics body")
	}
}

func TestAdminUsersDisabledWithNoToken(t *testing.T) {
	router := NewRouter(testConfig(), platformlog.New("auth-service", "test"), nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, RouteAdminUsers, nil))
	// testConfig has no ADMIN_BOOTSTRAP_TOKEN => 503
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
}

func TestAdminUsersMethodNotAllowed(t *testing.T) {
	router := NewRouter(testConfig(), platformlog.New("auth-service", "test"), nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, RouteAdminUsers, nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
}

// ── PATCH /admin/users/{id}/status router-level tests ─────────────────────

func TestAdminUserStatus_NoToken_Returns503(t *testing.T) {
	// testConfig has no ADMIN_BOOTSTRAP_TOKEN → token is empty → guard returns 503
	router := NewRouter(testConfig(), platformlog.New("auth-service", "test"), nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/admin/users/user-1/status", strings.NewReader(`{"status":"suspended"}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when no bootstrap token configured, got %d — %s", rec.Code, rec.Body.String())
	}
}

func TestAdminUserStatus_WrongToken_Returns401(t *testing.T) {
	cfg := testConfig()
	cfg.AdminBootstrapToken = "correct-token"
	router := NewRouter(cfg, platformlog.New("auth-service", "test"), nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/admin/users/user-1/status", strings.NewReader(`{"status":"suspended"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-NChat-Admin-Token", "wrong-token")
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for wrong bootstrap token, got %d — %s", rec.Code, rec.Body.String())
	}
}

func TestAdminUserStatus_BearerOnly_Returns401(t *testing.T) {
	// Only a Bearer JWT (no admin token) must be rejected
	cfg := testConfig()
	cfg.AdminBootstrapToken = "correct-token"
	router := NewRouter(cfg, platformlog.New("auth-service", "test"), nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/admin/users/user-1/status", strings.NewReader(`{"status":"suspended"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer some.jwt.token")
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for bearer-only request, got %d — %s", rec.Code, rec.Body.String())
	}
}

func TestAdminUserStatus_MethodNotAllowed_Returns405(t *testing.T) {
	router := NewRouter(testConfig(), platformlog.New("auth-service", "test"), nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/users/user-1/status", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 for GET on status route, got %d", rec.Code)
	}
}

func TestAdminUserStatus_NoService_Returns503(t *testing.T) {
	// Service nil + correct token → 503 (service unavailable from handler)
	cfg := testConfig()
	cfg.AdminBootstrapToken = "correct-token"
	router := NewRouter(cfg, platformlog.New("auth-service", "test"), nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/admin/users/user-1/status", strings.NewReader(`{"status":"suspended"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-NChat-Admin-Token", "correct-token")
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when service nil, got %d — %s", rec.Code, rec.Body.String())
	}
}

func TestAuthMeLoginAttemptsDisabledReturns503BeforeAuth(t *testing.T) {
	cfg := testConfig()
	cfg.AuthJWTHMACSecret = strings.Repeat("r", 32)
	cfg.AuthJWTIssuer = "test-issuer"
	cfg.AuthJWTAudience = "test-audience"
	cfg.AuthAccessTokenTTLSeconds = 900
	cfg.AuthRefreshTokenTTLSeconds = 3600

	router := NewRouter(cfg, platformlog.New("auth-service", "test"), nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, RouteAuthMeLoginAttempts, nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d body=%s", rec.Code, rec.Body.String())
	}
}

type routerAuthStub struct{}

func (routerAuthStub) Refresh(_ context.Context, _ string) (domain.TokenPair, error) {
	return domain.TokenPair{AccessToken: makeInternalTestOpaqueValue("router-auth-access"), RefreshToken: makeInternalTestOpaqueValue("router-auth-refresh"), TokenType: "Bearer", ExpiresIn: 900}, nil
}

func (routerAuthStub) Logout(_ context.Context, _ string) error {
	return nil
}

func TestAuthTokenEndpointRateLimiterAllowsRequestsUnderLimit(t *testing.T) {
	cfg := testConfig()
	cfg.AuthTokenEndpointRateLimitPerMinute = 60
	cfg.AuthTokenEndpointRateLimitBurst = 2
	router := NewRouter(cfg, platformlog.New("auth-service", "test"), nil, routerAuthStub{}, nil, nil, nil, nil, nil, nil, nil, nil)

	submitted := makeInternalTestOpaqueValue("router-rate-limit-refresh")
	for i := 0; i < 2; i++ {
		response := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, RouteAuthRefresh, nil)
		req.AddCookie(&http.Cookie{
			Name:     "nchat_rt",
			Value:    submitted,
			Secure:   true,
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
		})
		req.RemoteAddr = "203.0.113.10:12345"

		router.ServeHTTP(response, req)

		assertJSONResponse(t, response, http.StatusOK)
	}
}

func TestAuthTokenEndpointRateLimiterRejectsRequestsOverLimit(t *testing.T) {
	cfg := testConfig()
	cfg.AuthTokenEndpointRateLimitPerMinute = 60
	cfg.AuthTokenEndpointRateLimitBurst = 1
	router := NewRouter(cfg, platformlog.New("auth-service", "test"), nil, routerAuthStub{}, nil, nil, nil, nil, nil, nil, nil, nil)

	submitted := makeInternalTestOpaqueValue("router-rate-limit-secret")
	first := httptest.NewRecorder()
	firstReq := httptest.NewRequest(http.MethodPost, RouteAuthRefresh, nil)
	firstReq.AddCookie(&http.Cookie{
		Name:     "nchat_rt",
		Value:    submitted,
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	firstReq.RemoteAddr = "203.0.113.20:12345"
	router.ServeHTTP(first, firstReq)
	assertJSONResponse(t, first, http.StatusOK)

	second := httptest.NewRecorder()
	secondReq := httptest.NewRequest(http.MethodPost, RouteAuthLogout, nil)
	secondReq.RemoteAddr = "203.0.113.20:12345"
	router.ServeHTTP(second, secondReq)

	assertJSONResponse(t, second, http.StatusTooManyRequests)
	if strings.Contains(second.Body.String(), submitted) {
		t.Fatalf("rate limit response must not include refresh token: %s", second.Body.String())
	}
}

func TestAuthRoutesMethodNotAllowed(t *testing.T) {
	router := NewRouter(testConfig(), platformlog.New("auth-service", "test"), nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)

	tests := []struct {
		name string
		path string
	}{
		{name: "refresh", path: RouteAuthRefresh},
		{name: "logout", path: RouteAuthLogout},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, tt.path, nil))
			assertJSONResponse(t, response, http.StatusMethodNotAllowed)
		})
	}
}

type routerLoginStub struct{}

func (routerLoginStub) Login(_ context.Context, _ domain.LoginInput) (domain.LoginResult, error) {
	return domain.LoginResult{
		AccessToken:  makeInternalTestOpaqueValue("router-login-access"),
		RefreshToken: makeInternalTestOpaqueValue("router-login-refresh"),
		TokenType:    "Bearer",
		ExpiresIn:    900,
		User:         domain.LoginUser{ID: "user-1", Email: "user@example.com", DisplayName: "User"},
	}, nil
}

func TestAuthLoginMethodNotAllowed(t *testing.T) {
	router := NewRouter(testConfig(), platformlog.New("auth-service", "test"), nil, nil, routerLoginStub{}, nil, nil, nil, nil, nil, nil, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, RouteAuthLogin, nil))
	assertJSONResponse(t, rec, http.StatusMethodNotAllowed)
}

func TestAuthLoginRateLimiterAllowsRequestsUnderLimit(t *testing.T) {
	cfg := testConfig()
	cfg.AuthTokenEndpointRateLimitPerMinute = 60
	cfg.AuthTokenEndpointRateLimitBurst = 2
	router := NewRouter(cfg, platformlog.New("auth-service", "test"), nil, nil, routerLoginStub{}, nil, nil, nil, nil, nil, nil, nil)

	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, RouteAuthLogin, strings.NewReader(`{"email":"user@example.com","password":"Pass@123"}`))
		req.RemoteAddr = "203.0.113.30:12345"
		router.ServeHTTP(rec, req)
		assertJSONResponse(t, rec, http.StatusOK)
	}
}

func TestAuthLoginRateLimiterRejectsRequestsOverLimit(t *testing.T) {
	cfg := testConfig()
	cfg.AuthTokenEndpointRateLimitPerMinute = 60
	cfg.AuthTokenEndpointRateLimitBurst = 1
	router := NewRouter(cfg, platformlog.New("auth-service", "test"), nil, nil, routerLoginStub{}, nil, nil, nil, nil, nil, nil, nil)

	first := httptest.NewRecorder()
	firstReq := httptest.NewRequest(http.MethodPost, RouteAuthLogin, strings.NewReader(`{"email":"user@example.com","password":"Pass@123"}`))
	firstReq.RemoteAddr = "203.0.113.40:12345"
	router.ServeHTTP(first, firstReq)
	assertJSONResponse(t, first, http.StatusOK)

	second := httptest.NewRecorder()
	secondReq := httptest.NewRequest(http.MethodPost, RouteAuthLogin, strings.NewReader(`{"email":"user@example.com","password":"Pass@123"}`))
	secondReq.RemoteAddr = "203.0.113.40:12345"
	router.ServeHTTP(second, secondReq)
	assertJSONResponse(t, second, http.StatusTooManyRequests)
}

// TestRateLimiter_NoTrustedProxy_IgnoresXForwardedFor verifies that when no
// trusted proxy CIDRs are configured, X-Forwarded-For is ignored and
// RemoteAddr is used for rate-limiting.
func TestRateLimiter_NoTrustedProxy_IgnoresXForwardedFor(t *testing.T) {
	cfg := testConfig()
	cfg.AuthTokenEndpointRateLimitPerMinute = 60
	cfg.AuthTokenEndpointRateLimitBurst = 1
	cfg.AuthTrustedProxyCIDRs = "" // no trusted proxies
	router := NewRouter(cfg, platformlog.New("auth-service", "test"), nil, nil, routerLoginStub{}, nil, nil, nil, nil, nil, nil, nil)

	// First request from 10.0.0.1 — allowed.
	first := httptest.NewRecorder()
	firstReq := httptest.NewRequest(http.MethodPost, RouteAuthLogin, strings.NewReader(`{"email":"u@e.com","password":"P@ss1"}`))
	firstReq.RemoteAddr = "10.0.0.1:1111"
	firstReq.Header.Set("X-Forwarded-For", "203.0.113.50") // should be ignored
	router.ServeHTTP(first, firstReq)
	assertJSONResponse(t, first, http.StatusOK)

	// Second request from same RemoteAddr — blocked (XFF has a different IP but it's ignored).
	second := httptest.NewRecorder()
	secondReq := httptest.NewRequest(http.MethodPost, RouteAuthLogin, strings.NewReader(`{"email":"u@e.com","password":"P@ss1"}`))
	secondReq.RemoteAddr = "10.0.0.1:1111"
	secondReq.Header.Set("X-Forwarded-For", "203.0.113.99")
	router.ServeHTTP(second, secondReq)
	assertJSONResponse(t, second, http.StatusTooManyRequests)
}

// TestRateLimiter_TrustedProxy_UsesXForwardedFor verifies that when the
// request RemoteAddr is inside a configured trusted-proxy CIDR, the leftmost
// IP from X-Forwarded-For is used as the rate-limit key.
func TestRateLimiter_TrustedProxy_UsesXForwardedFor(t *testing.T) {
	cfg := testConfig()
	cfg.AuthTokenEndpointRateLimitPerMinute = 60
	cfg.AuthTokenEndpointRateLimitBurst = 1
	cfg.AuthTrustedProxyCIDRs = "10.0.0.0/8"
	router := NewRouter(cfg, platformlog.New("auth-service", "test"), nil, nil, routerLoginStub{}, nil, nil, nil, nil, nil, nil, nil)

	proxyAddr := "10.0.0.1:9999"

	// First request from client 203.0.113.1 — allowed.
	first := httptest.NewRecorder()
	firstReq := httptest.NewRequest(http.MethodPost, RouteAuthLogin, strings.NewReader(`{"email":"u@e.com","password":"P@ss1"}`))
	firstReq.RemoteAddr = proxyAddr
	firstReq.Header.Set("X-Forwarded-For", "203.0.113.1")
	router.ServeHTTP(first, firstReq)
	assertJSONResponse(t, first, http.StatusOK)

	// Second request from a different client IP — allowed (fresh bucket).
	second := httptest.NewRecorder()
	secondReq := httptest.NewRequest(http.MethodPost, RouteAuthLogin, strings.NewReader(`{"email":"u@e.com","password":"P@ss1"}`))
	secondReq.RemoteAddr = proxyAddr
	secondReq.Header.Set("X-Forwarded-For", "203.0.113.2")
	router.ServeHTTP(second, secondReq)
	assertJSONResponse(t, second, http.StatusOK)

	// Third request from client 203.0.113.1 again — blocked (bucket exhausted).
	third := httptest.NewRecorder()
	thirdReq := httptest.NewRequest(http.MethodPost, RouteAuthLogin, strings.NewReader(`{"email":"u@e.com","password":"P@ss1"}`))
	thirdReq.RemoteAddr = proxyAddr
	thirdReq.Header.Set("X-Forwarded-For", "203.0.113.1")
	router.ServeHTTP(third, thirdReq)
	assertJSONResponse(t, third, http.StatusTooManyRequests)
}

// TestRateLimiter_UntrustedRemoteAddr_IgnoresXForwardedFor verifies that when
// RemoteAddr is NOT inside any configured trusted-proxy CIDR, X-Forwarded-For
// is ignored even when trusted CIDRs are present.
func TestRateLimiter_UntrustedRemoteAddr_IgnoresXForwardedFor(t *testing.T) {
	cfg := testConfig()
	cfg.AuthTokenEndpointRateLimitPerMinute = 60
	cfg.AuthTokenEndpointRateLimitBurst = 1
	cfg.AuthTrustedProxyCIDRs = "10.0.0.0/8" // 5.5.5.5 is NOT in this range
	router := NewRouter(cfg, platformlog.New("auth-service", "test"), nil, nil, routerLoginStub{}, nil, nil, nil, nil, nil, nil, nil)

	// First request from 5.5.5.5 — allowed.
	first := httptest.NewRecorder()
	firstReq := httptest.NewRequest(http.MethodPost, RouteAuthLogin, strings.NewReader(`{"email":"u@e.com","password":"P@ss1"}`))
	firstReq.RemoteAddr = "5.5.5.5:1234"
	firstReq.Header.Set("X-Forwarded-For", "203.0.113.1")
	router.ServeHTTP(first, firstReq)
	assertJSONResponse(t, first, http.StatusOK)

	// Second request from same 5.5.5.5 with a different XFF — blocked (RemoteAddr is the key).
	second := httptest.NewRecorder()
	secondReq := httptest.NewRequest(http.MethodPost, RouteAuthLogin, strings.NewReader(`{"email":"u@e.com","password":"P@ss1"}`))
	secondReq.RemoteAddr = "5.5.5.5:1234"
	secondReq.Header.Set("X-Forwarded-For", "203.0.113.2")
	router.ServeHTTP(second, secondReq)
	assertJSONResponse(t, second, http.StatusTooManyRequests)
}

// TestRateLimiter_TrustedProxy_UsesXRealIPWhenXFFAbsent verifies that when a trusted
// proxy is configured, XFF is absent, and X-Real-IP contains a valid IP, the limiter
// uses the canonicalized X-Real-IP as the rate-limit key.
func TestRateLimiter_TrustedProxy_UsesXRealIPWhenXFFAbsent(t *testing.T) {
	cfg := testConfig()
	cfg.AuthTokenEndpointRateLimitPerMinute = 60
	cfg.AuthTokenEndpointRateLimitBurst = 1
	cfg.AuthTrustedProxyCIDRs = "10.0.0.0/8"
	router := NewRouter(cfg, platformlog.New("auth-service", "test"), nil, nil, routerLoginStub{}, nil, nil, nil, nil, nil, nil, nil)

	proxyAddr := "10.0.0.5:9999"
	clientXRI := "203.0.113.77"

	// First request from client via X-Real-IP — allowed.
	first := httptest.NewRecorder()
	firstReq := httptest.NewRequest(http.MethodPost, RouteAuthLogin, strings.NewReader(`{"email":"u@e.com","password":"P@ss1"}`))
	firstReq.RemoteAddr = proxyAddr
	firstReq.Header.Set("X-Real-IP", clientXRI)
	router.ServeHTTP(first, firstReq)
	assertJSONResponse(t, first, http.StatusOK)

	// Second request from same X-Real-IP — bucket exhausted → blocked.
	second := httptest.NewRecorder()
	secondReq := httptest.NewRequest(http.MethodPost, RouteAuthLogin, strings.NewReader(`{"email":"u@e.com","password":"P@ss1"}`))
	secondReq.RemoteAddr = proxyAddr
	secondReq.Header.Set("X-Real-IP", clientXRI)
	router.ServeHTTP(second, secondReq)
	assertJSONResponse(t, second, http.StatusTooManyRequests)

	// Third request from a different X-Real-IP — fresh bucket → allowed.
	third := httptest.NewRecorder()
	thirdReq := httptest.NewRequest(http.MethodPost, RouteAuthLogin, strings.NewReader(`{"email":"u@e.com","password":"P@ss1"}`))
	thirdReq.RemoteAddr = proxyAddr
	thirdReq.Header.Set("X-Real-IP", "203.0.113.78")
	router.ServeHTTP(third, thirdReq)
	assertJSONResponse(t, third, http.StatusOK)
}

// TestRateLimiter_InvalidXFF_FallsBackToRemoteAddr verifies that when a trusted proxy
// is configured but X-Forwarded-For contains a non-IP string, the limiter falls back
// to RemoteAddr and never uses the raw invalid header value as a limiter key.
func TestRateLimiter_InvalidXFF_FallsBackToRemoteAddr(t *testing.T) {
	cfg := testConfig()
	cfg.AuthTokenEndpointRateLimitPerMinute = 60
	cfg.AuthTokenEndpointRateLimitBurst = 1
	cfg.AuthTrustedProxyCIDRs = "10.0.0.0/8"
	router := NewRouter(cfg, platformlog.New("auth-service", "test"), nil, nil, routerLoginStub{}, nil, nil, nil, nil, nil, nil, nil)

	// First request from trusted proxy — XFF is "not-an-ip", falls back to RemoteAddr "10.0.0.2".
	first := httptest.NewRecorder()
	firstReq := httptest.NewRequest(http.MethodPost, RouteAuthLogin, strings.NewReader(`{"email":"u@e.com","password":"P@ss1"}`))
	firstReq.RemoteAddr = "10.0.0.2:1234"
	firstReq.Header.Set("X-Forwarded-For", "not-an-ip")
	router.ServeHTTP(first, firstReq)
	assertJSONResponse(t, first, http.StatusOK)

	// Second request from same RemoteAddr (different invalid XFF) — same key → blocked.
	second := httptest.NewRecorder()
	secondReq := httptest.NewRequest(http.MethodPost, RouteAuthLogin, strings.NewReader(`{"email":"u@e.com","password":"P@ss1"}`))
	secondReq.RemoteAddr = "10.0.0.2:1234"
	secondReq.Header.Set("X-Forwarded-For", "also-not-an-ip")
	router.ServeHTTP(second, secondReq)
	assertJSONResponse(t, second, http.StatusTooManyRequests)
}

// TestRateLimiter_MalformedXRealIP_FallsBackToRemoteAddr verifies that when a trusted
// proxy is configured, no XFF is present, and X-Real-IP contains a non-IP string,
// the limiter falls back to RemoteAddr.
func TestRateLimiter_MalformedXRealIP_FallsBackToRemoteAddr(t *testing.T) {
	cfg := testConfig()
	cfg.AuthTokenEndpointRateLimitPerMinute = 60
	cfg.AuthTokenEndpointRateLimitBurst = 1
	cfg.AuthTrustedProxyCIDRs = "10.0.0.0/8"
	router := NewRouter(cfg, platformlog.New("auth-service", "test"), nil, nil, routerLoginStub{}, nil, nil, nil, nil, nil, nil, nil)

	// First request from trusted proxy — X-Real-IP is invalid, falls back to RemoteAddr "10.0.0.3".
	first := httptest.NewRecorder()
	firstReq := httptest.NewRequest(http.MethodPost, RouteAuthLogin, strings.NewReader(`{"email":"u@e.com","password":"P@ss1"}`))
	firstReq.RemoteAddr = "10.0.0.3:2345"
	firstReq.Header.Set("X-Real-IP", "not-an-ip-either")
	router.ServeHTTP(first, firstReq)
	assertJSONResponse(t, first, http.StatusOK)

	// Second request from same RemoteAddr — same key (RemoteAddr) → blocked.
	second := httptest.NewRecorder()
	secondReq := httptest.NewRequest(http.MethodPost, RouteAuthLogin, strings.NewReader(`{"email":"u@e.com","password":"P@ss1"}`))
	secondReq.RemoteAddr = "10.0.0.3:2345"
	secondReq.Header.Set("X-Real-IP", "still-not-an-ip")
	router.ServeHTTP(second, secondReq)
	assertJSONResponse(t, second, http.StatusTooManyRequests)
}

// TestRateLimiter_MalformedXFF_FallsBackToRemoteAddr verifies that when a trusted proxy
// CIDR is configured but X-Forwarded-For contains an empty/malformed first element,
// the limiter falls back to RemoteAddr as the rate-limit key.
func TestRateLimiter_MalformedXFF_FallsBackToRemoteAddr(t *testing.T) {
	cfg := testConfig()
	cfg.AuthTokenEndpointRateLimitPerMinute = 60
	cfg.AuthTokenEndpointRateLimitBurst = 1
	cfg.AuthTrustedProxyCIDRs = "10.0.0.0/8"
	router := NewRouter(cfg, platformlog.New("auth-service", "test"), nil, nil, routerLoginStub{}, nil, nil, nil, nil, nil, nil, nil)

	// Trusted proxy at 10.0.0.1 with a malformed XFF (empty first element after split).
	// clientIP() falls back to RemoteAddr = "10.0.0.1".
	first := httptest.NewRecorder()
	firstReq := httptest.NewRequest(http.MethodPost, RouteAuthLogin, strings.NewReader(`{"email":"u@e.com","password":"P@ss1"}`))
	firstReq.RemoteAddr = "10.0.0.1:5555"
	firstReq.Header.Set("X-Forwarded-For", ",,,") // malformed: first element is empty
	router.ServeHTTP(first, firstReq)
	assertJSONResponse(t, first, http.StatusOK)

	// Second request from same RemoteAddr with malformed XFF — same key (RemoteAddr) → blocked.
	second := httptest.NewRecorder()
	secondReq := httptest.NewRequest(http.MethodPost, RouteAuthLogin, strings.NewReader(`{"email":"u@e.com","password":"P@ss1"}`))
	secondReq.RemoteAddr = "10.0.0.1:5555"
	secondReq.Header.Set("X-Forwarded-For", ",,,")
	router.ServeHTTP(second, secondReq)
	assertJSONResponse(t, second, http.StatusTooManyRequests)
}

type routerPasswordRecoveryStub struct {
	forgotCalls int
	resetCalls  int
}

func (s *routerPasswordRecoveryStub) ForgotPassword(_ context.Context, _ domain.ForgotPasswordInput) error {
	s.forgotCalls++
	return nil
}

func (s *routerPasswordRecoveryStub) ResetPassword(_ context.Context, _ domain.ResetPasswordInput) error {
	s.resetCalls++
	return nil
}

type routerInviteStub struct {
	acceptCalls int
}

func (s *routerInviteStub) CreateBootstrapInvite(context.Context, domain.BootstrapInviteInput) (domain.InviteResult, error) {
	return domain.InviteResult{}, nil
}

func (s *routerInviteStub) CreateInvite(_ context.Context, _ domain.AdminInviteInput) (domain.InviteResult, error) {
	return domain.InviteResult{}, nil
}

func (s *routerInviteStub) AcceptInvite(_ context.Context, _ domain.AcceptInviteInput) (domain.AcceptInviteResult, error) {
	s.acceptCalls++
	return domain.AcceptInviteResult{UserID: "user-1", Email: "user@example.com", DisplayName: "User", CreatedAt: time.Now()}, nil
}

type unavailableRouterPasswordRecoveryStub struct{}

func (unavailableRouterPasswordRecoveryStub) EmailHandoffAvailable() bool {
	return false
}

func (unavailableRouterPasswordRecoveryStub) ForgotPassword(context.Context, domain.ForgotPasswordInput) error {
	return domain.ErrEmailOutboxUnavailable
}

func (unavailableRouterPasswordRecoveryStub) ResetPassword(context.Context, domain.ResetPasswordInput) error {
	return nil
}

func TestForgotPasswordMissingOutboxKeyReturns503BeforeRateLimit(t *testing.T) {
	cfg := testConfig()
	cfg.AuthJWTHMACSecret = strings.Repeat("r", 32)
	cfg.AuthTokenEndpointRateLimitPerMinute = 60
	cfg.AuthTokenEndpointRateLimitBurst = 1
	router := NewRouter(cfg, platformlog.New("auth-service", "test"), nil, nil, nil, unavailableRouterPasswordRecoveryStub{}, nil, nil, nil, nil, nil, nil)

	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, RouteAuthPasswordForgot, strings.NewReader(`{"email":"user@example.com"}`))
		req.RemoteAddr = "203.0.113.151:1000"
		router.ServeHTTP(rec, req)
		assertJSONResponse(t, rec, http.StatusServiceUnavailable)
	}
}

func TestRecoveryRateLimiterForgotPerEmailLimitTriggers429(t *testing.T) {
	cfg := testConfig()
	cfg.AuthJWTHMACSecret = strings.Repeat("r", 32)
	cfg.AuthTokenEndpointRateLimitPerMinute = 60
	cfg.AuthTokenEndpointRateLimitBurst = 1
	passwords := &routerPasswordRecoveryStub{}
	router := NewRouter(cfg, platformlog.New("auth-service", "test"), nil, nil, nil, passwords, nil, nil, nil, nil, nil, nil)

	first := httptest.NewRecorder()
	firstReq := httptest.NewRequest(http.MethodPost, RouteAuthPasswordForgot, strings.NewReader(`{"email":"USER@example.com"}`))
	firstReq.RemoteAddr = "203.0.113.101:1000"
	router.ServeHTTP(first, firstReq)
	if first.Code != http.StatusAccepted {
		t.Fatalf("expected first forgot request 202, got %d body=%s", first.Code, first.Body.String())
	}

	second := httptest.NewRecorder()
	secondReq := httptest.NewRequest(http.MethodPost, RouteAuthPasswordForgot, strings.NewReader(`{"email":" user@example.com "}`))
	secondReq.RemoteAddr = "203.0.113.102:1000"
	router.ServeHTTP(second, secondReq)
	assertJSONResponse(t, second, http.StatusTooManyRequests)
	if strings.Contains(second.Body.String(), "user@example.com") {
		t.Fatalf("rate limit response must not include target email: %s", second.Body.String())
	}
	if passwords.forgotCalls != 1 {
		t.Fatalf("expected target limiter to block before second service call, got %d calls", passwords.forgotCalls)
	}
}

func TestRecoveryRateLimiterResetPerTokenLimitTriggers429(t *testing.T) {
	cfg := testConfig()
	cfg.AuthJWTHMACSecret = strings.Repeat("r", 32)
	cfg.AuthTokenEndpointRateLimitPerMinute = 60
	cfg.AuthTokenEndpointRateLimitBurst = 1
	passwords := &routerPasswordRecoveryStub{}
	router := NewRouter(cfg, platformlog.New("auth-service", "test"), nil, nil, nil, passwords, nil, nil, nil, nil, nil, nil)
	token := strings.Repeat("a", 43)
	body := `{"token":"` + token + `","new_password":"StrongPassword@123"}`

	first := httptest.NewRecorder()
	firstReq := httptest.NewRequest(http.MethodPost, RouteAuthPasswordReset, strings.NewReader(body))
	firstReq.RemoteAddr = "203.0.113.111:1000"
	router.ServeHTTP(first, firstReq)
	if first.Code != http.StatusNoContent {
		t.Fatalf("expected first reset request 204, got %d body=%s", first.Code, first.Body.String())
	}

	second := httptest.NewRecorder()
	secondReq := httptest.NewRequest(http.MethodPost, RouteAuthPasswordReset, strings.NewReader(body))
	secondReq.RemoteAddr = "203.0.113.112:1000"
	router.ServeHTTP(second, secondReq)
	assertJSONResponse(t, second, http.StatusTooManyRequests)
	if strings.Contains(second.Body.String(), token) {
		t.Fatalf("rate limit response must not include token: %s", second.Body.String())
	}
	if passwords.resetCalls != 1 {
		t.Fatalf("expected target limiter to block before second service call, got %d calls", passwords.resetCalls)
	}
}

func TestRecoveryRateLimiterInviteAcceptPerTokenLimitTriggers429(t *testing.T) {
	cfg := testConfig()
	cfg.AuthJWTHMACSecret = strings.Repeat("r", 32)
	cfg.AuthTokenEndpointRateLimitPerMinute = 60
	cfg.AuthTokenEndpointRateLimitBurst = 1
	invites := &routerInviteStub{}
	router := NewRouter(cfg, platformlog.New("auth-service", "test"), nil, nil, nil, nil, invites, nil, nil, nil, nil, nil)
	token := strings.Repeat("b", 43)
	body := `{"token":"` + token + `","display_name":"User","password":"StrongPassword@123"}`

	first := httptest.NewRecorder()
	firstReq := httptest.NewRequest(http.MethodPost, RouteAuthInvitesAccept, strings.NewReader(body))
	firstReq.RemoteAddr = "203.0.113.121:1000"
	router.ServeHTTP(first, firstReq)
	if first.Code != http.StatusCreated {
		t.Fatalf("expected first invite accept request 201, got %d body=%s", first.Code, first.Body.String())
	}

	second := httptest.NewRecorder()
	secondReq := httptest.NewRequest(http.MethodPost, RouteAuthInvitesAccept, strings.NewReader(body))
	secondReq.RemoteAddr = "203.0.113.122:1000"
	router.ServeHTTP(second, secondReq)
	assertJSONResponse(t, second, http.StatusTooManyRequests)
	if strings.Contains(second.Body.String(), token) {
		t.Fatalf("rate limit response must not include token: %s", second.Body.String())
	}
	if invites.acceptCalls != 1 {
		t.Fatalf("expected target limiter to block before second service call, got %d calls", invites.acceptCalls)
	}
}

func TestRecoveryRateLimiterEndpointBucketsDoNotBlockEachOther(t *testing.T) {
	cfg := testConfig()
	cfg.AuthJWTHMACSecret = strings.Repeat("r", 32)
	cfg.AuthTokenEndpointRateLimitPerMinute = 60
	cfg.AuthTokenEndpointRateLimitBurst = 1
	passwords := &routerPasswordRecoveryStub{}
	invites := &routerInviteStub{}
	router := NewRouter(cfg, platformlog.New("auth-service", "test"), nil, nil, nil, passwords, invites, nil, nil, nil, nil, nil)
	remoteAddr := "203.0.113.131:1000"

	forgot := httptest.NewRecorder()
	forgotReq := httptest.NewRequest(http.MethodPost, RouteAuthPasswordForgot, strings.NewReader(`{"email":"one@example.com"}`))
	forgotReq.RemoteAddr = remoteAddr
	router.ServeHTTP(forgot, forgotReq)
	if forgot.Code != http.StatusAccepted {
		t.Fatalf("expected forgot request 202, got %d body=%s", forgot.Code, forgot.Body.String())
	}

	reset := httptest.NewRecorder()
	resetReq := httptest.NewRequest(http.MethodPost, RouteAuthPasswordReset, strings.NewReader(`{"token":"`+strings.Repeat("c", 43)+`","new_password":"StrongPassword@123"}`))
	resetReq.RemoteAddr = remoteAddr
	router.ServeHTTP(reset, resetReq)
	if reset.Code != http.StatusNoContent {
		t.Fatalf("expected reset request 204, got %d body=%s", reset.Code, reset.Body.String())
	}

	accept := httptest.NewRecorder()
	acceptReq := httptest.NewRequest(http.MethodPost, RouteAuthInvitesAccept, strings.NewReader(`{"token":"`+strings.Repeat("d", 43)+`","display_name":"User","password":"StrongPassword@123"}`))
	acceptReq.RemoteAddr = remoteAddr
	router.ServeHTTP(accept, acceptReq)
	if accept.Code != http.StatusCreated {
		t.Fatalf("expected invite accept request 201, got %d body=%s", accept.Code, accept.Body.String())
	}
}

func TestRecoveryRateLimiterMalformedResetTokenUsesGenericLimiter(t *testing.T) {
	cfg := testConfig()
	cfg.AuthJWTHMACSecret = strings.Repeat("r", 32)
	cfg.AuthTokenEndpointRateLimitPerMinute = 60
	cfg.AuthTokenEndpointRateLimitBurst = 1
	passwords := &routerPasswordRecoveryStub{}
	router := NewRouter(cfg, platformlog.New("auth-service", "test"), nil, nil, nil, passwords, nil, nil, nil, nil, nil, nil)

	first := httptest.NewRecorder()
	firstReq := httptest.NewRequest(http.MethodPost, RouteAuthPasswordReset, strings.NewReader(`{"token":"bad token","new_password":"StrongPassword@123"}`))
	firstReq.RemoteAddr = "203.0.113.141:1000"
	router.ServeHTTP(first, firstReq)
	if first.Code != http.StatusNoContent {
		t.Fatalf("expected first malformed token to reach service without panic, got %d body=%s", first.Code, first.Body.String())
	}

	second := httptest.NewRecorder()
	secondReq := httptest.NewRequest(http.MethodPost, RouteAuthPasswordReset, strings.NewReader(`{"token":"other bad token","new_password":"StrongPassword@123"}`))
	secondReq.RemoteAddr = "203.0.113.142:1000"
	router.ServeHTTP(second, secondReq)
	assertJSONResponse(t, second, http.StatusTooManyRequests)
	if strings.Contains(second.Body.String(), "bad token") {
		t.Fatalf("rate limit response must not include malformed token: %s", second.Body.String())
	}
	if passwords.resetCalls != 1 {
		t.Fatalf("expected generic malformed value target limiter to block second service call, got %d calls", passwords.resetCalls)
	}
}

// ── /auth/me/login-attempts active-session guard tests ────────────────────

// routerLoginAttemptsStub satisfies LoginAttemptsManager for router-level tests.
type routerLoginAttemptsStub struct{}

func (routerLoginAttemptsStub) GetMyAttempts(_ context.Context, _ string, _ int, _ string) ([]domain.LoginAttempt, string, error) {
	return nil, "", nil
}

// routerSessionStub satisfies SessionManager for router-level tests.
// ValidateErr controls what ValidateActiveSession returns.
type routerSessionStub struct {
	validateErr error
}

func (s routerSessionStub) ValidateActiveSession(_ context.Context, _, _ string) error {
	return s.validateErr
}
func (routerSessionStub) ListSessions(_ context.Context, _ string, _ bool, _ int) ([]domain.SessionInfo, error) {
	return nil, nil
}
func (routerSessionStub) RevokeSession(_ context.Context, _, _ string) error { return nil }
func (routerSessionStub) RevokeAllSessionsExcept(_ context.Context, _, _ string) error {
	return nil
}

// jwtTestConfig returns a router config with a valid JWT setup for active-session tests.
// Must use the same HMAC secret as makeRouterTokens so tokens validate correctly.
func jwtTestConfig() config.Config {
	cfg := testConfig()
	cfg.AuthJWTHMACSecret = strings.Repeat("a", 32)
	cfg.AuthJWTIssuer = "test-issuer"
	cfg.AuthJWTAudience = "test-audience"
	cfg.AuthAccessTokenTTLSeconds = 900
	cfg.AuthRefreshTokenTTLSeconds = 3600
	return cfg
}

// makeRouterTokens creates a TokenManager matching jwtTestConfig.
func makeRouterTokens(t *testing.T) *service.TokenManager {
	t.Helper()
	tokens, err := service.NewTokenManager(service.TokenConfig{
		HMACSecret: strings.Repeat("a", 32),
		Issuer:     "test-issuer",
		Audience:   "test-audience",
		AccessTTL:  15 * time.Minute,
		RefreshTTL: 7 * 24 * time.Hour,
	})
	if err != nil {
		t.Fatalf("create router token manager: %v", err)
	}
	return tokens
}

// mustAccessTokenForRouter generates a signed access token for router-level tests.
func mustAccessTokenForRouter(t *testing.T, userID, sessionID string) string {
	t.Helper()
	tok, _, err := makeRouterTokens(t).GenerateAccessToken(userID, sessionID)
	if err != nil {
		t.Fatalf("generate access token: %v", err)
	}
	return tok
}

func TestLoginAttempts_RequiresActiveSession_RevokedSessionReturns401(t *testing.T) {
	// Valid JWT, but session is revoked/suspended → active-session guard must reject.
	accessToken := mustAccessTokenForRouter(t, "user-1", "session-revoked")
	sessions := routerSessionStub{validateErr: domain.ErrInvalidToken}

	router := NewRouter(jwtTestConfig(), platformlog.New("auth-service", "test"),
		nil, nil, nil, nil, nil, routerLoginAttemptsStub{}, sessions, nil, nil, nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, RouteAuthMeLoginAttempts, nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for revoked session, got %d — %s", rec.Code, rec.Body.String())
	}
}

func TestLoginAttempts_RequiresActiveSession_MissingBearerReturns401(t *testing.T) {
	// No Authorization header — BearerAuth should reject before session check.
	sessions := routerSessionStub{validateErr: nil}
	router := NewRouter(jwtTestConfig(), platformlog.New("auth-service", "test"),
		nil, nil, nil, nil, nil, routerLoginAttemptsStub{}, sessions, nil, nil, nil)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, RouteAuthMeLoginAttempts, nil))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for missing bearer, got %d — %s", rec.Code, rec.Body.String())
	}
}

func TestLoginAttempts_RequiresActiveSession_ActiveSessionSucceeds(t *testing.T) {
	// Valid JWT + active session → handler must be reached (returns 200).
	// Session ID must be a valid UUID because RequireActiveSession validates UUID format.
	accessToken := mustAccessTokenForRouter(t, "user-2", "00000000-0000-0000-0000-000000000002")
	sessions := routerSessionStub{validateErr: nil}

	router := NewRouter(jwtTestConfig(), platformlog.New("auth-service", "test"),
		nil, nil, nil, nil, nil, routerLoginAttemptsStub{}, sessions, nil, nil, nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, RouteAuthMeLoginAttempts, nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for valid bearer + active session, got %d — %s", rec.Code, rec.Body.String())
	}
}

func TestLoginAttempts_RequiresActiveSession_NilSessionValidatorFailsClosed(t *testing.T) {
	// sessions is nil → RequireActiveSession must return 503 (fail-closed).
	accessToken := mustAccessTokenForRouter(t, "user-3", "session-any")
	router := NewRouter(jwtTestConfig(), platformlog.New("auth-service", "test"),
		nil, nil, nil, nil, nil, routerLoginAttemptsStub{}, nil, nil, nil, nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, RouteAuthMeLoginAttempts, nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when session validator nil (fail-closed), got %d — %s", rec.Code, rec.Body.String())
	}
}

func TestLoginAttempts_RequiresActiveSession_NotFoundSessionReturns401(t *testing.T) {
	// Valid JWT but session not found in DB → should be 401, not 200.
	accessToken := mustAccessTokenForRouter(t, "user-4", "session-gone")
	sessions := routerSessionStub{validateErr: domain.ErrNotFound}

	router := NewRouter(jwtTestConfig(), platformlog.New("auth-service", "test"),
		nil, nil, nil, nil, nil, routerLoginAttemptsStub{}, sessions, nil, nil, nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, RouteAuthMeLoginAttempts, nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for not-found session, got %d — %s", rec.Code, rec.Body.String())
	}
}
