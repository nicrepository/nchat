package ws

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// message.link_updated (issue #807): the per-link payload survives the bus only
// when every closed set validates, and an href never rides on anything but a
// direct link.

func safeLinkPayload() LinkPayload {
	return LinkPayload{
		TargetKey: strings.Repeat("ab", 16),
		URL:       "https://example.test/a", Hostname: "example.test", Safety: "safe", Click: "direct",
		Href: "https://example.test/a", UpdatedAt: time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC),
		Preview: &LinkPreviewPayload{State: "ready", Hostname: "example.test", Title: "T", ImageID: "img-1"},
	}
}

func linkUpdatedEvent() Event {
	return Event{
		SchemaVersion: CurrentEventSchemaVersion, Type: EventTypeMessageLinkUpdated,
		WorkspaceID: testWorkspaceID, TargetType: TargetTypeChannel, TargetID: testChannelID,
		MessageID:  testMessageID,
		LinkUpdate: &MessageLinkUpdatePayload{MessageID: testMessageID, Link: safeLinkPayload()},
		EventID:    testEventID, SourceInstanceID: "instance-B", CreatedAt: time.Now().UTC(),
	}
}

func TestCanonicalizeRemoteLinkUpdateEvent(t *testing.T) {
	valid := linkUpdatedEvent()
	valid.Payload = &MessagePayload{BodyText: "https://secret.example/path"}

	canonical, ok := canonicalizeRemoteEvent(valid)
	if !ok {
		t.Fatal("valid message.link_updated event was rejected")
	}
	if canonical.LinkUpdate == nil || canonical.LinkUpdate.Link.Href != "https://example.test/a" || canonical.Payload != nil {
		t.Fatalf("canonical = %+v", canonical)
	}

	// The block belongs to one event type only.
	typing := typingUpdatedEvent()
	typing.LinkUpdate = valid.LinkUpdate
	canonical, ok = canonicalizeRemoteEvent(typing)
	if !ok || canonical.LinkUpdate != nil {
		t.Fatal("link update survived on typing.updated")
	}
}

func TestCanonicalizeRemoteLinkUpdateEventRejectsInvalidPayload(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*Event)
	}{
		{name: "missing block", mutate: func(e *Event) { e.LinkUpdate = nil }},
		{name: "missing target key", mutate: func(e *Event) { e.LinkUpdate.Link.TargetKey = "" }},
		{name: "malformed target key", mutate: func(e *Event) { e.LinkUpdate.Link.TargetKey = strings.Repeat("zz", 16) }},
		{name: "missing envelope message", mutate: func(e *Event) { e.MessageID = "" }},
		{name: "mismatched message", mutate: func(e *Event) { e.LinkUpdate.MessageID = testEventID2 }},
		{name: "unknown safety", mutate: func(e *Event) { e.LinkUpdate.Link.Safety = "trusted" }},
		{name: "unknown click", mutate: func(e *Event) { e.LinkUpdate.Link.Click = "popup" }},
		{name: "href on an interstitial", mutate: func(e *Event) {
			e.LinkUpdate.Link.Safety, e.LinkUpdate.Link.Click = "unknown", "interstitial"
		}},
		{name: "unknown preview state", mutate: func(e *Event) { e.LinkUpdate.Link.Preview.State = "rendered" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			event := linkUpdatedEvent()
			test.mutate(&event)
			if _, ok := canonicalizeRemoteEvent(event); ok {
				t.Fatal("invalid link update was accepted")
			}
		})
	}
}

func TestPublishMessageLinkUpdatedRoutesTheEntity(t *testing.T) {
	bus := &fakeBus{}
	hub := newBusTestHub(NopAuthorizer{}, bus)
	t.Cleanup(hub.Shutdown)

	hub.PublishMessageLinkUpdated(t.Context(), testWorkspaceID, TargetTypeChannel, testChannelID, testMessageID, safeLinkPayload())
	event, ok := bus.lastPublished()
	if !ok {
		t.Fatal("message.link_updated was not published")
	}
	if event.Type != EventTypeMessageLinkUpdated || event.MessageID != testMessageID || event.LinkUpdate == nil ||
		event.LinkUpdate.Link.Href != "https://example.test/a" || event.LinkUpdate.Link.Preview == nil {
		t.Fatalf("unexpected event: %+v", event)
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"link_update"`) || strings.Contains(string(encoded), "image_url") {
		t.Fatalf("encoded = %s", encoded)
	}

	// A payload this version cannot vouch for is not published at all.
	bad := safeLinkPayload()
	bad.Click = "interstitial"
	hub.PublishMessageLinkUpdated(t.Context(), testWorkspaceID, TargetTypeChannel, testChannelID, testMessageID, bad)
	hub.PublishMessageLinkUpdated(t.Context(), testWorkspaceID, TargetTypeChannel, testChannelID, "", safeLinkPayload())
	if got := bus.publishCount(); got != 1 {
		t.Fatalf("publish count = %d", got)
	}
}
