package service_test

import (
	"context"
	"errors"
	"testing"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
)

func TestChannelService_CreatePublicChannel_ManagerSucceeds(t *testing.T) {
	ms := newFakeMemberStore()
	ms.workspaceMembers[wmKey("ws-1", "owner-1")] = domain.WorkspaceMember{
		WorkspaceID: "ws-1", UserID: "owner-1", Role: domain.WorkspaceRoleOwner, Status: domain.MemberStatusActive,
	}
	channels := &fakeChannelStore{createdChannel: domain.Channel{
		ID: "ch-public", WorkspaceID: "ws-1", Slug: "team", DisplayName: "Team", Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive, CreatedBy: "owner-1",
	}}
	channels.category = domain.ChannelCategory{ID: "cat-1", WorkspaceID: "ws-1"}

	got, err := service.NewChannelService(activeWorkspaceStore("ws-1"), channels, ms).CreateChannel(context.Background(), service.CreateChannelInput{
		WorkspaceID: "ws-1",
		CallerID:    "owner-1",
		CategoryID:  " cat-1 ",
		Slug:        "team",
		DisplayName: "Team",
		Type:        domain.ChannelTypePublic,
		Position:    10,
	})
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	if got.ID != "ch-public" || got.CreatedBy != "owner-1" {
		t.Fatalf("unexpected channel: %+v", got)
	}
	if channels.createWithMemberCalls != 0 {
		t.Fatal("public channel creation must not fan out channel_members")
	}
	if channels.lastCreateInput.CreatedBy != "owner-1" || channels.lastCreateInput.IsGeneral {
		t.Fatalf("service must own created_by/is_general, input=%+v", channels.lastCreateInput)
	}
	if channels.lastCreateInput.CategoryID != "cat-1" {
		t.Fatalf("category_id should be trimmed before storage, input=%+v", channels.lastCreateInput)
	}
}

func TestChannelService_CreatePrivateChannel_ManagerAddsCreatorMembership(t *testing.T) {
	ms := newFakeMemberStore()
	ms.workspaceMembers[wmKey("ws-1", "admin-1")] = domain.WorkspaceMember{
		WorkspaceID: "ws-1", UserID: "admin-1", Role: domain.WorkspaceRoleAdmin, Status: domain.MemberStatusActive,
	}
	channels := &fakeChannelStore{createdChannel: domain.Channel{
		ID: "ch-private", WorkspaceID: "ws-1", Slug: "leadership", DisplayName: "Leadership", Type: domain.ChannelTypePrivate, Status: domain.ChannelStatusActive,
	}}

	got, err := service.NewChannelService(activeWorkspaceStore("ws-1"), channels, ms).CreateChannel(context.Background(), service.CreateChannelInput{
		WorkspaceID: "ws-1",
		CallerID:    "admin-1",
		Slug:        "leadership",
		DisplayName: "Leadership",
		Type:        domain.ChannelTypePrivate,
	})
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	if got.ID != "ch-private" {
		t.Fatalf("unexpected channel: %+v", got)
	}
	if channels.createWithMemberCalls != 1 || channels.lastCreateMemberUserID != "admin-1" {
		t.Fatalf("private channel must add creator atomically, calls=%d user=%q", channels.createWithMemberCalls, channels.lastCreateMemberUserID)
	}
}

func TestChannelService_CreateChannel_MemberWithoutManageRoleDenied(t *testing.T) {
	ms := newFakeMemberStore()
	ms.workspaceMembers[wmKey("ws-1", "member-1")] = domain.WorkspaceMember{
		WorkspaceID: "ws-1", UserID: "member-1", Role: domain.WorkspaceRoleMember, Status: domain.MemberStatusActive,
	}

	_, err := service.NewChannelService(activeWorkspaceStore("ws-1"), &fakeChannelStore{}, ms).CreateChannel(context.Background(), service.CreateChannelInput{
		WorkspaceID: "ws-1",
		CallerID:    "member-1",
		Slug:        "team",
		DisplayName: "Team",
		Type:        domain.ChannelTypePublic,
	})
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("expected ErrForbidden, got %v", err)
	}
}

