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
	// MessageStatusPendingLinkScan marks a message accepted but withheld while
	// Cloudflare URL Scanner decides about the links it carries (RF-21).
	//
	// It is not a visible state. Every read path filters `status = 'active'`, so
	// a message in this state is delivered to nobody — including its sender's own
	// listing — until it is promoted. The sender learns about it from the create
	// response, which is the one place it is ever named.
	MessageStatusPendingLinkScan MessageStatus = "pending_link_scan"
)

// isEditable reports whether a message in this state may have its body changed.
//
// Only `active`. A deleted message has nothing to edit, and a withheld one must
// not be edited at all: applying the change would broadcast the new body to
// every subscriber of the target, which is exactly the delivery the withholding
// prevents — and it would also decouple the content from the scan that is
// running against it. Waiting until the scan resolves costs the author one
// retry; the alternative costs the guarantee.
func (s MessageStatus) isEditable() bool {
	return s == MessageStatusActive
}

// LinkSafetyState is the authoritative answer to "what happened to the message
// I am still showing as being checked?" (RF-21).
//
// It exists because realtime delivery is best-effort. A message.blocked
// published while its author's socket was down reaches nobody, and the author's
// client would otherwise show "checking links…" forever — the message is gone
// from the database's point of view and no further event is coming. On reconnect
// the client asks for the current state of the ids it still holds as pending,
// and these are the four answers.
//
// Absence from a reply is deliberately not one of the four. A message this
// service will not talk about — someone else's, another workspace's, one that
// never existed — is simply omitted, so the caller cannot use the endpoint to
// discover which ids are real, and the client leaves an unanswered id alone
// rather than guessing.
type LinkSafetyState string

const (
	// LinkSafetyStatePending is still withheld: no verdict yet.
	LinkSafetyStatePending LinkSafetyState = "pending"
	// LinkSafetyStateActive was cleared and published.
	LinkSafetyStateActive LinkSafetyState = "active"
	// LinkSafetyStateBlocked was refused because a link in it was malicious.
	LinkSafetyStateBlocked LinkSafetyState = "blocked"
	// LinkSafetyStateDeleted is gone for a reason this service cannot attribute
	// to the link check. It is separate from blocked on purpose: telling an
	// author their link was malicious when it may simply have been deleted is a
	// claim we have no evidence for.
	LinkSafetyStateDeleted LinkSafetyState = "deleted"
)

// LinkSafetyReasonMaliciousLink and LinkSafetyReasonLinkCheckInconclusive are
// the only reasons a blocked state carries — a closed set, like the websocket
// event's. Which category Cloudflare reported is not the author's to receive,
// and repeating it would make this an oracle for what the provider already
// knows; a scan that finished without a usable verdict is reported as such and
// not conflated with malicious, because it is not evidence the link is unsafe.
const (
	LinkSafetyReasonMaliciousLink         = "malicious_link"
	LinkSafetyReasonLinkCheckInconclusive = "link_check_inconclusive"
)

// MessageLinkSafetyState pairs one message id with its current state and, for
// a blocked state, why.
type MessageLinkSafetyState struct {
	MessageID string
	State     LinkSafetyState
	// Reason is one of the LinkSafetyReason* constants, and is only ever set
	// when State is LinkSafetyStateBlocked.
	Reason string
}

// MessageLinkSafety is the link-safety axis of a published message (RF-21,
// issue #135).
//
// It is deliberately not part of MessageStatus. Status answers "does this message
// exist for readers" — pending_link_scan, active, deleted — and this answers "what
// is known about the URLs it carries". Folding the two into one enum is what
// produced the bug this exists to fix: a scan that finished without a usable
// verdict had to be expressed as `deleted`, so an operational refusal by the
// provider silently became "your message was rejected".
//
// The three non-empty values and what each one authorises:
//
//	safe          published, links usable, server-side preview ALLOWED
//	inconclusive  published, links usable, server-side preview FORBIDDEN
//	malicious     published body, links NOT usable, preview FORBIDDEN
//
// Inconclusive is the asymmetric one and that asymmetry is the feature. It means
// "this deployment has no clearance to fetch that URL on its own behalf", which is
// a statement about our own authority, not about the link. A reader may still
// click it; their browser is not our server.
type MessageLinkSafety string

const (
	// MessageLinkSafetyNone is a message with nothing to say about links: it
	// carries none, or it predates this axis. It is the zero value, and that is
	// safe in the only direction that matters — see AllowsServerFetch, which says
	// no to it.
	MessageLinkSafetyNone MessageLinkSafety = ""
	// MessageLinkSafetySafe means every URL in the message holds a fresh explicit
	// clearance. It is the only value that authorises anything.
	MessageLinkSafetySafe MessageLinkSafety = "safe"
	// MessageLinkSafetyInconclusive means at least one URL's scan reached a
	// terminal state without a usable verdict, and none was malicious.
	MessageLinkSafetyInconclusive MessageLinkSafety = "inconclusive"
	// MessageLinkSafetyMalicious means at least one URL was condemned. Reachable
	// on a published message only through reconciliation, which may prove a link
	// malicious after the message was already delivered on inconclusive.
	MessageLinkSafetyMalicious MessageLinkSafety = "malicious"
)

