package workschedule

import (
	"iter"
	"slices"
	"time"
)

// State is the answer to "was this instant inside working hours?".
//
// Three values and not a bool, because the third one is the whole point: "there
// is no schedule" is not "outside working hours", and a consumer that receives
// false for both cannot tell a quiet Sunday from an unconfigured workspace.
type State string

const (
	// StateNotConfigured means no schedule was available. Nothing about the
	// person's availability is asserted, and no hours are assumed on their
	// behalf.
	StateNotConfigured State = "not_configured"
	// StateWithinWorkHours means the instant fell inside one of the day's
	// intervals.
	StateWithinWorkHours State = "within_work_hours"
	// StateOutsideWorkHours means there is a schedule and the instant fell
	// outside every interval of that local day — before work, after work,
	// between two blocks, or on a day with no work at all.
	StateOutsideWorkHours State = "outside_work_hours"
)

// Evaluation is one temporal answer about one instant.
type Evaluation struct {
	// State is the answer. Always set.
	State State

	// Interval is the civil block the instant fell inside, set only when State
	// is StateWithinWorkHours. Otherwise it is the zero Interval, which no valid
	// schedule can contain — an interval must end after it starts — so an
	// absent block cannot be mistaken for a real one.
	Interval Interval

	// NextTransition is the absolute instant at which State changes, and it is
	// exactly that: the first instant after the evaluated one where a further
	// call to Evaluate would answer differently. No state change is skipped and
	// none is invented in between.
	//
	// Zero when there is nothing to wait for — no schedule, or a schedule with
	// no working stretch inside the search horizon, which for a weekly schedule
	// means none at all. That is the "when calculable" half of the contract, and
	// it is checked with IsZero rather than signalled by a second field.
	NextTransition time.Time
}

// searchHorizonDays bounds the forward search for the next state change.
//
// A schedule repeats weekly, so a stretch of work that exists at all begins
// within today plus seven days — seven covers every other weekday, and the
// eighth day is today's own weekday coming round again, which is what a
// schedule whose only block already ended today needs. Past that there is
// provably nothing to find, so the bound is not a heuristic: it is the point at
// which continuing would repeat the same week forever. A schedule with no
// intervals at all is the case it actually stops, and it stops after eight
// cheap iterations rather than never.
const searchHorizonDays = 8

// Evaluate answers whether instant falls inside working hours, and when that
// answer next changes.
//
// The instant is absolute and supplied by the caller; no clock is read here.
// That is what makes the function replayable, testable at exact boundaries, and
// independent of both the server's zone and the browser's. The schedule's own
// zone is the only authority over what the local clock reads.
//
// Both halves of the answer come out of one walk over the same resolved
// windows, and that is the property that matters: the state and the transition
// cannot describe different timelines, because they are read off the same data
// in the same pass.
func (s Schedule) Evaluate(instant time.Time) Evaluation {
	if !s.IsConfigured() {
		return Evaluation{State: StateNotConfigured}
	}
	evaluation := Evaluation{State: StateOutsideWorkHours}
	// runEnd, once set, is the end of the uninterrupted stretch of work that
	// covers instant. It is followed instead of returned straight away because
	// two windows can meet exactly — the two halves of a repeated hour, or a
	// block ending at midnight and the next one starting there — and a boundary
	// where the state does not change is not a transition.
	var runEnd time.Time
	for current := range s.upcomingWindows(instant) {
		switch {
		case !runEnd.IsZero() && !current.start.Equal(runEnd):
			evaluation.NextTransition = runEnd
			return evaluation
		case !runEnd.IsZero():
			runEnd = current.end
		case current.contains(instant):
			evaluation.State = StateWithinWorkHours
			evaluation.Interval = current.interval
			runEnd = current.end
		case current.start.After(instant):
			evaluation.NextTransition = current.start
			return evaluation
		}
	}
	evaluation.NextTransition = runEnd
	return evaluation
}

// window is one civil interval of one local date, resolved to the absolute
// stretch of time it actually occupies. Half-open as [start, end), for the same
// reason the civil interval is.
//
// Resolving before deciding anything is what pins both questions to one
// timeline. A civil reading is not a point in time on its own: on the morning a
// zone rewinds its clock "01:15" names two instants, and on the morning it jumps
// forward it may name none. Answering "am I at work?" by comparing civil
// readings while answering "when does that change?" by comparing instants lets
// the two drift apart exactly where it matters most.
type window struct {
	interval Interval
	start    time.Time
	end      time.Time
}

// contains reports whether instant falls inside the window.
func (w window) contains(instant time.Time) bool {
	return !instant.Before(w.start) && instant.Before(w.end)
}

