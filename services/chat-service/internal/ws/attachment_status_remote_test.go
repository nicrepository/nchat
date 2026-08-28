package ws

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// attachment.status across services (RF-22).
//
// This is the only event in the protocol whose producer is not a chat-service
// instance: file-service publishes it after a malware verdict is persisted, on
// the same Valkey bus every other cross-instance event uses. So the tests below
// are about a trust boundary rather than a rolling deploy — the envelope arrives
// from another program, and everything that decides who sees it has to be
// re-derived on this side.
//
// The routing is the ordinary target-scoped one: canonicalize, hand to the
// broadcast queue, re-check each subscriber's access at fan-out. It is not the
// recipient-addressed path conversation.available takes, and it must not become
// one — an event that named its own recipient could be aimed at anybody.

const (
	asWorkspace  = "5c2e8a41-7b3d-4f19-8e60-2a4c6b8d1e93"
	asChannel    = "3f7b1d95-8c4a-4e26-b071-5d9e2f4a7c18"
	asAttachment = "9a4c7e21-5f8b-4d63-8194-6e2b5a9c3d07"
	asSubscriber = "1e6d3b87-4a29-4c50-9f83-7b1c4e8a2d65"
	asOutsider   = "8b5f2c93-6d17-4a84-b3e2-9c5a1f7d4b60"
	asOtherRoom  = "2d9e4a16-3b78-4c05-8f91-6a3d7b2e5c84"
)

// attachmentStatusEvent is the envelope file-service puts on the bus.
func attachmentStatusEvent() Event {
	return Event{
		SchemaVersion: CurrentEventSchemaVersion, Type: EventTypeAttachmentStatus,
		WorkspaceID: asWorkspace, TargetType: TargetTypeChannel, TargetID: asChannel,
		Attachment: &AttachmentStatusPayload{
			AttachmentID: asAttachment,
			Status:       "clean",
			UpdatedAt:    "2026-08-07T12:00:00Z",
		},
		EventID:          "7c1a4e style-placeholder",
		SourceInstanceID: "file-service-0",
		CreatedAt:        time.Now().UTC(),
	}
}

// validAttachmentStatusEvent fixes the event id, which the helper above leaves
// deliberately unusable so no test can forget to set one.
func validAttachmentStatusEvent() Event {
	evt := attachmentStatusEvent()
	evt.EventID = "6b2f9d43-1c85-4a70-9e26-3f8b5c1a7d94"
	return evt
}

// ── Canonicalization ────────────────────────────────────────────────────────

func TestCanonicalizeRemoteAttachmentStatusIsAccepted(t *testing.T) {
	evt := validAttachmentStatusEvent()
	// Upper-case identifiers on the wire must come back canonicalized, so the id
	// a client reconciles against matches the one the file API returns.
	evt.WorkspaceID = strings.ToUpper(asWorkspace)
	evt.TargetID = strings.ToUpper(asChannel)
	evt.Attachment.AttachmentID = strings.ToUpper(asAttachment)

	canonical, ok := canonicalizeRemoteEvent(evt)
	if !ok {
		t.Fatal("a valid attachment.status must canonicalize; without this no verdict ever reaches a client")
	}
	if canonical.Type != EventTypeAttachmentStatus {
		t.Fatalf("Type = %q, want %q", canonical.Type, EventTypeAttachmentStatus)
	}
	if canonical.WorkspaceID != asWorkspace || canonical.TargetID != asChannel {
		t.Fatalf("scope = %s/%s, want the canonicalized envelope",
			canonical.WorkspaceID, canonical.TargetID)
	}
	if canonical.Attachment == nil {
		t.Fatal("the payload must survive: it is what tells a client which row moved")
	}
	if canonical.Attachment.AttachmentID != asAttachment {
		t.Fatalf("AttachmentID = %q, want lowercase canonical form", canonical.Attachment.AttachmentID)
	}
	if canonical.Attachment.Status != "clean" {
		t.Fatalf("Status = %q", canonical.Attachment.Status)
	}
	// It routes by target, never by recipient — the field that would let a
	// producer address a person.
	if canonical.RecipientUserID != "" {
		t.Fatalf("RecipientUserID = %q; attachment.status must stay target-scoped",
			canonical.RecipientUserID)
	}
}

