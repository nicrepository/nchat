package notificationpolicy_test

import (
	"slices"
	"testing"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/notificationevent"
	"github.com/nicrepository/nchat/libs/go/platform/notificationpolicy"
	"github.com/nicrepository/nchat/libs/go/platform/workschedule"
)

// live is a context that alerts on everything the recipient's presence allows:
// a live direct message, in working hours, nothing muted, nothing suppressed.
// Every case below states only what it changes about it, so what a case is
// actually testing is the diff.
func live() notificationpolicy.Context {
	return notificationpolicy.Context{
		EventID:          "event-1",
		WorkspaceID:      "workspace-1",
		RecipientID:      "user-1",
		EventType:        notificationevent.EventTypeDirectMessage,
		Priority:         notificationevent.PriorityNormal,
		Origin:           notificationevent.OriginLive,
		Conversation:     notificationpolicy.ConversationDirect,
		WorkSchedule:     workschedule.StateWithinWorkHours,
		Presence:         notificationpolicy.PresenceForeground,
		WebPushAvailable: true,
	}
}

// allow and deny name the two plans a case can expect, so a table row reads as
// a sentence instead of as three booleans.
var (
	nothing    = notificationpolicy.Channels{}
	inAppOnly  = notificationpolicy.Channels{InApp: true}
	foreground = notificationpolicy.Channels{InApp: true, Sound: true}
	pushOnly   = notificationpolicy.Channels{WebPush: true}
)

type policyCase struct {
	name        string
	mutate      func(*notificationpolicy.Context)
	wantChannel notificationpolicy.Channels
	wantReasons []notificationpolicy.Reason
}

func (tc policyCase) context() notificationpolicy.Context {
	c := live()
	if tc.mutate != nil {
		tc.mutate(&c)
	}
	return c
}

func run(t *testing.T, cases []policyCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { tc.check(t) })
	}
}

func (tc policyCase) check(t *testing.T) {
	t.Helper()
	c := tc.context()
	decision := notificationpolicy.Evaluate(c)
	if decision.Channels != tc.wantChannel {
		t.Fatalf("channels = %+v, want %+v", decision.Channels, tc.wantChannel)
	}
	if !slices.Equal(decision.Reasons, tc.wantReasons) {
		t.Fatalf("reasons = %v, want %v", decision.Reasons, tc.wantReasons)
	}
	if decision.PolicyVersion != notificationpolicy.Version {
		t.Fatalf("policy version = %d, want %d", decision.PolicyVersion, notificationpolicy.Version)
	}
	if decision.EventID != c.EventID {
		t.Fatalf("event id = %q, want %q", decision.EventID, c.EventID)
	}
}

func TestEvaluatePresence(t *testing.T) {
	run(t, []policyCase{{
		name:        "foreground in another conversation alerts in app and chimes, never pushes",
		wantChannel: foreground,
	}, {
		name:        "background pushes and does not chime, because there is no UI in front",
		mutate:      func(c *notificationpolicy.Context) { c.Presence = notificationpolicy.PresenceBackground },
		wantChannel: pushOnly,
	}, {
		name:        "offline pushes",
		mutate:      func(c *notificationpolicy.Context) { c.Presence = notificationpolicy.PresenceOffline },
		wantChannel: pushOnly,
	}, {
		name:        "an unknown presence is treated as no client at all",
		mutate:      func(c *notificationpolicy.Context) { c.Presence = notificationpolicy.Presence("wat") },
		wantChannel: pushOnly,
	}, {
		name:        "the zero presence is treated as no client at all",
		mutate:      func(c *notificationpolicy.Context) { c.Presence = "" },
		wantChannel: pushOnly,
	}, {
		name: "background without a usable push subscription alerts nowhere",
		mutate: func(c *notificationpolicy.Context) {
			c.Presence = notificationpolicy.PresenceBackground
			c.WebPushAvailable = false
		},
		wantChannel: nothing,
		wantReasons: []notificationpolicy.Reason{notificationpolicy.ReasonUnsupportedChannel},
	}, {
		name:        "an unavailable push channel is not a reason while in the foreground",
		mutate:      func(c *notificationpolicy.Context) { c.WebPushAvailable = false },
		wantChannel: foreground,
	}})
}

