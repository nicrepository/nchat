package config

import (
	"net"
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
	defaultAvatarBaseURL           = "/api/auth/avatars"
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
	// OIDCAdminRedirectURL is the provider callback URI for the administrative
	// console, which is served from its own origin (issue #578). Empty means
	// single sign-on is simply unavailable on that host; it never falls back to
	// the chat URI, which would land an administrator on the wrong origin.
	OIDCAdminRedirectURL     string
	OIDCFrontendCallbackURL  string
	OIDCScopes               string
	OIDCHTTPTimeoutSeconds   int
	OIDCStateTTLMinutes      int
	OIDCAutoProvisionEnabled bool
	OIDCAllowedEmailDomains  string
	// OIDCAdminACRValues is the comma-separated list of `acr` values this
	// deployment accepts as evidence that the administrative authentication
	// context ran — in practice, the value its Keycloak authentication flow
	// emits for a login that required a second factor.
	//
	// Empty states no requirement. Non-empty makes the requirement real and
	// fail-closed: the administrative login asks the provider for the context
	// and the callback refuses a token that does not come back with one of
	// these values. Local development normally leaves it empty.
	OIDCAdminACRValues string
	// AuthTrustedProxyCIDRs is a comma-separated list of CIDRs (e.g. "10.0.0.0/8,172.16.0.0/12")
	// whose X-Forwarded-For header is trusted for client-IP extraction by the rate limiter.
	// Leave empty (default) to always use RemoteAddr — safe for direct or single-instance deployments.
	// In production behind Traefik, set this to the Traefik ingress CIDR.
	AuthTrustedProxyCIDRs string

	// AuthAvatarDir is the directory where processed avatar images are written.
	// It must sit on a persistent volume in production (see the deployment
	// manifest). Empty disables the avatar endpoints.
	AuthAvatarDir string
	// AuthAvatarBaseURL is the same-origin, root-relative prefix under which
	// avatars are served and persisted into auth.users.avatar_url.
	AuthAvatarBaseURL string
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
		OIDCAdminRedirectURL:                strings.TrimSpace(platformconfig.GetString("OIDC_ADMIN_REDIRECT_URL", "")),
		OIDCFrontendCallbackURL:             strings.TrimSpace(platformconfig.GetString("OIDC_FRONTEND_CALLBACK_URL", "")),
		OIDCScopes:                          strings.TrimSpace(platformconfig.GetString("OIDC_SCOPES", defaultOIDCScopes)),
		OIDCHTTPTimeoutSeconds:              positiveInt("OIDC_HTTP_TIMEOUT_SECONDS", defaultOIDCHTTPTimeoutSeconds),
		OIDCStateTTLMinutes:                 positiveInt("OIDC_STATE_TTL_MINUTES", defaultOIDCStateTTLMinutes),
		OIDCAutoProvisionEnabled:            platformconfig.GetBool("OIDC_AUTO_PROVISION_ENABLED", defaultOIDCAutoProvisionEnable),
		OIDCAllowedEmailDomains:             strings.TrimSpace(platformconfig.GetString("OIDC_ALLOWED_EMAIL_DOMAINS", "")),
		OIDCAdminACRValues:                  strings.TrimSpace(platformconfig.GetString("OIDC_ADMIN_ACR_VALUES", "")),
		AuthTrustedProxyCIDRs:               platformconfig.GetString("AUTH_TRUSTED_PROXY_CIDRS", ""),
		AuthAvatarDir:                       platformconfig.GetString("AUTH_AVATAR_DIR", ""),
		AuthAvatarBaseURL:                   platformconfig.GetString("AUTH_AVATAR_BASE_URL", defaultAvatarBaseURL),
	}
}

func (c Config) ValidateOIDC() error {
	if !c.OIDCEnabled {
		return nil
	}
	slug := domain.IdentityProviderSlug(c.NormalizedOIDCProviderName())
	if !domain.IsOIDCSlug(slug) {
		return domain.ErrOIDCMisconfigured
	}
	if strings.TrimSpace(c.OIDCIssuerURL) == "" || strings.TrimSpace(c.OIDCClientID) == "" || strings.TrimSpace(c.OIDCClientSecret) == "" || strings.TrimSpace(c.OIDCRedirectURL) == "" || strings.TrimSpace(c.OIDCFrontendCallbackURL) == "" {
		return domain.ErrOIDCMisconfigured
	}
	if !validOIDCFrontendCallbackPath(c.OIDCFrontendCallbackURL) {
		return domain.ErrOIDCMisconfigured
	}
	// The administrative redirect is optional, but a value that is present must
	// be usable: a deployment that sets a malformed one should fail at startup,
	// not at an administrator's first sign-in attempt.
	if c.OIDCAdminRedirectURL != "" && !validOIDCProviderRedirectURL(c.OIDCAdminRedirectURL) {
		return domain.ErrOIDCMisconfigured
	}
	return nil
}

// validOIDCProviderRedirectURL accepts an absolute callback URI. HTTPS is
// required except on loopback, so local development works over plain HTTP while
// no deployed environment can.
func validOIDCProviderRedirectURL(raw string) bool {
	if strings.ContainsAny(raw, "\r\n") {
		return false
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return false
	}
	if parsed.Scheme == "https" {
		return true
	}
	return parsed.Scheme == "http" && isLoopbackHost(parsed.Hostname())
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (c Config) NormalizedOIDCProviderName() string {
	return strings.ToLower(strings.TrimSpace(c.OIDCProviderName))
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
