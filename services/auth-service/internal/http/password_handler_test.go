//nolint:gosec // Test fixtures intentionally use example opaque/password strings.
package httpapi_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	platformlog "github.com/nicrepository/nchat/libs/go/platform/log"
	"github.com/nicrepository/nchat/services/auth-service/internal/config"
	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
	httpapi "github.com/nicrepository/nchat/services/auth-service/internal/http"
)

type fakePasswordRecoveryService struct {
	forgotErr error
	resetErr  error
	forgotGot domain.ForgotPasswordInput
	resetGot  domain.ResetPasswordInput
}

func (f *fakePasswordRecoveryService) ForgotPassword(_ context.Context, input domain.ForgotPasswordInput) error {
	f.forgotGot = input
	return f.forgotErr
}

func (f *fakePasswordRecoveryService) ResetPassword(_ context.Context, input domain.ResetPasswordInput) error {
	f.resetGot = input
	return f.resetErr
}

func TestAuthForgotPasswordAlwaysReturns202ForServiceSuccess(t *testing.T) {
	svc := &fakePasswordRecoveryService{}
	handler := httpapi.AuthForgotPassword(svc)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, httpapi.RouteAuthPasswordForgot, strings.NewReader(`{"email":"user@example.com"}`))

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}
	if svc.forgotGot.Email != "user@example.com" {
		t.Fatalf("expected email passed to service, got %+v", svc.forgotGot)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("expected empty body, got %s", rec.Body.String())
	}
}

func TestAuthForgotPasswordKnownAndUnknownResponsesAreIdentical(t *testing.T) {
	handler := httpapi.AuthForgotPassword(&fakePasswordRecoveryService{})

	known := httptest.NewRecorder()
	handler.ServeHTTP(known, httptest.NewRequest(http.MethodPost, httpapi.RouteAuthPasswordForgot, strings.NewReader(`{"email":"known@example.com"}`)))
	unknown := httptest.NewRecorder()
	handler.ServeHTTP(unknown, httptest.NewRequest(http.MethodPost, httpapi.RouteAuthPasswordForgot, strings.NewReader(`{"email":"unknown@example.com"}`)))

	if known.Code != unknown.Code || known.Body.String() != unknown.Body.String() {
		t.Fatalf("forgot responses must be identical, known=%d %q unknown=%d %q", known.Code, known.Body.String(), unknown.Code, unknown.Body.String())
	}
}

func TestAuthForgotPasswordResponseDoesNotLeakTokenOrHash(t *testing.T) {
	handler := httpapi.AuthForgotPassword(&fakePasswordRecoveryService{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, httpapi.RouteAuthPasswordForgot, strings.NewReader(`{"email":"user@example.com"}`))

	handler.ServeHTTP(rec, req)

	body := rec.Body.String()
	if strings.Contains(body, "token") || strings.Contains(body, "hash") || strings.Contains(body, "password") {
		t.Fatalf("forgot response must not contain sensitive fields: %s", body)
	}
}

func TestAuthForgotPasswordOversizedBodyReturns413(t *testing.T) {
	svc := &fakePasswordRecoveryService{}
	handler := httpapi.AuthForgotPassword(svc)
	rec := httptest.NewRecorder()
	body := `{"email":"` + strings.Repeat("a", 5000) + `@example.com"}`
	req := httptest.NewRequest(http.MethodPost, httpapi.RouteAuthPasswordForgot, strings.NewReader(body))

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d body=%s", rec.Code, rec.Body.String())
	}
	if svc.forgotGot.Email != "" {
		t.Fatalf("oversized body must not reach service, got %+v", svc.forgotGot)
	}
}

func TestAuthResetPasswordSuccessReturns204(t *testing.T) {
	svc := &fakePasswordRecoveryService{}
	handler := httpapi.AuthResetPassword(svc)
	rec := httptest.NewRecorder()
	submitted := makeTestOpaqueValue("reset-success")
	req := httptest.NewRequest(http.MethodPost, httpapi.RouteAuthPasswordReset, strings.NewReader(`{"token":"`+submitted+`","new_password":"NewStrongPassword@123"}`))

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d body=%s", rec.Code, rec.Body.String())
	}
	if svc.resetGot.Token != submitted || svc.resetGot.NewPassword != "NewStrongPassword@123" {
		t.Fatalf("unexpected service input: %+v", svc.resetGot)
	}
}

