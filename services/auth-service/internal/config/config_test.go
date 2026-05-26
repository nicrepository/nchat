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
