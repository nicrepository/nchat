package observability

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPMiddlewarePassesThrough(t *testing.T) {
	cfg := Config{ServiceName: "test-svc"}
	m := NewMetrics(cfg)

	handler := HTTPMiddleware(cfg, m)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if rr.Body.String() != "ok" {
		t.Fatalf("expected body ok, got %q", rr.Body.String())
	}
}

func TestHTTPMiddlewareRecordsMetrics(t *testing.T) {
	cfg := Config{ServiceName: "test-svc", MetricsEnabled: true}
	m := NewMetrics(cfg)

	handler := HTTPMiddleware(cfg, m)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}))

	req := httptest.NewRequest(http.MethodPost, "/resource", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	metricsReq := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	metricsRR := httptest.NewRecorder()
	m.Handler().ServeHTTP(metricsRR, metricsReq)

	body, _ := io.ReadAll(metricsRR.Body)
	if !strings.Contains(string(body), "nchat_http_requests_total") {
		t.Fatalf("expected nchat_http_requests_total in metrics output, got:\n%s", body)
	}
}

func TestHTTPMiddlewareNilMetricsDoesNotPanic(t *testing.T) {
	cfg := Config{ServiceName: "test-svc", MetricsEnabled: false}

	handler := HTTPMiddleware(cfg, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rr := httptest.NewRecorder()

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("middleware panicked with nil metrics: %v", r)
		}
	}()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
}

func TestHTTPMiddlewareTracingDisabledDoesNotPanic(t *testing.T) {
	cfg := Config{ServiceName: "test-svc", TracingEnabled: false}
	m := NewMetrics(cfg)

	handler := HTTPMiddleware(cfg, m)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
}

func TestResponseWriterCapturesStatusCode(t *testing.T) {
	rr := httptest.NewRecorder()
	rw := &responseWriter{ResponseWriter: rr, statusCode: http.StatusOK}

	rw.WriteHeader(http.StatusTeapot)

	if rw.statusCode != http.StatusTeapot {
		t.Fatalf("expected 418, got %d", rw.statusCode)
	}
	if rr.Code != http.StatusTeapot {
		t.Fatalf("expected underlying 418, got %d", rr.Code)
	}
}

func TestResponseWriterDefaultsTo200OnWrite(t *testing.T) {
	rr := httptest.NewRecorder()
	rw := &responseWriter{ResponseWriter: rr, statusCode: http.StatusOK}

	_, _ = rw.Write([]byte("hello"))

	if rw.statusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", rw.statusCode)
	}
}

func TestResponseWriterDoesNotOverwriteStatus(t *testing.T) {
	rr := httptest.NewRecorder()
	rw := &responseWriter{ResponseWriter: rr, statusCode: http.StatusOK}

	rw.WriteHeader(http.StatusBadRequest)
	rw.WriteHeader(http.StatusOK) // second call should be ignored

	if rw.statusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 after double WriteHeader, got %d", rw.statusCode)
	}
}
