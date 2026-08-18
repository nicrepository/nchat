package ws

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
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

type blockingAllowAuthorizer struct {
	started chan struct{}
	release chan struct{}
}

func (a *blockingAllowAuthorizer) CanAccess(ctx context.Context, _, _ string, _ TargetType, _ string) (bool, error) {
	close(a.started)
	select {
	case <-a.release:
		return true, nil
	case <-ctx.Done():
		return false, ctx.Err()
	}
}

func (f *fakeWorkspaceResolver) GetDefaultWorkspaceID(_ context.Context) (string, error) {
	return f.id, f.err
}

// fakeUserDisplayNameResolver returns a fixed display name or error.
type fakeUserDisplayNameResolver struct {
	name string
	err  error
}

func (f *fakeUserDisplayNameResolver) GetDisplayName(_ context.Context, _ string) (string, error) {
	return f.name, f.err
}

func TestResolveDisplayName_NilResolver_ReturnsEmpty(t *testing.T) {
	got := resolveDisplayName(context.Background(), slog.Default(), nil, "user-1")
	if got != "" {
		t.Fatalf("got %q, want empty string for nil resolver", got)
	}
}

func TestResolveDisplayName_ResolverError_ReturnsEmptyNotFail(t *testing.T) {
	resolver := &fakeUserDisplayNameResolver{err: errors.New("db down")}
	got := resolveDisplayName(context.Background(), slog.Default(), resolver, "user-1")
	if got != "" {
		t.Fatalf("got %q, want empty string on resolver error (must degrade, not propagate)", got)
	}
}

func TestResolveDisplayName_Success_ReturnsName(t *testing.T) {
	resolver := &fakeUserDisplayNameResolver{name: "Bruno Lima"}
	got := resolveDisplayName(context.Background(), slog.Default(), resolver, "user-1")
	if got != "Bruno Lima" {
		t.Fatalf("got %q, want %q", got, "Bruno Lima")
	}
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

type subscribeAcknowledgement struct {
	Type       string     `json:"type"`
	Operation  string     `json:"operation"`
	TargetType TargetType `json:"target_type"`
	TargetID   string     `json:"target_id"`
}

func readSubscribeAcknowledgement(t *testing.T, ctx context.Context, conn *websocket.Conn) ([]byte, subscribeAcknowledgement) {
	t.Helper()
	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read subscribe acknowledgement: %v", err)
	}
	var acknowledgement subscribeAcknowledgement
	if err := json.Unmarshal(data, &acknowledgement); err != nil {
		t.Fatalf("decode subscribe acknowledgement: %v", err)
	}
	return data, acknowledgement
}

func TestNormalizeSubscriptionTarget_CanonicalizesAcceptedUUIDRepresentations(t *testing.T) {
	const canonicalID = "550e8400-e29b-41d4-a716-446655440000"
	tests := []struct {
		name     string
		targetID string
	}{
		{name: "canonical", targetID: canonicalID},
		{name: "uppercase", targetID: "550E8400-E29B-41D4-A716-446655440000"},
		{name: "without hyphens", targetID: "550e8400e29b41d4a716446655440000"},
		{name: "braced", targetID: "{550e8400-e29b-41d4-a716-446655440000}"},
		{name: "external spaces", targetID: " 550e8400-e29b-41d4-a716-446655440000\t"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target, err := normalizeSubscriptionTarget(ClientMessage{
				Type:       ClientMessageTypeSubscribe,
				TargetType: TargetTypeChannel,
				TargetID:   tt.targetID,
			})
			if err != nil {
				t.Fatalf("normalize subscription target: %v", err)
			}
			if target.targetType != TargetTypeChannel || target.targetID != canonicalID {
				t.Fatalf("unexpected normalized target: %+v", target)
			}
		})
	}
}

func TestServeWS_EquivalentTargetIDUsesCanonicalAuthorizationRoomAckAndEvents(t *testing.T) {
	const (
		workspaceID       = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
		userID            = "user-canonical-target"
		canonicalTargetID = "550e8400-e29b-41d4-a716-446655440000"
		uppercaseTargetID = "550E8400-E29B-41D4-A716-446655440000"
	)
	auth := &fakeAuthorizer{}
	auth.setAccess(userID, workspaceID, TargetTypeChannel, canonicalTargetID, true)
	hub := NewHub(auth, slog.Default(), NopBus{}, "canonical-target-instance")
	defer hub.Shutdown()
	srv := newTestWSServer(t, hub, &fakeWorkspaceResolver{id: workspaceID}, userID)
	conn := dialWS(t, srv)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	subscribe, err := json.Marshal(ClientMessage{
		Type:       ClientMessageTypeSubscribe,
		TargetType: TargetTypeChannel,
		TargetID:   uppercaseTargetID,
	})
	if err != nil {
		t.Fatalf("marshal subscribe: %v", err)
	}
	if err := conn.Write(ctx, websocket.MessageText, subscribe); err != nil {
		t.Fatalf("write subscribe: %v", err)
	}

	_, acknowledgement := readSubscribeAcknowledgement(t, ctx, conn)
	if acknowledgement.Type != "subscribed" || acknowledgement.TargetID != canonicalTargetID {
		t.Fatalf("unexpected canonical acknowledgement: %+v", acknowledgement)
	}
	if got := auth.lastTargetID(); got != canonicalTargetID {
		t.Fatalf("authorizer received target ID %q, want %q", got, canonicalTargetID)
	}

	canonicalKey := targetKey{
		workspaceID: workspaceID, targetType: TargetTypeChannel, targetID: canonicalTargetID,
	}.String()
	nonCanonicalKey := targetKey{
		workspaceID: workspaceID, targetType: TargetTypeChannel, targetID: uppercaseTargetID,
	}.String()
	if !hubHasSubscriptionTarget(hub, canonicalKey) {
		t.Fatal("canonical room key was not registered")
	}
	if hubHasSubscriptionTarget(hub, nonCanonicalKey) {
		t.Fatal("original non-canonical room key must not appear in hub state")
	}

	localMessageID := "11111111-1111-4111-8111-111111111111"
	hub.PublishMessageCreated(ctx, workspaceID, TargetTypeChannel, canonicalTargetID, MessagePayload{ID: localMessageID})
	_, localData, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read local canonical event: %v", err)
	}
	var localEvent Event
	if err := json.Unmarshal(localData, &localEvent); err != nil {
		t.Fatalf("decode local canonical event: %v", err)
	}
	if localEvent.TargetID != canonicalTargetID || localEvent.MessageID != localMessageID {
		t.Fatalf("unexpected local event: %+v", localEvent)
	}

	hub.handleRemoteBusEvent(Event{
		SchemaVersion:    CurrentEventSchemaVersion,
		Type:             EventTypeMessageCreated,
		WorkspaceID:      workspaceID,
		TargetType:       TargetTypeChannel,
		TargetID:         canonicalTargetID,
		MessageID:        "22222222-2222-4222-8222-222222222222",
		EventID:          "33333333-3333-4333-8333-333333333333",
		SourceInstanceID: "remote-instance",
		CreatedAt:        time.Now().UTC(),
	})
	_, remoteData, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read remote canonical event: %v", err)
	}
	var remoteEvent Event
	if err := json.Unmarshal(remoteData, &remoteEvent); err != nil {
		t.Fatalf("decode remote canonical event: %v", err)
	}
	if remoteEvent.TargetID != canonicalTargetID {
		t.Fatalf("unexpected remote event target: %+v", remoteEvent)
	}
}

