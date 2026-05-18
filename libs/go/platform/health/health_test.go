package health

import "testing"

func TestNewUsesProvidedServiceName(t *testing.T) {
	got := New("auth-service")

	if got.Service != "auth-service" {
		t.Fatalf("expected service auth-service, got %q", got.Service)
	}
	if got.Status != "ok" {
		t.Fatalf("expected status ok, got %q", got.Status)
	}
}

func TestNewDefaultsEmptyServiceName(t *testing.T) {
	got := New("")

	if got.Service != "unknown" {
		t.Fatalf("expected service unknown, got %q", got.Service)
	}
	if got.Status != "ok" {
		t.Fatalf("expected status ok, got %q", got.Status)
	}
}
