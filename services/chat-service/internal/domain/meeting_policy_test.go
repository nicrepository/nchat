package domain_test

import (
	"errors"
	"testing"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

const (
	policyCohost   = "6a1c0000-0000-4000-8000-0000000000c0"
	policyAttendee = "6a1c0000-0000-4000-8000-0000000000a0"
	policyMember   = "6a1c0000-0000-4000-8000-0000000000b0"
	policyOther    = "6a1c0000-0000-4000-8000-0000000000f0"
)

type policyActor struct {
	user         string
	workspace    string
	role         domain.WorkspaceRole
	status       domain.MemberStatus
	meetingRole  domain.MeetingRole
	conversation bool
}

func (p policyActor) build(m domain.Meeting) domain.MeetingActor {
	a := domain.MeetingActor{UserID: p.user, ConversationAccess: p.conversation}
	if p.role != "" {
		a.Membership = &domain.WorkspaceMember{WorkspaceID: p.workspace, UserID: p.user, Role: p.role, Status: p.status}
	}
	if p.meetingRole != "" {
		a.Participant = &domain.MeetingParticipant{MeetingID: m.ID, UserID: p.user, Role: p.meetingRole, RSVP: domain.MeetingRSVPPending}
	}
	return a
}

func member(user string, meetingRole domain.MeetingRole, conversation bool) policyActor {
	return policyActor{user, meetingWorkspace, domain.WorkspaceRoleMember, domain.MemberStatusActive, meetingRole, conversation}
}

func policyMeeting(t *testing.T, policy domain.MeetingJoinPolicy) domain.Meeting {
	t.Helper()
	details := meetingDetails()
	details.JoinPolicy = policy
	return newTestMeeting(t, &domain.MeetingConversation{Kind: domain.MeetingConversationChannel, ID: meetingChannel}, details)
}

// capabilityOutcome is one actor's result for view, join, update, cancel,
// manage_participants and rsvp, in that order: "ok", "hidden" (ErrNotFound) or
// "denied" (ErrForbidden).
type capabilityOutcome [6]string

var matrixCapabilities = [6]domain.MeetingCapability{
	domain.MeetingCapabilityView, domain.MeetingCapabilityJoin, domain.MeetingCapabilityUpdate,
	domain.MeetingCapabilityCancel, domain.MeetingCapabilityManageParticipants, domain.MeetingCapabilityRSVP,
}

func outcomeOf(err error) string {
	switch {
	case err == nil:
		return "ok"
	case errors.Is(err, domain.ErrNotFound):
		return "hidden"
	case errors.Is(err, domain.ErrForbidden):
		return "denied"
	default:
		return err.Error()
	}
}

// The matrix catches both directions: a capability granted to someone who
// should not have it, and one withheld from someone who should.
func TestMeetingCapabilityMatrix(t *testing.T) {
	organizer := capabilityOutcome{"ok", "ok", "ok", "ok", "ok", "denied"}
	cohost := capabilityOutcome{"ok", "ok", "ok", "denied", "ok", "ok"}
	attendee := capabilityOutcome{"ok", "ok", "denied", "denied", "denied", "ok"}
	admitted := capabilityOutcome{"ok", "ok", "denied", "denied", "denied", "denied"}
	hidden := capabilityOutcome{"hidden", "hidden", "hidden", "hidden", "hidden", "hidden"}

	type policies struct{ invited, conversation, link capabilityOutcome }
	same := func(o capabilityOutcome) policies { return policies{o, o, o} }
	actors := []struct {
		name  string
		actor policyActor
		want  policies
	}{
		{"organizer", member(meetingOrganizer, domain.MeetingRoleOrganizer, false), same(organizer)},
		{"cohost", member(policyCohost, domain.MeetingRoleCohost, false), same(cohost)},
		{"invited attendee", member(policyAttendee, domain.MeetingRoleAttendee, false), same(attendee)},
		{"invited guest", policyActor{policyAttendee, meetingWorkspace, domain.WorkspaceRoleGuest, domain.MemberStatusActive, domain.MeetingRoleAttendee, false}, same(attendee)},
		{"uninvited conversation member", member(policyMember, "", true), policies{hidden, admitted, admitted}},
		{"uninvited member outside the conversation", member(policyMember, "", false), policies{hidden, hidden, admitted}},
		{"workspace admin, uninvited", policyActor{policyMember, meetingWorkspace, domain.WorkspaceRoleAdmin, domain.MemberStatusActive, "", false}, policies{hidden, hidden, admitted}},
		{"uninvited guest in the conversation", policyActor{policyMember, meetingWorkspace, domain.WorkspaceRoleGuest, domain.MemberStatusActive, "", true}, policies{hidden, admitted, hidden}},
		{"other workspace's member", policyActor{policyOther, "6a1c0000-0000-4000-8000-0000000000ff", domain.WorkspaceRoleOwner, domain.MemberStatusActive, "", true}, same(hidden)},
		{"participant who left the workspace", policyActor{policyAttendee, meetingWorkspace, domain.WorkspaceRoleMember, domain.MemberStatusLeft, domain.MeetingRoleAttendee, true}, same(hidden)},
		{"suspended organizer", policyActor{meetingOrganizer, meetingWorkspace, domain.WorkspaceRoleMember, domain.MemberStatusSuspended, domain.MeetingRoleOrganizer, true}, same(hidden)},
		{"participant without membership", policyActor{user: policyAttendee, meetingRole: domain.MeetingRoleAttendee, conversation: true}, same(hidden)},
		{"forged organizer row", member(policyAttendee, domain.MeetingRoleOrganizer, false), policies{hidden, hidden, admitted}},
		{"unknown participant role", member(policyAttendee, "owner", false), policies{hidden, hidden, admitted}},
		{"no session", policyActor{}, same(hidden)},
	}
	for _, tt := range actors {
		for _, policy := range []struct {
			policy domain.MeetingJoinPolicy
			want   capabilityOutcome
		}{
			{domain.MeetingJoinInvitedOnly, tt.want.invited},
			{domain.MeetingJoinConversationMembers, tt.want.conversation},
			{domain.MeetingJoinWorkspaceLink, tt.want.link},
		} {
			m := policyMeeting(t, policy.policy)
			actor := tt.actor.build(m)
			var got capabilityOutcome
			for i, capability := range matrixCapabilities {
				got[i] = outcomeOf(domain.AuthorizeMeeting(m, actor, capability))
			}
			if got != policy.want {
				t.Errorf("%s / %s = %v, want %v", tt.name, policy.policy, got, policy.want)
			}
		}
	}
}

func TestMeetingCapabilitiesInClosedMeetings(t *testing.T) {
	organizer := member(meetingOrganizer, domain.MeetingRoleOrganizer, false)
	closed := domain.ErrMeetingInvalidState.Error()
	checks := []struct {
		actor      policyActor
		capability domain.MeetingCapability
		want       string
	}{
		// History stays readable; edits and cancels are refused by the state
		// machine itself, which also keeps a repeated cancel idempotent.
		{organizer, domain.MeetingCapabilityView, "ok"},
		{organizer, domain.MeetingCapabilityUpdate, "ok"},
		{organizer, domain.MeetingCapabilityCancel, "ok"},
		{organizer, domain.MeetingCapabilityJoin, closed},
		{organizer, domain.MeetingCapabilityManageParticipants, closed},
		{member(policyAttendee, domain.MeetingRoleAttendee, false), domain.MeetingCapabilityRSVP, closed},
		// A closed meeting does not reveal its state to an outsider.
		{member(policyMember, "", true), domain.MeetingCapabilityJoin, "hidden"},
	}
	for _, status := range []domain.MeetingStatus{domain.MeetingStatusEnded, domain.MeetingStatusCancelled} {
		m := policyMeeting(t, domain.MeetingJoinInvitedOnly)
		m.Status = status
		for _, c := range checks {
			if got := outcomeOf(domain.AuthorizeMeeting(m, c.actor.build(m), c.capability)); got != c.want {
				t.Errorf("%s %s by %s = %s, want %s", status, c.capability, c.actor.user, got, c.want)
			}
		}
	}
}

func TestAuthorizeMeetingRefusesUnknownCapability(t *testing.T) {
	m := policyMeeting(t, domain.MeetingJoinInvitedOnly)
	if err := domain.AuthorizeMeeting(m, member(meetingOrganizer, domain.MeetingRoleOrganizer, false).build(m), "meeting:delete"); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("unknown capability: %v", err)
	}
}

