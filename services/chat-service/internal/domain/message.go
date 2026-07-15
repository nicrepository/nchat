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

// MessageBodyFormat selects the grammar used to render BodyText.
type MessageBodyFormat string

const (
	MessageBodyFormatV1 MessageBodyFormat = "v1"
	MessageBodyFormatV2 MessageBodyFormat = "v2"
	MessageBodyFormatV3 MessageBodyFormat = "v3"
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
	BodyFormat             MessageBodyFormat
	Status                 MessageStatus
	ParentMessageID        string
	ForwardedFromMessageID string
	ReferencedMessageID    string
	EditedAt               time.Time
	EditCount              int
	DeletedAt              time.Time
	CreatedAt              time.Time
	UpdatedAt              time.Time

	// Populated by list queries that JOIN auth.users; empty for create results.
	SenderDisplayName string
	SenderEmail       string
	Reactions         []MessageReaction

	// IsFavorited reports whether the requesting user favorited this message.
	// Always scoped to the caller — never exposes other users' favorites.
	IsFavorited bool

	// Quoted is the immediate parent message preview for quote-reply.
	// It is intentionally one level only; nested parent quotes are not populated.
	Quoted *QuotedMessage
}

// MessageEditHistory is a previous persisted version of a message body.
type MessageEditHistory struct {
	ID           string
	MessageID    string
	Body         string
	BodyFormat   MessageBodyFormat
	EditorUserID string
	VersionedAt  time.Time
}

// ValidateMessageEdit enforces author, deletion, and workspace-window rules.
// now must come from the server/database, never from the request payload.
func ValidateMessageEdit(message Message, requesterID string, editWindowSeconds *int, now time.Time) error {
	if message.SenderID != requesterID || message.Status == MessageStatusDeleted || !message.DeletedAt.IsZero() {
		return ErrEditForbidden
	}
	if editWindowSeconds != nil && now.Sub(message.CreatedAt) > time.Duration(*editWindowSeconds)*time.Second {
		return ErrEditWindowExpired
	}
	return nil
}

// ValidateMessageDelete allows only the author of a user message to remove it.
// Already-deleted messages are valid so DELETE remains idempotent.
func ValidateMessageDelete(message Message, requesterID string) error {
	if message.SenderID != requesterID || message.Kind != MessageKindUser {
		return ErrForbidden
	}
	return nil
}

// QuotedMessage is the inline parent preview attached to a reply.
// Status is kept for serializers to reuse the deleted-message placeholder rule.
type QuotedMessage struct {
	ID         string
	AuthorID   string
	BodyText   string
	BodyFormat MessageBodyFormat
	Status     MessageStatus
	DeletedAt  time.Time
	CreatedAt  time.Time
}

// FavoriteMessage is one entry in a user's private favorites list.
type FavoriteMessage struct {
	Message     Message
	FavoritedAt time.Time
}

// MessageReaction is an aggregate safe to expose to a message viewer.
type MessageReaction struct {
	Emoji       string
	Count       int
	ReactedByMe bool
}
