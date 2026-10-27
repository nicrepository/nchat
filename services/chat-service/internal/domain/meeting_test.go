package domain_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

const (
	meetingWorkspace = "6a1c0000-0000-4000-8000-000000000001"
	meetingOrganizer = "6a1c0000-0000-4000-8000-000000000002"
	meetingChannel   = "6a1c0000-0000-4000-8000-000000000003"
)

var meetingStart = time.Date(2026, 11, 3, 13, 0, 0, 0, time.UTC)

func meetingDetails() domain.MeetingDetails {
	return domain.MeetingDetails{
		Title:    "Planejamento",
		StartsAt: meetingStart,
		EndsAt:   meetingStart.Add(time.Hour),
		Timezone: "America/Sao_Paulo",
	}
}

func newTestMeeting(t *testing.T, conversation *domain.MeetingConversation, details domain.MeetingDetails) domain.Meeting {
	t.Helper()
	m, err := domain.NewMeeting(meetingWorkspace, meetingOrganizer, conversation, details)
	if err != nil {
		t.Fatalf("NewMeeting: %v", err)
	}
	return m
}

func TestNewMeetingBuildsAScheduledRestrictiveMeeting(t *testing.T) {
	details := meetingDetails()
	details.Title = "  Planejamento  "
	details.Description = "Pauta:\n\t1. números\r\n"
	conversation := &domain.MeetingConversation{Kind: domain.MeetingConversationChannel, ID: strings.ToUpper(meetingChannel)}

	m := newTestMeeting(t, conversation, details)

	want := domain.Meeting{
		ID:              m.ID,
		WorkspaceID:     meetingWorkspace,
		Conversation:    &domain.MeetingConversation{Kind: domain.MeetingConversationChannel, ID: meetingChannel},
		OrganizerUserID: meetingOrganizer,
		MeetingDetails: domain.MeetingDetails{
			Title: "Planejamento", Description: "Pauta:\n\t1. números",
			StartsAt: meetingStart, EndsAt: meetingStart.Add(time.Hour),
			Timezone: "America/Sao_Paulo", JoinPolicy: domain.MeetingJoinInvitedOnly,
		},
		Status:  domain.MeetingStatusScheduled,
		RoomKey: "meeting:" + m.ID,
		Version: 1,
	}
	if m.ID == "" || m.Conversation == nil || *m.Conversation != *want.Conversation {
		t.Fatalf("identity = %q conversation = %+v", m.ID, m.Conversation)
	}
	m.Conversation, want.Conversation = nil, nil
	if m != want {
		t.Fatalf("meeting = %+v, want %+v", m, want)
	}
}

func TestNewMeetingGivesEveryMeetingItsOwnIdentity(t *testing.T) {
	a, b := newTestMeeting(t, nil, meetingDetails()), newTestMeeting(t, nil, meetingDetails())
	if a.ID == b.ID || a.RoomKey == b.RoomKey {
		t.Fatalf("two meetings share an identity: %q/%q", a.ID, b.ID)
	}
}

