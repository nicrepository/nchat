package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
)

// The administrative authentication context (issue #578).
//
// NChat does not implement a second factor: Keycloak runs the authentication
// flow and states, in the `acr` claim, which context it ran. What this service
// owes is the other half — asking for that context on an administrative login,
// and refusing a token that does not come back carrying it.
//
// The value below is a fixture, not a value this codebase defines. Whatever an
// operator's identity provider actually emits is what goes in the
// configuration; that indirection is deliberate, because an invented value
// would prove nothing.
const testAdminACR = "nchat-admin-mfa"

func newAssuranceService(t *testing.T, store *fakeOIDCStore, provider *fakeOIDCProvider, acr []string) *OIDCService {
	t.Helper()
	svc, err := NewOIDCService(OIDCServiceConfig{
		Enabled:             true,
		Configured:          true,
		ProviderName:        "keycloak",
		FrontendCallbackURL: "/oidc-callback",
		StateTTL:            10 * time.Minute,
		AutoProvision:       true,
		RedirectURLs: map[domain.OIDCAppContext]string{
			domain.OIDCAppChat:  testChatRedirectURL,
			domain.OIDCAppAdmin: testAdminRedirectURL,
		},
		AdminACRValues: acr,
	}, newTestOIDCTokenManager(t), store, provider)
	if err != nil {
		t.Fatalf("NewOIDCService: %v", err)
	}
	return svc
}

// runCallback drives a full sign-in for one context, with the provider
// returning the given `acr`.
func runCallback(t *testing.T, svc *OIDCService, store *fakeOIDCStore, provider *fakeOIDCProvider,
	tokens *TokenManager, app domain.OIDCAppContext, acr string) error {
	t.Helper()
	location, err := svc.Login(context.Background(), app)
	if err != nil {
		return err
	}
	parsed, parseErr := url.Parse(location)
	if parseErr != nil {
		t.Fatalf("parse authorization url: %v", parseErr)
	}
	nonce := parsed.Query().Get("nonce")
	provider.claims = domain.OIDCClaims{
		Subject:                    "subject-1",
		Email:                      "person@example.com",
		EmailVerified:              true,
		Nonce:                      nonce,
		AuthenticationContextClass: acr,
	}
	store.consumeReq = domain.OIDCConsumedAuthRequest{
		ID:                    store.createdAuthReq.ID,
		Provider:              "keycloak",
		NonceHash:             tokens.HashOIDCNonce(nonce),
		PKCEVerifierEncrypted: store.createdAuthReq.PKCEVerifierEncrypted,
		AppContext:            app,
	}
	_, err = svc.Callback(context.Background(), OIDCCallbackInput{
		Code:  "provider-code",
		State: parsed.Query().Get("state"),
	})
	return err
}

// The requirement is only real once a deployment states it, and stating it must
// also make the login *ask* for the context — otherwise the provider has no
// reason to run the stronger flow and every login would be refused.
func TestLogin_RequestsTheAdministrativeAuthenticationContext(t *testing.T) {
	provider := &fakeOIDCProvider{}
	svc := newAssuranceService(t, &fakeOIDCStore{}, provider, []string{testAdminACR})

	if _, err := svc.Login(context.Background(), domain.OIDCAppAdmin); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if len(provider.authorizationACRValues) != 1 || provider.authorizationACRValues[0] != testAdminACR {
		t.Fatalf("expected the admin context to be requested, got %v", provider.authorizationACRValues)
	}

	// The administrative policy must not change how everyone else signs in.
	provider.authorizationACRValues = nil
	if _, err := svc.Login(context.Background(), domain.OIDCAppChat); err != nil {
		t.Fatalf("chat Login: %v", err)
	}
	if provider.authorizationACRValues != nil {
		t.Fatalf("the chat login must carry no acr_values, got %v", provider.authorizationACRValues)
	}
}

