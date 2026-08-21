package service

import (
	"context"
	"errors"
	"net/url"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
)

// Both applications must be able to sign in, and each must be sent back to its
// own origin. The redirect URI is what carries that, and it comes from the
// service's allowlist keyed by the context — never from the request.
func TestLogin_SendsEachApplicationItsOwnRedirectURI(t *testing.T) {
	tests := map[domain.OIDCAppContext]string{
		domain.OIDCAppChat:  testChatRedirectURL,
		domain.OIDCAppAdmin: testAdminRedirectURL,
	}
	for app, want := range tests {
		tokens := newTestOIDCTokenManager(t)
		store := &fakeOIDCStore{}
		provider := &fakeOIDCProvider{}
		svc := newTestOIDCService(t, tokens, store, provider)

		if _, err := svc.Login(context.Background(), app); err != nil {
			t.Fatalf("Login(%q): %v", app, err)
		}
		if provider.authorizationRedirectURL != want {
			t.Fatalf("Login(%q) used redirect %q, want %q", app, provider.authorizationRedirectURL, want)
		}
		// The context is written next to the state, which is what lets the
		// callback recover it without trusting the returning request.
		if store.createdAuthReq.AppContext != app {
			t.Fatalf("Login(%q) stored context %q", app, store.createdAuthReq.AppContext)
		}
	}
}

// A context the deployment did not configure is unavailable, not defaulted.
// Falling back to the chat URI would authenticate an administrator and then
// drop them on the wrong origin, where their console session cannot exist.
func TestLogin_RefusesAnUnconfiguredContextInsteadOfFallingBack(t *testing.T) {
	tokens := newTestOIDCTokenManager(t)
	store := &fakeOIDCStore{}
	provider := &fakeOIDCProvider{}
	svc, err := NewOIDCService(OIDCServiceConfig{
		Enabled:             true,
		Configured:          true,
		ProviderName:        "keycloak",
		FrontendCallbackURL: "/oidc-callback",
		StateTTL:            10 * time.Minute,
		RedirectURLs:        map[domain.OIDCAppContext]string{domain.OIDCAppChat: testChatRedirectURL},
	}, tokens, store, provider)
	if err != nil {
		t.Fatalf("NewOIDCService: %v", err)
	}

	if _, err := svc.Login(context.Background(), domain.OIDCAppAdmin); !errors.Is(err, domain.ErrOIDCMisconfigured) {
		t.Fatalf("expected ErrOIDCMisconfigured, got %v", err)
	}
	if store.createdAuthReq.StateHash != "" {
		t.Fatal("a refused context must not create a login request")
	}
	// The chat flow is untouched by the administrative one being absent.
	if _, err := svc.Login(context.Background(), domain.OIDCAppChat); err != nil {
		t.Fatalf("chat login must keep working: %v", err)
	}
}

// A label that is not in the closed set never reaches the map at all.
func TestLogin_RefusesAnUnknownContext(t *testing.T) {
	svc := newTestOIDCService(t, newTestOIDCTokenManager(t), &fakeOIDCStore{}, &fakeOIDCProvider{})

	for _, raw := range []string{"root", "https://evil.test", "CHAT"} {
		if _, err := svc.Login(context.Background(), domain.OIDCAppContext(raw)); !errors.Is(err, domain.ErrOIDCMisconfigured) {
			t.Fatalf("Login(%q): expected ErrOIDCMisconfigured, got %v", raw, err)
		}
	}
}

// The token endpoint requires the same redirect URI the authorization request
// carried. The callback re-derives it from the *stored* context, so a run that
// started on the console finishes against the console's URI.
func TestCallback_ExchangesWithTheStoredContextsRedirectURI(t *testing.T) {
	for app, want := range map[domain.OIDCAppContext]string{
		domain.OIDCAppChat:  testChatRedirectURL,
		domain.OIDCAppAdmin: testAdminRedirectURL,
	} {
		tokens := newTestOIDCTokenManager(t)
		store := &fakeOIDCStore{}
		provider := &fakeOIDCProvider{}
		svc := newTestOIDCService(t, tokens, store, provider)

		location, err := svc.Login(context.Background(), app)
		if err != nil {
			t.Fatalf("Login(%q): %v", app, err)
		}
		state := queryValue(t, location, "state")
		nonce := queryValue(t, location, "nonce")

		provider.claims = domain.OIDCClaims{
			Subject: "subject-1", Email: "person@example.com", EmailVerified: true, Nonce: nonce,
		}
		store.consumeReq = domain.OIDCConsumedAuthRequest{
			ID:                    store.createdAuthReq.ID,
			Provider:              "keycloak",
			NonceHash:             tokens.HashOIDCNonce(nonce),
			PKCEVerifierEncrypted: store.createdAuthReq.PKCEVerifierEncrypted,
			AppContext:            app,
		}

		if _, err := svc.Callback(context.Background(), OIDCCallbackInput{Code: "provider-code", State: state}); err != nil {
			t.Fatalf("Callback(%q): %v", app, err)
		}
		if provider.exchangeRedirectURL != want {
			t.Fatalf("Callback(%q) exchanged with %q, want %q", app, provider.exchangeRedirectURL, want)
		}
	}
}

