package domain

// MeetingCapability names one thing an actor may do with a meeting (issue
// #1052). AuthorizeMeeting is the only place any of them is decided.
type MeetingCapability string

const (
	MeetingCapabilityCreate             MeetingCapability = "meeting:create"
	MeetingCapabilityUpdate             MeetingCapability = "meeting:update"
	MeetingCapabilityCancel             MeetingCapability = "meeting:cancel"
	MeetingCapabilityManageParticipants MeetingCapability = "meeting:manage_participants"
	MeetingCapabilityJoin               MeetingCapability = "meeting:join"
	MeetingCapabilityRSVP               MeetingCapability = "meeting:rsvp"
	// MeetingCapabilityView is the precondition every other capability on an
	// existing meeting shares: reading it.
	MeetingCapabilityView MeetingCapability = "meeting:view"
)

// MeetingActor is one user's server-resolved relationship to a meeting, read
// in the same transaction as the decision. It is never built from a request.
type MeetingActor struct {
	UserID string
	// Membership is nil unless the user is a member of an active workspace with
	// an active, undeleted account.
	Membership *WorkspaceMember
	// Participant is nil unless the user has a row in the meeting's roster.
	Participant *MeetingParticipant
	// ConversationAccess is whether the user can read the meeting's
	// conversation right now; false when the meeting has none.
	ConversationAccess bool
}

// AuthorizeMeeting decides capability c for actor a on meeting m. For
// MeetingCapabilityCreate, m is the meeting about to be created.
//
// An actor who may not see the meeting gets ErrNotFound — the same answer as a
// meeting that does not exist or lives in another workspace — so a meeting id
// grants nothing and reveals nothing. ErrForbidden is reserved for actors who
// can see it. Join, RSVP and participant changes also need an open meeting.
func AuthorizeMeeting(m Meeting, a MeetingActor, c MeetingCapability) error {
	if c == MeetingCapabilityCreate {
		return authorizeMeetingCreate(m, a)
	}
	if !canSeeMeeting(m, a) {
		return ErrNotFound
	}
	if !meetingRoleAllows(meetingParticipant(m, a), c) {
		return ErrForbidden
	}
	needsOpen := c == MeetingCapabilityJoin || c == MeetingCapabilityRSVP || c == MeetingCapabilityManageParticipants
	if needsOpen && m.Status.Terminal() {
		return ErrMeetingInvalidState
	}
	return nil
}

// MeetingTransitionCapability is the capability a move to next requires:
// cancelling is its own capability, starting and ending are edits.
func MeetingTransitionCapability(next MeetingStatus) MeetingCapability {
	if next == MeetingStatusCancelled {
		return MeetingCapabilityCancel
	}
	return MeetingCapabilityUpdate
}

// AuthorizeMeetingUpdate is meeting:update plus one rule: only the organizer
// may change who can join, so a cohost cannot widen access.
func AuthorizeMeetingUpdate(m Meeting, a MeetingActor, joinPolicy MeetingJoinPolicy) error {
	if err := AuthorizeMeeting(m, a, MeetingCapabilityUpdate); err != nil {
		return err
	}
	if joinPolicy.orDefault() != m.JoinPolicy && !isMeetingOrganizer(m, a) {
		return ErrForbidden
	}
	return nil
}

// AuthorizeMeetingParticipantChange decides adding, re-roling or removing one
// participant. target is the target's current row (nil when adding) and role
// the role it should hold afterwards; an empty role removes it.
//
// Nobody becomes or stops being the organizer this way, and only the organizer
// grants or revokes cohost, so a cohost can manage attendees but not itself.
func AuthorizeMeetingParticipantChange(m Meeting, a MeetingActor, target *MeetingParticipant, role MeetingRole) error {
	if err := AuthorizeMeeting(m, a, MeetingCapabilityManageParticipants); err != nil {
		return err
	}
	if !role.assignable() {
		return ErrInvalidInput
	}
	if target != nil && target.Role == MeetingRoleOrganizer {
		return ErrForbidden
	}
	touchesCohost := role == MeetingRoleCohost || (target != nil && target.Role == MeetingRoleCohost)
	if touchesCohost && !isMeetingOrganizer(m, a) {
		return ErrForbidden
	}
	return nil
}

