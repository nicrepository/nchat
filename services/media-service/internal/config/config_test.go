package config

import (
	"strings"
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	cfg := Load()
	if cfg.ServiceName != "media-service" || cfg.Env != "development" || cfg.Port != 8087 || cfg.ReadHeaderTimeoutSeconds != 5 {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	if cfg.MediaSpikeEnabled || cfg.MediaSpikeTokenTTLSeconds != 300 {
		t.Fatalf("unexpected spike defaults: %+v", cfg)
	}
}

func TestValidateAcceptsDisabledSpike(t *testing.T) {
	if err := (Config{Env: "production"}).Validate(); err != nil {
		t.Fatalf("disabled spike should not affect service startup: %v", err)
	}
}

func TestValidateAcceptsCompleteDevelopmentSpike(t *testing.T) {
	cfg := validSpikeConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected valid development spike config: %v", err)
	}
	if !cfg.MediaSpikeActive() {
		t.Fatal("expected complete local spike configuration to be active")
	}
}

func TestValidateRejectsDevelopmentSpikeWithoutLocalMarker(t *testing.T) {
	cfg := validSpikeConfig()
	cfg.MediaSpikeLocalOnly = false
	if err := cfg.Validate(); err == nil || !strings.Contains(strings.ToLower(err.Error()), "local") {
		t.Fatalf("expected explicit local-only marker error, got %v", err)
	}
	if cfg.MediaSpikeActive() {
		t.Fatal("expected spike without local marker to remain inactive")
	}
}

func TestValidateRejectsEnabledSpikeOutsideLocalDevelopment(t *testing.T) {
	for _, env := range []string{"staging", "production", "test", "", "unknown"} {
		t.Run(env, func(t *testing.T) {
			cfg := validSpikeConfig()
			cfg.Env = env
			if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "development") {
				t.Fatalf("expected development-only error for %q, got %v", env, err)
			}
			if cfg.MediaSpikeActive() {
				t.Fatalf("expected spike to remain inactive in %q", env)
			}
		})
	}
}

func TestValidateAcceptsDisabledSpikeInStagingWithoutCredentials(t *testing.T) {
	cfg := Config{Env: "staging", MediaSpikeEnabled: false}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("disabled staging spike should not affect service startup: %v", err)
	}
	if cfg.MediaSpikeActive() {
		t.Fatal("disabled staging spike must remain inactive")
	}
}

func TestValidateRejectsMissingSpikeConfigWithoutLeakingSecret(t *testing.T) {
	cfg := validSpikeConfig()
	cfg.LiveKitAPIKey = ""
	cfg.LiveKitAPISecret = "do-not-leak-this-secret"
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected missing configuration error")
	}
	if strings.Contains(err.Error(), cfg.LiveKitAPISecret) {
		t.Fatalf("configuration error leaked secret: %v", err)
	}
}

func TestValidateRejectsUnsafeSpikeValues(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{name: "http livekit URL", mutate: func(cfg *Config) { cfg.LiveKitURL = "http://127.0.0.1:7880" }},
		{name: "non-local livekit URL", mutate: func(cfg *Config) { cfg.LiveKitURL = "wss://livekit.example.test" }},
		{name: "invalid room", mutate: func(cfg *Config) { cfg.MediaSpikeRoom = "room with spaces" }},
		{name: "missing origins", mutate: func(cfg *Config) { cfg.MediaSpikeAllowedOrigins = "" }},
		{name: "origin with path", mutate: func(cfg *Config) { cfg.MediaSpikeAllowedOrigins = "http://localhost:5173/path" }},
		{name: "non-local origin", mutate: func(cfg *Config) { cfg.MediaSpikeAllowedOrigins = "https://chat.example.test" }},
		{name: "TTL below minimum", mutate: func(cfg *Config) { cfg.MediaSpikeTokenTTLSeconds = 59 }},
		{name: "TTL zero", mutate: func(cfg *Config) { cfg.MediaSpikeTokenTTLSeconds = 0 }},
		{name: "TTL negative", mutate: func(cfg *Config) { cfg.MediaSpikeTokenTTLSeconds = -1 }},
		{name: "TTL too long", mutate: func(cfg *Config) { cfg.MediaSpikeTokenTTLSeconds = 601 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validSpikeConfig()
			tt.mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("expected invalid spike configuration")
			}
		})
	}
}

