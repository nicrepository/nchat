package service

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
)

// testProviderRedirectURL is the callback URI these tests hand the provider.
// Since issue #578 the redirect is a per-request parameter rather than provider
// configuration, because one deployment serves two origins.
const testProviderRedirectURL = "https://auth.example.com/api/auth/oidc/keycloak/callback"

func TestKeycloakProvider_ValidateIDTokenRejectsInvalidClaimsAndAlgorithm(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate rsa key: %v", err)
	}
	const kid = "test-key"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			//nolint:gosec // Test endpoint names are OIDC metadata keys, not credentials.
			_ = json.NewEncoder(w).Encode(map[string]string{
				"issuer":                 serverIssuer(r),
				"authorization_endpoint": "http://" + r.Host + "/auth",
				"token_endpoint":         "http://" + r.Host + "/token",
				"jwks_uri":               "http://" + r.Host + "/certs",
			})
		case "/certs":
			_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
				"kty": "RSA",
				"kid": kid,
				"alg": "RS256",
				"use": "sig",
				"n":   base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
				"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
			}}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	provider := NewKeycloakProvider(KeycloakProviderConfig{
		IssuerURL:  server.URL,
		ClientID:   "nchat-web",
		Scopes:     "openid email profile",
		HTTPClient: server.Client(),
	})

	now := time.Now()
	baseClaims := keycloakIDClaims{
		Nonce:             "nonce",
		Email:             "user@example.com",
		EmailVerified:     true,
		PreferredUsername: "User",
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    server.URL,
			Subject:   "subject-1",
			Audience:  jwt.ClaimStrings{"nchat-web"},
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
			IssuedAt:  jwt.NewNumericDate(now),
		},
	}

	good := signOIDCTestToken(t, key, kid, baseClaims)
	claims, err := provider.ValidateIDToken(context.Background(), good)
	if err != nil {
		t.Fatalf("expected valid token, got %v", err)
	}
	if claims.Subject != "subject-1" || claims.Email != "user@example.com" || claims.Nonce != "nonce" {
		t.Fatalf("unexpected claims: %+v", claims)
	}

	tests := []struct {
		name   string
		mutate func(keycloakIDClaims) keycloakIDClaims
	}{
		{name: "issuer", mutate: func(c keycloakIDClaims) keycloakIDClaims { c.Issuer = "https://evil.example.com"; return c }},
		{name: "audience", mutate: func(c keycloakIDClaims) keycloakIDClaims { c.Audience = jwt.ClaimStrings{"other-client"}; return c }},
		{name: "expired", mutate: func(c keycloakIDClaims) keycloakIDClaims {
			c.ExpiresAt = jwt.NewNumericDate(now.Add(-time.Minute))
			return c
		}},
		{name: "authorized party mismatch", mutate: func(c keycloakIDClaims) keycloakIDClaims { c.AuthorizedParty = "other"; return c }},
		{name: "multi audience missing azp", mutate: func(c keycloakIDClaims) keycloakIDClaims {
			c.Audience = jwt.ClaimStrings{"nchat-web", "other"}
			return c
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := signOIDCTestToken(t, key, kid, tt.mutate(baseClaims))
			if _, err := provider.ValidateIDToken(context.Background(), raw); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}

	noneToken := jwt.NewWithClaims(jwt.SigningMethodNone, baseClaims)
	noneToken.Header["kid"] = kid
	rawNone, err := noneToken.SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatalf("sign none token: %v", err)
	}
	if _, err := provider.ValidateIDToken(context.Background(), rawNone); err == nil {
		t.Fatal("expected alg none rejection")
	}
}

