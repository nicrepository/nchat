package httpapi_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	httpapi "github.com/nicrepository/nchat/services/chat-service/internal/http"
)

// ---- helpers ----

const (
	testHMACSecret = "test-hmac-secret-must-be-at-least-32-bytes!"
	testIssuer     = "nchat-auth"
	testAudience   = "nchat-api"
	testUserID     = "user-abc-123"
)

func makeTestToken(t *testing.T, userID, secret, issuer, audience string, expiry time.Duration) string {
	t.Helper()
	claims := jwt.MapClaims{
		"sub": userID,
		"sid": "session-1",
		"iss": issuer,
		"aud": jwt.ClaimStrings{audience},
		"iat": time.Now().Unix(),
		"nbf": time.Now().Unix(),
		"exp": time.Now().Add(expiry).Unix(),
		"jti": "jti-1",
	}
	tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("sign test token: %v", err)
	}
	return tok
}

func makeTestValidator(t *testing.T) *httpapi.TokenValidator {
	t.Helper()
	v, err := httpapi.NewTokenValidator(testHMACSecret, testIssuer, testAudience)
	if err != nil {
		t.Fatalf("new token validator: %v", err)
	}
	return v
}

// ---- BearerAuth middleware tests ----

func TestBearerAuth_NilValidator_Returns503(t *testing.T) {
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	mw := httpapi.BearerAuth(nil)(next)
	rr := httptest.NewRecorder()
	mw.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rr.Code)
	}
	if called {
		t.Fatal("next handler must not be called when validator is nil")
	}
}

func TestBearerAuth_MissingHeader_Returns401(t *testing.T) {
	v := makeTestValidator(t)
	mw := httpapi.BearerAuth(v)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rr := httptest.NewRecorder()
	mw.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
}

func TestBearerAuth_InvalidToken_Returns401(t *testing.T) {
	v := makeTestValidator(t)
	mw := httpapi.BearerAuth(v)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer notavalidjwt")
	mw.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
}

func TestBearerAuth_ExpiredToken_Returns401(t *testing.T) {
	v := makeTestValidator(t)
	tok := makeTestToken(t, testUserID, testHMACSecret, testIssuer, testAudience, -time.Hour)
	mw := httpapi.BearerAuth(v)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	mw.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
}

func TestBearerAuth_WrongSecret_Returns401(t *testing.T) {
	v := makeTestValidator(t)
	tok := makeTestToken(t, testUserID, "different-secret-that-is-at-least-32-bytes!!", testIssuer, testAudience, time.Hour)
	mw := httpapi.BearerAuth(v)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	mw.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
}

func TestBearerAuth_ValidToken_InjectsUserID(t *testing.T) {
	v := makeTestValidator(t)
	tok := makeTestToken(t, testUserID, testHMACSecret, testIssuer, testAudience, time.Hour)

	var capturedUserID string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedUserID = httpapi.GetContextUserID(r)
		w.WriteHeader(http.StatusOK)
	})

	mw := httpapi.BearerAuth(v)(next)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	mw.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if capturedUserID != testUserID {
		t.Fatalf("expected user ID %q, got %q", testUserID, capturedUserID)
	}
}

func TestBearerAuth_WrongIssuer_Returns401(t *testing.T) {
	v := makeTestValidator(t)
	tok := makeTestToken(t, testUserID, testHMACSecret, "wrong-issuer", testAudience, time.Hour)
	mw := httpapi.BearerAuth(v)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	mw.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
}
