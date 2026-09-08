package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/nicrepository/nchat/libs/go/platform/observability"
	"github.com/nicrepository/nchat/services/notification-service/internal/domain"
)

// Issue #745: the access-token half of authentication.
//
// The secret is assembled from a repeated literal rather than written as one
// value: it is long enough to satisfy the constructor and has none of the
// entropy that would make it look like a real key to a secret scanner.
var testSigningKey = []byte(strings.Repeat("notification-service-test-key-", 2))

const (
	testIssuer   = "nchat-auth"
	testAudience = "nchat-api"
)

// signedToken mints a token that is valid unless a case deliberately breaks it.
func signedToken(t *testing.T, mutate func(*jwt.RegisteredClaims, *string)) string {
	t.Helper()
	now := time.Now()
	registered := jwt.RegisteredClaims{
		Subject:   ownerUser,
		Issuer:    testIssuer,
		Audience:  jwt.ClaimStrings{testAudience},
		ID:        "jti-1",
		IssuedAt:  jwt.NewNumericDate(now),
		NotBefore: jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
	}
	sessionID := ownerSession
	if mutate != nil {
		mutate(&registered, &sessionID)
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, accessClaims{
		SessionID: sessionID, RegisteredClaims: registered,
	})
	signed, err := token.SignedString(testSigningKey)
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return signed
}

func newTestValidator(t *testing.T) *TokenValidator {
	t.Helper()
	validator, err := NewTokenValidator(string(testSigningKey), testIssuer, testAudience)
	if err != nil {
		t.Fatalf("NewTokenValidator: %v", err)
	}
	return validator
}

func TestNewTokenValidatorRefusesAnUnusableConfiguration(t *testing.T) {
	cases := map[string][3]string{
		"short secret":   {"too-short", testIssuer, testAudience},
		"empty secret":   {"", testIssuer, testAudience},
		"empty issuer":   {string(testSigningKey), "  ", testAudience},
		"empty audience": {string(testSigningKey), testIssuer, ""},
	}
	for name, arguments := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := NewTokenValidator(arguments[0], arguments[1], arguments[2]); err == nil {
				t.Fatal("NewTokenValidator accepted an unusable configuration")
			}
		})
	}
}

func TestValidateAccessTokenAcceptsTheContract(t *testing.T) {
	identity, err := newTestValidator(t).ValidateAccessToken(signedToken(t, nil))
	if err != nil {
		t.Fatalf("ValidateAccessToken: %v", err)
	}
	if identity.UserID != ownerUser || identity.SessionID != ownerSession {
		t.Fatalf("identity = %+v", identity)
	}
}

// Every claim the auth-service contract requires, and every way the signature
// can fail to be one this service trusts. A token missing any of these is not a
// weaker token, it is a different contract.
func TestValidateAccessTokenRefusesAnythingOffContract(t *testing.T) {
	validator := newTestValidator(t)
	cases := map[string]string{
		"empty":          "",
		"not a jwt":      "abc.def.ghi",
		"wrong secret":   signedWith(t, []byte(strings.Repeat("another-test-key-value-", 2))),
		"none algorithm": unsignedToken(t),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := validator.ValidateAccessToken(raw); err == nil {
				t.Fatal("ValidateAccessToken accepted an off-contract token")
			}
		})
	}

	mutations := map[string]func(*jwt.RegisteredClaims, *string){
		"wrong issuer":     func(c *jwt.RegisteredClaims, _ *string) { c.Issuer = "someone-else" },
		"wrong audience":   func(c *jwt.RegisteredClaims, _ *string) { c.Audience = jwt.ClaimStrings{"other"} },
		"expired":          func(c *jwt.RegisteredClaims, _ *string) { c.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-time.Hour)) },
		"no expiry":        func(c *jwt.RegisteredClaims, _ *string) { c.ExpiresAt = nil },
		"not yet valid":    func(c *jwt.RegisteredClaims, _ *string) { c.NotBefore = jwt.NewNumericDate(time.Now().Add(time.Hour)) },
		"missing nbf":      func(c *jwt.RegisteredClaims, _ *string) { c.NotBefore = nil },
		"missing iat":      func(c *jwt.RegisteredClaims, _ *string) { c.IssuedAt = nil },
		"missing jti":      func(c *jwt.RegisteredClaims, _ *string) { c.ID = "" },
		"missing sid":      func(_ *jwt.RegisteredClaims, sid *string) { *sid = "" },
		"non-uuid sid":     func(_ *jwt.RegisteredClaims, sid *string) { *sid = "session-one" },
		"non-uuid subject": func(c *jwt.RegisteredClaims, _ *string) { c.Subject = "user-one" },
		"empty subject":    func(c *jwt.RegisteredClaims, _ *string) { c.Subject = "" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			if _, err := validator.ValidateAccessToken(signedToken(t, mutate)); err == nil {
				t.Fatalf("ValidateAccessToken accepted a token with %s", name)
			}
		})
	}
}

