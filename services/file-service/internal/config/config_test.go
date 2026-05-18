package config

import "testing"

func TestLoadDefaults(t *testing.T) {
	cfg := Load()
	if cfg.ServiceName != "file-service" || cfg.Env != "development" || cfg.Port != 8083 || cfg.ReadHeaderTimeoutSeconds != 5 {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
}

func TestLoadUsesEnvironmentOverrides(t *testing.T) {
	t.Setenv("APP_ENV", "test")
	t.Setenv("PORT", "18083")
	cfg := Load()
	if cfg.Env != "test" || cfg.Port != 18083 {
		t.Fatalf("expected env/port overrides, got %+v", cfg)
	}
}