func TestServeWS_EquivalentTargetIDsShareOneRoomAndReceiveSameEvent(t *testing.T) {
	const (
		workspaceID       = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
		userID            = "user-shared-canonical-room"
		canonicalTargetID = "550e8400-e29b-41d4-a716-446655440000"
		uppercaseTargetID = "550E8400-E29B-41D4-A716-446655440000"
	)
	auth := &fakeAuthorizer{}
	auth.setAccess(userID, workspaceID, TargetTypeChannel, canonicalTargetID, true)
	hub := NewHub(auth, slog.Default(), NopBus{}, "shared-canonical-room")
	defer hub.Shutdown()
	srv := newTestWSServer(t, hub, &fakeWorkspaceResolver{id: workspaceID}, userID)
	canonicalConn := dialWS(t, srv)
	equivalentConn := dialWS(t, srv)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	writeSubscribe := func(conn *websocket.Conn, targetID string) {
		t.Helper()
		data, err := json.Marshal(ClientMessage{
			Type: ClientMessageTypeSubscribe, TargetType: TargetTypeChannel, TargetID: targetID,
		})
		if err != nil {
			t.Fatalf("marshal subscribe: %v", err)
		}
		if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
			t.Fatalf("write subscribe: %v", err)
		}
		_, acknowledgement := readSubscribeAcknowledgement(t, ctx, conn)
		if acknowledgement.TargetID != canonicalTargetID {
			t.Fatalf("ack target = %q, want canonical %q", acknowledgement.TargetID, canonicalTargetID)
		}
	}

	writeSubscribe(canonicalConn, canonicalTargetID)
	writeSubscribe(equivalentConn, uppercaseTargetID)

	key := targetKey{workspaceID: workspaceID, targetType: TargetTypeChannel, targetID: canonicalTargetID}.String()
	nonCanonicalKey := targetKey{workspaceID: workspaceID, targetType: TargetTypeChannel, targetID: uppercaseTargetID}.String()
	hub.mu.RLock()
	roomCount := len(hub.subs)
	subscriberCount := len(hub.subs[key])
	_, hasNonCanonicalRoom := hub.subs[nonCanonicalKey]
	hub.mu.RUnlock()
	if roomCount != 1 || subscriberCount != 2 || hasNonCanonicalRoom {
		t.Fatalf("equivalent IDs split room state: rooms=%d subscribers=%d nonCanonical=%v", roomCount, subscriberCount, hasNonCanonicalRoom)
	}

	messageID := "11111111-1111-4111-8111-111111111111"
	hub.PublishMessageCreated(ctx, workspaceID, TargetTypeChannel, canonicalTargetID, MessagePayload{ID: messageID})
	for name, conn := range map[string]*websocket.Conn{"canonical": canonicalConn, "equivalent": equivalentConn} {
		_, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("%s client read event: %v", name, err)
		}
		var event Event
		if err := json.Unmarshal(data, &event); err != nil {
			t.Fatalf("%s client decode event: %v", name, err)
		}
		if event.TargetID != canonicalTargetID || event.MessageID != messageID {
			t.Fatalf("%s client received unexpected event: %+v", name, event)
		}
	}
}