func TestNewMeetingRejectsInvalidInput(t *testing.T) {
	channel := &domain.MeetingConversation{Kind: domain.MeetingConversationChannel, ID: meetingChannel}
	tests := []struct {
		name         string
		conversation *domain.MeetingConversation
		change       func(*domain.MeetingDetails)
		want         error
	}{
		{name: "empty title", change: func(d *domain.MeetingDetails) { d.Title = "" }, want: domain.ErrMeetingTitleRequired},
		{name: "blank title", change: func(d *domain.MeetingDetails) { d.Title = " \t\n " }, want: domain.ErrMeetingTitleRequired},
		{name: "long title", change: func(d *domain.MeetingDetails) { d.Title = strings.Repeat("é", 121) }, want: domain.ErrMeetingTitleInvalid},
		{name: "title line break", change: func(d *domain.MeetingDetails) { d.Title = "a\nb" }, want: domain.ErrMeetingTitleInvalid},
		{name: "title NUL", change: func(d *domain.MeetingDetails) { d.Title = "a\x00b" }, want: domain.ErrMeetingTitleInvalid},
		{name: "title invalid UTF-8", change: func(d *domain.MeetingDetails) { d.Title = "a\xffb" }, want: domain.ErrMeetingTitleInvalid},
		{name: "long description", change: func(d *domain.MeetingDetails) { d.Description = strings.Repeat("x", 5001) }, want: domain.ErrMeetingDescriptionInvalid},
		{name: "description bell", change: func(d *domain.MeetingDetails) { d.Description = "a\ab" }, want: domain.ErrMeetingDescriptionInvalid},
		{name: "missing start", change: func(d *domain.MeetingDetails) { d.StartsAt = time.Time{} }, want: domain.ErrMeetingInvalidTimeRange},
		{name: "empty interval", change: func(d *domain.MeetingDetails) { d.EndsAt = d.StartsAt }, want: domain.ErrMeetingInvalidTimeRange},
		{name: "ends before start", change: func(d *domain.MeetingDetails) { d.EndsAt = d.StartsAt.Add(-time.Minute) }, want: domain.ErrMeetingInvalidTimeRange},
		{name: "sub-microsecond interval", change: func(d *domain.MeetingDetails) { d.EndsAt = d.StartsAt.Add(time.Nanosecond) }, want: domain.ErrMeetingInvalidTimeRange},
		{name: "missing timezone", change: func(d *domain.MeetingDetails) { d.Timezone = "" }, want: domain.ErrMeetingInvalidTimezone},
		{name: "server timezone", change: func(d *domain.MeetingDetails) { d.Timezone = "Local" }, want: domain.ErrMeetingInvalidTimezone},
		{name: "negative offset", change: func(d *domain.MeetingDetails) { d.Timezone = "-03:00" }, want: domain.ErrMeetingInvalidTimezone},
		{name: "positive offset", change: func(d *domain.MeetingDetails) { d.Timezone = "+01:00" }, want: domain.ErrMeetingInvalidTimezone},
		{name: "misspelled zone", change: func(d *domain.MeetingDetails) { d.Timezone = "America/Sao_Pualo" }, want: domain.ErrMeetingInvalidTimezone},
		{name: "path traversal", change: func(d *domain.MeetingDetails) { d.Timezone = "../../etc/passwd" }, want: domain.ErrMeetingInvalidTimezone},
		{name: "oversized zone", change: func(d *domain.MeetingDetails) { d.Timezone = strings.Repeat("A", 65) }, want: domain.ErrMeetingInvalidTimezone},
		{name: "unknown join policy", change: func(d *domain.MeetingDetails) { d.JoinPolicy = "public" }, want: domain.ErrMeetingInvalidJoinPolicy},
		{name: "conversation policy without conversation", change: func(d *domain.MeetingDetails) {
			d.JoinPolicy = domain.MeetingJoinConversationMembers
		}, want: domain.ErrMeetingConversationRequired},
		{name: "unknown conversation kind", conversation: &domain.MeetingConversation{Kind: "group", ID: meetingChannel}, want: domain.ErrInvalidInput},
		{name: "invalid conversation id", conversation: &domain.MeetingConversation{Kind: domain.MeetingConversationDM, ID: "x"}, want: domain.ErrInvalidInput},
		{name: "valid conversation still validates details", conversation: channel, change: func(d *domain.MeetingDetails) { d.Title = "" }, want: domain.ErrMeetingTitleRequired},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			details := meetingDetails()
			if tt.change != nil {
				tt.change(&details)
			}
			_, err := domain.NewMeeting(meetingWorkspace, meetingOrganizer, tt.conversation, details)
			if !errors.Is(err, tt.want) || !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestNewMeetingRejectsNonUUIDReferences(t *testing.T) {
	for _, ids := range [][2]string{{"workspace", meetingOrganizer}, {meetingWorkspace, ""}} {
		if _, err := domain.NewMeeting(ids[0], ids[1], nil, meetingDetails()); !errors.Is(err, domain.ErrInvalidInput) {
			t.Fatalf("NewMeeting(%q, %q) err = %v", ids[0], ids[1], err)
		}
	}
}

func TestNewMeetingAcceptsBoundaryValues(t *testing.T) {
	details := meetingDetails()
	details.Title = strings.Repeat("é", domain.MaxMeetingTitleCodePoints)
	details.Description = strings.Repeat("d", domain.MaxMeetingDescriptionCodePoints)
	details.Timezone = " UTC "
	m := newTestMeeting(t, nil, details)
	if m.Timezone != "UTC" {
		t.Fatalf("timezone = %q, want UTC", m.Timezone)
	}
}

// Any positive interval is a meeting: no requirement caps its length, and a
// civil day is not always 24 hours.
func TestMeetingAcceptsAnyPositiveDuration(t *testing.T) {
	newYork := mustLocation(t, "America/New_York")
	tests := []struct {
		name       string
		start, end time.Time
		want       time.Duration
	}{
		{"one microsecond", meetingStart, meetingStart.Add(time.Microsecond), time.Microsecond},
		{"exactly a day", meetingStart, meetingStart.Add(24 * time.Hour), 24 * time.Hour},
		{"twenty-five hours", meetingStart, meetingStart.Add(25 * time.Hour), 25 * time.Hour},
		{"several days", meetingStart, meetingStart.AddDate(0, 0, 3), 72 * time.Hour},
		// The fall-back day: civil midnight to civil midnight lasts 25 hours.
		{"a civil day across a DST change", time.Date(2026, 11, 1, 0, 0, 0, 0, newYork), time.Date(2026, 11, 2, 0, 0, 0, 0, newYork), 25 * time.Hour},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			details := meetingDetails()
			details.StartsAt, details.EndsAt, details.Timezone = tt.start, tt.end, "America/New_York"
			m := newTestMeeting(t, nil, details)
			if got := m.EndsAt.Sub(m.StartsAt); got != tt.want {
				t.Fatalf("duration = %s, want %s", got, tt.want)
			}
		})
	}
}

