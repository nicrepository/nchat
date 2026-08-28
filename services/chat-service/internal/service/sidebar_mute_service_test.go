package service_test

import (
	"context"
	"errors"
	"testing"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// Muting a conversation, at the service layer (issue #527).
//
// The client supplies a target and nothing else. The workspace is resolved from
// the same server-side sidebar context GET uses — never taken from the request —
// and the store re-checks visibility and the general-channel invariant inside
// its own statement. What these assert is that first half: who the mute is
// attributed to, and what happens when the caller has no standing at all.

type fakeNotificationPrefStore struct {
	muted    []MuteCall
	unmuted  []MuteCall
	muteErr  error
	listed   []storage.MutedConversation
	listErr  error
	unmutErr error
}

// MuteCall is what the store was asked to change, recorded in order.
type MuteCall struct {
	WorkspaceID string
	UserID      string
	TargetType  string
	TargetID    string
}

func (f *fakeNotificationPrefStore) Mute(_ context.Context, workspaceID, userID, targetType, targetID string) error {
	f.muted = append(f.muted, MuteCall{workspaceID, userID, targetType, targetID})
	return f.muteErr
}

func (f *fakeNotificationPrefStore) Unmute(_ context.Context, userID, targetType, targetID string) error {
	f.unmuted = append(f.unmuted, MuteCall{UserID: userID, TargetType: targetType, TargetID: targetID})
	return f.unmutErr
}

func (f *fakeNotificationPrefStore) ListMuted(_ context.Context, _, _ string) ([]storage.MutedConversation, error) {
	return f.listed, f.listErr
}

func TestSidebarService_MuteConversation_AttributesTheMuteToTheResolvedWorkspace(t *testing.T) {
	notifs := &fakeNotificationPrefStore{}
	svc := newSidebarService(
		&sidebarFakeWorkspaceStore{workspace: activeWorkspace()},
		&sidebarFakeMemberStore{member: activeMember()},
		&sidebarFakeChannelStore{},
		&sidebarFakeDMStore{},
	).WithNotificationPrefs(notifs)

	if err := svc.MuteConversation(context.Background(), sidebarUserID, storage.NotificationPrefTargetChannel, "channel-1"); err != nil {
		t.Fatalf("MuteConversation: %v", err)
	}
	if len(notifs.muted) != 1 || notifs.muted[0] != (MuteCall{
		WorkspaceID: sidebarWsID, UserID: sidebarUserID,
		TargetType: storage.NotificationPrefTargetChannel, TargetID: "channel-1",
	}) {
		t.Fatalf("muted = %+v, want the resolved workspace and the caller's own id", notifs.muted)
	}
}

// Unmute is deliberately not workspace-scoped: a user must always be able to
// undo their own preference, so the store deletes by user and target alone.
func TestSidebarService_UnmuteConversation_UndoesTheCallersOwnPreference(t *testing.T) {
	notifs := &fakeNotificationPrefStore{}
	svc := newSidebarService(
		&sidebarFakeWorkspaceStore{workspace: activeWorkspace()},
		&sidebarFakeMemberStore{member: activeMember()},
		&sidebarFakeChannelStore{},
		&sidebarFakeDMStore{},
	).WithNotificationPrefs(notifs)

	if err := svc.UnmuteConversation(context.Background(), sidebarUserID, storage.NotificationPrefTargetDM, "dm-1"); err != nil {
		t.Fatalf("UnmuteConversation: %v", err)
	}
	if len(notifs.unmuted) != 1 || notifs.unmuted[0].UserID != sidebarUserID || notifs.unmuted[0].TargetID != "dm-1" {
		t.Fatalf("unmuted = %+v, want the caller's own row for that target", notifs.unmuted)
	}
}

// A caller with no standing in the workspace never reaches the preference store:
// the authorization runs first, and its refusal is the same one GET returns.
func TestSidebarService_Mute_RefusesACallerWithNoWorkspaceStanding(t *testing.T) {
	for _, test := range []struct {
		name    string
		members *sidebarFakeMemberStore
		wantErr error
	}{
		{name: "not a member", members: &sidebarFakeMemberStore{err: domain.ErrNotFound}, wantErr: domain.ErrForbidden},
		{
			name:    "suspended member",
			members: &sidebarFakeMemberStore{member: domain.WorkspaceMember{WorkspaceID: sidebarWsID, UserID: sidebarUserID, Status: domain.MemberStatusSuspended}},
			wantErr: domain.ErrForbidden,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			notifs := &fakeNotificationPrefStore{}
			svc := newSidebarService(
				&sidebarFakeWorkspaceStore{workspace: activeWorkspace()},
				test.members,
				&sidebarFakeChannelStore{},
				&sidebarFakeDMStore{},
			).WithNotificationPrefs(notifs)

			if err := svc.MuteConversation(context.Background(), sidebarUserID, storage.NotificationPrefTargetChannel, "channel-1"); !errors.Is(err, test.wantErr) {
				t.Fatalf("MuteConversation error = %v, want %v", err, test.wantErr)
			}
			if err := svc.UnmuteConversation(context.Background(), sidebarUserID, storage.NotificationPrefTargetChannel, "channel-1"); !errors.Is(err, test.wantErr) {
				t.Fatalf("UnmuteConversation error = %v, want %v", err, test.wantErr)
			}
			if len(notifs.muted) != 0 || len(notifs.unmuted) != 0 {
				t.Fatal("the preference store was reached by a caller with no standing")
			}
		})
	}
}

