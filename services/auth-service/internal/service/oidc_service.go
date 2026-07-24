package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
)

const oidcExchangeTTL = 2 * time.Minute

type OIDCServiceConfig struct {
	Enabled             bool
	Configured          bool
	ProviderName        string
	FrontendCallbackURL string
	StateTTL            time.Duration
	AutoProvision       bool
	AllowedDomains      []string
}

type OIDCCallbackInput struct {
	Code              string
	State             string
	DeviceFingerprint string
	DeviceName        string
	Platform          string
	IPAddress         string
	UserAgent         string
}

type OIDCStore interface {
	CreateAuthRequest(ctx context.Context, req domain.OIDCLoginRequest) error
	ConsumeAuthRequest(ctx context.Context, provider, stateHash string) (domain.OIDCConsumedAuthRequest, error)
	CreateOIDCSessionAndExchange(ctx context.Context, input domain.OIDCSessionInput, buildExchange func(domain.Session, domain.LoginUser) (domain.OIDCExchangeInput, error)) (domain.OIDCCreatedSession, error)
	ConsumeExchange(ctx context.Context, provider, codeHash string) (domain.OIDCConsumedExchange, error)
}

type OIDCManager interface {
	Login(ctx context.Context) (string, error)
	Callback(ctx context.Context, input OIDCCallbackInput) (string, error)
	Exchange(ctx context.Context, code string) (domain.OIDCExchangeResult, error)
}

type OIDCService struct {
	cfg      OIDCServiceConfig
	tokens   *TokenManager
	store    OIDCStore
	provider OIDCProvider
	crypto   *oidcCrypto
}

func NewOIDCService(cfg OIDCServiceConfig, tokens *TokenManager, store OIDCStore, provider OIDCProvider) (*OIDCService, error) {
	if cfg.ProviderName == "" {
		cfg.ProviderName = "keycloak"
	}
	if cfg.StateTTL <= 0 {
		cfg.StateTTL = 10 * time.Minute
	}
	if !cfg.Enabled {
		return &OIDCService{cfg: cfg}, nil
	}
	if !cfg.Configured || tokens == nil || store == nil || provider == nil {
		cfg.Configured = false
		return &OIDCService{cfg: cfg}, nil
	}
	crypto, err := newOIDCCrypto(tokens.secret)
	if err != nil {
		return nil, err
	}
	return &OIDCService{cfg: cfg, tokens: tokens, store: store, provider: provider, crypto: crypto}, nil
}

func (s *OIDCService) Login(ctx context.Context) (string, error) {
	if err := s.ensureAvailable(); err != nil {
		return "", err
	}
	state, err := randomOpaqueString(32)
	if err != nil {
		return "", fmt.Errorf("generate oidc state: %w", err)
	}
	nonce, err := randomOpaqueString(32)
	if err != nil {
		return "", fmt.Errorf("generate oidc nonce: %w", err)
	}
	verifier, err := randomOpaqueString(32)
	if err != nil {
		return "", fmt.Errorf("generate oidc pkce verifier: %w", err)
	}
	id := uuid.NewString()
	encryptedVerifier, err := s.crypto.encryptPKCEVerifier(s.cfg.ProviderName, id, verifier)
	if err != nil {
		return "", err
	}
	if err := s.store.CreateAuthRequest(ctx, domain.OIDCLoginRequest{
		ID:                    id,
		Provider:              s.cfg.ProviderName,
		StateHash:             s.tokens.HashOIDCState(state),
		NonceHash:             s.tokens.HashOIDCNonce(nonce),
		PKCEVerifierEncrypted: encryptedVerifier,
		ExpiresAt:             time.Now().UTC().Add(s.cfg.StateTTL),
	}); err != nil {
		return "", err
	}
	challenge := pkceChallenge(verifier)
	return s.provider.AuthorizationURL(state, nonce, challenge)
}