func TestServeWS_EquivalentSubscribeIsIdempotentAndUnsubscribeUsesCanonicalRoom(t *testing.T) {
	const (
		workspaceID       = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
		userID            = "user-idempotent-canonical-room"
		canonicalTargetID = "550e8400-e29b-41d4-a716-446655440000"
		uppercaseTargetID = "550E8400-E29B-41D4-A716-446655440000"
	)
	for _, order := range []struct {
		name   string
		first  string
		second string
	}{
		{name: "canonical then equivalent", first: canonicalTargetID, second: uppercaseTargetID},
		{name: "equivalent then canonical", first: uppercaseTargetID, second: canonicalTargetID},
	} {
		t.Run(order.name, func(t *testing.T) {
			auth := &fakeAuthorizer{}
			auth.setAccess(userID, workspaceID, TargetTypeChannel, canonicalTargetID, true)
			hub := NewHub(auth, slog.Default(), NopBus{}, "idempotent-canonical-room")
			defer hub.Shutdown()
			srv := newTestWSServer(t, hub, &fakeWorkspaceResolver{id: workspaceID}, userID)
			conn := dialWS(t, srv)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			write := func(messageType ClientMessageType, targetID string) {
				t.Helper()
				data, err := json.Marshal(ClientMessage{
					Type: messageType, TargetType: TargetTypeChannel, TargetID: targetID,
				})
				if err != nil {
					t.Fatalf("marshal subscription command: %v", err)
				}
				if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
					t.Fatalf("write subscription command: %v", err)
				}
			}

			write(ClientMessageTypeSubscribe, order.first)
			readSubscribeAcknowledgement(t, ctx, conn)
			write(ClientMessageTypeSubscribe, order.second)
			readSubscribeAcknowledgement(t, ctx, conn)

			key := targetKey{workspaceID: workspaceID, targetType: TargetTypeChannel, targetID: canonicalTargetID}.String()
			hub.mu.RLock()
			roomCount := len(hub.subs)
			subscriberCount := len(hub.subs[key])
			clientRoomCounts := make([]int, 0, len(hub.clientSubs))
			for _, subscriptions := range hub.clientSubs {
				clientRoomCounts = append(clientRoomCounts, len(subscriptions))
			}
			hub.mu.RUnlock()
			if roomCount != 1 || subscriberCount != 1 || len(clientRoomCounts) != 1 || clientRoomCounts[0] != 1 {
				t.Fatalf("equivalent resubscribe duplicated state: rooms=%d subscribers=%d clientRooms=%v", roomCount, subscriberCount, clientRoomCounts)
			}

			write(ClientMessageTypeUnsubscribe, uppercaseTargetID)
			eventually(t, func() bool { return !hubHasSubscriptionTarget(hub, key) }, testIOTimeout,
				"equivalent unsubscribe should remove canonical subscription")
		})
	}
}

func TestServeWS_SubscribeDenied_ReturnsGenericErrorAndNoRoomEvents(t *testing.T) {
	const (
		workspaceID = "ws-room-auth"
		userID      = "user-room-auth"
		channelID   = "11111111-1111-4111-8111-111111111111"
	)

	hub := NewHub(&fakeAuthorizer{}, slog.Default(), NopBus{}, "test-ws-room-auth")
	defer hub.Shutdown()

	srv := newTestWSServer(t, hub, &fakeWorkspaceResolver{id: workspaceID}, userID)
	conn := dialWS(t, srv)

	ctx, cancel := context.WithTimeout(context.Background(), testIOTimeout)
	defer cancel()

	subscribe, err := json.Marshal(ClientMessage{
		Type:       ClientMessageTypeSubscribe,
		TargetType: TargetTypeChannel,
		TargetID:   channelID,
	})
	if err != nil {
		t.Fatalf("marshal subscribe: %v", err)
	}
	if err := conn.Write(ctx, websocket.MessageText, subscribe); err != nil {
		t.Fatalf("write subscribe: %v", err)
	}

	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read subscribe denial: %v", err)
	}
	var response clientErrorResponse
	if err := json.Unmarshal(data, &response); err != nil {
		t.Fatalf("decode subscribe denial: %v", err)
	}
	if response.Type != "error" || response.Operation != "subscribe" || response.Code != "room_access_denied" {
		t.Fatalf("unexpected subscribe denial: %+v", response)
	}

	key := targetKey{workspaceID: workspaceID, targetType: TargetTypeChannel, targetID: channelID}.String()
	if hubHasSubscriptionTarget(hub, key) {
		t.Fatal("denied client must not be registered in the room")
	}

	hub.PublishMessageCreated(context.Background(), workspaceID, TargetTypeChannel, channelID, MessagePayload{ID: "message-private"})

	noEventCtx, noEventCancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer noEventCancel()
	if _, _, err := conn.Read(noEventCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("denied socket must receive no room events, got: %v", err)
	}
}

func TestServeWS_SubscribeDeniedThenAllowedOnSameSocket(t *testing.T) {
	const (
		workspaceID = "ws-denied-then-allowed"
		userID      = "user-denied-then-allowed"
		deniedID    = "66666666-6666-4666-8666-666666666666"
		allowedID   = "77777777-7777-4777-8777-777777777777"
	)
	auth := &fakeAuthorizer{}
	auth.setAccess(userID, workspaceID, TargetTypeChannel, allowedID, true)
	hub := NewHub(auth, slog.Default(), NopBus{}, "test-ws-denied-then-allowed")
	defer hub.Shutdown()

	srv := newTestWSServer(t, hub, &fakeWorkspaceResolver{id: workspaceID}, userID)
	conn := dialWS(t, srv)
	ctx, cancel := context.WithTimeout(context.Background(), testIOTimeout)
	defer cancel()

	writeSubscribe := func(targetID string) {
		t.Helper()
		data, err := json.Marshal(ClientMessage{
			Type: ClientMessageTypeSubscribe, TargetType: TargetTypeChannel, TargetID: targetID,
		})
		if err != nil {
			t.Fatalf("marshal subscribe: %v", err)
		}
		if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
			t.Fatalf("write subscribe: %v", err)
		}
	}

	writeSubscribe(deniedID)
	_, denial, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read denial: %v", err)
	}
	var response clientErrorResponse
	if err := json.Unmarshal(denial, &response); err != nil {
		t.Fatalf("decode denial: %v", err)
	}
	if response.Operation != "subscribe" || response.Code != "room_access_denied" {
		t.Fatalf("unexpected denial: %+v", response)
	}

	writeSubscribe(allowedID)
	_, acknowledgement := readSubscribeAcknowledgement(t, ctx, conn)
	if acknowledgement.Type != "subscribed" || acknowledgement.Operation != "subscribe" ||
		acknowledgement.TargetType != TargetTypeChannel || acknowledgement.TargetID != allowedID {
		t.Fatalf("unexpected subscribe acknowledgement: %+v", acknowledgement)
	}
	allowedKey := targetKey{workspaceID: workspaceID, targetType: TargetTypeChannel, targetID: allowedID}.String()
	eventually(t, func() bool { return hubHasSubscriptionTarget(hub, allowedKey) }, testIOTimeout, "allowed room subscription")
	hub.PublishMessageCreated(context.Background(), workspaceID, TargetTypeChannel, allowedID, MessagePayload{
		ID: "88888888-8888-4888-8888-888888888888",
	})

	_, eventData, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read allowed room event: %v", err)
	}
	var event Event
	if err := json.Unmarshal(eventData, &event); err != nil {
		t.Fatalf("decode allowed room event: %v", err)
	}
	if event.Type != EventTypeMessageCreated || event.TargetID != allowedID {
		t.Fatalf("unexpected allowed room event: %+v", event)
	}
}