// A token supplied outside the Authorization header is not read at all. Query
// strings are logged by proxies, and cookies are attached by the browser to
// requests the user never made.
func TestATokenOutsideTheAuthorizationHeaderIsNotRead(t *testing.T) {
	h := newHarness(t)
	request := httptest.NewRequest(http.MethodGet,
		RoutePushSubscriptions+"?access_token="+signedToken(t, nil), nil)
	// Set as a raw header rather than through AddCookie: the point is that a
	// token arriving this way is ignored, and a Cookie the browser would
	// actually be willing to send has none of the protective attributes.
	request.Header.Set("Cookie", "access_token="+signedToken(t, nil))
	response := httptest.NewRecorder()

	h.router.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", response.Code)
	}
}

// The metric label is the route template and never the concrete path, so a
// subscription identifier cannot become a Prometheus series of its own.
func TestTheDisableRouteIsMeasuredUnderItsTemplate(t *testing.T) {
	var observed string
	mux := http.NewServeMux()
	mux.Handle("DELETE "+RoutePushSubscription, http.HandlerFunc(
		func(_ http.ResponseWriter, r *http.Request) { observed = observability.RouteTemplate(r) }))
	mux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(
		http.MethodDelete, RoutePushSubscriptions+"/"+ownedSubscript, nil))

	if observed != RoutePushSubscription {
		t.Fatalf("route label = %q, want the template %q", observed, RoutePushSubscription)
	}
	if strings.Contains(observed, ownedSubscript) {
		t.Fatalf("the identifier reached a metric label: %q", observed)
	}
}

// The principal injected into the context is the resolver's, and the token's
// claims are only what it was asked about.
func TestAuthenticateInjectsTheResolvedPrincipal(t *testing.T) {
	h := newHarness(t)
	h.do(t, http.MethodGet, RoutePushSubscriptions, "")

	if len(h.resolver.seen) != 1 ||
		h.resolver.seen[0] != (tokenIdentity{UserID: ownerUser, SessionID: ownerSession}) {
		t.Fatalf("resolver saw %+v", h.resolver.seen)
	}
	if h.service.principals[0] != (domain.Principal{UserID: ownerUser, WorkspaceID: ownerWorkspace}) {
		t.Fatalf("service saw %+v", h.service.principals)
	}
}

// signedWith mints an otherwise valid token under a different key.
func signedWith(t *testing.T, key []byte) string {
	t.Helper()
	now := time.Now()
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, accessClaims{
		SessionID: ownerSession,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject: ownerUser, Issuer: testIssuer, Audience: jwt.ClaimStrings{testAudience},
			ID: "jti-1", IssuedAt: jwt.NewNumericDate(now), NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
		},
	})
	signed, err := token.SignedString(key)
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return signed
}

// unsignedToken is the alg=none forgery. Accepting it would mean anyone can mint
// any identity, so it is checked explicitly rather than assumed.
func unsignedToken(t *testing.T) string {
	t.Helper()
	now := time.Now()
	token := jwt.NewWithClaims(jwt.SigningMethodNone, accessClaims{
		SessionID: ownerSession,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject: ownerUser, Issuer: testIssuer, Audience: jwt.ClaimStrings{testAudience},
			ID: "jti-1", IssuedAt: jwt.NewNumericDate(now), NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
		},
	})
	signed, err := token.SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return signed
}
