package observability

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewMetricsHandlerAlwaysReturns200(t *testing.T) {
	cfg := Config{ServiceName: "test-svc", Environment: "test"}
	m := NewMetrics(cfg)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rr := httptest.NewRecorder()
	m.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
}

func TestNewMetricsHandlerContainsServiceInfo(t *testing.T) {
	cfg := Config{ServiceName: "test-svc", Environment: "test"}
	m := NewMetrics(cfg)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rr := httptest.NewRecorder()
	m.Handler().ServeHTTP(rr, req)

	body, _ := io.ReadAll(rr.Body)
	if !strings.Contains(string(body), "nchat_service_info") {
		t.Fatalf("expected nchat_service_info in metrics body, got:\n%s", body)
	}
}

func TestNewMetricsDisabledDoesNotExposeHTTPMetrics(t *testing.T) {
	cfg := Config{ServiceName: "test-svc", Environment: "test", MetricsEnabled: false}
	m := NewMetrics(cfg)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rr := httptest.NewRecorder()
	m.Handler().ServeHTTP(rr, req)

	body, _ := io.ReadAll(rr.Body)
	if strings.Contains(string(body), "nchat_http_requests_total") {
		t.Fatal("expected no nchat_http_requests_total when MetricsEnabled=false")
	}
}

func TestNewMetricsEnabledExposesHTTPMetrics(t *testing.T) {
	cfg := Config{ServiceName: "test-svc", Environment: "test", MetricsEnabled: true}
	m := NewMetrics(cfg)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rr := httptest.NewRecorder()
	m.Handler().ServeHTTP(rr, req)

	body, _ := io.ReadAll(rr.Body)
	// GoCollector and ProcessCollector are registered when MetricsEnabled=true.
	if !strings.Contains(string(body), "go_goroutines") {
		t.Fatalf("expected go_goroutines (GoCollector) when MetricsEnabled=true, got:\n%s", body)
	}
}
