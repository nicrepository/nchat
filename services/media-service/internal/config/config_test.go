package config

import "testing"

func TestLoadDefaults(t *testing.T) {
	cfg := Load()
	if cfg.ServiceName != "media-service" || cfg.Env != "development" || cfg.Port != 8087 || cfg.ReadHeaderTimeoutSeconds != 5 {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
}

func TestLoadUsesEnvironmentOverrides(t *testing.T) {
	t.Setenv("APP_ENV", "test")
	t.Setenv("PORT", "18087")
	t.Setenv("LIVEKIT_URL", "http://livekit.internal:7880")
	cfg := Load()
	if cfg.Env != "test" || cfg.Port != 18087 || cfg.LiveKitURL != "http://livekit.internal:7880" {
		t.Fatalf("expected env/port overrides, got %+v", cfg)
	}
}