// The Keycloak provider must put the request on the wire in the form the spec
// defines: a space-separated acr_values parameter.
func TestKeycloakAuthorizationURLCarriesACRValues(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/openid-configuration" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		//nolint:gosec // OIDC metadata keys, not credentials.
		_ = json.NewEncoder(w).Encode(map[string]string{
			"issuer":                 serverIssuer(r),
			"authorization_endpoint": "http://" + r.Host + "/auth",
			"token_endpoint":         "http://" + r.Host + "/token",
			"jwks_uri":               "http://" + r.Host + "/certs",
		})
	}))
	defer server.Close()

	provider := NewKeycloakProvider(KeycloakProviderConfig{
		IssuerURL: server.URL, ClientID: "nchat", Scopes: "openid", HTTPClient: server.Client(),
	})

	location, err := provider.AuthorizationURL(AuthorizationRequest{
		State: "s", Nonce: "n", CodeChallenge: "c", RedirectURL: testProviderRedirectURL,
		ACRValues: []string{testAdminACR, "second-level"},
	})
	if err != nil {
		t.Fatalf("AuthorizationURL: %v", err)
	}
	parsed, err := url.Parse(location)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := parsed.Query().Get("acr_values"); got != testAdminACR+" second-level" {
		t.Fatalf("unexpected acr_values %q", got)
	}

	// And omits the parameter entirely when nothing is required, rather than
	// sending an empty one.
	location, err = provider.AuthorizationURL(AuthorizationRequest{
		State: "s", Nonce: "n", CodeChallenge: "c", RedirectURL: testProviderRedirectURL,
	})
	if err != nil {
		t.Fatalf("AuthorizationURL: %v", err)
	}
	if strings.Contains(location, "acr_values") {
		t.Fatalf("expected no acr_values parameter, got %s", location)
	}
}

func TestCallback_AdministrativeAssurancePolicy(t *testing.T) {
	tests := map[string]struct {
		required []string
		returned string
		wantErr  error
	}{
		"required and satisfied":        {[]string{testAdminACR}, testAdminACR, nil},
		"required, one of several":      {[]string{"other", testAdminACR}, testAdminACR, nil},
		"required but claim absent":     {[]string{testAdminACR}, "", domain.ErrOIDCInsufficientAssurance},
		"required but context too weak": {[]string{testAdminACR}, "pwd", domain.ErrOIDCInsufficientAssurance},
		"not required":                  {nil, "", nil},
		"not required, claim present":   {nil, "pwd", nil},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			tokens := newTestOIDCTokenManager(t)
			store := &fakeOIDCStore{}
			provider := &fakeOIDCProvider{}
			svc := newAssuranceService(t, store, provider, tt.required)
			svc.tokens = tokens

			err := runCallback(t, svc, store, provider, tokens, domain.OIDCAppAdmin, tt.returned)
			if tt.wantErr == nil && err != nil {
				t.Fatalf("expected the login to succeed, got %v", err)
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Fatalf("expected %v, got %v", tt.wantErr, err)
			}
			if tt.wantErr != nil && store.createdInput.Subject != "" {
				t.Fatal("a refused login must not create a session")
			}
		})
	}
}

// An administrative policy must never reach into the chat. A chat login with no
// `acr` at all keeps working while the administrative requirement is in force.
func TestCallback_ChatIsUnaffectedByTheAdministrativePolicy(t *testing.T) {
	tokens := newTestOIDCTokenManager(t)
	store := &fakeOIDCStore{}
	provider := &fakeOIDCProvider{}
	svc := newAssuranceService(t, store, provider, []string{testAdminACR})
	svc.tokens = tokens

	if err := runCallback(t, svc, store, provider, tokens, domain.OIDCAppChat, ""); err != nil {
		t.Fatalf("the chat login must be unaffected by the administrative policy: %v", err)
	}
}

// The claim is read from the validated ID token and nowhere else: there is no
// request field, header or body value that can assert it.
func TestCallbackInputCarriesNoAssuranceField(t *testing.T) {
	input := OIDCCallbackInput{}
	if fields := describeCallbackInputFields(input); strings.Contains(strings.ToLower(fields), "acr") ||
		strings.Contains(strings.ToLower(fields), "amr") ||
		strings.Contains(strings.ToLower(fields), "mfa") {
		t.Fatalf("the callback input must not accept assurance from the client: %s", fields)
	}
}

func describeCallbackInputFields(input OIDCCallbackInput) string {
	return fmt.Sprintf("%+v", input)
}