func TestChannelService_CreateChannel_NonWorkspaceMemberDenied(t *testing.T) {
	_, err := service.NewChannelService(activeWorkspaceStore("ws-1"), &fakeChannelStore{}, newFakeMemberStore()).CreateChannel(context.Background(), service.CreateChannelInput{
		WorkspaceID: "ws-1",
		CallerID:    "outsider",
		Slug:        "team",
		DisplayName: "Team",
		Type:        domain.ChannelTypePublic,
	})
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("expected ErrForbidden, got %v", err)
	}
}

func TestChannelService_CreateChannel_DisabledWorkspaceDenied(t *testing.T) {
	ms := newFakeMemberStore()
	ms.workspaceMembers[wmKey("ws-disabled", "owner-1")] = domain.WorkspaceMember{
		WorkspaceID: "ws-disabled", UserID: "owner-1", Role: domain.WorkspaceRoleOwner, Status: domain.MemberStatusActive,
	}
	workspace := &fakeWorkspaceStore{workspace: domain.Workspace{ID: "ws-disabled", Status: domain.WorkspaceStatusDisabled}}

	_, err := service.NewChannelService(workspace, &fakeChannelStore{}, ms).CreateChannel(context.Background(), service.CreateChannelInput{
		WorkspaceID: "ws-disabled",
		CallerID:    "owner-1",
		Slug:        "team",
		DisplayName: "Team",
		Type:        domain.ChannelTypePublic,
	})
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("expected ErrForbidden, got %v", err)
	}
}

func TestChannelService_CreateChannel_InactiveWorkspaceMemberDenied(t *testing.T) {
	for _, status := range []domain.MemberStatus{domain.MemberStatusSuspended, domain.MemberStatusLeft} {
		t.Run(string(status), func(t *testing.T) {
			ms := newFakeMemberStore()
			ms.workspaceMembers[wmKey("ws-1", "admin-1")] = domain.WorkspaceMember{
				WorkspaceID: "ws-1", UserID: "admin-1", Role: domain.WorkspaceRoleAdmin, Status: status,
			}

			_, err := service.NewChannelService(activeWorkspaceStore("ws-1"), &fakeChannelStore{}, ms).CreateChannel(context.Background(), service.CreateChannelInput{
				WorkspaceID: "ws-1",
				CallerID:    "admin-1",
				Slug:        "team",
				DisplayName: "Team",
				Type:        domain.ChannelTypePublic,
			})
			if !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("expected ErrForbidden, got %v", err)
			}
		})
	}
}

func TestChannelService_CreateChannel_CrossWorkspaceCategoryRejected(t *testing.T) {
	ms := newFakeMemberStore()
	ms.workspaceMembers[wmKey("ws-1", "owner-1")] = domain.WorkspaceMember{
		WorkspaceID: "ws-1", UserID: "owner-1", Role: domain.WorkspaceRoleOwner, Status: domain.MemberStatusActive,
	}
	channels := &fakeChannelStore{getCategoryErr: domain.ErrNotFound}

	_, err := service.NewChannelService(activeWorkspaceStore("ws-1"), channels, ms).CreateChannel(context.Background(), service.CreateChannelInput{
		WorkspaceID: "ws-1",
		CallerID:    "owner-1",
		CategoryID:  "cat-other-workspace",
		Slug:        "team",
		DisplayName: "Team",
		Type:        domain.ChannelTypePublic,
	})
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
}

func TestChannelService_CreateChannel_GeralSlugRejected(t *testing.T) {
	ms := newFakeMemberStore()
	ms.workspaceMembers[wmKey("ws-1", "owner-1")] = domain.WorkspaceMember{
		WorkspaceID: "ws-1", UserID: "owner-1", Role: domain.WorkspaceRoleOwner, Status: domain.MemberStatusActive,
	}

	_, err := service.NewChannelService(activeWorkspaceStore("ws-1"), &fakeChannelStore{}, ms).CreateChannel(context.Background(), service.CreateChannelInput{
		WorkspaceID: "ws-1",
		CallerID:    "owner-1",
		Slug:        "geral",
		DisplayName: "Geral",
		Type:        domain.ChannelTypePublic,
	})
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
}

