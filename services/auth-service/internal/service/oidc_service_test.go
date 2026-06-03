package service

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
)

type fakeOIDCStore struct {
	createdAuthReq     domain.OIDCLoginRequest
	createAuthErr      error
	consumeReq         domain.OIDCConsumedAuthRequest
	consumeErr         error
	createdInput       domain.OIDCSessionInput
	createdExchange    domain.OIDCExchangeInput
	createErr          error
	emptyCreated       bool
	consumeExchange    domain.OIDCConsumedExchange
	consumeExchangeErr error
}

func (f *fakeOIDCStore) CreateAuthRequest(_ context.Context, req domain.OIDCLoginRequest) error {
	f.createdAuthReq = req
	return f.createAuthErr
}

func (f *fakeOIDCStore) ConsumeAuthRequest(_ context.Context, provider, stateHash string) (domain.OIDCConsumedAuthRequest, error) {
	if f.consumeErr != nil {
		return domain.OIDCConsumedAuthRequest{}, f.consumeErr
	}
	if provider != f.consumeReq.Provider {
		return domain.OIDCConsumedAuthRequest{}, domain.ErrInvalidToken
	}
	return f.consumeReq, nil
}

func (f *fakeOIDCStore) CreateOIDCSessionAndExchange(_ context.Context, input domain.OIDCSessionInput, build func(domain.Session, domain.LoginUser) (domain.OIDCExchangeInput, error)) (domain.OIDCCreatedSession, error) {
	f.createdInput = input
	if f.createErr != nil {
		return domain.OIDCCreatedSession{}, f.createErr
	}
	user := domain.LoginUser{ID: "user-id", Email: input.Email, DisplayName: input.DisplayName}
	exchange, err := build(domain.Session{ID: "session-id", UserID: "user-id"}, user)
	if err != nil {
		return domain.OIDCCreatedSession{}, err
	}
	f.createdExchange = exchange
	if f.emptyCreated {
		return domain.OIDCCreatedSession{}, nil
	}
	f.consumeExchange = domain.OIDCConsumedExchange{
		ID:                    exchange.ID,
		Provider:              exchange.Provider,
		AccessValueEncrypted:  exchange.AccessValueEncrypted,
		RefreshValueEncrypted: exchange.RefreshValueEncrypted,
		BearerScheme:          exchange.BearerScheme,
		ExpiresIn:             exchange.ExpiresIn,
		User:                  exchange.User,
	}
	return domain.OIDCCreatedSession{Session: domain.Session{ID: "session-id", UserID: "user-id"}, User: user}, nil
}

func (f *fakeOIDCStore) ConsumeExchange(_ context.Context, provider, codeHash string) (domain.OIDCConsumedExchange, error) {
	if f.consumeExchangeErr != nil {
		return domain.OIDCConsumedExchange{}, f.consumeExchangeErr
	}
	if provider != f.consumeExchange.Provider || codeHash == "" {
		return domain.OIDCConsumedExchange{}, domain.ErrInvalidToken
	}
	return f.consumeExchange, nil
}

type fakeOIDCProvider struct {
	claims        domain.OIDCClaims
	authErr       error
	exchangeErr   error
	validateErr   error
	exchangedCode string
	verifier      string
}

