package domain

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	// The runtime image is distroless and ships no zoneinfo; embedding the IANA
	// database keeps timezone validation identical in production and on a
	// developer's machine (the same reason auth-service and workschedule do).
	_ "time/tzdata"

	"github.com/google/uuid"
)

// MeetingStatus is a meeting's persisted lifecycle state (issue #1052). It is
// changed only by an explicit, authorized transition — never by the clock:
// reaching ends_at does not end a meeting.
type MeetingStatus string

const (
	MeetingStatusScheduled  MeetingStatus = "scheduled"
	MeetingStatusInProgress MeetingStatus = "in_progress"
	MeetingStatusEnded      MeetingStatus = "ended"
	MeetingStatusCancelled  MeetingStatus = "cancelled"
)

// meetingTransitions is the whole state machine. Terminal states have no exit,
// and an in-progress meeting is ended rather than cancelled.
var meetingTransitions = map[MeetingStatus][]MeetingStatus{
	MeetingStatusScheduled:  {MeetingStatusInProgress, MeetingStatusCancelled},
	MeetingStatusInProgress: {MeetingStatusEnded},
}

func (s MeetingStatus) Valid() bool {
	switch s {
	case MeetingStatusScheduled, MeetingStatusInProgress, MeetingStatusEnded, MeetingStatusCancelled:
		return true
	default:
		return false
	}
}

func (s MeetingStatus) Terminal() bool {
	return s == MeetingStatusEnded || s == MeetingStatusCancelled
}

func (s MeetingStatus) CanTransitionTo(next MeetingStatus) bool {
	return slices.Contains(meetingTransitions[s], next)
}

// MeetingJoinPolicy says who, besides invited participants, may see and join a
// meeting. The zero value normalizes to the restrictive default.
type MeetingJoinPolicy string

const (
	MeetingJoinInvitedOnly         MeetingJoinPolicy = "invited_only"
	MeetingJoinConversationMembers MeetingJoinPolicy = "conversation_members"
	// MeetingJoinWorkspaceLink admits workspace members (not guests) who hold
	// the meeting's link, i.e. its id. It grants no listing: knowing the id is
	// the precondition, and no secret token exists.
	MeetingJoinWorkspaceLink MeetingJoinPolicy = "workspace_members_with_link"
)

func (p MeetingJoinPolicy) Valid() bool {
	return p == MeetingJoinInvitedOnly || p == MeetingJoinConversationMembers || p == MeetingJoinWorkspaceLink
}

func (p MeetingJoinPolicy) orDefault() MeetingJoinPolicy {
	if p == "" {
		return MeetingJoinInvitedOnly
	}
	return p
}

type MeetingRole string

const (
	MeetingRoleOrganizer MeetingRole = "organizer"
	MeetingRoleCohost    MeetingRole = "cohost"
	MeetingRoleAttendee  MeetingRole = "attendee"
)

func (r MeetingRole) Valid() bool {
	return r == MeetingRoleOrganizer || r == MeetingRoleCohost || r == MeetingRoleAttendee
}

// assignable reports whether a roster change may leave a participant with r;
// the empty role means removal. The organizer role is never assigned.
func (r MeetingRole) assignable() bool {
	return r == "" || r == MeetingRoleCohost || r == MeetingRoleAttendee
}

// MeetingRSVP is a participant's own answer. It grants nothing.
type MeetingRSVP string

const (
	MeetingRSVPPending   MeetingRSVP = "pending"
	MeetingRSVPAccepted  MeetingRSVP = "accepted"
	MeetingRSVPDeclined  MeetingRSVP = "declined"
	MeetingRSVPTentative MeetingRSVP = "tentative"
)

func (r MeetingRSVP) Valid() bool {
	switch r {
	case MeetingRSVPPending, MeetingRSVPAccepted, MeetingRSVPDeclined, MeetingRSVPTentative:
		return true
	default:
		return false
	}
}

// MeetingConversationKind uses the same two values calls use for their targets.
type MeetingConversationKind string

const (
	MeetingConversationChannel MeetingConversationKind = "channel"
	MeetingConversationDM      MeetingConversationKind = "dm"
)

// MeetingConversation is the conversation a meeting was scheduled in. It is a
// reference, never a grant: access to it is resolved server-side.
type MeetingConversation struct {
	Kind MeetingConversationKind
	ID   string
}

// MeetingDetails is everything about a meeting an edit may change. Identity,
// workspace, organizer, conversation and room key are deliberately not here.
type MeetingDetails struct {
	Title       string
	Description string
	StartsAt    time.Time
	EndsAt      time.Time
	// Timezone is the civil IANA zone the meeting was scheduled in. It is
	// metadata for presenting the instants, never used to reinterpret them.
	Timezone   string
	JoinPolicy MeetingJoinPolicy
}

