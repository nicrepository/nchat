package service_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
)

// Group rename, group self-leave and channel self-leave, at the service layer
// (issue #527).
//
// The service's job here is narrow and worth pinning down: canonicalise the
// actor, validate the title, and hand the decision to the store — which takes
// the real one inside its transaction. What these assert is that nothing is
// invented on the way (no target user, no actor from the payload) and that a
// request the service can already tell is malformed never reaches the database.

const (
	adminCaller = "00000000-0000-4000-8000-0000000000a1"
	adminWSID   = "ws-1"
	adminGroup  = "dm-1"
)

func TestDMService_RenameGroup_ForwardsTheNormalisedTitleAndCanonicalActor(t *testing.T) {
	dms := &fakeDMStore{}
	svc := service.NewDMService(dms, &fakeMemberStore{})

	result, err := svc.RenameGroup(context.Background(), service.RenameGroupInput{
		WorkspaceID:    adminWSID,
		ConversationID: adminGroup,
		CallerID:       strings.ToUpper(adminCaller),
		Title:          "  Piloto NChat  ",
	})
	if err != nil {
		t.Fatalf("RenameGroup: %v", err)
	}
	if dms.lastRenameGroup.Title != "Piloto NChat" {
		t.Fatalf("title = %q, want it trimmed", dms.lastRenameGroup.Title)
	}
	// The actor is canonicalised rather than passed through, so the same person
	// is one identity for the row lock the store takes.
	if dms.lastRenameGroup.CallerID != adminCaller {
		t.Fatalf("caller = %q, want the canonical form", dms.lastRenameGroup.CallerID)
	}
	if result.Conversation.Title != "Piloto NChat" || result.Event.Kind != domain.MessageKindSystem {
		t.Fatalf("result = %+v, want the renamed conversation and its event", result)
	}
}

// A rename to nothing is not a name — it is a request with no content, and
// silently keeping the old title would leave the dialog showing a change that
// never happened.
func TestDMService_RenameGroup_RejectsMalformedRequestsBeforeTheStore(t *testing.T) {
	for _, test := range []struct {
		name  string
		input service.RenameGroupInput
	}{
		{
			name:  "no workspace",
			input: service.RenameGroupInput{ConversationID: adminGroup, CallerID: adminCaller, Title: "x"},
		},
		{
			name:  "no conversation",
			input: service.RenameGroupInput{WorkspaceID: adminWSID, CallerID: adminCaller, Title: "x"},
		},
		{
			name:  "blank title",
			input: service.RenameGroupInput{WorkspaceID: adminWSID, ConversationID: adminGroup, CallerID: adminCaller, Title: "   "},
		},
		{
			name: "title over the cap",
			input: service.RenameGroupInput{
				WorkspaceID: adminWSID, ConversationID: adminGroup, CallerID: adminCaller,
				// The same cap creation enforces: a rename must not be the way past it.
				Title: strings.Repeat("á", 121),
			},
		},
		{
			name:  "actor that is not a user id",
			input: service.RenameGroupInput{WorkspaceID: adminWSID, ConversationID: adminGroup, CallerID: "not-a-uuid", Title: "x"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			dms := &fakeDMStore{}
			_, err := service.NewDMService(dms, &fakeMemberStore{}).RenameGroup(context.Background(), test.input)
			if err == nil {
				t.Fatal("expected the request to be refused")
			}
			if dms.lastRenameGroup.ConversationID != "" {
				t.Fatalf("the store was reached with %+v", dms.lastRenameGroup)
			}
		})
	}
}

// A title exactly at the cap is accepted: the boundary belongs to the valid side.
func TestDMService_RenameGroup_AcceptsATitleAtTheCap(t *testing.T) {
	dms := &fakeDMStore{}
	title := strings.Repeat("á", 120)

	if _, err := service.NewDMService(dms, &fakeMemberStore{}).RenameGroup(
		context.Background(), service.RenameGroupInput{
			WorkspaceID: adminWSID, ConversationID: adminGroup, CallerID: adminCaller, Title: title,
		}); err != nil {
		t.Fatalf("RenameGroup: %v", err)
	}
	if dms.lastRenameGroup.Title != title {
		t.Fatal("a title at the cap must be forwarded unchanged")
	}
}

