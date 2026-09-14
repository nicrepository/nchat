// Package notificationevent is the contract for one notification event: what
// happened, who it is addressed to, where it came from, and how far its
// delivery has got (issue #741, RNF-25).
//
// # Why it is here and not in a service
//
// The event has two owners that must not disagree. chat-service *produces* it,
// in the same PostgreSQL statement that makes the message observable, because a
// message that exists without its notification is the failure this whole design
// exists to prevent. notification-service will *consume* it, deciding what to
// suppress and what to deliver. Restating the event types, the states and the
// dedupe rule in both modules would let them drift, and an outbox whose two
// halves disagree about what "suppressed" means is worse than no outbox at all.
// So the vocabulary lives here once, exactly like urlsafety and
// channelmembership.
//
// # What it is not
//
// It is not the policy engine. Nothing here decides whether an event should
// alert somebody — not the priority, not the origin. It only makes the facts a
// later policy needs unambiguous. Origin in particular reports where an event
// came from; it does not report what should be done about that.
//
// It is not the payload either. The outbox stores references, never content: a
// recipient, an event type, the identity of the source entity. The message body
// stays in chat.messages, behind the authorization every read path applies, and
// nothing downstream of the outbox gains an authority the message itself does
// not have.
//
// # Where the rows live
//
// chat.notification_outbox, in the chat schema, because the row is written by
// the same statement as the message and cross-schema is not cross-database.
// The notification-service worker will read it the same way its SMTP worker
// already reads auth.email_outbox.
package notificationevent

import (
	"errors"
	"fmt"
	"strings"
)

// ErrInvalidIdentity is returned when an event identity cannot be turned into a
// dedupe key. It is a single static error: the caller's response to every
// variant is the same, and the detail belongs in the wrapped message, not in a
// type the caller might branch on.
var ErrInvalidIdentity = errors.New("invalid notification event identity")

// ErrInvalidSuppressedReason is returned when a state and a suppression reason
// do not agree, or when the reason is longer than the column will hold.
var ErrInvalidSuppressedReason = errors.New("invalid notification suppressed reason")

// ErrInvalidTransition is returned when a state change is not one the machine
// allows.
var ErrInvalidTransition = errors.New("invalid notification state transition")

// EventType is what happened, from the recipient's point of view.
//
// The set is deliberately closed and deliberately larger than what produces
// rows today: the table's CHECK constraint has to be widened by a migration,
// and a contract that has to be migrated every time a producer is connected is
// a contract that will be worked around instead. Connected in issue #741:
// mention, reply, direct_message. Declared and not yet produced:
// channel_message, reaction, call.
type EventType string

const (
	// EventTypeDirectMessage is a message in a conversation the recipient is a
	// member of, addressed to them by the conversation itself rather than by
	// name.
	EventTypeDirectMessage EventType = "direct_message"
	// EventTypeMention is the recipient being named in a message body.
	EventTypeMention EventType = "mention"
	// EventTypeReply is a message answering one the recipient wrote.
	EventTypeReply EventType = "reply"
	// EventTypeChannelMessage is a message in a channel the recipient belongs
	// to. Declared, not produced: fan-out to every member of a large channel is
	// a decision that belongs with the policy that says who wants it.
	EventTypeChannelMessage EventType = "channel_message"
	// EventTypeReaction is a reaction on a message the recipient wrote.
	// Declared, not produced.
	EventTypeReaction EventType = "reaction"
	// EventTypeCall is a call the recipient is being invited to. Declared, not
	// produced.
	EventTypeCall EventType = "call"
)

var eventTypes = map[EventType]struct{}{
	EventTypeDirectMessage:  {},
	EventTypeMention:        {},
	EventTypeReply:          {},
	EventTypeChannelMessage: {},
	EventTypeReaction:       {},
	EventTypeCall:           {},
}

// Valid reports whether e is one of the declared event types. The zero value is
// never valid, which is the point: an unset event type must not read as a
// usable one.
func (e EventType) Valid() bool {
	_, ok := eventTypes[e]
	return ok
}

// SourceType is the kind of entity that produced the event. It is what makes a
// dedupe key from one entity family incapable of colliding with another's.
type SourceType string

