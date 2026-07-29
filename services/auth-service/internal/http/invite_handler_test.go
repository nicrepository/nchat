//nolint:gosec // Test fixtures intentionally use example opaque/password strings.
package httpapi_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	platformlog "github.com/nicrepository/nchat/libs/go/platform/log"
	"github.com/nicrepository/nchat/services/auth-service/internal/config"
	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
	httpapi "github.com/nicrepository/nchat/services/auth-service/internal/http"
)

type fakeInviteManager struct {
	createResult domain.InviteResult
	acceptResult domain.AcceptInviteResult
	createErr    error
	acceptErr    error
	createGot    domain.AdminInviteInput
	acceptGot    domain.AcceptInviteInput

	bootstrapGot   domain.BootstrapInviteInput
	bootstrapCalls int
}

func (f *fakeInviteManager) CreateInvite(_ context.Context, input domain.AdminInviteInput) (domain.InviteResult, error) {
	f.createGot = input
	return f.createResult, f.createErr
}

func (f *fakeInviteManager) CreateBootstrapInvite(_ context.Context, input domain.BootstrapInviteInput) (domain.InviteResult, error) {
	f.bootstrapGot = input
	f.bootstrapCalls++
	return f.createResult, f.createErr
}

func (f *fakeInviteManager) AcceptInvite(_ context.Context, input domain.AcceptInviteInput) (domain.AcceptInviteResult, error) {
	f.acceptGot = input
	return f.acceptResult, f.acceptErr
}

// Issue #425: the handler reads its workspace and actor from the context the
// guard chain builds. These direct-handler tests exercise it in isolation, so
// they stand in for the guard.
const (
	inviteRetryAfterSeconds = 600
	handlerWorkspaceID      = "9a8b7c6d-5e4f-4a3b-8c2d-1e0f9a8b7c6d"
	handlerActorID          = "3f1c2d4e-5a6b-4c8d-9e0f-1a2b3c4d5e6f"
)

func adminInviteRequest(body io.Reader) *http.Request {
	req := httptest.NewRequest(http.MethodPost, httpapi.RouteAuthAdminInvites, body)
	req.Header.Set("Content-Type", "application/json")
	return httpapi.WithAdminContext(req, handlerWorkspaceID, handlerActorID)
}

func TestAdminCreateInviteSuccessReturnsSafeSummary(t *testing.T) {
	svc := &fakeInviteManager{createResult: domain.InviteResult{ID: "invite-1", Email: "user@example.com", CreatedAt: time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)}}
	handler := httpapi.AdminCreateInvite(svc, inviteRetryAfterSeconds)
	rec := httptest.NewRecorder()
	req := adminInviteRequest(strings.NewReader(`{"email":"user@example.com","display_name":"User","full_name":"User Full"}`))

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d body=%s", rec.Code, rec.Body.String())
	}
	if svc.createGot.Email != "user@example.com" || svc.createGot.DisplayName != "User" || svc.createGot.FullName != "User Full" {
		t.Fatalf("unexpected service input: %+v", svc.createGot)
	}
	body := rec.Body.String()
	if strings.Contains(body, "token") || strings.Contains(body, "hash") || strings.Contains(body, "password") {
		t.Fatalf("invite response must not expose secrets: %s", body)
	}
}

func TestAdminCreateInviteDuplicateOrPendingReturns409(t *testing.T) {
	for _, err := range []error{domain.ErrDuplicateEmail, domain.ErrInviteAlreadyPending} {
		handler := httpapi.AdminCreateInvite(&fakeInviteManager{createErr: err}, inviteRetryAfterSeconds)
		rec := httptest.NewRecorder()
		req := adminInviteRequest(strings.NewReader(`{"email":"user@example.com","display_name":"User"}`))

		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusConflict {
			t.Fatalf("expected 409 for %v, got %d body=%s", err, rec.Code, rec.Body.String())
		}
	}
}

func TestAdminCreateInviteOutboxUnavailableReturns503(t *testing.T) {
	handler := httpapi.AdminCreateInvite(&fakeInviteManager{createErr: domain.ErrEmailOutboxUnavailable}, inviteRetryAfterSeconds)
	rec := httptest.NewRecorder()
	req := adminInviteRequest(strings.NewReader(`{"email":"user@example.com","display_name":"User"}`))

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAdminCreateInviteGuardRejectsMissingOrWrongToken(t *testing.T) {
	cfg := config.Config{ServiceName: "auth-service", Env: "test"}
	router := httpapi.NewRouter(cfg, platformlog.New("auth-service", "test"), nil, nil, nil, nil, &fakeInviteManager{}, nil, nil, nil, nil, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, httpapi.RouteAdminInvites, strings.NewReader(`{"email":"user@example.com","display_name":"User"}`)))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 with empty ADMIN_BOOTSTRAP_TOKEN, got %d", rec.Code)
	}

	cfg.AdminBootstrapToken = "expected-credential"
	router = httpapi.NewRouter(cfg, platformlog.New("auth-service", "test"), nil, nil, nil, nil, &fakeInviteManager{}, nil, nil, nil, nil, nil)
	rec = httptest.NewRecorder()
	// Deliberately the bootstrap route, not the browser one: this asserts the
	// bootstrap-token guard still rejects a wrong token.
	req := httptest.NewRequest(http.MethodPost, httpapi.RouteAdminInvites, strings.NewReader(`{"email":"user@example.com","display_name":"User"}`))
	req.Header.Set("X-NChat-Admin-Token", "wrong-credential")
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 with wrong admin token, got %d", rec.Code)
	}
}

