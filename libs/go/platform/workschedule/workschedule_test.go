package workschedule_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/workschedule"
)

// The schedule from issue #743, which is also the shape every evaluation test
// reads from: two blocks a day with a lunch break between them, a shorter
// Friday, and no weekend.
var (
	morning         = workschedule.Interval{Start: workschedule.At(7, 0), End: workschedule.At(12, 0)}
	afternoon       = workschedule.Interval{Start: workschedule.At(13, 0), End: workschedule.At(16, 0)}
	fridayAfternoon = workschedule.Interval{Start: workschedule.At(13, 0), End: workschedule.At(15, 0)}
)

const officeZone = "America/Sao_Paulo"

func officeDays() workschedule.Days {
	return workschedule.Days{
		time.Monday:    {morning, afternoon},
		time.Tuesday:   {morning, afternoon},
		time.Wednesday: {morning, afternoon},
		time.Thursday:  {morning, afternoon},
		time.Friday:    {morning, fridayAfternoon},
	}
}

func mustSchedule(t *testing.T, timezone string, days workschedule.Days) workschedule.Schedule {
	t.Helper()
	schedule, err := workschedule.New(workschedule.SourceOrganization, timezone, days)
	if err != nil {
		t.Fatalf("New(%q): %v", timezone, err)
	}
	return schedule
}

func officeSchedule(t *testing.T) workschedule.Schedule {
	t.Helper()
	return mustSchedule(t, officeZone, officeDays())
}

func mustLocation(t *testing.T, name string) *time.Location {
	t.Helper()
	location, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("LoadLocation(%q): %v", name, err)
	}
	return location
}

// localTime builds an instant from a wall clock reading in a named zone. Every
// expectation in these tests is written the way the schedule is written — in
// civil time — so a wrong offset shows up as a wrong instant rather than as a
// number nobody can check by eye.
func localTime(t *testing.T, zone string, year int, month time.Month, day, hour, minute int) time.Time {
	t.Helper()
	return time.Date(year, month, day, hour, minute, 0, 0, mustLocation(t, zone))
}

func TestSourceAuthority(t *testing.T) {
	if !workschedule.SourceOrganization.Valid() || !workschedule.SourcePersonal.Valid() {
		t.Fatal("declared sources must be valid")
	}
	if workschedule.Source("").Valid() || workschedule.Source("hr-import").Valid() {
		t.Fatal("undeclared sources must not be valid")
	}
	if !workschedule.SourceOrganization.ReadOnly() {
		t.Fatal("an organizational schedule must be read-only to the user it applies to")
	}
	if workschedule.SourcePersonal.ReadOnly() {
		t.Fatal("a personal schedule must be editable by the user it belongs to")
	}
	// The fail-closed half: a value this build does not know is somebody
	// else's policy, never the user's own.
	if !workschedule.Source("hr-import").ReadOnly() || !workschedule.Source("").ReadOnly() {
		t.Fatal("an unknown source must fail closed as read-only")
	}
}

func TestNewAcceptsRealTimezones(t *testing.T) {
	for _, zone := range []string{"America/Sao_Paulo", "America/New_York", "Europe/Berlin", "UTC"} {
		schedule := mustSchedule(t, zone, officeDays())
		if got := schedule.Timezone(); got != zone {
			t.Fatalf("Timezone() = %q, want %q", got, zone)
		}
		if !schedule.IsConfigured() {
			t.Fatalf("%q: a schedule returned by New must be configured", zone)
		}
		if got := schedule.Source(); got != workschedule.SourceOrganization {
			t.Fatalf("%q: Source() = %q", zone, got)
		}
	}
}

