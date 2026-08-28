package service_test

import (
	"context"
	"errors"
	"testing"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
)

func TestMemberService_JoinWorkspace_Success(t *testing.T) {
	ms := newFakeMemberStore()
	ms.generalChannels["ws-1"] = "ch-geral"
	general := domain.Channel{ID: "ch-geral", WorkspaceID: "ws-1", IsGeneral: true, Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive}
	workspace := domain.Workspace{ID: "ws-1", Status: domain.WorkspaceStatusActive}
	svc := service.NewMemberService(ms, &fakeChannelStore{channels: []domain.Channel{general}}, &fakeWorkspaceStore{workspace: workspace})
	m, err := svc.JoinWorkspace(context.Background(), "ws-1", "user-1", domain.WorkspaceRoleMember)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.UserID != "user-1" || m.Status != domain.MemberStatusActive {
		t.Fatalf("unexpected member: %+v", m)
	}
	if _, ok := ms.channelMembers[cmKey("ch-geral", "user-1")]; !ok {
		t.Fatal("active workspace member should be auto-added to #geral")
	}
}

func TestMemberService_JoinWorkspace_AlreadyMember_ReturnsExisting(t *testing.T) {
	ms := newFakeMemberStore()
	ms.generalChannels["ws-1"] = "ch-geral"
	ms.workspaceMembers[wmKey("ws-1", "user-1")] = domain.WorkspaceMember{
		WorkspaceID: "ws-1", UserID: "user-1",
		Role: domain.WorkspaceRoleAdmin, Status: domain.MemberStatusActive,
	}
	general := domain.Channel{ID: "ch-geral", WorkspaceID: "ws-1", IsGeneral: true, Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive}
	workspace := domain.Workspace{ID: "ws-1", Status: domain.WorkspaceStatusActive}
	svc := service.NewMemberService(ms, &fakeChannelStore{channels: []domain.Channel{general}}, &fakeWorkspaceStore{workspace: workspace})
	m, err := svc.JoinWorkspace(context.Background(), "ws-1", "user-1", domain.WorkspaceRoleMember)
	if err != nil {
		t.Fatalf("unexpected error on already-member: %v", err)
	}
	if m.Role != domain.WorkspaceRoleAdmin {
		t.Fatalf("expected existing role admin, got %q", m.Role)
	}
	if _, ok := ms.channelMembers[cmKey("ch-geral", "user-1")]; !ok {
		t.Fatal("existing active workspace member should be synced to #geral")
	}
}

func TestMemberService_JoinWorkspace_DisabledWorkspaceDenied(t *testing.T) {
	ms := newFakeMemberStore()
	ms.generalChannels["ws-1"] = "ch-geral"
	ms.workspaceStatus["ws-1"] = domain.WorkspaceStatusDisabled
	general := domain.Channel{ID: "ch-geral", WorkspaceID: "ws-1", IsGeneral: true, Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive}
	workspace := domain.Workspace{ID: "ws-1", Status: domain.WorkspaceStatusDisabled}

	_, err := service.NewMemberService(ms, &fakeChannelStore{channels: []domain.Channel{general}}, &fakeWorkspaceStore{workspace: workspace}).JoinWorkspace(
		context.Background(), "ws-1", "user-1", domain.WorkspaceRoleMember,
	)
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("expected ErrForbidden, got %v", err)
	}
	if _, ok := ms.workspaceMembers[wmKey("ws-1", "user-1")]; ok {
		t.Fatal("disabled workspace must not create an active workspace member")
	}
	if _, ok := ms.channelMembers[cmKey("ch-geral", "user-1")]; ok {
		t.Fatal("disabled workspace must not auto-add #geral membership")
	}
}

func TestMemberService_JoinWorkspace_MissingGeneralReturnsExplicitError(t *testing.T) {
	ms := newFakeMemberStore()
	workspace := domain.Workspace{ID: "ws-1", Status: domain.WorkspaceStatusActive}

	_, err := service.NewMemberService(ms, &fakeChannelStore{}, &fakeWorkspaceStore{workspace: workspace}).JoinWorkspace(
		context.Background(), "ws-1", "user-1", domain.WorkspaceRoleMember,
	)
	if !errors.Is(err, domain.ErrGeneralChannelMissing) {
		t.Fatalf("expected ErrGeneralChannelMissing, got %v", err)
	}
	if _, ok := ms.workspaceMembers[wmKey("ws-1", "user-1")]; ok {
		t.Fatal("missing #geral must not leave a newly active workspace member unsynced")
	}
}

func TestMemberService_JoinWorkspace_DoesNotUseOtherWorkspaceGeneral(t *testing.T) {
	ms := newFakeMemberStore()
	ms.generalChannels["ws-b"] = "geral-b"
	workspace := domain.Workspace{ID: "ws-a", Status: domain.WorkspaceStatusActive}

	_, err := service.NewMemberService(ms, &fakeChannelStore{}, &fakeWorkspaceStore{workspace: workspace}).JoinWorkspace(
		context.Background(), "ws-a", "user-1", domain.WorkspaceRoleMember,
	)
	if !errors.Is(err, domain.ErrGeneralChannelMissing) {
		t.Fatalf("expected ErrGeneralChannelMissing, got %v", err)
	}
	if _, ok := ms.channelMembers[cmKey("geral-b", "user-1")]; ok {
		t.Fatal("workspace A member must not be inserted into workspace B #geral")
	}
}

func TestMemberService_ActivateWorkspaceMember_SyncsGeneral(t *testing.T) {
	ms := newFakeMemberStore()
	ms.generalChannels["ws-1"] = "ch-geral"
	ms.workspaceMembers[wmKey("ws-1", "user-1")] = domain.WorkspaceMember{
		WorkspaceID: "ws-1", UserID: "user-1", Role: domain.WorkspaceRoleMember, Status: domain.MemberStatusLeft,
	}
	general := domain.Channel{ID: "ch-geral", WorkspaceID: "ws-1", IsGeneral: true, Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive}
	workspace := domain.Workspace{ID: "ws-1", Status: domain.WorkspaceStatusActive}

	m, err := service.NewMemberService(ms, &fakeChannelStore{channels: []domain.Channel{general}}, &fakeWorkspaceStore{workspace: workspace}).ActivateWorkspaceMember(
		context.Background(), "ws-1", "user-1",
	)
	if err != nil {
		t.Fatalf("unexpected activation error: %v", err)
	}
	if m.Status != domain.MemberStatusActive {
		t.Fatalf("expected active member, got %+v", m)
	}
	if _, ok := ms.channelMembers[cmKey("ch-geral", "user-1")]; !ok {
		t.Fatal("reactivated workspace member should be synced to #geral")
	}
}