func TestEvaluateConversationOpen(t *testing.T) {
	run(t, []policyCase{{
		name:        "the conversation the recipient is looking at alerts nowhere",
		mutate:      open,
		wantChannel: nothing,
		wantReasons: []notificationpolicy.Reason{notificationpolicy.ReasonConversationOpen},
	}, {
		name: "an open conversation the recipient is not looking at is not open",
		mutate: func(c *notificationpolicy.Context) {
			c.Presence = notificationpolicy.PresenceBackground
		},
		wantChannel: pushOnly,
	}})
}

func open(c *notificationpolicy.Context) { c.ConversationOpen = true }

// TestConversationOpenNeedsForeground is the rule that a stale flag must not
// silence a recipient who is not looking at anything. "Open" is a claim about a
// client's own UI: it is reported by that client, it survives a minimised
// window and a closed laptop, and taking it on its own would delete the push of
// somebody who has no client in front of them at all.
func TestConversationOpenNeedsForeground(t *testing.T) {
	run(t, []policyCase{{
		name:        "foreground and open suppresses every channel",
		mutate:      open,
		wantChannel: nothing,
		wantReasons: []notificationpolicy.Reason{notificationpolicy.ReasonConversationOpen},
	}, {
		name:        "background keeps its push despite an open flag",
		mutate:      background(open),
		wantChannel: pushOnly,
	}, {
		name:        "offline keeps its push despite a stale open flag",
		mutate:      both(open, presence(notificationpolicy.PresenceOffline)),
		wantChannel: pushOnly,
	}, {
		name:        "an unknown presence keeps its push and gains no in-app surface",
		mutate:      both(open, presence(notificationpolicy.PresenceUnknown)),
		wantChannel: pushOnly,
	}, {
		name:        "a presence this build does not know behaves the same",
		mutate:      both(open, presence("wat")),
		wantChannel: pushOnly,
	}})
}

// TestConversationOpenDoesNotReviveDeniedChannels checks the other direction of
// the same change: dropping the rule in the background must not hand back a
// channel some other rule took away.
func TestConversationOpenDoesNotReviveDeniedChannels(t *testing.T) {
	run(t, []policyCase{{
		name:        "outside working hours still silences an open background conversation",
		mutate:      background(both(open, schedule(workschedule.StateOutsideWorkHours))),
		wantChannel: nothing,
		wantReasons: []notificationpolicy.Reason{notificationpolicy.ReasonOutsideWorkHours},
	}, {
		name:        "a muted conversation still silences it",
		mutate:      background(both(open, func(c *notificationpolicy.Context) { c.Preferences.Muted = true })),
		wantChannel: nothing,
		wantReasons: []notificationpolicy.Reason{notificationpolicy.ReasonMuted},
	}, {
		name: "an imported event still silences it",
		mutate: background(both(open, func(c *notificationpolicy.Context) {
			c.Origin = notificationevent.OriginImport
		})),
		wantChannel: nothing,
		wantReasons: []notificationpolicy.Reason{notificationpolicy.ReasonHistoricalOrImported},
	}, {
		name: "an unavailable push channel still silences it",
		mutate: background(both(open, func(c *notificationpolicy.Context) {
			c.WebPushAvailable = false
		})),
		wantChannel: nothing,
		wantReasons: []notificationpolicy.Reason{notificationpolicy.ReasonUnsupportedChannel},
	}, {
		name:        "and the global off switch still silences it",
		mutate:      background(both(open, func(c *notificationpolicy.Context) { c.Preferences.Disabled = true })),
		wantChannel: nothing,
		wantReasons: []notificationpolicy.Reason{notificationpolicy.ReasonUserPreference},
	}})
}

func presence(p notificationpolicy.Presence) func(*notificationpolicy.Context) {
	return func(c *notificationpolicy.Context) { c.Presence = p }
}

func schedule(state workschedule.State) func(*notificationpolicy.Context) {
	return func(c *notificationpolicy.Context) { c.WorkSchedule = state }
}

