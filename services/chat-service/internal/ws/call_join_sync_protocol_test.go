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
	// Distinct call.sync sync_ids for two overlapping direct syncs of the
	// SAME call_id (issue #614 blocker follow-up) — see
	// TestTwoConcurrentCallSyncsCorrelateBySyncIDNeverCrossResolve below.
	callSyncTestSyncIDA = "00000000-0000-4000-8000-000000000205"
	callSyncTestSyncIDB = "00000000-0000-4000-8000-000000000206"
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

// Resource AND direct call.start errors both correlate by request_id (issue
// #615): direct call.start's own call.error used to carry none at all —
// eventCompletesPending's success-side correlation (call.ringing echoing
// request_id) already existed, but a FAILED call.start had no equivalent,
// so useCallSignaling's errorMatchesPending could only correlate a direct
// call.start error by "is there a call.start currently pending" — a stale
// call.error from an abandoned attempt A could be mistaken for a
// later, still-pending attempt B's own failure. See
// TestDirectCallStartErrorCorrelatesByRequestID below for the end-to-end
// wire proof.
func TestCallStartErrorAlwaysCorrelatesByRequestID(t *testing.T) {
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
	if got := callResponseTo(directMsg); got != callTestRequest {
		t.Fatalf("direct call.start response_to = %q, want %q", got, callTestRequest)
	}
}

// End-to-end wire proof for issue #615: a DIRECT call.start's own call.error
// carries response_to == the command's request_id — the same shape a
// resource call.start's error already had (see
// TestResourceCallStartBusyProducesParticipantBusyCallError, unaffected by
// this change). Success is untouched: direct call.start still never gets a
// call.admitted ACK (TestDirectCallStartNeverSendsCallAdmitted above), only
// the pre-existing call.ringing lifecycle broadcast.
func TestDirectCallStartErrorCorrelatesByRequestID(t *testing.T) {
	handler := &fakeCallHandler{err: domain.ErrCallParticipantBusy}
	hub := NewHub(&fakeAuthorizer{}, newTestLogger(), NopBus{}, "direct-start-busy-correlated",
		WithCallHandler(handler), WithCallLimiter(allowCallLimiter{allowed: true}, 10, 60))
	t.Cleanup(hub.Shutdown)
	client := newClient("caller-client", callTestCaller, callTestWorkspace, &fakeSender{})
	if !hub.Register(client) {
		t.Fatal("register caller")
	}

	msg := ClientMessage{
		Type: ClientMessageTypeCallStart, RequestID: callTestRequest,
		TargetUserID: callTestCallee, CallType: domain.CallTypeVideo,
	}
	err := hub.handleClientMessage(context.Background(), client, msg)
	if !errors.Is(err, domain.ErrCallParticipantBusy) {
		t.Fatalf("start direct call: %v", err)
	}
	if !handleCallClientError(client, msg.Type, "", callResponseTo(msg), err) {
		t.Fatal("expected direct call.start error to be handled")
	}
	var response clientErrorResponse
	if err := json.Unmarshal(<-client.outbox, &response); err != nil {
		t.Fatalf("decode call error: %v", err)
	}
	if response.Type != "call.error" || response.Operation != "call.start" ||
		response.Code != "call_participant_busy" || response.ResponseTo != callTestRequest {
		t.Fatalf("unexpected direct call error: %+v", response)
	}
}

// ── call.sync (direct, correlated) — issue #614 blocker follow-up ──────────
//
// resolveCall's old correlation-by-call_id-alone let a stale reply to an
// abandoned/superseded call.sync, or a live broadcast for the same call_id,
// be mistaken for the answer to a DIFFERENT, later call.sync of the same
// call — because sendCallToClient's plain lifecycle-event shape is wire-
// identical to a real broadcast and carries no per-request identifier. These
// tests cover the new sync_id-correlated call.synced reply that closes that
// gap, and prove the pre-existing (sync_id-less) path is untouched.

func TestCallSyncWithSyncIDRepliesCorrelatedNeverBroadcast(t *testing.T) {
	handler := &fakeCallHandler{call: callProtocolCall(domain.CallStatusActive, 4)}
	hub := NewHub(&fakeAuthorizer{}, newTestLogger(), NopBus{}, "call-sync-synced", WithCallHandler(handler))
	t.Cleanup(hub.Shutdown)
	requester := newClient("sync-client", callTestCallee, callTestWorkspace, &fakeSender{})
	outsider := newClient("other-client", callTestCaller, callTestWorkspace, &fakeSender{})
	for _, c := range []*Client{requester, outsider} {
		if !hub.Register(c) {
			t.Fatal("register client")
		}
	}

	err := hub.handleClientMessage(context.Background(), requester, ClientMessage{
		Type: ClientMessageTypeCallSync, CallID: callTestID, SyncID: callSyncTestSyncIDA,
	})
	if err != nil {
		t.Fatalf("call.sync: %v", err)
	}

	select {
	case payload := <-requester.outbox:
		var response callSyncedResponse
		if err := json.Unmarshal(payload, &response); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if response.Type != "call.synced" || response.SyncID != callSyncTestSyncIDA || response.Call.ID != callTestID {
			t.Fatalf("unexpected call.synced response: %+v", response)
		}
	default:
		t.Fatal("expected a direct call.synced response")
	}
	if len(outsider.outbox) != 0 {
		t.Fatal("call.sync must never broadcast, correlated or not")
	}
}

