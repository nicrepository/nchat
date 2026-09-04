package workschedule_test

import (
	"testing"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/workschedule"
)

// wallClock is one reading of the office schedule's own clock: the tests state
// every instant and every expectation the way the schedule itself is written.
type wallClock struct {
	day    int
	hour   int
	minute int
}

// boundaryCase is one instant, the state it must produce, and the next
// transition it must promise.
type boundaryCase struct {
	name     string
	instant  wallClock
	want     workschedule.State
	interval workschedule.Interval
	next     wallClock
}

func TestEvaluateBoundaries(t *testing.T) {
	schedule := officeSchedule(t)

	// September 2026: the 7th is a Monday, the 11th a Friday, the 12th and
	// 13th the weekend, the 14th the Monday after.
	tests := []boundaryCase{
		{
			name:     "inside the morning block",
			instant:  wallClock{7, 9, 0},
			want:     workschedule.StateWithinWorkHours,
			interval: morning,
			next:     wallClock{7, 12, 0},
		},
		{
			name:    "before the day starts",
			instant: wallClock{7, 6, 59},
			want:    workschedule.StateOutsideWorkHours,
			next:    wallClock{7, 7, 0},
		},
		{
			name:    "after the day ends",
			instant: wallClock{7, 16, 30},
			want:    workschedule.StateOutsideWorkHours,
			next:    wallClock{8, 7, 0},
		},
		{
			// Lunch is not a flag on the day. It is the gap between two blocks,
			// and it reads as outside working hours for exactly that reason.
			name:    "during the lunch break",
			instant: wallClock{7, 12, 30},
			want:    workschedule.StateOutsideWorkHours,
			next:    wallClock{7, 13, 0},
		},
		{
			name:     "exactly at the start, which is inclusive",
			instant:  wallClock{7, 7, 0},
			want:     workschedule.StateWithinWorkHours,
			interval: morning,
			next:     wallClock{7, 12, 0},
		},
		{
			name:     "one minute before the end",
			instant:  wallClock{7, 11, 59},
			want:     workschedule.StateWithinWorkHours,
			interval: morning,
			next:     wallClock{7, 12, 0},
		},
		{
			name:    "exactly at the end, which is exclusive",
			instant: wallClock{7, 12, 0},
			want:    workschedule.StateOutsideWorkHours,
			next:    wallClock{7, 13, 0},
		},
		{
			name:     "inside the afternoon block",
			instant:  wallClock{7, 14, 0},
			want:     workschedule.StateWithinWorkHours,
			interval: afternoon,
			next:     wallClock{7, 16, 0},
		},
		{
			name:    "last minute of the day",
			instant: wallClock{7, 23, 59},
			want:    workschedule.StateOutsideWorkHours,
			next:    wallClock{8, 7, 0},
		},
		{
			name:    "midnight of the next day",
			instant: wallClock{8, 0, 0},
			want:    workschedule.StateOutsideWorkHours,
			next:    wallClock{8, 7, 0},
		},
		{
			name:     "friday closes an hour earlier",
			instant:  wallClock{11, 14, 59},
			want:     workschedule.StateWithinWorkHours,
			interval: fridayAfternoon,
			next:     wallClock{11, 15, 0},
		},
		{
			name:    "friday into the weekend",
			instant: wallClock{11, 15, 0},
			want:    workschedule.StateOutsideWorkHours,
			next:    wallClock{14, 7, 0},
		},
		{
			name:    "saturday has no work at all",
			instant: wallClock{12, 10, 0},
			want:    workschedule.StateOutsideWorkHours,
			next:    wallClock{14, 7, 0},
		},
		{
			name:    "sunday evening waits for monday",
			instant: wallClock{13, 20, 0},
			want:    workschedule.StateOutsideWorkHours,
			next:    wallClock{14, 7, 0},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			checkBoundary(t, schedule, test)
		})
	}
}

