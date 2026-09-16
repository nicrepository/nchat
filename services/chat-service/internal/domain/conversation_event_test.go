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
		domain.ConversationEventCreated,
		domain.ConversationEventArchived,
		domain.ConversationEventMemberAdded,
		domain.ConversationEventMemberRemoved,
		domain.ConversationEventCallStarted,
		domain.ConversationEventCallEnded,
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
		"meeting_scheduled",
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

// member.added/member.removed carry target_users and nothing from an
// unrelated event type — no old_name/new_name, no call fields.
func TestConversationEventPayload_TargetUsersOmitsUnrelatedFields(t *testing.T) {
	payload := domain.ConversationEventPayload{
		TargetUsers: []domain.ConversationEventUser{
			{UserID: "11111111-1111-4111-8111-111111111111", DisplayName: "Juliane Lino"},
			{UserID: "22222222-2222-4222-8222-222222222222"},
		},
	}
	out, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"target_users":[{"user_id":"11111111-1111-4111-8111-111111111111","display_name":"Juliane Lino"},{"user_id":"22222222-2222-4222-8222-222222222222"}]}`
	if string(out) != want {
		t.Fatalf("payload = %s, want %s", out, want)
	}
}

// call.started/call.ended carry call fields and nothing from an unrelated
// event type. call_duration_seconds is omitted for call.started, which has
// not ended yet.
func TestConversationEventPayload_CallFieldsOmitUnrelatedFields(t *testing.T) {
	started, err := json.Marshal(domain.ConversationEventPayload{
		CallID: "33333333-3333-4333-8333-333333333333", CallType: "video",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	wantStarted := `{"call_id":"33333333-3333-4333-8333-333333333333","call_type":"video"}`
	if string(started) != wantStarted {
		t.Fatalf("payload = %s, want %s", started, wantStarted)
	}

	ended, err := json.Marshal(domain.ConversationEventPayload{
		CallID: "33333333-3333-4333-8333-333333333333", CallType: "video", CallDurationSeconds: 1083,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	wantEnded := `{"call_id":"33333333-3333-4333-8333-333333333333","call_type":"video","call_duration_seconds":1083}`
	if string(ended) != wantEnded {
		t.Fatalf("payload = %s, want %s", ended, wantEnded)
	}
}