func TestEvaluateEventTypes(t *testing.T) {
	run(t, []policyCase{{
		name:        "a direct message alerts",
		wantChannel: foreground,
	}, {
		name:        "a mention alerts",
		mutate:      mention,
		wantChannel: foreground,
	}, {
		name: "a reply alerts",
		mutate: func(c *notificationpolicy.Context) {
			c.EventType = notificationevent.EventTypeReply
			c.Conversation = notificationpolicy.ConversationChannel
		},
		wantChannel: foreground,
	}, {
		name: "a plain channel message alerts",
		mutate: func(c *notificationpolicy.Context) {
			c.EventType = notificationevent.EventTypeChannelMessage
			c.Conversation = notificationpolicy.ConversationChannel
		},
		wantChannel: foreground,
	}, {
		name: "a group message alerts",
		mutate: func(c *notificationpolicy.Context) {
			c.Conversation = notificationpolicy.ConversationGroup
		},
		wantChannel: foreground,
	}, {
		name:        "a reaction is silent on every channel",
		mutate:      func(c *notificationpolicy.Context) { c.EventType = notificationevent.EventTypeReaction },
		wantChannel: nothing,
		wantReasons: []notificationpolicy.Reason{notificationpolicy.ReasonSilentEventType},
	}, {
		name: "a reaction is silent in the background too",
		mutate: func(c *notificationpolicy.Context) {
			c.EventType = notificationevent.EventTypeReaction
			c.Presence = notificationpolicy.PresenceBackground
		},
		wantChannel: nothing,
		wantReasons: []notificationpolicy.Reason{notificationpolicy.ReasonSilentEventType},
	}, {
		name:        "a call alerts inside working hours",
		mutate:      func(c *notificationpolicy.Context) { c.EventType = notificationevent.EventTypeCall },
		wantChannel: foreground,
	}, {
		name: "a call outside working hours does not",
		mutate: func(c *notificationpolicy.Context) {
			c.EventType = notificationevent.EventTypeCall
			c.WorkSchedule = workschedule.StateOutsideWorkHours
		},
		wantChannel: nothing,
		wantReasons: []notificationpolicy.Reason{notificationpolicy.ReasonOutsideWorkHours},
	}, {
		name:        "an event type this build does not know still alerts on its own merits",
		mutate:      func(c *notificationpolicy.Context) { c.EventType = notificationevent.EventType("wat") },
		wantChannel: foreground,
	}})
}

func mention(c *notificationpolicy.Context) {
	c.EventType = notificationevent.EventTypeMention
	c.Conversation = notificationpolicy.ConversationChannel
	c.Priority = notificationevent.PriorityHigh
}

func TestEvaluateOrigin(t *testing.T) {
	run(t, []policyCase{{
		name:        "an imported event alerts nowhere",
		mutate:      func(c *notificationpolicy.Context) { c.Origin = notificationevent.OriginImport },
		wantChannel: nothing,
		wantReasons: []notificationpolicy.Reason{notificationpolicy.ReasonHistoricalOrImported},
	}, {
		name:        "a replayed event alerts nowhere",
		mutate:      func(c *notificationpolicy.Context) { c.Origin = notificationevent.OriginReplay },
		wantChannel: nothing,
		wantReasons: []notificationpolicy.Reason{notificationpolicy.ReasonHistoricalOrImported},
	}, {
		name:        "a resynced event alerts nowhere",
		mutate:      func(c *notificationpolicy.Context) { c.Origin = notificationevent.OriginResync },
		wantChannel: nothing,
		wantReasons: []notificationpolicy.Reason{notificationpolicy.ReasonHistoricalOrImported},
	}, {
		name:        "an origin this build does not know is not treated as live",
		mutate:      func(c *notificationpolicy.Context) { c.Origin = notificationevent.Origin("wat") },
		wantChannel: nothing,
		wantReasons: []notificationpolicy.Reason{notificationpolicy.ReasonHistoricalOrImported},
	}, {
		name:        "the zero origin is not treated as live",
		mutate:      func(c *notificationpolicy.Context) { c.Origin = "" },
		wantChannel: nothing,
		wantReasons: []notificationpolicy.Reason{notificationpolicy.ReasonHistoricalOrImported},
	}})
}