func TestDMService_RenameGroup_PropagatesTheStoreRefusal(t *testing.T) {
	dms := &fakeDMStore{renameGroupErr: domain.ErrForbidden}

	_, err := service.NewDMService(dms, &fakeMemberStore{}).RenameGroup(
		context.Background(), service.RenameGroupInput{
			WorkspaceID: adminWSID, ConversationID: adminGroup, CallerID: adminCaller, Title: "Piloto",
		})
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("error = %v, want the store's ErrForbidden", err)
	}
}

// Self-leave carries no target user, here or in the store's statement: the row
// it updates is the caller's own, which is what makes it impossible to turn into
// "remove that person".
func TestDMService_LeaveGroup_ForwardsOnlyTheCallersOwnIdentity(t *testing.T) {
	dms := &fakeDMStore{}

	result, err := service.NewDMService(dms, &fakeMemberStore{}).LeaveGroup(
		context.Background(), service.LeaveGroupInput{
			WorkspaceID: adminWSID, ConversationID: adminGroup, CallerID: strings.ToUpper(adminCaller),
		})
	if err != nil {
		t.Fatalf("LeaveGroup: %v", err)
	}
	if dms.lastLeaveGroup != [3]string{adminWSID, adminGroup, adminCaller} {
		t.Fatalf("forwarded %v, want the workspace, the conversation and the canonical caller", dms.lastLeaveGroup)
	}
	if result.Event.Kind != domain.MessageKindSystem {
		t.Fatalf("event = %+v, want the departure's system message", result.Event)
	}
}

func TestDMService_LeaveGroup_RejectsMalformedRequestsBeforeTheStore(t *testing.T) {
	for _, test := range []struct {
		name  string
		input service.LeaveGroupInput
	}{
		{name: "no workspace", input: service.LeaveGroupInput{ConversationID: adminGroup, CallerID: adminCaller}},
		{name: "no conversation", input: service.LeaveGroupInput{WorkspaceID: adminWSID, CallerID: adminCaller}},
		{name: "no actor", input: service.LeaveGroupInput{WorkspaceID: adminWSID, ConversationID: adminGroup}},
	} {
		t.Run(test.name, func(t *testing.T) {
			dms := &fakeDMStore{}
			if _, err := service.NewDMService(dms, &fakeMemberStore{}).LeaveGroup(
				context.Background(), test.input); err == nil {
				t.Fatal("expected the request to be refused")
			}
			if dms.lastLeaveGroup != [3]string{} {
				t.Fatalf("the store was reached with %v", dms.lastLeaveGroup)
			}
		})
	}
}

func TestDMService_LeaveGroup_PropagatesTheStoreRefusal(t *testing.T) {
	dms := &fakeDMStore{leaveGroupErr: domain.ErrNotFound}

	_, err := service.NewDMService(dms, &fakeMemberStore{}).LeaveGroup(
		context.Background(), service.LeaveGroupInput{
			WorkspaceID: adminWSID, ConversationID: adminGroup, CallerID: adminCaller,
		})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("error = %v, want the store's ErrNotFound", err)
	}
}

// ── Channel self-leave ──────────────────────────────────────────────────────
//
// Two structural refusals, and both are re-derived in SQL rather than trusted
// from here: the general channel cannot be left, and a channel outside this
// workspace does not exist as far as this caller is concerned. The check at this
// layer is the fail-fast that produces a legible error.

func leavableChannelService(channel domain.Channel, member domain.WorkspaceMember) (*service.ChannelService, *fakeChannelStore) {
	channels := &fakeChannelStore{visibleChannel: channel}
	members := &fakeMemberStore{workspaceMembers: map[string]domain.WorkspaceMember{
		wmKey(adminWSID, adminCaller): member,
	}}
	return service.NewChannelService(activeWorkspaceStore(adminWSID), channels, members), channels
}

