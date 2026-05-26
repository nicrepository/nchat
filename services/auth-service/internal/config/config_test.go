package config

import "testing"

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