// The store's refusal — no such conversation, not visible, or the general
// channel — is the caller's answer unchanged.
func TestSidebarService_MuteConversation_PropagatesTheStoreRefusal(t *testing.T) {
	notifs := &fakeNotificationPrefStore{muteErr: domain.ErrNotFound}
	svc := newSidebarService(
		&sidebarFakeWorkspaceStore{workspace: activeWorkspace()},
		&sidebarFakeMemberStore{member: activeMember()},
		&sidebarFakeChannelStore{},
		&sidebarFakeDMStore{},
	).WithNotificationPrefs(notifs)

	if err := svc.MuteConversation(context.Background(), sidebarUserID, storage.NotificationPrefTargetChannel, "geral"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("error = %v, want the store's ErrNotFound", err)
	}
}

// A build without the optional store serves a sidebar with nothing muted rather
// than failing — but it must refuse a *write* instead of pretending it landed.
func TestSidebarService_MuteWithoutTheOptionalStore_IsRefusedRatherThanIgnored(t *testing.T) {
	svc := newSidebarService(
		&sidebarFakeWorkspaceStore{workspace: activeWorkspace()},
		&sidebarFakeMemberStore{member: activeMember()},
		&sidebarFakeChannelStore{},
		&sidebarFakeDMStore{},
	)

	if err := svc.MuteConversation(context.Background(), sidebarUserID, storage.NotificationPrefTargetChannel, "channel-1"); err == nil {
		t.Fatal("expected a mute without the store to fail")
	}
	if err := svc.UnmuteConversation(context.Background(), sidebarUserID, storage.NotificationPrefTargetChannel, "channel-1"); err == nil {
		t.Fatal("expected an unmute without the store to fail")
	}
}

// The sidebar itself still renders without the store: nothing is muted, and no
// request fails because an optional dependency is absent.
func TestSidebarService_GetSidebar_WithoutTheOptionalStoreReportsNothingMuted(t *testing.T) {
	channels := &sidebarFakeChannelStore{accesses: []storage.VisibleChannelAccess{
		{Channel: domain.Channel{ID: "channel-1", Status: domain.ChannelStatusActive}},
	}}
	svc := newSidebarService(
		&sidebarFakeWorkspaceStore{workspace: activeWorkspace()},
		&sidebarFakeMemberStore{member: activeMember()},
		channels,
		&sidebarFakeDMStore{},
	)

	data, err := svc.GetSidebar(context.Background(), sidebarUserID)
	if err != nil {
		t.Fatalf("GetSidebar: %v", err)
	}
	if len(data.Channels) != 1 || data.Channels[0].Muted {
		t.Fatalf("channels = %+v, want one row and nothing muted", data.Channels)
	}
}

// With the store wired in, what it reports is what the row shows.
func TestSidebarService_GetSidebar_MarksTheMutedRows(t *testing.T) {
	channels := &sidebarFakeChannelStore{accesses: []storage.VisibleChannelAccess{
		{Channel: domain.Channel{ID: "channel-1", Status: domain.ChannelStatusActive}},
		{Channel: domain.Channel{ID: "channel-2", Status: domain.ChannelStatusActive}},
	}}
	notifs := &fakeNotificationPrefStore{listed: []storage.MutedConversation{
		{TargetType: storage.NotificationPrefTargetChannel, TargetID: "channel-2"},
	}}
	svc := newSidebarService(
		&sidebarFakeWorkspaceStore{workspace: activeWorkspace()},
		&sidebarFakeMemberStore{member: activeMember()},
		channels,
		&sidebarFakeDMStore{},
	).WithNotificationPrefs(notifs)

	data, err := svc.GetSidebar(context.Background(), sidebarUserID)
	if err != nil {
		t.Fatalf("GetSidebar: %v", err)
	}
	if data.Channels[0].Muted || !data.Channels[1].Muted {
		t.Fatalf("muted flags = %v/%v, want only the second row muted", data.Channels[0].Muted, data.Channels[1].Muted)
	}
}

// A failure reading the preferences fails the sidebar rather than serving a
// silently wrong one: a row drawn as unmuted when it is muted would send a
// notification the person asked not to receive.
func TestSidebarService_GetSidebar_PropagatesAMutedListingFailure(t *testing.T) {
	notifs := &fakeNotificationPrefStore{listErr: errors.New("db down")}
	svc := newSidebarService(
		&sidebarFakeWorkspaceStore{workspace: activeWorkspace()},
		&sidebarFakeMemberStore{member: activeMember()},
		&sidebarFakeChannelStore{},
		&sidebarFakeDMStore{},
	).WithNotificationPrefs(notifs)

	if _, err := svc.GetSidebar(context.Background(), sidebarUserID); err == nil {
		t.Fatal("expected the listing failure to fail the sidebar")
	}
}
