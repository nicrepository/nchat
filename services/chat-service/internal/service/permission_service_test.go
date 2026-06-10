package service_test

import (
	"context"
	"testing"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
)

func activeMembership(workspaceID, userID string) domain.WorkspaceMember {
	return domain.WorkspaceMember{WorkspaceID: workspaceID, UserID: userID, Status: domain.MemberStatusActive}
}

func TestPermissionService_ListVisibleChannels_NonMember_Empty(t *testing.T) {
	ms := newFakeMemberStore()
	svc := service.NewPermissionService(ms, &fakeChannelStore{channels: []domain.Channel{
		{ID: "ch-pub", Type: domain.ChannelTypePublic},
	}})
	got, err := svc.ListVisibleChannels(context.Background(), "ws-1", "user-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("non-member should see no channels, got %d", len(got))
	}
}

func TestPermissionService_ListVisibleChannels_ActiveMember_SeesPublicAndGeneral(t *testing.T) {
	ms := newFakeMemberStore()
	ms.workspaceMembers[wmKey("ws-1", "user-1")] = activeMembership("ws-1", "user-1")
	svc := service.NewPermissionService(ms, &fakeChannelStore{channels: []domain.Channel{
		{ID: "ch-pub", WorkspaceID: "ws-1", Type: domain.ChannelTypePublic},
		{ID: "ch-gen", WorkspaceID: "ws-1", Type: domain.ChannelTypePublic, IsGeneral: true},
	}})
	got, err := svc.ListVisibleChannels(context.Background(), "ws-1", "user-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 channels, got %d", len(got))
	}
}

func TestPermissionService_ListVisibleChannels_ActiveMember_DoesNotSeePrivate(t *testing.T) {
	ms := newFakeMemberStore()
	ms.workspaceMembers[wmKey("ws-1", "user-1")] = activeMembership("ws-1", "user-1")
	svc := service.NewPermissionService(ms, &fakeChannelStore{channels: []domain.Channel{
		{ID: "ch-priv", WorkspaceID: "ws-1", Type: domain.ChannelTypePrivate},
	}})
	got, err := svc.ListVisibleChannels(context.Background(), "ws-1", "user-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("private channel without membership should not be visible, got %d", len(got))
	}
}

func TestPermissionService_ListVisibleChannels_SeesPrivateIfChannelMember(t *testing.T) {
	ms := newFakeMemberStore()
	ms.workspaceMembers[wmKey("ws-1", "user-1")] = activeMembership("ws-1", "user-1")
	ms.channelMembers[cmKey("ch-priv", "user-1")] = domain.ChannelMember{ChannelID: "ch-priv", UserID: "user-1"}
	svc := service.NewPermissionService(ms, &fakeChannelStore{channels: []domain.Channel{
		{ID: "ch-priv", WorkspaceID: "ws-1", Type: domain.ChannelTypePrivate},
	}})
	got, err := svc.ListVisibleChannels(context.Background(), "ws-1", "user-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("channel member should see private channel, got %d", len(got))
	}
}

func TestPermissionService_CanRead_PublicChannel_WorkspaceMember_True(t *testing.T) {
	ms := newFakeMemberStore()
	ms.workspaceMembers[wmKey("ws-1", "user-1")] = activeMembership("ws-1", "user-1")
	ch := domain.Channel{ID: "ch-pub", WorkspaceID: "ws-1", Type: domain.ChannelTypePublic}
	svc := service.NewPermissionService(ms, &fakeChannelStore{channel: ch})
	ok, err := svc.CanRead(context.Background(), "ws-1", "ch-pub", "user-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("active workspace member should read public channel")
	}
}

func TestPermissionService_CanRead_NonMember_False(t *testing.T) {
	ms := newFakeMemberStore()
	svc := service.NewPermissionService(ms, &fakeChannelStore{})
	ok, err := svc.CanRead(context.Background(), "ws-1", "ch-pub", "user-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("non-member should not read channel")
	}
}

func TestPermissionService_CanRead_PrivateChannel_NoChannelMembership_False(t *testing.T) {
	ms := newFakeMemberStore()
	ms.workspaceMembers[wmKey("ws-1", "user-1")] = activeMembership("ws-1", "user-1")
	ch := domain.Channel{ID: "ch-priv", WorkspaceID: "ws-1", Type: domain.ChannelTypePrivate}
	svc := service.NewPermissionService(ms, &fakeChannelStore{channel: ch})
	ok, err := svc.CanRead(context.Background(), "ws-1", "ch-priv", "user-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("workspace member without channel membership should not read private channel")
	}
}

func TestPermissionService_CanRead_PrivateChannel_WithChannelMembership_True(t *testing.T) {
	ms := newFakeMemberStore()
	ms.workspaceMembers[wmKey("ws-1", "user-1")] = activeMembership("ws-1", "user-1")
	ms.channelMembers[cmKey("ch-priv", "user-1")] = domain.ChannelMember{ChannelID: "ch-priv", UserID: "user-1"}
	ch := domain.Channel{ID: "ch-priv", WorkspaceID: "ws-1", Type: domain.ChannelTypePrivate}
	svc := service.NewPermissionService(ms, &fakeChannelStore{channel: ch})
	ok, err := svc.CanRead(context.Background(), "ws-1", "ch-priv", "user-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("channel member should read private channel")
	}
}

func TestPermissionService_CanRead_GeneralChannel_WorkspaceMember_True(t *testing.T) {
	ms := newFakeMemberStore()
	ms.workspaceMembers[wmKey("ws-1", "user-1")] = activeMembership("ws-1", "user-1")
	ch := domain.Channel{ID: "ch-geral", WorkspaceID: "ws-1", Type: domain.ChannelTypePublic, IsGeneral: true}
	svc := service.NewPermissionService(ms, &fakeChannelStore{channel: ch})
	ok, err := svc.CanRead(context.Background(), "ws-1", "ch-geral", "user-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("workspace member should read #geral channel")
	}
}

func TestPermissionService_CanWrite_MatchesCanRead(t *testing.T) {
	ms := newFakeMemberStore()
	ms.workspaceMembers[wmKey("ws-1", "user-1")] = activeMembership("ws-1", "user-1")
	ch := domain.Channel{ID: "ch-pub", WorkspaceID: "ws-1", Type: domain.ChannelTypePublic}
	svc := service.NewPermissionService(ms, &fakeChannelStore{channel: ch})
	canRead, _ := svc.CanRead(context.Background(), "ws-1", "ch-pub", "user-1")
	canWrite, err := svc.CanWrite(context.Background(), "ws-1", "ch-pub", "user-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if canWrite != canRead {
		t.Fatalf("CanWrite must match CanRead: read=%v write=%v", canRead, canWrite)
	}
}