// The domain stores absolute instants plus the civil zone as metadata. It never
// interprets wall-clock values, so a DST gap or a midnight crossing is just an
// interval between two instants, and the zone is never derived from an offset.
func TestMeetingTimezoneIsMetadataBesideAbsoluteInstants(t *testing.T) {
	newYork, saoPaulo := mustLocation(t, "America/New_York"), mustLocation(t, "America/Sao_Paulo")
	tests := []struct {
		name           string
		zone           string
		start, end     time.Time
		wantDuration   time.Duration
		wantStartInUTC time.Time
	}{
		{
			// 01:30 EST to 03:30 EDT on the spring-forward day is one real hour.
			name: "across a DST gap", zone: "America/New_York",
			start:        time.Date(2026, 3, 8, 1, 30, 0, 0, newYork),
			end:          time.Date(2026, 3, 8, 3, 30, 0, 0, newYork),
			wantDuration: time.Hour, wantStartInUTC: time.Date(2026, 3, 8, 6, 30, 0, 0, time.UTC),
		},
		{
			// 01:30 occurs twice on the fall-back day; the instant decides which.
			name: "inside a DST overlap", zone: "America/New_York",
			start:        time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC),
			end:          time.Date(2026, 11, 1, 6, 30, 0, 0, time.UTC),
			wantDuration: time.Hour, wantStartInUTC: time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC),
		},
		{
			name: "across civil midnight", zone: "America/Sao_Paulo",
			start:        time.Date(2026, 12, 31, 23, 0, 0, 0, saoPaulo),
			end:          time.Date(2027, 1, 1, 1, 0, 0, 0, saoPaulo),
			wantDuration: 2 * time.Hour, wantStartInUTC: time.Date(2027, 1, 1, 2, 0, 0, 0, time.UTC),
		},
		{
			name: "zone is not derived from the input offset", zone: "Europe/London",
			start:        time.Date(2026, 7, 1, 9, 0, 0, 123456789, time.FixedZone("-03:00", -3*3600)),
			end:          time.Date(2026, 7, 1, 10, 0, 0, 123456789, time.FixedZone("-03:00", -3*3600)),
			wantDuration: time.Hour, wantStartInUTC: time.Date(2026, 7, 1, 12, 0, 0, 123456000, time.UTC),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			details := meetingDetails()
			details.StartsAt, details.EndsAt, details.Timezone = tt.start, tt.end, tt.zone
			m := newTestMeeting(t, nil, details)
			got := storedInterval{m.Timezone, m.StartsAt, m.EndsAt.Location(), m.EndsAt.Sub(m.StartsAt)}
			if want := (storedInterval{tt.zone, tt.wantStartInUTC, time.UTC, tt.wantDuration}); got != want {
				t.Fatalf("stored %+v, want %+v", got, want)
			}
		})
	}
}