func TestMemberService_SyncGeneralMemberships_ActiveMembersOnly(t *testing.T) {
	ms := newFakeMemberStore()
	ms.generalChannels["ws-1"] = "ch-geral"
	ms.workspaceMembers[wmKey("ws-1", "active")] = activeMembership("ws-1", "active")
	ms.workspaceMembers[wmKey("ws-1", "suspended")] = domain.WorkspaceMember{WorkspaceID: "ws-1", UserID: "suspended", Role: domain.WorkspaceRoleMember, Status: domain.MemberStatusSuspended}
	ms.workspaceMembers[wmKey("ws-1", "left")] = domain.WorkspaceMember{WorkspaceID: "ws-1", UserID: "left", Role: domain.WorkspaceRoleMember, Status: domain.MemberStatusLeft}
	general := domain.Channel{ID: "ch-geral", WorkspaceID: "ws-1", IsGeneral: true, Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive}
	workspace := domain.Workspace{ID: "ws-1", Status: domain.WorkspaceStatusActive}

	inserted, err := service.NewMemberService(ms, &fakeChannelStore{channels: []domain.Channel{general}}, &fakeWorkspaceStore{workspace: workspace}).SyncGeneralMemberships(
		context.Background(), "ws-1",
	)
	if err != nil {
		t.Fatalf("unexpected sync error: %v", err)
	}
	if inserted != 1 {
		t.Fatalf("expected 1 inserted membership, got %d", inserted)
	}
	if _, ok := ms.channelMembers[cmKey("ch-geral", "active")]; !ok {
		t.Fatal("active member should be synced to #geral")
	}
	for _, userID := range []string{"suspended", "left"} {
		if _, ok := ms.channelMembers[cmKey("ch-geral", userID)]; ok {
			t.Fatalf("%s member must not be synced to #geral", userID)
		}
	}
}

func TestMemberService_EnsureGeneralMembership_AddsToGeneral(t *testing.T) {
	ms := newFakeMemberStore()
	ms.generalChannels["ws-1"] = "ch-geral"
	ms.workspaceMembers[wmKey("ws-1", "user-1")] = activeMembership("ws-1", "user-1")
	general := domain.Channel{ID: "ch-geral", WorkspaceID: "ws-1", IsGeneral: true, Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive}
	workspace := domain.Workspace{ID: "ws-1", Status: domain.WorkspaceStatusActive}
	svc := service.NewMemberService(ms, &fakeChannelStore{channel: general, channels: []domain.Channel{general}}, &fakeWorkspaceStore{workspace: workspace})
	if err := svc.EnsureGeneralMembership(context.Background(), "ws-1", "user-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := ms.channelMembers[cmKey("ch-geral", "user-1")]; !ok {
		t.Fatal("expected user to be added to #geral channel")
	}
}

func TestMemberService_EnsureGeneralMembership_Idempotent(t *testing.T) {
	ms := newFakeMemberStore()
	ms.generalChannels["ws-1"] = "ch-geral"
	ms.channelMembers[cmKey("ch-geral", "user-1")] = domain.ChannelMember{
		ChannelID: "ch-geral", UserID: "user-1", Role: domain.ChannelRoleMember,
	}
	ms.workspaceMembers[wmKey("ws-1", "user-1")] = activeMembership("ws-1", "user-1")
	general := domain.Channel{ID: "ch-geral", WorkspaceID: "ws-1", IsGeneral: true, Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive}
	workspace := domain.Workspace{ID: "ws-1", Status: domain.WorkspaceStatusActive}
	svc := service.NewMemberService(ms, &fakeChannelStore{channel: general, channels: []domain.Channel{general}}, &fakeWorkspaceStore{workspace: workspace})
	if err := svc.EnsureGeneralMembership(context.Background(), "ws-1", "user-1"); err != nil {
		t.Fatalf("idempotent call should not error, got %v", err)
	}
}

func TestMemberService_EnsureGeneralMembership_NoGeneralChannel_ReturnsExplicitError(t *testing.T) {
	ms := newFakeMemberStore()
	ms.workspaceMembers[wmKey("ws-1", "user-1")] = activeMembership("ws-1", "user-1")
	pub := domain.Channel{ID: "ch-pub", WorkspaceID: "ws-1", IsGeneral: false, Type: domain.ChannelTypePublic}
	workspace := domain.Workspace{ID: "ws-1", Status: domain.WorkspaceStatusActive}
	svc := service.NewMemberService(ms, &fakeChannelStore{channels: []domain.Channel{pub}}, &fakeWorkspaceStore{workspace: workspace})
	if err := svc.EnsureGeneralMembership(context.Background(), "ws-1", "user-1"); !errors.Is(err, domain.ErrGeneralChannelMissing) {
		t.Fatalf("expected ErrGeneralChannelMissing, got %v", err)
	}
}

func TestMemberService_EnsureGeneralMembership_SuspendedAndLeftMembersAreSkipped(t *testing.T) {
	for _, status := range []domain.MemberStatus{domain.MemberStatusSuspended, domain.MemberStatusLeft} {
		t.Run(string(status), func(t *testing.T) {
			ms := newFakeMemberStore()
			ms.generalChannels["ws-1"] = "ch-geral"
			ms.workspaceMembers[wmKey("ws-1", "user-1")] = domain.WorkspaceMember{WorkspaceID: "ws-1", UserID: "user-1", Role: domain.WorkspaceRoleMember, Status: status}
			general := domain.Channel{ID: "ch-geral", WorkspaceID: "ws-1", IsGeneral: true, Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive}
			workspace := domain.Workspace{ID: "ws-1", Status: domain.WorkspaceStatusActive}

			err := service.NewMemberService(ms, &fakeChannelStore{channels: []domain.Channel{general}}, &fakeWorkspaceStore{workspace: workspace}).EnsureGeneralMembership(
				context.Background(), "ws-1", "user-1",
			)
			if !errors.Is(err, domain.ErrMemberInactive) {
				t.Fatalf("expected ErrMemberInactive, got %v", err)
			}
			if _, ok := ms.channelMembers[cmKey("ch-geral", "user-1")]; ok {
				t.Fatal("inactive workspace member must not be added to #geral")
			}
		})
	}
}

