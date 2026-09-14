package notificationpolicy_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/nicrepository/nchat/libs/go/platform/notificationevent"
	"github.com/nicrepository/nchat/libs/go/platform/notificationpolicy"
	"github.com/nicrepository/nchat/libs/go/platform/workschedule"
)

func TestConversationKindValid(t *testing.T) {
	valid := []notificationpolicy.ConversationKind{
		notificationpolicy.ConversationDirect,
		notificationpolicy.ConversationGroup,
		notificationpolicy.ConversationChannel,
	}
	for _, kind := range valid {
		if !kind.Valid() {
			t.Fatalf("%q should be a declared conversation kind", kind)
		}
	}
	for _, kind := range []notificationpolicy.ConversationKind{"", "thread", "DIRECT"} {
		if kind.Valid() {
			t.Fatalf("%q should not be a conversation kind", kind)
		}
	}
}

func TestPresenceValid(t *testing.T) {
	valid := []notificationpolicy.Presence{
		notificationpolicy.PresenceForeground,
		notificationpolicy.PresenceBackground,
		notificationpolicy.PresenceOffline,
	}
	for _, presence := range valid {
		if !presence.Valid() {
			t.Fatalf("%q should be a declared presence", presence)
		}
	}
	for _, presence := range []notificationpolicy.Presence{"", "hidden", "FOREGROUND"} {
		if presence.Valid() {
			t.Fatalf("%q should not be a presence", presence)
		}
	}
}

func TestSoundModeValidAndEffective(t *testing.T) {
	declared := []notificationpolicy.SoundMode{
		notificationpolicy.SoundModeAll,
		notificationpolicy.SoundModeOff,
		notificationpolicy.SoundModeMentions,
		notificationpolicy.SoundModeMentionsAndDMs,
	}
	for _, mode := range declared {
		if !mode.Valid() {
			t.Fatalf("%q should be a declared sound mode", mode)
		}
		if got := mode.Effective(); got != mode {
			t.Fatalf("Effective(%q) = %q, want it unchanged", mode, got)
		}
	}
	for _, mode := range []notificationpolicy.SoundMode{"", "loud", "OFF"} {
		if mode.Valid() {
			t.Fatalf("%q should not be a sound mode", mode)
		}
		if got := mode.Effective(); got != notificationpolicy.SoundModeAll {
			t.Fatalf("Effective(%q) = %q, want the product default", mode, got)
		}
	}
}

func TestDecisionEligibility(t *testing.T) {
	cases := []struct {
		name         string
		channels     notificationpolicy.Channels
		wantEligible bool
	}{
		{"nothing allowed is suppressed", notificationpolicy.Channels{}, false},
		{"a toast alone is eligible", notificationpolicy.Channels{InApp: true}, true},
		{"a chime alone is eligible", notificationpolicy.Channels{Sound: true}, true},
		{"a push alone is eligible", notificationpolicy.Channels{WebPush: true}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			decision := notificationpolicy.Decision{Channels: tc.channels}
			if decision.Eligible() != tc.wantEligible {
				t.Fatalf("Eligible() = %v, want %v", decision.Eligible(), tc.wantEligible)
			}
			if decision.Suppressed() == tc.wantEligible {
				t.Fatalf("Suppressed() = %v, want %v", decision.Suppressed(), !tc.wantEligible)
			}
		})
	}
}

func TestDecisionSuppressedReason(t *testing.T) {
	eligible := notificationpolicy.Decision{
		Channels: notificationpolicy.Channels{InApp: true},
		Reasons:  []notificationpolicy.Reason{notificationpolicy.ReasonUserPreference},
	}
	if got := eligible.SuppressedReason(); got != "" {
		t.Fatalf("SuppressedReason() = %q, want empty for an eligible decision", got)
	}
	suppressed := notificationpolicy.Decision{Reasons: []notificationpolicy.Reason{
		notificationpolicy.ReasonUserPreference,
		notificationpolicy.ReasonDuplicate,
	}}
	if got := suppressed.SuppressedReason(); got != "user_preference,duplicate" {
		t.Fatalf("SuppressedReason() = %q, want the reasons joined in order", got)
	}
}

// TestSuppressedReasonFitsTheOutboxColumn checks the one bound the renderer
// relies on rather than enforces: every reason this package can produce, joined
// together, still fits chat.notification_outbox.suppressed_reason.
func TestSuppressedReasonFitsTheOutboxColumn(t *testing.T) {
	worst := notificationpolicy.Decision{Reasons: declaredReasons}
	reason := worst.SuppressedReason()
	if err := notificationevent.ValidateSuppressedReason(notificationevent.StateSuppressed, reason); err != nil {
		t.Fatalf("every reason at once is not persistable: %v", err)
	}
}

