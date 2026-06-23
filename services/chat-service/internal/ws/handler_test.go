package ws

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/golang-jwt/jwt/v5"
)

// ── helpers for handler tests ─────────────────────────────────────────────────

const testIOTimeout = 500 * time.Millisecond

func testSecWebSocketKey() string {
	return base64.StdEncoding.EncodeToString(make([]byte, 16))
}

// fakeWorkspaceResolver returns a fixed workspaceID or error.
type fakeWorkspaceResolver struct {
	id  string
	err error
}

func (f *fakeWorkspaceResolver) GetDefaultWorkspaceID(_ context.Context) (string, error) {
	return f.id, f.err
}

// userIDFromCtxFn returns a userIDFromCtx function that always returns the given userID.
func userIDFromCtxFn(userID string) func(*http.Request) string {
	return func(_ *http.Request) string { return userID }
}

// ── query-string credential rejection ────────────────────────────────────────

func TestServeWS_TokenInQueryString_Rejected(t *testing.T) {
	for _, param := range []string{"token", "access_token", "authorization"} {
		t.Run(param, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/ws?"+param+"=x", nil)
			w := httptest.NewRecorder()

			ServeWS(nil, nil, nil, nil).ServeHTTP(w, req)

			if w.Code != http.StatusBadRequest {
				t.Errorf("expected 400 for %q in URL, got %d", param, w.Code)
			}
		})
	}
}

// TestServeWS_CredentialQueryParams_CaseInsensitive verifies that credential
// params are rejected regardless of the case used in the parameter name.
func TestServeWS_CredentialQueryParams_CaseInsensitive(t *testing.T) {
	cases := []string{
		"Authorization", "TOKEN", "ACCESS_TOKEN",
		"Auth", "JWT", "Bearer",
		"AUTHORIZATION", "Token",
	}
	for _, param := range cases {
		t.Run(param, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/ws?"+param+"=somevalue", nil)
			w := httptest.NewRecorder()

			ServeWS(nil, nil, nil, nil).ServeHTTP(w, req)

			if w.Code != http.StatusBadRequest {
				t.Errorf("expected 400 for mixed-case param %q, got %d", param, w.Code)
			}
		})
	}
}

// TestServeWS_AuthHeader_NotInQueryString_Allowed verifies that a valid
// Authorization header (not query string) is not rejected by the credential check.
// The connection still fails (no hub/workspaces wired) but with 401, not 400.
func TestServeWS_AuthHeader_NotInQueryString_Allowed(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	req.Header.Set("Authorization", "Bearer sometoken")
	w := httptest.NewRecorder()

	// No userIDFromCtx → 401 (not 400), proving the header was not rejected.
	ServeWS(nil, nil, nil, nil).ServeHTTP(w, req)

	if w.Code == http.StatusBadRequest {
		t.Error("Authorization header must not be treated as a query-string credential")
	}
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestServeWS_CredentialInQueryString_RejectedBeforeUpgrade(t *testing.T) {
	credParam := "token"
	req := httptest.NewRequest(http.MethodGet, "/ws?"+credParam+"=x", nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")

	w := httptest.NewRecorder()
	ServeWS(nil, nil, nil, nil).ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("credential param in URL must be rejected with 400, got %d", w.Code)
	}
}

// ── unauthenticated connection rejection ──────────────────────────────────────

func TestServeWS_NoAuth_Returns401(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	w := httptest.NewRecorder()

	// nil userIDFromCtx → no auth → 401.
	ServeWS(nil, nil, nil, nil).ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 Unauthorized, got %d", w.Code)
	}
}

func TestServeWS_EmptyUserID_Returns401(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	w := httptest.NewRecorder()

	ServeWS(nil, nil, nil, userIDFromCtxFn("")).ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for empty userID, got %d", w.Code)
	}
}

func TestServeWS_NoUpgradeOccursWithoutAuth(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", testSecWebSocketKey())

	w := httptest.NewRecorder()
	ServeWS(nil, nil, nil, nil).ServeHTTP(w, req)

	if w.Code == http.StatusSwitchingProtocols {
		t.Fatal("handler must never upgrade an unauthenticated connection")
	}
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestServeWS_ResponseIsJSON(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	w := httptest.NewRecorder()

	ServeWS(nil, nil, nil, nil).ServeHTTP(w, req)

	ct := w.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("expected application/json Content-Type, got %q", ct)
	}
}