// TestKeycloakProvider_ValidateIDTokenDecodesProfileClaims proves the profile
// claims added for the identity pipeline (given_name, family_name, picture) are
// actually parsed out of a signed token, not merely present on the struct.
func TestKeycloakProvider_ValidateIDTokenDecodesProfileClaims(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate rsa key: %v", err)
	}
	const kid = "profile-key"
	claims := keycloakIDClaims{
		Nonce:             "nonce",
		Email:             "ana@example.com",
		EmailVerified:     true,
		PreferredUsername: "ana.souza",
		Name:              "Ana Carolina Souza",
		GivenName:         "Ana",
		FamilyName:        "Souza",
		Picture:           "https://idp.example.test/avatars/ana.png",
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "subject-1",
			Audience:  jwt.ClaimStrings{"nchat-web"},
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			//nolint:gosec // OIDC metadata keys, not credentials.
			_ = json.NewEncoder(w).Encode(map[string]string{
				"issuer":                 serverIssuer(r),
				"authorization_endpoint": "http://" + r.Host + "/auth",
				"token_endpoint":         "http://" + r.Host + "/token",
				"jwks_uri":               "http://" + r.Host + "/certs",
			})
		case "/certs":
			_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
				"kty": "RSA",
				"kid": kid,
				"alg": "RS256",
				"use": "sig",
				"n":   base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
				"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
			}}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	claims.Issuer = server.URL

	provider := NewKeycloakProvider(KeycloakProviderConfig{IssuerURL: server.URL, ClientID: "nchat-web", HTTPClient: server.Client()})
	got, err := provider.ValidateIDToken(context.Background(), signOIDCTestToken(t, key, kid, claims))
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if got.Name != "Ana Carolina Souza" || got.GivenName != "Ana" || got.FamilyName != "Souza" {
		t.Fatalf("name claims not decoded: %+v", got)
	}
	if got.Picture != "https://idp.example.test/avatars/ana.png" {
		t.Fatalf("picture claim not decoded: %q", got.Picture)
	}
	if got.PreferredUsername != "ana.souza" {
		t.Fatalf("preferred_username not decoded: %q", got.PreferredUsername)
	}
}