func TestNewRefusesInvalidInput(t *testing.T) {
	valid := officeDays()

	tests := []struct {
		name     string
		source   workschedule.Source
		timezone string
		days     workschedule.Days
	}{
		{
			name:     "undeclared source",
			source:   workschedule.Source("hr-import"),
			timezone: officeZone,
			days:     valid,
		},
		{
			name:     "empty source",
			timezone: officeZone,
			days:     valid,
		},
		{
			name:     "missing timezone",
			source:   workschedule.SourceOrganization,
			timezone: "",
			days:     valid,
		},
		{
			name:     "blank timezone",
			source:   workschedule.SourceOrganization,
			timezone: "   ",
			days:     valid,
		},
		{
			name:     "timezone that does not exist",
			source:   workschedule.SourceOrganization,
			timezone: "America/Sao_Pualo",
			days:     valid,
		},
		{
			// Accepted by time.LoadLocation, and refused here: it names the
			// server's own zone, which is the one authority a schedule must
			// never have.
			name:     "server local zone",
			source:   workschedule.SourceOrganization,
			timezone: "Local",
			days:     valid,
		},
		{
			name:     "utc offset instead of an IANA name",
			source:   workschedule.SourceOrganization,
			timezone: "-03:00",
			days:     valid,
		},
		{
			name:     "interval starting and ending at the same minute",
			source:   workschedule.SourceOrganization,
			timezone: officeZone,
			days: workschedule.Days{
				time.Monday: {{Start: workschedule.At(9, 0), End: workschedule.At(9, 0)}},
			},
		},
		{
			name:     "interval ending before it starts",
			source:   workschedule.SourceOrganization,
			timezone: officeZone,
			days: workschedule.Days{
				time.Monday: {{Start: workschedule.At(16, 0), End: workschedule.At(7, 0)}},
			},
		},
		{
			name:     "negative start",
			source:   workschedule.SourceOrganization,
			timezone: officeZone,
			days: workschedule.Days{
				time.Monday: {{Start: workschedule.At(-1, 0), End: workschedule.At(9, 0)}},
			},
		},
		{
			name:     "start at the end of the day",
			source:   workschedule.SourceOrganization,
			timezone: officeZone,
			days: workschedule.Days{
				time.Monday: {{Start: workschedule.At(24, 0), End: workschedule.At(24, 30)}},
			},
		},
		{
			name:     "interval running past midnight",
			source:   workschedule.SourceOrganization,
			timezone: officeZone,
			days: workschedule.Days{
				time.Monday: {{Start: workschedule.At(22, 0), End: workschedule.At(26, 0)}},
			},
		},
		{
			name:     "overlapping intervals",
			source:   workschedule.SourceOrganization,
			timezone: officeZone,
			days: workschedule.Days{
				time.Monday: {morning, {Start: workschedule.At(11, 0), End: workschedule.At(16, 0)}},
			},
		},
		{
			name:     "duplicated interval",
			source:   workschedule.SourceOrganization,
			timezone: officeZone,
			days: workschedule.Days{
				time.Monday: {morning, morning},
			},
		},
		{
			// [Start, End) makes 12:00 unambiguous about containment, but a
			// boundary where nothing changes is a transition nobody can act on.
			name:     "adjacent intervals that should be one block",
			source:   workschedule.SourceOrganization,
			timezone: officeZone,
			days: workschedule.Days{
				time.Monday: {
					{Start: workschedule.At(7, 0), End: workschedule.At(12, 0)},
					{Start: workschedule.At(12, 0), End: workschedule.At(16, 0)},
				},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			schedule, err := workschedule.New(test.source, test.timezone, test.days)
			if !errors.Is(err, workschedule.ErrInvalidSchedule) {
				t.Fatalf("New() error = %v, want ErrInvalidSchedule", err)
			}
			if schedule.IsConfigured() {
				t.Fatal("a refused schedule must not come back configured")
			}
		})
	}
}

