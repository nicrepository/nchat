package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/auth-service/internal/config"
	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
	"github.com/nicrepository/nchat/services/auth-service/internal/storage"
)

func TestNewCreatesApp(t *testing.T) {
	cfg := config.Config{ServiceName: "auth-service", Env: "test", Port: 8081, ReadHeaderTimeoutSeconds: 5}

	app, err := New(cfg)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
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

// stubPool is a non-nil storage.Pool whose methods are never called during
// bootstrap wiring (constructors only store the pool).
type stubPool struct{ storage.Pool }

// stubOpenDB swaps the bootstrap DB opener for the duration of the test.
// Tests in this package must not use t.Parallel while a stub is installed.
func stubOpenDB(t *testing.T, fn func(context.Context, string, int, *slog.Logger) (storage.Pool, error)) {
	t.Helper()
	previous := openDBWithRetry
	openDBWithRetry = fn
	t.Cleanup(func() { openDBWithRetry = previous })
}

func TestNewWithUnreachableDB_FailsFast(t *testing.T) {
	// The DSN below never reaches the network: the opener is stubbed. It only
	// serves as a known fragment that must not leak into the returned error.
	const testDSN = "postgres://nchat:sentinel-password@db.invalid:5432/nchat" //nolint:gosec
	attempts := 0
	stubOpenDB(t, func(context.Context, string, int, *slog.Logger) (storage.Pool, error) {
		attempts++
		return nil, storage.ErrDBBootstrapFailed
	})

	cfg := config.Config{ServiceName: "auth-service", Env: "test", Port: 8081, ReadHeaderTimeoutSeconds: 5, DatabaseURL: testDSN, DBConnectTimeoutSeconds: 1}
	start := time.Now()
	app, err := New(cfg)

	if err == nil {
		t.Fatal("expected bootstrap error when DATABASE_URL is set and the DB is unreachable")
	}
	if !errors.Is(err, storage.ErrDBBootstrapFailed) {
		t.Fatalf("expected sanitized ErrDBBootstrapFailed, got %v", err)
	}
	for _, fragment := range []string{"sentinel-password", "db.invalid", "5432"} {
		if strings.Contains(err.Error(), fragment) {
			t.Fatalf("bootstrap error must not leak DSN details (%q): %v", fragment, err)
		}
	}
	if app != nil {
		t.Fatal("expected no app when DB bootstrap fails; a degraded server must not start")
	}
	if attempts != 1 {
		t.Fatalf("expected exactly one opener call, got %d", attempts)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("fail-fast path must not sleep for real; took %s", elapsed)
	}
}

// TestNewWithStubbedDB_ReadyzReportsReady covers the success path without a
// real database: with a stubbed pool and valid JWT config, the app boots and
// /readyz reports 200 with every mandatory check passing.
func TestNewWithStubbedDB_ReadyzReportsReady(t *testing.T) {
	stubOpenDB(t, func(context.Context, string, int, *slog.Logger) (storage.Pool, error) {
		return stubPool{}, nil
	})

	cfg := config.Config{
		ServiceName:                "auth-service",
		Env:                        "test",
		Port:                       8081,
		ReadHeaderTimeoutSeconds:   5,
		DatabaseURL:                "postgres://stubbed",
		DBConnectTimeoutSeconds:    1,
		AuthJWTHMACSecret:          strings.Repeat("a", 32),
		AuthJWTIssuer:              "test-issuer",
		AuthJWTAudience:            "test-audience",
		AuthAccessTokenTTLSeconds:  900,
		AuthRefreshTokenTTLSeconds: 3600,
	}
	app, err := New(cfg)
	if err != nil {
		t.Fatalf("unexpected bootstrap error: %v", err)
	}

	rec := httptest.NewRecorder()
	app.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected readyz 200 after successful bootstrap, got %d — %s", rec.Code, rec.Body.String())
	}
}