func TestChannelService_CreateChannel_DuplicateSlugRejected(t *testing.T) {
	ms := newFakeMemberStore()
	ms.workspaceMembers[wmKey("ws-1", "owner-1")] = domain.WorkspaceMember{
		WorkspaceID: "ws-1", UserID: "owner-1", Role: domain.WorkspaceRoleOwner, Status: domain.MemberStatusActive,
	}
	channels := &fakeChannelStore{createChanErr: domain.ErrDuplicateSlug}

	_, err := service.NewChannelService(activeWorkspaceStore("ws-1"), channels, ms).CreateChannel(context.Background(), service.CreateChannelInput{
		WorkspaceID: "ws-1",
		CallerID:    "owner-1",
		Slug:        "team",
		DisplayName: "Team",
		Type:        domain.ChannelTypePublic,
	})
	if !errors.Is(err, domain.ErrDuplicateSlug) {
		t.Fatalf("expected ErrDuplicateSlug, got %v", err)
	}
}

func TestChannelService_CreateChannel_InvalidInputsRejected(t *testing.T) {
	ms := newFakeMemberStore()
	ms.workspaceMembers[wmKey("ws-1", "owner-1")] = domain.WorkspaceMember{
		WorkspaceID: "ws-1", UserID: "owner-1", Role: domain.WorkspaceRoleOwner, Status: domain.MemberStatusActive,
	}
	svc := service.NewChannelService(activeWorkspaceStore("ws-1"), &fakeChannelStore{}, ms)

	for _, tc := range []struct {
		name  string
		input service.CreateChannelInput
	}{
		{
			name: "invalid slug",
			input: service.CreateChannelInput{
				WorkspaceID: "ws-1", CallerID: "owner-1", Slug: "has space", DisplayName: "Team", Type: domain.ChannelTypePublic,
			},
		},
		{
			name: "empty display name",
			input: service.CreateChannelInput{
				WorkspaceID: "ws-1", CallerID: "owner-1", Slug: "team", DisplayName: " ", Type: domain.ChannelTypePublic,
			},
		},
		{
			name: "invalid type",
			input: service.CreateChannelInput{
				WorkspaceID: "ws-1", CallerID: "owner-1", Slug: "team", DisplayName: "Team", Type: "secret",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.CreateChannel(context.Background(), tc.input)
			if !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("expected ErrInvalidInput, got %v", err)
			}
		})
	}
}

func TestChannelService_ListChannels_UsesSQLVisibility(t *testing.T) {
	ms := newFakeMemberStore()
	ms.workspaceMembers[wmKey("ws-1", "member-1")] = activeMembership("ws-1", "member-1")
	visible := []domain.Channel{
		{ID: "general", WorkspaceID: "ws-1", Slug: "geral", Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive, IsGeneral: true},
		{ID: "public", WorkspaceID: "ws-1", Slug: "public", Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive},
	}
	channels := &fakeChannelStore{visibleChannels: visible}

	got, err := service.NewChannelService(activeWorkspaceStore("ws-1"), channels, ms).ListChannels(context.Background(), "ws-1", "member-1")
	if err != nil {
		t.Fatalf("ListChannels: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected SQL-visible channels only, got %+v", got)
	}
	if channels.listVisibleCalls != 1 || channels.listCalls != 0 {
		t.Fatalf("expected SQL visible list only, visible=%d all=%d", channels.listVisibleCalls, channels.listCalls)
	}
}

func TestChannelService_ListChannels_StorageErrorPropagates(t *testing.T) {
	ms := newFakeMemberStore()
	ms.workspaceMembers[wmKey("ws-1", "member-1")] = activeMembership("ws-1", "member-1")
	want := errors.New("db unavailable")

	_, err := service.NewChannelService(activeWorkspaceStore("ws-1"), &fakeChannelStore{listVisibleErr: want}, ms).ListChannels(context.Background(), "ws-1", "member-1")
	if !errors.Is(err, want) {
		t.Fatalf("expected storage error, got %v", err)
	}
}

