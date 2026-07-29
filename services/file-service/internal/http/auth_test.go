package httpapi_test

import (
	"crypto/rand"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	httpapi "github.com/nicrepository/nchat/services/file-service/internal/http"
)

const (
	testIssuer   = "nchat-auth"
	testAudience = "nchat-api"
)

func testSecret(t *testing.T) string {
	t.Helper()
	secret := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, secret); err != nil {
		t.Fatalf("generate secret: %v", err)
	}
	return string(secret)
}

type claimOverrides struct {
	subject   string
	sessionID string
	issuer    string
	audience  string
	expiresAt *time.Time
	omitID    bool
	omitNBF   bool
	omitIAT   bool
	method    jwt.SigningMethod
}

func signToken(t *testing.T, secret string, overrides claimOverrides) string {
	t.Helper()
	now := time.Now()
	expiry := now.Add(time.Hour)
	if overrides.expiresAt != nil {
		expiry = *overrides.expiresAt
	}
	subject := overrides.subject
	if subject == "" {
		subject = uuid.NewString()
	}
	sessionID := overrides.sessionID
	if sessionID == "" {
		sessionID = uuid.NewString()
	}
	issuer := overrides.issuer
	if issuer == "" {
		issuer = testIssuer
	}
	audience := overrides.audience
	if audience == "" {
		audience = testAudience
	}

	registered := jwt.RegisteredClaims{
		Subject:   subject,
		Issuer:    issuer,
		Audience:  jwt.ClaimStrings{audience},
		ExpiresAt: jwt.NewNumericDate(expiry),
	}
	if !overrides.omitID {
		registered.ID = uuid.NewString()
	}
	if !overrides.omitIAT {
		registered.IssuedAt = jwt.NewNumericDate(now)
	}
	if !overrides.omitNBF {
		registered.NotBefore = jwt.NewNumericDate(now)
	}

	claims := struct {
		SessionID string `json:"sid"`
		jwt.RegisteredClaims
	}{SessionID: sessionID, RegisteredClaims: registered}

	method := overrides.method
	if method == nil {
		method = jwt.SigningMethodHS256
	}
	signed, err := jwt.NewWithClaims(method, claims).SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return signed
}

func TestNewTokenValidatorRejectsWeakConfiguration(t *testing.T) {
	tests := []struct {
		name     string
		secret   string
		issuer   string
		audience string
	}{
		{name: "short secret", secret: "too-short", issuer: testIssuer, audience: testAudience},
		{name: "empty issuer", secret: testSecret(t), issuer: "  ", audience: testAudience},
		{name: "empty audience", secret: testSecret(t), issuer: testIssuer, audience: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := httpapi.NewTokenValidator(tt.secret, tt.issuer, tt.audience); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestValidateAccessTokenAcceptsAWellFormedToken(t *testing.T) {
	secret := testSecret(t)
	validator, err := httpapi.NewTokenValidator(secret, testIssuer, testAudience)
	if err != nil {
		t.Fatalf("build validator: %v", err)
	}
	userID, sessionID := uuid.NewString(), uuid.NewString()

	principal, err := validator.ValidateAccessToken(
		signToken(t, secret, claimOverrides{subject: userID, sessionID: sessionID}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if principal.UserID != userID || principal.SessionID != sessionID {
		t.Fatalf("unexpected principal %+v", principal)
	}
	if principal.AccessExpiresAt.IsZero() {
		t.Fatal("expected an expiry")
	}
}

func TestValidateAccessTokenRejectsBadTokens(t *testing.T) {
	secret := testSecret(t)
	otherSecret := testSecret(t)
	validator, err := httpapi.NewTokenValidator(secret, testIssuer, testAudience)
	if err != nil {
		t.Fatalf("build validator: %v", err)
	}
	expired := time.Now().Add(-time.Hour)

	tests := []struct {
		name  string
		token string
	}{
		{name: "garbage", token: "not.a.token"},
		{name: "empty", token: ""},
		{name: "another secret", token: signToken(t, otherSecret, claimOverrides{})},
		{name: "wrong issuer", token: signToken(t, secret, claimOverrides{issuer: "evil"})},
		{name: "wrong audience", token: signToken(t, secret, claimOverrides{audience: "other-api"})},
		{name: "expired", token: signToken(t, secret, claimOverrides{expiresAt: &expired})},
		{name: "missing jti", token: signToken(t, secret, claimOverrides{omitID: true})},
		{name: "missing nbf", token: signToken(t, secret, claimOverrides{omitNBF: true})},
		{name: "missing iat", token: signToken(t, secret, claimOverrides{omitIAT: true})},
		{name: "non uuid subject", token: signToken(t, secret, claimOverrides{subject: "admin"})},
		{name: "non uuid session", token: signToken(t, secret, claimOverrides{sessionID: "session"})},
		{name: "missing session", token: signToken(t, secret, claimOverrides{sessionID: " "})},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := validator.ValidateAccessToken(tt.token); err == nil {
				t.Fatal("expected the token to be rejected")
			}
		})
	}
}

// An unsigned token must never be accepted, whatever it claims.
func TestValidateAccessTokenRejectsTheNoneAlgorithm(t *testing.T) {
	secret := testSecret(t)
	validator, err := httpapi.NewTokenValidator(secret, testIssuer, testAudience)
	if err != nil {
		t.Fatalf("build validator: %v", err)
	}
	claims := jwt.MapClaims{
		"sub": uuid.NewString(), "sid": uuid.NewString(),
		"iss": testIssuer, "aud": testAudience, "jti": uuid.NewString(),
		"exp": time.Now().Add(time.Hour).Unix(),
		"iat": time.Now().Unix(), "nbf": time.Now().Unix(),
	}
	unsigned, err := jwt.NewWithClaims(jwt.SigningMethodNone, claims).
		SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatalf("sign none token: %v", err)
	}
	if _, err := validator.ValidateAccessToken(unsigned); err == nil {
		t.Fatal("an unsigned token must be rejected")
	}
}

func TestBearerAuthWithoutAValidatorIsUnavailable(t *testing.T) {
	handler := httpapi.BearerAuth(nil)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("the handler must not run without a validator")
	}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", response.Code)
	}
}

func TestAuthenticatedPrincipalIsAbsentWithoutTheMiddleware(t *testing.T) {
	if _, ok := httpapi.AuthenticatedPrincipal(httptest.NewRequest(http.MethodGet, "/", nil)); ok {
		t.Fatal("no principal may be present without BearerAuth")
	}
}
