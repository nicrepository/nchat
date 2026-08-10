package ws

import (
	"time"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

// TargetType identifies the kind of chat target a subscription refers to.
type TargetType string

const (
	TargetTypeChannel TargetType = "channel"
	TargetTypeDM      TargetType = "dm"
	TargetTypeUser    TargetType = "user"
)

// EventType identifies the kind of server-sent event envelope.
type EventType string

const (
	// EventTypeMessageCreated is emitted after a message has been persisted.
	EventTypeMessageCreated  EventType = "message.created"
	EventTypeMessageUpdated  EventType = "message.updated"
	EventTypeReactionUpdated EventType = "reaction.updated"
	// EventTypePinUpdated is emitted after a message is pinned or unpinned in a
	// channel or DM (RF-05). Delivered to readable target subscribers only.
	EventTypePinUpdated EventType = "pin.updated"
	// EventTypeMembersAdded is emitted after participants are added to a channel
	// or group conversation (issue #398). Delivered to readable target
	// subscribers only, and carries no identities — see MembersAddedPayload.
	EventTypeMembersAdded EventType = "members.added"
	// EventTypeConversationAvailable tells one user that a conversation they can
	// now see exists (issue #398).
	//
	// It is the only user-scoped event in this protocol, and it has to be: a
	// person who has just been added to a private channel or a group is by
	// definition not subscribed to it, so a room broadcast reaches everyone
	// except the one who needs it. It is delivered straight to that user's
	// sessions instead.
	//
	// It is a pure invalidation signal — "refetch your sidebar" — and grants
	// nothing on its own: the sidebar API re-derives membership server-side, so
	// a client that receives this for a conversation it cannot read still sees
	// nothing.
	EventTypeConversationAvailable EventType = "conversation.available"

	// EventTypeAttachmentStatus is emitted after an attachment's antimalware
	// verdict has been persisted (RF-22).
	//
	// It is the one event in this protocol chat-service never publishes. Its
	// producer is file-service, which owns the scan and the row, and it reaches
	// this hub the same way any other instance's event does: over the broadcast
	// bus, as an untrusted payload that is canonicalised and then delivered to
	// the target's subscribers, each re-authorised at fan-out.
	//
	// That routing is the whole authorization story. The event names a channel or
	// a conversation, never a user, so it can only reach people who could already
	// read that destination — it cannot be aimed, cannot widen a subscription and
	// cannot reveal that a private target exists.
	//
	// Like pin.updated it is route-plus-flag: it carries the attachment's id, its
	// new status and when it changed, and nothing about the file itself. The
	// client reconciles by refetching the attachment list, which the file service
	// re-authorises.
	EventTypeAttachmentStatus EventType = "attachment.status"

	// Call lifecycle (RF-23). User-scoped like conversation.available in that
	// they name a peer, but with their own payload and their own validation.
	EventTypeCallRinging   EventType = "call.ringing"
	EventTypeCallAccepted  EventType = "call.accepted"
	EventTypeCallDeclined  EventType = "call.declined"
	EventTypeCallCancelled EventType = "call.cancelled"
	EventTypeCallTimedOut  EventType = "call.timed_out"
	EventTypeCallEnded     EventType = "call.ended"
)

// CurrentEventSchemaVersion is the version of the outbound WebSocket event
// envelope. Missing schema_version is treated as v1 for rolling deploys.
const CurrentEventSchemaVersion = 1

// ClientMessageType is the type of inbound control message a client may send.
// Clients are limited to control actions; they may not send chat messages over WS.
type ClientMessageType string

const (
	ClientMessageTypeSubscribe      ClientMessageType = "subscribe"
	ClientMessageTypeUnsubscribe    ClientMessageType = "unsubscribe"
	ClientMessageTypePing           ClientMessageType = "ping"
	ClientMessageTypeReactionToggle ClientMessageType = "reaction.toggle"
	ClientMessageTypeCallStart      ClientMessageType = "call.start"
	ClientMessageTypeCallAccept     ClientMessageType = "call.accept"
	ClientMessageTypeCallDecline    ClientMessageType = "call.decline"
	ClientMessageTypeCallCancel     ClientMessageType = "call.cancel"
	ClientMessageTypeCallEnd        ClientMessageType = "call.end"
	ClientMessageTypeCallSync       ClientMessageType = "call.sync"
)

// MessagePayload carries the full message DTO for message.created events.
// It mirrors the non-sensitive render fields returned by the list endpoints so
// that the browser can render the message immediately without an additional GET.
//
// Security notes:
//   - BodyText must NEVER be included in server logs (user-generated content).
//   - All fields are server-populated from the authoritative DB record.
//   - No tokens, secrets, credentials, or sender email may appear in any field.
type MessagePayload struct {
	ID                string        `json:"id"`
	WorkspaceID       string        `json:"workspace_id"`
	ChannelID         string        `json:"channel_id,omitempty"`
	DMConversationID  string        `json:"dm_conversation_id,omitempty"`
	SenderID          string        `json:"sender_id"`
	SenderDisplayName string        `json:"sender_display_name"`
	Kind              string        `json:"kind"`
	BodyText          string        `json:"body_text"`
	BodyFormat        string        `json:"body_format"`
	Status            string        `json:"status"`
	IsRemoved         bool          `json:"is_removed"`
	CreatedAt         time.Time     `json:"created_at"`
	UpdatedAt         time.Time     `json:"updated_at"`
	EditedAt          *time.Time    `json:"edited_at,omitempty"`
	DeletedAt         *time.Time    `json:"deleted_at,omitempty"`
	Quoted            *QuotePayload `json:"quoted,omitempty"`
	IsForwarded       bool          `json:"is_forwarded"`
	// Attachments lets a subscriber render a message that carries a file without
	// a follow-up GET, exactly like BodyText and Quoted (RF-32). It is the same
	// metadata the list endpoints publish and grants nothing: content and preview
	// delivery are re-authorised by file-service per request. Omitted when the
	// message has none, and — like the rest of this payload — dropped from the
	// event relayed over the bus, where a remote instance re-reads it instead.
	Attachments []MessageAttachmentPayload `json:"attachments,omitempty"`
	// HasReference forces route-only delivery so each subscriber resolves RF-09
	// through an authenticated GET. It is process-local and never serialized.
	HasReference bool `json:"-"`
}

// MessageUpdatedPayload carries authoritative edit or deletion fields.
type MessageUpdatedPayload struct {
	MessageID  string     `json:"message_id"`
	ChannelID  string     `json:"channel_id,omitempty"`
	DMID       string     `json:"dm_id,omitempty"`
	Body       string     `json:"body"`
	BodyFormat string     `json:"body_format"`
	EditedAt   time.Time  `json:"edited_at"`
	EditCount  int        `json:"edit_count"`
	IsEdited   bool       `json:"is_edited"`
	Status     string     `json:"status"`
	IsRemoved  bool       `json:"is_removed"`
	DeletedAt  *time.Time `json:"deleted_at,omitempty"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

// MessageAttachmentPayload mirrors the attachment metadata the HTTP message
// representation carries. Nothing internal to file-service appears here: no
// storage key, no key material, no scanner detail.
type MessageAttachmentPayload struct {
	ID            string `json:"id"`
	Filename      string `json:"filename"`
	ContentType   string `json:"content_type"`
	Size          int64  `json:"size"`
	Status        string `json:"status"`
	PreviewStatus string `json:"preview_status"`
}

type QuotePayload struct {
	ID         string     `json:"id"`
	AuthorID   string     `json:"author_id"`
	Body       string     `json:"body,omitempty"`
	BodyFormat string     `json:"body_format"`
	IsRemoved  bool       `json:"is_removed,omitempty"`
	DeletedAt  *time.Time `json:"deleted_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

type ReactionPayload struct {
	Emoji string `json:"emoji"`
	Count int    `json:"count"`
}

// PinEventPayload carries a pin change (RF-05). It is route-plus-flag
// only: clients refetch the authoritative pin list on receipt. No message body
// travels on this event.
// MembersAddedPayload signals that a channel or group gained participants
// (issue #398).
//
// It names nobody. The added users' IDs, display names and avatars are all
// deliberately absent: a subscriber's own read authorization decides what they
// may see of a roster, and that decision belongs to the details endpoint, not to
// a broadcast that is fanned out to every subscriber at once. Clients treat this
// as "your view of this target is stale" and refetch, exactly as they already do
// for pin.updated.
//
// Carrying no identities is also what makes the event idempotent. A refetch
// replaces the panel's list wholesale rather than appending to it, so the HTTP
// response and this event cannot combine into a duplicated member row, and a
// retry that broadcasts twice costs one extra request and changes nothing.
//
// ActorUserID is exposed like PinEventPayload's, so a client can recognise the
// echo of its own write. MemberCount is the authoritative post-commit total,
// letting a subscriber correct its counter without waiting for the refetch.
type MembersAddedPayload struct {
	ActorUserID string `json:"actor_user_id"`
	// AddedCount is how many participants were newly persisted, never who.
	AddedCount int `json:"added_count"`
	// MemberCount is the target's total active participants after the commit.
	MemberCount int `json:"member_count"`
}

// AttachmentStatusPayload carries one attachment's new antimalware state
// (RF-22).
//
// Three fields, and the omissions are the design. There is no filename, no
// content type, no size, no uploader and no signature name: the recipient is
// every subscriber of a channel, and a broadcast is the wrong place to describe
// a file. What a client needs is which row changed and what it changed to, so
// that is all it gets — everything else comes from an authorised read.
//
// Status is one of the three functional states the file service persists:
// "pending_scan" (em analise), "clean" (aprovado), "rejected" (bloqueado). It is
// echoed as the producer sent it, after canonicalisation against that closed
// set, and grants nothing on its own: the download gate is server-side in
// file-service and re-evaluated on every request, so a client that fabricated a
// "clean" for itself would still be refused the bytes.
type AttachmentStatusPayload struct {
	AttachmentID string `json:"attachment_id"`
	Status       string `json:"status"`
	// UpdatedAt is when the verdict was persisted, as an RFC 3339 string. It is
	// a string rather than a time.Time so a producer's formatting cannot make
	// the whole envelope undecodable; the client uses it only to ignore an
	// event older than what it already shows.
	UpdatedAt string `json:"updated_at"`
}

type PinEventPayload struct {
	MessageID string `json:"message_id"`
	// ActorUserID is the user who pinned/unpinned, exposed like sender_id so
	// clients can reconcile their own optimistic state.
	ActorUserID string `json:"actor_user_id"`
	// Pinned is true for a pin, false for an unpin.
	Pinned bool `json:"pinned"`
}

type ReactionEventPayload struct {
	MessageID string `json:"message_id"`
	// ActorUserID is intentionally exposed, equivalent to sender_id on
	// message.created, so clients can reconcile the authenticated user's state.
	ActorUserID string            `json:"actor_user_id"`
	Emoji       string            `json:"emoji"`
	Added       bool              `json:"added"`
	Reactions   []ReactionPayload `json:"reactions"`
}

type CallEventPayload struct {
	ID         string            `json:"call_id"`
	RequestID  string            `json:"request_id"`
	CallerID   string            `json:"caller_id"`
	CalleeID   string            `json:"callee_id"`
	CallType   domain.CallType   `json:"call_type"`
	Status     domain.CallStatus `json:"status"`
	Version    int64             `json:"version"`
	CreatedAt  time.Time         `json:"created_at"`
	OccurredAt time.Time         `json:"occurred_at"`
	ExpiresAt  time.Time         `json:"expires_at"`
	AcceptedAt *time.Time        `json:"accepted_at,omitempty"`
	EndedAt    *time.Time        `json:"ended_at,omitempty"`
}

// Event is the outbound event envelope sent to WebSocket clients and exchanged
// over the distributed BroadcastBus.
//
// Security notes:
//   - Payload.BodyText must not be included in any server log statement.
//   - WorkspaceID, TargetType, TargetID are server-generated; never client-provided.
//   - No tokens, secrets, or credentials may appear in any field.
//   - SourceInstanceID is used for echo-suppression only; do not trust it for
//     authorization. Authorization re-check happens independently.
type Event struct {
	SchemaVersion int        `json:"schema_version"`
	Type          EventType  `json:"type"`
	WorkspaceID   string     `json:"workspace_id"`
	TargetType    TargetType `json:"target_type"`
	TargetID      string     `json:"target_id"`
	// MessageID is populated for message.created events (retained for bus relay).
	MessageID string `json:"message_id,omitempty"`
	// Payload carries the full message DTO for direct browser insertion.
	// Omitted on canonicalized remote events (body_text is not re-trusted from bus).
	Payload       *MessagePayload        `json:"payload,omitempty"`
	MessageUpdate *MessageUpdatedPayload `json:"message_update,omitempty"`
	Reaction      *ReactionEventPayload  `json:"reaction,omitempty"`
	Pin           *PinEventPayload       `json:"pin,omitempty"`
	Members       *MembersAddedPayload   `json:"members,omitempty"`
	// Attachment carries an antimalware verdict (RF-22). Set only by
	// file-service, over the bus; this hub never populates it.
	Attachment *AttachmentStatusPayload `json:"attachment,omitempty"`
	// RecipientUserID routes a user-scoped event to exactly one user.
	//
	// Set only for conversation.available, which is not delivered by
	// subscription: the person it concerns has just been added to a target they
	// do not subscribe to. Every other event routes by (workspace, target) and
	// leaves this empty.
	//
	// It is the routing key across the distributed bus, so a receiving instance
	// can find that user's local sessions without any shared subscription state.
	// It is always a value the publishing transaction confirmed, never one taken
	// from a request body.
	RecipientUserID string            `json:"recipient_user_id,omitempty"`
	Call            *CallEventPayload `json:"call,omitempty"`
	// EventID is a server-generated UUID assigned at publish time.
	// Used for idempotency and observability; not a security boundary.
	EventID string `json:"event_id,omitempty"`
	// SourceInstanceID identifies the chat-service instance that originated
	// the event. Remote receivers use this to suppress self-echo — events
	// with a matching SourceInstanceID are dropped without delivery.
	SourceInstanceID string    `json:"source_instance_id,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
}

// ClientMessage is an inbound control message from a connected WebSocket client.
// Clients may only subscribe, unsubscribe, or ping – never send chat messages.
// Workspace and user identity are always taken from the server auth context,
// never from the fields of this message.
type ClientMessage struct {
	Type         ClientMessageType `json:"type"`
	TargetType   TargetType        `json:"target_type,omitempty"`
	TargetID     string            `json:"target_id,omitempty"`
	MessageID    string            `json:"message_id,omitempty"`
	Emoji        string            `json:"emoji,omitempty"`
	RequestID    string            `json:"request_id,omitempty"`
	CallID       string            `json:"call_id,omitempty"`
	TargetUserID string            `json:"target_user_id,omitempty"`
	CallType     domain.CallType   `json:"call_type,omitempty"`
}
