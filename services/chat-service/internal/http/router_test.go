package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/nicrepository/nchat/libs/go/platform/health"
	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	platformlog "github.com/nicrepository/nchat/libs/go/platform/log"
	"github.com/nicrepository/nchat/services/chat-service/internal/config"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	ws "github.com/nicrepository/nchat/services/chat-service/internal/ws"
)

const (
	routerTestIssuer    = "nchat-auth"
	routerTestAudience  = "nchat-api"
	routerTestUserID    = "router-user"
	routerTestSessionID = "b1e2c3d4-0000-0000-0000-000000000001"
)

type allowRouterSessionValidator struct{}

func (allowRouterSessionValidator) ValidateActiveSession(_ context.Context, _, _ string) error {
	return nil
}

type rejectRouterSessionValidator struct{}

func (rejectRouterSessionValidator) ValidateActiveSession(_ context.Context, _, _ string) error {
	return domain.ErrInvalidToken
}

func TestHealthzContract(t *testing.T) {
	router := NewRouter(testConfig(), platformlog.New("chat-service", "test"), ReadinessState{}, nil, nil, NewSidebarHandler(nil), NewMessageHandler(nil, nil, nil), nil, nil)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, RouteHealthz, nil))

	assertJSONResponse(t, response, http.StatusOK)
	body := decodeHealthEnvelope(t, response)
	if body.Data.Service != "chat-service" {
		t.Fatalf("expected service chat-service, got %q", body.Data.Service)
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

// readySidebarStub / readyMessagesStub / readyWorkspacesStub give the
// handlers non-nil dependencies for readiness tests; the embedded interfaces
// are never invoked by the probe.
type readySidebarStub struct{ sidebarProvider }
type readyMessagesStub struct{ messageProvider }
type readyWorkspacesStub struct{ workspaceResolver }

// newFullyWiredRouter builds a router with every mandatory dependency
// present, mirroring a successful bootstrap.
// fullyReadyState mirrors what app.New reports after a successful bootstrap.
func fullyReadyState() ReadinessState {
	return ReadinessState{Database: true, TokenValidator: true, SessionValidator: true, Sidebar: true, Messages: true, WebSocket: true}
}

// stubWSHandler stands in for the real, functional WebSocket handler.
func stubWSHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
}

func routerTestValidator(t *testing.T) *TokenValidator {
	t.Helper()
	validator, err := NewTokenValidator(routerTestSigningKey(), routerTestIssuer, routerTestAudience)
	if err != nil {
		t.Fatalf("create validator: %v", err)
	}
	return validator
}

func newFullyWiredRouter(t *testing.T) http.Handler {
	t.Helper()
	return NewRouter(testConfig(), platformlog.New("chat-service", "test"), fullyReadyState(),
		routerTestValidator(t), allowRouterSessionValidator{},
		NewSidebarHandler(readySidebarStub{}),
		NewMessageHandler(readyWorkspacesStub{}, readyMessagesStub{}, nil),
		stubWSHandler(), nil)
}

// TestReadyzContract: a partially initialized instance (no DB-backed
// services, no validators) must never report Ready — Kubernetes keeps it out
// of the Endpoints and the previous pod continues serving.
func TestReadyzContract(t *testing.T) {
	router := NewRouter(testConfig(), platformlog.New("chat-service", "test"), ReadinessState{}, nil, nil, NewSidebarHandler(nil), NewMessageHandler(nil, nil, nil), nil, nil)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, RouteReadyz, nil))

	assertJSONResponse(t, response, http.StatusServiceUnavailable)
	body := decodeHealthEnvelope(t, response)
	if body.Data.Service != "chat-service" {
		t.Fatalf("expected service chat-service, got %q", body.Data.Service)
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
	for _, name := range []string{"database", "jwt-validator", "session-validator", "sidebar-service", "message-service", "websocket"} {
		assertReadinessCheckFails(t, body.Data.Checks, name)
	}
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
	for _, name := range []string{"database", "jwt-validator", "session-validator", "sidebar-service", "message-service", "websocket"} {
		assertReadinessCheck(t, body.Data.Checks, name)
	}
}