// The three functional states and nothing else. A fourth would be a value the
// client cannot draw, and a producer must not be able to invent one.
func TestCanonicalizeRemoteAttachmentStatusAcceptsOnlyTheThreeStates(t *testing.T) {
	for _, status := range []string{"pending_scan", "clean", "rejected"} {
		evt := validAttachmentStatusEvent()
		evt.Attachment.Status = status
		if _, ok := canonicalizeRemoteEvent(evt); !ok {
			t.Fatalf("the contract state %q was rejected", status)
		}
	}
	for _, status := range []string{
		"", "approved", "CLEAN", "pending_upload", "failed", "deleted", "clean ",
	} {
		evt := validAttachmentStatusEvent()
		evt.Attachment.Status = status
		if _, ok := canonicalizeRemoteEvent(evt); ok {
			t.Fatalf("a status outside the contract was accepted: %q", status)
		}
	}
}

func TestCanonicalizeRemoteAttachmentStatusRejectsMalformedEnvelopes(t *testing.T) {
	tests := map[string]func(*Event){
		"no payload":            func(e *Event) { e.Attachment = nil },
		"attachment not a uuid": func(e *Event) { e.Attachment.AttachmentID = "not-a-uuid" },
		"no attachment":         func(e *Event) { e.Attachment.AttachmentID = "" },
		"workspace not a uuid":  func(e *Event) { e.WorkspaceID = "nchat:chat:ws:broadcast:*" },
		"no workspace":          func(e *Event) { e.WorkspaceID = "" },
		"target not a uuid":     func(e *Event) { e.TargetID = "nope" },
		"no target":             func(e *Event) { e.TargetID = "" },
		"unknown target type":   func(e *Event) { e.TargetType = "elsewhere" },
		"no target type":        func(e *Event) { e.TargetType = "" },
		// A user target would put this on the recipient-addressed delivery path,
		// which is exactly the routing this event must never reach.
		"user target":         func(e *Event) { e.TargetType = TargetTypeUser },
		"bad event id":        func(e *Event) { e.EventID = "nope" },
		"no source instance":  func(e *Event) { e.SourceInstanceID = "" },
		"unknown schema":      func(e *Event) { e.SchemaVersion = 99 },
		"carries a recipient": func(e *Event) { e.RecipientUserID = asSubscriber },
		"carries a message":   func(e *Event) { e.MessageID = asChannel },
		"unbounded timestamp": func(e *Event) { e.Attachment.UpdatedAt = strings.Repeat("9", 4096) },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			evt := validAttachmentStatusEvent()
			mutate(&evt)
			if canonical, ok := canonicalizeRemoteEvent(evt); ok {
				t.Fatalf("a malformed attachment.status was accepted: %+v", canonical)
			}
		})
	}
}

// Whatever a producer attaches, only the attachment state may reach a client.
// The event describes a row that changed; it must never become a way to push a
// message body, a reaction or a call into somebody's socket.
func TestCanonicalizeRemoteAttachmentStatusStripsForeignPayloads(t *testing.T) {
	evt := validAttachmentStatusEvent()
	evt.Payload = &MessagePayload{ID: asChannel, BodyText: "sensitive message body", SenderID: asOutsider}
	evt.MessageUpdate = &MessageUpdatedPayload{MessageID: asChannel, Body: "sensitive edit"}
	evt.Reaction = &ReactionEventPayload{MessageID: asChannel, ActorUserID: asOutsider, Emoji: "👍"}
	evt.Members = &MembersAddedPayload{ActorUserID: asOutsider, AddedCount: 1, MemberCount: 2}
	evt.Pin = &PinEventPayload{MessageID: asChannel, ActorUserID: asOutsider, Pinned: true}

	canonical, ok := canonicalizeRemoteEvent(evt)
	if !ok {
		t.Fatal("a stray payload must be stripped, not reject the event")
	}
	if canonical.Payload != nil || canonical.MessageUpdate != nil ||
		canonical.Reaction != nil || canonical.Members != nil || canonical.Pin != nil {
		t.Fatalf("foreign payloads survived canonicalization: %+v", canonical)
	}
	data, err := json.Marshal(canonical)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, leak := range []string{"sensitive", asOutsider, "body_text", "display_name", "email"} {
		if strings.Contains(string(data), leak) {
			t.Fatalf("canonical attachment.status carried %q: %s", leak, data)
		}
	}
}