const (
	// SourceTypeMessage is a chat.messages row.
	SourceTypeMessage SourceType = "message"
	// SourceTypeReaction is a chat.message_reactions row, whose identity is
	// composite and therefore carried in the discriminator.
	SourceTypeReaction SourceType = "reaction"
	// SourceTypeCall is a chat.calls row, which has no message at all.
	SourceTypeCall SourceType = "call"
)

var sourceTypes = map[SourceType]struct{}{
	SourceTypeMessage:  {},
	SourceTypeReaction: {},
	SourceTypeCall:     {},
}

// Valid reports whether s is one of the declared source types.
func (s SourceType) Valid() bool {
	_, ok := sourceTypes[s]
	return ok
}

// Priority is how loudly an event asks to be delivered. It is a fact recorded
// by the producer, not an instruction: the policy engine reads it, and is free
// to suppress a high one.
type Priority string

const (
	// PriorityHigh is an event that names the recipient personally.
	PriorityHigh Priority = "high"
	// PriorityNormal is an event addressed to the recipient by a conversation
	// they belong to.
	PriorityNormal Priority = "normal"
	// PriorityLow is ambient activity.
	PriorityLow Priority = "low"
)

var priorities = map[Priority]struct{}{
	PriorityHigh:   {},
	PriorityNormal: {},
	PriorityLow:    {},
}

// Valid reports whether p is one of the declared priorities.
func (p Priority) Valid() bool {
	_, ok := priorities[p]
	return ok
}

// Origin is where the event came from.
//
// It exists because "this happened just now" and "this is being written into
// the database now" are different facts, and only the first one is a reason to
// interrupt somebody. A timestamp cannot tell them apart — an import writes
// old occurred_at values, a replay writes new ones — so the producer states it
// outright and nothing downstream has to guess.
type Origin string

const (
	// OriginLive is an operation a user just performed.
	OriginLive Origin = "live"
	// OriginImport is data brought in from another system.
	OriginImport Origin = "import"
	// OriginReplay is an event re-emitted from something already recorded.
	OriginReplay Origin = "replay"
	// OriginResync is an event written to repair a divergence.
	OriginResync Origin = "resync"
)

var origins = map[Origin]struct{}{
	OriginLive:   {},
	OriginImport: {},
	OriginReplay: {},
	OriginResync: {},
}

// Valid reports whether o is one of the declared origins.
func (o Origin) Valid() bool {
	_, ok := origins[o]
	return ok
}

// IsHistorical reports whether the event describes something other than an
// operation that just happened.
//
// This is a statement of fact, not a policy: it says the event did not
// originate live, and stops there. What a policy does with that — suppress,
// downgrade, deliver anyway — is decided elsewhere. An invalid origin is not
// historical and not live; it is refused before it can be stored.
func (o Origin) IsHistorical() bool {
	return o.Valid() && o != OriginLive
}

// State is how far the event has got.
//
// Seven states, and the three terminal ones are the reason for all of it:
// Suppressed, Sent and Failed must never be confused with one another.
// "Nobody was told, on purpose", "somebody was told" and "we tried and could
// not" are three different facts, and an outbox that renders the first as the
// third turns a working quiet-hours rule into a delivery incident.
//
// The evaluated step of the issue's diagram is a transition, not a resting
// place: an event leaves Pending as Eligible or as Suppressed, and no row is
// ever observed mid-decision. Adding a state nothing can observe would only
// give the worker a value it has to handle and can never see.
type State string

const (
	// StatePending is a freshly created event no policy has looked at yet. It
	// is what every producer writes.
	StatePending State = "pending"
	// StateEligible is an event a policy decided to deliver.
	StateEligible State = "eligible"
	// StateSuppressed is an event a policy decided not to deliver. Terminal,
	// and emphatically not a failure.
	StateSuppressed State = "suppressed"
	// StateProcessing is an event a worker has claimed and is sending.
	StateProcessing State = "processing"
	// StateSent is an event a delivery channel accepted. Terminal.
	StateSent State = "sent"
	// StateRetrying is an event whose delivery failed and will be attempted
	// again.
	StateRetrying State = "retrying"
	// StateFailed is an event whose delivery failed and will not be attempted
	// again. Terminal.
	StateFailed State = "failed"
)