func TestEvaluatePreferences(t *testing.T) {
	run(t, []policyCase{{
		name:        "the global off switch alerts nowhere",
		mutate:      func(c *notificationpolicy.Context) { c.Preferences.Disabled = true },
		wantChannel: nothing,
		wantReasons: []notificationpolicy.Reason{notificationpolicy.ReasonUserPreference},
	}, {
		name: "the global off switch also silences push",
		mutate: func(c *notificationpolicy.Context) {
			c.Preferences.Disabled = true
			c.Presence = notificationpolicy.PresenceBackground
		},
		wantChannel: nothing,
		wantReasons: []notificationpolicy.Reason{notificationpolicy.ReasonUserPreference},
	}, {
		name:        "a muted conversation alerts nowhere",
		mutate:      func(c *notificationpolicy.Context) { c.Preferences.Muted = true },
		wantChannel: nothing,
		wantReasons: []notificationpolicy.Reason{notificationpolicy.ReasonMuted},
	}})
}

func TestEvaluateSoundPreference(t *testing.T) {
	run(t, []policyCase{{
		name:        "no expressed sound preference chimes, which is the product default",
		wantChannel: foreground,
	}, {
		name:        "a sound preference this build does not know falls back to the default",
		mutate:      func(c *notificationpolicy.Context) { c.Preferences.SoundMode = "wat" },
		wantChannel: foreground,
	}, {
		name:        "sound off keeps the toast and drops the chime",
		mutate:      soundMode(notificationpolicy.SoundModeOff),
		wantChannel: inAppOnly,
		wantReasons: []notificationpolicy.Reason{notificationpolicy.ReasonUserPreference},
	}, {
		name:        "sound off does not touch push",
		mutate:      background(soundMode(notificationpolicy.SoundModeOff)),
		wantChannel: pushOnly,
	}, {
		name:        "mentions-only does not chime for a direct message",
		mutate:      soundMode(notificationpolicy.SoundModeMentions),
		wantChannel: inAppOnly,
		wantReasons: []notificationpolicy.Reason{notificationpolicy.ReasonUserPreference},
	}, {
		name:        "mentions-only chimes for a mention",
		mutate:      both(mention, soundMode(notificationpolicy.SoundModeMentions)),
		wantChannel: foreground,
	}, {
		name:        "mentions and DMs chimes for a plain direct message",
		mutate:      soundMode(notificationpolicy.SoundModeMentionsAndDMs),
		wantChannel: foreground,
	}, {
		name: "mentions and DMs chimes for a group message",
		mutate: both(soundMode(notificationpolicy.SoundModeMentionsAndDMs), func(c *notificationpolicy.Context) {
			c.Conversation = notificationpolicy.ConversationGroup
		}),
		wantChannel: foreground,
	}, {
		name: "mentions and DMs does not chime for a plain channel message",
		mutate: both(soundMode(notificationpolicy.SoundModeMentionsAndDMs), func(c *notificationpolicy.Context) {
			c.EventType = notificationevent.EventTypeChannelMessage
			c.Conversation = notificationpolicy.ConversationChannel
		}),
		wantChannel: inAppOnly,
		wantReasons: []notificationpolicy.Reason{notificationpolicy.ReasonUserPreference},
	}, {
		name:        "sound all chimes for a plain channel message",
		mutate:      both(soundMode(notificationpolicy.SoundModeAll), func(c *notificationpolicy.Context) { c.Conversation = notificationpolicy.ConversationChannel }),
		wantChannel: foreground,
	}})
}

func soundMode(mode notificationpolicy.SoundMode) func(*notificationpolicy.Context) {
	return func(c *notificationpolicy.Context) { c.Preferences.SoundMode = mode }
}

func background(mutate func(*notificationpolicy.Context)) func(*notificationpolicy.Context) {
	return both(mutate, func(c *notificationpolicy.Context) { c.Presence = notificationpolicy.PresenceBackground })
}

func both(first, second func(*notificationpolicy.Context)) func(*notificationpolicy.Context) {
	return func(c *notificationpolicy.Context) {
		first(c)
		second(c)
	}
}