// --- SelfJoinChannel ---

func TestMemberService_SelfJoinChannel_PublicChannel_Success(t *testing.T) {
	ms := newFakeMemberStore()
	ch := domain.Channel{ID: "ch-1", WorkspaceID: "ws-1", Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive}
	ws := domain.Workspace{ID: "ws-1", Status: domain.WorkspaceStatusActive}
	ms.workspaceMembers[wmKey("ws-1", "user-1")] = domain.WorkspaceMember{
		WorkspaceID: "ws-1", UserID: "user-1", Role: domain.WorkspaceRoleMember, Status: domain.MemberStatusActive,
	}
	svc := service.NewMemberService(ms, &fakeChannelStore{channel: ch}, &fakeWorkspaceStore{workspace: ws})
	m, err := svc.SelfJoinChannel(context.Background(), "ws-1", "ch-1", "user-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.ChannelID != "ch-1" || m.UserID != "user-1" || m.Role != domain.ChannelRoleMember {
		t.Fatalf("unexpected member: %+v", m)
	}
}

// RF-74: self-join is the shortest path around guest isolation there is —
// scoping a guest to the channels it was added to means nothing if it can add
// itself to any public channel. Owner, admin, moderator and member still join
// freely; a guest and an unrecognised role are refused and write nothing.
func TestMemberService_SelfJoinChannel_FollowsCanReachPublicChannels(t *testing.T) {
	for _, role := range []domain.WorkspaceRole{
		domain.WorkspaceRoleOwner, domain.WorkspaceRoleAdmin, domain.WorkspaceRoleModerator,
		domain.WorkspaceRoleMember, domain.WorkspaceRoleGuest, domain.WorkspaceRole("wizard"),
	} {
		t.Run(string(role), func(t *testing.T) {
			ms := newFakeMemberStore()
			ch := domain.Channel{ID: "ch-1", WorkspaceID: "ws-1", Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive}
			ws := domain.Workspace{ID: "ws-1", Status: domain.WorkspaceStatusActive}
			ms.workspaceMembers[wmKey("ws-1", "user-1")] = domain.WorkspaceMember{
				WorkspaceID: "ws-1", UserID: "user-1", Role: role, Status: domain.MemberStatusActive,
			}
			svc := service.NewMemberService(ms, &fakeChannelStore{channel: ch}, &fakeWorkspaceStore{workspace: ws})

			_, err := svc.SelfJoinChannel(context.Background(), "ws-1", "ch-1", "user-1")
			allowed := domain.CanReachPublicChannels(&domain.WorkspaceMember{Role: role, Status: domain.MemberStatusActive})

			if allowed && err != nil {
				t.Fatalf("predicate allows %s but the service refused: %v", role, err)
			}
			if !allowed {
				if !errors.Is(err, domain.ErrForbidden) {
					t.Fatalf("predicate denies %s but the service returned %v", role, err)
				}
				if _, ok := ms.channelMembers[cmKey("ch-1", "user-1")]; ok {
					t.Fatal("a refused caller joined the channel anyway")
				}
			}
		})
	}
}

func TestMemberService_SelfJoinChannel_DuplicateJoin_Idempotent(t *testing.T) {
	ms := newFakeMemberStore()
	ch := domain.Channel{ID: "ch-1", WorkspaceID: "ws-1", Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive}
	ws := domain.Workspace{ID: "ws-1", Status: domain.WorkspaceStatusActive}
	ms.workspaceMembers[wmKey("ws-1", "user-1")] = domain.WorkspaceMember{
		WorkspaceID: "ws-1", UserID: "user-1", Role: domain.WorkspaceRoleMember, Status: domain.MemberStatusActive,
	}
	ms.channelMembers[cmKey("ch-1", "user-1")] = domain.ChannelMember{
		ChannelID: "ch-1", UserID: "user-1", Role: domain.ChannelRoleMember,
	}
	svc := service.NewMemberService(ms, &fakeChannelStore{channel: ch}, &fakeWorkspaceStore{workspace: ws})
	m, err := svc.SelfJoinChannel(context.Background(), "ws-1", "ch-1", "user-1")
	if err != nil {
		t.Fatalf("duplicate join should be idempotent: %v", err)
	}
	if m.ChannelID != "ch-1" || m.UserID != "user-1" {
		t.Fatalf("unexpected member: %+v", m)
	}
}

func TestMemberService_SelfJoinChannel_GeneralChannel_Idempotent(t *testing.T) {
	ms := newFakeMemberStore()
	ch := domain.Channel{ID: "ch-geral", WorkspaceID: "ws-1", Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive, IsGeneral: true}
	ws := domain.Workspace{ID: "ws-1", Status: domain.WorkspaceStatusActive}
	ms.workspaceMembers[wmKey("ws-1", "user-1")] = domain.WorkspaceMember{
		WorkspaceID: "ws-1", UserID: "user-1", Role: domain.WorkspaceRoleMember, Status: domain.MemberStatusActive,
	}
	svc := service.NewMemberService(ms, &fakeChannelStore{channel: ch}, &fakeWorkspaceStore{workspace: ws})
	m, err := svc.SelfJoinChannel(context.Background(), "ws-1", "ch-geral", "user-1")
	if err != nil {
		t.Fatalf("#geral explicit join should be idempotent: %v", err)
	}
	if m.ChannelID != "ch-geral" || m.UserID != "user-1" {
		t.Fatalf("unexpected member: %+v", m)
	}
}