func TestServeWS_RepeatedSubscribeAuthorizationErrorFailsClosed(t *testing.T) {
	const (
		workspaceID = "ws-room-error"
		userID      = "user-room-error"
		dmID        = "22222222-2222-4222-8222-222222222222"
	)

	auth := &fakeAuthorizer{}
	auth.setErr(userID, workspaceID, TargetTypeDM, dmID, errors.New("database unavailable"))
	hub := NewHub(auth, slog.Default(), NopBus{}, "test-ws-room-error")
	defer hub.Shutdown()

	srv := newTestWSServer(t, hub, &fakeWorkspaceResolver{id: workspaceID}, userID)
	conn := dialWS(t, srv)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	subscribe, err := json.Marshal(ClientMessage{
		Type: ClientMessageTypeSubscribe, TargetType: TargetTypeDM, TargetID: dmID,
	})
	if err != nil {
		t.Fatalf("marshal subscribe: %v", err)
	}

	for attempt := 0; attempt < DefaultHandlerConfig().MaxInvalidMessages+1; attempt++ {
		if err := conn.Write(ctx, websocket.MessageText, subscribe); err != nil {
			t.Fatalf("write subscribe attempt %d: %v", attempt, err)
		}
		_, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("read subscribe denial attempt %d: %v", attempt, err)
		}
		var response clientErrorResponse
		if err := json.Unmarshal(data, &response); err != nil {
			t.Fatalf("decode subscribe denial attempt %d: %v", attempt, err)
		}
		if response.Type != "error" || response.Operation != "subscribe" || response.Code != "room_subscription_unavailable" {
			t.Fatalf("attempt %d returned %+v", attempt, response)
		}
		if strings.Contains(string(data), "database unavailable") {
			t.Fatalf("technical details leaked in subscribe response: %s", data)
		}
	}

	key := targetKey{workspaceID: workspaceID, targetType: TargetTypeDM, targetID: dmID}.String()
	if hubHasSubscriptionTarget(hub, key) {
		t.Fatal("authorization errors must never create a room subscription")
	}
}

func TestValidateSubscribe(t *testing.T) {
	validTargetID := "33333333-3333-4333-8333-333333333333"
	tests := []struct {
		name string
		msg  ClientMessage
	}{
		{name: "wrong message type", msg: ClientMessage{Type: ClientMessageTypePing, TargetType: TargetTypeChannel, TargetID: validTargetID}},
		{name: "missing target type", msg: ClientMessage{Type: ClientMessageTypeSubscribe, TargetID: validTargetID}},
		{name: "blank target type", msg: ClientMessage{Type: ClientMessageTypeSubscribe, TargetType: TargetType("   "), TargetID: validTargetID}},
		{name: "unknown target type", msg: ClientMessage{Type: ClientMessageTypeSubscribe, TargetType: TargetType("thread"), TargetID: validTargetID}},
		{name: "missing target id", msg: ClientMessage{Type: ClientMessageTypeSubscribe, TargetType: TargetTypeChannel}},
		{name: "blank target id", msg: ClientMessage{Type: ClientMessageTypeSubscribe, TargetType: TargetTypeChannel, TargetID: "   "}},
		{name: "invalid target id", msg: ClientMessage{Type: ClientMessageTypeSubscribe, TargetType: TargetTypeDM, TargetID: "not-a-uuid"}},
		{name: "unexpected reaction fields", msg: ClientMessage{Type: ClientMessageTypeSubscribe, TargetType: TargetTypeChannel, TargetID: validTargetID, MessageID: validTargetID}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateSubscribe(tt.msg); err == nil {
				t.Fatal("expected invalid subscribe payload")
			}
		})
	}

	for _, targetType := range []TargetType{TargetTypeChannel, TargetTypeDM} {
		if err := validateSubscribe(ClientMessage{
			Type:       ClientMessageTypeSubscribe,
			TargetType: targetType,
			TargetID:   validTargetID,
		}); err != nil {
			t.Fatalf("valid %s subscribe rejected: %v", targetType, err)
		}
	}
}

func TestServeWS_InvalidSubscribeDoesNotAuthorizeAndConsumesInvalidBudget(t *testing.T) {
	auth := &fakeAuthorizer{}
	hub := NewHub(auth, slog.Default(), NopBus{}, "test-ws-invalid-subscribe")
	defer hub.Shutdown()

	cfg := DefaultHandlerConfig()
	cfg.MaxInvalidMessages = 2
	srv := newTestWSServerWithConfig(
		t,
		hub,
		&fakeWorkspaceResolver{id: "ws-invalid-subscribe"},
		"user-invalid-subscribe",
		cfg,
	)
	conn := dialWS(t, srv)

	ctx, cancel := context.WithTimeout(context.Background(), testIOTimeout)
	defer cancel()
	invalid := []byte(`{"type":"subscribe","target_type":"channel","target_id":"not-a-uuid"}`)
	for attempt := 0; attempt < cfg.MaxInvalidMessages; attempt++ {
		if err := conn.Write(ctx, websocket.MessageText, invalid); err != nil {
			break
		}
	}

	_, _, err := conn.Read(ctx)
	if err == nil {
		t.Fatal("expected invalid subscribe flood to close the connection")
	}
	if status := websocket.CloseStatus(err); status != websocket.StatusPolicyViolation {
		t.Fatalf("expected policy violation close status, got %v from %v", status, err)
	}
	if got := auth.callCount(); got != 0 {
		t.Fatalf("invalid subscribe must not call authorizer/storage, got %d calls", got)
	}
	hub.mu.RLock()
	clientSubCount := 0
	for _, subscriptions := range hub.clientSubs {
		clientSubCount += len(subscriptions)
	}
	hub.mu.RUnlock()
	if clientSubCount != 0 {
		t.Fatalf("invalid subscribe must not add room subscriptions, got %d", clientSubCount)
	}
}