func TestAuthResetPasswordInvalidTokenReturnsGeneric401(t *testing.T) {
	handler := httpapi.AuthResetPassword(&fakePasswordRecoveryService{resetErr: domain.ErrInvalidToken})
	rec := httptest.NewRecorder()
	submitted := makeTestOpaqueValue("reset-invalid")
	req := httptest.NewRequest(http.MethodPost, httpapi.RouteAuthPasswordReset, strings.NewReader(`{"token":"`+submitted+`","new_password":"NewStrongPassword@123"}`))

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "invalid_token") || strings.Contains(body, submitted) {
		t.Fatalf("expected generic invalid token response without raw token, got %s", body)
	}
}

func TestAuthResetPasswordWeakPasswordReturns400(t *testing.T) {
	handler := httpapi.AuthResetPassword(&fakePasswordRecoveryService{resetErr: domain.ErrPasswordPolicy})
	rec := httptest.NewRecorder()
	submitted := makeTestOpaqueValue("reset-weak-password")
	req := httptest.NewRequest(http.MethodPost, httpapi.RouteAuthPasswordReset, strings.NewReader(`{"token":"`+submitted+`","new_password":"weak"}`))

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAuthResetPasswordOversizedBodyReturns413(t *testing.T) {
	svc := &fakePasswordRecoveryService{}
	handler := httpapi.AuthResetPassword(svc)
	rec := httptest.NewRecorder()
	body := `{"token":"` + strings.Repeat("t", 5000) + `","new_password":"NewStrongPassword@123"}`
	req := httptest.NewRequest(http.MethodPost, httpapi.RouteAuthPasswordReset, strings.NewReader(body))

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d body=%s", rec.Code, rec.Body.String())
	}
	if svc.resetGot.Token != "" {
		t.Fatalf("oversized body must not reach service, got %+v", svc.resetGot)
	}
}

func TestAuthForgotPasswordOutboxUnavailableReturns503(t *testing.T) {
	handler := httpapi.AuthForgotPassword(&fakePasswordRecoveryService{forgotErr: domain.ErrEmailOutboxUnavailable})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, httpapi.RouteAuthPasswordForgot, strings.NewReader(`{"email":"user@example.com"}`))

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAuthPasswordRecoveryUnavailableReturns503(t *testing.T) {
	for _, handler := range []http.Handler{httpapi.AuthForgotPassword(nil), httpapi.AuthResetPassword(nil)} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, httpapi.RouteAuthPasswordForgot, strings.NewReader(`{}`))
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("expected 503, got %d", rec.Code)
		}
	}
}

func TestAuthPasswordRecoveryInternalErrorReturns500(t *testing.T) {
	handler := httpapi.AuthResetPassword(&fakePasswordRecoveryService{resetErr: errors.New("db down")})
	rec := httptest.NewRecorder()
	submitted := makeTestOpaqueValue("reset-internal")
	req := httptest.NewRequest(http.MethodPost, httpapi.RouteAuthPasswordReset, strings.NewReader(`{"token":"`+submitted+`","new_password":"NewStrongPassword@123"}`))

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
}

func TestAuthPasswordRecoveryRouterRateLimitsPublicEndpoints(t *testing.T) {
	cfg := config.Config{ServiceName: "auth-service", Env: "test", AuthTokenEndpointRateLimitPerMinute: 60, AuthTokenEndpointRateLimitBurst: 1}
	router := httpapi.NewRouter(cfg, platformlog.New("auth-service", "test"), nil, nil, nil, &fakePasswordRecoveryService{}, nil)

	first := httptest.NewRecorder()
	firstReq := httptest.NewRequest(http.MethodPost, httpapi.RouteAuthPasswordForgot, strings.NewReader(`{"email":"user@example.com"}`))
	firstReq.RemoteAddr = "203.0.113.40:12345"
	router.ServeHTTP(first, firstReq)
	if first.Code != http.StatusAccepted {
		t.Fatalf("expected first request 202, got %d body=%s", first.Code, first.Body.String())
	}

	second := httptest.NewRecorder()
	secondReq := httptest.NewRequest(http.MethodPost, httpapi.RouteAuthPasswordForgot, strings.NewReader(`{"email":"user@example.com"}`))
	secondReq.RemoteAddr = "203.0.113.40:12345"
	router.ServeHTTP(second, secondReq)
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("expected second request 429, got %d body=%s", second.Code, second.Body.String())
	}
}
