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
)

func TestHealthzContract(t *testing.T) {
	router := NewRouter(testConfig(), platformlog.New("auth-service", "test"), nil, nil, nil)
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

func TestReadyzContract(t *testing.T) {
	router := NewRouter(testConfig(), platformlog.New("auth-service", "test"), nil, nil, nil)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, RouteReadyz, nil))

	assertJSONResponse(t, response, http.StatusOK)
	body := decodeHealthEnvelope(t, response)
	if body.Data.Service != "auth-service" {
		t.Fatalf("expected service auth-service, got %q", body.Data.Service)
	}
	if body.Data.Probe != health.ProbeReadiness {
		t.Fatalf("expected readiness probe, got %q", body.Data.Probe)
	}
	if body.Data.Status != health.StatusReady {
		t.Fatalf("expected ready status, got %q", body.Data.Status)
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
}

func TestVersionRouteStillWorks(t *testing.T) {
	router := NewRouter(testConfig(), platformlog.New("auth-service", "test"), nil, nil, nil)
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
	router := NewRouter(testConfig(), platformlog.New("auth-service", "test"), nil, nil, nil)

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

func assertRFC3339(t *testing.T, value string) {
	t.Helper()

	if _, err := time.Parse(time.RFC3339, value); err != nil {
		t.Fatalf("expected RFC3339 timestamp, got %q: %v", value, err)
	}
}

func testConfig() config.Config {
	return config.Config{ServiceName: "auth-service", Env: "test", Port: 8081, ReadHeaderTimeoutSeconds: 5}
}

func TestMetricsRouteReturns200(t *testing.T) {
	router := NewRouter(testConfig(), platformlog.New("auth-service", "test"), nil, nil, nil)
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
	router := NewRouter(testConfig(), platformlog.New("auth-service", "test"), nil, nil, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, RouteAdminUsers, nil))
	// testConfig has no ADMIN_BOOTSTRAP_TOKEN => 503
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
}

func TestAdminUsersMethodNotAllowed(t *testing.T) {
	router := NewRouter(testConfig(), platformlog.New("auth-service", "test"), nil, nil, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, RouteAdminUsers, nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
}

type routerAuthStub struct{}

func (routerAuthStub) Refresh(_ context.Context, _ string) (domain.TokenPair, error) {
	return domain.TokenPair{AccessToken: "access-token", RefreshToken: "refresh-token", TokenType: "Bearer", ExpiresIn: 900}, nil
}

func (routerAuthStub) Logout(_ context.Context, _ string) error {
	return nil
}

func TestAuthTokenEndpointRateLimiterAllowsRequestsUnderLimit(t *testing.T) {
	cfg := testConfig()
	cfg.AuthTokenEndpointRateLimitPerMinute = 60
	cfg.AuthTokenEndpointRateLimitBurst = 2
	router := NewRouter(cfg, platformlog.New("auth-service", "test"), nil, routerAuthStub{}, nil)

	for i := 0; i < 2; i++ {
		response := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, RouteAuthRefresh, strings.NewReader(`{"refresh_token":"refresh-token"}`))
		req.RemoteAddr = "203.0.113.10:12345"

		router.ServeHTTP(response, req)

		assertJSONResponse(t, response, http.StatusOK)
	}
}

func TestAuthTokenEndpointRateLimiterRejectsRequestsOverLimit(t *testing.T) {
	cfg := testConfig()
	cfg.AuthTokenEndpointRateLimitPerMinute = 60
	cfg.AuthTokenEndpointRateLimitBurst = 1
	router := NewRouter(cfg, platformlog.New("auth-service", "test"), nil, routerAuthStub{}, nil)

	first := httptest.NewRecorder()
	firstReq := httptest.NewRequest(http.MethodPost, RouteAuthRefresh, strings.NewReader(`{"refresh_token":"secret-refresh-token"}`))
	firstReq.RemoteAddr = "203.0.113.20:12345"
	router.ServeHTTP(first, firstReq)
	assertJSONResponse(t, first, http.StatusOK)

	second := httptest.NewRecorder()
	secondReq := httptest.NewRequest(http.MethodPost, RouteAuthLogout, strings.NewReader(`{"refresh_token":"secret-refresh-token"}`))
	secondReq.RemoteAddr = "203.0.113.20:12345"
	router.ServeHTTP(second, secondReq)

	assertJSONResponse(t, second, http.StatusTooManyRequests)
	if strings.Contains(second.Body.String(), "secret-refresh-token") {
		t.Fatalf("rate limit response must not include refresh token: %s", second.Body.String())
	}
}

func TestAuthRoutesMethodNotAllowed(t *testing.T) {
	router := NewRouter(testConfig(), platformlog.New("auth-service", "test"), nil, nil, nil)

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
		AccessToken:  "access-token",
		RefreshToken: "refresh-token",
		TokenType:    "Bearer",
		ExpiresIn:    900,
		User:         domain.LoginUser{ID: "user-1", Email: "user@example.com", DisplayName: "User"},
	}, nil
}

func TestAuthLoginMethodNotAllowed(t *testing.T) {
	router := NewRouter(testConfig(), platformlog.New("auth-service", "test"), nil, nil, routerLoginStub{})
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, RouteAuthLogin, nil))
	assertJSONResponse(t, rec, http.StatusMethodNotAllowed)
}

func TestAuthLoginRateLimiterAllowsRequestsUnderLimit(t *testing.T) {
	cfg := testConfig()
	cfg.AuthTokenEndpointRateLimitPerMinute = 60
	cfg.AuthTokenEndpointRateLimitBurst = 2
	router := NewRouter(cfg, platformlog.New("auth-service", "test"), nil, nil, routerLoginStub{})

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
	router := NewRouter(cfg, platformlog.New("auth-service", "test"), nil, nil, routerLoginStub{})

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
	router := NewRouter(cfg, platformlog.New("auth-service", "test"), nil, nil, routerLoginStub{})

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
	router := NewRouter(cfg, platformlog.New("auth-service", "test"), nil, nil, routerLoginStub{})

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
	router := NewRouter(cfg, platformlog.New("auth-service", "test"), nil, nil, routerLoginStub{})

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

// TestRateLimiter_InvalidXFF_FallsBackToRemoteAddr verifies that when a trusted proxy
// is configured but X-Forwarded-For contains a non-IP string, the limiter falls back
// to RemoteAddr and never uses the raw invalid header value as a limiter key.
func TestRateLimiter_InvalidXFF_FallsBackToRemoteAddr(t *testing.T) {
	cfg := testConfig()
	cfg.AuthTokenEndpointRateLimitPerMinute = 60
	cfg.AuthTokenEndpointRateLimitBurst = 1
	cfg.AuthTrustedProxyCIDRs = "10.0.0.0/8"
	router := NewRouter(cfg, platformlog.New("auth-service", "test"), nil, nil, routerLoginStub{})

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
	router := NewRouter(cfg, platformlog.New("auth-service", "test"), nil, nil, routerLoginStub{})

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
	router := NewRouter(cfg, platformlog.New("auth-service", "test"), nil, nil, routerLoginStub{})

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
