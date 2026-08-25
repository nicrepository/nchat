package service_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
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
	if channels.creatorMembershipSeeds != 0 {
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
	if channels.creatorMembershipSeeds != 1 || channels.lastSeededMemberUserID != "admin-1" {
		t.Fatalf("private channel must add creator atomically, calls=%d user=%q", channels.creatorMembershipSeeds, channels.lastSeededMemberUserID)
	}
}

// Creating a channel takes active membership, not a management role (BUG #393):
// every active role that reaches the workspace's public channels gets to
// storage, and the creator recorded is the caller the service was given — never
// anything the role could have influenced. The guest is covered separately
// below, because RF-74 makes it the one active role that may not create.
func TestChannelService_CreateChannel_AnyActiveRoleSucceeds(t *testing.T) {
	for _, role := range []domain.WorkspaceRole{
		domain.WorkspaceRoleOwner,
		domain.WorkspaceRoleAdmin,
		domain.WorkspaceRoleModerator,
		domain.WorkspaceRoleMember,
	} {
		t.Run(string(role), func(t *testing.T) {
			ms := newFakeMemberStore()
			ms.workspaceMembers[wmKey("ws-1", "caller-1")] = domain.WorkspaceMember{
				WorkspaceID: "ws-1", UserID: "caller-1", Role: role, Status: domain.MemberStatusActive,
			}
			channels := &fakeChannelStore{createdChannel: domain.Channel{
				ID: "ch-1", WorkspaceID: "ws-1", Slug: "team", DisplayName: "Team", Type: domain.ChannelTypePublic,
			}}

			got, err := service.NewChannelService(activeWorkspaceStore("ws-1"), channels, ms).CreateChannel(context.Background(), service.CreateChannelInput{
				WorkspaceID: "ws-1",
				CallerID:    "caller-1",
				Slug:        "team",
				DisplayName: "Team",
				Type:        domain.ChannelTypePublic,
			})
			if err != nil {
				t.Fatalf("CreateChannel: %v", err)
			}
			if got.ID != "ch-1" {
				t.Fatalf("unexpected channel: %+v", got)
			}
			if channels.lastCreateInput.CreatedBy != "caller-1" || channels.lastCreateInput.IsGeneral {
				t.Fatalf("service must own created_by/is_general, input=%+v", channels.lastCreateInput)
			}
		})
	}
}

// RF-74: a guest reaches only the channels it was added to, so it may not mint
// channels of its own — a guest-created public channel would also be visible to
// every real member. An unrecognised role is denied for the same reason a
// guest is: the predicate is an allowlist, so a role the code does not know is
// never treated as a full member. Neither ever reaches storage.
func TestChannelService_CreateChannel_GuestAndUnknownRoleDenied(t *testing.T) {
	for _, role := range []domain.WorkspaceRole{
		domain.WorkspaceRoleGuest,
		domain.WorkspaceRole("wizard"),
		domain.WorkspaceRole(""),
	} {
		t.Run(string(role), func(t *testing.T) {
			ms := newFakeMemberStore()
			ms.workspaceMembers[wmKey("ws-1", "caller-1")] = domain.WorkspaceMember{
				WorkspaceID: "ws-1", UserID: "caller-1", Role: role, Status: domain.MemberStatusActive,
			}
			channels := &fakeChannelStore{createdChannel: domain.Channel{ID: "ch-1", WorkspaceID: "ws-1"}}

			_, err := service.NewChannelService(activeWorkspaceStore("ws-1"), channels, ms).CreateChannel(context.Background(), service.CreateChannelInput{
				WorkspaceID: "ws-1",
				CallerID:    "caller-1",
				Slug:        "team",
				DisplayName: "Team",
				Type:        domain.ChannelTypePublic,
			})
			if !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("CreateChannel = %v, want ErrForbidden", err)
			}
			if channels.lastCreateInput.Slug != "" {
				t.Fatalf("a denied creation reached storage: %+v", channels.lastCreateInput)
			}
		})
	}
}

