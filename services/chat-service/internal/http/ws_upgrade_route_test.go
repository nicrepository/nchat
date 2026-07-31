package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	platformlog "github.com/nicrepository/nchat/libs/go/platform/log"
	"github.com/nicrepository/nchat/services/chat-service/internal/ws"
	"github.com/nicrepository/nchat/services/chat-service/internal/wsutil"
)

// ── RouteWS upgrade regression tests (issue #449) ─────────────────────────────
//
// The other RouteWS tests in this package drive the router with an
// httptest.ResponseRecorder and a stub handler that answers 200. A recorder is
// not an http.Hijacker, so those tests can prove the auth chain but cannot
// prove the upgrade itself: a middleware that dropped Hijack, or a wrapper that
// wrote to the connection after the switch, would corrupt the 101 and surface
// at the proxy as 502 while every plain /api/chat/* route kept answering 200 —
// exactly the shape of the reported failure.
//
// These tests run the real router over a real server, so the whole chain
// (Recover → RequestID → SecurityHeaders → observability → mux →
// WSTokenMiddleware → BearerAuth → RequireActiveSession → ws handler) has to
// survive a genuine handshake.

type fixedWorkspaceResolver struct{ id string }

func (r fixedWorkspaceResolver) GetDefaultWorkspaceID(context.Context) (string, error) {
	return r.id, nil
}

// newUpgradeRouter builds the production router wired to a real WebSocket
// handler and hub, with real JWT validation.
func newUpgradeRouter(t *testing.T) http.Handler {
	t.Helper()
	validator, err := NewTokenValidator(routerTestSigningKey(), routerTestIssuer, routerTestAudience)
	if err != nil {
		t.Fatalf("NewTokenValidator: %v", err)
	}
	logger := platformlog.New("chat-service", "test")
	hub := ws.NewHub(ws.NopAuthorizer{}, logger, ws.NopBus{}, "test-upgrade")
	t.Cleanup(hub.Shutdown)
	wsHandler := ws.ServeWS(hub, logger,
		fixedWorkspaceResolver{id: "00000000-0000-4000-8000-000000000001"}, GetContextUserID)
	return NewRouter(
		testConfig(), logger, ReadinessState{}, validator, allowRouterSessionValidator{},
		NewSidebarHandler(nil), NewMessageHandler(nil, nil, nil), wsHandler,
		nil, nil, nil, nil,
	)
}

// TestNewRouter_WS_BrowserHandshake_Completes101 proves GET /api/chat/ws is
// registered at exactly that path and completes the upgrade through the full
// middleware chain, using the subprotocol form a browser actually sends.
func TestNewRouter_WS_BrowserHandshake_Completes101(t *testing.T) {
	srv := httptest.NewServer(newUpgradeRouter(t))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	token := makeRouterTestToken(t)
	conn, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+RouteWS,
		&websocket.DialOptions{Subprotocols: []string{token, wsutil.NegotiatedSubprotocol}})
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		t.Fatalf("browser-form handshake must complete, got: %v", err)
	}
	t.Cleanup(func() { _ = conn.CloseNow() })

	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("expected 101, got %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Sec-WebSocket-Protocol"); got != wsutil.NegotiatedSubprotocol {
		t.Fatalf("expected only %q to be selected, got %q", wsutil.NegotiatedSubprotocol, got)
	}
	// The credential must never come back to the client.
	for name, values := range resp.Header {
		for _, value := range values {
			if strings.Contains(value, token) {
				t.Fatalf("access token echoed in response header %s", name)
			}
		}
	}
}

// TestNewRouter_WS_NoSession_RejectedBeforeUpgrade verifies an unauthenticated
// handshake gets a typed 401 rather than a broken upgrade, so a rejection is
// never indistinguishable from an upstream failure.
func TestNewRouter_WS_NoSession_RejectedBeforeUpgrade(t *testing.T) {
	srv := httptest.NewServer(newUpgradeRouter(t))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+RouteWS, nil)
	if err == nil {
		_ = conn.CloseNow()
		t.Fatal("expected the unauthenticated handshake to be rejected")
	}
	if resp == nil {
		t.Fatal("expected an HTTP response for the rejected handshake")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}