func TestChannelService_ReadPrivateChannelHiddenFromNonMember(t *testing.T) {
	ms := newFakeMemberStore()
	ms.workspaceMembers[wmKey("ws-1", "member-1")] = activeMembership("ws-1", "member-1")
	channels := &fakeChannelStore{getVisibleErr: domain.ErrNotFound}

	_, err := service.NewChannelService(activeWorkspaceStore("ws-1"), channels, ms).GetChannel(context.Background(), service.GetChannelInput{
		WorkspaceID: "ws-1",
		CallerID:    "member-1",
		ChannelID:   "private",
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("hidden private channel should look not found, got %v", err)
	}
}

func TestChannelService_ReadPrivateChannelVisibleToChannelMember(t *testing.T) {
	ms := newFakeMemberStore()
	ms.workspaceMembers[wmKey("ws-1", "member-1")] = activeMembership("ws-1", "member-1")
	private := domain.Channel{ID: "private", WorkspaceID: "ws-1", Slug: "private", Type: domain.ChannelTypePrivate, Status: domain.ChannelStatusActive}
	channels := &fakeChannelStore{visibleChannel: private}

	got, err := service.NewChannelService(activeWorkspaceStore("ws-1"), channels, ms).GetChannel(context.Background(), service.GetChannelInput{
		WorkspaceID: "ws-1",
		CallerID:    "member-1",
		ChannelID:   "private",
	})
	if err != nil {
		t.Fatalf("GetChannel: %v", err)
	}
	if got.ID != "private" {
		t.Fatalf("expected private channel, got %+v", got)
	}
}

func TestChannelService_ReadChannelBySlugUsesVisibility(t *testing.T) {
	ms := newFakeMemberStore()
	ms.workspaceMembers[wmKey("ws-1", "member-1")] = activeMembership("ws-1", "member-1")
	public := domain.Channel{ID: "public", WorkspaceID: "ws-1", Slug: "public", Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive}
	channels := &fakeChannelStore{visibleChannel: public}

	got, err := service.NewChannelService(activeWorkspaceStore("ws-1"), channels, ms).GetChannel(context.Background(), service.GetChannelInput{
		WorkspaceID: "ws-1",
		CallerID:    "member-1",
		Slug:        " PUBLIC ",
	})
	if err != nil {
		t.Fatalf("GetChannel by slug: %v", err)
	}
	if got.Slug != "public" || channels.getVisibleBySlugCalls != 1 {
		t.Fatalf("expected normalized visible slug lookup, got=%+v slugCalls=%d", got, channels.getVisibleBySlugCalls)
	}
}

func TestChannelService_ReadChannel_InvalidSlugRejected(t *testing.T) {
	ms := newFakeMemberStore()
	ms.workspaceMembers[wmKey("ws-1", "member-1")] = activeMembership("ws-1", "member-1")

	_, err := service.NewChannelService(activeWorkspaceStore("ws-1"), &fakeChannelStore{}, ms).GetChannel(context.Background(), service.GetChannelInput{
		WorkspaceID: "ws-1",
		CallerID:    "member-1",
		Slug:        "bad slug",
	})
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
}

func TestChannelService_ReadCrossWorkspaceChannelDenied(t *testing.T) {
	ms := newFakeMemberStore()
	ms.workspaceMembers[wmKey("ws-1", "member-1")] = activeMembership("ws-1", "member-1")
	channels := &fakeChannelStore{getVisibleErr: domain.ErrNotFound}

	_, err := service.NewChannelService(activeWorkspaceStore("ws-1"), channels, ms).GetChannel(context.Background(), service.GetChannelInput{
		WorkspaceID: "ws-1",
		CallerID:    "member-1",
		ChannelID:   "channel-from-ws-2",
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("cross-workspace channel should be not found, got %v", err)
	}
}

func TestChannelService_WorkspaceLookupErrorPropagates(t *testing.T) {
	want := errors.New("workspace db unavailable")
	_, err := service.NewChannelService(&fakeWorkspaceStore{getByIDErr: want}, &fakeChannelStore{}, newFakeMemberStore()).ListChannels(context.Background(), "ws-1", "member-1")
	if !errors.Is(err, want) {
		t.Fatalf("expected workspace lookup error, got %v", err)
	}
}

func TestChannelService_MemberLookupErrorPropagates(t *testing.T) {
	ms := newFakeMemberStore()
	want := errors.New("member db unavailable")
	ms.getWMErr = want

	_, err := service.NewChannelService(activeWorkspaceStore("ws-1"), &fakeChannelStore{}, ms).ListChannels(context.Background(), "ws-1", "member-1")
	if !errors.Is(err, want) {
		t.Fatalf("expected member lookup error, got %v", err)
	}
}

func TestChannelService_UpdateChannel_ManagerCanUpdateMutableFields(t *testing.T) {
	ms := newFakeMemberStore()
	ms.workspaceMembers[wmKey("ws-1", "admin-1")] = domain.WorkspaceMember{
		WorkspaceID: "ws-1", UserID: "admin-1", Role: domain.WorkspaceRoleAdmin, Status: domain.MemberStatusActive,
	}
	current := domain.Channel{ID: "ch-1", WorkspaceID: "ws-1", Slug: "team", DisplayName: "Team", Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive}
	updated := current
	updated.Slug = "team-updates"
	updated.DisplayName = "Team Updates"
	updated.CategoryID = "cat-1"
	updated.Position = 20
	channels := &fakeChannelStore{
		channel:         current,
		category:        domain.ChannelCategory{ID: "cat-1", WorkspaceID: "ws-1"},
		updatedChannel:  updated,
		visibleChannels: []domain.Channel{updated},
	}

	got, err := service.NewChannelService(activeWorkspaceStore("ws-1"), channels, ms).UpdateChannel(context.Background(), service.UpdateChannelInput{
		WorkspaceID: "ws-1",
		CallerID:    "admin-1",
		ChannelID:   "ch-1",
		Slug:        "team-updates",
		DisplayName: "Team Updates",
		CategoryID:  stringPtr("cat-1"),
		Position:    intPtr(20),
	})
	if err != nil {
		t.Fatalf("UpdateChannel: %v", err)
	}
	if got.Slug != "team-updates" || got.CategoryID != "cat-1" || got.Position != 20 {
		t.Fatalf("unexpected update: %+v", got)
	}
}

func TestChannelService_UpdateChannel_InvalidInputsRejected(t *testing.T) {
	ms := newFakeMemberStore()
	ms.workspaceMembers[wmKey("ws-1", "owner-1")] = domain.WorkspaceMember{
		WorkspaceID: "ws-1", UserID: "owner-1", Role: domain.WorkspaceRoleOwner, Status: domain.MemberStatusActive,
	}
	current := domain.Channel{ID: "ch-1", WorkspaceID: "ws-1", Slug: "team", DisplayName: "Team", Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive}
	svc := service.NewChannelService(activeWorkspaceStore("ws-1"), &fakeChannelStore{channel: current}, ms)
	invalidType := domain.ChannelType("secret")

	for _, tc := range []struct {
		name  string
		input service.UpdateChannelInput
	}{
		{
			name:  "invalid slug",
			input: service.UpdateChannelInput{WorkspaceID: "ws-1", CallerID: "owner-1", ChannelID: "ch-1", Slug: "bad slug"},
		},
		{
			name:  "reserved geral slug",
			input: service.UpdateChannelInput{WorkspaceID: "ws-1", CallerID: "owner-1", ChannelID: "ch-1", Slug: "geral"},
		},
		{
			name:  "empty display name after trim",
			input: service.UpdateChannelInput{WorkspaceID: "ws-1", CallerID: "owner-1", ChannelID: "ch-1", DisplayName: " "},
		},
		{
			name:  "invalid type",
			input: service.UpdateChannelInput{WorkspaceID: "ws-1", CallerID: "owner-1", ChannelID: "ch-1", Type: &invalidType},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.UpdateChannel(context.Background(), tc.input)
			if !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("expected ErrInvalidInput, got %v", err)
			}
		})
	}
}

