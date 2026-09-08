package notificationpolicy

import (
	"github.com/nicrepository/nchat/libs/go/platform/notificationevent"
	"github.com/nicrepository/nchat/libs/go/platform/workschedule"
)

// channelSet is a set of alert channels, as bits.
//
// A set rather than three booleans passed around, because every rule answers
// the same question — "which channels does this take away?" — and a set makes
// removing them an intersection instead of nine assignments nothing checks.
type channelSet uint8

const (
	setInApp channelSet = 1 << iota
	setSound
	setWebPush
)

// setAll is every channel, which is what a rule that suppresses the
// notification outright takes away.
const setAll = setInApp | setSound | setWebPush

// Evaluate produces the delivery plan for one event and one recipient.
//
// It is pure, allocation-light and O(1) in the number of rules and recipients:
// a fan-out calls it once per recipient with a context the coordinator already
// built, and nothing here reaches for anything it was not handed.
//
// The shape is the guarantee. The plan starts as the channels the recipient's
// presence could use at all, and every rule may only subtract from it — so the
// outcome does not depend on the order the rules run in, and no rule can undo
// an earlier one. What the order does decide is Reasons, which is why the
// declaration below is written as the precedence it documents.
func Evaluate(c Context) Decision {
	allowed := surface(c.Presence)
	var reasons []Reason
	for _, r := range rules {
		denied := allowed & r.denies(c)
		if denied == 0 {
			continue
		}
		allowed &^= denied
		reasons = append(reasons, r.reason)
	}
	return Decision{
		EventID:       c.EventID,
		PolicyVersion: Version,
		Channels:      channelsFrom(allowed),
		Reasons:       reasons,
	}
}

// surface is the set of channels that can exist at all where the recipient is.
//
// It is not a permission and grants nothing: everything it admits still has to
// survive every rule below. It only refuses to plan for a surface that is not
// there — a toast on a page nobody has open, a chime into a closed browser, an
// OS push racing the live client that is already showing the message.
//
// Foreground and Connected admit the same surfaces: both are a live client, and
// what separates them is only whether the reader's focus was observed, which no
// surface depends on. The difference is spent by denyConversationOpen instead.
//
// A presence this build does not know, including the zero value, is treated as
// no client at all. That is the fail-closed direction: it withholds the two
// channels that assume somebody is looking, and leaves push, whose own
// availability is checked separately, as the channel that exists precisely for
// when nobody is.
func surface(presence Presence) channelSet {
	if presence == PresenceForeground || presence == PresenceConnected {
		return setInApp | setSound
	}
	return setWebPush
}

// channelsFrom renders the surviving set as the decision.
func channelsFrom(allowed channelSet) Channels {
	return Channels{
		InApp:   allowed&setInApp != 0,
		Sound:   allowed&setSound != 0,
		WebPush: allowed&setWebPush != 0,
	}
}

// rule is one named restriction: what it takes away, and what to call it when
// it does.
type rule struct {
	reason Reason
	denies func(Context) channelSet
}

// rules is the whole policy, in precedence order.
//
// Order does not decide the outcome — every rule subtracts, so the result is
// the same set whatever order they run in. It decides which reason is recorded
// when two rules would take away the same channel, and that is a real
// distinction for an operator: an event both outside working hours and in a
// muted conversation is recorded as the corporate rule, because that is the one
// the recipient cannot do anything about.
//
// The grouping is the precedence the issue requires:
//
//	corporate policy      outside working hours
//	event and context     historical origin, silent event type, open conversation
//	personal preference   unreadable preferences, muted conversation, globals
//	channel availability  no usable push subscription
//	coordinator state     already delivered, inside a burst cooldown
//
// Corporate policy first and personal preference after is a statement about
// reasons only. That a preference cannot *reverse* the corporate rule is not
// enforced by this ordering at all — it is enforced by there being no operation
// in this package that grants a channel back.
var rules = []rule{
	{ReasonOutsideWorkHours, denyOutsideWorkHours},
	{ReasonHistoricalOrImported, denyHistorical},
	{ReasonSilentEventType, denySilentEventType},
	{ReasonConversationOpen, denyConversationOpen},
	{ReasonPreferencesUnavailable, denyUnresolvedPreferences},
	{ReasonMuted, denyMuted},
	{ReasonUserPreference, denyUserPreference},
	{ReasonUnsupportedChannel, denyUnavailablePush},
	{ReasonDuplicate, denyDuplicate},
	{ReasonBurstCooldown, denyBurstCooldown},
}

// denyOutsideWorkHours is the corporate rule: outside working hours nothing
// alerts. Push, chime and toast all go, and the message itself is untouched —
// it is waiting when the recipient comes back.
//
// StateNotConfigured does not suppress, and this is the product decision #743
// deliberately left to this engine rather than the default it refused to
// invent. A workspace with no schedule has not said anything about when its
// people work, and there is no writer for a schedule yet: reading silence as
// "outside working hours" would silence every notification in the product on
// the day this shipped.
//
// Anything else — the zero value, a state written by a newer release — is
// treated as outside working hours. That is the opposite direction from
// SoundMode.Effective, and deliberately so: this is the rule an organisation
// imposes, so an unreadable answer must not become permission to interrupt.
func denyOutsideWorkHours(c Context) channelSet {
	switch c.WorkSchedule {
	case workschedule.StateWithinWorkHours, workschedule.StateNotConfigured:
		return 0
	default:
		return setAll
	}
}