type Meeting struct {
	ID              string
	WorkspaceID     string
	Conversation    *MeetingConversation
	OrganizerUserID string
	MeetingDetails
	Status  MeetingStatus
	RoomKey string
	// Version increases with every persisted change of the meeting row.
	Version   int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

type MeetingParticipant struct {
	MeetingID string
	UserID    string
	Role      MeetingRole
	RSVP      MeetingRSVP
	InvitedAt time.Time
	UpdatedAt time.Time
}

const (
	// MaxMeetingTitleCodePoints matches the group title cap: a label, not prose.
	MaxMeetingTitleCodePoints = 120
	// MaxMeetingDescriptionCodePoints leaves room for an agenda while staying
	// far below a message body.
	MaxMeetingDescriptionCodePoints = 5000
	maxMeetingTimezoneBytes         = 64
)

// None of these repeats the rejected value.
var (
	ErrMeetingTitleRequired        = fmt.Errorf("%w: meeting title is required", ErrInvalidInput)
	ErrMeetingTitleInvalid         = fmt.Errorf("%w: meeting title must be at most %d characters without control characters", ErrInvalidInput, MaxMeetingTitleCodePoints)
	ErrMeetingDescriptionInvalid   = fmt.Errorf("%w: meeting description must be at most %d characters without control characters", ErrInvalidInput, MaxMeetingDescriptionCodePoints)
	ErrMeetingInvalidTimeRange     = fmt.Errorf("%w: meeting must end after it starts", ErrInvalidInput)
	ErrMeetingInvalidTimezone      = fmt.Errorf("%w: timezone must be an IANA time zone name", ErrInvalidInput)
	ErrMeetingInvalidJoinPolicy    = fmt.Errorf("%w: unknown meeting join policy", ErrInvalidInput)
	ErrMeetingConversationRequired = fmt.Errorf("%w: conversation_members requires a conversation", ErrInvalidInput)
	// ErrMeetingParticipantNotEligible is non-enumerating: an unknown user,
	// another workspace's user, an inactive account and a user outside the
	// meeting's conversation are all this one error.
	ErrMeetingParticipantNotEligible = fmt.Errorf("%w: user cannot be invited to this meeting", ErrInvalidInput)
	ErrMeetingParticipantExists      = fmt.Errorf("%w: user is already a meeting participant", ErrConflict)
	// ErrMeetingStaleVersion is a write based on a version that is no longer
	// current. Nothing was changed.
	ErrMeetingStaleVersion = fmt.Errorf("%w: meeting version is stale", ErrConflict)
	// ErrMeetingInvalidState is an operation the meeting's status forbids,
	// including any change to an ended or cancelled meeting.
	ErrMeetingInvalidState = fmt.Errorf("%w: meeting status does not allow this operation", ErrConflict)
)

// NewMeeting builds a scheduled meeting. workspaceID and organizerUserID must
// come from the authenticated session, never from a request body; whether the
// organizer may create it is AuthorizeMeeting's decision, made by the store.
func NewMeeting(workspaceID, organizerUserID string, conversation *MeetingConversation, details MeetingDetails) (Meeting, error) {
	workspace, errWorkspace := uuid.Parse(workspaceID)
	organizer, errOrganizer := uuid.Parse(organizerUserID)
	if err := errors.Join(errWorkspace, errOrganizer); err != nil {
		return Meeting{}, ErrInvalidInput
	}
	conversation, err := canonicalMeetingConversation(conversation)
	if err != nil {
		return Meeting{}, err
	}
	normalized, err := normalizeMeetingDetails(details, conversation != nil)
	if err != nil {
		return Meeting{}, err
	}
	id := uuid.NewString()
	return Meeting{
		ID:              id,
		WorkspaceID:     workspace.String(),
		Conversation:    conversation,
		OrganizerUserID: organizer.String(),
		MeetingDetails:  normalized,
		Status:          MeetingStatusScheduled,
		RoomKey:         MeetingRoomKey(id),
		Version:         1,
	}, nil
}

// MeetingRoomKey names a meeting's logical room. It is derived from the id, so
// it is stable and cannot collide with another resource; it is not a secret.
func MeetingRoomKey(meetingID string) string { return "meeting:" + meetingID }

// Update replaces the meeting's details. The caller's version must be current
// and the meeting not ended or cancelled.
func (m Meeting) Update(details MeetingDetails, expectedVersion int64) (Meeting, error) {
	if m.Version != expectedVersion {
		return m, ErrMeetingStaleVersion
	}
	if m.Status.Terminal() {
		return m, ErrMeetingInvalidState
	}
	normalized, err := normalizeMeetingDetails(details, m.Conversation != nil)
	if err != nil {
		return m, err
	}
	m.MeetingDetails = normalized
	return m, nil
}

// Transition moves the meeting to next. Asking for the status it already has
// is an idempotent no-op (changed is false) whatever the version, so a retried
// cancel succeeds; any real change needs the current version.
func (m Meeting) Transition(next MeetingStatus, expectedVersion int64) (Meeting, bool, error) {
	if !next.Valid() {
		return m, false, ErrInvalidInput
	}
	if m.Status == next {
		return m, false, nil
	}
	if m.Version != expectedVersion {
		return m, false, ErrMeetingStaleVersion
	}
	if !m.Status.CanTransitionTo(next) {
		return m, false, ErrMeetingInvalidState
	}
	m.Status = next
	return m, true, nil
}

func canonicalMeetingConversation(c *MeetingConversation) (*MeetingConversation, error) {
	if c == nil {
		return nil, nil
	}
	id, err := uuid.Parse(c.ID)
	if err != nil || (c.Kind != MeetingConversationChannel && c.Kind != MeetingConversationDM) {
		return nil, ErrInvalidInput
	}
	return &MeetingConversation{Kind: c.Kind, ID: id.String()}, nil
}

func normalizeMeetingDetails(d MeetingDetails, hasConversation bool) (MeetingDetails, error) {
	var err error
	if d.Title, err = normalizeMeetingTitle(d.Title); err != nil {
		return MeetingDetails{}, err
	}
	d.Description = strings.TrimSpace(d.Description)
	if !meetingTextValid(d.Description, MaxMeetingDescriptionCodePoints, true) {
		return MeetingDetails{}, ErrMeetingDescriptionInvalid
	}
	if d.StartsAt, d.EndsAt, err = normalizeMeetingInterval(d.StartsAt, d.EndsAt); err != nil {
		return MeetingDetails{}, err
	}
	if d.Timezone, err = ValidateMeetingTimezone(d.Timezone); err != nil {
		return MeetingDetails{}, err
	}
	d.JoinPolicy = d.JoinPolicy.orDefault()
	if !d.JoinPolicy.Valid() {
		return MeetingDetails{}, ErrMeetingInvalidJoinPolicy
	}
	if d.JoinPolicy == MeetingJoinConversationMembers && !hasConversation {
		return MeetingDetails{}, ErrMeetingConversationRequired
	}
	return d, nil
}

func normalizeMeetingTitle(title string) (string, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return "", ErrMeetingTitleRequired
	}
	if !meetingTextValid(title, MaxMeetingTitleCodePoints, false) {
		return "", ErrMeetingTitleInvalid
	}
	return title, nil
}

