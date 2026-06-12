package ws

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestServeWS_TokenInQueryString_Rejected(t *testing.T) {
	for _, param := range []string{"token", "access_token", "authorization"} {
		t.Run(param, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/ws?"+param+"=x", nil)
			w := httptest.NewRecorder()

			ServeWS(nil, nil).ServeHTTP(w, req)

			if w.Code != http.StatusBadRequest {
				t.Errorf("expected 400 for %q in URL, got %d", param, w.Code)
			}
		})
	}
}

func TestServeWS_NoAuth_Returns501(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	w := httptest.NewRecorder()

	ServeWS(nil, nil).ServeHTTP(w, req)

	if w.Code != http.StatusNotImplemented {
		t.Errorf("expected 501 Not Implemented, got %d", w.Code)
	}
}

func TestServeWS_NoUpgradeOccurs(t *testing.T) {
	// Verify that no WebSocket upgrade (101 Switching Protocols) occurs in stub mode.
	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", "invalid-test-key")

	w := httptest.NewRecorder()
	ServeWS(nil, nil).ServeHTTP(w, req)

	if w.Code == http.StatusSwitchingProtocols {
		t.Fatal("stub handler must never upgrade to WebSocket")
	}
	if w.Code != http.StatusNotImplemented {
		t.Errorf("expected 501, got %d", w.Code)
	}
}

func TestServeWS_CredentialInQueryString_RejectedBeforeUpgrade(t *testing.T) {
	// Even with valid upgrade headers, a credential in the URL query string
	// is rejected before any upgrade attempt.
	credParam := "token" // tested param name; value is intentionally non-sensitive
	req := httptest.NewRequest(http.MethodGet, "/ws?"+credParam+"=x", nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")

	w := httptest.NewRecorder()
	ServeWS(nil, nil).ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("credential param in URL must be rejected with 400, got %d", w.Code)
	}
}

func TestServeWS_ResponseIsJSON(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	w := httptest.NewRecorder()

	ServeWS(nil, nil).ServeHTTP(w, req)

	ct := w.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("expected application/json Content-Type, got %q", ct)
	}
}
