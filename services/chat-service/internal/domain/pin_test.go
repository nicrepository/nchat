package domain_test

import (
	"testing"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

func publicChannel() domain.Channel {
	return domain.Channel{ID: "ch-1", WorkspaceID: "ws-1", Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive}
}

func privateChannel() domain.Channel {
	return domain.Channel{ID: "ch-1", WorkspaceID: "ws-1", Type: domain.ChannelTypePrivate, Status: domain.ChannelStatusActive}
}

func member(role domain.WorkspaceRole) *domain.WorkspaceMember {
	return &domain.WorkspaceMember{WorkspaceID: "ws-1", UserID: "user-1", Role: role, Status: domain.MemberStatusActive}
}

func TestCanPinInChannel_WorkspaceAdminAndOwner_Allowed(t *testing.T) {
	for _, role := range []domain.WorkspaceRole{domain.WorkspaceRoleOwner, domain.WorkspaceRoleAdmin} {
		if !domain.CanPinInChannel(member(role), nil, publicChannel()) {
			t.Fatalf("role %q should be allowed to pin", role)
		}
	}
}

func TestCanPinInChannel_RegularMember_Denied(t *testing.T) {
	if domain.CanPinInChannel(member(domain.WorkspaceRoleMember), nil, publicChannel()) {
		t.Fatal("regular member should not be allowed to pin")
	}
}

func TestCanPinInChannel_GuestDenied(t *testing.T) {
	if domain.CanPinInChannel(member(domain.WorkspaceRoleGuest), nil, publicChannel()) {
		t.Fatal("guest should not be allowed to pin")
	}
}

func TestCanPinInChannel_ChannelModerator_Allowed(t *testing.T) {
	cm := &domain.ChannelMember{ChannelID: "ch-1", UserID: "user-1", Role: domain.ChannelRoleModerator}
	if !domain.CanPinInChannel(member(domain.WorkspaceRoleMember), cm, privateChannel()) {
		t.Fatal("channel moderator should be allowed to pin")
	}
}

func TestCanPinInChannel_ChannelMemberRole_Denied(t *testing.T) {
	cm := &domain.ChannelMember{ChannelID: "ch-1", UserID: "user-1", Role: domain.ChannelRoleMember}
	if domain.CanPinInChannel(member(domain.WorkspaceRoleMember), cm, privateChannel()) {
		t.Fatal("plain channel member should not be allowed to pin")
	}
}

func TestCanPinInChannel_NoReadAccess_Denied(t *testing.T) {
	// Admin of another workspace: read access fails, so pin must fail too
	// (multi-tenancy: workspace admin only within their own workspace).
	admin := &domain.WorkspaceMember{WorkspaceID: "ws-other", UserID: "user-1", Role: domain.WorkspaceRoleAdmin, Status: domain.MemberStatusActive}
	if domain.CanPinInChannel(admin, nil, publicChannel()) {
		t.Fatal("admin of a different workspace must not pin here")
	}
	// Admin but private channel they are not a member of → no read access.
	if domain.CanPinInChannel(member(domain.WorkspaceRoleAdmin), nil, privateChannel()) {
		t.Fatal("workspace admin without channel membership: read access is required first")
	}
}

func TestCanPinInChannel_ModeratorMismatchedMembership_Denied(t *testing.T) {
	for _, cm := range []*domain.ChannelMember{
		{ChannelID: "ch-other", UserID: "user-1", Role: domain.ChannelRoleModerator},
		{ChannelID: "ch-1", UserID: "user-other", Role: domain.ChannelRoleModerator},
	} {
		if domain.CanPinInChannel(member(domain.WorkspaceRoleMember), cm, privateChannel()) {
			t.Fatalf("mismatched moderator membership must be denied: %+v", cm)
		}
	}
}