// The same guarantee asserted against the canonicalizer for this event type on
// its own, not against the pipeline that happens to call it.
//
// canonicalizeRemoteEvent clears the message payloads for every event after the
// per-type step, so the test above passes whether or not attachment.status
// strips them itself. That makes it a test of the caller's ordering rather than
// of this event's contract — and the contract is what a future refactor of the
// pipeline has to keep. Asserting it here is what stops a reordering from
// quietly turning a verdict into a message-body carrier.
func TestCanonicalizeAttachmentEventStripsForeignPayloadsItself(t *testing.T) {
	evt := validAttachmentStatusEvent()
	evt.Payload = &MessagePayload{ID: asChannel, BodyText: "sensitive message body", SenderID: asOutsider}
	evt.MessageUpdate = &MessageUpdatedPayload{MessageID: asChannel, Body: "sensitive edit"}
	evt.Reaction = &ReactionEventPayload{MessageID: asChannel, ActorUserID: asOutsider, Emoji: "👍"}
	evt.Members = &MembersAddedPayload{ActorUserID: asOutsider, AddedCount: 1, MemberCount: 2}
	evt.Pin = &PinEventPayload{MessageID: asChannel, ActorUserID: asOutsider, Pinned: true}
	evt.Call = &CallEventPayload{ID: asChannel, CallerID: asOutsider}

	canonical, ok := canonicalizeAttachmentEvent(evt)
	if !ok {
		t.Fatal("a stray payload must be stripped, not reject the event")
	}
	if canonical.Payload != nil {
		t.Fatalf("a message DTO survived attachment.status canonicalization: %+v", canonical.Payload)
	}
	if canonical.MessageUpdate != nil {
		t.Fatalf("a message update survived attachment.status canonicalization: %+v", canonical.MessageUpdate)
	}
	if canonical.Reaction != nil || canonical.Members != nil ||
		canonical.Pin != nil || canonical.Call != nil {
		t.Fatalf("foreign payloads survived: %+v", canonical)
	}
	// The one payload that must survive, since it is the whole event.
	if canonical.Attachment == nil || canonical.Attachment.AttachmentID != asAttachment ||
		canonical.Attachment.Status != "clean" {
		t.Fatalf("the attachment payload did not survive: %+v", canonical.Attachment)
	}
	// Routing is untouched: still target-scoped, still addressed to nobody.
	if canonical.TargetType != TargetTypeChannel || canonical.TargetID != asChannel ||
		canonical.WorkspaceID != asWorkspace || canonical.RecipientUserID != "" {
		t.Fatalf("canonicalization changed the routing: %+v", canonical)
	}
}

// End to end: a contaminated envelope goes through the real hub, and what lands
// in a subscriber's socket carries the verdict and nothing else.
func TestContaminatedAttachmentStatusDeliversOnlyTheVerdict(t *testing.T) {
	auth := &recordingAuthorizer{allowed: true}
	_, hubB, _ := twoHubsWithAuthorizer(t, auth)
	startHubLoop(t, hubB)
	subscriber := registerForTest(hubB, "c-subscriber", asSubscriber, asWorkspace)
	subscribeForTest(hubB, subscriber, TargetTypeChannel, asChannel)

	contaminated := validAttachmentStatusEvent()
	contaminated.Payload = &MessagePayload{
		ID: asChannel, BodyText: "sensitive message body", SenderID: asOutsider,
	}
	contaminated.MessageUpdate = &MessageUpdatedPayload{MessageID: asChannel, Body: "sensitive edit"}
	contaminated.Pin = &PinEventPayload{MessageID: asChannel, ActorUserID: asOutsider, Pinned: true}
	contaminated.Reaction = &ReactionEventPayload{
		MessageID: asChannel, ActorUserID: asOutsider, Emoji: "👍",
	}

	hubB.handleRemoteBusEvent(contaminated)
	if delivered := deliverRemoteBroadcast(t, hubB); delivered != 1 {
		t.Fatalf("dispatched %d broadcasts, want 1", delivered)
	}

	messages := drain(subscriber)
	if len(messages) != 1 {
		t.Fatalf("subscriber got %d message(s), want 1", len(messages))
	}
	var evt Event
	if err := json.Unmarshal(messages[0], &evt); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if evt.Payload != nil || evt.MessageUpdate != nil || evt.Pin != nil || evt.Reaction != nil {
		t.Fatalf("a contaminated payload was delivered: %s", messages[0])
	}
	if evt.Attachment == nil || evt.Attachment.AttachmentID != asAttachment ||
		evt.Attachment.Status != "clean" {
		t.Fatalf("the verdict did not survive delivery: %s", messages[0])
	}
	for _, leak := range []string{"sensitive", asOutsider, "body_text", "message_update"} {
		if strings.Contains(string(messages[0]), leak) {
			t.Fatalf("delivered attachment.status carried %q: %s", leak, messages[0])
		}
	}
}

