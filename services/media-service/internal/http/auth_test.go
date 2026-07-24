package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	testAuthUserID    = "11111111-1111-4111-8111-111111111111"
	testAuthSessionID = "22222222-2222-4222-8222-222222222222"
	testAuthIssuer    = "nchat-auth-test"
	testAuthAudience  = "nchat-api-test"
)

func TestBearerAuthInjectsValidatedPrincipal(t *testing.T) {
	secret := strings.Repeat("s", 32)
	validator, err := NewTokenValidator(secret, testAuthIssuer, testAuthAudience)
	if err != nil {
		t.Fatalf("new validator: %v", err)
	}
	expiresAt := time.Now().UTC().Add(10 * time.Minute).Truncate(time.Second)
	token := signMediaAccessToken(t, secret, mediaTestClaims(expiresAt))

	called := false
	handler := BearerAuth(validator)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		principal, ok := AuthenticatedPrincipal(r)
		if !ok {
			t.Fatal("expected authenticated principal")
		}
		if principal.UserID != testAuthUserID || principal.SessionID != testAuthSessionID {
			t.Fatalf("unexpected principal: %+v", principal)
		}
		if !principal.AccessExpiresAt.Equal(expiresAt) {
			t.Fatalf("expected access expiry %v, got %v", expiresAt, principal.AccessExpiresAt)
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, RouteLiveKitToken, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)

	if !called || response.Code != http.StatusNoContent {
		t.Fatalf("expected authenticated handler call, status=%d called=%v", response.Code, called)
	}
}

func TestBearerAuthRejectsMissingAndInvalidTokens(t *testing.T) {
	secret := strings.Repeat("s", 32)
	validator, err := NewTokenValidator(secret, testAuthIssuer, testAuthAudience)
	if err != nil {
		t.Fatalf("new validator: %v", err)
	}

	validClaims := mediaTestClaims(time.Now().UTC().Add(time.Hour))
	tests := []struct {
		name   string
		header string
	}{
		{name: "missing"},
		{name: "empty bearer", header: "Bearer "},
		{name: "wrong scheme", header: "Basic abc"},
		{name: "malformed", header: "Bearer not-a-jwt"},
		{name: "wrong signature", header: "Bearer " + signMediaAccessToken(t, strings.Repeat("x", 32), validClaims)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			handler := BearerAuth(validator)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				called = true
			}))
			req := httptest.NewRequest(http.MethodPost, RouteLiveKitToken, nil)
			req.Header.Set("Authorization", tt.header)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, req)
			if response.Code != http.StatusUnauthorized || called {
				t.Fatalf("expected 401 without handler call, got %d called=%v", response.Code, called)
			}
		})
	}
}

func TestBearerAuthRejectsInvalidRequiredClaims(t *testing.T) {
	secret := strings.Repeat("s", 32)
	validator, err := NewTokenValidator(secret, testAuthIssuer, testAuthAudience)
	if err != nil {
		t.Fatalf("new validator: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*mediaAccessClaims)
	}{
		{name: "invalid user UUID", mutate: func(c *mediaAccessClaims) { c.Subject = "not-a-uuid" }},
		{name: "missing sid", mutate: func(c *mediaAccessClaims) { c.SessionID = "" }},
		{name: "invalid sid UUID", mutate: func(c *mediaAccessClaims) { c.SessionID = "not-a-uuid" }},
		{name: "missing jti", mutate: func(c *mediaAccessClaims) { c.ID = "" }},
		{name: "missing iat", mutate: func(c *mediaAccessClaims) { c.IssuedAt = nil }},
		{name: "missing nbf", mutate: func(c *mediaAccessClaims) { c.NotBefore = nil }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			claims := mediaTestClaims(time.Now().UTC().Add(time.Hour))
			tt.mutate(&claims)
			token := signMediaAccessToken(t, secret, claims)
			req := httptest.NewRequest(http.MethodPost, RouteLiveKitToken, nil)
			req.Header.Set("Authorization", "Bearer "+token)
			response := httptest.NewRecorder()
			BearerAuth(validator)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				t.Fatal("invalid claims reached handler")
			})).ServeHTTP(response, req)
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("expected 401, got %d", response.Code)
			}
		})
	}
}

func TestNewTokenValidatorRejectsUnsafeConfiguration(t *testing.T) {
	for _, tc := range []struct {
		secret, issuer, audience string
	}{
		{secret: "short", issuer: testAuthIssuer, audience: testAuthAudience},
		{secret: strings.Repeat("s", 32), audience: testAuthAudience},
		{secret: strings.Repeat("s", 32), issuer: testAuthIssuer},
	} {
		if _, err := NewTokenValidator(tc.secret, tc.issuer, tc.audience); err == nil {
			t.Fatal("expected unsafe validator configuration to fail")
		}
	}
}

func TestBearerAuthWithoutValidatorReturnsServiceUnavailable(t *testing.T) {
	response := httptest.NewRecorder()
	BearerAuth(nil)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("nil validator reached handler")
	})).ServeHTTP(response, httptest.NewRequest(http.MethodPost, RouteLiveKitToken, nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", response.Code)
	}
}

func mediaTestClaims(expiresAt time.Time) mediaAccessClaims {
	now := time.Now().UTC().Add(-time.Second)
	return mediaAccessClaims{
		SessionID: testAuthSessionID,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject: testAuthUserID, Issuer: testAuthIssuer,
			Audience: jwt.ClaimStrings{testAuthAudience},
			IssuedAt: jwt.NewNumericDate(now), NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(expiresAt), ID: "test-jti",
		},
	}
}

func signMediaAccessToken(t *testing.T, secret string, claims mediaAccessClaims) string {
	t.Helper()
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return token
}