// ── authenticated upgrade integration tests ───────────────────────────────────
//
// These tests start a real httptest.Server to exercise the full upgrade path
// with actual WebSocket connections.

func newTestWSServer(t *testing.T, hub *Hub, workspaces WorkspaceResolver, userID string) *httptest.Server {
	t.Helper()
	handler := ServeWS(hub, slog.Default(), workspaces, userIDFromCtxFn(userID))
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

func newTestWSServerWithConfig(t *testing.T, hub *Hub, workspaces WorkspaceResolver, userID string, cfg HandlerConfig) *httptest.Server {
	t.Helper()
	handler := ServeWSWithConfig(hub, slog.Default(), workspaces, userIDFromCtxFn(userID), cfg)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

// dialWS dials the test server's /ws endpoint and returns the connection.
// It fails the test immediately if the dial fails.
func dialWS(t *testing.T, srv *httptest.Server) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testIOTimeout)
	defer cancel()
	url := "ws" + srv.URL[len("http"):]
	conn, resp, err := websocket.Dial(ctx, url, nil)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		t.Fatalf("dial ws: %v", err)
	}
	t.Cleanup(func() { _ = conn.CloseNow() })
	return conn
}

func realisticAccessTokenSubprotocol(t *testing.T) string {
	t.Helper()
	now := time.Now().UTC()
	claims := struct {
		SessionID string `json:"sid"`
		jwt.RegisteredClaims
	}{
		SessionID: "123e4567-e89b-12d3-a456-426614174001",
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "123e4567-e89b-12d3-a456-426614174000",
			Issuer:    "nchat-auth",
			Audience:  jwt.ClaimStrings{"nchat"},
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(15 * time.Minute)),
			ID:        "123e4567-e89b-12d3-a456-426614174002",
		},
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatalf("sign realistic access token: %v", err)
	}
	if strings.Contains(token, "=") {
		t.Fatalf("JWT subprotocol must use unpadded base64url, got %q", token)
	}
	return token
}

func TestDefaultHandlerConfigDocumentsResourceDefaults(t *testing.T) {
	cfg := DefaultHandlerConfig()
	if cfg.MaxConnectionsPerUser != 5 ||
		cfg.InboundMessagesPerMinute != 60 ||
		cfg.InboundBurst != 10 ||
		cfg.MaxInvalidMessages != 5 {
		t.Fatalf("unexpected default handler config: %+v", cfg)
	}
}

func TestHandlerConfigInvalidValuesFallBackToDefaults(t *testing.T) {
	hub := NewHub(&fakeAuthorizer{}, slog.Default(), NopBus{}, "test-ws-config")
	defer hub.Shutdown()

	h := newWSHandler(
		hub,
		slog.Default(),
		&fakeWorkspaceResolver{id: "ws-config"},
		userIDFromCtxFn("user-config"),
		HandlerConfig{
			MaxConnectionsPerUser:    0,
			InboundMessagesPerMinute: -1,
			InboundBurst:             0,
			MaxInvalidMessages:       -1,
		},
	)

	if h.config != DefaultHandlerConfig() {
		t.Fatalf("expected invalid handler config to normalize to defaults, got %+v", h.config)
	}
	if h.limiter.max != DefaultHandlerConfig().MaxConnectionsPerUser {
		t.Fatalf("expected limiter max to use default, got %d", h.limiter.max)
	}
}

func TestServeWSWithConfig_NilLoggerUsesDefault(t *testing.T) {
	handler := ServeWSWithConfig(
		nil,
		nil,
		nil,
		nil,
		DefaultHandlerConfig(),
	)
	wsHandler, ok := handler.(*wsHandler)
	if !ok {
		t.Fatalf("expected *wsHandler, got %T", handler)
	}
	if wsHandler.logger == nil {
		t.Fatal("ServeWSWithConfig must normalize nil logger")
	}
}