func TestServeWS_SubscribeDenialDoesNotConsumeInvalidBudget(t *testing.T) {
	const targetID = "55555555-5555-4555-8555-555555555555"
	auth := &fakeAuthorizer{}
	hub := NewHub(auth, slog.Default(), NopBus{}, "test-ws-denial-budget")
	defer hub.Shutdown()

	cfg := DefaultHandlerConfig()
	cfg.MaxInvalidMessages = 1
	srv := newTestWSServerWithConfig(
		t,
		hub,
		&fakeWorkspaceResolver{id: "ws-denial-budget"},
		"user-denial-budget",
		cfg,
	)
	conn := dialWS(t, srv)
	ctx, cancel := context.WithTimeout(context.Background(), testIOTimeout)
	defer cancel()

	subscribe, err := json.Marshal(ClientMessage{
		Type:       ClientMessageTypeSubscribe,
		TargetType: TargetTypeChannel,
		TargetID:   targetID,
	})
	if err != nil {
		t.Fatalf("marshal subscribe: %v", err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := conn.Write(ctx, websocket.MessageText, subscribe); err != nil {
			t.Fatalf("write denied subscribe %d: %v", attempt, err)
		}
		_, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("denial %d unexpectedly closed the connection: %v", attempt, err)
		}
		var response clientErrorResponse
		if err := json.Unmarshal(data, &response); err != nil {
			t.Fatalf("decode denial %d: %v", attempt, err)
		}
		if response.Operation != "subscribe" || response.Code != "room_access_denied" {
			t.Fatalf("unexpected denial %d: %+v", attempt, response)
		}
	}
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
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte("01234567890123456789012345678901")) //nolint:gosec // test-only fake HMAC secret
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

	// Compared field by field: the config now carries a function, which Go will
	// not compare, and the defaulting is what this asserts.
	defaults := DefaultHandlerConfig()
	if h.config.MaxConnectionsPerUser != defaults.MaxConnectionsPerUser ||
		h.config.InboundMessagesPerMinute != defaults.InboundMessagesPerMinute ||
		h.config.InboundBurst != defaults.InboundBurst ||
		h.config.MaxInvalidMessages != defaults.MaxInvalidMessages ||
		h.config.SessionRevalidateInterval != defaults.SessionRevalidateInterval {
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

func TestServeWS_JWTSubprotocolHandshake_SelectsOnlySafeProtocol(t *testing.T) {
	hub := NewHub(&fakeAuthorizer{}, slog.Default(), NopBus{}, "test-ws-jwt-subprotocol")
	defer hub.Shutdown()

	srv := newTestWSServer(t, hub, &fakeWorkspaceResolver{id: "ws-jwt"}, "user-jwt")
	token := realisticAccessTokenSubprotocol(t)
	ctx, cancel := context.WithTimeout(context.Background(), testIOTimeout)
	defer cancel()
	url := "ws" + srv.URL[len("http"):]

	conn, resp, err := websocket.Dial(ctx, url, &websocket.DialOptions{
		Subprotocols: []string{token, "nchat.v1"},
	})
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		t.Fatalf("dial ws with JWT subprotocol: %v", err)
	}
	t.Cleanup(func() { _ = conn.CloseNow() })

	if got := conn.Subprotocol(); got != "nchat.v1" {
		t.Fatalf("server must select only the safe protocol, got %q", got)
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
	const channelID = "44444444-4444-4444-8444-444444444444"
	auth := &fakeAuthorizer{}
	auth.setAccess("user-b", "ws-2", TargetTypeChannel, channelID, true)

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
		TargetID:   channelID,
	}
	data, _ := json.Marshal(msg)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
		t.Fatalf("send subscribe: %v", err)
	}

	rawAcknowledgement, acknowledgement := readSubscribeAcknowledgement(t, ctx, conn)
	if acknowledgement.Type != "subscribed" || acknowledgement.Operation != "subscribe" {
		t.Fatalf("unexpected subscribe acknowledgement: %+v", acknowledgement)
	}
	if acknowledgement.TargetType != TargetTypeChannel || acknowledgement.TargetID != channelID {
		t.Fatalf("acknowledgement target mismatch: %+v", acknowledgement)
	}
	for _, sensitiveField := range []string{"workspace_id", "user_id", "room_name", "members", "role"} {
		if strings.Contains(string(rawAcknowledgement), sensitiveField) {
			t.Fatalf("subscribe acknowledgement leaked %q: %s", sensitiveField, rawAcknowledgement)
		}
	}

	key := targetKey{workspaceID: "ws-2", targetType: TargetTypeChannel, targetID: channelID}.String()
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

