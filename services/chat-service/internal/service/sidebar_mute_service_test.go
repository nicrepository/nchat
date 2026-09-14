package service_test

import (
	"context"
	"errors"
	"testing"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
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
	levels   []LevelCall
	muteErr  error
	listed   []storage.ConversationNotificationPref
	listErr  error
	unmutErr error
	levelErr error
}

// MuteCall is what the store was asked to change, recorded in order.
type MuteCall struct {
	WorkspaceID string
	UserID      string
	TargetType  string
	TargetID    string
}

// LevelCall is the same for the level write (issue #136), with the level it was
// asked for — the field the whole non-destructive contract turns on.
type LevelCall struct {
	WorkspaceID string
	UserID      string
	TargetType  string
	TargetID    string
	Level       string
}

func (f *fakeNotificationPrefStore) Mute(_ context.Context, workspaceID, userID, targetType, targetID string) error {
	f.muted = append(f.muted, MuteCall{workspaceID, userID, targetType, targetID})
	return f.muteErr
}

func (f *fakeNotificationPrefStore) Unmute(_ context.Context, userID, targetType, targetID string) error {
	f.unmuted = append(f.unmuted, MuteCall{UserID: userID, TargetType: targetType, TargetID: targetID})
	return f.unmutErr
}

func (f *fakeNotificationPrefStore) SetLevel(
	_ context.Context, workspaceID, userID, targetType, targetID, level string,
) error {
	f.levels = append(f.levels, LevelCall{workspaceID, userID, targetType, targetID, level})
	return f.levelErr
}

// PreferencesForUsers is the realtime fan-out's read (issues #744/#136) and no
// sidebar path calls it; the fake satisfies the interface without pretending to
// model it.
func (f *fakeNotificationPrefStore) PreferencesForUsers(
	_ context.Context, _, _, _ string, _ []string,
) ([]storage.UserConversationNotificationPref, error) {
	return nil, nil
}