func TestChannelService_UpdateChannel_MemberWithoutManageRoleDenied(t *testing.T) {
	ms := newFakeMemberStore()
	ms.workspaceMembers[wmKey("ws-1", "member-1")] = domain.WorkspaceMember{
		WorkspaceID: "ws-1", UserID: "member-1", Role: domain.WorkspaceRoleMember, Status: domain.MemberStatusActive,
	}
	current := domain.Channel{ID: "ch-1", WorkspaceID: "ws-1", Slug: "team", DisplayName: "Team", Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive}

	_, err := service.NewChannelService(activeWorkspaceStore("ws-1"), &fakeChannelStore{channel: current}, ms).UpdateChannel(context.Background(), service.UpdateChannelInput{
		WorkspaceID: "ws-1",
		CallerID:    "member-1",
		ChannelID:   "ch-1",
		DisplayName: "New",
	})
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("expected ErrForbidden, got %v", err)
	}
}

func TestChannelService_UpdateChannel_CategoryLookupErrorPropagates(t *testing.T) {
	ms := newFakeMemberStore()
	ms.workspaceMembers[wmKey("ws-1", "owner-1")] = domain.WorkspaceMember{
		WorkspaceID: "ws-1", UserID: "owner-1", Role: domain.WorkspaceRoleOwner, Status: domain.MemberStatusActive,
	}
	current := domain.Channel{ID: "ch-1", WorkspaceID: "ws-1", Slug: "team", DisplayName: "Team", Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive}
	want := errors.New("category db unavailable")

	_, err := service.NewChannelService(activeWorkspaceStore("ws-1"), &fakeChannelStore{channel: current, getCategoryErr: want}, ms).UpdateChannel(context.Background(), service.UpdateChannelInput{
		WorkspaceID: "ws-1",
		CallerID:    "owner-1",
		ChannelID:   "ch-1",
		CategoryID:  stringPtr("cat-1"),
	})
	if !errors.Is(err, want) {
		t.Fatalf("expected category lookup error, got %v", err)
	}
}