func TestAuthorizeMeetingCreate(t *testing.T) {
	tests := []struct {
		name         string
		actor        policyActor
		conversation bool
		want         string
	}{
		{"member", member(meetingOrganizer, "", false), false, "ok"},
		{"member in the conversation", member(meetingOrganizer, "", true), true, "ok"},
		{"admin", policyActor{meetingOrganizer, meetingWorkspace, domain.WorkspaceRoleAdmin, domain.MemberStatusActive, "", false}, false, "ok"},
		{"member outside the conversation", member(meetingOrganizer, "", false), true, "hidden"},
		{"guest", policyActor{meetingOrganizer, meetingWorkspace, domain.WorkspaceRoleGuest, domain.MemberStatusActive, "", true}, true, "denied"},
		{"suspended member", policyActor{meetingOrganizer, meetingWorkspace, domain.WorkspaceRoleMember, domain.MemberStatusSuspended, "", false}, false, "denied"},
		{"other workspace's member", policyActor{meetingOrganizer, policyOther, domain.WorkspaceRoleOwner, domain.MemberStatusActive, "", false}, false, "denied"},
		{"actor is not the organizer", member(policyMember, "", false), false, "denied"},
		{"no session", policyActor{}, false, "denied"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var conversation *domain.MeetingConversation
			if tt.conversation {
				conversation = &domain.MeetingConversation{Kind: domain.MeetingConversationDM, ID: meetingChannel}
			}
			m := newTestMeeting(t, conversation, meetingDetails())
			if got := outcomeOf(domain.AuthorizeMeeting(m, tt.actor.build(m), domain.MeetingCapabilityCreate)); got != tt.want {
				t.Fatalf("create = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestOnlyTheOrganizerChangesTheJoinPolicy(t *testing.T) {
	m := policyMeeting(t, domain.MeetingJoinInvitedOnly)
	organizer := member(meetingOrganizer, domain.MeetingRoleOrganizer, false).build(m)
	cohost := member(policyCohost, domain.MeetingRoleCohost, false).build(m)
	attendee := member(policyAttendee, domain.MeetingRoleAttendee, false).build(m)
	tests := []struct {
		name   string
		actor  domain.MeetingActor
		policy domain.MeetingJoinPolicy
		want   string
	}{
		{"organizer widens", organizer, domain.MeetingJoinWorkspaceLink, "ok"},
		{"cohost widens", cohost, domain.MeetingJoinWorkspaceLink, "denied"},
		{"cohost keeps", cohost, domain.MeetingJoinInvitedOnly, "ok"},
		{"cohost omits, meaning the default", cohost, "", "ok"},
		{"attendee keeps", attendee, domain.MeetingJoinInvitedOnly, "denied"},
	}
	for _, tt := range tests {
		if got := outcomeOf(domain.AuthorizeMeetingUpdate(m, tt.actor, tt.policy)); got != tt.want {
			t.Errorf("%s = %s, want %s", tt.name, got, tt.want)
		}
	}
}

func TestMeetingParticipantChanges(t *testing.T) {
	m := policyMeeting(t, domain.MeetingJoinInvitedOnly)
	organizer := member(meetingOrganizer, domain.MeetingRoleOrganizer, false).build(m)
	cohost := member(policyCohost, domain.MeetingRoleCohost, false).build(m)
	attendee := member(policyAttendee, domain.MeetingRoleAttendee, false).build(m)
	row := func(a domain.MeetingActor) *domain.MeetingParticipant { return a.Participant }
	tests := []struct {
		name   string
		actor  domain.MeetingActor
		target *domain.MeetingParticipant
		role   domain.MeetingRole
		want   error
	}{
		{"organizer invites attendee", organizer, nil, domain.MeetingRoleAttendee, nil},
		{"organizer invites cohost", organizer, nil, domain.MeetingRoleCohost, nil},
		{"organizer promotes attendee", organizer, row(attendee), domain.MeetingRoleCohost, nil},
		{"organizer demotes cohost", organizer, row(cohost), domain.MeetingRoleAttendee, nil},
		{"organizer removes cohost", organizer, row(cohost), "", nil},
		{"cohost invites attendee", cohost, nil, domain.MeetingRoleAttendee, nil},
		{"cohost removes attendee", cohost, row(attendee), "", nil},
		{"cohost invites cohost", cohost, nil, domain.MeetingRoleCohost, domain.ErrForbidden},
		{"cohost promotes attendee", cohost, row(attendee), domain.MeetingRoleCohost, domain.ErrForbidden},
		{"cohost demotes itself", cohost, row(cohost), domain.MeetingRoleAttendee, domain.ErrForbidden},
		{"cohost removes cohost", cohost, row(cohost), "", domain.ErrForbidden},
		{"cohost removes organizer", cohost, row(organizer), "", domain.ErrForbidden},
		{"organizer demotes itself", organizer, row(organizer), domain.MeetingRoleAttendee, domain.ErrForbidden},
		{"organizer removes itself", organizer, row(organizer), "", domain.ErrForbidden},
		{"attendee promotes itself", attendee, row(attendee), domain.MeetingRoleCohost, domain.ErrForbidden},
		{"attendee invites", attendee, nil, domain.MeetingRoleAttendee, domain.ErrForbidden},
		{"organizer role is never assigned", organizer, row(attendee), domain.MeetingRoleOrganizer, domain.ErrInvalidInput},
		{"unknown role", organizer, nil, "owner", domain.ErrInvalidInput},
		{"outsider", member(policyMember, "", true).build(m), nil, domain.MeetingRoleAttendee, domain.ErrNotFound},
	}
	for _, tt := range tests {
		if err := domain.AuthorizeMeetingParticipantChange(m, tt.actor, tt.target, tt.role); !errors.Is(err, tt.want) || (tt.want == nil) != (err == nil) {
			t.Errorf("%s: err = %v, want %v", tt.name, err, tt.want)
		}
	}
	m.Status = domain.MeetingStatusCancelled
	if err := domain.AuthorizeMeetingParticipantChange(m, organizer, nil, domain.MeetingRoleAttendee); !errors.Is(err, domain.ErrMeetingInvalidState) {
		t.Errorf("cancelled meeting roster changed: %v", err)
	}
}

func TestMeetingInviteeEligibility(t *testing.T) {
	plain := newTestMeeting(t, nil, meetingDetails())
	inConversation := policyMeeting(t, domain.MeetingJoinInvitedOnly)
	guest := policyActor{policyMember, meetingWorkspace, domain.WorkspaceRoleGuest, domain.MemberStatusActive, "", false}
	tests := []struct {
		name    string
		meeting domain.Meeting
		invitee policyActor
		want    bool
	}{
		{"member", plain, member(policyMember, "", false), true},
		{"guest excluded from new invitations (#1054)", plain, guest, false},
		{"suspended member", plain, policyActor{policyMember, meetingWorkspace, domain.WorkspaceRoleMember, domain.MemberStatusSuspended, "", false}, false},
		{"other workspace's member", plain, policyActor{policyMember, policyOther, domain.WorkspaceRoleMember, domain.MemberStatusActive, "", false}, false},
		{"unknown user", plain, policyActor{user: policyMember}, false},
		{"conversation member", inConversation, member(policyMember, "", true), true},
		{"member outside the conversation", inConversation, member(policyMember, "", false), false},
	}
	for _, tt := range tests {
		if got := domain.MeetingInviteeEligible(tt.meeting, tt.invitee.build(tt.meeting)); got != tt.want {
			t.Errorf("%s = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestMeetingTransitionCapability(t *testing.T) {
	want := map[domain.MeetingStatus]domain.MeetingCapability{
		domain.MeetingStatusInProgress: domain.MeetingCapabilityUpdate,
		domain.MeetingStatusEnded:      domain.MeetingCapabilityUpdate,
		domain.MeetingStatusCancelled:  domain.MeetingCapabilityCancel,
	}
	for status, capability := range want {
		if got := domain.MeetingTransitionCapability(status); got != capability {
			t.Errorf("%s needs %s, want %s", status, got, capability)
		}
	}
}

func TestMeetingParticipantBulkAuthorization(t *testing.T) {
	m := policyMeeting(t, domain.MeetingJoinInvitedOnly)
	organizer := member(m.OrganizerUserID, domain.MeetingRoleOrganizer, true).build(m)
	if err := domain.AuthorizeMeetingParticipantBulk(m, organizer); err != nil {
		t.Fatal(err)
	}
	organizer.ConversationAccess = false
	if err := domain.AuthorizeMeetingParticipantBulk(m, organizer); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal(err)
	}
	plain := m
	plain.Conversation = nil
	if err := domain.AuthorizeMeetingParticipantBulk(plain, organizer); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatal(err)
	}
	attendee := member(policyAttendee, domain.MeetingRoleAttendee, true).build(m)
	if err := domain.AuthorizeMeetingParticipantBulk(m, attendee); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal(err)
	}
	m.Status = domain.MeetingStatusEnded
	if err := domain.AuthorizeMeetingParticipantBulk(m, organizer); !errors.Is(err, domain.ErrMeetingInvalidState) {
		t.Fatal(err)
	}
}
