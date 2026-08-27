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
	EventTypeMessageCreated EventType = "message.created"
	EventTypeMessageUpdated EventType = "message.updated"
	// EventTypeMessageBlocked tells one author that their withheld message was
	// refused by the link-safety check (RF-21).
	//
	// It carries no body and no verdict detail — only the message id and a fixed
	// reason — and it is addressed to a single recipient rather than to a
	// conversation. Everyone else was never shown the message and must not learn
	// that it existed.
	EventTypeMessageBlocked EventType = "message.blocked"
	// EventTypeMessageLinkSafetyChanged tells the subscribers of a conversation
	// that what is known about a published message's links has changed (issue
	// #135).
	//
	// It exists because a message may now be published while its links are only
	// *inconclusive* — the provider confirmed a scan finished without producing a
	// verdict, which is not evidence of anything and must not block a legitimate
	// send. Reconciliation may later obtain the real answer, in either direction,
	// and this is how every client that already holds the message converges:
	//
	//   inconclusive -> safe       the "could not verify" notice is removed
	//   inconclusive -> malicious  the links stop being usable
	//
	// It is addressed to the conversation and not to the author, unlike
	// message.blocked, and that is the whole difference: the message *was*
	// delivered, so everyone holding it has to be corrected. Emitting a second
	// message.created instead would duplicate the message and re-fire its
	// mentions; this event mutates one field of a message the client already has.
	//
	// The payload is a message id and a state from a closed set. No URL, no scan
	// uuid, no provider text — see MessageLinkSafetyPayload.
	EventTypeMessageLinkSafetyChanged EventType = "message.link_safety_changed"
)

// MessageLinkSafetyPayload carries the new link-safety state of one published
// message (issue #135).
//
// Three fields, and the omissions are the contract. There is no URL, because a
// subscriber does not need to be told which of a message's links changed and a
// broadcast is the worst possible place to enumerate them. There is no scan uuid,
// because that is an account-internal provider identifier. There is no provider
// message: Cloudflare's own text is an English sentence that can name the
// hostname, and it is never shown to a user or carried in an event.
//
// State is one of the domain.MessageLinkSafety values, as a string. It is
// re-validated against that closed set when the event arrives over the bus, so a
// remote instance cannot inject a state this version does not understand — and in
// particular cannot invent one that a client might treat as a clearance.
//
// UpdatedAt orders this correction against message.updated/message.created.
// It is safe to relay across the bus, unlike MessagePayload, precisely because it
// carries no user content. There is nothing here to re-fetch by id.
type MessageLinkSafetyPayload struct {
	MessageID string `json:"message_id"`
	// State is a domain.MessageLinkSafety value: "safe", "inconclusive" or
	// "malicious". The empty state is never announced — a message with nothing to
	// say about links has nothing to correct.
	State     string    `json:"state"`
	UpdatedAt time.Time `json:"updated_at"`
}

// MessageBlockedReasonMaliciousLink and MessageBlockedReasonLinkCheckInconclusive
// are the only reasons message.blocked carries — a closed set.
//
// Constants rather than a message from the provider: whichever category
// Cloudflare reported is not the author's to receive, and repeating it would
// make the endpoint an oracle for which domains are already known. The two are
// kept distinct so a scan that finished without a usable verdict is never
// announced to its author as "malicious" — it is fail-closed, not a finding.
const (
	MessageBlockedReasonMaliciousLink         = "malicious_link"
	MessageBlockedReasonLinkCheckInconclusive = "link_check_inconclusive"
)