func TestEvaluateCoordinatorState(t *testing.T) {
	run(t, []policyCase{{
		name:        "an event the coordinator has already delivered alerts nowhere",
		mutate:      func(c *notificationpolicy.Context) { c.Duplicate = true },
		wantChannel: nothing,
		wantReasons: []notificationpolicy.Reason{notificationpolicy.ReasonDuplicate},
	}, {
		name:        "a burst cooldown alerts nowhere",
		mutate:      func(c *notificationpolicy.Context) { c.BurstCooldown = true },
		wantChannel: nothing,
		wantReasons: []notificationpolicy.Reason{notificationpolicy.ReasonBurstCooldown},
	}, {
		name:        "a burst cooldown silences push too",
		mutate:      background(func(c *notificationpolicy.Context) { c.BurstCooldown = true }),
		wantChannel: nothing,
		wantReasons: []notificationpolicy.Reason{notificationpolicy.ReasonBurstCooldown},
	}})
}

func TestEvaluateWorkSchedule(t *testing.T) {
	run(t, []policyCase{{
		name:        "inside working hours alerts",
		wantChannel: foreground,
	}, {
		name:        "outside working hours alerts nowhere",
		mutate:      func(c *notificationpolicy.Context) { c.WorkSchedule = workschedule.StateOutsideWorkHours },
		wantChannel: nothing,
		wantReasons: []notificationpolicy.Reason{notificationpolicy.ReasonOutsideWorkHours},
	}, {
		name:        "outside working hours silences push as well",
		mutate:      background(func(c *notificationpolicy.Context) { c.WorkSchedule = workschedule.StateOutsideWorkHours }),
		wantChannel: nothing,
		wantReasons: []notificationpolicy.Reason{notificationpolicy.ReasonOutsideWorkHours},
	}, {
		name:        "no configured schedule does not suppress",
		mutate:      func(c *notificationpolicy.Context) { c.WorkSchedule = workschedule.StateNotConfigured },
		wantChannel: foreground,
	}, {
		name:        "a schedule state this build does not know is treated as outside working hours",
		mutate:      func(c *notificationpolicy.Context) { c.WorkSchedule = workschedule.State("wat") },
		wantChannel: nothing,
		wantReasons: []notificationpolicy.Reason{notificationpolicy.ReasonOutsideWorkHours},
	}, {
		name:        "the zero schedule state is treated as outside working hours",
		mutate:      func(c *notificationpolicy.Context) { c.WorkSchedule = "" },
		wantChannel: nothing,
		wantReasons: []notificationpolicy.Reason{notificationpolicy.ReasonOutsideWorkHours},
	}})
}

// officeSchedule is the roster from issue #743: Mon-Thu 07:00-12:00 and
// 13:00-16:00, Fri 07:00-12:00 and 13:00-15:00, in Sao Paulo.
func officeSchedule(t *testing.T, timezone string) workschedule.Schedule {
	t.Helper()
	morning := workschedule.Interval{Start: workschedule.At(7, 0), End: workschedule.At(12, 0)}
	afternoon := workschedule.Interval{Start: workschedule.At(13, 0), End: workschedule.At(16, 0)}
	var days workschedule.Days
	for _, weekday := range []time.Weekday{time.Monday, time.Tuesday, time.Wednesday, time.Thursday} {
		days[weekday] = []workschedule.Interval{morning, afternoon}
	}
	days[time.Friday] = []workschedule.Interval{morning, {Start: workschedule.At(13, 0), End: workschedule.At(15, 0)}}
	schedule, err := workschedule.New(workschedule.SourceOrganization, timezone, days)
	if err != nil {
		t.Fatalf("build schedule: %v", err)
	}
	return schedule
}

