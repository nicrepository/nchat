package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/services/admin-service/internal/config"
	"github.com/nicrepository/nchat/services/admin-service/internal/domain"
	"github.com/nicrepository/nchat/services/admin-service/internal/service"
)

const (
	testJWTSecret = "0123456789abcdef0123456789abcdef"
	testIssuer    = "nchat-auth"
	testAudience  = "nchat-api"

	testUserID        = "11111111-1111-1111-1111-111111111111"
	testAuthSessionID = "33333333-3333-3333-3333-333333333333"
	testAdminSession  = "22222222-2222-2222-2222-222222222222"

	testOrigin = "https://admin.nchat.test"
)

// stubStore is the database the Admin API would talk to. Every knob here is a
// state an operator can really put the platform in: not an administrator, a
// revoked login, an expired console session, a role removed mid-session.
type stubStore struct {
	principal    domain.AdminPrincipal
	principalErr error
	sessionErr   error
	touchErr     error
	revoked      bool
	revokeErr    error
	audit        []domain.AuditEvent
	auditEntries []domain.AuditEntry
	auditFilter  domain.AuditFilter
	auditListErr error
	pingErr      error
}

func (s *stubStore) AuthorizeHandshake(ctx context.Context, userID string, authSessionID string) (domain.AdminPrincipal, error) {
	return s.ReauthorizeSession(ctx, userID, authSessionID)
}

func (s *stubStore) ReauthorizeSession(_ context.Context, userID string, _ string) (domain.AdminPrincipal, error) {
	if s.principalErr != nil {
		return domain.AdminPrincipal{}, s.principalErr
	}
	principal := s.principal
	principal.UserID = userID
	return principal, nil
}

func (s *stubStore) CreateSession(_ context.Context, input domain.AdminSessionInput) (domain.AdminSession, error) {
	if s.sessionErr != nil {
		return domain.AdminSession{}, s.sessionErr
	}
	return domain.AdminSession{
		ID:                testAdminSession,
		UserID:            input.UserID,
		AuthSessionID:     input.AuthSessionID,
		IdleExpiresAt:     input.IdleExpiresAt,
		AbsoluteExpiresAt: input.AbsoluteExpiresAt,
	}, nil
}

func (s *stubStore) TouchSession(_ context.Context, _ string, idleTTL time.Duration) (domain.AdminSession, error) {
	if s.touchErr != nil {
		return domain.AdminSession{}, s.touchErr
	}
	if s.revoked {
		return domain.AdminSession{}, domain.ErrUnauthorized
	}
	now := time.Now().UTC()
	return domain.AdminSession{
		ID:                testAdminSession,
		UserID:            testUserID,
		AuthSessionID:     testAuthSessionID,
		IdleExpiresAt:     now.Add(idleTTL),
		AbsoluteExpiresAt: now.Add(8 * time.Hour),
	}, nil
}

func (s *stubStore) RevokeSession(_ context.Context, _ string, _ string) error {
	if s.revokeErr != nil {
		return s.revokeErr
	}
	s.revoked = true
	return nil
}

func (s *stubStore) AppendAudit(_ context.Context, event domain.AuditEvent) error {
	s.audit = append(s.audit, event)
	return nil
}

func (s *stubStore) ListAuditEvents(_ context.Context, filter domain.AuditFilter) ([]domain.AuditEntry, error) {
	s.auditFilter = filter
	return s.auditEntries, s.auditListErr
}

// Ping is what /readyz asks. The stub answers for the same store the rest of
// the harness uses, so a harness that models a healthy admin-service reports
// itself ready — and a test that makes the store unhealthy sees 503.
func (s *stubStore) Ping(_ context.Context) error {
	return s.pingErr
}

func (s *stubStore) recordedActions() []string {
	actions := make([]string, 0, len(s.audit))
	for _, event := range s.audit {
		actions = append(actions, event.Action+":"+string(event.Result))
	}
	return actions
}

func adminStore(capabilities ...domain.Capability) *stubStore {
	if len(capabilities) == 0 {
		capabilities = []domain.Capability{domain.CapabilityAuditRead}
	}
	return &stubStore{
		principal: domain.AdminPrincipal{
			Email:        "admin@example.test",
			DisplayName:  "Admin Master",
			AvatarURL:    "/api/auth/avatars/a.png",
			Capabilities: domain.NewCapabilitySet(capabilities),
		},
	}
}

func adminConfig() config.Config {
	return config.Config{
		ServiceName:              "admin-service",
		Env:                      "staging",
		Port:                     8085,
		ReadHeaderTimeoutSeconds: 5,
		DatabaseURL:              "postgres://localhost/nchat",
		AuthJWTHMACSecret:        testJWTSecret,
		AuthJWTIssuer:            testIssuer,
		AuthJWTAudience:          testAudience,
		SessionIdleTTL:           15 * time.Minute,
		SessionAbsoluteTTL:       8 * time.Hour,
		AllowedOrigins:           []string{testOrigin},
	}
}

type testHarness struct {
	router http.Handler
	store  *stubStore
	cfg    config.Config
	auth   *service.AdminSessionService
}

