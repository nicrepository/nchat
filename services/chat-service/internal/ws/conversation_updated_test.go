package ws

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// conversation.updated (issue #527).
//
// A rename is target-scoped: it goes to the people already subscribed to the
// channel, telling them their view of it is stale. Its remote path is therefore
// the ordinary broadcast one — canonicalize, queue, re-check each subscriber's
// access at fan-out — the same as members.added.
//
// What makes it different from every other event here is that it carries no
// payload at all. The assertions below are mostly about that: the new name never
// travels over the bus, so nothing has to be stripped to keep it out of an
// unauthorized reader's socket.

func conversationUpdatedEvent() Event {
	return Event{
		SchemaVersion: CurrentEventSchemaVersion, Type: EventTypeConversationUpdated,
		WorkspaceID: maWorkspace, TargetType: TargetTypeChannel, TargetID: maChannel,
		EventID:          "1d8c4b52-7e3a-4f96-b105-8c2d7e4a3b91",
		SourceInstanceID: "instance-B",
		CreatedAt:        time.Now().UTC(),
	}
}

func TestCanonicalizeRemoteConversationUpdatedIsAccepted(t *testing.T) {
	evt := conversationUpdatedEvent()
	evt.WorkspaceID = strings.ToUpper(maWorkspace)
	evt.TargetID = strings.ToUpper(maChannel)

	canonical, ok := canonicalizeRemoteEvent(evt)
	if !ok {
		t.Fatal("a valid conversation.updated must canonicalize; without this a rename never crosses instances")
	}
	if canonical.WorkspaceID != maWorkspace || canonical.TargetID != maChannel {
		t.Fatalf("scope = %s/%s, want the canonicalized envelope", canonical.WorkspaceID, canonical.TargetID)
	}
	if canonical.RecipientUserID != "" {
		t.Fatalf("RecipientUserID = %q; conversation.updated must stay target-scoped", canonical.RecipientUserID)
	}
}

func TestCanonicalizeRemoteConversationUpdatedRejectsMalformedEnvelopes(t *testing.T) {
	for name, mutate := range map[string]func(*Event){
		"workspace not a uuid": func(e *Event) { e.WorkspaceID = "nope" },
		"no workspace":         func(e *Event) { e.WorkspaceID = "" },
		"target not a uuid":    func(e *Event) { e.TargetID = "nope" },
		"no target":            func(e *Event) { e.TargetID = "" },
		"unknown target type":  func(e *Event) { e.TargetType = "elsewhere" },
		"bad event id":         func(e *Event) { e.EventID = "nope" },
		"no source instance":   func(e *Event) { e.SourceInstanceID = "" },
		"unknown schema":       func(e *Event) { e.SchemaVersion = 99 },
	} {
		t.Run(name, func(t *testing.T) {
			evt := conversationUpdatedEvent()
			mutate(&evt)
			if canonical, ok := canonicalizeRemoteEvent(evt); ok {
				t.Fatalf("a malformed conversation.updated was accepted: %+v", canonical)
			}
		})
	}
}

// A remote producer cannot smuggle a name, a body or an actor along with the
// invalidation signal: the new name reaches a client only through the sidebar
// endpoint, which authorizes for itself.
func TestRemoteConversationUpdatedCarriesNothingButTheRoute(t *testing.T) {
	evt := conversationUpdatedEvent()
	evt.Payload = &MessagePayload{ID: maChannel, BodyText: "sensitive message body", SenderID: maOutsider}
	evt.MessageUpdate = &MessageUpdatedPayload{MessageID: maChannel, Body: "sensitive edit"}
	evt.Presence = &PresencePayload{UserID: maOutsider, State: "online"}
	evt.LinkSafety = &MessageLinkSafetyPayload{MessageID: maChannel, State: "safe"}
	evt.Members = &MembersAddedPayload{ActorUserID: maOutsider, AddedCount: 1, MemberCount: 9}
	evt.Pin = &PinEventPayload{MessageID: maChannel, ActorUserID: maOutsider, Pinned: true}

	canonical, ok := canonicalizeRemoteEvent(evt)
	if !ok {
		t.Fatal("expected the event to canonicalize")
	}
	if canonical.Members != nil || canonical.Pin != nil || canonical.Payload != nil {
		t.Fatalf("a foreign payload rode along on conversation.updated: %+v", canonical)
	}
	data, err := json.Marshal(canonical)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, leak := range []string{"sensitive", maOutsider, "body_text", "display_name", "email", "presence"} {
		if strings.Contains(string(data), leak) {
			t.Fatalf("conversation.updated carried %q: %s", leak, data)
		}
	}
}

