package domain_test

import (
	"testing"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

func activeMember() *domain.WorkspaceMember {
	return &domain.WorkspaceMember{WorkspaceID: "ws-1", UserID: "user-1", Role: domain.WorkspaceRoleMember, Status: domain.MemberStatusActive}
}

func suspendedMember() *domain.WorkspaceMember {
	return &domain.WorkspaceMember{WorkspaceID: "ws-1", UserID: "user-1", Role: domain.WorkspaceRoleMember, Status: domain.MemberStatusSuspended}
}

func TestCanReadChannel_NilWorkspaceMember_Denied(t *testing.T) {
	ch := domain.Channel{ID: "ch-1", WorkspaceID: "ws-1", Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive}
	if domain.CanReadChannel(nil, nil, ch) {
		t.Fatal("nil workspace member should be denied")
	}
}

func TestCanReadChannel_SuspendedMember_Denied(t *testing.T) {
	ch := domain.Channel{ID: "ch-1", WorkspaceID: "ws-1", Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive}
	if domain.CanReadChannel(suspendedMember(), nil, ch) {
		t.Fatal("suspended member should be denied")
	}
}

func TestCanReadChannel_ActiveMember_PublicChannel_Allowed(t *testing.T) {
	ch := domain.Channel{ID: "ch-1", WorkspaceID: "ws-1", Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive}
	if !domain.CanReadChannel(activeMember(), nil, ch) {
		t.Fatal("active member + public channel should be allowed")
	}
}

func TestCanReadChannel_ActiveMember_GeneralChannel_Allowed(t *testing.T) {
	ch := domain.Channel{ID: "ch-1", WorkspaceID: "ws-1", Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive, IsGeneral: true}
	if !domain.CanReadChannel(activeMember(), nil, ch) {
		t.Fatal("#geral channel should be accessible to any active workspace member")
	}
}

func TestCanReadChannel_ActiveMember_PrivateChannel_NoChannelMembership_Denied(t *testing.T) {
	ch := domain.Channel{ID: "ch-1", WorkspaceID: "ws-1", Type: domain.ChannelTypePrivate, Status: domain.ChannelStatusActive}
	if domain.CanReadChannel(activeMember(), nil, ch) {
		t.Fatal("private channel without channel membership should be denied")
	}
}

func TestCanReadChannel_ActiveMember_PrivateChannel_WithChannelMembership_Allowed(t *testing.T) {
	ch := domain.Channel{ID: "ch-1", WorkspaceID: "ws-1", Type: domain.ChannelTypePrivate, Status: domain.ChannelStatusActive}
	cm := &domain.ChannelMember{ChannelID: "ch-1", UserID: "user-1"}
	if !domain.CanReadChannel(activeMember(), cm, ch) {
		t.Fatal("private channel with channel membership should be allowed")
	}
}

func TestCanWriteChannel_MatchesCanRead(t *testing.T) {
	cases := []struct {
		name string
		wm   *domain.WorkspaceMember
		cm   *domain.ChannelMember
		ch   domain.Channel
	}{
		{"nil member", nil, nil, domain.Channel{ID: "ch-1", WorkspaceID: "ws-1", Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive}},
		{"public", activeMember(), nil, domain.Channel{ID: "ch-1", WorkspaceID: "ws-1", Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive}},
		{"private no cm", activeMember(), nil, domain.Channel{ID: "ch-1", WorkspaceID: "ws-1", Type: domain.ChannelTypePrivate, Status: domain.ChannelStatusActive}},
		{"private with cm", activeMember(), &domain.ChannelMember{ChannelID: "ch-1", UserID: "user-1"}, domain.Channel{ID: "ch-1", WorkspaceID: "ws-1", Type: domain.ChannelTypePrivate, Status: domain.ChannelStatusActive}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if domain.CanWriteChannel(tc.wm, tc.cm, tc.ch) != domain.CanReadChannel(tc.wm, tc.cm, tc.ch) {
				t.Fatal("CanWriteChannel must match CanReadChannel")
			}
		})
	}
}

func TestCanReadChannel_CrossWorkspace_Denied(t *testing.T) {
	ch := domain.Channel{ID: "ch-2", WorkspaceID: "ws-2", Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive}
	if domain.CanReadChannel(activeMember(), nil, ch) {
		t.Fatal("workspace membership must match the channel workspace")
	}
}

func TestCanReadChannel_ArchivedChannel_Denied(t *testing.T) {
	ch := domain.Channel{ID: "ch-1", WorkspaceID: "ws-1", Type: domain.ChannelTypePublic, Status: domain.ChannelStatusArchived}
	if domain.CanReadChannel(activeMember(), nil, ch) {
		t.Fatal("archived channel must be denied")
	}
}

