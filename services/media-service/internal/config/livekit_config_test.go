package config

import (
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestLiveKitConfigurationDefaultsDisabled(t *testing.T) {
	cfg := Load()

	if cfg.LiveKitEnabled {
		t.Fatal("LiveKit must be disabled by default")
	}
	if cfg.LiveKitTokenTTLSeconds != 300 {
		t.Fatalf("expected default token TTL 300, got %d", cfg.LiveKitTokenTTLSeconds)
	}
	if cfg.AuthJWTIssuer != "nchat-auth" || cfg.AuthJWTAudience != "nchat-api" {
		t.Fatalf("unexpected JWT defaults: issuer=%q audience=%q", cfg.AuthJWTIssuer, cfg.AuthJWTAudience)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("disabled integration must not prevent startup: %v", err)
	}
}

func TestLiveKitConfigurationValidWhenEnabled(t *testing.T) {
	cfg := validLiveKitConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected valid configuration: %v", err)
	}
}

func TestLiveKitConfigurationAcceptsSecureWebSocketURL(t *testing.T) {
	cfg := validLiveKitConfig()
	cfg.LiveKitAPIURL = "wss://livekit-dev.nic-labs.com"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected secure WebSocket URL to be valid: %v", err)
	}
}

func TestLiveKitConfigurationRejectsMissingRequiredValues(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{name: "API URL", mutate: func(cfg *Config) { cfg.LiveKitAPIURL = "" }},
		{name: "API key", mutate: func(cfg *Config) { cfg.LiveKitAPIKey = "" }},
		{name: "API secret", mutate: func(cfg *Config) { cfg.LiveKitAPISecret = "" }},
		{name: "database URL", mutate: func(cfg *Config) { cfg.DatabaseURL = "" }},
		{name: "JWT secret", mutate: func(cfg *Config) { cfg.AuthJWTHMACSecret = "" }},
		{name: "JWT issuer", mutate: func(cfg *Config) { cfg.AuthJWTIssuer = "" }},
		{name: "JWT audience", mutate: func(cfg *Config) { cfg.AuthJWTAudience = "" }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validLiveKitConfig()
			tt.mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("expected missing required configuration to fail")
			}
		})
	}
}

func TestLiveKitConfigurationRejectsInvalidTTL(t *testing.T) {
	for _, ttl := range []int{0, 59, 601} {
		t.Run(strconv.Itoa(ttl), func(t *testing.T) {
			cfg := validLiveKitConfig()
			cfg.LiveKitTokenTTLSeconds = ttl
			if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "TTL") {
				t.Fatalf("expected TTL validation error for %d, got %v", ttl, err)
			}
		})
	}
}

func TestLiveKitConfigurationRejectsInvalidAPIURL(t *testing.T) {
	for _, rawURL := range []string{"ws://livekit:7880", "http://user:pass@livekit:7880", "://invalid"} {
		cfg := validLiveKitConfig()
		cfg.LiveKitAPIURL = rawURL
		if err := cfg.Validate(); err == nil {
			t.Fatalf("expected invalid API URL %q to fail", rawURL)
		}
	}
}

func TestLiveKitConfigurationErrorsDoNotLeakSecret(t *testing.T) {
	cfg := validLiveKitConfig()
	cfg.LiveKitAPIKey = ""
	cfg.LiveKitAPISecret = "do-not-leak-this-livekit-secret"

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected invalid configuration")
	}
	if strings.Contains(err.Error(), cfg.LiveKitAPISecret) {
		t.Fatalf("configuration error leaked secret: %v", err)
	}
}