func checkBoundary(t *testing.T, schedule workschedule.Schedule, test boundaryCase) {
	t.Helper()
	evaluation := schedule.Evaluate(officeInstant(t, test.instant))

	if evaluation.State != test.want {
		t.Fatalf("State = %q, want %q", evaluation.State, test.want)
	}
	if evaluation.Interval != test.interval {
		t.Fatalf("Interval = %+v, want %+v", evaluation.Interval, test.interval)
	}
	if want := officeInstant(t, test.next); !evaluation.NextTransition.Equal(want) {
		t.Fatalf("NextTransition = %s, want %s", evaluation.NextTransition, want)
	}
	// The claim the field makes: the state is different there. A boundary that
	// reported no change would be a consumer waking up for nothing, which is
	// what refusing touching intervals prevents.
	if at := schedule.Evaluate(evaluation.NextTransition); at.State == evaluation.State {
		t.Fatalf("state at the reported transition is still %q", at.State)
	}
}

func officeInstant(t *testing.T, clock wallClock) time.Time {
	t.Helper()
	return localTime(t, officeZone, 2026, time.September, clock.day, clock.hour, clock.minute)
}

// The schedule's zone is the only authority. Neither the zone the process runs
// in nor the zone the caller happens to have formatted the instant in may
// change the answer.
func TestEvaluateIgnoresTheProcessAndCallerZones(t *testing.T) {
	original := time.Local
	// UTC+14, about as far from São Paulo as a zone gets, and a different
	// calendar day for most of the office's working hours.
	time.Local = mustLocation(t, "Pacific/Kiritimati")
	t.Cleanup(func() { time.Local = original })

	schedule := officeSchedule(t)
	instant := officeInstant(t, wallClock{7, 9, 0})
	wantNext := officeInstant(t, wallClock{7, 12, 0})

	for _, zone := range []string{"UTC", "Asia/Tokyo", "Local", officeZone} {
		location := time.Local
		if zone != "Local" {
			location = mustLocation(t, zone)
		}
		evaluation := schedule.Evaluate(instant.In(location))

		if evaluation.State != workschedule.StateWithinWorkHours {
			t.Fatalf("%s: State = %q, want within_work_hours", zone, evaluation.State)
		}
		if evaluation.Interval != morning {
			t.Fatalf("%s: Interval = %+v, want the morning block", zone, evaluation.Interval)
		}
		if !evaluation.NextTransition.Equal(wantNext) {
			t.Fatalf("%s: NextTransition = %s, want %s", zone, evaluation.NextTransition, wantNext)
		}
	}
}

// The forward search has to cross a week of empty days, and stop rather than
// run forever when it cannot find anything.
func TestEvaluateCrossesConsecutiveDaysWithoutWork(t *testing.T) {
	schedule := mustSchedule(t, officeZone, workschedule.Days{
		time.Wednesday: {morning},
	})

	// Wednesday the 9th, after the only block of the only working day.
	evaluation := schedule.Evaluate(officeInstant(t, wallClock{9, 17, 0}))
	if evaluation.State != workschedule.StateOutsideWorkHours {
		t.Fatalf("State = %q, want outside_work_hours", evaluation.State)
	}
	// Six days with nothing on them, then the same weekday coming round again.
	if want := officeInstant(t, wallClock{16, 7, 0}); !evaluation.NextTransition.Equal(want) {
		t.Fatalf("NextTransition = %s, want %s", evaluation.NextTransition, want)
	}
}