func TestMemberService_SelfJoinChannel_PrivateChannel_ReturnsNotFound(t *testing.T) {
	ms := newFakeMemberStore()
	ch := domain.Channel{ID: "ch-priv", WorkspaceID: "ws-1", Type: domain.ChannelTypePrivate, Status: domain.ChannelStatusActive}
	ws := domain.Workspace{ID: "ws-1", Status: domain.WorkspaceStatusActive}
	ms.workspaceMembers[wmKey("ws-1", "user-1")] = domain.WorkspaceMember{
		WorkspaceID: "ws-1", UserID: "user-1", Role: domain.WorkspaceRoleMember, Status: domain.MemberStatusActive,
	}
	svc := service.NewMemberService(ms, &fakeChannelStore{channel: ch}, &fakeWorkspaceStore{workspace: ws})
	_, err := svc.SelfJoinChannel(context.Background(), "ws-1", "ch-priv", "user-1")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("private channel self-join must be non-enumerating (ErrNotFound), got: %v", err)
	}
}

func TestMemberService_SelfJoinChannel_NonWorkspaceMember_Denied(t *testing.T) {
	ms := newFakeMemberStore()
	ch := domain.Channel{ID: "ch-1", WorkspaceID: "ws-1", Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive}
	ws := domain.Workspace{ID: "ws-1", Status: domain.WorkspaceStatusActive}
	svc := service.NewMemberService(ms, &fakeChannelStore{channel: ch}, &fakeWorkspaceStore{workspace: ws})
	_, err := svc.SelfJoinChannel(context.Background(), "ws-1", "ch-1", "user-2")
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("non-workspace member should get ErrForbidden, got: %v", err)
	}
}

func TestMemberService_SelfJoinChannel_SuspendedMember_Denied(t *testing.T) {
	ms := newFakeMemberStore()
	ch := domain.Channel{ID: "ch-1", WorkspaceID: "ws-1", Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive}
	ws := domain.Workspace{ID: "ws-1", Status: domain.WorkspaceStatusActive}
	ms.workspaceMembers[wmKey("ws-1", "user-1")] = domain.WorkspaceMember{
		WorkspaceID: "ws-1", UserID: "user-1", Role: domain.WorkspaceRoleMember, Status: domain.MemberStatusSuspended,
	}
	// Stale channel_members row must not bypass active workspace-member status check
	ms.channelMembers[cmKey("ch-1", "user-1")] = domain.ChannelMember{
		ChannelID: "ch-1", UserID: "user-1", Role: domain.ChannelRoleMember,
	}
	svc := service.NewMemberService(ms, &fakeChannelStore{channel: ch}, &fakeWorkspaceStore{workspace: ws})
	_, err := svc.SelfJoinChannel(context.Background(), "ws-1", "ch-1", "user-1")
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("suspended member should get ErrForbidden even with stale channel membership row, got: %v", err)
	}
}

func TestMemberService_SelfJoinChannel_LeftMember_Denied(t *testing.T) {
	ms := newFakeMemberStore()
	ch := domain.Channel{ID: "ch-1", WorkspaceID: "ws-1", Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive}
	ws := domain.Workspace{ID: "ws-1", Status: domain.WorkspaceStatusActive}
	ms.workspaceMembers[wmKey("ws-1", "user-1")] = domain.WorkspaceMember{
		WorkspaceID: "ws-1", UserID: "user-1", Role: domain.WorkspaceRoleMember, Status: domain.MemberStatusLeft,
	}
	// Stale channel_members row must not bypass active workspace-member status check
	ms.channelMembers[cmKey("ch-1", "user-1")] = domain.ChannelMember{
		ChannelID: "ch-1", UserID: "user-1", Role: domain.ChannelRoleMember,
	}
	svc := service.NewMemberService(ms, &fakeChannelStore{channel: ch}, &fakeWorkspaceStore{workspace: ws})
	_, err := svc.SelfJoinChannel(context.Background(), "ws-1", "ch-1", "user-1")
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("left member should get ErrForbidden even with stale channel membership row, got: %v", err)
	}
}

func TestMemberService_SelfJoinChannel_DisabledWorkspace_Denied(t *testing.T) {
	ms := newFakeMemberStore()
	ch := domain.Channel{ID: "ch-1", WorkspaceID: "ws-1", Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive}
	ws := domain.Workspace{ID: "ws-1", Status: domain.WorkspaceStatusDisabled}
	// Seed active workspace membership so denial is from disabled workspace, not missing member
	ms.workspaceMembers[wmKey("ws-1", "user-1")] = domain.WorkspaceMember{
		WorkspaceID: "ws-1", UserID: "user-1", Role: domain.WorkspaceRoleMember, Status: domain.MemberStatusActive,
	}
	svc := service.NewMemberService(ms, &fakeChannelStore{channel: ch}, &fakeWorkspaceStore{workspace: ws})
	_, err := svc.SelfJoinChannel(context.Background(), "ws-1", "ch-1", "user-1")
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("disabled workspace should get ErrForbidden, got: %v", err)
	}
}

func TestMemberService_SelfJoinChannel_ArchivedChannel_Denied(t *testing.T) {
	ms := newFakeMemberStore()
	ws := domain.Workspace{ID: "ws-1", Status: domain.WorkspaceStatusActive}
	ms.workspaceMembers[wmKey("ws-1", "user-1")] = domain.WorkspaceMember{
		WorkspaceID: "ws-1", UserID: "user-1", Role: domain.WorkspaceRoleMember, Status: domain.MemberStatusActive,
	}
	// fakeChannelStore.GetChannelByIDInWorkspace returns ErrNotFound for archived channels
	archivedCh := domain.Channel{ID: "ch-arch", WorkspaceID: "ws-1", Type: domain.ChannelTypePublic, Status: domain.ChannelStatusArchived}
	svc := service.NewMemberService(ms, &fakeChannelStore{channel: archivedCh}, &fakeWorkspaceStore{workspace: ws})
	_, err := svc.SelfJoinChannel(context.Background(), "ws-1", "ch-arch", "user-1")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("archived channel should return ErrNotFound (workspace-scoped lookup filters active), got: %v", err)
	}
}

