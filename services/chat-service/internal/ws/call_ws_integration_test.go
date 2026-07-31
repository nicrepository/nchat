package ws

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

type lifecycleCallHandler struct {
	mu   sync.Mutex
	hub  *Hub
	call domain.Call
}

func (h *lifecycleCallHandler) StartCall(ctx context.Context, command StartCallCommand) (domain.Call, error) {
	h.mu.Lock()
	h.call = callProtocolCall(domain.CallStatusRinging, 1)
	h.call.CallerID, h.call.CalleeID = command.CallerID, command.CalleeID
	call := h.call
	h.mu.Unlock()
	h.hub.PublishCall(ctx, call)
	return call, nil
}

func (h *lifecycleCallHandler) TransitionCall(ctx context.Context, _, actorID, _ string, action ClientMessageType) (domain.Call, error) {
	h.mu.Lock()
	switch action {
	case ClientMessageTypeCallAccept:
		if actorID != h.call.CalleeID {
			h.mu.Unlock()
			return domain.Call{}, domain.ErrNotFound
		}
		h.call.Status = domain.CallStatusActive
	case ClientMessageTypeCallEnd:
		if !h.call.IsParticipant(actorID) {
			h.mu.Unlock()
			return domain.Call{}, domain.ErrNotFound
		}
		h.call.Status = domain.CallStatusEnded
	}
	h.call.Version++
	h.call.UpdatedAt = h.call.UpdatedAt.Add(time.Second)
	call := h.call
	h.mu.Unlock()
	h.hub.PublishCall(ctx, call)
	return call, nil
}

func (h *lifecycleCallHandler) CurrentCall(context.Context, string, string) (domain.Call, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.call.ID == "" {
		return domain.Call{}, domain.ErrNotFound
	}
	return h.call, nil
}

func TestCallWebSocketLifecycleNotifiesTwoAuthenticatedClients(t *testing.T) {
	handler := &lifecycleCallHandler{}
	hub := NewHub(&fakeAuthorizer{}, newTestLogger(), NopBus{}, "call-ws-integration",
		WithCallHandler(handler), WithCallLimiter(allowCallLimiter{allowed: true}, 10, 60))
	handler.hub = hub
	t.Cleanup(hub.Shutdown)
	workspace := &fakeWorkspaceResolver{id: callTestWorkspace}
	callerServer := newTestWSServer(t, hub, workspace, callTestCaller)
	calleeServer := newTestWSServer(t, hub, workspace, callTestCallee)
	caller := dialWS(t, callerServer)
	callee := dialWS(t, calleeServer)

	writeCallCommand(t, caller, map[string]any{
		"type": "call.start", "request_id": callTestRequest,
		"target_user_id": callTestCallee, "call_type": "video",
	})
	assertCallEvent(t, caller, EventTypeCallRinging)
	assertCallEvent(t, callee, EventTypeCallRinging)

	writeCallCommand(t, callee, map[string]any{"type": "call.accept", "call_id": callTestID})
	assertCallEvent(t, caller, EventTypeCallAccepted)
	assertCallEvent(t, callee, EventTypeCallAccepted)

	writeCallCommand(t, caller, map[string]any{"type": "call.end", "call_id": callTestID})
	assertCallEvent(t, caller, EventTypeCallEnded)
	assertCallEvent(t, callee, EventTypeCallEnded)
}

func writeCallCommand(t *testing.T, connection *websocket.Conn, command map[string]any) {
	t.Helper()
	data, err := json.Marshal(command)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), testIOTimeout)
	defer cancel()
	if err := connection.Write(ctx, websocket.MessageText, data); err != nil {
		t.Fatalf("write call command: %v", err)
	}
}

func assertCallEvent(t *testing.T, connection *websocket.Conn, want EventType) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testIOTimeout)
	defer cancel()
	_, data, err := connection.Read(ctx)
	if err != nil {
		t.Fatalf("read %s: %v", want, err)
	}
	var event Event
	if err := json.Unmarshal(data, &event); err != nil {
		t.Fatalf("decode %s: %v", want, err)
	}
	if event.Type != want || event.Call == nil {
		t.Fatalf("event=%+v want=%s", event, want)
	}
}
