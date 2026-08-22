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
	crsTestSyncID   = "00000000-0000-4000-8000-000000000201"
	crsTestTarget   = "00000000-0000-4000-8000-000000000202"
	crsTestRequest  = "00000000-0000-4000-8000-000000000203"
	crsTestCallID   = "00000000-0000-4000-8000-000000000204"
	crsObservedTime = "2026-08-10T12:00:00Z"
)

func mustParseTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatalf("parse time %q: %v", value, err)
	}
	return parsed
}

// ── call.resource.sync ──────────────────────────────────────────────────────

func TestResourceSyncRepliesRequesterOnlyWithActiveCall(t *testing.T) {
	observedAt := mustParseTime(t, crsObservedTime)
	handler := &fakeCallHandler{
		call:       callProtocolCall(domain.CallStatusActive, 3),
		found:      true,
		observedAt: observedAt,
	}
	hub := NewHub(&fakeAuthorizer{}, newTestLogger(), NopBus{}, "resource-sync-found", WithCallHandler(handler))
	t.Cleanup(hub.Shutdown)
	requester := newClient("sync-client", callTestCaller, callTestWorkspace, &fakeSender{})
	outsider := newClient("other-client", callTestCallee, callTestWorkspace, &fakeSender{})
	for _, c := range []*Client{requester, outsider} {
		if !hub.Register(c) {
			t.Fatal("register client")
		}
	}

	err := hub.handleClientMessage(context.Background(), requester, ClientMessage{
		Type: ClientMessageTypeCallResourceSync, SyncID: crsTestSyncID,
		TargetType: TargetTypeChannel, TargetID: crsTestTarget,
	})
	if err != nil {
		t.Fatalf("resource sync: %v", err)
	}

	select {
	case payload := <-requester.outbox:
		var response callResourceSyncedResponse
		if err := json.Unmarshal(payload, &response); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if response.Type != "call.resource.synced" || response.SyncID != crsTestSyncID ||
			response.TargetType != TargetTypeChannel || response.TargetID != crsTestTarget {
			t.Fatalf("unexpected envelope: %+v", response)
		}
		if response.Call == nil || response.Call.ID != callTestID {
			t.Fatalf("expected active call payload, got %+v", response.Call)
		}
		if !response.ObservedAt.Equal(observedAt) {
			t.Fatalf("observed_at = %v, want %v", response.ObservedAt, observedAt)
		}
	default:
		t.Fatal("expected a direct resource sync response")
	}
	if len(outsider.outbox) != 0 {
		t.Fatal("call.resource.sync must never broadcast")
	}
}

// call:null is a valid, necessary answer — both for a genuinely idle target
// and (indistinguishably, at the wire) for a target the caller cannot
// access. handler.found=false covers both; the protocol never learns which.
func TestResourceSyncRepliesNullCallWhenNotFoundOrUnauthorized(t *testing.T) {
	observedAt := mustParseTime(t, crsObservedTime)
	handler := &fakeCallHandler{found: false, observedAt: observedAt}
	hub := NewHub(&fakeAuthorizer{}, newTestLogger(), NopBus{}, "resource-sync-null", WithCallHandler(handler))
	t.Cleanup(hub.Shutdown)
	client := newClient("sync-client", callTestCaller, callTestWorkspace, &fakeSender{})
	if !hub.Register(client) {
		t.Fatal("register client")
	}

	err := hub.handleClientMessage(context.Background(), client, ClientMessage{
		Type: ClientMessageTypeCallResourceSync, SyncID: crsTestSyncID,
		TargetType: TargetTypeDM, TargetID: crsTestTarget,
	})
	if err != nil {
		t.Fatalf("resource sync: %v", err)
	}

	var response callResourceSyncedResponse
	if err := json.Unmarshal(<-client.outbox, &response); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if response.Call != nil {
		t.Fatalf("expected null call, got %+v", response.Call)
	}
	if !response.ObservedAt.Equal(observedAt) {
		t.Fatalf("observed_at = %v, want %v (must be present even when not found)", response.ObservedAt, observedAt)
	}
}

