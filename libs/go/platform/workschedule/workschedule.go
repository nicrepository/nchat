// Package workschedule models when a person is at work, so that a later policy
// can decide what to do about it (issue #743, parent #678).
//
// # What it answers, and what it deliberately does not
//
// Given a schedule and an absolute instant, this package answers one question:
// was that instant inside working hours, outside them, or is there no schedule
// to answer with. It does not decide whether a notification is delivered. That
// decision belongs to the policy engine, which will combine this answer with
// mute preferences, priority, holidays and overrides — and which is free to
// deliver outside working hours or to suppress inside them. Putting the
// delivery decision here would make the temporal rule impossible to reuse and
// impossible to test without a delivery channel, which is the coupling
// notificationevent already refuses for the same reason.
//
// # not_configured is a state, not a default
//
// The absence of a schedule is represented, never invented. A missing schedule
// is not "09:00-18:00", not "always allowed" and not "always blocked": it is
// StateNotConfigured, and what to do about it is a product decision the policy
// engine owns. A boolean return would have lost exactly this distinction, which
// is why Evaluate returns a state.
//
// # The timezone is the authority, never the server
//
// A schedule is civil time — "07:00" means seven in the morning where the
// person is — plus an IANA zone that turns civil time into instants. So
// evaluation takes an absolute instant from the caller, converts it into that
// zone, and reads the schedule of the resulting local weekday. Nothing here
// reads the process's own zone, no offset is ever stored or computed by hand,
// and no clock is read: Evaluate is a pure function of (schedule, instant),
// which is what makes it replayable and makes every boundary in the tests exact.
//
// # Local readings a zone skips or repeats
//
// A civil reading is not an instant. When a zone rewinds its clock, "01:15"
// names two of them; when it jumps forward, it may name none. So a schedule is
// resolved against a date and a zone into the absolute stretches it actually
// occupies, and every question is answered from those: a repeated interval is
// worked twice, and an interval inside the skipped hour is worked not at all.
// Both fall out of the same resolution rather than from a rule about daylight
// saving, and both halves of an Evaluation are read off it in one pass, so the
// state and the transition cannot disagree about which timeline they are on.
//
// # What the model cannot express, on purpose
//
// An interval never crosses midnight. A shift from 22:00 to 06:00 is two
// intervals on two days, which is also how it reads on a roster. Allowing an
// interval to wrap would make every containment check and every boundary search
// carry a special case, in exchange for a shape the day-based model already has
// a natural spelling for.
//
// # Extension
//
// Holidays, vacation and temporary overrides compose *on top* of this answer;
// they are not sources this package consults. Nothing is declared here for
// them, because an interface with no implementation is a guess about a consumer
// that does not exist yet. What this package guarantees is only that adding
// them costs nothing here: an availability decision that starts from a
// work-schedule state can subtract days without the schedule knowing.
package workschedule

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	// The runtime image is distroless (see Dockerfile.service) and ships no
	// /usr/share/zoneinfo, so every zone other than "UTC" would fail to load in
	// production while working on a developer's machine. auth-service embeds
	// the database for the same reason where it validates auth.users.timezone;
	// a schedule whose zone cannot be loaded is a schedule that cannot be
	// evaluated at all, so the dependency is not left to the environment.
	_ "time/tzdata"
)

// ErrInvalidSchedule is returned when a schedule cannot be built. One sentinel,
// wrapped with the detail: every caller's response is the same — refuse the
// input — and a caller that branched on the kind of invalidity would be
// re-implementing the validation it just called.
var ErrInvalidSchedule = errors.New("invalid work schedule")

// Source is where a schedule came from, and therefore who may change it.
//
// It exists because "the company says these are the hours" and "I said these
// are my hours" are different claims with different authority, and a consumer
// that cannot tell them apart cannot enforce the first one.
type Source string

const (
	// SourceOrganization is a schedule set by the organization. Read-only to
	// the person it applies to.
	SourceOrganization Source = "organization"
	// SourcePersonal is a schedule the person set for themselves. Declared
	// because the read-only distinction is meaningless without the other half;
	// no producer writes it yet.
	SourcePersonal Source = "personal"
)

var sources = map[Source]struct{}{
	SourceOrganization: {},
	SourcePersonal:     {},
}

