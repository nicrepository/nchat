package domain

import "errors"

// Server-generated conversation events (issue #527, extended by issue #685).
//
// A rename, a departure, a membership change, a lifecycle change or a call
// starting/ending are all facts about a conversation that its members must
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
	// ConversationEventCreated records that a channel or group conversation
	// was created. Never emitted for a 1:1 direct conversation — there is no
	// membership event to narrate for a conversation with no roster. Payload
	// is empty: the actor is the creator, and where is the message's own
	// target.
	ConversationEventCreated ConversationEventType = "conversation_created"
	// ConversationEventArchived records that a channel was archived. Payload
	// is empty. Groups have no archive operation today, so this is
	// channel-only.
	ConversationEventArchived ConversationEventType = "conversation_archived"
	// ConversationEventMemberAdded records that the actor added one or more
	// members to a channel or group in a single batch operation — one event
	// per batch, never one per member. Payload: target_users.
	ConversationEventMemberAdded ConversationEventType = "conversation_member_added"
	// ConversationEventMemberRemoved records that the actor removed another
	// member's membership (an administrative removal, as opposed to
	// ConversationEventMemberLeft's voluntary departure). Payload:
	// target_users, always exactly one.
	ConversationEventMemberRemoved ConversationEventType = "conversation_member_removed"
	// ConversationEventCallStarted records that a resource call (attached to
	// a channel or a group, never a 1:1 direct call) started. Payload:
	// call_id, call_type.
	ConversationEventCallStarted ConversationEventType = "call_started"
	// ConversationEventCallEnded records that a resource call ended because
	// the caller who started it ended it — not a decline, a cancel or a
	// timeout, none of which describe a call anyone actually joined. Payload:
	// call_id, call_type, call_duration_seconds.
	ConversationEventCallEnded ConversationEventType = "call_ended"
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
	case ConversationEventRenamed, ConversationEventMemberLeft, ConversationEventCreated,
		ConversationEventArchived, ConversationEventMemberAdded, ConversationEventMemberRemoved,
		ConversationEventCallStarted, ConversationEventCallEnded:
		return true
	default:
		return false
	}
}

// ConversationEventUser is the minimal portrait of a member.added/removed
// target: an id (the authority — "is this me?") and a display name resolved
// once, at write time, by the same authorized projection every sender name
// already goes through.
//
// This is a deliberate, narrow exception to the "facts only, no names" rule
// the rest of this file follows: unlike the actor (always
// chat.messages.sender_id), a *target* has no other column to be resolved
// from later, and a renderer must never fetch a profile by id on its own
// (that would be exactly the N+1 this design avoids everywhere else). The
// name here is informational only — never used for authorization, and never
// re-derived from it.
type ConversationEventUser struct {
	UserID      string `json:"user_id"`
	DisplayName string `json:"display_name,omitempty"`
}

// ConversationEventPayload is the whole structured content of a system message.
//
// Every field is omitted when the event does not carry it, so a "member left"
// row stores `{}` rather than fields from an unrelated event type pretending
// to apply. There is deliberately no actor name, no avatar, no role and no
// free text: everything a renderer needs beyond these is either the message's
// own columns, TargetUsers (see its own doc comment for why that one field
// carries a name), or something it must resolve through an authorized read.
type ConversationEventPayload struct {
	// conversation_renamed
	OldName string `json:"old_name,omitempty"`
	NewName string `json:"new_name,omitempty"`

	// conversation_member_added / conversation_member_removed. Always at
	// least one entry; conversation_member_added may carry several for one
	// batch operation.
	TargetUsers []ConversationEventUser `json:"target_users,omitempty"`

	// call_started / call_ended
	CallID              string `json:"call_id,omitempty"`
	CallType            string `json:"call_type,omitempty"`
	CallDurationSeconds int64  `json:"call_duration_seconds,omitempty"`
}