// meetingTextValid accepts plain text: valid UTF-8, within maxRunes, and free
// of control characters (line breaks and tabs only where multiline is true).
// Nothing is escaped or rewritten; rendering is the reader's job.
func meetingTextValid(value string, maxRunes int, multiline bool) bool {
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) > maxRunes {
		return false
	}
	return !strings.ContainsFunc(value, func(r rune) bool {
		if multiline && (r == '\n' || r == '\r' || r == '\t') {
			return false
		}
		return unicode.IsControl(r)
	})
}

// normalizeMeetingInterval returns the instants in UTC at PostgreSQL's
// microsecond precision, so what is validated is exactly what is stored and
// read back. The UTC form changes the representation, never the instant.
func normalizeMeetingInterval(startsAt, endsAt time.Time) (time.Time, time.Time, error) {
	startsAt = startsAt.UTC().Truncate(time.Microsecond)
	endsAt = endsAt.UTC().Truncate(time.Microsecond)
	if startsAt.IsZero() || !endsAt.After(startsAt) {
		return time.Time{}, time.Time{}, ErrMeetingInvalidTimeRange
	}
	return startsAt, endsAt, nil
}

// ValidateMeetingTimezone accepts an IANA zone name known to the embedded
// database. "Local" is refused because it names the server's zone, and "" and
// bare offsets such as "-03:00" are refused because they are not zones.
func ValidateMeetingTimezone(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || name == "Local" || len(name) > maxMeetingTimezoneBytes {
		return "", ErrMeetingInvalidTimezone
	}
	if _, err := time.LoadLocation(name); err != nil {
		return "", ErrMeetingInvalidTimezone
	}
	return name, nil
}
