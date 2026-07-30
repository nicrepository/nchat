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

	// Invite creation budget is conservative on purpose: onboarding
	// is a human-paced action, so ten invites per admin per ten minutes covers
	// legitimate bulk onboarding while bounding how fast a stolen admin session
	// can spray invitations. The IP ceiling is the complementary control and is
	// counted per hour.
	defaultInviteRateLimitPerActor      = 10
	defaultInviteRateLimitWindowMinutes = 10
	defaultInviteRateLimitPerIPPerHour  = 30

	// Ranges an operator may choose from. Outside these the default applies —
	// see boundedInt for why the value is rejected rather than clamped.
	minInviteRateLimitPerActor      = 1
	maxInviteRateLimitPerActor      = 500
	minInviteRateLimitWindowMinutes = 1
	maxInviteRateLimitWindowMinutes = 1440
	minInviteRateLimitPerIPPerHour  = 1
	maxInviteRateLimitPerIPPerHour  = 5000

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
	AuthInviteRateLimitPerActor         int
	AuthInviteRateLimitWindowMinutes    int
	AuthInviteRateLimitPerIPPerHour     int
	// AuthBootstrapWorkspaceID is the workspace POST /admin/invites issues into.
	// Empty (the default) disables that endpoint entirely, which is why it is
	// not defaulted to the seeded workspace: enabling a pre-shared-credential
	// route must be an explicit operator decision, not something a deployment
	// inherits by accident.
	AuthBootstrapWorkspaceID string
	OIDCEnabled              bool
	OIDCProviderName         string
	OIDCIssuerURL            string
	OIDCClientID             string
	OIDCClientSecret         string
	OIDCRedirectURL          string
	OIDCFrontendCallbackURL  string
	OIDCScopes               string
	OIDCHTTPTimeoutSeconds   int
	OIDCStateTTLMinutes      int
	OIDCAutoProvisionEnabled bool
	OIDCAllowedEmailDomains  string
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
		AuthInviteRateLimitPerActor: boundedInt("AUTH_INVITE_RATE_LIMIT_PER_ACTOR",
			defaultInviteRateLimitPerActor, minInviteRateLimitPerActor, maxInviteRateLimitPerActor),
		AuthInviteRateLimitWindowMinutes: boundedInt("AUTH_INVITE_RATE_LIMIT_WINDOW_MINUTES",
			defaultInviteRateLimitWindowMinutes, minInviteRateLimitWindowMinutes, maxInviteRateLimitWindowMinutes),
		AuthInviteRateLimitPerIPPerHour: boundedInt("AUTH_INVITE_RATE_LIMIT_PER_IP_PER_HOUR",
			defaultInviteRateLimitPerIPPerHour, minInviteRateLimitPerIPPerHour, maxInviteRateLimitPerIPPerHour),
		AuthBootstrapWorkspaceID: strings.TrimSpace(platformconfig.GetString("AUTH_BOOTSTRAP_WORKSPACE_ID", "")),
		OIDCEnabled:              platformconfig.GetBool("OIDC_ENABLED", false),
		OIDCProviderName:         strings.TrimSpace(platformconfig.GetString("OIDC_PROVIDER_NAME", defaultOIDCProviderName)),
		OIDCIssuerURL:            strings.TrimRight(strings.TrimSpace(platformconfig.GetString("OIDC_ISSUER_URL", "")), "/"),
		OIDCClientID:             strings.TrimSpace(platformconfig.GetString("OIDC_CLIENT_ID", "")),
		OIDCClientSecret:         platformconfig.GetString("OIDC_CLIENT_SECRET", ""),
		OIDCRedirectURL:          strings.TrimSpace(platformconfig.GetString("OIDC_REDIRECT_URL", "")),
		OIDCFrontendCallbackURL:  strings.TrimSpace(platformconfig.GetString("OIDC_FRONTEND_CALLBACK_URL", "")),
		OIDCScopes:               strings.TrimSpace(platformconfig.GetString("OIDC_SCOPES", defaultOIDCScopes)),
		OIDCHTTPTimeoutSeconds:   positiveInt("OIDC_HTTP_TIMEOUT_SECONDS", defaultOIDCHTTPTimeoutSeconds),
		OIDCStateTTLMinutes:      positiveInt("OIDC_STATE_TTL_MINUTES", defaultOIDCStateTTLMinutes),
		OIDCAutoProvisionEnabled: platformconfig.GetBool("OIDC_AUTO_PROVISION_ENABLED", defaultOIDCAutoProvisionEnable),
		OIDCAllowedEmailDomains:  strings.TrimSpace(platformconfig.GetString("OIDC_ALLOWED_EMAIL_DOMAINS", "")),
		AuthTrustedProxyCIDRs:    platformconfig.GetString("AUTH_TRUSTED_PROXY_CIDRS", ""),
		AuthAvatarDir:            platformconfig.GetString("AUTH_AVATAR_DIR", ""),
		AuthAvatarBaseURL:        platformconfig.GetString("AUTH_AVATAR_BASE_URL", defaultAvatarBaseURL),
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
	return nil
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

// boundedInt reads an operator-supplied limit and keeps it inside a range the
// service can actually honour. A value outside the range falls back to the
// default rather than being clamped: silently accepting "1000000" as the
// maximum would let a typo disable a control that exists to bound abuse, and
// silently clamping it would hide the mistake from whoever set it.
func boundedInt(key string, fallback, minValue, maxValue int) int {
	value := platformconfig.GetInt(key, fallback)
	if value < minValue || value > maxValue {
		return fallback
	}
	return value
}
