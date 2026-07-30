package config

import (
	"errors"
	"testing"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
)

func TestLoadDefaults(t *testing.T) {
	cfg := Load()

	if cfg.ServiceName != "auth-service" {
		t.Fatalf("expected auth-service, got %q", cfg.ServiceName)
	}
	if cfg.Env != "development" {
		t.Fatalf("expected development env, got %q", cfg.Env)
	}
	if cfg.Port != 8081 {
		t.Fatalf("expected port 8081, got %d", cfg.Port)
	}
	if cfg.ReadHeaderTimeoutSeconds != 5 {
		t.Fatalf("expected timeout 5, got %d", cfg.ReadHeaderTimeoutSeconds)
	}
}

func TestLoadUsesEnvironmentOverrides(t *testing.T) {
	t.Setenv("APP_ENV", "test")
	t.Setenv("PORT", "18081")

	cfg := Load()

	if cfg.Env != "test" {
		t.Fatalf("expected test env, got %q", cfg.Env)
	}
	if cfg.Port != 18081 {
		t.Fatalf("expected port 18081, got %d", cfg.Port)
	}
}

func TestLoadDatabaseURLDefault(t *testing.T) {
	cfg := Load()
	if cfg.DatabaseURL != "" {
		t.Fatalf("expected empty DatabaseURL, got %q", cfg.DatabaseURL)
	}
}

func TestLoadDatabaseURLFromEnv(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://nchat:pass@localhost/nchat")
	cfg := Load()
	if cfg.DatabaseURL != "postgres://nchat:pass@localhost/nchat" { //nolint:gosec
		t.Fatalf("unexpected DatabaseURL %q", cfg.DatabaseURL)
	}
}

func TestLoadDBConnectTimeoutDefault(t *testing.T) {
	cfg := Load()
	if cfg.DBConnectTimeoutSeconds != 5 {
		t.Fatalf("expected 5, got %d", cfg.DBConnectTimeoutSeconds)
	}
}

func TestLoadAdminBootstrapTokenDefault(t *testing.T) {
	cfg := Load()
	if cfg.AdminBootstrapToken != "" {
		t.Fatalf("expected empty AdminBootstrapToken, got %q", cfg.AdminBootstrapToken)
	}
}

func TestLoadAdminBootstrapTokenFromEnv(t *testing.T) {
	t.Setenv("ADMIN_BOOTSTRAP_TOKEN", "super-secret")
	cfg := Load()
	if cfg.AdminBootstrapToken != "super-secret" {
		t.Fatalf("unexpected AdminBootstrapToken %q", cfg.AdminBootstrapToken)
	}
}

func TestLoadAuthJWTDefaults(t *testing.T) {
	cfg := Load()

	if cfg.AuthJWTHMACSecret != "" {
		t.Fatalf("expected empty AuthJWTHMACSecret, got %q", cfg.AuthJWTHMACSecret)
	}
	if cfg.AuthJWTIssuer != "nchat-auth" {
		t.Fatalf("expected nchat-auth issuer, got %q", cfg.AuthJWTIssuer)
	}
	if cfg.AuthJWTAudience != "nchat-api" {
		t.Fatalf("expected nchat-api audience, got %q", cfg.AuthJWTAudience)
	}
	if cfg.AuthAccessTokenTTLSeconds != 900 {
		t.Fatalf("expected access ttl 900, got %d", cfg.AuthAccessTokenTTLSeconds)
	}
	if cfg.AuthRefreshTokenTTLSeconds != 2592000 {
		t.Fatalf("expected refresh ttl 2592000, got %d", cfg.AuthRefreshTokenTTLSeconds)
	}
}

