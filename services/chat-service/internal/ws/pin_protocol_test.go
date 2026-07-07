package ws

import (
	"strings"
	"testing"
)

func remotePinEvt(pinned bool) Event {
	evt := remoteEvt(testEventID2)
	evt.Type = EventTypePinUpdated
	evt.Pin = &PinEventPayload{MessageID: testMessageID, ActorUserID: testEventIDEcho, Pinned: pinned}
	return evt
}

func TestCanonicalizeRemotePin_ValidRoutePreservesFlag(t *testing.T) {
	evt := remotePinEvt(true)
	evt.WorkspaceID = strings.ToUpper(testWorkspaceID)
	evt.Pin.ActorUserID = strings.ToUpper(testEventIDEcho)

	canonical, ok := canonicalizeRemoteEvent(evt)
	if !ok {
		t.Fatal("expected valid remote pin route to canonicalize")
	}
	if canonical.Pin == nil || !canonical.Pin.Pinned {
		t.Fatalf("pin flag must survive canonicalization, got %+v", canonical.Pin)
	}
	if canonical.Pin.ActorUserID != testEventIDEcho {
		t.Errorf("actor not canonicalized: %q", canonical.Pin.ActorUserID)
	}
	if canonical.WorkspaceID != testWorkspaceID {
		t.Errorf("workspace not canonicalized: %q", canonical.WorkspaceID)
	}
	// A pin event carries no message body — nothing sensitive to leak.
	if canonical.Payload != nil {
		t.Fatal("pin event must not carry a message payload")
	}
}

func TestCanonicalizeRemotePin_RejectsInvalidPayload(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Event)
	}{
		{name: "missing payload", mutate: func(e *Event) { e.Pin = nil }},
		{name: "message mismatch", mutate: func(e *Event) { e.Pin.MessageID = testEventID }},
		{name: "invalid actor", mutate: func(e *Event) { e.Pin.ActorUserID = "not-a-uuid" }},
		{name: "wrong target type", mutate: func(e *Event) { e.TargetType = TargetTypeDM }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			evt := remotePinEvt(false)
			tt.mutate(&evt)
			if _, ok := canonicalizeRemoteEvent(evt); ok {
				t.Fatal("expected invalid remote pin to be rejected")
			}
		})
	}
}