// America/New_York changes its offset on 2026-03-08 and 2026-11-01, both
// Sundays, so a Sunday block is what actually straddles the change. São Paulo
// would prove nothing here: Brazil stopped observing daylight saving in 2019,
// so a test written against it would silently stop testing anything.
func TestEvaluateAcrossDaylightSavingTransitions(t *testing.T) {
	const zone = "America/New_York"
	schedule := mustSchedule(t, zone, workschedule.Days{
		time.Sunday: {{Start: workschedule.At(0, 0), End: workschedule.At(6, 0)}},
	})

	tests := []struct {
		name    string
		month   time.Month
		day     int
		elapsed time.Duration
	}{
		{
			// 02:00 becomes 03:00: the block is six hours on the clock and five
			// in real time.
			name:    "spring forward",
			month:   time.March,
			day:     8,
			elapsed: 4*time.Hour + 30*time.Minute,
		},
		{
			// 02:00 becomes 01:00: six hours on the clock, seven in real time.
			name:    "fall back",
			month:   time.November,
			day:     1,
			elapsed: 6*time.Hour + 30*time.Minute,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			instant := localTime(t, zone, 2026, test.month, test.day, 0, 30)
			evaluation := schedule.Evaluate(instant)

			if evaluation.State != workschedule.StateWithinWorkHours {
				t.Fatalf("State = %q, want within_work_hours", evaluation.State)
			}
			// The block ends at six in the morning, whatever the offset does in
			// between — the wall clock is the rule, not a fixed duration.
			want := localTime(t, zone, 2026, test.month, test.day, 6, 0)
			if !evaluation.NextTransition.Equal(want) {
				t.Fatalf("NextTransition = %s, want %s", evaluation.NextTransition, want)
			}
			if got := evaluation.NextTransition.Sub(instant); got != test.elapsed {
				t.Fatalf("real time until the transition = %s, want %s", got, test.elapsed)
			}
		})
	}
}

// dstZone is the zone every case below is written against. America/New_York
// still observes daylight saving, which São Paulo has not done since 2019, so a
// test written against the office zone would silently stop testing anything.
const dstZone = "America/New_York"

// In 2026 the zone jumps from 02:00 to 03:00 on 08 March and rewinds from 02:00
// to 01:00 on 01 November. Both changes land at 06:00/07:00 UTC, and the cases
// state their instants in UTC precisely because a civil reading on those two
// mornings is either ambiguous or nonexistent — which is the thing under test.
func utc2026(month time.Month, day, hour, minute int) time.Time {
	return time.Date(2026, month, day, hour, minute, 0, 0, time.UTC)
}

// dstCase is one absolute instant and everything the domain must say about it.
type dstCase struct {
	name     string
	instant  time.Time
	want     workschedule.State
	interval workschedule.Interval
	next     time.Time
}

func runDSTCases(t *testing.T, schedule workschedule.Schedule, tests []dstCase) {
	t.Helper()
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			evaluation := schedule.Evaluate(test.instant)

			if evaluation.State != test.want {
				t.Fatalf("State = %q, want %q", evaluation.State, test.want)
			}
			if evaluation.Interval != test.interval {
				t.Fatalf("Interval = %+v, want %+v", evaluation.Interval, test.interval)
			}
			if !evaluation.NextTransition.Equal(test.next) {
				t.Fatalf("NextTransition = %s, want %s",
					evaluation.NextTransition.UTC(), test.next.UTC())
			}
			assertTransitionIsReal(t, schedule, test.instant, evaluation)
		})
	}
}

// assertTransitionIsReal checks the three things NextTransition promises: it is
// in the future, the current answer still holds right up to it, and the answer
// at it is different.
func assertTransitionIsReal(t *testing.T, schedule workschedule.Schedule, instant time.Time, evaluation workschedule.Evaluation) {
	t.Helper()
	if evaluation.NextTransition.IsZero() {
		return
	}
	if !evaluation.NextTransition.After(instant) {
		t.Fatalf("NextTransition %s is not after %s", evaluation.NextTransition.UTC(), instant.UTC())
	}
	before := schedule.Evaluate(evaluation.NextTransition.Add(-time.Nanosecond))
	if before.State != evaluation.State {
		t.Fatalf("state changed to %q before the reported transition", before.State)
	}
	if at := schedule.Evaluate(evaluation.NextTransition); at.State == evaluation.State {
		t.Fatalf("state at the reported transition is still %q", at.State)
	}
}

