package ws

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// Issue #824. The acknowledgement event is an invalidation hint, and these tests
// pin the two properties that make that safe: it reaches exactly the
// conversation it happened in, and it carries nothing about who answered.

const ackOtherChannelID = "77777777-7777-4777-8777-777777777777"

func remoteAcknowledgementEvt() Event {
	evt := remoteEvt(testEventID2)
	evt.Type = EventTypeAcknowledgementUpdated
	evt.MessageID = testMessageID
	return evt
}

// The subscribers of the conversation are told; a reader of another conversation
// in the same workspace is not, and neither is somebody the authorizer refuses.
func TestHub_AcknowledgementUpdatedReachesOnlyTheConversation(t *testing.T) {
	auth := &fakeAuthorizer{}
	auth.setAccess("sender", testWorkspaceID, TargetTypeChannel, testChannelID, true)
	auth.setAccess("elsewhere", testWorkspaceID, TargetTypeChannel, ackOtherChannelID, true)
	hub := NewHub(auth, newTestLogger(), NopBus{}, "test-ack-delivery")
	t.Cleanup(hub.Shutdown)

	sender := newClient("client-sender", "sender", testWorkspaceID, &fakeSender{})
	elsewhere := newClient("client-elsewhere", "elsewhere", testWorkspaceID, &fakeSender{})
	registerInRunningHub(t, hub, sender)
	registerInRunningHub(t, hub, elsewhere)
	if err := hub.Subscribe(context.Background(), sender, TargetTypeChannel, testChannelID); err != nil {
		t.Fatalf("sender subscribe: %v", err)
	}
	if err := hub.Subscribe(context.Background(), elsewhere, TargetTypeChannel, ackOtherChannelID); err != nil {
		t.Fatalf("elsewhere subscribe: %v", err)
	}

	hub.PublishAcknowledgementUpdated(
		context.Background(), testWorkspaceID, TargetTypeChannel, testChannelID, testMessageID)

	select {
	case raw := <-sender.outbox:
		assertAcknowledgementEnvelope(t, raw)
	case <-time.After(time.Second):
		t.Fatal("a subscriber of the conversation did not receive the acknowledgement event")
	}
	select {
	case raw := <-elsewhere.outbox:
		t.Fatalf("another conversation received the acknowledgement event: %s", string(raw))
	default:
	}
}

// assertAcknowledgementEnvelope is the security half: the event routes, and the
// per-recipient answer it would be catastrophic to broadcast is simply not in it.
func assertAcknowledgementEnvelope(t *testing.T, raw []byte) {
	t.Helper()
	evt, err := decodeEvent(raw)
	if err != nil {
		t.Fatal(err)
	}
	if evt.Type != EventTypeAcknowledgementUpdated {
		t.Fatalf("event type = %q", evt.Type)
	}
	if evt.TargetType != TargetTypeChannel || evt.TargetID != testChannelID {
		t.Fatalf("event routed to %s/%s", evt.TargetType, evt.TargetID)
	}
	if evt.MessageID != testMessageID {
		t.Fatalf("event message id = %q, want the message whose acknowledgement changed", evt.MessageID)
	}
	// Nothing about the acknowledgement itself travels. A subscriber learns
	// "re-read this message" and has to ask the authorised endpoint for the rest,
	// which is the only place the server can decide what *this* reader may see.
	if evt.Payload != nil || evt.Pin != nil || evt.Reaction != nil {
		t.Fatalf("acknowledgement event carries a payload: %+v", evt)
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"recipients", "recipient_id", "viewer_state", "state",
		"acknowledged", "pending", "total", "summary", "resolved_at",
	} {
		if _, present := envelope[forbidden]; present {
			t.Fatalf("acknowledgement event exposes %q on the wire: %s", forbidden, string(raw))
		}
	}
}

