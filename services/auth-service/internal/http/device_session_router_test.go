package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	platformlog "github.com/nicrepository/nchat/libs/go/platform/log"
	"github.com/nicrepository/nchat/services/auth-service/internal/config"
	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
	"github.com/nicrepository/nchat/services/auth-service/internal/service"
)

// Low-entropy UUID fixtures for middleware unit tests.
// All-zero prefixes are intentionally non-secret; they exist only to satisfy
// the UUID format required by RequireActiveSession.
const (
	testSIDServiceUnavailable = "00000000-0000-0000-0000-000000000001"
	testSIDStoreError         = "00000000-0000-0000-0000-000000000002"
)

type routerDeviceSessionStub struct {
	validateErr error

	validateCalls      int
	listSessionsCalls  int
	listDevicesCalls   int
	revokeSessionCalls int
	revokeAllCalls     int

	lastValidateUserID    string
	lastValidateSessionID string
	lastRevokedSessionID  string
	lastExceptSessionID   string
	sequence              []string
}

func (s *routerDeviceSessionStub) ValidateActiveSession(_ context.Context, userID, sessionID string) error {
	s.validateCalls++
	s.lastValidateUserID = userID
	s.lastValidateSessionID = sessionID
	s.sequence = append(s.sequence, "validate")
	return s.validateErr
}

func (s *routerDeviceSessionStub) ListSessions(_ context.Context, _ string, _ bool, _ int) ([]domain.SessionInfo, error) {
	s.listSessionsCalls++
	s.sequence = append(s.sequence, "listSessions")
	return nil, nil
}

func (s *routerDeviceSessionStub) RevokeSession(_ context.Context, sessionID, _ string) error {
	s.revokeSessionCalls++
	s.lastRevokedSessionID = sessionID
	s.sequence = append(s.sequence, "revokeSession")
	return nil
}

func (s *routerDeviceSessionStub) RevokeAllSessionsExcept(_ context.Context, _, exceptSessionID string) error {
	s.revokeAllCalls++
	s.lastExceptSessionID = exceptSessionID
	s.sequence = append(s.sequence, "revokeAll")
	return nil
}

func (s *routerDeviceSessionStub) ListDevices(_ context.Context, _, _ string, _ bool, _ int) ([]domain.DeviceInfo, domain.DeviceSessionPolicy, error) {
	s.listDevicesCalls++
	s.sequence = append(s.sequence, "listDevices")
	return nil, domain.DeviceSessionPolicy{MaxDevicesPerUser: 5}, nil
}

func (s *routerDeviceSessionStub) RevokeDevice(_ context.Context, _, _ string) error { return nil }

func (s *routerDeviceSessionStub) UpdateDeviceDisplayName(_ context.Context, _, _, _ string) error {
	return nil
}

func TestDeviceSessionRoutes_RequireActiveCurrentSession(t *testing.T) {
	cfg := testConfigWithJWT()
	userID := "123e4567-e89b-12d3-a456-426614174100"
	sessionID := "123e4567-e89b-12d3-a456-426614174101"
	token := signRouterAccessToken(t, cfg, userID, sessionID)

	tests := []struct {
		name          string
		method        string
		path          string
		wantListCalls func(*routerDeviceSessionStub) int
	}{
		{
			name:          "revoked current session cannot list sessions",
			method:        http.MethodGet,
			path:          RouteAuthMeSessions,
			wantListCalls: func(s *routerDeviceSessionStub) int { return s.listSessionsCalls },
		},
		{
			name:          "revoked current session cannot list devices",
			method:        http.MethodGet,
			path:          RouteAuthMeDevices,
			wantListCalls: func(s *routerDeviceSessionStub) int { return s.listDevicesCalls },
		},
		{
			name:          "idle-expired current session is rejected",
			method:        http.MethodGet,
			path:          RouteAuthMeSessions,
			wantListCalls: func(s *routerDeviceSessionStub) int { return s.listSessionsCalls },
		},
		{
			name:          "absolute-expired current session is rejected",
			method:        http.MethodGet,
			path:          RouteAuthMeSessions,
			wantListCalls: func(s *routerDeviceSessionStub) int { return s.listSessionsCalls },
		},
		{
			name:          "cross-user current session is rejected",
			method:        http.MethodGet,
			path:          RouteAuthMeDevices,
			wantListCalls: func(s *routerDeviceSessionStub) int { return s.listDevicesCalls },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stub := &routerDeviceSessionStub{validateErr: domain.ErrInvalidToken}
			router := NewRouter(cfg, platformlog.New("auth-service", "test"), nil, nil, nil, nil, nil, nil, stub, stub, nil, nil, allowAllBootstrapAttempts{})
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(tt.method, tt.path, nil)
			req.Header.Set("Authorization", "Bearer "+token)

			router.ServeHTTP(rec, req)

			assertJSONResponse(t, rec, http.StatusUnauthorized)
			if stub.validateCalls != 1 {
				t.Fatalf("expected one active-session validation, got %d", stub.validateCalls)
			}
			if stub.lastValidateUserID != userID || stub.lastValidateSessionID != sessionID {
				t.Fatalf("validated (%q, %q), want (%q, %q)", stub.lastValidateUserID, stub.lastValidateSessionID, userID, sessionID)
			}
			if calls := tt.wantListCalls(stub); calls != 0 {
				t.Fatalf("handler should not be called after invalid current session, got %d calls", calls)
			}
		})
	}
}