// stateTransitions is the whole state machine. A state absent from the map, or
// present with an empty target set, accepts no transition at all.
var stateTransitions = map[State][]State{
	StatePending:    {StateEligible, StateSuppressed},
	StateEligible:   {StateProcessing, StateSuppressed},
	StateProcessing: {StateSent, StateRetrying, StateFailed},
	StateRetrying:   {StateProcessing, StateFailed},
	StateSuppressed: {},
	StateSent:       {},
	StateFailed:     {},
}

// Valid reports whether s is one of the declared states.
func (s State) Valid() bool {
	_, ok := stateTransitions[s]
	return ok
}

// IsTerminal reports whether no further transition is possible from s. An
// invalid state is not terminal — it is not a state at all.
func (s State) IsTerminal() bool {
	targets, ok := stateTransitions[s]
	return ok && len(targets) == 0
}

// CanTransitionTo reports whether s may become next. Both states must be
// declared, and a state is never allowed to transition to itself: re-entering
// the same state is a no-op the caller should not have to distinguish from
// progress.
func (s State) CanTransitionTo(next State) bool {
	if !next.Valid() {
		return false
	}
	for _, allowed := range stateTransitions[s] {
		if allowed == next {
			return true
		}
	}
	return false
}

// Identity is everything that decides whether two notification events are the
// same logical event.
//
// A retry of one logical operation must produce one notification; two genuinely
// different operations must produce two. Both halves matter, and a key that is
// too broad fails the second one silently — two different reactions on one
// message would collapse into a single notification and one of them would
// simply never be delivered.
type Identity struct {
	// WorkspaceID and RecipientID are part of the uniqueness, but not part of
	// the key string: they are the other two columns of the unique index
	// (workspace_id, recipient_user_id, dedupe_key), so repeating them inside
	// the string would store the same fact twice. They are still required here,
	// because an identity that does not know its tenant cannot be checked for
	// cross-tenant collision at all.
	WorkspaceID string
	RecipientID string

	// EventType, SourceType and SourceID name the thing that happened. Source
	// type is in the key rather than assumed, so a reaction id can never
	// collide with a message id that happens to be equal.
	EventType  EventType
	SourceType SourceType
	SourceID   string

	// Discriminator separates distinct operations on the same source entity.
	// Empty is correct whenever the source entity is the operation, which is
	// the case for every producer connected today. A reaction needs it — the
	// source message is shared by every reaction on it, so the actor and the
	// emoji are what make two reactions two events.
	Discriminator string
}

// dedupeSegmentMaxLen bounds one segment of the key. The column is bounded too;
// this is the earlier of the two refusals, so a bad value never reaches SQL.
const dedupeSegmentMaxLen = 64

// DedupeKey returns the dedupe identity of the event, in the exact form
// chat.notification_outbox.dedupe_key stores:
//
//	<source_type>:<source_id>:<event_type>[:<discriminator>]
//
// This function is the authority for that format. chat-service builds the same
// string in SQL, because the fan-out is set-based and Go never sees the
// individual rows; TestNotificationOutboxDedupeKeyMatchesSQL proves the two
// agree against a real database.
//
// Every segment is checked for the separator itself. That is not tidiness: the
// discriminator is the one segment that will eventually carry a value derived
// from user input, and a discriminator containing ':' could otherwise be shaped
// to produce the key of a different event and suppress it.
func (i Identity) DedupeKey() (string, error) {
	if err := i.validateScope(); err != nil {
		return "", err
	}
	if !i.EventType.Valid() {
		return "", fmt.Errorf("%w: unknown event type %q", ErrInvalidIdentity, i.EventType)
	}
	if !i.SourceType.Valid() {
		return "", fmt.Errorf("%w: unknown source type %q", ErrInvalidIdentity, i.SourceType)
	}
	if err := validateSegment("source id", i.SourceID, false); err != nil {
		return "", err
	}
	if err := validateSegment("discriminator", i.Discriminator, true); err != nil {
		return "", err
	}
	key := string(i.SourceType) + ":" + i.SourceID + ":" + string(i.EventType)
	if i.Discriminator == "" {
		return key, nil
	}
	return key + ":" + i.Discriminator, nil
}