func TestMemberService_SelfJoinChannel_CrossWorkspace_Denied(t *testing.T) {
	ms := newFakeMemberStore()
	// Channel belongs to ws-2; caller asserts ws-1
	chInOtherWS := domain.Channel{ID: "ch-2", WorkspaceID: "ws-2", Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive}
	ws := domain.Workspace{ID: "ws-1", Status: domain.WorkspaceStatusActive}
	ms.workspaceMembers[wmKey("ws-1", "user-1")] = domain.WorkspaceMember{
		WorkspaceID: "ws-1", UserID: "user-1", Role: domain.WorkspaceRoleMember, Status: domain.MemberStatusActive,
	}
	// fakeChannelStore.GetChannelByIDInWorkspace returns ErrNotFound when workspaceID doesn't match
	svc := service.NewMemberService(ms, &fakeChannelStore{channel: chInOtherWS}, &fakeWorkspaceStore{workspace: ws})
	_, err := svc.SelfJoinChannel(context.Background(), "ws-1", "ch-2", "user-1")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("cross-workspace channel should return ErrNotFound, got: %v", err)
	}
}

func TestMemberService_SelfJoinChannel_RoleIsAlwaysMember(t *testing.T) {
	ms := newFakeMemberStore()
	ch := domain.Channel{ID: "ch-1", WorkspaceID: "ws-1", Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive}
	ws := domain.Workspace{ID: "ws-1", Status: domain.WorkspaceStatusActive}
	ms.workspaceMembers[wmKey("ws-1", "user-1")] = domain.WorkspaceMember{
		WorkspaceID: "ws-1", UserID: "user-1", Role: domain.WorkspaceRoleOwner, Status: domain.MemberStatusActive,
	}
	svc := service.NewMemberService(ms, &fakeChannelStore{channel: ch}, &fakeWorkspaceStore{workspace: ws})
	m, err := svc.SelfJoinChannel(context.Background(), "ws-1", "ch-1", "user-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.Role != domain.ChannelRoleMember {
		t.Fatalf("channel role must always be ChannelRoleMember even for owner, got %q", m.Role)
	}
}

// --- LeaveChannel ---

func TestMemberService_LeaveChannel_PublicChannel_RemovesRow(t *testing.T) {
	ms := newFakeMemberStore()
	ch := domain.Channel{ID: "ch-1", WorkspaceID: "ws-1", Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive}
	ms.channelMembers[cmKey("ch-1", "user-1")] = domain.ChannelMember{ChannelID: "ch-1", UserID: "user-1", Role: domain.ChannelRoleMember}
	svc := service.NewMemberService(ms, &fakeChannelStore{channel: ch}, &fakeWorkspaceStore{})
	if err := svc.LeaveChannel(context.Background(), "ws-1", "ch-1", "user-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := ms.channelMembers[cmKey("ch-1", "user-1")]; ok {
		t.Fatal("channel_members row should have been deleted")
	}
}

func TestMemberService_LeaveChannel_PrivateChannel_RemovesRow(t *testing.T) {
	ms := newFakeMemberStore()
	ch := domain.Channel{ID: "ch-priv", WorkspaceID: "ws-1", Type: domain.ChannelTypePrivate, Status: domain.ChannelStatusActive}
	ms.channelMembers[cmKey("ch-priv", "user-1")] = domain.ChannelMember{ChannelID: "ch-priv", UserID: "user-1", Role: domain.ChannelRoleMember}
	svc := service.NewMemberService(ms, &fakeChannelStore{channel: ch}, &fakeWorkspaceStore{})
	if err := svc.LeaveChannel(context.Background(), "ws-1", "ch-priv", "user-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := ms.channelMembers[cmKey("ch-priv", "user-1")]; ok {
		t.Fatal("channel_members row should have been deleted")
	}
}

func TestMemberService_LeaveChannel_GeneralChannel_Denied(t *testing.T) {
	ms := newFakeMemberStore()
	ch := domain.Channel{ID: "ch-geral", WorkspaceID: "ws-1", Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive, IsGeneral: true}
	svc := service.NewMemberService(ms, &fakeChannelStore{channel: ch}, &fakeWorkspaceStore{})
	err := svc.LeaveChannel(context.Background(), "ws-1", "ch-geral", "user-1")
	if !errors.Is(err, domain.ErrCannotLeaveGeneralChannel) {
		t.Fatalf("leaving #geral should return ErrCannotLeaveGeneralChannel, got: %v", err)
	}
}

func TestMemberService_LeaveChannel_NonMember_Idempotent(t *testing.T) {
	ms := newFakeMemberStore()
	ch := domain.Channel{ID: "ch-1", WorkspaceID: "ws-1", Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive}
	svc := service.NewMemberService(ms, &fakeChannelStore{channel: ch}, &fakeWorkspaceStore{})
	if err := svc.LeaveChannel(context.Background(), "ws-1", "ch-1", "user-1"); err != nil {
		t.Fatalf("non-member leave should be idempotent (nil), got: %v", err)
	}
}

func TestMemberService_LeaveChannel_ArchivedChannel_ReturnsNotFound(t *testing.T) {
	ms := newFakeMemberStore()
	// Archived channel: GetChannelByIDInWorkspace returns ErrNotFound (filters status=active)
	archivedCh := domain.Channel{ID: "ch-arch", WorkspaceID: "ws-1", Type: domain.ChannelTypePublic, Status: domain.ChannelStatusArchived}
	svc := service.NewMemberService(ms, &fakeChannelStore{channel: archivedCh}, &fakeWorkspaceStore{})
	err := svc.LeaveChannel(context.Background(), "ws-1", "ch-arch", "user-1")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("archived channel leave should return ErrNotFound, got: %v", err)
	}
}

func TestMemberService_LeaveChannel_CrossWorkspace_ReturnsNotFound(t *testing.T) {
	ms := newFakeMemberStore()
	chInOtherWS := domain.Channel{ID: "ch-2", WorkspaceID: "ws-2", Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive}
	svc := service.NewMemberService(ms, &fakeChannelStore{channel: chInOtherWS}, &fakeWorkspaceStore{})
	err := svc.LeaveChannel(context.Background(), "ws-1", "ch-2", "user-1")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("cross-workspace leave should return ErrNotFound, got: %v", err)
	}
}

