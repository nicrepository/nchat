package storage

import (
	"testing"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

// Reading a system message back (issue #527).
//
// The decode is where a row written by a newer — or hostile — producer meets
// this build. It fails closed: an event type this version does not know leaves
// the message with no event at all, so the client renders nothing rather than
// something it cannot vouch for.

func TestDecodeConversationEvent(t *testing.T) {
	t.Run("an ordinary message carries no event", func(t *testing.T) {
		message := domain.Message{Kind: domain.MessageKindUser}
		if err := decodeConversationEvent(&message, []byte(`{"old_name":"a"}`)); err != nil {
			t.Fatalf("decodeConversationEvent: %v", err)
		}
		// The payload is ignored outright: without an event type there is nothing
		// it could belong to.
		if message.EventPayload != (domain.ConversationEventPayload{}) {
			t.Fatalf("payload = %+v, want it untouched", message.EventPayload)
		}
	})

	t.Run("a known event is decoded", func(t *testing.T) {
		message := domain.Message{
			Kind:      domain.MessageKindSystem,
			EventType: string(domain.ConversationEventRenamed),
		}
		if err := decodeConversationEvent(&message, []byte(`{"old_name":"Equipe","new_name":"Piloto"}`)); err != nil {
			t.Fatalf("decodeConversationEvent: %v", err)
		}
		if message.EventPayload.OldName != "Equipe" || message.EventPayload.NewName != "Piloto" {
			t.Fatalf("payload = %+v, want the old and new names", message.EventPayload)
		}
	})

	// Fail closed: the row keeps existing, it simply stops claiming to be an
	// event this build understands.
	t.Run("an unknown event is dropped rather than surfaced", func(t *testing.T) {
		message := domain.Message{
			Kind:      domain.MessageKindSystem,
			EventType: "conversation_nuked",
		}
		if err := decodeConversationEvent(&message, []byte(`{"old_name":"Equipe"}`)); err != nil {
			t.Fatalf("decodeConversationEvent: %v", err)
		}
		if message.EventType != "" {
			t.Fatalf("event type = %q, want it cleared", message.EventType)
		}
		if message.EventPayload != (domain.ConversationEventPayload{}) {
			t.Fatalf("payload = %+v, want nothing decoded for an unknown event", message.EventPayload)
		}
	})

	// A member-left row stores no payload at all, and reading one back must not
	// be an error.
	t.Run("a known event with no payload", func(t *testing.T) {
		message := domain.Message{
			Kind:      domain.MessageKindSystem,
			EventType: string(domain.ConversationEventMemberLeft),
		}
		if err := decodeConversationEvent(&message, nil); err != nil {
			t.Fatalf("decodeConversationEvent: %v", err)
		}
		if message.EventType != string(domain.ConversationEventMemberLeft) {
			t.Fatalf("event type = %q, want it kept", message.EventType)
		}
	})

	t.Run("a malformed payload is an error", func(t *testing.T) {
		message := domain.Message{
			Kind:      domain.MessageKindSystem,
			EventType: string(domain.ConversationEventRenamed),
		}
		if err := decodeConversationEvent(&message, []byte(`{"old_name":`)); err == nil {
			t.Fatal("expected a malformed payload to fail rather than be half-applied")
		}
	})
}

// muteSQLFor is the whole target-kind dispatch: two known kinds and an outright
// refusal for anything else, so an unknown kind never reaches the database.
func TestMuteSQLFor(t *testing.T) {
	channel, err := muteSQLFor(NotificationPrefTargetChannel)
	if err != nil || channel != muteChannelSQL {
		t.Fatalf("channel = %q, err = %v", channel, err)
	}
	dm, err := muteSQLFor(NotificationPrefTargetDM)
	if err != nil || dm != muteDMSQL {
		t.Fatalf("dm = %q, err = %v", dm, err)
	}
	if _, err := muteSQLFor("workspace"); err == nil {
		t.Fatal("an unknown target kind must be refused")
	}
}
