package config

import (
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	cfg := Load()
	if cfg.ServiceName != "admin-service" || cfg.Env != "development" || cfg.Port != 8085 || cfg.ReadHeaderTimeoutSeconds != 5 {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	if cfg.SessionIdleTTL != 15*time.Minute || cfg.SessionAbsoluteTTL != 480*time.Minute {
		t.Fatalf("unexpected session policy: idle=%s absolute=%s", cfg.SessionIdleTTL, cfg.SessionAbsoluteTTL)
	}
	if len(cfg.AllowedOrigins) != 0 {
		t.Fatalf("expected no CORS allowlist by default, got %v", cfg.AllowedOrigins)
	}
}

func TestLoadUsesEnvironmentOverrides(t *testing.T) {
	t.Setenv("APP_ENV", "test")
	t.Setenv("PORT", "18085")
	cfg := Load()
	if cfg.Env != "test" || cfg.Port != 18085 {
		t.Fatalf("expected env/port overrides, got %+v", cfg)
	}
}

// The administrative session policy must never be silently widened by a bad
// value: a zero or negative TTL falls back to the default rather than becoming
// an unbounded session.
func TestLoadRejectsNonPositiveSessionTTLs(t *testing.T) {
	t.Setenv("ADMIN_SESSION_IDLE_MINUTES", "0")
	t.Setenv("ADMIN_SESSION_ABSOLUTE_MINUTES", "-1")
	cfg := Load()
	if cfg.SessionIdleTTL != 15*time.Minute || cfg.SessionAbsoluteTTL != 480*time.Minute {
		t.Fatalf("expected fallbacks, got idle=%s absolute=%s", cfg.SessionIdleTTL, cfg.SessionAbsoluteTTL)
	}
}

// A wildcard origin alongside credentials is the combination that would let any
// page drive the console. It is dropped at load time so no later layer has to
// remember to refuse it.
func TestLoadDropsWildcardOrigin(t *testing.T) {
	t.Setenv("ADMIN_ALLOWED_ORIGINS", "https://admin.example.test, *, , https://other.example.test")
	cfg := Load()
	if len(cfg.AllowedOrigins) != 2 {
		t.Fatalf("expected 2 origins, got %v", cfg.AllowedOrigins)
	}
	for _, origin := range cfg.AllowedOrigins {
		if origin == "*" {
			t.Fatal("the wildcard origin must never survive configuration loading")
		}
	}
}

func TestEnvironmentMapping(t *testing.T) {
	tests := []struct {
		env  string
		want Environment
	}{
		{"development", EnvironmentDevelopment},
		{"dev", EnvironmentDevelopment},
		{"nchat-dev", EnvironmentDevelopment},
		{"local", EnvironmentDevelopment},
		{"test", EnvironmentDevelopment},
		{" Staging ", EnvironmentStaging},
		{"homolog", EnvironmentStaging},
		{"production", EnvironmentProduction},
		{"prod", EnvironmentProduction},
	}
	for _, tt := range tests {
		if got := (Config{Env: tt.env}).Environment(); got != tt.want {
			t.Fatalf("APP_ENV %q: expected %q, got %q", tt.env, tt.want, got)
		}
	}
}

// An environment nobody described must read as the most dangerous one. Guessing
// "development" would understate the blast radius of a click, which is the only
// reason the banner exists.
func TestEnvironmentUnknownFailsClosedToProduction(t *testing.T) {
	for _, env := range []string{"", "  ", "qa-sandbox", "🙂"} {
		if got := (Config{Env: env}).Environment(); got != EnvironmentProduction {
			t.Fatalf("APP_ENV %q: expected PRODUCTION, got %q", env, got)
		}
	}
}

func TestValidateAdminAPI(t *testing.T) {
	base := Config{
		DatabaseURL:        "postgres://localhost/nchat",
		AuthJWTHMACSecret:  "01234567890123456789012345678901",
		AuthJWTIssuer:      "nchat-auth",
		AuthJWTAudience:    "nchat-api",
		SessionIdleTTL:     15 * time.Minute,
		SessionAbsoluteTTL: 8 * time.Hour,
	}
	if err := base.ValidateAdminAPI(); err != nil {
		t.Fatalf("expected a complete configuration to validate, got %v", err)
	}
	if !base.AdminAPIEnabled() {
		t.Fatal("expected the admin api to be enabled")
	}

	tests := map[string]func(Config) Config{
		"no database":     func(c Config) Config { c.DatabaseURL = ""; return c },
		"short secret":    func(c Config) Config { c.AuthJWTHMACSecret = "too-short"; return c },
		"no issuer":       func(c Config) Config { c.AuthJWTIssuer = ""; return c },
		"no audience":     func(c Config) Config { c.AuthJWTAudience = ""; return c },
		"no idle ttl":     func(c Config) Config { c.SessionIdleTTL = 0; return c },
		"no absolute ttl": func(c Config) Config { c.SessionAbsoluteTTL = 0; return c },
		"idle > absolute": func(c Config) Config { c.SessionIdleTTL = 9 * time.Hour; return c },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			cfg := mutate(base)
			if err := cfg.ValidateAdminAPI(); err == nil {
				t.Fatal("expected the admin api to refuse this configuration")
			}
			if cfg.AdminAPIEnabled() {
				t.Fatal("expected the admin api to stay disabled")
			}
		})
	}
}