func TestCanReadChannel_PrivateGeneral_Denied(t *testing.T) {
	ch := domain.Channel{ID: "ch-1", WorkspaceID: "ws-1", Type: domain.ChannelTypePrivate, Status: domain.ChannelStatusActive, IsGeneral: true}
	if domain.CanReadChannel(activeMember(), nil, ch) {
		t.Fatal("general channel must be public")
	}
}

func TestCanReadChannel_PrivateMembershipMustMatchChannelAndUser(t *testing.T) {
	ch := domain.Channel{ID: "ch-1", WorkspaceID: "ws-1", Type: domain.ChannelTypePrivate, Status: domain.ChannelStatusActive}
	for _, cm := range []*domain.ChannelMember{
		{ChannelID: "ch-other", UserID: "user-1"},
		{ChannelID: "ch-1", UserID: "user-other"},
	} {
		if domain.CanReadChannel(activeMember(), cm, ch) {
			t.Fatalf("mismatched channel membership must be denied: %+v", cm)
		}
	}
}

// ── RF-74 role model ─────────────────────────────────────────────────────────

func memberWithRole(role domain.WorkspaceRole) *domain.WorkspaceMember {
	return &domain.WorkspaceMember{
		WorkspaceID: "ws-1", UserID: "user-1", Role: role, Status: domain.MemberStatusActive,
	}
}

// The whole RF-74 capability matrix in one table, so a role gaining or losing a
// capability is a one-line diff that has to be argued for rather than a silent
// consequence of editing one predicate. Read the columns as: reaches public
// channels without being added, manages the workspace, moderates it, creates
// channels, manages categories, manages channel members.
func TestWorkspaceCapabilities_RF74Matrix(t *testing.T) {
	for _, tc := range []struct {
		role                             domain.WorkspaceRole
		reachPublic, manage, moderate    bool
		createChannel, categories, chMem bool
	}{
		{domain.WorkspaceRoleOwner, true, true, true, true, true, true},
		{domain.WorkspaceRoleAdmin, true, true, true, true, true, true},
		{domain.WorkspaceRoleModerator, true, false, true, true, true, true},
		{domain.WorkspaceRoleMember, true, false, false, true, false, false},
		{domain.WorkspaceRoleGuest, false, false, false, false, false, false},
		// Deny by default: a role no predicate recognises gets nothing at all.
		{domain.WorkspaceRole("wizard"), false, false, false, false, false, false},
		{domain.WorkspaceRole(""), false, false, false, false, false, false},
	} {
		t.Run(string(tc.role), func(t *testing.T) {
			wm := memberWithRole(tc.role)
			for _, check := range []struct {
				name string
				got  bool
				want bool
			}{
				{"CanReachPublicChannels", domain.CanReachPublicChannels(wm), tc.reachPublic},
				{"CanManageWorkspace", domain.CanManageWorkspace(wm), tc.manage},
				{"CanModerateWorkspace", domain.CanModerateWorkspace(wm), tc.moderate},
				{"CanCreateChannel", domain.CanCreateChannel(wm), tc.createChannel},
				{"CanManageChannelCategories", domain.CanManageChannelCategories(wm), tc.categories},
				{"CanManageChannelMembers", domain.CanManageChannelMembers(wm), tc.chMem},
			} {
				if check.got != check.want {
					t.Errorf("%s = %v, want %v", check.name, check.got, check.want)
				}
			}
		})
	}
}

// Every capability requires an active membership, whatever the role. A
// suspended owner is not an owner, and a nil membership is never anybody.
func TestWorkspaceCapabilities_InactiveAndNilMembershipDeniedForEveryRole(t *testing.T) {
	predicates := map[string]func(*domain.WorkspaceMember) bool{
		"CanReachPublicChannels":     domain.CanReachPublicChannels,
		"CanManageWorkspace":         domain.CanManageWorkspace,
		"CanModerateWorkspace":       domain.CanModerateWorkspace,
		"CanCreateChannel":           domain.CanCreateChannel,
		"CanManageChannelCategories": domain.CanManageChannelCategories,
		"CanManageChannelMembers":    domain.CanManageChannelMembers,
	}
	members := []*domain.WorkspaceMember{nil}
	for _, role := range []domain.WorkspaceRole{
		domain.WorkspaceRoleOwner, domain.WorkspaceRoleAdmin, domain.WorkspaceRoleModerator,
		domain.WorkspaceRoleMember, domain.WorkspaceRoleGuest,
	} {
		for _, status := range []domain.MemberStatus{domain.MemberStatusSuspended, domain.MemberStatusLeft} {
			members = append(members, &domain.WorkspaceMember{
				WorkspaceID: "ws-1", UserID: "user-1", Role: role, Status: status,
			})
		}
	}
	for name, predicate := range predicates {
		for _, wm := range members {
			if predicate(wm) {
				t.Errorf("%s allowed an inactive membership: %+v", name, wm)
			}
		}
	}
}