func TestResourceSyncRejectsMalformedOrForeignFields(t *testing.T) {
	handler := &fakeCallHandler{}
	hub := NewHub(&fakeAuthorizer{}, newTestLogger(), NopBus{}, "resource-sync-invalid", WithCallHandler(handler))
	t.Cleanup(hub.Shutdown)
	client := newClient("sync-client", callTestCaller, callTestWorkspace, &fakeSender{})
	if !hub.Register(client) {
		t.Fatal("register client")
	}

	tests := []struct {
		name string
		msg  ClientMessage
	}{
		{"no sync_id", ClientMessage{Type: ClientMessageTypeCallResourceSync, TargetType: TargetTypeChannel, TargetID: crsTestTarget}},
		{"no target_type", ClientMessage{Type: ClientMessageTypeCallResourceSync, SyncID: crsTestSyncID, TargetID: crsTestTarget}},
		{"user target_type", ClientMessage{Type: ClientMessageTypeCallResourceSync, SyncID: crsTestSyncID, TargetType: TargetTypeUser, TargetID: crsTestTarget}},
		{"no target_id", ClientMessage{Type: ClientMessageTypeCallResourceSync, SyncID: crsTestSyncID, TargetType: TargetTypeChannel}},
		{"target_id not a uuid", ClientMessage{Type: ClientMessageTypeCallResourceSync, SyncID: crsTestSyncID, TargetType: TargetTypeChannel, TargetID: "nope"}},
		{"carries request_id", ClientMessage{Type: ClientMessageTypeCallResourceSync, SyncID: crsTestSyncID, TargetType: TargetTypeChannel, TargetID: crsTestTarget, RequestID: crsTestRequest}},
		{"carries call_id", ClientMessage{Type: ClientMessageTypeCallResourceSync, SyncID: crsTestSyncID, TargetType: TargetTypeChannel, TargetID: crsTestTarget, CallID: crsTestCallID}},
		{"carries target_user_id", ClientMessage{Type: ClientMessageTypeCallResourceSync, SyncID: crsTestSyncID, TargetType: TargetTypeChannel, TargetID: crsTestTarget, TargetUserID: callTestCallee}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := hub.handleClientMessage(context.Background(), client, tt.msg); !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("error = %v, want invalid input", err)
			}
		})
	}
}

// The null-sync race (issue #622): observed_at is whatever the storage layer
// derived from its own read snapshot, echoed back verbatim — the WS layer
// does not substitute its own clock. This is what lets a client safely
// compare it against a call it already knows about.
func TestResourceSyncObservedAtIsNeverSubstitutedByWallClock(t *testing.T) {
	past := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	handler := &fakeCallHandler{found: false, observedAt: past}
	hub := NewHub(&fakeAuthorizer{}, newTestLogger(), NopBus{}, "resource-sync-observed-at", WithCallHandler(handler))
	t.Cleanup(hub.Shutdown)
	client := newClient("sync-client", callTestCaller, callTestWorkspace, &fakeSender{})
	if !hub.Register(client) {
		t.Fatal("register client")
	}
	if err := hub.handleClientMessage(context.Background(), client, ClientMessage{
		Type: ClientMessageTypeCallResourceSync, SyncID: crsTestSyncID,
		TargetType: TargetTypeChannel, TargetID: crsTestTarget,
	}); err != nil {
		t.Fatalf("resource sync: %v", err)
	}
	var response callResourceSyncedResponse
	if err := json.Unmarshal(<-client.outbox, &response); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !response.ObservedAt.Equal(past) {
		t.Fatalf("observed_at = %v, want the storage-derived %v untouched", response.ObservedAt, past)
	}
}

// ── call.join ────────────────────────────────────────────────────────────────

func TestCallJoinAdmitsAndAcksOnlyTheRequesterCorrelatedByRequestID(t *testing.T) {
	handler := &fakeCallHandler{call: callProtocolCall(domain.CallStatusActive, 5), participationID: callTestParticipation}
	hub := NewHub(&fakeAuthorizer{}, newTestLogger(), NopBus{}, "call-join-test", WithCallHandler(handler))
	t.Cleanup(hub.Shutdown)
	requester := newClient("joiner-client", callTestCaller, callTestWorkspace, &fakeSender{})
	other := newClient("other-client", callTestCallee, callTestWorkspace, &fakeSender{})
	for _, c := range []*Client{requester, other} {
		if !hub.Register(c) {
			t.Fatal("register client")
		}
	}

	err := hub.handleClientMessage(context.Background(), requester, ClientMessage{
		Type: ClientMessageTypeCallJoin, RequestID: crsTestRequest, CallID: crsTestCallID,
		TargetType: TargetTypeChannel, TargetID: crsTestTarget,
	})
	if err != nil {
		t.Fatalf("call.join: %v", err)
	}
	if handler.joinCallID != crsTestCallID || handler.joinTargetType != TargetTypeChannel || handler.joinTargetID != crsTestTarget {
		t.Fatalf("join input = call=%q targetType=%q targetID=%q", handler.joinCallID, handler.joinTargetType, handler.joinTargetID)
	}

	select {
	case payload := <-requester.outbox:
		var response callAdmittedResponse
		if err := json.Unmarshal(payload, &response); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if response.Type != "call.admitted" || response.Operation != "call.join" ||
			response.ResponseTo != crsTestRequest || response.Call.ID != callTestID ||
			response.ParticipationID != callTestParticipation {
			t.Fatalf("unexpected admitted response: %+v", response)
		}
	default:
		t.Fatal("expected a direct call.admitted response")
	}
	if len(other.outbox) != 0 {
		t.Fatal("call.join must never broadcast — only the lease table changes")
	}
}

