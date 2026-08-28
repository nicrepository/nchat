package service_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

func detailsMemberStore() *fakeMemberStore {
	ms := newFakeMemberStore()
	ms.workspaceMembers[wmKey("ws-1", "user-1")] = domain.WorkspaceMember{
		WorkspaceID: "ws-1", UserID: "user-1", Role: domain.WorkspaceRoleMember, Status: domain.MemberStatusActive,
	}
	return ms
}

func detailsChannelStore() *fakeChannelStore {
	return &fakeChannelStore{visibleChannel: domain.Channel{
		ID: "ch-1", WorkspaceID: "ws-1", Slug: "infra", DisplayName: "Infra",
		Type: domain.ChannelTypePrivate, Status: domain.ChannelStatusActive,
		CreatedAt: time.Date(2024, 1, 12, 9, 30, 0, 0, time.UTC),
	}}
}

func detailsInput(onlineUserIDs []string, limit int) service.ChannelDetailsInput {
	return service.ChannelDetailsInput{
		WorkspaceID:   "ws-1",
		CallerID:      "user-1",
		ChannelID:     "ch-1",
		OnlineUserIDs: onlineUserIDs,
		MemberLimit:   limit,
	}
}

// rosterOf builds a channel roster of size n whose display names sort in the
// order they are generated, so "the first N alphabetically" is unambiguous.
func rosterOf(n int) []domain.ChannelMemberProfile {
	members := make([]domain.ChannelMemberProfile, 0, n)
	for i := 0; i < n; i++ {
		members = append(members, domain.ChannelMemberProfile{
			UserID:      fmt.Sprintf("user-%03d", i),
			DisplayName: fmt.Sprintf("Membro %03d", i),
			Role:        domain.ChannelRoleMember,
		})
	}
	return members
}

// TestChannelService_GetChannelDetails_ShowsAnOnlineMemberPastTheAlphabeticalCut
// is the regression test for the reported defect: a channel with more members
// than the preview holds, where the only online member sorts last.
//
// Before the fix the preview was cut to the first MaxChannelDetailsMembers by
// name and presence was applied afterwards, so this member could never appear.
func TestChannelService_GetChannelDetails_ShowsAnOnlineMemberPastTheAlphabeticalCut(t *testing.T) {
	roster := rosterOf(domain.MaxChannelDetailsMembers + 1)
	lastMember := roster[len(roster)-1]

	ms := detailsMemberStore()
	ms.memberProfiles = storage.ChannelMemberPage{
		Online:     roster,
		TotalCount: len(roster),
	}

	// Only the member who sorts last of all 31 is online.
	got, err := service.NewChannelService(activeWorkspaceStore("ws-1"), detailsChannelStore(), ms).
		GetChannelDetails(context.Background(), detailsInput(
			[]string{lastMember.UserID}, domain.MaxChannelDetailsMembers,
		))
	if err != nil {
		t.Fatalf("GetChannelDetails: %v", err)
	}

	if len(got.OnlineMembers) != 1 || got.OnlineMembers[0].UserID != lastMember.UserID {
		t.Fatalf("the only online member must be the one returned, got %+v", got.OnlineMembers)
	}
	if got.OnlineCount != 1 {
		t.Fatalf("expected 1 online, got %d", got.OnlineCount)
	}
	// The channel's size is unaffected by how many of its members are online.
	if got.MemberCount != domain.MaxChannelDetailsMembers+1 {
		t.Fatalf("expected the full member total, got %d", got.MemberCount)
	}

	// The presence snapshot must have reached the store as a filter, which is
	// what makes the row selection happen before the limit.
	if len(ms.memberProfileCalls) != 1 {
		t.Fatalf("expected exactly one member query, got %d", len(ms.memberProfileCalls))
	}
	call := ms.memberProfileCalls[0]
	if len(call.onlineUserIDs) != 1 || call.onlineUserIDs[0] != lastMember.UserID {
		t.Fatalf("the presence snapshot must reach the store, got %v", call.onlineUserIDs)
	}
	if call.limit != domain.MaxChannelDetailsMembers {
		t.Fatalf("expected the server-side cap, got %d", call.limit)
	}
}

