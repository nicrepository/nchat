package config

import "testing"

func TestLoadDefaults(t *testing.T) {
	cfg := Load()
	if cfg.ServiceName != "notification-service" || cfg.Env != "development" || cfg.Port != 8084 || cfg.ReadHeaderTimeoutSeconds != 5 {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
}

func TestLoadUsesEnvironmentOverrides(t *testing.T) {
	t.Setenv("APP_ENV", "test")
	t.Setenv("PORT", "18084")
	cfg := Load()
	if cfg.Env != "test" || cfg.Port != 18084 {
		t.Fatalf("expected env/port overrides, got %+v", cfg)
	}
}
