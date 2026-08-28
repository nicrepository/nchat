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

func TestSMTPWorkerReady_WorkerDisabled(t *testing.T) {
	cfg := Config{SMTPWorkerEnabled: false}
	ready, reason := cfg.SMTPWorkerReady()
	if !ready || reason != "" {
		t.Fatalf("disabled worker should be ready, got ready=%v, reason=%q", ready, reason)
	}
}

func TestSMTPWorkerReady_MissingSMTPHost(t *testing.T) {
	cfg := Config{
		SMTPWorkerEnabled: true,
		SMTPHost:          "",
		SMTPFrom:          "test@example.com",
		Env:               "development",
	}
	ready, reason := cfg.SMTPWorkerReady()
	if ready || reason != "SMTP_HOST is required" {
		t.Fatalf("expected not ready with SMTP_HOST reason, got ready=%v, reason=%q", ready, reason)
	}
}

func TestSMTPWorkerReady_MissingSMTPFrom(t *testing.T) {
	cfg := Config{
		SMTPWorkerEnabled: true,
		SMTPHost:          "smtp.example.com",
		SMTPFrom:          "",
		Env:               "development",
	}
	ready, reason := cfg.SMTPWorkerReady()
	if ready || reason != "SMTP_FROM is required" {
		t.Fatalf("expected not ready with SMTP_FROM reason, got ready=%v, reason=%q", ready, reason)
	}
}

func TestSMTPWorkerReady_TLSNoneInProduction(t *testing.T) {
	cfg := Config{
		SMTPWorkerEnabled: true,
		SMTPHost:          "smtp.example.com",
		SMTPFrom:          "test@example.com",
		SMTPTLSMode:       "none",
		Env:               "production",
	}
	ready, reason := cfg.SMTPWorkerReady()
	if ready || reason != "SMTP_TLS_MODE=none is only allowed in development/test/local environments" {
		t.Fatalf("expected not ready with TLS mode reason, got ready=%v, reason=%q", ready, reason)
	}
}

func TestSMTPWorkerReady_TLSNoneInDevelopment(t *testing.T) {
	cfg := Config{
		SMTPWorkerEnabled: true,
		SMTPHost:          "smtp.example.com",
		SMTPFrom:          "test@example.com",
		SMTPTLSMode:       "none",
		Env:               "development",
	}
	ready, reason := cfg.SMTPWorkerReady()
	if !ready || reason != "" {
		t.Fatalf("expected ready in development, got ready=%v, reason=%q", ready, reason)
	}
}

func TestSMTPWorkerReady_TLSNoneInTest(t *testing.T) {
	cfg := Config{
		SMTPWorkerEnabled: true,
		SMTPHost:          "smtp.example.com",
		SMTPFrom:          "test@example.com",
		SMTPTLSMode:       "none",
		Env:               "test",
	}
	ready, reason := cfg.SMTPWorkerReady()
	if !ready || reason != "" {
		t.Fatalf("expected ready in test, got ready=%v, reason=%q", ready, reason)
	}
}

func TestSMTPWorkerReady_FullConfigWithStartTLS(t *testing.T) {
	cfg := Config{
		SMTPWorkerEnabled: true,
		SMTPHost:          "smtp.example.com",
		SMTPFrom:          "test@example.com",
		SMTPTLSMode:       "starttls",
		Env:               "production",
	}
	ready, reason := cfg.SMTPWorkerReady()
	if !ready || reason != "" {
		t.Fatalf("expected ready with full config, got ready=%v, reason=%q", ready, reason)
	}
}
