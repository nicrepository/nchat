package service

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
)

type oidcTokenSet struct {
	IDToken string
}

type OIDCProvider interface {
	AuthorizationURL(state, nonce, codeChallenge string) (string, error)
	ExchangeCode(ctx context.Context, code, verifier string) (oidcTokenSet, error)
	ValidateIDToken(ctx context.Context, rawIDToken string) (domain.OIDCClaims, error)
}

type KeycloakProviderConfig struct {
	IssuerURL    string
	ClientID     string
	ClientSecret string
	RedirectURL  string
	Scopes       string
	HTTPTimeout  time.Duration
	HTTPClient   *http.Client
}

type KeycloakProvider struct {
	cfg                KeycloakProviderConfig
	httpClient         *http.Client
	mu                 sync.Mutex
	discovery          *oidcDiscovery
	discoveryFetchedAt time.Time
	jwks               *oidcJWKS
	jwksFetchedAt      time.Time
}

const oidcMetadataCacheTTL = time.Hour

type oidcDiscovery struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	JWKSURI               string `json:"jwks_uri"`
}

type oidcJWKS struct {
	Keys []oidcJWK `json:"keys"`
}

type oidcJWK struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	N   string `json:"n"`
	E   string `json:"e"`
}

type keycloakIDClaims struct {
	Nonce             string `json:"nonce"`
	Email             string `json:"email"`
	EmailVerified     bool   `json:"email_verified"`
	PreferredUsername string `json:"preferred_username"`
	Name              string `json:"name"`
	GivenName         string `json:"given_name"`
	FamilyName        string `json:"family_name"`
	Picture           string `json:"picture"`
	AuthorizedParty   string `json:"azp"`
	jwt.RegisteredClaims
}

func NewKeycloakProvider(cfg KeycloakProviderConfig) *KeycloakProvider {
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: cfg.HTTPTimeout}
	}
	if client.Timeout == 0 && cfg.HTTPTimeout > 0 {
		client.Timeout = cfg.HTTPTimeout
	}
	cfg.IssuerURL = strings.TrimRight(strings.TrimSpace(cfg.IssuerURL), "/")
	return &KeycloakProvider{cfg: cfg, httpClient: client}
}

func (p *KeycloakProvider) AuthorizationURL(state, nonce, codeChallenge string) (string, error) {
	discovery, err := p.getDiscovery(context.Background())
	if err != nil {
		return "", err
	}
	authURL, err := url.Parse(discovery.AuthorizationEndpoint)
	if err != nil {
		return "", fmt.Errorf("parse authorization endpoint: %w", err)
	}
	q := authURL.Query()
	q.Set("client_id", p.cfg.ClientID)
	q.Set("redirect_uri", p.cfg.RedirectURL)
	q.Set("response_type", "code")
	q.Set("scope", p.cfg.Scopes)
	q.Set("state", state)
	q.Set("nonce", nonce)
	q.Set("code_challenge", codeChallenge)
	q.Set("code_challenge_method", "S256")
	authURL.RawQuery = q.Encode()
	return authURL.String(), nil
}

