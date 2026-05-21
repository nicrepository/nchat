package health

import (
	"testing"
	"time"
)

func TestNewLivenessBuildsStandardResponse(t *testing.T) {
	response := NewLiveness("auth-service", "1.2.3", "abc123")

	if response.Service != "auth-service" {
		t.Fatalf("expected service auth-service, got %q", response.Service)
	}
	if response.Probe != ProbeLiveness {
		t.Fatalf("expected probe liveness, got %q", response.Probe)
	}
	if response.Status != StatusOK {
		t.Fatalf("expected status ok, got %q", response.Status)
	}
	if response.Version != "1.2.3" {
		t.Fatalf("expected version 1.2.3, got %q", response.Version)
	}
	if response.Commit != "abc123" {
		t.Fatalf("expected commit abc123, got %q", response.Commit)
	}
	assertRFC3339(t, response.CheckedAt)
	if len(response.Checks) != 0 {
		t.Fatalf("expected no liveness checks, got %d", len(response.Checks))
	}
}

func TestNewKeepsBackwardCompatibleLivenessResponse(t *testing.T) {
	response := New("auth-service")

	if response.Service != "auth-service" {
		t.Fatalf("expected service auth-service, got %q", response.Service)
	}
	if response.Probe != ProbeLiveness {
		t.Fatalf("expected liveness probe, got %q", response.Probe)
	}
	if response.Status != StatusOK {
		t.Fatalf("expected ok status, got %q", response.Status)
	}
}

func TestNewLivenessAppliesDefaults(t *testing.T) {
	response := NewLiveness("", "", "")

	if response.Service != "unknown" {
		t.Fatalf("expected default service unknown, got %q", response.Service)
	}
	if response.Version != "0.0.0" {
		t.Fatalf("expected default version 0.0.0, got %q", response.Version)
	}
	if response.Commit != "dev" {
		t.Fatalf("expected default commit dev, got %q", response.Commit)
	}
}

func assertRFC3339(t *testing.T, value string) {
	t.Helper()

	if value == "" {
		t.Fatal("expected checkedAt to be populated")
	}
	if _, err := time.Parse(time.RFC3339, value); err != nil {
		t.Fatalf("expected RFC3339 timestamp, got %q: %v", value, err)
	}
}
