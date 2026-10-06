package domain

import (
	"fmt"
	"time"
)

// Presence (issue #798, evolving RF-58).
//
// Four independent facts meet here, and keeping them apart is the point of this
// file:
//
//   - reach: what the user's sessions say — someone is interacting, every
//     session has gone idle, or no session is left. Owned by the realtime
//     layer, which aggregates tabs, devices and replicas.
//   - override: what the user chose — a manual state with a server-side
//     expiry. Owned by chat.user_presence.
//   - activity: what the user is doing that others should know — in a call
//     today, a meeting or a presentation when a source for those exists.
//   - custom status: the free-text message on the profile. It lives in
//     auth-service and is deliberately absent from everything below.
//
// ResolvePresence is the only place they are combined, and it is pure: no
// clock, no store, no socket. Everything that publishes presence asks it. The
// facts it is given were already found in force by the read that returned
// them — on the database's clock — and it does not judge them again on any
// other: whether a timed fact still holds when it is published is the commit
// authority's question (issue #798).

// PresenceReach is what a user's sessions, taken together, say about them.
type PresenceReach string

const (
	// PresenceReachActive is at least one session with recent activity.
	PresenceReachActive PresenceReach = "active"
	// PresenceReachIdle is sessions that exist but have all been idle past the
	// away timeout.
	PresenceReachIdle PresenceReach = "idle"
	// PresenceReachNone is no valid session anywhere. It is the only reach an
	// override cannot turn into presence.
	PresenceReachNone PresenceReach = "none"
)

// Connected reports whether any valid session stands behind this reach.
func (r PresenceReach) Connected() bool {
	return r == PresenceReachActive || r == PresenceReachIdle
}

// PresenceAvailability is the effective state other people see.
type PresenceAvailability string

const (
	PresenceAvailable    PresenceAvailability = "available"
	PresenceBusy         PresenceAvailability = "busy"
	PresenceDoNotDisturb PresenceAvailability = "dnd"
	PresenceBeRightBack  PresenceAvailability = "brb"
	PresenceAway         PresenceAvailability = "away"
	PresenceOffline      PresenceAvailability = "offline"
)

// PresenceManualState is a state the user may choose. It is a closed set and
// every value is checked against it before it reaches storage.
//
// Appear offline is a choice about *display*, not about connectivity: the
// sessions stay connected and keep receiving everything. It is a manual state
// rather than a separate column because the product offers it as one of the
// mutually exclusive choices in the same menu.
type PresenceManualState string

const (
	PresenceManualAvailable     PresenceManualState = "available"
	PresenceManualBusy          PresenceManualState = "busy"
	PresenceManualDoNotDisturb  PresenceManualState = "dnd"
	PresenceManualBeRightBack   PresenceManualState = "brb"
	PresenceManualAway          PresenceManualState = "away"
	PresenceManualAppearOffline PresenceManualState = "appear_offline"
)

var presenceManualStates = map[PresenceManualState]struct{}{
	PresenceManualAvailable:     {},
	PresenceManualBusy:          {},
	PresenceManualDoNotDisturb:  {},
	PresenceManualBeRightBack:   {},
	PresenceManualAway:          {},
	PresenceManualAppearOffline: {},
}

// ParsePresenceManualState accepts only the declared states.
func ParsePresenceManualState(value string) (PresenceManualState, error) {
	state := PresenceManualState(value)
	if _, ok := presenceManualStates[state]; !ok {
		return "", fmt.Errorf("%w: unknown presence state", ErrInvalidInput)
	}
	return state, nil
}

// PresenceActivity is public context about what the user is doing. The zero
// value is "nothing to say".
type PresenceActivity string

const (
	PresenceActivityNone       PresenceActivity = ""
	PresenceActivityInCall     PresenceActivity = "in_call"
	PresenceActivityInMeeting  PresenceActivity = "in_meeting"
	PresenceActivityPresenting PresenceActivity = "presenting"
)