func (f *fakeNotificationPrefStore) ListPreferences(
	_ context.Context, _, _ string,
) ([]storage.ConversationNotificationPref, error) {
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
	notifs := &fakeNotificationPrefStore{listed: []storage.ConversationNotificationPref{
		{TargetType: storage.NotificationPrefTargetChannel, TargetID: "channel-2", Muted: true},
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

// The canonical preference write (issue #136): one place translates the public
// modes into stored columns, and which store operation each mode reaches is the
// whole of the non-destructive contract.
func TestSidebarService_SetConversationNotificationPreference_TranslatesEveryMode(t *testing.T) {
	for _, test := range []struct {
		mode      string
		wantMute  bool
		wantLevel string
	}{
		// Silencing reaches Mute, which never writes a level — that omission is
		// what preserves the one already stored.
		{mode: service.NotificationModeMuted, wantMute: true},
		{mode: service.NotificationModeMentionsReplies, wantLevel: storage.NotificationLevelMentionsReplies},
		// The default is a level too, and SetLevel is what clears any mute along
		// with it: choosing what to hear is choosing to hear something.
		{mode: service.NotificationModeAll, wantLevel: storage.NotificationLevelAll},
	} {
		t.Run(test.mode, func(t *testing.T) {
			notifs := &fakeNotificationPrefStore{}
			// The rollout gate open, because this case is about the translation
			// of every mode. Its closed behaviour is its own suite below.
			svc := newSidebarService(
				&sidebarFakeWorkspaceStore{workspace: activeWorkspace()},
				&sidebarFakeMemberStore{member: activeMember()},
				&sidebarFakeChannelStore{},
				&sidebarFakeDMStore{},
			).WithNotificationPrefs(notifs).WithConversationNotificationLevels(true)

			if err := svc.SetConversationNotificationPreference(context.Background(),
				sidebarUserID, storage.NotificationPrefTargetChannel, "channel-1", test.mode); err != nil {
				t.Fatalf("SetConversationNotificationPreference: %v", err)
			}

			if test.wantMute {
				if len(notifs.muted) != 1 || len(notifs.levels) != 0 {
					t.Fatalf("mutes = %v, levels = %v, want exactly one mute and no level write",
						notifs.muted, notifs.levels)
				}
				// The workspace is the one resolved server-side, never a value
				// the caller supplied.
				if notifs.muted[0].WorkspaceID != activeWorkspace().ID ||
					notifs.muted[0].UserID != sidebarUserID {
					t.Fatalf("mute = %+v, want the resolved workspace and the session's user", notifs.muted[0])
				}
				return
			}
			if len(notifs.levels) != 1 || len(notifs.muted) != 0 {
				t.Fatalf("levels = %v, mutes = %v, want exactly one level write and no mute",
					notifs.levels, notifs.muted)
			}
			got := notifs.levels[0]
			if got.Level != test.wantLevel {
				t.Fatalf("level = %q, want %q", got.Level, test.wantLevel)
			}
			if got.WorkspaceID != activeWorkspace().ID || got.UserID != sidebarUserID {
				t.Fatalf("level write = %+v, want the resolved workspace and the session's user", got)
			}
			if got.TargetType != storage.NotificationPrefTargetChannel || got.TargetID != "channel-1" {
				t.Fatalf("level write = %+v, want the target from the path", got)
			}
		})
	}
}

// A mode outside the closed set never reaches the store, and never reaches the
// workspace lookup either: an invalid request is refused on its own terms.
func TestSidebarService_SetConversationNotificationPreference_RefusesAnUnknownMode(t *testing.T) {
	for _, mode := range []string{"", "mentions", "MUTED", "silenced"} {
		notifs := &fakeNotificationPrefStore{}
		svc := newSidebarService(
			&sidebarFakeWorkspaceStore{workspace: activeWorkspace()},
			&sidebarFakeMemberStore{member: activeMember()},
			&sidebarFakeChannelStore{},
			&sidebarFakeDMStore{},
		).WithNotificationPrefs(notifs)

		err := svc.SetConversationNotificationPreference(context.Background(),
			sidebarUserID, storage.NotificationPrefTargetChannel, "channel-1", mode)
		if !errors.Is(err, domain.ErrInvalidInput) {
			t.Fatalf("mode %q: error = %v, want ErrInvalidInput", mode, err)
		}
		if len(notifs.muted) != 0 || len(notifs.levels) != 0 {
			t.Fatalf("mode %q reached the store: %v / %v", mode, notifs.muted, notifs.levels)
		}
	}
}

// Standing is checked before anything is written, on this route as on the mute
// shortcut: a caller with no active membership configures nothing.
func TestSidebarService_SetConversationNotificationPreference_RequiresAnActiveMembership(t *testing.T) {
	notifs := &fakeNotificationPrefStore{}
	svc := newSidebarService(
		&sidebarFakeWorkspaceStore{workspace: activeWorkspace()},
		&sidebarFakeMemberStore{err: domain.ErrNotFound},
		&sidebarFakeChannelStore{},
		&sidebarFakeDMStore{},
	).WithNotificationPrefs(notifs)

	err := svc.SetConversationNotificationPreference(context.Background(),
		sidebarUserID, storage.NotificationPrefTargetChannel, "channel-1", service.NotificationModeMuted)
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("error = %v, want ErrForbidden", err)
	}
	if len(notifs.muted) != 0 || len(notifs.levels) != 0 {
		t.Fatalf("a caller without standing reached the store: %v / %v", notifs.muted, notifs.levels)
	}
}

// Both halves of the stored preference reach the sidebar rows, and a
// conversation nobody expressed anything about reads as the default rather than
// as an empty level the client would have to interpret (issue #136).
func TestSidebarService_GetSidebar_ProjectsTheNotificationLevel(t *testing.T) {
	channels := &sidebarFakeChannelStore{accesses: []storage.VisibleChannelAccess{
		{Channel: domain.Channel{ID: "channel-1", Status: domain.ChannelStatusActive}},
		{Channel: domain.Channel{ID: "channel-2", Status: domain.ChannelStatusActive}},
		{Channel: domain.Channel{ID: "channel-3", Status: domain.ChannelStatusActive}},
	}}
	notifs := &fakeNotificationPrefStore{listed: []storage.ConversationNotificationPref{
		// Narrowed and not silenced: the row exists and must not read as a mute.
		{TargetType: storage.NotificationPrefTargetChannel, TargetID: "channel-2",
			Level: storage.NotificationLevelMentionsReplies},
		// Silenced with a level underneath it: both travel, so the client can
		// show "silenced" and still restore the level when it is lifted.
		{TargetType: storage.NotificationPrefTargetChannel, TargetID: "channel-3",
			Level: storage.NotificationLevelMentionsReplies, Muted: true},
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
	if got := data.Channels[0]; got.Muted || got.NotificationLevel != storage.NotificationLevelAll {
		t.Fatalf("the unconfigured row = %+v, want the default level and no mute", got)
	}
	if got := data.Channels[1]; got.Muted || got.NotificationLevel != storage.NotificationLevelMentionsReplies {
		t.Fatalf("the narrowed row = %+v, want mentions_replies and no mute", got)
	}
	if got := data.Channels[2]; !got.Muted || got.NotificationLevel != storage.NotificationLevelMentionsReplies {
		t.Fatalf("the silenced row = %+v, want the mute with the level intact", got)
	}
}

// A level this build does not recognise must not reach a client as a level: the
// row is normalised to the default, which is the state that grants nothing.
func TestSidebarService_GetSidebar_NormalisesAnUnknownLevel(t *testing.T) {
	channels := &sidebarFakeChannelStore{accesses: []storage.VisibleChannelAccess{
		{Channel: domain.Channel{ID: "channel-1", Status: domain.ChannelStatusActive}},
	}}
	notifs := &fakeNotificationPrefStore{listed: []storage.ConversationNotificationPref{
		{TargetType: storage.NotificationPrefTargetChannel, TargetID: "channel-1",
			Level: "somente_mencoes"},
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
	if got := data.Channels[0].NotificationLevel; got != storage.NotificationLevelAll {
		t.Fatalf("level = %q, want the default", got)
	}
}

// One statement for the whole sidebar: the level arrives in the listing the
// mute already used, so no conversation costs a query of its own.
func TestSidebarService_GetSidebar_ReadsPreferencesOnce(t *testing.T) {
	channels := &sidebarFakeChannelStore{accesses: []storage.VisibleChannelAccess{
		{Channel: domain.Channel{ID: "channel-1", Status: domain.ChannelStatusActive}},
		{Channel: domain.Channel{ID: "channel-2", Status: domain.ChannelStatusActive}},
		{Channel: domain.Channel{ID: "channel-3", Status: domain.ChannelStatusActive}},
	}}
	notifs := &countingNotificationPrefStore{}
	svc := newSidebarService(
		&sidebarFakeWorkspaceStore{workspace: activeWorkspace()},
		&sidebarFakeMemberStore{member: activeMember()},
		channels,
		&sidebarFakeDMStore{},
	).WithNotificationPrefs(notifs)

	if _, err := svc.GetSidebar(context.Background(), sidebarUserID); err != nil {
		t.Fatalf("GetSidebar: %v", err)
	}
	if notifs.listCalls != 1 {
		t.Fatalf("the preference listing ran %d times, want exactly once", notifs.listCalls)
	}
}

// countingNotificationPrefStore counts the listing calls and nothing else.
type countingNotificationPrefStore struct {
	fakeNotificationPrefStore
	listCalls int
}

func (f *countingNotificationPrefStore) ListPreferences(
	_ context.Context, _, _ string,
) ([]storage.ConversationNotificationPref, error) {
	f.listCalls++
	return nil, nil
}

// The issue #136 rollout gate, at the only layer that writes.
//
// The gate exists because production runs two release slots against one
// database and a slot from before #136 reads any preference row as a mute. What
// these prove is the shape of that protection: which modes are refused, which
// keep working, and — the part a rollback depends on — that a row already
// written keeps its meaning while the gate is shut.

func gatedSidebarService(notifs *fakeNotificationPrefStore, enabled bool) *service.SidebarService {
	return newSidebarService(
		&sidebarFakeWorkspaceStore{workspace: activeWorkspace()},
		&sidebarFakeMemberStore{member: activeMember()},
		&sidebarFakeChannelStore{},
		&sidebarFakeDMStore{},
	).WithNotificationPrefs(notifs).WithConversationNotificationLevels(enabled)
}

// The one mode that can persist a row a pre-#136 reader would misread is the
// one that is refused, and it is refused before anything is written.
func TestSidebarService_SetConversationNotificationPreference_GateRefusesTheGranularMode(t *testing.T) {
	notifs := &fakeNotificationPrefStore{}
	svc := gatedSidebarService(notifs, false)

	err := svc.SetConversationNotificationPreference(context.Background(),
		sidebarUserID, storage.NotificationPrefTargetChannel, "channel-1",
		service.NotificationModeMentionsReplies)

	if !errors.Is(err, domain.ErrConversationNotificationLevelsDisabled) {
		t.Fatalf("error = %v, want ErrConversationNotificationLevelsDisabled", err)
	}
	if len(notifs.levels) != 0 || len(notifs.muted) != 0 {
		t.Fatalf("the store was reached: levels=%v mutes=%v", notifs.levels, notifs.muted)
	}
}

// The two modes the old model can represent keep working while the gate is
// shut: the default is the absence of a row and a mute is a row, so neither can
// produce anything a pre-#136 slot would read wrongly. Phase one's binary UX is
// exactly these two.
func TestSidebarService_SetConversationNotificationPreference_GateAllowsTheCompatibleModes(t *testing.T) {
	for _, test := range []struct {
		mode      string
		wantMute  bool
		wantLevel string
	}{
		{mode: service.NotificationModeMuted, wantMute: true},
		{mode: service.NotificationModeAll, wantLevel: storage.NotificationLevelAll},
	} {
		t.Run(test.mode, func(t *testing.T) {
			notifs := &fakeNotificationPrefStore{}
			svc := gatedSidebarService(notifs, false)

			if err := svc.SetConversationNotificationPreference(context.Background(),
				sidebarUserID, storage.NotificationPrefTargetChannel, "channel-1", test.mode); err != nil {
				t.Fatalf("SetConversationNotificationPreference: %v", err)
			}
			if test.wantMute {
				if len(notifs.muted) != 1 {
					t.Fatalf("mutes = %v, want the mute to have gone through", notifs.muted)
				}
				return
			}
			if len(notifs.levels) != 1 || notifs.levels[0].Level != test.wantLevel {
				t.Fatalf("levels = %v, want one write of %q", notifs.levels, test.wantLevel)
			}
		})
	}
}

// The sidebar's own shortcut is untouched by the gate: muting and unmuting are
// the capability this product has always had.
func TestSidebarService_MuteAndUnmute_AreNotGated(t *testing.T) {
	notifs := &fakeNotificationPrefStore{}
	svc := gatedSidebarService(notifs, false)
	ctx := context.Background()

	if err := svc.MuteConversation(ctx, sidebarUserID,
		storage.NotificationPrefTargetChannel, "channel-1"); err != nil {
		t.Fatalf("MuteConversation: %v", err)
	}
	if err := svc.UnmuteConversation(ctx, sidebarUserID,
		storage.NotificationPrefTargetChannel, "channel-1"); err != nil {
		t.Fatalf("UnmuteConversation: %v", err)
	}
	if len(notifs.muted) != 1 || len(notifs.unmuted) != 1 {
		t.Fatalf("mutes = %v, unmutes = %v, want one of each", notifs.muted, notifs.unmuted)
	}
}

// Reading is never gated, and this is the property a rollback from phase two to
// phase one rests on: a granular row that was written while the gate was open
// keeps its meaning after it closes. The gate stops new granular writes; it must
// never reinterpret an existing row as a mute.
func TestSidebarService_GetSidebar_ReadsGranularRowsWithTheGateShut(t *testing.T) {
	channels := &sidebarFakeChannelStore{accesses: []storage.VisibleChannelAccess{
		{Channel: domain.Channel{ID: "channel-1", Status: domain.ChannelStatusActive}},
		{Channel: domain.Channel{ID: "channel-2", Status: domain.ChannelStatusActive}},
	}}
	notifs := &fakeNotificationPrefStore{listed: []storage.ConversationNotificationPref{
		// Written by a build that had the gate open, and read back by this one.
		{TargetType: storage.NotificationPrefTargetChannel, TargetID: "channel-1",
			Level: storage.NotificationLevelMentionsReplies},
		{TargetType: storage.NotificationPrefTargetChannel, TargetID: "channel-2",
			Level: storage.NotificationLevelMentionsReplies, Muted: true},
	}}
	svc := newSidebarService(
		&sidebarFakeWorkspaceStore{workspace: activeWorkspace()},
		&sidebarFakeMemberStore{member: activeMember()},
		channels,
		&sidebarFakeDMStore{},
	).WithNotificationPrefs(notifs).WithConversationNotificationLevels(false)

	data, err := svc.GetSidebar(context.Background(), sidebarUserID)
	if err != nil {
		t.Fatalf("GetSidebar: %v", err)
	}
	// The unsilenced granular row is still unsilenced. Reading it as a mute is
	// the exact regression the gate exists to avoid in the *other* direction.
	if got := data.Channels[0]; got.Muted ||
		got.NotificationLevel != storage.NotificationLevelMentionsReplies {
		t.Fatalf("channel-1 = %+v, want mentions_replies and not muted", got)
	}
	if got := data.Channels[1]; !got.Muted ||
		got.NotificationLevel != storage.NotificationLevelMentionsReplies {
		t.Fatalf("channel-2 = %+v, want the mute with the level intact", got)
	}
}

// The capability the payload publishes is the same answer the write path
// enforces, from one source — so a client cannot be offered something the
// server would refuse.
func TestSidebarService_ConversationNotificationLevelsEnabled_ReportsTheGate(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		svc := gatedSidebarService(&fakeNotificationPrefStore{}, enabled)
		if svc.ConversationNotificationLevelsEnabled() != enabled {
			t.Fatalf("capability = %v, want %v", !enabled, enabled)
		}
		err := svc.SetConversationNotificationPreference(context.Background(),
			sidebarUserID, storage.NotificationPrefTargetChannel, "channel-1",
			service.NotificationModeMentionsReplies)
		refused := errors.Is(err, domain.ErrConversationNotificationLevelsDisabled)
		if refused == enabled {
			t.Fatalf("capability says %v but the write path %s", enabled,
				map[bool]string{true: "refused", false: "allowed"}[refused])
		}
	}
}

// A service assembled without the option has not been told the gate is open,
// and the default is the safe one.
func TestSidebarService_ConversationNotificationLevels_DefaultIsOff(t *testing.T) {
	svc := newSidebarService(
		&sidebarFakeWorkspaceStore{workspace: activeWorkspace()},
		&sidebarFakeMemberStore{member: activeMember()},
		&sidebarFakeChannelStore{},
		&sidebarFakeDMStore{},
	).WithNotificationPrefs(&fakeNotificationPrefStore{})

	if svc.ConversationNotificationLevelsEnabled() {
		t.Fatal("a service that was never told about the gate reported it open")
	}
}

// The blue/green property, restated after Unmute became one statement.
//
// Phase 1 is safe only if no sequence of calls this build can make produces a
// row a release slot from before issue #136 would misread. That slot reads any
// row as a mute, so the rows it reads *correctly* are exactly the ones carrying
// a muted_at — and the two rows that would fool it are `all` + NULL and
// `mentions_replies` + NULL.
//
// This drives every public mode and both shortcut operations, in every order,
// against a fake store that records what each one asked for. What it asserts is
// not the sequence but the reachable set: with the gate shut, nothing asks the
// store to write a level at all.
func TestSidebarService_GateShut_NoCallPathWritesAGranularLevel(t *testing.T) {
	operations := []struct {
		name string
		run  func(*service.SidebarService) error
	}{
		{name: "mode all", run: func(s *service.SidebarService) error {
			return s.SetConversationNotificationPreference(context.Background(),
				sidebarUserID, storage.NotificationPrefTargetChannel, "channel-1",
				service.NotificationModeAll)
		}},
		{name: "mode mentions_replies", run: func(s *service.SidebarService) error {
			return s.SetConversationNotificationPreference(context.Background(),
				sidebarUserID, storage.NotificationPrefTargetChannel, "channel-1",
				service.NotificationModeMentionsReplies)
		}},
		{name: "mode muted", run: func(s *service.SidebarService) error {
			return s.SetConversationNotificationPreference(context.Background(),
				sidebarUserID, storage.NotificationPrefTargetChannel, "channel-1",
				service.NotificationModeMuted)
		}},
		{name: "mute", run: func(s *service.SidebarService) error {
			return s.MuteConversation(context.Background(),
				sidebarUserID, storage.NotificationPrefTargetChannel, "channel-1")
		}},
		{name: "unmute", run: func(s *service.SidebarService) error {
			return s.UnmuteConversation(context.Background(),
				sidebarUserID, storage.NotificationPrefTargetChannel, "channel-1")
		}},
	}

	// Every ordered pair, plus each operation on its own: a two-step sequence is
	// enough, because no operation's effect depends on more than the row it
	// finds.
	for _, first := range operations {
		for _, second := range operations {
			t.Run(first.name+" then "+second.name, func(t *testing.T) {
				notifs := &fakeNotificationPrefStore{}
				svc := gatedSidebarService(notifs, false)
				for _, operation := range []func(*service.SidebarService) error{first.run, second.run} {
					err := operation(svc)
					// The only refusal this may produce is the gate's; anything
					// else would be a fault in the fixture.
					if err != nil && !errors.Is(err, domain.ErrConversationNotificationLevelsDisabled) {
						t.Fatalf("unexpected error: %v", err)
					}
				}
				// The reachable set. A level write is the only call that can put
				// a non-default level in the table, and with the gate shut the
				// only level it may carry is the default — which SetLevel
				// expresses by *removing* the row.
				for _, level := range notifs.levels {
					if level.Level != storage.NotificationLevelAll {
						t.Fatalf("the store was asked to write %q with the gate shut", level.Level)
					}
				}
			})
		}
	}
}

// ...and with the gate open, the granular level is exactly what does reach the
// store — so the property above is the gate's doing and not an accident of the
// call paths.
func TestSidebarService_GateOpen_TheGranularLevelReachesTheStore(t *testing.T) {
	notifs := &fakeNotificationPrefStore{}
	svc := gatedSidebarService(notifs, true)

	if err := svc.SetConversationNotificationPreference(context.Background(),
		sidebarUserID, storage.NotificationPrefTargetChannel, "channel-1",
		service.NotificationModeMentionsReplies); err != nil {
		t.Fatalf("SetConversationNotificationPreference: %v", err)
	}
	if len(notifs.levels) != 1 ||
		notifs.levels[0].Level != storage.NotificationLevelMentionsReplies {
		t.Fatalf("levels = %v, want one write of the granular level", notifs.levels)
	}
}