func TestDeleteAllMySessions_InvalidCurrentSessionDoesNotRevokeOthers(t *testing.T) {
	cfg := testConfigWithJWT()
	token := signRouterAccessToken(t, cfg, "123e4567-e89b-12d3-a456-426614174200", "123e4567-e89b-12d3-a456-426614174201")
	stub := &routerDeviceSessionStub{validateErr: domain.ErrInvalidToken}
	router := NewRouter(cfg, platformlog.New("auth-service", "test"), nil, nil, nil, nil, nil, nil, stub, stub, nil, nil, allowAllBootstrapAttempts{})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, RouteAuthMeSessions, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	router.ServeHTTP(rec, req)

	assertJSONResponse(t, rec, http.StatusUnauthorized)
	if stub.revokeAllCalls != 0 {
		t.Fatalf("bulk revoke must not run for invalid current session, got %d calls", stub.revokeAllCalls)
	}
}

func TestDeleteMySession_CurrentActiveSessionCanRevokeItself(t *testing.T) {
	cfg := testConfigWithJWT()
	userID := "123e4567-e89b-12d3-a456-426614174300"
	currentSID := "123e4567-e89b-12d3-a456-426614174301"
	token := signRouterAccessToken(t, cfg, userID, currentSID)
	stub := &routerDeviceSessionStub{}
	router := NewRouter(cfg, platformlog.New("auth-service", "test"), nil, nil, nil, nil, nil, nil, stub, stub, nil, nil, allowAllBootstrapAttempts{})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/auth/me/sessions/"+currentSID, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}
	if stub.lastRevokedSessionID != currentSID {
		t.Fatalf("expected current session %q revoked, got %q", currentSID, stub.lastRevokedSessionID)
	}
	wantSequence := "validate,revokeSession"
	if got := strings.Join(stub.sequence, ","); got != wantSequence {
		t.Fatalf("expected validation before revocation, got %s", got)
	}
}

func TestDeleteAllMySessions_MissingSIDTokenIsPubliclyRejected(t *testing.T) {
	cfg := testConfigWithJWT()
	token := signRouterAccessToken(t, cfg, "123e4567-e89b-12d3-a456-426614174400", "")
	stub := &routerDeviceSessionStub{}
	router := NewRouter(cfg, platformlog.New("auth-service", "test"), nil, nil, nil, nil, nil, nil, stub, stub, nil, nil, allowAllBootstrapAttempts{})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, RouteAuthMeSessions, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	router.ServeHTTP(rec, req)

	assertJSONResponse(t, rec, http.StatusUnauthorized)
	if stub.validateCalls != 0 || stub.revokeAllCalls != 0 {
		t.Fatalf("missing sid should fail in public auth before validation/revocation, validate=%d revokeAll=%d", stub.validateCalls, stub.revokeAllCalls)
	}
}