// The guest boundary: workspace membership alone reaches nothing. Only an
// explicit chat.channel_members row does, and it reaches exactly its own
// channel — a guest added to one channel gains nothing in any other.
func TestCanReadChannel_GuestReachesOnlyChannelsItBelongsTo(t *testing.T) {
	guest := memberWithRole(domain.WorkspaceRoleGuest)
	included := &domain.ChannelMember{ChannelID: "ch-1", UserID: "user-1"}

	for _, tc := range []struct {
		name string
		cm   *domain.ChannelMember
		ch   domain.Channel
		want bool
	}{
		{"public channel, not included", nil,
			domain.Channel{ID: "ch-1", WorkspaceID: "ws-1", Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive}, false},
		{"geral, not included", nil,
			domain.Channel{ID: "ch-1", WorkspaceID: "ws-1", Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive, IsGeneral: true}, false},
		{"private channel, not included", nil,
			domain.Channel{ID: "ch-1", WorkspaceID: "ws-1", Type: domain.ChannelTypePrivate, Status: domain.ChannelStatusActive}, false},
		{"public channel, included", included,
			domain.Channel{ID: "ch-1", WorkspaceID: "ws-1", Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive}, true},
		{"private channel, included", included,
			domain.Channel{ID: "ch-1", WorkspaceID: "ws-1", Type: domain.ChannelTypePrivate, Status: domain.ChannelStatusActive}, true},
		{"included elsewhere does not carry over", included,
			domain.Channel{ID: "ch-2", WorkspaceID: "ws-1", Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive}, false},
		{"archived channel it belongs to", included,
			domain.Channel{ID: "ch-1", WorkspaceID: "ws-1", Type: domain.ChannelTypePublic, Status: domain.ChannelStatusArchived}, false},
		{"channel in another workspace", included,
			domain.Channel{ID: "ch-1", WorkspaceID: "ws-2", Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := domain.CanReadChannel(guest, tc.cm, tc.ch); got != tc.want {
				t.Fatalf("CanReadChannel = %v, want %v", got, tc.want)
			}
			// RF-05: pin and unpin reuse read access and add no role of their
			// own, so whatever a guest may read it may pin, and nothing else.
			if got := domain.CanWriteChannel(guest, tc.cm, tc.ch); got != tc.want {
				t.Fatalf("CanWriteChannel = %v, want %v", got, tc.want)
			}
		})
	}
}

// A private channel stays private for everyone. Workspace authority is not a
// key to channels: neither an owner, an admin nor a moderator reads a private
// channel they were not added to.
func TestCanReadChannel_PrivateChannelIgnoresWorkspaceAuthority(t *testing.T) {
	ch := domain.Channel{ID: "ch-1", WorkspaceID: "ws-1", Type: domain.ChannelTypePrivate, Status: domain.ChannelStatusActive}
	for _, role := range []domain.WorkspaceRole{
		domain.WorkspaceRoleOwner, domain.WorkspaceRoleAdmin, domain.WorkspaceRoleModerator,
	} {
		if domain.CanReadChannel(memberWithRole(role), nil, ch) {
			t.Fatalf("%s must not read a private channel it does not belong to", role)
		}
	}
}

// The per-channel moderator role is not workspace authority. A channel member
// carrying ChannelRoleModerator gets exactly what any channel member gets:
// access to that channel, and no workspace capability at all.
func TestChannelModeratorIsNotWorkspaceModerator(t *testing.T) {
	member := memberWithRole(domain.WorkspaceRoleMember)
	channelModerator := &domain.ChannelMember{ChannelID: "ch-1", UserID: "user-1", Role: domain.ChannelRoleModerator}
	ch := domain.Channel{ID: "ch-1", WorkspaceID: "ws-1", Type: domain.ChannelTypePrivate, Status: domain.ChannelStatusActive}

	if !domain.CanReadChannel(member, channelModerator, ch) {
		t.Fatal("a channel moderator is a channel member and must read its channel")
	}
	if domain.CanModerateWorkspace(member) || domain.CanManageChannelCategories(member) ||
		domain.CanManageChannelMembers(member) || domain.CanManageWorkspace(member) {
		t.Fatal("moderating a channel must confer no workspace capability")
	}
}