const (
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

	// EventTypeConversationUpdated tells the subscribers of a channel or group
	// that its metadata changed — today, that it was renamed (issue #527).
	//
	// Route-only, exactly like pin.updated minus the flag: it names the
	// conversation and says nothing else. No name, no actor, no old value. A
	// subscriber who receives it refetches the sidebar, which re-derives
	// visibility server-side, so the event grants nothing and cannot be used to
	// read a new name without the authorization the sidebar endpoint applies.
	//
	// One event for channels and groups because it is one fact — "this
	// conversation's metadata changed" — and target_type already says which kind
	// it is. Two near-identical events would be two things to keep in step.
	//
	// Carrying no payload is also what makes it idempotent for free: two copies
	// of the same event, or one arriving alongside a refetch that already has the
	// new name, cost one extra refetch and can never produce a second row for the
	// same conversation.
	EventTypeConversationUpdated EventType = "conversation.updated"

	// EventTypeConversationEvent tells the subscribers of a channel or group
	// that a system message was persisted in it — a rename, a departure
	// (issue #527).
	//
	// Route plus message id, and nothing else. Not the event type, not the old
	// or new name, not the actor: the message is read back through the same
	// authorized listing every other message goes through, so a subscriber who
	// may not read the conversation learns nothing from the broadcast, and the
	// rendered text can never come from a payload a remote node supplied.
	//
	// Carrying the id rather than the content is also what makes it idempotent:
	// two copies name the same message, and a client that already has it does
	// nothing.
	EventTypeConversationEvent EventType = "conversation.event"

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

	// EventTypePresenceUpdated carries one user's online/away/offline state
	// (RF-58).
	//
	// It routes by (workspace, target) like message.created, and that is the
	// whole authorization story: a presence event is published once per target
	// the user is subscribed to, so it can only reach people who share a channel
	// or a conversation with them and who pass the same fan-out re-check every
	// other event passes. There is deliberately no workspace-wide presence
	// broadcast — that would let any member, Guest included, enumerate everyone
	// connected to the workspace, which no existing read grants.
	//
	// The payload is a user id, a state and when the server decided it. No last
	// seen instant, no device, no session, no address: presence answers "can I
	// reach this person now", and everything else would be a second, unrequested
	// disclosure riding on it.
	//
	// The state is always the server's. A client cannot set its own presence and
	// cannot name anyone else's: the only inbound signal that touches presence is
	// activity on an authenticated connection, credited to that connection's
	// server-asserted identity (see Hub.handleClientMessage).
	EventTypePresenceUpdated EventType = "presence.updated"

	// Call lifecycle (RF-23). User-scoped like conversation.available in that
	// they name a peer, but with their own payload and their own validation.
	EventTypeCallRinging   EventType = "call.ringing"
	EventTypeCallAccepted  EventType = "call.accepted"
	EventTypeCallDeclined  EventType = "call.declined"
	EventTypeCallCancelled EventType = "call.cancelled"
	EventTypeCallTimedOut  EventType = "call.timed_out"
	EventTypeCallEnded     EventType = "call.ended"

	// EventTypeTypingUpdated carries one user's typing state in a channel or DM.
	//
	// It routes by (workspace, target) exactly like presence.updated, and for
	// the same reason: fan-out already re-authorizes every subscriber, so that
	// is the whole authorization story here too. Unlike presence, the state is
	// client-declared (typing.start/typing.stop) rather than inferred from
	// arbitrary traffic — the server never invents a typing state, it only
	// relays and expires the one the client asserted, and only for a target the
	// asserting connection is currently authorized on.
	//
	// The payload never carries what was typed, only that someone is typing.
	// Ephemeral by design: never persisted to Postgres, backed by a short-TTL
	// Valkey key that self-clears if no stop is ever sent.
	EventTypeTypingUpdated EventType = "typing.updated"
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
	// ClientMessageTypeCallLeave releases one participant's own presence in a
	// resource (channel/group-DM) call without ending it for anyone else —
	// see CallHandler.LeaveCall (issue #569). It has no direct-call meaning:
	// a 1:1 call's participants use call.decline/call.cancel/call.end.
	ClientMessageTypeCallLeave ClientMessageType = "call.leave"
	ClientMessageTypeCallSync  ClientMessageType = "call.sync"
	// ClientMessageTypeCallResourceSync asks for the authoritative active call
	// (if any) of one channel/group-DM target — target-scoped discovery,
	// distinct from call.sync's own-call-only lookup (issue #622). Always
	// answered requester-only; never broadcast, never creates a lease, never
	// issues a token.
	ClientMessageTypeCallResourceSync ClientMessageType = "call.resource.sync"
	// ClientMessageTypeCallJoin admits the actor into an already-known,
	// active resource call — explicit join, distinct from call.start's
	// create-or-reuse semantics (issue #622). Never creates a call.
	ClientMessageTypeCallJoin     ClientMessageType = "call.join"
	ClientMessageTypeCallPresence ClientMessageType = "call.presence"
	ClientMessageTypeTypingStart  ClientMessageType = "typing.start"
	ClientMessageTypeTypingStop   ClientMessageType = "typing.stop"
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
	ID                string `json:"id"`
	WorkspaceID       string `json:"workspace_id"`
	ChannelID         string `json:"channel_id,omitempty"`
	DMConversationID  string `json:"dm_conversation_id,omitempty"`
	SenderID          string `json:"sender_id"`
	SenderDisplayName string `json:"sender_display_name"`
	// SenderAvatarURL mirrors the HTTP message contract's field of the same
	// name (issue #495): the client renders a subscriber's own avatar image
	// instead of initials when this is present, using the exact same
	// same-origin validation the sidebar/profile avatars already apply.
	// Omitted for the overwhelming majority of senders, who have none set.
	SenderAvatarURL string `json:"sender_avatar_url,omitempty"`
	Kind            string `json:"kind"`
	BodyText        string `json:"body_text"`
	BodyFormat      string `json:"body_format"`
	Status          string `json:"status"`
	// LinkSafetyState is the link-safety axis, independent of Status (issue #135).
	// A subscriber uses it to decide whether to draw the "could not verify this
	// link" notice on a message it is inserting. It authorises nothing — see
	// domain.MessageLinkSafety — and is omitted for the overwhelming majority of
	// messages, which carry no links at all.
	LinkSafetyState string        `json:"link_safety_state,omitempty"`
	IsRemoved       bool          `json:"is_removed"`
	CreatedAt       time.Time     `json:"created_at"`
	UpdatedAt       time.Time     `json:"updated_at"`
	EditedAt        *time.Time    `json:"edited_at,omitempty"`
	DeletedAt       *time.Time    `json:"deleted_at,omitempty"`
	Quoted          *QuotePayload `json:"quoted,omitempty"`
	IsForwarded     bool          `json:"is_forwarded"`
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
	MessageID       string     `json:"message_id"`
	ChannelID       string     `json:"channel_id,omitempty"`
	DMID            string     `json:"dm_id,omitempty"`
	Body            string     `json:"body"`
	BodyFormat      string     `json:"body_format"`
	LinkSafetyState string     `json:"link_safety_state"`
	EditedAt        time.Time  `json:"edited_at"`
	EditCount       int        `json:"edit_count"`
	IsEdited        bool       `json:"is_edited"`
	Status          string     `json:"status"`
	IsRemoved       bool       `json:"is_removed"`
	DeletedAt       *time.Time `json:"deleted_at,omitempty"`
	UpdatedAt       time.Time  `json:"updated_at"`
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
	// AudioKind and DurationMs mirror the HTTP payload's own RF-670 fields, so
	// a message delivered over the socket needs no follow-up GET to tell a
	// voice message from an ordinary attachment.
	AudioKind  string `json:"audio_kind,omitempty"`
	DurationMs int64  `json:"duration_ms,omitempty"`
}

