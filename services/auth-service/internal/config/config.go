package config

import (
	"net/url"
	"strings"

	platformconfig "github.com/nicrepository/nchat/libs/go/platform/config"
	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
)

const (
	serviceName = "auth-service"
	defaultPort = 8081

	defaultJWTIssuer                       = "nchat-auth"
	defaultJWTAudience                     = "nchat-api"
	defaultAccessTokenTTLSeconds           = 900
	defaultRefreshTokenTTLSeconds          = 2592000
	defaultTokenEndpointRateLimitPerMinute = 60
	defaultTokenEndpointRateLimitBurst     = 10

	defaultOIDCProviderName        = "keycloak"
	defaultOIDCScopes              = "openid email profile"
	defaultOIDCHTTPTimeoutSeconds  = 10
	defaultOIDCStateTTLMinutes     = 10
	defaultOIDCAutoProvisionEnable = false
)

type Config struct {
	ServiceName                         string
	Env                                 string
	Port                                int
	ReadHeaderTimeoutSeconds            int
	DatabaseURL                         string
	DBConnectTimeoutSeconds             int
	AdminBootstrapToken                 string
	AuthJWTHMACSecret                   string
	AuthEmailOutboxEncryptionKey        string
	AuthJWTIssuer                       string
	AuthJWTAudience                     string
	AuthAccessTokenTTLSeconds           int
	AuthRefreshTokenTTLSeconds          int
	AuthTokenEndpointRateLimitPerMinute int
	AuthTokenEndpointRateLimitBurst     int
	OIDCEnabled                         bool
	OIDCProviderName                    string
	OIDCIssuerURL                       string
	OIDCClientID                        string
	OIDCClientSecret                    string
	OIDCRedirectURL                     string
	OIDCFrontendCallbackURL             string
	OIDCScopes                          string
	OIDCHTTPTimeoutSeconds              int
	OIDCStateTTLMinutes                 int
	OIDCAutoProvisionEnabled            bool
	OIDCAllowedEmailDomains             string
	// AuthTrustedProxyCIDRs is a comma-separated list of CIDRs (e.g. "10.0.0.0/8,172.16.0.0/12")
	// whose X-Forwarded-For header is trusted for client-IP extraction by the rate limiter.
	// Leave empty (default) to always use RemoteAddr — safe for direct or single-instance deployments.
	// In production behind Traefik, set this to the Traefik ingress CIDR.
	AuthTrustedProxyCIDRs string
}