// A membership row belonging to another workspace never authorizes a creation
// in this one, even when it is active and holds the highest role.
func TestChannelService_CreateChannel_CrossWorkspaceMembershipDenied(t *testing.T) {
	ms := newFakeMemberStore()
	ms.workspaceMembers[wmKey("ws-1", "owner-2")] = domain.WorkspaceMember{
		WorkspaceID: "ws-2", UserID: "owner-2", Role: domain.WorkspaceRoleOwner, Status: domain.MemberStatusActive,
	}
	channels := &fakeChannelStore{}

	_, err := service.NewChannelService(activeWorkspaceStore("ws-1"), channels, ms).CreateChannel(context.Background(), service.CreateChannelInput{
		WorkspaceID: "ws-1",
		CallerID:    "owner-2",
		Slug:        "team",
		DisplayName: "Team",
		Type:        domain.ChannelTypePublic,
	})
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("expected ErrForbidden, got %v", err)
	}
	if channels.lastCreateInput.Slug != "" {
		t.Fatalf("denied caller must not reach storage, input=%+v", channels.lastCreateInput)
	}
}

// Update and archive stay management operations; loosening creation must not
// have loosened them.
func TestChannelService_UpdateAndArchive_StillRequireManageRole(t *testing.T) {
	ms := newFakeMemberStore()
	ms.workspaceMembers[wmKey("ws-1", "member-1")] = domain.WorkspaceMember{
		WorkspaceID: "ws-1", UserID: "member-1", Role: domain.WorkspaceRoleMember, Status: domain.MemberStatusActive,
	}
	svc := service.NewChannelService(activeWorkspaceStore("ws-1"), &fakeChannelStore{}, ms)

	if _, err := svc.UpdateChannel(context.Background(), service.UpdateChannelInput{
		WorkspaceID: "ws-1", CallerID: "member-1", ChannelID: "ch-1", DisplayName: "Team",
	}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("UpdateChannel: expected ErrForbidden, got %v", err)
	}
	if _, err := svc.ArchiveChannel(context.Background(), "ws-1", "ch-1", "member-1"); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("ArchiveChannel: expected ErrForbidden, got %v", err)
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
	if got.Channel.Slug != "team-updates" || got.Channel.CategoryID != "cat-1" || got.Channel.Position != 20 {
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
	if got.Channel.Type != domain.ChannelTypePrivate {
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
	if got.Channel.Type != domain.ChannelTypePublic {
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

// display_name is required, trimmed, and bounded at
// domain.MaxChannelDisplayNameCodePoints — counted in code points, so an emoji
// costs exactly what an ASCII letter costs. A rejected name is never persisted
// and never echoed back: it can be tens of kilobytes of caller-controlled text.
func TestChannelService_CreateChannel_DisplayNameValidation(t *testing.T) {
	const maxCodePoints = domain.MaxChannelDisplayNameCodePoints
	for _, test := range []struct {
		name        string
		displayName string
		wantErr     error
		wantStored  string
	}{
		{name: "empty", displayName: "", wantErr: domain.ErrChannelDisplayNameRequired},
		{name: "whitespace only", displayName: "   \t\n ", wantErr: domain.ErrChannelDisplayNameRequired},
		{
			name:        "100 ascii",
			displayName: strings.Repeat("a", maxCodePoints),
			wantStored:  strings.Repeat("a", maxCodePoints),
		},
		{
			name:        "101 ascii",
			displayName: strings.Repeat("a", maxCodePoints+1),
			wantErr:     domain.ErrChannelDisplayNameTooLong,
		},
		// 100 emoji are 400 UTF-8 bytes and 200 UTF-16 units; only a code-point
		// count accepts them, which is what the database also does.
		{
			name:        "100 emoji",
			displayName: strings.Repeat("😀", maxCodePoints),
			wantStored:  strings.Repeat("😀", maxCodePoints),
		},
		{
			name:        "101 emoji",
			displayName: strings.Repeat("😀", maxCodePoints+1),
			wantErr:     domain.ErrChannelDisplayNameTooLong,
		},
		{
			name:        "mixed ascii and emoji at the limit",
			displayName: strings.Repeat("a", 50) + strings.Repeat("😀", 50),
			wantStored:  strings.Repeat("a", 50) + strings.Repeat("😀", 50),
		},
		{
			name:        "mixed ascii and emoji one over",
			displayName: strings.Repeat("a", 50) + strings.Repeat("😀", 51),
			wantErr:     domain.ErrChannelDisplayNameTooLong,
		},
		// Trimming happens before counting, so padding never decides the outcome
		// and the stored value carries no surrounding whitespace.
		{
			name:        "trimmed back to the limit",
			displayName: "  " + strings.Repeat("a", maxCodePoints) + "\t",
			wantStored:  strings.Repeat("a", maxCodePoints),
		},
		{
			name:        "over the limit only once trimmed",
			displayName: " " + strings.Repeat("😀", maxCodePoints+1) + " ",
			wantErr:     domain.ErrChannelDisplayNameTooLong,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ms := newFakeMemberStore()
			ms.workspaceMembers[wmKey("ws-1", "member-1")] = domain.WorkspaceMember{
				WorkspaceID: "ws-1", UserID: "member-1", Role: domain.WorkspaceRoleMember, Status: domain.MemberStatusActive,
			}
			channels := &fakeChannelStore{createdChannel: domain.Channel{ID: "ch-1", WorkspaceID: "ws-1"}}

			_, err := service.NewChannelService(activeWorkspaceStore("ws-1"), channels, ms).CreateChannel(context.Background(), service.CreateChannelInput{
				WorkspaceID: "ws-1",
				CallerID:    "member-1",
				Slug:        "team",
				DisplayName: test.displayName,
				Type:        domain.ChannelTypePublic,
			})
			if test.wantErr == nil {
				if err != nil {
					t.Fatalf("CreateChannel: %v", err)
				}
				got := channels.lastCreateInput.DisplayName
				if got != test.wantStored {
					t.Fatalf("stored %d code points, want %d",
						utf8.RuneCountInString(got), utf8.RuneCountInString(test.wantStored))
				}
				if utf8.RuneCountInString(got) > maxCodePoints {
					t.Fatalf("stored name exceeds the cap: %d code points", utf8.RuneCountInString(got))
				}
				return
			}
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v, want %v", err, test.wantErr)
			}
			// Both sentinels stay mapped to the status the endpoint already
			// returns; the HTTP contract does not move.
			if !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("error = %v, want it to satisfy ErrInvalidInput", err)
			}
			if channels.lastCreateInput.DisplayName != "" || channels.lastCreateInput.Slug != "" {
				t.Fatalf("invalid name reached storage: %+v", channels.lastCreateInput)
			}
			if trimmed := strings.TrimSpace(test.displayName); trimmed != "" && strings.Contains(err.Error(), trimmed) {
				t.Fatal("error echoes the rejected value")
			}
		})
	}
}

// Renaming obeys the same rule from the same helper: an update must not be the
// way past a cap creation enforces.
func TestChannelService_UpdateChannel_DisplayNameValidation(t *testing.T) {
	const maxCodePoints = domain.MaxChannelDisplayNameCodePoints
	newSvc := func() (*service.ChannelService, *fakeChannelStore) {
		ms := newFakeMemberStore()
		ms.workspaceMembers[wmKey("ws-1", "owner-1")] = domain.WorkspaceMember{
			WorkspaceID: "ws-1", UserID: "owner-1", Role: domain.WorkspaceRoleOwner, Status: domain.MemberStatusActive,
		}
		channels := &fakeChannelStore{channel: domain.Channel{
			ID: "ch-1", WorkspaceID: "ws-1", Slug: "team", DisplayName: "Team",
			Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive,
		}}
		return service.NewChannelService(activeWorkspaceStore("ws-1"), channels, ms), channels
	}

	for _, test := range []struct {
		name        string
		displayName string
		wantErr     error
		wantStored  string
	}{
		{name: "whitespace only", displayName: "   ", wantErr: domain.ErrChannelDisplayNameRequired},
		{name: "101 ascii", displayName: strings.Repeat("a", maxCodePoints+1), wantErr: domain.ErrChannelDisplayNameTooLong},
		{name: "101 emoji", displayName: strings.Repeat("😀", maxCodePoints+1), wantErr: domain.ErrChannelDisplayNameTooLong},
		{name: "100 ascii", displayName: strings.Repeat("a", maxCodePoints), wantStored: strings.Repeat("a", maxCodePoints)},
		{name: "100 emoji", displayName: strings.Repeat("😀", maxCodePoints), wantStored: strings.Repeat("😀", maxCodePoints)},
		{name: "trimmed", displayName: "  Infra  ", wantStored: "Infra"},
	} {
		t.Run(test.name, func(t *testing.T) {
			svc, channels := newSvc()
			_, err := svc.UpdateChannel(context.Background(), service.UpdateChannelInput{
				WorkspaceID: "ws-1", CallerID: "owner-1", ChannelID: "ch-1", DisplayName: test.displayName,
			})
			if test.wantErr == nil {
				if err != nil {
					t.Fatalf("UpdateChannel: %v", err)
				}
				if got := channels.lastUpdateInput.DisplayName; got != test.wantStored {
					t.Fatalf("stored %d code points, want %d",
						utf8.RuneCountInString(got), utf8.RuneCountInString(test.wantStored))
				}
				return
			}
			if !errors.Is(err, test.wantErr) || !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("error = %v, want %v", err, test.wantErr)
			}
			if channels.lastUpdateInput.ChannelID != "" {
				t.Fatalf("an invalid rename reached storage: %+v", channels.lastUpdateInput)
			}
		})
	}
}