func TestConsumeInboundTokenUsesConfiguredRate(t *testing.T) {
	start := time.Unix(100, 0)
	bucket := newInboundTokenBucket(HandlerConfig{
		InboundMessagesPerMinute: 120,
		InboundBurst:             2,
	})
	bucket.tokens = 0
	bucket.lastRefill = start

	if !consumeInboundToken(&bucket, start.Add(500*time.Millisecond)) {
		t.Fatal("expected 120 messages/minute to refill one token after 500ms")
	}
	if consumeInboundToken(&bucket, start.Add(500*time.Millisecond)) {
		t.Fatal("expected only one refilled token to be available")
	}
}

// TestServeWS_AuthenticatedConnection_RegistersClientInHub verifies that a
// successful WebSocket upgrade registers the client in the Hub.
func TestServeWS_AuthenticatedConnection_RegistersClientInHub(t *testing.T) {
	hub := NewHub(&fakeAuthorizer{}, slog.Default(), NopBus{}, "test-ws-inst")
	defer hub.Shutdown()

	ws := &fakeWorkspaceResolver{id: "ws-1"}
	srv := newTestWSServer(t, hub, ws, "user-a")

	conn := dialWS(t, srv)

	eventually(t, func() bool {
		hub.mu.RLock()
		defer hub.mu.RUnlock()
		for _, c := range hub.clients {
			if c.userID == "user-a" && c.workspaceID == "ws-1" {
				return true
			}
		}
		return false
	}, 2*time.Second, "client registered in hub")

	_ = conn.CloseNow()
}

func TestServeWS_JWTSubprotocolHandshakeEchoesTokenAndStaysOpen(t *testing.T) {
	hub := NewHub(&fakeAuthorizer{}, slog.Default(), NopBus{}, "test-ws-jwt-subprotocol")
	defer hub.Shutdown()

	srv := newTestWSServer(t, hub, &fakeWorkspaceResolver{id: "ws-jwt"}, "user-jwt")
	token := realisticAccessTokenSubprotocol(t)
	ctx, cancel := context.WithTimeout(context.Background(), testIOTimeout)
	defer cancel()
	url := "ws" + srv.URL[len("http"):]

	conn, resp, err := websocket.Dial(ctx, url, &websocket.DialOptions{
		Subprotocols: []string{token},
	})
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		t.Fatalf("dial ws with JWT subprotocol: %v", err)
	}
	t.Cleanup(func() { _ = conn.CloseNow() })
	if got := conn.Subprotocol(); got != token {
		t.Fatalf("server echoed subprotocol %q, want JWT token", got)
	}

	eventually(t, func() bool {
		hub.mu.RLock()
		defer hub.mu.RUnlock()
		for _, c := range hub.clients {
			if c.userID == "user-jwt" && c.workspaceID == "ws-jwt" {
				return true
			}
		}
		return false
	}, 2*time.Second, "JWT subprotocol client registered")

	ping, _ := json.Marshal(ClientMessage{Type: ClientMessageTypePing})
	if err := conn.Write(ctx, websocket.MessageText, ping); err != nil {
		t.Fatalf("connection should remain writable after JWT subprotocol handshake: %v", err)
	}
}

func TestServeWS_SubprotocolHeaderAbsent_UsesAuthenticatedContext(t *testing.T) {
	hub := NewHub(&fakeAuthorizer{}, slog.Default(), NopBus{}, "test-ws-no-subprotocol")
	defer hub.Shutdown()

	srv := newTestWSServer(t, hub, &fakeWorkspaceResolver{id: "ws-no-subprotocol"}, "user-no-subprotocol")
	conn := dialWS(t, srv)

	if got := conn.Subprotocol(); got != "" {
		t.Fatalf("server echoed unexpected subprotocol %q", got)
	}
	eventually(t, func() bool {
		hub.mu.RLock()
		defer hub.mu.RUnlock()
		for _, c := range hub.clients {
			if c.userID == "user-no-subprotocol" && c.workspaceID == "ws-no-subprotocol" {
				return true
			}
		}
		return false
	}, 2*time.Second, "client registered without subprotocol when auth context is present")
}

