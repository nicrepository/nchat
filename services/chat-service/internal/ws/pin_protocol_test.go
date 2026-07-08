package ws

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
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

func TestCanonicalizePinEvent_AcceptsChannelAndDMOnly(t *testing.T) {
	for _, targetType := range []TargetType{TargetTypeChannel, TargetTypeDM} {
		t.Run(string(targetType), func(t *testing.T) {
			evt := remotePinEvt(true)
			evt.TargetType = targetType
			if _, ok := canonicalizePinEvent(evt); !ok {
				t.Fatalf("expected %s pin event to be accepted", targetType)
			}
		})
	}

	evt := remotePinEvt(true)
	evt.TargetType = TargetType("thread")
	if _, ok := canonicalizePinEvent(evt); ok {
		t.Fatal("expected non-channel/dm pin event to be rejected")
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
		{name: "wrong target type", mutate: func(e *Event) { e.TargetType = TargetType("thread") }},
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

func TestHub_PinUpdatedDeliveredOnlyToReadableSubscriber(t *testing.T) {
	auth := &fakeAuthorizer{}
	auth.setAccess("member", testWorkspaceID, TargetTypeChannel, testChannelID, true)
	auth.setAccess("outsider", testWorkspaceID, TargetTypeChannel, testChannelID, false)
	hub := NewHub(auth, newTestLogger(), NopBus{}, "test-pin-delivery")
	t.Cleanup(hub.Shutdown)

	member := newClient("client-member", "member", testWorkspaceID, &fakeSender{})
	outsider := newClient("client-outsider", "outsider", testWorkspaceID, &fakeSender{})
	registerInRunningHub(t, hub, member)
	registerInRunningHub(t, hub, outsider)
	if err := hub.Subscribe(context.Background(), member, TargetTypeChannel, testChannelID); err != nil {
		t.Fatalf("member subscribe: %v", err)
	}
	if err := hub.Subscribe(context.Background(), outsider, TargetTypeChannel, testChannelID); !errors.Is(err, ErrSubscribeForbidden) {
		t.Fatalf("outsider subscribe: expected forbidden, got %v", err)
	}

	hub.PublishPinUpdated(context.Background(), testWorkspaceID, TargetTypeChannel, testChannelID, testMessageID, testEventIDEcho, true)

	select {
	case raw := <-member.outbox:
		evt, err := decodeEvent(raw)
		if err != nil {
			t.Fatal(err)
		}
		if evt.Type != EventTypePinUpdated || evt.TargetType != TargetTypeChannel || evt.TargetID != testChannelID {
			t.Fatalf("unexpected event: %+v", evt)
		}
	case <-time.After(time.Second):
		t.Fatal("member did not receive pin.updated")
	}
	select {
	case raw := <-outsider.outbox:
		t.Fatalf("outsider received pin.updated: %s", string(raw))
	default:
	}
}