func TestLoadAuthJWTOverrides(t *testing.T) {
	t.Setenv("AUTH_JWT_HMAC_SECRET", "abcdefghijklmnopqrstuvwxyz123456")
	t.Setenv("AUTH_JWT_ISSUER", "issuer")
	t.Setenv("AUTH_JWT_AUDIENCE", "audience")
	t.Setenv("AUTH_ACCESS_TOKEN_TTL_SECONDS", "60")
	t.Setenv("AUTH_REFRESH_TOKEN_TTL_SECONDS", "120")

	cfg := Load()

	if cfg.AuthJWTHMACSecret != "abcdefghijklmnopqrstuvwxyz123456" {
		t.Fatal("expected AuthJWTHMACSecret from environment")
	}
	if cfg.AuthJWTIssuer != "issuer" {
		t.Fatalf("expected issuer override, got %q", cfg.AuthJWTIssuer)
	}
	if cfg.AuthJWTAudience != "audience" {
		t.Fatalf("expected audience override, got %q", cfg.AuthJWTAudience)
	}
	if cfg.AuthAccessTokenTTLSeconds != 60 {
		t.Fatalf("expected access ttl 60, got %d", cfg.AuthAccessTokenTTLSeconds)
	}
	if cfg.AuthRefreshTokenTTLSeconds != 120 {
		t.Fatalf("expected refresh ttl 120, got %d", cfg.AuthRefreshTokenTTLSeconds)
	}
}

func TestLoadAuthJWTInvalidTTLUsesDefault(t *testing.T) {
	t.Setenv("AUTH_ACCESS_TOKEN_TTL_SECONDS", "0")
	t.Setenv("AUTH_REFRESH_TOKEN_TTL_SECONDS", "-1")

	cfg := Load()

	if cfg.AuthAccessTokenTTLSeconds != 900 {
		t.Fatalf("expected default access ttl 900, got %d", cfg.AuthAccessTokenTTLSeconds)
	}
	if cfg.AuthRefreshTokenTTLSeconds != 2592000 {
		t.Fatalf("expected default refresh ttl 2592000, got %d", cfg.AuthRefreshTokenTTLSeconds)
	}
}

func TestLoadEmailOutboxEncryptionKey(t *testing.T) {
	cfg := Load()
	if cfg.AuthEmailOutboxEncryptionKey != "" {
		t.Fatalf("expected empty AuthEmailOutboxEncryptionKey, got %q", cfg.AuthEmailOutboxEncryptionKey)
	}

	t.Setenv("AUTH_EMAIL_OUTBOX_ENCRYPTION_KEY", "base64-key")
	cfg = Load()
	if cfg.AuthEmailOutboxEncryptionKey != "base64-key" {
		t.Fatalf("expected email outbox key from environment, got %q", cfg.AuthEmailOutboxEncryptionKey)
	}
}

func TestLoadAuthTokenEndpointRateLimitDefaults(t *testing.T) {
	cfg := Load()

	if cfg.AuthTokenEndpointRateLimitPerMinute != 60 {
		t.Fatalf("expected token endpoint rate limit 60, got %d", cfg.AuthTokenEndpointRateLimitPerMinute)
	}
	if cfg.AuthTokenEndpointRateLimitBurst != 10 {
		t.Fatalf("expected token endpoint burst 10, got %d", cfg.AuthTokenEndpointRateLimitBurst)
	}
}

func TestLoadAuthTokenEndpointRateLimitOverrides(t *testing.T) {
	t.Setenv("AUTH_TOKEN_ENDPOINT_RATE_LIMIT_PER_MINUTE", "5")
	t.Setenv("AUTH_TOKEN_ENDPOINT_RATE_LIMIT_BURST", "2")

	cfg := Load()

	if cfg.AuthTokenEndpointRateLimitPerMinute != 5 {
		t.Fatalf("expected token endpoint rate limit 5, got %d", cfg.AuthTokenEndpointRateLimitPerMinute)
	}
	if cfg.AuthTokenEndpointRateLimitBurst != 2 {
		t.Fatalf("expected token endpoint burst 2, got %d", cfg.AuthTokenEndpointRateLimitBurst)
	}
}

func TestLoadAuthTokenEndpointRateLimitInvalidUsesDefault(t *testing.T) {
	t.Setenv("AUTH_TOKEN_ENDPOINT_RATE_LIMIT_PER_MINUTE", "0")
	t.Setenv("AUTH_TOKEN_ENDPOINT_RATE_LIMIT_BURST", "-1")

	cfg := Load()

	if cfg.AuthTokenEndpointRateLimitPerMinute != 60 {
		t.Fatalf("expected default token endpoint rate limit 60, got %d", cfg.AuthTokenEndpointRateLimitPerMinute)
	}
	if cfg.AuthTokenEndpointRateLimitBurst != 10 {
		t.Fatalf("expected default token endpoint burst 10, got %d", cfg.AuthTokenEndpointRateLimitBurst)
	}
}

