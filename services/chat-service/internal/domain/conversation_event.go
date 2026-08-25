package domain

import "errors"

// Server-generated conversation events (issue #527).
//
// A rename and a departure are facts about a conversation that its members must
// be able to see in the history, in order, alongside the messages around them.
// They are persisted as chat.messages rows with kind='system' — the kind the
// schema has always allowed and nothing had ever written — plus a structured
// event and payload.
//
// Structured rather than a pre-formatted sentence, for two reasons. A persisted
// sentence freezes one language into the database, so translating the product
// later would need a data migration; and it invites a writer to put a display
// name in the row, which is exactly the thing a reader must not trust. The
// payload carries facts only. The actor is chat.messages.sender_id and is
// resolved by the same authorized projection every other message's sender goes
// through, so no caller can name themselves in one of these.
type ConversationEventType string

const (
	// ConversationEventRenamed records that a channel or group changed its
	// display name. Payload: old_name, new_name.
	ConversationEventRenamed ConversationEventType = "conversation_renamed"
	// ConversationEventMemberLeft records that the actor removed their own
	// membership. Payload is empty: who left is the actor, and where is the
	// message's own target.
	ConversationEventMemberLeft ConversationEventType = "conversation_member_left"
)

// ErrUnknownConversationEvent rejects an event type this build does not
// produce, so a row written by a newer or hostile writer is never rendered as
// something a client might mistake for a fact this version vouches for.
var ErrUnknownConversationEvent = errors.New("unknown conversation event")

// ValidConversationEventType reports whether value is one this build produces.
//
// An allowlist rather than a length check: the set is closed, and an
// unrecognised event must fail closed at every boundary that reads one.
func ValidConversationEventType(value ConversationEventType) bool {
	switch value {
	case ConversationEventRenamed, ConversationEventMemberLeft:
		return true
	default:
		return false
	}
}

// ConversationEventPayload is the whole structured content of a system message.
//
// Both name fields are omitted for events that do not carry them, so a
// "member left" row stores `{}` rather than two empty strings pretending to be
// a rename. There is deliberately no actor name, no avatar, no role and no
// free text: everything a renderer needs beyond these is either the message's
// own columns or something it must resolve through an authorized read.
type ConversationEventPayload struct {
	OldName string `json:"old_name,omitempty"`
	NewName string `json:"new_name,omitempty"`
}