func TestServeWS_RepeatedAuthorizedSubscribeAcknowledgesWithoutDuplicateState(t *testing.T) {
	const (
		workspaceID = "ws-idempotent-ack"
		userID      = "user-idempotent-ack"
		channelID   = "99999999-9999-4999-8999-999999999999"
	)
	auth := &fakeAuthorizer{}
	auth.setAccess(userID, workspaceID, TargetTypeChannel, channelID, true)
	hub := NewHub(auth, slog.Default(), NopBus{}, "test-ws-idempotent-ack")
	defer hub.Shutdown()
	srv := newTestWSServer(t, hub, &fakeWorkspaceResolver{id: workspaceID}, userID)
	conn := dialWS(t, srv)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	subscribe, err := json.Marshal(ClientMessage{
		Type: ClientMessageTypeSubscribe, TargetType: TargetTypeChannel, TargetID: channelID,
	})
	if err != nil {
		t.Fatalf("marshal subscribe: %v", err)
	}

	for attempt := 0; attempt < 2; attempt++ {
		if err := conn.Write(ctx, websocket.MessageText, subscribe); err != nil {
			t.Fatalf("write subscribe %d: %v", attempt, err)
		}
		_, acknowledgement := readSubscribeAcknowledgement(t, ctx, conn)
		if acknowledgement.Type != "subscribed" || acknowledgement.TargetID != channelID {
			t.Fatalf("unexpected acknowledgement %d: %+v", attempt, acknowledgement)
		}
	}

	key := targetKey{workspaceID: workspaceID, targetType: TargetTypeChannel, targetID: channelID}.String()
	hub.mu.RLock()
	subscriberCount := len(hub.subs[key])
	clientRoomCount := 0
	for _, subscriptions := range hub.clientSubs {
		clientRoomCount += len(subscriptions)
	}
	hub.mu.RUnlock()
	if subscriberCount != 1 || clientRoomCount != 1 {
		t.Fatalf("idempotent subscribe duplicated state: subscribers=%d client_rooms=%d", subscriberCount, clientRoomCount)
	}
}

func TestServeWS_ClientRemovedDuringAuthorizationReceivesNoAcknowledgement(t *testing.T) {
	const (
		workspaceID = "ws-removed-during-auth"
		userID      = "user-removed-during-auth"
		channelID   = "99999999-9999-4999-8999-999999999999"
	)
	auth := &blockingAllowAuthorizer{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	defer func() {
		select {
		case <-auth.release:
		default:
			close(auth.release)
		}
	}()
	hub := NewHub(auth, slog.Default(), NopBus{}, "test-ws-removed-during-auth")
	defer hub.Shutdown()

	srv := newTestWSServer(t, hub, &fakeWorkspaceResolver{id: workspaceID}, userID)
	conn := dialWS(t, srv)
	ctx, cancel := context.WithTimeout(context.Background(), testIOTimeout)
	defer cancel()

	subscribe, err := json.Marshal(ClientMessage{
		Type: ClientMessageTypeSubscribe, TargetType: TargetTypeChannel, TargetID: channelID,
	})
	if err != nil {
		t.Fatalf("marshal subscribe: %v", err)
	}
	if err := conn.Write(ctx, websocket.MessageText, subscribe); err != nil {
		t.Fatalf("write subscribe: %v", err)
	}

	select {
	case <-auth.started:
	case <-ctx.Done():
		t.Fatal("authorizer was not called")
	}

	hub.mu.RLock()
	var client *Client
	for _, registered := range hub.clients {
		client = registered
		break
	}
	hub.mu.RUnlock()
	if client == nil {
		t.Fatal("expected registered websocket client")
	}
	hub.Unregister(client)
	eventually(t, func() bool { return !hubHasClient(hub, client.id) }, testIOTimeout, "real client unregister")
	close(auth.release)

	readCtx, readCancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer readCancel()
	if _, data, readErr := conn.Read(readCtx); readErr == nil {
		var acknowledgement subscribeAcknowledgement
		if json.Unmarshal(data, &acknowledgement) == nil && acknowledgement.Type == "subscribed" {
			t.Fatalf("removed client must not receive subscribe acknowledgement: %+v", acknowledgement)
		}
	}

	key := targetKey{workspaceID: workspaceID, targetType: TargetTypeChannel, targetID: channelID}.String()
	if hubHasSubscriptionTarget(hub, key) {
		t.Fatal("removed client must not be registered in the room")
	}
}

func TestServeWS_SlowAuthorizationDoesNotBlockAnotherClientsAcknowledgement(t *testing.T) {
	const (
		workspaceID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
		userID      = "user-concurrent-subscribe"
		slowID      = "11111111-1111-4111-8111-111111111111"
		readyID     = "22222222-2222-4222-8222-222222222222"
	)
	auth := newBlockingTargetAuthorizer(slowID)
	defer auth.unblock()
	hub := NewHub(auth, slog.Default(), NopBus{}, "concurrent-client-subscribe")
	defer hub.Shutdown()
	srv := newTestWSServer(t, hub, &fakeWorkspaceResolver{id: workspaceID}, userID)
	slowConn := dialWS(t, srv)
	readyConn := dialWS(t, srv)
	ctx, cancel := context.WithTimeout(context.Background(), testIOTimeout)
	defer cancel()

	writeSubscribe := func(conn *websocket.Conn, targetID string) {
		t.Helper()
		data, err := json.Marshal(ClientMessage{
			Type: ClientMessageTypeSubscribe, TargetType: TargetTypeChannel, TargetID: targetID,
		})
		if err != nil {
			t.Fatalf("marshal subscribe: %v", err)
		}
		if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
			t.Fatalf("write subscribe: %v", err)
		}
	}

	writeSubscribe(slowConn, slowID)
	select {
	case <-auth.started:
	case <-ctx.Done():
		t.Fatal("slow authorization did not start")
	}

	writeSubscribe(readyConn, readyID)
	_, readyAcknowledgement := readSubscribeAcknowledgement(t, ctx, readyConn)
	if readyAcknowledgement.TargetID != readyID {
		t.Fatalf("other client acknowledgement = %+v", readyAcknowledgement)
	}

	auth.unblock()
	_, slowAcknowledgement := readSubscribeAcknowledgement(t, ctx, slowConn)
	if slowAcknowledgement.TargetID != slowID {
		t.Fatalf("slow client acknowledgement = %+v", slowAcknowledgement)
	}
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

// TestServeWS_InboundRateLimit_ClosesConnectionAtConfiguredBurst verifies, for
// each configured burst, that exactly that many immediate messages are
// consumed and the next one is rejected with a policy-violation close (1008).
// The burst=60 case matches the WS_INBOUND_BURST used by nchat-dev-server
// (issue #455): InboundMessagesPerMinute is pinned to its default 60 in both
// cases to prove burst and sustained rate are independent settings.
func TestServeWS_InboundRateLimit_ClosesConnectionAtConfiguredBurst(t *testing.T) {
	tests := []struct {
		name  string
		burst int
	}{
		{name: "burst_1", burst: 1},
		{name: "burst_60_matches_nchat_dev_server", burst: 60},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hub := NewHub(&fakeAuthorizer{}, slog.Default(), NopBus{}, "test-ws-ratelimit-"+tt.name)
			defer hub.Shutdown()

			cfg := DefaultHandlerConfig()
			cfg.InboundBurst = tt.burst
			cfg.InboundMessagesPerMinute = 60 // sustained rate stays at its default in every case
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
			// Consume exactly the configured burst with no measurable time elapsed
			// between writes, so refill contributes no extra token.
			for i := 0; i < tt.burst; i++ {
				if err := conn.Write(ctx, websocket.MessageText, ping); err != nil {
					t.Fatalf("write message %d of %d within burst: %v", i+1, tt.burst, err)
				}
			}
			// One more than the burst must exhaust the bucket and close the connection.
			_ = conn.Write(ctx, websocket.MessageText, ping)

			_, _, err := conn.Read(ctx)
			if err == nil {
				t.Fatal("expected connection to be closed after rate limit exceeded")
			}
			if status := websocket.CloseStatus(err); status != websocket.StatusPolicyViolation {
				t.Fatalf("expected policy violation close status (1008), got %v from %v", status, err)
			}
		})
	}
}

