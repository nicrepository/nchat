package events

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/nicrepository/nchat/services/file-service/internal/domain"
	"github.com/nicrepository/nchat/services/file-service/internal/service"
)

const (
	testWorkspaceID  = "44444444-4444-4444-8444-444444444444"
	testChannelID    = "33333333-3333-4333-8333-333333333333"
	testAttachmentID = "88888888-8888-4888-8888-888888888888"
)

func validChange() service.AttachmentStatusChange {
	return service.AttachmentStatusChange{
		AttachmentID:    testAttachmentID,
		WorkspaceID:     testWorkspaceID,
		DestinationKind: domain.DestinationKindChannel,
		DestinationID:   testChannelID,
		Status:          domain.StatusClean,
		UpdatedAt:       time.Now().UTC(),
	}
}

// The channel name is built from the workspace id, so it is the one value that
// must never reach the broker unparsed: a glob character in it would publish
// into a pattern instead of a workspace.
func TestPublisherRefusesIdentifiersItCannotCanonicalize(t *testing.T) {
	for name, mutate := range map[string]func(*service.AttachmentStatusChange){
		"workspace is not a UUID": func(c *service.AttachmentStatusChange) {
			c.WorkspaceID = "nchat:chat:ws:broadcast:*"
		},
		"destination is not a UUID": func(c *service.AttachmentStatusChange) {
			c.DestinationID = "../../etc"
		},
		"attachment is not a UUID": func(c *service.AttachmentStatusChange) {
			c.AttachmentID = "not-an-id"
		},
		"unknown destination kind": func(c *service.AttachmentStatusChange) {
			c.DestinationKind = "everyone"
		},
		"unknown status": func(c *service.AttachmentStatusChange) {
			c.Status = "approved-by-the-client"
		},
	} {
		t.Run(name, func(t *testing.T) {
			change := validChange()
			mutate(&change)
			publisher := &Publisher{instanceID: "file-service-0"}
			if _, _, err := publisher.encode(change); err == nil {
				t.Fatal("encode accepted an identifier it cannot canonicalize")
			}
		})
	}
}

// A publisher with no connection must fail rather than silently drop, so the
// caller counts a failed announcement instead of believing one happened.
func TestPublisherFailsWithoutAConnection(t *testing.T) {
	var publisher *Publisher
	if err := publisher.PublishAttachmentStatus(t.Context(), validChange()); err == nil {
		t.Fatal("a nil publisher reported success")
	}
	if err := (&Publisher{}).PublishAttachmentStatus(t.Context(), validChange()); err == nil {
		t.Fatal("an unconnected publisher reported success")
	}
}

// The instance id ends up in an envelope the consumer validates. A value it
// would reject is refused at construction rather than producing events nothing
// ever delivers.
func TestNewPublisherValidatesTheInstanceID(t *testing.T) {
	for name, id := range map[string]string{
		"empty":     "",
		"a glob":    "file-*",
		"a newline": "file\nsvc",
		"a colon":   "file:svc",
		"too long":  strings.Repeat("a", 65),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewPublisher("valkey://localhost:6379", id); err == nil {
				t.Fatalf("NewPublisher accepted instance id %q", id)
			}
		})
	}
}

func TestNewPublisherRequiresAURL(t *testing.T) {
	if _, err := NewPublisher("", "file-service-0"); err == nil {
		t.Fatal("NewPublisher accepted an empty URL")
	}
	// The URL carries credentials, so neither it nor a parser message may travel
	// back with the error.
	_, err := NewPublisher("valkey://user:hunter2@localhost:6379/notanumber", "file-service-0")
	if err != nil && strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("the error echoes the URL's credentials: %v", err)
	}
}

func TestCloseIsSafeWithoutAConnection(t *testing.T) {
	var publisher *Publisher
	publisher.Close()
	(&Publisher{}).Close()
}

func TestTargetTypeMapsOnlyTheTwoDestinationKinds(t *testing.T) {
	channel, err := targetTypeFor(domain.DestinationKindChannel)
	if err != nil || channel != "channel" {
		t.Fatalf("channel -> %q, %v", channel, err)
	}
	dm, err := targetTypeFor(domain.DestinationKindDM)
	if err != nil || dm != "dm" {
		t.Fatalf("dm -> %q, %v", dm, err)
	}
	// "user" is deliberately unreachable: it is the bus's recipient-addressed
	// routing, and this producer must not be able to aim an event at a person.
	if _, err := targetTypeFor("user"); err == nil {
		t.Fatal("an unknown destination kind was mapped to a target type")
	}
}