func TestAuthAcceptInviteSuccessReturnsSafeUserSummary(t *testing.T) {
	svc := &fakeInviteManager{acceptResult: domain.AcceptInviteResult{UserID: "user-1", Email: "user@example.com", DisplayName: "User", FullName: "User Full", CreatedAt: time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)}}
	handler := httpapi.AuthAcceptInvite(svc)
	rec := httptest.NewRecorder()
	submitted := makeTestOpaqueValue("invite-success")
	req := httptest.NewRequest(http.MethodPost, httpapi.RouteAuthInvitesAccept, strings.NewReader(`{"token":"`+submitted+`","display_name":"User","full_name":"User Full","password":"StrongPassword@123"}`))

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d body=%s", rec.Code, rec.Body.String())
	}
	if svc.acceptGot.Token != submitted || svc.acceptGot.Password != "StrongPassword@123" {
		t.Fatalf("unexpected service input: %+v", svc.acceptGot)
	}
	body := rec.Body.String()
	if strings.Contains(body, submitted) || strings.Contains(body, "password") || strings.Contains(body, "hash") || strings.Contains(body, "access_token") || strings.Contains(body, "refresh_token") {
		t.Fatalf("accept response must not expose secrets or session tokens: %s", body)
	}
}

func TestAuthAcceptInviteInvalidTokenReturnsGeneric401(t *testing.T) {
	handler := httpapi.AuthAcceptInvite(&fakeInviteManager{acceptErr: domain.ErrInvalidToken})
	rec := httptest.NewRecorder()
	submitted := makeTestOpaqueValue("invite-invalid")
	req := httptest.NewRequest(http.MethodPost, httpapi.RouteAuthInvitesAccept, strings.NewReader(`{"token":"`+submitted+`","display_name":"User","password":"StrongPassword@123"}`))

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), submitted) {
		t.Fatalf("invalid invite response must not echo token: %s", rec.Body.String())
	}
}

func TestAuthAcceptInviteWeakPasswordReturns400(t *testing.T) {
	handler := httpapi.AuthAcceptInvite(&fakeInviteManager{acceptErr: domain.ErrPasswordPolicy})
	rec := httptest.NewRecorder()
	submitted := makeTestOpaqueValue("invite-weak-password")
	req := httptest.NewRequest(http.MethodPost, httpapi.RouteAuthInvitesAccept, strings.NewReader(`{"token":"`+submitted+`","display_name":"User","password":"weak"}`))

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAuthAcceptInviteOversizedBodyReturns413(t *testing.T) {
	svc := &fakeInviteManager{}
	handler := httpapi.AuthAcceptInvite(svc)
	rec := httptest.NewRecorder()
	body := `{"token":"` + strings.Repeat("t", 5000) + `","display_name":"User","password":"StrongPassword@123"}`
	req := httptest.NewRequest(http.MethodPost, httpapi.RouteAuthInvitesAccept, strings.NewReader(body))

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d body=%s", rec.Code, rec.Body.String())
	}
	if svc.acceptGot.Token != "" {
		t.Fatalf("oversized body must not reach service, got %+v", svc.acceptGot)
	}
}

func TestAuthAcceptInviteRouterRateLimitsPublicEndpoint(t *testing.T) {
	cfg := config.Config{ServiceName: "auth-service", Env: "test", AuthTokenEndpointRateLimitPerMinute: 60, AuthTokenEndpointRateLimitBurst: 1}
	router := httpapi.NewRouter(cfg, platformlog.New("auth-service", "test"), nil, nil, nil, nil, &fakeInviteManager{acceptResult: domain.AcceptInviteResult{UserID: "user-1", Email: "user@example.com", DisplayName: "User", CreatedAt: time.Now()}}, nil, nil, nil, nil, nil)

	submitted := makeTestOpaqueValue("invite-rate-limit")
	body := `{"token":"` + submitted + `","display_name":"User","password":"StrongPassword@123"}`
	first := httptest.NewRecorder()
	firstReq := httptest.NewRequest(http.MethodPost, httpapi.RouteAuthInvitesAccept, strings.NewReader(body))
	firstReq.RemoteAddr = "203.0.113.50:12345"
	router.ServeHTTP(first, firstReq)
	if first.Code != http.StatusCreated {
		t.Fatalf("expected first request 201, got %d body=%s", first.Code, first.Body.String())
	}

	second := httptest.NewRecorder()
	secondReq := httptest.NewRequest(http.MethodPost, httpapi.RouteAuthInvitesAccept, strings.NewReader(body))
	secondReq.RemoteAddr = "203.0.113.50:12345"
	router.ServeHTTP(second, secondReq)
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("expected second request 429, got %d body=%s", second.Code, second.Body.String())
	}
}

func TestAuthInviteHandlersUnavailableReturn503(t *testing.T) {
	for _, handler := range []http.Handler{httpapi.AdminCreateInvite(nil, inviteRetryAfterSeconds), httpapi.AuthAcceptInvite(nil)} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, httpapi.RouteAuthInvitesAccept, strings.NewReader(`{}`))
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("expected 503, got %d", rec.Code)
		}
	}
}

func TestAuthAcceptInviteInternalErrorReturns500(t *testing.T) {
	handler := httpapi.AuthAcceptInvite(&fakeInviteManager{acceptErr: errors.New("db down")})
	rec := httptest.NewRecorder()
	submitted := makeTestOpaqueValue("invite-internal")
	req := httptest.NewRequest(http.MethodPost, httpapi.RouteAuthInvitesAccept, strings.NewReader(`{"token":"`+submitted+`","display_name":"User","password":"StrongPassword@123"}`))

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
}
