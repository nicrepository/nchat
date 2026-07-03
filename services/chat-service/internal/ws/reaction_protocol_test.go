package ws

import "testing"

func TestDecodeClientMessage_RejectsUnknownFields(t *testing.T) {
	_, err := decodeClientMessage([]byte(`{"type":"reaction.toggle","message_id":"m1","emoji":"👍","user_id":"attacker"}`))
	if err == nil {
		t.Fatal("expected unknown user_id field to be rejected")
	}
}

func TestDecodeClientMessage_AcceptsStrictReactionToggle(t *testing.T) {
	msg, err := decodeClientMessage([]byte(`{"type":"reaction.toggle","message_id":"m1","emoji":"👍"}`))
	if err != nil {
		t.Fatalf("decode reaction: %v", err)
	}
	if msg.Type != ClientMessageTypeReactionToggle || msg.MessageID != "m1" || msg.Emoji != "👍" {
		t.Fatalf("unexpected message: %+v", msg)
	}
}
