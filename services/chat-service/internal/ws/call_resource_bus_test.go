package ws

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

// canonicalizeCallEvent (issue #622 fix to #546): before this fix it
// unconditionally required event.TargetType == TargetTypeUser, so every
// resource (channel/group-DM) call event arriving from another chat-service
// instance was dropped at the trust boundary — a resource call could go
// active/end locally and every other pod would simply never relay it.

const (
	crbWorkspace = "b1000000-0000-4000-8000-000000000001"
	crbChannel   = "b1000000-0000-4000-8000-000000000002"
	crbDM        = "b1000000-0000-4000-8000-000000000003"
	crbCallID    = "b1000000-0000-4000-8000-000000000004"
	crbRequestID = "b1000000-0000-4000-8000-000000000005"
	crbCaller    = "b1000000-0000-4000-8000-000000000006"
	crbSubscribr = "b1000000-0000-4000-8000-000000000007"
	crbOtherRoom = "b1000000-0000-4000-8000-000000000008"
)

func resourceCallEvent(targetType TargetType, targetID string, domainTargetType domain.CallTargetType) Event {
	now := time.Now().UTC()
	return Event{
		SchemaVersion: CurrentEventSchemaVersion,
		Type:          EventTypeCallAccepted,
		WorkspaceID:   crbWorkspace,
		TargetType:    targetType,
		TargetID:      targetID,
		Call: &CallEventPayload{
			ID: crbCallID, RequestID: crbRequestID, CallerID: crbCaller,
			TargetType: domainTargetType, TargetID: targetID,
			CallType: domain.CallTypeAudio, Status: domain.CallStatusActive, Version: 1,
			CreatedAt: now, OccurredAt: now, ExpiresAt: now.Add(30 * time.Second),
		},
		EventID:          "b2000000-0000-4000-8000-000000000001",
		SourceInstanceID: "pod-a",
		CreatedAt:        now,
	}
}

func TestCanonicalizeRemoteResourceCallEventAccepted(t *testing.T) {
	for _, tt := range []struct {
		name             string
		targetType       TargetType
		targetID         string
		domainTargetType domain.CallTargetType
	}{
		{"channel", TargetTypeChannel, crbChannel, domain.CallTargetChannel},
		{"group dm", TargetTypeDM, crbDM, domain.CallTargetDM},
	} {
		t.Run(tt.name, func(t *testing.T) {
			evt := resourceCallEvent(tt.targetType, tt.targetID, tt.domainTargetType)
			canonical, ok := canonicalizeRemoteEvent(evt)
			if !ok {
				t.Fatal("a valid resource call event must canonicalize")
			}
			if canonical.Call == nil || canonical.Call.CalleeID != "" {
				t.Fatalf("resource call must carry no callee_id: %+v", canonical.Call)
			}
			if canonical.Call.TargetType != tt.domainTargetType || canonical.Call.TargetID != tt.targetID {
				t.Fatalf("call target = %s/%s, want %s/%s",
					canonical.Call.TargetType, canonical.Call.TargetID, tt.domainTargetType, tt.targetID)
			}
		})
	}
}

func TestCanonicalizeRemoteResourceCallEventRejectsMismatches(t *testing.T) {
	tests := map[string]func(*Event){
		"envelope target_id does not match call target_id": func(e *Event) {
			e.Call.TargetID = crbOtherRoom
		},
		"call target_type disagrees with envelope (dm claimed as channel)": func(e *Event) {
			e.Call.TargetType = domain.CallTargetDM
		},
		"resource call carries a callee_id": func(e *Event) {
			e.Call.CalleeID = crbSubscribr
		},
		"call id not a uuid": func(e *Event) {
			e.Call.ID = "not-a-uuid"
		},
		"request id not a uuid": func(e *Event) {
			e.Call.RequestID = "not-a-uuid"
		},
		"caller id not a uuid": func(e *Event) {
			e.Call.CallerID = "not-a-uuid"
		},
		"call target_id not a uuid (not merely mismatched)": func(e *Event) {
			e.Call.TargetID = "not-a-uuid"
		},
		"invalid status": func(e *Event) {
			e.Call.Status = "levitating"
		},
		"version below 1": func(e *Event) {
			e.Call.Version = 0
		},
		"event type disagrees with call status": func(e *Event) {
			e.Type = EventTypeCallEnded // Call.Status stays "active" from the fixture.
		},
		"zero created_at": func(e *Event) {
			e.Call.CreatedAt = time.Time{}
		},
		"unknown envelope target type": func(e *Event) {
			e.TargetType = "workspace"
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			evt := resourceCallEvent(TargetTypeChannel, crbChannel, domain.CallTargetChannel)
			mutate(&evt)
			if canonical, ok := canonicalizeRemoteEvent(evt); ok {
				t.Fatalf("a mismatched resource call event was accepted: %+v", canonical)
			}
		})
	}
}