type storedInterval struct {
	zone         string
	start        time.Time
	endsLocation *time.Location
	duration     time.Duration
}

func mustLocation(t *testing.T, name string) *time.Location {
	t.Helper()
	location, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return location
}

var (
	statusScheduled  = domain.MeetingStatusScheduled
	statusInProgress = domain.MeetingStatusInProgress
	statusEnded      = domain.MeetingStatusEnded
	statusCancelled  = domain.MeetingStatusCancelled
)

func transitionFrom(t *testing.T, from, to domain.MeetingStatus) (domain.Meeting, bool, error) {
	t.Helper()
	m := newTestMeeting(t, nil, meetingDetails())
	m.Status, m.Version = from, 7
	return m.Transition(to, 7)
}

// These three are the whole state machine.
func TestMeetingAllowedTransitions(t *testing.T) {
	for _, pair := range [][2]domain.MeetingStatus{{statusScheduled, statusInProgress}, {statusScheduled, statusCancelled}, {statusInProgress, statusEnded}} {
		got, changed, err := transitionFrom(t, pair[0], pair[1])
		if err != nil || !changed || got.Status != pair[1] || got.Version != 7 {
			t.Errorf("%s→%s: status=%s version=%d changed=%v err=%v", pair[0], pair[1], got.Status, got.Version, changed, err)
		}
	}
}

func TestMeetingRefusedTransitions(t *testing.T) {
	refused := [][2]domain.MeetingStatus{
		{statusScheduled, statusEnded},
		{statusInProgress, statusScheduled}, {statusInProgress, statusCancelled},
		{statusEnded, statusScheduled}, {statusEnded, statusInProgress}, {statusEnded, statusCancelled},
		{statusCancelled, statusScheduled}, {statusCancelled, statusInProgress}, {statusCancelled, statusEnded},
	}
	for _, pair := range refused {
		got, changed, err := transitionFrom(t, pair[0], pair[1])
		if !errors.Is(err, domain.ErrMeetingInvalidState) || changed || got.Status != pair[0] {
			t.Errorf("%s→%s: status=%s changed=%v err=%v, want refusal", pair[0], pair[1], got.Status, changed, err)
		}
	}
}

func TestMeetingRepeatedTransitionIsANoOp(t *testing.T) {
	for _, status := range []domain.MeetingStatus{statusScheduled, statusInProgress, statusEnded, statusCancelled} {
		got, changed, err := transitionFrom(t, status, status)
		if err != nil || changed || got.Status != status || got.Version != 7 {
			t.Errorf("%s repeated: changed=%v err=%v", status, changed, err)
		}
	}
}

