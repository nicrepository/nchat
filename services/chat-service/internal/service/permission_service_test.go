package service_test

import (
	"context"
	"errors"
	"testing"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
)

func activeMembership(workspaceID, userID string) domain.WorkspaceMember {
	return domain.WorkspaceMember{WorkspaceID: workspaceID, UserID: userID, Role: domain.WorkspaceRoleMember, Status: domain.MemberStatusActive}
}

func TestPermissionService_ListVisibleChannels_NonMember_Empty(t *testing.T) {
	ms := newFakeMemberStore()
	channels := &fakeChannelStore{}
	svc := service.NewPermissionService(ms, channels)
	got, err := svc.ListVisibleChannels(context.Background(), "ws-1", "user-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("non-member should see no channels, got %d", len(got))
	}
	if channels.listVisibleCalls != 1 || channels.listCalls != 0 {
		t.Fatalf("expected SQL-visible list only, visible=%d all=%d", channels.listVisibleCalls, channels.listCalls)
	}
}

func TestPermissionService_ListVisibleChannels_ActiveMember_SeesPublicAndGeneral(t *testing.T) {
	ms := newFakeMemberStore()
	ms.workspaceMembers[wmKey("ws-1", "user-1")] = activeMembership("ws-1", "user-1")
	visible := []domain.Channel{
		{ID: "ch-pub", WorkspaceID: "ws-1", Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive},
		{ID: "ch-gen", WorkspaceID: "ws-1", Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive, IsGeneral: true},
	}
	svc := service.NewPermissionService(ms, &fakeChannelStore{channels: visible, visibleChannels: visible})
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
	svc := service.NewPermissionService(ms, &fakeChannelStore{
		channels: []domain.Channel{{ID: "ch-priv", WorkspaceID: "ws-1", Type: domain.ChannelTypePrivate, Status: domain.ChannelStatusActive}},
	})
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
	private := domain.Channel{ID: "ch-priv", WorkspaceID: "ws-1", Type: domain.ChannelTypePrivate, Status: domain.ChannelStatusActive}
	svc := service.NewPermissionService(ms, &fakeChannelStore{channels: []domain.Channel{private}, visibleChannels: []domain.Channel{private}})
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
	ch := domain.Channel{ID: "ch-pub", WorkspaceID: "ws-1", Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive}
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
	ch := domain.Channel{ID: "ch-priv", WorkspaceID: "ws-1", Type: domain.ChannelTypePrivate, Status: domain.ChannelStatusActive}
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
	ch := domain.Channel{ID: "ch-priv", WorkspaceID: "ws-1", Type: domain.ChannelTypePrivate, Status: domain.ChannelStatusActive}
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
	ch := domain.Channel{ID: "ch-geral", WorkspaceID: "ws-1", Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive, IsGeneral: true}
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
	ch := domain.Channel{ID: "ch-pub", WorkspaceID: "ws-1", Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive}
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

func TestPermissionService_CanManageWorkspace_OnlyActiveOwnerOrAdmin(t *testing.T) {
	for _, tt := range []struct {
		name   string
		role   domain.WorkspaceRole
		status domain.MemberStatus
		want   bool
	}{
		{name: "owner", role: domain.WorkspaceRoleOwner, status: domain.MemberStatusActive, want: true},
		{name: "admin", role: domain.WorkspaceRoleAdmin, status: domain.MemberStatusActive, want: true},
		{name: "member", role: domain.WorkspaceRoleMember, status: domain.MemberStatusActive},
		{name: "suspended admin", role: domain.WorkspaceRoleAdmin, status: domain.MemberStatusSuspended},
	} {
		t.Run(tt.name, func(t *testing.T) {
			members := newFakeMemberStore()
			members.workspaceMembers[wmKey("ws-1", "user-1")] = domain.WorkspaceMember{
				WorkspaceID: "ws-1", UserID: "user-1", Role: tt.role, Status: tt.status,
			}
			got, err := service.NewPermissionService(members, &fakeChannelStore{}).
				CanManageWorkspace(context.Background(), "ws-1", "user-1")
			if err != nil || got != tt.want {
				t.Fatalf("CanManageWorkspace() = %v, %v; want %v", got, err, tt.want)
			}
		})
	}
}

// The role matrix above only reaches CanManageWorkspace when a membership row
// exists. The two remaining answers are what an administrative endpoint is
// actually guarded by, and they must not be the same answer: somebody with no
// membership at all is simply refused, while a store that could not answer is
// an error — never a quiet "false" that a caller could mistake for a decision,
// and never a "true".
func TestPermissionService_CanManageWorkspace_NonMemberIsRefusedWithoutAnError(t *testing.T) {
	members := newFakeMemberStore() // no membership row for user-1

	got, err := service.NewPermissionService(members, &fakeChannelStore{}).
		CanManageWorkspace(context.Background(), "ws-1", "user-1")

	if err != nil {
		t.Fatalf("a non-member must be a decision, not an error: %v", err)
	}
	if got {
		t.Fatal("CanManageWorkspace authorized a user with no membership")
	}
}

func TestPermissionService_CanManageWorkspace_StoreFailureIsReportedNotDecided(t *testing.T) {
	storeErr := errors.New("connection reset")
	members := newFakeMemberStore()
	members.getWMErr = storeErr
	// A row that would authorize, to prove the failure is what is answered and
	// not the row behind it.
	members.workspaceMembers[wmKey("ws-1", "user-1")] = domain.WorkspaceMember{
		WorkspaceID: "ws-1", UserID: "user-1",
		Role: domain.WorkspaceRoleOwner, Status: domain.MemberStatusActive,
	}

	got, err := service.NewPermissionService(members, &fakeChannelStore{}).
		CanManageWorkspace(context.Background(), "ws-1", "user-1")

	if !errors.Is(err, storeErr) {
		t.Fatalf("error = %v, want the store's error wrapped", err)
	}
	if got {
		t.Fatal("a failed membership lookup authorized the caller")
	}
}

func TestPermissionService_ListVisibleChannels_UsesSQLVisibilityFiltering(t *testing.T) {
	ms := newFakeMemberStore()
	ms.workspaceMembers[wmKey("ws-1", "user-1")] = activeMembership("ws-1", "user-1")
	ms.channelMembers[cmKey("ch-private", "user-1")] = domain.ChannelMember{ChannelID: "ch-private", UserID: "user-1"}
	channels := &fakeChannelStore{
		channels: []domain.Channel{
			{ID: "ch-public", WorkspaceID: "ws-1", Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive},
			{ID: "ch-private", WorkspaceID: "ws-2", Type: domain.ChannelTypePrivate, Status: domain.ChannelStatusActive},
		},
		visibleChannels: []domain.Channel{
			{ID: "ch-public", WorkspaceID: "ws-1", Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive},
		},
	}

	got, err := service.NewPermissionService(ms, channels).ListVisibleChannels(context.Background(), "ws-1", "user-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].ID != "ch-public" {
		t.Fatalf("expected only SQL-visible public channel, got %+v", got)
	}
	if channels.listVisibleCalls != 1 || channels.listCalls != 0 {
		t.Fatalf("expected SQL-visible list only, visible=%d all=%d", channels.listVisibleCalls, channels.listCalls)
	}
}

func TestPermissionService_CanRead_CrossWorkspacePublicChannel_Denied(t *testing.T) {
	ms := newFakeMemberStore()
	ms.workspaceMembers[wmKey("ws-a", "user-1")] = activeMembership("ws-a", "user-1")
	channel := domain.Channel{ID: "ch-b", WorkspaceID: "ws-b", Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive}

	ok, err := service.NewPermissionService(ms, &fakeChannelStore{channel: channel}).CanRead(context.Background(), "ws-a", "ch-b", "user-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("workspace A member must not read a public channel from workspace B")
	}
}

func TestPermissionService_CanRead_CrossWorkspaceGeneralChannel_Denied(t *testing.T) {
	ms := newFakeMemberStore()
	ms.workspaceMembers[wmKey("ws-a", "user-1")] = activeMembership("ws-a", "user-1")
	channel := domain.Channel{ID: "geral-b", WorkspaceID: "ws-b", Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive, IsGeneral: true}

	ok, err := service.NewPermissionService(ms, &fakeChannelStore{channel: channel}).CanRead(context.Background(), "ws-a", "geral-b", "user-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("workspace A member must not read #geral from workspace B")
	}
}

func TestPermissionService_CanRead_StalePrivateMembershipCannotCrossWorkspace(t *testing.T) {
	for _, status := range []domain.MemberStatus{domain.MemberStatusSuspended, domain.MemberStatusLeft} {
		t.Run(string(status), func(t *testing.T) {
			ms := newFakeMemberStore()
			ms.workspaceMembers[wmKey("ws-a", "user-1")] = activeMembership("ws-a", "user-1")
			ms.workspaceMembers[wmKey("ws-b", "user-1")] = domain.WorkspaceMember{WorkspaceID: "ws-b", UserID: "user-1", Role: domain.WorkspaceRoleMember, Status: status}
			ms.channelMembers[cmKey("private-b", "user-1")] = domain.ChannelMember{ChannelID: "private-b", UserID: "user-1"}
			channel := domain.Channel{ID: "private-b", WorkspaceID: "ws-b", Type: domain.ChannelTypePrivate, Status: domain.ChannelStatusActive}

			ok, err := service.NewPermissionService(ms, &fakeChannelStore{channel: channel}).CanRead(context.Background(), "ws-a", "private-b", "user-1")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if ok {
				t.Fatalf("%s workspace-B membership with stale channel membership must be denied", status)
			}
		})
	}
}

func TestPermissionService_CanRead_ChannelMembershipDBError_Propagates(t *testing.T) {
	ms := newFakeMemberStore()
	ms.workspaceMembers[wmKey("ws-1", "user-1")] = activeMembership("ws-1", "user-1")
	ms.getCMErr = errors.New("database unavailable")
	channel := domain.Channel{ID: "private", WorkspaceID: "ws-1", Type: domain.ChannelTypePrivate, Status: domain.ChannelStatusActive}

	_, err := service.NewPermissionService(ms, &fakeChannelStore{channel: channel}).CanRead(context.Background(), "ws-1", "private", "user-1")
	if err == nil {
		t.Fatal("expected channel membership database error")
	}
}

// Regression: PermissionService used to load the channel membership only for
// private channels, so a guest explicitly added to a *public* channel was judged
// as if it had no membership at all and was refused — while the SQL policy that
// backs the listing admitted it. The service and chat.channel_visible_to_user
// disagreed about the same user and the same channel.
//
// The matrix is unchanged by this fix; only the input handed to the domain is.
// Every non-guest row below states the behaviour that already existed.
func TestPermissionService_CanRead_GuestReachesOnlyChannelsItBelongsTo(t *testing.T) {
	const workspace, user = "ws-1", "user-1"
	public := domain.Channel{ID: "ch-pub", WorkspaceID: workspace, Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive}
	general := domain.Channel{ID: "ch-geral", WorkspaceID: workspace, Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive, IsGeneral: true}
	private := domain.Channel{ID: "ch-priv", WorkspaceID: workspace, Type: domain.ChannelTypePrivate, Status: domain.ChannelStatusActive}

	for _, tt := range []struct {
		name       string
		role       domain.WorkspaceRole
		channel    domain.Channel
		channelMem bool
		want       bool
	}{
		// The finding: an included guest must read the public channel it is in.
		{name: "guest in public channel reads it", role: domain.WorkspaceRoleGuest, channel: public, channelMem: true, want: true},
		{name: "guest outside public channel is denied", role: domain.WorkspaceRoleGuest, channel: public, want: false},
		{name: "guest in general reads it", role: domain.WorkspaceRoleGuest, channel: general, channelMem: true, want: true},
		{name: "guest outside general is denied", role: domain.WorkspaceRoleGuest, channel: general, want: false},
		{name: "guest in private channel reads it", role: domain.WorkspaceRoleGuest, channel: private, channelMem: true, want: true},
		{name: "guest outside private channel is denied", role: domain.WorkspaceRoleGuest, channel: private, want: false},

		// Unchanged for every role that reaches public channels on its own.
		{name: "member reads public without channel membership", role: domain.WorkspaceRoleMember, channel: public, want: true},
		{name: "moderator reads public without channel membership", role: domain.WorkspaceRoleModerator, channel: public, want: true},
		{name: "admin reads public without channel membership", role: domain.WorkspaceRoleAdmin, channel: public, want: true},
		{name: "owner reads general without channel membership", role: domain.WorkspaceRoleOwner, channel: general, want: true},
		{name: "member in public channel still reads it", role: domain.WorkspaceRoleMember, channel: public, channelMem: true, want: true},

		// Private still takes channel membership from everybody.
		{name: "owner outside private channel is denied", role: domain.WorkspaceRoleOwner, channel: private, want: false},
		{name: "admin outside private channel is denied", role: domain.WorkspaceRoleAdmin, channel: private, want: false},
		{name: "member in private channel reads it", role: domain.WorkspaceRoleMember, channel: private, channelMem: true, want: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ms := newFakeMemberStore()
			ms.workspaceMembers[wmKey(workspace, user)] = domain.WorkspaceMember{
				WorkspaceID: workspace, UserID: user, Role: tt.role, Status: domain.MemberStatusActive,
			}
			if tt.channelMem {
				ms.channelMembers[cmKey(tt.channel.ID, user)] = domain.ChannelMember{ChannelID: tt.channel.ID, UserID: user}
			}
			svc := service.NewPermissionService(ms, &fakeChannelStore{channel: tt.channel})

			got, err := svc.CanRead(context.Background(), workspace, tt.channel.ID, user)
			if err != nil {
				t.Fatalf("CanRead: %v", err)
			}
			if got != tt.want {
				t.Fatalf("CanRead = %v, want %v", got, tt.want)
			}
			// Write follows read (CanWriteChannel == CanReadChannel), so the fix
			// must reach it too — this is the half MentionService and the message
			// routes consume.
			gotWrite, err := svc.CanWrite(context.Background(), workspace, tt.channel.ID, user)
			if err != nil {
				t.Fatalf("CanWrite: %v", err)
			}
			if gotWrite != tt.want {
				t.Fatalf("CanWrite = %v, want %v", gotWrite, tt.want)
			}
		})
	}
}

