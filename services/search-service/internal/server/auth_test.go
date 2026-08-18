package server

import (
	"context"
	"errors"
	"github.com/nicrepository/nchat/services/search-service/internal/domain"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type fakeTokenValidator struct {
	principal Principal
	err       error
}

func signedAccessToken(t *testing.T, secret, issuer, audience string, mutate func(*accessClaims)) string {
	t.Helper()
	now := time.Now().UTC()
	claims := accessClaims{SessionID: "11111111-1111-4111-8111-111111111111", RegisteredClaims: jwt.RegisteredClaims{Subject: "22222222-2222-4222-8222-222222222222", Issuer: issuer, Audience: jwt.ClaimStrings{audience}, ID: "token-id", IssuedAt: jwt.NewNumericDate(now), NotBefore: jwt.NewNumericDate(now.Add(-time.Second)), ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour))}}
	if mutate != nil {
		mutate(&claims)
	}
	raw, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestTokenValidatorValidatesConfigurationClaimsAndAlgorithm(t *testing.T) {
	secret := "12345678901234567890123456789012"
	for _, tc := range []struct{ name, secret, issuer, audience string }{{"short secret", "short", "issuer", "audience"}, {"missing issuer", secret, "", "audience"}, {"missing audience", secret, "issuer", ""}} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewTokenValidator(tc.secret, tc.issuer, tc.audience); err == nil {
				t.Fatal("expected invalid configuration")
			}
		})
	}
	v, err := NewTokenValidator(secret, "issuer", "audience")
	if err != nil {
		t.Fatal(err)
	}
	p, err := v.ValidateAccessToken(signedAccessToken(t, secret, "issuer", "audience", nil))
	if err != nil || p.UserID != "22222222-2222-4222-8222-222222222222" {
		t.Fatalf("principal=%+v err=%v", p, err)
	}
	if _, err := v.ValidateAccessToken("invalid"); err == nil {
		t.Fatal("invalid token accepted")
	}
	missingID := signedAccessToken(t, secret, "issuer", "audience", func(c *accessClaims) { c.ID = "" })
	if _, err := v.ValidateAccessToken(missingID); err == nil {
		t.Fatal("missing claims accepted")
	}
	badSubject := signedAccessToken(t, secret, "issuer", "audience", func(c *accessClaims) { c.Subject = "bad" })
	if _, err := v.ValidateAccessToken(badSubject); err == nil {
		t.Fatal("bad subject accepted")
	}
	badSession := signedAccessToken(t, secret, "issuer", "audience", func(c *accessClaims) { c.SessionID = "bad" })
	if _, err := v.ValidateAccessToken(badSession); err == nil {
		t.Fatal("bad session accepted")
	}
	claims := jwt.MapClaims{"sub": "22222222-2222-4222-8222-222222222222"}
	none, err := jwt.NewWithClaims(jwt.SigningMethodNone, claims).SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.ValidateAccessToken(none); err == nil {
		t.Fatal("none algorithm accepted")
	}
}

func TestAuthMiddlewareFailsClosedForMissingDependenciesAndBackendErrors(t *testing.T) {
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("must not reach next") })
	res := httptest.NewRecorder()
	BearerAuth(nil)(next).ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/", nil))
	if res.Code != http.StatusServiceUnavailable {
		t.Fatalf("nil token validator status=%d", res.Code)
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer bad")
	res = httptest.NewRecorder()
	BearerAuth(fakeTokenValidator{err: errors.New("invalid")})(next).ServeHTTP(res, req)
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("invalid token status=%d", res.Code)
	}
	res = httptest.NewRecorder()
	RequireActiveSession(nil)(next).ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/", nil))
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("missing principal status=%d", res.Code)
	}
	principalReq := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(context.WithValue(context.Background(), principalKey{}, Principal{UserID: "user", SessionID: "session"}))
	res = httptest.NewRecorder()
	RequireActiveSession(nil)(next).ServeHTTP(res, principalReq)
	if res.Code != http.StatusServiceUnavailable {
		t.Fatalf("nil sessions status=%d", res.Code)
	}
	res = httptest.NewRecorder()
	RequireActiveSession(fakeSessionValidator{err: errors.New("database")})(next).ServeHTTP(res, principalReq)
	if res.Code != http.StatusInternalServerError {
		t.Fatalf("backend status=%d", res.Code)
	}
}

func (f fakeTokenValidator) ValidateAccessToken(string) (Principal, error) { return f.principal, f.err }

type fakeSessionValidator struct{ err error }

func (f fakeSessionValidator) ValidateActiveSession(context.Context, string, string) error {
	return f.err
}

func TestAuthenticationRequiresBearerAndActiveSession(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if authenticatedUserID(r) != "user" {
			t.Fatal("principal missing")
		}
		w.WriteHeader(http.StatusNoContent)
	})
	h := BearerAuth(fakeTokenValidator{principal: Principal{UserID: "user", SessionID: "11111111-1111-4111-8111-111111111111"}})(RequireActiveSession(fakeSessionValidator{})(next))
	missing := httptest.NewRecorder()
	h.ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/", nil))
	if missing.Code != http.StatusUnauthorized {
		t.Fatalf("missing status=%d", missing.Code)
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer token")
	ok := httptest.NewRecorder()
	h.ServeHTTP(ok, req)
	if ok.Code != http.StatusNoContent {
		t.Fatalf("valid status=%d body=%s", ok.Code, ok.Body.String())
	}
}

func TestAuthenticationRejectsRevokedSession(t *testing.T) {
	h := BearerAuth(fakeTokenValidator{principal: Principal{UserID: "user", SessionID: "11111111-1111-4111-8111-111111111111"}})(RequireActiveSession(fakeSessionValidator{err: domain.ErrUnauthorized})(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("must not reach handler") })))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer token")
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d", res.Code)
	}
}
