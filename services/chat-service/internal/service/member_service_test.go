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
	svc := service.NewMemberService(ms, &fakeChannelStore{}, &fakeWorkspaceStore{})
	m, err := svc.JoinWorkspace(context.Background(), "ws-1", "user-1", domain.WorkspaceRoleMember)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.UserID != "user-1" || m.Status != domain.MemberStatusActive {
		t.Fatalf("unexpected member: %+v", m)
	}
}

func TestMemberService_JoinWorkspace_AlreadyMember_ReturnsExisting(t *testing.T) {
	ms := newFakeMemberStore()
	ms.workspaceMembers[wmKey("ws-1", "user-1")] = domain.WorkspaceMember{
		WorkspaceID: "ws-1", UserID: "user-1",
		Role: domain.WorkspaceRoleAdmin, Status: domain.MemberStatusActive,
	}
	svc := service.NewMemberService(ms, &fakeChannelStore{}, &fakeWorkspaceStore{})
	m, err := svc.JoinWorkspace(context.Background(), "ws-1", "user-1", domain.WorkspaceRoleMember)
	if err != nil {
		t.Fatalf("unexpected error on already-member: %v", err)
	}
	if m.Role != domain.WorkspaceRoleAdmin {
		t.Fatalf("expected existing role admin, got %q", m.Role)
	}
}

func TestMemberService_JoinChannel_Success(t *testing.T) {
	ms := newFakeMemberStore()
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

func TestMemberService_EnsureGeneralMembership_NoGeneralChannel_NoError(t *testing.T) {
	ms := newFakeMemberStore()
	pub := domain.Channel{ID: "ch-pub", WorkspaceID: "ws-1", IsGeneral: false, Type: domain.ChannelTypePublic}
	svc := service.NewMemberService(ms, &fakeChannelStore{channels: []domain.Channel{pub}}, &fakeWorkspaceStore{})
	if err := svc.EnsureGeneralMembership(context.Background(), "ws-1", "user-1"); err != nil {
		t.Fatalf("no #geral channel should not error, got %v", err)
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