func TestChannelService_UpdateChannel_GeneralImmutable(t *testing.T) {
	ms := newFakeMemberStore()
	ms.workspaceMembers[wmKey("ws-1", "owner-1")] = domain.WorkspaceMember{
		WorkspaceID: "ws-1", UserID: "owner-1", Role: domain.WorkspaceRoleOwner, Status: domain.MemberStatusActive,
	}
	general := domain.Channel{ID: "general", WorkspaceID: "ws-1", Slug: "geral", DisplayName: "Geral", Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive, IsGeneral: true}

	_, err := service.NewChannelService(activeWorkspaceStore("ws-1"), &fakeChannelStore{channel: general}, ms).UpdateChannel(context.Background(), service.UpdateChannelInput{
		WorkspaceID: "ws-1",
		CallerID:    "owner-1",
		ChannelID:   "general",
		DisplayName: "General",
	})
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
}

func TestChannelService_UpdateChannel_StorageErrorPropagates(t *testing.T) {
	ms := newFakeMemberStore()
	ms.workspaceMembers[wmKey("ws-1", "owner-1")] = domain.WorkspaceMember{
		WorkspaceID: "ws-1", UserID: "owner-1", Role: domain.WorkspaceRoleOwner, Status: domain.MemberStatusActive,
	}
	current := domain.Channel{ID: "ch-1", WorkspaceID: "ws-1", Slug: "team", DisplayName: "Team", Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive}
	want := errors.New("update db unavailable")

	_, err := service.NewChannelService(activeWorkspaceStore("ws-1"), &fakeChannelStore{channel: current, updateErr: want}, ms).UpdateChannel(context.Background(), service.UpdateChannelInput{
		WorkspaceID: "ws-1",
		CallerID:    "owner-1",
		ChannelID:   "ch-1",
		DisplayName: "Team Updates",
	})
	if !errors.Is(err, want) {
		t.Fatalf("expected update error, got %v", err)
	}
}

