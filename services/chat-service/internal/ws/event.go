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
	EventTypeMessageCreated EventType = "message.created"
)

// ClientMessageType is the type of inbound control message a client may send.
// Clients are limited to control actions; they may not send chat messages over WS.
type ClientMessageType string

const (
	ClientMessageTypeSubscribe   ClientMessageType = "subscribe"
	ClientMessageTypeUnsubscribe ClientMessageType = "unsubscribe"
	ClientMessageTypePing        ClientMessageType = "ping"
)

// Event is the outbound event envelope sent to WebSocket clients.
//
// Security notes:
//   - Payload must never contain message body text (no log-leakage risk).
//   - WorkspaceID, TargetType, TargetID are server-generated; never client-provided.
//   - No tokens, secrets, or credentials may appear in any field.
type Event struct {
	Type        EventType  `json:"type"`
	WorkspaceID string     `json:"workspace_id"`
	TargetType  TargetType `json:"target_type"`
	TargetID    string     `json:"target_id"`
	// MessageID is populated for message.created events.
	MessageID string    `json:"message_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// ClientMessage is an inbound control message from a connected WebSocket client.
// Clients may only subscribe, unsubscribe, or ping – never send chat messages.
// Workspace and user identity are always taken from the server auth context,
// never from the fields of this message.
type ClientMessage struct {
	Type       ClientMessageType `json:"type"`
	TargetType TargetType        `json:"target_type,omitempty"`
	TargetID   string            `json:"target_id,omitempty"`
}