// AllowsServerFetch reports whether this deployment may open a connection to the
// URLs this message carries — an Open Graph GET, a HEAD, a thumbnail, an unfurl,
// anything at all.
//
// An allowlist of exactly one value, and phrased as a question about clearance
// rather than as "is it not blocked". That is the point: `active` is not a
// clearance, `inconclusive` is not a clearance, the zero value is not a
// clearance, and a value added to this type in a later version will not be one
// either. Nothing in this codebase may infer permission to fetch from the fact
// that a message was published.
//
// It is not the authorisation itself — file-service re-derives the verdict from
// its own store on every preview request, against the row rather than against
// anything a chat payload said. This is the in-process expression of the same
// rule, so a caller here cannot reach for the weaker one.
func (s MessageLinkSafety) AllowsServerFetch() bool {
	return s == MessageLinkSafetySafe
}

// RestrictsLinks reports whether a reader's client must refuse to make the URLs
// in this message usable.
//
// Only a condemned link. An inconclusive one is deliberately not restricted:
// there is no evidence against it, and dressing an operational refusal up as a
// security warning teaches readers the wrong thing about both.
func (s MessageLinkSafety) RestrictsLinks() bool {
	return s == MessageLinkSafetyMalicious
}

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
	ID               string
	WorkspaceID      string
	ChannelID        string
	DMConversationID string
	SenderID         string
	Kind             MessageKind
	BodyText         string
	BodyFormat       MessageBodyFormat
	Status           MessageStatus
	// LinkSafety is the link-safety axis, independent of Status. See
	// MessageLinkSafety: it is what a client needs to decide whether to draw the
	// "could not verify" notice, and what nothing in this service may read as
	// permission to fetch a URL.
	LinkSafety             MessageLinkSafety
	ParentMessageID        string
	ForwardedFromMessageID string
	ReferencedMessageID    string
	EditedAt               time.Time
	EditCount              int
	DeletedAt              time.Time
	CreatedAt              time.Time
	UpdatedAt              time.Time

	// EventType and EventPayload describe a server-generated conversation event
	// and are set only on Kind == MessageKindSystem (issue #527). The database
	// enforces that pairing, so a user message can never carry one — which is
	// what keeps a system event unforgeable through the ordinary send endpoint.
	EventType    string
	EventPayload ConversationEventPayload

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

	// CreateFingerprint identifies the operation that created this message, and
	// is populated only by the idempotency replay lookup. It never leaves the
	// service layer — no HTTP response carries it — and exists so a reused key
	// can be told from a genuine retry.
	CreateFingerprint string
}

// MaxMessageAttachments bounds how many attachments one message may be created
// with.
//
// The associative table stores positions 0..9, so ten is both the product
// policy and the storage ceiling. It is enforced server-side: an unbounded list
// would turn one message request into unbounded authorization work.
const (
	MaxMessageAttachments            = 10
	DefaultMaxMessageAttachmentBytes = int64(512 << 20)
)

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

// ValidateMessageEdit enforces author, state, and workspace-window rules.
// now must come from the server/database, never from the request payload.
//
// The state rule is an allowlist rather than a "not deleted" check, and that is
// deliberate. It used to permit every status except deleted, so RF-21's new
// withheld state was editable by default — and editing one published a
// message.updated carrying the new body to everyone subscribed to the target,
// which is precisely the content the withholding exists to keep from them.
// Adding a state to MessageStatus must not silently make it editable, so the
// question this asks is "is this state editable?" and not "is this state one of
// the ones I remembered to exclude?".
func ValidateMessageEdit(message Message, requesterID string, editWindowSeconds *int, now time.Time) error {
	// A system message is an immutable record of something that happened — a
	// rename, a departure (issue #527). Its sender_id is the actor, so without
	// this the person who renamed a channel could edit the line that says they
	// did. The same rule ValidateMessageDelete already applies, for the same
	// reason: these operations presuppose a user message.
	if message.Kind != MessageKindUser {
		return ErrEditForbidden
	}
	if message.SenderID != requesterID || !message.DeletedAt.IsZero() || !message.Status.isEditable() {
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
	UpdatedAt  time.Time
	// LinkSafety is the quoted message's own marker, read live from its row.
	// A quote is a copy of a body, so it has to answer the same question the
	// body does — and a message condemned after being quoted has to stop
	// showing through the quote (issue #135, CQ-002).
	LinkSafety MessageLinkSafety
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
	UpdatedAt         time.Time
	// LinkSafety of the referenced message, for the same reason as on a quote.
	LinkSafety MessageLinkSafety
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