// AuthorizeMeetingParticipantBulk requires roster management and current access
// to the linked conversation: meeting visibility never grants member discovery.
func AuthorizeMeetingParticipantBulk(m Meeting, actor MeetingActor) error {
	if err := AuthorizeMeeting(m, actor, MeetingCapabilityManageParticipants); err != nil {
		return err
	}
	if m.Conversation == nil {
		return ErrInvalidInput
	}
	if !actor.ConversationAccess {
		return ErrForbidden
	}
	return nil
}

// MeetingInviteeEligible reports whether invitee may be added to m: an
// eligible member of m's workspace who, when m belongs to a conversation, can
// read it — an invitation must not reveal a conversation to an outsider.
func MeetingInviteeEligible(m Meeting, invitee MeetingActor) bool {
	return eligibleMeetingMember(m, invitee) && CanReachPublicChannels(invitee.Membership) &&
		(m.Conversation == nil || invitee.ConversationAccess)
}

// Creating takes a workspace-wide role (guests are excluded, as for channel
// creation) and, for a conversation's meeting, access to that conversation.
// An inaccessible conversation is ErrNotFound so its existence is not revealed.
func authorizeMeetingCreate(m Meeting, a MeetingActor) error {
	if a.UserID != m.OrganizerUserID || !eligibleMeetingMember(m, a) || !CanReachPublicChannels(a.Membership) {
		return ErrForbidden
	}
	if m.Conversation != nil && !a.ConversationAccess {
		return ErrNotFound
	}
	return nil
}

// canSeeMeeting: eligible workspace membership is always required, so a
// participant row never outlives the membership it was granted under. On top
// of it, the roster always admits; the join policy may admit more.
func canSeeMeeting(m Meeting, a MeetingActor) bool {
	if !eligibleMeetingMember(m, a) {
		return false
	}
	if meetingParticipant(m, a) != nil {
		return true
	}
	switch m.JoinPolicy {
	case MeetingJoinConversationMembers:
		return m.Conversation != nil && a.ConversationAccess
	case MeetingJoinWorkspaceLink:
		return CanReachPublicChannels(a.Membership)
	default:
		return false
	}
}

func meetingRoleAllows(p *MeetingParticipant, c MeetingCapability) bool {
	var role MeetingRole
	if p != nil {
		role = p.Role
	}
	switch c {
	case MeetingCapabilityView, MeetingCapabilityJoin:
		return true
	case MeetingCapabilityUpdate, MeetingCapabilityManageParticipants:
		return role == MeetingRoleOrganizer || role == MeetingRoleCohost
	case MeetingCapabilityCancel:
		return role == MeetingRoleOrganizer
	case MeetingCapabilityRSVP:
		// The organizer's answer is fixed to accepted.
		return role == MeetingRoleCohost || role == MeetingRoleAttendee
	default:
		return false
	}
}

func eligibleMeetingMember(m Meeting, a MeetingActor) bool {
	wm := a.Membership
	return a.UserID != "" && wm != nil && wm.UserID == a.UserID &&
		wm.WorkspaceID == m.WorkspaceID && eligibleConversationWorkspaceMember(*wm)
}

// meetingParticipant returns a's roster row only when it is consistent with m:
// the organizer role belongs to m's organizer and to nobody else.
func meetingParticipant(m Meeting, a MeetingActor) *MeetingParticipant {
	p := a.Participant
	if p == nil || p.MeetingID != m.ID || p.UserID != a.UserID || !p.Role.Valid() ||
		(p.Role == MeetingRoleOrganizer) != (a.UserID == m.OrganizerUserID) {
		return nil
	}
	return p
}

func isMeetingOrganizer(m Meeting, a MeetingActor) bool {
	p := meetingParticipant(m, a)
	return p != nil && p.Role == MeetingRoleOrganizer
}