func (f *fakeOIDCProvider) AuthorizationURL(state, nonce, challenge string) (string, error) {
	if f.authErr != nil {
		return "", f.authErr
	}
	u, _ := url.Parse("https://keycloak.example.com/realms/nchat/protocol/openid-connect/auth")
	q := u.Query()
	q.Set("state", state)
	q.Set("nonce", nonce)
	q.Set("code_challenge", challenge)
	q.Set("scope", "openid email profile")
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func (f *fakeOIDCProvider) ExchangeCode(_ context.Context, code, verifier string) (oidcTokenSet, error) {
	if f.exchangeErr != nil {
		return oidcTokenSet{}, f.exchangeErr
	}
	f.exchangedCode = code
	f.verifier = verifier
	return oidcTokenSet{IDToken: "provider-id-token"}, nil
}

func (f *fakeOIDCProvider) ValidateIDToken(_ context.Context, _ string) (domain.OIDCClaims, error) {
	if f.validateErr != nil {
		return domain.OIDCClaims{}, f.validateErr
	}
	return f.claims, nil
}

func TestOIDCService_LoginStoresHashedStateAndBuildsAuthorizationURL(t *testing.T) {
	tokens := newTestOIDCTokenManager(t)
	store := &fakeOIDCStore{}
	svc := newTestOIDCService(t, tokens, store, &fakeOIDCProvider{})

	location, err := svc.Login(context.Background())
	if err != nil {
		t.Fatalf("login: %v", err)
	}

	u, err := url.Parse(location)
	if err != nil {
		t.Fatalf("parse location: %v", err)
	}
	state := u.Query().Get("state")
	nonce := u.Query().Get("nonce")
	if state == "" || nonce == "" || u.Query().Get("code_challenge") == "" {
		t.Fatalf("authorization URL missing OIDC parameters: %s", location)
	}
	if store.createdAuthReq.StateHash == state || store.createdAuthReq.NonceHash == nonce {
		t.Fatalf("state and nonce must be stored hashed, req=%+v url=%s", store.createdAuthReq, location)
	}
	if store.createdAuthReq.StateHash != tokens.HashOIDCState(state) {
		t.Fatalf("stored state hash mismatch")
	}
	if store.createdAuthReq.NonceHash != tokens.HashOIDCNonce(nonce) {
		t.Fatalf("stored nonce hash mismatch")
	}
	if store.createdAuthReq.PKCEVerifierEncrypted == "" || strings.Contains(location, store.createdAuthReq.PKCEVerifierEncrypted) {
		t.Fatalf("pkce verifier must be encrypted and not exposed")
	}
}

func TestOIDCService_CallbackRejectsMissingState(t *testing.T) {
	svc := newTestOIDCService(t, newTestOIDCTokenManager(t), &fakeOIDCStore{}, &fakeOIDCProvider{})

	_, err := svc.Callback(context.Background(), OIDCCallbackInput{Code: "code"})

	if !errors.Is(err, domain.ErrOIDCInvalidCallback) {
		t.Fatalf("expected invalid callback, got %v", err)
	}
}

func TestOIDCService_CallbackRejectsUnverifiedEmail(t *testing.T) {
	tokens := newTestOIDCTokenManager(t)
	store, provider := newCallbackStoreAndProvider(t, tokens, domain.OIDCClaims{Subject: "sub", Email: "user@example.com", EmailVerified: false, Nonce: "nonce"})
	svc := newTestOIDCService(t, tokens, store, provider)

	_, err := svc.Callback(context.Background(), OIDCCallbackInput{Code: "code", State: "state"})

	if !errors.Is(err, domain.ErrOIDCEmailUnverified) {
		t.Fatalf("expected unverified email error, got %v", err)
	}
}

func TestOIDCService_CallbackRejectsDisallowedDomain(t *testing.T) {
	tokens := newTestOIDCTokenManager(t)
	store, provider := newCallbackStoreAndProvider(t, tokens, domain.OIDCClaims{Subject: "sub", Email: "user@blocked.example", EmailVerified: true, Nonce: "nonce"})
	svc := newTestOIDCServiceWithDomains(t, tokens, store, provider, []string{"example.com"})

	_, err := svc.Callback(context.Background(), OIDCCallbackInput{Code: "code", State: "state"})

	if !errors.Is(err, domain.ErrOIDCDomainForbidden) {
		t.Fatalf("expected domain forbidden, got %v", err)
	}
}

func TestOIDCService_CallbackDoesNotHideManualEmailConflict(t *testing.T) {
	tokens := newTestOIDCTokenManager(t)
	store, provider := newCallbackStoreAndProvider(t, tokens, domain.OIDCClaims{Subject: "sub", Email: "user@example.com", EmailVerified: true, Nonce: "nonce"})
	store.createErr = domain.ErrOIDCAccountConflict
	svc := newTestOIDCService(t, tokens, store, provider)

	_, err := svc.Callback(context.Background(), OIDCCallbackInput{Code: "code", State: "state"})

	if !errors.Is(err, domain.ErrOIDCAccountConflict) {
		t.Fatalf("expected account conflict, got %v", err)
	}
}

func TestOIDCService_CallbackCreatesExchangeAndExchangeReturnsInternalTokens(t *testing.T) {
	tokens := newTestOIDCTokenManager(t)
	store, provider := newCallbackStoreAndProvider(t, tokens, domain.OIDCClaims{Subject: "sub", Email: "User@Example.com", EmailVerified: true, PreferredUsername: "Keycloak User", Nonce: "nonce"})
	svc := newTestOIDCService(t, tokens, store, provider)

	redirectURL, err := svc.Callback(context.Background(), OIDCCallbackInput{Code: "provider-code", State: "state", DeviceName: "Browser", Platform: "web", IPAddress: "127.0.0.1", UserAgent: "test-agent"})
	if err != nil {
		t.Fatalf("callback: %v", err)
	}

	u, err := url.Parse(redirectURL)
	if err != nil {
		t.Fatalf("parse redirect: %v", err)
	}
	exchangeCode := u.Query().Get("code")
	if u.Scheme != "" || u.Host != "" || u.Path != "/oidc-callback" {
		t.Fatalf("expected relative frontend callback redirect, got %s", redirectURL)
	}
	if exchangeCode == "" {
		t.Fatalf("expected one-time exchange code in redirect: %s", redirectURL)
	}
	if strings.Contains(redirectURL, store.createdExchange.AccessValueEncrypted) || strings.Contains(redirectURL, store.createdExchange.RefreshValueEncrypted) {
		t.Fatalf("redirect must not contain encrypted token values: %s", redirectURL)
	}
	if store.createdInput.Email != "user@example.com" || store.createdInput.DisplayName != "Keycloak User" || store.createdInput.Subject != "sub" {
		t.Fatalf("unexpected session input: %+v", store.createdInput)
	}
	if provider.exchangedCode != "provider-code" || provider.verifier == "" {
		t.Fatalf("provider exchange did not receive code and PKCE verifier")
	}

	result, err := svc.Exchange(context.Background(), exchangeCode)
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if result.AccessToken == "" || result.RefreshToken == "" || result.TokenType != "Bearer" || result.ExpiresIn <= 0 {
		t.Fatalf("unexpected exchange result: %+v", result)
	}
	if result.User.Email != "user@example.com" || result.User.DisplayName != "Keycloak User" {
		t.Fatalf("unexpected exchange user: %+v", result.User)
	}
}

func newTestOIDCTokenManager(t *testing.T) *TokenManager {
	t.Helper()
	tokens, err := NewTokenManager(TokenConfig{
		HMACSecret: strings.Repeat("a", 32),
		Issuer:     "issuer",
		Audience:   "audience",
		AccessTTL:  time.Minute,
		RefreshTTL: time.Hour,
	})
	if err != nil {
		t.Fatalf("new token manager: %v", err)
	}
	return tokens
}

func newTestOIDCService(t *testing.T, tokens *TokenManager, store *fakeOIDCStore, provider *fakeOIDCProvider) *OIDCService {
	t.Helper()
	return newTestOIDCServiceWithDomains(t, tokens, store, provider, nil)
}

func newTestOIDCServiceWithDomains(t *testing.T, tokens *TokenManager, store *fakeOIDCStore, provider *fakeOIDCProvider, domains []string) *OIDCService {
	t.Helper()
	svc, err := NewOIDCService(OIDCServiceConfig{
		Enabled:             true,
		Configured:          true,
		ProviderName:        "keycloak",
		FrontendCallbackURL: "/oidc-callback",
		StateTTL:            10 * time.Minute,
		AutoProvision:       true,
		AllowedDomains:      domains,
	}, tokens, store, provider)
	if err != nil {
		t.Fatalf("new oidc service: %v", err)
	}
	return svc
}

func newCallbackStoreAndProvider(t *testing.T, tokens *TokenManager, claims domain.OIDCClaims) (*fakeOIDCStore, *fakeOIDCProvider) {
	t.Helper()
	crypto, err := newOIDCCrypto(tokens.secret)
	if err != nil {
		t.Fatalf("new oidc crypto: %v", err)
	}
	encryptedVerifier, err := crypto.encryptPKCEVerifier("keycloak", "auth-id", "verifier")
	if err != nil {
		t.Fatalf("encrypt verifier: %v", err)
	}
	return &fakeOIDCStore{consumeReq: domain.OIDCConsumedAuthRequest{
		ID:                    "auth-id",
		Provider:              "keycloak",
		NonceHash:             tokens.HashOIDCNonce("nonce"),
		PKCEVerifierEncrypted: encryptedVerifier,
	}}, &fakeOIDCProvider{claims: claims}
}

func TestOIDCService_DisabledAndMisconfiguredFailClosed(t *testing.T) {
	disabled, err := NewOIDCService(OIDCServiceConfig{Enabled: false}, nil, nil, nil)
	if err != nil {
		t.Fatalf("new disabled service: %v", err)
	}
	if _, err := disabled.Login(context.Background()); !errors.Is(err, domain.ErrOIDCDisabled) {
		t.Fatalf("expected disabled error, got %v", err)
	}

	misconfigured, err := NewOIDCService(OIDCServiceConfig{Enabled: true, Configured: false, ProviderName: "keycloak"}, nil, nil, nil)
	if err != nil {
		t.Fatalf("new misconfigured service: %v", err)
	}
	if _, err := misconfigured.Login(context.Background()); !errors.Is(err, domain.ErrOIDCMisconfigured) {
		t.Fatalf("expected misconfigured error, got %v", err)
	}
}

func TestOIDCService_CallbackRejectsNonceMismatch(t *testing.T) {
	tokens := newTestOIDCTokenManager(t)
	store, provider := newCallbackStoreAndProvider(t, tokens, domain.OIDCClaims{Subject: "sub", Email: "user@example.com", EmailVerified: true, Nonce: "wrong-nonce"})
	svc := newTestOIDCService(t, tokens, store, provider)

	_, err := svc.Callback(context.Background(), OIDCCallbackInput{Code: "code", State: "state"})

	if !errors.Is(err, domain.ErrOIDCInvalidCallback) {
		t.Fatalf("expected invalid callback, got %v", err)
	}
}

func TestOIDCService_ExchangeRejectsMissingAndStoreRejectedCodes(t *testing.T) {
	tokens := newTestOIDCTokenManager(t)
	store := &fakeOIDCStore{consumeExchangeErr: domain.ErrInvalidToken}
	svc := newTestOIDCService(t, tokens, store, &fakeOIDCProvider{})

	if _, err := svc.Exchange(context.Background(), ""); !errors.Is(err, domain.ErrInvalidToken) {
		t.Fatalf("expected missing code invalid token, got %v", err)
	}
	if _, err := svc.Exchange(context.Background(), "bad-code"); !errors.Is(err, domain.ErrInvalidToken) {
		t.Fatalf("expected rejected code invalid token, got %v", err)
	}
}

func TestOIDCDisplayNameFallsBackWithoutEmailPII(t *testing.T) {
	if got := oidcDisplayName(domain.OIDCClaims{Name: "Full Name", PreferredUsername: "username", Email: "pii@example.com"}); got != "Full Name" {
		t.Fatalf("expected name to take precedence, got %q", got)
	}
	if got := oidcDisplayName(domain.OIDCClaims{PreferredUsername: "username", Email: "pii@example.com"}); got != "username" {
		t.Fatalf("expected preferred username fallback, got %q", got)
	}
	if got := oidcDisplayName(domain.OIDCClaims{Email: "pii@example.com"}); got != "Usuário" {
		t.Fatalf("expected generic fallback, got %q", got)
	}
}

func TestOIDCService_ExchangeRejectsUndecryptableStoredValues(t *testing.T) {
	tokens := newTestOIDCTokenManager(t)
	store := &fakeOIDCStore{consumeExchange: domain.OIDCConsumedExchange{ID: "exchange-id", Provider: "keycloak", AccessValueEncrypted: "not-valid", RefreshValueEncrypted: "not-valid", BearerScheme: "Bearer", ExpiresIn: 900}}
	svc := newTestOIDCService(t, tokens, store, &fakeOIDCProvider{})

	_, err := svc.Exchange(context.Background(), "opaque-code")
	if !errors.Is(err, domain.ErrInvalidToken) {
		t.Fatalf("expected invalid token, got %v", err)
	}
}

func TestOIDCService_CallbackRejectsInvalidProviderClaims(t *testing.T) {
	tokens := newTestOIDCTokenManager(t)
	store, provider := newCallbackStoreAndProvider(t, tokens, domain.OIDCClaims{Subject: "sub", Email: "not-an-email", EmailVerified: true, Nonce: "nonce"})
	svc := newTestOIDCService(t, tokens, store, provider)

	_, err := svc.Callback(context.Background(), OIDCCallbackInput{Code: "code", State: "state"})
	if !errors.Is(err, domain.ErrOIDCInvalidCallback) {
		t.Fatalf("expected invalid callback, got %v", err)
	}
}

func TestOIDCService_EmailDomainAllowedAcceptsConfiguredDomain(t *testing.T) {
	svc := &OIDCService{cfg: OIDCServiceConfig{AllowedDomains: []string{"Example.COM"}}}
	if !svc.emailDomainAllowed("user@example.com") {
		t.Fatal("expected configured domain to be allowed")
	}
	if svc.emailDomainAllowed("missing-at") {
		t.Fatal("expected malformed email to be rejected when allowlist is configured")
	}
}

func TestOIDCService_LoginPropagatesStoreAndProviderErrors(t *testing.T) {
	tokens := newTestOIDCTokenManager(t)
	store := &fakeOIDCStore{}
	svc := newTestOIDCService(t, tokens, store, &fakeOIDCProvider{authErr: errors.New("provider unavailable")})
	if _, err := svc.Login(context.Background()); err == nil {
		t.Fatal("expected provider authorization error")
	}
}

func TestOIDCService_CallbackRejectsReplayedStateAndBadVerifier(t *testing.T) {
	tokens := newTestOIDCTokenManager(t)
	replayed := newTestOIDCService(t, tokens, &fakeOIDCStore{consumeErr: domain.ErrInvalidToken}, &fakeOIDCProvider{})
	if _, err := replayed.Callback(context.Background(), OIDCCallbackInput{Code: "code", State: "state"}); !errors.Is(err, domain.ErrOIDCInvalidCallback) {
		t.Fatalf("expected replay invalid callback, got %v", err)
	}

	badStore := &fakeOIDCStore{consumeReq: domain.OIDCConsumedAuthRequest{ID: "auth-id", Provider: "keycloak", NonceHash: tokens.HashOIDCNonce("nonce"), PKCEVerifierEncrypted: "bad-envelope"}}
	badVerifier := newTestOIDCService(t, tokens, badStore, &fakeOIDCProvider{})
	if _, err := badVerifier.Callback(context.Background(), OIDCCallbackInput{Code: "code", State: "state"}); !errors.Is(err, domain.ErrOIDCInvalidCallback) {
		t.Fatalf("expected bad verifier invalid callback, got %v", err)
	}
}

func TestOIDCService_CallbackRejectsProviderExchangeAndValidationErrors(t *testing.T) {
	tokens := newTestOIDCTokenManager(t)
	store, provider := newCallbackStoreAndProvider(t, tokens, domain.OIDCClaims{Subject: "sub", Email: "user@example.com", EmailVerified: true, Nonce: "nonce"})
	provider.exchangeErr = errors.New("provider exchange failed")
	svc := newTestOIDCService(t, tokens, store, provider)
	if _, err := svc.Callback(context.Background(), OIDCCallbackInput{Code: "code", State: "state"}); !errors.Is(err, domain.ErrOIDCInvalidCallback) {
		t.Fatalf("expected exchange invalid callback, got %v", err)
	}

	store, provider = newCallbackStoreAndProvider(t, tokens, domain.OIDCClaims{})
	provider.validateErr = errors.New("provider validation failed")
	svc = newTestOIDCService(t, tokens, store, provider)
	if _, err := svc.Callback(context.Background(), OIDCCallbackInput{Code: "code", State: "state"}); !errors.Is(err, domain.ErrOIDCInvalidCallback) {
		t.Fatalf("expected validation invalid callback, got %v", err)
	}
}

func TestOIDCService_LoginPropagatesAuthRequestStoreError(t *testing.T) {
	tokens := newTestOIDCTokenManager(t)
	store := &fakeOIDCStore{createAuthErr: errors.New("state insert failed")}
	svc := newTestOIDCService(t, tokens, store, &fakeOIDCProvider{})

	if _, err := svc.Login(context.Background()); err == nil {
		t.Fatal("expected auth request store error")
	}
}

func TestOIDCService_CallbackRejectsInvalidFrontendCallbackURL(t *testing.T) {
	for _, callbackURL := range []string{
		"https://nchat.example.com/oidc-callback",
		"//evil.example.com/oidc-callback",
		"oidc-callback",
		"/oidc-callback\r\nSet-Cookie:bad=1",
		"/unexpected-callback",
	} {
		t.Run(callbackURL, func(t *testing.T) {
			tokens := newTestOIDCTokenManager(t)
			store, provider := newCallbackStoreAndProvider(t, tokens, domain.OIDCClaims{Subject: "sub", Email: "user@example.com", EmailVerified: true, Nonce: "nonce"})
			svc := newTestOIDCService(t, tokens, store, provider)
			svc.cfg.FrontendCallbackURL = callbackURL

			_, err := svc.Callback(context.Background(), OIDCCallbackInput{Code: "code", State: "state"})
			if !errors.Is(err, domain.ErrOIDCMisconfigured) {
				t.Fatalf("expected oidc misconfigured, got %v", err)
			}
		})
	}
}

func TestAppendFixedCallbackCodeReturnsRelativeRedirect(t *testing.T) {
	location, err := appendFixedCallbackCode("/oidc-callback", "opaque-code")
	if err != nil {
		t.Fatalf("append callback code: %v", err)
	}
	if location != "/oidc-callback?code=opaque-code" {
		t.Fatalf("expected relative redirect location, got %q", location)
	}
}

func TestOIDCService_ExchangeRejectsOversizedCode(t *testing.T) {
	tokens := newTestOIDCTokenManager(t)
	store := &fakeOIDCStore{}
	svc := newTestOIDCService(t, tokens, store, &fakeOIDCProvider{})

	_, err := svc.Exchange(context.Background(), strings.Repeat("a", 257))
	if !errors.Is(err, domain.ErrInvalidToken) {
		t.Fatalf("expected invalid token, got %v", err)
	}
}

func TestOIDCCryptoRejectsInvalidAESKeyMaterial(t *testing.T) {
	if _, err := encryptOIDCValue([]byte("short"), []byte("aad"), "value"); err == nil {
		t.Fatal("expected invalid encryption key to be rejected")
	}
	if _, err := decryptOIDCValue([]byte("short"), []byte("aad"), ""); err == nil {
		t.Fatal("expected invalid decryption key to be rejected")
	}
}

func TestOIDCService_CallbackRejectsMissingCreatedSession(t *testing.T) {
	tokens := newTestOIDCTokenManager(t)
	store, provider := newCallbackStoreAndProvider(t, tokens, domain.OIDCClaims{Subject: "sub", Email: "user@example.com", EmailVerified: true, Nonce: "nonce"})
	store.emptyCreated = true
	svc := newTestOIDCService(t, tokens, store, provider)

	if _, err := svc.Callback(context.Background(), OIDCCallbackInput{Code: "code", State: "state"}); err == nil {
		t.Fatal("expected missing created session error")
	}
}