// A block that lies entirely inside the hour the clock rewinds is worked twice,
// because the clock reads it twice. Both stretches are real half hours of the
// person's evening, and the domain reports the end of the one they are in and
// the start of the one that follows — the failure this replaces skipped the
// second stretch entirely and pointed a week ahead.
func TestEvaluateThroughAnIntervalInsideTheRepeatedHour(t *testing.T) {
	block := workschedule.Interval{Start: workschedule.At(1, 15), End: workschedule.At(1, 45)}
	schedule := mustSchedule(t, dstZone, workschedule.Days{time.Sunday: {block}})

	runDSTCases(t, schedule, []dstCase{
		{
			name:    "first pass, before the block",
			instant: utc2026(time.November, 1, 5, 0),
			want:    workschedule.StateOutsideWorkHours,
			next:    utc2026(time.November, 1, 5, 15),
		},
		{
			name:     "first pass, exactly at the start",
			instant:  utc2026(time.November, 1, 5, 15),
			want:     workschedule.StateWithinWorkHours,
			interval: block,
			next:     utc2026(time.November, 1, 5, 45),
		},
		{
			name:     "first pass, inside",
			instant:  utc2026(time.November, 1, 5, 30),
			want:     workschedule.StateWithinWorkHours,
			interval: block,
			next:     utc2026(time.November, 1, 5, 45),
		},
		{
			// The end of the first pass points at the second one, half an hour
			// of absolute time later, not at next week.
			name:    "first pass, exactly at the end",
			instant: utc2026(time.November, 1, 5, 45),
			want:    workschedule.StateOutsideWorkHours,
			next:    utc2026(time.November, 1, 6, 15),
		},
		{
			// 01:00 for the second time: the clock has just been rewound.
			name:    "second pass, before the block",
			instant: utc2026(time.November, 1, 6, 0),
			want:    workschedule.StateOutsideWorkHours,
			next:    utc2026(time.November, 1, 6, 15),
		},
		{
			name:     "second pass, exactly at the start",
			instant:  utc2026(time.November, 1, 6, 15),
			want:     workschedule.StateWithinWorkHours,
			interval: block,
			next:     utc2026(time.November, 1, 6, 45),
		},
		{
			name:     "second pass, inside",
			instant:  utc2026(time.November, 1, 6, 30),
			want:     workschedule.StateWithinWorkHours,
			interval: block,
			next:     utc2026(time.November, 1, 6, 45),
		},
		{
			// Only now is the week's work over, and the next Sunday is 01:15
			// EST, an hour later in UTC than this morning's first pass was.
			name:    "second pass, exactly at the end",
			instant: utc2026(time.November, 1, 6, 45),
			want:    workschedule.StateOutsideWorkHours,
			next:    utc2026(time.November, 8, 6, 15),
		},
	})
}

// When only the end of a block falls in the repeated hour, the second pass is
// clipped: 00:30 happened once, 01:00 to 01:30 happened again. So the person
// finishes, and is then back at work when the clock is rewound past their start
// time — which is what the clock on their wall says, and the domain agrees.
func TestEvaluateThroughAnIntervalEndingInTheRepeatedHour(t *testing.T) {
	block := workschedule.Interval{Start: workschedule.At(0, 30), End: workschedule.At(1, 30)}
	schedule := mustSchedule(t, dstZone, workschedule.Days{time.Sunday: {block}})

	runDSTCases(t, schedule, []dstCase{
		{
			name:     "inside the first pass",
			instant:  utc2026(time.November, 1, 5, 0),
			want:     workschedule.StateWithinWorkHours,
			interval: block,
			next:     utc2026(time.November, 1, 5, 30),
		},
		{
			name:    "the first pass has ended",
			instant: utc2026(time.November, 1, 5, 30),
			want:    workschedule.StateOutsideWorkHours,
			next:    utc2026(time.November, 1, 6, 0),
		},
		{
			// 01:00 the second time round is already past 00:30, so the clipped
			// stretch begins at the change itself rather than at the start time.
			name:     "the rewound clock lands back inside the block",
			instant:  utc2026(time.November, 1, 6, 0),
			want:     workschedule.StateWithinWorkHours,
			interval: block,
			next:     utc2026(time.November, 1, 6, 30),
		},
		{
			name:    "the second pass has ended",
			instant: utc2026(time.November, 1, 6, 30),
			want:    workschedule.StateOutsideWorkHours,
			next:    utc2026(time.November, 8, 5, 30),
		},
	})
}