func TestChannelService_UpdateChannel_DuplicateSlugRejected(t *testing.T) {
	ms := newFakeMemberStore()
	ms.workspaceMembers[wmKey("ws-1", "owner-1")] = domain.WorkspaceMember{
		WorkspaceID: "ws-1", UserID: "owner-1", Role: domain.WorkspaceRoleOwner, Status: domain.MemberStatusActive,
	}
	current := domain.Channel{ID: "ch-1", WorkspaceID: "ws-1", Slug: "team", DisplayName: "Team", Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive}
	channels := &fakeChannelStore{channel: current, updateErr: domain.ErrDuplicateSlug}

	_, err := service.NewChannelService(activeWorkspaceStore("ws-1"), channels, ms).UpdateChannel(context.Background(), service.UpdateChannelInput{
		WorkspaceID: "ws-1",
		CallerID:    "owner-1",
		ChannelID:   "ch-1",
		Slug:        "existing",
	})
	if !errors.Is(err, domain.ErrDuplicateSlug) {
		t.Fatalf("expected ErrDuplicateSlug, got %v", err)
	}
}

func TestChannelService_UpdateChannel_PublicToPrivateKeepsCallerAsMember(t *testing.T) {
	ms := newFakeMemberStore()
	ms.workspaceMembers[wmKey("ws-1", "owner-1")] = domain.WorkspaceMember{
		WorkspaceID: "ws-1", UserID: "owner-1", Role: domain.WorkspaceRoleOwner, Status: domain.MemberStatusActive,
	}
	current := domain.Channel{ID: "ch-1", WorkspaceID: "ws-1", Slug: "team", DisplayName: "Team", Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive}
	updated := current
	updated.Type = domain.ChannelTypePrivate
	channels := &fakeChannelStore{channel: current, updatedChannel: updated}
	privateType := domain.ChannelTypePrivate

	got, err := service.NewChannelService(activeWorkspaceStore("ws-1"), channels, ms).UpdateChannel(context.Background(), service.UpdateChannelInput{
		WorkspaceID: "ws-1",
		CallerID:    "owner-1",
		ChannelID:   "ch-1",
		Type:        &privateType,
	})
	if err != nil {
		t.Fatalf("UpdateChannel: %v", err)
	}
	if got.Type != domain.ChannelTypePrivate {
		t.Fatalf("expected private type, got %+v", got)
	}
	if channels.lastUpdateInput.EnsureMemberUserID != "owner-1" {
		t.Fatalf("public->private must keep caller as member, input=%+v", channels.lastUpdateInput)
	}
}

func TestChannelService_UpdateChannel_PrivateToPublicAllowedForManager(t *testing.T) {
	ms := newFakeMemberStore()
	ms.workspaceMembers[wmKey("ws-1", "admin-1")] = domain.WorkspaceMember{
		WorkspaceID: "ws-1", UserID: "admin-1", Role: domain.WorkspaceRoleAdmin, Status: domain.MemberStatusActive,
	}
	current := domain.Channel{ID: "ch-1", WorkspaceID: "ws-1", Slug: "private", DisplayName: "Private", Type: domain.ChannelTypePrivate, Status: domain.ChannelStatusActive}
	updated := current
	updated.Type = domain.ChannelTypePublic
	channels := &fakeChannelStore{channel: current, updatedChannel: updated}
	publicType := domain.ChannelTypePublic

	got, err := service.NewChannelService(activeWorkspaceStore("ws-1"), channels, ms).UpdateChannel(context.Background(), service.UpdateChannelInput{
		WorkspaceID: "ws-1",
		CallerID:    "admin-1",
		ChannelID:   "ch-1",
		Type:        &publicType,
	})
	if err != nil {
		t.Fatalf("UpdateChannel: %v", err)
	}
	if got.Type != domain.ChannelTypePublic {
		t.Fatalf("expected public type, got %+v", got)
	}
	if channels.lastUpdateInput.EnsureMemberUserID != "" {
		t.Fatalf("private->public should not add memberships, input=%+v", channels.lastUpdateInput)
	}
}

