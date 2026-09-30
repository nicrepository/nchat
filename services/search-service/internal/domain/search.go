package domain

import (
	"errors"
	"time"
)

var (
	ErrInvalidInput = errors.New("invalid input")
	ErrUnauthorized = errors.New("unauthorized")
)

// Conversation kinds are the two route shapes the chat already has:
// /chat/channel/:id and /chat/dm/:id (a group is a dm conversation).
const (
	ConversationChannel = "channel"
	ConversationDM      = "dm"
)

// LegacyMessageResult is GET /api/search/messages exactly as it was before
// #900: channel messages only, for clients that route every result to
// /chat/channel/:channel_id. Deprecated, retained for rollout compatibility
// (docs/api/search.md); a DM or a group can never be expressed in it.
type LegacyMessageResult struct {
	ID                string    `json:"id"`
	ChannelID         string    `json:"channel_id"`
	ChannelName       string    `json:"channel_name"`
	SenderID          string    `json:"sender_id"`
	SenderDisplayName string    `json:"sender_display_name"`
	BodyText          string    `json:"body_text"`
	CreatedAt         time.Time `json:"created_at"`
	Score             float64   `json:"score"`
}

// MessageResult (GET /api/search/v2/messages) is a message the caller may read. ConversationType is the
// conversation's own type ("public"/"private" for a channel, "direct"/"group"
// for a dm) and ConversationName is what the chat already calls it: the channel
// name, the group title, or the other participant of a direct conversation.
type MessageResult struct {
	ID                string    `json:"id"`
	ConversationKind  string    `json:"conversation_kind"`
	ConversationID    string    `json:"conversation_id"`
	ConversationType  string    `json:"conversation_type"`
	ConversationName  string    `json:"conversation_name"`
	SenderID          string    `json:"sender_id"`
	SenderDisplayName string    `json:"sender_display_name"`
	SenderAvatarURL   *string   `json:"sender_avatar_url,omitempty"`
	BodyText          string    `json:"body_text"`
	CreatedAt         time.Time `json:"created_at"`
	Score             float64   `json:"score"`
}
type UserResult struct {
	ID          string  `json:"id"`
	DisplayName string  `json:"display_name"`
	AvatarURL   *string `json:"avatar_url,omitempty"`
	SortName    string  `json:"-"`
}

// ChannelResult carries MemberCount under the channel-details predicate (#877):
// active channel membership, active workspace membership, active user.
type ChannelResult struct {
	ID          string  `json:"id"`
	Slug        string  `json:"slug"`
	DisplayName string  `json:"display_name"`
	Type        string  `json:"type"`
	Description *string `json:"description,omitempty"`
	MemberCount int     `json:"member_count"`
	IsGeneral   bool    `json:"is_general"`
	SortName    string  `json:"-"`
}

// GroupResult is a group conversation the caller participates in.
// LastMessageAt is the sidebar's own activity signal and is absent when the
// group has no message yet.
type GroupResult struct {
	ID               string     `json:"id"`
	Title            string     `json:"title"`
	ParticipantCount int        `json:"participant_count"`
	LastMessageAt    *time.Time `json:"last_message_at,omitempty"`
	SortName         string     `json:"-"`
}

// FileResult is attachment metadata only: never a storage key, a URL or bytes.
// Status and PreviewStatus are the same columns chat-service publishes on a
// message's attachments, so the viewer reads them exactly as it does there.
type FileResult struct {
	ID               string    `json:"id"`
	Filename         string    `json:"filename"`
	ContentType      string    `json:"content_type"`
	SizeBytes        int64     `json:"size"`
	Status           string    `json:"status"`
	PreviewStatus    string    `json:"preview_status"`
	MessageID        string    `json:"message_id"`
	ConversationKind string    `json:"conversation_kind"`
	ConversationID   string    `json:"conversation_id"`
	ConversationType string    `json:"conversation_type"`
	ConversationName string    `json:"conversation_name"`
	CreatedAt        time.Time `json:"created_at"`
}

type Page[T any] struct {
	Items      []T
	NextCursor string
}

type (
	LegacyMessagePage = Page[LegacyMessageResult]
	MessagePage       = Page[MessageResult]
	UserPage          = Page[UserResult]
	ChannelPage       = Page[ChannelResult]
	GroupPage         = Page[GroupResult]
	FilePage          = Page[FileResult]
)