// --- RemoveMemberFromChannel ---

func TestMemberService_RemoveMemberFromChannel_OwnerCanRemove(t *testing.T) {
	ms := newFakeMemberStore()
	ch := domain.Channel{ID: "ch-1", WorkspaceID: "ws-1", Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive}
	ws := domain.Workspace{ID: "ws-1", Status: domain.WorkspaceStatusActive}
	ms.workspaceMembers[wmKey("ws-1", "owner-1")] = domain.WorkspaceMember{
		WorkspaceID: "ws-1", UserID: "owner-1", Role: domain.WorkspaceRoleOwner, Status: domain.MemberStatusActive,
	}
	ms.channelMembers[cmKey("ch-1", "user-1")] = domain.ChannelMember{ChannelID: "ch-1", UserID: "user-1", Role: domain.ChannelRoleMember}
	svc := service.NewMemberService(ms, &fakeChannelStore{channel: ch}, &fakeWorkspaceStore{workspace: ws})
	if err := svc.RemoveMemberFromChannel(context.Background(), "ws-1", "ch-1", "owner-1", "user-1"); err != nil {
		t.Fatalf("owner should be able to remove member: %v", err)
	}
	if _, ok := ms.channelMembers[cmKey("ch-1", "user-1")]; ok {
		t.Fatal("channel_members row should have been deleted")
	}
}

func TestMemberService_RemoveMemberFromChannel_AdminCanRemove(t *testing.T) {
	ms := newFakeMemberStore()
	ch := domain.Channel{ID: "ch-1", WorkspaceID: "ws-1", Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive}
	ws := domain.Workspace{ID: "ws-1", Status: domain.WorkspaceStatusActive}
	ms.workspaceMembers[wmKey("ws-1", "admin-1")] = domain.WorkspaceMember{
		WorkspaceID: "ws-1", UserID: "admin-1", Role: domain.WorkspaceRoleAdmin, Status: domain.MemberStatusActive,
	}
	ms.channelMembers[cmKey("ch-1", "user-1")] = domain.ChannelMember{ChannelID: "ch-1", UserID: "user-1", Role: domain.ChannelRoleMember}
	svc := service.NewMemberService(ms, &fakeChannelStore{channel: ch}, &fakeWorkspaceStore{workspace: ws})
	if err := svc.RemoveMemberFromChannel(context.Background(), "ws-1", "ch-1", "admin-1", "user-1"); err != nil {
		t.Fatalf("admin should be able to remove member: %v", err)
	}
	if _, ok := ms.channelMembers[cmKey("ch-1", "user-1")]; ok {
		t.Fatal("channel_members row should have been deleted")
	}
}

func TestMemberService_RemoveMemberFromChannel_MemberRoleDenied(t *testing.T) {
	ms := newFakeMemberStore()
	ch := domain.Channel{ID: "ch-1", WorkspaceID: "ws-1", Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive}
	ws := domain.Workspace{ID: "ws-1", Status: domain.WorkspaceStatusActive}
	ms.workspaceMembers[wmKey("ws-1", "member-1")] = domain.WorkspaceMember{
		WorkspaceID: "ws-1", UserID: "member-1", Role: domain.WorkspaceRoleMember, Status: domain.MemberStatusActive,
	}
	svc := service.NewMemberService(ms, &fakeChannelStore{channel: ch}, &fakeWorkspaceStore{workspace: ws})
	if err := svc.RemoveMemberFromChannel(context.Background(), "ws-1", "ch-1", "member-1", "user-2"); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("member role should get ErrForbidden, got: %v", err)
	}
}

func TestMemberService_RemoveMemberFromChannel_GuestRoleDenied(t *testing.T) {
	ms := newFakeMemberStore()
	ch := domain.Channel{ID: "ch-1", WorkspaceID: "ws-1", Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive}
	ws := domain.Workspace{ID: "ws-1", Status: domain.WorkspaceStatusActive}
	ms.workspaceMembers[wmKey("ws-1", "guest-1")] = domain.WorkspaceMember{
		WorkspaceID: "ws-1", UserID: "guest-1", Role: domain.WorkspaceRoleGuest, Status: domain.MemberStatusActive,
	}
	svc := service.NewMemberService(ms, &fakeChannelStore{channel: ch}, &fakeWorkspaceStore{workspace: ws})
	if err := svc.RemoveMemberFromChannel(context.Background(), "ws-1", "ch-1", "guest-1", "user-2"); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("guest role should get ErrForbidden, got: %v", err)
	}
}

// Removal deliberately remains on the pre-#705 management predicate. The route
// must consult that predicate and never inherit CanAddChannelMembers.
func TestMemberService_RemoveMemberFromChannel_FollowsCanManageChannelMembers(t *testing.T) {
	for _, role := range []domain.WorkspaceRole{
		domain.WorkspaceRoleOwner, domain.WorkspaceRoleAdmin, domain.WorkspaceRoleModerator,
		domain.WorkspaceRoleMember, domain.WorkspaceRoleGuest, domain.WorkspaceRole("wizard"),
	} {
		t.Run(string(role), func(t *testing.T) {
			ms := newFakeMemberStore()
			ch := domain.Channel{ID: "ch-1", WorkspaceID: "ws-1", Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive}
			ws := domain.Workspace{ID: "ws-1", Status: domain.WorkspaceStatusActive}
			ms.workspaceMembers[wmKey("ws-1", "caller-1")] = domain.WorkspaceMember{
				WorkspaceID: "ws-1", UserID: "caller-1", Role: role, Status: domain.MemberStatusActive,
			}
			ms.channelMembers[cmKey("ch-1", "user-1")] = domain.ChannelMember{ChannelID: "ch-1", UserID: "user-1", Role: domain.ChannelRoleMember}
			svc := service.NewMemberService(ms, &fakeChannelStore{channel: ch}, &fakeWorkspaceStore{workspace: ws})

			err := svc.RemoveMemberFromChannel(context.Background(), "ws-1", "ch-1", "caller-1", "user-1")
			allowed := domain.CanManageChannelMembers(&domain.WorkspaceMember{Role: role, Status: domain.MemberStatusActive})

			if allowed && err != nil {
				t.Fatalf("predicate allows %s but the service refused: %v", role, err)
			}
			if !allowed {
				if !errors.Is(err, domain.ErrForbidden) {
					t.Fatalf("predicate denies %s but the service returned %v", role, err)
				}
				if _, ok := ms.channelMembers[cmKey("ch-1", "user-1")]; !ok {
					t.Fatal("a refused caller removed a membership")
				}
			}
		})
	}
}