func Load() Config {
	return Config{
		ServiceName:                         serviceName,
		Env:                                 platformconfig.GetString("APP_ENV", "development"),
		Port:                                platformconfig.GetInt("PORT", defaultPort),
		ReadHeaderTimeoutSeconds:            platformconfig.GetInt("READ_HEADER_TIMEOUT_SECONDS", 5),
		DatabaseURL:                         platformconfig.GetString("DATABASE_URL", ""),
		DBConnectTimeoutSeconds:             platformconfig.GetInt("DB_CONNECT_TIMEOUT_SECONDS", 5),
		AdminBootstrapToken:                 platformconfig.GetString("ADMIN_BOOTSTRAP_TOKEN", ""),
		AuthJWTHMACSecret:                   platformconfig.GetString("AUTH_JWT_HMAC_SECRET", ""),
		AuthEmailOutboxEncryptionKey:        platformconfig.GetString("AUTH_EMAIL_OUTBOX_ENCRYPTION_KEY", ""),
		AuthJWTIssuer:                       platformconfig.GetString("AUTH_JWT_ISSUER", defaultJWTIssuer),
		AuthJWTAudience:                     platformconfig.GetString("AUTH_JWT_AUDIENCE", defaultJWTAudience),
		AuthAccessTokenTTLSeconds:           positiveInt("AUTH_ACCESS_TOKEN_TTL_SECONDS", defaultAccessTokenTTLSeconds),
		AuthRefreshTokenTTLSeconds:          positiveInt("AUTH_REFRESH_TOKEN_TTL_SECONDS", defaultRefreshTokenTTLSeconds),
		AuthTokenEndpointRateLimitPerMinute: positiveInt("AUTH_TOKEN_ENDPOINT_RATE_LIMIT_PER_MINUTE", defaultTokenEndpointRateLimitPerMinute),
		AuthTokenEndpointRateLimitBurst:     positiveInt("AUTH_TOKEN_ENDPOINT_RATE_LIMIT_BURST", defaultTokenEndpointRateLimitBurst),
		OIDCEnabled:                         platformconfig.GetBool("OIDC_ENABLED", false),
		OIDCProviderName:                    strings.TrimSpace(platformconfig.GetString("OIDC_PROVIDER_NAME", defaultOIDCProviderName)),
		OIDCIssuerURL:                       strings.TrimRight(strings.TrimSpace(platformconfig.GetString("OIDC_ISSUER_URL", "")), "/"),
		OIDCClientID:                        strings.TrimSpace(platformconfig.GetString("OIDC_CLIENT_ID", "")),
		OIDCClientSecret:                    platformconfig.GetString("OIDC_CLIENT_SECRET", ""),
		OIDCRedirectURL:                     strings.TrimSpace(platformconfig.GetString("OIDC_REDIRECT_URL", "")),
		OIDCFrontendCallbackURL:             strings.TrimSpace(platformconfig.GetString("OIDC_FRONTEND_CALLBACK_URL", "")),
		OIDCScopes:                          strings.TrimSpace(platformconfig.GetString("OIDC_SCOPES", defaultOIDCScopes)),
		OIDCHTTPTimeoutSeconds:              positiveInt("OIDC_HTTP_TIMEOUT_SECONDS", defaultOIDCHTTPTimeoutSeconds),
		OIDCStateTTLMinutes:                 positiveInt("OIDC_STATE_TTL_MINUTES", defaultOIDCStateTTLMinutes),
		OIDCAutoProvisionEnabled:            platformconfig.GetBool("OIDC_AUTO_PROVISION_ENABLED", defaultOIDCAutoProvisionEnable),
		OIDCAllowedEmailDomains:             strings.TrimSpace(platformconfig.GetString("OIDC_ALLOWED_EMAIL_DOMAINS", "")),
		AuthTrustedProxyCIDRs:               platformconfig.GetString("AUTH_TRUSTED_PROXY_CIDRS", ""),
	}
}

func (c Config) ValidateOIDC() error {
	if !c.OIDCEnabled {
		return nil
	}
	slug := domain.IdentityProviderSlug(strings.ToLower(strings.TrimSpace(c.OIDCProviderName)))
	if !domain.IsOIDCSlug(slug) {
		return domain.ErrOIDCMisconfigured
	}
	if strings.TrimSpace(c.OIDCIssuerURL) == "" || strings.TrimSpace(c.OIDCClientID) == "" || strings.TrimSpace(c.OIDCClientSecret) == "" || strings.TrimSpace(c.OIDCRedirectURL) == "" || strings.TrimSpace(c.OIDCFrontendCallbackURL) == "" {
		return domain.ErrOIDCMisconfigured
	}
	if !validOIDCFrontendCallbackPath(c.OIDCFrontendCallbackURL) {
		return domain.ErrOIDCMisconfigured
	}
	return nil
}

func validOIDCFrontendCallbackPath(callbackPath string) bool {
	if strings.ContainsAny(callbackPath, "\r\n") {
		return false
	}
	if !strings.HasPrefix(callbackPath, "/") || strings.HasPrefix(callbackPath, "//") {
		return false
	}
	parsed, err := url.Parse(callbackPath)
	if err != nil {
		return false
	}
	return parsed.Scheme == "" && parsed.Host == "" && parsed.User == nil && parsed.Fragment == "" && parsed.RawQuery == "" && parsed.Path == domain.OIDCFrontendCallbackPath
}

func positiveInt(key string, fallback int) int {
	value := platformconfig.GetInt(key, fallback)
	if value <= 0 {
		return fallback
	}
	return value
}
