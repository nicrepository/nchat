package service_test

import (
	"context"
	"errors"
	"testing"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// The administrable channel roster (issue #469).
//
// The whole point of this surface is that it is *not* the details panel's
// presence preview: it answers "who belongs", and it answers it only to a
// caller who may change that membership. Every case below is about one of
// those two sentences.

func rosterMemberStore(role domain.WorkspaceRole) *fakeMemberStore {
	ms := newFakeMemberStore()
	ms.workspaceMembers[wmKey("ws-1", "caller-1")] = domain.WorkspaceMember{
		WorkspaceID: "ws-1", UserID: "caller-1", Role: role, Status: domain.MemberStatusActive,
	}
	ms.roster = storage.ChannelRosterPage{
		Members: []domain.ChannelMemberProfile{
			{UserID: "user-a", DisplayName: "Ana", Role: domain.ChannelRoleMember},
			{UserID: "user-b", DisplayName: "Bruno", Role: domain.ChannelRoleModerator},
		},
		TotalCount: 7,
	}
	return ms
}

func rosterChannelStore(isGeneral bool) *fakeChannelStore {
	return &fakeChannelStore{visibleChannel: domain.Channel{
		ID: "ch-1", WorkspaceID: "ws-1", Slug: "infra", DisplayName: "Infra",
		Type: domain.ChannelTypePrivate, Status: domain.ChannelStatusActive, IsGeneral: isGeneral,
	}}
}

func rosterInput() service.ChannelRosterInput {
	return service.ChannelRosterInput{
		WorkspaceID: "ws-1", CallerID: "caller-1", ChannelID: "ch-1",
		MemberLimit: domain.MaxChannelDetailsMembers,
	}
}

// A manager gets the membership itself, with the server's own total beside the
// page — never the page's length, which is capped.
func TestChannelService_ListChannelMembers_ReturnsMembershipAndTheServerTotal(t *testing.T) {
	ms := rosterMemberStore(domain.WorkspaceRoleAdmin)
	svc := service.NewChannelService(activeWorkspaceStore("ws-1"), rosterChannelStore(false), ms)

	roster, err := svc.ListChannelMembers(context.Background(), rosterInput())
	if err != nil {
		t.Fatalf("ListChannelMembers: %v", err)
	}
	if roster.MemberCount != 7 {
		t.Fatalf("MemberCount = %d, want the store's total 7", roster.MemberCount)
	}
	if len(roster.Members) != 2 || roster.Members[0].UserID != "user-a" {
		t.Fatalf("Members = %+v", roster.Members)
	}
	if len(ms.rosterCalls) != 1 {
		t.Fatalf("roster queries = %d, want exactly one", len(ms.rosterCalls))
	}
	call := ms.rosterCalls[0]
	if call.workspaceID != "ws-1" || call.channelID != "ch-1" {
		t.Fatalf("roster call = %+v, want the session workspace and the resolved channel", call)
	}
}

// The gate is the removal policy, not read access: a roster answered to someone
// who could not remove anybody would publish the membership list for nothing.
func TestChannelService_ListChannelMembers_AllowsEveryActiveVisibleMember(t *testing.T) {
	for _, role := range []domain.WorkspaceRole{
		domain.WorkspaceRoleOwner, domain.WorkspaceRoleAdmin, domain.WorkspaceRoleModerator,
		domain.WorkspaceRoleMember, domain.WorkspaceRoleGuest, domain.WorkspaceRole("wizard"),
	} {
		t.Run(string(role), func(t *testing.T) {
			ms := rosterMemberStore(role)
			svc := service.NewChannelService(activeWorkspaceStore("ws-1"), rosterChannelStore(false), ms)

			_, err := svc.ListChannelMembers(context.Background(), rosterInput())
			if err != nil {
				t.Fatalf("active %s was refused: %v", role, err)
			}
			if len(ms.rosterCalls) != 1 {
				t.Fatal("visible member did not reach roster query")
			}
		})
	}
}

// Authority is settled before the channel is looked up, so this route cannot be
// used to probe which channel UUIDs exist.
func TestChannelService_ListChannelMembers_ResolvesVisibleChannelForMembers(t *testing.T) {
	ms := rosterMemberStore(domain.WorkspaceRoleMember)
	channels := rosterChannelStore(false)
	svc := service.NewChannelService(activeWorkspaceStore("ws-1"), channels, ms)

	if _, err := svc.ListChannelMembers(context.Background(), rosterInput()); err != nil {
		t.Fatalf("err = %v", err)
	}
	if channels.getVisibleByIDCalls != 1 {
		t.Fatalf("channel lookups = %d, want one", channels.getVisibleByIDCalls)
	}
}

// #geral's membership belongs to the workspace sync, and the removal refuses
// it — so there is nothing here to administer and no roster to hand over.
func TestChannelService_ListChannelMembers_IncludesTheGeneralChannel(t *testing.T) {
	ms := rosterMemberStore(domain.WorkspaceRoleOwner)
	svc := service.NewChannelService(activeWorkspaceStore("ws-1"), rosterChannelStore(true), ms)

	if _, err := svc.ListChannelMembers(context.Background(), rosterInput()); err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(ms.rosterCalls) != 1 {
		t.Fatal("#geral did not reach roster query")
	}
}

// A channel the caller cannot see keeps the uniform not-found the visibility
// predicate produces, even for a manager.
func TestChannelService_ListChannelMembers_PropagatesTheVisibilityDenial(t *testing.T) {
	ms := rosterMemberStore(domain.WorkspaceRoleAdmin)
	channels := &fakeChannelStore{getVisibleErr: domain.ErrNotFound}
	svc := service.NewChannelService(activeWorkspaceStore("ws-1"), channels, ms)

	if _, err := svc.ListChannelMembers(context.Background(), rosterInput()); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if len(ms.rosterCalls) != 0 {
		t.Fatal("an invisible channel reached the roster query")
	}
}

// A caller who is not an active member of the workspace never reaches the
// authority check, let alone the roster.
func TestChannelService_ListChannelMembers_RefusesANonMemberOfTheWorkspace(t *testing.T) {
	ms := newFakeMemberStore()
	svc := service.NewChannelService(activeWorkspaceStore("ws-1"), rosterChannelStore(false), ms)

	if _, err := svc.ListChannelMembers(context.Background(), rosterInput()); err == nil {
		t.Fatal("a caller with no workspace membership got a roster")
	}
}

// The store's failure is a failure, never an empty roster: an empty list would
// read on screen as "this channel has nobody in it".
func TestChannelService_ListChannelMembers_PropagatesTheStoreFailure(t *testing.T) {
	ms := rosterMemberStore(domain.WorkspaceRoleAdmin)
	ms.rosterErr = errors.New("boom")
	svc := service.NewChannelService(activeWorkspaceStore("ws-1"), rosterChannelStore(false), ms)

	if _, err := svc.ListChannelMembers(context.Background(), rosterInput()); err == nil {
		t.Fatal("a store failure was reported as a roster")
	}
}

// The removal capability is its own answer in the details payload (issue #469).
// It must track the removal's policy — never be inferred from can_manage_members
// by the client, and never be true where the write path refuses.
func TestChannelService_GetChannelDetails_ReportsTheRemovalCapability(t *testing.T) {
	for _, test := range []struct {
		name      string
		role      domain.WorkspaceRole
		isGeneral bool
		want      bool
	}{
		{name: "admin in an ordinary channel", role: domain.WorkspaceRoleAdmin, want: true},
		{name: "moderator in an ordinary channel", role: domain.WorkspaceRoleModerator, want: true},
		{name: "member in an ordinary channel", role: domain.WorkspaceRoleMember, want: false},
		{name: "admin in #geral", role: domain.WorkspaceRoleAdmin, isGeneral: true, want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			ms := newFakeMemberStore()
			ms.workspaceMembers[wmKey("ws-1", "user-1")] = domain.WorkspaceMember{
				WorkspaceID: "ws-1", UserID: "user-1", Role: test.role, Status: domain.MemberStatusActive,
			}
			channels := rosterChannelStore(test.isGeneral)
			svc := service.NewChannelService(activeWorkspaceStore("ws-1"), channels, ms)

			details, err := svc.GetChannelDetails(context.Background(), service.ChannelDetailsInput{
				WorkspaceID: "ws-1", CallerID: "user-1", ChannelID: "ch-1",
			})
			if err != nil {
				t.Fatalf("GetChannelDetails: %v", err)
			}
			if details.CanRemoveMembers != test.want {
				t.Fatalf("CanRemoveMembers = %v, want %v", details.CanRemoveMembers, test.want)
			}
		})
	}
}
