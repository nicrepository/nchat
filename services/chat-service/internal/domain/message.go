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

	// Reference is the caller-authorized RF-09 preview. When the message has a
	// reference but the caller cannot currently read its origin, Available is false
	// and every other field is empty.
	Reference *MessageReference

	// Attachments are the files bound to this message (RF-32), read through
	// chat.message_attachments. Empty for every message that carries none.
	Attachments []MessageAttachment
}

// MaxMessageAttachments bounds how many attachments one message may be created
// with.
//
// It is one because the composer uploads one file at a time and keeps a single
// pending attachment; the API is nonetheless a list so raising this is a
// constant and a UI change rather than a new field. It is enforced server-side
// and is not a UI convenience: an unbounded list is an unbounded amount of
// validation work per request.
const MaxMessageAttachments = 1

// MessageAttachment is the metadata a message viewer may see about a file bound
// to a message.
//
// The omissions are the contract. There is no storage object key, no wrapped
// DEK, no KEK identifier, no envelope version and no scanner detail: those are
// file-service's internals and never leave it. Status and PreviewStatus are the
// two lifecycle values the UI needs to decide what to draw, and neither grants
// anything — content and preview delivery are gated by file-service on every
// request, against the row rather than against anything said here.
type MessageAttachment struct {
	ID       string
	Filename string
	// ContentType is the detected type when the server has one, and the declared
	// one otherwise. It is a rendering hint, never an authorization input.
	ContentType   string
	SizeBytes     int64
	Status        string
	PreviewStatus string
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

// MessageReference is the one-level, read-time preview of a cross-target
// message reference. It deliberately excludes author IDs, reactions, edit
// history, deletion state, attachments, and nested references.
type MessageReference struct {
	Available         bool
	MessageID         string
	TargetType        string
	TargetID          string
	TargetLabel       string
	AuthorDisplayName string
	BodyText          string
	BodyFormat        MessageBodyFormat
	CreatedAt         time.Time
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
