package ws

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

const (
	callTestWorkspace = "00000000-0000-4000-8000-000000000101"
	callTestRequest   = "00000000-0000-4000-8000-000000000102"
	callTestID        = "00000000-0000-4000-8000-000000000103"
	callTestCaller    = "00000000-0000-4000-8000-000000000104"
	callTestCallee    = "00000000-0000-4000-8000-000000000105"
	callTestOutsider  = "00000000-0000-4000-8000-000000000106"
)

type fakeCallHandler struct {
	call           domain.Call
	started        StartCallCommand
	action         ClientMessageType
	actorID        string
	callID         string
	presenceCallID string
	syncCallID     string
	err            error
}

func (h *fakeCallHandler) StartCall(_ context.Context, command StartCallCommand) (domain.Call, error) {
	h.started = command
	return h.call, h.err
}

func (h *fakeCallHandler) TransitionCall(_ context.Context, workspaceID, actorID, callID string, action ClientMessageType) (domain.Call, error) {
	h.action, h.actorID, h.callID = action, actorID, callID
	return h.call, h.err
}

func (h *fakeCallHandler) CurrentCall(_ context.Context, _, _, callID string) (domain.Call, error) {
	h.syncCallID = callID
	return h.call, h.err
}

func (h *fakeCallHandler) RenewCallPresence(_ context.Context, _ string, actorID, callID string) error {
	h.actorID, h.presenceCallID = actorID, callID
	return h.err
}

type allowCallLimiter struct{ allowed bool }

func (l allowCallLimiter) AllowActionWithLimit(context.Context, string, string, int, int) (bool, error) {
	return l.allowed, nil
}

func callProtocolCall(status domain.CallStatus, version int64) domain.Call {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	return domain.Call{
		ID: callTestID, WorkspaceID: callTestWorkspace, RequestID: callTestRequest,
		CallerID: callTestCaller, CalleeID: callTestCallee, Type: domain.CallTypeVideo,
		Status: status, Version: version, CreatedAt: now, UpdatedAt: now,
		ExpiresAt: now.Add(30 * time.Second),
	}
}

func TestCallStartUsesAuthenticatedClientIdentity(t *testing.T) {
	handler := &fakeCallHandler{call: callProtocolCall(domain.CallStatusRinging, 1)}
	hub := NewHub(&fakeAuthorizer{}, newTestLogger(), NopBus{}, "call-test",
		WithCallHandler(handler), WithCallLimiter(allowCallLimiter{allowed: true}, 10, 60))
	t.Cleanup(hub.Shutdown)
	client := newClient("caller-client", callTestCaller, callTestWorkspace, &fakeSender{})
	if !hub.Register(client) {
		t.Fatal("register caller")
	}

	err := hub.handleClientMessage(context.Background(), client, ClientMessage{
		Type: ClientMessageTypeCallStart, RequestID: callTestRequest,
		TargetUserID: callTestCallee, CallType: domain.CallTypeVideo,
	})
	if err != nil {
		t.Fatalf("start call: %v", err)
	}
	if handler.started.CallerID != callTestCaller || handler.started.WorkspaceID != callTestWorkspace {
		t.Fatalf("identity did not come from session: %+v", handler.started)
	}
}

func TestCallSyncByIDUsesAuthenticatedIdentityAndRepliesOnlyToRequester(t *testing.T) {
	handler := &fakeCallHandler{call: callProtocolCall(domain.CallStatusActive, 2)}
	hub := NewHub(&fakeAuthorizer{}, newTestLogger(), NopBus{}, "call-sync-test", WithCallHandler(handler))
	t.Cleanup(hub.Shutdown)
	client := newClient("sync-client", callTestCallee, callTestWorkspace, &fakeSender{})

	err := hub.handleClientMessage(context.Background(), client, ClientMessage{
		Type: ClientMessageTypeCallSync, CallID: callTestID,
	})
	if err != nil {
		t.Fatalf("sync call: %v", err)
	}
	if handler.syncCallID != callTestID || handler.actorID != "" {
		t.Fatalf("unexpected sync input: call=%q actor side effect=%q", handler.syncCallID, handler.actorID)
	}
	select {
	case payload := <-client.outbox:
		var event Event
		if err := json.Unmarshal(payload, &event); err != nil || event.Call == nil || event.Call.ID != callTestID {
			t.Fatalf("unexpected sync response: %s (%v)", payload, err)
		}
	default:
		t.Fatal("expected direct sync response")
	}
}

