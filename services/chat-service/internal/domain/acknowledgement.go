package domain

import (
	"fmt"
	"time"
)

// AcknowledgementState is one eligible recipient's answer to a message that
// asked for explicit confirmation (issue #824, parent #820).
//
// It is per recipient and not per message: a group message has one row in
// chat.messages and one of these per person it was sent to, because "did this
// person confirm" is the only question the feature is about.
//
// It is also a different axis from everything the conversation already tracks.
// #820 states the separation as DELIVERED != READ != ACKNOWLEDGED, and nothing
// in this package reads chat.conversation_read_state or is written when
// somebody opens a conversation. Opening a message is not answering it.
type AcknowledgementState string

const (
	// AcknowledgementStatePending is the only unresolved state: the recipient
	// was asked and has not answered. Everything else is terminal.
	AcknowledgementStatePending AcknowledgementState = "pending"
	// AcknowledgementStateAcknowledged is the recipient confirming deliberately
	// — the explicit act the whole feature exists for.
	AcknowledgementStateAcknowledged AcknowledgementState = "acknowledged"
	// AcknowledgementStateResponded is the recipient replying to the message
	// instead of confirming it. #820 decides that an answer ends the request:
	// somebody who wrote back has done more than press a button, and continuing
	// to ask them would be the reminder loop that issue exists to stop.
	AcknowledgementStateResponded AcknowledgementState = "responded"
	// AcknowledgementStateExpired is a request that stopped being asked without
	// ever being answered.
	//
	// Nothing in this issue produces it. The deadline is part of the persistent
	// reminder lifecycle, which issue #825 owns, and inventing one here would be
	// making a product decision #820 does not state. It is declared — and
	// accepted by the database — so that worker can be written without widening
	// a CHECK constraint on a live table.
	AcknowledgementStateExpired AcknowledgementState = "expired"
	// AcknowledgementStateCancelled is a request withdrawn rather than answered:
	// today, the sender deleting the message. It is deliberately not a second
	// spelling of "expired" — one says nobody answered in time, the other says
	// the question was taken back, and a sender reading a summary is owed the
	// difference.
	AcknowledgementStateCancelled AcknowledgementState = "cancelled"
)

// acknowledgementStates is the closed vocabulary, stated once. The database
// repeats it as a CHECK constraint; that is defence in depth, not the primary
// control.
var acknowledgementStates = map[AcknowledgementState]struct{}{
	AcknowledgementStatePending:      {},
	AcknowledgementStateAcknowledged: {},
	AcknowledgementStateResponded:    {},
	AcknowledgementStateExpired:      {},
	AcknowledgementStateCancelled:    {},
}

// Valid reports whether s is one of the declared states. The empty value is not
// one of them: it is how a projection says "this viewer is not a recipient of
// this message", which is a different answer from any state a recipient can be
// in.
func (s AcknowledgementState) Valid() bool {
	_, ok := acknowledgementStates[s]
	return ok
}

// Resolved reports whether s has stopped being a question.
//
// Stated as "not pending" rather than as a list of the four terminal values, so
// a state added later is resolved by default. The unsafe direction is the other
// one: a new value silently treated as still-pending would keep a reminder loop
// running against it.
func (s AcknowledgementState) Resolved() bool {
	return s.Valid() && s != AcknowledgementStatePending
}

// CanResolveAcknowledgement reports whether a recipient in state `from` may be
// moved to state `to`.
//
// The whole rule is one line, and that is the point: only pending resolves, and
// it resolves into a terminal state. Every forbidden transition #824 enumerates
// — acknowledged -> responded, acknowledged -> cancelled, responded ->
// acknowledged, expired -> acknowledged, cancelled -> acknowledged — is
// forbidden by the same clause, and so is every one it did not think to
// enumerate, including any return to pending.
//
// It is stated here rather than spread across the handler, the store and the
// client because a transition rule that lives in three places is a transition
// rule with three answers. The storage layer enforces it again as the WHERE of
// a single conditional UPDATE, which is what makes it hold under concurrency
// rather than only under review.
func CanResolveAcknowledgement(from, to AcknowledgementState) bool {
	return from == AcknowledgementStatePending && to.Resolved()
}