// TestEvaluateAgainstRealSchedule composes the two contracts the way a
// coordinator will: workschedule answers the temporal question for the instant
// the event occurred, and that answer — never a clock, never a zone — is what
// reaches the engine.
func TestEvaluateAgainstRealSchedule(t *testing.T) {
	saoPaulo := officeSchedule(t, "America/Sao_Paulo")
	tokyo := officeSchedule(t, "Asia/Tokyo")

	cases := []struct {
		name     string
		schedule workschedule.Schedule
		// Wednesday 2026-09-02, expressed in UTC so the zone under test is the
		// schedule's own and never the test process's.
		instant time.Time
		want    notificationpolicy.Channels
	}{
		{"mid morning is working time", saoPaulo, utc(t, "2026-09-02T13:30:00Z"), foreground},
		{"the lunch gap is outside working hours", saoPaulo, utc(t, "2026-09-02T15:30:00Z"), nothing},
		{"back from lunch is working time", saoPaulo, utc(t, "2026-09-02T16:30:00Z"), foreground},
		{"after the working day", saoPaulo, utc(t, "2026-09-02T20:00:00Z"), nothing},
		{"past local midnight, the next day has not started", saoPaulo, utc(t, "2026-09-03T04:00:00Z"), nothing},
		{"the next working day has started", saoPaulo, utc(t, "2026-09-03T11:00:00Z"), foreground},
		{"Saturday is not a working day", saoPaulo, utc(t, "2026-09-05T13:30:00Z"), nothing},
		{"the same instant is the middle of the night in Tokyo", tokyo, utc(t, "2026-09-02T13:30:00Z"), nothing},
		{"and Tokyo's own mid morning is working time", tokyo, utc(t, "2026-09-02T01:30:00Z"), foreground},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := live()
			c.WorkSchedule = tc.schedule.Evaluate(tc.instant).State
			if got := notificationpolicy.Evaluate(c).Channels; got != tc.want {
				t.Fatalf("channels = %+v, want %+v (schedule state %q)", got, tc.want, c.WorkSchedule)
			}
		})
	}
}

func utc(t *testing.T, value string) time.Time {
	t.Helper()
	instant, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatalf("parse %q: %v", value, err)
	}
	return instant
}

// PresenceConnected is the state a realtime publisher is actually in: it can see
// the socket, it cannot see the reader (issue #744, review round 6).
//
// The distinction it exists to keep is between "this conversation is not in
// front of them" and "whether it is in front of them is not known here". Before
// it, a publisher that knew only the first had to send Foreground with
// ConversationOpen=false — two claims it had no evidence for — and the second of
// them fed a rule.
func TestPresenceConnectedIsALiveClientWithoutAClaimAboutFocus(t *testing.T) {
	base := notificationpolicy.Context{
		EventType:    notificationevent.EventTypeChannelMessage,
		Origin:       notificationevent.OriginLive,
		WorkSchedule: workschedule.StateNotConfigured,
	}

	// It admits exactly the surfaces a live client can execute, like Foreground:
	// a connection is a connection, and no surface depends on focus.
	connected := base
	connected.Presence = notificationpolicy.PresenceConnected
	foreground := base
	foreground.Presence = notificationpolicy.PresenceForeground
	if got, want := notificationpolicy.Evaluate(connected).Channels,
		notificationpolicy.Evaluate(foreground).Channels; got != want {
		t.Fatalf("connected channels = %+v, want the same surfaces as foreground %+v", got, want)
	}

	// ...and it is a declared state, not the zero value's fallback.
	if !notificationpolicy.PresenceConnected.Valid() {
		t.Fatal("PresenceConnected must be a declared presence")
	}
	if notificationpolicy.PresenceUnknown.Valid() {
		t.Fatal("the zero value must stay undeclared")
	}
}

// The whole point of the new state: a claim about what the reader is looking at
// only counts where that claim could have been observed. A publisher that says
// "connected" has not observed it, so the rule must not fire — in either
// direction — and the client applies it locally instead.
func TestConversationOpenIsInertWithoutObservedFocus(t *testing.T) {
	base := notificationpolicy.Context{
		EventType:    notificationevent.EventTypeChannelMessage,
		Origin:       notificationevent.OriginLive,
		WorkSchedule: workschedule.StateNotConfigured,
		Presence:     notificationpolicy.PresenceConnected,
	}
	open, closed := base, base
	open.ConversationOpen = true
	closed.ConversationOpen = false

	if notificationpolicy.Evaluate(open).Channels != notificationpolicy.Evaluate(closed).Channels {
		t.Fatal("ConversationOpen changed the outcome for a presence that cannot have observed it")
	}
	// Under Foreground — where focus *was* observed — the same flag still decides.
	observed := base
	observed.Presence = notificationpolicy.PresenceForeground
	observed.ConversationOpen = true
	if notificationpolicy.Evaluate(observed).Eligible() {
		t.Fatal("an observed open conversation must still suppress")
	}
}

