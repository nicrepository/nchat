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

func TestMemberService_JoinChannel_Success(t *testing.T) {
	ms := newFakeMemberStore()
	ms.generalChannels["ws-1"] = "ch-geral"
	ms.workspaceMembers[wmKey("ws-1", "user-1")] = activeMembership("ws-1", "user-1")
	channel := domain.Channel{ID: "ch-1", WorkspaceID: "ws-1", Type: domain.ChannelTypePrivate, Status: domain.ChannelStatusActive}
	workspace := domain.Workspace{ID: "ws-1", Status: domain.WorkspaceStatusActive}
	svc := service.NewMemberService(ms, &fakeChannelStore{channel: channel}, &fakeWorkspaceStore{workspace: workspace})
	m, err := svc.JoinChannel(context.Background(), "ch-1", "user-1", domain.ChannelRoleMember)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.ChannelID != "ch-1" {
		t.Fatalf("expected ch-1, got %q", m.ChannelID)
	}
}

func TestMemberService_JoinChannel_AlreadyMember_ReturnsExisting(t *testing.T) {
	ms := newFakeMemberStore()
	ms.channelMembers[cmKey("ch-1", "user-1")] = domain.ChannelMember{
		ChannelID: "ch-1", UserID: "user-1", Role: domain.ChannelRoleModerator,
	}
	ms.workspaceMembers[wmKey("ws-1", "user-1")] = activeMembership("ws-1", "user-1")
	channel := domain.Channel{ID: "ch-1", WorkspaceID: "ws-1", Type: domain.ChannelTypePrivate, Status: domain.ChannelStatusActive}
	workspace := domain.Workspace{ID: "ws-1", Status: domain.WorkspaceStatusActive}
	svc := service.NewMemberService(ms, &fakeChannelStore{channel: channel}, &fakeWorkspaceStore{workspace: workspace})
	m, err := svc.JoinChannel(context.Background(), "ch-1", "user-1", domain.ChannelRoleMember)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.Role != domain.ChannelRoleModerator {
		t.Fatalf("expected moderator, got %q", m.Role)
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
			if err != nil {
				t.Fatalf("inactive workspace member should be skipped without error, got %v", err)
			}
			if _, ok := ms.channelMembers[cmKey("ch-geral", "user-1")]; ok {
				t.Fatal("inactive workspace member must not be added to #geral")
			}
		})
	}
}

func TestMemberService_JoinChannel_RequiresActiveMembershipInChannelWorkspace(t *testing.T) {
	for _, status := range []domain.MemberStatus{"", domain.MemberStatusSuspended, domain.MemberStatusLeft} {
		name := string(status)
		if name == "" {
			name = "missing"
		}
		t.Run(name, func(t *testing.T) {
			ms := newFakeMemberStore()
			if status != "" {
				ms.workspaceMembers[wmKey("ws-2", "user-1")] = domain.WorkspaceMember{WorkspaceID: "ws-2", UserID: "user-1", Status: status}
			}
			channel := domain.Channel{ID: "ch-2", WorkspaceID: "ws-2", Type: domain.ChannelTypePrivate, Status: domain.ChannelStatusActive}
			workspace := domain.Workspace{ID: "ws-2", Status: domain.WorkspaceStatusActive}
			svc := service.NewMemberService(ms, &fakeChannelStore{channel: channel}, &fakeWorkspaceStore{workspace: workspace})

			_, err := svc.JoinChannel(context.Background(), "ch-2", "user-1", domain.ChannelRoleMember)
			if !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("expected ErrForbidden, got %v", err)
			}
			if _, added := ms.channelMembers[cmKey("ch-2", "user-1")]; added {
				t.Fatal("user without active workspace membership must not be added to channel")
			}
		})
	}
}

func TestMemberService_JoinChannel_DisabledWorkspace_Denied(t *testing.T) {
	ms := newFakeMemberStore()
	ms.workspaceMembers[wmKey("ws-1", "user-1")] = activeMembership("ws-1", "user-1")
	channel := domain.Channel{ID: "ch-1", WorkspaceID: "ws-1", Type: domain.ChannelTypePrivate, Status: domain.ChannelStatusActive}
	workspace := domain.Workspace{ID: "ws-1", Status: domain.WorkspaceStatusDisabled}

	_, err := service.NewMemberService(ms, &fakeChannelStore{channel: channel}, &fakeWorkspaceStore{workspace: workspace}).JoinChannel(
		context.Background(), "ch-1", "user-1", domain.ChannelRoleMember,
	)
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("expected ErrForbidden, got %v", err)
	}
}

func TestMemberService_JoinChannel_RejectsElevatedCallerRole(t *testing.T) {
	ms := newFakeMemberStore()
	channel := domain.Channel{ID: "ch-1", WorkspaceID: "ws-1", Type: domain.ChannelTypePrivate, Status: domain.ChannelStatusActive}
	workspace := domain.Workspace{ID: "ws-1", Status: domain.WorkspaceStatusActive}

	_, err := service.NewMemberService(ms, &fakeChannelStore{channel: channel}, &fakeWorkspaceStore{workspace: workspace}).JoinChannel(
		context.Background(), "ch-1", "user-1", domain.ChannelRoleModerator,
	)
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
}

func TestMemberService_JoinChannel_WorkspaceMembershipDBError_Propagates(t *testing.T) {
	want := errors.New("database unavailable")
	ms := newFakeMemberStore()
	ms.getWMErr = want
	channel := domain.Channel{ID: "ch-1", WorkspaceID: "ws-1", Type: domain.ChannelTypePrivate, Status: domain.ChannelStatusActive}
	workspace := domain.Workspace{ID: "ws-1", Status: domain.WorkspaceStatusActive}

	_, err := service.NewMemberService(ms, &fakeChannelStore{channel: channel}, &fakeWorkspaceStore{workspace: workspace}).JoinChannel(
		context.Background(), "ch-1", "user-1", domain.ChannelRoleMember,
	)
	if !errors.Is(err, want) {
		t.Fatalf("expected membership DB error, got %v", err)
	}
}

func TestMemberService_JoinChannel_ChannelLookupError_Propagates(t *testing.T) {
	want := errors.New("database unavailable")
	_, err := service.NewMemberService(newFakeMemberStore(), &fakeChannelStore{getByIDErr: want}, &fakeWorkspaceStore{}).JoinChannel(
		context.Background(), "ch-1", "user-1", domain.ChannelRoleMember,
	)
	if !errors.Is(err, want) {
		t.Fatalf("expected channel lookup error, got %v", err)
	}
}