// upcomingWindows yields every resolved window from the local date of instant
// forward, in order, across the search horizon. Windows that are already over
// are yielded too: the caller compares against instant and is the only place
// that decides what "already over" means.
func (s Schedule) upcomingWindows(instant time.Time) iter.Seq[window] {
	return func(yield func(window) bool) {
		year, month, day := instant.In(s.location).Date()
		for offset := range searchHorizonDays {
			for _, resolved := range s.windowsOn(year, month, day+offset) {
				if !yield(resolved) {
					return
				}
			}
		}
	}
}

// windowsOn resolves one local date's intervals into the absolute windows they
// actually occupy, ordered by start.
//
// A date may produce more windows than it has intervals, or fewer. Both come
// out of the same rule and neither is a special case: an interval is resolved
// once per zone offset the date is lived under, and clipped to the stretch that
// offset was actually in force for. Where a zone rewinds its clock both
// resolutions survive the clipping, and the interval genuinely happens twice.
// Where it jumps forward, an interval falling entirely in the skipped hour
// survives neither and produces no window at all — because no clock ever read
// those minutes, so there is nothing for a consumer to be woken up for.
//
// The date is normalised by time.Date, so a caller may pass a day past the end
// of its month and get the following month's, which is what the forward search
// relies on.
func (s Schedule) windowsOn(year int, month time.Month, day int) []window {
	// The civil date read as if it were UTC. It carries no offset of its own,
	// which is the point: it is the calendar half of a reading, and each zone
	// period below supplies the other half.
	civilMidnight := time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
	intervals := s.days[civilMidnight.Weekday()]
	if len(intervals) == 0 {
		return nil
	}

	var windows []window
	for _, lived := range s.periodsAround(civilMidnight) {
		for _, interval := range intervals {
			if resolved, ok := s.resolve(interval, civilMidnight, lived); ok {
				windows = append(windows, resolved)
			}
		}
	}
	slices.SortFunc(windows, func(a, b window) int { return a.start.Compare(b.start) })
	return windows
}

// resolve places one civil interval on the absolute timeline under one zone
// period, and reports whether any of it was actually lived.
//
// The clipping is what makes a skipped or a repeated hour fall out of the
// arithmetic instead of needing a rule of its own. An interval placed under an
// offset that was not in force for it lands wholly outside that period and is
// clipped to nothing; one that straddles the change keeps only the part the
// offset covers, and the rest comes back from the neighbouring period.
func (s Schedule) resolve(interval Interval, civilMidnight time.Time, lived zonePeriod) (window, bool) {
	start := s.absolute(civilMidnight, interval.Start, lived.offset)
	end := s.absolute(civilMidnight, interval.End, lived.offset)
	if !lived.start.IsZero() && start.Before(lived.start) {
		start = lived.start
	}
	if !lived.end.IsZero() && end.After(lived.end) {
		end = lived.end
	}
	if !start.Before(end) {
		return window{}, false
	}
	return window{interval: interval, start: start, end: end}, true
}

// absolute converts a civil reading of a date into the instant it names under
// one offset. No offset is guessed and none is hardcoded: the caller took it
// from the zone itself.
func (s Schedule) absolute(civilMidnight time.Time, minute Minutes, offset time.Duration) time.Time {
	return civilMidnight.Add(time.Duration(minute)*time.Minute - offset).In(s.location)
}

// zonePeriod is one uninterrupted stretch during which a zone held one offset.
// A zero start or end means the stretch is unbounded in that direction, which is
// what a zone with no transitions at all reports.
type zonePeriod struct {
	start  time.Time
	end    time.Time
	offset time.Duration
}

// zoneProbeMargin brackets one local date in absolute time.
//
// A zone offset is always inside ±24 hours of UTC, so local midnight of a civil
// date lies within a day either side of that same civil reading taken as UTC,
// and the local date ends at most a day after that. Probing from one margin
// before to one margin after therefore cannot miss an offset the date is lived
// under.
const zoneProbeMargin = 24 * time.Hour

// maxZonePeriods bounds the walk over zone transitions.
//
// The probe spans three days and no zone in the IANA database changes offset
// more than twice in that span. The bound is here so that malformed or hostile
// zone data cannot turn one evaluation into an unbounded walk, not because real
// data comes anywhere near it.
const maxZonePeriods = 8

// periodsAround returns the zone periods the given civil date is lived under,
// earliest first.
func (s Schedule) periodsAround(civilMidnight time.Time) []zonePeriod {
	cursor := civilMidnight.Add(-zoneProbeMargin).In(s.location)
	last := civilMidnight.Add(zoneProbeMargin + zoneProbeMargin)

	periods := make([]zonePeriod, 0, 2)
	for range maxZonePeriods {
		start, end := cursor.ZoneBounds()
		_, offsetSeconds := cursor.Zone()
		periods = append(periods, zonePeriod{
			start:  start,
			end:    end,
			offset: time.Duration(offsetSeconds) * time.Second,
		})
		if end.IsZero() || end.After(last) {
			break
		}
		cursor = end
	}
	return periods
}