func TestKeycloakProvider_ValidateIDTokenRejectsMissingKidUnknownKidAndBadJWK(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate rsa key: %v", err)
	}
	const kid = "test-key"
	claims := keycloakIDClaims{
		Nonce:         "nonce",
		Email:         "user@example.com",
		EmailVerified: true,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "",
			Subject:   "subject-1",
			Audience:  jwt.ClaimStrings{"nchat-web"},
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}

	missingKid := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	missingKid.Close()
	claims.Issuer = missingKid.URL
	provider := NewKeycloakProvider(KeycloakProviderConfig{IssuerURL: missingKid.URL, ClientID: "nchat-web", HTTPClient: missingKid.Client()})
	if _, err := provider.ValidateIDToken(context.Background(), signOIDCTestToken(t, key, "", claims)); err == nil {
		t.Fatal("expected missing kid validation error")
	}

	for _, tt := range []struct {
		name string
		jwks map[string]any
	}{
		{name: "unknown kid", jwks: map[string]any{"keys": []map[string]string{{
			"kty": "RSA",
			"kid": "other-key",
			"alg": "RS256",
			"use": "sig",
			"n":   base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		}}}},
		{name: "bad exponent", jwks: map[string]any{"keys": []map[string]string{{
			"kty": "RSA",
			"kid": kid,
			"alg": "RS256",
			"use": "sig",
			"n":   base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e":   "",
		}}}},
		{name: "wrong alg", jwks: map[string]any{"keys": []map[string]string{{
			"kty": "RSA",
			"kid": kid,
			"alg": "RS512",
			"use": "sig",
			"n":   base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		}}}},
		{name: "wrong use", jwks: map[string]any{"keys": []map[string]string{{
			"kty": "RSA",
			"kid": kid,
			"alg": "RS256",
			"use": "enc",
			"n":   base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		}}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/.well-known/openid-configuration":
					//nolint:gosec // Test endpoint names are OIDC metadata keys, not credentials.
					_ = json.NewEncoder(w).Encode(map[string]string{
						"issuer":                 serverIssuer(r),
						"authorization_endpoint": "http://" + r.Host + "/auth",
						"token_endpoint":         "http://" + r.Host + "/token",
						"jwks_uri":               "http://" + r.Host + "/certs",
					})
				case "/certs":
					_ = json.NewEncoder(w).Encode(tt.jwks)
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			claims.Issuer = server.URL
			provider := NewKeycloakProvider(KeycloakProviderConfig{IssuerURL: server.URL, ClientID: "nchat-web", HTTPClient: server.Client()})
			if _, err := provider.ValidateIDToken(context.Background(), signOIDCTestToken(t, key, kid, claims)); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func signOIDCTestToken(t *testing.T, key *rsa.PrivateKey, kid string, claims keycloakIDClaims) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = kid
	raw, err := token.SignedString(key)
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return raw
}

func TestKeycloakProvider_AuthorizationURLAndExchangeCode(t *testing.T) {
	var sawCode string
	var sawVerifier string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			//nolint:gosec // Test endpoint names are OIDC metadata keys, not credentials.
			_ = json.NewEncoder(w).Encode(map[string]string{
				"issuer":                 serverIssuer(r),
				"authorization_endpoint": "http://" + r.Host + "/auth",
				"token_endpoint":         "http://" + r.Host + "/token",
				"jwks_uri":               "http://" + r.Host + "/certs",
			})
		case "/token":
			if err := r.ParseForm(); err != nil {
				t.Fatalf("parse form: %v", err)
			}
			sawCode = r.Form.Get("code")
			sawVerifier = r.Form.Get("code_verifier")
			_ = json.NewEncoder(w).Encode(map[string]string{"id_token": "id-token-value"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	provider := NewKeycloakProvider(KeycloakProviderConfig{
		IssuerURL:    server.URL,
		ClientID:     "nchat-web",
		ClientSecret: "client-secret",
		Scopes:       "openid email profile",
		HTTPClient:   server.Client(),
	})

	location, err := provider.AuthorizationURL(AuthorizationRequest{State: "state", Nonce: "nonce", CodeChallenge: "challenge", RedirectURL: testProviderRedirectURL})
	if err != nil {
		t.Fatalf("AuthorizationURL: %v", err)
	}
	parsed, err := url.Parse(location)
	if err != nil {
		t.Fatalf("parse location: %v", err)
	}
	if parsed.Path != "/auth" || parsed.Query().Get("state") != "state" || parsed.Query().Get("nonce") != "nonce" || parsed.Query().Get("code_challenge_method") != "S256" {
		t.Fatalf("unexpected auth URL: %s", location)
	}

	set, err := provider.ExchangeCode(context.Background(), "provider-code", "pkce-verifier", testProviderRedirectURL)
	if err != nil {
		t.Fatalf("ExchangeCode: %v", err)
	}
	if set.IDToken != "id-token-value" {
		t.Fatalf("unexpected id token %q", set.IDToken)
	}
	if sawCode != "provider-code" || sawVerifier != "pkce-verifier" {
		t.Fatalf("unexpected token request form code=%q verifier=%q", sawCode, sawVerifier)
	}
}

func TestKeycloakProvider_ExchangeCodeRejectsProviderErrorAndMissingIDToken(t *testing.T) {
	for _, tt := range []struct {
		name       string
		statusCode int
		body       string
	}{
		{name: "provider error", statusCode: http.StatusBadGateway, body: `{}`},
		{name: "missing id token", statusCode: http.StatusOK, body: `{}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/.well-known/openid-configuration":
					//nolint:gosec // Test endpoint names are OIDC metadata keys, not credentials.
					_ = json.NewEncoder(w).Encode(map[string]string{"issuer": serverIssuer(r), "authorization_endpoint": "http://" + r.Host + "/auth", "token_endpoint": "http://" + r.Host + "/token", "jwks_uri": "http://" + r.Host + "/certs"})
				case "/token":
					w.WriteHeader(tt.statusCode)
					_, _ = w.Write([]byte(tt.body))
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			provider := NewKeycloakProvider(KeycloakProviderConfig{IssuerURL: server.URL, ClientID: "nchat-web", ClientSecret: "client-secret", Scopes: "openid", HTTPClient: server.Client()})
			if _, err := provider.ExchangeCode(context.Background(), "code", "verifier", testProviderRedirectURL); err == nil {
				t.Fatal("expected exchange error")
			}
		})
	}
}

func TestKeycloakProvider_AuthorizationURLRejectsBadDiscovery(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"issuer": serverIssuer(r), "authorization_endpoint": "http://[bad-url", "token_endpoint": "http://" + r.Host + "/token", "jwks_uri": "http://" + r.Host + "/certs"})
	}))
	defer server.Close()

	provider := NewKeycloakProvider(KeycloakProviderConfig{IssuerURL: server.URL, ClientID: "nchat-web", Scopes: "openid", HTTPClient: server.Client()})
	if _, err := provider.AuthorizationURL(AuthorizationRequest{State: "state", Nonce: "nonce", CodeChallenge: "challenge", RedirectURL: testProviderRedirectURL}); err == nil {
		t.Fatal("expected authorization URL error")
	}
}

func TestNewKeycloakProviderCreatesDefaultClientAndTrimsIssuer(t *testing.T) {
	provider := NewKeycloakProvider(KeycloakProviderConfig{
		IssuerURL:   " https://keycloak.example.com/realms/nchat/ ",
		HTTPTimeout: 3 * time.Second,
	})

	if provider.cfg.IssuerURL != "https://keycloak.example.com/realms/nchat" {
		t.Fatalf("expected trimmed issuer, got %q", provider.cfg.IssuerURL)
	}
	if provider.httpClient == nil || provider.httpClient.Timeout != 3*time.Second {
		t.Fatalf("expected default client timeout, got %+v", provider.httpClient)
	}
}

func TestKeycloakProvider_DiscoveryRejectsBadResponses(t *testing.T) {
	for _, tt := range []struct {
		name       string
		statusCode int
		body       string
	}{
		{name: "status", statusCode: http.StatusInternalServerError, body: `{}`},
		{name: "invalid json", statusCode: http.StatusOK, body: `not-json`},
		{name: "missing endpoints", statusCode: http.StatusOK, body: `{"authorization_endpoint":"https://keycloak.example.com/auth"}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()

			provider := NewKeycloakProvider(KeycloakProviderConfig{IssuerURL: server.URL, ClientID: "nchat-web", HTTPClient: server.Client()})
			if _, err := provider.getDiscovery(context.Background()); err == nil {
				t.Fatal("expected discovery error")
			}
		})
	}
}

func TestKeycloakProvider_DiscoveryRejectsIssuerAndEndpointOriginMismatches(t *testing.T) {
	for _, tt := range []struct {
		name   string
		mutate func(host string, discovery map[string]string)
	}{
		{
			name: "issuer mismatch",
			mutate: func(_ string, discovery map[string]string) {
				discovery["issuer"] = "https://evil.example.com/realms/nchat"
			},
		},
		{
			name: "authorization endpoint origin mismatch",
			mutate: func(_ string, discovery map[string]string) {
				discovery["authorization_endpoint"] = "https://evil.example.com/auth"
			},
		},
		{
			name: "token endpoint origin mismatch",
			mutate: func(_ string, discovery map[string]string) {
				discovery["token_endpoint"] = "https://evil.example.com/token"
			},
		},
		{
			name: "jwks uri origin mismatch",
			mutate: func(_ string, discovery map[string]string) {
				discovery["jwks_uri"] = "https://evil.example.com/certs"
			},
		},
		{
			name: "endpoint userinfo",
			mutate: func(host string, discovery map[string]string) {
				discovery["token_endpoint"] = "http://user@" + host + "/token"
			},
		},
		{
			name: "endpoint fragment",
			mutate: func(host string, discovery map[string]string) {
				discovery["jwks_uri"] = "http://" + host + "/certs#keys"
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				discovery := oidcDiscoveryFixture(r)
				tt.mutate(r.Host, discovery)
				_ = json.NewEncoder(w).Encode(discovery)
			}))
			defer server.Close()

			provider := NewKeycloakProvider(KeycloakProviderConfig{IssuerURL: server.URL, ClientID: "nchat-web", HTTPClient: server.Client()})
			_, err := provider.getDiscovery(context.Background())
			if !errors.Is(err, domain.ErrOIDCMisconfigured) {
				t.Fatalf("expected oidc misconfigured, got %v", err)
			}
		})
	}
}

