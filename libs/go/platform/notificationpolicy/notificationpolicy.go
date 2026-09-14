// Package notificationpolicy decides which alert channels one notification
// event may use for one recipient, and records why (issue #744, parent #678).
//
// # One authority, and why it is here
//
// The requirement the parent issue makes is not "add a rule" but "stop having
// the rules in three places". Today a chime is decided in React
// (apps/web/src/chat/soundRules.ts), a suppression reason is a free string the
// outbox will accept from anybody, and nothing at all decides Web Push. Each of
// those is a place the answer can drift.
//
// So the answer is computed once, here, and the two services that will ask for
// it import the same package: notification-service, whose worker already has
// the seam for it (worker.Evaluator), and chat-service, which owns the live
// connection an in-app alert travels over. A package under
// services/notification-service/internal could not be imported by chat-service
// at all — Go forbids it — so the second consumer would have had no option but
// to restate the rules, which is the exact failure this issue exists to end.
// notificationevent and workschedule, the two contracts this one is built from,
// live in libs/go/platform for the same reason and are named in their own doc
// comments as this engine's inputs.
//
// # What it decides, and what it must not touch
//
// Three alert channels, and nothing else:
//
//	in_app    the interruptive in-app surface — the toast
//	sound     the local chime
//	web_push  the OS-level push notification
//
// Unread counts, read state, badges and the sidebar are not channels and are
// not governed here. They are properties of the message, and a policy that
// silences an alert must never also hide the message: that is the difference
// between "nobody was interrupted, on purpose" and "nobody was told".
//
// # Purity
//
// Evaluate is a pure function of its argument. It reads no clock, no database,
// no Valkey, no browser and no provider; it resolves no timezone and computes
// no calendar. Every fact it needs — the work-schedule state, the presence, the
// preferences, whether the coordinator has already seen this event — arrives
// resolved, and resolving them is the caller's job. That is what makes the
// whole matrix testable without a fixture and cheap enough to run once per
// recipient of a large fan-out.
//
// The timezone in particular is deliberately absent. It is the authority
// workschedule.Schedule.Evaluate already applied to reach a State; carrying the
// zone name past that point would invite a second, disagreeing calendar.
//
// # Deny-only, which is what makes precedence hold
//
// A decision starts from the channels the recipient's presence could possibly
// use and every rule may only remove channels from it. Nothing re-allows. That
// is a structural guarantee rather than an ordering convention: a personal
// preference cannot re-enable a channel the corporate work-schedule rule
// removed, because no code path exists that would let it. See evaluate.go for
// the rule order and what it does and does not affect.
package notificationpolicy

import (
	"strings"

	"github.com/nicrepository/nchat/libs/go/platform/notificationevent"
	"github.com/nicrepository/nchat/libs/go/platform/workschedule"
)

// Version identifies the rule set that produced a decision.
//
// It is a constant of this package and never an input: a caller that could
// choose the policy version could choose a lenient one, and an audit record
// naming a version the caller supplied proves nothing about what actually ran.
// It changes when the outcome of an unchanged Context could change.
const Version = 1

// ConversationKind is the kind of conversation the event happened in.
//
// Three and not two because the product has three, and folding groups into
// channels would put a five-person group conversation under the rules written
// for a thousand-member channel. Where a rule treats two of them alike it says
// so.
type ConversationKind string

const (
	// ConversationDirect is a 1:1 conversation.
	ConversationDirect ConversationKind = "direct"
	// ConversationGroup is a multi-party conversation that is not a channel.
	ConversationGroup ConversationKind = "group"
	// ConversationChannel is a channel.
	ConversationChannel ConversationKind = "channel"
)

var conversationKinds = map[ConversationKind]struct{}{
	ConversationDirect:  {},
	ConversationGroup:   {},
	ConversationChannel: {},
}

// Valid reports whether k is one of the declared kinds. The zero value is never
// valid, which is the point: an unset kind must not read as a usable one.
func (k ConversationKind) Valid() bool {
	_, ok := conversationKinds[k]
	return ok
}

// isDM reports whether the conversation is addressed to its members rather than
// to a room they subscribed to.
//
// A group is a DM here, and that is not a shortcut: chat.dm_conversations holds
// both, the web subscribes to both under the same "dm" target, and the sound
// preference the user actually set was worded against that same distinction.
// Splitting them at this one place would make the engine disagree with the
// preference it is reading.
func (k ConversationKind) isDM() bool {
	return k == ConversationDirect || k == ConversationGroup
}

