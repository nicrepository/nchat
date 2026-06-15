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

func TestLoad_WSInstanceID_ValidChars(t *testing.T) {
	valid := []string{
		"pod-abc123",
		"instance.A",
		"my_host-01",
		"A",
		"a-b.c_d",
	}
	for _, id := range valid {
		t.Setenv("WS_INSTANCE_ID", id)
		cfg := Load()
		if cfg.WSInstanceID != id {
			t.Errorf("valid WSInstanceID %q was rejected, got %q", id, cfg.WSInstanceID)
		}
	}
}

func TestLoad_WSInstanceID_InvalidChars_FallsBackToEmpty(t *testing.T) {
	invalid := []string{
		"bad instance id", // spaces
		"id!@#$%",         // special chars
		"id/slash",        // slash
		"id:colon",        // colon
	}
	for _, id := range invalid {
		t.Setenv("WS_INSTANCE_ID", id)
		cfg := Load()
		if cfg.WSInstanceID != "" {
			t.Errorf("invalid WSInstanceID %q must fall back to empty, got %q", id, cfg.WSInstanceID)
		}
	}
}

func TestLoad_WSInstanceID_OversizedFallsBackToEmpty(t *testing.T) {
	safe64 := string(make([]byte, 64))
	for i := 0; i < 64; i++ {
		safe64 = safe64[:i] + "a" + safe64[i+1:]
	}
	safe65 := safe64 + "a"

	t.Setenv("WS_INSTANCE_ID", safe64)
	if cfg := Load(); cfg.WSInstanceID != safe64 {
		t.Errorf("64-char WSInstanceID must be accepted, got %q", cfg.WSInstanceID)
	}

	t.Setenv("WS_INSTANCE_ID", safe65)
	if cfg := Load(); cfg.WSInstanceID != "" {
		t.Errorf("65-char WSInstanceID must fall back to empty, got %q", cfg.WSInstanceID)
	}
}

func TestLoad_WSInstanceID_EmptyRemainsEmpty(t *testing.T) {
	t.Setenv("WS_INSTANCE_ID", "")
	cfg := Load()
	if cfg.WSInstanceID != "" {
		t.Fatalf("empty WS_INSTANCE_ID must stay empty, got %q", cfg.WSInstanceID)
	}
}
