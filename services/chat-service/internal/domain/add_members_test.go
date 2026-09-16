package domain_test

import (
	"errors"
	"testing"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

// CanManageChannelMembers is the named seam for issue #398. The assertion that
// matters is not that it returns true for owners, but that it returns false for
// a plain member and a guest: a channel member who could add others would make
// a private channel's audience grow without a manager ever deciding to.
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

// The predicate must stay identical to CanManageWorkspace rather than becoming a
// second, independently-edited copy of the same rule. If a future change widens
// one, this fails and forces the divergence to be deliberate.
func TestCanManageChannelMembersMatchesWorkspaceManagement(t *testing.T) {
	roles := []domain.WorkspaceRole{
		domain.WorkspaceRoleOwner, domain.WorkspaceRoleAdmin,
		domain.WorkspaceRoleMember, domain.WorkspaceRoleGuest,
	}
	statuses := []domain.MemberStatus{
		domain.MemberStatusActive, domain.MemberStatusSuspended, domain.MemberStatusLeft,
	}
	for _, role := range roles {
		for _, status := range statuses {
			member := &domain.WorkspaceMember{Role: role, Status: status}
			if domain.CanManageChannelMembers(member) != domain.CanManageWorkspace(member) {
				t.Fatalf("divergence for role=%s status=%s", role, status)
			}
		}
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