type QuotePayload struct {
	ID              string     `json:"id"`
	AuthorID        string     `json:"author_id"`
	Body            string     `json:"body,omitempty"`
	BodyFormat      string     `json:"body_format"`
	LinkSafetyState string     `json:"link_safety_state,omitempty"`
	IsRemoved       bool       `json:"is_removed,omitempty"`
	DeletedAt       *time.Time `json:"deleted_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
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

// PresencePayload is one user's presence, as this server currently holds it
// (RF-58).
//
// State is one of the three PresenceStatus values. UpdatedAt is when the server
// decided that state, as an RFC 3339 string — a string rather than a time.Time
// for the same reason AttachmentStatusPayload's is: a producer's formatting must
// not be able to make the whole envelope undecodable. It is the ordering key,
// and it is server time on every path, so a client that reconciles two events
// never has to trust a browser clock, its own or anyone else's.
type PresencePayload struct {
	UserID    string `json:"user_id"`
	State     string `json:"state"`
	UpdatedAt string `json:"updated_at"`
}

// TypingEventPayload is one user's typing state in a channel or DM.
//
// IsTyping is exactly what the client asserted (typing.start => true,
// typing.stop or expiry => false) — never inferred. UpdatedAt is when the
// server accepted that assertion, as an RFC 3339 string for the same reason
// PresencePayload's is: a producer's formatting must not be able to make the
// whole envelope undecodable, and it is the ordering key a client uses to
// discard a stale update without trusting its own clock.
//
// UserDisplayName is resolved server-side once per WebSocket connection (see
// ws.UserDisplayNameResolver / Client.displayName) rather than guessed by each
// recipient's client from whatever roster or message history it happens to
// have loaded — that guess is what previously fell back to the literal
// placeholder "Alguém" for a channel typist who hadn't posted yet. Empty when
// the lookup failed or the user has no display name; clients keep their own
// heuristic as a fallback in that case.
//
// There is deliberately no body, no draft, no character count: this payload
// answers "is this person typing right now", nothing about what they wrote.
type TypingEventPayload struct {
	UserID          string `json:"user_id"`
	UserDisplayName string `json:"user_display_name,omitempty"`
	IsTyping        bool   `json:"is_typing"`
	UpdatedAt       string `json:"updated_at"`
}

// PresenceSnapshotResponse answers a subscribe with the presence of the users
// already in that target (RF-58).
//
// It is addressed to one client rather than broadcast, exactly like the
// "subscribed" acknowledgement it follows, so it needs no recipient field and
// never reaches the bus. The client has just been authorized for this target, so
// nothing here is disclosed that the subsequent presence.updated stream would
// not disclose anyway.
//
// Users lists only people this instance currently holds a connection for, so
// every entry is online or away. Someone offline is simply absent.
//
// Complete is what makes that absence readable. When true, the list is every
// present subscriber of this target that this instance knows of, so a client may
// conclude that anyone it expected here and did not find is offline. When false
// the list was cut short (see presenceSnapshotMaxUsers) and says nothing about
// who is missing — the entries it does carry remain valid, because each is a
// positive statement about one person.
//
// The scope is always one target. A snapshot for a channel says nothing about
// anybody in a conversation, and a client that treated it as a global answer
// would report strangers as offline on the strength of a list they were never
// eligible to appear in.
type PresenceSnapshotResponse struct {
	Type       string            `json:"type"`
	TargetType TargetType        `json:"target_type"`
	TargetID   string            `json:"target_id"`
	Users      []PresencePayload `json:"users"`
	Complete   bool              `json:"complete"`
	// TakenAt is when the server read this roster, from the same clock every
	// updated_at comes from.
	//
	// A complete snapshot replaces a client's view of one conversation, and
	// replacing means removing people it does not name. Without an instant of its
	// own it could only do that blindly, and a snapshot that was read *before* a
	// transition would undo it — someone would come online and then be erased by
	// an answer computed a moment earlier. With it, the client keeps anything it
	// knows to be newer and drops the rest.
	TakenAt string `json:"taken_at"`
}

// PresenceSnapshotType is the wire value of PresenceSnapshotResponse.Type.
const PresenceSnapshotType = "presence.snapshot"

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
	ID         string                `json:"call_id"`
	RequestID  string                `json:"request_id"`
	CallerID   string                `json:"caller_id"`
	CalleeID   string                `json:"callee_id,omitempty"`
	TargetType domain.CallTargetType `json:"target_type"`
	TargetID   string                `json:"target_id"`
	CallType   domain.CallType       `json:"call_type"`
	Status     domain.CallStatus     `json:"status"`
	Version    int64                 `json:"version"`
	CreatedAt  time.Time             `json:"created_at"`
	OccurredAt time.Time             `json:"occurred_at"`
	ExpiresAt  time.Time             `json:"expires_at"`
	AcceptedAt *time.Time            `json:"accepted_at,omitempty"`
	EndedAt    *time.Time            `json:"ended_at,omitempty"`
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
	// Presence carries one user's online/away/offline state (RF-58).
	Presence *PresencePayload `json:"presence,omitempty"`
	// LinkSafety carries a change to what is known about a published message's
	// links (issue #135). Set only for message.link_safety_changed, and stripped
	// from every other event type on arrival — an event carrying one alongside an
	// unrelated type is relaying a state nobody asked it about.
	LinkSafety *MessageLinkSafetyPayload `json:"link_safety,omitempty"`
	// Typing carries one user's typing state in a channel or DM.
	Typing *TypingEventPayload `json:"typing,omitempty"`
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
	RecipientUserID string `json:"recipient_user_id,omitempty"`
	// Reason is a fixed, closed-set explanation for a terminal event.
	//
	// Set only for message.blocked, where the author needs to know their message
	// was refused and nothing more. It is a controlled enum precisely so no
	// provider text, URL or verdict detail can ever travel in it.
	Reason string            `json:"reason,omitempty"`
	Call   *CallEventPayload `json:"call,omitempty"`
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
	// SyncID correlates a call.resource.sync request to its requester-only
	// call.resource.synced response (issue #622). Client-generated, never
	// used for authorization.
	SyncID string `json:"sync_id,omitempty"`
	// ParticipationID fences a call.leave or call.presence to one specific
	// resource-call admission (issue #622 round 3) — the value that
	// admission's own call.admitted response carried. Empty claims the
	// pre-fencing legacy identity; never used for direct calls, which are
	// never fenced. See docs/api/calls-websocket.md.
	ParticipationID string `json:"participation_id,omitempty"`
}