func TestLoadLiveKitConfigurationFromEnvironment(t *testing.T) {
	t.Setenv("LIVEKIT_ENABLED", "true")
	t.Setenv("LIVEKIT_API_URL", "https://livekit.internal:7880")
	t.Setenv("LIVEKIT_API_KEY", "server-key")
	t.Setenv("LIVEKIT_API_SECRET", "server-secret")
	t.Setenv("LIVEKIT_TOKEN_TTL_SECONDS", "420")
	t.Setenv("DATABASE_URL", "postgres://nchat@postgres/nchat")
	t.Setenv("DB_CONNECT_TIMEOUT_SECONDS", "7")
	t.Setenv("AUTH_JWT_HMAC_SECRET", strings.Repeat("x", 32))
	t.Setenv("AUTH_JWT_ISSUER", "issuer")
	t.Setenv("AUTH_JWT_AUDIENCE", "audience")

	cfg := Load()
	if !cfg.LiveKitEnabled || cfg.LiveKitAPIURL != "https://livekit.internal:7880" ||
		cfg.LiveKitAPIKey != "server-key" || cfg.LiveKitAPISecret != "server-secret" ||
		cfg.LiveKitTokenTTLSeconds != 420 || cfg.DatabaseURL == "" ||
		cfg.DBConnectTimeoutSeconds != 7 || cfg.AuthJWTIssuer != "issuer" ||
		cfg.AuthJWTAudience != "audience" {
		t.Fatalf("unexpected loaded configuration: %+v", cfg)
	}
}

func TestLoadLiveKitEnabledStrictly(t *testing.T) {
	t.Run("absent defaults to false", func(t *testing.T) {
		unsetEnv(t, "LIVEKIT_ENABLED")

		cfg := Load()
		if cfg.LiveKitEnabled {
			t.Fatal("expected absent LIVEKIT_ENABLED to default to false")
		}
		if err := cfg.Validate(); err != nil {
			t.Fatalf("expected absent LIVEKIT_ENABLED to validate: %v", err)
		}
	})

	for _, tt := range []struct {
		value   string
		enabled bool
	}{
		{value: "true", enabled: true},
		{value: "false", enabled: false},
	} {
		t.Run(tt.value, func(t *testing.T) {
			t.Setenv("LIVEKIT_ENABLED", tt.value)
			cfg := Load()
			if cfg.LiveKitEnabled != tt.enabled {
				t.Fatalf("expected enabled=%t, got %t", tt.enabled, cfg.LiveKitEnabled)
			}
		})
	}

	for _, value := range []string{"tru", "yes-invalid", ""} {
		t.Run("invalid_"+value, func(t *testing.T) {
			t.Setenv("LIVEKIT_ENABLED", value)
			t.Setenv("LIVEKIT_API_KEY", "sensitive-key")
			t.Setenv("LIVEKIT_API_SECRET", "sensitive-secret")

			err := Load().Validate()
			if err == nil {
				t.Fatalf("expected explicitly configured value %q to fail", value)
			}
			for _, sensitive := range []string{"sensitive-key", "sensitive-secret"} {
				if strings.Contains(err.Error(), sensitive) {
					t.Fatalf("validation error leaked sensitive value: %v", err)
				}
			}
		})
	}
}

func TestLoadPreservesExplicitlyInvalidTTLForValidation(t *testing.T) {
	t.Setenv("LIVEKIT_TOKEN_TTL_SECONDS", "invalid")
	if cfg := Load(); cfg.LiveKitTokenTTLSeconds != 0 {
		t.Fatalf("expected invalid TTL to remain invalid, got %d", cfg.LiveKitTokenTTLSeconds)
	}
}

func validLiveKitConfig() Config {
	return Config{
		ServiceName:              "media-service",
		Env:                      "test",
		LiveKitEnabled:           true,
		LiveKitAPIURL:            "http://livekit.internal:7880",
		LiveKitAPIKey:            "server-key",
		LiveKitAPISecret:         "server-secret",
		LiveKitTokenTTLSeconds:   300,
		DatabaseURL:              "postgres://nchat@postgres/nchat",
		DBConnectTimeoutSeconds:  5,
		AuthJWTHMACSecret:        strings.Repeat("h", 32),
		AuthJWTIssuer:            "nchat-auth",
		AuthJWTAudience:          "nchat-api",
		ReadHeaderTimeoutSeconds: 5,
		ReadTimeoutSeconds:       10,
		WriteTimeoutSeconds:      10,
	}
}

func unsetEnv(t *testing.T, key string) {
	t.Helper()
	value, present := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatalf("unset %s: %v", key, err)
	}
	t.Cleanup(func() {
		if present {
			_ = os.Setenv(key, value)
			return
		}
		_ = os.Unsetenv(key)
	})
}
