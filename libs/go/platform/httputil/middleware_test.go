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

// The administrative audit trail is indexed by this value, so the caller must
// not be able to choose it. A client that supplies X-Request-ID gets a
// server-minted one back instead.
func TestGeneratedRequestIDIgnoresTheInboundHeader(t *testing.T) {
	const forged = "attacker-controlled-value"
	var seen string
	handler := GeneratedRequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = RequestIDFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("X-Request-ID", forged)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if seen == forged {
		t.Fatal("the forged identifier reached the handler")
	}
	if seen == "" {
		t.Fatal("expected a generated identifier")
	}
	if got := response.Header().Get("X-Request-ID"); got != seen {
		t.Fatalf("the response must carry the generated identifier, got %q want %q", got, seen)
	}
	if len(seen) != 32 {
		t.Fatalf("expected a 32-character hex identifier, got %q", seen)
	}
}

// Two requests must not share an identifier, forged header or not.
func TestGeneratedRequestIDIsUniquePerRequest(t *testing.T) {
	ids := make(map[string]struct{}, 64)
	handler := GeneratedRequestID(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		ids[RequestIDFromContext(r.Context())] = struct{}{}
	}))

	for i := 0; i < 64; i++ {
		request := httptest.NewRequest(http.MethodGet, "/", nil)
		request.Header.Set("X-Request-ID", "same-for-every-request")
		handler.ServeHTTP(httptest.NewRecorder(), request)
	}
	if len(ids) != 64 {
		t.Fatalf("expected 64 distinct identifiers, got %d", len(ids))
	}
}

// RequestID keeps propagating an upstream trace: that is what the other
// services use it for, and this change must not alter it.
func TestRequestIDStillAdoptsAnInboundTrace(t *testing.T) {
	const upstream = "trace-from-the-gateway"
	var seen string
	handler := RequestID(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = RequestIDFromContext(r.Context())
	}))

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("X-Request-ID", upstream)
	handler.ServeHTTP(httptest.NewRecorder(), request)

	if seen != upstream {
		t.Fatalf("expected the upstream trace to survive, got %q", seen)
	}
}