// The workspace bootstrap persists display_name too, so it must not be the path
// that skips the cap.
func TestWorkspaceService_CreateChannel_EnforcesDisplayNameCap(t *testing.T) {
	channels := &fakeChannelStore{createdChannel: domain.Channel{ID: "ch-1"}}
	svc := service.NewWorkspaceService(activeWorkspaceStore("ws-1"), channels)

	_, err := svc.CreateChannel(context.Background(), storage.CreateChannelInput{
		WorkspaceID: "ws-1",
		Slug:        "team",
		DisplayName: strings.Repeat("😀", domain.MaxChannelDisplayNameCodePoints+1),
		Type:        domain.ChannelTypePublic,
	})
	if !errors.Is(err, domain.ErrChannelDisplayNameTooLong) {
		t.Fatalf("error = %v, want ErrChannelDisplayNameTooLong", err)
	}
	if channels.lastCreateInput.Slug != "" {
		t.Fatalf("an oversized name reached storage: %+v", channels.lastCreateInput)
	}
}

func TestChannelService_GetCallParticipantProfiles_ResolvesOnlyRequestedActiveMembers(t *testing.T) {
	userA := uuid.NewString()
	userMissing := uuid.NewString()
	members := detailsMemberStore()
	members.callParticipantProfiles = map[string]domain.CallParticipantProfile{
		userA: {UserID: userA, DisplayName: "Ana Souza", AvatarURL: "https://x/a.png"},
	}
	svc := service.NewChannelService(activeWorkspaceStore("ws-1"), detailsChannelStore(), members)

	got, err := svc.GetCallParticipantProfiles(context.Background(), service.ChannelCallParticipantProfilesInput{
		WorkspaceID: "ws-1", CallerID: "user-1", ChannelID: "ch-1", UserIDs: []string{userA, userMissing},
	})
	if err != nil {
		t.Fatalf("GetCallParticipantProfiles: %v", err)
	}
	if len(got) != 1 || got[0].UserID != userA {
		t.Fatalf("unexpected profiles: %#v", got)
	}
}