// readinessCheckStatus decodes the /readyz envelope and returns the status
// of the named check, failing the test if the check is absent.
func readinessCheckStatus(t *testing.T, body []byte, name string) string {
	t.Helper()
	var envelope struct {
		Data struct {
			Checks []struct {
				Name   string `json:"name"`
				Status string `json:"status"`
			} `json:"checks"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode readyz envelope: %v", err)
	}
	for _, check := range envelope.Data.Checks {
		if check.Name == name {
			return check.Status
		}
	}
	t.Fatalf("readiness check %q not found in %s", name, body)
	return ""
}

// TestNewWithStubbedDBAndMissingJWTSecret_JWTCheckFails: pool opened via
// stub but AUTH_JWT_HMAC_SECRET empty — the token manager is never built.
// jwt-token-manager must fail (a skipped construction must not read as
// pass) while database stays pass, and /readyz stays 503.
func TestNewWithStubbedDBAndMissingJWTSecret_JWTCheckFails(t *testing.T) {
	stubOpenDB(t, func(context.Context, string, int, *slog.Logger) (storage.Pool, error) {
		return stubPool{}, nil
	})

	cfg := config.Config{
		ServiceName:              "auth-service",
		Env:                      "test",
		Port:                     8081,
		ReadHeaderTimeoutSeconds: 5,
		DatabaseURL:              "postgres://stubbed",
		DBConnectTimeoutSeconds:  1,
		// AuthJWTHMACSecret intentionally empty.
	}
	app, err := New(cfg)
	if err != nil {
		t.Fatalf("unexpected bootstrap error: %v", err)
	}

	rec := httptest.NewRecorder()
	app.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected readyz 503 with missing JWT secret, got %d — %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.Bytes()
	if got := readinessCheckStatus(t, body, "database"); got != "pass" {
		t.Fatalf("expected database check to pass with stubbed pool, got %q", got)
	}
	for _, name := range []string{"jwt-token-manager", "login-manager", "session-manager"} {
		if got := readinessCheckStatus(t, body, name); got != "fail" {
			t.Fatalf("expected %s check to fail with missing JWT secret, got %q", name, got)
		}
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

	app, err := New(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if app == nil || app.Handler == nil {
		t.Fatal("expected app with handler")
	}
}

// TestAppShutdownClosesPoolOnce verifies that Shutdown closes the DB pool
// exactly once even when called repeatedly.
func TestAppShutdownClosesPoolOnce(t *testing.T) {
	cfg := config.Config{ServiceName: "auth-service", Env: "test", Port: 8081, ReadHeaderTimeoutSeconds: 5}
	app, err := New(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	closed := 0
	app.closeDB = func() { closed++ }

	if err := app.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	_ = app.Shutdown(context.Background())

	if closed != 1 {
		t.Fatalf("expected pool closed exactly once, got %d", closed)
	}
}

// TestAppShutdownConcurrentClosesPoolOnce hammers Shutdown from many
// goroutines: the pool must close exactly once and no call may panic.
// Run with -race.
func TestAppShutdownConcurrentClosesPoolOnce(t *testing.T) {
	cfg := config.Config{ServiceName: "auth-service", Env: "test", Port: 8081, ReadHeaderTimeoutSeconds: 5}
	app, err := New(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	closed := 0
	app.closeDB = func() { closed++ }

	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = app.Shutdown(context.Background())
		}()
	}
	wg.Wait()

	if closed != 1 {
		t.Fatalf("expected pool closed exactly once under concurrency, got %d", closed)
	}
}

// TestAppShutdownNilPoolDoesNotPanic: Shutdown without a DB pool (closeDB
// nil) must be a safe no-op.
func TestAppShutdownNilPoolDoesNotPanic(t *testing.T) {
	cfg := config.Config{ServiceName: "auth-service", Env: "test", Port: 8081, ReadHeaderTimeoutSeconds: 5}
	app, err := New(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := app.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown with nil pool: %v", err)
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