// Preferences that could not be read are not preferences that said nothing
// (issue #744, review round 7).
//
// The failure mode this closes is specific: a recipient who had silenced a
// conversation gets alerted because a preference read failed and its zero value
// read as "not muted". That is a fault they experience and cannot undo.
func TestUnreadablePreferencesSuppressEveryAlertChannel(t *testing.T) {
	base := notificationpolicy.Context{
		EventType:    notificationevent.EventTypeChannelMessage,
		Origin:       notificationevent.OriginLive,
		WorkSchedule: workschedule.StateNotConfigured,
		Presence:     notificationpolicy.PresenceConnected,
	}

	// Resolved and empty: the ordinary case, and it still alerts. Absence of a
	// preference is an answer.
	resolved := notificationpolicy.Evaluate(base)
	if !resolved.Eligible() {
		t.Fatalf("a recipient who expressed nothing lost their alerts: %+v", resolved)
	}

	unavailable := base
	unavailable.Preferences.Status = notificationpolicy.PreferenceStatusUnavailable
	decision := notificationpolicy.Evaluate(unavailable)

	if decision.Eligible() {
		t.Fatalf("an unreadable preference still authorised a surface: %+v", decision.Channels)
	}
	if decision.Channels.InApp || decision.Channels.Sound || decision.Channels.WebPush {
		t.Fatalf("channels = %+v, want every alert channel denied", decision.Channels)
	}
	// The reason has to say what actually happened: a fault, not a choice.
	if got := decision.SuppressedReason(); got != string(notificationpolicy.ReasonPreferencesUnavailable) {
		t.Fatalf("reason = %q, want %q", got, notificationpolicy.ReasonPreferencesUnavailable)
	}
}

// The new reason must not displace one that explains the outcome better. A
// decision already settled by something knowable stays recorded under that.
func TestAKnowableRuleStillOwnsTheReasonWhenPreferencesAreAlsoUnreadable(t *testing.T) {
	cases := map[string]struct {
		mutate func(*notificationpolicy.Context)
		want   notificationpolicy.Reason
	}{
		"outside working hours": {
			func(c *notificationpolicy.Context) { c.WorkSchedule = workschedule.StateOutsideWorkHours },
			notificationpolicy.ReasonOutsideWorkHours,
		},
		"an imported event": {
			func(c *notificationpolicy.Context) { c.Origin = notificationevent.OriginImport },
			notificationpolicy.ReasonHistoricalOrImported,
		},
		"a reaction": {
			func(c *notificationpolicy.Context) { c.EventType = notificationevent.EventTypeReaction },
			notificationpolicy.ReasonSilentEventType,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := notificationpolicy.Context{
				EventType:    notificationevent.EventTypeChannelMessage,
				Origin:       notificationevent.OriginLive,
				WorkSchedule: workschedule.StateNotConfigured,
				Presence:     notificationpolicy.PresenceConnected,
				Preferences: notificationpolicy.Preferences{
					Status: notificationpolicy.PreferenceStatusUnavailable,
				},
			}
			tc.mutate(&c)
			if got := notificationpolicy.Evaluate(c).SuppressedReason(); got != string(tc.want) {
				t.Fatalf("reason = %q, want %q", got, tc.want)
			}
		})
	}
}

// The zero value must stay "resolved", because every existing caller fills these
// fields in by reading them. A caller that has to opt in to fail-closed is one
// that cannot forget it is fail-closed.
func TestTheZeroPreferenceStatusIsResolved(t *testing.T) {
	if (notificationpolicy.Preferences{}).Status != notificationpolicy.PreferenceStatusResolved {
		t.Fatal("the zero value of Preferences must mean its fields are the recipient's own")
	}
}