// MaxAcknowledgementRecipients bounds how many recipients one message may ask.
//
// A message that asks for acknowledgement writes one row per eligible
// recipient, in the statement that sends it. In a DM or a group that set is the
// conversation's membership and is small by construction; in a channel it is
// the channel's member list, which nothing else in this service fans out to per
// person — issue #741 deliberately declined to, and called the amplification a
// thing to bound before building. This is that bound.
//
// It is also what keeps the sender's detail view finite without a pagination
// contract: the endpoint can never return more rows than a send was allowed to
// create, so "every recipient of this message" is a bounded answer by
// construction rather than by a LIMIT somebody has to remember.
//
// 200 rather than MaxGroupAllMentionRecipients' 50: that bound exists to stop
// one person notifying a group, and 50 is a group. This one has to cover a real
// team channel, where asking 120 people to confirm an incident notice is the
// use case rather than the abuse.
const MaxAcknowledgementRecipients = 200

// ErrAcknowledgementRecipientsExceeded reports a send whose acknowledgement
// would ask more than MaxAcknowledgementRecipients people.
//
// Wraps ErrInvalidInput so the existing generic 400 mapping applies with no new
// HTTP case, exactly as ErrGroupAllMentionRecipientsExceeded does. The message
// states only the public bound, never the conversation's actual size or who was
// counted: a caller learns "too many", not "who".
var ErrAcknowledgementRecipientsExceeded = fmt.Errorf(
	"%w: acknowledgement is limited to %d recipients", ErrInvalidInput, MaxAcknowledgementRecipients)

// AcknowledgementSummary is how one message's acknowledgement stands, counted
// over its recipients.
//
// Every state gets its own count rather than a "confirmed / not confirmed"
// pair, because the four terminal states do not mean the same thing to the
// person who asked: a reply, a withdrawal and a deadline that passed all stop a
// request being pending, and a sender told only "3 of 7 outstanding" cannot
// tell which happened. Total - Pending is the resolved count; Resolved states
// it so no caller has to rediscover the subtraction.
type AcknowledgementSummary struct {
	// Required is the message's own flag. It is carried here because a summary
	// of a message that asked for nothing is a legitimate answer — all counts
	// zero — and a client must be able to tell that from a message whose
	// recipients simply have not answered yet.
	Required bool
	// Total is how many recipients were asked. It is the snapshot taken when the
	// message was sent, not the conversation's membership now.
	Total        int
	Pending      int
	Acknowledged int
	Responded    int
	Expired      int
	Cancelled    int
	// ViewerState is the requesting user's own row, or empty when they are not a
	// recipient of this message — the sender of a group message, or somebody who
	// joined the channel after it was sent. Empty is not a state; see
	// AcknowledgementState.Valid.
	ViewerState AcknowledgementState
	// Recipients is the per-person detail, and is populated only for a viewer
	// the service authorised to see it. Everyone else who can read the message
	// gets the counts above and their own ViewerState and nothing more: who
	// personally has not answered is the sender's information, not the
	// conversation's.
	Recipients []AcknowledgementRecipient
}

// Resolved is how many of the asked recipients are no longer pending.
func (s AcknowledgementSummary) Resolved() int {
	return s.Total - s.Pending
}

// AcknowledgementRecipient is one recipient's row as the sender sees it.
//
// It carries an id and a state and no display name, address or avatar. The
// viewer authorised to read this list is the message's sender, who can already
// enumerate the conversation's members through the endpoints that exist for
// that; re-deriving their names here would add a second place personal data
// leaves the service, for no information the caller does not already hold.
type AcknowledgementRecipient struct {
	RecipientID string
	State       AcknowledgementState
	// ResolvedAt is when this recipient stopped being pending, zero while they
	// still are. The schema refuses to hold one without the other.
	ResolvedAt time.Time
}