// The envelope is the contract with chat-service's canonicalizer, so the exact
// bytes are asserted rather than the fields that produced them: a rename on
// either side has to be a deliberate, matching change.
func TestEncodeProducesTheEnvelopeTheConsumerAccepts(t *testing.T) {
	publisher := &Publisher{instanceID: "file-service-0"}
	change := validChange()
	change.UpdatedAt = time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)

	channel, payload, err := publisher.encode(change)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	// The channel is workspace-scoped and built from the parsed UUID, which is
	// what chat-service's PSUBSCRIBE pattern matches.
	if channel != "nchat:chat:ws:broadcast:"+testWorkspaceID {
		t.Fatalf("channel = %q", channel)
	}

	var envelope map[string]any
	if err := json.Unmarshal(payload, &envelope); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if envelope["type"] != "attachment.status" {
		t.Fatalf("type = %v", envelope["type"])
	}
	if envelope["schema_version"] != float64(1) {
		t.Fatalf("schema_version = %v", envelope["schema_version"])
	}
	if envelope["target_type"] != "channel" || envelope["target_id"] != testChannelID {
		t.Fatalf("target = %v/%v", envelope["target_type"], envelope["target_id"])
	}
	if envelope["source_instance_id"] != "file-service-0" {
		t.Fatalf("source_instance_id = %v", envelope["source_instance_id"])
	}
	// Required by the consumer's canonicalizer, and a fresh one per event.
	if _, err := uuid.Parse(envelope["event_id"].(string)); err != nil {
		t.Fatalf("event_id is not a UUID: %v", envelope["event_id"])
	}

	attachment, ok := envelope["attachment"].(map[string]any)
	if !ok {
		t.Fatalf("no attachment payload: %v", envelope["attachment"])
	}
	if attachment["attachment_id"] != testAttachmentID || attachment["status"] != "clean" {
		t.Fatalf("attachment = %v", attachment)
	}
	if attachment["updated_at"] != "2026-08-07T12:00:00Z" {
		t.Fatalf("updated_at = %v", attachment["updated_at"])
	}
	// Exactly three fields: anything more would be this service describing a
	// user's file to every subscriber of the room.
	if len(attachment) != 3 {
		t.Fatalf("attachment payload has %d fields, want 3: %v", len(attachment), attachment)
	}
}

// Identifiers are canonicalized, not echoed, so the id a client reconciles
// against matches the one the file API returns.
func TestEncodeCanonicalizesIdentifiers(t *testing.T) {
	publisher := &Publisher{instanceID: "file-service-0"}
	change := validChange()
	change.WorkspaceID = strings.ToUpper(testWorkspaceID)
	change.DestinationID = strings.ToUpper(testChannelID)
	change.AttachmentID = strings.ToUpper(testAttachmentID)

	channel, payload, err := publisher.encode(change)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if channel != "nchat:chat:ws:broadcast:"+testWorkspaceID {
		t.Fatalf("channel = %q, want the lowercase canonical form", channel)
	}
	if !strings.Contains(string(payload), testAttachmentID) {
		t.Fatalf("payload does not carry the canonical attachment id: %s", payload)
	}
}

// The event must never become a description of the file. There is nothing in the
// change struct that could carry a filename, and this asserts the envelope keeps
// it that way.
func TestEncodeCarriesNothingAboutTheFile(t *testing.T) {
	publisher := &Publisher{instanceID: "file-service-0"}
	_, payload, err := publisher.encode(validChange())
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	for _, leak := range []string{
		"filename", "content_type", "size", "signature", "uploader",
		"storage", "wrapped", "object_key", "recipient",
	} {
		if strings.Contains(string(payload), leak) {
			t.Fatalf("envelope carried %q: %s", leak, payload)
		}
	}
}

// A DM verdict addresses the conversation, so subscribers of the DM are told and
// nobody else is.
func TestEncodeAddressesADMConversation(t *testing.T) {
	publisher := &Publisher{instanceID: "file-service-0"}
	change := validChange()
	change.DestinationKind = domain.DestinationKindDM

	_, payload, err := publisher.encode(change)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if !strings.Contains(string(payload), `"target_type":"dm"`) {
		t.Fatalf("a DM verdict was not addressed to the conversation: %s", payload)
	}
}

// All three functional states travel; none of them is special-cased away.
func TestEncodeCarriesEachFunctionalState(t *testing.T) {
	publisher := &Publisher{instanceID: "file-service-0"}
	for _, status := range []domain.Status{
		domain.StatusPendingScan, domain.StatusClean, domain.StatusRejected,
	} {
		change := validChange()
		change.Status = status
		_, payload, err := publisher.encode(change)
		if err != nil {
			t.Fatalf("encode(%q): %v", status, err)
		}
		if !strings.Contains(string(payload), `"status":"`+string(status)+`"`) {
			t.Fatalf("status %q did not reach the envelope: %s", status, payload)
		}
	}
}