func (s *OIDCService) Callback(ctx context.Context, input OIDCCallbackInput) (string, error) {
	if err := s.ensureAvailable(); err != nil {
		return "", err
	}
	code := strings.TrimSpace(input.Code)
	state := strings.TrimSpace(input.State)
	if code == "" || state == "" {
		return "", domain.ErrOIDCInvalidCallback
	}
	authReq, err := s.store.ConsumeAuthRequest(ctx, s.cfg.ProviderName, s.tokens.HashOIDCState(state))
	if err != nil {
		if errors.Is(err, domain.ErrInvalidToken) {
			return "", domain.ErrOIDCInvalidCallback
		}
		return "", err
	}
	verifier, err := s.crypto.decryptPKCEVerifier(authReq.Provider, authReq.ID, authReq.PKCEVerifierEncrypted)
	if err != nil {
		return "", domain.ErrOIDCInvalidCallback
	}
	providerTokens, err := s.provider.ExchangeCode(ctx, code, verifier)
	if err != nil {
		return "", domain.ErrOIDCInvalidCallback
	}
	claims, err := s.provider.ValidateIDToken(ctx, providerTokens.IDToken)
	if err != nil {
		return "", domain.ErrOIDCInvalidCallback
	}
	if !hmac.Equal([]byte(s.tokens.HashOIDCNonce(claims.Nonce)), []byte(authReq.NonceHash)) {
		return "", domain.ErrOIDCInvalidCallback
	}
	if !claims.EmailVerified {
		return "", domain.ErrOIDCEmailUnverified
	}
	email := domain.NormalizeEmail(claims.Email)
	if err := domain.ValidateEmail(email); err != nil {
		return "", domain.ErrOIDCInvalidCallback
	}
	if !s.emailDomainAllowed(email) {
		return "", domain.ErrOIDCDomainForbidden
	}

	refreshToken, refreshHash, refreshExpiresAt, err := s.tokens.GenerateRefreshToken()
	if err != nil {
		return "", err
	}
	var rawExchangeCode string
	created, err := s.store.CreateOIDCSessionAndExchange(ctx, domain.OIDCSessionInput{
		Provider:              s.cfg.ProviderName,
		Subject:               strings.TrimSpace(claims.Subject),
		Email:                 email,
		DisplayName:           oidcDisplayName(claims),
		FullName:              oidcFullName(claims),
		AvatarURL:             oidcAvatarURL(claims),
		RefreshTokenHash:      refreshHash,
		RefreshExpiresAt:      refreshExpiresAt,
		DeviceFingerprintHash: s.tokens.HashDeviceFingerprint(input.DeviceFingerprint),
		DeviceName:            strings.TrimSpace(input.DeviceName),
		Platform:              strings.TrimSpace(input.Platform),
		IPAddress:             input.IPAddress,
		UserAgent:             input.UserAgent,
		AutoProvision:         s.cfg.AutoProvision,
	}, func(session domain.Session, user domain.LoginUser) (domain.OIDCExchangeInput, error) {
		accessToken, expiresIn, issueErr := s.tokens.GenerateAccessToken(session.UserID, session.ID)
		if issueErr != nil {
			return domain.OIDCExchangeInput{}, issueErr
		}
		rawExchangeCode, issueErr = randomOpaqueString(32)
		if issueErr != nil {
			return domain.OIDCExchangeInput{}, issueErr
		}
		exchangeID := uuid.NewString()
		accessEncrypted, issueErr := s.crypto.encryptExchangeValue(s.cfg.ProviderName, exchangeID, accessToken)
		if issueErr != nil {
			return domain.OIDCExchangeInput{}, issueErr
		}
		refreshEncrypted, issueErr := s.crypto.encryptExchangeValue(s.cfg.ProviderName, exchangeID, refreshToken)
		if issueErr != nil {
			return domain.OIDCExchangeInput{}, issueErr
		}
		return domain.OIDCExchangeInput{
			ID:                    exchangeID,
			Provider:              s.cfg.ProviderName,
			CodeHash:              s.tokens.HashOIDCExchangeCode(rawExchangeCode),
			AccessValueEncrypted:  accessEncrypted,
			RefreshValueEncrypted: refreshEncrypted,
			BearerScheme:          bearerTokenType,
			ExpiresIn:             expiresIn,
			User:                  user,
			ExpiresAt:             time.Now().UTC().Add(oidcExchangeTTL),
		}, nil
	})
	if err != nil {
		return "", err
	}
	if created.Session.ID == "" || rawExchangeCode == "" {
		return "", fmt.Errorf("oidc session exchange not created")
	}
	return appendFixedCallbackCode(s.cfg.FrontendCallbackURL, rawExchangeCode)
}

