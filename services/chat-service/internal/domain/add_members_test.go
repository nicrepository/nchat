package domain_test

import (
	"errors"
	"testing"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

// CanManageChannelMembers is the administrative removal/roster seam. It must
// remain narrow even though adding now follows channel access.
func TestCanManageChannelMembers(t *testing.T) {
	tests := map[string]struct {
		member *domain.WorkspaceMember
		want   bool
	}{
		"active owner": {
			member: &domain.WorkspaceMember{Role: domain.WorkspaceRoleOwner, Status: domain.MemberStatusActive},
			want:   true,
		},
		"active admin": {
			member: &domain.WorkspaceMember{Role: domain.WorkspaceRoleAdmin, Status: domain.MemberStatusActive},
			want:   true,
		},
		"active member": {
			member: &domain.WorkspaceMember{Role: domain.WorkspaceRoleMember, Status: domain.MemberStatusActive},
			want:   false,
		},
		"active guest": {
			member: &domain.WorkspaceMember{Role: domain.WorkspaceRoleGuest, Status: domain.MemberStatusActive},
			want:   false,
		},
		"suspended admin": {
			member: &domain.WorkspaceMember{Role: domain.WorkspaceRoleAdmin, Status: domain.MemberStatusSuspended},
			want:   false,
		},
		"left owner": {
			member: &domain.WorkspaceMember{Role: domain.WorkspaceRoleOwner, Status: domain.MemberStatusLeft},
			want:   false,
		},
		"no membership": {member: nil, want: false},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if got := domain.CanManageChannelMembers(test.member); got != test.want {
				t.Fatalf("CanManageChannelMembers = %v, want %v", got, test.want)
			}
		})
	}
}

// The predicate delegates to the workspace moderation capability rather than
// restating its role list.
func TestCanManageChannelMembersMatchesWorkspaceModeration(t *testing.T) {
	roles := []domain.WorkspaceRole{
		domain.WorkspaceRoleOwner, domain.WorkspaceRoleAdmin, domain.WorkspaceRoleModerator,
		domain.WorkspaceRoleMember, domain.WorkspaceRoleGuest,
	}
	statuses := []domain.MemberStatus{
		domain.MemberStatusActive, domain.MemberStatusSuspended, domain.MemberStatusLeft,
	}
	for _, role := range roles {
		for _, status := range statuses {
			member := &domain.WorkspaceMember{Role: role, Status: status}
			if domain.CanManageChannelMembers(member) != domain.CanModerateWorkspace(member) {
				t.Fatalf("divergence for role=%s status=%s", role, status)
			}
		}
	}
}

func TestCanAddChannelMembersFollowsChannelAccessWithoutGrantingManagement(t *testing.T) {
	public := domain.Channel{
		ID: "ch-public", WorkspaceID: "ws-1", Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive,
	}
	private := domain.Channel{
		ID: "ch-private", WorkspaceID: "ws-1", Type: domain.ChannelTypePrivate, Status: domain.ChannelStatusActive,
	}
	member := &domain.WorkspaceMember{
		WorkspaceID: "ws-1", UserID: "user-1", Role: domain.WorkspaceRoleMember, Status: domain.MemberStatusActive,
	}
	guest := &domain.WorkspaceMember{
		WorkspaceID: "ws-1", UserID: "guest-1", Role: domain.WorkspaceRoleGuest, Status: domain.MemberStatusActive,
	}

	tests := []struct {
		name string
		wm   *domain.WorkspaceMember
		cm   *domain.ChannelMember
		ch   domain.Channel
		want bool
	}{
		{name: "member reaches public channel", wm: member, ch: public, want: true},
		{name: "member needs private membership", wm: member, ch: private, want: false},
		{
			name: "member with private access", wm: member,
			cm: &domain.ChannelMember{ChannelID: private.ID, UserID: member.UserID}, ch: private, want: true,
		},
		{name: "guest needs explicit public membership", wm: guest, ch: public, want: false},
		{
			name: "guest with explicit public access", wm: guest,
			cm: &domain.ChannelMember{ChannelID: public.ID, UserID: guest.UserID}, ch: public, want: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := domain.CanAddChannelMembers(test.wm, test.cm, test.ch); got != test.want {
				t.Fatalf("CanAddChannelMembers = %v, want %v", got, test.want)
			}
		})
	}

	if domain.CanManageChannelMembers(member) {
		t.Fatal("add access granted channel-management authority to an ordinary member")
	}
	if domain.CanManageWorkspace(member) || domain.CanRenameChannel(member, public) {
		t.Fatal("add access granted workspace or rename authority to an ordinary member")
	}
}

// The batch cap bounds one HTTP request and nothing else.
//
// It is deliberately not a capacity: channels and groups have no fixed
// participant limit, so successive requests may grow a conversation without
// bound. This asserts only that the cap is usable and stays a human-sized
// batch — a number large enough to be a conversation ceiling in disguise would
// misrepresent what it is.
func TestMaxAddMembersPerRequestIsAPerRequestBatchCap(t *testing.T) {
	if domain.MaxAddMembersPerRequest < 1 {
		t.Fatalf("MaxAddMembersPerRequest = %d, must allow at least one user", domain.MaxAddMembersPerRequest)
	}
	if domain.MaxAddMembersPerRequest > 100 {
		t.Fatalf("MaxAddMembersPerRequest = %d, larger than a per-request batch should be",
			domain.MaxAddMembersPerRequest)
	}
}

// Error families decide HTTP status codes, so the wrapping is part of the
// contract rather than an implementation detail.
func TestAddMembersErrorsWrapTheRightFamilies(t *testing.T) {
	tests := map[string]struct {
		err    error
		family error
	}{
		"batch cap is invalid input":  {domain.ErrTooManyMembersRequested, domain.ErrInvalidInput},
		"empty list is invalid input": {domain.ErrNoMembersRequested, domain.ErrInvalidInput},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if !errors.Is(test.err, test.family) {
				t.Fatalf("%v does not wrap %v", test.err, test.family)
			}
		})
	}
}