func TestServeWS_InvalidSubprotocolTokenReturns400(t *testing.T) {
	cases := []struct {
		name  string
		value string
	}{
		{name: "empty_value", value: ""},
		{name: "space", value: "bad token"},
		{name: "comma", value: "bad,token"},
		{name: "crlf", value: "bad\r\ntoken"},
		{name: "separator", value: "bad/token"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hub := NewHub(&fakeAuthorizer{}, slog.Default(), NopBus{}, "test-ws-invalid-subprotocol-"+tc.name)
			defer hub.Shutdown()

			req := httptest.NewRequest(http.MethodGet, "/ws", nil)
			req.Header.Set("Connection", "Upgrade")
			req.Header.Set("Upgrade", "websocket")
			req.Header.Set("Sec-WebSocket-Version", "13")
			req.Header.Set("Sec-WebSocket-Key", testSecWebSocketKey())
			req.Header["Sec-Websocket-Protocol"] = []string{tc.value}
			w := httptest.NewRecorder()

			ServeWS(hub, nil, &fakeWorkspaceResolver{id: "ws-invalid-subprotocol"}, userIDFromCtxFn("user-invalid-subprotocol")).
				ServeHTTP(w, req)

			if w.Code != http.StatusBadRequest {
				t.Fatalf("expected 400 for %q, got %d — body: %s", tc.value, w.Code, w.Body.String())
			}
		})
	}
}

// TestServeWS_ReadLoop_CallsHandleClientMessage verifies that a client message
// received over the WebSocket is dispatched to hub.handleClientMessage.
// We verify the side-effect: a subscribe message causes the client to be
// subscribed to the target in the hub.
func TestServeWS_ReadLoop_CallsHandleClientMessage(t *testing.T) {
	auth := &fakeAuthorizer{}
	auth.setAccess("user-b", "ws-2", TargetTypeChannel, "chan-1", true)

	hub := NewHub(auth, slog.Default(), NopBus{}, "test-ws-inst2")
	defer hub.Shutdown()

	ws := &fakeWorkspaceResolver{id: "ws-2"}
	srv := newTestWSServer(t, hub, ws, "user-b")

	conn := dialWS(t, srv)

	// Wait for client to be registered before sending a message.
	eventually(t, func() bool {
		hub.mu.RLock()
		defer hub.mu.RUnlock()
		for _, c := range hub.clients {
			if c.userID == "user-b" {
				return true
			}
		}
		return false
	}, 2*time.Second, "client registered before subscribe")

	// Send a subscribe control message.
	msg := ClientMessage{
		Type:       ClientMessageTypeSubscribe,
		TargetType: TargetTypeChannel,
		TargetID:   "chan-1",
	}
	data, _ := json.Marshal(msg)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
		t.Fatalf("send subscribe: %v", err)
	}

	key := targetKey{workspaceID: "ws-2", targetType: TargetTypeChannel, targetID: "chan-1"}.String()
	eventually(t, func() bool {
		hub.mu.RLock()
		defer hub.mu.RUnlock()
		for clientID := range hub.clientSubs {
			if _, ok := hub.clientSubs[clientID][key]; ok {
				return true
			}
		}
		return false
	}, 2*time.Second, "subscribe side-effect: client subscribed to channel")

	_ = conn.CloseNow()
}

// TestServeWS_ReadLoopExit_CancelsPumps verifies that when the client closes
// the connection, the pumps exit and the client is removed from the hub.
func TestServeWS_ReadLoopExit_CancelsPumps(t *testing.T) {
	hub := NewHub(&fakeAuthorizer{}, slog.Default(), NopBus{}, "test-ws-inst3")
	defer hub.Shutdown()

	ws := &fakeWorkspaceResolver{id: "ws-3"}
	srv := newTestWSServer(t, hub, ws, "user-c")

	conn := dialWS(t, srv)

	// Wait for client registration.
	eventually(t, func() bool {
		hub.mu.RLock()
		defer hub.mu.RUnlock()
		for _, c := range hub.clients {
			if c.userID == "user-c" {
				return true
			}
		}
		return false
	}, 2*time.Second, "client registered")

	// Close the client connection — the read loop should exit and clean up.
	_ = conn.CloseNow()

	eventually(t, func() bool {
		hub.mu.RLock()
		defer hub.mu.RUnlock()
		for _, c := range hub.clients {
			if c.userID == "user-c" {
				return false
			}
		}
		return true
	}, 2*time.Second, "client removed from hub after connection close")
}