func activeChannelMember() domain.WorkspaceMember {
	return domain.WorkspaceMember{
		WorkspaceID: adminWSID, UserID: adminCaller,
		Role: domain.WorkspaceRoleMember, Status: domain.MemberStatusActive,
	}
}

func ordinaryChannel() domain.Channel {
	return domain.Channel{
		ID: "chan-1", WorkspaceID: adminWSID, Slug: "infra", DisplayName: "Infra",
		Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive,
	}
}

// Leaving takes no management role: an ordinary member removes their own
// membership, and nobody else's.
func TestChannelService_LeaveChannel_ForwardsTheActorsOwnDeparture(t *testing.T) {
	svc, channels := leavableChannelService(ordinaryChannel(), activeChannelMember())

	result, err := svc.LeaveChannel(context.Background(), adminWSID, "chan-1", adminCaller)
	if err != nil {
		t.Fatalf("LeaveChannel: %v", err)
	}
	if len(channels.leftChannels) != 1 || channels.leftChannels[0] != [3]string{adminWSID, "chan-1", adminCaller} {
		t.Fatalf("forwarded %v, want the workspace, the channel and the caller", channels.leftChannels)
	}
	if result.Event.Kind != domain.MessageKindSystem {
		t.Fatalf("event = %+v, want the departure's system message", result.Event)
	}
}

// Membership of the general channel is owned by the workspace sync, not by the
// person, so it is refused before the store is reached — and with the single
// error every structural refusal on #geral uses.
func TestChannelService_LeaveChannel_RefusesTheGeneralChannel(t *testing.T) {
	general := ordinaryChannel()
	general.IsGeneral = true
	svc, channels := leavableChannelService(general, activeChannelMember())

	_, err := svc.LeaveChannel(context.Background(), adminWSID, "chan-1", adminCaller)
	if !errors.Is(err, domain.ErrGeneralChannelImmutable) {
		t.Fatalf("error = %v, want ErrGeneralChannelImmutable", err)
	}
	if len(channels.leftChannels) != 0 {
		t.Fatalf("the store was reached with %v", channels.leftChannels)
	}
}

// A caller with no active membership in the workspace, and a channel they cannot
// see, are both refused before any write — and neither answer describes state
// the caller is not entitled to.
func TestChannelService_LeaveChannel_RefusesWhatTheCallerCannotReach(t *testing.T) {
	t.Run("not an active workspace member", func(t *testing.T) {
		channels := &fakeChannelStore{visibleChannel: ordinaryChannel()}
		members := &fakeMemberStore{}
		svc := service.NewChannelService(activeWorkspaceStore(adminWSID), channels, members)

		if _, err := svc.LeaveChannel(context.Background(), adminWSID, "chan-1", adminCaller); !errors.Is(err, domain.ErrForbidden) {
			t.Fatalf("error = %v, want ErrForbidden", err)
		}
		if len(channels.leftChannels) != 0 {
			t.Fatal("the store was reached by a caller with no standing")
		}
	})

	t.Run("channel not visible", func(t *testing.T) {
		channels := &fakeChannelStore{getVisibleErr: domain.ErrNotFound}
		members := &fakeMemberStore{workspaceMembers: map[string]domain.WorkspaceMember{
			wmKey(adminWSID, adminCaller): activeChannelMember(),
		}}
		svc := service.NewChannelService(activeWorkspaceStore(adminWSID), channels, members)

		if _, err := svc.LeaveChannel(context.Background(), adminWSID, "chan-1", adminCaller); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("error = %v, want ErrNotFound", err)
		}
		if len(channels.leftChannels) != 0 {
			t.Fatal("the store was reached for a channel the caller cannot see")
		}
	})
}

func TestChannelService_LeaveChannel_PropagatesTheStoreRefusal(t *testing.T) {
	svc, channels := leavableChannelService(ordinaryChannel(), activeChannelMember())
	channels.leaveErr = domain.ErrNotFound

	if _, err := svc.LeaveChannel(context.Background(), adminWSID, "chan-1", adminCaller); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("error = %v, want the store's ErrNotFound", err)
	}
}