func TestMemberService_RemoveMemberFromChannel_GeneralChannel_Denied(t *testing.T) {
	ms := newFakeMemberStore()
	ch := domain.Channel{ID: "ch-geral", WorkspaceID: "ws-1", Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive, IsGeneral: true}
	ws := domain.Workspace{ID: "ws-1", Status: domain.WorkspaceStatusActive}
	ms.workspaceMembers[wmKey("ws-1", "owner-1")] = domain.WorkspaceMember{
		WorkspaceID: "ws-1", UserID: "owner-1", Role: domain.WorkspaceRoleOwner, Status: domain.MemberStatusActive,
	}
	svc := service.NewMemberService(ms, &fakeChannelStore{channel: ch}, &fakeWorkspaceStore{workspace: ws})
	if err := svc.RemoveMemberFromChannel(context.Background(), "ws-1", "ch-geral", "owner-1", "user-1"); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("removing from #geral should return ErrForbidden, got: %v", err)
	}
}

// Private channel non-enumeration for every caller state

func TestMemberService_SelfJoinChannel_PrivateChannel_NonMember_ReturnsNotFound(t *testing.T) {
	ms := newFakeMemberStore()
	ch := domain.Channel{ID: "ch-priv", WorkspaceID: "ws-1", Type: domain.ChannelTypePrivate, Status: domain.ChannelStatusActive}
	ws := domain.Workspace{ID: "ws-1", Status: domain.WorkspaceStatusActive}
	// user-2 has no workspace membership
	svc := service.NewMemberService(ms, &fakeChannelStore{channel: ch}, &fakeWorkspaceStore{workspace: ws})
	_, err := svc.SelfJoinChannel(context.Background(), "ws-1", "ch-priv", "user-2")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("private channel + non-member must return ErrNotFound, got: %v", err)
	}
}

func TestMemberService_SelfJoinChannel_PrivateChannel_SuspendedMember_ReturnsNotFound(t *testing.T) {
	ms := newFakeMemberStore()
	ch := domain.Channel{ID: "ch-priv", WorkspaceID: "ws-1", Type: domain.ChannelTypePrivate, Status: domain.ChannelStatusActive}
	ws := domain.Workspace{ID: "ws-1", Status: domain.WorkspaceStatusActive}
	ms.workspaceMembers[wmKey("ws-1", "user-1")] = domain.WorkspaceMember{
		WorkspaceID: "ws-1", UserID: "user-1", Role: domain.WorkspaceRoleMember, Status: domain.MemberStatusSuspended,
	}
	svc := service.NewMemberService(ms, &fakeChannelStore{channel: ch}, &fakeWorkspaceStore{workspace: ws})
	_, err := svc.SelfJoinChannel(context.Background(), "ws-1", "ch-priv", "user-1")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("private channel + suspended member must return ErrNotFound, got: %v", err)
	}
}

func TestMemberService_SelfJoinChannel_PrivateChannel_LeftMember_ReturnsNotFound(t *testing.T) {
	ms := newFakeMemberStore()
	ch := domain.Channel{ID: "ch-priv", WorkspaceID: "ws-1", Type: domain.ChannelTypePrivate, Status: domain.ChannelStatusActive}
	ws := domain.Workspace{ID: "ws-1", Status: domain.WorkspaceStatusActive}
	ms.workspaceMembers[wmKey("ws-1", "user-1")] = domain.WorkspaceMember{
		WorkspaceID: "ws-1", UserID: "user-1", Role: domain.WorkspaceRoleMember, Status: domain.MemberStatusLeft,
	}
	svc := service.NewMemberService(ms, &fakeChannelStore{channel: ch}, &fakeWorkspaceStore{workspace: ws})
	_, err := svc.SelfJoinChannel(context.Background(), "ws-1", "ch-priv", "user-1")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("private channel + left member must return ErrNotFound, got: %v", err)
	}
}

func TestMemberService_SelfJoinChannel_PrivateChannel_DisabledWorkspace_ReturnsNotFound(t *testing.T) {
	ms := newFakeMemberStore()
	ch := domain.Channel{ID: "ch-priv", WorkspaceID: "ws-1", Type: domain.ChannelTypePrivate, Status: domain.ChannelStatusActive}
	ws := domain.Workspace{ID: "ws-1", Status: domain.WorkspaceStatusDisabled}
	ms.workspaceMembers[wmKey("ws-1", "user-1")] = domain.WorkspaceMember{
		WorkspaceID: "ws-1", UserID: "user-1", Role: domain.WorkspaceRoleMember, Status: domain.MemberStatusActive,
	}
	svc := service.NewMemberService(ms, &fakeChannelStore{channel: ch}, &fakeWorkspaceStore{workspace: ws})
	_, err := svc.SelfJoinChannel(context.Background(), "ws-1", "ch-priv", "user-1")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("private channel + disabled workspace must return ErrNotFound, got: %v", err)
	}
}