func TestMeetingTerminalStatuses(t *testing.T) {
	want := map[domain.MeetingStatus]bool{statusScheduled: false, statusInProgress: false, statusEnded: true, statusCancelled: true}
	for status, terminal := range want {
		if !status.Valid() || status.Terminal() != terminal {
			t.Errorf("%s: Valid=%v Terminal=%v", status, status.Valid(), status.Terminal())
		}
	}
}

func TestMeetingTransitionRefusesStaleAndUnknownRequests(t *testing.T) {
	m := newTestMeeting(t, nil, meetingDetails())
	if _, _, err := m.Transition(domain.MeetingStatusCancelled, 2); !errors.Is(err, domain.ErrMeetingStaleVersion) {
		t.Fatalf("stale cancel err = %v", err)
	}
	if _, _, err := m.Transition("archived", 1); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("unknown status err = %v", err)
	}
	// A replay of a transition that already happened is not a stale write: the
	// caller's intended state holds and nothing is written.
	m.Status, m.Version = domain.MeetingStatusCancelled, 2
	if _, changed, err := m.Transition(domain.MeetingStatusCancelled, 1); err != nil || changed {
		t.Fatalf("cancel replay changed=%v err=%v", changed, err)
	}
}

func TestMeetingUpdateChangesOnlyDetails(t *testing.T) {
	m := newTestMeeting(t, &domain.MeetingConversation{Kind: domain.MeetingConversationDM, ID: meetingChannel}, meetingDetails())
	details := meetingDetails()
	details.Title = "Retro"
	details.Timezone = "Europe/London"
	details.JoinPolicy = domain.MeetingJoinConversationMembers

	got, err := m.Update(details, 1)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	want := m
	want.Title, want.Timezone, want.JoinPolicy = "Retro", "Europe/London", domain.MeetingJoinConversationMembers
	if got != want {
		t.Fatalf("updated = %+v, want %+v", got, want)
	}
}

func TestMeetingUpdateRefusals(t *testing.T) {
	tests := []struct {
		name    string
		status  domain.MeetingStatus
		version int64
		title   string
		want    error
	}{
		{"stale version", domain.MeetingStatusScheduled, 0, "x", domain.ErrMeetingStaleVersion},
		{"ended", domain.MeetingStatusEnded, 1, "x", domain.ErrMeetingInvalidState},
		{"cancelled", domain.MeetingStatusCancelled, 1, "x", domain.ErrMeetingInvalidState},
		{"invalid details", domain.MeetingStatusInProgress, 1, "", domain.ErrMeetingTitleRequired},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newTestMeeting(t, nil, meetingDetails())
			m.Status = tt.status
			details := meetingDetails()
			details.Title = tt.title
			got, err := m.Update(details, tt.version)
			if !errors.Is(err, tt.want) || got != m {
				t.Fatalf("err = %v, meeting changed = %v", err, got != m)
			}
		})
	}
}

func TestMeetingClosedValueSets(t *testing.T) {
	for _, valid := range []bool{
		domain.MeetingJoinInvitedOnly.Valid(), domain.MeetingJoinConversationMembers.Valid(), domain.MeetingJoinWorkspaceLink.Valid(),
		domain.MeetingRoleOrganizer.Valid(), domain.MeetingRoleCohost.Valid(), domain.MeetingRoleAttendee.Valid(),
		domain.MeetingRSVPPending.Valid(), domain.MeetingRSVPAccepted.Valid(), domain.MeetingRSVPDeclined.Valid(), domain.MeetingRSVPTentative.Valid(),
	} {
		if !valid {
			t.Fatal("a declared value is not valid")
		}
	}
	for _, valid := range []bool{
		domain.MeetingJoinPolicy("").Valid(), domain.MeetingRole("owner").Valid(),
		domain.MeetingRSVP("maybe").Valid(), domain.MeetingStatus("paused").Valid(),
	} {
		if valid {
			t.Fatal("an undeclared value is valid")
		}
	}
}