func TestKeycloakProvider_DiscoveryCacheExpiresAfterOneHour(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		discovery := oidcDiscoveryFixture(r)
		discovery["authorization_endpoint"] = "http://" + r.Host + "/auth-" + strconv.Itoa(calls)
		_ = json.NewEncoder(w).Encode(discovery)
	}))
	defer server.Close()

	provider := NewKeycloakProvider(KeycloakProviderConfig{IssuerURL: server.URL, ClientID: "nchat-web", HTTPClient: server.Client()})
	first, err := provider.getDiscovery(context.Background())
	if err != nil {
		t.Fatalf("first discovery: %v", err)
	}
	provider.mu.Lock()
	provider.discoveryFetchedAt = time.Now().Add(-2 * time.Hour)
	provider.mu.Unlock()

	second, err := provider.getDiscovery(context.Background())
	if err != nil {
		t.Fatalf("second discovery: %v", err)
	}
	if first.AuthorizationEndpoint == second.AuthorizationEndpoint || calls != 2 {
		t.Fatalf("expected expired discovery cache refresh, first=%q second=%q calls=%d", first.AuthorizationEndpoint, second.AuthorizationEndpoint, calls)
	}
}

func TestKeycloakProvider_IssuerURLSchemeValidation(t *testing.T) {
	for _, issuerURL := range []string{
		"https://keycloak.example.com/realms/nchat",
		"http://localhost:8080/realms/nchat",
		"http://127.0.0.1:8080/realms/nchat",
		"http://[::1]:8080/realms/nchat",
	} {
		t.Run("allowed "+issuerURL, func(t *testing.T) {
			if err := validateOIDCIssuerURL(issuerURL); err != nil {
				t.Fatalf("expected issuer URL to be allowed, got %v", err)
			}
		})
	}

	for _, issuerURL := range []string{
		"http://keycloak.example.com/realms/nchat",
		"ftp://keycloak.example.com/realms/nchat",
		"https://user@keycloak.example.com/realms/nchat",
		"https://keycloak.example.com/realms/nchat?realm=other",
		"https://keycloak.example.com/realms/nchat#fragment",
	} {
		t.Run("rejected "+issuerURL, func(t *testing.T) {
			if err := validateOIDCIssuerURL(issuerURL); !errors.Is(err, domain.ErrOIDCMisconfigured) {
				t.Fatalf("expected issuer URL to be rejected, got %v", err)
			}
		})
	}
}