func TestCallJoinErrorCorrelatesWithRequestID(t *testing.T) {
	handler := &fakeCallHandler{err: domain.ErrCallParticipantBusy}
	hub := NewHub(&fakeAuthorizer{}, newTestLogger(), NopBus{}, "call-join-busy", WithCallHandler(handler))
	t.Cleanup(hub.Shutdown)
	client := newClient("joiner-client", callTestCaller, callTestWorkspace, &fakeSender{})
	if !hub.Register(client) {
		t.Fatal("register client")
	}

	msg := ClientMessage{
		Type: ClientMessageTypeCallJoin, RequestID: crsTestRequest, CallID: crsTestCallID,
		TargetType: TargetTypeChannel, TargetID: crsTestTarget,
	}
	err := hub.handleClientMessage(context.Background(), client, msg)
	if !errors.Is(err, domain.ErrCallParticipantBusy) {
		t.Fatalf("error = %v, want participant busy", err)
	}
	if !handleCallClientError(client, msg.Type, msg.CallID, callResponseTo(msg), err) {
		t.Fatal("expected call.join error to be handled")
	}
	var response clientErrorResponse
	if err := json.Unmarshal(<-client.outbox, &response); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if response.Type != "call.error" || response.Operation != "call.join" ||
		response.Code != "call_participant_busy" || response.ResponseTo != crsTestRequest {
		t.Fatalf("unexpected call error: %+v", response)
	}
}

func TestCallJoinRejectsMalformedOrForeignFields(t *testing.T) {
	handler := &fakeCallHandler{call: callProtocolCall(domain.CallStatusActive, 1)}
	hub := NewHub(&fakeAuthorizer{}, newTestLogger(), NopBus{}, "call-join-invalid", WithCallHandler(handler))
	t.Cleanup(hub.Shutdown)
	client := newClient("joiner-client", callTestCaller, callTestWorkspace, &fakeSender{})
	if !hub.Register(client) {
		t.Fatal("register client")
	}

	tests := []struct {
		name string
		msg  ClientMessage
	}{
		{"no request_id", ClientMessage{Type: ClientMessageTypeCallJoin, CallID: crsTestCallID, TargetType: TargetTypeChannel, TargetID: crsTestTarget}},
		{"no call_id", ClientMessage{Type: ClientMessageTypeCallJoin, RequestID: crsTestRequest, TargetType: TargetTypeChannel, TargetID: crsTestTarget}},
		{"no target_type", ClientMessage{Type: ClientMessageTypeCallJoin, RequestID: crsTestRequest, CallID: crsTestCallID, TargetID: crsTestTarget}},
		{"user target_type", ClientMessage{Type: ClientMessageTypeCallJoin, RequestID: crsTestRequest, CallID: crsTestCallID, TargetType: TargetTypeUser, TargetID: crsTestTarget}},
		{"no target_id", ClientMessage{Type: ClientMessageTypeCallJoin, RequestID: crsTestRequest, CallID: crsTestCallID, TargetType: TargetTypeChannel}},
		{"call_id not a uuid", ClientMessage{Type: ClientMessageTypeCallJoin, RequestID: crsTestRequest, CallID: "nope", TargetType: TargetTypeChannel, TargetID: crsTestTarget}},
		{"carries target_user_id", ClientMessage{Type: ClientMessageTypeCallJoin, RequestID: crsTestRequest, CallID: crsTestCallID, TargetType: TargetTypeChannel, TargetID: crsTestTarget, TargetUserID: callTestCallee}},
		{"carries call_type", ClientMessage{Type: ClientMessageTypeCallJoin, RequestID: crsTestRequest, CallID: crsTestCallID, TargetType: TargetTypeChannel, TargetID: crsTestTarget, CallType: domain.CallTypeAudio}},
		{"carries participation_id", ClientMessage{Type: ClientMessageTypeCallJoin, RequestID: crsTestRequest, CallID: crsTestCallID, TargetType: TargetTypeChannel, TargetID: crsTestTarget, ParticipationID: callTestParticipation}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := hub.handleClientMessage(context.Background(), client, tt.msg); !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("error = %v, want invalid input", err)
			}
		})
	}
}

// ── resource call.start's call.admitted ACK ─────────────────────────────────