// denyHistorical suppresses anything that did not just happen.
//
// Written as "not live" rather than as a list of the historical origins, so an
// origin this build does not know — including the zero value — is suppressed
// rather than announced. An import backfilling a year of messages must not ring
// a thousand phones, and the failure mode of guessing wrong in the other
// direction is exactly that.
func denyHistorical(c Context) channelSet {
	if c.Origin == notificationevent.OriginLive {
		return 0
	}
	return setAll
}

// denySilentEventType suppresses event kinds that never alert.
//
// A reaction is the whole list today. It is genuinely silent and not merely
// quiet: somebody reacting to a message is not a reason to interrupt, on any
// channel, and the recipient sees it the moment they look at the conversation.
func denySilentEventType(c Context) channelSet {
	if c.EventType == notificationevent.EventTypeReaction {
		return setAll
	}
	return 0
}

// denyConversationOpen suppresses everything for a conversation the recipient
// is looking at right now: they have already seen it. Read state is not touched
// — being shown a message is not the same as having read it, and that
// distinction belongs to the conversation, not to an alert policy.
//
// Foreground is required, and it is required here rather than trusted from the
// flag. "Open" is a fact about a client's own UI, so it is reported by that
// client and can be stale by the time it is used: a browser that was minimised,
// a tab closed, a laptop shut. Taking the flag on its own would then let a
// stale "I am looking at it" delete the push of somebody who is not looking at
// anything — the one channel that exists precisely for that case. Requiring the
// presence the claim only makes sense under is what makes the rule fail towards
// delivering rather than towards silence.
func denyConversationOpen(c Context) channelSet {
	if c.ConversationOpen && c.Presence == PresenceForeground {
		return setAll
	}
	return 0
}

// denyUnresolvedPreferences suppresses every alert channel when the recipient's
// own preferences could not be read.
//
// It is fail-closed, and the asymmetry is deliberate: alerting somebody who had
// silenced a conversation is a fault they experience and cannot undo, while
// withholding an alert from somebody who wanted it costs them a notification
// they can still see in the app — the message itself is untouched by this and
// by every other rule here. The cheaper mistake is the quiet one.
//
// It sits ahead of the two rules it stands in for. Neither denyMuted nor
// denyUserPreference can answer for a recipient whose preferences never
// arrived: reading their zero values as "not muted, no global off switch" is
// exactly the assumption that has no evidence behind it.
//
// It is *after* the corporate and event rules on purpose, so a decision that
// was already settled by something knowable — outside working hours, an
// imported event, a reaction — is still recorded under that reason rather than
// under a fault that changed nothing.
func denyUnresolvedPreferences(c Context) channelSet {
	if c.Preferences.Status == PreferenceStatusUnavailable {
		return setAll
	}
	return 0
}

// denyMuted suppresses everything for a conversation this recipient silenced.
// Their unread count is unaffected, which is the entire point of muting rather
// than leaving.
func denyMuted(c Context) channelSet {
	if c.Preferences.Muted {
		return setAll
	}
	return 0
}

// denyUserPreference applies the recipient's global preferences: the off
// switch, and then the chime preference, which is about the sound channel only.
func denyUserPreference(c Context) channelSet {
	if c.Preferences.Disabled {
		return setAll
	}
	if soundWanted(c) {
		return 0
	}
	return setSound
}

// soundWanted reports whether the chime preference covers this event.
//
// It mirrors what apps/web/src/chat/soundRules.ts already does, so that moving
// the decision here does not change what a user's existing choice means.
func soundWanted(c Context) bool {
	switch c.Preferences.SoundMode.Effective() {
	case SoundModeOff:
		return false
	case SoundModeMentions:
		return c.EventType == notificationevent.EventTypeMention
	case SoundModeMentionsAndDMs:
		return c.EventType == notificationevent.EventTypeMention || c.Conversation.isDM()
	default:
		return true
	}
}

// denyUnavailablePush takes away the push channel when there is no usable
// subscription for it.
//
// An unavailable channel is a fact about the channel, never a failed
// evaluation: the decision is still produced, still auditable, and the other
// channels are still decided on their own merits.
func denyUnavailablePush(c Context) channelSet {
	if c.WebPushAvailable {
		return 0
	}
	return setWebPush
}

// denyDuplicate suppresses an event the coordinator has already alerted for.
// Whatever recognised it — an idempotency key, a multi-tab election — is the
// coordinator's business; this rule only applies the answer.
func denyDuplicate(c Context) channelSet {
	if c.Duplicate {
		return setAll
	}
	return 0
}

// denyBurstCooldown suppresses while the coordinator is holding a cooldown
// open. Everything goes, not just the loud channels: a burst is a decision to
// stop interrupting for a while, and a toast is an interruption.
func denyBurstCooldown(c Context) channelSet {
	if c.BurstCooldown {
		return setAll
	}
	return 0
}
