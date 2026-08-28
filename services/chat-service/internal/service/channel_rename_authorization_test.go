package service_test

import (
	"context"
	"errors"
	"testing"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
)

// Renaming a channel is a management operation (issue #527): it goes through the
// same requireManagePermission -> domain.CanManageWorkspace predicate that every
// other channel update and the archival do.
//
// The moderator row is the point of this table. RF-74 introduced a workspace
// moderator and deliberately did *not* add it to CanManageWorkspace: a moderator
// moderates channel structure and membership, while changing what a channel *is*
// remains administration. Renaming through the sidebar menu must not become the
// route that widens it.
func TestChannelService_UpdateChannel_RenameFollowsCanManageWorkspace(t *testing.T) {
	active := func(role domain.WorkspaceRole) domain.WorkspaceMember {
		return domain.WorkspaceMember{
			WorkspaceID: "ws-1", UserID: "actor-1", Role: role, Status: domain.MemberStatusActive,
		}
	}

	for _, test := range []struct {
		name    string
		member  domain.WorkspaceMember
		allowed bool
	}{
		{name: "owner", member: active(domain.WorkspaceRoleOwner), allowed: true},
		{name: "admin", member: active(domain.WorkspaceRoleAdmin), allowed: true},
		{name: "moderator", member: active(domain.WorkspaceRoleModerator)},
		{name: "member", member: active(domain.WorkspaceRoleMember)},
		{name: "guest", member: active(domain.WorkspaceRoleGuest)},
		{name: "suspended admin", member: domain.WorkspaceMember{
			WorkspaceID: "ws-1", UserID: "actor-1",
			Role: domain.WorkspaceRoleAdmin, Status: domain.MemberStatusSuspended,
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			ms := newFakeMemberStore()
			ms.workspaceMembers[wmKey("ws-1", "actor-1")] = test.member
			channels := &fakeChannelStore{channel: domain.Channel{
				ID: "ch-1", WorkspaceID: "ws-1", Slug: "infra", DisplayName: "Infra",
				Type: domain.ChannelTypePrivate, Status: domain.ChannelStatusActive,
			}}
			svc := service.NewChannelService(activeWorkspaceStore("ws-1"), channels, ms)

			updated, err := svc.UpdateChannel(context.Background(), service.UpdateChannelInput{
				WorkspaceID: "ws-1", CallerID: "actor-1", ChannelID: "ch-1", DisplayName: "Plataforma",
			})
			if !test.allowed {
				if !errors.Is(err, domain.ErrForbidden) {
					t.Fatalf("error = %v, want ErrForbidden", err)
				}
				if channels.lastUpdateInput.ChannelID != "" {
					t.Fatalf("a refused caller reached storage: %+v", channels.lastUpdateInput)
				}
				return
			}
			if err != nil {
				t.Fatalf("UpdateChannel: %v", err)
			}
			// The actor is threaded down to the store, which re-derives the very
			// same role inside the write transaction (issue #527 security
			// review). What travels is an identity, never a decision: a boolean
			// computed here is exactly what a concurrent revocation would make
			// stale.
			if stored := channels.lastUpdateInput; stored.CallerID != "actor-1" {
				t.Fatalf("store received CallerID %q, want actor-1", stored.CallerID)
			}
			if updated.Channel.ID != "ch-1" {
				t.Fatalf("channel id = %q, want the unchanged ch-1", updated.Channel.ID)
			}
			// The rename and its system message are one transaction, so a
			// successful rename always comes back with the event (issue #527).
			if updated.Event.ID == "" || updated.Event.Kind != domain.MessageKindSystem {
				t.Fatalf("event = %+v, want the system message the rename wrote", updated.Event)
			}
			// A rename touches the name and nothing else: the slug, the type and
			// the category are carried through from the current row, so the
			// channel's address, visibility and placement survive it.
			stored := channels.lastUpdateInput
			if stored.Slug != "infra" || stored.Type != domain.ChannelTypePrivate {
				t.Fatalf("rename changed more than the name: %+v", stored)
			}
			if stored.DisplayName != "Plataforma" {
				t.Fatalf("stored display name = %q, want Plataforma", stored.DisplayName)
			}
		})
	}
}

// A channel ID from another workspace is ErrNotFound, and it is refused *after*
// authorization: an admin of ws-1 aiming at a channel of ws-2 learns nothing
// about whether that ID exists, and a non-manager never gets that far at all.
func TestChannelService_UpdateChannel_RenameIsWorkspaceBound(t *testing.T) {
	ms := newFakeMemberStore()
	ms.workspaceMembers[wmKey("ws-1", "admin-1")] = domain.WorkspaceMember{
		WorkspaceID: "ws-1", UserID: "admin-1", Role: domain.WorkspaceRoleAdmin, Status: domain.MemberStatusActive,
	}
	// The channel exists — in a workspace this caller does not administer.
	channels := &fakeChannelStore{channel: domain.Channel{
		ID: "ch-outra", WorkspaceID: "ws-2", Slug: "infra", DisplayName: "Infra",
		Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive,
	}}
	svc := service.NewChannelService(activeWorkspaceStore("ws-1"), channels, ms)

	if _, err := svc.UpdateChannel(context.Background(), service.UpdateChannelInput{
		WorkspaceID: "ws-1", CallerID: "admin-1", ChannelID: "ch-outra", DisplayName: "Plataforma",
	}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
	if channels.lastUpdateInput.ChannelID != "" {
		t.Fatalf("a cross-workspace rename reached storage: %+v", channels.lastUpdateInput)
	}
}

// An arbitrary ID nobody issued is the same answer as a channel in another
// workspace, so the route cannot be used to enumerate which UUIDs exist.
func TestChannelService_UpdateChannel_RenameUnknownChannelIsNotFound(t *testing.T) {
	ms := newFakeMemberStore()
	ms.workspaceMembers[wmKey("ws-1", "owner-1")] = domain.WorkspaceMember{
		WorkspaceID: "ws-1", UserID: "owner-1", Role: domain.WorkspaceRoleOwner, Status: domain.MemberStatusActive,
	}
	channels := &fakeChannelStore{}
	svc := service.NewChannelService(activeWorkspaceStore("ws-1"), channels, ms)

	if _, err := svc.UpdateChannel(context.Background(), service.UpdateChannelInput{
		WorkspaceID: "ws-1", CallerID: "owner-1", ChannelID: "ch-inexistente", DisplayName: "Plataforma",
	}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
}
