package httpapi_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	httpapi "github.com/nicrepository/nchat/services/chat-service/internal/http"
)

// ── helpers ───────────────────────────────────────────────────────────────────

const (
	testHMACSecret = "test-hmac-secret-must-be-at-least-32-bytes!"
	testIssuer     = "nchat-auth"
	testAudience   = "nchat-api"
	testUserID     = "user-abc-123"
	// testSessionID is a valid UUID used as the JWT "sid" claim.
	testSessionID = "b1e2c3d4-0000-0000-0000-000000000001"
)

func makeTestToken(t *testing.T, userID, secret, issuer, audience string, expiry time.Duration) string {
	t.Helper()
	claims := jwt.MapClaims{
		"sub": userID,
		"sid": testSessionID,
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

// makeTokenWithClaims signs a token with arbitrary MapClaims.
func makeTokenWithClaims(t *testing.T, claims jwt.MapClaims, secret string) string {
	t.Helper()
	tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("sign custom token: %v", err)
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

// allowAllSessionValidator accepts every (userID, sessionID) pair.
type allowAllSessionValidator struct{}

func (allowAllSessionValidator) ValidateActiveSession(_ context.Context, _, _ string) error {
	return nil
}

// denySessionValidator rejects every session with the configured error.
type denySessionValidator struct{ err error }

func (d denySessionValidator) ValidateActiveSession(_ context.Context, _, _ string) error {
	return d.err
}

// ── BearerAuth tests ──────────────────────────────────────────────────────────

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

func TestBearerAuth_WrongAudience_Returns401(t *testing.T) {
	v := makeTestValidator(t)
	tok := makeTestToken(t, testUserID, testHMACSecret, testIssuer, "wrong-audience", time.Hour)
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

func TestBearerAuth_FutureNbf_Returns401(t *testing.T) {
	v := makeTestValidator(t)
	// nbf is 1 hour in the future — token not yet valid.
	claims := jwt.MapClaims{
		"sub": testUserID,
		"sid": testSessionID,
		"iss": testIssuer,
		"aud": jwt.ClaimStrings{testAudience},
		"iat": time.Now().Unix(),
		"nbf": time.Now().Add(time.Hour).Unix(),
		"exp": time.Now().Add(2 * time.Hour).Unix(),
		"jti": "jti-nbf",
	}
	tok := makeTokenWithClaims(t, claims, testHMACSecret)
	mw := httpapi.BearerAuth(v)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	mw.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for future nbf, got %d", rr.Code)
	}
}

func TestBearerAuth_UnexpectedAlg_Returns401(t *testing.T) {
	v := makeTestValidator(t)
	// Sign with HMAC-384 instead of HMAC-256.
	claims := jwt.MapClaims{
		"sub": testUserID,
		"sid": testSessionID,
		"iss": testIssuer,
		"aud": jwt.ClaimStrings{testAudience},
		"iat": time.Now().Unix(),
		"nbf": time.Now().Unix(),
		"exp": time.Now().Add(time.Hour).Unix(),
		"jti": "jti-alg",
	}
	tok, err := jwt.NewWithClaims(jwt.SigningMethodHS384, claims).SignedString([]byte(testHMACSecret))
	if err != nil {
		t.Fatalf("sign HS384 token: %v", err)
	}
	mw := httpapi.BearerAuth(v)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	mw.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for unexpected alg, got %d", rr.Code)
	}
}

func TestBearerAuth_MissingExp_Returns401(t *testing.T) {
	v := makeTestValidator(t)
	claims := jwt.MapClaims{
		"sub": testUserID,
		"sid": testSessionID,
		"iss": testIssuer,
		"aud": jwt.ClaimStrings{testAudience},
		"iat": time.Now().Unix(),
		"nbf": time.Now().Unix(),
		// no "exp"
		"jti": "jti-noexp",
	}
	tok := makeTokenWithClaims(t, claims, testHMACSecret)
	mw := httpapi.BearerAuth(v)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	mw.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for missing exp, got %d", rr.Code)
	}
}

func TestBearerAuth_MissingSub_Returns401(t *testing.T) {
	v := makeTestValidator(t)
	claims := jwt.MapClaims{
		// no "sub"
		"sid": testSessionID,
		"iss": testIssuer,
		"aud": jwt.ClaimStrings{testAudience},
		"iat": time.Now().Unix(),
		"nbf": time.Now().Unix(),
		"exp": time.Now().Add(time.Hour).Unix(),
		"jti": "jti-nosub",
	}
	tok := makeTokenWithClaims(t, claims, testHMACSecret)
	mw := httpapi.BearerAuth(v)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	mw.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for missing sub, got %d", rr.Code)
	}
}