// TestReadyzDatabaseUpWithInvalidJWT: PostgreSQL opened but the JWT
// validator is missing → database must still report pass (it is an
// independent signal), while jwt-validator and the dependent components
// fail. This is the operational-diagnosis scenario from the code review.
func TestReadyzDatabaseUpWithInvalidJWT(t *testing.T) {
	// Mirrors app.New with pool opened and validator==nil: DB-backed services
	// are not wired, so only Database is true.
	state := ReadinessState{Database: true}
	router := NewRouter(testConfig(), platformlog.New("chat-service", "test"), state,
		nil, nil,
		NewSidebarHandler(nil), NewMessageHandler(nil, nil, nil), nil, nil)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, RouteReadyz, nil))

	assertJSONResponse(t, response, http.StatusServiceUnavailable)
	body := decodeHealthEnvelope(t, response)
	assertReadinessCheck(t, body.Data.Checks, "database")
	for _, name := range []string{"jwt-validator", "session-validator", "sidebar-service", "message-service", "websocket"} {
		assertReadinessCheckFails(t, body.Data.Checks, name)
	}
}

// TestReadyzUnreadyWithoutSessionValidator: everything wired except the
// session validator → readiness (including the websocket check) must fail
// while database stays pass.
func TestReadyzUnreadyWithoutSessionValidator(t *testing.T) {
	state := fullyReadyState()
	state.SessionValidator = false
	state.WebSocket = false // app computes WebSocket from the validators too
	router := NewRouter(testConfig(), platformlog.New("chat-service", "test"), state,
		routerTestValidator(t), nil,
		NewSidebarHandler(readySidebarStub{}),
		NewMessageHandler(readyWorkspacesStub{}, readyMessagesStub{}, nil),
		stubWSHandler(), nil)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, RouteReadyz, nil))

	assertJSONResponse(t, response, http.StatusServiceUnavailable)
	body := decodeHealthEnvelope(t, response)
	assertReadinessCheckFails(t, body.Data.Checks, "session-validator")
	assertReadinessCheckFails(t, body.Data.Checks, "websocket")
	assertReadinessCheck(t, body.Data.Checks, "database")
}

// TestReadyzUnreadyWithoutDatabase: validators present but the pool never
// opened → database fails and every DB-backed component fails with it.
func TestReadyzUnreadyWithoutDatabase(t *testing.T) {
	router := NewRouter(testConfig(), platformlog.New("chat-service", "test"), ReadinessState{TokenValidator: true, SessionValidator: true},
		routerTestValidator(t), allowRouterSessionValidator{},
		NewSidebarHandler(nil), NewMessageHandler(nil, nil, nil), nil, nil)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, RouteReadyz, nil))

	assertJSONResponse(t, response, http.StatusServiceUnavailable)
	body := decodeHealthEnvelope(t, response)
	for _, name := range []string{"database", "sidebar-service", "message-service", "websocket"} {
		assertReadinessCheckFails(t, body.Data.Checks, name)
	}
	assertReadinessCheck(t, body.Data.Checks, "jwt-validator")
	assertReadinessCheck(t, body.Data.Checks, "session-validator")
}

// TestReadyzNilWSHandlerNeverReportsWebSocketPass: when the router has to
// substitute the 503 WebSocket handler, the websocket check must fail even
// if the caller claimed it was ready.
func TestReadyzNilWSHandlerNeverReportsWebSocketPass(t *testing.T) {
	router := NewRouter(testConfig(), platformlog.New("chat-service", "test"), fullyReadyState(),
		routerTestValidator(t), allowRouterSessionValidator{},
		NewSidebarHandler(readySidebarStub{}),
		NewMessageHandler(readyWorkspacesStub{}, readyMessagesStub{}, nil),
		nil, nil)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, RouteReadyz, nil))

	assertJSONResponse(t, response, http.StatusServiceUnavailable)
	body := decodeHealthEnvelope(t, response)
	assertReadinessCheckFails(t, body.Data.Checks, "websocket")
	assertReadinessCheck(t, body.Data.Checks, "database")
}

// TestReadyzUnwiredHandlersDowngradeChecksButNotDatabase: a caller claiming
// sidebar/messages ready while passing unwired handlers gets downgraded by
// the router; database is untouched because it is the app's own signal.
func TestReadyzUnwiredHandlersDowngradeChecksButNotDatabase(t *testing.T) {
	router := NewRouter(testConfig(), platformlog.New("chat-service", "test"), fullyReadyState(),
		routerTestValidator(t), allowRouterSessionValidator{},
		NewSidebarHandler(nil), NewMessageHandler(nil, nil, nil),
		stubWSHandler(), nil)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, RouteReadyz, nil))

	assertJSONResponse(t, response, http.StatusServiceUnavailable)
	body := decodeHealthEnvelope(t, response)
	assertReadinessCheckFails(t, body.Data.Checks, "sidebar-service")
	assertReadinessCheckFails(t, body.Data.Checks, "message-service")
	assertReadinessCheck(t, body.Data.Checks, "database")
}

