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
	)

	_, err := svc.SearchMentions(context.Background(), service.SearchMentionsInput{
		WorkspaceID: "ws-1", ChannelID: "private-1", CallerID: user1, Query: "a",
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected non-enumerating ErrNotFound, got %v", err)
	}
}

func TestMentionService_Search_ReturnsChannelMembersAndVisibleChannelsByPrefix(t *testing.T) {
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
			{ID: "ch-1", WorkspaceID: "ws-1", Slug: "arquitetura", DisplayName: "Arquitetura", Status: domain.ChannelStatusActive},
			{ID: "ch-2", WorkspaceID: "ws-1", Slug: "geral", DisplayName: "Geral", Status: domain.ChannelStatusActive},
		},
	}
	svc := service.NewMentionService(
		service.NewMemberService(members, channels, &fakeWorkspaceStore{}),
		service.NewPermissionService(members, channels),
	)

	got, err := svc.SearchMentions(context.Background(), service.SearchMentionsInput{
		WorkspaceID: "ws-1", ChannelID: "ch-1", CallerID: user1, Query: "a",
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got.Users) != 1 || got.Users[0].ID != user2 {
		t.Fatalf("unexpected users: %#v", got.Users)
	}
	if len(got.Channels) != 1 || got.Channels[0].ID != "ch-1" {
		t.Fatalf("unexpected channels: %#v", got.Channels)
	}
}

func TestMentionService_Search_ValidatesAndPropagatesDependencyErrors(t *testing.T) {
	t.Run("rejects overlong query", func(t *testing.T) {
		svc := service.NewMentionService(
			service.NewMemberService(newFakeMemberStore(), &fakeChannelStore{}, &fakeWorkspaceStore{}),
			service.NewPermissionService(newFakeMemberStore(), &fakeChannelStore{}),
		)
		_, err := svc.SearchMentions(context.Background(), service.SearchMentionsInput{
			WorkspaceID: "ws-1", ChannelID: "ch-1", CallerID: user1,
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
		)
		_, err := svc.SearchMentions(context.Background(), service.SearchMentionsInput{
			WorkspaceID: "ws-1", ChannelID: "ch-1", CallerID: user1,
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
		)
		_, err := svc.SearchMentions(context.Background(), service.SearchMentionsInput{
			WorkspaceID: "ws-1", ChannelID: "ch-1", CallerID: user1,
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
		)
	}

	t.Run("included guest searches mentions", func(t *testing.T) {
		got, err := newGuestService(true).SearchMentions(context.Background(), service.SearchMentionsInput{
			WorkspaceID: "ws-1", ChannelID: "ch-1", CallerID: user1, Query: "a",
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
			WorkspaceID: "ws-1", ChannelID: "ch-1", CallerID: user1, Query: "a",
		})
		if !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("expected non-enumerating ErrNotFound, got %v", err)
		}
	})
}