// The membership is loaded only when it could still change the answer, so the
// hot path — a full member reading a public channel — keeps its single query.
// Fetching unconditionally would have fixed the bug too, at the cost of a second
// round trip on the most frequent authorization check in the service.
func TestPermissionService_CanRead_LoadsChannelMembershipOnlyWhenItCanMatter(t *testing.T) {
	const workspace, user = "ws-1", "user-1"
	public := domain.Channel{ID: "ch-pub", WorkspaceID: workspace, Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive}

	for _, tt := range []struct {
		name     string
		role     domain.WorkspaceRole
		wantCall int
	}{
		{name: "member decided by role alone", role: domain.WorkspaceRoleMember, wantCall: 0},
		{name: "guest needs the membership", role: domain.WorkspaceRoleGuest, wantCall: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ms := newFakeMemberStore()
			ms.workspaceMembers[wmKey(workspace, user)] = domain.WorkspaceMember{
				WorkspaceID: workspace, UserID: user, Role: tt.role, Status: domain.MemberStatusActive,
			}
			if _, err := service.NewPermissionService(ms, &fakeChannelStore{channel: public}).
				CanRead(context.Background(), workspace, public.ID, user); err != nil {
				t.Fatalf("CanRead: %v", err)
			}
			if ms.getCMCalls != tt.wantCall {
				t.Fatalf("GetChannelMember calls = %d, want %d", ms.getCMCalls, tt.wantCall)
			}
		})
	}
}

func TestPermissionService_ListVisibleChannels_StorageError_Propagates(t *testing.T) {
	want := errors.New("database unavailable")
	_, err := service.NewPermissionService(newFakeMemberStore(), &fakeChannelStore{listVisibleErr: want}).ListVisibleChannels(context.Background(), "ws-1", "user-1")
	if !errors.Is(err, want) {
		t.Fatalf("expected storage error, got %v", err)
	}
}