// TestServeWS_WorkspaceError_Returns500 verifies that when workspace resolution
// fails the handler returns a non-101 error before any upgrade.
func TestServeWS_WorkspaceError_Returns500(t *testing.T) {
	hub := NewHub(&fakeAuthorizer{}, slog.Default(), NopBus{}, "test-ws-err")
	defer hub.Shutdown()

	fakeWS := &fakeWorkspaceResolver{err: errors.New("db unavailable")}
	handler := ServeWS(hub, nil, fakeWS, userIDFromCtxFn("user-x"))

	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code == http.StatusSwitchingProtocols {
		t.Fatal("must not upgrade when workspace resolution fails")
	}
	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 on workspace error, got %d", w.Code)
	}
}

// TestServeWS_CleanupIdempotent verifies that stop() can be called multiple
// times without panicking or leaking goroutines (idempotent cleanup).
func TestServeWS_CleanupIdempotent(t *testing.T) {
	hub := NewHub(&fakeAuthorizer{}, slog.Default(), NopBus{}, "test-ws-inst4")
	defer hub.Shutdown()

	ws := &fakeWorkspaceResolver{id: "ws-4"}
	srv := newTestWSServer(t, hub, ws, "user-d")

	conn := dialWS(t, srv)

	eventually(t, func() bool {
		hub.mu.RLock()
		defer hub.mu.RUnlock()
		for _, c := range hub.clients {
			if c.userID == "user-d" {
				return true
			}
		}
		return false
	}, 2*time.Second, "client registered")

	// Close twice; must not panic.
	_ = conn.CloseNow()
	_ = conn.CloseNow()

	eventually(t, func() bool {
		hub.mu.RLock()
		defer hub.mu.RUnlock()
		for _, c := range hub.clients {
			if c.userID == "user-d" {
				return false
			}
		}
		return true
	}, 2*time.Second, "client removed from hub after double close")
}

func TestServeWS_HubShutdownBeforeRegister_ClosesConnection(t *testing.T) {
	hub := NewHub(&fakeAuthorizer{}, slog.Default(), NopBus{}, "test-ws-shutdown")
	hub.Shutdown()

	srv := newTestWSServer(t, hub, &fakeWorkspaceResolver{id: "ws-shutdown"}, "user-shutdown")
	conn := dialWS(t, srv)

	ctx, cancel := context.WithTimeout(context.Background(), testIOTimeout)
	defer cancel()

	_, _, err := conn.Read(ctx)
	if err == nil {
		t.Fatal("expected server to close connection when hub registration fails")
	}
	if errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
		t.Fatalf("connection was not closed promptly after failed hub registration: %v", err)
	}
	hub.mu.RLock()
	clientCount := len(hub.clients)
	hub.mu.RUnlock()
	if clientCount != 0 {
		t.Fatal("client must not be tracked when registration races with shutdown")
	}
}

// ── resource control tests ────────────────────────────────────────────────────

// TestServeWS_ConnectionLimit_PerUser verifies that a user cannot open more
// than the configured maximum number of concurrent WebSocket connections.
// The (maxConns+1)th connection must be rejected with 429.
func TestServeWS_ConnectionLimit_PerUser(t *testing.T) {
	const limit = 2

	hub := NewHub(&fakeAuthorizer{}, slog.Default(), NopBus{}, "test-ws-limit")
	defer hub.Shutdown()

	cfg := DefaultHandlerConfig()
	cfg.MaxConnectionsPerUser = limit
	srv := newTestWSServerWithConfig(t, hub, &fakeWorkspaceResolver{id: "ws-lim"}, "user-lim", cfg)

	// Open limit connections — all must succeed.
	conns := make([]*websocket.Conn, limit)
	for i := range conns {
		conns[i] = dialWS(t, srv)
	}

	// Wait for all limit clients to be registered so the counter is accurate.
	eventually(t, func() bool {
		hub.mu.RLock()
		defer hub.mu.RUnlock()
		count := 0
		for _, c := range hub.clients {
			if c.userID == "user-lim" {
				count++
			}
		}
		return count == limit
	}, 2*time.Second, "all limit connections registered")

	// One more connection from the same user must be rejected with 429.
	ctx, cancel := context.WithTimeout(context.Background(), testIOTimeout)
	defer cancel()
	wsURL := "ws" + srv.URL[len("http"):]
	_, resp, err := websocket.Dial(ctx, wsURL, nil)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err == nil {
		t.Fatal("expected connection over limit to be rejected")
	}
	if resp == nil {
		t.Fatal("expected an HTTP response for the rejected connection")
	}
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("expected 429 Too Many Requests, got %d", resp.StatusCode)
	}

	for _, c := range conns {
		_ = c.CloseNow()
	}
}

