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
	ms.workspaceMembers[wmKey("ws-1", "suspended")] = domain.WorkspaceMember{WorkspaceID: "ws-1", UserID: "suspended", Status: domain.MemberStatusSuspended}
	ms.workspaceMembers[wmKey("ws-1", "left")] = domain.WorkspaceMember{WorkspaceID: "ws-1", UserID: "left", Status: domain.MemberStatusLeft}
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
			ms.workspaceMembers[wmKey("ws-1", "user-1")] = domain.WorkspaceMember{WorkspaceID: "ws-1", UserID: "user-1", Status: status}
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

func TestMemberService_SelfJoinChannel_PrivateChannel_Denied(t *testing.T) {
	ms := newFakeMemberStore()
	ch := domain.Channel{ID: "ch-priv", WorkspaceID: "ws-1", Type: domain.ChannelTypePrivate, Status: domain.ChannelStatusActive}
	ws := domain.Workspace{ID: "ws-1", Status: domain.WorkspaceStatusActive}
	ms.workspaceMembers[wmKey("ws-1", "user-1")] = domain.WorkspaceMember{
		WorkspaceID: "ws-1", UserID: "user-1", Role: domain.WorkspaceRoleMember, Status: domain.MemberStatusActive,
	}
	svc := service.NewMemberService(ms, &fakeChannelStore{channel: ch}, &fakeWorkspaceStore{workspace: ws})
	_, err := svc.SelfJoinChannel(context.Background(), "ws-1", "ch-priv", "user-1")
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("private channel self-join should return ErrForbidden, got: %v", err)
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
	svc := service.NewMemberService(ms, &fakeChannelStore{channel: ch}, &fakeWorkspaceStore{workspace: ws})
	_, err := svc.SelfJoinChannel(context.Background(), "ws-1", "ch-1", "user-1")
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("suspended member should get ErrForbidden, got: %v", err)
	}
}

func TestMemberService_SelfJoinChannel_LeftMember_Denied(t *testing.T) {
	ms := newFakeMemberStore()
	ch := domain.Channel{ID: "ch-1", WorkspaceID: "ws-1", Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive}
	ws := domain.Workspace{ID: "ws-1", Status: domain.WorkspaceStatusActive}
	ms.workspaceMembers[wmKey("ws-1", "user-1")] = domain.WorkspaceMember{
		WorkspaceID: "ws-1", UserID: "user-1", Role: domain.WorkspaceRoleMember, Status: domain.MemberStatusLeft,
	}
	svc := service.NewMemberService(ms, &fakeChannelStore{channel: ch}, &fakeWorkspaceStore{workspace: ws})
	_, err := svc.SelfJoinChannel(context.Background(), "ws-1", "ch-1", "user-1")
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("left member should get ErrForbidden, got: %v", err)
	}
}

func TestMemberService_SelfJoinChannel_DisabledWorkspace_Denied(t *testing.T) {
	ms := newFakeMemberStore()
	ch := domain.Channel{ID: "ch-1", WorkspaceID: "ws-1", Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive}
	ws := domain.Workspace{ID: "ws-1", Status: domain.WorkspaceStatusDisabled}
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
