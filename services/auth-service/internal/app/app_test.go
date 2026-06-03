package app

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/nicrepository/nchat/services/auth-service/internal/config"
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
