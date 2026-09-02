package service_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
)

func TestMentionService_Search_PrivateChannelOutsiderDenied(t *testing.T) {
	members := newFakeMemberStore()
	members.workspaceMembers[wmKey("ws-1", user1)] = domain.WorkspaceMember{
		WorkspaceID: "ws-1", UserID: user1, Role: domain.WorkspaceRoleMember, Status: domain.MemberStatusActive,
	}
	channels := &fakeChannelStore{channel: privateActiveChannel("ws-1", "private-1")}
	svc := service.NewMentionService(
		service.NewMemberService(members, channels, &fakeWorkspaceStore{}),
		service.NewPermissionService(members, channels),
		nil,
	)

	_, err := svc.SearchMentions(context.Background(), service.SearchMentionsInput{
		WorkspaceID: "ws-1", TargetType: "channel", TargetID: "private-1", CallerID: user1, Query: "a",
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected non-enumerating ErrNotFound, got %v", err)
	}
}

func TestMentionService_Search_ReturnsInitialChannelCandidatesInDeterministicOrder(t *testing.T) {
	members := newFakeMemberStore()
	members.workspaceMembers[wmKey("ws-1", user1)] = domain.WorkspaceMember{
		WorkspaceID: "ws-1", UserID: user1, Role: domain.WorkspaceRoleMember, Status: domain.MemberStatusActive,
	}
	members.mentionCandidates = []domain.MentionCandidate{
		{Type: domain.MentionTypeUser, ID: user2, Label: "Alice"},
	}
	channels := &fakeChannelStore{
		channel: publicActiveChannel("ws-1", "ch-1"),
		visibleChannels: []domain.Channel{
			{ID: "ch-2", WorkspaceID: "ws-1", Slug: "geral", DisplayName: "Geral", Status: domain.ChannelStatusActive},
			{ID: "ch-1", WorkspaceID: "ws-1", Slug: "arquitetura", DisplayName: "Arquitetura", Status: domain.ChannelStatusActive},
		},
	}
	svc := service.NewMentionService(
		service.NewMemberService(members, channels, &fakeWorkspaceStore{}),
		service.NewPermissionService(members, channels),
		nil,
	)

	got, err := svc.SearchMentions(context.Background(), service.SearchMentionsInput{
		WorkspaceID: "ws-1", TargetType: "channel", TargetID: "ch-1", CallerID: user1,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got.Users) != 1 || got.Users[0].ID != user2 {
		t.Fatalf("unexpected users: %#v", got.Users)
	}
	if len(got.Channels) != 2 || got.Channels[0].ID != "ch-1" || got.Channels[1].ID != "ch-2" {
		t.Fatalf("unexpected channels: %#v", got.Channels)
	}
	if members.mentionQuery != "" || members.mentionLimit != 20 {
		t.Fatalf("initial query=%q limit=%d", members.mentionQuery, members.mentionLimit)
	}

	got, err = svc.SearchMentions(context.Background(), service.SearchMentionsInput{
		WorkspaceID: "ws-1", TargetType: "channel", TargetID: "ch-1", CallerID: user1, Query: "g",
	})
	if err != nil || len(got.Channels) != 1 || got.Channels[0].ID != "ch-2" {
		t.Fatalf("prefix search channels=%#v err=%v", got.Channels, err)
	}
	if members.mentionQuery != "g" || members.mentionLimit != 20 {
		t.Fatalf("prefix query=%q limit=%d", members.mentionQuery, members.mentionLimit)
	}
}

func TestMentionService_Search_ValidatesAndPropagatesDependencyErrors(t *testing.T) {
	t.Run("rejects overlong query", func(t *testing.T) {
		svc := service.NewMentionService(
			service.NewMemberService(newFakeMemberStore(), &fakeChannelStore{}, &fakeWorkspaceStore{}),
			service.NewPermissionService(newFakeMemberStore(), &fakeChannelStore{}),
			nil,
		)
		_, err := svc.SearchMentions(context.Background(), service.SearchMentionsInput{
			WorkspaceID: "ws-1", TargetType: "channel", TargetID: "ch-1", CallerID: user1,
			Query: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		})
		if !errors.Is(err, domain.ErrInvalidInput) {
			t.Fatalf("expected ErrInvalidInput, got %v", err)
		}
	})

	t.Run("member search error", func(t *testing.T) {
		members := newFakeMemberStore()
		members.workspaceMembers[wmKey("ws-1", user1)] = domain.WorkspaceMember{
			WorkspaceID: "ws-1", UserID: user1, Role: domain.WorkspaceRoleMember, Status: domain.MemberStatusActive,
		}
		members.mentionErr = errors.New("member store failed")
		channels := &fakeChannelStore{channel: publicActiveChannel("ws-1", "ch-1")}
		svc := service.NewMentionService(
			service.NewMemberService(members, channels, &fakeWorkspaceStore{}),
			service.NewPermissionService(members, channels),
			nil,
		)
		_, err := svc.SearchMentions(context.Background(), service.SearchMentionsInput{
			WorkspaceID: "ws-1", TargetType: "channel", TargetID: "ch-1", CallerID: user1,
		})
		if err == nil || !strings.Contains(err.Error(), "search channel members") {
			t.Fatalf("expected wrapped member error, got %v", err)
		}
	})

	t.Run("visible channel error", func(t *testing.T) {
		members := newFakeMemberStore()
		members.workspaceMembers[wmKey("ws-1", user1)] = domain.WorkspaceMember{
			WorkspaceID: "ws-1", UserID: user1, Role: domain.WorkspaceRoleMember, Status: domain.MemberStatusActive,
		}
		channels := &fakeChannelStore{
			channel: publicActiveChannel("ws-1", "ch-1"), listVisibleErr: errors.New("channels failed"),
		}
		svc := service.NewMentionService(
			service.NewMemberService(members, channels, &fakeWorkspaceStore{}),
			service.NewPermissionService(members, channels),
			nil,
		)
		_, err := svc.SearchMentions(context.Background(), service.SearchMentionsInput{
			WorkspaceID: "ws-1", TargetType: "channel", TargetID: "ch-1", CallerID: user1,
		})
		if err == nil || !strings.Contains(err.Error(), "channels failed") {
			t.Fatalf("expected visible channel error, got %v", err)
		}
	})
}

// The flow the code-quality review named: SearchMentions gates on
// PermissionService.CanRead, so a guest that was added to a public channel used
// to get a non-enumerating ErrNotFound for a channel it is a member of. A guest
// still outside the channel keeps getting exactly that.
func TestMentionService_Search_GuestInPublicChannelIsAdmitted(t *testing.T) {
	newGuestService := func(inChannel bool) *service.MentionService {
		members := newFakeMemberStore()
		members.workspaceMembers[wmKey("ws-1", user1)] = domain.WorkspaceMember{
			WorkspaceID: "ws-1", UserID: user1, Role: domain.WorkspaceRoleGuest, Status: domain.MemberStatusActive,
		}
		if inChannel {
			members.channelMembers[cmKey("ch-1", user1)] = domain.ChannelMember{ChannelID: "ch-1", UserID: user1}
		}
		members.mentionCandidates = []domain.MentionCandidate{
			{Type: domain.MentionTypeUser, ID: user2, Label: "Alice"},
		}
		channels := &fakeChannelStore{channel: publicActiveChannel("ws-1", "ch-1")}
		return service.NewMentionService(
			service.NewMemberService(members, channels, &fakeWorkspaceStore{}),
			service.NewPermissionService(members, channels),
			nil,
		)
	}

	t.Run("included guest searches mentions", func(t *testing.T) {
		got, err := newGuestService(true).SearchMentions(context.Background(), service.SearchMentionsInput{
			WorkspaceID: "ws-1", TargetType: "channel", TargetID: "ch-1", CallerID: user1, Query: "a",
		})
		if err != nil {
			t.Fatalf("SearchMentions for an included guest: %v", err)
		}
		if len(got.Users) != 1 || got.Users[0].ID != user2 {
			t.Fatalf("unexpected users: %#v", got.Users)
		}
	})

	t.Run("excluded guest is denied", func(t *testing.T) {
		_, err := newGuestService(false).SearchMentions(context.Background(), service.SearchMentionsInput{
			WorkspaceID: "ws-1", TargetType: "channel", TargetID: "ch-1", CallerID: user1, Query: "a",
		})
		if !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("expected non-enumerating ErrNotFound, got %v", err)
		}
	})
}

func TestMentionService_Search_GroupReturnsOnlyConversationMembers(t *testing.T) {
	members := newFakeMemberStore()
	members.mentionCandidates = []domain.MentionCandidate{
		{Type: domain.MentionTypeUser, ID: user2, Label: "Juliane Lino"},
	}
	dms := &fakeDMStore{visibleConversation: domain.DMConversation{
		ID: "group-1", WorkspaceID: "ws-1", Type: domain.DMConversationTypeGroup,
		Status: domain.DMConversationStatusActive,
	}}
	svc := service.NewMentionService(
		service.NewMemberService(members, &fakeChannelStore{}, &fakeWorkspaceStore{}),
		service.NewPermissionService(members, &fakeChannelStore{}),
		dms,
	)

	got, err := svc.SearchMentions(context.Background(), service.SearchMentionsInput{
		WorkspaceID: "ws-1", TargetType: "dm", TargetID: "group-1", CallerID: user1,
	})
	if err != nil {
		t.Fatalf("SearchMentions: %v", err)
	}
	if len(got.Users) != 1 || got.Users[0].ID != user2 || len(got.Channels) != 0 {
		t.Fatalf("unexpected group candidates: %#v", got)
	}
}

func TestMentionService_Search_DirectAndInvisibleDMsAreNotFound(t *testing.T) {
	tests := []struct {
		name string
		dms  *fakeDMStore
	}{
		{name: "direct", dms: &fakeDMStore{visibleConversation: domain.DMConversation{
			ID: "dm-1", WorkspaceID: "ws-1", Type: domain.DMConversationTypeDirect,
			Status: domain.DMConversationStatusActive,
		}}},
		{name: "invisible", dms: &fakeDMStore{getVisibleErr: domain.ErrNotFound}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			members := newFakeMemberStore()
			svc := service.NewMentionService(
				service.NewMemberService(members, &fakeChannelStore{}, &fakeWorkspaceStore{}),
				service.NewPermissionService(members, &fakeChannelStore{}),
				tt.dms,
			)
			_, err := svc.SearchMentions(context.Background(), service.SearchMentionsInput{
				WorkspaceID: "ws-1", TargetType: "dm", TargetID: "dm-1", CallerID: user1,
			})
			if !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("expected ErrNotFound, got %v", err)
			}
		})
	}
}