func TestChannelService_GetCallParticipantProfiles_RejectsOversizedBatch(t *testing.T) {
	svc := service.NewChannelService(activeWorkspaceStore("ws-1"), detailsChannelStore(), detailsMemberStore())
	ids := make([]string, domain.MaxCallParticipantProfileIDs+1)
	for i := range ids {
		ids[i] = uuid.NewString()
	}
	_, err := svc.GetCallParticipantProfiles(context.Background(), service.ChannelCallParticipantProfilesInput{
		WorkspaceID: "ws-1", CallerID: "user-1", ChannelID: "ch-1", UserIDs: ids,
	})
	if !errors.Is(err, domain.ErrTooManyCallParticipantsRequested) {
		t.Fatalf("want ErrTooManyCallParticipantsRequested, got %v", err)
	}
}

func TestChannelService_GetCallParticipantProfiles_UnauthorizedCallerIsRejected(t *testing.T) {
	svc := service.NewChannelService(activeWorkspaceStore("ws-1"), detailsChannelStore(), newFakeMemberStore())
	_, err := svc.GetCallParticipantProfiles(context.Background(), service.ChannelCallParticipantProfilesInput{
		WorkspaceID: "ws-1", CallerID: "user-1", ChannelID: "ch-1", UserIDs: []string{uuid.NewString()},
	})
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("want ErrForbidden for a caller with no workspace membership, got %v", err)
	}
}