func TestResourceCallStartAndPresenceUseAuthenticatedClientIdentity(t *testing.T) {
	handler := &fakeCallHandler{call: callProtocolCall(domain.CallStatusActive, 1)}
	hub := NewHub(&fakeAuthorizer{}, newTestLogger(), NopBus{}, "resource-call-test",
		WithCallHandler(handler), WithCallLimiter(allowCallLimiter{allowed: true}, 10, 60))
	t.Cleanup(hub.Shutdown)
	client := newClient("caller-client", callTestCaller, callTestWorkspace, &fakeSender{})
	if !hub.Register(client) {
		t.Fatal("register caller")
	}

	err := hub.handleClientMessage(context.Background(), client, ClientMessage{
		Type: ClientMessageTypeCallStart, RequestID: callTestRequest,
		TargetType: TargetTypeChannel, TargetID: callTestCallee, CallType: domain.CallTypeVideo,
	})
	if err != nil {
		t.Fatalf("start resource call: %v", err)
	}
	if handler.started.CallerID != callTestCaller || handler.started.TargetType != TargetTypeChannel ||
		handler.started.TargetID != callTestCallee {
		t.Fatalf("resource command=%+v", handler.started)
	}

	if err := hub.handleClientMessage(context.Background(), client, ClientMessage{
		Type: ClientMessageTypeCallPresence, CallID: callTestID,
	}); err != nil {
		t.Fatalf("presence: %v", err)
	}
	if handler.actorID != callTestCaller || handler.presenceCallID != callTestID {
		t.Fatalf("presence actor=%q call=%q", handler.actorID, handler.presenceCallID)
	}
}

func TestCallCommandValidationRejectsClientControlledIdentity(t *testing.T) {
	_, err := decodeClientMessage([]byte(`{"type":"call.start","request_id":"` + callTestRequest +
		`","target_user_id":"` + callTestCallee + `","call_type":"audio","actor_user_id":"` + callTestOutsider + `"}`))
	if err == nil {
		t.Fatal("client-controlled actor_user_id must be rejected")
	}
}

func TestPublishCallDeliversOnlyToParticipants(t *testing.T) {
	hub := NewHub(&fakeAuthorizer{}, newTestLogger(), NopBus{}, "call-publisher")
	t.Cleanup(hub.Shutdown)
	caller := newClient("caller", callTestCaller, callTestWorkspace, &fakeSender{})
	callee := newClient("callee", callTestCallee, callTestWorkspace, &fakeSender{})
	outsider := newClient("outsider", callTestOutsider, callTestWorkspace, &fakeSender{})
	for _, client := range []*Client{caller, callee, outsider} {
		if !hub.Register(client) {
			t.Fatal("register client")
		}
	}

	hub.PublishCall(context.Background(), callProtocolCall(domain.CallStatusActive, 2))
	waitForOutbox(caller, 1)
	waitForOutbox(callee, 1)
	if len(outsider.outbox) != 0 {
		t.Fatal("outsider received call event")
	}
	for _, client := range []*Client{caller, callee} {
		var event Event
		if err := json.Unmarshal(<-client.outbox, &event); err != nil {
			t.Fatalf("decode call event: %v", err)
		}
		if event.Type != EventTypeCallAccepted || event.Call == nil || event.Call.Version != 2 {
			t.Fatalf("unexpected event: %+v", event)
		}
	}
}

func TestPublishResourceCallUsesAuthorizedTargetSubscription(t *testing.T) {
	auth := &fakeAuthorizer{}
	auth.setAccess(callTestCaller, callTestWorkspace, TargetTypeChannel, callTestCallee, true)
	hub := NewHub(auth, newTestLogger(), NopBus{}, "resource-call-publisher")
	t.Cleanup(hub.Shutdown)
	member := newClient("member", callTestCaller, callTestWorkspace, &fakeSender{})
	if !hub.Register(member) {
		t.Fatal("register member")
	}
	if err := hub.Subscribe(context.Background(), member, TargetTypeChannel, callTestCallee); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	call := callProtocolCall(domain.CallStatusActive, 1)
	call.CalleeID = ""
	call.TargetType = domain.CallTargetChannel
	call.TargetID = callTestCallee
	hub.PublishCall(context.Background(), call)
	waitForOutbox(member, 1)
	var event Event
	if err := json.Unmarshal(<-member.outbox, &event); err != nil {
		t.Fatalf("decode event: %v", err)
	}
	if event.TargetType != TargetTypeChannel || event.TargetID != callTestCallee ||
		event.Call == nil || event.Call.TargetType != domain.CallTargetChannel {
		t.Fatalf("resource event=%+v", event)
	}
}

func TestCallErrorsAreStableAndDoNotConsumeInvalidBudget(t *testing.T) {
	handler := &fakeCallHandler{err: domain.ErrNotFound}
	hub := NewHub(&fakeAuthorizer{}, newTestLogger(), NopBus{}, "call-errors", WithCallHandler(handler))
	t.Cleanup(hub.Shutdown)
	client := newClient("callee-client", callTestCallee, callTestWorkspace, &fakeSender{})
	if !hub.Register(client) {
		t.Fatal("register callee")
	}
	err := hub.handleClientMessage(context.Background(), client, ClientMessage{
		Type: ClientMessageTypeCallAccept, CallID: callTestID,
	})
	if !errors.Is(err, domain.ErrNotFound) || !handleCallClientError(client, ClientMessageTypeCallAccept, callTestID, err) {
		t.Fatalf("unexpected error classification: %v", err)
	}
	var response clientErrorResponse
	if err := json.Unmarshal(<-client.outbox, &response); err != nil {
		t.Fatalf("decode call error: %v", err)
	}
	if response.Code != "call_not_found" || response.Operation != "call.accept" {
		t.Fatalf("unexpected call error: %+v", response)
	}
}