// Direct (1:1) call events must keep working exactly as before #622: no
// target_type, no target_id on the call payload, callee_id required.
func TestCanonicalizeRemoteDirectCallEventUnaffectedByResourceSupport(t *testing.T) {
	now := time.Now().UTC()
	evt := Event{
		SchemaVersion: CurrentEventSchemaVersion, Type: EventTypeCallAccepted,
		WorkspaceID: crbWorkspace, TargetType: TargetTypeUser, TargetID: crbSubscribr,
		Call: &CallEventPayload{
			ID: crbCallID, RequestID: crbRequestID, CallerID: crbCaller, CalleeID: crbSubscribr,
			CallType: domain.CallTypeAudio, Status: domain.CallStatusActive, Version: 1,
			CreatedAt: now, OccurredAt: now, ExpiresAt: now.Add(30 * time.Second),
		},
		EventID: "b2000000-0000-4000-8000-000000000002", SourceInstanceID: "pod-a", CreatedAt: now,
	}
	canonical, ok := canonicalizeRemoteEvent(evt)
	if !ok {
		t.Fatal("a valid direct call event must still canonicalize")
	}
	if canonical.Call.CalleeID != crbSubscribr {
		t.Fatalf("direct call callee_id = %q", canonical.Call.CalleeID)
	}

	// The event's own target_id must still be caller or callee — an
	// unrelated target_id must still be rejected exactly as before.
	evt.TargetID = crbOtherRoom
	if _, ok := canonicalizeRemoteEvent(evt); ok {
		t.Fatal("a direct call event whose target_id is neither caller nor callee was accepted")
	}
}

// ── Cross-instance delivery ─────────────────────────────────────────────────

// The headline case for issue #622's bus fix: a resource call goes active on
// pod A, and a subscriber whose only socket is on pod B must still learn
// about it. Before the fix, canonicalizeCallEvent's TargetTypeUser-only
// requirement dropped this at the boundary and pod B never delivered it.
func TestPublishResourceCallCrossesInstances(t *testing.T) {
	auth := &recordingAuthorizer{allowed: true}
	_, hubB, bus := twoHubsWithAuthorizer(t, auth)
	startHubLoop(t, hubB)
	subscriber := registerForTest(hubB, "c-remote", crbSubscribr, crbWorkspace)
	subscribeForTest(hubB, subscriber, TargetTypeChannel, crbChannel)

	// The envelope pod A would have produced via Hub.PublishCall for this
	// call — constructed directly here, as the other cross-instance tests in
	// this package do, so the test exercises pod B's receive path without
	// needing pod A's own broadcast plumbing wired up.
	bus.inject(resourceCallEvent(TargetTypeChannel, crbChannel, domain.CallTargetChannel))
	if delivered := deliverRemoteBroadcast(t, hubB); delivered != 1 {
		t.Fatalf("dispatched %d remote broadcast(s), want 1", delivered)
	}

	messages := drain(subscriber)
	if len(messages) != 1 {
		t.Fatalf("remote subscriber got %d message(s), want 1", len(messages))
	}
	var evt Event
	if err := json.Unmarshal(messages[0], &evt); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if evt.Type != EventTypeCallAccepted || evt.Call == nil || evt.Call.ID != crbCallID {
		t.Fatalf("wrong event delivered: %+v", evt)
	}
}

// Terminal cross-instance is critical, not merely symmetric with accepted:
// it is the only way an observer whose socket is on a DIFFERENT pod ever
// learns the call ended and clears its "call in progress" indicator. Before
// the #622 bus fix this terminal event would have been dropped exactly like
// the accepted one — leaving every off-pod observer permanently stuck
// believing a call that already ended was still active.
func TestPublishResourceCallEndedCrossesInstances(t *testing.T) {
	auth := &recordingAuthorizer{allowed: true}
	_, hubB, bus := twoHubsWithAuthorizer(t, auth)
	startHubLoop(t, hubB)
	subscriber := registerForTest(hubB, "c-remote", crbSubscribr, crbWorkspace)
	subscribeForTest(hubB, subscriber, TargetTypeChannel, crbChannel)

	ended := resourceCallEvent(TargetTypeChannel, crbChannel, domain.CallTargetChannel)
	ended.Type = EventTypeCallEnded
	ended.Call.Status = domain.CallStatusEnded
	endedAt := ended.Call.OccurredAt
	ended.Call.EndedAt = &endedAt

	bus.inject(ended)
	if delivered := deliverRemoteBroadcast(t, hubB); delivered != 1 {
		t.Fatalf("dispatched %d remote broadcast(s), want 1", delivered)
	}

	messages := drain(subscriber)
	if len(messages) != 1 {
		t.Fatalf("remote subscriber got %d message(s), want 1", len(messages))
	}
	var evt Event
	if err := json.Unmarshal(messages[0], &evt); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if evt.Type != EventTypeCallEnded || evt.Call == nil || evt.Call.Status != domain.CallStatusEnded ||
		evt.Call.EndedAt == nil {
		t.Fatalf("wrong event delivered: %+v", evt)
	}
}

// The negative counterpart: an envelope whose target does not match the call
// payload's own persisted target must be dropped before it ever reaches the
// broadcast queue — never delivered to anyone, on any instance.
func TestMismatchedResourceCallEventNeverReachesTheBroadcastQueue(t *testing.T) {
	auth := &recordingAuthorizer{allowed: true}
	_, hubB, _ := twoHubsWithAuthorizer(t, auth)
	startHubLoop(t, hubB)
	subscriber := registerForTest(hubB, "c-remote", crbSubscribr, crbWorkspace)
	subscribeForTest(hubB, subscriber, TargetTypeChannel, crbChannel)

	poisoned := resourceCallEvent(TargetTypeChannel, crbChannel, domain.CallTargetChannel)
	poisoned.Call.TargetID = crbOtherRoom
	hubB.handleRemoteBusEvent(poisoned)

	if delivered := deliverRemoteBroadcast(t, hubB); delivered != 0 {
		t.Fatalf("dispatched %d broadcast(s) for a mismatched event, want 0", delivered)
	}
	if got := len(drain(subscriber)); got != 0 {
		t.Fatalf("subscriber got %d message(s) from a mismatched event, want 0", got)
	}
}