func (s *OIDCService) Exchange(ctx context.Context, code string) (domain.OIDCExchangeResult, error) {
	if err := s.ensureAvailable(); err != nil {
		return domain.OIDCExchangeResult{}, err
	}
	code = strings.TrimSpace(code)
	if code == "" || len(code) > 256 {
		return domain.OIDCExchangeResult{}, domain.ErrInvalidToken
	}
	stored, err := s.store.ConsumeExchange(ctx, s.cfg.ProviderName, s.tokens.HashOIDCExchangeCode(code))
	if err != nil {
		return domain.OIDCExchangeResult{}, err
	}
	access, err := s.crypto.decryptExchangeValue(stored.Provider, stored.ID, stored.AccessValueEncrypted)
	if err != nil {
		return domain.OIDCExchangeResult{}, domain.ErrInvalidToken
	}
	refresh, err := s.crypto.decryptExchangeValue(stored.Provider, stored.ID, stored.RefreshValueEncrypted)
	if err != nil {
		return domain.OIDCExchangeResult{}, domain.ErrInvalidToken
	}
	return domain.OIDCExchangeResult{
		AccessToken:  access,
		RefreshToken: refresh,
		TokenType:    stored.BearerScheme,
		ExpiresIn:    stored.ExpiresIn,
		User:         stored.User,
	}, nil
}

func (s *OIDCService) ensureAvailable() error {
	if s == nil || !s.cfg.Enabled {
		return domain.ErrOIDCDisabled
	}
	if !s.cfg.Configured || s.tokens == nil || s.store == nil || s.provider == nil || s.crypto == nil {
		return domain.ErrOIDCMisconfigured
	}
	return nil
}

func (s *OIDCService) emailDomainAllowed(email string) bool {
	if len(s.cfg.AllowedDomains) == 0 {
		return true
	}
	_, domainPart, ok := strings.Cut(email, "@")
	if !ok {
		return false
	}
	domainPart = strings.ToLower(strings.TrimSpace(domainPart))
	for _, allowed := range s.cfg.AllowedDomains {
		if domainPart == strings.ToLower(strings.TrimSpace(allowed)) {
			return true
		}
	}
	return false
}