func TestLoadOIDCDefaults(t *testing.T) {
	cfg := Load()

	if cfg.OIDCEnabled {
		t.Fatal("expected OIDC disabled by default")
	}
	if cfg.OIDCProviderName != "keycloak" {
		t.Fatalf("expected keycloak provider, got %q", cfg.OIDCProviderName)
	}
	if cfg.OIDCScopes != "openid email profile" {
		t.Fatalf("expected default scopes, got %q", cfg.OIDCScopes)
	}
	if cfg.OIDCHTTPTimeoutSeconds != 10 {
		t.Fatalf("expected HTTP timeout 10, got %d", cfg.OIDCHTTPTimeoutSeconds)
	}
	if cfg.OIDCStateTTLMinutes != 10 {
		t.Fatalf("expected state ttl 10, got %d", cfg.OIDCStateTTLMinutes)
	}
	if cfg.OIDCAutoProvisionEnabled {
		t.Fatal("expected auto-provision disabled by default")
	}
	if cfg.OIDCAllowedEmailDomains != "" {
		t.Fatalf("expected empty domain allowlist, got %q", cfg.OIDCAllowedEmailDomains)
	}
	if err := cfg.ValidateOIDC(); err != nil {
		t.Fatalf("disabled OIDC config should validate, got %v", err)
	}
}

func TestLoadOIDCOverrides(t *testing.T) {
	t.Setenv("OIDC_ENABLED", "true")
	t.Setenv("OIDC_PROVIDER_NAME", "keycloak")
	t.Setenv("OIDC_ISSUER_URL", "https://keycloak.example.com/realms/nchat")
	t.Setenv("OIDC_CLIENT_ID", "nchat-web")
	t.Setenv("OIDC_CLIENT_SECRET", "client-secret")
	t.Setenv("OIDC_REDIRECT_URL", "https://auth.example.com/auth/oidc/keycloak/callback")
	t.Setenv("OIDC_FRONTEND_CALLBACK_URL", "/oidc-callback")
	t.Setenv("OIDC_SCOPES", "openid email")
	t.Setenv("OIDC_HTTP_TIMEOUT_SECONDS", "3")
	t.Setenv("OIDC_STATE_TTL_MINUTES", "7")
	t.Setenv("OIDC_AUTO_PROVISION_ENABLED", "false")
	t.Setenv("OIDC_ALLOWED_EMAIL_DOMAINS", "nic.br,example.com")

	cfg := Load()

	if !cfg.OIDCEnabled {
		t.Fatal("expected OIDC enabled")
	}
	if cfg.OIDCIssuerURL != "https://keycloak.example.com/realms/nchat" {
		t.Fatalf("unexpected issuer %q", cfg.OIDCIssuerURL)
	}
	if cfg.OIDCClientID != "nchat-web" {
		t.Fatalf("unexpected client id %q", cfg.OIDCClientID)
	}
	if cfg.OIDCClientSecret != "client-secret" {
		t.Fatalf("unexpected client secret %q", cfg.OIDCClientSecret)
	}
	if cfg.OIDCRedirectURL != "https://auth.example.com/auth/oidc/keycloak/callback" {
		t.Fatalf("unexpected redirect url %q", cfg.OIDCRedirectURL)
	}
	if cfg.OIDCFrontendCallbackURL != "/oidc-callback" {
		t.Fatalf("unexpected frontend callback url %q", cfg.OIDCFrontendCallbackURL)
	}
	if cfg.OIDCScopes != "openid email" {
		t.Fatalf("unexpected scopes %q", cfg.OIDCScopes)
	}
	if cfg.OIDCHTTPTimeoutSeconds != 3 {
		t.Fatalf("unexpected HTTP timeout %d", cfg.OIDCHTTPTimeoutSeconds)
	}
	if cfg.OIDCStateTTLMinutes != 7 {
		t.Fatalf("unexpected state ttl %d", cfg.OIDCStateTTLMinutes)
	}
	if cfg.OIDCAutoProvisionEnabled {
		t.Fatal("expected auto-provision disabled")
	}
	if cfg.OIDCAllowedEmailDomains != "nic.br,example.com" {
		t.Fatalf("unexpected domain allowlist %q", cfg.OIDCAllowedEmailDomains)
	}
	if err := cfg.ValidateOIDC(); err != nil {
		t.Fatalf("enabled OIDC config should validate, got %v", err)
	}
}

