package service_test

import (
	"context"
	"testing"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
)

func TestMemberService_JoinWorkspace_Success(t *testing.T) {
	ms := newFakeMemberStore()
	svc := service.NewMemberService(ms, &fakeChannelStore{})
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
	svc := service.NewMemberService(ms, &fakeChannelStore{})
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
	svc := service.NewMemberService(ms, &fakeChannelStore{})
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
	svc := service.NewMemberService(ms, &fakeChannelStore{})
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
	general := domain.Channel{ID: "ch-geral", WorkspaceID: "ws-1", IsGeneral: true, Type: domain.ChannelTypePublic}
	svc := service.NewMemberService(ms, &fakeChannelStore{channels: []domain.Channel{general}})
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
	general := domain.Channel{ID: "ch-geral", WorkspaceID: "ws-1", IsGeneral: true}
	svc := service.NewMemberService(ms, &fakeChannelStore{channels: []domain.Channel{general}})
	if err := svc.EnsureGeneralMembership(context.Background(), "ws-1", "user-1"); err != nil {
		t.Fatalf("idempotent call should not error, got %v", err)
	}
}

func TestMemberService_EnsureGeneralMembership_NoGeneralChannel_NoError(t *testing.T) {
	ms := newFakeMemberStore()
	pub := domain.Channel{ID: "ch-pub", WorkspaceID: "ws-1", IsGeneral: false, Type: domain.ChannelTypePublic}
	svc := service.NewMemberService(ms, &fakeChannelStore{channels: []domain.Channel{pub}})
	if err := svc.EnsureGeneralMembership(context.Background(), "ws-1", "user-1"); err != nil {
		t.Fatalf("no #geral channel should not error, got %v", err)
	}
}