// A block whose start and end both name minutes the clock skips is never
// worked, so it produces nothing at all: no window, and above all no transition
// announced for a moment that never arrives.
func TestEvaluateThroughAnIntervalInsideTheSpringForwardGap(t *testing.T) {
	skipped := workschedule.Interval{Start: workschedule.At(2, 10), End: workschedule.At(2, 40)}
	morning := workschedule.Interval{Start: workschedule.At(5, 0), End: workschedule.At(6, 0)}
	schedule := mustSchedule(t, dstZone, workschedule.Days{time.Sunday: {skipped, morning}})

	runDSTCases(t, schedule, []dstCase{
		{
			// An hour before the jump. The next thing that happens is the
			// morning block, not the block nobody will ever work.
			name:    "before the clocks jump",
			instant: utc2026(time.March, 8, 6, 0),
			want:    workschedule.StateOutsideWorkHours,
			next:    utc2026(time.March, 8, 9, 0),
		},
		{
			name:    "at the instant the clocks jump",
			instant: utc2026(time.March, 8, 7, 0),
			want:    workschedule.StateOutsideWorkHours,
			next:    utc2026(time.March, 8, 9, 0),
		},
		{
			// The instant that would have read 02:30 had the clock not moved.
			name:    "past the skipped minutes",
			instant: utc2026(time.March, 8, 7, 30),
			want:    workschedule.StateOutsideWorkHours,
			next:    utc2026(time.March, 8, 9, 0),
		},
		{
			name:     "the morning block still happens",
			instant:  utc2026(time.March, 8, 9, 0),
			want:     workschedule.StateWithinWorkHours,
			interval: morning,
			next:     utc2026(time.March, 8, 10, 0),
		},
	})

	// The skipped block is worked normally on a Sunday the zone leaves alone.
	ordinary := schedule.Evaluate(utc2026(time.March, 15, 6, 20))
	if ordinary.State != workschedule.StateWithinWorkHours || ordinary.Interval != skipped {
		t.Fatalf("the week after: got %+v, want the 02:10 block", ordinary)
	}
}

// A block that only partly overlaps the skipped hour keeps the part that is
// actually lived: work begins when the clock reaches the block, which is the
// change itself, and ends at its own civil end.
func TestEvaluateThroughAnIntervalStartingInTheSpringForwardGap(t *testing.T) {
	block := workschedule.Interval{Start: workschedule.At(2, 30), End: workschedule.At(5, 0)}
	schedule := mustSchedule(t, dstZone, workschedule.Days{time.Sunday: {block}})

	runDSTCases(t, schedule, []dstCase{
		{
			name:    "before the clocks jump",
			instant: utc2026(time.March, 8, 6, 0),
			want:    workschedule.StateOutsideWorkHours,
			next:    utc2026(time.March, 8, 7, 0),
		},
		{
			// 03:00, the first minute that exists after the jump, and the first
			// one the block covers.
			name:     "work starts when the clock reaches the block",
			instant:  utc2026(time.March, 8, 7, 0),
			want:     workschedule.StateWithinWorkHours,
			interval: block,
			next:     utc2026(time.March, 8, 9, 0),
		},
		{
			name:     "still inside, an hour later",
			instant:  utc2026(time.March, 8, 8, 0),
			want:     workschedule.StateWithinWorkHours,
			interval: block,
			next:     utc2026(time.March, 8, 9, 0),
		},
		{
			// 05:00 EDT: the civil end is unaffected by the jump.
			name:    "the block ends at its own civil end",
			instant: utc2026(time.March, 8, 9, 0),
			want:    workschedule.StateOutsideWorkHours,
			next:    utc2026(time.March, 15, 6, 30),
		},
	})
}