// The payload is minimal by contract. A filename, a size or a scanner signature
// on a broadcast would be this service describing somebody's file to everyone
// subscribed to the room.
func TestRemoteAttachmentStatusCarriesNothingAboutTheFile(t *testing.T) {
	canonical, ok := canonicalizeRemoteEvent(validAttachmentStatusEvent())
	if !ok {
		t.Fatal("expected the event to canonicalize")
	}
	data, err := json.Marshal(canonical)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, leak := range []string{
		"filename", "content_type", "size", "signature", "uploader",
		"storage", "wrapped", "object_key",
	} {
		if strings.Contains(string(data), leak) {
			t.Fatalf("attachment.status carried %q: %s", leak, data)
		}
	}
}

// ── Delivery ────────────────────────────────────────────────────────────────

// The headline case: file-service persists the verdict, and a subscriber of the
// attachment's channel is told.
func TestAttachmentStatusReachesSubscribersOfItsTarget(t *testing.T) {
	auth := &recordingAuthorizer{allowed: true}
	_, hubB, _ := twoHubsWithAuthorizer(t, auth)
	startHubLoop(t, hubB)
	subscriber := registerForTest(hubB, "c-subscriber", asSubscriber, asWorkspace)
	subscribeForTest(hubB, subscriber, TargetTypeChannel, asChannel)

	hubB.handleRemoteBusEvent(validAttachmentStatusEvent())
	if delivered := deliverRemoteBroadcast(t, hubB); delivered != 1 {
		t.Fatalf("dispatched %d broadcasts, want 1", delivered)
	}

	messages := drain(subscriber)
	if len(messages) != 1 {
		t.Fatalf("subscriber got %d message(s), want 1", len(messages))
	}
	var evt Event
	if err := json.Unmarshal(messages[0], &evt); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if evt.Type != EventTypeAttachmentStatus || evt.Attachment == nil ||
		evt.Attachment.AttachmentID != asAttachment || evt.Attachment.Status != "clean" {
		t.Fatalf("wrong event delivered: %+v", evt)
	}
}

// The event must not become a side channel for discovering rooms. Someone
// subscribed to a different channel in the same workspace learns nothing.
func TestAttachmentStatusDoesNotCrossTargets(t *testing.T) {
	auth := &recordingAuthorizer{allowed: true}
	_, hubB, _ := twoHubsWithAuthorizer(t, auth)
	startHubLoop(t, hubB)
	elsewhere := registerForTest(hubB, "c-elsewhere", asOutsider, asWorkspace)
	subscribeForTest(hubB, elsewhere, TargetTypeChannel, asOtherRoom)

	hubB.handleRemoteBusEvent(validAttachmentStatusEvent())
	deliverRemoteBroadcast(t, hubB)

	if got := len(drain(elsewhere)); got != 0 {
		t.Fatalf("a subscriber of another channel got %d message(s), want 0", got)
	}
}

// A subscription is an authorization decision, and it is re-checked at fan-out.
// Someone whose access was revoked between subscribing and the verdict landing
// must not be told the file exists.
func TestAttachmentStatusIsWithheldWhenAccessIsDenied(t *testing.T) {
	auth := &recordingAuthorizer{allowed: false}
	_, hubB, _ := twoHubsWithAuthorizer(t, auth)
	startHubLoop(t, hubB)
	subscriber := registerForTest(hubB, "c-subscriber", asSubscriber, asWorkspace)
	subscribeForTest(hubB, subscriber, TargetTypeChannel, asChannel)

	hubB.handleRemoteBusEvent(validAttachmentStatusEvent())
	deliverRemoteBroadcast(t, hubB)

	if got := len(drain(subscriber)); got != 0 {
		t.Fatalf("a denied subscriber got %d message(s), want 0", got)
	}
}

// A malformed envelope is dropped at the boundary and never enters the
// broadcast queue at all.
func TestMalformedAttachmentStatusNeverReachesTheBroadcastQueue(t *testing.T) {
	auth := &recordingAuthorizer{allowed: true}
	_, hubB, _ := twoHubsWithAuthorizer(t, auth)
	startHubLoop(t, hubB)
	subscriber := registerForTest(hubB, "c-subscriber", asSubscriber, asWorkspace)
	subscribeForTest(hubB, subscriber, TargetTypeChannel, asChannel)

	invalid := validAttachmentStatusEvent()
	invalid.Attachment.Status = "approved"
	hubB.handleRemoteBusEvent(invalid)

	if delivered := deliverRemoteBroadcast(t, hubB); delivered != 0 {
		t.Fatalf("dispatched %d broadcasts for a malformed event, want 0", delivered)
	}
	if got := len(drain(subscriber)); got != 0 {
		t.Fatalf("subscriber got %d message(s) from a malformed event, want 0", got)
	}
}