// TestChannelService_GetCallParticipantProfiles_InvisibleChannelIsRejected proves
// a caller cannot use this endpoint against a channel they cannot already see —
// a private channel from another workspace, or one this caller never joined —
// exactly the same visibility gate GetChannelDetails enforces, so the batch
// contract cannot be used as a side door into a channel's membership.
func TestChannelService_GetCallParticipantProfiles_InvisibleChannelIsRejected(t *testing.T) {
	members := detailsMemberStore()
	// A channel the store does not vouch for as visible to this caller —
	// detailsChannelStore's fixture is "ch-1"; asking about "ch-2" must fail.
	svc := service.NewChannelService(activeWorkspaceStore("ws-1"), detailsChannelStore(), members)
	_, err := svc.GetCallParticipantProfiles(context.Background(), service.ChannelCallParticipantProfilesInput{
		WorkspaceID: "ws-1", CallerID: "user-1", ChannelID: "ch-2", UserIDs: []string{uuid.NewString()},
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("want ErrNotFound for a channel outside the caller's visibility, got %v", err)
	}
	if members.lastCallParticipantIDs != nil {
		t.Fatalf("store must never be reached for a caller failing the visibility gate, got %v", members.lastCallParticipantIDs)
	}
}

// TestChannelService_GetCallParticipantProfiles_RejectsMalformedUUID proves a
// non-UUID (or the nil UUID) in the batch fails safely as invalid input,
// before ever reaching the store — never a 500 from a malformed SQL array
// literal, and never silently dropped as "just another unresolved id".
func TestChannelService_GetCallParticipantProfiles_RejectsMalformedUUID(t *testing.T) {
	for name, ids := range map[string][]string{
		"not a uuid at all":         {"not-a-uuid"},
		"nil uuid":                  {"00000000-0000-0000-0000-000000000000"},
		"valid uuid plus malformed": {uuid.NewString(), "<script>alert(1)</script>"},
	} {
		t.Run(name, func(t *testing.T) {
			members := detailsMemberStore()
			svc := service.NewChannelService(activeWorkspaceStore("ws-1"), detailsChannelStore(), members)
			_, err := svc.GetCallParticipantProfiles(context.Background(), service.ChannelCallParticipantProfilesInput{
				WorkspaceID: "ws-1", CallerID: "user-1", ChannelID: "ch-1", UserIDs: ids,
			})
			if !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("want ErrInvalidInput, got %v", err)
			}
			if members.lastCallParticipantIDs != nil {
				t.Fatalf("a malformed id must never reach the store, got %v", members.lastCallParticipantIDs)
			}
		})
	}
}

// TestChannelService_GetCallParticipantProfiles_RejectsEmptyBatch documents and
// proves the intentional behavior for an empty/all-blank id list: a resolve
// with nothing to resolve is a client bug, not a silent no-op success — the
// same rule normalizeAddMemberIDs applies to add-members.
func TestChannelService_GetCallParticipantProfiles_RejectsEmptyBatch(t *testing.T) {
	for name, ids := range map[string][]string{
		"nil":            nil,
		"empty slice":    {},
		"only blank ids": {"   ", ""},
	} {
		t.Run(name, func(t *testing.T) {
			svc := service.NewChannelService(activeWorkspaceStore("ws-1"), detailsChannelStore(), detailsMemberStore())
			_, err := svc.GetCallParticipantProfiles(context.Background(), service.ChannelCallParticipantProfilesInput{
				WorkspaceID: "ws-1", CallerID: "user-1", ChannelID: "ch-1", UserIDs: ids,
			})
			if !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("want ErrInvalidInput (empty batch), got %v", err)
			}
		})
	}
}

// TestChannelService_GetCallParticipantProfiles_DeduplicatesDeterministically
// proves a batch naming the same user twice (including two different letter
// cases of the same UUID) reaches the store exactly once, sorted — so a retry
// is byte-identical and the store is never asked to do duplicate work.
func TestChannelService_GetCallParticipantProfiles_DeduplicatesDeterministically(t *testing.T) {
	id := uuid.New()
	lower := id.String()
	upper := strings.ToUpper(lower)
	members := detailsMemberStore()
	svc := service.NewChannelService(activeWorkspaceStore("ws-1"), detailsChannelStore(), members)

	_, err := svc.GetCallParticipantProfiles(context.Background(), service.ChannelCallParticipantProfilesInput{
		WorkspaceID: "ws-1", CallerID: "user-1", ChannelID: "ch-1", UserIDs: []string{upper, lower, lower},
	})
	if err != nil {
		t.Fatalf("GetCallParticipantProfiles: %v", err)
	}
	if len(members.lastCallParticipantIDs) != 1 || members.lastCallParticipantIDs[0] != lower {
		t.Fatalf("want exactly one canonical lowercase id reaching the store, got %v", members.lastCallParticipantIDs)
	}
}