// Valid reports whether s is one of the declared sources. The zero value is
// never valid, which is the point: an unset source must not read as a usable
// one.
func (s Source) Valid() bool {
	_, ok := sources[s]
	return ok
}

// ReadOnly reports whether the person the schedule applies to may not change it.
//
// Written as "anything that is not personal" rather than "organization", so it
// fails closed: a source this build does not know — a row written by a newer
// release, a value that survived a migration — is treated as somebody else's
// policy rather than as the user's own. Getting that backwards would let an
// unrecognised value become a permission.
func (s Source) ReadOnly() bool {
	return s != SourcePersonal
}

// Minutes is a local wall-clock time, counted in minutes from local midnight.
//
// Minutes rather than a time.Time because a schedule has no date: "07:00" is
// the same rule every Monday, and a date attached to it would be a date that
// has to be ignored everywhere. Minutes rather than a string because the only
// thing anything does with it is compare and order it.
type Minutes int

// MinutesPerDay is the exclusive upper bound of a start and the inclusive upper
// bound of an end. An interval may finish at midnight; none may begin there and
// run past it.
const MinutesPerDay Minutes = 24 * 60

// At builds a local wall-clock time from hours and minutes. Out-of-range values
// are not corrected here — they are refused by New, where refusing them is
// visible.
func At(hour, minute int) Minutes {
	return Minutes(hour*60 + minute)
}

// Interval is one continuous stretch of working time within a single local day,
// half-open as [Start, End).
//
// Half-open is what makes an instant belong to exactly one interval and makes a
// boundary unambiguous: at exactly End the person is already outside, and the
// next interval that begins at that same minute would own it. Adjacent
// intervals are refused for a different reason — see New.
//
// It is a civil reading and nothing more. Turning it into instants is the job of
// Evaluate, which resolves it against the date and the zone, and that is where
// a reading the zone skipped or repeated is accounted for.
type Interval struct {
	Start Minutes
	End   Minutes
}

// Days holds the intervals of each weekday, indexed by time.Weekday.
//
// An array and not a map: the weekdays are a closed, ordered set, iteration
// over it has to be deterministic for the boundary search, and a missing key
// and a day off would otherwise be the same absence spelled two ways. A day
// with no intervals is a day with no work — a Sunday, a public holiday the
// roster already accounts for — and needs no separate representation.
type Days [7][]Interval

// Schedule is a validated weekly work schedule in one IANA time zone.
//
// The fields are unexported and the only constructor validates, so a Schedule
// that exists is a Schedule that can be evaluated: Evaluate has no error to
// return and no invariant left to re-check on a hot path. The zero value is a
// real and meaningful value — the absence of a schedule — which is why it is
// not a pointer.
type Schedule struct {
	source   Source
	location *time.Location
	days     Days
}

// New validates a schedule and returns it.
//
// Intervals are sorted by start time before they are checked, so the caller is
// not required to supply them in order. Sorting cannot hide a bad input: every
// rule below is order-independent once applied to a sorted day, and nothing is
// merged, clamped or dropped.
//
// Refused:
//   - a source this build does not declare;
//   - an empty timezone, "Local" (the server's own zone, not a zone anyone
//     chose), or a name the IANA database does not have;
//   - an interval starting outside 00:00-24:00, ending past midnight, or ending
//     at or before it starts — which covers both start == end and start > end;
//   - two intervals on one day that overlap, that are equal, or that merely
//     touch.
//
// Touching intervals — 07:00-12:00 followed by 12:00-16:00 — are refused rather
// than merged, and that is the one rule that needs a reason. Under [Start, End)
// they are not ambiguous about containment; they are ambiguous about change.
// The boundary at 12:00 would be reported as the next state transition while
// nothing about the state actually changes there, so a consumer waiting for the
// end of the working block would wake up in the middle of it. One block is one
// interval, and saying so is the caller's job.
//
// An empty schedule — no interval on any day — is accepted. It says the person
// never works, which is a different statement from having no schedule at all,
// and the two must not collapse into one another.
func New(source Source, timezone string, days Days) (Schedule, error) {
	if !source.Valid() {
		return Schedule{}, fmt.Errorf("%w: unknown source %q", ErrInvalidSchedule, source)
	}
	location, err := loadLocation(timezone)
	if err != nil {
		return Schedule{}, err
	}
	normalized, err := normalizeDays(days)
	if err != nil {
		return Schedule{}, err
	}
	return Schedule{source: source, location: location, days: normalized}, nil
}