// TestMemberService_SelfJoinChannel_NotFound_ExactErrorShape verifies that every
// "not found" path (private channel, missing, archived, cross-workspace) returns
// the exact bare domain.ErrNotFound sentinel — not a wrapped variant.
// Using pointer equality (err != domain.ErrNotFound) ensures err.Error() is
// indistinguishable across all paths.
func TestMemberService_SelfJoinChannel_NotFound_ExactErrorShape(t *testing.T) {
	ws := domain.Workspace{ID: "ws-1", Status: domain.WorkspaceStatusActive}

	cases := []struct {
		name string
		cs   *fakeChannelStore
	}{
		{
			name: "private channel",
			cs: &fakeChannelStore{channel: domain.Channel{
				ID: "ch-1", WorkspaceID: "ws-1",
				Type: domain.ChannelTypePrivate, Status: domain.ChannelStatusActive,
			}},
		},
		{
			name: "missing channel",
			cs:   &fakeChannelStore{},
		},
		{
			name: "archived channel",
			cs: &fakeChannelStore{channel: domain.Channel{
				ID: "ch-1", WorkspaceID: "ws-1",
				Type: domain.ChannelTypePublic, Status: domain.ChannelStatusArchived,
			}},
		},
		{
			name: "cross-workspace channel",
			cs: &fakeChannelStore{channel: domain.Channel{
				ID: "ch-1", WorkspaceID: "ws-other",
				Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive,
			}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ms := newFakeMemberStore()
			ms.workspaceMembers[wmKey("ws-1", "user-1")] = domain.WorkspaceMember{
				WorkspaceID: "ws-1", UserID: "user-1",
				Role: domain.WorkspaceRoleMember, Status: domain.MemberStatusActive,
			}
			svc := service.NewMemberService(ms, tc.cs, &fakeWorkspaceStore{workspace: ws})
			_, err := svc.SelfJoinChannel(context.Background(), "ws-1", "ch-1", "user-1")
			if err != domain.ErrNotFound {
				t.Fatalf("%s: want exact domain.ErrNotFound, got %T(%v)", tc.name, err, err)
			}
		})
	}
}

// Regression for the test double itself: the fake member store used to join
// every active member to #geral, guests included, while PGXMemberStore stopped
// doing that for guests in RF-74. A fake that is more permissive than the store
// makes the service suite agree with a state production cannot produce, so a
// regression in the guest boundary would have gone unnoticed here.
//
// Both sides now gate on domain.CanReachPublicChannels.
func TestFakeMemberStore_GeneralMembershipMirrorsTheStoreForGuests(t *testing.T) {
	general := func(ms *fakeMemberStore) (string, bool) {
		_, ok := ms.channelMembers[cmKey("ch-geral", "u")]
		return "ch-geral", ok
	}

	t.Run("AddWorkspaceMember", func(t *testing.T) {
		for _, tt := range []struct {
			role domain.WorkspaceRole
			want bool
		}{
			{role: domain.WorkspaceRoleGuest, want: false},
			{role: domain.WorkspaceRoleMember, want: true},
			{role: domain.WorkspaceRoleModerator, want: true},
			{role: domain.WorkspaceRoleAdmin, want: true},
			{role: domain.WorkspaceRoleOwner, want: true},
		} {
			t.Run(string(tt.role), func(t *testing.T) {
				ms := newFakeMemberStore()
				ms.generalChannels["ws-1"] = "ch-geral"
				if _, err := ms.AddWorkspaceMember(context.Background(), "ws-1", "u", tt.role); err != nil {
					t.Fatalf("AddWorkspaceMember: %v", err)
				}
				if _, got := general(ms); got != tt.want {
					t.Fatalf("%s in #geral = %v, want %v", tt.role, got, tt.want)
				}
			})
		}
	})

	t.Run("ActivateWorkspaceMember", func(t *testing.T) {
		for _, tt := range []struct {
			role domain.WorkspaceRole
			want bool
		}{
			{role: domain.WorkspaceRoleGuest, want: false},
			{role: domain.WorkspaceRoleMember, want: true},
		} {
			t.Run(string(tt.role), func(t *testing.T) {
				ms := newFakeMemberStore()
				ms.generalChannels["ws-1"] = "ch-geral"
				ms.workspaceMembers[wmKey("ws-1", "u")] = domain.WorkspaceMember{
					WorkspaceID: "ws-1", UserID: "u", Role: tt.role, Status: domain.MemberStatusLeft,
				}
				if _, err := ms.ActivateWorkspaceMember(context.Background(), "ws-1", "u"); err != nil {
					t.Fatalf("ActivateWorkspaceMember: %v", err)
				}
				if _, got := general(ms); got != tt.want {
					t.Fatalf("reactivated %s in #geral = %v, want %v", tt.role, got, tt.want)
				}
			})
		}
	})

	t.Run("SyncGeneralMemberships skips guests", func(t *testing.T) {
		ms := newFakeMemberStore()
		ms.generalChannels["ws-1"] = "ch-geral"
		ms.workspaceMembers[wmKey("ws-1", "u")] = domain.WorkspaceMember{
			WorkspaceID: "ws-1", UserID: "u", Role: domain.WorkspaceRoleGuest, Status: domain.MemberStatusActive,
		}
		ms.workspaceMembers[wmKey("ws-1", "m")] = activeMembership("ws-1", "m")

		inserted, err := ms.SyncGeneralMemberships(context.Background(), "ws-1")
		if err != nil {
			t.Fatalf("SyncGeneralMemberships: %v", err)
		}
		if inserted != 1 {
			t.Fatalf("inserted = %d, want 1 (the member only)", inserted)
		}
		if _, ok := general(ms); ok {
			t.Fatal("sync joined a guest to #geral")
		}
	})

	// "A guest is not joined automatically" is not "a guest may never belong".
	// RF-74 says a guest reaches the channels it was explicitly added to, and
	// #geral is not special: an explicit membership must survive a sync, which
	// only ever inserts.
	t.Run("explicit guest membership survives sync", func(t *testing.T) {
		ms := newFakeMemberStore()
		ms.generalChannels["ws-1"] = "ch-geral"
		ms.workspaceMembers[wmKey("ws-1", "u")] = domain.WorkspaceMember{
			WorkspaceID: "ws-1", UserID: "u", Role: domain.WorkspaceRoleGuest, Status: domain.MemberStatusActive,
		}
		if _, err := ms.AddChannelMember(context.Background(), "ch-geral", "u", domain.ChannelRoleMember); err != nil {
			t.Fatalf("explicitly add guest to #geral: %v", err)
		}

		if _, err := ms.SyncGeneralMemberships(context.Background(), "ws-1"); err != nil {
			t.Fatalf("SyncGeneralMemberships: %v", err)
		}
		if _, ok := general(ms); !ok {
			t.Fatal("sync removed a guest's explicit #geral membership")
		}
	})
}
