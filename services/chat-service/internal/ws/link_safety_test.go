package ws

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func linkSafetyChangedEvent() Event {
	updatedAt := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	return Event{
		SchemaVersion: CurrentEventSchemaVersion, Type: EventTypeMessageLinkSafetyChanged,
		WorkspaceID: testWorkspaceID, TargetType: TargetTypeChannel, TargetID: testChannelID,
		MessageID: testMessageID,
		LinkSafety: &MessageLinkSafetyPayload{
			MessageID: testMessageID, State: "inconclusive", UpdatedAt: updatedAt,
		},
		EventID: testEventID, SourceInstanceID: "instance-B", CreatedAt: updatedAt,
	}
}

func TestCanonicalizeRemoteLinkSafetyEvent(t *testing.T) {
	valid := linkSafetyChangedEvent()
	valid.Payload = &MessagePayload{BodyText: "https://secret.example/path"}
	valid.Typing = &TypingEventPayload{UserID: testMessageID, IsTyping: true}

	canonical, ok := canonicalizeRemoteEvent(valid)
	if !ok {
		t.Fatal("valid message.link_safety_changed event was rejected")
	}
	if canonical.LinkSafety == nil || canonical.LinkSafety.State != "inconclusive" {
		t.Fatalf("LinkSafety = %+v, want inconclusive", canonical.LinkSafety)
	}
	if canonical.Payload != nil || canonical.Typing != nil {
		t.Fatalf("foreign payload survived: %+v", canonical)
	}

	typing := typingUpdatedEvent()
	typing.LinkSafety = valid.LinkSafety
	canonical, ok = canonicalizeRemoteEvent(typing)
	if !ok || canonical.Typing == nil {
		t.Fatal("typing.updated was not preserved")
	}
	if canonical.LinkSafety != nil {
		t.Fatal("link-safety payload survived on typing.updated")
	}
}

func TestCanonicalizeRemoteLinkSafetyEventRejectsInvalidPayload(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*Event)
	}{
		{name: "missing payload", mutate: func(event *Event) { event.LinkSafety = nil }},
		{name: "missing envelope message", mutate: func(event *Event) { event.MessageID = "" }},
		{name: "mismatched message", mutate: func(event *Event) { event.LinkSafety.MessageID = testEventID2 }},
		{name: "unknown state", mutate: func(event *Event) { event.LinkSafety.State = "future" }},
		{name: "missing timestamp", mutate: func(event *Event) { event.LinkSafety.UpdatedAt = time.Time{} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			event := linkSafetyChangedEvent()
			test.mutate(&event)
			if _, ok := canonicalizeRemoteEvent(event); ok {
				t.Fatal("invalid link-safety event was accepted")
			}
		})
	}
}

func TestPublishMessageLinkSafetyChangedRoutesSanitizedPayload(t *testing.T) {
	bus := &fakeBus{}
	hub := newBusTestHub(NopAuthorizer{}, bus)
	t.Cleanup(hub.Shutdown)
	updatedAt := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)

	hub.PublishMessageLinkSafetyChanged(
		t.Context(), testWorkspaceID, TargetTypeChannel, testChannelID,
		testMessageID, "malicious", updatedAt,
	)
	event, ok := bus.lastPublished()
	if !ok {
		t.Fatal("message.link_safety_changed was not published")
	}
	if event.Type != EventTypeMessageLinkSafetyChanged || event.TargetID != testChannelID ||
		event.MessageID != testMessageID || event.LinkSafety == nil ||
		event.LinkSafety.MessageID != testMessageID || event.LinkSafety.State != "malicious" ||
		!event.LinkSafety.UpdatedAt.Equal(updatedAt) {
		t.Fatalf("unexpected event: %+v", event)
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"https://", "scan_uuid", "provider", "body_text"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("event leaked %q: %s", secret, encoded)
		}
	}

	hub.PublishMessageLinkSafetyChanged(
		t.Context(), testWorkspaceID, TargetTypeChannel, testChannelID,
		testMessageID, "future", updatedAt,
	)
	if got := bus.publishCount(); got != 1 {
		t.Fatalf("unknown state changed publish count to %d", got)
	}
}

func TestPublishMessageBlockedIsRecipientScopedAndSanitized(t *testing.T) {
	bus := &fakeBus{}
	hub := newBusTestHub(NopAuthorizer{}, bus)
	t.Cleanup(hub.Shutdown)
	recipientID := "33333333-3333-3333-3333-333333333333"

	hub.PublishMessageBlocked(
		t.Context(), testWorkspaceID, recipientID, testMessageID, MessageBlockedReasonMaliciousLink,
	)
	event, ok := bus.lastPublished()
	if !ok {
		t.Fatal("message.blocked was not published")
	}
	if event.Type != EventTypeMessageBlocked || event.RecipientUserID != recipientID ||
		event.MessageID != testMessageID || event.Reason != MessageBlockedReasonMaliciousLink ||
		event.TargetID != "" || event.LinkSafety != nil {
		t.Fatalf("unexpected event: %+v", event)
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"https://", "scan_uuid", "provider", "body_text", "canonical_url"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("event leaked %q: %s", secret, encoded)
		}
	}

	hub.PublishMessageBlocked(t.Context(), testWorkspaceID, recipientID, "", MessageBlockedReasonMaliciousLink)
	if got := bus.publishCount(); got != 1 {
		t.Fatalf("invalid event changed publish count to %d", got)
	}
}

// A bus that refuses the publish must not swallow the local half. Both of these
// carry a security correction that is already committed in the database, so the
// sessions on this instance are told regardless — a Valkey outage may cost the
// other pods their copy, never this one.
func TestLinkSafetyPublishSurvivesABusFailure(t *testing.T) {
	updatedAt := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	recipientID := "33333333-3333-3333-3333-333333333333"

	hub := newBusTestHub(NopAuthorizer{}, &fakeBus{publishErr: errors.New("valkey unavailable")})
	t.Cleanup(hub.Shutdown)
	local := registerForTest(hub, "c-blocked", recipientID, testWorkspaceID)

	hub.PublishMessageBlocked(
		t.Context(), testWorkspaceID, recipientID, testMessageID, MessageBlockedReasonMaliciousLink,
	)
	if got := len(drain(local)); got != 1 {
		t.Fatalf("message.blocked local delivery got %d message(s), want 1 despite the bus failure", got)
	}

	// The broadcast half is the same promise on the other publisher: it returns
	// rather than blocking or panicking, and the correction still reached the
	// local fan-out ahead of the failed bus hop.
	hub.PublishMessageLinkSafetyChanged(
		t.Context(), testWorkspaceID, TargetTypeChannel, testChannelID,
		testMessageID, "malicious", updatedAt,
	)
}