// TestServeWS_BootstrapBurst_CallSyncPlusSubscribesDoesNotTriggerRateLimit is a
// regression test for issue #455: chatSocket's bootstrap sends 1 call.sync +
// 12 subscribe messages immediately after open (13 messages). With the default
// InboundBurst=10 this exceeded the bucket and closed the connection with
// 1008, causing the client to reconnect and repeat the same burst forever.
// WS_INBOUND_BURST=60 (used by nchat-dev-server) must accept the whole burst
// without closing, while the sustained messages-per-minute rate is unchanged.
func TestServeWS_BootstrapBurst_CallSyncPlusSubscribesDoesNotTriggerRateLimit(t *testing.T) {
	const (
		workspaceID = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
		userID      = "user-bootstrap"
	)
	auth := &fakeAuthorizer{}
	targetIDs := make([]string, 12)
	for i := range targetIDs {
		targetIDs[i] = "550e8400-e29b-41d4-a716-44665544" + fmt.Sprintf("%04d", i)
		auth.setAccess(userID, workspaceID, TargetTypeChannel, targetIDs[i], true)
	}

	hub := NewHub(auth, slog.Default(), NopBus{}, "test-ws-bootstrap")
	defer hub.Shutdown()

	cfg := DefaultHandlerConfig()
	cfg.InboundBurst = 60 // matches WS_INBOUND_BURST in infra/k8s overlays/nchat-dev-server
	srv := newTestWSServerWithConfig(t, hub, &fakeWorkspaceResolver{id: workspaceID}, userID, cfg)
	conn := dialWS(t, srv)

	ctx, cancel := context.WithTimeout(context.Background(), testIOTimeout)
	defer cancel()

	callSync, err := json.Marshal(ClientMessage{Type: ClientMessageTypeCallSync})
	if err != nil {
		t.Fatalf("marshal call.sync: %v", err)
	}
	if writeErr := conn.Write(ctx, websocket.MessageText, callSync); writeErr != nil {
		t.Fatalf("write call.sync: %v", writeErr)
	}
	for _, targetID := range targetIDs {
		subscribe, marshalErr := json.Marshal(ClientMessage{
			Type: ClientMessageTypeSubscribe, TargetType: TargetTypeChannel, TargetID: targetID,
		})
		if marshalErr != nil {
			t.Fatalf("marshal subscribe: %v", marshalErr)
		}
		if writeErr := conn.Write(ctx, websocket.MessageText, subscribe); writeErr != nil {
			t.Fatalf("write subscribe: %v", writeErr)
		}
	}

	subscribedCount := 0
	for i := 0; i < len(targetIDs)+1; i++ {
		_, data, readErr := conn.Read(ctx)
		if readErr != nil {
			t.Fatalf("bootstrap burst closed the connection unexpectedly (message %d): %v", i, readErr)
		}
		var ack subscribeAcknowledgement
		if json.Unmarshal(data, &ack) == nil && ack.Type == "subscribed" {
			subscribedCount++
		}
	}
	if subscribedCount != len(targetIDs) {
		t.Fatalf("expected %d subscribe acknowledgements, got %d", len(targetIDs), subscribedCount)
	}

	// The connection and its token bucket must still process further inbound
	// messages after the burst, up to the sustained rate.
	ping, _ := json.Marshal(ClientMessage{Type: ClientMessageTypePing})
	if writeErr := conn.Write(ctx, websocket.MessageText, ping); writeErr != nil {
		t.Fatalf("write ping after bootstrap burst: %v", writeErr)
	}
	eventually(t, func() bool {
		hub.mu.RLock()
		defer hub.mu.RUnlock()
		for _, c := range hub.clients {
			if c.userID == userID {
				return true
			}
		}
		return false
	}, 2*time.Second, "connection remains open and registered after bootstrap burst")
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

// ── ServeWS handler-level auth gating tests (real TCP, simulated auth) ───────
//
// These tests exercise ServeWS's own auth gate (credential-in-query-string
// check and userIDFromCtx gating) over a real TCP connection. They use
// authCheckingUserIDFn, which simulates the auth contract by inspecting the
// Authorization header directly — NOT a real JWT validator. JWT validation is
// covered separately in internal/http/router_test.go via NewRouter + real
// TokenValidator + RequireActiveSession.
//
// Limitation: these tests do not exercise BearerAuth or RequireActiveSession.
// They validate that ServeWS correctly gates on userIDFromCtx returning "" and
// that credential-in-URL is rejected before any upgrade. Full JWT integration
// is tested in TestNewRouter_WS_* in router_test.go.

// authCheckingUserIDFn returns a userIDFromCtx function that inspects the
// Authorization header and returns userID only when it carries validToken.
// All other requests return "" (unauthenticated).
func authCheckingUserIDFn(validToken, userID string) func(*http.Request) string {
	want := "Bearer " + validToken
	return func(r *http.Request) string {
		if r.Header.Get("Authorization") == want {
			return userID
		}
		return ""
	}
}

// dialWSExpectFailure dials the given wsURL and asserts that the dial fails
// (i.e. the connection is rejected). It returns the HTTP response status code
// if a response was received, or -1 if no response was returned.
func dialWSExpectFailure(t *testing.T, wsURL string, opts *websocket.DialOptions) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testIOTimeout)
	defer cancel()
	conn, resp, err := websocket.Dial(ctx, wsURL, opts)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err == nil {
		_ = conn.CloseNow()
		t.Fatal("expected dial to fail (connection should be rejected)")
	}
	if resp == nil {
		return -1
	}
	return resp.StatusCode
}