func TestLoadOIDCInvalidPositiveIntsUseDefaults(t *testing.T) {
	t.Setenv("OIDC_HTTP_TIMEOUT_SECONDS", "0")
	t.Setenv("OIDC_STATE_TTL_MINUTES", "-5")

	cfg := Load()

	if cfg.OIDCHTTPTimeoutSeconds != 10 {
		t.Fatalf("expected default HTTP timeout 10, got %d", cfg.OIDCHTTPTimeoutSeconds)
	}
	if cfg.OIDCStateTTLMinutes != 10 {
		t.Fatalf("expected default state ttl 10, got %d", cfg.OIDCStateTTLMinutes)
	}
}

func TestValidateOIDCEnabledMissingRequiredConfigFailsClosed(t *testing.T) {
	cfg := Load()
	cfg.OIDCEnabled = true

	err := cfg.ValidateOIDC()

	if !errors.Is(err, domain.ErrOIDCMisconfigured) {
		t.Fatalf("expected ErrOIDCMisconfigured, got %v", err)
	}
}

func TestNormalizedOIDCProviderNameTrimsAndLowercases(t *testing.T) {
	cfg := Config{OIDCProviderName: " Keycloak "}

	if got := cfg.NormalizedOIDCProviderName(); got != "keycloak" {
		t.Fatalf("expected keycloak, got %q", got)
	}
}

func TestValidateOIDCAcceptsKnownOIDCSlugsAndRejectsUnknown(t *testing.T) {
	base := Load()
	base.OIDCEnabled = true
	base.OIDCIssuerURL = "https://issuer.example.com"
	base.OIDCClientID = "client"
	base.OIDCClientSecret = "secret"
	base.OIDCRedirectURL = "https://auth.example.com/callback"
	base.OIDCFrontendCallbackURL = "/oidc-callback"

	for _, slug := range []string{"keycloak", "azure_ad", "google_workspace"} {
		cfg := base
		cfg.OIDCProviderName = slug
		if err := cfg.ValidateOIDC(); err != nil {
			t.Errorf("slug %q: expected no error, got %v", slug, err)
		}
	}

	for _, slug := range []string{"google", "okta", "samba_ad", ""} {
		cfg := base
		cfg.OIDCProviderName = slug
		if err := cfg.ValidateOIDC(); !errors.Is(err, domain.ErrOIDCMisconfigured) {
			t.Errorf("slug %q: expected ErrOIDCMisconfigured, got %v", slug, err)
		}
	}
}

func TestValidateOIDCRequiresRelativeFrontendCallbackPath(t *testing.T) {
	base := Load()
	base.OIDCEnabled = true
	base.OIDCProviderName = "keycloak"
	base.OIDCIssuerURL = "https://issuer.example.com"
	base.OIDCClientID = "client"
	base.OIDCClientSecret = "secret"
	base.OIDCRedirectURL = "https://auth.example.com/callback"
	base.OIDCFrontendCallbackURL = "/oidc-callback"
	if err := base.ValidateOIDC(); err != nil {
		t.Fatalf("expected relative callback path to validate, got %v", err)
	}

	for _, callbackURL := range []string{
		"https://nchat.example.com/oidc-callback",
		"//evil.example.com/oidc-callback",
		"oidc-callback",
		"/oidc-callback\n",
		"/oidc-callback%",
		"/oidc-callback?next=/dashboard",
		"/oidc-callback#fragment",
		"/unexpected-callback",
	} {
		t.Run(callbackURL, func(t *testing.T) {
			cfg := base
			cfg.OIDCFrontendCallbackURL = callbackURL

			err := cfg.ValidateOIDC()
			if !errors.Is(err, domain.ErrOIDCMisconfigured) {
				t.Fatalf("expected ErrOIDCMisconfigured, got %v", err)
			}
		})
	}
}

// ── Invite rate limit bounds ──────────────────────────────────

