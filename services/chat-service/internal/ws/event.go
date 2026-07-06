package ws

import "time"

// TargetType identifies the kind of chat target a subscription refers to.
type TargetType string

const (
	TargetTypeChannel TargetType = "channel"
	TargetTypeDM      TargetType = "dm"
)

// EventType identifies the kind of server-sent event envelope.
type EventType string

const (
	// EventTypeMessageCreated is emitted after a message has been persisted.
	EventTypeMessageCreated  EventType = "message.created"
	EventTypeReactionUpdated EventType = "reaction.updated"
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
	ID                string     `json:"id"`
	WorkspaceID       string     `json:"workspace_id"`
	ChannelID         string     `json:"channel_id,omitempty"`
	DMConversationID  string     `json:"dm_conversation_id,omitempty"`
	SenderID          string     `json:"sender_id"`
	SenderDisplayName string     `json:"sender_display_name"`
	Kind              string     `json:"kind"`
	BodyText          string     `json:"body_text"`
	BodyFormat        string     `json:"body_format"`
	Status            string     `json:"status"`
	IsRemoved         bool       `json:"is_removed"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
	EditedAt          *time.Time `json:"edited_at,omitempty"`
	DeletedAt         *time.Time `json:"deleted_at,omitempty"`
}

type ReactionPayload struct {
	Emoji string `json:"emoji"`
	Count int    `json:"count"`
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
	Payload  *MessagePayload       `json:"payload,omitempty"`
	Reaction *ReactionEventPayload `json:"reaction,omitempty"`
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
	Type       ClientMessageType `json:"type"`
	TargetType TargetType        `json:"target_type,omitempty"`
	TargetID   string            `json:"target_id,omitempty"`
	MessageID  string            `json:"message_id,omitempty"`
	Emoji      string            `json:"emoji,omitempty"`
}