func TestBearerAuth_MissingSid_Returns401(t *testing.T) {
	v := makeTestValidator(t)
	claims := jwt.MapClaims{
		"sub": testUserID,
		// no "sid"
		"iss": testIssuer,
		"aud": jwt.ClaimStrings{testAudience},
		"iat": time.Now().Unix(),
		"nbf": time.Now().Unix(),
		"exp": time.Now().Add(time.Hour).Unix(),
		"jti": "jti-nosid",
	}
	tok := makeTokenWithClaims(t, claims, testHMACSecret)
	mw := httpapi.BearerAuth(v)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	mw.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for missing sid, got %d", rr.Code)
	}
}

func TestBearerAuth_MissingJTI_Returns401(t *testing.T) {
	v := makeTestValidator(t)
	claims := jwt.MapClaims{
		"sub": testUserID,
		"sid": testSessionID,
		"iss": testIssuer,
		"aud": jwt.ClaimStrings{testAudience},
		"iat": time.Now().Unix(),
		"nbf": time.Now().Unix(),
		"exp": time.Now().Add(time.Hour).Unix(),
		// no "jti"
	}
	tok := makeTokenWithClaims(t, claims, testHMACSecret)
	mw := httpapi.BearerAuth(v)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	mw.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for missing jti, got %d", rr.Code)
	}
}

func TestBearerAuth_MissingIAT_Returns401(t *testing.T) {
	v := makeTestValidator(t)
	claims := jwt.MapClaims{
		"sub": testUserID,
		"sid": testSessionID,
		"iss": testIssuer,
		"aud": jwt.ClaimStrings{testAudience},
		// no "iat"
		"nbf": time.Now().Unix(),
		"exp": time.Now().Add(time.Hour).Unix(),
		"jti": "jti-noiat",
	}
	tok := makeTokenWithClaims(t, claims, testHMACSecret)
	mw := httpapi.BearerAuth(v)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	mw.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for missing iat, got %d", rr.Code)
	}
}

func TestBearerAuth_MissingNBF_Returns401(t *testing.T) {
	v := makeTestValidator(t)
	claims := jwt.MapClaims{
		"sub": testUserID,
		"sid": testSessionID,
		"iss": testIssuer,
		"aud": jwt.ClaimStrings{testAudience},
		"iat": time.Now().Unix(),
		// no "nbf"
		"exp": time.Now().Add(time.Hour).Unix(),
		"jti": "jti-nonbf",
	}
	tok := makeTokenWithClaims(t, claims, testHMACSecret)
	mw := httpapi.BearerAuth(v)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	mw.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for missing nbf, got %d", rr.Code)
	}
}

func TestBearerAuth_TokenInQueryString_Ignored(t *testing.T) {
	// Tokens passed via query string must not be accepted.
	// Only the Authorization header is read.
	v := makeTestValidator(t)
	tok := makeTestToken(t, testUserID, testHMACSecret, testIssuer, testAudience, time.Hour)
	mw := httpapi.BearerAuth(v)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rr := httptest.NewRecorder()
	// Token in query string only — no Authorization header.
	// The query param name "blocked_param" is neutral; the test verifies the
	// middleware rejects credentials passed outside the Authorization header.
	req := httptest.NewRequest(http.MethodGet, "/?blocked_param="+tok, nil)
	mw.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 when token only in query string, got %d", rr.Code)
	}
}