func TestLoad_InviteRateLimitDefaults(t *testing.T) {
	cfg := Load()

	if cfg.AuthInviteRateLimitPerActor != 10 {
		t.Fatalf("expected 10 invites per actor, got %d", cfg.AuthInviteRateLimitPerActor)
	}
	if cfg.AuthInviteRateLimitWindowMinutes != 10 {
		t.Fatalf("expected a 10 minute window, got %d", cfg.AuthInviteRateLimitWindowMinutes)
	}
	if cfg.AuthInviteRateLimitPerIPPerHour != 30 {
		t.Fatalf("expected 30 invites per IP per hour, got %d", cfg.AuthInviteRateLimitPerIPPerHour)
	}
}

func TestLoad_InviteRateLimitAcceptsValuesInRange(t *testing.T) {
	t.Setenv("AUTH_INVITE_RATE_LIMIT_PER_ACTOR", "25")
	t.Setenv("AUTH_INVITE_RATE_LIMIT_WINDOW_MINUTES", "60")
	t.Setenv("AUTH_INVITE_RATE_LIMIT_PER_IP_PER_HOUR", "100")

	cfg := Load()

	if cfg.AuthInviteRateLimitPerActor != 25 || cfg.AuthInviteRateLimitWindowMinutes != 60 || cfg.AuthInviteRateLimitPerIPPerHour != 100 {
		t.Fatalf("expected the configured limits, got %+v", cfg)
	}
}

// Out-of-range values fall back to the default. Accepting them would let a
// typo disable a control; clamping would hide the mistake.
func TestLoad_InviteRateLimitRejectsOutOfRangeValues(t *testing.T) {
	for _, tc := range []struct {
		name  string
		key   string
		value string
		want  func(Config) int
		def   int
	}{
		{"actor zero", "AUTH_INVITE_RATE_LIMIT_PER_ACTOR", "0", func(c Config) int { return c.AuthInviteRateLimitPerActor }, 10},
		{"actor negative", "AUTH_INVITE_RATE_LIMIT_PER_ACTOR", "-5", func(c Config) int { return c.AuthInviteRateLimitPerActor }, 10},
		{"actor above max", "AUTH_INVITE_RATE_LIMIT_PER_ACTOR", "100000", func(c Config) int { return c.AuthInviteRateLimitPerActor }, 10},
		{"window zero", "AUTH_INVITE_RATE_LIMIT_WINDOW_MINUTES", "0", func(c Config) int { return c.AuthInviteRateLimitWindowMinutes }, 10},
		{"window above max", "AUTH_INVITE_RATE_LIMIT_WINDOW_MINUTES", "5000", func(c Config) int { return c.AuthInviteRateLimitWindowMinutes }, 10},
		{"ip zero", "AUTH_INVITE_RATE_LIMIT_PER_IP_PER_HOUR", "0", func(c Config) int { return c.AuthInviteRateLimitPerIPPerHour }, 30},
		{"ip above max", "AUTH_INVITE_RATE_LIMIT_PER_IP_PER_HOUR", "999999", func(c Config) int { return c.AuthInviteRateLimitPerIPPerHour }, 30},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(tc.key, tc.value)
			if got := tc.want(Load()); got != tc.def {
				t.Fatalf("expected fallback %d, got %d", tc.def, got)
			}
		})
	}
}

// ── Bootstrap workspace ───────────────────────────────────────

// Empty by default: enabling a route guarded by a pre-shared credential must be
// an explicit operator decision, not something a deployment inherits.
func TestLoad_BootstrapWorkspaceDisabledByDefault(t *testing.T) {
	if got := Load().AuthBootstrapWorkspaceID; got != "" {
		t.Fatalf("expected the bootstrap workspace to be unset by default, got %q", got)
	}
}

func TestLoad_BootstrapWorkspaceIsTrimmed(t *testing.T) {
	t.Setenv("AUTH_BOOTSTRAP_WORKSPACE_ID", "  00000000-0000-0000-0000-000000000001  ")

	if got := Load().AuthBootstrapWorkspaceID; got != "00000000-0000-0000-0000-000000000001" {
		t.Fatalf("expected the trimmed workspace id, got %q", got)
	}
}

// Whitespace-only is not a configured workspace.
func TestLoad_BootstrapWorkspaceBlankStaysDisabled(t *testing.T) {
	t.Setenv("AUTH_BOOTSTRAP_WORKSPACE_ID", "   ")

	if got := Load().AuthBootstrapWorkspaceID; got != "" {
		t.Fatalf("expected a blank value to leave the bootstrap disabled, got %q", got)
	}
}