// Presence is where the recipient is, as resolved by the caller from a
// server-side session — never from a value a client asserted about itself.
//
// It is not a channel and does not grant one. It says which surfaces could
// exist at all: a toast needs a page in front of somebody, and an OS push is
// the channel for when there is not one.
type Presence string

const (
	// PresenceUnknown is the absence of an answer, and it is the zero value so
	// that a caller who cannot resolve presence says nothing rather than
	// guessing. It is not a declared presence — Valid rejects it — and it grants
	// no surface that assumes somebody is looking. The notification worker uses
	// it: an outbox row carries no session, and no session registry reaches that
	// far.
	PresenceUnknown Presence = ""
	// PresenceForeground is an open, focused client.
	PresenceForeground Presence = "foreground"
	// PresenceConnected is a live client whose focus is not known here.
	//
	// It exists because the two facts a session gives you are separable and the
	// other states conflate them. A realtime publisher knows there is an open
	// client — the event is travelling down its socket — and knows nothing at
	// all about whether that client is in front of the reader. Reporting that as
	// Foreground claims a focus nobody observed; reporting it as Unknown throws
	// away the connection that is demonstrably there and withdraws the two
	// surfaces only a live client can execute.
	//
	// So it admits the same surfaces as Foreground, and differs in what it does
	// *not* license: a rule that turns on the reader actually looking at
	// something — see denyConversationOpen — does not fire for it, because "is
	// this conversation in front of them" has no answer here. The client applies
	// that locally, and may only remove a surface by doing so.
	PresenceConnected Presence = "connected"
	// PresenceBackground is an open client that is not in front — another tab,
	// another window, a minimised browser.
	PresenceBackground Presence = "background"
	// PresenceOffline is no client at all.
	PresenceOffline Presence = "offline"
)

var presences = map[Presence]struct{}{
	PresenceForeground: {},
	PresenceConnected:  {},
	PresenceBackground: {},
	PresenceOffline:    {},
}

// Valid reports whether p is one of the declared states.
func (p Presence) Valid() bool {
	_, ok := presences[p]
	return ok
}

// SoundMode is the recipient's global chime preference.
//
// The four values are the ones the product already has
// (apps/web/src/chat/soundPreference.ts). They are restated here rather than
// invented, so that moving the decision out of React does not also change what
// the user's existing choice means.
type SoundMode string

const (
	// SoundModeAll chimes for anything that reaches the sound channel.
	SoundModeAll SoundMode = "all"
	// SoundModeOff never chimes.
	SoundModeOff SoundMode = "off"
	// SoundModeMentions chimes only when the recipient was named.
	SoundModeMentions SoundMode = "mentions"
	// SoundModeMentionsAndDMs chimes when the recipient was named and for any
	// direct or group conversation. The difference from SoundModeMentions is
	// exactly the plain DM.
	SoundModeMentionsAndDMs SoundMode = "mentions_and_dms"
)

var soundModes = map[SoundMode]struct{}{
	SoundModeAll:            {},
	SoundModeOff:            {},
	SoundModeMentions:       {},
	SoundModeMentionsAndDMs: {},
}

// Valid reports whether m is one of the declared modes.
func (m SoundMode) Valid() bool {
	_, ok := soundModes[m]
	return ok
}

// Effective normalises a preference into a value safe to decide with.
//
// The zero value is the common case and not an error: no preferences endpoint
// exists yet, so most contexts carry no expressed choice, and the answer for
// them is the product's own default. A value this build does not recognise gets
// the same answer, exactly like antispampolicy.Effective — a chime is a
// preference and not a permission, so an unreadable one falls back to the
// default rather than silencing the product. Nothing that guards access
// normalises this way; see denyOutsideWorkHours for the other direction.
func (m SoundMode) Effective() SoundMode {
	if m.Valid() {
		return m
	}
	return SoundModeAll
}

// PreferenceStatus says whether a recipient's own preferences were readable.
//
// Two states, and the distinction between them is the whole point:
//
//	resolved     the preferences were read. What they say may still be
//	             "nothing" — no row is how this product records "not muted" —
//	             and that is a legitimate answer the defaults apply to.
//	unavailable  the read did not happen or did not succeed, so nothing is
//	             known about what this recipient wants.
//
// Absence of a preference and inability to read one are not the same fact, and
// a boolean cannot hold both. Treating the second as the first is what lets a
// database blip alert somebody who had asked for silence.
//
// The zero value is "resolved" on purpose: every caller that fills these fields
// in has, by construction, read them, and a caller with no preferences to offer
// at all is describing a recipient who expressed none. Only a caller that tried
// and failed says so, explicitly.
type PreferenceStatus string