// loadLocation resolves an IANA name against the real time zone database.
//
// A regular expression would accept "America/Sao_Pualo" and every other
// plausible-looking name that resolves to nothing; only the database can say
// whether a zone exists, and it is the same check auth-service applies before
// storing auth.users.timezone.
func loadLocation(timezone string) (*time.Location, error) {
	name := strings.TrimSpace(timezone)
	if name == "" {
		return nil, fmt.Errorf("%w: timezone is required", ErrInvalidSchedule)
	}
	if name == "Local" {
		return nil, fmt.Errorf("%w: %q names the server's own zone, not an IANA zone", ErrInvalidSchedule, name)
	}
	location, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("%w: %q is not an IANA time zone", ErrInvalidSchedule, name)
	}
	return location, nil
}

// maxIntervalsPerDay is the largest number of intervals a valid day can hold,
// and it is arithmetic rather than a policy: intervals may not touch and may not
// be empty, so each one costs at least a minute of work plus a minute of gap,
// and a 1440-minute day fits 720 of them.
//
// It is checked before the sort so that a list longer than this is refused
// without being processed at all. A day carrying a million intervals is not a
// schedule anyone could work — it is a request shaped to make validation
// expensive — and it is provably invalid without looking at a single value.
const maxIntervalsPerDay = int(MinutesPerDay) / 2

// normalizeDays returns the schedule's days sorted and validated, leaving the
// caller's slices untouched.
func normalizeDays(days Days) (Days, error) {
	var normalized Days
	for weekday, intervals := range days {
		if len(intervals) == 0 {
			continue
		}
		if len(intervals) > maxIntervalsPerDay {
			return Days{}, fmt.Errorf("%w: %s carries %d intervals, more than a day can hold",
				ErrInvalidSchedule, time.Weekday(weekday), len(intervals))
		}
		sorted := slices.Clone(intervals)
		slices.SortFunc(sorted, func(a, b Interval) int { return cmp.Compare(a.Start, b.Start) })
		if err := validateDay(time.Weekday(weekday), sorted); err != nil {
			return Days{}, err
		}
		normalized[weekday] = sorted
	}
	return normalized, nil
}

// validateDay checks one day's intervals, which are already sorted by start.
func validateDay(weekday time.Weekday, intervals []Interval) error {
	// Below every legal start, so the first interval is compared against
	// nothing and every later one against its predecessor's end.
	previousEnd := Minutes(-1)
	for _, interval := range intervals {
		if err := validateInterval(weekday, interval); err != nil {
			return err
		}
		if interval.Start <= previousEnd {
			return fmt.Errorf("%w: %s intervals overlap or touch at minute %d",
				ErrInvalidSchedule, weekday, interval.Start)
		}
		previousEnd = interval.End
	}
	return nil
}

// validateInterval checks one interval against the bounds of a single day.
func validateInterval(weekday time.Weekday, interval Interval) error {
	if interval.Start < 0 || interval.Start >= MinutesPerDay {
		return fmt.Errorf("%w: %s start minute %d is outside 00:00-24:00",
			ErrInvalidSchedule, weekday, interval.Start)
	}
	if interval.End <= interval.Start || interval.End > MinutesPerDay {
		return fmt.Errorf("%w: %s interval %d-%d is empty, reversed or runs past midnight",
			ErrInvalidSchedule, weekday, interval.Start, interval.End)
	}
	return nil
}

// IsConfigured reports whether there is a schedule at all. False for the zero
// value, and for nothing else: New never returns an unconfigured Schedule.
func (s Schedule) IsConfigured() bool {
	return s.location != nil
}

// Source returns where the schedule came from. The zero Schedule has no source,
// and the invalid zero Source is read-only — an absent policy is not the user's
// to edit.
func (s Schedule) Source() Source {
	return s.source
}

// Timezone returns the IANA name the schedule is expressed in, or "" when there
// is no schedule.
func (s Schedule) Timezone() string {
	if !s.IsConfigured() {
		return ""
	}
	return s.location.String()
}
