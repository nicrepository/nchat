package notificationpolicy_test

import (
	"testing"

	"github.com/nicrepository/nchat/libs/go/platform/notificationevent"
	"github.com/nicrepository/nchat/libs/go/platform/notificationpolicy"
	"github.com/nicrepository/nchat/libs/go/platform/workschedule"
)

// Do Not Disturb (issue #798) silences every alert channel, for every kind of
// event, and owns the reason only where nothing knowable already decided it.
func TestEvaluateDoNotDisturb(t *testing.T) {
	dnd := func(c *notificationpolicy.Context) { c.Preferences.DoNotDisturb = true }
	run(t, []policyCase{
		{
			name:        "a direct message is silenced",
			mutate:      dnd,
			wantChannel: nothing,
			wantReasons: []notificationpolicy.Reason{notificationpolicy.ReasonDoNotDisturb},
		},
		{
			name: "a high-priority mention is silenced too",
			mutate: func(c *notificationpolicy.Context) {
				dnd(c)
				c.EventType = notificationevent.EventTypeMention
				c.Priority = notificationevent.PriorityHigh
			},
			wantChannel: nothing,
			wantReasons: []notificationpolicy.Reason{notificationpolicy.ReasonDoNotDisturb},
		},
		{
			name: "push for an absent recipient is silenced",
			mutate: func(c *notificationpolicy.Context) {
				dnd(c)
				c.Presence = notificationpolicy.PresenceUnknown
			},
			wantChannel: nothing,
			wantReasons: []notificationpolicy.Reason{notificationpolicy.ReasonDoNotDisturb},
		},
		{
			name: "the corporate rule keeps its reason",
			mutate: func(c *notificationpolicy.Context) {
				dnd(c)
				c.WorkSchedule = workschedule.StateOutsideWorkHours
			},
			wantChannel: nothing,
			wantReasons: []notificationpolicy.Reason{notificationpolicy.ReasonOutsideWorkHours},
		},
		{
			name: "do not disturb is recorded before a mute",
			mutate: func(c *notificationpolicy.Context) {
				dnd(c)
				c.Preferences.Muted = true
			},
			wantChannel: nothing,
			wantReasons: []notificationpolicy.Reason{notificationpolicy.ReasonDoNotDisturb},
		},
		{
			name:        "without it the recipient is alerted",
			wantChannel: foreground,
		},
	})
}