// The promise NextTransition makes, checked exhaustively rather than at chosen
// instants: walking a minute at a time across both daylight saving changes, the
// transition reported from every sampled minute is the very next minute at which
// Evaluate answers differently. Nothing is skipped and nothing is invented.
func TestNextTransitionIsAlwaysTheFirstStateChange(t *testing.T) {
	day := []workschedule.Interval{
		{Start: workschedule.At(1, 15), End: workschedule.At(1, 45)}, // inside the repeated hour
		{Start: workschedule.At(2, 10), End: workschedule.At(2, 40)}, // inside the skipped hour
		{Start: workschedule.At(9, 0), End: workschedule.At(12, 0)},  // an ordinary morning
		{Start: workschedule.At(13, 0), End: workschedule.At(17, 0)}, // and afternoon
	}
	var days workschedule.Days
	for weekday := range days {
		days[weekday] = day
	}
	schedule := mustSchedule(t, dstZone, days)

	// Three days around each change, which is long enough for every transition
	// the sampled minutes point at to fall inside the walk.
	const span = 3 * 24 * 60
	t.Run("spring forward", func(t *testing.T) {
		assertTransitionsMatchTheTimeline(t, schedule, utc2026(time.March, 7, 0, 0), span)
	})
	t.Run("fall back", func(t *testing.T) {
		assertTransitionsMatchTheTimeline(t, schedule, utc2026(time.October, 31, 0, 0), span)
	})
}

func assertTransitionsMatchTheTimeline(t *testing.T, schedule workschedule.Schedule, from time.Time, minutes int) {
	t.Helper()
	// Every boundary the domain can produce lands on a whole minute: civil
	// intervals are minutes, and every zone offset is a whole number of them. So
	// a minute-by-minute walk cannot step over a state change.
	states := make([]workschedule.State, minutes)
	for minute := range states {
		states[minute] = schedule.Evaluate(from.Add(time.Duration(minute) * time.Minute)).State
	}

	for minute, change := range firstChangeAfter(states) {
		reported := schedule.Evaluate(from.Add(time.Duration(minute) * time.Minute)).NextTransition
		if change < 0 {
			assertBeyondTheWalk(t, reported, from.Add(time.Duration(minutes)*time.Minute), minute)
			continue
		}
		if want := from.Add(time.Duration(change) * time.Minute); !reported.Equal(want) {
			t.Fatalf("minute %d (%s): NextTransition = %s, want %s",
				minute, from.Add(time.Duration(minute)*time.Minute).UTC(), reported.UTC(), want.UTC())
		}
	}
}

// firstChangeAfter returns, for each minute, the first later minute whose state
// differs, or -1 when the walk ends before one does.
func firstChangeAfter(states []workschedule.State) []int {
	changes := make([]int, len(states))
	changes[len(changes)-1] = -1
	for minute := len(states) - 2; minute >= 0; minute-- {
		changes[minute] = changes[minute+1]
		if states[minute+1] != states[minute] {
			changes[minute] = minute + 1
		}
	}
	return changes
}

// assertBeyondTheWalk covers the minutes whose next change falls outside the
// window that was walked: the domain may not claim a transition inside it.
func assertBeyondTheWalk(t *testing.T, reported, end time.Time, minute int) {
	t.Helper()
	if !reported.IsZero() && reported.Before(end) {
		t.Fatalf("minute %d: NextTransition = %s claims a change the walk did not see",
			minute, reported.UTC())
	}
}

// Midnight between a block that ends at 24:00 and one that starts at 00:00 is
// continuous work, so nothing is announced there. It is the same rule the two
// halves of a repeated hour follow, reached without any daylight saving at all.
func TestEvaluateDoesNotAnnounceMidnightBetweenTouchingDays(t *testing.T) {
	night := workschedule.Interval{Start: workschedule.At(22, 0), End: workschedule.At(24, 0)}
	earlyMorning := workschedule.Interval{Start: workschedule.At(0, 0), End: workschedule.At(6, 0)}
	schedule := mustSchedule(t, officeZone, workschedule.Days{
		time.Monday:  {night},
		time.Tuesday: {earlyMorning},
	})

	// Monday the 7th, an hour before midnight.
	evaluation := schedule.Evaluate(officeInstant(t, wallClock{7, 23, 0}))
	if evaluation.State != workschedule.StateWithinWorkHours || evaluation.Interval != night {
		t.Fatalf("got %+v, want the Monday night block", evaluation)
	}
	if want := officeInstant(t, wallClock{8, 6, 0}); !evaluation.NextTransition.Equal(want) {
		t.Fatalf("NextTransition = %s, want %s: midnight changes nothing", evaluation.NextTransition, want)
	}
	assertTransitionIsReal(t, schedule, officeInstant(t, wallClock{7, 23, 0}), evaluation)
}