// A subscriber the authorizer refuses never subscribed, so the fan-out has
// nobody to reach — the same guarantee every other conversation-scoped event has.
func TestHub_AcknowledgementUpdatedSkipsUnauthorizedReader(t *testing.T) {
	auth := &fakeAuthorizer{}
	auth.setAccess("member", testWorkspaceID, TargetTypeChannel, testChannelID, true)
	auth.setAccess("outsider", testWorkspaceID, TargetTypeChannel, testChannelID, false)
	hub := NewHub(auth, newTestLogger(), NopBus{}, "test-ack-auth")
	t.Cleanup(hub.Shutdown)

	member := newClient("client-member", "member", testWorkspaceID, &fakeSender{})
	outsider := newClient("client-outsider", "outsider", testWorkspaceID, &fakeSender{})
	registerInRunningHub(t, hub, member)
	registerInRunningHub(t, hub, outsider)
	if err := hub.Subscribe(context.Background(), member, TargetTypeChannel, testChannelID); err != nil {
		t.Fatalf("member subscribe: %v", err)
	}
	_ = hub.Subscribe(context.Background(), outsider, TargetTypeChannel, testChannelID)

	hub.PublishAcknowledgementUpdated(
		context.Background(), testWorkspaceID, TargetTypeChannel, testChannelID, testMessageID)

	select {
	case <-member.outbox:
	case <-time.After(time.Second):
		t.Fatal("the authorized member received nothing")
	}
	select {
	case raw := <-outsider.outbox:
		t.Fatalf("an unauthorized reader received the acknowledgement event: %s", string(raw))
	default:
	}
}

// An incomplete route announces nothing rather than fanning out somewhere
// unintended.
func TestHub_AcknowledgementUpdatedRefusesAnIncompleteRoute(t *testing.T) {
	auth := &fakeAuthorizer{}
	auth.setAccess("member", testWorkspaceID, TargetTypeChannel, testChannelID, true)
	hub := NewHub(auth, newTestLogger(), NopBus{}, "test-ack-route")
	t.Cleanup(hub.Shutdown)
	member := newClient("client-member", "member", testWorkspaceID, &fakeSender{})
	registerInRunningHub(t, hub, member)
	if err := hub.Subscribe(context.Background(), member, TargetTypeChannel, testChannelID); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	for _, incomplete := range []struct{ workspace, target, message string }{
		{"", testChannelID, testMessageID},
		{testWorkspaceID, "", testMessageID},
		{testWorkspaceID, testChannelID, ""},
	} {
		hub.PublishAcknowledgementUpdated(
			context.Background(), incomplete.workspace, TargetTypeChannel, incomplete.target, incomplete.message)
	}
	select {
	case raw := <-member.outbox:
		t.Fatalf("an incomplete route was broadcast: %s", string(raw))
	case <-time.After(50 * time.Millisecond):
	}
}

// A relayed event from another instance is accepted and canonicalized like the
// other conversation-scoped types, so a rolling deploy keeps delivering it.
func TestCanonicalizeRemoteAcknowledgement_AcceptsAConversationRoute(t *testing.T) {
	evt := remoteAcknowledgementEvt()
	evt.WorkspaceID = strings.ToUpper(testWorkspaceID)

	canonical, ok := canonicalizeRemoteEvent(evt)
	if !ok {
		t.Fatal("expected a valid remote acknowledgement route to canonicalize")
	}
	if canonical.WorkspaceID != testWorkspaceID {
		t.Errorf("workspace not canonicalized: %q", canonical.WorkspaceID)
	}
	if canonical.MessageID != testMessageID {
		t.Errorf("message id not preserved: %q", canonical.MessageID)
	}
	if canonical.Payload != nil {
		t.Fatal("a relayed acknowledgement event must carry no message payload")
	}
}

// A relayed event that names a recipient is refused: RecipientUserID is what
// makes delivery bypass subscriptions, and this event must never do that.
func TestCanonicalizeRemoteAcknowledgement_RefusesARecipientScope(t *testing.T) {
	evt := remoteAcknowledgementEvt()
	evt.RecipientUserID = testEventIDEcho
	if _, ok := canonicalizeRemoteEvent(evt); ok {
		t.Fatal("expected a recipient-scoped acknowledgement event to be rejected")
	}
}
