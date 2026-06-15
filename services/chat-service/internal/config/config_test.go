package config

import "testing"

func TestLoadDefaults(t *testing.T) {
	cfg := Load()
	if cfg.ServiceName != "chat-service" || cfg.Env != "development" || cfg.Port != 8082 || cfg.ReadHeaderTimeoutSeconds != 5 {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
}

func TestLoadUsesEnvironmentOverrides(t *testing.T) {
	t.Setenv("APP_ENV", "test")
	t.Setenv("PORT", "18082")
	cfg := Load()
	if cfg.Env != "test" || cfg.Port != 18082 {
		t.Fatalf("expected env/port overrides, got %+v", cfg)
	}
}

func TestLoad_ValkeyURL(t *testing.T) {
	t.Setenv("VALKEY_URL", "valkey://localhost:6379")
	cfg := Load()
	if cfg.ValkeyURL != "valkey://localhost:6379" {
		t.Fatalf("expected ValkeyURL to be set from env, got %q", cfg.ValkeyURL)
	}
}

func TestLoad_ValkeyWSBroadcastEnabled(t *testing.T) {
	t.Setenv("VALKEY_WS_BROADCAST_ENABLED", "true")
	cfg := Load()
	if !cfg.ValkeyWSBroadcastEnabled {
		t.Fatal("expected ValkeyWSBroadcastEnabled=true when env is 'true'")
	}
}

func TestLoad_WSInstanceID(t *testing.T) {
	t.Setenv("WS_INSTANCE_ID", "my-pod-abc123")
	cfg := Load()
	if cfg.WSInstanceID != "my-pod-abc123" {
		t.Fatalf("expected WSInstanceID from env, got %q", cfg.WSInstanceID)
	}
}