func pkceChallenge(verifier string) string {
	digest := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

// oidcDisplayName resolves the short visual name: the provider's `name` first,
// then preferred_username as a fallback. The e-mail is never used, so a login
// cannot turn an address into a visible label.
//
// It returns "" when the provider sent nothing usable. That is deliberate: an
// empty result means "no opinion", which lets the storage layer keep an existing
// name on re-login and apply domain.DefaultDisplayName only when provisioning a
// brand new user.
func oidcDisplayName(claims domain.OIDCClaims) string {
	if name := sanitizeOIDCName(claims.Name); name != "" {
		return name
	}
	return sanitizeOIDCName(claims.PreferredUsername)
}

// maxOIDCNameLength bounds identity strings coming from an external IdP. Names
// are stored in unbounded TEXT columns, so the cap lives here rather than in the
// schema; it counts runes so accented and CJK names are not truncated early.
const maxOIDCNameLength = 200

// maxOIDCAvatarURLLength bounds the avatar reference for the same reason.
const maxOIDCAvatarURLLength = 512

// oidcFullName resolves the person's full name from the standard profile claims.
// `name` wins because Keycloak composes it from the user's first and last name;
// given_name/family_name is the fallback for providers that only emit the parts.
// preferred_username is deliberately never used: a username is not a full name,
// and display_name already covers that fallback.
// An empty result means "the provider told us nothing" and must never overwrite
// a stored value.
func oidcFullName(claims domain.OIDCClaims) string {
	if name := sanitizeOIDCName(claims.Name); name != "" {
		return name
	}
	given := sanitizeOIDCName(claims.GivenName)
	family := sanitizeOIDCName(claims.FamilyName)
	return sanitizeOIDCName(strings.TrimSpace(given + " " + family))
}

// sanitizeOIDCName trims, collapses internal whitespace and drops values that
// carry control characters or exceed the length cap. Unicode letters, marks and
// punctuation are preserved untouched.
func sanitizeOIDCName(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	for _, r := range trimmed {
		// Rejects C0/C1 controls (including CR/LF) and the DEL character, which
		// have no place in a name and would corrupt logs and headers downstream.
		if unicode.IsControl(r) {
			return ""
		}
	}
	if utf8.RuneCountInString(trimmed) > maxOIDCNameLength {
		return ""
	}
	return strings.Join(strings.Fields(trimmed), " ")
}

// oidcAvatarURL accepts the `picture` claim only as a same-origin reference.
//
// The auth-service has no knowledge of the browser origin, so the only form it
// can verify as same-origin is a root-relative path: it resolves against
// whatever origin serves the app, by construction. Every absolute URL is
// rejected — including https:// ones — because loading a third-party image in
// the browser would violate the deployed CSP (`img-src 'self'`), leak the
// viewer's IP to the provider, and turn the avatar into a tracking pixel.
//
// Consequence: Keycloak's `picture` claim, which is absolute, is stored as
// NULL. That is intentional. Making external pictures usable requires
// same-origin ingestion, which does not exist yet (see docs/architecture).
func oidcAvatarURL(claims domain.OIDCClaims) string {
	raw := strings.TrimSpace(claims.Picture)
	if raw == "" || utf8.RuneCountInString(raw) > maxOIDCAvatarURLLength {
		return ""
	}
	for _, r := range raw {
		if unicode.IsControl(r) {
			return ""
		}
	}
	// A leading "//" is protocol-relative: it points at another host despite
	// looking relative. A backslash is treated as a slash by browsers, so
	// "/\evil.com" would escape the origin too.
	if !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") || strings.ContainsRune(raw, '\\') {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "" || parsed.Host != "" || parsed.User != nil {
		return ""
	}
	return raw
}

func appendFixedCallbackCode(callbackURL, code string) (string, error) {
	target, err := parseFixedFrontendCallbackPath(callbackURL)
	if err != nil {
		return "", err
	}
	q := target.Query()
	q.Set("code", code)
	target.RawQuery = q.Encode()
	return target.String(), nil
}

func parseFixedFrontendCallbackPath(callbackURL string) (*url.URL, error) {
	if strings.ContainsAny(callbackURL, "\r\n") {
		return nil, domain.ErrOIDCMisconfigured
	}
	if !strings.HasPrefix(callbackURL, "/") || strings.HasPrefix(callbackURL, "//") {
		return nil, domain.ErrOIDCMisconfigured
	}
	target, err := url.Parse(callbackURL)
	if err != nil {
		return nil, domain.ErrOIDCMisconfigured
	}
	if target.Scheme != "" || target.Host != "" || target.User != nil || target.Fragment != "" || target.RawQuery != "" || target.Path != domain.OIDCFrontendCallbackPath {
		return nil, domain.ErrOIDCMisconfigured
	}
	return target, nil
}
