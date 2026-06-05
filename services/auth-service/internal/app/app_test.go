package app

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/nicrepository/nchat/services/auth-service/internal/config"
	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
)

func TestNewCreatesApp(t *testing.T) {
	cfg := config.Config{ServiceName: "auth-service", Env: "test", Port: 8081, ReadHeaderTimeoutSeconds: 5}

	app := New(cfg)

	if app == nil {
		t.Fatal("expected app")
	}
	if app.Logger == nil {
		t.Fatal("expected logger")
	}
	if app.Handler == nil {
		t.Fatal("expected handler")
	}
	if app.Config != cfg {
		t.Fatalf("expected config %+v, got %+v", cfg, app.Config)
	}
}

func TestNewWithUnreachableDB_DisablesAdminEndpoint(t *testing.T) {
	// Port 9 is discard protocol — connections are refused immediately.
	// DATABASE_URL contains a dummy password for testing purposes only.
	t.Setenv("DATABASE_URL", "postgres://nchat:pass@localhost:9/nonexistent?sslmode=disable") //nolint:gosec
	t.Setenv("DB_CONNECT_TIMEOUT_SECONDS", "1")

	cfg := config.Load()

	app := New(cfg)

	if app == nil {
		t.Fatal("expected app even when DB is unavailable")
	}
	if app.Handler == nil {
		t.Fatal("expected handler")
	}
}

func TestSplitOIDCDomainsNormalizesAndDropsEmptyValues(t *testing.T) {
	domains := splitOIDCDomains(" Example.COM, nic.br, ,Internal.LOCAL ")
	want := []string{"example.com", "nic.br", "internal.local"}
	if len(domains) != len(want) {
		t.Fatalf("expected %d domains, got %+v", len(want), domains)
	}
	for i := range want {
		if domains[i] != want[i] {
			t.Fatalf("expected %q at %d, got %+v", want[i], i, domains)
		}
	}
}

func TestNewWithOIDCEnabledAndInvalidConfigFailsClosed(t *testing.T) {
	cfg := config.Config{
		ServiceName:                         "auth-service",
		Env:                                 "test",
		Port:                                8081,
		ReadHeaderTimeoutSeconds:            5,
		OIDCEnabled:                         true,
		OIDCProviderName:                    "keycloak",
		AuthTokenEndpointRateLimitPerMinute: 60,
		AuthTokenEndpointRateLimitBurst:     10,
	}

	app := New(cfg)
	if app == nil || app.Handler == nil {
		t.Fatal("expected app with handler")
	}
}

func TestResolveOIDCProviderNormalizesUppercaseKeycloakFromEnv(t *testing.T) {
	t.Setenv("OIDC_ENABLED", "true")
	t.Setenv("OIDC_PROVIDER_NAME", "KEYCLOAK")
	t.Setenv("OIDC_ISSUER_URL", "https://keycloak.example.com/realms/nchat")
	t.Setenv("OIDC_CLIENT_ID", "client")
	t.Setenv("OIDC_CLIENT_SECRET", "secret") //nolint:gosec
	t.Setenv("OIDC_REDIRECT_URL", "https://auth.example.com/auth/oidc/keycloak/callback")
	t.Setenv("OIDC_FRONTEND_CALLBACK_URL", "/oidc-callback")
	cfg := config.Load()

	providerName, provider, err := resolveOIDCProvider(cfg)
	if err != nil {
		t.Fatalf("resolve OIDC provider: %v", err)
	}
	if providerName != "keycloak" {
		t.Fatalf("expected normalized provider name keycloak, got %q", providerName)
	}
	if provider == nil {
		t.Fatal("expected keycloak provider")
	}
}

func TestResolveOIDCProviderUnsupportedProviderFailsClosed(t *testing.T) {
	cfg := validOIDCConfig()
	cfg.OIDCProviderName = "okta"

	_, provider, err := resolveOIDCProvider(cfg)
	if !errors.Is(err, domain.ErrOIDCMisconfigured) {
		t.Fatalf("expected ErrOIDCMisconfigured, got %v", err)
	}
	if provider != nil {
		t.Fatal("expected no provider for unsupported OIDC provider")
	}
}

func TestResolveOIDCProviderRejectsSambaADAsOIDC(t *testing.T) {
	cfg := validOIDCConfig()
	cfg.OIDCProviderName = "samba_ad"

	_, provider, err := resolveOIDCProvider(cfg)
	if !errors.Is(err, domain.ErrOIDCMisconfigured) {
		t.Fatalf("expected ErrOIDCMisconfigured, got %v", err)
	}
	if provider != nil {
		t.Fatal("expected no provider for samba_ad")
	}
}

func TestResolveOIDCProviderConfiguredButUnregisteredProviderIsDisabled(t *testing.T) {
	cfg := validOIDCConfig()
	cfg.OIDCProviderName = "azure_ad"

	providerName, provider, err := resolveOIDCProvider(cfg)
	if !errors.Is(err, domain.ErrOIDCDisabled) {
		t.Fatalf("expected ErrOIDCDisabled, got %v", err)
	}
	if providerName != "azure_ad" {
		t.Fatalf("expected azure_ad provider name, got %q", providerName)
	}
	if provider != nil {
		t.Fatal("expected no provider for unregistered azure_ad")
	}
}

func TestOIDCProviderBootstrapReason(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "misconfigured", err: domain.ErrOIDCMisconfigured, want: "invalid_oidc_config"},
		{name: "disabled", err: domain.ErrOIDCDisabled, want: "provider_not_resolved"},
		{name: "registration", err: errors.New("registry failed"), want: "provider_registration_failed"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := oidcProviderBootstrapReason(tt.err); got != tt.want {
				t.Fatalf("expected %q, got %q", tt.want, got)
			}
		})
	}
}

func TestWarnOIDCAllowedEmailDomainsUnset(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	warnOIDCAllowedEmailDomainsUnset(logger, config.Config{OIDCEnabled: true}, nil)

	if !strings.Contains(buf.String(), "OIDC_ALLOWED_EMAIL_DOMAINS is not set; all email domains are permitted") {
		t.Fatalf("expected OIDC domain allowlist warning, got %s", buf.String())
	}
}

func TestWarnOIDCAllowedEmailDomainsUnsetSkipsWhenDisabledOrConfigured(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	warnOIDCAllowedEmailDomainsUnset(logger, config.Config{OIDCEnabled: false}, nil)
	warnOIDCAllowedEmailDomainsUnset(logger, config.Config{OIDCEnabled: true}, []string{"example.com"})
	warnOIDCAllowedEmailDomainsUnset(nil, config.Config{OIDCEnabled: true}, nil)

	if buf.Len() != 0 {
		t.Fatalf("expected no warning, got %s", buf.String())
	}
}

func validOIDCConfig() config.Config {
	return config.Config{ //nolint:gosec
		ServiceName:              "auth-service",
		Env:                      "test",
		Port:                     8081,
		ReadHeaderTimeoutSeconds: 5,
		OIDCEnabled:              true,
		OIDCProviderName:         "keycloak",
		OIDCIssuerURL:            "https://keycloak.example.com/realms/nchat",
		OIDCClientID:             "client",
		OIDCClientSecret:         "secret",
		OIDCRedirectURL:          "https://auth.example.com/auth/oidc/keycloak/callback",
		OIDCFrontendCallbackURL:  "/oidc-callback",
		OIDCScopes:               "openid email profile",
	}
}