func TestReadyzRejectsDatabaseBackedChatWithoutReactionLimiterConfig(t *testing.T) {
	cfg := testConfig()
	cfg.DatabaseURL = "postgres://configured"
	response := httptest.NewRecorder()

	fullyReady := ReadinessState{Database: true, TokenValidator: true, SessionValidator: true, Sidebar: true, Messages: true, WebSocket: true}
	Readyz(cfg, fullyReady).ServeHTTP(response, httptest.NewRequest(http.MethodGet, RouteReadyz, nil))

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status 503, got %d", response.Code)
	}
	body := decodeHealthEnvelope(t, response)
	if body.Data.Status != health.StatusUnready {
		t.Fatalf("expected unready status, got %q", body.Data.Status)
	}
}

func TestVersionRouteStillWorks(t *testing.T) {
	router := NewRouter(testConfig(), platformlog.New("chat-service", "test"), ReadinessState{}, nil, nil, NewSidebarHandler(nil), NewMessageHandler(nil, nil, nil), nil, nil)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, RouteVersion, nil))

	assertJSONResponse(t, response, http.StatusOK)
	var body struct {
		Data map[string]string `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode version response: %v", err)
	}
	if body.Data["service"] != "chat-service" || body.Data["version"] != "0.0.0" || body.Data["commit"] != "dev" {
		t.Fatalf("unexpected version response: %+v", body.Data)
	}
}

func TestAllowedReactionEmojisRouteRequiresAuthentication(t *testing.T) {
	validator, err := NewTokenValidator(routerTestSigningKey(), routerTestIssuer, routerTestAudience)
	if err != nil {
		t.Fatalf("new token validator: %v", err)
	}
	router := NewRouter(testConfig(), platformlog.New("chat-service", "test"), ReadinessState{}, validator, allowRouterSessionValidator{},
		NewSidebarHandler(nil), NewMessageHandler(nil, nil, nil), nil, nil)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, RouteAllowedReactionEmojis, nil))

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", response.Code)
	}
}

func TestMethodAndNotFoundBehavior(t *testing.T) {
	router := NewRouter(testConfig(), platformlog.New("chat-service", "test"), ReadinessState{}, nil, nil, NewSidebarHandler(nil), NewMessageHandler(nil, nil, nil), nil, nil)

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

func TestMentionAutocompleteRouteHasIndependentRateLimit(t *testing.T) {
	if mentionSearchRateLimit != 30 {
		t.Fatalf("mention autocomplete budget = %d, want 30", mentionSearchRateLimit)
	}
	validator, err := NewTokenValidator(routerTestSigningKey(), routerTestIssuer, routerTestAudience)
	if err != nil {
		t.Fatalf("new token validator: %v", err)
	}
	router := NewRouter(
		testConfig(), platformlog.New("chat-service", "test"), ReadinessState{}, validator,
		allowRouterSessionValidator{}, NewSidebarHandler(nil), NewMessageHandler(nil, nil, nil), nil, nil,
	)
	path := "/api/chat/channels/22222222-2222-2222-2222-222222222222/mentions?q=a"

	for range mentionSearchRateLimit {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", bearerScheme+makeRouterTestToken(t))
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		if response.Code == http.StatusTooManyRequests {
			t.Fatal("autocomplete was rate-limited before its budget was exhausted")
		}
	}
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", bearerScheme+makeRouterTestToken(t))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 after autocomplete budget, got %d", response.Code)
	}
}

func TestDMContractRoutesRequireAuthentication(t *testing.T) {
	validator, err := NewTokenValidator(routerTestSigningKey(), routerTestIssuer, routerTestAudience)
	if err != nil {
		t.Fatalf("new token validator: %v", err)
	}
	router := NewRouter(testConfig(), platformlog.New("chat-service", "test"), ReadinessState{}, validator,
		allowRouterSessionValidator{}, NewSidebarHandler(nil), NewMessageHandler(nil, nil, nil), nil,
		NewDMHandler(nil, nil, nil))
	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodGet, RouteDMCandidates+"?query=an", nil),
		httptest.NewRequest(http.MethodPost, RouteDMConversations, strings.NewReader(`{"other_user_id":"55555555-5555-5555-5555-555555555555"}`)),
	} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s status=%d, want 401", request.Method, request.URL.Path, response.Code)
		}
	}
}

func TestDMContractRoutesRejectInvalidTokenAndRevokedSession(t *testing.T) {
	validator, err := NewTokenValidator(routerTestSigningKey(), routerTestIssuer, routerTestAudience)
	if err != nil {
		t.Fatalf("new token validator: %v", err)
	}
	for _, test := range []struct {
		name     string
		sessions SessionValidator
		request  *http.Request
	}{
		{name: "invalid bearer", sessions: allowRouterSessionValidator{}, request: httptest.NewRequest(http.MethodGet, RouteDMCandidates+"?query=an", nil)},
		{name: "revoked session", sessions: rejectRouterSessionValidator{}, request: routerGETRequest(t, RouteDMCandidates+"?query=an")},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.name == "invalid bearer" {
				test.request.Header.Set("Authorization", bearerScheme+"not.a.valid.jwt")
			}
			router := NewRouter(testConfig(), platformlog.New("chat-service", "test"), ReadinessState{}, validator,
				test.sessions, NewSidebarHandler(nil), NewMessageHandler(nil, nil, nil), nil,
				NewDMHandler(nil, nil, nil))
			response := httptest.NewRecorder()
			router.ServeHTTP(response, test.request)
			if response.Code != http.StatusUnauthorized || strings.Contains(response.Body.String(), "session") {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestDMContractRoutesAreRegisteredOnlyWithHandler(t *testing.T) {
	validator, err := NewTokenValidator(routerTestSigningKey(), routerTestIssuer, routerTestAudience)
	if err != nil {
		t.Fatalf("new token validator: %v", err)
	}
	for _, test := range []struct {
		name    string
		handler *DMHandler
		want    int
	}{
		{name: "absent", want: http.StatusNotFound},
		{name: "registered", handler: NewDMHandler(nil, nil, nil), want: http.StatusServiceUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			router := NewRouter(testConfig(), platformlog.New("chat-service", "test"), ReadinessState{}, validator,
				allowRouterSessionValidator{}, NewSidebarHandler(nil), NewMessageHandler(nil, nil, nil), nil, test.handler)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, routerGETRequest(t, RouteDMCandidates+"?query=an"))
			if response.Code != test.want {
				t.Fatalf("status=%d, want %d", response.Code, test.want)
			}
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
	return config.Config{ServiceName: "chat-service", Env: "test", Port: 8082, ReadHeaderTimeoutSeconds: 5}
}

func TestMetricsRouteReturns200(t *testing.T) {
	router := NewRouter(testConfig(), platformlog.New("chat-service", "test"), ReadinessState{}, nil, nil, NewSidebarHandler(nil), NewMessageHandler(nil, nil, nil), nil, nil)
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

func TestNewRouter_NilWSHandlerReturns503AfterAuth(t *testing.T) {
	validator, err := NewTokenValidator(routerTestSigningKey(), routerTestIssuer, routerTestAudience)
	if err != nil {
		t.Fatalf("new token validator: %v", err)
	}
	router := NewRouter(
		testConfig(),
		platformlog.New("chat-service", "test"),
		ReadinessState{},
		validator,
		allowRouterSessionValidator{},
		NewSidebarHandler(nil),
		NewMessageHandler(nil, nil, nil),
		nil,
		nil,
	)

	req := httptest.NewRequest(http.MethodGet, RouteWS, nil)
	req.Header.Set("Authorization", bearerScheme+makeRouterTestToken(t))
	response := httptest.NewRecorder()

	router.ServeHTTP(response, req)

	assertJSONResponse(t, response, http.StatusServiceUnavailable)
}

func routerTestSigningKey() string {
	return strings.Repeat("r", 40)
}

func makeRouterTestToken(t *testing.T) string {
	t.Helper()
	claims := jwt.MapClaims{
		"sub": routerTestUserID,
		"sid": routerTestSessionID,
		"iss": routerTestIssuer,
		"aud": jwt.ClaimStrings{routerTestAudience},
		"iat": time.Now().Unix(),
		"nbf": time.Now().Unix(),
		"exp": time.Now().Add(time.Hour).Unix(),
		"jti": "router-test-token",
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(routerTestSigningKey()))
	if err != nil {
		t.Fatalf("sign router test token: %v", err)
	}
	return token
}

// ── NewRouter POST rate limit integration tests ───────────────────────────────
//
// These tests verify that the msgPostLimiter wired in NewRouter returns 429
// before the message handler is invoked, ensuring no DB write and no broadcast
// occur when the per-user budget is exhausted.
//
// Approach: pass a NewMessageHandler(nil, nil, nil) so that any handler invocation
// would panic (nil service). Exhaust the budget by sending msgPostRateLimit
// requests through the full router stack, then verify the next request returns
// 429 without panicking.

// newRouterForRateLimit builds a full router suitable for POST rate-limit
// integration testing. The token validator and session validator are real so
// the auth middleware is exercised; the message handler uses nil services so
// any unintended handler invocation panics.
func newRouterForRateLimit(t *testing.T) http.Handler {
	t.Helper()
	validator, err := NewTokenValidator(routerTestSigningKey(), routerTestIssuer, routerTestAudience)
	if err != nil {
		t.Fatalf("new token validator: %v", err)
	}
	return NewRouter(
		testConfig(),
		platformlog.New("chat-service", "test"),
		ReadinessState{},
		validator,
		allowRouterSessionValidator{},
		NewSidebarHandler(nil),
		NewMessageHandler(nil, nil, nil),
		nil,
		nil,
	)
}

// routerPOSTRequest creates an authenticated POST request for a send-message URL.
func routerPOSTRequest(t *testing.T, url string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, url, strings.NewReader(`{"body":"hello"}`))
	req.Header.Set("Authorization", bearerScheme+makeRouterTestToken(t))
	req.Header.Set("Content-Type", "application/json")
	return req
}

func routerGETRequest(t *testing.T, url string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Authorization", bearerScheme+makeRouterTestToken(t))
	return req
}

// TestNewRouter_PostChannelMessage_Returns429AfterBudgetExhausted verifies that
// POST /api/chat/channels/{channelID}/messages returns 429 when the per-user
// msgPostRateLimit budget is exceeded, and does NOT invoke the handler.
//
// The inner handler is NewMessageHandler(nil, nil, nil) — if any handler method is
// called it will panic because the services are nil. A panic here means the
// test fails with a clear signal that the rate limiter is not working.
func TestNewRouter_PostChannelMessage_Returns429AfterBudgetExhausted(t *testing.T) {
	router := newRouterForRateLimit(t)
	url := "/api/chat/channels/11111111-1111-1111-1111-111111111111/messages"

	// Exhaust the full budget. All requests will return 500 (nil service panics
	// recovered by the middleware stack), NOT 429 — confirming they reach the handler.
	for i := range msgPostRateLimit {
		w := httptest.NewRecorder()
		func() {
			defer func() { recover() }() //nolint:errcheck
			router.ServeHTTP(w, routerPOSTRequest(t, url))
		}()
		// Must not be 429 while within budget.
		if w.Code == http.StatusTooManyRequests {
			t.Fatalf("request %d should not be 429 (within budget); got 429", i+1)
		}
	}

	// The next request must be rate-limited at the router level.
	w := httptest.NewRecorder()
	router.ServeHTTP(w, routerPOSTRequest(t, url))
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 after budget exhausted; got %d", w.Code)
	}
	if ra := w.Header().Get("Retry-After"); ra == "" {
		t.Fatal("expected Retry-After header on 429 response")
	}
}

// TestNewRouter_PostDMMessage_Returns429AfterBudgetExhausted verifies the same
// guarantee for POST /api/chat/dm/{conversationID}/messages.
func TestNewRouter_PostDMMessage_Returns429AfterBudgetExhausted(t *testing.T) {
	router := newRouterForRateLimit(t)
	url := "/api/chat/dm/22222222-2222-2222-2222-222222222222/messages"

	for i := range msgPostRateLimit {
		w := httptest.NewRecorder()
		func() {
			defer func() { recover() }() //nolint:errcheck
			router.ServeHTTP(w, routerPOSTRequest(t, url))
		}()
		if w.Code == http.StatusTooManyRequests {
			t.Fatalf("DM POST %d should not be 429 (within budget); got 429", i+1)
		}
	}

	w := httptest.NewRecorder()
	router.ServeHTTP(w, routerPOSTRequest(t, url))
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 after DM POST budget exhausted; got %d", w.Code)
	}
}

func TestNewRouter_PinActionsUseDedicatedRateLimit(t *testing.T) {
	router := newRouterForRateLimit(t)
	channelPinURL := "/api/chat/channels/11111111-1111-1111-1111-111111111111/messages/22222222-2222-2222-2222-222222222222/pin"
	dmPinURL := "/api/chat/dm/33333333-3333-3333-3333-333333333333/messages/22222222-2222-2222-2222-222222222222/pin"

	if pinActionRateLimit != 10 {
		t.Fatalf("pinActionRateLimit = %d, want 10", pinActionRateLimit)
	}
	for i := range pinActionRateLimit {
		w := httptest.NewRecorder()
		func() {
			defer func() { recover() }() //nolint:errcheck
			router.ServeHTTP(w, routerPOSTRequest(t, channelPinURL))
		}()
		if w.Code == http.StatusTooManyRequests {
			t.Fatalf("pin request %d should not be 429 within pin budget", i+1)
		}
	}

	w := httptest.NewRecorder()
	router.ServeHTTP(w, routerPOSTRequest(t, dmPinURL))
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("expected shared pin budget 429 across channel/DM pins, got %d", w.Code)
	}

	w = httptest.NewRecorder()
	router.ServeHTTP(w, routerPOSTRequest(t, "/api/chat/channels/11111111-1111-1111-1111-111111111111/messages"))
	if w.Code == http.StatusTooManyRequests {
		t.Fatal("exhausting pin budget must not consume message post budget")
	}
}

func TestNewRouter_GetSingleMessage_Returns429AfterSingleBudgetExhausted(t *testing.T) {
	router := newRouterForRateLimit(t)
	url := "/api/chat/channels/11111111-1111-1111-1111-111111111111/messages/22222222-2222-2222-2222-222222222222"

	for i := range msgGetSingleRateLimit {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, routerGETRequest(t, url))
		if w.Code == http.StatusTooManyRequests {
			t.Fatalf("GET single request %d should not be 429 within budget", i+1)
		}
	}

	w := httptest.NewRecorder()
	router.ServeHTTP(w, routerGETRequest(t, url))
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 after GET single budget exhausted; got %d", w.Code)
	}
}

func TestNewRouter_AllowedReactionEmojisUsesListRateLimit(t *testing.T) {
	router := newRouterForRateLimit(t)
	for i := range msgListRateLimit {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, routerGETRequest(t, RouteAllowedReactionEmojis))
		if w.Code != http.StatusOK {
			t.Fatalf("allowed emojis request %d: expected 200, got %d", i+1, w.Code)
		}
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, routerGETRequest(t, RouteAllowedReactionEmojis))
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 after list budget exhausted, got %d", w.Code)
	}
}

func TestNewRouter_GetSingleBudgetDoesNotConsumeListBudget(t *testing.T) {
	router := newRouterForRateLimit(t)
	singleURL := "/api/chat/channels/11111111-1111-1111-1111-111111111111/messages/22222222-2222-2222-2222-222222222222"
	listURL := "/api/chat/channels/11111111-1111-1111-1111-111111111111/messages"

	for range msgGetSingleRateLimit {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, routerGETRequest(t, singleURL))
		if w.Code == http.StatusTooManyRequests {
			t.Fatalf("GET single should stay within its own budget")
		}
	}

	w := httptest.NewRecorder()
	router.ServeHTTP(w, routerGETRequest(t, listURL))
	if w.Code == http.StatusTooManyRequests {
		t.Fatal("exhausting GET single budget must not consume list budget")
	}
}

func TestNewRouter_ListBudgetDoesNotConsumeGetSingleBudget(t *testing.T) {
	router := newRouterForRateLimit(t)
	listURL := "/api/chat/channels/11111111-1111-1111-1111-111111111111/messages"
	singleURL := "/api/chat/channels/11111111-1111-1111-1111-111111111111/messages/22222222-2222-2222-2222-222222222222"

	for range msgListRateLimit {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, routerGETRequest(t, listURL))
		if w.Code == http.StatusTooManyRequests {
			t.Fatalf("GET list should stay within its own budget")
		}
	}

	w := httptest.NewRecorder()
	router.ServeHTTP(w, routerGETRequest(t, singleURL))
	if w.Code == http.StatusTooManyRequests {
		t.Fatal("exhausting list budget must not consume GET single budget")
	}
}

// ── WebSocket route auth integration tests (real JWT + real session validator) ─
//
// These tests exercise the full auth chain for RouteWS (/api/chat/ws):
//   WSTokenMiddleware → BearerAuth(real TokenValidator) → RequireActiveSession → wsHandler
//
// They cover the gaps left by handler_test.go, which uses a simulated
// authCheckingUserIDFn and does not exercise JWT validation or session checks.

// newRouterWithWS builds a full router with real JWT auth and a custom wsHandler.
func newRouterWithWS(t *testing.T, wsHandler http.Handler) http.Handler {
	t.Helper()
	validator, err := NewTokenValidator(routerTestSigningKey(), routerTestIssuer, routerTestAudience)
	if err != nil {
		t.Fatalf("NewTokenValidator: %v", err)
	}
	return NewRouter(
		testConfig(),
		platformlog.New("chat-service", "test"),
		ReadinessState{},
		validator,
		allowRouterSessionValidator{},
		NewSidebarHandler(nil),
		NewMessageHandler(nil, nil, nil),
		wsHandler,
		nil,
	)
}

// wsAcceptHandler is a minimal wsHandler that always returns 200 to signal the
// request reached the handler (i.e. passed all auth middleware).
func wsAcceptHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

// TestNewRouter_WS_NoToken_Returns401 verifies that a GET to RouteWS with no
// Authorization header is rejected with 401 by BearerAuth before any upgrade.
func TestNewRouter_WS_NoToken_Returns401(t *testing.T) {
	router := newRouterWithWS(t, wsAcceptHandler())

	req := httptest.NewRequest(http.MethodGet, RouteWS, nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("no-token WS: expected 401, got %d", w.Code)
	}
}

// TestNewRouter_WS_InvalidJWT_Returns401 verifies that a GET to RouteWS with a
// syntactically invalid or wrongly-signed JWT is rejected with 401 by BearerAuth.
func TestNewRouter_WS_InvalidJWT_Returns401(t *testing.T) {
	router := newRouterWithWS(t, wsAcceptHandler())

	req := httptest.NewRequest(http.MethodGet, RouteWS, nil)
	req.Header.Set("Authorization", bearerScheme+"not.a.valid.jwt")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("invalid-JWT WS: expected 401, got %d", w.Code)
	}
}

// TestNewRouter_WS_ValidJWT_ReachesHandler verifies that a GET to RouteWS with
// a correctly signed JWT and an active session reaches the wsHandler. The wsHandler
// used here returns 200 rather than attempting a real upgrade, so no ws library is
// needed. A 200 proves all auth middleware passed without 401/403.
func TestNewRouter_WS_ValidJWT_ReachesHandler(t *testing.T) {
	router := newRouterWithWS(t, wsAcceptHandler())

	req := httptest.NewRequest(http.MethodGet, RouteWS, nil)
	req.Header.Set("Authorization", bearerScheme+makeRouterTestToken(t))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("valid-JWT WS: expected 200 (wsAcceptHandler), got %d — auth may have rejected", w.Code)
	}
}

// TestNewRouter_WS_TokenInQueryString_Returns400 verifies that a GET to RouteWS
// with a credential in the query string is rejected with 400. The wsHandler is
// ws.ServeWS(nil, nil, nil, nil) — the real handler — so this test exercises the
// actual credentialParamNames set from handler.go without duplicating it here.
// rejectCredentialQueryParams fires before requireAuthenticatedUser, so BearerAuth
// passes (valid JWT in header) while the credential-in-QS gate returns 400.
func TestNewRouter_WS_TokenInQueryString_Returns400(t *testing.T) {
	router := newRouterWithWS(t, ws.ServeWS(nil, nil, nil, nil))

	// Provide a valid JWT so BearerAuth passes; the credential-in-QS check fires
	// inside ws.ServeWS before any nil-hub check.
	const credParam = "token"
	req := httptest.NewRequest(http.MethodGet, RouteWS+"?"+credParam+"=something", nil)
	req.Header.Set("Authorization", bearerScheme+makeRouterTestToken(t))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("token-in-QS WS: expected 400, got %d", w.Code)
	}
}