func (p *KeycloakProvider) ExchangeCode(ctx context.Context, code, verifier string) (oidcTokenSet, error) {
	discovery, err := p.getDiscovery(ctx)
	if err != nil {
		return oidcTokenSet{}, err
	}
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("client_id", p.cfg.ClientID)
	form.Set("client_secret", p.cfg.ClientSecret)
	form.Set("redirect_uri", p.cfg.RedirectURL)
	form.Set("code", code)
	form.Set("code_verifier", verifier)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, discovery.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return oidcTokenSet{}, fmt.Errorf("build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return oidcTokenSet{}, fmt.Errorf("exchange oidc code: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return oidcTokenSet{}, domain.ErrOIDCInvalidCallback
	}
	var payload struct {
		IDToken string `json:"id_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return oidcTokenSet{}, fmt.Errorf("decode oidc token response: %w", err)
	}
	if strings.TrimSpace(payload.IDToken) == "" {
		return oidcTokenSet{}, domain.ErrOIDCInvalidCallback
	}
	return oidcTokenSet{IDToken: payload.IDToken}, nil
}

func (p *KeycloakProvider) ValidateIDToken(ctx context.Context, rawIDToken string) (domain.OIDCClaims, error) {
	var claims keycloakIDClaims
	parser := jwt.NewParser(jwt.WithIssuer(p.cfg.IssuerURL), jwt.WithAudience(p.cfg.ClientID), jwt.WithExpirationRequired())
	token, err := parser.ParseWithClaims(rawIDToken, &claims, func(token *jwt.Token) (any, error) {
		if token.Method != jwt.SigningMethodRS256 {
			return nil, fmt.Errorf("unexpected oidc signing method")
		}
		kid, _ := token.Header["kid"].(string)
		if kid == "" {
			return nil, fmt.Errorf("missing oidc kid")
		}
		key, keyErr := p.getSigningKey(ctx, kid, false)
		if keyErr == nil {
			return key, nil
		}
		return p.getSigningKey(ctx, kid, true)
	})
	if err != nil {
		return domain.OIDCClaims{}, fmt.Errorf("validate oidc id token: %w", err)
	}
	if !token.Valid {
		return domain.OIDCClaims{}, domain.ErrOIDCInvalidCallback
	}
	if claims.AuthorizedParty != "" && claims.AuthorizedParty != p.cfg.ClientID {
		return domain.OIDCClaims{}, domain.ErrOIDCInvalidCallback
	}
	if len(claims.Audience) > 1 && claims.AuthorizedParty == "" {
		return domain.OIDCClaims{}, domain.ErrOIDCInvalidCallback
	}
	if strings.TrimSpace(claims.Subject) == "" || strings.TrimSpace(claims.Email) == "" || strings.TrimSpace(claims.Nonce) == "" {
		return domain.OIDCClaims{}, domain.ErrOIDCInvalidCallback
	}
	return domain.OIDCClaims{
		Subject:           claims.Subject,
		Email:             claims.Email,
		EmailVerified:     claims.EmailVerified,
		PreferredUsername: claims.PreferredUsername,
		Name:              claims.Name,
		GivenName:         claims.GivenName,
		FamilyName:        claims.FamilyName,
		Picture:           claims.Picture,
		Nonce:             claims.Nonce,
	}, nil
}

func (p *KeycloakProvider) getDiscovery(ctx context.Context) (oidcDiscovery, error) {
	now := time.Now()
	p.mu.Lock()
	cached := p.discovery
	fetchedAt := p.discoveryFetchedAt
	p.mu.Unlock()
	if cached != nil && now.Sub(fetchedAt) < oidcMetadataCacheTTL {
		return *cached, nil
	}

	if err := validateOIDCIssuerURL(p.cfg.IssuerURL); err != nil {
		return oidcDiscovery{}, err
	}
	discoveryURL := p.cfg.IssuerURL + "/.well-known/openid-configuration"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, discoveryURL, nil)
	if err != nil {
		return oidcDiscovery{}, fmt.Errorf("build oidc discovery request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return oidcDiscovery{}, fmt.Errorf("fetch oidc discovery: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return oidcDiscovery{}, domain.ErrOIDCMisconfigured
	}
	var discovery oidcDiscovery
	if err := json.NewDecoder(resp.Body).Decode(&discovery); err != nil {
		return oidcDiscovery{}, fmt.Errorf("decode oidc discovery: %w", err)
	}
	if discovery.Issuer == "" || discovery.AuthorizationEndpoint == "" || discovery.TokenEndpoint == "" || discovery.JWKSURI == "" {
		return oidcDiscovery{}, domain.ErrOIDCMisconfigured
	}
	if err := p.validateDiscovery(discovery); err != nil {
		return oidcDiscovery{}, err
	}
	p.mu.Lock()
	p.discovery = &discovery
	p.discoveryFetchedAt = now
	p.mu.Unlock()
	return discovery, nil
}

func (p *KeycloakProvider) validateDiscovery(discovery oidcDiscovery) error {
	if discovery.Issuer != p.cfg.IssuerURL {
		return domain.ErrOIDCMisconfigured
	}
	issuerURL, err := parseOIDCIssuerURL(p.cfg.IssuerURL)
	if err != nil {
		return err
	}
	for _, endpoint := range []string{discovery.AuthorizationEndpoint, discovery.TokenEndpoint, discovery.JWKSURI} {
		if err := validateOIDCEndpointOrigin(issuerURL, endpoint); err != nil {
			return err
		}
	}
	return nil
}

func validateOIDCIssuerURL(rawIssuerURL string) error {
	_, err := parseOIDCIssuerURL(rawIssuerURL)
	return err
}

func parseOIDCIssuerURL(rawIssuerURL string) (*url.URL, error) {
	issuerURL, err := url.Parse(rawIssuerURL)
	if err != nil || issuerURL.Scheme == "" || issuerURL.Host == "" || issuerURL.User != nil || issuerURL.Fragment != "" || issuerURL.RawQuery != "" {
		return nil, domain.ErrOIDCMisconfigured
	}
	if !oidcEndpointSchemeAllowed(issuerURL) {
		return nil, domain.ErrOIDCMisconfigured
	}
	return issuerURL, nil
}

func validateOIDCEndpointOrigin(issuerURL *url.URL, endpoint string) error {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return domain.ErrOIDCMisconfigured
	}
	if parsed.User != nil || parsed.Fragment != "" {
		return domain.ErrOIDCMisconfigured
	}
	if parsed.Scheme != issuerURL.Scheme || parsed.Host != issuerURL.Host {
		return domain.ErrOIDCMisconfigured
	}
	if !oidcEndpointSchemeAllowed(parsed) {
		return domain.ErrOIDCMisconfigured
	}
	return nil
}

func oidcEndpointSchemeAllowed(parsed *url.URL) bool {
	if parsed.Scheme == "https" {
		return true
	}
	return parsed.Scheme == "http" && oidcLocalhost(parsed.Hostname())
}

func oidcLocalhost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (p *KeycloakProvider) getSigningKey(ctx context.Context, kid string, refresh bool) (*rsa.PublicKey, error) {
	jwks, err := p.getJWKS(ctx, refresh)
	if err != nil {
		return nil, err
	}
	for _, key := range jwks.Keys {
		if key.Kid != kid || key.Kty != "RSA" {
			continue
		}
		if key.Use != "" && key.Use != "sig" {
			continue
		}
		if key.Alg != "" && key.Alg != "RS256" {
			continue
		}
		return jwkToRSAPublicKey(key)
	}
	return nil, errors.New("oidc signing key not found")
}

func (p *KeycloakProvider) getJWKS(ctx context.Context, refresh bool) (oidcJWKS, error) {
	now := time.Now()
	p.mu.Lock()
	cached := p.jwks
	fetchedAt := p.jwksFetchedAt
	p.mu.Unlock()
	if cached != nil && !refresh && now.Sub(fetchedAt) < oidcMetadataCacheTTL {
		return *cached, nil
	}
	discovery, err := p.getDiscovery(ctx)
	if err != nil {
		return oidcJWKS{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, discovery.JWKSURI, nil)
	if err != nil {
		return oidcJWKS{}, fmt.Errorf("build jwks request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return oidcJWKS{}, fmt.Errorf("fetch jwks: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return oidcJWKS{}, domain.ErrOIDCMisconfigured
	}
	var jwks oidcJWKS
	if err := json.NewDecoder(resp.Body).Decode(&jwks); err != nil {
		return oidcJWKS{}, fmt.Errorf("decode jwks: %w", err)
	}
	p.mu.Lock()
	p.jwks = &jwks
	p.jwksFetchedAt = now
	p.mu.Unlock()
	return jwks, nil
}

func jwkToRSAPublicKey(key oidcJWK) (*rsa.PublicKey, error) {
	nBytes, err := base64.RawURLEncoding.DecodeString(key.N)
	if err != nil {
		return nil, fmt.Errorf("decode jwk n: %w", err)
	}
	eBytes, err := base64.RawURLEncoding.DecodeString(key.E)
	if err != nil {
		return nil, fmt.Errorf("decode jwk e: %w", err)
	}
	e := 0
	for _, b := range eBytes {
		e = e<<8 + int(b)
	}
	if e == 0 {
		return nil, fmt.Errorf("invalid jwk exponent")
	}
	return &rsa.PublicKey{N: new(big.Int).SetBytes(nBytes), E: e}, nil
}

// AzureADProviderConfig configures an Azure Active Directory OIDC provider.
// TenantID is the Azure tenant GUID or domain (e.g. "contoso.onmicrosoft.com").
type AzureADProviderConfig struct {
	TenantID     string
	ClientID     string
	ClientSecret string
	RedirectURL  string
	Scopes       string
	HTTPTimeout  time.Duration
	HTTPClient   *http.Client
}

// NewAzureADProvider returns an OIDCProvider configured for Azure AD using the
// standard v2.0 endpoint for the given tenant.
// Returns ErrOIDCMisconfigured if TenantID or ClientID is empty.
func NewAzureADProvider(cfg AzureADProviderConfig) (OIDCProvider, error) {
	if strings.TrimSpace(cfg.TenantID) == "" || strings.TrimSpace(cfg.ClientID) == "" {
		return nil, domain.ErrOIDCMisconfigured
	}
	scopes := cfg.Scopes
	if scopes == "" {
		scopes = "openid email profile"
	}
	return NewKeycloakProvider(KeycloakProviderConfig{
		IssuerURL:    "https://login.microsoftonline.com/" + strings.TrimSpace(cfg.TenantID) + "/v2.0",
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		RedirectURL:  cfg.RedirectURL,
		Scopes:       scopes,
		HTTPTimeout:  cfg.HTTPTimeout,
		HTTPClient:   cfg.HTTPClient,
	}), nil
}

// GoogleWorkspaceProviderConfig configures a Google Workspace (Google Identity) OIDC provider.
type GoogleWorkspaceProviderConfig struct {
	ClientID     string
	ClientSecret string
	RedirectURL  string
	Scopes       string
	HTTPTimeout  time.Duration
	HTTPClient   *http.Client
}

// NewGoogleWorkspaceProvider returns an OIDCProvider configured for Google Identity.
// Returns ErrOIDCMisconfigured if ClientID is empty.
func NewGoogleWorkspaceProvider(cfg GoogleWorkspaceProviderConfig) (OIDCProvider, error) {
	if strings.TrimSpace(cfg.ClientID) == "" {
		return nil, domain.ErrOIDCMisconfigured
	}
	scopes := cfg.Scopes
	if scopes == "" {
		scopes = "openid email profile"
	}
	return NewKeycloakProvider(KeycloakProviderConfig{
		IssuerURL:    "https://accounts.google.com",
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		RedirectURL:  cfg.RedirectURL,
		Scopes:       scopes,
		HTTPTimeout:  cfg.HTTPTimeout,
		HTTPClient:   cfg.HTTPClient,
	}), nil
}
