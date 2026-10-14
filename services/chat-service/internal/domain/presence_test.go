package domain

import (
	"errors"
	"testing"
	"time"
)

func TestResolvePresence_Precedence(t *testing.T) {
	future := time.Date(2026, 10, 1, 13, 0, 0, 0, time.UTC)
	manual := func(state PresenceManualState, expires time.Time) PresenceContext {
		return PresenceContext{Override: PresenceOverride{State: state, ExpiresAt: expires}}
	}
	inCall := func(ctx PresenceContext) PresenceContext {
		ctx.Activity = PresenceActivityInCall
		return ctx
	}

	cases := []struct {
		name  string
		reach PresenceReach
		ctx   PresenceContext
		want  EffectivePresence
	}{
		{"active session is available", PresenceReachActive, PresenceContext{}, EffectivePresence{Availability: PresenceAvailable}},
		{"idle sessions are away", PresenceReachIdle, PresenceContext{}, EffectivePresence{Availability: PresenceAway}},
		{"no session is offline", PresenceReachNone, PresenceContext{}, EffectivePresence{Availability: PresenceOffline}},
		{"unknown reach is offline", PresenceReach("bogus"), PresenceContext{}, EffectivePresence{Availability: PresenceOffline}},
		{"dnd wins over activity", PresenceReachActive, manual(PresenceManualDoNotDisturb, future), EffectivePresence{Availability: PresenceDoNotDisturb}},
		{"busy wins over idle", PresenceReachIdle, manual(PresenceManualBusy, future), EffectivePresence{Availability: PresenceBusy}},
		{"brb is its own state", PresenceReachActive, manual(PresenceManualBeRightBack, future), EffectivePresence{Availability: PresenceBeRightBack}},
		{"manual away while active", PresenceReachActive, manual(PresenceManualAway, future), EffectivePresence{Availability: PresenceAway}},
		{"manual available survives idle", PresenceReachIdle, manual(PresenceManualAvailable, future), EffectivePresence{Availability: PresenceAvailable}},
		{"appear offline hides a connected user", PresenceReachActive, manual(PresenceManualAppearOffline, future), EffectivePresence{Availability: PresenceOffline}},
		{"appear offline hides the call too", PresenceReachActive, inCall(manual(PresenceManualAppearOffline, future)), EffectivePresence{Availability: PresenceOffline}},
		{"override cannot create a session", PresenceReachNone, manual(PresenceManualDoNotDisturb, future), EffectivePresence{Availability: PresenceOffline}},
		{"no override falls back to reach", PresenceReachIdle, PresenceContext{}, EffectivePresence{Availability: PresenceAway}},
		{"call prevents away", PresenceReachIdle, inCall(PresenceContext{}), EffectivePresence{Availability: PresenceBusy, Activity: PresenceActivityInCall}},
		{"call while active is busy", PresenceReachActive, inCall(PresenceContext{}), EffectivePresence{Availability: PresenceBusy, Activity: PresenceActivityInCall}},
		{"dnd during a call keeps the context", PresenceReachActive, inCall(manual(PresenceManualDoNotDisturb, future)), EffectivePresence{Availability: PresenceDoNotDisturb, Activity: PresenceActivityInCall}},
		{"manual busy during a call", PresenceReachIdle, inCall(manual(PresenceManualBusy, future)), EffectivePresence{Availability: PresenceBusy, Activity: PresenceActivityInCall}},
		{"manual away during a call hides the call", PresenceReachActive, inCall(manual(PresenceManualAway, future)), EffectivePresence{Availability: PresenceAway}},
		{"manual available during a call is busy", PresenceReachActive, inCall(manual(PresenceManualAvailable, future)), EffectivePresence{Availability: PresenceBusy, Activity: PresenceActivityInCall}},
		{"call ended with a live override", PresenceReachIdle, manual(PresenceManualBeRightBack, future), EffectivePresence{Availability: PresenceBeRightBack}},
		{"last session gone during a call", PresenceReachNone, inCall(PresenceContext{}), EffectivePresence{Availability: PresenceOffline}},
		{"meeting is busy context", PresenceReachIdle, PresenceContext{Activity: PresenceActivityInMeeting}, EffectivePresence{Availability: PresenceBusy, Activity: PresenceActivityInMeeting}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ResolvePresence(tc.reach, tc.ctx); got != tc.want {
				t.Fatalf("ResolvePresence = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestParsePresenceManualState(t *testing.T) {
	for _, valid := range []string{"available", "busy", "dnd", "brb", "away", "appear_offline"} {
		if state, err := ParsePresenceManualState(valid); err != nil || string(state) != valid {
			t.Fatalf("ParsePresenceManualState(%q) = %q, %v", valid, state, err)
		}
	}
	for _, invalid := range []string{"", "offline", "online", "DND", "busy ", "in_call"} {
		if _, err := ParsePresenceManualState(invalid); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("ParsePresenceManualState(%q) err = %v, want ErrInvalidInput", invalid, err)
		}
	}
}

func TestValidatePresenceExpiry(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name    string
		expires time.Time
		ok      bool
	}{
		{"one hour", now.Add(time.Hour), true},
		{"minimum", now.Add(PresenceOverrideMinDuration), true},
		{"maximum", now.Add(PresenceOverrideMaxDuration), true},
		{"past", now.Add(-time.Minute), false},
		{"now", now, false},
		{"too soon", now.Add(30 * time.Second), false},
		{"too far", now.Add(PresenceOverrideMaxDuration + time.Second), false},
	}
	for _, tc := range cases {
		err := ValidatePresenceExpiry(tc.expires, now)
		if tc.ok != (err == nil) {
			t.Fatalf("%s: err = %v, want ok=%v", tc.name, err, tc.ok)
		}
		if err != nil && !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("%s: err = %v, want ErrInvalidInput", tc.name, err)
		}
	}
}

// A timed fact is what the read that returned it said: the database found it
// in force on its own clock, and resolving judges no end again on any other
// (issue #798, HIGH-B) — an override whose end any clock would call past, and a
// call whose lease would, still count. Their ends only bound the commit, at the
// store.
func TestResolvePresence_TimedFactsAreNotJudgedAgain(t *testing.T) {
	past := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		ctx  PresenceContext
		want EffectivePresence
	}{
		{PresenceContext{Override: PresenceOverride{State: PresenceManualDoNotDisturb, ExpiresAt: past}}, EffectivePresence{Availability: PresenceDoNotDisturb}},
		{PresenceContext{Override: PresenceOverride{State: PresenceManualAppearOffline, ExpiresAt: past}}, EffectivePresence{Availability: PresenceOffline}},
		{PresenceContext{Activity: PresenceActivityInCall, ActivityUntil: past}, EffectivePresence{Availability: PresenceBusy, Activity: PresenceActivityInCall}},
		{PresenceContext{Override: PresenceOverride{}}, EffectivePresence{Availability: PresenceAvailable}},
	}
	for _, tc := range cases {
		if got := ResolvePresence(PresenceReachActive, tc.ctx); got != tc.want {
			t.Fatalf("ResolvePresence(%+v) = %+v, want %+v", tc.ctx, got, tc.want)
		}
	}
}