func TestValidateRejectsPartialSpikeConfiguration(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{name: "API key without secret", mutate: func(cfg *Config) { cfg.LiveKitAPISecret = "" }},
		{name: "secret without URL", mutate: func(cfg *Config) { cfg.LiveKitURL = "" }},
		{name: "enabled without room", mutate: func(cfg *Config) { cfg.MediaSpikeRoom = "" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validSpikeConfig()
			tt.mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("expected partial spike configuration to be rejected")
			}
			if cfg.MediaSpikeActive() {
				t.Fatal("partial spike configuration must remain inactive")
			}
		})
	}
}

func validSpikeConfig() Config {
	return Config{
		Env:                       "development",
		MediaSpikeEnabled:         true,
		MediaSpikeLocalOnly:       true,
		LiveKitURL:                "ws://127.0.0.1:7880",
		LiveKitAPIKey:             "dev-key",
		LiveKitAPISecret:          "dev-secret-with-sufficient-length",
		MediaSpikeRoom:            "spike-1to1",
		MediaSpikeAllowedOrigins:  "http://localhost:5173,https://nchat.local:8443",
		MediaSpikeTokenTTLSeconds: 300,
	}
}

func TestLoadUsesEnvironmentOverrides(t *testing.T) {
	t.Setenv("APP_ENV", "test")
	t.Setenv("PORT", "18087")
	cfg := Load()
	if cfg.Env != "test" || cfg.Port != 18087 {
		t.Fatalf("expected env/port overrides, got %+v", cfg)
	}
}

func TestLoadRejectsInvalidLocalOnlyMarker(t *testing.T) {
	setValidSpikeEnvironment(t)
	t.Setenv("MEDIA_SPIKE_LOCAL_ONLY", "sometimes")

	cfg := Load()
	if cfg.MediaSpikeLocalOnly {
		t.Fatal("invalid local-only marker must fail closed")
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(strings.ToLower(err.Error()), "local") {
		t.Fatalf("expected invalid local-only marker to be rejected, got %v", err)
	}
}

func TestLoadDoesNotReplaceConfiguredInvalidSpikeTTLWithDefault(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want int
	}{
		{name: "below minimum", raw: "59", want: 59},
		{name: "zero", raw: "0", want: 0},
		{name: "negative", raw: "-1", want: -1},
		{name: "not an integer", raw: "invalid", want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setValidSpikeEnvironment(t)
			t.Setenv("MEDIA_SPIKE_TOKEN_TTL_SECONDS", tt.raw)

			cfg := Load()

			if cfg.MediaSpikeTokenTTLSeconds != tt.want {
				t.Fatalf("expected configured TTL %q to load as %d, got %d", tt.raw, tt.want, cfg.MediaSpikeTokenTTLSeconds)
			}
			if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "TTL") {
				t.Fatalf("expected explicit TTL validation error, got %v", err)
			}
		})
	}
}

func setValidSpikeEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	t.Setenv("MEDIA_SPIKE_ENABLED", "true")
	t.Setenv("MEDIA_SPIKE_LOCAL_ONLY", "true")
	t.Setenv("LIVEKIT_URL", "ws://127.0.0.1:7880")
	t.Setenv("LIVEKIT_API_KEY", "dev-key")
	t.Setenv("LIVEKIT_API_SECRET", "dev-secret-with-sufficient-length")
	t.Setenv("MEDIA_SPIKE_ROOM", "spike-1to1")
	t.Setenv("MEDIA_SPIKE_ALLOWED_ORIGINS", "http://localhost:5173")
}