// declaredReasons is the public ordering contract of Decision.Reasons, restated
// here so a reordering in the engine has to be a deliberate change to the
// specification and not an accident.
var declaredReasons = []notificationpolicy.Reason{
	notificationpolicy.ReasonOutsideWorkHours,
	notificationpolicy.ReasonHistoricalOrImported,
	notificationpolicy.ReasonSilentEventType,
	notificationpolicy.ReasonConversationOpen,
	notificationpolicy.ReasonMuted,
	notificationpolicy.ReasonUserPreference,
	notificationpolicy.ReasonUnsupportedChannel,
	notificationpolicy.ReasonDuplicate,
	notificationpolicy.ReasonBurstCooldown,
}

// TestReasonsAreDeterministic pins both halves of the contract: the same
// context always produces the same reasons, and when several rules are
// load-bearing at once they come out in the declared precedence order rather
// than in the order they became true.
func TestReasonsAreDeterministic(t *testing.T) {
	c := live()
	c.Preferences.SoundMode = notificationpolicy.SoundModeOff
	c.Duplicate = true

	want := []notificationpolicy.Reason{
		notificationpolicy.ReasonUserPreference,
		notificationpolicy.ReasonDuplicate,
	}
	for range 100 {
		decision := notificationpolicy.Evaluate(c)
		if !slices.Equal(decision.Reasons, want) {
			t.Fatalf("reasons = %v, want %v", decision.Reasons, want)
		}
		if !decision.Suppressed() {
			t.Fatalf("channels = %+v, want nothing allowed", decision.Channels)
		}
	}
}

// TestHigherPrecedenceReasonWins records the reason an operator can act on: an
// event that is both outside working hours and in a muted conversation is
// recorded as the corporate rule, not as the recipient's own preference.
func TestHigherPrecedenceReasonWins(t *testing.T) {
	c := live()
	c.WorkSchedule = workschedule.StateOutsideWorkHours
	c.Preferences.Muted = true
	c.Preferences.Disabled = true
	c.Duplicate = true

	decision := notificationpolicy.Evaluate(c)
	want := []notificationpolicy.Reason{notificationpolicy.ReasonOutsideWorkHours}
	if !slices.Equal(decision.Reasons, want) {
		t.Fatalf("reasons = %v, want only the rule that decided it: %v", decision.Reasons, want)
	}
}

// mutator sets one field of a context, so a whole matrix can be built out of
// named axes instead of eleven nested loops.
type mutator func(*notificationpolicy.Context)

// contexts returns every combination of the given axes, applied to a permissive
// base. It is the whole input space of the engine, coarsened to the values that
// can change an answer.
func contexts(axes ...[]mutator) []notificationpolicy.Context {
	result := []notificationpolicy.Context{live()}
	for _, axis := range axes {
		next := make([]notificationpolicy.Context, 0, len(result)*len(axis))
		for _, base := range result {
			for _, apply := range axis {
				variant := base
				apply(&variant)
				next = append(next, variant)
			}
		}
		result = next
	}
	return result
}

func set[T any](assign func(*notificationpolicy.Context, T), values ...T) []mutator {
	axis := make([]mutator, 0, len(values))
	for _, value := range values {
		axis = append(axis, func(c *notificationpolicy.Context) { assign(c, value) })
	}
	return axis
}

func everyContext() []notificationpolicy.Context {
	return contexts(
		set(func(c *notificationpolicy.Context, v notificationevent.Origin) { c.Origin = v },
			notificationevent.OriginLive, notificationevent.OriginImport, ""),
		set(func(c *notificationpolicy.Context, v notificationevent.EventType) { c.EventType = v },
			notificationevent.EventTypeDirectMessage, notificationevent.EventTypeMention,
			notificationevent.EventTypeChannelMessage, notificationevent.EventTypeReaction),
		set(func(c *notificationpolicy.Context, v notificationpolicy.ConversationKind) { c.Conversation = v },
			notificationpolicy.ConversationDirect, notificationpolicy.ConversationGroup,
			notificationpolicy.ConversationChannel),
		set(func(c *notificationpolicy.Context, v workschedule.State) { c.WorkSchedule = v },
			workschedule.StateWithinWorkHours, workschedule.StateOutsideWorkHours,
			workschedule.StateNotConfigured, ""),
		set(func(c *notificationpolicy.Context, v notificationpolicy.Presence) { c.Presence = v },
			notificationpolicy.PresenceForeground, notificationpolicy.PresenceBackground,
			notificationpolicy.PresenceOffline, ""),
		set(func(c *notificationpolicy.Context, v notificationpolicy.SoundMode) { c.Preferences.SoundMode = v },
			notificationpolicy.SoundModeAll, notificationpolicy.SoundModeOff,
			notificationpolicy.SoundModeMentionsAndDMs),
		set(func(c *notificationpolicy.Context, v bool) { c.Preferences.Muted = v }, false, true),
		set(func(c *notificationpolicy.Context, v bool) { c.Preferences.Disabled = v }, false, true),
		set(func(c *notificationpolicy.Context, v bool) { c.ConversationOpen = v }, false, true),
		set(func(c *notificationpolicy.Context, v bool) { c.WebPushAvailable = v }, false, true),
		set(func(c *notificationpolicy.Context, v bool) { c.Duplicate = v }, false, true),
		set(func(c *notificationpolicy.Context, v bool) { c.BurstCooldown = v }, false, true),
	)
}