// A caller is not required to hand the intervals over in order, and sorting
// them cannot rescue an input that is actually invalid — the overlap above is
// still refused when it arrives reversed.
func TestNewOrdersIntervalsWithoutHidingInvalidOnes(t *testing.T) {
	schedule := mustSchedule(t, officeZone, workschedule.Days{
		time.Monday: {afternoon, morning},
	})
	evaluation := schedule.Evaluate(localTime(t, officeZone, 2026, time.September, 7, 9, 0))
	if evaluation.State != workschedule.StateWithinWorkHours || evaluation.Interval != morning {
		t.Fatalf("unsorted input: got %+v, want the morning block", evaluation)
	}

	_, err := workschedule.New(workschedule.SourceOrganization, officeZone, workschedule.Days{
		time.Monday: {{Start: workschedule.At(11, 0), End: workschedule.At(16, 0)}, morning},
	})
	if !errors.Is(err, workschedule.ErrInvalidSchedule) {
		t.Fatalf("reversed overlap: error = %v, want ErrInvalidSchedule", err)
	}
}

// A schedule with no interval on any day is a person who never works. It is
// accepted, because refusing it would push the caller into representing it as
// "no schedule" — and "never works" and "we do not know" are the two facts this
// package exists to keep apart.
func TestScheduleWithNoWorkingIntervalIsNotTheSameAsNoSchedule(t *testing.T) {
	empty := mustSchedule(t, officeZone, workschedule.Days{})
	evaluation := empty.Evaluate(localTime(t, officeZone, 2026, time.September, 7, 9, 0))

	if evaluation.State != workschedule.StateOutsideWorkHours {
		t.Fatalf("State = %q, want outside_work_hours", evaluation.State)
	}
	if !evaluation.NextTransition.IsZero() {
		t.Fatalf("NextTransition = %v, want none: nothing will ever change", evaluation.NextTransition)
	}
	if !empty.IsConfigured() {
		t.Fatal("an empty schedule is still a schedule")
	}
}

func TestZeroScheduleIsNotConfigured(t *testing.T) {
	var absent workschedule.Schedule

	if absent.IsConfigured() {
		t.Fatal("the zero Schedule must not report itself configured")
	}
	if got := absent.Timezone(); got != "" {
		t.Fatalf("Timezone() = %q, want empty", got)
	}
	if absent.Source().Valid() {
		t.Fatal("the zero Schedule must not carry a usable source")
	}
	if !absent.Source().ReadOnly() {
		t.Fatal("an absent policy is not the user's to edit")
	}

	evaluation := absent.Evaluate(time.Now())
	if evaluation.State != workschedule.StateNotConfigured {
		t.Fatalf("State = %q, want not_configured", evaluation.State)
	}
	if !evaluation.NextTransition.IsZero() {
		t.Fatal("an absent schedule has no next transition")
	}
	if evaluation.Interval != (workschedule.Interval{}) {
		t.Fatal("an absent schedule reports no current interval")
	}
}

// A day cannot hold more intervals than there are non-touching minutes in it, so
// an oversized list is refused before it is sorted or read. The point is the
// cost, not the count: validation must not be a place a caller can spend the
// server's CPU.
func TestNewRefusesMoreIntervalsThanADayCanHold(t *testing.T) {
	crowded := make([]workschedule.Interval, 0, 721)
	for minute := range 721 {
		crowded = append(crowded, workschedule.Interval{
			Start: workschedule.Minutes(minute),
			End:   workschedule.Minutes(minute) + 1,
		})
	}

	_, err := workschedule.New(workschedule.SourceOrganization, officeZone, workschedule.Days{
		time.Monday: crowded,
	})
	if !errors.Is(err, workschedule.ErrInvalidSchedule) {
		t.Fatalf("New() error = %v, want ErrInvalidSchedule", err)
	}
	// These intervals are also invalid for touching one another, so the message
	// is what proves the cheap refusal ran first instead of the sort.
	if !strings.Contains(err.Error(), "more than a day can hold") {
		t.Fatalf("New() error = %v, want the oversized-day refusal", err)
	}
}