// validateScope checks the two fields that place the event in a tenant. They
// are split out so DedupeKey stays a flat sequence of guards rather than a
// nested one.
func (i Identity) validateScope() error {
	if i.WorkspaceID == "" {
		return fmt.Errorf("%w: workspace id is required", ErrInvalidIdentity)
	}
	if i.RecipientID == "" {
		return fmt.Errorf("%w: recipient id is required", ErrInvalidIdentity)
	}
	return nil
}

// validateSegment refuses anything that would make the assembled key ambiguous:
// an empty required segment, one longer than the bound, and any value carrying
// the separator or a character that does not survive a round trip through logs
// and SQL unchanged.
func validateSegment(name, value string, optional bool) error {
	if value == "" {
		if optional {
			return nil
		}
		return fmt.Errorf("%w: %s is required", ErrInvalidIdentity, name)
	}
	if len(value) > dedupeSegmentMaxLen {
		return fmt.Errorf("%w: %s is longer than %d bytes", ErrInvalidIdentity, name, dedupeSegmentMaxLen)
	}
	if strings.ContainsFunc(value, isUnsafeSegmentRune) {
		return fmt.Errorf("%w: %s contains a reserved character", ErrInvalidIdentity, name)
	}
	return nil
}

// isUnsafeSegmentRune reports whether r may not appear inside a key segment:
// the separator, whitespace, and every control character.
func isUnsafeSegmentRune(r rune) bool {
	return r == ':' || r <= ' ' || r == 0x7f
}

// SuppressedReasonMaxLen bounds a suppression reason.
//
// The reason is operational shorthand — "quiet_hours", "conversation_muted",
// "already_read" — recorded so an operator can answer "why did nobody get
// this?" months later. It is deliberately not an enum: the reasons belong to
// the policy engine, which does not exist yet, and inventing a vocabulary now
// would give it one to migrate away from.
//
// It is equally deliberately not unbounded. This is a column every suppressed
// notification writes and nothing ever truncates, so without a bound it is a
// place a caller can park arbitrary text — a rendered message, a stack trace,
// the payload the outbox is specifically not supposed to carry. 200 is the same
// bound the dedupe key column uses: comfortably more than any reason phrase
// needs and far too little to smuggle content through.
//
// chat.notification_outbox mirrors this in notification_outbox_suppressed_reason_check.
const SuppressedReasonMaxLen = 200

// ValidateSuppressedReason enforces the contract between a state and its
// reason: exactly the suppressed state carries one, and it fits.
//
// Both halves matter. A suppression with no reason is a row nobody can
// interpret later, and a reason attached to a delivered or failed notification
// claims something that did not happen — which is precisely the confusion
// between "nobody was told, on purpose" and "we tried and could not" that the
// three terminal states exist to prevent.
func ValidateSuppressedReason(state State, reason string) error {
	if (state == StateSuppressed) != (reason != "") {
		return fmt.Errorf("%w: %q carries reason %q", ErrInvalidSuppressedReason, state, reason)
	}
	if len(reason) > SuppressedReasonMaxLen {
		return fmt.Errorf("%w: longer than %d bytes", ErrInvalidSuppressedReason, SuppressedReasonMaxLen)
	}
	return nil
}

// MessageDedupeKeySQL is the SQL expression that builds the dedupe key of a
// message-sourced event, for producers that write their rows set-based and
// therefore never see an individual row in Go.
//
// It exists so the format has one definition. chat-service produces
// notifications from two statements — creating a message, and promoting one
// that was withheld for a link scan — and a format that drifted between them
// would silently stop deduplicating for one of them. Both call this, and it is
// built from the same constants Identity.DedupeKey uses, so the two cannot
// disagree by construction; TestNotificationOutboxCommitsWithMessagePostgreSQL
// checks the result against the Go builder against a real database.
//
// The arguments are SQL expressions naming columns — "inserted.id", "r.kind" —
// supplied by the calling statement, never by a request. Nothing user-supplied
// is concatenated here, exactly as in channelmembership.EligibleTargetsCTE.
func MessageDedupeKeySQL(messageIDExpr, eventTypeExpr string) string {
	return "'" + string(SourceTypeMessage) + ":' || " + messageIDExpr + "::text || ':' || " + eventTypeExpr
}