// TestServeWS_ConnectionLimit_SlotReleasedOnClose verifies that after a
// connection closes, its slot is released and a new connection can be accepted.
func TestServeWS_ConnectionLimit_SlotReleasedOnClose(t *testing.T) {
	const limit = 1

	hub := NewHub(&fakeAuthorizer{}, slog.Default(), NopBus{}, "test-ws-slot")
	defer hub.Shutdown()

	cfg := DefaultHandlerConfig()
	cfg.MaxConnectionsPerUser = limit
	srv := newTestWSServerWithConfig(t, hub, &fakeWorkspaceResolver{id: "ws-slot"}, "user-slot", cfg)

	// Open and close the only allowed connection.
	conn := dialWS(t, srv)
	eventually(t, func() bool {
		hub.mu.RLock()
		defer hub.mu.RUnlock()
		for _, c := range hub.clients {
			if c.userID == "user-slot" {
				return true
			}
		}
		return false
	}, 2*time.Second, "first connection registered")
	_ = conn.CloseNow()

	// Wait for the slot to be released (client removed from hub → ServeHTTP returns).
	eventually(t, func() bool {
		hub.mu.RLock()
		defer hub.mu.RUnlock()
		for _, c := range hub.clients {
			if c.userID == "user-slot" {
				return false
			}
		}
		return true
	}, 2*time.Second, "first connection unregistered")

	// A new connection must now be accepted.
	conn2 := dialWS(t, srv)
	_ = conn2.CloseNow()
}

// TestServeWS_InboundRateLimit_ClosesConnection verifies that a connection
// sending more messages than the token bucket allows is closed by the server.
func TestServeWS_InboundRateLimit_ClosesConnection(t *testing.T) {
	hub := NewHub(&fakeAuthorizer{}, slog.Default(), NopBus{}, "test-ws-ratelimit")
	defer hub.Shutdown()

	// Use burst=1: the 2nd message exhausts the bucket (no seconds elapsed).
	cfg := DefaultHandlerConfig()
	cfg.InboundBurst = 1
	srv := newTestWSServerWithConfig(t, hub, &fakeWorkspaceResolver{id: "ws-rl"}, "user-rl", cfg)

	conn := dialWS(t, srv)

	eventually(t, func() bool {
		hub.mu.RLock()
		defer hub.mu.RUnlock()
		for _, c := range hub.clients {
			if c.userID == "user-rl" {
				return true
			}
		}
		return false
	}, 2*time.Second, "client registered before flood")

	ctx, cancel := context.WithTimeout(context.Background(), testIOTimeout)
	defer cancel()

	ping, _ := json.Marshal(ClientMessage{Type: ClientMessageTypePing})
	// First message consumes the only token; second should trigger rate limit.
	_ = conn.Write(ctx, websocket.MessageText, ping)
	_ = conn.Write(ctx, websocket.MessageText, ping)

	// Server must close the connection; the next read must return an error.
	_, _, err := conn.Read(ctx)
	if err == nil {
		t.Fatal("expected connection to be closed after rate limit exceeded")
	}
	if status := websocket.CloseStatus(err); status != websocket.StatusPolicyViolation {
		t.Fatalf("expected policy violation close status, got %v from %v", status, err)
	}
}

