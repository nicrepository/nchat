package health

import "testing"

func TestNewReadinessWithoutChecksIsReady(t *testing.T) {
	response := NewReadiness("auth-service", "1.2.3", "abc123", nil)

	if response.Service != "auth-service" {
		t.Fatalf("expected service auth-service, got %q", response.Service)
	}
	if response.Probe != ProbeReadiness {
		t.Fatalf("expected probe readiness, got %q", response.Probe)
	}
	if response.Status != StatusReady {
		t.Fatalf("expected status ready, got %q", response.Status)
	}
	if len(response.Checks) != 0 {
		t.Fatalf("expected no checks, got %d", len(response.Checks))
	}
	assertRFC3339(t, response.CheckedAt)
}

func TestNewReadinessWithCriticalPassIsReady(t *testing.T) {
	checks := []CheckResult{{
		Name:       "service-bootstrap",
		Status:     CheckPass,
		Critical:   true,
		DurationMS: 3,
	}}

	response := NewReadiness("auth-service", "1.2.3", "abc123", checks)

	if response.Status != StatusReady {
		t.Fatalf("expected ready, got %q", response.Status)
	}
	if response.Checks[0].DurationMS != 3 {
		t.Fatalf("expected duration 3, got %d", response.Checks[0].DurationMS)
	}
}

func TestNewReadinessWithCriticalFailIsUnready(t *testing.T) {
	checks := []CheckResult{{
		Name:       "service-bootstrap",
		Status:     CheckFail,
		Critical:   true,
		DurationMS: 1,
	}}

	response := NewReadiness("auth-service", "1.2.3", "abc123", checks)

	if response.Status != StatusUnready {
		t.Fatalf("expected unready, got %q", response.Status)
	}
}

func TestNewReadinessWithNonCriticalFailIsDegraded(t *testing.T) {
	checks := []CheckResult{{
		Name:       "optional-cache",
		Status:     CheckFail,
		Critical:   false,
		DurationMS: 1,
	}}

	response := NewReadiness("auth-service", "1.2.3", "abc123", checks)

	if response.Status != StatusDegraded {
		t.Fatalf("expected degraded, got %q", response.Status)
	}
}