// TestServeWS_RealServer_NoAuth_ConnectionRejected verifies that a WebSocket
// dial to a real server with no Authorization header is rejected with 401.
func TestServeWS_RealServer_NoAuth_ConnectionRejected(t *testing.T) {
	hub := NewHub(&fakeAuthorizer{}, slog.Default(), NopBus{}, "test-ws-realauth-noauth")
	defer hub.Shutdown()

	handler := ServeWS(hub, slog.Default(), &fakeWorkspaceResolver{id: "ws-realauth"},
		authCheckingUserIDFn("valid-token", "user-realauth"))
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	wsURL := "ws" + srv.URL[len("http"):]
	status := dialWSExpectFailure(t, wsURL, nil)
	if status != http.StatusUnauthorized {
		t.Errorf("expected 401 for no-auth connection, got %d", status)
	}
}

// TestServeWS_RealServer_TokenInQueryString_ConnectionRejected verifies that
// passing the token as a query parameter is rejected with 400.
func TestServeWS_RealServer_TokenInQueryString_ConnectionRejected(t *testing.T) {
	hub := NewHub(&fakeAuthorizer{}, slog.Default(), NopBus{}, "test-ws-realauth-qs")
	defer hub.Shutdown()

	handler := ServeWS(hub, slog.Default(), &fakeWorkspaceResolver{id: "ws-realauth-qs"},
		authCheckingUserIDFn("valid-token", "user-realauth-qs"))
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	credParam := "token"
	wsURL := "ws" + srv.URL[len("http"):] + "?" + credParam + "=valid-token"
	status := dialWSExpectFailure(t, wsURL, nil)
	if status != http.StatusBadRequest {
		t.Errorf("expected 400 for token in query string, got %d", status)
	}
}

// TestServeWS_RealServer_InvalidToken_ConnectionRejected verifies that a
// WebSocket dial with an invalid Bearer token is rejected with 401.
func TestServeWS_RealServer_InvalidToken_ConnectionRejected(t *testing.T) {
	hub := NewHub(&fakeAuthorizer{}, slog.Default(), NopBus{}, "test-ws-realauth-bad")
	defer hub.Shutdown()

	handler := ServeWS(hub, slog.Default(), &fakeWorkspaceResolver{id: "ws-realauth-bad"},
		authCheckingUserIDFn("valid-token", "user-realauth-bad"))
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	wsURL := "ws" + srv.URL[len("http"):]
	status := dialWSExpectFailure(t, wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": []string{"Bearer wrong-token"}},
	})
	if status != http.StatusUnauthorized {
		t.Errorf("expected 401 for invalid token, got %d", status)
	}
}

// TestServeWS_RealServer_ValidToken_ConnectionAccepted verifies that a
// WebSocket dial with a valid Bearer token in the Authorization header is
// accepted and the client is registered in the hub.
func TestServeWS_RealServer_ValidToken_ConnectionAccepted(t *testing.T) {
	hub := NewHub(&fakeAuthorizer{}, slog.Default(), NopBus{}, "test-ws-realauth-ok")
	defer hub.Shutdown()

	const (
		validToken = "valid-token-realserver"
		userID     = "user-realauth-ok"
		wsID       = "ws-realauth-ok"
	)

	handler := ServeWS(hub, slog.Default(), &fakeWorkspaceResolver{id: wsID},
		authCheckingUserIDFn(validToken, userID))
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	wsURL := "ws" + srv.URL[len("http"):]
	ctx, cancel := context.WithTimeout(context.Background(), testIOTimeout)
	defer cancel()

	conn, resp, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": []string{"Bearer " + validToken}},
	})
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		t.Fatalf("expected valid-token dial to succeed, got: %v", err)
	}
	t.Cleanup(func() { _ = conn.CloseNow() })

	eventually(t, func() bool {
		hub.mu.RLock()
		defer hub.mu.RUnlock()
		for _, c := range hub.clients {
			if c.userID == userID {
				return true
			}
		}
		return false
	}, 2*time.Second, "valid-token client must be registered in hub")
}