// TestTwoConcurrentCallSyncsCorrelateBySyncIDNeverCrossResolve is the
// protocol-level evidence for the #614 blocker: two overlapping call.sync
// requests for the SAME call_id, issued with distinct sync_ids (as
// resourceCallSignaling.ts's resolveCall now does for every attempt),
// produce two replies each tagged with their own request's sync_id — so a
// consumer holding two such requests in flight can always tell them apart
// and never treats B's own request as answered by A's reply, or vice versa,
// even though both answers describe the exact same call_id. This is the
// server-side half of the fix; resourceCallSignaling.test.ts covers the
// client-side consumer that relies on it.
func TestTwoConcurrentCallSyncsCorrelateBySyncIDNeverCrossResolve(t *testing.T) {
	handler := &fakeCallHandler{call: callProtocolCall(domain.CallStatusActive, 4)}
	hub := NewHub(&fakeAuthorizer{}, newTestLogger(), NopBus{}, "call-sync-concurrent", WithCallHandler(handler))
	t.Cleanup(hub.Shutdown)
	client := newClient("sync-client", callTestCallee, callTestWorkspace, &fakeSender{})
	if !hub.Register(client) {
		t.Fatal("register client")
	}

	// Sync A observes the call while still active.
	if err := hub.handleClientMessage(context.Background(), client, ClientMessage{
		Type: ClientMessageTypeCallSync, CallID: callTestID, SyncID: callSyncTestSyncIDA,
	}); err != nil {
		t.Fatalf("sync A: %v", err)
	}
	// The call ends between A's read and B's — a later, independent sync
	// (the recovery attempt in restoreOwnership's fence) must observe that.
	handler.call = callProtocolCall(domain.CallStatusEnded, 5)
	if err := hub.handleClientMessage(context.Background(), client, ClientMessage{
		Type: ClientMessageTypeCallSync, CallID: callTestID, SyncID: callSyncTestSyncIDB,
	}); err != nil {
		t.Fatalf("sync B: %v", err)
	}

	var replyA, replyB callSyncedResponse
	if err := json.Unmarshal(<-client.outbox, &replyA); err != nil {
		t.Fatalf("decode A: %v", err)
	}
	if err := json.Unmarshal(<-client.outbox, &replyB); err != nil {
		t.Fatalf("decode B: %v", err)
	}
	if replyA.SyncID != callSyncTestSyncIDA || replyA.Call.Status != domain.CallStatusActive {
		t.Fatalf("A's own reply must carry A's sync_id and A's observed state: %+v", replyA)
	}
	if replyB.SyncID != callSyncTestSyncIDB || replyB.Call.Status != domain.CallStatusEnded {
		t.Fatalf("B's own reply must carry B's sync_id and B's observed state: %+v", replyB)
	}
	// The defect this closes: a consumer correlating by call_id alone (the
	// old resolveCall) could not tell these two replies apart at all — both
	// describe callTestID. A caller keying strictly off SyncID, as the fixed
	// resolveCall now does, can never let A's stale "active" resolve B's
	// fence, which is exactly the cross-resolution #614's review flagged.
	if replyA.SyncID == replyB.SyncID {
		t.Fatal("A and B must never share a sync_id in this scenario")
	}
}

func TestCallSyncRejectsMalformedSyncID(t *testing.T) {
	handler := &fakeCallHandler{call: callProtocolCall(domain.CallStatusActive, 1)}
	hub := NewHub(&fakeAuthorizer{}, newTestLogger(), NopBus{}, "call-sync-bad-syncid", WithCallHandler(handler))
	t.Cleanup(hub.Shutdown)
	client := newClient("sync-client", callTestCallee, callTestWorkspace, &fakeSender{})
	if !hub.Register(client) {
		t.Fatal("register client")
	}

	err := hub.handleClientMessage(context.Background(), client, ClientMessage{
		Type: ClientMessageTypeCallSync, CallID: callTestID, SyncID: "not-a-uuid",
	})
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("error = %v, want invalid input", err)
	}
}

// A call.sync's own call_not_found error correlates by sync_id exactly like
// its success reply does when the requester supplied one — and, symmetrically,
// carries none at all (never a synthesized one) when the requester did not,
// keeping every pre-#614 legacy caller's error shape byte-identical.
func TestCallSyncErrorResponseToMirrorsWhetherSyncIDWasSupplied(t *testing.T) {
	withSyncID := ClientMessage{Type: ClientMessageTypeCallSync, CallID: callTestID, SyncID: callSyncTestSyncIDA}
	if got := callResponseTo(withSyncID); got != callSyncTestSyncIDA {
		t.Fatalf("response_to = %q, want the supplied sync_id %q", got, callSyncTestSyncIDA)
	}
	withoutSyncID := ClientMessage{Type: ClientMessageTypeCallSync, CallID: callTestID}
	if got := callResponseTo(withoutSyncID); got != "" {
		t.Fatalf("response_to = %q, want empty for a legacy call.sync", got)
	}

	handler := &fakeCallHandler{err: domain.ErrNotFound}
	hub := NewHub(&fakeAuthorizer{}, newTestLogger(), NopBus{}, "call-sync-error-correlation", WithCallHandler(handler))
	t.Cleanup(hub.Shutdown)
	client := newClient("sync-client", callTestCallee, callTestWorkspace, &fakeSender{})
	if !hub.Register(client) {
		t.Fatal("register client")
	}

	msg := ClientMessage{Type: ClientMessageTypeCallSync, CallID: callTestID, SyncID: callSyncTestSyncIDA}
	err := hub.handleClientMessage(context.Background(), client, msg)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("error = %v, want not found", err)
	}
	if !handleCallClientError(client, msg.Type, msg.CallID, callResponseTo(msg), err) {
		t.Fatal("expected call.sync error to be handled")
	}
	var response clientErrorResponse
	if err := json.Unmarshal(<-client.outbox, &response); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if response.Type != "call.error" || response.Operation != "call.sync" ||
		response.Code != "call_not_found" || response.ResponseTo != callSyncTestSyncIDA {
		t.Fatalf("unexpected call error: %+v", response)
	}
}
