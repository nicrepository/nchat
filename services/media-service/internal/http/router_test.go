package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/health"
	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	platformlog "github.com/nicrepository/nchat/libs/go/platform/log"
	"github.com/nicrepository/nchat/services/media-service/internal/config"
	"github.com/nicrepository/nchat/services/media-service/internal/domain"
	"github.com/nicrepository/nchat/services/media-service/internal/service"
	"go.opentelemetry.io/otel/trace"
)

func TestHealthzContract(t *testing.T) {
	router := NewRouter(testConfig(), platformlog.New("media-service", "test"))
	response := httptest.NewRecorder()

	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, RouteHealthz, nil))

	assertJSONResponse(t, response, http.StatusOK)
	body := decodeHealthEnvelope(t, response)
	if body.Data.Service != "media-service" {
		t.Fatalf("expected service media-service, got %q", body.Data.Service)
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
	router := NewRouter(testConfig(), platformlog.New("media-service", "test"))
	response := httptest.NewRecorder()

	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, RouteReadyz, nil))

	assertJSONResponse(t, response, http.StatusOK)
	body := decodeHealthEnvelope(t, response)
	if body.Data.Service != "media-service" {
		t.Fatalf("expected service media-service, got %q", body.Data.Service)
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
	router := NewRouter(testConfig(), platformlog.New("media-service", "test"))
	response := httptest.NewRecorder()

	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, RouteVersion, nil))

	assertJSONResponse(t, response, http.StatusOK)
	var body struct {
		Data map[string]string `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode version response: %v", err)
	}
	if body.Data["service"] != "media-service" || body.Data["version"] != "0.0.0" || body.Data["commit"] != "dev" {
		t.Fatalf("unexpected version response: %+v", body.Data)
	}
}

func TestMethodAndNotFoundBehavior(t *testing.T) {
	router := NewRouter(testConfig(), platformlog.New("media-service", "test"))

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
	return config.Config{ServiceName: "media-service", Env: "test", Port: 8087, ReadHeaderTimeoutSeconds: 5}
}

func TestMetricsRouteReturns200(t *testing.T) {
	router := NewRouter(testConfig(), platformlog.New("media-service", "test"))
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

func TestLiveKitAPICheckerUsesConfiguredEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	checker := liveKitAPIChecker{rawURL: server.URL}
	if result := checker.Check(context.Background()); result.Status != health.CheckPass {
		t.Fatalf("expected reachable LiveKit endpoint, got %+v", result)
	}
	server.Close()
	if result := checker.Check(context.Background()); result.Status != health.CheckFail {
		t.Fatalf("expected closed LiveKit endpoint to fail, got %+v", result)
	}
}

func TestReadinessSkipsLiveKitWhenIntegrationIsDisabled(t *testing.T) {
	cfg := testConfig()
	cfg.LiveKitAPIURL = "http://127.0.0.1:1"
	pinger := &readinessPinger{}
	checks := readinessChecks(cfg, pinger)
	if len(checks) != 2 {
		t.Fatalf("disabled LiveKit must not add readiness dependency, got %d checks", len(checks))
	}
	if pinger.calls != 0 {
		t.Fatalf("disabled LiveKit must not ping PostgreSQL, got %d calls", pinger.calls)
	}
}

func TestReadinessRequiresHealthyPostgreSQLWhenLiveKitEnabled(t *testing.T) {
	liveKit := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer liveKit.Close()

	tests := []struct {
		name        string
		databaseErr error
		wantStatus  int
	}{
		{name: "healthy", wantStatus: http.StatusOK},
		{name: "unavailable", databaseErr: errors.New("postgres://user:secret@internal-db/nchat"), wantStatus: http.StatusServiceUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testConfig()
			cfg.LiveKitEnabled = true
			cfg.LiveKitAPIURL = liveKit.URL
			pinger := &readinessPinger{err: tt.databaseErr}
			router := NewRouter(cfg, slog.Default(), RouterDependencies{ReadinessPinger: pinger})
			response := httptest.NewRecorder()

			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, RouteReadyz, nil))

			if response.Code != tt.wantStatus {
				t.Fatalf("expected %d, got %d: %s", tt.wantStatus, response.Code, response.Body.String())
			}
			if pinger.calls != 1 {
				t.Fatalf("expected one PostgreSQL ping, got %d", pinger.calls)
			}
			body := decodeHealthEnvelope(t, response)
			var postgres *health.CheckResult
			for i := range body.Data.Checks {
				if body.Data.Checks[i].Name == "postgres" {
					postgres = &body.Data.Checks[i]
					break
				}
			}
			if postgres == nil || !postgres.Critical {
				t.Fatalf("expected critical postgres check, got %+v", body.Data.Checks)
			}
			if tt.databaseErr == nil && postgres.Status != health.CheckPass {
				t.Fatalf("expected passing postgres check, got %+v", postgres)
			}
			if tt.databaseErr != nil {
				if postgres.Status != health.CheckFail || postgres.Message != "PostgreSQL unavailable" {
					t.Fatalf("expected sanitized PostgreSQL failure, got %+v", postgres)
				}
				for _, forbidden := range []string{"postgres://", "secret", "internal-db"} {
					if strings.Contains(response.Body.String(), forbidden) {
						t.Fatalf("readiness leaked internal database detail %q: %s", forbidden, response.Body.String())
					}
				}
			}
		})
	}
}

func TestPostgresReadinessCheckerTimesOutAndHonorsCancellation(t *testing.T) {
	pinger := &readinessPinger{blockUntilCanceled: true}
	result := (postgresChecker{pinger: pinger, timeout: 20 * time.Millisecond}).Check(context.Background())
	if result.Status != health.CheckFail || result.Message != "PostgreSQL check timeout" {
		t.Fatalf("expected timeout failure, got %+v", result)
	}
	if !pinger.sawDeadline {
		t.Fatal("expected PostgreSQL ping context to have a deadline")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result = (postgresChecker{pinger: &readinessPinger{}}).Check(ctx)
	if result.Status != health.CheckFail || result.Message != "PostgreSQL check canceled" {
		t.Fatalf("expected cancellation failure, got %+v", result)
	}
}

func TestHealthzDoesNotPingPostgreSQL(t *testing.T) {
	cfg := testConfig()
	cfg.LiveKitEnabled = true
	pinger := &readinessPinger{}
	router := NewRouter(cfg, slog.Default(), RouterDependencies{ReadinessPinger: pinger})
	response := httptest.NewRecorder()

	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, RouteHealthz, nil))

	if response.Code != http.StatusOK || pinger.calls != 0 {
		t.Fatalf("healthz must not check PostgreSQL: status=%d calls=%d", response.Code, pinger.calls)
	}
}

func TestLiveKitAPICheckerTimesOut(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()

	result := (liveKitAPIChecker{rawURL: server.URL, timeout: 50 * time.Millisecond}).Check(context.Background())
	if result.Status != health.CheckFail || result.Message != "LiveKit API timeout" {
		t.Fatalf("expected timeout failure, got %+v", result)
	}
}

func TestLiveKitAPICheckerRejectsNonSuccessStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	result := (liveKitAPIChecker{rawURL: server.URL}).Check(context.Background())
	if result.Status != health.CheckFail || result.Message != "LiveKit API returned non-success status" {
		t.Fatalf("expected non-success failure, got %+v", result)
	}
}

func TestLiveKitAPICheckerHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := (liveKitAPIChecker{rawURL: "http://127.0.0.1:1"}).Check(ctx)
	if result.Status != health.CheckFail || result.Message != "LiveKit API check canceled" {
		t.Fatalf("expected cancellation failure, got %+v", result)
	}
}

func TestLiveKitAPICheckerRejectsInvalidURL(t *testing.T) {
	result := (liveKitAPIChecker{rawURL: "://invalid"}).Check(context.Background())
	if result.Status != health.CheckFail || result.Message != "invalid LiveKit URL" {
		t.Fatalf("expected invalid URL failure, got %+v", result)
	}
}

func TestReadinessConvertsSecureWebSocketLiveKitURLToHTTPS(t *testing.T) {
	liveKit := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer liveKit.Close()

	previousTransport := http.DefaultTransport
	http.DefaultTransport = liveKit.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = previousTransport })

	cfg := testConfig()
	cfg.LiveKitEnabled = true
	cfg.LiveKitAPIURL = strings.Replace(liveKit.URL, "https://", "wss://", 1)
	router := NewRouter(cfg, slog.Default(), RouterDependencies{ReadinessPinger: &readinessPinger{}})
	response := httptest.NewRecorder()

	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, RouteReadyz, nil))

	if response.Code != http.StatusOK {
		t.Fatalf("expected readiness success for WSS LiveKit URL, got %d: %s", response.Code, response.Body.String())
	}
	assertReadinessCheck(t, decodeHealthEnvelope(t, response).Data.Checks, "livekit-api")
}

func TestNewRouterLiveKitDisabledLogsIntegrationDisabled(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	requestID := "router-disabled-request"
	requestContext, traceID := liveKitRouterTraceContext()
	response := serveRouterUnavailableRequest(
		NewRouter(testConfig(), logger), requestContext, requestID,
	)

	assertRouterUnavailableResponse(
		t, response, logs.String(), requestID, traceID,
		unavailableCauseIntegrationDisabled, unavailableCauseDependenciesUnavailable,
	)
}

func TestNewRouterLiveKitEnabledWithMissingDependencyLogsDependenciesUnavailable(t *testing.T) {
	cfg, validator, _ := liveKitRouterAuth(t)

	tests := []struct {
		name   string
		remove func(*RouterDependencies)
	}{
		{name: "token validator", remove: func(deps *RouterDependencies) { deps.TokenValidator = nil }},
		{name: "token issuer", remove: func(deps *RouterDependencies) { deps.TokenIssuer = nil }},
		{name: "rate limiter", remove: func(deps *RouterDependencies) { deps.RateLimiter = nil }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logs bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
			limiter := NewUserRateLimiter(1, time.Minute)
			defer limiter.Stop()
			deps := RouterDependencies{
				TokenValidator: validator,
				TokenIssuer: &fakeTokenIssuer{result: service.IssuedToken{
					Token: string([]byte{
						'm', 'u', 's', 't', '-', 'n', 'o', 't', '-',
						'b', 'e', '-', 'l', 'o', 'g', 'g', 'e', 'd',
					}), // Test-only sentinel used to verify token redaction.
					ExpiresAt: time.Now().UTC().Add(time.Minute),
				}},
				RateLimiter: limiter,
			}
			tt.remove(&deps)

			requestID := "router-missing-dependency-request"
			requestContext, traceID := liveKitRouterTraceContext()
			response := serveRouterUnavailableRequest(
				NewRouter(cfg, logger, deps), requestContext, requestID,
			)

			assertRouterUnavailableResponse(
				t, response, logs.String(), requestID, traceID,
				unavailableCauseDependenciesUnavailable, unavailableCauseIntegrationDisabled,
			)
		})
	}
}

func TestNewRouterLiveKitEnabledWithCompleteDependenciesUsesFunctionalFlow(t *testing.T) {
	cfg, validator, token := liveKitRouterAuth(t)
	issuer := &fakeTokenIssuer{result: service.IssuedToken{
		Token: "livekit-token", ExpiresAt: time.Now().UTC().Add(time.Minute),
	}}
	limiter := NewUserRateLimiter(1, time.Minute)
	t.Cleanup(limiter.Stop)
	router := NewRouter(cfg, slog.Default(), RouterDependencies{
		TokenValidator: validator, TokenIssuer: issuer, RateLimiter: limiter,
	})

	for _, auth := range []string{"", "Bearer invalid"} {
		response := serveRouterTokenRequest(router, auth, `{"call_id":"`+handlerTestResource+`"}`)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401 for auth %q, got %d", auth, response.Code)
		}
	}
	if issuer.calls != 0 {
		t.Fatalf("issuer called before authentication: %d", issuer.calls)
	}

	first := serveRouterTokenRequest(router, "Bearer "+token,
		`{"call_id":"`+handlerTestResource+`"}`)
	if first.Code != http.StatusOK || issuer.calls != 1 {
		t.Fatalf("expected issued token, status=%d calls=%d body=%s", first.Code, issuer.calls, first.Body.String())
	}
	second := serveRouterTokenRequest(router, "Bearer "+token,
		`{"call_id":"`+handlerTestResource+`"}`)
	if second.Code != http.StatusTooManyRequests || second.Header().Get("Retry-After") != "60" {
		t.Fatalf("expected 429 after limit, status=%d retry=%q", second.Code, second.Header().Get("Retry-After"))
	}
	if issuer.calls != 1 {
		t.Fatalf("rate-limited request reached issuer: %d", issuer.calls)
	}
}

func TestLiveKitTokenCacheControlNoStoreAtRouter(t *testing.T) {
	cfg := testConfig()
	cfg.LiveKitEnabled = true
	fixedNow := time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)
	fixedClock := func() time.Time {
		return fixedNow
	}
	const accessToken = "stub-access-token"
	issued := service.IssuedToken{
		Token:     "test-livekit-token",
		ExpiresAt: time.Date(2026, 7, 22, 4, 4, 5, 0, time.UTC),
	}
	validator := stubAccessTokenValidator{principal: Principal{
		UserID: handlerTestUserID, SessionID: handlerTestSessionID,
		AccessExpiresAt: time.Date(2026, 7, 22, 5, 4, 5, 0, time.UTC),
	}}

	newRouter := func(t *testing.T, validator accessTokenValidator, issuer LiveKitTokenIssuer) http.Handler {
		t.Helper()
		limiter := newUserRateLimiter(1, time.Minute, fixedClock)
		t.Cleanup(limiter.Stop)
		return NewRouter(cfg, slog.Default(), RouterDependencies{
			TokenValidator: validator,
			TokenIssuer:    issuer,
			RateLimiter:    limiter,
		})
	}
	assertResponseMetadata := func(t *testing.T, response *httptest.ResponseRecorder, status int) {
		t.Helper()
		if response.Code != status {
			t.Fatalf("expected status %d, got %d", status, response.Code)
		}
		if got := response.Header().Get("Cache-Control"); got != "no-store" {
			t.Fatalf("expected Cache-Control no-store, got %q", got)
		}
		if got := response.Header().Get("Content-Type"); !strings.Contains(got, "application/json") {
			t.Fatalf("expected JSON Content-Type, got %q", got)
		}
		if location := response.Header().Get("Location"); location != "" {
			t.Fatal("token endpoint must not set Location")
		}
	}
	assertTokenAbsentFromHeaders := func(t *testing.T, response *httptest.ResponseRecorder, token string) {
		t.Helper()
		for name, values := range response.Header() {
			for _, value := range values {
				if strings.Contains(value, token) {
					t.Fatalf("token leaked into response header %q", name)
				}
			}
		}
	}

	t.Run("success", func(t *testing.T) {
		response := serveRouterTokenRequest(newRouter(t, validator, &fakeTokenIssuer{result: issued}),
			"Bearer "+accessToken, `{"call_id":"`+handlerTestResource+`"}`)
		assertResponseMetadata(t, response, http.StatusOK)

		var envelope struct {
			Data struct {
				Token     string `json:"token"`
				ExpiresAt string `json:"expiresAt"`
			} `json:"data"`
		}
		if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
			t.Fatalf("decode token response: %v", err)
		}
		if envelope.Data.Token == "" {
			t.Fatal("expected non-empty token in response data")
		}
		if envelope.Data.ExpiresAt == "" {
			t.Fatal("expected non-empty expiresAt in response data")
		}
		if _, err := time.Parse(time.RFC3339, envelope.Data.ExpiresAt); err != nil {
			t.Fatalf("expected valid RFC3339 expiresAt: %v", err)
		}
		assertTokenAbsentFromHeaders(t, response, envelope.Data.Token)
	})

	tests := []struct {
		name          string
		validator     accessTokenValidator
		issuer        LiveKitTokenIssuer
		authorization string
		body          string
		status        int
		primeLimiter  bool
	}{
		{name: "invalid payload", issuer: &fakeTokenIssuer{}, authorization: "Bearer " + accessToken,
			body: `{`, status: http.StatusBadRequest},
		{name: "unauthorized", validator: stubAccessTokenValidator{err: errors.New("invalid access token")},
			issuer: &fakeTokenIssuer{}, authorization: "Bearer " + accessToken,
			body: `{"call_id":"` + handlerTestResource + `"}`, status: http.StatusUnauthorized},
		{name: "not found", issuer: &fakeTokenIssuer{err: domain.ErrNotFound}, authorization: "Bearer " + accessToken,
			body: `{"call_id":"` + handlerTestResource + `"}`, status: http.StatusNotFound},
		{name: "rate limited", issuer: &fakeTokenIssuer{result: issued}, authorization: "Bearer " + accessToken,
			body: `{"call_id":"` + handlerTestResource + `"}`, status: http.StatusTooManyRequests,
			primeLimiter: true},
		{name: "internal error", issuer: &fakeTokenIssuer{err: errors.New("operational failure")}, authorization: "Bearer " + accessToken,
			body: `{"call_id":"` + handlerTestResource + `"}`, status: http.StatusInternalServerError},
		{name: "unavailable", issuer: &fakeTokenIssuer{err: domain.ErrUnavailable}, authorization: "Bearer " + accessToken,
			body: `{"call_id":"` + handlerTestResource + `"}`, status: http.StatusServiceUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scenarioValidator := accessTokenValidator(validator)
			if tt.validator != nil {
				scenarioValidator = tt.validator
			}
			router := newRouter(t, scenarioValidator, tt.issuer)
			if tt.primeLimiter {
				first := serveRouterTokenRequest(router, tt.authorization, tt.body)
				if first.Code != http.StatusOK {
					t.Fatalf("expected limiter priming request to succeed, got %d", first.Code)
				}
			}
			response := serveRouterTokenRequest(router, tt.authorization, tt.body)
			assertResponseMetadata(t, response, tt.status)
			assertTokenAbsentFromHeaders(t, response, issued.Token)
		})
	}
}

type stubAccessTokenValidator struct {
	principal Principal
	err       error
}

func (s stubAccessTokenValidator) ValidateAccessToken(_ string) (Principal, error) {
	return s.principal, s.err
}

func liveKitRouterAuth(t *testing.T) (config.Config, *TokenValidator, string) {
	t.Helper()
	secret := strings.Repeat("s", 32)
	validator, err := NewTokenValidator(secret, testAuthIssuer, testAuthAudience)
	if err != nil {
		t.Fatalf("new validator: %v", err)
	}
	cfg := testConfig()
	cfg.LiveKitEnabled = true
	token := signMediaAccessToken(t, secret, mediaTestClaims(time.Now().UTC().Add(time.Hour)))
	return cfg, validator, token
}

func serveRouterTokenRequest(router http.Handler, authorization, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, RouteLiveKitToken, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func serveRouterUnavailableRequest(router http.Handler, ctx context.Context, requestID string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, RouteLiveKitToken,
		strings.NewReader(`{"call_id":"`+handlerTestResource+`"}`)).WithContext(ctx)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Request-ID", requestID)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func liveKitRouterTraceContext() (context.Context, string) {
	spanContext := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16},
		SpanID:     trace.SpanID{1, 2, 3, 4, 5, 6, 7, 8},
		TraceFlags: trace.FlagsSampled,
		Remote:     true,
	})
	return trace.ContextWithSpanContext(context.Background(), spanContext), spanContext.TraceID().String()
}

func assertRouterUnavailableResponse(
	t *testing.T,
	response *httptest.ResponseRecorder,
	logs string,
	requestID string,
	traceID string,
	wantCause string,
	forbiddenCause string,
) {
	t.Helper()
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d: %s", response.Code, response.Body.String())
	}
	if response.Header().Get("X-Request-ID") != requestID {
		t.Fatalf("expected response request ID %q, got %q", requestID, response.Header().Get("X-Request-ID"))
	}
	for _, want := range []string{
		`"level":"WARN"`,
		`"status":503`,
		`"result":"unavailable"`,
		`"cause":"` + wantCause + `"`,
		`"request_id":"` + requestID + `"`,
		`"trace_id":"` + traceID + `"`,
	} {
		if !strings.Contains(logs, want) {
			t.Fatalf("expected log field %s, got %s", want, logs)
		}
	}
	if strings.Contains(logs, `"cause":"`+forbiddenCause+`"`) {
		t.Fatalf("unexpected cause %q in logs: %s", forbiddenCause, logs)
	}
	for _, forbidden := range []string{
		unavailableCauseIntegrationDisabled,
		unavailableCauseDependenciesUnavailable,
		"must-not-be-logged",
		"secret",
	} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatalf("response leaked internal detail %q: %s", forbidden, response.Body.String())
		}
	}
	for _, forbidden := range []string{"must-not-be-logged", "secret"} {
		if strings.Contains(logs, forbidden) {
			t.Fatalf("logs leaked sensitive detail %q: %s", forbidden, logs)
		}
	}
}

type readinessPinger struct {
	err                error
	calls              int
	blockUntilCanceled bool
	sawDeadline        bool
}

func (p *readinessPinger) Ping(ctx context.Context) error {
	p.calls++
	_, p.sawDeadline = ctx.Deadline()
	if p.blockUntilCanceled {
		<-ctx.Done()
		return ctx.Err()
	}
	return p.err
}
