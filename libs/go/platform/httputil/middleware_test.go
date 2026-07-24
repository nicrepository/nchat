package httputil

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSecurityHeadersAppliesAPIHeaders(t *testing.T) {
	handler := SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))

	if response.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("expected X-Content-Type-Options nosniff")
	}
	if response.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatal("expected X-Frame-Options DENY")
	}
	if response.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatal("expected Referrer-Policy no-referrer")
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("expected Cache-Control no-store")
	}
	if response.Header().Get("Strict-Transport-Security") != "max-age=63072000; includeSubDomains" {
		t.Fatalf("expected HSTS header, got %q", response.Header().Get("Strict-Transport-Security"))
	}
}

func TestRequestIDGeneratesMissingID(t *testing.T) {
	var contextRequestID string
	handler := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contextRequestID = RequestIDFromContext(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}))
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))

	if response.Header().Get("X-Request-ID") == "" {
		t.Fatal("expected generated X-Request-ID")
	}
	if contextRequestID == "" || contextRequestID != response.Header().Get("X-Request-ID") {
		t.Fatalf("generated request ID was not propagated to context: header=%q context=%q", response.Header().Get("X-Request-ID"), contextRequestID)
	}
}

func TestRequestIDReusesIncomingID(t *testing.T) {
	var contextRequestID string
	handler := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contextRequestID = RequestIDFromContext(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}))
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("X-Request-ID", "req-123")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Header().Get("X-Request-ID") != "req-123" {
		t.Fatalf("expected request id req-123, got %q", response.Header().Get("X-Request-ID"))
	}
	if contextRequestID != "req-123" {
		t.Fatalf("expected context request id req-123, got %q", contextRequestID)
	}
}

func TestRecoverReturnsGenericInternalError(t *testing.T) {
	handler := Recover(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		panic("secret panic")
	}))
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("expected status 500, got %d", response.Code)
	}
	if response.Body.String() == "" {
		t.Fatal("expected JSON error body")
	}
	if strings.Contains(response.Body.String(), "secret panic") {
		t.Fatal("panic details leaked to client")
	}
}

func TestMethodNotAllowedRejectsInvalidMethod(t *testing.T) {
	handler := MethodNotAllowed(http.MethodGet, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/", nil))

	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected status 405, got %d", response.Code)
	}
}