func TestChannelService_GetChannelDetails_OfflineMembersNeverConsumeAPreviewSlot(t *testing.T) {
	roster := rosterOf(domain.MaxChannelDetailsMembers + 5)
	// The five members who sort last are the online ones; everyone ahead of them
	// is offline and must not take a slot.
	online := make([]string, 0, 5)
	for _, member := range roster[len(roster)-5:] {
		online = append(online, member.UserID)
	}

	ms := detailsMemberStore()
	ms.memberProfiles = storage.ChannelMemberPage{Online: roster, TotalCount: len(roster)}

	got, err := service.NewChannelService(activeWorkspaceStore("ws-1"), detailsChannelStore(), ms).
		GetChannelDetails(context.Background(), detailsInput(online, domain.MaxChannelDetailsMembers))
	if err != nil {
		t.Fatalf("GetChannelDetails: %v", err)
	}
	if len(got.OnlineMembers) != 5 {
		t.Fatalf("expected all five online members, got %d", len(got.OnlineMembers))
	}
	for _, member := range got.OnlineMembers {
		if member.UserID < roster[len(roster)-5].UserID {
			t.Fatalf("an offline member reached the preview: %+v", member)
		}
	}
}

func TestChannelService_GetChannelDetails_CapsTheOnlinePreviewButNotTheCounts(t *testing.T) {
	roster := rosterOf(domain.MaxChannelDetailsMembers + 10)
	online := make([]string, 0, len(roster))
	for _, member := range roster {
		online = append(online, member.UserID)
	}

	ms := detailsMemberStore()
	ms.memberProfiles = storage.ChannelMemberPage{Online: roster, TotalCount: len(roster)}

	got, err := service.NewChannelService(activeWorkspaceStore("ws-1"), detailsChannelStore(), ms).
		GetChannelDetails(context.Background(), detailsInput(online, domain.MaxChannelDetailsMembers))
	if err != nil {
		t.Fatalf("GetChannelDetails: %v", err)
	}
	if len(got.OnlineMembers) != domain.MaxChannelDetailsMembers {
		t.Fatalf("expected exactly the cap, got %d", len(got.OnlineMembers))
	}
	// Both counts describe the whole set, never the truncated preview.
	if got.OnlineCount != len(roster) || got.MemberCount != len(roster) {
		t.Fatalf("counts must not be truncated: online %d, total %d", got.OnlineCount, got.MemberCount)
	}
	// Deterministic order: the preview is the alphabetically first slice of the
	// online set, and the same input always yields the same page.
	if got.OnlineMembers[0].UserID != roster[0].UserID {
		t.Fatalf("expected a deterministic ordered page, got %+v", got.OnlineMembers[0])
	}
}

func TestChannelService_GetChannelDetails_ReturnsNoMembersWhenNobodyIsOnline(t *testing.T) {
	roster := rosterOf(12)
	ms := detailsMemberStore()
	ms.memberProfiles = storage.ChannelMemberPage{Online: roster, TotalCount: len(roster)}

	got, err := service.NewChannelService(activeWorkspaceStore("ws-1"), detailsChannelStore(), ms).
		GetChannelDetails(context.Background(), detailsInput(nil, domain.MaxChannelDetailsMembers))
	if err != nil {
		t.Fatalf("GetChannelDetails: %v", err)
	}
	if len(got.OnlineMembers) != 0 || got.OnlineCount != 0 {
		t.Fatalf("an empty presence snapshot must yield no online members, got %+v", got.OnlineMembers)
	}
	// Nobody online does not mean the channel has no members.
	if got.MemberCount != 12 {
		t.Fatalf("expected the member total to survive, got %d", got.MemberCount)
	}
}

func TestChannelService_GetChannelDetails_IgnoresOnlineUsersOutsideTheChannel(t *testing.T) {
	roster := rosterOf(3)
	ms := detailsMemberStore()
	ms.memberProfiles = storage.ChannelMemberPage{Online: roster, TotalCount: len(roster)}

	// A presence snapshot naturally covers the whole workspace: users of other
	// channels are in it, and the roster intersection is what excludes them.
	got, err := service.NewChannelService(activeWorkspaceStore("ws-1"), detailsChannelStore(), ms).
		GetChannelDetails(context.Background(), detailsInput(
			[]string{roster[0].UserID, "user-from-another-channel", "user-from-another-workspace"},
			domain.MaxChannelDetailsMembers,
		))
	if err != nil {
		t.Fatalf("GetChannelDetails: %v", err)
	}
	if len(got.OnlineMembers) != 1 || got.OnlineMembers[0].UserID != roster[0].UserID {
		t.Fatalf("only members of this channel may appear, got %+v", got.OnlineMembers)
	}
}

