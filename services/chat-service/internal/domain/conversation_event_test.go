package domain_test

import (
	"encoding/json"
	"testing"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

// The system-event vocabulary is a closed set (issue #527).
//
// It is an allowlist rather than a length or prefix check, and that matters at
// every boundary that reads one: a row written by a newer build, or by anything
// hostile, must fail closed rather than be rendered as a fact this version
// vouches for.
func TestValidConversationEventType(t *testing.T) {
	for _, valid := range []domain.ConversationEventType{
		domain.ConversationEventRenamed,
		domain.ConversationEventMemberLeft,
	} {
		if !domain.ValidConversationEventType(valid) {
			t.Fatalf("%q must be a produced event type", valid)
		}
	}

	for _, invalid := range []domain.ConversationEventType{
		"",
		"conversation_deleted",
		"CONVERSATION_RENAMED",
		"conversation_renamed ",
		"member_left",
	} {
		if domain.ValidConversationEventType(invalid) {
			t.Fatalf("%q must not be accepted: the set is closed", invalid)
		}
	}
}

// A "member left" row stores `{}` rather than two empty strings pretending to be
// a rename, and neither field is ever a display name the client could trust.
func TestConversationEventPayload_OmitsWhatAnEventDoesNotCarry(t *testing.T) {
	empty, err := json.Marshal(domain.ConversationEventPayload{})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(empty) != "{}" {
		t.Fatalf("payload = %s, want an empty object", empty)
	}

	renamed, err := json.Marshal(domain.ConversationEventPayload{OldName: "Equipe", NewName: "Piloto"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(renamed) != `{"old_name":"Equipe","new_name":"Piloto"}` {
		t.Fatalf("payload = %s, want the old and new names only", renamed)
	}
}