// TestDecisionInvariants runs the whole input space through the engine and
// checks the properties the contract rests on, which no table of examples can
// establish on its own.
func TestDecisionInvariants(t *testing.T) {
	for _, c := range everyContext() {
		decision := notificationpolicy.Evaluate(c)
		checkReasonOrder(t, c, decision)
		checkSuppressionIsExplainable(t, c, decision)
	}
}

func checkReasonOrder(t *testing.T, c notificationpolicy.Context, decision notificationpolicy.Decision) {
	t.Helper()
	previous := -1
	for _, reason := range decision.Reasons {
		at := slices.Index(declaredReasons, reason)
		if at < 0 {
			t.Fatalf("%+v: reason %q is not declared", c, reason)
		}
		if at <= previous {
			t.Fatalf("%+v: reasons %v are not in declared order", c, decision.Reasons)
		}
		previous = at
	}
}

// checkSuppressionIsExplainable is the property that makes a suppressed row
// interpretable months later: nothing is ever silenced without a reason the
// outbox will accept, and nothing that was delivered claims one.
func checkSuppressionIsExplainable(t *testing.T, c notificationpolicy.Context, decision notificationpolicy.Decision) {
	t.Helper()
	state := notificationevent.StateEligible
	if decision.Suppressed() {
		state = notificationevent.StateSuppressed
	}
	if err := notificationevent.ValidateSuppressedReason(state, decision.SuppressedReason()); err != nil {
		t.Fatalf("%+v: decision is not persistable: %v", c, err)
	}
	if decision.Suppressed() && len(decision.Reasons) == 0 {
		t.Fatalf("%+v: suppressed with no reason", c)
	}
}

// TestWorkScheduleOverridesEveryPersonalPreference is the acceptance criterion
// stated as a property: whatever the recipient asked for, whatever client they
// have and whatever the coordinator says, an event outside working hours
// reaches no channel at all.
func TestWorkScheduleOverridesEveryPersonalPreference(t *testing.T) {
	for _, c := range everyContext() {
		c.WorkSchedule = workschedule.StateOutsideWorkHours
		decision := notificationpolicy.Evaluate(c)
		if decision.Eligible() {
			t.Fatalf("%+v: channels = %+v, want nothing outside working hours", c, decision.Channels)
		}
		if decision.Reasons[0] != notificationpolicy.ReasonOutsideWorkHours {
			t.Fatalf("%+v: first reason = %q, want the corporate rule", c, decision.Reasons[0])
		}
	}
}

// TestPreferencesOnlyEverRemoveChannels proves the engine has no path that
// re-enables anything: relaxing a preference can never take a channel away, so
// tightening one can never grant it back.
func TestPreferencesOnlyEverRemoveChannels(t *testing.T) {
	for _, c := range everyContext() {
		relaxed := c
		relaxed.Preferences = notificationpolicy.Preferences{SoundMode: notificationpolicy.SoundModeAll}
		if grew := granted(notificationpolicy.Evaluate(c), notificationpolicy.Evaluate(relaxed)); grew != "" {
			t.Fatalf("%+v: %s is denied with no preferences but allowed with them", c, grew)
		}
	}
}

// granted names a channel the stricter decision allows and the more permissive
// one does not, which must never happen.
func granted(strict, permissive notificationpolicy.Decision) string {
	switch {
	case strict.Channels.InApp && !permissive.Channels.InApp:
		return "in_app"
	case strict.Channels.Sound && !permissive.Channels.Sound:
		return "sound"
	case strict.Channels.WebPush && !permissive.Channels.WebPush:
		return "web_push"
	default:
		return ""
	}
}

// TestReasonsCarryNoContent guards the one thing a suppression reason must
// never become: a place a message body, an address or a token could be parked.
func TestReasonsCarryNoContent(t *testing.T) {
	for _, reason := range declaredReasons {
		value := string(reason)
		if value != strings.ToLower(value) || strings.ContainsAny(value, " :@/\\\"") {
			t.Fatalf("reason %q is not a bare operational code", reason)
		}
	}
}