func TestBearerAuth_ValidToken_InjectsUserIDAndSessionID(t *testing.T) {
	v := makeTestValidator(t)
	tok := makeTestToken(t, testUserID, testHMACSecret, testIssuer, testAudience, time.Hour)

	var capturedUserID, capturedSessionID string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedUserID = httpapi.GetContextUserID(r)
		capturedSessionID = httpapi.GetContextSessionID(r)
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
	if capturedSessionID != testSessionID {
		t.Fatalf("expected session ID %q, got %q", testSessionID, capturedSessionID)
	}
}

// ── RequireActiveSession tests ────────────────────────────────────────────────

func withInjectedAuth(userID, sessionID string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	return r.WithContext(
		func() context.Context {
			ctx := context.WithValue(r.Context(), httpapi.ExportCtxKeyUserID, userID)
			return context.WithValue(ctx, httpapi.ExportCtxKeySessionID, sessionID)
		}(),
	)
}

func TestRequireActiveSession_NilValidator_Returns503(t *testing.T) {
	mw := httpapi.RequireActiveSession(nil)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rr := httptest.NewRecorder()
	mw.ServeHTTP(rr, withInjectedAuth(testUserID, testSessionID))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rr.Code)
	}
}

func TestRequireActiveSession_MissingUserID_Returns401(t *testing.T) {
	mw := httpapi.RequireActiveSession(allowAllSessionValidator{})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil) // no userID injected
	mw.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
}

func TestRequireActiveSession_MissingSessionID_Returns401(t *testing.T) {
	mw := httpapi.RequireActiveSession(allowAllSessionValidator{})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rr := httptest.NewRecorder()
	// userID present but no sessionID injected.
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	ctx := context.WithValue(r.Context(), httpapi.ExportCtxKeyUserID, testUserID)
	mw.ServeHTTP(rr, r.WithContext(ctx))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
}

func TestRequireActiveSession_NonUUIDSessionID_Returns401(t *testing.T) {
	mw := httpapi.RequireActiveSession(allowAllSessionValidator{})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rr := httptest.NewRecorder()
	mw.ServeHTTP(rr, withInjectedAuth(testUserID, "not-a-uuid"))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for non-UUID session ID, got %d", rr.Code)
	}
}

func TestRequireActiveSession_RevokedSession_Returns401(t *testing.T) {
	mw := httpapi.RequireActiveSession(denySessionValidator{err: domain.ErrInvalidToken})(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}),
	)
	rr := httptest.NewRecorder()
	mw.ServeHTTP(rr, withInjectedAuth(testUserID, testSessionID))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for revoked session, got %d", rr.Code)
	}
}

func TestRequireActiveSession_UnknownSession_Returns401(t *testing.T) {
	mw := httpapi.RequireActiveSession(denySessionValidator{err: domain.ErrNotFound})(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}),
	)
	rr := httptest.NewRecorder()
	mw.ServeHTTP(rr, withInjectedAuth(testUserID, testSessionID))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for unknown session, got %d", rr.Code)
	}
}

func TestRequireActiveSession_DBError_Returns500(t *testing.T) {
	mw := httpapi.RequireActiveSession(denySessionValidator{err: errors.New("db connection refused")})(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}),
	)
	rr := httptest.NewRecorder()
	mw.ServeHTTP(rr, withInjectedAuth(testUserID, testSessionID))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 for unexpected DB error, got %d", rr.Code)
	}
}

func TestRequireActiveSession_ActiveSession_CallsNext(t *testing.T) {
	called := false
	mw := httpapi.RequireActiveSession(allowAllSessionValidator{})(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			called = true
			w.WriteHeader(http.StatusOK)
		}),
	)
	rr := httptest.NewRecorder()
	mw.ServeHTTP(rr, withInjectedAuth(testUserID, testSessionID))
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if !called {
		t.Fatal("next handler was not called for active session")
	}
}
