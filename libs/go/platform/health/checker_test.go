package health

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestStaticCheckerReturnsConfiguredResult(t *testing.T) {
	checker := NewStaticChecker("service-bootstrap", true, CheckPass, "started")

	result := checker.Check(context.Background())

	if checker.Name() != "service-bootstrap" {
		t.Fatalf("expected checker name service-bootstrap, got %q", checker.Name())
	}
	if !checker.Critical() {
		t.Fatal("expected checker to be critical")
	}
	if result.Name != "service-bootstrap" {
		t.Fatalf("expected result name service-bootstrap, got %q", result.Name)
	}
	if result.Status != CheckPass {
		t.Fatalf("expected pass, got %q", result.Status)
	}
	if !result.Critical {
		t.Fatal("expected critical result")
	}
	if result.Message != "started" {
		t.Fatalf("expected message started, got %q", result.Message)
	}
	if result.DurationMS < 0 {
		t.Fatalf("expected non-negative duration, got %d", result.DurationMS)
	}
}

func TestStaticCheckerAppliesDefaultsAndCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	checker := NewStaticChecker("", true, "", "")
	result := checker.Check(ctx)

	if checker.Name() != "unnamed-check" {
		t.Fatalf("expected default checker name, got %q", checker.Name())
	}
	if result.Name != "unnamed-check" {
		t.Fatalf("expected default result name, got %q", result.Name)
	}
	if result.Status != CheckFail {
		t.Fatalf("expected canceled checker to fail, got %q", result.Status)
	}
	if result.Message != "check timed out" {
		t.Fatalf("expected timeout message, got %q", result.Message)
	}
}

func TestEvaluateReadinessHandlesNilCheckerAsCriticalFailure(t *testing.T) {
	response, statusCode := EvaluateReadiness("auth-service", "1.2.3", "abc123", []Checker{nil}, time.Second)

	if statusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected status code 503, got %d", statusCode)
	}
	if response.Status != StatusUnready {
		t.Fatalf("expected unready, got %q", response.Status)
	}
	if response.Checks[0].Name != "unknown-check" {
		t.Fatalf("expected unknown-check, got %q", response.Checks[0].Name)
	}
	if !response.Checks[0].Critical {
		t.Fatal("expected nil checker to be critical")
	}
}

func TestEvaluateReadinessNormalizesEmptyCheckResult(t *testing.T) {
	response, statusCode := EvaluateReadiness("auth-service", "1.2.3", "abc123", []Checker{
		emptyResultChecker{name: "service-bootstrap", critical: true},
	}, time.Second)

	if statusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected status code 503, got %d", statusCode)
	}
	check := response.Checks[0]
	if check.Name != "service-bootstrap" {
		t.Fatalf("expected fallback name service-bootstrap, got %q", check.Name)
	}
	if check.Status != CheckFail {
		t.Fatalf("expected empty status to default to fail, got %q", check.Status)
	}
	if !check.Critical {
		t.Fatal("expected critical flag from checker")
	}
}

func TestEvaluateReadinessFailsPassingResultAfterTimeout(t *testing.T) {
	response, statusCode := EvaluateReadiness("auth-service", "1.2.3", "abc123", []Checker{
		passingAfterTimeoutChecker{name: "service-bootstrap", critical: true},
	}, time.Millisecond)

	if statusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected status code 503, got %d", statusCode)
	}
	check := response.Checks[0]
	if check.Status != CheckFail {
		t.Fatalf("expected pass after timeout to be normalized to fail, got %q", check.Status)
	}
	if check.Message != "check timed out" {
		t.Fatalf("expected timeout message, got %q", check.Message)
	}
}

func TestEvaluateReadinessReturnsReadyAndHTTP200ForPassingCriticalChecks(t *testing.T) {
	checks := []Checker{
		NewStaticChecker("service-bootstrap", true, CheckPass, ""),
		NewStaticChecker("config-loaded", true, CheckPass, ""),
	}

	response, statusCode := EvaluateReadiness("auth-service", "1.2.3", "abc123", checks, time.Second)

	if statusCode != http.StatusOK {
		t.Fatalf("expected status code 200, got %d", statusCode)
	}
	if response.Status != StatusReady {
		t.Fatalf("expected ready, got %q", response.Status)
	}
	if len(response.Checks) != 2 {
		t.Fatalf("expected two checks, got %d", len(response.Checks))
	}
	for _, check := range response.Checks {
		if check.DurationMS < 0 {
			t.Fatalf("expected non-negative duration for %s, got %d", check.Name, check.DurationMS)
		}
	}
}

func TestEvaluateReadinessReturnsUnreadyAndHTTP503ForFailingCriticalCheck(t *testing.T) {
	checks := []Checker{
		NewStaticChecker("service-bootstrap", true, CheckFail, ""),
	}

	response, statusCode := EvaluateReadiness("auth-service", "1.2.3", "abc123", checks, time.Second)

	if statusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected status code 503, got %d", statusCode)
	}
	if response.Status != StatusUnready {
		t.Fatalf("expected unready, got %q", response.Status)
	}
}

func TestEvaluateReadinessReturnsDegradedAndHTTP200ForFailingNonCriticalCheck(t *testing.T) {
	checks := []Checker{
		NewStaticChecker("optional-cache", false, CheckFail, ""),
	}

	response, statusCode := EvaluateReadiness("auth-service", "1.2.3", "abc123", checks, time.Second)

	if statusCode != http.StatusOK {
		t.Fatalf("expected status code 200, got %d", statusCode)
	}
	if response.Status != StatusDegraded {
		t.Fatalf("expected degraded, got %q", response.Status)
	}
}

func TestEvaluateReadinessPassesTimeoutContextToChecks(t *testing.T) {
	checks := []Checker{
		blockingChecker{name: "service-bootstrap", critical: true},
	}

	response, statusCode := EvaluateReadiness("auth-service", "1.2.3", "abc123", checks, time.Millisecond)

	if statusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected status code 503, got %d", statusCode)
	}
	if response.Status != StatusUnready {
		t.Fatalf("expected unready, got %q", response.Status)
	}
	if got := response.Checks[0].Status; got != CheckFail {
		t.Fatalf("expected timeout check to fail, got %q", got)
	}
}

type blockingChecker struct {
	name     string
	critical bool
}

func (c blockingChecker) Name() string {
	return c.name
}

func (c blockingChecker) Critical() bool {
	return c.critical
}

func (c blockingChecker) Check(ctx context.Context) CheckResult {
	<-ctx.Done()

	return CheckResult{
		Name:     c.name,
		Status:   CheckFail,
		Critical: c.critical,
		Message:  "check timed out",
	}
}

type emptyResultChecker struct {
	name     string
	critical bool
}

func (c emptyResultChecker) Name() string {
	return c.name
}

func (c emptyResultChecker) Critical() bool {
	return c.critical
}

func (c emptyResultChecker) Check(ctx context.Context) CheckResult {
	return CheckResult{}
}

type passingAfterTimeoutChecker struct {
	name     string
	critical bool
}

func (c passingAfterTimeoutChecker) Name() string {
	return c.name
}

func (c passingAfterTimeoutChecker) Critical() bool {
	return c.critical
}

func (c passingAfterTimeoutChecker) Check(ctx context.Context) CheckResult {
	<-ctx.Done()

	return CheckResult{
		Name:     c.name,
		Status:   CheckPass,
		Critical: c.critical,
	}
}