const (
	// PreferenceStatusResolved is the zero value: these preferences are what the
	// recipient actually has.
	PreferenceStatusResolved PreferenceStatus = ""
	// PreferenceStatusUnavailable is a read that could not be completed. See
	// denyUnresolvedPreferences for what the engine does with it.
	PreferenceStatusUnavailable PreferenceStatus = "unavailable"
)

// Preferences is what the recipient asked for.
//
// Every field is written so that its zero value is the absence of a
// preference, which is what chat.conversation_notification_prefs already
// encodes: there is no row for "not muted" and none for "notifications on". A
// caller that could not read the preferences at all therefore passes the zero
// value and gets the product default, not silence and not a lie.
//
// The engine reads these fields and asserts nothing about where they came from.
// Whoever fills them in owes the checks: a conversation preference belongs to
// exactly one (workspace, recipient, conversation) and must be read through a
// path that has already established membership — chat-service's
// NotificationPrefStore.ListMuted is such a path, and its visibility predicates
// are the reason it is one.
type Preferences struct {
	// Status says whether these fields could be read at all.
	//
	// It exists because the zero value of the fields below is a real answer —
	// "this recipient expressed nothing", which the preference table encodes as
	// the absence of a row — and a caller whose *read failed* has a different
	// thing to say. Without somewhere to say it, a failed read is indistinguishable
	// from a recipient with no preferences, and the engine would authorise alerts
	// on facts nobody established. See PreferenceStatus.
	Status PreferenceStatus
	// Disabled is the recipient's global off switch: no alert on any channel.
	Disabled bool
	// Muted is this recipient's own preference for this one conversation. It
	// silences alerts and nothing else — a muted conversation still counts
	// unread, which this engine does not touch.
	Muted bool
	// SoundMode is the global chime preference. See Effective for the unset and
	// unrecognised cases.
	SoundMode SoundMode
}

// Context is one event, one recipient, and everything already resolved about
// them. It is built per recipient by the coordinator and consumed once.
//
// It holds facts, never handles: no store, no clock, no connection, no
// subscription and no provider detail. If a field cannot be filled in from a
// server-side authority it must be left at its zero value rather than guessed,
// because every zero value here is a documented state.
type Context struct {
	// EventID identifies the notification event this decision is about. It is
	// echoed into Decision so a persisted or logged decision names its subject;
	// no rule reads it.
	EventID string
	// WorkspaceID and RecipientID place the decision in a tenant. They are
	// carried for the same audit reason and are equally never read by a rule —
	// which is deliberate: an engine that branched on identity would be a place
	// to hide a per-tenant exception. They must come from server-side context;
	// a workspace or a recipient asserted by a client is not an authority.
	WorkspaceID string
	RecipientID string

	// EventType is what happened, in the vocabulary of the outbox row.
	EventType notificationevent.EventType
	// Priority is the producer's classification, carried for the decision
	// record. No rule reads it: the one rule that would — an urgent bypass of
	// working hours — is explicitly out of scope for #744, whose acceptance
	// criteria say working hours never enable push, sound or an invasive toast.
	Priority notificationevent.Priority
	// Origin is where the event came from. See denyHistorical.
	Origin notificationevent.Origin
	// Conversation is the kind of conversation the event happened in.
	Conversation ConversationKind

	// WorkSchedule is the answer workschedule.Schedule.Evaluate already gave
	// for the instant the event occurred, in the recipient's own zone. The
	// schedule, the zone and the calendar stay behind that call.
	WorkSchedule workschedule.State
	// Presence is where the recipient is.
	Presence Presence
	// ConversationOpen means this exact conversation is open *and* visible: the
	// recipient is looking at it right now. It is not "a client is connected"
	// and not "this conversation was the last one opened", and it may only be
	// set from the mechanism the architecture trusts for it.
	//
	// It is only ever read together with PresenceForeground, and that pairing is
	// enforced by the rule rather than by the caller: see denyConversationOpen.
	// A claim about what somebody is looking at is meaningless — and, once
	// stale, harmful — for a recipient who is not in front of a client at all.
	ConversationOpen bool

	// Preferences is what the recipient asked for.
	Preferences Preferences

	// WebPushAvailable is the logical state of the push channel for this
	// recipient: there is a subscription and the channel can be attempted. It
	// is a capability, not a provider handle — no endpoint, no key and no token
	// enters this package. A provider being down makes this false; it never
	// makes an evaluation fail.
	WebPushAvailable bool

	// Duplicate says the coordinator has already alerted this recipient for
	// this logical event. Burst says the recipient is inside a cooldown the
	// coordinator is enforcing.
	//
	// Both are inputs and neither is a capability: this package owns no store,
	// no SETNX, no TTL and no timer, and it must not, because a policy that
	// remembers is a policy that cannot be replayed.
	Duplicate     bool
	BurstCooldown bool
}