// The headline case: pod A persists the rename, the reader's socket is on pod B,
// and B delivers so that reader's sidebar refetches without a reload.
func TestConversationUpdatedCrossesInstances(t *testing.T) {
	auth := &recordingAuthorizer{allowed: true}
	_, hubB, bus := twoHubsWithAuthorizer(t, auth)
	startHubLoop(t, hubB)
	subscriber := registerForTest(hubB, "c-remote", maMember, maWorkspace)
	subscribeForTest(hubB, subscriber, TargetTypeChannel, maChannel)

	bus.inject(conversationUpdatedEvent())
	if queued := deliverRemoteBroadcast(t, hubB); queued != 1 {
		t.Fatalf("%d remote broadcast(s) queued, want 1", queued)
	}

	messages := drain(subscriber)
	if len(messages) != 1 {
		t.Fatalf("remote subscriber got %d message(s), want 1", len(messages))
	}
	var evt Event
	if err := json.Unmarshal(messages[0], &evt); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if evt.Type != EventTypeConversationUpdated || evt.TargetID != maChannel {
		t.Fatalf("wrong event delivered: %+v", evt)
	}
}

// The same event twice is indistinguishable from once for anything downstream:
// it carries no state, so a duplicate can only cost one extra refetch.
func TestConversationUpdatedIsIdempotentOnRepeat(t *testing.T) {
	auth := &recordingAuthorizer{allowed: true}
	_, hubB, bus := twoHubsWithAuthorizer(t, auth)
	startHubLoop(t, hubB)
	subscriber := registerForTest(hubB, "c-remote", maMember, maWorkspace)
	subscribeForTest(hubB, subscriber, TargetTypeChannel, maChannel)

	first := conversationUpdatedEvent()
	second := conversationUpdatedEvent()
	second.EventID = "2e9d5c63-8f4b-4a07-c216-9d3e8f5b4c02"
	bus.inject(first)
	bus.inject(second)
	deliverRemoteBroadcast(t, hubB)

	var seen []Event
	for _, raw := range drain(subscriber) {
		var evt Event
		if err := json.Unmarshal(raw, &evt); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		seen = append(seen, evt)
	}
	for _, evt := range seen {
		if evt.TargetID != maChannel {
			t.Fatalf("a repeat named a different channel: %+v", evt)
		}
	}
}

// A subscription is not a standing permission: a reader who lost access to the
// channel is not told it was renamed.
func TestRemoteConversationUpdatedIsNotDeliveredToAnUnauthorizedSubscriber(t *testing.T) {
	auth := &recordingAuthorizer{allowed: false}
	_, hubB, bus := twoHubsWithAuthorizer(t, auth)
	startHubLoop(t, hubB)
	subscriber := registerForTest(hubB, "c-remote", maMember, maWorkspace)
	subscribeForTest(hubB, subscriber, TargetTypeChannel, maChannel)

	bus.inject(conversationUpdatedEvent())
	deliverRemoteBroadcast(t, hubB)

	if got := len(drain(subscriber)); got != 0 {
		t.Fatalf("a subscriber whose access was revoked received %d frame(s)", got)
	}
}