// TestServeWS_FloodInvalidMessages_ClosesConnection verifies that sending
// MaxInvalidMessages invalid-JSON messages causes the server to close the connection.
func TestServeWS_FloodInvalidMessages_ClosesConnection(t *testing.T) {
	hub := NewHub(&fakeAuthorizer{}, slog.Default(), NopBus{}, "test-ws-invalid")
	defer hub.Shutdown()

	cfg := DefaultHandlerConfig()
	cfg.MaxInvalidMessages = 2
	srv := newTestWSServerWithConfig(t, hub, &fakeWorkspaceResolver{id: "ws-inv"}, "user-inv", cfg)

	conn := dialWS(t, srv)

	eventually(t, func() bool {
		hub.mu.RLock()
		defer hub.mu.RUnlock()
		for _, c := range hub.clients {
			if c.userID == "user-inv" {
				return true
			}
		}
		return false
	}, 2*time.Second, "client registered before flood")

	ctx, cancel := context.WithTimeout(context.Background(), testIOTimeout)
	defer cancel()

	invalid := []byte("not-valid-json!!!")
	for i := 0; i < cfg.MaxInvalidMessages; i++ {
		if err := conn.Write(ctx, websocket.MessageText, invalid); err != nil {
			break // server may have already closed
		}
	}

	// Server must close after MaxInvalidMessages invalid messages.
	_, _, err := conn.Read(ctx)
	if err == nil {
		t.Fatal("expected connection to be closed after too many invalid messages")
	}
	if status := websocket.CloseStatus(err); status != websocket.StatusPolicyViolation {
		t.Fatalf("expected policy violation close status, got %v from %v", status, err)
	}
}

// TestServeWS_ValidMessages_DoNotIncrementInvalidCounter verifies that valid
// messages are processed normally and do not accumulate invalid-message count.
func TestServeWS_ValidMessages_DoNotIncrementInvalidCounter(t *testing.T) {
	auth := &fakeAuthorizer{}
	hub := NewHub(auth, slog.Default(), NopBus{}, "test-ws-valid")
	defer hub.Shutdown()

	cfg := DefaultHandlerConfig()
	srv := newTestWSServerWithConfig(t, hub, &fakeWorkspaceResolver{id: "ws-v"}, "user-v", cfg)

	conn := dialWS(t, srv)

	eventually(t, func() bool {
		hub.mu.RLock()
		defer hub.mu.RUnlock()
		for _, c := range hub.clients {
			if c.userID == "user-v" {
				return true
			}
		}
		return false
	}, 2*time.Second, "client registered")

	ctx, cancel := context.WithTimeout(context.Background(), testIOTimeout)
	defer cancel()

	// Send more than MaxInvalidMessages valid ping messages; connection must stay open.
	ping, _ := json.Marshal(ClientMessage{Type: ClientMessageTypePing})
	for i := 0; i < cfg.MaxInvalidMessages+2; i++ {
		if err := conn.Write(ctx, websocket.MessageText, ping); err != nil {
			t.Fatalf("write ping %d: %v", i, err)
		}
	}

	// Connection must still be alive — close from our side, not the server's.
	_ = conn.CloseNow()
}

// TestServeWS_WithPresence_OnlineAndOffline is an integrated test that verifies
// the full ServeWS → Hub → PresenceTracker lifecycle:
//
//  1. An authenticated connection sets presence to online via Hub.Register.
//  2. Closing the connection sets presence to offline via Hub.Unregister.
func TestServeWS_WithPresence_OnlineAndOffline(t *testing.T) {
	// Use a real PresenceTracker (with background goroutine) so that Connect/
	// Disconnect calls from the hub run goroutine are exercised end-to-end.
	p := NewPresenceTracker(time.Hour)
	t.Cleanup(p.Stop)

	hub := NewHub(&fakeAuthorizer{}, slog.Default(), NopBus{}, "test-ws-presence", WithPresence(p))
	defer hub.Shutdown()

	ws := &fakeWorkspaceResolver{id: "ws-presence"}
	srv := newTestWSServer(t, hub, ws, "user-presence")

	conn := dialWS(t, srv)

	// Connection must make user online.
	eventually(t, func() bool {
		return p.Status("ws-presence", "user-presence") == PresenceOnline
	}, 2*time.Second, "presence online after connect")

	// Close the connection; read loop exits → hub unregisters → presence offline.
	_ = conn.CloseNow()

	eventually(t, func() bool {
		return p.Status("ws-presence", "user-presence") == PresenceOffline
	}, 2*time.Second, "presence offline after disconnect")
}