// Reason is why a channel was taken away. The set is closed, the values are
// stable, and none of them carries content: they are recorded against
// chat.notification_outbox.suppressed_reason and read by an operator months
// later, so a reason must survive being logged and must never be a place a
// message body could end up.
type Reason string

const (
	// ReasonOutsideWorkHours is the corporate schedule. It is first because it
	// is the one rule no personal preference may undo.
	ReasonOutsideWorkHours Reason = "outside_work_hours"
	// ReasonHistoricalOrImported is an event that did not just happen.
	ReasonHistoricalOrImported Reason = "historical_or_imported"
	// ReasonSilentEventType is an event kind that never alerts — a reaction.
	ReasonSilentEventType Reason = "silent_event_type"
	// ReasonConversationOpen is the recipient already looking at it.
	ReasonConversationOpen Reason = "conversation_open"
	// ReasonPreferencesUnavailable is the recipient's own preferences failing to
	// resolve. It is not "they asked for this" — it is "nobody could find out
	// what they asked for", which is why it is its own code and not muted or
	// user_preference. An operator reading it months later is being told the
	// alert was withheld by a fault, not by a choice.
	ReasonPreferencesUnavailable Reason = "preferences_unavailable"
	// ReasonMuted is this conversation silenced by this recipient.
	ReasonMuted Reason = "muted"
	// ReasonUserPreference is a global preference of the recipient.
	ReasonUserPreference Reason = "user_preference"
	// ReasonUnsupportedChannel is a channel that cannot be attempted at all.
	ReasonUnsupportedChannel Reason = "unsupported_or_unavailable_channel"
	// ReasonDuplicate is an event the coordinator has already alerted for.
	ReasonDuplicate Reason = "duplicate"
	// ReasonBurstCooldown is a cooldown the coordinator is enforcing.
	ReasonBurstCooldown Reason = "burst_cooldown"
)

// Channels is the decision itself: one allow/deny per alert channel. True is
// allow. There is no third value — "maybe" is not something a delivery
// coordinator can act on.
type Channels struct {
	// InApp is the interruptive in-app surface, the toast. It is not the
	// sidebar, the badge or the unread count.
	InApp bool
	// Sound is the local chime.
	Sound bool
	// WebPush is the OS-level push notification.
	WebPush bool
}

// Decision is the delivery plan for one event and one recipient.
//
// Eligibility is derived from the channels rather than stored beside them, so
// the contradictory row — suppressed while a channel says allow — has no
// representation at all.
type Decision struct {
	// EventID is Context.EventID, so a decision that has been logged or handed
	// to another goroutine still names what it is about.
	EventID string
	// PolicyVersion is the Version constant that produced this decision.
	PolicyVersion int
	// Channels is the per-channel plan.
	Channels Channels
	// Reasons are the rules that actually changed the outcome, in the fixed
	// order the rules are declared in — never the order they happened to run,
	// because they always run in that same order. A rule that would have taken
	// away only channels something earlier had already taken away is not
	// listed: the list is what this decision rests on, not everything that
	// might have applied.
	//
	// Empty exactly when nothing was denied.
	Reasons []Reason
}

// Eligible reports whether any channel may be used.
func (d Decision) Eligible() bool {
	return d.Channels.InApp || d.Channels.Sound || d.Channels.WebPush
}

// Suppressed reports whether no channel may be used. It is the state
// notificationevent.StateSuppressed records, and it is a successful outcome:
// nobody was interrupted, on purpose.
func (d Decision) Suppressed() bool {
	return !d.Eligible()
}

// SuppressedReason renders the reasons for chat.notification_outbox.
//
// Empty exactly when the decision is eligible, and never empty when it is not,
// which is the contract notificationevent.ValidateSuppressedReason enforces —
// a suppression with no reason is a row nobody can interpret later. The whole
// closed set joined together is well inside SuppressedReasonMaxLen, so this
// never truncates and never needs to.
func (d Decision) SuppressedReason() string {
	if d.Eligible() {
		return ""
	}
	parts := make([]string, len(d.Reasons))
	for i, reason := range d.Reasons {
		parts[i] = string(reason)
	}
	return strings.Join(parts, ",")
}