// harnessOption tweaks how the router under test is assembled. Config changes
// and the logger both arrive this way so a spec can say what it is exercising
// without a second constructor.
type harnessOption func(*harnessSettings)

type harnessSettings struct {
	cfg config.Config
	// management is the issue #579 surface. It defaults to nil so the
	// foundation's specs keep exercising a router that serves those paths as
	// unavailable, which is the deployment state they describe.
	management *ManagementPorts
	// configuration is the issue #580 surface, nil by default for the same
	// reason: the foundation's specs describe a deployment that serves those
	// paths as unavailable.
	configuration ConfigAdmin
	logger        *slog.Logger
}

func withManagement(ports *ManagementPorts) harnessOption {
	return func(s *harnessSettings) { s.management = ports }
}

func withConfiguration(configuration ConfigAdmin) harnessOption {
	return func(s *harnessSettings) { s.configuration = configuration }
}

func withConfig(apply func(*config.Config)) harnessOption {
	return func(s *harnessSettings) { apply(&s.cfg) }
}

func withLogger(logger *slog.Logger) harnessOption {
	return func(s *harnessSettings) { s.logger = logger }
}

func newHarness(t *testing.T, store *stubStore, options ...harnessOption) *testHarness {
	t.Helper()
	settings := harnessSettings{cfg: adminConfig(), logger: slog.New(slog.DiscardHandler)}
	for _, apply := range options {
		apply(&settings)
	}
	cfg := settings.cfg
	sessions, err := service.NewAdminSessionService(store, cfg.AuthJWTHMACSecret, cfg.SessionIdleTTL, cfg.SessionAbsoluteTTL)
	if err != nil {
		t.Fatalf("NewAdminSessionService: %v", err)
	}
	validator, err := NewTokenValidator(cfg.AuthJWTHMACSecret, cfg.AuthJWTIssuer, cfg.AuthJWTAudience)
	if err != nil {
		t.Fatalf("NewTokenValidator: %v", err)
	}
	audit := service.NewAuditService(store, settings.logger)
	router := NewRouter(cfg, settings.logger, RouterDependencies{
		TokenValidator:  validator,
		Sessions:        sessions,
		Authenticator:   sessions,
		CSRF:            sessions,
		Audit:           NewAuditPorts(audit, audit),
		RateLimiter:     NewIPRateLimiter(1000, 1000, nil),
		ReadinessPinger: store,
		Management:      settings.management,
		Configuration:   settings.configuration,
	})
	return &testHarness{router: router, store: store, cfg: cfg, auth: sessions}
}

func (h *testHarness) do(request *http.Request) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	h.router.ServeHTTP(response, request)
	return response
}

// establish performs the real handshake and returns the cookie the browser
// would keep, plus the CSRF token the shell would hold.
func (h *testHarness) establish(t *testing.T) (*http.Cookie, string) {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, RouteAdminSession, nil)
	request.Header.Set("Authorization", "Bearer "+signAccessToken(t, testUserID, testAuthSessionID))
	response := h.do(request)
	if response.Code != http.StatusCreated {
		t.Fatalf("establish: expected 201, got %d (%s)", response.Code, response.Body.String())
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("expected exactly one cookie, got %d", len(cookies))
	}
	return cookies[0], decodeEnvelope(t, response).Data.CSRFToken
}

func (h *testHarness) authenticated(t *testing.T, method, path string, cookie *http.Cookie, csrf string) *http.Request {
	t.Helper()
	request := httptest.NewRequest(method, path, nil)
	request.AddCookie(cookie)
	request.Header.Set("Origin", testOrigin)
	if csrf != "" {
		request.Header.Set(csrfHeaderName, csrf)
	}
	return request
}

func signAccessToken(t *testing.T, subject, sessionID string) string {
	t.Helper()
	return signToken(t, subject, sessionID, testIssuer, testJWTSecret)
}

func signWithSecret(t *testing.T, secret string) string {
	t.Helper()
	return signToken(t, testUserID, testAuthSessionID, testIssuer, secret)
}

func signWithIssuer(t *testing.T, issuer string) string {
	t.Helper()
	return signToken(t, testUserID, testAuthSessionID, issuer, testJWTSecret)
}

func signToken(t *testing.T, subject, sessionID, issuer, secret string) string {
	t.Helper()
	now := time.Now().UTC()
	claims := accessClaims{
		SessionID: sessionID,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   subject,
			Issuer:    issuer,
			Audience:  jwt.ClaimStrings{testAudience},
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(15 * time.Minute)),
			ID:        "jwt-1",
		},
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("sign access token: %v", err)
	}
	return signed
}

type bootstrapEnvelope struct {
	Data  bootstrapPayload        `json:"data"`
	Error *httputil.ErrorResponse `json:"error"`
}

func decodeEnvelope(t *testing.T, response *httptest.ResponseRecorder) bootstrapEnvelope {
	t.Helper()
	var envelope bootstrapEnvelope
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode envelope: %v (body %s)", err, response.Body.String())
	}
	return envelope
}