func TestResourceCallStartSendsCallAdmittedCorrelatedByRequestID(t *testing.T) {
	handler := &fakeCallHandler{call: callProtocolCall(domain.CallStatusActive, 1), participationID: callTestParticipation}
	hub := NewHub(&fakeAuthorizer{}, newTestLogger(), NopBus{}, "resource-start-admitted",
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

	var response callAdmittedResponse
	if err := json.Unmarshal(<-client.outbox, &response); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if response.Type != "call.admitted" || response.Operation != "call.start" ||
		response.ResponseTo != callTestRequest || response.Call.ID != callTestID ||
		response.ParticipationID != callTestParticipation {
		t.Fatalf("unexpected admitted response: %+v", response)
	}
}

// TestResourceCallStartReuseAcksWithCurrentRequestButHistoricalCallRequestID
// is the adversarial-review scenario for #622's ACK: A already created X
// (its request_id persisted forever on that row). B's own call.start reuses
// the same active X rather than creating a second one — StartCall returns
// the existing row unchanged. B's call.admitted must correlate by B's own
// current request_id (response_to), while the Call payload inside it still
// carries A's original, historical request_id — response_to is never the
// same field as Call.request_id, and persisted request_id is never rewritten
// by a later reuse.
func TestResourceCallStartReuseAcksWithCurrentRequestButHistoricalCallRequestID(t *testing.T) {
	reused := callProtocolCall(domain.CallStatusActive, 2)
	reused.RequestID = "00000000-0000-4000-8000-000000000199" // A's historical request_id — distinct from B's below.
	handler := &fakeCallHandler{call: reused, participationID: callTestParticipation}
	hub := NewHub(&fakeAuthorizer{}, newTestLogger(), NopBus{}, "resource-start-reuse-admitted",
		WithCallHandler(handler), WithCallLimiter(allowCallLimiter{allowed: true}, 10, 60))
	t.Cleanup(hub.Shutdown)
	requesterB := newClient("b-client", callTestCallee, callTestWorkspace, &fakeSender{})
	if !hub.Register(requesterB) {
		t.Fatal("register B")
	}

	bRequestID := "00000000-0000-4000-8000-000000000198"
	err := hub.handleClientMessage(context.Background(), requesterB, ClientMessage{
		Type: ClientMessageTypeCallStart, RequestID: bRequestID,
		TargetType: TargetTypeChannel, TargetID: callTestCallee, CallType: domain.CallTypeVideo,
	})
	if err != nil {
		t.Fatalf("B's call.start (reuse): %v", err)
	}
	if handler.started.RequestID != bRequestID {
		t.Fatalf("StartCall must still receive B's own request_id as the command's identity: %+v", handler.started)
	}

	var response callAdmittedResponse
	if err := json.Unmarshal(<-requesterB.outbox, &response); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if response.ResponseTo != bRequestID {
		t.Fatalf("response_to = %q, want B's own request_id %q", response.ResponseTo, bRequestID)
	}
	if response.Call.RequestID != reused.RequestID {
		t.Fatalf("admitted.call.request_id = %q, want A's historical request_id %q (never rewritten)",
			response.Call.RequestID, reused.RequestID)
	}
	if response.ParticipationID != callTestParticipation {
		t.Fatalf("participation_id = %q", response.ParticipationID)
	}
	if response.ResponseTo == response.Call.RequestID {
		t.Fatal("response_to and Call.request_id must never collapse into the same value in this scenario")
	}
}

// Direct call.start must keep its pre-#622 behavior exactly: no call.admitted
// ACK at all — only the existing call.ringing lifecycle broadcast.
func TestDirectCallStartNeverSendsCallAdmitted(t *testing.T) {
	handler := &fakeCallHandler{call: callProtocolCall(domain.CallStatusRinging, 1)}
	hub := NewHub(&fakeAuthorizer{}, newTestLogger(), NopBus{}, "direct-start-no-admitted",
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
		t.Fatalf("start direct call: %v", err)
	}
	if len(client.outbox) != 0 {
		t.Fatal("direct call.start must not produce any requester-only response")
	}
}

// Resource call.start errors correlate by request_id; direct call.start
// errors keep carrying none, unchanged from before #622.
func TestCallStartErrorCorrelationDependsOnResourceVsDirect(t *testing.T) {
	resourceMsg := ClientMessage{
		Type: ClientMessageTypeCallStart, RequestID: callTestRequest,
		TargetType: TargetTypeChannel, TargetID: callTestCallee, CallType: domain.CallTypeVideo,
	}
	if got := callResponseTo(resourceMsg); got != callTestRequest {
		t.Fatalf("resource call.start response_to = %q, want %q", got, callTestRequest)
	}
	directMsg := ClientMessage{
		Type: ClientMessageTypeCallStart, RequestID: callTestRequest,
		TargetUserID: callTestCallee, CallType: domain.CallTypeVideo,
	}
	if got := callResponseTo(directMsg); got != "" {
		t.Fatalf("direct call.start response_to = %q, want empty", got)
	}
}
