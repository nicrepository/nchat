package observability

import (
	"io"
	"net"
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

// TestResponseWriterPreservesHijacker confirms that a WebSocket upgrade
// (HTTP/1.1 → 101 Switching Protocols) succeeds when the handler is wrapped
// by the observability middleware.
func TestResponseWriterPreservesHijacker(t *testing.T) {
	cfg := Config{ServiceName: "test-svc", MetricsEnabled: true}
	m := NewMetrics(cfg)

	handler := HTTPMiddleware(cfg, m)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "hijack not supported", http.StatusNotImplemented)
			return
		}
		conn, bufrw, err := hijacker.Hijack()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer func() { _ = conn.Close() }()
		_, _ = bufrw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
		_ = bufrw.Flush()
	}))

	srv := httptest.NewServer(handler)
	defer srv.Close()

	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	upgradeReq := "GET /ws HTTP/1.1\r\nHost: localhost\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n"
	if _, err = conn.Write([]byte(upgradeReq)); err != nil {
		t.Fatal(err)
	}

	buf := make([]byte, 256)
	n, err := conn.Read(buf)
	if err != nil && err != io.EOF {
		t.Fatal(err)
	}

	response := string(buf[:n])
	if !strings.HasPrefix(response, "HTTP/1.1 101") {
		t.Fatalf("expected 101 Switching Protocols, got:\n%s", response)
	}
}

func TestResponseWriterUnwrap(t *testing.T) {
	rr := httptest.NewRecorder()
	rw := &responseWriter{ResponseWriter: rr, statusCode: http.StatusOK}

	if rw.Unwrap() != rr {
		t.Fatal("Unwrap() did not return the underlying ResponseWriter")
	}
}

// TestResponseWriterFlushMarksWritten verifies that Flush() locks in 200 OK so
// a subsequent WriteHeader(500) does not corrupt the recorded status code.
// This prevents metrics and tracing from reporting 500 when the client already
// received an implicit 200.
func TestResponseWriterFlushMarksWritten(t *testing.T) {
	rr := httptest.NewRecorder()
	rw := &responseWriter{ResponseWriter: rr, statusCode: http.StatusOK}

	rw.Flush()

	if !rw.written {
		t.Fatal("Flush() must mark rw.written = true")
	}
	if rw.statusCode != http.StatusOK {
		t.Fatalf("Flush() must set statusCode to 200, got %d", rw.statusCode)
	}

	// A WriteHeader after Flush must be ignored for status tracking.
	rw.WriteHeader(http.StatusInternalServerError)
	if rw.statusCode != http.StatusOK {
		t.Fatalf("WriteHeader(500) after Flush() must not change recorded statusCode, got %d", rw.statusCode)
	}
}

// TestHTTPMiddlewareFlushThenWriteHeaderRecords200 exercises the full middleware
// stack: a handler that flushes first and then tries to set a 500 header must
// result in 200 being recorded in metrics, not 500.
func TestHTTPMiddlewareFlushThenWriteHeaderRecords200(t *testing.T) {
	cfg := Config{ServiceName: "test-svc", MetricsEnabled: true}
	m := NewMetrics(cfg)

	handler := HTTPMiddleware(cfg, m)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Flush locks in the implicit 200.
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		// This WriteHeader arrives after Flush — must be a no-op for status tracking.
		w.WriteHeader(http.StatusInternalServerError)
	}))

	req := httptest.NewRequest(http.MethodGet, "/stream", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	// The underlying recorder will have received WriteHeader(500) because we
	// delegate it, but our wrapper must have recorded 200 for metrics.
	// We verify the wrapper state indirectly via the metrics output.
	metricsReq := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	metricsRR := httptest.NewRecorder()
	m.Handler().ServeHTTP(metricsRR, metricsReq)

	body := metricsRR.Body.String()
	if !strings.Contains(body, `status="200"`) {
		t.Fatalf("expected status=\"200\" in metrics, got:\n%s", body)
	}
	if strings.Contains(body, `status="500"`) {
		t.Fatalf("unexpected status=\"500\" in metrics after Flush-then-WriteHeader(500):\n%s", body)
	}
}