// A returning request cannot change the context: there is no request field for
// it, and the callback reads the row the login wrote. This pins that — the
// browser comes back with only code and state, and the admin run still uses the
// admin URI.
func TestCallback_IgnoresAnythingTheReturningRequestSaysAboutContext(t *testing.T) {
	tokens := newTestOIDCTokenManager(t)
	store := &fakeOIDCStore{}
	provider := &fakeOIDCProvider{}
	svc := newTestOIDCService(t, tokens, store, provider)

	location, err := svc.Login(context.Background(), domain.OIDCAppAdmin)
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	state := queryValue(t, location, "state")
	nonce := queryValue(t, location, "nonce")

	provider.claims = domain.OIDCClaims{
		Subject: "subject-1", Email: "person@example.com", EmailVerified: true, Nonce: nonce,
	}
	store.consumeReq = domain.OIDCConsumedAuthRequest{
		ID:                    store.createdAuthReq.ID,
		Provider:              "keycloak",
		NonceHash:             tokens.HashOIDCNonce(nonce),
		PKCEVerifierEncrypted: store.createdAuthReq.PKCEVerifierEncrypted,
		AppContext:            domain.OIDCAppAdmin,
	}

	// The callback input carries no context field at all — this is the whole
	// input surface, and none of it names a destination.
	frontend, err := svc.Callback(context.Background(), OIDCCallbackInput{
		Code:  "provider-code",
		State: state,
	})
	if err != nil {
		t.Fatalf("Callback: %v", err)
	}
	if provider.exchangeRedirectURL != testAdminRedirectURL {
		t.Fatalf("exchange used %q, want the admin URI", provider.exchangeRedirectURL)
	}
	// And the browser is sent to a root-relative path, so it stays on whichever
	// origin the callback was served from. There is no absolute location here
	// for an attacker to have influenced.
	parsed, err := url.Parse(frontend)
	if err != nil {
		t.Fatalf("parse frontend location: %v", err)
	}
	if parsed.Scheme != "" || parsed.Host != "" || parsed.Path != domain.OIDCFrontendCallbackPath {
		t.Fatalf("frontend redirect must stay same-origin and fixed, got %q", frontend)
	}
}

// A stored context the deployment no longer publishes cannot complete a run.
// Silently exchanging with another context's URI would fail at the provider
// anyway; refusing here keeps the reason honest.
func TestCallback_RefusesAStoredContextThatIsNoLongerConfigured(t *testing.T) {
	tokens := newTestOIDCTokenManager(t)
	store := &fakeOIDCStore{}
	provider := &fakeOIDCProvider{}
	svc, err := NewOIDCService(OIDCServiceConfig{
		Enabled:             true,
		Configured:          true,
		ProviderName:        "keycloak",
		FrontendCallbackURL: "/oidc-callback",
		StateTTL:            10 * time.Minute,
		RedirectURLs:        map[domain.OIDCAppContext]string{domain.OIDCAppChat: testChatRedirectURL},
	}, tokens, store, provider)
	if err != nil {
		t.Fatalf("NewOIDCService: %v", err)
	}

	store.consumeReq = domain.OIDCConsumedAuthRequest{
		ID:                    "auth-id",
		Provider:              "keycloak",
		NonceHash:             tokens.HashOIDCNonce("nonce"),
		PKCEVerifierEncrypted: "",
		AppContext:            domain.OIDCAppAdmin,
	}

	if _, err := svc.Callback(context.Background(), OIDCCallbackInput{Code: "code", State: "state"}); !errors.Is(err, domain.ErrOIDCInvalidCallback) {
		t.Fatalf("expected ErrOIDCInvalidCallback, got %v", err)
	}
	if provider.exchangeRedirectURL != "" {
		t.Fatalf("no exchange may be attempted, got %q", provider.exchangeRedirectURL)
	}
}

func queryValue(t *testing.T, rawURL, key string) string {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse %q: %v", rawURL, err)
	}
	value := parsed.Query().Get(key)
	if value == "" {
		t.Fatalf("missing %q in %q", key, rawURL)
	}
	return value
}
