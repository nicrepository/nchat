package domain

import "time"

// MessageKind classifies the origin of a message.
type MessageKind string

const (
	// MessageKindUser is a message posted by a human sender.
	MessageKindUser MessageKind = "user"
	// MessageKindSystem is a server-generated informational message.
	MessageKindSystem MessageKind = "system"
)

// MessageStatus represents the lifecycle state of a message.
type MessageStatus string

const (
	// MessageStatusActive is the default state for a visible message.
	MessageStatusActive MessageStatus = "active"
	// MessageStatusDeleted marks a soft-deleted message kept as a placeholder.
	MessageStatusDeleted MessageStatus = "deleted"
)

// Message is a single message posted to a channel or a DM conversation.
//
// Exactly one of ChannelID and DMConversationID is non-empty; the other is
// always empty string (maps to NULL in the database).
//
// ParentMessageID, ForwardedFromMessageID, and ReferencedMessageID are empty
// string when NULL (no reference). EditedAt and DeletedAt are zero time.Time
// when NULL.
type Message struct {
	ID                     string
	WorkspaceID            string
	ChannelID              string
	DMConversationID       string
	SenderID               string
	Kind                   MessageKind
	BodyText               string
	Status                 MessageStatus
	ParentMessageID        string
	ForwardedFromMessageID string
	ReferencedMessageID    string
	EditedAt               time.Time
	DeletedAt              time.Time
	CreatedAt              time.Time
	UpdatedAt              time.Time

	// Populated by list queries that JOIN auth.users; empty for create results.
	SenderDisplayName string
	SenderEmail       string
}