func TestDeviceSessionRoutes_MethodNotAllowedUsesJSONEnvelopeAndAllow(t *testing.T) {
	cfg := testConfigWithJWT()
	stub := &routerDeviceSessionStub{}
	router := NewRouter(cfg, platformlog.New("auth-service", "test"), nil, nil, nil, nil, nil, nil, stub, stub, nil, nil, allowAllBootstrapAttempts{})

	tests := []struct {
		name    string
		method  string
		path    string
		allowed []string
	}{
		{name: "sessions collection", method: http.MethodPost, path: RouteAuthMeSessions, allowed: []string{http.MethodDelete, http.MethodGet}},
		{name: "session by id", method: http.MethodGet, path: "/auth/me/sessions/123e4567-e89b-12d3-a456-426614174500", allowed: []string{http.MethodDelete}},
		{name: "devices collection", method: http.MethodPost, path: RouteAuthMeDevices, allowed: []string{http.MethodGet}},
		{name: "device by id", method: http.MethodGet, path: "/auth/me/devices/123e4567-e89b-12d3-a456-426614174501", allowed: []string{http.MethodDelete, http.MethodPatch}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(tt.method, tt.path, nil))

			assertJSONResponse(t, rec, http.StatusMethodNotAllowed)
			assertAllowHeader(t, rec.Header().Get("Allow"), tt.allowed)
		})
	}
}

func testConfigWithJWT() config.Config {
	cfg := testConfig()
	cfg.AuthJWTHMACSecret = strings.Repeat("z", 32)
	cfg.AuthJWTIssuer = "router-test-issuer"
	cfg.AuthJWTAudience = "router-test-audience"
	cfg.AuthAccessTokenTTLSeconds = 900
	cfg.AuthRefreshTokenTTLSeconds = 86400
	return cfg
}

func signRouterAccessToken(t *testing.T, cfg config.Config, userID, sessionID string) string {
	t.Helper()

	now := time.Now().UTC()
	claims := service.AccessClaims{
		SessionID: sessionID,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID,
			Issuer:    cfg.AuthJWTIssuer,
			Audience:  jwt.ClaimStrings{cfg.AuthJWTAudience},
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(15 * time.Minute)),
			ID:        "router-test-jti",
		},
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(cfg.AuthJWTHMACSecret))
	if err != nil {
		t.Fatalf("sign access token: %v", err)
	}
	return token
}

func assertAllowHeader(t *testing.T, got string, want []string) {
	t.Helper()
	if got == "" {
		t.Fatal("expected Allow header")
	}
	parts := strings.Split(got, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	sort.Strings(parts)
	sort.Strings(want)
	if strings.Join(parts, ",") != strings.Join(want, ",") {
		t.Fatalf("Allow header %q, want %q", got, strings.Join(want, ", "))
	}
}

func TestRequireActiveSession_ServiceUnavailableWhenValidatorMissing(t *testing.T) {
	handler := RequireActiveSession(nil)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, RouteAuthMeSessions, nil)
	req = req.WithContext(context.WithValue(req.Context(), ctxKeyUserID, "user-1"))
	req = req.WithContext(context.WithValue(req.Context(), ctxKeySessionID, testSIDServiceUnavailable))

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
}

func TestRequireActiveSession_MalformedSIDReturns401BeforeStore(t *testing.T) {
	stub := &routerDeviceSessionStub{}
	handler := RequireActiveSession(stub)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, RouteAuthMeSessions, nil)
	req = req.WithContext(context.WithValue(req.Context(), ctxKeyUserID, "user-1"))
	req = req.WithContext(context.WithValue(req.Context(), ctxKeySessionID, "not-a-uuid"))

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
	if stub.validateCalls != 0 {
		t.Fatalf("store should not be called for malformed sid, got %d calls", stub.validateCalls)
	}
}

func TestRequireActiveSession_StoreErrorReturns500(t *testing.T) {
	stub := &routerDeviceSessionStub{validateErr: errors.New("db down")}
	handler := RequireActiveSession(stub)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, RouteAuthMeSessions, nil)
	req = req.WithContext(context.WithValue(req.Context(), ctxKeyUserID, "user-1"))
	req = req.WithContext(context.WithValue(req.Context(), ctxKeySessionID, testSIDStoreError))

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
}