func TestChannelService_ArchiveChannel_ManagerArchivesNonGeneral(t *testing.T) {
	ms := newFakeMemberStore()
	ms.workspaceMembers[wmKey("ws-1", "owner-1")] = domain.WorkspaceMember{
		WorkspaceID: "ws-1", UserID: "owner-1", Role: domain.WorkspaceRoleOwner, Status: domain.MemberStatusActive,
	}
	current := domain.Channel{ID: "ch-1", WorkspaceID: "ws-1", Slug: "team", DisplayName: "Team", Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive}
	archived := current
	archived.Status = domain.ChannelStatusArchived
	channels := &fakeChannelStore{channel: current, archivedChannel: archived}

	got, err := service.NewChannelService(activeWorkspaceStore("ws-1"), channels, ms).ArchiveChannel(context.Background(), "ws-1", "ch-1", "owner-1")
	if err != nil {
		t.Fatalf("ArchiveChannel: %v", err)
	}
	if got.Status != domain.ChannelStatusArchived || channels.archiveCalls != 1 {
		t.Fatalf("expected archived channel, got=%+v calls=%d", got, channels.archiveCalls)
	}
}

func TestChannelService_ArchiveChannel_StorageErrorPropagates(t *testing.T) {
	ms := newFakeMemberStore()
	ms.workspaceMembers[wmKey("ws-1", "owner-1")] = domain.WorkspaceMember{
		WorkspaceID: "ws-1", UserID: "owner-1", Role: domain.WorkspaceRoleOwner, Status: domain.MemberStatusActive,
	}
	current := domain.Channel{ID: "ch-1", WorkspaceID: "ws-1", Slug: "team", DisplayName: "Team", Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive}
	want := errors.New("archive db unavailable")

	_, err := service.NewChannelService(activeWorkspaceStore("ws-1"), &fakeChannelStore{channel: current, archiveErr: want}, ms).ArchiveChannel(context.Background(), "ws-1", "ch-1", "owner-1")
	if !errors.Is(err, want) {
		t.Fatalf("expected archive error, got %v", err)
	}
}

func TestChannelService_ArchiveChannel_GeneralImmutable(t *testing.T) {
	ms := newFakeMemberStore()
	ms.workspaceMembers[wmKey("ws-1", "owner-1")] = domain.WorkspaceMember{
		WorkspaceID: "ws-1", UserID: "owner-1", Role: domain.WorkspaceRoleOwner, Status: domain.MemberStatusActive,
	}
	general := domain.Channel{ID: "general", WorkspaceID: "ws-1", Slug: "geral", DisplayName: "Geral", Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive, IsGeneral: true}

	_, err := service.NewChannelService(activeWorkspaceStore("ws-1"), &fakeChannelStore{channel: general}, ms).ArchiveChannel(context.Background(), "ws-1", "general", "owner-1")
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
}

func TestChannelService_ArchivedChannelExcludedFromRead(t *testing.T) {
	ms := newFakeMemberStore()
	ms.workspaceMembers[wmKey("ws-1", "member-1")] = activeMembership("ws-1", "member-1")
	channels := &fakeChannelStore{getVisibleErr: domain.ErrNotFound}

	_, err := service.NewChannelService(activeWorkspaceStore("ws-1"), channels, ms).GetChannel(context.Background(), service.GetChannelInput{
		WorkspaceID: "ws-1",
		CallerID:    "member-1",
		ChannelID:   "archived",
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("archived channel should be excluded, got %v", err)
	}
}

func intPtr(v int) *int { return &v }

func stringPtr(v string) *string { return &v }