func TestChannelService_GetChannelDetails_ReturnsTheVisibleChannel(t *testing.T) {
	ms := detailsMemberStore()
	ms.memberProfiles = storage.ChannelMemberPage{
		Online: []domain.ChannelMemberProfile{
			{UserID: "user-1", DisplayName: "Álvaro", Role: domain.ChannelRoleModerator},
		},
		TotalCount: 42,
	}

	got, err := service.NewChannelService(activeWorkspaceStore("ws-1"), detailsChannelStore(), ms).
		GetChannelDetails(context.Background(), detailsInput([]string{"user-1"}, 5))
	if err != nil {
		t.Fatalf("GetChannelDetails: %v", err)
	}
	if got.Channel.ID != "ch-1" || got.Channel.Type != domain.ChannelTypePrivate {
		t.Fatalf("unexpected channel: %+v", got.Channel)
	}
	if got.MemberCount != 42 {
		t.Fatalf("member count must come from the store, got %d", got.MemberCount)
	}
	if len(got.OnlineMembers) != 1 || got.OnlineMembers[0].Role != domain.ChannelRoleModerator {
		t.Fatalf("unexpected members: %+v", got.OnlineMembers)
	}
	call := ms.memberProfileCalls[0]
	if call.workspaceID != "ws-1" || call.channelID != "ch-1" || call.limit != 5 {
		t.Fatalf("member query must be scoped to the resolved channel: %+v", call)
	}
}

func TestChannelService_GetChannelDetails_ReportsAddCapability(t *testing.T) {
	for _, test := range []struct {
		role      domain.WorkspaceRole
		isGeneral bool
		want      bool
	}{
		{domain.WorkspaceRoleOwner, false, true},
		{domain.WorkspaceRoleAdmin, false, true},
		{domain.WorkspaceRoleModerator, false, true},
		{domain.WorkspaceRoleMember, false, true},
		{domain.WorkspaceRoleGuest, false, false},
		{domain.WorkspaceRoleMember, true, false},
	} {
		t.Run(fmt.Sprintf("%s/general=%v", test.role, test.isGeneral), func(t *testing.T) {
			ms := detailsMemberStore()
			member := ms.workspaceMembers[wmKey("ws-1", "user-1")]
			member.Role = test.role
			ms.workspaceMembers[wmKey("ws-1", "user-1")] = member
			channels := detailsChannelStore()
			channels.visibleChannel.IsGeneral = test.isGeneral

			got, err := service.NewChannelService(activeWorkspaceStore("ws-1"), channels, ms).
				GetChannelDetails(context.Background(), detailsInput(nil, 1))
			if err != nil {
				t.Fatalf("GetChannelDetails: %v", err)
			}
			if got.CanManageMembers != test.want {
				t.Fatalf("CanManageMembers = %v, want %v", got.CanManageMembers, test.want)
			}
		})
	}
}

func TestChannelService_GetChannelDetails_RefusesNonMembersBeforeReadingMembers(t *testing.T) {
	// No workspace membership seeded: the caller has no business here at all.
	ms := newFakeMemberStore()
	channels := detailsChannelStore()

	_, err := service.NewChannelService(activeWorkspaceStore("ws-1"), channels, ms).
		GetChannelDetails(context.Background(), service.ChannelDetailsInput{
			WorkspaceID: "ws-1", CallerID: "stranger", ChannelID: "ch-1",
			OnlineUserIDs: []string{"user-000"},
		})
	if err == nil {
		t.Fatal("expected a denial for a non-member")
	}
	if channels.getVisibleByIDCalls != 0 || len(ms.memberProfileCalls) != 0 {
		t.Fatal("a denied caller must never reach the channel or its roster")
	}
}

func TestChannelService_GetChannelDetails_PropagatesInvisibleChannelAsNotFound(t *testing.T) {
	ms := detailsMemberStore()
	channels := &fakeChannelStore{getVisibleErr: domain.ErrNotFound}

	_, err := service.NewChannelService(activeWorkspaceStore("ws-1"), channels, ms).
		GetChannelDetails(context.Background(), service.ChannelDetailsInput{
			WorkspaceID: "ws-1", CallerID: "user-1", ChannelID: "ch-hidden",
			OnlineUserIDs: []string{"user-000"},
		})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if len(ms.memberProfileCalls) != 0 {
		t.Fatal("an invisible channel must never have its members read")
	}
}

func TestChannelService_GetChannelDetails_SurfacesAMemberQueryFailure(t *testing.T) {
	ms := detailsMemberStore()
	ms.memberProfilesErr = errors.New("boom")

	_, err := service.NewChannelService(activeWorkspaceStore("ws-1"), detailsChannelStore(), ms).
		GetChannelDetails(context.Background(), detailsInput([]string{"user-000"}, 0))
	if err == nil {
		t.Fatal("expected the member query failure to surface")
	}
}