// Bounds on a manual state's expiry. Every manual state expires: a state that
// outlives the reason it was set for is the "forgotten Busy" the issue exists
// to prevent, and a month is longer than any choice the menu offers.
const (
	PresenceOverrideMinDuration = time.Minute
	PresenceOverrideMaxDuration = 31 * 24 * time.Hour
)

// ValidatePresenceExpiry checks a requested expiry against the server's clock.
func ValidatePresenceExpiry(expiresAt, now time.Time) error {
	if expiresAt.Before(now.Add(PresenceOverrideMinDuration)) {
		return fmt.Errorf("%w: presence expiry must be in the future", ErrInvalidInput)
	}
	if expiresAt.After(now.Add(PresenceOverrideMaxDuration)) {
		return fmt.Errorf("%w: presence expiry is too far ahead", ErrInvalidInput)
	}
	return nil
}

// PresenceOverride is one user's manual choice. The zero value is "none".
type PresenceOverride struct {
	State     PresenceManualState
	ExpiresAt time.Time
	UpdatedAt time.Time
}

// PresenceContext is everything about a user that does not come from their
// sessions.
type PresenceContext struct {
	Override PresenceOverride
	Activity PresenceActivity
	// ActivityUntil is when the activity stops holding unless renewed — the
	// latest live call lease behind it. Zero when it is not timed (a direct
	// call lasts until it is ended) or there is no activity. It does not decide
	// whether the activity holds: the read that returned it did, on the
	// database's clock, the same clock a lease renewal is judged on. It bounds
	// how long a composition that used the activity may still be committed.
	ActivityUntil time.Time
}

// EffectivePresence is what may be shown about a user to someone else.
type EffectivePresence struct {
	Availability PresenceAvailability
	Activity     PresenceActivity
}

// manualOutranksActivity holds the manual states that win over an activity.
// Each is an explicit statement by the user about how reachable they are, and a
// call does not overrule it. Manual "available" is absent on purpose: its job is
// to stop the idle timer, not to hide a call.
var manualOutranksActivity = map[PresenceManualState]PresenceAvailability{
	PresenceManualDoNotDisturb: PresenceDoNotDisturb,
	PresenceManualBusy:         PresenceBusy,
	PresenceManualBeRightBack:  PresenceBeRightBack,
	PresenceManualAway:         PresenceAway,
}

// ResolvePresence combines reach, override and activity, in this precedence:
//
//  1. no valid session                → offline (an override cannot invent one)
//  2. appear offline                  → offline, with no activity
//  3. dnd, busy, brb, away (manual)   → that state
//  4. an activity (a call)            → busy — and never away
//  5. available (manual)              → available, even when idle
//  6. an active session               → available
//  7. otherwise (every session idle)  → away
//
// Activity is public only where it explains the state (busy, dnd), so a choice
// such as "Volto já" is not undermined by announcing a call behind it.
func ResolvePresence(reach PresenceReach, ctx PresenceContext) EffectivePresence {
	manual := ctx.Override.State
	if !reach.Connected() || manual == PresenceManualAppearOffline {
		return EffectivePresence{Availability: PresenceOffline}
	}
	availability := resolveAvailability(reach, manual, ctx.Activity)
	return EffectivePresence{Availability: availability, Activity: publicActivity(availability, ctx.Activity)}
}

func resolveAvailability(
	reach PresenceReach, manual PresenceManualState, activity PresenceActivity,
) PresenceAvailability {
	if state, ok := manualOutranksActivity[manual]; ok {
		return state
	}
	if activity != PresenceActivityNone {
		return PresenceBusy
	}
	if manual == PresenceManualAvailable || reach == PresenceReachActive {
		return PresenceAvailable
	}
	return PresenceAway
}

func publicActivity(availability PresenceAvailability, activity PresenceActivity) PresenceActivity {
	if availability == PresenceBusy || availability == PresenceDoNotDisturb {
		return activity
	}
	return PresenceActivityNone
}