func TestKeycloakProvider_GetJWKSRejectsBadResponses(t *testing.T) {
	for _, tt := range []struct {
		name       string
		jwksURI    string
		statusCode int
		body       string
	}{
		{name: "bad jwks url", jwksURI: "http://[bad-url", statusCode: http.StatusOK, body: `{}`},
		{name: "status", statusCode: http.StatusInternalServerError, body: `{}`},
		{name: "invalid json", statusCode: http.StatusOK, body: `not-json`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/.well-known/openid-configuration":
					jwksURI := tt.jwksURI
					if jwksURI == "" {
						jwksURI = "http://" + r.Host + "/certs"
					}
					//nolint:gosec // Test endpoint names are OIDC metadata keys, not credentials.
					_ = json.NewEncoder(w).Encode(map[string]string{
						"issuer":                 serverIssuer(r),
						"authorization_endpoint": "http://" + r.Host + "/auth",
						"token_endpoint":         "http://" + r.Host + "/token",
						"jwks_uri":               jwksURI,
					})
				case "/certs":
					w.WriteHeader(tt.statusCode)
					_, _ = w.Write([]byte(tt.body))
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()

			provider := NewKeycloakProvider(KeycloakProviderConfig{IssuerURL: server.URL, ClientID: "nchat-web", HTTPClient: server.Client()})
			if _, err := provider.getJWKS(context.Background(), true); err == nil {
				t.Fatal("expected jwks error")
			}
		})
	}
}

func TestKeycloakProvider_GetJWKSCacheExpiresAfterOneHour(t *testing.T) {
	var jwksCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(oidcDiscoveryFixture(r))
		case "/certs":
			jwksCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
				"kty": "RSA",
				"kid": "key-" + strconv.Itoa(jwksCalls),
				"alg": "RS256",
				"use": "sig",
				"n":   "AQAB",
				"e":   "AQAB",
			}}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	provider := NewKeycloakProvider(KeycloakProviderConfig{IssuerURL: server.URL, ClientID: "nchat-web", HTTPClient: server.Client()})
	first, err := provider.getJWKS(context.Background(), false)
	if err != nil {
		t.Fatalf("first jwks: %v", err)
	}
	provider.mu.Lock()
	provider.jwksFetchedAt = time.Now().Add(-2 * time.Hour)
	provider.mu.Unlock()

	second, err := provider.getJWKS(context.Background(), false)
	if err != nil {
		t.Fatalf("second jwks: %v", err)
	}
	if first.Keys[0].Kid == second.Keys[0].Kid || jwksCalls != 2 {
		t.Fatalf("expected expired jwks cache refresh, first=%q second=%q calls=%d", first.Keys[0].Kid, second.Keys[0].Kid, jwksCalls)
	}
}

func oidcDiscoveryFixture(r *http.Request) map[string]string {
	return map[string]string{
		"issuer":                 serverIssuer(r),
		"authorization_endpoint": "http://" + r.Host + "/auth",
		"token_endpoint":         "http://" + r.Host + "/token",
		"jwks_uri":               "http://" + r.Host + "/certs",
	}
}

func serverIssuer(r *http.Request) string {
	return "http://" + r.Host
}