// A zone may remove a civil date from its calendar. Pacific/Apia removed
// 2011-12-30 when Samoa crossed the International Date Line: the local clock
// went from Thursday the 29th straight to Saturday the 31st, and that Friday
// never existed there.
//
// A Friday-only schedule therefore skips a week, and the search has to survive
// it. It is the reason the horizon is two weekly cycles rather than one: asked
// on the Thursday, the next occurrence is eight civil dates away; asked on the
// previous Friday, fourteen. One cycle misses both, and a one-day margin on it
// covers the Thursday and still misses the Friday.
func TestEvaluateAcrossACivilDateTheZoneRemoved(t *testing.T) {
	const zone = "Pacific/Apia"
	block := workschedule.Interval{Start: workschedule.At(9, 0), End: workschedule.At(17, 0)}
	schedule := mustSchedule(t, zone, workschedule.Days{time.Friday: {block}})

	// Friday 2011-12-30 does not exist in this zone; the next one that does is
	// 2012-01-06.
	nextFriday := localTime(t, zone, 2012, time.January, 6, 9, 0)

	tests := []dstCase{
		{
			// The review's case: the skipped Friday is the very next civil date.
			name:    "the day before the calendar jumps",
			instant: localTime(t, zone, 2011, time.December, 29, 8, 0),
			want:    workschedule.StateOutsideWorkHours,
			next:    nextFriday,
		},
		{
			// The harder one: the next occurrence is a full second cycle away,
			// because the first cycle's Friday is the date that was removed.
			name:    "the previous Friday, after that day's block",
			instant: localTime(t, zone, 2011, time.December, 23, 18, 0),
			want:    workschedule.StateOutsideWorkHours,
			next:    nextFriday,
		},
		{
			name:    "the Saturday the calendar lands on",
			instant: localTime(t, zone, 2011, time.December, 31, 12, 0),
			want:    workschedule.StateOutsideWorkHours,
			next:    nextFriday,
		},
		{
			name:     "the next Friday that exists",
			instant:  nextFriday,
			want:     workschedule.StateWithinWorkHours,
			interval: block,
			next:     localTime(t, zone, 2012, time.January, 6, 17, 0),
		},
	}
	runDSTCases(t, schedule, tests)

	// No window is invented for the date nobody lived. The whole stretch the
	// removed Friday would have covered reads as outside work hours.
	for hour := range 24 {
		removed := time.Date(2011, time.December, 30, hour, 0, 0, 0, time.UTC)
		if state := schedule.Evaluate(removed).State; state != workschedule.StateOutsideWorkHours {
			t.Fatalf("%s: State = %q, want outside_work_hours on a date the zone removed",
				removed, state)
		}
	}
}

// The neighbouring weekday is untouched: Saturday still happens, two civil dates
// after the Thursday, even though the date between them was removed.
func TestEvaluateFindsTheWeekdayAfterARemovedCivilDate(t *testing.T) {
	const zone = "Pacific/Apia"
	block := workschedule.Interval{Start: workschedule.At(9, 0), End: workschedule.At(17, 0)}
	schedule := mustSchedule(t, zone, workschedule.Days{time.Saturday: {block}})

	instant := localTime(t, zone, 2011, time.December, 29, 8, 0)
	evaluation := schedule.Evaluate(instant)

	if evaluation.State != workschedule.StateOutsideWorkHours {
		t.Fatalf("State = %q, want outside_work_hours", evaluation.State)
	}
	if want := localTime(t, zone, 2011, time.December, 31, 9, 0); !evaluation.NextTransition.Equal(want) {
		t.Fatalf("NextTransition = %s, want %s", evaluation.NextTransition, want)
	}
	assertTransitionIsReal(t, schedule, instant, evaluation)
}
